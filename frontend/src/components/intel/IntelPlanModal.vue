<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import type { FormInst, FormRules, SelectOption } from '@/components/ui'
import { allPages, listKeys } from '@/api/admin'
import {
  CANDY_CONTRACT,
  CANDY_PROMPT,
  INTEL_INTERVAL_OPTIONS,
  PELICAN_CONTRACT,
  PELICAN_PROMPT,
  createIntelPlan,
  updateIntelPlan,
  type IntelPlanItem,
  type IntelQuestionKind,
  type IntelTestPlan,
} from '@/api/intel'
import type { RouteGroup } from '@/api/types'
import { errText } from '@/utils/format'

const props = defineProps<{
  show: boolean
  groups: RouteGroup[]
  editing: IntelPlanItem | null
}>()
const emit = defineEmits<{
  'update:show': [value: boolean]
  saved: [plan: IntelTestPlan]
}>()

const message = useMessage()
const formRef = ref<FormInst | null>(null)
const saving = ref(false)
const loadingModels = ref(false)
const modelOptions = ref<SelectOption[]>([])

const KIND_OPTIONS: Array<{ label: string; value: IntelQuestionKind; hint: string }> = [
  { label: '糖果题', value: 'candy', hint: '数学推理题，判定答案对错，结果进成绩榜' },
  { label: '鹈鹕题', value: 'pelican', hint: '生成 SVG 动画作品，成功作品进画廊' },
]

const form = reactive({
  name: '',
  route_group_id: null as number | null,
  model: null as string | null,
  question_kind: 'candy' as IntelQuestionKind,
  prompt: CANDY_PROMPT + '\n\n' + CANDY_CONTRACT,
  protocol: '',
  interval_minutes: 0,
  parallel: 4,
  enabled: true,
  quarantine_enabled: false,
  quarantine_min_samples: 3,
  quarantine_threshold: 50,
})

const INTEL_MIN_SAMPLE_OPTIONS = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10].map(n => ({ label: `${n} 次`, value: n }))
const INTEL_THRESHOLD_OPTIONS = [30, 40, 50, 60, 70, 80].map(n => ({ label: `${n}%`, value: n }))

const rules: FormRules = {
  route_group_id: { required: true, type: 'number', message: '请选择分组', trigger: 'change' },
  model: { required: true, message: '请选择或输入模型 ID', trigger: ['change', 'blur'] },
}

const defaultPrompt = (kind: IntelQuestionKind) =>
  kind === 'pelican' ? `${PELICAN_PROMPT}\n\n${PELICAN_CONTRACT}` : `${CANDY_PROMPT}\n\n${CANDY_CONTRACT}`

const groupOptions = computed<SelectOption[]>(() =>
  props.groups.map(g => ({ label: g.name, value: g.id })),
)

let loadSequence = 0
async function loadGroupModels(groupId: number | null) {
  const sequence = ++loadSequence
  if (!groupId) {
    modelOptions.value = []
    return
  }
  loadingModels.value = true
  try {
    const keys = await allPages(params => listKeys({ ...params, route_group_id: groupId }))
    if (sequence !== loadSequence) return
    modelOptions.value = [...new Set(keys.flatMap(k => k.last_models ?? []))].sort()
      .map(value => ({ label: value, value }))
  } catch {
    if (sequence === loadSequence) modelOptions.value = []
  } finally {
    if (sequence === loadSequence) loadingModels.value = false
  }
}

watch(() => props.show, show => {
  if (!show) return
  const editing = props.editing
  if (editing) {
    form.name = editing.name
    form.route_group_id = editing.route_group_id
    form.model = editing.model
    form.question_kind = editing.question_kind
    form.prompt = editing.prompt || defaultPrompt(editing.question_kind)
    form.protocol = editing.protocol || ''
    form.interval_minutes = editing.interval_minutes || 0
    form.parallel = editing.parallel || 4
    form.enabled = editing.enabled
    form.quarantine_enabled = editing.quarantine_enabled
    form.quarantine_min_samples = editing.quarantine_min_samples || 3
    form.quarantine_threshold = editing.quarantine_threshold || 50
    void loadGroupModels(editing.route_group_id)
  } else {
    form.name = ''
    form.route_group_id = props.groups.length === 1 ? props.groups[0].id : null
    form.model = null
    form.question_kind = 'candy'
    form.prompt = defaultPrompt('candy')
    form.protocol = ''
    form.interval_minutes = 0
    form.parallel = 4
    form.enabled = true
    form.quarantine_enabled = false
    form.quarantine_min_samples = 3
    form.quarantine_threshold = 50
    modelOptions.value = []
    void loadGroupModels(form.route_group_id)
  }
})

watch(() => form.question_kind, kind => {
  form.prompt = defaultPrompt(kind)
  if (kind !== 'candy') form.quarantine_enabled = false
})

function onGroupChange(groupId: number | null) {
  // v-model has already assigned the new value; only user-driven changes clear
  // the model, so restoring an editing plan keeps its model selection.
  form.model = null
  void loadGroupModels(groupId)
}

function close(show: boolean) {
  if (!saving.value) emit('update:show', show)
}

