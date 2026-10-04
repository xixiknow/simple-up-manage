<script setup lang="ts">
import { computed, defineComponent, h, onMounted, onUnmounted, reactive, ref, watch, type VNodeChild } from 'vue'
import { UiDatePicker, UiInput, UiPopover, UiSelect, UiTag, UiTimeline, UiTimelineItem, useMessage } from '@/components/ui'
import { ArrowBackOutline, ArrowForwardOutline, ArrowLeftRightOutline, CheckOutline, ClockOutline, CloseOutline, CopyOutline, DownloadOutline, FilterOutline, RefreshOutline, RepeatOutline } from '@/components/ui/icons'
import type { DataTableColumns, SelectOption } from '@/components/ui'
import { allPages, downloadLogBody, getLogBody, getRequestLog, listConsumerKeys, listKeyOptions, listRequestLogs, listRouteGroups, listUpstreams } from '@/api/admin'
import type { CostDetail, RequestLog, RequestLogDetail, RequestLogQuery, SchedulerDecision, SchedulerCandidate, RequestAttempt } from '@/api/types'
import { EXTERNAL_PROBE_LABEL } from '@/api/types'
import { copyText, errText, formatMoney, formatNumber, formatSeconds, formatTime, formatTokenCount, formatTps } from '@/utils/format'
import { useRoute } from 'vue-router'

const LIVE_MS = 4000
const IN_FLIGHT_CAP_MS = 6 * 60 * 1000
const message = useMessage()
const route = useRoute()
const traceResultLabel: Record<string, string> = { selected: '已选中', retry: '重试', switch: '切换', failed: '失败', rejected: '无可用路由', waiting: '等待恢复', wait_finished: '恢复等待结束', recovered: '已恢复路由' }
const recoveryReasonLabel: Record<string, string> = { recovery_in_progress: '等待恢复结果', recovery_wait_timeout: '恢复等待超时', recovery_state_changed: '恢复状态已更新', first_valid_output: '首个有效输出已解除熔断', client_cancelled: '客户端已取消' }
const probeShortLabel: Record<string, string> = { rp_arithmetic: 'RP探测', example_arithmetic: '示例探测', health_manager: '健康探测' }
const failureActionLabel: Record<string, string> = { exclude_busy_resource: '忙碌资源排除', transport_failure: '传输失败', invalid_response: '响应无效', credential_disabled: '凭证已禁用', key_quota_exhausted: 'Key 配额耗尽', cooldown_key: 'Key 冷却', cooldown_key_model: 'Key·模型冷却', capability_unsupported: '能力不支持', request_scope_failure: '请求级失败', request_rejected: '请求被拒绝', probe_disabled: '探测被禁用', client_cancelled: '客户端取消', no_available_route: '无可用路由' }
const failureScopeLabel: Record<string, string> = { key: 'Key 维度', key_model: 'Key·模型维度', provider: '提供商维度', client: '客户端', route: '路由' }
const decisionReasonLabel: Record<string, string> = { initial_selection: '常规排序', reuse: '会话续用', latency_improved: '延迟更优', reliability_dropped: '可靠性下降', binding_unavailable: '会话绑定不可用', failover: '故障转移', exploration: '探索取样', recovery_validation: '恢复验证', legacy_ranking: '兼容排序', no_available_route: '无可用路由' }
const sessionSourceLabel: Record<string, string> = { 'X-Session-Id': '请求头会话', Session_id: '请求头会话', previous_response_id: '响应链', derived: '内容派生', none: '无会话' }
const skipReasonLabel: Record<string, string> = { cooldown: '冷却中', route_rate_drift: '速率漂移', not_in_route_group: '不在分组内', provider_excluded: '提供商被排除', key_model_excluded: 'Key·模型被排除', key_model_cooldown: 'Key·模型冷却', key_rpm_exceeded: 'Key RPM 超限', key_concurrency_exceeded: 'Key 并发超限', provider_concurrency_exceeded: '提供商并发超限' }
const recoveryStatusLabel: Record<string, string> = { cooldown: '冷却中', waiting_check: '等待恢复检查', checking: '恢复检查中', check_failed: '恢复检查失败', waiting_request: '等待业务验证', waiting_session: '保持当前会话', waiting_budget: '等待恢复名额', validating: '业务验证中' }

type TraceEvent = { key_id?: number; key_name: string; upstream_name: string; result: string; reason?: string; retry_count?: number; at: string; decision?: SchedulerDecision }
function parseTrace(raw?: string | null): TraceEvent[] { try { return raw ? JSON.parse(raw) as TraceEvent[] : [] } catch { return [] } }
function selectionTrace(row: RequestLogDetail) { return parseTrace(row.selection_trace) }
const traceResultIcon: Record<string, any> = { selected: CheckOutline, recovered: RefreshOutline, retry: RepeatOutline, switch: ArrowLeftRightOutline, failed: CloseOutline, rejected: CloseOutline, waiting: ClockOutline, wait_finished: ClockOutline }
function traceIcon(result: string) { return traceResultIcon[result] || ClockOutline }
function traceType(result: string) { return ['selected', 'recovered'].includes(result) ? 'success' : result === 'retry' ? 'warning' : ['waiting', 'wait_finished'].includes(result) ? 'info' : 'error' }
function traceReasonLabel(event: TraceEvent) {
  const r = event.reason || ''
  if (recoveryReasonLabel[r]) return recoveryReasonLabel[r]
  if (failureActionLabel[r]) return failureActionLabel[r]
  if (r === 'picker') return '常规排序'
  if (r === 'failover') return '故障转移'
  return r
}

