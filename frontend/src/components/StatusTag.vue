<script setup lang="ts">
import { computed } from 'vue'
import { UiTag } from '@/components/ui'
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
  <ui-tag :type="meta.type" size="small" :bordered="false">{{ meta.label }}</ui-tag>
</template>
