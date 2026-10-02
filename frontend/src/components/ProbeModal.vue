<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { allPages, listKeys, probeKey, runProbes, type ProbeResult } from '@/api/admin'
import { errText } from '@/utils/format'

const props = defineProps<{
  show: boolean
  target: { name: string; key_id?: number; upstream_id?: number; models?: string[] }
}>()
const emit = defineEmits<{
  'update:show': [value: boolean]
  running: [value: boolean]
  completed: []
}>()

const deep = ref(true)
const model = ref<string | null>(null)
const protocol = ref<'' | 'openai' | 'anthropic'>('')
const prompt = ref('hi')
const models = ref<string[]>([])
const loadingModels = ref(false)
const running = ref(false)
const error = ref('')
const modelError = ref('')
const result = ref<ProbeResult | null>(null)
let loadSequence = 0
const modelOptions = computed(() => models.value.map(value => ({ label: value, value })))
const valid = computed(() => !deep.value || (prompt.value.trim().length > 0 && [...prompt.value].length <= 4000 && [...(model.value ?? '')].length <= 256))
const resultType = computed(() => {
  if (result.value?.success === false || (result.value?.failed ?? 0) > 0) return 'error'
  if (result.value?.skipped) return 'warning'
  return 'success'
})

watch(() => props.show, async show => {
  const sequence = ++loadSequence
  if (!show) return
  deep.value = true
  model.value = null
  protocol.value = ''
  prompt.value = 'hi'
  result.value = null
  error.value = ''
  modelError.value = ''
  models.value = props.target.models ?? []
  loadingModels.value = false
  if (props.target.key_id) return
  loadingModels.value = true
  try {
    const keys = await allPages(params => listKeys({ ...params, upstream_id: props.target.upstream_id }))
    if (sequence !== loadSequence) return
    models.value = [...new Set(keys.filter(k => k.status === 'enabled' && k.probe_enabled !== false).flatMap(k => k.last_models ?? []))].sort()
  } catch (e) {
    if (sequence === loadSequence) modelError.value = errText(e, '模型列表加载失败，可手动输入模型 ID')
  } finally {
    if (sequence === loadSequence) loadingModels.value = false
  }
})

function close(show: boolean) {
  if (!running.value) emit('update:show', show)
}

async function submit() {
  if (running.value || !valid.value) return
  running.value = true
  emit('running', true)
  error.value = ''
  result.value = null
  const options = deep.value ? {
    model: model.value?.trim() || undefined,
    prompt: prompt.value,
    protocol: protocol.value || undefined,
  } : {}
  try {
    result.value = props.target.key_id
      ? await probeKey(props.target.key_id, deep.value, options)
      : await runProbes({ upstream_id: props.target.upstream_id, deep: deep.value, ...options })
    emit('completed')
  } catch (e) {
    error.value = errText(e, '探测失败')
  } finally {
    running.value = false
    emit('running', false)
  }
}
</script>

<template>
  <ui-modal :show="show" preset="card" :title="`探测 · ${target.name}`"
    style="width: min(560px, calc(100vw - 24px))" :closable="!running" :mask-closable="!running"
    :close-on-esc="!running" @update:show="close">
    <ui-form label-placement="top" :disabled="running">
      <ui-form-item label="探测方式">
        <ui-radio-group v-model:value="deep">
          <ui-radio-button :value="true">对话探测</ui-radio-button>
          <ui-radio-button :value="false">轻量探测</ui-radio-button>
        </ui-radio-group>
      </ui-form-item>
      <template v-if="deep">
        <ui-form-item label="模型">
          <ui-select v-model:value="model" :options="modelOptions" :loading="loadingModels" filterable tag clearable
            placeholder="自动选择，或搜索 / 输入模型 ID" />
        </ui-form-item>
        <div v-if="modelError" class="muted" style="margin-bottom: 12px">{{ modelError }}</div>
        <ui-form-item label="接口协议">
          <ui-select v-model:value="protocol" :options="[
            { label: '自动（按 Key 能力和模型）', value: '' },
            { label: 'OpenAI', value: 'openai' },
            { label: 'Anthropic', value: 'anthropic' },
          ]" />
        </ui-form-item>
        <ui-form-item label="探测对话">
          <ui-input v-model:value="prompt" type="textarea" :autosize="{ minRows: 3, maxRows: 8 }"
            :maxlength="4000" show-count placeholder="例如：who are you" />
        </ui-form-item>
        <p class="muted">模型列表来自已获取的模型，也可手动输入。留空时自动选模；本次设置仅用于此次探测，最多生成 256 tokens。</p>
        <p v-if="!target.key_id" class="muted">指定模型和对话将用于范围内每把允许探测的启用 Key；不支持所选协议的 Key 会跳过。</p>
      </template>
      <p v-else class="muted">轻量探测检查模型或用量接口，不发送对话。</p>
    </ui-form>
    <ui-alert v-if="error" type="error" :bordered="false" style="margin-top: 12px">{{ error }}</ui-alert>
    <ui-alert v-if="result" :type="resultType" :bordered="false" style="margin-top: 12px">
      {{ result.message || result.error || '探测完成' }}
      <div v-if="result.model">模型：{{ result.model }} · {{ result.protocol }} · {{ result.path }}</div>
    </ui-alert>
    <template #footer>
      <ui-space justify="end">
        <ui-button :disabled="running" @click="close(false)">关闭</ui-button>
        <ui-button type="primary" :loading="running" :disabled="!valid" @click="submit">{{ result ? '再次探测' : '开始探测' }}</ui-button>
      </ui-space>
    </template>
  </ui-modal>
</template>
