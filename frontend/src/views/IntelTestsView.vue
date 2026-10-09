<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { UiButton, UiSpace, UiSwitch, UiTag, useDialog, useMessage, type DataTableColumns } from '@/components/ui'
import { listRouteGroups } from '@/api/admin'
import {
  INTEL_KIND_LABEL,
  deleteIntelPlan,
  listIntelPlans,
  listIntelRuns,
  runIntelPlan,
  updateIntelPlan,
  type IntelPlanItem,
  type IntelQuestionKind,
  type IntelTestRun,
} from '@/api/intel'
import type { RouteGroup } from '@/api/types'
import { errText, formatDurationMs, formatTime } from '@/utils/format'
import IntelPlanModal from '@/components/intel/IntelPlanModal.vue'
import CandyScoreboard from '@/components/intel/CandyScoreboard.vue'
import PelicanGallery from '@/components/intel/PelicanGallery.vue'

const message = useMessage()
const dialog = useDialog()

const loading = ref(false)
const error = ref('')
const plans = ref<IntelPlanItem[]>([])
const groups = ref<RouteGroup[]>([])
const selectedId = ref<number | null>(null)
const tick = ref(0)
const recentRuns = ref<IntelTestRun[]>([])
const modalShow = ref(false)
const editing = ref<IntelPlanItem | null>(null)
const actingIds = ref(new Set<number>())

const selected = computed(() => plans.value.find(p => p.id === selectedId.value) ?? null)
const anyRunning = computed(() => plans.value.some(p => p.running))

async function loadPlans(silent = false) {
  if (!silent) loading.value = true
  error.value = ''
  try {
    plans.value = await listIntelPlans()
    if (selectedId.value && !plans.value.some(p => p.id === selectedId.value)) {
      selectedId.value = null
    }
  } catch (e) {
    error.value = errText(e, '任务列表加载失败')
  } finally {
    if (!silent) loading.value = false
  }
}

async function loadGroups() {
  try {
    groups.value = await listRouteGroups()
  } catch {
    groups.value = []
  }
}

async function loadRuns() {
  if (!selectedId.value) {
    recentRuns.value = []
    return
  }
  try {
    const page = await listIntelRuns(selectedId.value, { page: 1, page_size: 8 })
    recentRuns.value = page.items ?? []
  } catch {
    recentRuns.value = []
  }
}

async function refreshDetail(silent = true) {
  await Promise.all([loadPlans(silent), loadRuns()])
}

const refreshing = ref(false)
async function refreshAll() {
  if (refreshing.value) return
  refreshing.value = true
  try {
    await Promise.all([loadPlans(), loadRuns()])
    tick.value++
  } finally {
    refreshing.value = false
  }
}

function describeInterval(minutes: number) {
  // 须与后端 domain.IntelMinIntervalMinutes（5）一致：5/10 分钟周期曾在这里被误显示为仅手动
  if (!minutes || minutes < 5) return '仅手动'
  if (minutes % 1440 === 0) return `每 ${minutes / 1440} 天`
  if (minutes % 60 === 0) return `每 ${minutes / 60} 小时`
  return `每 ${minutes} 分钟`
}

function kindTag(kind: IntelQuestionKind) {
  return h(UiTag, { type: kind === 'pelican' ? 'info' : 'warning', size: 'small', bordered: false }, { default: () => INTEL_KIND_LABEL[kind] })
}

function accuracyTag(row: IntelPlanItem) {
  const { samples, success, accuracy } = row.stats
  if (!samples) return '—'
  const type = accuracy >= 80 ? 'success' : accuracy >= 50 ? 'warning' : 'error'
  return h(UiTag, { type, size: 'small', bordered: false }, { default: () => `${accuracy.toFixed(1)}% · ${success}/${samples}` })
}

function runSummary(row: IntelPlanItem) {
  const run = row.last_run
  if (!run) return '尚未运行'
  if (run.status === 'running') {
    return h('span', { class: 'run-running' }, `测试中 ${run.done}/${run.total}`)
  }
  return `${formatTime(run.finished_at || run.started_at)} · 通过 ${run.success}/${run.total}`
}

