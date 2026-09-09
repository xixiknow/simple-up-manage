<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { NButton, NDropdown, NSpace, NTag, NTooltip, useDialog, useMessage } from 'naive-ui'
import type { DataTableColumns, DropdownOption, FormInst, FormRules } from 'naive-ui'
import {
  actionMessage,
  createKey,
  createUpstream,
  deleteKey,
  deleteUpstream,
  fetchKeyModels,
  listKeys,
  listRouteGroups,
  listUpstreams,
  probeKey,
  refreshAllBalances,
  refreshUpstreamBalance,
  refreshKeyBilling,
  runProbes,
  updateKey,
  updateUpstream,
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
const RECENT_MS = 24 * 60 * 60 * 1000

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

function lastUsedMs(list: PlatformKey[]) {
  let max = 0
  for (const k of list) {
    if (!k.last_request_at) continue
    const t = Date.parse(k.last_request_at)
    if (!Number.isNaN(t) && t > max) max = t
  }
  return max > 0 ? max : null
}

function isRecentlyUsed(ms: number | null) {
  return ms != null && Date.now() - ms <= RECENT_MS
}

function isLowBalance(value?: number | null) {
  return typeof value === 'number' && value < LOW_BALANCE
}

function keyRowKey(row: PlatformKey) {
  return row.id
}

const boards = computed(() => {
  const mapped = items.value.map((up) => {
    const list = keys.value.filter((k) => k.upstream_id === up.id)
    const counts: Partial<Record<HealthStatus, number>> = {}
    for (const k of list) {
      counts[k.health_status] = (counts[k.health_status] || 0) + 1
    }
    return { upstream: up, keys: list, counts, usedAt: lastUsedMs(list) }
  })
  mapped.sort((a, b) => {
    const aRecent = isRecentlyUsed(a.usedAt)
    const bRecent = isRecentlyUsed(b.usedAt)
    if (aRecent !== bRecent) return aRecent ? -1 : 1
    if (aRecent) {
      const va = typeof a.upstream.last_balance === 'number' ? a.upstream.last_balance : Number.POSITIVE_INFINITY
      const vb = typeof b.upstream.last_balance === 'number' ? b.upstream.last_balance : Number.POSITIVE_INFINITY
      if (va !== vb) return va - vb
      return a.upstream.id - b.upstream.id
    }
    const ua = a.usedAt ?? 0
    const ub = b.usedAt ?? 0
    if (ua !== ub) return ub - ua
    return a.upstream.id - b.upstream.id
  })
  return mapped
})

async function load(opts?: { silent?: boolean }) {
  const silent = !!opts?.silent
  if (!silent) {
    loading.value = true
    error.value = ''
  }
  try {
    const [upRes, keyRes] = await Promise.all([
      listUpstreams({ page: 1, page_size: 100 }),
      listKeys({ page: 1, page_size: 200 }),
      silent ? Promise.resolve() : loadRouteGroups(),
    ])
    items.value = upRes.items
    keys.value = keyRes.items
    lastRefresh.value = formatTime(new Date().toISOString())
  } catch (e) {
    if (!silent) {
      error.value = errText(e)
      items.value = []
      keys.value = []
    }
  } finally {
    if (!silent) loading.value = false
  }
}

function startLive() {
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
  busy.value = `rate-${row.id}`
  try {
    const data = await refreshKeyBilling(row.id)
    message.success(actionMessage(data, '已同步倍率'))
    await load()
  } catch (e) {
    message.error(errText(e, '同步倍率失败'))
  } finally {
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
    title: '探测间隔',
    key: 'probe_interval_sec',
    width: 96,
    render(row) {
      const label = formatProbeInterval(row.probe_interval_sec)
      return h('span', { class: label === '默认' ? 'muted' : undefined }, label)
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
    width: 196,
    align: 'right',
    render(row) {
      const rowBusy =
        busy.value === `probe-${row.id}` ||
        busy.value === `rate-${row.id}` ||
        busy.value === `models-${row.id}`
      const more: DropdownOption[] = [
        { label: '探测', key: 'probe', disabled: rowBusy },
        { label: '获取模型', key: 'models', disabled: rowBusy },
      ]
      if (row.upstream_kind && BILLING_KINDS.includes(row.upstream_kind)) {
        more.splice(1, 0, { label: '同步倍率', key: 'rate', disabled: rowBusy })
      }
      return h(
        NSpace,
        { size: 4, wrap: false, justify: 'end' },
        {
          default: () => [
            h(NButton, { size: 'tiny', quaternary: true, onClick: () => openEditKey(row) }, { default: () => '编辑' }),
            h(
              NButton,
              { size: 'tiny', quaternary: true, type: 'error', onClick: () => confirmDeleteKey(row) },
              { default: () => '删除' },
            ),
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
                },
              },
              {
                default: () =>
                  h(NButton, { size: 'tiny', quaternary: true, loading: rowBusy }, { default: () => '更多' }),
              },
            ),
          ],
        },
      )
    },
  },
]

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

function formatProbeInterval(sec?: number | null) {
  const n = Number(sec) || 0
  if (n <= 0) return '默认'
  if (n % 60 === 0 && n >= 60) return `${n / 60} 分`
  return `${n} 秒`
}

