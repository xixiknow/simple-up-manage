<script setup lang="ts">
import { computed } from 'vue'
import type { HealthPulseCell, PulseState } from '@/api/types'
import { formatDurationMs, formatTime } from '@/utils/format'

const props = defineProps<{
  cells?: HealthPulseCell[] | null
  lastProbeAt?: string | null
}>()

const labels: Record<PulseState, string> = {
  ok: '正常',
  degraded: '降级',
  mix: '失败',
  bad: '失败',
  empty: '无数据',
}

const cells = computed(() => (props.cells || []).slice(-60))
const emptyCount = computed(() => 60 - cells.value.length)

function cellClass(cell: HealthPulseCell) {
  if (cell.state === 'empty' || (cell.ok || 0) + (cell.fail || 0) === 0) return 'empty'
  if (cell.state === 'bad' || cell.state === 'mix') return 'bad'
  if (cell.state === 'degraded') return 'degraded'
  return 'ok'
}

function lines(cell: HealthPulseCell) {
  const total = (cell.ok || 0) + (cell.fail || 0)
  const out = [formatTime(cell.start)]
  if (cell.state === 'empty' || total === 0) {
    out.push('无探测/请求')
    return out
  }
  out.push(`${labels[cell.state] || cell.state} · 成功 ${cell.ok} / 失败 ${cell.fail}`)
  out.push(`耗时 ${formatDurationMs(cell.last_latency_ms)}`)
  if (total > 1 && cell.latency_p50_ms) {
    out.push(`耗时 p50 ${formatDurationMs(cell.latency_p50_ms)}`)
  }
  return out
}
</script>

<template>
  <div class="pulse">
    <span
      v-for="i in emptyCount"
      :key="`placeholder-${i}`"
      class="pulse-cell empty"
      aria-hidden="true"
    />
    <n-tooltip
      v-for="(cell, i) in cells"
      :key="`${cell.start}-${i}`"
      trigger="hover"
      placement="top"
    >
      <template #trigger>
        <span class="pulse-cell" :class="cellClass(cell)" />
      </template>
      <div class="tip">
        <div v-for="(line, li) in lines(cell)" :key="li" :class="{ title: li === 0 }">{{ line }}</div>
      </div>
    </n-tooltip>
  </div>
</template>

<style scoped>
.pulse {
  display: grid;
  grid-template-columns: repeat(60, minmax(2px, 1fr));
  align-items: stretch;
  gap: 1px;
  min-width: 180px;
  height: 18px;
}
.pulse :deep(.n-tooltip-trigger) {
  flex: 1 1 0;
  min-width: 2px;
  display: flex;
}
.pulse-cell {
  flex: 1 1 0;
  width: 100%;
  min-width: 2px;
  height: 18px;
  border-radius: 1px;
}
.pulse-cell.empty {
  background: #e5e7eb;
}
.pulse-cell.ok {
  background: #10b981;
}
.pulse-cell.degraded {
  background: #f59e0b;
}
.pulse-cell.bad {
  background: #ef4444;
}
.tip .title {
  font-weight: 600;
  margin-bottom: 2px;
}
</style>