const planColumns: DataTableColumns<IntelPlanItem> = [
  {
    title: '任务', key: 'name', width: 230, fixed: 'left', ellipsis: { tooltip: true }, mobileTitle: true,
    render: row => row.name || `${row.group_name} · ${row.model}`,
  },
  { title: '分组', key: 'group_name', width: 140, ellipsis: { tooltip: true }, render: row => row.group_name || `#${row.route_group_id}` },
  { title: '模型', key: 'model', width: 200, ellipsis: { tooltip: true } },
  { title: '题型', key: 'question_kind', width: 90, render: row => kindTag(row.question_kind) },
  { title: '周期', key: 'interval_minutes', width: 105, render: row => describeInterval(row.interval_minutes) },
  { title: '近期正确率', key: 'stats', width: 130, mobileTag: true, render: row => accuracyTag(row) },
  { title: '最近运行', key: 'last_run', width: 210, render: row => runSummary(row) },
  {
    title: '自动', key: 'enabled', width: 80,
    render: row => h(UiSwitch, {
      value: row.enabled,
      size: 'small',
      onUpdateValue: (value: boolean) => toggleEnabled(row, value),
    }),
  },
  {
    title: '操作', key: 'actions', width: 240, fixed: 'right',
    render: row => h(UiSpace, { size: 6 }, {
      default: () => [
        h(UiButton, {
          size: 'tiny', type: 'primary', secondary: true,
          loading: row.running || actingIds.value.has(row.id),
          onClick: () => runNow(row),
        }, { default: () => '立即测试' }),
        h(UiButton, { size: 'tiny', secondary: true, onClick: () => openEdit(row) }, { default: () => '编辑' }),
        h(UiButton, { size: 'tiny', secondary: true, type: 'error', onClick: () => confirmDelete(row) }, { default: () => '删除' }),
      ],
    }),
  },
]

function rowProps(row: IntelPlanItem) {
  return {
    class: row.id === selectedId.value ? 'row-selected' : '',
    style: 'cursor: pointer',
    onClick: () => { selectedId.value = row.id },
  }
}

function toggleEnabled(row: IntelPlanItem, value: boolean) {
  row.enabled = value
  void updateIntelPlan(row.id, {
    name: row.name,
    route_group_id: row.route_group_id,
    model: row.model,
    question_kind: row.question_kind,
    prompt: row.prompt,
    protocol: row.protocol,
    interval_minutes: row.interval_minutes,
    parallel: row.parallel,
    enabled: value,
    quarantine_enabled: row.quarantine_enabled,
    quarantine_min_samples: row.quarantine_min_samples,
    quarantine_threshold: row.quarantine_threshold,
  }).then(() => {
    message.success(value ? '已启用自动测试' : '已停用自动测试')
  }).catch((e: unknown) => {
    row.enabled = !value
    message.error(errText(e, '操作失败'))
  })
}

async function runNow(row: IntelPlanItem) {
  actingIds.value.add(row.id)
  try {
    await runIntelPlan(row.id)
    message.success(`已开始测试（${INTEL_KIND_LABEL[row.question_kind]} · ${row.model}）`)
    selectedId.value = row.id
    await refreshDetail()
    tick.value++
  } catch (e) {
    message.error(errText(e, '发起测试失败'))
  } finally {
    actingIds.value.delete(row.id)
  }
}

function openCreate() {
  if (!groups.value.length) {
    message.error('请先在「API 密钥」页创建路由分组')
    return
  }
  editing.value = null
  modalShow.value = true
}

function openEdit(row: IntelPlanItem) {
  editing.value = row
  modalShow.value = true
}

