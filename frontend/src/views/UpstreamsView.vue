<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { NButton, NDropdown, NIcon, NSpace, NSwitch, NTag, NTooltip, useDialog, useMessage } from 'naive-ui'
import { AddOutline, CreateOutline, EllipsisHorizontalOutline, RefreshOutline } from '@vicons/ionicons5'
import type { DataTableColumns, DropdownOption, FormInst, FormRules } from 'naive-ui'
import {
  actionMessage,
  allPages,
  createKey,
  createUpstream,
  deleteKey,
  deleteUpstream,
  fetchKeyModels,
  listKeys,
  listKeyRates,
  listRouteGroups,
  listUpstreams,
  probeKey,
  refreshAllBalances,
  refreshUpstreamBalance,
  refreshKeyBilling,
  runProbes,
  updateKey,
  updateUpstream,
  type KeyRate,
} from '@/api/admin'
import {
  BILLING_KINDS,
  HEALTH_LABEL,
  KIND_LABEL,
  KIND_OPTIONS,
  PROTOCOL_LABEL,
  PROTOCOL_OPTIONS,
  STATUS_OPTIONS,
  type EnableStatus,
  type HealthStatus,
  type PlatformKey,
  type PlatformKeyPayload,
  type Protocol,
  type RouteGroup,
  type RouteGroupRef,
  type Upstream,
  type UpstreamKind,
  type UpstreamPayload,
} from '@/api/types'
import HealthPulse from '@/components/HealthPulse.vue'
import HealthTag from '@/components/HealthTag.vue'
import ModelListModal from '@/components/ModelListModal.vue'
import RouteGroupTags from '@/components/RouteGroupTags.vue'
import StatusTag from '@/components/StatusTag.vue'
import { composeKeyName, errText, formatMoney, formatPercent, formatRate, formatTime, inferNameTag } from '@/utils/format'

const message = useMessage()
const dialog = useDialog()

const loading = ref(false)
const error = ref('')
const items = ref<Upstream[]>([])
const keys = ref<PlatformKey[]>([])
const routeGroups = ref<RouteGroup[]>([])

async function loadRouteGroups() {
  try {
    routeGroups.value = await listRouteGroups()
  } catch {
    routeGroups.value = []
  }
}
const probing = ref(false)
const refreshingBal = ref(false)
const deep = ref(true)
const busy = ref<string | null>(null)
const live = ref(true)
const lastRefresh = ref('')
let timer: number | undefined

const LIVE_MS = 15_000
const LOW_BALANCE = 50

const showForm = ref(false)
const saving = ref(false)
const editing = ref<Upstream | null>(null)
const formRef = ref<FormInst | null>(null)
const form = reactive<UpstreamPayload>({
  name: '',
  base_url: '',
  kind: 'openai_compat',
  protocols: ['openai'],
  status: 'enabled',
  note: '',
  concurrency: 0,
})

const rules: FormRules = {
  name: { required: true, message: '请输入名称', trigger: 'blur' },
  base_url: { required: true, message: '请输入 Base URL', trigger: 'blur' },
  kind: { required: true, message: '请选择类型', trigger: 'change' },
  protocols: { type: 'array', required: true, min: 1, message: '至少选择一种协议', trigger: 'change' },
}

const page = ref(1)
const pageSize = ref(10)
const orderedIds = ref<number[]>([])
const filters = reactive({ query: '', kind: null as UpstreamKind | null, protocol: null as Protocol | null, status: null as EnableStatus | null })
const quick = ref('all')
const narrow = ref(window.innerWidth < 760)
let loadSequence = 0
let pending = false
let disposed = false
let initialized = false
const rateSnapshot = new Map<number, number>()
const syncingRateIds = new Set<number>()
let rateRevision = 0

function isLowBalance(value?: number | null) {
  return typeof value === 'number' && value < LOW_BALANCE
}

function abnormal(up: Upstream) {
  return up.status === 'enabled' && (up.summary?.abnormal_count ?? 0) > 0
}

function priority(up: Upstream) {
  if (up.status === 'disabled') return 3
  if (abnormal(up)) return 0
  return isLowBalance(up.last_balance) ? 1 : 2
}

function matches(up: Upstream) {
  const query = filters.query.trim().toLowerCase()
  return (!query || [up.name, up.base_url, up.note].some((v) => v?.toLowerCase().includes(query)))
    && (!filters.kind || up.kind === filters.kind)
    && (!filters.protocol || up.protocols.includes(filters.protocol))
    && (!filters.status || up.status === filters.status)
    && (quick.value !== 'abnormal' || abnormal(up))
    && (quick.value !== 'low' || (up.status === 'enabled' && isLowBalance(up.last_balance)))
    && (quick.value !== 'disabled' || up.status === 'disabled')
}

