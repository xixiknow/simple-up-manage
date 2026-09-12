<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { NTag, useMessage } from 'naive-ui'
import type { DataTableColumns, SelectOption } from 'naive-ui'
import { allPages, getRequestLog, listKeyOptions, listRequestLogs, listUpstreams } from '@/api/admin'
import type { RequestLog, RequestLogDetail, RequestLogQuery } from '@/api/types'
import { copyText, errText, formatMoney, formatNumber, formatSeconds, formatTime, formatTokenCount, formatTps } from '@/utils/format'

const LIVE_MS = 4000
const IN_FLIGHT_CAP_MS = 6 * 60 * 1000
const message = useMessage()

const loading = ref(false)
const error = ref('')
const items = ref<RequestLog[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const live = ref(true)
const lastRefresh = ref('')
const upstreamOptions = ref<SelectOption[]>([])
const keyOptions = ref<SelectOption[]>([])

const filters = reactive({
  upstream_id: null as number | null,
  key_id: null as number | null,
  model: '',
  success: '' as '' | 'true' | 'false',
  range: null as [number, number] | null,
})

const successOptions = [
  { label: '全部', value: '' },
  { label: '成功', value: 'true' },
  { label: '失败', value: 'false' },
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
  return (d[activeIo.value.field] as string | undefined) || ''
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
  const [up, keys] = await Promise.all([allPages(listUpstreams), allPages(listKeyOptions)])
  upstreamOptions.value = up.map((u) => ({ label: u.name, value: u.id }))
  keyOptions.value = keys.map((k) => ({ label: `${k.name} (${k.key_preview})`, value: k.id }))
}

const appliedFilters = ref<RequestLogQuery>({})
let snapshotId: number | undefined
let snapshotAt: string | undefined
let disposed = false
let loadSequence = 0
let listPending = false
let detailSequence = 0
let detailPending = false
let activeDetailId: number | null = null

async function load(opts?: { silent?: boolean }) {
  if (disposed) return
  if (opts?.silent && listPending) return
  const sequence = ++loadSequence
  listPending = true
  if (!opts?.silent) loading.value = true
  const requestedPage = page.value
  try {
    const res = await listRequestLogs({
      ...appliedFilters.value, page: requestedPage, page_size: pageSize.value,
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

function search() {
  appliedFilters.value = {
    upstream_id: filters.upstream_id || undefined,
    key_id: filters.key_id || undefined,
    model: filters.model.trim() || undefined,
    success: filters.success === '' ? undefined : filters.success === 'true',
    from: filters.range ? new Date(filters.range[0]).toISOString() : undefined,
    to: filters.range ? new Date(filters.range[1]).toISOString() : undefined,
  }
  refresh()
}

function reset() {
  filters.upstream_id = null
  filters.key_id = null
  filters.model = ''
  filters.success = ''
  filters.range = null
  search()
}

function startLive() {
  if (disposed) return
  stopLive()
  timer = window.setInterval(() => {
    if (live.value) void load({ silent: true })
    if (showDetail.value && detail.value?.in_flight) void refreshDetail(true)
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

const columns = computed<DataTableColumns<RequestLog>>(() => {
  clock.value
  return [
  {
    title: '时间',
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
    ellipsis: { tooltip: true },
    render(row) {
      return h('span', { class: 'preview' }, row.client_ip || '—')
    },
  },
  {
    title: 'API 密钥',
    key: 'consumer',
    width: 120,
    ellipsis: { tooltip: true },
    render(row) {
      return row.consumer_name || (row.consumer_key_id != null ? `#${row.consumer_key_id}` : '—')
    },
  },
  {
    title: '提供商',
    key: 'upstream_name',
    width: 120,
    ellipsis: { tooltip: true },
    render(row) {
      return row.upstream_name || (row.upstream_id != null ? `#${row.upstream_id}` : '—')
    },
  },
  { title: '模型', key: 'model', width: 140, ellipsis: { tooltip: true } },
  { title: '协议', key: 'protocol', width: 90 },
  {
    title: '类型',
    key: 'stream',
    width: 72,
    render(row) {
      return h(
        NTag,
        { size: 'small', bordered: false, type: row.stream_known && row.stream ? 'info' : 'default' },
        { default: () => streamLabel(row) },
      )
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
      return h('div', { class: 'tok-cell', title: `TTFT ${ttft} · 总耗时 ${formatSeconds(dur)}` }, [
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
      return formatMoney(row.cost_usd, 6)
    },
  },
  {
    title: '成功',
    key: 'success',
    width: 80,
    render(row) {
      if (row.in_flight) {
        return h(NTag, { type: 'warning', size: 'small', bordered: false }, { default: () => '进行中' })
      }
      return h(
        NTag,
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
  try {
    await loadOptions()
  } catch {
    /* filters remain empty if options fail */
  }
  await load()
  startLive()
})

onUnmounted(() => {
  disposed = true
  loadSequence++
  detailSequence++
  stopLive()
  stopClock()
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h2>请求记录</h2>
        <p>点击行查看请求头 / 请求体 / 响应；记录保留 24 小时后自动清理</p>
      </div>
      <div class="live-ctl">
        <n-switch v-model:value="live" size="small" />
        <span>实时刷新</span>
        <span v-if="lastRefresh" class="muted">更新于 {{ lastRefresh }}</span>
      </div>
    </div>

    <n-card size="small" :bordered="false">
      <div class="toolbar" style="margin-bottom: 12px">
        <n-select
          v-model:value="filters.upstream_id"
          :options="upstreamOptions"
          clearable
          placeholder="提供商"
          style="width: 180px"
        />
        <n-select
          v-model:value="filters.key_id"
          :options="keyOptions"
          clearable
          filterable
          placeholder="Key"
          style="width: 220px"
        />
        <n-input v-model:value="filters.model" clearable placeholder="模型" style="width: 160px" />
        <n-select v-model:value="filters.success" :options="successOptions" placeholder="成败" style="width: 110px" />
        <n-date-picker
          v-model:value="filters.range"
          type="datetimerange"
          clearable
          start-placeholder="从"
          end-placeholder="到"
        />
        <n-button type="primary" size="small" @click="search">查询</n-button>
        <n-button size="small" @click="reset">重置</n-button>
        <n-button size="small" @click="refresh">刷新</n-button>
      </div>
      <n-alert v-if="error" type="error" :title="error" style="margin-bottom: 10px" />
      <n-data-table
        remote
        size="small"
        :columns="columns"
        :data="items"
        :loading="loading"
        :scroll-x="1280"
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
    </n-card>

    <n-drawer v-model:show="showDetail" width="min(640px, 100vw)" placement="right">
      <n-drawer-content title="请求明细" closable :native-scrollbar="false">
        <n-spin :show="detailLoading">
          <n-alert v-if="detailError" type="error" :title="detailError" style="margin-bottom: 10px" />
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
              <div>
                <span class="meta-k">Tokens</span>{{ formatNumber(detail.input_tokens) }} /
                {{ formatNumber(detail.output_tokens) }}
              </div>
              <div>
                <span class="meta-k">Cache</span>读 {{ formatNumber(detail.cache_read_tokens) }} / 写
                {{ formatNumber(detail.cache_creation_tokens) }}
              </div>
              <div><span class="meta-k">费用</span>{{ formatMoney(detail.cost_usd, 6) }}</div>
              <div v-if="detail.failure_scope">
                <span class="meta-k">故障范围</span><span class="mono">{{ detail.failure_scope }}</span>
              </div>
              <div v-if="detail.failure_action">
                <span class="meta-k">调度动作</span><span class="mono">{{ detail.failure_action }}</span>
              </div>
            </div>
            <n-alert v-if="detail.error_message" type="error" :title="detail.error_message" style="margin: 10px 0" />
            <n-alert v-if="detail.error_message === 'stale in-flight request'" type="warning" title="请求异常中断，耗时为最后记录值" style="margin: 10px 0" />
            <n-alert
              v-if="detail.request_body_truncated || detail.response_body_truncated"
              type="warning"
              title="部分内容已截断或未保存二进制（上限 64KB；图片 / multipart 只记占位）"
              style="margin: 10px 0"
            />

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
            <n-card size="small" :bordered="true" class="io-pane">
              <div class="block-head">
                <h3>{{ activeIo.label }}</h3>
                <n-button size="tiny" @click="copySection(activeIo.label, activeIoRaw)">复制</n-button>
              </div>
              <pre class="log-pre">{{ pretty(activeIoRaw) || '—' }}</pre>
            </n-card>
          </template>
        </n-spin>
      </n-drawer-content>
    </n-drawer>
  </div>
</template>

<style scoped>
.live-ctl {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: #344054;
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
  color: #667085;
}
.meta-grid > div { min-width: 0; overflow-wrap: anywhere; }
@media (max-width: 600px) { .meta-grid { grid-template-columns: minmax(0, 1fr); } }
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
  background: #0f1720;
  color: #e2e8f0;
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
  color: #667085;
  font-size: 12px;
}
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
  color: #344054;
}
.io-tab:hover {
  border-color: #98a2b3;
}
.io-tab.active {
  border-color: #0f766e;
  background: #eef6f4;
  box-shadow: inset 0 0 0 1px #0f766e;
}
.io-tab-label {
  font-size: 13px;
  font-weight: 650;
}
.io-tab-hint {
  font-size: 11px;
  color: #667085;
}
.io-pane {
  margin-bottom: 8px;
}
:deep(.log-row-active td) {
  background: #eef6f4 !important;
}
</style>
