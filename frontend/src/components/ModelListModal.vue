<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NEmpty, NInput, NModal, NTag, useMessage } from 'naive-ui'
import { fetchKeyModels } from '@/api/admin'
import type { PlatformKey } from '@/api/types'
import { errText, formatTime } from '@/utils/format'

/**
 * Shows the model ids last fetched from a platform key's upstream, with a
 * search box and a "re-fetch" action. The parent passes the key row and gets
 * the updated row back through `updated`.
 */
const props = defineProps<{
  show: boolean
  row: PlatformKey | null
}>()
const emit = defineEmits<{
  (e: 'update:show', v: boolean): void
  (e: 'updated', row: PlatformKey): void
}>()

const message = useMessage()
const query = ref('')
const fetching = ref(false)

watch(
  () => props.show,
  (v) => {
    if (v) query.value = ''
  },
)

const models = computed(() => props.row?.last_models ?? [])
const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return models.value
  return models.value.filter((m) => m.toLowerCase().includes(q))
})

const title = computed(() => {
  if (!props.row) return '模型列表'
  const up = props.row.upstream_name ? `${props.row.upstream_name} · ` : ''
  return `${up}${props.row.name} 的模型`
})

async function refetch() {
  if (!props.row) return
  fetching.value = true
  try {
    const out = await fetchKeyModels(props.row.id)
    const next: PlatformKey = {
      ...props.row,
      last_models: out.models,
      last_models_at: out.fetched_at,
      models_count: out.count,
    }
    emit('updated', next)
    message.success(out.message || `获取到 ${out.count} 个模型`)
  } catch (e) {
    message.error(errText(e, '获取失败'))
  } finally {
    fetching.value = false
  }
}

function copyAll() {
  if (!models.value.length) return
  void navigator.clipboard?.writeText(models.value.join('\n')).then(
    () => message.success('已复制'),
    () => message.error('复制失败'),
  )
}
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    :title="title"
    style="width: 640px"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <div class="head">
      <n-input v-model:value="query" size="small" clearable placeholder="搜索模型 id" style="max-width: 280px" />
      <span class="muted">
        共 {{ models.length }} 个
        <template v-if="row?.last_models_at"> · 获取于 {{ formatTime(row.last_models_at) }}</template>
      </span>
      <span style="flex: 1 1 auto" />
      <n-button size="small" quaternary :disabled="!models.length" @click="copyAll">复制全部</n-button>
      <n-button size="small" type="primary" secondary :loading="fetching" @click="refetch">重新获取</n-button>
    </div>
    <n-empty
      v-if="!models.length"
      description="还没有获取过模型，点「重新获取」向提供商请求 GET /v1/models"
      style="padding: 24px 0"
    />
    <n-empty v-else-if="!filtered.length" description="没有匹配的模型" style="padding: 24px 0" />
    <div v-else class="list">
      <n-tag v-for="m in filtered" :key="m" size="small" :bordered="false" class="model">{{ m }}</n-tag>
    </div>
    <p class="muted hint">
      调度时若该 Key 的模型列表非空，请求的 model 不在列表内则跳过该 Key；列表为空不过滤。可在「调度」页关闭该规则。
    </p>
  </n-modal>
</template>

<style scoped>
.head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  max-height: 420px;
  overflow: auto;
  padding: 2px;
}
.model {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
.muted {
  color: #667085;
  font-size: 12px;
}
.hint {
  margin: 12px 0 0;
}
</style>
