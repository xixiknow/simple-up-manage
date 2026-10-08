<script setup lang="ts">
import { computed, h, reactive, ref, watch } from 'vue'
import {
  actionMessage,
  allPages,
  batchKeyBilling,
  batchKeyDelete,
  batchKeyStatus,
  batchRouteGroupKeys,
  deleteKey,
  fetchKeyModels,
  listKeyRates,
  listKeys,
  refreshKeyBilling,
  runProbes,
  updateKey,
} from '@/api/admin'
import { BILLING_KINDS, type PlatformKey, type RouteGroup, type RouteGroupRef, type Upstream } from '@/api/types'
import HealthPulse from '@/components/HealthPulse.vue'
import HealthTag from '@/components/HealthTag.vue'
import ModelListModal from '@/components/ModelListModal.vue'
import RouteGroupTags from '@/components/RouteGroupTags.vue'
import StatusTag from '@/components/StatusTag.vue'
import { errText, formatMoney, formatPercent, formatRate, formatTime, inferNameTag } from '@/utils/format'
import type { DataTableColumns, DropdownOption } from '@/components/ui'
import { UiButton, UiDropdown, UiIcon, UiSpace, UiSwitch, UiTag, UiTooltip, useDialog, useMessage } from '@/components/ui'
import { CreateOutline, EllipsisHorizontalOutline } from '@/components/ui/icons'

const props = defineProps<{ provider: Upstream; groups: RouteGroup[]; refreshToken: number; probing?: boolean }>()
const emit = defineEmits<{
  'provider-action': [action: 'edit' | 'toggle' | 'delete' | 'probe' | 'refresh-balance' | 'fetch-models' | 'add-key']
  'key-edit': [key: PlatformKey]
  'open-probe': [target: { name: string; key_id?: number; models?: string[] }]
  changed: []
}>()

const message = useMessage()
const dialog = useDialog()

/* ---------- 摘要 ---------- */
function isLow(v?: number | null) {
  return typeof v === 'number' && v < 50
}
const balanceText = computed(() => (props.provider.last_balance == null ? (props.provider.last_balance_at ? '不限' : '未知') : formatMoney(props.provider.last_balance)))
const healthCounts = computed(() => props.provider.summary?.health_counts ?? {})
const abnormalCount = computed(() => (props.provider.status === 'enabled' ? props.provider.summary?.abnormal_count ?? 0 : 0))
const lastRequestAt = computed(() => {
  const t = props.provider.summary?.last_request_at
  return t ? formatTime(t) : '暂无请求'
})

/* ---------- Key 列表（服务端分页/筛选/排序） ---------- */
const items = ref<PlatformKey[]>([])
const total = ref(0)
const loading = ref(false)
const filters = reactive({ search: '', status: null as string | null, health: null as string | null, sort: 'id' })
const page = ref(1)
const pageSize = ref(20)
const selection = ref(new Set<number>())
const busyKeys = ref(new Set<string>())
const syncingRateIds = new Set<number>()
const rateSnapshot = new Map<number, number>()
let loadSeq = 0

const sortRule = computed(() => {
  switch (filters.sort) {
    case 'rate_asc': return { sort: 'rate_multiplier' as const, order: 'asc' as const }
    case 'rate_desc': return { sort: 'rate_multiplier' as const, order: 'desc' as const }
    case 'last_request': return { sort: 'last_request_at' as const, order: 'desc' as const }
    default: return { sort: 'id' as const, order: 'asc' as const }
  }
})