// ---- 费用明细回执（cost_detail）----
function multLabel(v?: number | null) {
  if (!v || v === 1) return ''
  // 保留有效精度且不做两位四舍五入：0.045 必须显示 ×0.045 而不是 ×0.05
  return `×${parseFloat(v.toFixed(6))}`
}
function perM(pricePerToken?: number | null) {
  if (pricePerToken === null || pricePerToken === undefined || pricePerToken === 0) return '—'
  const perMValue = pricePerToken * 1e6
  return `$${formatMoney(perMValue, perMValue < 1 ? 4 : 2)}/M`
}
function costAmount(v: number) {
  return `$${formatMoney(v, 6)}`
}
const CostPanel = defineComponent({
  props: { detail: { type: Object as () => CostDetail, required: true } },
  setup(props) {
    return () => {
      const d = props.detail
      const inputBilled = d.input_uncached_tokens ?? d.input_tokens
      const items: { label: string; tokens: number; price?: number | null; amount: number; note?: string }[] = [
        { label: '输入', tokens: inputBilled, price: d.input_price, amount: d.input_cost, note: inputBilled !== d.input_tokens ? `总 ${formatNumber(d.input_tokens)}` : undefined },
        { label: '输出', tokens: d.output_tokens, price: d.output_price, amount: d.output_cost },
        { label: '缓存读', tokens: d.cache_read_tokens, price: d.cache_read_price, amount: d.cache_read_cost },
      ]
      if (d.cache_write_mode === 'breakdown') {
        let n5 = d.cache_write_5m_tokens || 0
        const n1 = d.cache_write_1h_tokens || 0
        if (!n5 && !n1) n5 = d.cache_write_tokens
        if (n5) items.push({ label: '缓存写 5m', tokens: n5, price: d.cache_write_5m_price, amount: n5 * (d.cache_write_5m_price || 0) })
        if (n1) items.push({ label: '缓存写 1h', tokens: n1, price: d.cache_write_1h_price, amount: n1 * (d.cache_write_1h_price || 0) })
      } else if (d.cache_write_tokens) {
        items.push({ label: '缓存写', tokens: d.cache_write_tokens, price: d.cache_write_5m_price, amount: d.cache_write_cost })
      }

      const head = h('div', { class: 'cost-head' }, [
        h(UiTag, { type: d.source === 'reported' ? 'success' : 'info', size: 'small', bordered: false }, { default: () => (d.source === 'reported' ? '上游自报' : '价卡估算') }),
        d.matched ? h('span', { class: 'cost-matched mono' }, d.matched) : null,
      ])

      const rows = items
        .filter((it) => it.tokens > 0 || it.amount > 0)
        .map((it) => h('div', { class: 'cost-tr' }, [
          h('span', { class: 'cost-td-label' }, [it.label, it.note ? h('span', { class: 'cost-note' }, ` ${it.note}`) : null]),
          h('span', { class: 'cost-td mono' }, formatNumber(it.tokens)),
          h('span', { class: 'cost-td mono cost-td-price' }, perM(it.price)),
          h('span', { class: 'cost-td mono cost-td-amount' }, costAmount(it.amount)),
        ]))
      const table = [
        h('div', { class: 'cost-tr cost-thead' }, [
          h('span', { class: 'cost-td-label' }, '分项'),
          h('span', { class: 'cost-td' }, 'Tokens'),
          h('span', { class: 'cost-td cost-td-price' }, '单价'),
          h('span', { class: 'cost-td cost-td-amount' }, '费用'),
        ]),
        ...rows,
      ]
      const tableWrap = h('div', { class: 'cost-table' }, table)

      const notes: ReturnType<typeof h>[] = []
      if (d.long_ctx?.applied) {
        notes.push(h('span', { class: 'cost-chip mono' }, `长上下文 ${formatNumber(d.long_ctx.total_tokens)}>${formatNumber(d.long_ctx.threshold)} 输入${multLabel(d.long_ctx.input_multiplier)} 输出${multLabel(d.long_ctx.output_multiplier)}`))
      }
      if (d.service_tier) {
        notes.push(h('span', { class: 'cost-chip mono' }, `档位 ${d.service_tier} ${multLabel(d.tier_multiplier)}`.replace(/\s+$/, '')))
      }
      if (d.time_multiplier && d.time_multiplier !== 1) {
        notes.push(h('span', { class: 'cost-chip mono' }, `峰谷 ${multLabel(d.time_multiplier)}`))
      }
      if (d.effort) {
        notes.push(h('span', { class: 'cost-chip mono' }, `力度 ${d.effort} ${multLabel(d.effort_multiplier)}`.replace(/\s+$/, '')))
      }
      const notesWrap = notes.length ? h('div', { class: 'cost-chips' }, notes) : null

      const rate = d.rate_multiplier !== 1 ? multLabel(d.rate_multiplier) : ''
      const subtotal = h('div', { class: 'cost-tr cost-subtotal' }, [
        h('span', { class: 'cost-td-label' }, '小计'),
        h('span', { class: 'cost-sub-note' }, rate ? `上游倍率 ${rate}` : ''),
        h('span', { class: 'cost-td mono cost-td-amount' }, costAmount(d.total)),
      ])
      const grand = h('div', { class: 'cost-grand' }, [
        h('span', { class: 'cost-grand-k' }, d.source === 'reported' ? '上游扣费' : '实计成本'),
        h('span', { class: 'cost-grand-v mono' }, costAmount(d.final)),
      ])

      return h('div', { class: 'cost-detail-panel' }, [head, tableWrap, notesWrap, subtotal, grand].filter(Boolean))
    }
  },
})

// 列表行的切换标志：按行缓存解析结果，4s 轮询替换行对象后重新解析
const traceFlagsCache = new WeakMap<RequestLog, { retryCount: number; switched: boolean; switchReason?: string }>()
function traceFlags(row: RequestLog) {
  let flags = traceFlagsCache.get(row)
  if (flags) return flags
  flags = { retryCount: 0, switched: false, switchReason: undefined }
  for (const e of parseTrace(row.selection_trace)) {
    if (e.result === 'retry') flags.retryCount = Math.max(flags.retryCount, e.retry_count || 1)
    else if (e.result === 'switch' || (e.result === 'selected' && e.reason === 'failover')) {
      if (!flags.switched) { flags.switched = true; flags.switchReason = e.reason }
    }
  }
  traceFlagsCache.set(row, flags)
  return flags
}

const decisionColumns: DataTableColumns<SchedulerCandidate> = [
    { title: 'Key', key: 'key_name', width: 180, ellipsis: { tooltip: true } },
    {
      title: '状态', key: 'state', width: 170,
      render(r) {
        const bits: VNodeChild[] = []
        if (r.selected) bits.push(h(UiTag, { type: 'success', size: 'small', bordered: false }, { default: () => '选中' }))
        else {
          const text = r.skip_reason ? (skipReasonLabel[r.skip_reason] || r.skip_reason) : (r.reliable ? '可靠候选' : '样本不足')
          bits.push(h('span', { class: 'cand-skip', title: r.skip_reason || undefined }, text))
        }
        if (r.recovery_status) bits.push(h(UiTag, { type: 'info', size: 'small', bordered: false, title: r.recovery_check_error || undefined }, { default: () => recoveryStatusLabel[r.recovery_status ?? ''] || r.recovery_status }))
        if (r.circuit_state && r.circuit_state !== 'closed') bits.push(h(UiTag, { type: 'warning', size: 'small', bordered: false }, { default: () => (r.circuit_state === 'open' ? '熔断中' : '半开') }))
        return h('div', { class: 'cand-status' }, bits)
      },
    },
    { title: '成功率', key: 'success_rate', width: 72, render: r => (r.samples ? `${(r.success_rate * 100).toFixed(1)}%` : '-') },
    {
      title: '首字 P50', key: 'ttft_p50', width: 96,
      render(r) {
        return h('div', { class: 'tok-cell' }, [
          h('div', { class: 'tok-line' }, r.ttft_p50 ? formatSeconds(r.ttft_p50) : '-'),
          h('div', { class: 'tok-line tok-cache' }, `${r.samples ?? 0} 样本`),
        ])
      },
    },
    { title: '在途 / 上限', key: 'key_inflight', width: 92, render: r => `${r.key_inflight} / ${r.max_concurrency || '不限'}` },
    { title: '成本', key: 'effective_cost', width: 68, render: r => r.effective_cost.toFixed(3) },
]

const attemptColumns: DataTableColumns<RequestAttempt> = [
  { title: 'Key ID', key: 'platform_key_id', width: 75 },
  { title: '结果', key: 'result', width: 140 },
  { title: 'HTTP', key: 'status_code', width: 65 },
  { title: '单次首字', key: 'ttft_ms', width: 100, render: r => r.ttft_ms ? formatSeconds(r.ttft_ms) : '-' },
  { title: '单次耗时', key: 'duration_ms', width: 100, render: r => formatSeconds(r.duration_ms) },
  { title: '响应头', key: 'headers_ms', width: 90, render: r => r.headers_ms ? formatSeconds(r.headers_ms) : '-' },
  { title: '阶段', key: 'failure_phase', width: 120, render: r => phaseLabel[r.failure_phase || ''] || r.failure_phase || '-' },
  { title: '错误', key: 'error_message', width: 220, ellipsis: { tooltip: true } },
  { title: '首字事件', key: 'ttft_event', width: 220, render: r => r.ttft_event || ttftLabel[r.ttft_status || ''] || '历史未记录' },
]
const phaseLabel: Record<string, string> = { local: '本地准备', connecting: '连接上游', dns: 'DNS 解析', tls: 'TLS 握手', sending_request: '发送请求', awaiting_headers: '等待响应头', awaiting_first_output: '等待有效输出', compacting: '上下文压缩', streaming: '传输响应' }
const ttftLabel: Record<string, string> = { measured: '已测量', pending: '等待首字', no_output: '未检测到有效输出', interrupted: '首字前中断', event_limit: '事件超出检测上限' }
const bodyStatusLabel: Record<string, string> = { complete: '原文完整', saving: '保存中', partial: '原文不完整', omitted: '二进制已省略', error: '归档失败' }
const bodyReasonLabel: Record<string, string> = { size_limit: '超过单份保存上限', queue_full: '归档缓冲已满', storage_error: '存储写入失败', storage_or_quota_error: '存储写入失败或容量不足', metadata_write_failed: '归档信息保存失败', stream_interrupted: '响应中断或提前结束', process_interrupted: '进程中断', binary_omitted: '二进制已省略', multipart_files_omitted: '上传文件仅保留元信息' }

