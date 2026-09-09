<script setup lang="ts">
import { computed } from 'vue'
import type { HealthPulseCell, PulseState } from '@/api/types'
import { formatDurationMs } from '@/utils/format'

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

const cells = computed(() => props.cells || [])

function formatRange(start: string) {
  const d = new Date(start)
  if (Number.isNaN(d.getTime())) return start
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function cellClass(cell: HealthPulseCell) {
  if (cell.state === 'empty' || (cell.ok || 0) + (cell.fail || 0) === 0) return 'empty'
  if (cell.state === 'bad' || cell.state === 'mix') return 'bad'
  if (cell.state === 'degraded') return 'degraded'
  return 'ok'
}

function lines(cell: HealthPulseCell) {
  const total = (cell.ok || 0) + (cell.fail || 0)
  const out = [formatRange(cell.start)]
  if (cell.state === 'empty' || total === 0) {
    out.push('无探测/请求')
    return out
  }
  out.push(`${labels[cell.state] || cell.state} · 成功 ${cell.ok} / 失败 ${cell.fail}`)
  out.push(`最近耗时 ${formatDurationMs(cell.last_latency_ms)}`)
  if (total > 1 && cell.latency_p50_ms) {
    out.push(`耗时 p50 ${formatDurationMs(cell.latency_p50_ms)}`)
  }
  return out
}
</script>

<template>
  <div class="pulse">
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
    <span v-if="!cells.length" class="pulse-empty">暂无历史</span>
  </div>
</template>

<style scoped>
.pulse {
  display: flex;
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
.pulse-empty {
  color: #98a2b3;
  font-size: 12px;
}
.tip .title {
  font-weight: 600;
  margin-bottom: 2px;
}
</style>