async function loadKeys(opts?: { silent?: boolean }) {
  const seq = ++loadSeq
  const pid = props.provider.id
  if (!opts?.silent) loading.value = true
  try {
    const result = await listKeys({
      upstream_id: pid,
      page: page.value,
      page_size: pageSize.value,
      search: filters.search.trim() || undefined,
      status: filters.status || undefined,
      health_status: filters.health || undefined,
      sort: sortRule.value.sort,
      order: sortRule.value.order,
    })
    if (seq !== loadSeq || props.provider.id !== pid) return
    items.value = result.items
    total.value = result.total
    void diffRates(pid)
  } catch (e) {
    if (seq === loadSeq) message.error(errText(e, '加载 Key 失败'))
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

// 倍率变动提醒只针对当前查看的提供商；全局变动走顶栏消息收件箱。
async function diffRates(pid: number) {
  try {
    const rates = await allPages((params) => listKeyRates({ ...params, upstream_id: pid }))
    if (pid !== props.provider.id) return
    const changes: Array<{ name: string; previous: number; current: number }> = []
    for (const rate of rates) {
      if (syncingRateIds.has(rate.id)) continue
      const previous = rateSnapshot.get(rate.id)
      const current = rate.rate_multiplier
      if (!Number.isFinite(current)) continue
      if (previous != null && Math.abs(previous - current) > 1e-9) {
        changes.push({ name: rate.name, previous, current })
      }
      rateSnapshot.set(rate.id, current)
    }
    const shown = changes.slice(0, 3)
    for (const change of shown) {
      const direction = change.current < change.previous ? '降价' : '涨价'
      const detail = `${props.provider.name} / ${change.name}: ×${formatAlertRate(change.previous)} → ×${formatAlertRate(change.current)}`
      const content = () => h('div', { style: 'max-width: min(560px, calc(100vw - 100px)); overflow-wrap: anywhere' }, `倍率${direction}：${detail}`)
      if (direction === '降价') message.success(content, { duration: 7000, closable: true })
      else message.warning(content, { duration: 9000, closable: true })
    }
    if (changes.length > shown.length) {
      message.info(`另有 ${changes.length - shown.length} 把 Key 的倍率发生变化`, { duration: 7000 })
    }
  } catch {
    /* rate diff is best-effort */
  }
}

function formatAlertRate(value: number) {
  return value.toLocaleString('en-US', { maximumFractionDigits: 9, useGrouping: false })
}

watch(() => props.provider.id, () => {
  rateSnapshot.clear()
  Object.assign(filters, { search: '', status: null, health: null, sort: 'id' })
  page.value = 1
  selection.value = new Set()
  void loadKeys()
}, { immediate: true })
watch(() => [filters.search, filters.status, filters.health, filters.sort], () => {
  page.value = 1
  void loadKeys()
})
watch(() => [page.value, pageSize.value], () => void loadKeys())
watch(() => props.refreshToken, () => void loadKeys({ silent: true }))

/* ---------- 表格 ---------- */
const showModels = ref(false)
const modelsRow = ref<PlatformKey | null>(null)
const SCORE_TERM_LABEL: Record<string, string> = { success: '成功率', latency: '延迟', cache: '缓存' }

function keyBusy(kind: string, id: number) {
  return busyKeys.value.has(`${kind}-${id}`)
}
function setBusy(kind: string, id: number, on: boolean) {
  if (on) busyKeys.value.add(`${kind}-${id}`)
  else busyKeys.value.delete(`${kind}-${id}`)
  busyKeys.value = new Set(busyKeys.value)
}

const columns = computed<DataTableColumns<PlatformKey>>(() => [
  {
    title: '',
    key: 'select',
    width: 34,
    mobileHead: true,
    render(row) {
      return h('input', {
        type: 'checkbox',
        'aria-label': `选择 ${row.name}`,
        checked: selection.value.has(row.id),
        onChange: (e: Event) => {
          const next = new Set(selection.value)
          if ((e.target as HTMLInputElement).checked) next.add(row.id)
          else next.delete(row.id)
          selection.value = next
        },
      })
    },
  },
  {
    title: 'Key',
    key: 'name',
    minWidth: 150,
    ellipsis: { tooltip: true },
    mobileTitle: true,
    render(row) {
      return h('span', { class: 'key-name', title: row.name }, [
        row.name,
        row.probe_enabled === false
          ? h(UiTag, { size: 'tiny', bordered: false, type: 'warning', style: 'margin-left: 6px' }, { default: () => '探测已关闭' })
          : null,
      ])
    },
  },
  {
    title: '预览',
    key: 'key_preview',
    width: 140,
    render(row) {
      return h('span', { class: 'preview' }, row.key_preview || '—')
    },
  },
  {
    title: '状态',
    key: 'status',
    width: 74,
    render(row) {
      return h(UiSwitch, {
        size: 'small',
        value: row.status === 'enabled',
        loading: keyBusy('status', row.id),
        disabled: keyBusy('status', row.id),
        onUpdateValue: (on: boolean) => void toggleKeyStatus(row, on),
      }, { checked: () => '启用', unchecked: () => '停用' })
    },
  },
  {
    title: '健康',
    key: 'health_status',
    width: 96,
    render(row) {
      return h(HealthTag, { status: row.health_status })
    },
  },
  {
    title: '倍率',
    key: 'rate_multiplier',
    width: 104,
    render(row) {
      const synced = row.rate_synced_at ? `同步于 ${formatTime(row.rate_synced_at)}` : '手动填写'
      const extra = row.upstream_kind === 'new_api' && row.billing_group ? ` · 分组 ${row.billing_group}` : ''
      return h(UiTooltip, null, {
        trigger: () => h('span', { style: 'font-weight:600;font-variant-numeric:tabular-nums' }, `×${formatRate(row.rate_multiplier)}`),
        default: () => synced + extra,
      })
    },
  },
  {
    title: '评分',
    key: 'channel_score',
    width: 76,
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
      return h(UiTooltip, null, {
        trigger: () => h(UiTag, { size: 'small', bordered: false, type }, { default: () => String(n) }),
        default: () => lines.map((l) => h('div', l)),
      })
    },
  },
  {
    title: '近 30 次',
    key: 'health_pulse',
    width: 220,
    render(row) {
      return h(HealthPulse, { cells: row.health_pulse, lastProbeAt: formatTime(row.last_probe_at) })
    },
  },
  {
    title: '模型',
    key: 'models_count',
    width: 76,
    render(row) {
      const n = row.models_count ?? row.last_models?.length ?? 0
      return h(UiButton, { size: 'tiny', quaternary: true, type: n ? 'info' : 'default', onClick: () => openModels(row) }, { default: () => (n ? `${n} 个` : '未获取') })
    },
  },
  {
    title: '缓存',
    key: 'cache_rate',
    width: 68,
    mobileHide: true,
    render(row) {
      return row.cache_samples ? formatPercent(row.cache_rate) : '—'
    },
  },
  {
    title: '路由分组',
    key: 'route_groups',
    width: 150,
    mobileHide: true,
    render(row) {
      return h(RouteGroupTags, {
        keyId: row.id,
        groups: row.route_groups,
        options: props.groups,
        onUpdated: (groups: RouteGroupRef[]) => {
          row.route_groups = groups
          emit('changed')
        },
      })
    },
  },
  {
    title: '操作',
    key: 'actions',
    width: 140,
    align: 'right',
    fixed: 'right',
    render(row) {
      const rowBusy = keyBusy('rate', row.id) || keyBusy('models', row.id)
      const more: DropdownOption[] = [
        { label: row.probe_enabled === false ? '探测（已关闭）' : '探测', key: 'probe', disabled: rowBusy || props.probing || row.probe_enabled === false },
        { label: '获取模型', key: 'models', disabled: rowBusy },
        { label: '删除 Key', key: 'delete', disabled: rowBusy },
      ]
      if (row.upstream_kind && BILLING_KINDS.includes(row.upstream_kind)) {
        more.splice(1, 0, { label: '同步倍率', key: 'rate', disabled: rowBusy })
      }
      return h(UiSpace, { size: 4, wrap: false, justify: 'end', align: 'center' }, {
        default: () => [
          h(UiButton, { size: 'tiny', quaternary: true, 'aria-label': `编辑 Key ${row.name}`, onClick: () => emit('key-edit', row) }, { icon: () => h(UiIcon, null, { default: () => h(CreateOutline) }) }),
          h(UiDropdown, {
            trigger: 'click',
            placement: 'bottom-end',
            options: more,
            onSelect: (key: string) => {
              if (key === 'probe') emit('open-probe', { name: row.name, key_id: row.id, models: row.last_models })
              else if (key === 'rate') void syncRateOne(row)
              else if (key === 'models') void fetchModelsOne(row)
              else if (key === 'delete') confirmDeleteKey(row)
            },
          }, {
            default: () => h(UiButton, { size: 'tiny', quaternary: true, loading: rowBusy, 'aria-label': 'Key 操作' }, { icon: () => h(UiIcon, null, { default: () => h(EllipsisHorizontalOutline) }) }),
          }),
        ],
      })
    },
  },
])

/* ---------- 行级操作 ---------- */
async function toggleKeyStatus(row: PlatformKey, enabled: boolean) {
  const next = enabled ? 'enabled' : 'disabled'
  if (row.status === next) return
  setBusy('status', row.id, true)
  try {
    await updateKey(row.id, { name_tag: row.name_tag || inferNameTag(row.name, props.provider.name), status: next })
    row.status = next
    message.success(next === 'enabled' ? '已启用' : '已停用')
    emit('changed')
  } catch (e) {
    message.error(errText(e, '更新状态失败'))
  } finally {
    setBusy('status', row.id, false)
  }
}

async function syncRateOne(row: PlatformKey) {
  if (syncingRateIds.has(row.id)) return
  syncingRateIds.add(row.id)
  setBusy('rate', row.id, true)
  try {
    const data = await refreshKeyBilling(row.id)
    if (data.billing_unsupported) {
      message.warning('该 Key 暂不支持同步倍率')
    } else {
      message.success(`已同步倍率：×${formatAlertRate(data.rate_multiplier)}`)
    }
    syncingRateIds.delete(row.id)
    await loadKeys({ silent: true })
    emit('changed')
  } catch (e) {
    message.error(errText(e, '同步倍率失败'))
  } finally {
    syncingRateIds.delete(row.id)
    setBusy('rate', row.id, false)
  }
}

async function fetchModelsOne(row: PlatformKey) {
  setBusy('models', row.id, true)
  try {
    const data = await fetchKeyModels(row.id)
    message.success(actionMessage(data, '已获取模型'))
    await loadKeys({ silent: true })
  } catch (e) {
    message.error(errText(e, '获取模型失败'))
  } finally {
    setBusy('models', row.id, false)
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
        selection.value = new Set([...selection.value].filter((id) => id !== row.id))
        await loadKeys({ silent: true })
        emit('changed')
      } catch (e) {
        message.error(errText(e, '删除失败'))
      }
    },
  })
}

