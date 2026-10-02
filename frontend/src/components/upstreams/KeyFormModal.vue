<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { createKey, updateKey } from '@/api/admin'
import { BILLING_KINDS, PROTOCOL_LABEL, STATUS_OPTIONS, type EnableStatus, type PlatformKey, type Protocol, type Upstream } from '@/api/types'
import { composeKeyName, errText, inferNameTag } from '@/utils/format'
import type { FormInst, FormRules } from '@/components/ui'
import { useMessage } from '@/components/ui'

const props = defineProps<{ show: boolean; provider: Upstream | null; editing: PlatformKey | null }>()
const emit = defineEmits<{ 'update:show': [show: boolean]; saved: [] }>()

const message = useMessage()
const saving = ref(false)
const formRef = ref<FormInst | null>(null)
const form = reactive({
  inherit_protocols: true,
  protocols: [] as Protocol[],
  name_tag: '',
  api_key: '',
  rate_multiplier: 1 as number | null,
  billing_group: '',
  probe_interval_sec: null as number | null,
  rpm_limit: 0 as number | null,
  max_concurrency: 0 as number | null,
  probe_enabled: true,
  status: 'enabled' as EnableStatus,
})
const rules: FormRules = {
  name_tag: { required: true, message: '请输入标识', trigger: 'blur' },
}
const isNewAPI = computed(() => props.provider?.kind === 'new_api')
const canSync = computed(() => !!props.provider && BILLING_KINDS.includes(props.provider.kind))
const namePreview = computed(() => composeKeyName(props.provider?.name || '', form.name_tag, form.rate_multiplier))

function reset() {
  const up = props.provider
  const row = props.editing
  if (!up) return
  Object.assign(form, row
    ? {
        inherit_protocols: !row.protocols?.length,
        protocols: [...(row.protocols?.length ? row.protocols : up.protocols || [])],
        name_tag: row.name_tag || inferNameTag(row.name, up.name),
        api_key: '',
        rate_multiplier: row.rate_multiplier ?? 1,
        billing_group: row.billing_group || '',
        probe_interval_sec: row.probe_interval_sec && row.probe_interval_sec > 0 ? row.probe_interval_sec : null,
        rpm_limit: row.rpm_limit ?? 0,
        max_concurrency: row.max_concurrency ?? 0,
        probe_enabled: row.probe_enabled !== false,
        status: row.status,
      }
    : {
        inherit_protocols: true,
        protocols: [...up.protocols],
        name_tag: '',
        api_key: '',
        rate_multiplier: null,
        billing_group: '',
        probe_interval_sec: null,
        rpm_limit: 0,
        max_concurrency: 0,
        probe_enabled: true,
        status: 'enabled' as EnableStatus,
      })
}

watch(() => props.show, (open) => { if (open) reset() })

