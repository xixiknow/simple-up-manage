<script setup lang="ts">
import { computed, ref } from 'vue'
import { NPopselect, NTag, useMessage } from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import { setKeyRouteGroups } from '@/api/admin'
import type { RouteGroup, RouteGroupRef } from '@/api/types'
import { errText } from '@/utils/format'

/**
 * Inline route-group tags for a platform key row. Clicking opens a multi-select
 * popover so membership can be changed without leaving the list.
 */
const props = defineProps<{
  keyId: number
  groups?: RouteGroupRef[] | null
  options: RouteGroup[]
}>()
const emit = defineEmits<{ (e: 'updated', groups: RouteGroupRef[]): void }>()

const message = useMessage()
const saving = ref(false)
const show = ref(false)

const current = computed(() => (props.groups ?? []).map((g) => g.id))

const selectOptions = computed<SelectOption[]>(() =>
  props.options.map((g) => ({
    label: `${g.name}${g.protocol ? ` · ${g.protocol}` : ''}${g.status !== 'enabled' ? ' · 停用' : ''}`,
    value: g.id,
  })),
)

async function onChange(value: number[]) {
  saving.value = true
  try {
    const updated = await setKeyRouteGroups(props.keyId, value)
    emit('updated', updated.route_groups ?? [])
    message.success('已更新路由分组')
  } catch (e) {
    message.error(errText(e, '更新失败'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <n-popselect
    v-model:show="show"
    :value="current"
    :options="selectOptions"
    multiple
    scrollable
    trigger="click"
    placement="bottom-start"
    :disabled="saving"
    @update:value="onChange"
  >
    <div class="rg-tags" :class="{ saving }" title="点击修改路由分组">
      <template v-if="groups && groups.length">
        <n-tag v-for="g in groups" :key="g.id" size="tiny" :bordered="false" type="info">{{ g.name }}</n-tag>
      </template>
      <n-tag v-else size="tiny" :bordered="false" class="unassigned">未分组</n-tag>
    </div>
    <template #empty>
      <div class="rg-empty">还没有分组，先去「API 密钥」页新建</div>
    </template>
  </n-popselect>
</template>

<style scoped>
.rg-tags {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
  cursor: pointer;
  min-height: 20px;
  align-items: center;
  padding: 1px 2px;
  border-radius: 4px;
}
.rg-tags:hover {
  background: #f2f4f7;
}
.rg-tags.saving {
  opacity: 0.5;
  pointer-events: none;
}
.unassigned {
  color: #98a2b3;
  border: 1px dashed #d0d5dd !important;
  background: transparent !important;
}
.rg-empty {
  padding: 8px 12px;
  font-size: 12px;
  color: #667085;
}
</style>