function openModels(row: PlatformKey) {
  modelsRow.value = row
  showModels.value = true
}
function onModelsUpdated(next: PlatformKey) {
  modelsRow.value = next
  const idx = items.value.findIndex((k) => k.id === next.id)
  if (idx >= 0) items.value[idx] = next
}

/* ---------- 批量操作 ---------- */
const showGroupModal = ref(false)
const groupTarget = ref<number | null>(null)
const groupBusy = ref(false)
const selectedIds = computed(() => [...selection.value])

function requireSelection() {
  if (!selection.value.size) {
    message.warning('请先勾选 Key')
    return false
  }
  return true
}

const batchActions = computed(() => {
  const anyBilling = items.value.some((k) => selection.value.has(k.id) && k.upstream_kind && BILLING_KINDS.includes(k.upstream_kind))
  return { billing: anyBilling }
})

async function batchToggle(status: 'enabled' | 'disabled') {
  if (!requireSelection()) return
  try {
    const data = await batchKeyStatus(selectedIds.value, status)
    message.success(`已批量${status === 'enabled' ? '启用' : '停用'} ${data.updated} 把 Key`)
    selection.value = new Set()
    await loadKeys({ silent: true })
    emit('changed')
  } catch (e) {
    message.error(errText(e, '批量操作失败'))
  }
}

