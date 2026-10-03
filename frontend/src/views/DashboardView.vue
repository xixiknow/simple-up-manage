<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import type { DataTableColumns } from '@/components/ui'
import { UiButton } from '@/components/ui'
import {
  getDashboardOverview,
  getDashboardRankings,
  getDashboardRecommendations,
} from '@/api/admin'
import type {
  DashInvestItem,
  DashOverview,
  DashProviderBalance,
  DashRankingRow,
  DashRecommendations,
  DashUrgentItem,
} from '@/api/types'
import { errText, formatMoney, formatNumber, formatPercent, formatTime } from '@/utils/format'
import { dashboardRange, type DashboardPeriod } from '@/utils/dashboardRange'

const router = useRouter()

const period = ref<DashboardPeriod>('today')
const customRange = ref<[number, number] | null>(null)

const overview = ref<DashOverview | null>(null)
const providerRank = ref<DashRankingRow[]>([])
const recs = ref<DashRecommendations | null>(null)
const histError = ref('')
const histLoading = ref(false)
const lastHistAt = ref('')
const availableFrom = ref('')

let histTimer: number | undefined

const currentRange = ref(dashboardRange(period.value, customRange.value, availableFrom.value))
let historyRequest = 0

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

async function loadHistory() {
  const request = ++historyRequest
  currentRange.value = dashboardRange(period.value, customRange.value, availableFrom.value)
  histLoading.value = true
  const { from, to } = currentRange.value
  try {
    const [ov, p, r] = await Promise.all([
      getDashboardOverview(from, to),
      getDashboardRankings(from, to, 'provider'),
      getDashboardRecommendations(),
    ])
    if (request !== historyRequest) return
    overview.value = ov
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

function goProvider(id: number) {
  void router.push({ path: '/upstreams', query: { id: String(id) } })
}

function goLogs(extra?: Record<string, string>) {
  const q: Record<string, string> = {
    from: currentRange.value.from,
    to: currentRange.value.to,
    ...extra,
  }
  void router.push({ path: '/logs', query: q })
}

const balanceRows = computed(() => overview.value?.balance.providers ?? [])

function balanceAmount(r: DashProviderBalance) {
  if (r.unlimited) return '不限额'
  if (r.unknown) return '未知'
  return money(r.balance_usd)
}

function balanceStatus(r: DashProviderBalance) {
  const tags: string[] = []
  if (!r.enabled) tags.push('停用')
  if (r.stale) tags.push('过期')
  return tags.join(' · ')
}

const balanceColumns: DataTableColumns<DashProviderBalance> = [
  { title: '提供商', key: 'name', ellipsis: { tooltip: true }, minWidth: 100 },
  { title: '消耗', key: 'consumption_usd', width: 90, render: (r) => (r.consumption_usd == null ? '—' : money(r.consumption_usd)) },
  { title: '余额', key: 'balance_usd', width: 100, render: balanceAmount },
  { title: '状态', key: 'status', width: 80, render: (r) => balanceStatus(r) || '—' },
  { title: '刷新时间', key: 'balance_at', width: 150, render: (r) => (r.balance_at ? formatTime(r.balance_at) : '—') },
]

const providerColumns: DataTableColumns<DashRankingRow> = [
  { title: '提供商', key: 'name', ellipsis: { tooltip: true }, minWidth: 120 },
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
    render: (r) => h(UiButton, { size: 'tiny', text: true, type: 'primary', onClick: () => goProvider(r.id) }, { default: () => '查看' }),
  },
]

const urgentColumns: DataTableColumns<DashUrgentItem> = [
  { title: '提供商', key: 'name', ellipsis: { tooltip: true }, minWidth: 120 },
  { title: '余额', key: 'balance_usd', width: 100, render: (r) => money(r.balance_usd) },
  { title: '预计小时', key: 'hours_left', width: 90, render: (r) => (r.hours_left == null ? '—' : r.hours_left.toFixed(1)) },
  { title: '24h 消耗', key: 'consumed_24h_usd', width: 100, render: (r) => money(r.consumed_24h_usd) },
  { title: '原因', key: 'reason', ellipsis: { tooltip: true }, minWidth: 160 },
  { title: '', key: 'go', width: 70, render: (r) => h(UiButton, { size: 'tiny', text: true, type: 'primary', onClick: () => goProvider(r.provider_id) }, { default: () => '查看' }) },
]

const investColumns: DataTableColumns<DashInvestItem> = [
  { title: '提供商', key: 'name', ellipsis: { tooltip: true }, minWidth: 120 },
  { title: '单位毛利', key: 'profit_per_cost', width: 100, render: (r) => r.profit_per_cost == null ? '—' : r.profit_per_cost.toFixed(2) },
  { title: '毛利率', key: 'margin', width: 80, render: (r) => pct(r.margin) },
  { title: '覆盖', key: 'coverage', width: 80, render: (r) => pct(r.coverage) },
  { title: '样本', key: 'samples', width: 70 },
  { title: '成功率', key: 'success_rate', width: 80, render: (r) => pct(r.success_rate) },
  { title: '原因', key: 'reason', ellipsis: { tooltip: true }, minWidth: 160 },
  { title: '', key: 'go', width: 70, render: (r) => h(UiButton, { size: 'tiny', text: true, type: 'primary', onClick: () => goProvider(r.provider_id) }, { default: () => '查看' }) },
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
  void loadHistory()
  startHistTimer()
})