const quickOptions = computed(() => [
  { label: `全部 ${items.value.length}`, value: 'all' },
  { label: `异常 ${items.value.filter(abnormal).length}`, value: 'abnormal' },
  { label: `低余额 ${items.value.filter((u) => u.status === 'enabled' && isLowBalance(u.last_balance)).length}`, value: 'low' },
  { label: `停用 ${items.value.filter((u) => u.status === 'disabled').length}`, value: 'disabled' },
])
const providerMap = computed(() => new Map(items.value.map((up) => [up.id, up])))
const visibleIds = computed(() => orderedIds.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
type ProviderRow = { id: string; upstream: Upstream; key: PlatformKey | null; span: number; first: boolean }
const rows = computed<ProviderRow[]>(() => {
  const grouped = new Map<number, PlatformKey[]>()
  for (const key of keys.value) {
    const group = grouped.get(key.upstream_id) ?? []
    group.push(key)
    grouped.set(key.upstream_id, group)
  }
  return visibleIds.value.flatMap<ProviderRow>((id) => {
    const upstream = providerMap.value.get(id)
    if (!upstream) return []
    const group = grouped.get(id) ?? []
    if (!group.length) return [{ id: `up-${id}`, upstream, key: null, span: 1, first: true }]
    return group.map((key, i) => ({ id: `key-${key.id}`, upstream, key, span: i === 0 ? group.length : 0, first: i === 0 }))
  })
})

function notifyRateChanges(nextKeys: KeyRate[]) {
  const changes: Array<{ key: KeyRate; previous: number; current: number }> = []
  for (const key of nextKeys) {
    if (syncingRateIds.has(key.id)) continue
    const previous = rateSnapshot.get(key.id)
    const current = key.rate_multiplier
    if (!Number.isFinite(current)) continue
    if (previous != null && Math.abs(previous - current) > 1e-9) {
      changes.push({ key, previous, current })
    }
    rateSnapshot.set(key.id, current)
  }
  if (!changes.length) return false
  const shown = changes.slice(0, 3)
  for (const change of shown) {
    const direction = change.current < change.previous ? '降价' : '涨价'
    const detail = `${change.key.upstream_name || '提供商'} / ${change.key.name}: ×${formatAlertRate(change.previous)} → ×${formatAlertRate(change.current)}`
    const content = () => h('div', { style: 'max-width: min(560px, calc(100vw - 100px)); overflow-wrap: anywhere' }, `倍率${direction}：${detail}`)
    if (direction === '降价') message.success(content, { duration: 7000, closable: true })
    else message.warning(content, { duration: 9000, closable: true })
  }
  if (changes.length > shown.length) {
    message.info(`另有 ${changes.length - shown.length} 把 Key 的倍率发生变化`, { duration: 7000 })
  }
  return true
}

function formatAlertRate(value: number) {
  return value.toLocaleString('en-US', { maximumFractionDigits: 9, useGrouping: false })
}

async function load(opts?: { silent?: boolean; preserveOrder?: boolean }) {
  if (disposed) return
  const silent = !!opts?.silent
  if (silent && pending) return
  const sequence = ++loadSequence
  pending = true
  if (!silent) loading.value = true
  try {
    const upstreams = await allPages((params) => listUpstreams({ ...params, include_summary: true }))
    if (sequence !== loadSequence) return
    const knownIds = new Set(upstreams.map((up) => up.id))
    const order = initialized && (silent || opts?.preserveOrder)
      ? orderedIds.value.filter((id) => knownIds.has(id))
      : upstreams.filter(matches).sort((a, b) => priority(a) - priority(b) || a.id - b.id).map((up) => up.id)
    const nextPage = Math.min(page.value, Math.max(1, Math.ceil(order.length / pageSize.value)))
    const ids = order.slice((nextPage - 1) * pageSize.value, nextPage * pageSize.value)
    const revision = rateRevision
    const [allRates, pageKeys] = await Promise.all([
      allPages((params) => listKeyRates(params)),
      ids.length ? allPages((params) => listKeys({ ...params, upstream_ids: ids })) : Promise.resolve([]),
    ])
    if (sequence !== loadSequence) return
    // A manual sync supersedes any rate snapshot already in flight.
    if (revision === rateRevision) notifyRateChanges(allRates)
    items.value = upstreams
    orderedIds.value = order
    page.value = nextPage
    keys.value = pageKeys
    initialized = true
    error.value = ''
    lastRefresh.value = formatTime(new Date().toISOString())
  } catch (e) {
    if (sequence === loadSequence) error.value = errText(e)
  } finally {
    if (sequence === loadSequence) {
      pending = false
      loading.value = false
    }
  }
}

function search() {
  page.value = 1
  void load()
}

function resetFilters() {
  Object.assign(filters, { query: '', kind: null, protocol: null, status: null })
  quick.value = 'all'
  search()
}

function changePage(next: number) {
  page.value = next
  keys.value = []
  void load({ preserveOrder: true })
}

function resize() { narrow.value = window.innerWidth < 760 }

function startLive() {
  if (disposed) return
  stopLive()
  timer = window.setInterval(() => void load({ silent: true }), LIVE_MS)
}

function stopLive() {
  if (timer != null) {
    window.clearInterval(timer)
    timer = undefined
  }
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '',
    base_url: '',
    kind: 'openai_compat' as UpstreamKind,
    protocols: ['openai'] as Protocol[],
    status: 'enabled' as EnableStatus,
    note: '',
    concurrency: 0,
  })
  showForm.value = true
}

function openEdit(row: Upstream) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    base_url: row.base_url,
    kind: row.kind,
    protocols: [...(row.protocols || [])],
    status: row.status,
    note: row.note || '',
    concurrency: row.concurrency ?? 0,
  })
  showForm.value = true
}

