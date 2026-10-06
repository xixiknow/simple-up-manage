<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useMessage } from '@/components/ui'
import { allPages, listKeys, probeKey, runProbes, type ProbeResult } from '@/api/admin'
import { copyText, errText } from '@/utils/format'

const props = defineProps<{
  show: boolean
  target: { name: string; key_id?: number; upstream_id?: number; models?: string[] }
}>()
const emit = defineEmits<{
  'update:show': [value: boolean]
  running: [value: boolean]
  completed: []
}>()

const message = useMessage()
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
const expanded = ref<Set<number>>(new Set())
let loadSequence = 0
const modelOptions = computed(() => models.value.map(value => ({ label: value, value })))
const valid = computed(() => !deep.value || (prompt.value.trim().length > 0 && [...prompt.value].length <= 4000 && [...(model.value ?? '')].length <= 256))
const resultType = computed(() => {
  if (result.value?.success === false || (result.value?.failed ?? 0) > 0) return 'error'
  if (result.value?.skipped) return 'warning'
  return 'success'
})
const batchResults = computed(() => result.value?.results ?? [])

function toggleResult(keyId: number) {
  const next = new Set(expanded.value)
  if (next.has(keyId)) next.delete(keyId)
  else next.add(keyId)
  expanded.value = next
}

function outcomeState(r: ProbeResult) {
  if (r.skipped) return '跳过'
  if (r.success) return '成功'
  return '失败'
}

function stateClass(r: ProbeResult) {
  if (r.skipped) return 'skip'
  if (r.success) return 'ok'
  return 'bad'
}

function outcomeText(r: ProbeResult) {
  return r.reply || r.error || r.message || '（无响应内容）'
}

async function copyReply() {
  const text = result.value?.reply || ''
  if (!text) return
  const ok = await copyText(text)
  if (ok) message.success('已复制回复内容')
  else message.error('复制失败')
}

watch(() => props.show, async show => {
  const sequence = ++loadSequence
  if (!show) return
  deep.value = true
  model.value = null
  protocol.value = ''
  prompt.value = 'hi'
  result.value = null
  expanded.value = new Set()
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
    const res = props.target.key_id
      ? await probeKey(props.target.key_id, deep.value, options)
      : await runProbes({ upstream_id: props.target.upstream_id, deep: deep.value, ...options })
    result.value = res
    // 少量批量结果默认全部展开，方便直接阅读每把 Key 的响应内容
    expanded.value = res.results && res.results.length > 0 && res.results.length <= 5
      ? new Set(res.results.map(r => r.key_id ?? -1))
      : new Set()
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
      <div v-if="result.models">共 {{ result.models }} 个模型</div>
    </ui-alert>
    <div v-if="result?.reply" class="probe-content">
      <div class="probe-content-head">
        <span>回复内容</span>
        <ui-button size="tiny" quaternary @click="copyReply">复制</ui-button>
      </div>
      <pre class="probe-content-body">{{ result.reply }}</pre>
    </div>
    <ul v-if="batchResults.length" class="probe-outcomes">
      <li v-for="r in batchResults" :key="r.key_id" class="probe-outcome">
        <button type="button" class="probe-outcome-head" @click="r.key_id != null && toggleResult(r.key_id)">
          <span class="probe-state" :class="stateClass(r)">{{ outcomeState(r) }}</span>
          <span class="probe-outcome-name">{{ r.key_name || (r.key_id != null ? `Key #${r.key_id}` : 'Key') }}</span>
          <span class="muted">{{ r.latency_ms != null ? `${r.latency_ms}ms` : '' }}</span>
          <span v-if="r.key_id != null" class="probe-caret">{{ expanded.has(r.key_id) ? '收起' : '展开' }}</span>
        </button>
        <pre v-if="r.key_id != null && expanded.has(r.key_id)" class="probe-content-body">{{ outcomeText(r) }}</pre>
      </li>
    </ul>
    <template #footer>
      <ui-space justify="end">
        <ui-button :disabled="running" @click="close(false)">关闭</ui-button>
        <ui-button type="primary" :loading="running" :disabled="!valid" @click="submit">{{ result ? '再次探测' : '开始探测' }}</ui-button>
      </ui-space>
    </template>
  </ui-modal>
</template>

<style scoped>
.probe-content {
  margin-top: 12px;
}

.probe-content-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
  color: var(--muted);
  font-size: 12px;
  font-weight: 600;
}

.probe-content-body {
  margin: 0;
  max-height: 240px;
  overflow: auto;
  padding: 10px 12px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--row);
  color: var(--ink);
  font-family: inherit;
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

.probe-outcomes {
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 12px 0 0;
  padding: 0;
}

.probe-outcome {
  border: 1px solid var(--line);
  border-radius: 8px;
  overflow: hidden;
}

.probe-outcome-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 10px;
  border: none;
  background: #fbfcf8;
  cursor: pointer;
  font-size: 12px;
  text-align: left;
}

.probe-state {
  font-weight: 650;
}

.probe-state.ok {
  color: var(--ok);
}

.probe-state.bad {
  color: var(--bad);
}

.probe-state.skip {
  color: var(--warn);
}

.probe-outcome-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  color: var(--ink);
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.probe-caret {
  color: var(--muted);
}

.probe-outcome .probe-content-body {
  border: none;
  border-top: 1px solid var(--line);
  border-radius: 0;
}
</style>