onUnmounted(() => {
  historyRequest++
  stopHistTimer()
})
</script>

<template>
  <div class="page dash">
    <div class="page-head">
      <div>
        <span class="eyebrow">经营 / OVERVIEW</span>
        <h2>仪表盘</h2>
        <p>提供商表现、余额与收益概览。</p>
      </div>
      <div class="toolbar">
        <span v-if="lastHistAt" class="muted">经营数据 {{ lastHistAt }}</span>
      </div>
    </div>

    <ui-card size="small" :bordered="false">
      <div class="toolbar" style="margin-bottom: 10px">
        <ui-radio-group v-model:value="period" size="small">
          <ui-radio-button value="today">今日</ui-radio-button>
          <ui-radio-button value="7d">7 天</ui-radio-button>
          <ui-radio-button value="30d">30 天</ui-radio-button>
          <ui-radio-button value="all">累计</ui-radio-button>
          <ui-radio-button value="custom">自定义</ui-radio-button>
        </ui-radio-group>
        <ui-date-picker
          v-if="period === 'custom'"
          v-model:value="customRange"
          type="datetimerange"
          clearable
          start-placeholder="从"
          end-placeholder="到"
        />
        <ui-button size="small" :loading="histLoading" @click="loadHistory">刷新经营数据</ui-button>
      </div>
      <ui-alert v-if="histError" type="error" :title="histError" style="margin-bottom: 10px">失败时保留上次数据，不显示虚假零值。</ui-alert>
      <ui-alert v-if="dq && !dq.complete" type="warning" :bordered="false" style="margin-bottom: 10px">
        数据不完整
        <span v-if="dq.overflow"> · 结算队列溢出</span>
        <span v-if="unmeasuredText"> · {{ unmeasuredText }}</span>
        <span v-if="overview?.meta.available_from"> · 统计起点 {{ formatTime(overview.meta.available_from) }}</span>
      </ui-alert>
      <ui-alert v-if="logsBeyondRetention" type="info" :bordered="false" style="margin-bottom: 10px">
        当前区间超过请求日志保留期（约 24 小时），明细可能已清理，汇总仍可查。
      </ui-alert>

      <div class="split">
        <ui-card size="small" title="当前余额" embedded>
          <div class="metric-v">{{ money(overview?.balance.total_known_usd) }}</div>
          <div class="muted">启用提供商 {{ money(overview?.balance.enabled_known_usd) }} · 不限额 {{ overview?.balance.unlimited_count ?? 0 }} · 未知 {{ overview?.balance.unknown_count ?? 0 }} · 过期 {{ overview?.balance.stale_count ?? 0 }}</div>
          <div class="muted">明细按所选周期消耗、余额降序排列；余额刷新时间 {{ formatTime(overview?.balance.refreshed_at) }}，与本页查询时间无关</div>
          <div v-if="balanceRows.length" class="bal-list">
            <ui-data-table size="small" :columns="balanceColumns" :data="balanceRows" :scroll-x="540" :pagination="false" />
          </div>
          <ui-empty v-else description="暂无提供商余额数据" />
        </ui-card>
        <ui-card size="small" title="期间经营" embedded>
          <div class="fin-grid">
            <div><span class="metric-k">请求开始 / 完成</span><div>{{ formatNumber(overview?.requests_started) }} / {{ formatNumber(overview?.requests_completed) }} <ui-button text size="tiny" type="primary" @click="goLogs()">日志</ui-button></div></div>
            <div><span class="metric-k">业务成功率</span><div>{{ pct(overview?.success_rate) }}</div></div>
            <div><span class="metric-k">已知收入</span><div>{{ money(overview?.finance.known_revenue_usd) }}</div></div>
            <div><span class="metric-k">已知成本</span><div>{{ money(overview?.finance.known_estimated_cost_usd) }}</div></div>
            <div><span class="metric-k">完整集合毛利</span><div :class="{ neg: (overview?.finance.estimated_profit_usd ?? 0) < 0 }">{{ money(overview?.finance.estimated_profit_usd) }}</div></div>
            <div><span class="metric-k">毛利率 / 覆盖率</span><div>{{ pct(overview?.finance.margin) }} / {{ pct(overview?.finance.coverage) }}</div></div>
          </div>
          <div class="muted">完整集合收入 {{ money(overview?.finance.covered_revenue_usd) }} − 成本 {{ money(overview?.finance.covered_cost_usd) }}。毛利仅用完整测算集合。开始计数按开始时间，成功率与财务按完成时间。</div>
        </ui-card>
      </div>
    </ui-card>

    <ui-card size="small" title="提供商表现" :bordered="false">
      <ui-data-table
        size="small"
        :columns="providerColumns"
        :data="providerRank"
        :scroll-x="860"
        :pagination="false"
      />
    </ui-card>

    <div class="chart-grid">
      <ui-card size="small" title="急需充值（近 24 小时）" :bordered="false">
        <ui-data-table size="small" :columns="urgentColumns" :data="recs?.urgent ?? []" :scroll-x="720" />
        <div class="muted" style="margin-top: 8px">仅按本网关流量估算，不把其他渠道用量当作已知；近 24 小时无消耗的提供商不会出现在这里。</div>
      </ui-card>
      <ui-card size="small" title="值得投入 · 预估性价比（近 7 天）" :bordered="false">
        <ui-data-table size="small" :columns="investColumns" :data="recs?.invest ?? []" :scroll-x="860" />
        <div class="muted" style="margin-top: 8px">
          共同需求覆盖 {{ pct(recs?.common_coverage) }}。{{ recs?.note }}
        </div>
        <div v-if="!recs?.invest?.length && recs?.demand_boards?.length" class="muted">共同需求不足，以下按需求分别比较。</div>
        <div v-for="board in recs?.demand_boards ?? []" :key="board.key" style="margin-top: 16px">
          <div style="margin-bottom: 8px">{{ board.label }} · 需求占比 {{ pct(board.weight) }}</div>
          <ui-data-table size="small" :columns="investColumns" :data="board.items" :scroll-x="860" />
        </div>
      </ui-card>
    </div>
  </div>
</template>

<style scoped>
.split,
.chart-grid {
  display: grid;
  gap: 10px;
}
.split {
  grid-template-columns: 1.2fr 1fr;
}
.chart-grid {
  grid-template-columns: 1fr 1fr;
}
.metric-k {
  color: #758574;
  font-size: 12px;
}
.metric-v {
  margin: 8px 0 4px;
  color: #263b34;
  font-size: 32px;
  font-weight: 500;
  font-variant-numeric: tabular-nums;
  letter-spacing: -1px;
}
.fin-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px 16px;
}
.bal-list {
  margin-top: 10px;
  max-height: 300px;
  overflow: auto;
}
.neg {
  color: #a16d50;
}
@media (max-width: 1100px) {
  .split,
  .chart-grid {
    grid-template-columns: 1fr;
  }
}
@media (max-width: 760px) {
  .metric-v {
    font-size: 28px;
  }
  .fin-grid {
    grid-template-columns: 1fr;
  }
}
</style>
