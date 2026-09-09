<script setup lang="ts">
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import { STATUS_LABEL, type EnableStatus } from '@/api/types'

const props = defineProps<{ status?: EnableStatus | string | null }>()

const meta = computed(() => {
  const on = props.status === 'enabled'
  return {
    type: on ? ('success' as const) : ('default' as const),
    label: STATUS_LABEL[(props.status as EnableStatus) || 'disabled'] ?? props.status ?? '—',
  }
})
</script>

<template>
  <n-tag :type="meta.type" size="small" :bordered="false">{{ meta.label }}</n-tag>
</template>