const loading = ref(false)
const error = ref('')
const items = ref<RequestLog[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(100)
const live = ref(true)
const lastRefresh = ref('')
const upstreamOptions = ref<SelectOption[]>([])
const keyOptions = ref<SelectOption[]>([])
const consumerOptions = ref<SelectOption[]>([])
const groupOptions = ref<SelectOption[]>([])

const filters = reactive({
  external_probe_rule: null as RequestLogQuery['external_probe_rule'] | null,
  consumer_key_id: null as number | null,
  upstream_id: null as number | null,
  key_id: null as number | null,
  route_group_id: null as number | null,
  model: '',
  success: '' as '' | 'true' | 'false',
  range: null as [number, number] | null,
})

const successOptions = [
  { label: '全部', value: '' },
  { label: '成功', value: 'true' },
  { label: '失败', value: 'false' },
]

const probeOptions = [
  { label: '全部已识别外部探测', value: 'any' },
  ...Object.entries(EXTERNAL_PROBE_LABEL).map(([value, label]) => ({ value, label })),
]

const showDetail = ref(false)
const detailLoading = ref(false)
const detailError = ref('')
const detail = ref<RequestLogDetail | null>(null)
const ioTab = ref<'req_headers' | 'req_body' | 'resp_headers' | 'resp_body'>('req_headers')

const ioTabs = [
  { key: 'req_headers' as const, label: '请求头', field: 'request_headers' as const },
  { key: 'req_body' as const, label: '请求体', field: 'request_body' as const },
  { key: 'resp_headers' as const, label: '响应头', field: 'response_headers' as const },
  { key: 'resp_body' as const, label: '响应体', field: 'response_body' as const },
]

const activeIo = computed(() => ioTabs.find((t) => t.key === ioTab.value) || ioTabs[0])
const activeIoRaw = computed(() => {
  const d = detail.value
  if (!d) return ''
  if (activeBody.value && loadedBodyId.value === activeBody.value.id) return bodyPage.value
  if (activeBody.value && selectedBodyId.value && activeBody.value.id !== bodyCandidates.value.at(-1)?.id) return ''
  return (d[activeIo.value.field] as string | undefined) || ''
})

const selectedBodyId = ref('')
const bodyCandidates = computed(() => {
  const direction = ioTab.value === 'req_body' ? 'request' : ioTab.value === 'resp_body' ? 'response' : ''
  return (detail.value?.bodies || []).filter(b => b.direction === direction)
})
const activeBody = computed(() => bodyCandidates.value.find(b => b.id === selectedBodyId.value) || bodyCandidates.value.at(-1))
const bodyOptions = computed(() => bodyCandidates.value.map(b => {
  const attempt = detail.value?.attempts?.find(a => a.id === b.attempt_id)
  return { value: b.id, label: `${attempt ? `Key ${attempt.platform_key_id} · ${formatTime(attempt.started_at)}` : '请求正文'} · ${bodyStatusLabel[b.status]}` }
}))
const bodyNotice = computed(() => {
  const b = activeBody.value
  if (b) return `${bodyStatusLabel[b.status]} · 接收 ${formatNumber(b.received_bytes)} B / 保存 ${formatNumber(b.saved_bytes)} B${b.reason ? ` · ${bodyReasonLabel[b.reason] || b.reason}` : ''}`
  const truncated = ioTab.value === 'req_body' ? detail.value?.request_body_truncated : ioTab.value === 'resp_body' ? detail.value?.response_body_truncated : false
  return truncated ? '历史正文已截断或省略，未保存完整原文' : ''
})
const bodyPage = ref('')
const loadedBodyId = ref('')
const bodyLoading = ref(false)
const bodyDownloadLoading = ref(false)
const bodyError = ref('')
const bodyOffsets = ref<number[]>([0])
const bodyNext = ref(0)
const bodyEof = ref(true)
let bodySequence = 0
async function loadBody(offset = 0) {
  const b = activeBody.value
  const id = detail.value?.id
  if (!b || !id || b.status === 'saving' || b.status === 'error') return
  const seq = ++bodySequence
  bodyLoading.value = true
  bodyError.value = ''
  try {
    const result = await getLogBody(id, b.id, offset)
    if (seq !== bodySequence) return
    bodyPage.value = result.text
    loadedBodyId.value = b.id
    bodyNext.value = result.next_offset
    bodyEof.value = result.eof
  } catch (e) { if (seq === bodySequence) bodyError.value = errText(e) }
  finally { if (seq === bodySequence) bodyLoading.value = false }
}
function nextBodyPage() { bodyOffsets.value.push(bodyNext.value); void loadBody(bodyNext.value) }
function previousBodyPage() { bodyOffsets.value.pop(); void loadBody(bodyOffsets.value.at(-1) || 0) }
async function downloadBody() {
  const b = activeBody.value
  if (!b || !detail.value) return
  bodyDownloadLoading.value = true
  try { await downloadLogBody(detail.value.id, b.id) } catch (e) { message.error(errText(e)) }
  finally { bodyDownloadLoading.value = false }
}
watch(() => [activeBody.value?.id, activeBody.value?.status, showDetail.value], () => {
  ++bodySequence
  bodyPage.value = ''; loadedBodyId.value = ''; bodyOffsets.value = [0]; bodyNext.value = 0; bodyEof.value = true; bodyError.value = ''; bodyLoading.value = false
  if (showDetail.value) void loadBody()
})

let timer: number | undefined
const clock = ref(Date.now())
let clockTimer: number | undefined

function rowElapsedMs(row: RequestLog) {
  if (row.in_flight) {
    const start = new Date(row.created_at).getTime()
    if (!Number.isNaN(start)) {
      return Math.min(IN_FLIGHT_CAP_MS, Math.max(row.duration_ms || 0, clock.value - start))
    }
  }
  return row.duration_ms
}

function startClock() {
  if (clockTimer != null) return
  clock.value = Date.now()
  clockTimer = window.setInterval(() => {
    clock.value = Date.now()
  }, 250)
}

function stopClock() {
  if (clockTimer != null) {
    window.clearInterval(clockTimer)
    clockTimer = undefined
  }
}

async function loadOptions() {
  const [up, keys, consumers, groups] = await Promise.all([allPages(listUpstreams), allPages(listKeyOptions), allPages(listConsumerKeys), listRouteGroups()])
  consumerOptions.value = consumers.map((k) => ({ label: `${k.name} (${k.key_preview})`, value: k.id }))
  upstreamOptions.value = up.map((u) => ({ label: u.name, value: u.id }))
  keyOptions.value = keys.map((k) => ({ label: `${k.name} (${k.key_preview})`, value: k.id }))
  groupOptions.value = groups.map((g) => ({ label: g.name, value: g.id }))
}

let snapshotId: number | undefined
let snapshotAt: string | undefined
let disposed = false
let loadSequence = 0
let listPending = false
let detailSequence = 0
let detailPending = false
let activeDetailId: number | null = null

function queryFromFilters(): RequestLogQuery {
  return {
    external_probe_rule: filters.external_probe_rule || undefined,
    upstream_id: filters.upstream_id || undefined,
    key_id: filters.key_id || undefined,
    consumer_key_id: filters.consumer_key_id || undefined,
    route_group_id: filters.route_group_id || undefined,
    model: filters.model.trim() || undefined,
    success: filters.success === '' ? undefined : filters.success === 'true',
    from: filters.range ? new Date(filters.range[0]).toISOString() : undefined,
    to: filters.range ? new Date(filters.range[1]).toISOString() : undefined,
  }
}

async function load(opts?: { silent?: boolean }) {
  if (disposed) return
  if (opts?.silent && listPending) return
  const sequence = ++loadSequence
  listPending = true
  if (!opts?.silent) loading.value = true
  const requestedPage = page.value
  try {
    const res = await listRequestLogs({
      ...queryFromFilters(), page: requestedPage, page_size: pageSize.value,
      snapshot_id: requestedPage === 1 && live.value ? undefined : snapshotId,
      snapshot_at: requestedPage === 1 && live.value ? undefined : snapshotAt,
    })
    if (sequence !== loadSequence) return
    snapshotId = res.snapshot_id
    snapshotAt = res.snapshot_at
    const lastPage = Math.max(1, Math.ceil(res.total / pageSize.value))
    if (requestedPage > lastPage) {
      page.value = lastPage
      void load()
      return
    }
    items.value = res.items
    total.value = res.total
    error.value = ''
    lastRefresh.value = formatTime(new Date().toISOString())
  } catch (e) {
    if (sequence === loadSequence) error.value = errText(e)
  } finally {
    if (sequence === loadSequence) {
      listPending = false
      loading.value = false
    }
  }
}

function refresh() {
  page.value = 1
  snapshotId = undefined
  snapshotAt = undefined
  void load()
}

// 表头 / 抽屉筛选均为「改动即查」；模型文本输入做防抖
function applyFilters() {
  refresh()
}

let modelTimer: number | undefined
function scheduleApply() {
  if (modelTimer != null) window.clearTimeout(modelTimer)
  modelTimer = window.setTimeout(() => {
    modelTimer = undefined
    applyFilters()
  }, 400)
}

function reset() {
  filters.external_probe_rule = null
  filters.upstream_id = null
  filters.key_id = null
  filters.consumer_key_id = null
  filters.model = ''
  filters.success = ''
  filters.range = null
  filters.route_group_id = null
  if (modelTimer != null) {
    window.clearTimeout(modelTimer)
    modelTimer = undefined
  }
  applyFilters()
}

// 手机端筛选收进抽屉：角标显示已生效条件数
const filterOpen = ref(false)
const activeFilterCount = computed(() => {
  const f = filters
  return [f.upstream_id, f.key_id, f.consumer_key_id, f.route_group_id, f.model, f.external_probe_rule]
    .filter(v => v != null && v !== '').length
    + (f.success !== '' ? 1 : 0)
    + (f.range?.length ? 1 : 0)
})

function startLive() {
  if (disposed) return
  stopLive()
  timer = window.setInterval(() => {
    if (live.value) void load({ silent: true })
    if (showDetail.value && (detail.value?.in_flight || detail.value?.bodies?.some(b => b.status === 'saving'))) void refreshDetail(true)
  }, LIVE_MS)
}

function stopLive() {
  if (timer != null) window.clearInterval(timer)
  timer = undefined
}

async function refreshDetail(silent = false) {
  if (activeDetailId == null || (silent && detailPending)) return
  const id = activeDetailId
  const sequence = ++detailSequence
  detailPending = true
  if (!silent) detailLoading.value = true
  try {
    const result = await getRequestLog(id)
    if (sequence !== detailSequence || !showDetail.value || activeDetailId !== id) return
    detail.value = result
    detailError.value = ''
  } catch (e) {
    if (sequence === detailSequence) detailError.value = errText(e)
  } finally {
    if (sequence === detailSequence) {
      detailPending = false
      detailLoading.value = false
    }
  }
}

function openDetail(row: RequestLog) {
  selectedBodyId.value = ''
  showDetail.value = true
  activeDetailId = row.id
  ioTab.value = 'req_headers'
  detailError.value = ''
  detail.value = null
  void refreshDetail()
}

function streamLabel(row: RequestLog) {
  return row.stream_known ? (row.stream ? '流式' : '同步') : '未知'
}

const detailTrace = computed(() => (detail.value ? selectionTrace(detail.value) : []))
const lastDecision = computed(() => detailTrace.value.filter(e => e.decision).at(-1)?.decision ?? null)

function pretty(raw?: string | null) {
  const text = (raw || '').trim()
  if (!text) return ''
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return raw || ''
  }
}