function batchDelete() {
  if (!requireSelection()) return
  const n = selection.value.size
  dialog.warning({
    title: '批量删除 Key',
    content: `将删除选中的 ${n} 把 Key，其路由分组关联会一并移除。此操作不可恢复。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        const data = await batchKeyDelete(selectedIds.value)
        message.success(`已删除 ${data.deleted} 把 Key`)
        selection.value = new Set()
        await loadKeys({ silent: true })
        emit('changed')
      } catch (e) {
        message.error(errText(e, '批量删除失败'))
      }
    },
  })
}

async function batchProbe() {
  if (!requireSelection()) return
  try {
    const data = await runProbes({ key_ids: selectedIds.value })
    message.success(actionMessage(data, `探测完成：${data.message ?? ''}`))
    await loadKeys({ silent: true })
    emit('changed')
  } catch (e) {
    message.error(errText(e, '批量探测失败'))
  }
}

async function batchBilling() {
  if (!requireSelection()) return
  try {
    const data = await batchKeyBilling(selectedIds.value)
    if (data.failed > 0) message.warning(`${data.message} · 首个失败：${data.errors[0]?.error ?? ''}`)
    else message.success(data.message)
    selection.value = new Set()
    await loadKeys({ silent: true })
    emit('changed')
  } catch (e) {
    message.error(errText(e, '批量同步倍率失败'))
  }
}

function openGroupModal() {
  if (!requireSelection()) return
  if (!props.groups.length) {
    message.warning('还没有路由分组，请先到「API 密钥」页创建')
    return
  }
  groupTarget.value = props.groups[0]?.id ?? null
  showGroupModal.value = true
}

async function applyGroup(op: 'add' | 'remove') {
  const groupId = groupTarget.value
  if (!groupId || groupBusy.value) return
  groupBusy.value = true
  try {
    await batchRouteGroupKeys(groupId, op === 'add' ? { add: selectedIds.value } : { remove: selectedIds.value })
    const name = props.groups.find((g) => g.id === groupId)?.name ?? `#${groupId}`
    message.success(`已${op === 'add' ? '加入' : '移出'}分组「${name}」· ${selection.value.size} 把`)
    showGroupModal.value = false
    selection.value = new Set()
    await loadKeys({ silent: true })
    emit('changed')
  } catch (e) {
    message.error(errText(e, '批量调整分组失败'))
  } finally {
    groupBusy.value = false
  }
}

function onProviderMenu(key: string) {
  emit('provider-action', key as 'edit' | 'toggle' | 'delete')
}
</script>

<template>
  <div class="detail">
    <div class="card">
      <div class="d-head">
        <div class="d-title">
          <h2>
            {{ provider.name }}
            <StatusTag :status="provider.status" />
            <HealthTag
              :status="provider.health_status === 'cooldown' && (!provider.cooldown_until || new Date(provider.cooldown_until).getTime() <= Date.now()) ? 'healthy' : provider.health_status"
            />
            <span class="kind-label">{{ provider.base_url }}</span>
          </h2>
          <div class="muted note">
            <template v-if="provider.note">备注：{{ provider.note }} · </template>
            <template v-if="provider.cooldown_until && provider.health_status === 'cooldown'">冷却至 {{ formatTime(provider.cooldown_until) }} · </template>
            {{ (provider.protocols || []).length }} 种协议 · 并发 {{ provider.concurrency || '不限' }}
          </div>
        </div>
        <div class="d-actions">
          <ui-button type="primary" size="small" @click="emit('provider-action', 'add-key')">＋ 添加 Key</ui-button>
          <ui-button size="small" :disabled="props.probing" @click="emit('provider-action', 'probe')">探测</ui-button>
          <ui-button size="small" @click="emit('provider-action', 'refresh-balance')">刷新余额</ui-button>
          <ui-button size="small" @click="emit('provider-action', 'fetch-models')">获取模型</ui-button>
          <ui-dropdown trigger="click" :options="[
            { label: '编辑提供商', key: 'edit' },
            { label: provider.status === 'enabled' ? '停用提供商' : '启用提供商', key: 'toggle' },
            { label: '删除提供商', key: 'delete' },
          ]" @select="onProviderMenu">
            <ui-button size="small">更多 ▾</ui-button>
          </ui-dropdown>
        </div>
      </div>
    </div>

    <div class="stat-row">
      <div class="stat stat-key">
        <span>KEY 总数</span>
        <b>{{ provider.summary?.key_count ?? 0 }}</b>
        <div class="sub">停用 {{ healthCounts.disabled ?? 0 }}</div>
      </div>
      <div class="stat" :class="{ bad: abnormalCount > 0 }">
        <span>异常 KEY</span>
        <b>{{ abnormalCount }}</b>
        <div class="sub">失败 {{ healthCounts.down ?? 0 }} · 慢响应 {{ healthCounts.degraded ?? 0 }} · 冷却 {{ healthCounts.cooldown ?? 0 }}</div>
      </div>
      <div class="stat" :class="{ bad: isLow(provider.last_balance) }">
        <span>余额</span>
        <b>{{ balanceText }}</b>
        <div class="sub">更新于 {{ provider.last_balance_at ? formatTime(provider.last_balance_at) : '—' }}</div>
      </div>
      <div class="stat">
        <span>最近请求</span>
        <b class="time">{{ lastRequestAt }}</b>
        <div class="sub">管理时区 Asia/Shanghai</div>
      </div>
    </div>

    <div class="card keys-card">
      <div class="k-tools">
        <ui-input v-model:value="filters.search" clearable placeholder="搜索 Key 名称 / 标识 / 预览" style="width: 230px" />
        <ui-select v-model:value="filters.status" :options="[{ label: '状态：全部', value: null }, { label: '启用', value: 'enabled' }, { label: '停用', value: 'disabled' }]" placeholder="状态" style="width: 128px" />
        <ui-select
          v-model:value="filters.health"
          :options="[
            { label: '健康：全部', value: null },
            { label: '健康', value: 'healthy' },
            { label: '慢响应', value: 'degraded' },
            { label: '失败', value: 'down' },
            { label: '冷却中', value: 'cooldown' },
            { label: '低余额', value: 'low_balance' },
            { label: '停用', value: 'disabled' },
          ]"
          placeholder="健康"
          style="width: 136px"
        />
        <ui-select
          v-model:value="filters.sort"
          :options="[
            { label: '默认排序', value: 'id' },
            { label: '倍率从低到高', value: 'rate_asc' },
            { label: '倍率从高到低', value: 'rate_desc' },
            { label: '最近请求', value: 'last_request' },
          ]"
          style="width: 150px"
        />
        <span class="k-count muted">共 {{ total }} 把</span>
      </div>

      <ui-data-table
        size="small"
        card
        :columns="columns"
        :data="items"
        :loading="loading"
        :scroll-x="1430"
        :row-key="(row: PlatformKey) => row.id"
        :row-class-name="(row: PlatformKey) => (selection.has(row.id) ? 'row-selected' : '')"
      >
        <template #empty><ui-empty :description="filters.search || filters.status || filters.health ? '没有匹配的 Key' : '该提供商还没有 Key'" /></template>
      </ui-data-table>

      <ui-pagination
        class="k-pager"
        :page="page"
        :page-size="pageSize"
        :item-count="total"
        show-size-picker
        :page-sizes="[20, 50, 100]"
        @update:page="(n: number) => (page = n)"
        @update:page-size="(s: number) => { pageSize = s; page = 1 }"
      />

      <div class="batch-bar" :class="{ show: selection.size > 0 }">
        <span>已选 <b>{{ selection.size }}</b> 把 Key</span>
        <ui-button size="tiny" quaternary @click="batchToggle('enabled')">启用</ui-button>
        <ui-button size="tiny" quaternary @click="batchToggle('disabled')">停用</ui-button>
        <ui-button size="tiny" quaternary :disabled="props.probing" @click="batchProbe">探测</ui-button>
        <ui-button v-if="batchActions.billing" size="tiny" quaternary @click="batchBilling">同步倍率</ui-button>
        <ui-button size="tiny" quaternary @click="openGroupModal">分组</ui-button>
        <ui-button size="tiny" quaternary class="danger" @click="batchDelete">删除</ui-button>
        <ui-button size="tiny" quaternary class="ghost" @click="selection = new Set()">取消选择</ui-button>
      </div>
    </div>

    <ui-modal v-model:show="showGroupModal" preset="card" title="批量调整路由分组" style="width: min(420px, calc(100vw - 24px))">
      <div class="group-body">
        <div>对已选 <b>{{ selection.size }}</b> 把 Key：</div>
        <ui-select v-model:value="groupTarget" :options="groups.map((g) => ({ label: `${g.name}（${g.member_count ?? 0} 把）`, value: g.id }))" style="width: 100%; margin-top: 10px" />
        <div class="group-ops">
          <ui-button size="small" secondary :loading="groupBusy" @click="applyGroup('add')">加入该分组</ui-button>
          <ui-button size="small" secondary :loading="groupBusy" @click="applyGroup('remove')">移出该分组</ui-button>
        </div>
      </div>
      <template #footer>
        <ui-button @click="showGroupModal = false">取消</ui-button>
      </template>
    </ui-modal>

    <ModelListModal v-model:show="showModels" :row="modelsRow" @updated="onModelsUpdated" />
  </div>
