<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { UiAlert, UiButton, UiCard, UiForm, UiFormItem, UiInput, UiInputNumber, UiSwitch, useMessage } from '@/components/ui'
import type { FormInst } from '@/components/ui'
import { getAlertSettings, testAlert, updateAlertSettings } from '@/api/admin'
import type { AlertSettingsPayload } from '@/api/types'
import { errText } from '@/utils/format'

const message = useMessage()
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const error = ref('')
const formRef = ref<FormInst | null>(null)
const hasKey = ref(false)
const keyPreview = ref('')
const sendKeyInput = ref('')
const clearKey = ref(false)

const form = reactive({
  enabled: false,
  hours_threshold: 5,
  silence_hours: 6,
})

const rules = {
  hours_threshold: { required: true, type: 'number' as const, message: '请设置小时阈值' },
  silence_hours: { required: true, type: 'number' as const, message: '请设置静默小时数' },
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const res = await getAlertSettings()
    form.enabled = res.enabled
    form.hours_threshold = res.hours_threshold
    form.silence_hours = res.silence_hours
    hasKey.value = res.has_key
    keyPreview.value = res.send_key_preview
    sendKeyInput.value = ''
    clearKey.value = false
  } catch (e) {
    error.value = errText(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  await formRef.value?.validate()
  saving.value = true
  error.value = ''
  try {
    const payload: AlertSettingsPayload = {
      enabled: form.enabled,
      hours_threshold: form.hours_threshold,
      silence_hours: form.silence_hours,
    }
    if (clearKey.value) {
      payload.clear_send_key = true
    } else if (sendKeyInput.value.trim()) {
      payload.send_key = sendKeyInput.value.trim()
    }
    const res = await updateAlertSettings(payload)
    form.enabled = res.enabled
    form.hours_threshold = res.hours_threshold
    form.silence_hours = res.silence_hours
    hasKey.value = res.has_key
    keyPreview.value = res.send_key_preview
    sendKeyInput.value = ''
    clearKey.value = false
    message.success('已保存')
  } catch (e) {
    error.value = errText(e)
  } finally {
    saving.value = false
  }
}

async function sendTest() {
  testing.value = true
  error.value = ''
  try {
    const res = await testAlert()
    message.success(res.message || '测试消息已发送')
  } catch (e) {
    error.value = errText(e)
  } finally {
    testing.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>余额提醒</h1>
        <p class="muted">提供商预计小时数低于阈值时，通过 Server酱³ 推送微信提醒。</p>
      </div>
      <ui-button type="primary" size="small" :loading="saving" @click="save">保存配置</ui-button>
    </div>

    <ui-alert v-if="error" type="error" :title="error" />

    <ui-form ref="formRef" :model="form" :rules="rules" label-placement="left" label-width="128">
      <div class="cards">
        <ui-card size="small" title="Server酱推送" :bordered="false" :loading="loading">
          <div class="grid">
            <ui-form-item label="启用提醒">
              <ui-switch v-model:value="form.enabled" />
              <template #feedback>关闭后不再检查与推送，已保存的 SendKey 保留</template>
            </ui-form-item>
            <ui-form-item label="SendKey">
              <div class="key-row">
                <ui-input
                  v-model:value="sendKeyInput"
                  type="password"
                  show-password-on="click"
                  :placeholder="hasKey ? `已保存（${keyPreview || '已加密'}），留空保持不变` : 'sctp... 开头的 Server酱³ SendKey'"
                  :disabled="clearKey"
                />
                <ui-button v-if="hasKey" size="tiny" :disabled="clearKey" @click="clearKey = !clearKey">
                  {{ clearKey ? '保留原 Key' : '清除已存 Key' }}
                </ui-button>
              </div>
              <template #feedback>
                在 <a href="https://sc3.ft07.com/" target="_blank" rel="noopener">sc3.ft07.com</a> 获取，密文存储，仅展示预览
              </template>
            </ui-form-item>
            <ui-form-item label=" ">
              <ui-button size="small" :loading="testing" :disabled="!hasKey" @click="sendTest">发送测试消息</ui-button>
            </ui-form-item>
          </div>
        </ui-card>

        <ui-card size="small" title="提醒规则" :bordered="false" :loading="loading">
          <div class="grid">
            <ui-form-item label="小时阈值" path="hours_threshold">
              <div class="num-row">
                <ui-input-number v-model:value="form.hours_threshold" :min="0.5" :max="72" :step="0.5" style="width: 100%" />
                <span class="unit">小时</span>
              </div>
              <template #feedback>预计小时数 = 余额 ÷ 近 24 小时消耗速率；低于该值即提醒</template>
            </ui-form-item>
            <ui-form-item label="静默窗口" path="silence_hours">
              <div class="num-row">
                <ui-input-number v-model:value="form.silence_hours" :min="1" :max="168" :step="1" style="width: 100%" />
                <span class="unit">小时</span>
              </div>
              <template #feedback>同一提供商提醒后 N 小时内不重复；余额回升后自动重置</template>
            </ui-form-item>
          </div>
          <p class="muted card-hint">后台每 5 分钟检查一次余额与消耗速率；每次推送聚合所有到期的提供商，并同步写入站内收件箱。</p>
        </ui-card>
      </div>
    </ui-form>
  </div>
</template>

<style scoped>
.page {
  padding: 16px 20px 32px;
}
.page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}
.page-head h1 {
  margin: 0 0 2px;
  font-size: 18px;
}
.muted {
  color: var(--ui-text-muted, #8a8f9d);
  margin: 0;
}
.cards {
  display: grid;
  gap: 14px;
  max-width: 720px;
}
.grid {
  display: grid;
  gap: 4px 16px;
}
.key-row {
  display: flex;
  gap: 8px;
  align-items: center;
  width: 100%;
}
.num-row {
  display: flex;
  gap: 8px;
  align-items: center;
  width: 100%;
}
.unit {
  color: var(--ui-text-muted, #8a8f9d);
  white-space: nowrap;
}
.card-hint {
  margin-top: 8px;
  font-size: 12px;
}
</style>