function ioHasContent(field: (typeof ioTabs)[number]['field']) {
  return Boolean((detail.value?.[field] || '').trim())
}

async function copySection(label: string, raw?: string | null) {
  const text = pretty(raw) || raw || ''
  if (!text) {
    message.warning(`${label}为空`)
    return
  }
  const ok = await copyText(text)
  if (ok) message.success(`已复制${label}`)
  else message.error('复制失败')
}

// 表头筛选：标题 + 筛选图标按钮（激活高亮），弹层内容即查即生效
function headerFilter(label: string, active: boolean, panel: () => VNodeChild) {
  return () => h('span', { class: 'th-inner' }, [
    label,
    h(UiPopover, { placement: 'bottom-end' }, {
      trigger: () => h('button', { type: 'button', class: ['th-filter', { on: active }], 'aria-label': `筛选${label}`, title: `筛选${label}` }, h(FilterOutline)),
      default: panel,
    }),
  ])
}

const columns = computed<DataTableColumns<RequestLog>>(() => {
  clock.value
  return [
  {
    title: headerFilter('时间', !!filters.range, () => h('div', { class: 'th-filter-panel' }, [
      h(UiDatePicker, {
        value: filters.range, type: 'datetimerange', clearable: true, startPlaceholder: '从', endPlaceholder: '到',
        'onUpdate:value': (v: [number, number] | null) => { filters.range = v; applyFilters() },
      }),
    ])),
    key: 'created_at',
    width: 160,
    render(row) {
      return formatTime(row.created_at)
    },
  },
  {
    title: '来源 IP',
    key: 'client_ip',
    width: 140,
    mobileHide: true,
    ellipsis: { tooltip: true },
    render(row) {
      return h('span', { class: 'preview' }, row.client_ip || '—')
    },
  },
  {
    title: headerFilter('API 密钥', filters.consumer_key_id != null, () => h('div', { class: 'th-filter-panel' }, [
      h(UiSelect, {
        value: filters.consumer_key_id, options: consumerOptions.value, clearable: true, filterable: true, size: 'small', placeholder: '全部', style: { width: '200px' },
        'onUpdate:value': (v: number | null) => { filters.consumer_key_id = v; applyFilters() },
      }),
    ])),
    key: 'consumer',
    width: 120,
    ellipsis: { tooltip: true },
    render(row) {
      return row.consumer_name || (row.consumer_key_id != null ? `#${row.consumer_key_id}` : '—')
    },
  },
  {
    title: headerFilter('提供商', filters.upstream_id != null || filters.key_id != null, () => h('div', { class: 'th-filter-panel' }, [
      h('label', { class: 'th-filter-field' }, [
        '提供商',
        h(UiSelect, {
          value: filters.upstream_id, options: upstreamOptions.value, clearable: true, filterable: true, size: 'small', placeholder: '全部', style: { width: '200px' },
          'onUpdate:value': (v: number | null) => { filters.upstream_id = v; applyFilters() },
        }),
      ]),
      h('label', { class: 'th-filter-field' }, [
        '平台 Key',
        h(UiSelect, {
          value: filters.key_id, options: keyOptions.value, clearable: true, filterable: true, size: 'small', placeholder: '全部', style: { width: '200px' },
          'onUpdate:value': (v: number | null) => { filters.key_id = v; applyFilters() },
        }),
      ]),
    ])),
    key: 'upstream_name',
    width: 120,
    ellipsis: { tooltip: true },
    render(row) {
      return row.upstream_name || (row.upstream_id != null ? `#${row.upstream_id}` : '—')
    },
  },
  {
    title: headerFilter('分组', filters.route_group_id != null, () => h('div', { class: 'th-filter-panel' }, [
      h(UiSelect, {
        value: filters.route_group_id, options: groupOptions.value, clearable: true, filterable: true, size: 'small', placeholder: '全部', style: { width: '200px' },
        'onUpdate:value': (v: number | null) => { filters.route_group_id = v; applyFilters() },
      }),
    ])),
    key: 'route_group_name',
    width: 118,
    mobileHide: true,
    ellipsis: { tooltip: true },
    render(row) {
      const name = row.route_group_name || (row.route_group_id != null ? `#${row.route_group_id}` : '—')
      const flags = traceFlags(row)
      const kids: VNodeChild[] = []
      if (flags.retryCount) kids.push(h(RepeatOutline, { class: 'switch-flag flag-retry', title: `自身重试 ${flags.retryCount} 次` }))
      if (flags.switched) {
        const reason = flags.switchReason ? (failureActionLabel[flags.switchReason] || flags.switchReason) : ''
        kids.push(h(ArrowLeftRightOutline, { class: 'switch-flag flag-switch', title: `发生 Key/提供商切换${reason ? ` · ${reason}` : ''}` }))
      }
      kids.push(h('span', { class: 'group-name' }, name))
      return h('span', { class: 'group-cell' }, kids)
    },
  },
  {
    title: headerFilter('模型', !!filters.model.trim(), () => h('div', { class: 'th-filter-panel' }, [
      h(UiInput, {
        value: filters.model, clearable: true, size: 'small', placeholder: '模型名称', style: { width: '200px' },
        'onUpdate:value': (v: string) => { filters.model = v; scheduleApply() },
      }),
    ])),
    key: 'model',
    width: 140,
    ellipsis: { tooltip: true },
    mobileTitle: true,
  },
  { title: '协议', key: 'protocol', width: 90, mobileHide: true },
  {
    title: headerFilter('类型', !!filters.external_probe_rule, () => h('div', { class: 'th-filter-panel' }, [
      h('label', { class: 'th-filter-field' }, [
        '外部探测',
        h(UiSelect, {
          value: filters.external_probe_rule, options: probeOptions, clearable: true, size: 'small', placeholder: '全部', style: { width: '200px' },
          'onUpdate:value': (v: RequestLogQuery['external_probe_rule']) => { filters.external_probe_rule = v ?? null; applyFilters() },
        }),
      ]),
    ])),
    key: 'stream',
    width: 136,
    mobileHide: true,
    render(row) {
      const kids: VNodeChild[] = [
        h(UiTag, { size: 'small', bordered: false, type: row.stream_known && row.stream ? 'info' : 'default' }, { default: () => streamLabel(row) }),
      ]
      if (row.external_probe_rule) {
        kids.push(h(UiTag, {
          size: 'small', bordered: false, type: 'warning',
          title: EXTERNAL_PROBE_LABEL[row.external_probe_rule] || row.external_probe_rule,
        }, { default: () => probeShortLabel[row.external_probe_rule!] || row.external_probe_rule }))
      }
      return h('div', { class: 'kind-cell' }, kids)
    },
  },
  {
    title: 'Tokens / Cache',
    key: 'tokens',
    width: 148,
    render(row) {
      return h(
        'div',
        {
          class: 'tok-cell',
          title: `${formatNumber(row.input_tokens)} / ${formatNumber(row.output_tokens)}\n读 ${formatNumber(row.cache_read_tokens)} / 写 ${formatNumber(row.cache_creation_tokens)}`,
        },
        [
          h('div', { class: 'tok-line' }, `${formatTokenCount(row.input_tokens)} / ${formatTokenCount(row.output_tokens)}`),
          h(
            'div',
            { class: 'tok-line tok-cache' },
            `读 ${formatTokenCount(row.cache_read_tokens)} / 写 ${formatTokenCount(row.cache_creation_tokens)}`,
          ),
        ],
      )
    },
  },
  {
    title: 'TTFT / 耗时',
    key: 'timing',
    width: 132,
    render(row) {
      const dur = rowElapsedMs(row)
      const ttft = row.ttft_ms > 0 ? formatSeconds(row.ttft_ms) : '—'
      return h('div', { class: ['tok-cell', { 'timing-live': row.in_flight }], title: `TTFT ${ttft} · 总耗时 ${formatSeconds(dur)}` }, [
        h('div', { class: 'tok-line' }, `${ttft} / ${formatSeconds(dur)}`),
        h('div', { class: 'tok-line tok-cache' }, formatTps(row.output_tokens, dur, row.ttft_ms)),
      ])
    },
  },
  {
    title: '费用',
    key: 'cost_usd',
    width: 100,
    render(row) {
      const text = formatMoney(row.cost_usd, 6)
      if (!row.cost_detail) return text
      return h(UiPopover, { placement: 'bottom-end' }, {
        trigger: () => h('span', { class: 'cost-cell' }, text),
        default: () => h(CostPanel, { detail: row.cost_detail! }),
      })
    },
  },
  {
    title: headerFilter('成功', filters.success !== '', () => h('div', { class: 'th-filter-panel' }, [
      h(UiSelect, {
        value: filters.success, options: successOptions, size: 'small', placeholder: '全部', style: { width: '200px' },
        'onUpdate:value': (v: '' | 'true' | 'false') => { filters.success = v; applyFilters() },
      }),
    ])),
    key: 'success',
    width: 80,
    mobileTag: true,
    render(row) {
      if (row.in_flight) {
        return h(UiTag, { type: 'warning', size: 'small', bordered: false }, { default: () => '进行中' })
      }
      return h(
        UiTag,
        { type: row.success ? 'success' : 'error', size: 'small', bordered: false },
        { default: () => (row.success ? '成功' : '失败') },
      )
    },
  },
]
})

