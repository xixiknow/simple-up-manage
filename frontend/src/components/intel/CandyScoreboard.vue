<script setup lang="ts">
import { computed, h, onBeforeUnmount, ref, watch } from 'vue'
import { UiTag, type DataTableColumns } from '@/components/ui'
import { getIntelPlanSummary, type IntelKeyStat, type IntelPlanItem } from '@/api/intel'
import { errText, formatDurationMs, formatTime } from '@/utils/format'
import VerdictTimeline from './VerdictTimeline.vue'

const props = defineProps<{
  plan: IntelPlanItem
  tick: number
}>()

const loading = ref(false)
const error = ref('')
const stats = ref<IntelKeyStat[]>([])
const sortBy = ref<'accuracy' | 'latency' | 'samples'>('accuracy')
let loadSequence = 0

const sorted = computed(() => {
  const rows = [...stats.value]
  if (sortBy.value === 'latency') {
    rows.sort((a, b) => (a.avg_latency_ms || Infinity) - (b.avg_latency_ms || Infinity))
  } else if (sortBy.value === 'samples') {
    rows.sort((a, b) => b.samples - a.samples)
  } else {
    rows.sort((a, b) => b.accuracy - a.accuracy || b.samples - a.samples)
  }
  return rows
})

const totals = computed(() => {
  const samples = stats.value.reduce((n, s) => n + s.samples, 0)
  const success = stats.value.reduce((n, s) => n + s.success, 0)
  const quarantined = stats.value.filter(s => s.quarantine?.status === 'quarantined').length
  const latencyRows = stats.value.filter(s => s.success > 0 && s.avg_latency_ms > 0)
  const avgLatency = latencyRows.length
    ? latencyRows.reduce((n, s) => n + s.avg_latency_ms * s.success, 0) / latencyRows.reduce((n, s) => n + s.success, 0)
    : 0
  return {
    keys: stats.value.length,
    samples,
    success,
    quarantined,
    accuracy: samples ? (success / samples) * 100 : 0,
    avgLatency,
  }
})

function accuracyType(accuracy: number) {
  if (accuracy >= 80) return 'success'
  if (accuracy >= 50) return 'warning'
  return 'error'
}

const VERDICT_LABEL: Record<string, string> = {
  correct: '正确',
  success: '通过',
  incorrect: '答错',
  invalid: '非 HTML',
  error: '出错',
}

function quarantineCell(row: IntelKeyStat) {
  const q = row.quarantine
  if (!q) return '—'
  if (q.status === 'restored') {
    return h('div', { class: 'quarantine-cell' }, [
      h(UiTag, { type: 'success', size: 'small', bordered: false }, { default: () => '已恢复' }),
      q.restored_at ? h('span', { class: 'quarantine-sub' }, formatTime(q.restored_at)) : null,
    ])
  }
  const sub: string[] = [`连续正确 ${q.pass_streak}/2`]
  if (q.next_test_at) {
    const waitSec = Math.max(0, Math.round((new Date(q.next_test_at).getTime() - Date.now()) / 1000))
    sub.push(waitSec >= 60 ? `约 ${Math.ceil(waitSec / 60)} 分钟后复测` : '即将复测')
  }
  if (q.quarantine_count > 1) sub.push(`第 ${q.quarantine_count} 次隔离`)
  return h('div', {
    class: 'quarantine-cell quarantined',
    title: q.reason || '最近 10 次有效测试正确率低于 50%',
  }, [
    h(UiTag, { type: 'error', size: 'small', bordered: false }, { default: () => '隔离中' }),
    h('span', { class: 'quarantine-sub' }, sub.join(' · ')),
  ])
}

const columns: DataTableColumns<IntelKeyStat> = [
  {
    title: 'Key', key: 'key_name', width: 220, fixed: 'left', ellipsis: { tooltip: true }, mobileTitle: true,
    render: row => row.key_name || `Key #${row.platform_key_id}`,
  },
  { title: '提供商', key: 'upstream_name', width: 150, ellipsis: { tooltip: true } },
  {
    title: '正确率', key: 'accuracy', width: 130, mobileTag: true,
    render: row => h(UiTag, { type: accuracyType(row.accuracy), size: 'small', bordered: false },
      { default: () => (row.samples ? `${row.accuracy.toFixed(1)}%` : '—') }),
  },
  { title: '正确 / 测试', key: 'samples', width: 105, render: row => `${row.success} / ${row.samples}` },
  { title: '平均耗时', key: 'avg_latency_ms', width: 95, mobileHide: true, render: row => (row.avg_latency_ms ? formatDurationMs(Math.round(row.avg_latency_ms)) : '—') },
  {
    title: '最近答案', key: 'last_answer', width: 200, mobileHide: true, ellipsis: { tooltip: true },
    render: row => row.last_answer || (row.last_verdict === 'error' ? '—' : ''),
  },
  {
    title: '最近判定', key: 'last_verdict', width: 95,
    render: row => row.last_verdict
      ? h(UiTag, {
          type: row.last_verdict === 'correct' || row.last_verdict === 'success' ? 'success'
            : row.last_verdict === 'error' ? 'error' : 'warning',
          size: 'small', bordered: false,
        }, { default: () => VERDICT_LABEL[row.last_verdict] || row.last_verdict })
      : '—',
  },
  {
    title: '隔离', key: 'quarantine', width: 190,
    render: row => quarantineCell(row),
  },
  { title: '最近测试', key: 'last_at', width: 160, mobileHide: true, render: row => (row.last_at ? formatTime(row.last_at) : '—') },
  {
    title: '对错时间线', key: 'history', width: 190,
    render: row => h(VerdictTimeline, { points: row.history, max: 20 }),
  },
]

