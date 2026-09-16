<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import type { DataTableColumns } from 'naive-ui'
import { NButton } from 'naive-ui'
import type { EChartsCoreOption } from 'echarts/core'
import {
  getDashboardOverview,
  getDashboardRankings,
  getDashboardRecommendations,
  getDashboardTrends,
} from '@/api/admin'
import { subscribeDashboardLive, type LiveStatus } from '@/api/sse'
import type {
  DashInvestItem,
  DashLiveSnapshot,
  DashOverview,
  DashRankingRow,
  DashRecommendations,
  DashTrendPoint,
  DashTrends,
  DashUrgentItem,
  DashWatchItem,
} from '@/api/types'
import DashChart from '@/components/DashChart.vue'
import { errText, formatMoney, formatNumber, formatPercent, formatTime } from '@/utils/format'
import { useAuthStore } from '@/stores/auth'
import { dashboardRange, type DashboardPeriod } from '@/utils/dashboardRange'

const router = useRouter()
const auth = useAuthStore()

const period = ref<DashboardPeriod>('today')
const customRange = ref<[number, number] | null>(null)
const rankingDim = ref<'group' | 'provider'>('group')

const live = ref<DashLiveSnapshot | null>(null)
const liveStatus = ref<LiveStatus>('connecting')
const liveUpdatedAt = ref('')
const liveStale = ref(false)
const paused = ref(false)
const spark = ref<{ t: number; biz: number | null; bizRpm: number | null; up: number | null; upRpm: number | null }[]>([])
let lastInstance = ''

const overview = ref<DashOverview | null>(null)
const trends = ref<DashTrends | null>(null)
const groupRank = ref<DashRankingRow[]>([])
const providerRank = ref<DashRankingRow[]>([])
const recs = ref<DashRecommendations | null>(null)
const histError = ref('')
const histLoading = ref(false)
const lastHistAt = ref('')
const availableFrom = ref('')

let liveSub: ReturnType<typeof subscribeDashboardLive> | null = null
let histTimer: number | undefined

const STATUS_LABEL: Record<LiveStatus, string> = {
  connecting: '连接中',
  live: '实时',
  reconnecting: '重连中',
  paused: '已暂停',
  unauthorized: '未授权',
  stale: '已过期',
}

const currentRange = ref(dashboardRange(period.value, customRange.value, availableFrom.value))
let historyRequest = 0
let liveGap = false

const logsBeyondRetention = computed(() => {
  const from = new Date(currentRange.value.from).getTime()
  return Date.now() - from > 24 * 3600 * 1000
})

function money(v?: number | null, digits = 4) {
  return formatMoney(v, digits)
}

function pct(v?: number | null) {
  return formatPercent(v)
}

function onLiveSnap(snap: DashLiveSnapshot) {
  if (liveGap || (lastInstance && lastInstance !== snap.instance_id)) {
    spark.value.push({ t: Date.now(), biz: null, bizRpm: null, up: null, upRpm: null })
  }
  liveGap = false
  lastInstance = snap.instance_id
  live.value = snap
  liveUpdatedAt.value = formatTime(snap.server_time)
  liveStale.value = liveStatus.value !== 'live'
  spark.value = [...spark.value, {
    t: Date.now(),
    biz: snap.business_inflight,
    bizRpm: snap.business_rpm,
    up: snap.upstream_inflight,
    upRpm: snap.upstream_rpm,
  }].slice(-180)
}

async function loadHistory() {
  const request = ++historyRequest
  currentRange.value = dashboardRange(period.value, customRange.value, availableFrom.value)
  histLoading.value = true
  const { from, to, gran } = currentRange.value
  try {
    const [ov, tr, g, p, r] = await Promise.all([
      getDashboardOverview(from, to),
      getDashboardTrends(from, to, gran),
      getDashboardRankings(from, to, 'group'),
      getDashboardRankings(from, to, 'provider'),
      getDashboardRecommendations(),
    ])
    if (request !== historyRequest) return
    overview.value = ov
    trends.value = tr
    groupRank.value = g.items ?? []
    providerRank.value = p.items ?? []
    recs.value = r
    availableFrom.value = ov.meta.available_from
    histError.value = ''
    lastHistAt.value = formatTime(ov.meta.generated_at)
  } catch (e) {
    if (request === historyRequest) histError.value = errText(e)
  } finally {
    if (request === historyRequest) histLoading.value = false
  }
}

function startHistTimer() {
  stopHistTimer()
  histTimer = window.setInterval(() => {
    void loadHistory()
  }, 30000)
}