</template>

<style scoped>
.detail { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
.card { background: #fff; border: 1px solid var(--line, #e1e7df); border-radius: 12px; padding: 16px 18px; min-width: 0; }
.d-head { display: flex; align-items: flex-start; gap: 10px; flex-wrap: wrap; }
.d-title { flex: 1; min-width: 220px; }
.d-title h2 { margin: 0; font-size: 16px; font-weight: 650; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.kind-label { color: #819087; font-size: 11px; font-weight: 400; overflow-wrap: anywhere; }
.note { margin-top: 5px; font-size: 12px; }
.d-actions { display: flex; gap: 7px; flex-wrap: wrap; }
.stat-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 9px; }
.stat { border: 1px solid #eef0e9; border-radius: 10px; padding: 10px 13px; background: #fbfcf8; }
.stat-key { background: var(--accent-soft, #e8eedb); border-color: #dbe7c8; }
.stat > span { font-size: 10px; color: #819087; letter-spacing: .8px; display: block; }
.stat b { display: block; font-size: 19px; font-weight: 650; letter-spacing: -.3px; }
.stat b.time { font-size: 14px; padding-top: 4px; }
.stat .sub { font-size: 11px; color: #819087; margin-top: 3px; }
.stat.bad b { color: #a16d50; }
.k-tools { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 12px; }
.k-count { margin-left: auto; font-size: 12px; }
.k-pager { justify-content: flex-end; }
.batch-bar {
  display: none; align-items: center; gap: 6px; flex-wrap: wrap;
  position: fixed; bottom: 18px; left: 50%; transform: translateX(-50%); z-index: 40;
  width: max-content; max-width: min(920px, calc(100vw - 300px)); padding: 10px 16px; border-radius: 11px;
  background: #234b37; color: #dbe9ba; box-shadow: 0 10px 30px #0c1d2340;
}
.batch-bar.show { display: flex; }
.batch-bar .danger { color: #f0c9b4; }
.batch-bar .ghost { color: #9db5a5; }
@media (max-width: 1100px) {
  .batch-bar { max-width: calc(100vw - 220px); }
}
@media (max-width: 760px) {
  .batch-bar { max-width: calc(100vw - 24px); }
}
.group-body { font-size: 13px; }
.group-ops { display: flex; gap: 8px; margin-top: 14px; }
:deep(.row-selected td) { background: var(--row, #f3f7ee) !important; }
:deep(.table-card.row-selected) { border-color: #b8cf9e; background: var(--row, #f3f7ee); }
@media (max-width: 760px) {
  .card { padding: 13px 13px; }
}
</style>