async function submit() {
  const valid = await formRef.value?.validate().then(() => true).catch(() => false)
  if (!valid || saving.value || !form.route_group_id) return
  saving.value = true
  const payload = {
    name: form.name.trim(),
    route_group_id: form.route_group_id,
    model: (form.model ?? '').trim(),
    question_kind: form.question_kind,
    prompt: form.prompt.trim(),
    protocol: form.protocol as '' | 'openai' | 'anthropic',
    interval_minutes: form.interval_minutes,
    parallel: form.parallel,
    enabled: form.enabled,
    quarantine_enabled: form.quarantine_enabled && form.question_kind === 'candy',
    quarantine_min_samples: form.quarantine_min_samples,
    quarantine_threshold: form.quarantine_threshold,
  }
  try {
    const saved = props.editing
      ? await updateIntelPlan(props.editing.id, payload)
      : await createIntelPlan(payload)
    message.success(props.editing ? '任务已更新' : '任务已创建')
    emit('saved', saved)
    emit('update:show', false)
  } catch (e) {
    message.error(errText(e, props.editing ? '任务更新失败' : '任务创建失败'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <ui-modal :show="show" preset="card" :title="editing ? '编辑测试任务' : '新建测试任务'"
    style="width: min(640px, calc(100vw - 24px))" :closable="!saving" :mask-closable="!saving"
    :close-on-esc="!saving" @update:show="close">
    <ui-form ref="formRef" label-placement="top" :model="form" :rules="rules" :disabled="saving">
      <ui-form-item label="任务名称（可选）">
        <ui-input v-model:value="form.name" placeholder="留空时按 分组·模型·题型 展示" :maxlength="128" />
      </ui-form-item>
      <ui-form-item label="分组" path="route_group_id">
        <ui-select v-model:value="form.route_group_id" :options="groupOptions" filterable
          placeholder="选择要测试的路由分组" @update:value="onGroupChange" />
      </ui-form-item>
      <ui-form-item label="模型" path="model">
        <ui-select v-model:value="form.model" :options="modelOptions" :loading="loadingModels" filterable tag
          placeholder="选择或直接输入模型 ID" />
      </ui-form-item>
      <ui-form-item label="题型">
        <ui-radio-group v-model:value="form.question_kind">
          <ui-radio-button v-for="kind in KIND_OPTIONS" :key="kind.value" :value="kind.value">{{ kind.label }}</ui-radio-button>
        </ui-radio-group>
        <p class="muted">{{ KIND_OPTIONS.find(k => k.value === form.question_kind)?.hint }}</p>
      </ui-form-item>
      <ui-form-item label="题目提示词">
        <ui-input v-model:value="form.prompt" type="textarea" :autosize="{ minRows: 3, maxRows: 8 }"
          placeholder="题目提示词" />
        <p class="muted">已预填默认题目与交付约定，可整段替换为自定义题目；判分规则不受提示词影响。</p>
      </ui-form-item>
      <ui-form-item label="测试周期">
        <ui-select v-model:value="form.interval_minutes" :options="INTEL_INTERVAL_OPTIONS" />
        <p class="muted">选择周期后按计划自动执行（首次在下个周期）；仅手动时随时点「立即测试」。</p>
      </ui-form-item>
      <ui-form-item label="接口协议">
        <ui-select v-model:value="form.protocol" :options="[
          { label: '自动（按 Key 能力）', value: '' },
          { label: 'OpenAI', value: 'openai' },
          { label: 'Anthropic', value: 'anthropic' },
        ]" />
      </ui-form-item>
      <ui-form-item label="并发数">
        <ui-select v-model:value="form.parallel" :options="[1, 2, 3, 4, 5, 6, 7, 8].map(n => ({ label: `${n}`, value: n }))" />
        <p class="muted">同时向多少把 Key 发送测试请求；仅覆盖分组内启用且支持该模型的 Key。</p>
      </ui-form-item>
      <ui-form-item label="启用">
        <ui-switch v-model:value="form.enabled" />
        <span class="muted" style="margin-left: 8px">停用后保留历史结果，不再自动执行</span>
      </ui-form-item>
      <ui-form-item v-if="form.question_kind === 'candy'" label="自动隔离（答错自动移出分组）">
        <ui-switch v-model:value="form.quarantine_enabled" />
        <p class="muted quarantine-rules">
          开启后：以分组内 Key 最近
          <b>{{ form.quarantine_min_samples }}</b> 次有效测试为判定窗口，窗口内正确率未达到
          <b>{{ form.quarantine_threshold }}%</b> 即自动移出分组（全部答对不会移除）；
          隔离后约 1 分钟内开始复测，未通过则间隔逐次翻倍（最长 30 分钟），答对一次立即回到快速确认，
          连续两次答对自动加回分组。传输错误不计入窗口，网络问题不会导致移除。关闭开关不会自动放行已隔离的 Key。
        </p>
        <div class="quarantine-rule-row">
          <span class="rule-label">最少样本数</span>
          <ui-select v-model:value="form.quarantine_min_samples" :options="INTEL_MIN_SAMPLE_OPTIONS" style="width: 110px" />
          <span class="rule-label">正确率阈值</span>
          <ui-select v-model:value="form.quarantine_threshold" :options="INTEL_THRESHOLD_OPTIONS" style="width: 110px" />
        </div>
      </ui-form-item>
    </ui-form>
    <template #footer>
      <ui-space justify="end">
        <ui-button :disabled="saving" @click="close(false)">取消</ui-button>
        <ui-button type="primary" :loading="saving" @click="submit">{{ editing ? '保存' : '创建' }}</ui-button>
      </ui-space>
    </template>
  </ui-modal>
</template>

<style scoped>
.quarantine-rules {
  margin-top: 6px;
  line-height: 1.7;
}
.quarantine-rule-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 8px;
  flex-wrap: wrap;
}
.rule-label {
  font-size: 12px;
  color: #546c58;
}
</style>