function stopHistTimer() {
  if (histTimer != null) window.clearInterval(histTimer)
  histTimer = undefined
}

function startLive() {
  liveSub?.stop()
  liveSub = subscribeDashboardLive({
    onSnapshot: onLiveSnap,
    onHeartbeat: (hb) => {
      liveUpdatedAt.value = formatTime(hb.server_time)
    },
    onStatus: (s) => {
      if (s !== 'live' && live.value) liveGap = true
      liveStatus.value = s
      liveStale.value = s !== 'live'
    },
    onUnauthorized: () => {
      auth.logout()
      if (router.currentRoute.value.path !== '/login') {
        void router.push({ path: '/login', query: { redirect: '/dashboard' } })
      }
    },
  })
}

function togglePause(on: boolean) {
  paused.value = on
  if (on) liveSub?.pause()
  else liveSub?.resume()
}

function goProvider(id: number) {
  void router.push({ path: '/upstreams', query: { id: String(id) } })
}

function goGroup(id: number) {
  void router.push({ path: '/api-keys', query: { group: String(id) } })
}

function goLogs(extra?: Record<string, string>) {
  const q: Record<string, string> = {
    from: currentRange.value.from,
    to: currentRange.value.to,
    ...extra,
  }
  void router.push({ path: '/logs', query: q })
}

function lineOption(points: DashTrendPoint[], keys: { key: keyof DashTrendPoint; name: string; percent?: boolean }[]): EChartsCoreOption {
  return {
    tooltip: { trigger: 'axis' },
    legend: { top: 0, textStyle: { fontSize: 11 } },
    grid: { left: 40, right: 16, top: 28, bottom: 24 },
    xAxis: {
      type: 'category',
      data: points.map((p) => formatTime(p.bucket).slice(5, 16)),
      axisLabel: { fontSize: 10 },
    },
    yAxis: { type: 'value', splitLine: { lineStyle: { color: '#eef2f6' } } },
    series: keys.map((k) => ({
      name: k.name,
      type: 'line',
      showSymbol: false,
      data: points.map((p) => {
        const v = p[k.key]
        if (v == null || v === false) return null
        if (typeof v === 'number' && k.percent) return Number((v * 100).toFixed(2))
        return v
      }),
    })),
  }
}

const sparkOption = computed<EChartsCoreOption>(() => ({
  tooltip: { trigger: 'axis' },
  legend: { top: 0, textStyle: { fontSize: 11 } },
  grid: { left: 36, right: 12, top: 24, bottom: 20 },
  xAxis: { type: 'category', data: spark.value.map((p) => formatTime(new Date(p.t).toISOString()).slice(11, 19)), axisLabel: { fontSize: 10 } },
  yAxis: { type: 'value', splitLine: { lineStyle: { color: '#eef2f6' } } },
  series: [
    { name: '业务并发', type: 'line', showSymbol: false, data: spark.value.map((p) => p.biz) },
    { name: '业务 RPM', type: 'line', showSymbol: false, data: spark.value.map((p) => p.bizRpm) },
    { name: '尝试并发', type: 'line', showSymbol: false, data: spark.value.map((p) => p.up) },
    { name: '尝试 RPM', type: 'line', showSymbol: false, data: spark.value.map((p) => p.upRpm) },
  ],
}))

const trafficOption = computed(() => lineOption(trends.value?.points ?? [], [
  { key: 'requests_started', name: '请求开始' },
  { key: 'failure_rate', name: '失败率 %', percent: true },
]))

const moneyOption = computed(() => lineOption(trends.value?.points ?? [], [
  { key: 'consumption_usd', name: '消耗 USD' },
  { key: 'covered_profit_usd', name: '完整集合毛利 USD' },
  { key: 'known_revenue_usd', name: '已知收入 USD' },
]))

const qualityOption = computed(() => lineOption(trends.value?.points ?? [], [
  { key: 'success_rate', name: '业务成功率 %', percent: true },
  { key: 'retry_rate', name: '重试率 %', percent: true },
  { key: 'ttft_p50_ms', name: 'P50 ms' },
  { key: 'ttft_p95_ms', name: 'P95 ms' },
]))