function rowProps(row: RequestLog) {
  return {
    style: 'cursor: pointer',
    onClick: () => void openDetail(row),
  }
}

function rowKey(row: RequestLog) {
  return row.id
}

function rowClassName(row: RequestLog) {
  return showDetail.value && detail.value?.id === row.id ? 'log-row-active' : ''
}

watch([items, detail, showDetail], () => {
  if (items.value.some((r) => r.in_flight) || (showDetail.value && detail.value?.in_flight)) startClock()
  else stopClock()
})

watch(live, (on) => {
  if (on) void load({ silent: true })
})

watch(showDetail, (show) => {
  if (!show) {
    activeDetailId = null
    detailSequence++
    detailPending = false
  }
})

onMounted(async () => {
  const q = route.query
  if (q.route_group_id) filters.route_group_id = Number(q.route_group_id) || null
  if (q.upstream_id) filters.upstream_id = Number(q.upstream_id) || null
  if (q.from && q.to) {
    const from = Date.parse(String(q.from))
    const to = Date.parse(String(q.to))
    if (!Number.isNaN(from) && !Number.isNaN(to)) filters.range = [from, to]
  }
  try {
    await loadOptions()
  } catch {
    /* filters remain empty if options fail */
  }
  if (q.route_group_id || q.from || q.upstream_id) applyFilters()
  else await load()
  startLive()
})