function confirmDelete(row: IntelPlanItem) {
  dialog.warning({
    title: '删除测试任务',
    content: `确认删除「${row.name || `${row.group_name} · ${row.model}`}」？历史结果将一并删除。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await deleteIntelPlan(row.id)
        message.success('任务已删除')
        if (selectedId.value === row.id) selectedId.value = null
        await loadPlans(true)
      } catch (e) {
        message.error(errText(e, '删除失败'))
      }
    },
  })
}

function onSaved() {
  void loadPlans(true)
}

let pollTimer: number | null = null
function stopPolling() {
  if (pollTimer !== null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}
function startPolling() {
  if (pollTimer !== null) return
  pollTimer = window.setInterval(async () => {
    const before = JSON.stringify(plans.value.map(p => [p.id, p.running, p.last_run?.done, p.last_run?.status]))
    await loadPlans(true)
    const after = JSON.stringify(plans.value.map(p => [p.id, p.running, p.last_run?.done, p.last_run?.status]))
    if (before !== after) {
      tick.value++
      void loadRuns()
    }
  }, 3000)
}

onMounted(async () => {
  await Promise.all([loadPlans(), loadGroups()])
})

onBeforeUnmount(stopPolling)

watch(anyRunning, running => {
  stopPolling()
  if (running) startPolling()
}, { immediate: true })
watch(selectedId, () => {
  tick.value++
  void loadRuns()
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div class="head-text">
        <span class="eyebrow">测智 / INTELLIGENCE</span>
        <h2>测智</h2>
        <p>按分组与模型自动发起智力测试，每条结果绑定 分组 · 提供商 · Key。</p>
      </div>
      <div class="toolbar">
        <ui-button secondary :loading="refreshing" @click="refreshAll">刷新</ui-button>
        <ui-button type="primary" @click="openCreate">新建任务</ui-button>
      </div>
    </div>
    <ui-alert v-if="error" type="error" :bordered="false">{{ error }}</ui-alert>

    <ui-card size="small" :bordered="false">
      <template #header>测试任务</template>
      <template #header-extra><span class="muted">{{ plans.length }} 个任务</span></template>
      <ui-data-table
        :columns="planColumns"
        :data="plans"
        :loading="loading"
        :row-key="(row: IntelPlanItem) => row.id"
        :row-props="rowProps"
        size="small"
        :bordered="false"
        card
      >
        <template #empty>
          <div class="muted" style="padding: 30px 0">还没有测试任务，点击右上角「新建任务」开始</div>
        </template>
      </ui-data-table>
    </ui-card>

    <ui-card v-if="selected" size="small" :bordered="false" class="detail-card">
      <template #header>
        <span class="detail-title">
          {{ INTEL_KIND_LABEL[selected.question_kind] }} · {{ selected.name || `${selected.group_name} · ${selected.model}` }}
        </span>
      </template>
      <template #header-extra>
        <ui-space size="small">
          <ui-button size="small" :loading="selected.running" type="primary" secondary @click="runNow(selected)">立即测试</ui-button>
          <ui-button size="small" secondary @click="openEdit(selected)">编辑</ui-button>
          <ui-button size="small" secondary type="error" @click="confirmDelete(selected)">删除</ui-button>
        </ui-space>
      </template>
      <div class="detail-sub">
        <span class="meta-chip">分组 {{ selected.group_name }}</span>
        <span class="meta-chip">模型 {{ selected.model }}</span>
        <span class="meta-chip">{{ describeInterval(selected.interval_minutes) }}</span>
        <span class="meta-chip">并发 {{ selected.parallel }}</span>
        <span v-if="selected.stats.samples" class="meta-chip">
          近10轮正确率 {{ selected.stats.accuracy.toFixed(1) }}% · 平均 {{ selected.stats.avg_latency_ms ? formatDurationMs(Math.round(selected.stats.avg_latency_ms)) : '—' }}
        </span>
        <span v-if="selected.quarantined_count > 0" class="meta-chip quarantined-chip">隔离中 {{ selected.quarantined_count }}（已移出分组，退避复测中）</span>
        <span v-if="selected.next_run_at" class="meta-chip">下次自动 {{ formatTime(selected.next_run_at) }}</span>
      </div>
      <div v-if="recentRuns.length" class="runs-strip">
        <ui-tooltip v-for="run in recentRuns" :key="run.id" trigger="hover" placement="top">
          <template #trigger>
            <span class="run-chip" :class="[run.scope === 'quarantine' ? 'retest' : '', run.status === 'running' ? 'running' : run.success === run.total ? 'ok' : run.success === 0 ? 'bad' : 'mid']">
              {{ run.scope === 'quarantine' ? '复测 ' : '' }}{{ run.status === 'running' ? `${run.done}/${run.total}` : `${run.success}/${run.total}` }}
            </span>
          </template>
          <div class="tip">
            <div class="title">{{ formatTime(run.started_at) }}{{ run.scope === 'quarantine' ? ' · 隔离复测' : '' }}</div>
            <div>完成 {{ run.done }} / {{ run.total }}，通过 {{ run.success }}</div>
          </div>
        </ui-tooltip>
        <span class="muted runs-caption">最近 {{ recentRuns.length }} 轮（通过 / 总数）</span>
      </div>
      <CandyScoreboard v-if="selected.question_kind === 'candy'" :plan="selected" :tick="tick" />
      <PelicanGallery v-else :plan="selected" :tick="tick" />
    </ui-card>
  </div>

  <IntelPlanModal v-model:show="modalShow" :groups="groups" :editing="editing" @saved="onSaved" />
</template>

<style scoped>
.detail-card {
  margin-top: 16px;
}
.detail-title {
  font-size: 14px;
  font-weight: 650;
}
.detail-sub {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 14px;
}
.meta-chip {
  background: #f3f7ee;
  color: #546c58;
  font-size: 12px;
  border-radius: 6px;
  padding: 4px 10px;
}
.meta-chip.quarantined-chip {
  background: #f7ece4;
  color: #a16d50;
}
.runs-strip {
  display: flex;
  align-items: center;
  gap: 5px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.run-chip {
  min-width: 44px;
  text-align: center;
  font-size: 12px;
  font-weight: 600;
  border-radius: 5px;
  padding: 3px 7px;
  color: #ffffffd9;
  cursor: default;
}
.run-chip.ok { background: #7da36a; }
.run-chip.mid { background: #c4a35a; }
.run-chip.bad { background: #c28d70; }
.run-chip.running { background: #8ba394; animation: pulse 1.2s ease-in-out infinite; }
.run-chip.retest:not(.running) { background: #8ba394; }
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.55; }
}
.runs-caption {
  margin-left: 6px;
  font-size: 12px;
}
:deep(.row-selected) {
  background: #f3f7ee;
}
.tip .title {
  font-weight: 600;
  margin-bottom: 2px;
}
</style>