function rankColumns(dim: 'group' | 'provider'): DataTableColumns<DashRankingRow> {
  return [
    { title: dim === 'group' ? '分组' : '提供商', key: 'name', ellipsis: { tooltip: true }, minWidth: 120 },
    { title: '完成', key: 'requests_completed', width: 80, render: (r) => formatNumber(r.requests_completed) },
    { title: '成功率', key: 'success_rate', width: 80, render: (r) => pct(r.success_rate) },
    { title: '预估毛利', key: 'estimated_profit_usd', width: 110, render: (r) => money(r.estimated_profit_usd) },
    { title: '毛利率', key: 'margin', width: 80, render: (r) => pct(r.margin) },
    { title: '覆盖率', key: 'coverage', width: 80, render: (r) => pct(r.coverage) },
    { title: '消耗', key: 'consumption_usd', width: 100, render: (r) => money(r.consumption_usd) },
    {
      title: '',
      key: 'go',
      width: 70,
      render: (r) => h(NButton, { size: 'tiny', text: true, type: 'primary', onClick: () => (dim === 'group' ? goGroup(r.id) : goProvider(r.id)) }, { default: () => '查看' }),
    },
  ]
}

const urgentColumns: DataTableColumns<DashUrgentItem> = [
  { title: '提供商', key: 'name', ellipsis: { tooltip: true }, minWidth: 120 },
  { title: '余额', key: 'balance_usd', width: 100, render: (r) => money(r.balance_usd) },
  { title: '预计小时', key: 'hours_left', width: 90, render: (r) => (r.zero_consumption ? '暂无消耗' : r.hours_left == null ? '—' : r.hours_left.toFixed(1)) },
  { title: '24h 消耗', key: 'consumed_24h_usd', width: 100, render: (r) => money(r.consumed_24h_usd) },
  { title: '原因', key: 'reason', ellipsis: { tooltip: true }, minWidth: 160 },
  { title: '', key: 'go', width: 70, render: (r) => h(NButton, { size: 'tiny', text: true, type: 'primary', onClick: () => goProvider(r.provider_id) }, { default: () => '查看' }) },
]

const investColumns: DataTableColumns<DashInvestItem> = [
  { title: '提供商', key: 'name', ellipsis: { tooltip: true }, minWidth: 120 },
  { title: '单位毛利', key: 'profit_per_cost', width: 100, render: (r) => r.profit_per_cost == null ? '—' : r.profit_per_cost.toFixed(2) },
  { title: '毛利率', key: 'margin', width: 80, render: (r) => pct(r.margin) },
  { title: '覆盖', key: 'coverage', width: 80, render: (r) => pct(r.coverage) },
  { title: '样本', key: 'samples', width: 70 },
  { title: '成功率', key: 'success_rate', width: 80, render: (r) => pct(r.success_rate) },
  { title: '原因', key: 'reason', ellipsis: { tooltip: true }, minWidth: 160 },
  { title: '', key: 'go', width: 70, render: (r) => h(NButton, { size: 'tiny', text: true, type: 'primary', onClick: () => goProvider(r.provider_id) }, { default: () => '查看' }) },
]

const watchColumns: DataTableColumns<DashWatchItem> = [
  { title: '提供商', key: 'name', ellipsis: { tooltip: true } },
  { title: '原因', key: 'reason', ellipsis: { tooltip: true } },
  { title: '', key: 'go', width: 70, render: (r) => h(NButton, { size: 'tiny', text: true, type: 'primary', onClick: () => goProvider(r.provider_id) }, { default: () => '查看' }) },
]

const unmeasuredText = computed(() => {
  const u = overview.value?.meta.data_quality.unmeasured
  if (!u) return ''
  const labels: Record<string, string> = {
    unbound: '未绑定',
    missing_sale: '缺售价',
    missing_price: '缺价格',
    missing_usage: '缺用量',
    interrupted: '中断',
    local_only: '仅本地失败',
  }
  return Object.entries(u)
    .filter(([, n]) => n > 0)
    .map(([k, n]) => `${labels[k] || k} ${n}`)
    .join(' · ')
})

const dq = computed(() => overview.value?.meta.data_quality)

watch(period, () => {
  void loadHistory()
})
watch(customRange, () => {
  if (period.value === 'custom') void loadHistory()
})

onMounted(() => {
  startLive()
  void loadHistory()
  startHistTimer()
})

onUnmounted(() => {
  historyRequest++
  liveSub?.stop()
  liveSub = null
  stopHistTimer()
})
</script>