async function save() {
  await formRef.value?.validate()
  if (!props.editing && !form.api_key.trim()) {
    message.warning('新建时必须填写 API Key')
    return
  }
  const up = props.provider
  if (!up) return
  if (!form.inherit_protocols && !form.protocols.length) {
    message.warning('至少选择一种协议')
    return
  }
  saving.value = true
  try {
    const payload = {
      protocols: form.inherit_protocols ? [] : form.protocols,
      name_tag: form.name_tag.trim(),
      api_key: form.api_key.trim() || undefined,
      billing_group: isNewAPI.value ? form.billing_group.trim() : '',
      status: form.status,
      rate_multiplier: form.rate_multiplier ?? undefined,
      probe_interval_sec: Number(form.probe_interval_sec) || 0,
      probe_enabled: form.probe_enabled,
      rpm_limit: Number(form.rpm_limit) || 0,
      max_concurrency: Number(form.max_concurrency) || 0,
    }
    if (props.editing) await updateKey(props.editing.id, payload)
    else await createKey(up.id, payload)
    message.success('已保存')
    emit('update:show', false)
    emit('saved')
  } catch (e) {
    message.error(errText(e, '保存失败'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <ui-modal :show="props.show" preset="card" :title="props.editing ? '编辑 Key' : `添加 Key · ${props.provider?.name || ''}`" style="width: min(520px, calc(100vw - 24px))" @update:show="emit('update:show', $event)">
    <ui-form ref="formRef" :model="form" :rules="rules" label-placement="left" label-width="100">
      <ui-form-item label="标识" path="name_tag">
        <div class="rate-field">
          <ui-input v-model:value="form.name_tag" placeholder="例如 稳定" maxlength="64" />
          <div class="muted rate-hint">完整名称固定为「提供商-标识-倍率」：{{ namePreview }}</div>
        </div>
      </ui-form-item>
      <ui-form-item label="API Key" path="api_key">
        <ui-input
          v-model:value="form.api_key"
          type="password"
          show-password-on="click"
          :placeholder="props.editing ? '留空则不修改' : '仅此次提交，列表不会回显'"
        />
      </ui-form-item>
      <ui-form-item label="继承提供商协议">
        <ui-switch v-model:value="form.inherit_protocols" />
      </ui-form-item>
      <ui-form-item v-if="!form.inherit_protocols" label="协议" path="protocols">
        <ui-checkbox-group v-model:value="form.protocols">
          <ui-space>
            <ui-checkbox v-for="p in props.provider?.protocols || []" :key="p" :value="p">{{ PROTOCOL_LABEL[p] }}</ui-checkbox>
          </ui-space>
        </ui-checkbox-group>
      </ui-form-item>
      <ui-form-item label="倍率" path="rate_multiplier">
        <div class="rate-field">
          <ui-input-number
            v-model:value="form.rate_multiplier"
            :min="0"
            :max="1000"
            :step="0.01"
            :precision="4"
            placeholder="可不填，保存后自动同步"
            style="width: 100%"
            clearable
          />
          <div class="muted rate-hint">
            <template v-if="canSync">可不填。保存后自动拉一次倍率、模型，并刷新该提供商余额；之后每分钟同步。</template>
            <template v-else>可不填，默认 1。保存后仍会拉取模型，并刷新该提供商余额。</template>
          </div>
        </div>
      </ui-form-item>
      <ui-form-item v-if="isNewAPI" label="new-api 分组" path="billing_group">
        <div class="rate-field">
          <ui-input v-model:value="form.billing_group" placeholder="default" />
          <div class="muted rate-hint">令牌在 new-api 上所属的分组名，「同步倍率」按此名在 /api/pricing 的 group_ratio 中取值。</div>
        </div>
      </ui-form-item>
      <ui-form-item label="探测间隔" path="probe_interval_sec">
        <div class="rate-field">
          <ui-input-number
            v-model:value="form.probe_interval_sec"
            :min="0"
            :step="60"
            placeholder="0 跟随全局"
            style="width: 100%"
            clearable
            :disabled="!form.probe_enabled"
          />
          <div class="muted rate-hint">单位秒。留空或 0 跟随全局定时任务（默认 1 分钟）。关闭探测后仍保留该间隔，重新打开后按原窗口调度。</div>
        </div>
      </ui-form-item>
      <ui-form-item label="允许探测">
        <div class="rate-field">
          <ui-switch v-model:value="form.probe_enabled" />
          <div class="muted rate-hint">关闭后跳过平台探测，以及已识别的外部算术和 health-manager 探测。普通业务和工具调用仍可使用此 Key；余额、倍率和模型操作不受影响。</div>
        </div>
      </ui-form-item>
      <ui-form-item label="RPM 上限" path="rpm_limit">
        <div class="rate-field">
          <ui-input-number v-model:value="form.rpm_limit" :min="0" :step="1" style="width: 100%" />
          <div class="muted rate-hint">该 Key 每分钟最多接收的请求数，0 表示不限。</div>
        </div>
      </ui-form-item>
      <ui-form-item label="Key 并发" path="max_concurrency">
        <div class="rate-field">
          <ui-input-number v-model:value="form.max_concurrency" :min="0" :step="1" style="width: 100%" />
          <div class="muted rate-hint">该 Key 的实例内在途请求上限，0 表示不限。</div>
        </div>
      </ui-form-item>
      <ui-form-item label="状态" path="status">
        <ui-radio-group v-model:value="form.status">
          <ui-radio v-for="opt in STATUS_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</ui-radio>
        </ui-radio-group>
      </ui-form-item>
    </ui-form>
    <template #footer>
      <ui-space justify="end">
        <ui-button @click="emit('update:show', false)">取消</ui-button>
        <ui-button type="primary" :loading="saving" @click="save">保存</ui-button>
      </ui-space>
    </template>
  </ui-modal>
</template>

<style scoped>
.rate-field { width: 100%; }
.rate-hint { margin-top: 6px; font-size: 12px; line-height: 1.5; }
</style>
