<script setup lang="ts">
import { computed } from 'vue'
import type { IntelVerdict, IntelVerdictPoint } from '@/api/intel'
import { formatDurationMs, formatTime } from '@/utils/format'

const props = withDefaults(defineProps<{
  points?: IntelVerdictPoint[]
  max?: number
}>(), { max: 20 })

const LABEL: Record<string, string> = {
  correct: '正确',
  success: '通过',
  incorrect: '答错',
  invalid: '非 HTML',
  error: '出错',
}

const slots = computed(() => {
  const points = (props.points || []).slice(0, props.max)
  const empties = Math.max(0, props.max - points.length)
  return { points, empties }
})

function tone(verdict: IntelVerdict | string) {
  if (verdict === 'correct' || verdict === 'success') return 'ok'
  if (verdict === 'incorrect' || verdict === 'invalid') return 'warn'
  return 'bad'
}

function lines(point: IntelVerdictPoint) {
  const out = [formatTime(point.created_at)]
  out.push(LABEL[point.verdict] || point.verdict)
  if (point.answer_preview) out.push(`答案：${point.answer_preview}`)
  if (point.verdict === 'error' && point.error_message) out.push(point.error_message)
  out.push(`耗时 ${formatDurationMs(point.latency_ms)}`)
  return out
}
</script>

<template>
  <div class="vt">
    <span v-for="i in slots.empties" :key="`e-${i}`" class="vt-cell empty" aria-hidden="true" />
    <ui-tooltip v-for="(point, i) in slots.points" :key="`${point.id}-${i}`" trigger="hover" placement="top">
      <template #trigger>
        <span class="vt-cell" :class="tone(point.verdict)" />
      </template>
      <div class="tip">
        <div v-for="(line, li) in lines(point)" :key="li" :class="{ title: li === 0 }">{{ line }}</div>
      </div>
    </ui-tooltip>
  </div>
</template>

<style scoped>
.vt {
  display: flex;
  gap: 2px;
  min-width: 150px;
  height: 14px;
  align-items: stretch;
}
.vt-cell {
  flex: 1 1 0;
  min-width: 3px;
  width: 100%;
  height: 14px;
  border-radius: 2px;
}
.vt-cell.empty {
  background: #e5ebde;
}
.vt-cell.ok {
  background: #7da36a;
}
.vt-cell.warn {
  background: #c4a35a;
}
.vt-cell.bad {
  background: #c28d70;
}
.tip .title {
  font-weight: 600;
  margin-bottom: 2px;
}
</style>