<template>
  <div class="page dash">
    <div class="page-head">
      <div>
        <h2>仪表盘</h2>
        <p>实时运行、期间经营与充值建议。实时卡片不受下方日期筛选影响。</p>
      </div>
      <div class="toolbar">
        <n-tag :type="liveStatus === 'live' ? 'success' : liveStatus === 'paused' ? 'default' : 'warning'" size="small">
          {{ STATUS_LABEL[liveStatus] }}
        </n-tag>
        <span v-if="liveUpdatedAt" class="muted" :class="{ stale: liveStale }">更新于 {{ liveUpdatedAt }}</span>
        <n-switch :value="paused" size="small" @update:value="togglePause">
          <template #checked>暂停</template>
          <template #unchecked>实时</template>
        </n-switch>
      </div>
    </div>

    <n-alert v-if="live && !live.window_complete" type="info" :bordered="false">
      进程刚启动，RPM 窗口还在积累（满 60 秒后完整）。
    </n-alert>

    <div class="live-grid">
      <n-card size="small" :bordered="false" :class="{ stale: liveStale }">
        <div class="metric-k">业务并发</div>
        <div class="metric-v">{{ live ? formatNumber(live.business_inflight) : '—' }}</div>
      </n-card>
      <n-card size="small" :bordered="false" :class="{ stale: liveStale }">
        <div class="metric-k">业务 RPM</div>
        <div class="metric-v">{{ live ? formatNumber(live.business_rpm) : '—' }}</div>
        <div class="muted">近 60 秒开始的业务请求</div>
      </n-card>
      <n-card size="small" :bordered="false" :class="{ stale: liveStale }">
        <div class="metric-k">上游尝试并发</div>
        <div class="metric-v">{{ live ? formatNumber(live.upstream_inflight) : '—' }}</div>
      </n-card>
      <n-card size="small" :bordered="false" :class="{ stale: liveStale }">
        <div class="metric-k">上游尝试 RPM</div>
        <div class="metric-v">{{ live ? formatNumber(live.upstream_rpm) : '—' }}</div>
        <div class="muted">近 60 秒实际 HTTP 调用</div>
      </n-card>
    </div>
    <n-card size="small" title="实时短时曲线" :bordered="false">
      <DashChart :option="sparkOption" height="180px" />
      <div class="muted">断线空档不插值。离开页面会关闭订阅。</div>
    </n-card>

    <n-card size="small" :bordered="false">
      <div class="toolbar" style="margin-bottom: 10px">
        <n-radio-group v-model:value="period" size="small">
          <n-radio-button value="today">今日</n-radio-button>
          <n-radio-button value="7d">7 天</n-radio-button>
          <n-radio-button value="30d">30 天</n-radio-button>
          <n-radio-button value="all">累计</n-radio-button>
          <n-radio-button value="custom">自定义</n-radio-button>
        </n-radio-group>
        <n-date-picker
          v-if="period === 'custom'"
          v-model:value="customRange"
          type="datetimerange"
          clearable
          start-placeholder="从"
          end-placeholder="到"
        />
        <n-button size="small" :loading="histLoading" @click="loadHistory">刷新经营数据</n-button>
        <span v-if="lastHistAt" class="muted">经营数据 {{ lastHistAt }}</span>
      </div>
      <n-alert v-if="histError" type="error" :title="histError" style="margin-bottom: 10px">失败时保留上次数据，不显示虚假零值。</n-alert>
      <n-alert v-if="dq && !dq.complete" type="warning" :bordered="false" style="margin-bottom: 10px">
        数据不完整
        <span v-if="dq.overflow"> · 结算队列溢出</span>
        <span v-if="unmeasuredText"> · {{ unmeasuredText }}</span>
        <span v-if="overview?.meta.available_from"> · 统计起点 {{ formatTime(overview.meta.available_from) }}</span>
      </n-alert>
      <n-alert v-if="logsBeyondRetention" type="info" :bordered="false" style="margin-bottom: 10px">
        当前区间超过请求日志保留期（约 24 小时），明细可能已清理，汇总仍可查。
      </n-alert>

      <div class="split">
        <n-card size="small" title="当前余额" embedded>
          <div class="metric-v">{{ money(overview?.balance.total_known_usd) }}</div>
          <div class="muted">启用提供商 {{ money(overview?.balance.enabled_known_usd) }} · 不限额 {{ overview?.balance.unlimited_count ?? 0 }} · 未知 {{ overview?.balance.unknown_count ?? 0 }} · 过期 {{ overview?.balance.stale_count ?? 0 }}</div>
          <div class="muted">余额刷新时间 {{ formatTime(overview?.balance.refreshed_at) }}，与本页查询时间无关</div>
        </n-card>
        <n-card size="small" title="期间经营" embedded>
          <div class="fin-grid">
            <div><span class="metric-k">请求开始 / 完成</span><div>{{ formatNumber(overview?.requests_started) }} / {{ formatNumber(overview?.requests_completed) }} <n-button text size="tiny" type="primary" @click="goLogs()">日志</n-button></div></div>
            <div><span class="metric-k">业务成功率</span><div>{{ pct(overview?.success_rate) }}</div></div>
            <div><span class="metric-k">已知收入</span><div>{{ money(overview?.finance.known_revenue_usd) }}</div></div>
            <div><span class="metric-k">已知成本</span><div>{{ money(overview?.finance.known_estimated_cost_usd) }}</div></div>
            <div><span class="metric-k">完整集合毛利</span><div :class="{ neg: (overview?.finance.estimated_profit_usd ?? 0) < 0 }">{{ money(overview?.finance.estimated_profit_usd) }}</div></div>
            <div><span class="metric-k">毛利率 / 覆盖率</span><div>{{ pct(overview?.finance.margin) }} / {{ pct(overview?.finance.coverage) }}</div></div>
          </div>
          <div class="muted">完整集合收入 {{ money(overview?.finance.covered_revenue_usd) }} − 成本 {{ money(overview?.finance.covered_cost_usd) }}。毛利仅用完整测算集合。开始计数按开始时间，成功率与财务按完成时间。</div>
        </n-card>
      </div>
    </n-card>

    <div class="chart-grid">
      <n-card size="small" title="流量 / 失败率" :bordered="false">
        <DashChart v-if="trends?.points?.length" :option="trafficOption" />
        <n-empty v-else description="暂无趋势数据" />
      </n-card>
      <n-card size="small" title="消耗 / 毛利" :bordered="false">
        <DashChart v-if="trends?.points?.length" :option="moneyOption" />
        <n-empty v-else description="暂无财务趋势" />
      </n-card>
      <n-card size="small" title="成功率 / 重试 / 首字延迟（近似分位数）" :bordered="false">
        <DashChart v-if="trends?.points?.length" :option="qualityOption" />
        <n-empty v-else description="暂无质量趋势" />
      </n-card>
    </div>

    <n-card size="small" :bordered="false">
      <template #header>
        <n-space align="center">
          <span>经营排行</span>
          <n-radio-group v-model:value="rankingDim" size="small">
            <n-radio-button value="group">分组</n-radio-button>
            <n-radio-button value="provider">提供商</n-radio-button>
          </n-radio-group>
        </n-space>
      </template>
      <n-data-table
        size="small"
        :columns="rankColumns(rankingDim)"
        :data="rankingDim === 'group' ? groupRank : providerRank"
        :scroll-x="860"
        :pagination="false"
      />
    </n-card>

    <div class="chart-grid">
      <n-card size="small" title="急需充值（近 24 小时）" :bordered="false">
        <n-data-table size="small" :columns="urgentColumns" :data="recs?.urgent ?? []" :scroll-x="720" />
        <div class="muted" style="margin-top: 8px">仅按本网关流量估算，不把其他渠道用量当作已知。</div>
      </n-card>
      <n-card size="small" title="值得投入 · 预估性价比（近 7 天）" :bordered="false">
        <n-data-table size="small" :columns="investColumns" :data="recs?.invest ?? []" :scroll-x="860" />
        <div class="muted" style="margin-top: 8px">
          共同需求覆盖 {{ pct(recs?.common_coverage) }}。{{ recs?.note }}
        </div>
        <div v-if="!recs?.invest?.length && recs?.demand_boards?.length" class="muted">共同需求不足，以下按需求分别比较。</div>
        <div v-for="board in recs?.demand_boards ?? []" :key="board.key" style="margin-top: 16px">
          <div style="margin-bottom: 8px">{{ board.label }} · 需求占比 {{ pct(board.weight) }}</div>
          <n-data-table size="small" :columns="investColumns" :data="board.items" :scroll-x="860" />
        </div>
      </n-card>
    </div>
    <n-card size="small" title="待观察" :bordered="false">
      <n-data-table size="small" :columns="watchColumns" :data="recs?.watch ?? []" />
    </n-card>
  </div>
</template>

<style scoped>
.live-grid,
.split,
.chart-grid {
  display: grid;
  gap: 10px;
}
.live-grid {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}
.split {
  grid-template-columns: 1fr 1.4fr;
}
.chart-grid {
  grid-template-columns: 1fr 1fr;
}
.metric-k {
  color: #667085;
  font-size: 12px;
}
.metric-v {
  font-size: 26px;
  font-weight: 650;
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.02em;
}
.fin-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px 16px;
}
.stale .metric-v,
.stale.muted {
  opacity: 0.55;
}
.neg {
  color: #d92d20;
}
@media (max-width: 960px) {
  .live-grid,
  .split,
  .chart-grid {
    grid-template-columns: 1fr;
  }
}
</style>