function providerHref(url?: string | null) {
  const u = (url || '').trim()
  if (!u) return ''
  if (/^https?:\/\//i.test(u)) return u
  return `https://${u}`
}

onMounted(() => {
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

onUnmounted(stopLive)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h2>提供商</h2>
        <p>登记供应商并管理其 Key。余额与近 60 分钟健康色块：绿正常（&lt;6s） / 黄降级（≥6s） / 红失败 / 灰无数据</p>
      </div>
      <div class="toolbar">
        <n-switch v-model:value="live" size="small" />
        <span class="live-label">自动刷新</span>
        <span v-if="lastRefresh" class="muted">{{ lastRefresh }}</span>
        <n-checkbox v-model:checked="deep">深度探测</n-checkbox>
        <n-button size="small" :loading="refreshingBal" @click="refreshAll">刷新全部余额</n-button>
        <n-button size="small" :loading="probing" @click="probeAll">探测全部</n-button>
        <n-button type="primary" size="small" @click="openCreate">新建提供商</n-button>
      </div>
    </div>

    <n-alert v-if="error" type="error" :title="error" closable @close="error = ''" />

    <n-spin :show="loading">
      <n-empty v-if="!loading && !boards.length" description="还没有提供商">
        <template #extra>
          <n-button type="primary" size="small" @click="openCreate">新建第一个提供商</n-button>
        </template>
      </n-empty>
      <div v-else class="boards">
        <n-card v-for="board in boards" :key="board.upstream.id" size="small" :bordered="false">
          <template #header>
            <div class="board-title">
              <strong class="board-name">{{ board.upstream.name }}</strong>
              <StatusTag :status="board.upstream.status" />
              <n-tag size="small" :bordered="false">{{ KIND_LABEL[board.upstream.kind] || board.upstream.kind }}</n-tag>
              <n-tag v-for="p in board.upstream.protocols" :key="p" size="small" :bordered="false">
                {{ PROTOCOL_LABEL[p] || p }}
              </n-tag>
            </div>
          </template>
          <template #header-extra>
            <n-space size="small" align="center" :wrap="false">
              <n-tooltip v-if="board.upstream.last_balance_at" trigger="hover">
                <template #trigger>
                  <span
                    class="board-balance"
                    :class="{
                      'is-low': isLowBalance(board.upstream.last_balance),
                      'is-empty': board.upstream.last_balance == null,
                    }"
                  >
                    <template v-if="board.upstream.last_balance != null">
                      <span class="yen">￥</span>{{ formatMoney(board.upstream.last_balance) }}
                    </template>
                    <template v-else>—</template>
                  </span>
                </template>
                刷新于 {{ formatTime(board.upstream.last_balance_at) }}
              </n-tooltip>
              <span
                v-else
                class="board-balance"
                :class="{
                  'is-low': isLowBalance(board.upstream.last_balance),
                  'is-empty': board.upstream.last_balance == null,
                }"
              >
                <template v-if="board.upstream.last_balance != null">
                  <span class="yen">￥</span>{{ formatMoney(board.upstream.last_balance) }}
                </template>
                <template v-else>—</template>
              </span>
              <n-button
                size="tiny"
                quaternary
                :loading="busy === `bal-up-${board.upstream.id}`"
                @click="refreshUpstream(board.upstream)"
              >
                刷余额
              </n-button>
              <n-button size="tiny" type="primary" secondary @click="openCreateKey(board.upstream)">添加 Key</n-button>
              <n-button size="tiny" quaternary @click="openEdit(board.upstream)">编辑</n-button>
              <n-button size="tiny" quaternary type="error" @click="confirmDelete(board.upstream)">删除</n-button>
            </n-space>
          </template>
          <p class="muted board-meta">
            <a
              class="preview board-link"
              :href="providerHref(board.upstream.base_url)"
              target="_blank"
              rel="noopener noreferrer"
            >{{ board.upstream.base_url }}</a>
            <span> · 并发 {{ concLabel(board.upstream.concurrency) }}</span>
            <span v-if="board.keys.length"> · {{ healthSummary(board.counts) || `${board.keys.length} 把 Key` }}</span>
            <span v-if="board.upstream.note"> · {{ board.upstream.note }}</span>
          </p>
          <n-empty v-if="!board.keys.length" description="该提供商还没有 Key">
            <template #extra>
              <n-button size="small" type="primary" @click="openCreateKey(board.upstream)">添加 Key</n-button>
            </template>
          </n-empty>
          <n-data-table
            v-else
            size="small"
            :columns="keyColumns"
            :data="board.keys"
            :row-key="keyRowKey"
            :scroll-x="1420"
          />
        </n-card>
      </div>
    </n-spin>

    <n-modal v-model:show="showForm" preset="card" :title="editing ? '编辑提供商' : '新建提供商'" style="width: 560px">
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

    <n-modal v-model:show="showKeyForm" preset="card" :title="editingKey ? '编辑 Key' : `添加 Key · ${keyHost?.name || ''}`" style="width: 520px">
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
.boards {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.board-title {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.board-name {
  font-size: 15px;
  font-weight: 650;
}
.board-meta {
  margin: -4px 0 10px;
}
.board-link {
  color: inherit;
  text-decoration: none;
}
.board-link:hover {
  color: #0f9d8e;
  text-decoration: underline;
}
.board-balance {
  display: inline-flex;
  align-items: baseline;
  justify-content: flex-end;
  width: max-content;
  padding: 3px 8px;
  border-radius: 6px;
  font-size: 18px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  line-height: 1.15;
  color: #0f766e;
  background: #ccfbf1;
}
.board-balance .yen {
  margin-right: 2px;
  font-size: 12px;
  font-weight: 650;
  opacity: 0.75;
}
.board-balance.is-low {
  color: #b42318;
  background: #fee4e2;
}
.board-balance.is-empty {
  color: #98a2b3;
  background: #f2f4f7;
  font-size: 14px;
  font-weight: 500;
  justify-content: center;
}
.live-label {
  font-size: 13px;
  color: #344054;
}
.rate-field {
  width: 100%;
}
.rate-hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.5;
}
</style>
