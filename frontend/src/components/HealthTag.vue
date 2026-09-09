<script setup lang="ts">
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import { HEALTH_LABEL, type HealthStatus } from '@/api/types'

const props = defineProps<{ status?: HealthStatus | string | null }>()

const meta = computed(() => {
  const key = (props.status || 'down') as HealthStatus
  const map: Record<HealthStatus, 'success' | 'warning' | 'error' | 'info' | 'default'> = {
    healthy: 'success',
    degraded: 'warning',
    down: 'error',
    cooldown: 'info',
    low_balance: 'warning',
    disabled: 'default',
  }
  return {
    type: map[key] ?? 'default',
    label: HEALTH_LABEL[key] ?? props.status ?? '未知',
  }
})
</script>

<template>
  <n-tag :type="meta.type" size="small" :bordered="false">{{ meta.label }}</n-tag>
</template>