onUnmounted(() => {
  disposed = true
  loadSequence++
  detailSequence++
  stopLive()
  stopClock()
  if (modelTimer != null) window.clearTimeout(modelTimer)
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <span class="eyebrow">记录 / LOGS</span>
        <h2>请求记录</h2>
        <p>点击行查看请求头 / 请求体 / 响应；表头漏斗可筛选；记录保留 24 小时后自动清理</p>
      </div>
      <div class="live-ctl">
        <ui-button size="small" quaternary class="filter-toggle" @click="filterOpen = true">
          <template #icon><ui-icon><FilterOutline /></ui-icon></template>
          筛选<span v-if="activeFilterCount"> · {{ activeFilterCount }}</span>
        </ui-button>
        <ui-button size="small" quaternary @click="reset">重置</ui-button>
        <ui-button size="small" quaternary @click="refresh">刷新</ui-button>
        <ui-switch v-model:value="live" size="small" />
        <span>实时刷新</span>
        <span v-if="lastRefresh" class="muted">更新于 {{ lastRefresh }}</span>
      </div>
    </div>

    <ui-card size="small" :bordered="false">
      <ui-alert v-if="error" type="error" :title="error" style="margin-bottom: 10px" />
      <ui-data-table
        remote
        size="small"
        card
        :columns="columns"
        :data="items"
        :loading="loading"
        :scroll-x="1340"
        :row-key="rowKey"
        :row-props="rowProps"
        :row-class-name="rowClassName"
        :pagination="{
          page,
          pageSize,
          itemCount: total,
          showSizePicker: true,
          pageSizes: [20, 50, 100],
          onChange: (p: number) => {
            page = p
            load()
          },
          onUpdatePageSize: (s: number) => {
            pageSize = s
            refresh()
          },
        }"
      />
    </ui-card>

    <ui-drawer v-model:show="filterOpen" width="min(360px, 92vw)" placement="right">
      <ui-drawer-content title="筛选请求记录" closable>
        <div class="filter-stack">
          <label class="filter-field"><span>提供商</span><ui-select v-model:value="filters.upstream_id" :options="upstreamOptions" clearable placeholder="全部" @update:value="applyFilters" /></label>
          <label class="filter-field"><span>Key</span><ui-select v-model:value="filters.key_id" :options="keyOptions" clearable filterable placeholder="全部" @update:value="applyFilters" /></label>
          <label class="filter-field"><span>API 密钥</span><ui-select v-model:value="filters.consumer_key_id" :options="consumerOptions" clearable filterable placeholder="全部" @update:value="applyFilters" /></label>
          <label class="filter-field"><span>分组</span><ui-select v-model:value="filters.route_group_id" :options="groupOptions" clearable filterable placeholder="全部" @update:value="applyFilters" /></label>
          <label class="filter-field"><span>模型</span><ui-input v-model:value="filters.model" clearable placeholder="模型名称" @update:value="scheduleApply" /></label>
          <label class="filter-field"><span>成败</span><ui-select v-model:value="filters.success" :options="successOptions" placeholder="全部" @update:value="applyFilters" /></label>
          <label class="filter-field"><span>外部探测</span><ui-select v-model:value="filters.external_probe_rule" :options="probeOptions" clearable placeholder="全部" @update:value="applyFilters" /></label>
          <div class="filter-field"><span>时间范围</span><ui-date-picker v-model:value="filters.range" type="datetimerange" clearable start-placeholder="从" end-placeholder="到" @update:value="applyFilters" /></div>
        </div>
        <template #footer>
          <div class="filter-foot">
            <ui-button size="small" @click="reset">重置</ui-button>
            <ui-button type="primary" size="small" @click="filterOpen = false">完成</ui-button>
          </div>
        </template>
      </ui-drawer-content>
    </ui-drawer>

    <ui-drawer v-model:show="showDetail" width="min(640px, 100vw)" placement="right">
      <ui-drawer-content title="请求明细" closable :native-scrollbar="false">
        <ui-spin :show="detailLoading">
          <ui-alert v-if="detailError" type="error" :title="detailError" style="margin-bottom: 10px" />
          <template v-if="detail">
            <div class="meta-grid">
              <div><span class="meta-k">时间</span>{{ formatTime(detail.created_at) }}</div>
              <div>
                <span class="meta-k">Request ID</span><span class="mono">{{ detail.request_id || '—' }}</span>
              </div>
              <div>
                <span class="meta-k">来源 IP</span><span class="mono">{{ detail.client_ip || '—' }}</span>
              </div>
              <div>
                <span class="meta-k">API 密钥</span
                >{{ detail.consumer_name || (detail.consumer_key_id != null ? `#${detail.consumer_key_id}` : '—') }}
              </div>
              <div>
                <span class="meta-k">提供商</span
                >{{ detail.upstream_name || (detail.upstream_id != null ? `#${detail.upstream_id}` : '—') }}
              </div>
              <div><span class="meta-k">模型</span>{{ detail.model || '—' }}</div>
              <div><span class="meta-k">外部探测</span>{{ detail.external_probe_rule ? EXTERNAL_PROBE_LABEL[detail.external_probe_rule] || detail.external_probe_rule : '未标记' }}</div>
              <div>
                <span class="meta-k">类型</span>{{ streamLabel(detail) }}
              </div>
              <div>
                <span class="meta-k">路径</span><span class="mono">{{ detail.path || '—' }}</span>
              </div>
              <div>
                <span class="meta-k">状态</span>
                <template v-if="detail.in_flight">进行中</template>
                <template v-else>{{ detail.status_code || '—' }} · {{ detail.success ? '成功' : '失败' }}</template>
              </div>
              <div>
                <span class="meta-k">耗时</span>{{ detail.ttft_ms > 0 ? formatSeconds(detail.ttft_ms) : '—' }} /
                {{ formatSeconds(rowElapsedMs(detail)) }}
                <span class="muted"> · {{ formatTps(detail.output_tokens, rowElapsedMs(detail), detail.ttft_ms) }}</span>
              </div>
              <div class="ttft-meta"><span class="meta-k">首字检测</span>{{ ttftLabel[detail.ttft_status || ''] || '历史未记录' }}<span v-if="detail.ttft_event" class="mono"> · {{ detail.ttft_event }}</span></div>
              <div>
                <span class="meta-k">Tokens</span>{{ formatNumber(detail.input_tokens) }} /
                {{ formatNumber(detail.output_tokens) }}
              </div>
              <div>
                <span class="meta-k">Cache</span>读 {{ formatNumber(detail.cache_read_tokens) }} / 写
                {{ formatNumber(detail.cache_creation_tokens) }}
              </div>
              <div><span class="meta-k">费用</span>{{ formatMoney(detail.cost_usd, 6) }}</div>
              <CostPanel v-if="detail.cost_detail" :detail="detail.cost_detail" class="drawer-cost-panel" />
              <div v-if="detail.failure_scope">
                <span class="meta-k">故障范围</span>{{ failureScopeLabel[detail.failure_scope] || detail.failure_scope }}
              </div>
              <div v-if="detail.failure_action">
                <span class="meta-k">调度动作</span>{{ failureActionLabel[detail.failure_action] || detail.failure_action }}
              </div>
            </div>
            <section v-if="detailTrace.length" class="selection-trace">
              <div class="trace-head">
                <h3>Key 选择过程</h3>
                <div v-if="lastDecision" class="decision-chips">
                  <span class="decision-chip" :title="`决策原因 ${lastDecision.reason}`">{{ decisionReasonLabel[lastDecision.reason] || lastDecision.reason }}</span>
                  <span class="decision-chip">会话 · {{ sessionSourceLabel[lastDecision.session_source || 'none'] || lastDecision.session_source || '无' }}</span>
                  <span v-if="lastDecision.exploration" class="decision-chip chip-accent">探索取样</span>
                  <span v-if="lastDecision.degraded" class="decision-chip chip-warn">降级调度</span>
                  <span v-if="lastDecision.previous_key_id && lastDecision.previous_key_id !== lastDecision.selected_key_id" class="decision-chip chip-swap">
                    <ArrowLeftRightOutline class="chip-icon" />原Key #{{ lastDecision.previous_key_id }} → Key #{{ lastDecision.selected_key_id }}
                  </span>
                </div>
              </div>
              <ui-timeline>
                <ui-timeline-item v-for="(event, index) in detailTrace" :key="`${event.at}-${index}`" :type="traceType(event.result)" :title="event.key_name || traceResultLabel[event.result] || event.result" :time="formatTime(event.at)">
                  <div class="trace-line">
                    <component :is="traceIcon(event.result)" class="trace-icon" />
                    <span class="trace-result">{{ traceResultLabel[event.result] || event.result }}</span>
                    <span v-if="event.upstream_name" class="trace-upstream">{{ event.upstream_name }}</span>
                    <span v-if="event.retry_count && event.retry_count > 1" class="trace-retry"><RepeatOutline class="chip-icon" />× {{ event.retry_count }}</span>
                  </div>
                  <div v-if="event.reason" class="trace-reason">{{ traceReasonLabel(event) }}</div>
                  <details v-if="event.decision" class="decision-details">
                    <summary>候选依据 · {{ event.decision.candidates.length }} 个候选<span v-if="event.decision.reason" class="muted"> · {{ decisionReasonLabel[event.decision.reason] || event.decision.reason }}</span></summary>
                    <ui-data-table size="small" :columns="decisionColumns" :data="event.decision.candidates" :scroll-x="680" :row-class-name="(r: SchedulerCandidate) => (r.selected ? 'cand-selected' : '')" />
                  </details>
                </ui-timeline-item>
              </ui-timeline>
            </section>
            <section v-if="detail.attempts?.length" class="selection-trace">
              <h3>上游尝试</h3>
              <ui-data-table size="small" :columns="attemptColumns" :data="detail.attempts" :scroll-x="1130" />
              <details v-for="attempt in detail.attempts.filter(a => a.event_summary)" :key="attempt.id" class="decision-details">
                <summary>Key {{ attempt.platform_key_id }} · 事件摘要 · 接收 {{ formatNumber(attempt.received_bytes || 0) }} B</summary>
                <pre class="log-pre">{{ pretty(attempt.event_summary || '') }}</pre>
              </details>
            </section>
            <ui-alert v-if="detail.error_message" type="error" :title="detail.error_message" style="margin: 10px 0" />
            <ui-alert v-if="detail.error_message === 'stale in-flight request'" type="warning" title="请求异常中断，耗时为最后记录值" style="margin: 10px 0" />

            <div class="io-tabs">
              <button
                v-for="tab in ioTabs"
                :key="tab.key"
                type="button"
                class="io-tab"
                :class="{ active: ioTab === tab.key }"
                @click="ioTab = tab.key"
              >
                <span class="io-tab-label">{{ tab.label }}</span>
                <span class="io-tab-hint">{{ ioHasContent(tab.field) ? '有内容' : '空' }}</span>
              </button>
            </div>
            <ui-card size="small" :bordered="true" class="io-pane">
              <ui-select v-if="bodyCandidates.length > 1" v-model:value="selectedBodyId" :options="bodyOptions" :placeholder="bodyOptions.at(-1)?.label" size="small" />
              <p v-if="bodyNotice" class="muted body-notice">{{ bodyNotice }}</p>
              <ui-alert v-if="bodyError" type="error" :title="bodyError" />
              <div class="block-head">
                <h3>{{ activeIo.label }}</h3>
                <div class="body-tools">
                  <ui-button v-if="activeBody" size="tiny" quaternary title="重新读取" aria-label="重新读取" :loading="bodyLoading" @click="refreshDetail().then(() => loadBody(bodyOffsets.at(-1) || 0))"><ui-icon :component="RefreshOutline" /></ui-button>
                  <ui-button size="tiny" quaternary title="复制当前内容" aria-label="复制当前内容" @click="copySection(activeIo.label, activeIoRaw)"><ui-icon :component="CopyOutline" /></ui-button>
                  <ui-button v-if="activeBody" size="tiny" quaternary title="下载已保存正文" aria-label="下载已保存正文" :loading="bodyDownloadLoading" :disabled="activeBody.status === 'saving' || activeBody.status === 'error'" @click="downloadBody"><ui-icon :component="DownloadOutline" /></ui-button>
                </div>
              </div>
              <ui-spin :show="bodyLoading"><pre class="log-pre">{{ pretty(activeIoRaw) || '—' }}</pre></ui-spin>
              <div v-if="activeBody && loadedBodyId" class="body-pagination">
                <ui-button size="tiny" quaternary title="上一段" aria-label="上一段" :disabled="bodyLoading || bodyOffsets.length < 2" @click="previousBodyPage"><ui-icon :component="ArrowBackOutline" /></ui-button>
                <span>第 {{ bodyOffsets.length }} 段</span>
                <ui-button size="tiny" quaternary title="下一段" aria-label="下一段" :disabled="bodyLoading || bodyEof" @click="nextBodyPage"><ui-icon :component="ArrowForwardOutline" /></ui-button>
              </div>
            </ui-card>
          </template>
        </ui-spin>
      </ui-drawer-content>
    </ui-drawer>
  </div>
</template>

<style scoped>
.body-tools, .body-pagination { display: flex; align-items: center; gap: 8px; }
.body-pagination { justify-content: flex-end; margin-top: 8px; }
.body-notice { font-size: 12px; overflow-wrap: anywhere; }
.ttft-meta { grid-column: 1 / -1; }
.decision-details { margin-top: 8px; max-width: 100%; }
.decision-details summary { cursor: pointer; margin-bottom: 8px; }
.live-ctl {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: #546c58;
}
.meta-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px 16px;
  font-size: 13px;
  margin-bottom: 8px;
}
.meta-k {
  display: inline-block;
  width: 88px;
  color: #819087;
}
.meta-grid > div { min-width: 0; overflow-wrap: anywhere; }
@media (max-width: 760px) { .meta-grid { grid-template-columns: minmax(0, 1fr); } }
.block-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: 0 0 8px;
}
.block-head h3 {
  margin: 0;
  font-size: 13px;
  font-weight: 650;
}
.log-pre {
  margin: 0;
  max-height: calc(100vh - 420px);
  min-height: 220px;
  overflow: auto;
  padding: 10px 12px;
  border-radius: 8px;
  background: #edf1e6;
  color: #425b35;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: 12px;
  line-height: 1.45;
  white-space: pre-wrap;
  word-break: break-word;
}
.tok-cell {
  line-height: 1.35;
}
.tok-line {
  font-variant-numeric: tabular-nums;
}
.tok-cache {
  color: #819087;
  font-size: 12px;
}
/* 表头筛选（单元格由 UiDataTable 渲染，需经 :deep 穿透） */
.page :deep(.th-inner) { display: inline-flex; align-items: center; gap: 3px; }
.page :deep(.th-filter) {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 19px;
  height: 19px;
  border-radius: 5px;
  color: #a8b8ac;
}
.page :deep(.th-filter:hover) { color: #425b35; background: #eef3e7; }
.page :deep(.th-filter.on) { color: #2f6a4f; background: #e7eedd; }
.page :deep(.th-filter svg) { width: 12px; height: 12px; }
.page :deep(.th-filter-panel) { display: flex; flex-direction: column; gap: 8px; padding: 4px 2px; min-width: 200px; }
.page :deep(.th-filter-panel .ui-date-picker) { flex-direction: column; align-items: stretch; }
.page :deep(.th-filter-field) { display: flex; flex-direction: column; gap: 4px; font-size: 11px; color: #819087; }
/* 分组列切换标志 */
.page :deep(.group-cell) { display: inline-flex; align-items: center; gap: 4px; min-width: 0; max-width: 100%; }
.page :deep(.group-name) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.page :deep(.switch-flag) { width: 13px; height: 13px; flex: none; }
.page :deep(.flag-retry) { color: #b0803c; }
.page :deep(.flag-switch) { color: #5c7f5c; }
/* 类型列标签组 */
.page :deep(.kind-cell) { display: inline-flex; align-items: center; gap: 4px; flex-wrap: wrap; }
/* Tokens / TTFT 单元格 */
.page :deep(.tok-cell) { line-height: 1.35; }
.page :deep(.tok-line) { font-variant-numeric: tabular-nums; }
.page :deep(.tok-cache) { color: #819087; font-size: 12px; }
/* 进行中请求的耗时呼吸 */
.page :deep(.timing-live) { animation: timing-breathe 1.6s ease-in-out infinite; }
@keyframes timing-breathe { 0%, 100% { opacity: 1; } 50% { opacity: 0.4; } }
@media (prefers-reduced-motion: reduce) { .page :deep(.timing-live) { animation: none; } }
/* 明细：Key 选择过程 */
.trace-head { display: flex; flex-direction: column; gap: 8px; margin-bottom: 10px; }
.selection-trace h3 { margin: 0; font-size: 13px; font-weight: 650; }
.decision-chips { display: flex; flex-wrap: wrap; gap: 6px; }
.decision-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 9px;
  border-radius: 999px;
  background: #eef3e7;
  color: #425b35;
  font-size: 11px;
}
.chip-icon { width: 12px; height: 12px; }
.chip-accent { background: #e4ecf4; color: #2f5d7c; }
.chip-warn { background: #f7ecd7; color: #8a5a1e; }
.chip-swap { background: #f3f7ee; color: #4d7f68; }
.trace-line { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: 12px; }
.trace-icon { width: 13px; height: 13px; color: #6b8070; flex: none; }
.trace-result { font-weight: 650; color: #3c543e; }
.trace-upstream { color: #819087; }
.trace-retry { display: inline-flex; align-items: center; gap: 3px; color: #b0803c; font-size: 11px; font-variant-numeric: tabular-nums; }
.trace-reason { margin-top: 2px; font-size: 11px; color: #819087; overflow-wrap: anywhere; }
.decision-details :deep(.cand-status) { display: flex; align-items: center; gap: 5px; flex-wrap: wrap; }
.decision-details :deep(.cand-skip) { color: #819087; font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 110px; }
.decision-details :deep(.cand-selected td) { background: #f3f7ee !important; }
.decision-details :deep(.tok-cell) { line-height: 1.35; }
.decision-details :deep(.tok-line) { font-variant-numeric: tabular-nums; }
.decision-details :deep(.tok-cache) { color: #819087; font-size: 12px; }
.io-tabs {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
  margin: 12px 0 10px;
}
.io-tab {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  padding: 10px 12px;
  border: 1px solid #e4e7ec;
  border-radius: 8px;
  background: #fbfcfd;
  cursor: pointer;
  text-align: left;
  color: #3c543e;
}
.io-tab:hover {
  border-color: #b7c4ae;
}
.io-tab.active {
  border-color: #174b3d;
  background: #f3f7ee;
  box-shadow: inset 0 0 0 1px #174b3d;
}
.io-tab-label {
  font-size: 13px;
  font-weight: 650;
}
.io-tab-hint {
  font-size: 11px;
  color: #819087;
}
.io-pane {
  margin-bottom: 8px;
}
:deep(.log-row-active td) {
  background: #f3f7ee !important;
}
:deep(.table-card.log-row-active) {
  border-color: #b8cf9e;
  background: #f3f7ee;
}
@media (min-width: 761px) {
  .filter-toggle { display: none; }
}
@media (max-width: 760px) {
  .io-tabs { grid-template-columns: 1fr; }
  .live-ctl { flex-wrap: wrap; }
}
</style>

<style>
/* 费用回执面板：由子组件渲染，父页 scoped 样式够不到，走全局命名空间 cost-* */
.cost-detail-panel { display: flex; flex-direction: column; gap: 8px; min-width: 400px; font-size: 12px; }
.cost-detail-panel .cost-head { display: flex; align-items: center; gap: 8px; }
.cost-detail-panel .cost-matched { opacity: 0.7; font-size: 11px; }
.cost-detail-panel .cost-table { display: flex; flex-direction: column; gap: 2px; }
.cost-detail-panel .cost-tr { display: grid; grid-template-columns: 108px 1fr 88px 92px; gap: 8px; align-items: baseline; padding: 1px 0; }
.cost-detail-panel .cost-thead { opacity: 0.55; font-size: 11px; border-bottom: 1px solid var(--line); padding-bottom: 3px; }
.cost-detail-panel .cost-td { text-align: right; white-space: nowrap; }
.cost-detail-panel .cost-td-label { text-align: left; }
.cost-detail-panel .cost-note { opacity: 0.6; font-size: 11px; white-space: nowrap; }
.cost-detail-panel .cost-td-amount { font-variant-numeric: tabular-nums; font-weight: 600; }
.cost-detail-panel .cost-td-price { opacity: 0.7; }
.cost-detail-panel .cost-chips { display: flex; flex-wrap: wrap; gap: 4px; }
.cost-detail-panel .cost-chip { border: 1px solid var(--line); background: var(--row); border-radius: 4px; padding: 1px 6px; font-size: 11px; opacity: 0.85; }
.cost-detail-panel .cost-subtotal { border-top: 1px solid var(--line); padding-top: 5px; }
.cost-detail-panel .cost-sub-note { font-size: 11px; opacity: 0.65; }
.cost-detail-panel .cost-grand { display: flex; align-items: baseline; justify-content: space-between; background: var(--row); border-radius: 6px; padding: 6px 10px; }
.cost-detail-panel .cost-grand-k { opacity: 0.75; }
.cost-detail-panel .cost-grand-v { font-size: 14px; font-weight: 700; font-variant-numeric: tabular-nums; }
.cost-cell { border-bottom: 1px dashed var(--line); cursor: help; }
.drawer-cost-panel { margin-top: 6px; }
</style>