async function load() {
  const sequence = ++loadSequence
  loading.value = stats.value.length === 0
  error.value = ''
  try {
    const data = await getIntelPlanSummary(props.plan.id)
    if (sequence !== loadSequence) return
    stats.value = data.items ?? []
  } catch (e) {
    if (sequence === loadSequence) error.value = errText(e, '成绩榜加载失败')
  } finally {
    if (sequence === loadSequence) loading.value = false
  }
}

watch(() => [props.plan.id, props.tick], () => void load(), { immediate: true })

onBeforeUnmount(() => {
  loadSequence++
})
</script>

<template>
  <div class="scoreboard">
    <div class="muted scope-note">正确率与计数按最近 10 次有效测试滚动计算（传输错误不计入），时间线展示最近 20 次。</div>
    <div class="summary-row">
      <div class="summary-item"><span class="summary-value">{{ totals.keys }}</span><span class="summary-label">参与 Key</span></div>
      <div class="summary-item"><span class="summary-value">{{ totals.success }} / {{ totals.samples }}</span><span class="summary-label">近10轮正确 / 测试</span></div>
      <div class="summary-item"><span class="summary-value" :class="accuracyType(totals.accuracy) === 'success' ? 'good' : accuracyType(totals.accuracy) === 'warning' ? 'mid' : 'poor'">{{ totals.accuracy.toFixed(1) }}%</span><span class="summary-label">近10轮正确率</span></div>
      <div class="summary-item"><span class="summary-value">{{ totals.avgLatency ? formatDurationMs(Math.round(totals.avgLatency)) : '—' }}</span><span class="summary-label">平均耗时</span></div>
      <div v-if="totals.quarantined > 0" class="summary-item warn-item">
        <span class="summary-value poor">{{ totals.quarantined }}</span>
        <span class="summary-label">隔离中（已移出分组，按退避节奏复测）</span>
      </div>
    </div>
    <ui-alert v-if="error" type="error" :bordered="false">{{ error }}</ui-alert>
    <ui-data-table
      :columns="columns"
      :data="sorted"
      :loading="loading"
      :row-key="(row: IntelKeyStat) => row.platform_key_id"
      size="small"
      :bordered="false"
      card
    >
      <template #empty>
        <div class="muted" style="padding: 28px 0">尚无测试数据，点击「立即测试」发起一轮</div>
      </template>
    </ui-data-table>
    <div class="sort-row">
      <span class="muted">排序</span>
      <ui-radio-group v-model:value="sortBy" size="small">
        <ui-radio-button value="accuracy">按正确率</ui-radio-button>
        <ui-radio-button value="latency">按耗时</ui-radio-button>
        <ui-radio-button value="samples">按次数</ui-radio-button>
      </ui-radio-group>
      <span class="muted legend">
        <span class="legend-dot ok" />正确
        <span class="legend-dot warn" />答错/非 HTML
        <span class="legend-dot bad" />出错
      </span>
    </div>
  </div>
</template>

<style scoped>
.summary-row {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-bottom: 14px;
}
.summary-item {
  flex: 1 1 130px;
  background: #f3f7ee;
  border-radius: 9px;
  padding: 12px 16px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.summary-value {
  font-size: 19px;
  font-weight: 650;
  color: #263b34;
}
.summary-value.good { color: #578049; }
.summary-value.mid { color: #9d853f; }
.summary-value.poor { color: #a16d50; }
.summary-label {
  font-size: 11px;
  color: #819087;
}
.scope-note {
  font-size: 11px;
  margin-bottom: 10px;
}
.warn-item {
  background: #f7ece4;
}
.quarantine-cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  align-items: flex-start;
}
.quarantine-cell.quarantined {
  cursor: help;
}
.quarantine-sub {
  font-size: 11px;
  color: #819087;
  line-height: 1.4;
}
.sort-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
  flex-wrap: wrap;
}
.legend {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
}
.legend-dot {
  width: 9px;
  height: 9px;
  border-radius: 2px;
  display: inline-block;
  margin-left: 8px;
}
.legend-dot:first-child { margin-left: 0; }
.legend-dot.ok { background: #7da36a; }
.legend-dot.warn { background: #c4a35a; }
.legend-dot.bad { background: #c28d70; }
</style>