async function save() {
  await formRef.value?.validate()
  saving.value = true
  try {
    const payload: UpstreamPayload = {
      name: form.name.trim(),
      base_url: form.base_url.trim(),
      kind: form.kind,
      protocols: form.protocols,
      status: form.status,
      note: form.note?.trim() || undefined,
      concurrency: Number(form.concurrency) || 0,
    }
    if (editing.value) await updateUpstream(editing.value.id, payload)
    else await createUpstream(payload)
    message.success('已保存')
    showForm.value = false
    await load()
  } catch (e) {
    message.error(errText(e, '保存失败'))
  } finally {
    saving.value = false
  }
}

function confirmDelete(row: Upstream) {
  dialog.warning({
    title: '删除提供商',
    content: `确认删除「${row.name}」？其下的 Key 会一并删除。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await deleteUpstream(row.id)
        message.success('已删除')
        await load()
      } catch (e) {
        message.error(errText(e, '删除失败'))
      }
    },
  })
}

async function refreshAll() {
  refreshingBal.value = true
  try {
    const data = await refreshAllBalances()
    message.success(actionMessage(data, '已刷新余额'))
    await load()
  } catch (e) {
    message.error(errText(e, '刷新余额失败'))
  } finally {
    refreshingBal.value = false
  }
}

async function probeAll() {
  probing.value = true
  try {
    const data = await runProbes({ deep: deep.value })
    message.success(actionMessage(data, '探测任务已提交'))
    await load()
  } catch (e) {
    message.error(errText(e, '探测失败'))
  } finally {
    probing.value = false
  }
}

async function refreshUpstream(up: Upstream) {
  busy.value = `bal-up-${up.id}`
  try {
    const data = await refreshUpstreamBalance(up.id)
    message.success(actionMessage(data, `已刷新 ${up.name} 余额`))
    await load()
  } catch (e) {
    message.error(errText(e, '刷新余额失败'))
  } finally {
    busy.value = null
  }
}

async function probeOne(row: PlatformKey) {
  busy.value = `probe-${row.id}`
  try {
    const data = await probeKey(row.id, deep.value)
    message.success(actionMessage(data, '探测完成'))
    await load()
  } catch (e) {
    message.error(errText(e, '探测失败'))
  } finally {
    busy.value = null
  }
}

async function syncRateOne(row: PlatformKey) {
  if (syncingRateIds.has(row.id)) return
  syncingRateIds.add(row.id)
  rateRevision++
  busy.value = `rate-${row.id}`
  try {
    const data = await refreshKeyBilling(row.id)
    if (disposed) return
    syncingRateIds.delete(row.id)
    rateRevision++
    if (data.billing_unsupported) {
      message.warning('该 Key 暂不支持同步倍率')
    } else if (!notifyRateChanges([data])) {
      message.success(`已同步倍率：×${formatAlertRate(data.rate_multiplier)}`)
    }
    await load()
  } catch (e) {
    message.error(errText(e, '同步倍率失败'))
  } finally {
    syncingRateIds.delete(row.id)
    rateRevision++
    busy.value = null
  }
}

async function fetchModelsOne(row: PlatformKey) {
  busy.value = `models-${row.id}`
  try {
    const data = await fetchKeyModels(row.id)
    message.success(actionMessage(data, '已获取模型'))
    await load()
  } catch (e) {
    message.error(errText(e, '获取模型失败'))
  } finally {
    busy.value = null
  }
}

const showKeyForm = ref(false)
const keySaving = ref(false)
const editingKey = ref<PlatformKey | null>(null)
const keyHost = ref<Upstream | null>(null)
const keyFormRef = ref<FormInst | null>(null)
const keyForm = reactive({
  name_tag: '',
  api_key: '',
  rate_multiplier: 1 as number | null,
  billing_group: '',
  probe_interval_sec: null as number | null,
  rpm_limit: 0 as number | null,
  max_concurrency: 0 as number | null,
  status: 'enabled' as EnableStatus,
})
const keyRules: FormRules = {
  name_tag: { required: true, message: '请输入标识', trigger: 'blur' },
}
const keyFormIsNewAPI = computed(() => keyHost.value?.kind === 'new_api')
const keyFormCanSync = computed(() => !!keyHost.value && BILLING_KINDS.includes(keyHost.value.kind))
const keyNamePreview = computed(() =>
  composeKeyName(keyHost.value?.name || '', keyForm.name_tag, keyForm.rate_multiplier),
)

function openCreateKey(up: Upstream) {
  editingKey.value = null
  keyHost.value = up
  Object.assign(keyForm, {
    name_tag: '',
    api_key: '',
    rate_multiplier: null,
    billing_group: '',
    probe_interval_sec: null,
    rpm_limit: 0,
    max_concurrency: 0,
    status: 'enabled' as EnableStatus,
  })
  showKeyForm.value = true
}

function openEditKey(row: PlatformKey) {
  editingKey.value = row
  keyHost.value = items.value.find((u) => u.id === row.upstream_id) ?? null
  Object.assign(keyForm, {
    name_tag: row.name_tag || inferNameTag(row.name, keyHost.value?.name),
    api_key: '',
    rate_multiplier: row.rate_multiplier ?? 1,
    billing_group: row.billing_group || '',
    probe_interval_sec: row.probe_interval_sec && row.probe_interval_sec > 0 ? row.probe_interval_sec : null,
    rpm_limit: row.rpm_limit ?? 0,
    max_concurrency: row.max_concurrency ?? 0,
    status: row.status,
  })
  showKeyForm.value = true
}

async function saveKey() {
  await keyFormRef.value?.validate()
  if (!editingKey.value && !keyForm.api_key.trim()) {
    message.warning('新建时必须填写 API Key')
    return
  }
  const up = keyHost.value
  if (!up) return
  keySaving.value = true
  try {
    const tag = keyForm.name_tag.trim()
    const payload: PlatformKeyPayload = {
      name_tag: tag,
      api_key: keyForm.api_key.trim() || undefined,
      billing_group: keyFormIsNewAPI.value ? keyForm.billing_group.trim() : '',
      status: keyForm.status,
    }
    if (keyForm.rate_multiplier != null) payload.rate_multiplier = keyForm.rate_multiplier
    payload.probe_interval_sec = Number(keyForm.probe_interval_sec) || 0
    payload.rpm_limit = Number(keyForm.rpm_limit) || 0
    payload.max_concurrency = Number(keyForm.max_concurrency) || 0
    if (editingKey.value) await updateKey(editingKey.value.id, payload)
    else await createKey(up.id, payload)
    message.success('已保存')
    showKeyForm.value = false
    await load()
  } catch (e) {
    message.error(errText(e, '保存失败'))
  } finally {
    keySaving.value = false
  }
}

async function toggleKeyStatus(row: PlatformKey, enabled: boolean) {
  const next: EnableStatus = enabled ? 'enabled' : 'disabled'
  if (row.status === next) return
  busy.value = `status-${row.id}`
  try {
    await updateKey(row.id, {
      name_tag: row.name_tag || inferNameTag(row.name, row.upstream_name),
      status: next,
    })
    row.status = next
    message.success(next === 'enabled' ? '已启用' : '已停用')
  } catch (e) {
    message.error(errText(e, '更新状态失败'))
  } finally {
    busy.value = null
  }
}

function confirmDeleteKey(row: PlatformKey) {
  dialog.warning({
    title: '删除 Key',
    content: `确认删除「${row.name}」(${row.key_preview})？`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await deleteKey(row.id)
        message.success('已删除')
        await load()
      } catch (e) {
        message.error(errText(e, '删除失败'))
      }
    },
  })
}

const showModels = ref(false)
const modelsRow = ref<PlatformKey | null>(null)

function openModels(row: PlatformKey) {
  modelsRow.value = row
  showModels.value = true
}

function onModelsUpdated(next: PlatformKey) {
  modelsRow.value = next
  const idx = keys.value.findIndex((k) => k.id === next.id)
  if (idx >= 0) keys.value[idx] = next
}

const SCORE_TERM_LABEL: Record<string, string> = { success: '成功率', latency: '延迟', cache: '缓存' }

const keyColumns: DataTableColumns<PlatformKey> = [
  { title: 'Key', key: 'name', ellipsis: { tooltip: true } },
  {
    title: '预览',
    key: 'key_preview',
    width: 140,
    render(row) {
      return h('span', { class: 'preview' }, row.key_preview || '—')
    },
  },
  {
    title: '倍率',
    key: 'rate_multiplier',
    width: 110,
    render(row) {
      const synced = row.rate_synced_at ? `同步于 ${formatTime(row.rate_synced_at)}` : '手动填写'
      const extra = row.upstream_kind === 'new_api' && row.billing_group ? ` · 分组 ${row.billing_group}` : ''
      return h(
        NTooltip,
        { trigger: 'hover' },
        {
          trigger: () =>
            h(
              'span',
              { style: 'font-weight:600;font-variant-numeric:tabular-nums' },
              `×${formatRate(row.rate_multiplier)}`,
            ),
          default: () => synced + extra,
        },
      )
    },
  },
  {
    title: '路由分组',
    key: 'route_groups',
    width: 160,
    render(row) {
      return h(RouteGroupTags, {
        keyId: row.id,
        groups: row.route_groups,
        options: routeGroups.value,
        onUpdated: (groups: RouteGroupRef[]) => {
          row.route_groups = groups
          void loadRouteGroups()
        },
      })
    },
  },
  {
    title: '健康',
    key: 'health_status',
    width: 110,
    render(row) {
      return h(HealthTag, { status: row.health_status })
    },
  },
  {
    title: '渠道评分',
    key: 'channel_score',
    width: 100,
    render(row) {
      if (row.channel_score == null) return h('span', { class: 'muted' }, '—')
      const n = row.channel_score
      const type = n >= 80 ? 'success' : n >= 50 ? 'warning' : 'error'
      const meta = row.channel_score_meta
      const lines: string[] = [`近窗口综合分 ${n}`]
      if (meta) {
        const terms = meta.terms || []
        lines.push(`计入：${terms.map((t) => SCORE_TERM_LABEL[t] || t).join('、') || '成功率'}`)
        lines.push(`成功率 ${(meta.success * 100).toFixed(0)}% · 样本 ${meta.samples}`)
        if (terms.includes('latency')) lines.push(`延迟 p50 ${meta.latency_p50 ?? 0} ms`)
        else lines.push('延迟：窗口内无观测，已剔除')
        if (terms.includes('cache') && meta.cache != null) lines.push(`缓存 ${(meta.cache * 100).toFixed(0)}%`)
        else lines.push('缓存：无真实调用，已剔除')
        if (meta.low_sample) lines.push('样本不足，成功率用先验 70%')
      }
      return h(
        NTooltip,
        { trigger: 'hover' },
        {
          trigger: () =>
            h(NTag, { size: 'small', bordered: false, type }, { default: () => String(n) }),
          default: () => lines.map((l) => h('div', l)),
        },
      )
    },
  },
  {
    title: '近 60 分钟',
    key: 'health_pulse',
    width: 240,
    render(row) {
      return h(HealthPulse, { cells: row.health_pulse, lastProbeAt: formatTime(row.last_probe_at) })
    },
  },
  {
    title: '模型',
    key: 'models_count',
    width: 80,
    render(row) {
      const n = row.models_count ?? row.last_models?.length ?? 0
      return h(
        NButton,
        { size: 'tiny', quaternary: true, type: n ? 'info' : 'default', onClick: () => openModels(row) },
        { default: () => (n ? `${n} 个` : '未获取') },
      )
    },
  },
  {
    title: '缓存',
    key: 'cache_rate',
    width: 72,
    render(row) {
      return row.cache_samples ? formatPercent(row.cache_rate) : '—'
    },
  },
  {
    title: '操作',
    key: 'actions',
    width: 140,
    align: 'right',
    render(row) {
      const rowBusy =
        busy.value === `probe-${row.id}` ||
        busy.value === `rate-${row.id}` ||
        busy.value === `models-${row.id}`
      const statusBusy = busy.value === `status-${row.id}`
      const more: DropdownOption[] = [
        { label: '探测', key: 'probe', disabled: rowBusy },
        { label: '获取模型', key: 'models', disabled: rowBusy },
        { label: '删除 Key', key: 'delete', disabled: rowBusy },
      ]
      if (row.upstream_kind && BILLING_KINDS.includes(row.upstream_kind)) {
        more.splice(1, 0, { label: '同步倍率', key: 'rate', disabled: rowBusy })
      }
      return h(
        NSpace,
        { size: 4, wrap: false, justify: 'end', align: 'center' },
        {
          default: () => [
            h(
              NSwitch,
              {
                size: 'small',
                value: row.status === 'enabled',
                loading: statusBusy,
                disabled: statusBusy,
                onUpdateValue: (on: boolean) => void toggleKeyStatus(row, on),
              },
              { checked: () => '启用', unchecked: () => '停用' },
            ),
            iconButton(CreateOutline, '编辑 Key', () => openEditKey(row)),
            h(
              NDropdown,
              {
                trigger: 'click',
                placement: 'bottom-end',
                options: more,
                onSelect: (key: string) => {
                  if (key === 'probe') void probeOne(row)
                  else if (key === 'rate') void syncRateOne(row)
                  else if (key === 'models') void fetchModelsOne(row)
                  else if (key === 'delete') confirmDeleteKey(row)
                },
              },
              {
                default: () =>
                  h(NButton, { size: 'tiny', quaternary: true, loading: rowBusy, 'aria-label': 'Key 操作' }, { icon: () => h(NIcon, null, { default: () => h(EllipsisHorizontalOutline) }) }),
              },
            ),
          ],
        },
      )
    },
  },
]


function iconButton(icon: typeof RefreshOutline, label: string, action: () => void, isBusy = false) {
  return h(NTooltip, null, {
    trigger: () => h(NButton, { size: 'tiny', quaternary: true, 'aria-label': label, loading: isBusy, onClick: action },
      { icon: () => h(NIcon, null, { default: () => h(icon) }) }),
    default: () => label,
  })
}

function providerMenu(up: Upstream) {
  const options: DropdownOption[] = [
    { label: '添加 Key', key: 'add' },
    { label: '编辑提供商', key: 'edit' },
    { label: up.status === 'enabled' ? '停用提供商' : '启用提供商', key: 'status' },
    { label: '探测该提供商', key: 'probe' },
    { label: '删除提供商', key: 'delete' },
  ]
  return h(NDropdown, {
    trigger: 'click', options,
    onSelect: async (value: string) => {
      if (value === 'add') openCreateKey(up)
      if (value === 'edit') openEdit(up)
      if (value === 'delete') confirmDelete(up)
      if (value === 'status' || value === 'probe') {
        busy.value = `up-${up.id}`
        try {
          if (value === 'status') await updateUpstream(up.id, { ...up, status: up.status === 'enabled' ? 'disabled' : 'enabled' })
          else await runProbes({ upstream_id: up.id, deep: deep.value })
          message.success(value === 'status' ? '状态已更新' : '探测完成')
          await load({ preserveOrder: true })
        } catch (e) { message.error(errText(e)) }
        finally { busy.value = null }
      }
    },
  }, { default: () => h(NButton, { size: 'tiny', quaternary: true, 'aria-label': `${up.name} 操作`, loading: busy.value === `up-${up.id}` },
    { icon: () => h(NIcon, null, { default: () => h(EllipsisHorizontalOutline) }) }) })
}

function renderBalance(up: Upstream) {
  return h('div', { class: 'balance-cell' }, [
    h(NTooltip, null, {
      trigger: () => h('span', { class: ['balance-value', { 'is-low': isLowBalance(up.last_balance), 'is-unknown': up.last_balance == null && !up.last_balance_at }] },
        up.last_balance == null ? (up.last_balance_at ? '不限' : '未知') : formatMoney(up.last_balance)),
      default: () => up.last_balance_at ? (up.last_balance == null ? `不限额度 · 更新于 ${formatTime(up.last_balance_at)}` : `更新于 ${formatTime(up.last_balance_at)}`) : '尚无余额数据',
    }),
    iconButton(RefreshOutline, `刷新 ${up.name} 余额`, () => void refreshUpstream(up), busy.value === `bal-up-${up.id}`),
  ])
}

const columns = computed<DataTableColumns<ProviderRow>>(() => {
  const provider: DataTableColumns<ProviderRow> = [{
    title: narrow.value ? '提供商 / 余额' : '提供商', key: 'provider', width: narrow.value ? 154 : 215,
    fixed: 'left', rowSpan: (row) => row.span, className: 'provider-cell',
    render: ({ upstream: up }) => h('div', { class: 'provider-info' }, [
      h('div', { class: 'provider-name-line' }, [h('strong', { title: up.name }, up.name), providerMenu(up)]),
      h('a', { class: 'provider-url', href: providerHref(up.base_url), target: '_blank', rel: 'noopener noreferrer', title: up.base_url }, up.base_url),
      h('div', { class: 'provider-meta' }, [
        h(StatusTag, { status: up.status }),
        h(NTooltip, null, {
          trigger: () => h(HealthTag, { status: up.health_status || 'healthy' }),
          default: () => up.cooldown_until
            ? `冷却至 ${formatTime(up.cooldown_until)}${up.last_error ? ` · ${up.last_error}` : ''}`
            : (up.last_error || '提供商运行状态'),
        }),
        h('span', KIND_LABEL[up.kind]),
      ]),
      h('div', { class: 'muted', title: `${healthSummary(up.summary?.health_counts ?? {})} · 并发 ${concLabel(up.concurrency)}` },
        `${up.summary?.key_count ?? 0} 把 Key · ${up.protocols.map((p) => PROTOCOL_LABEL[p]).join(' / ')}`),
      up.note ? h('div', { class: 'provider-note', title: up.note }, up.note) : null,
      narrow.value ? renderBalance(up) : null,
    ]),
  }]
  if (!narrow.value) provider.push({
    title: '余额', key: 'balance', width: 130, fixed: 'left',
    rowSpan: (row) => row.span, className: 'provider-cell',
    render: ({ upstream }) => renderBalance(upstream),
  })
  const order = ['name', 'health_status', 'rate_multiplier', 'channel_score', 'health_pulse', 'route_groups', 'models_count', 'cache_rate', 'actions']
  for (const key of order) {
    const original = keyColumns.find((col) => 'key' in col && col.key === key)
    if (!original || !('key' in original) || 'children' in original) continue
    provider.push({
      title: original.title,
      key: original.key,
      align: original.align,
      width: key === 'name' ? 140 : key === 'health_status' ? 80 : key === 'rate_multiplier' ? 70 : key === 'channel_score' ? 80 : key === 'health_pulse' ? 220 : key === 'route_groups' ? 130 : original.width,
      render: (row, index) => {
        if (!row.key) {
          if (key !== 'name') return null
          if ((row.upstream.summary?.key_count ?? 0) > 0) return h('span', { class: 'muted' }, loading.value ? '加载中' : 'Key 数据待刷新')
          return h(NButton, { size: 'tiny', onClick: () => openCreateKey(row.upstream) }, { default: () => '添加 Key' })
        }
        if (key === 'name') return h('div', { class: 'key-name', title: row.key.name }, [
          h('span', row.key.name_tag || inferNameTag(row.key.name, row.upstream.name)),
          h('small', { class: 'preview' }, row.key.key_preview),
        ])
        return original.render ? original.render(row.key, index) : String(row.key[key as keyof PlatformKey] ?? '')
      },
    })
  }
  return provider
})


function healthSummary(counts: Partial<Record<HealthStatus, number>>) {
  const order: HealthStatus[] = ['healthy', 'degraded', 'down', 'cooldown', 'low_balance', 'disabled']
  return order
    .filter((k) => counts[k])
    .map((k) => `${HEALTH_LABEL[k]} ${counts[k]}`)
    .join(' · ')
}

function concLabel(n?: number | null) {
  const v = Number(n) || 0
  return v > 0 ? String(v) : '不限制'
}

function providerHref(url?: string | null) {
  const u = (url || '').trim()
  if (!u) return ''
  if (/^https?:\/\//i.test(u)) return u
  return `https://${u}`
}

onMounted(() => {
  window.addEventListener('resize', resize)
  void loadRouteGroups()
  void load().then(() => {
    if (live.value) startLive()
  })
})

watch(live, (on) => {
  if (on) {
    startLive()
    void load({ silent: true })
  } else stopLive()
})

onUnmounted(() => {
  disposed = true
  loadSequence++
  stopLive()
  window.removeEventListener('resize', resize)
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <h2>提供商</h2>
      <div class="toolbar">
        <n-switch v-model:value="live" size="small" aria-label="自动刷新" />
        <span class="live-label">自动刷新</span>
        <span v-if="lastRefresh" class="muted refresh-time">{{ lastRefresh }}</span>
        <n-tooltip>
          <template #trigger><n-button size="small" quaternary aria-label="刷新列表" :loading="loading" @click="load()"><template #icon><n-icon><RefreshOutline /></n-icon></template></n-button></template>
          刷新列表
        </n-tooltip>
        <n-checkbox v-model:checked="deep">深度探测</n-checkbox>
        <n-dropdown trigger="click" :options="[{ label: '刷新全部余额', key: 'balance', disabled: refreshingBal }, { label: '探测全部', key: 'probe', disabled: probing }]" @select="(key: string) => key === 'balance' ? refreshAll() : probeAll()">
          <n-button size="small" :loading="refreshingBal || probing">全部操作</n-button>
        </n-dropdown>
        <n-button type="primary" size="small" @click="openCreate"><template #icon><n-icon><AddOutline /></n-icon></template>新建提供商</n-button>
      </div>
    </div>
    <div class="provider-filters">
      <n-radio-group class="quick-filters" v-model:value="quick" size="small" @update:value="search">
        <n-radio-button v-for="option in quickOptions" :key="option.value" :value="option.value">{{ option.label }}</n-radio-button>
      </n-radio-group>
      <div class="filter-fields">
        <n-input v-model:value="filters.query" clearable placeholder="名称、地址或备注" :input-props="{ 'aria-label': '搜索提供商' }" @keyup.enter="search" />
        <n-select v-model:value="filters.kind" :options="KIND_OPTIONS" clearable placeholder="类型" />
        <n-select v-model:value="filters.protocol" :options="PROTOCOL_OPTIONS" clearable placeholder="协议" />
        <n-select v-model:value="filters.status" :options="STATUS_OPTIONS" clearable placeholder="启停状态" />
        <n-button size="small" type="primary" secondary @click="search">查询</n-button>
        <n-button size="small" @click="resetFilters">重置</n-button>
      </div>
    </div>
    <n-alert v-if="error" type="error" :title="error" />
    <div class="table-meta">
      <span>共 {{ orderedIds.length }} 家提供商<span v-if="rows.length"> · 本页 {{ keys.length }} 把 Key</span></span>
      <span class="muted">异常优先</span>
    </div>
    <n-data-table
      class="provider-table"
      size="small"
      :columns="columns"
      :data="rows"
      :loading="loading"
      :row-key="(row: ProviderRow) => row.id"
      :row-class-name="(row: ProviderRow) => row.first ? 'provider-first' : ''"
      :scroll-x="narrow ? 1166 : 1357"
      :max-height="720"
      :single-line="false"
    >
      <template #empty><n-empty :description="items.length ? '没有匹配的提供商' : '还没有提供商'" /></template>
    </n-data-table>
    <div class="provider-pagination">
      <n-pagination :page="page" :page-size="pageSize" :item-count="orderedIds.length" show-size-picker :page-sizes="[10, 20, 50]"
        @update:page="changePage"
        @update:page-size="(size: number) => { pageSize = size; changePage(1) }" />
    </div>

    <n-modal v-model:show="showForm" preset="card" :title="editing ? '编辑提供商' : '新建提供商'" style="width: min(560px, calc(100vw - 24px))">
      <n-form ref="formRef" :model="form" :rules="rules" label-placement="left" label-width="90">
        <n-form-item label="名称" path="name">
          <n-input v-model:value="form.name" placeholder="例如 NewAPI-主池" />
        </n-form-item>
        <n-form-item label="Base URL" path="base_url">
          <n-input v-model:value="form.base_url" placeholder="https://api.example.com" />
        </n-form-item>
        <n-form-item label="类型" path="kind">
          <n-select v-model:value="form.kind" :options="KIND_OPTIONS" />
        </n-form-item>
        <n-form-item label="协议" path="protocols">
          <n-checkbox-group v-model:value="form.protocols">
            <n-space>
              <n-checkbox v-for="opt in PROTOCOL_OPTIONS" :key="opt.value" :value="opt.value" :label="opt.label" />
            </n-space>
          </n-checkbox-group>
        </n-form-item>
        <n-form-item label="状态" path="status">
          <n-radio-group v-model:value="form.status">
            <n-radio v-for="opt in STATUS_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</n-radio>
          </n-radio-group>
        </n-form-item>
        <n-form-item label="并发" path="concurrency">
          <div class="rate-field">
            <n-input-number v-model:value="form.concurrency" :min="0" style="width: 100%" />
            <div class="muted rate-hint">该提供商下所有 Key 共享此上限。上游接口无法自动发现，0 表示不限制。</div>
          </div>
        </n-form-item>
        <n-form-item label="备注" path="note">
          <n-input v-model:value="form.note" type="textarea" :rows="2" placeholder="可选" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showForm = false">取消</n-button>
          <n-button type="primary" :loading="saving" @click="save">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <n-modal v-model:show="showKeyForm" preset="card" :title="editingKey ? '编辑 Key' : `添加 Key · ${keyHost?.name || ''}`" style="width: min(520px, calc(100vw - 24px))">
      <n-form ref="keyFormRef" :model="keyForm" :rules="keyRules" label-placement="left" label-width="100">
        <n-form-item label="标识" path="name_tag">
          <div class="rate-field">
            <n-input v-model:value="keyForm.name_tag" placeholder="例如 稳定" maxlength="64" />
            <div class="muted rate-hint">完整名称固定为「提供商-标识-倍率」：{{ keyNamePreview }}</div>
          </div>
        </n-form-item>
        <n-form-item label="API Key" path="api_key">
          <n-input
            v-model:value="keyForm.api_key"
            type="password"
            show-password-on="click"
            :placeholder="editingKey ? '留空则不修改' : '仅此次提交，列表不会回显'"
          />
        </n-form-item>
        <n-form-item label="倍率" path="rate_multiplier">
          <div class="rate-field">
            <n-input-number
              v-model:value="keyForm.rate_multiplier"
              :min="0"
              :max="1000"
              :step="0.01"
              :precision="4"
              placeholder="可不填，保存后自动同步"
              style="width: 100%"
              clearable
            />
            <div class="muted rate-hint">
              <template v-if="keyFormCanSync">可不填。保存后自动拉一次倍率、模型，并刷新该提供商余额；之后每分钟同步。</template>
              <template v-else>可不填，默认 1。保存后仍会拉取模型，并刷新该提供商余额。</template>
            </div>
          </div>
        </n-form-item>
        <n-form-item v-if="keyFormIsNewAPI" label="new-api 分组" path="billing_group">
          <div class="rate-field">
            <n-input v-model:value="keyForm.billing_group" placeholder="default" />
            <div class="muted rate-hint">令牌在 new-api 上所属的分组名，「同步倍率」按此名在 /api/pricing 的 group_ratio 中取值。</div>
          </div>
        </n-form-item>
        <n-form-item label="探测间隔" path="probe_interval_sec">
          <div class="rate-field">
            <n-input-number
              v-model:value="keyForm.probe_interval_sec"
              :min="0"
              :step="60"
              placeholder="0 跟随全局"
              style="width: 100%"
              clearable
            />
            <div class="muted rate-hint">单位秒。留空或 0 跟随全局定时任务（默认 1 分钟）。手动「探测全部」仍会探测此 Key。</div>
          </div>
        </n-form-item>
        <n-form-item label="RPM 上限" path="rpm_limit">
          <div class="rate-field">
            <n-input-number v-model:value="keyForm.rpm_limit" :min="0" :step="1" style="width: 100%" />
            <div class="muted rate-hint">该 Key 每分钟最多接收的请求数，0 表示不限。</div>
          </div>
        </n-form-item>
        <n-form-item label="Key 并发" path="max_concurrency">
          <div class="rate-field">
            <n-input-number v-model:value="keyForm.max_concurrency" :min="0" :step="1" style="width: 100%" />
            <div class="muted rate-hint">该 Key 的实例内在途请求上限，0 表示不限。</div>
          </div>
        </n-form-item>
        <n-form-item label="状态" path="status">
          <n-radio-group v-model:value="keyForm.status">
            <n-radio v-for="opt in STATUS_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</n-radio>
          </n-radio-group>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showKeyForm = false">取消</n-button>
          <n-button type="primary" :loading="keySaving" @click="saveKey">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <ModelListModal v-model:show="showModels" :row="modelsRow" @updated="onModelsUpdated" />
  </div>
</template>

<style scoped>
.page-head { align-items: center; flex-wrap: wrap; }
.page-head h2 { letter-spacing: 0; }
.provider-filters { display: flex; flex-direction: column; gap: 12px; padding: 14px 0; border-block: 1px solid #d4dce1; }
.quick-filters { display: flex; flex-wrap: wrap; gap: 4px 0; height: auto; }
.filter-fields { display: grid; grid-template-columns: minmax(180px, 1fr) 160px 135px 125px auto auto; gap: 8px; align-items: center; }
.table-meta { display: flex; justify-content: space-between; align-items: center; font-size: 13px; }
.provider-pagination { display: flex; justify-content: flex-end; overflow-x: auto; padding-bottom: 4px; }
:deep(.provider-cell) { vertical-align: top !important; background: #f6f9f9 !important; }
:deep(.provider-first td) { border-top: 2px solid #dce3e7; }
:deep(.provider-info) { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
:deep(.provider-name-line) { display: flex; align-items: start; justify-content: space-between; gap: 4px; }
:deep(.provider-name-line strong) { font-size: 13px; overflow-wrap: anywhere; min-width: 0; }
:deep(.provider-name-line .n-button) { flex: none; }
:deep(.provider-url), :deep(.provider-note) { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11px; color: #667085; text-decoration: none; }
:deep(.provider-url:hover) { color: #0f766e; }
:deep(.provider-meta) { display: flex; flex-wrap: wrap; gap: 5px; align-items: center; font-size: 11px; color: #667085; }
:deep(.balance-cell) { display: flex; flex-wrap: wrap; align-items: center; gap: 3px; }
:deep(.balance-value) { font-size: 14px; font-weight: 650; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; color: #137960; }
:deep(.balance-value.is-low) { color: #bc352c; }
:deep(.balance-value.is-unknown) { color: #7b8790; font-weight: 400; }
:deep(.key-name) { display: flex; flex-direction: column; gap: 3px; overflow-wrap: anywhere; }
:deep(.key-name small) { color: #7b8790; font-size: 11px; }
.live-label { font-size: 13px; color: #344054; }
.rate-field { width: 100%; }
.rate-hint { margin-top: 6px; font-size: 12px; line-height: 1.5; }
@media (max-width: 1000px) { .filter-fields { grid-template-columns: minmax(180px, 1fr) 130px 130px; } .refresh-time { display: none; } }
@media (max-width: 760px) { .filter-fields { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); } .page-head { align-items: start; } .toolbar { gap: 6px; } }
</style>
