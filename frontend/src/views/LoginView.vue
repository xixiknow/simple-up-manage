<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { KeyOutline } from '@vicons/ionicons5'
import { useAuthStore } from '@/stores/auth'
import { errText } from '@/utils/format'

const auth = useAuthStore()
const router = useRouter()
const token = ref('')
const loading = ref(false)
const error = ref('')

const canSubmit = computed(() => token.value.trim().length > 0)

async function submit() {
  if (!canSubmit.value) return
  loading.value = true
  error.value = ''
  try {
    await auth.login(token.value)
    const redirect = router.currentRoute.value.query.redirect
    void router.push(typeof redirect === 'string' ? redirect : '/')
  } catch (e) {
    error.value = errText(e, '登录失败')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <div class="panel">
      <div class="logo-row">
        <span class="mark" />
        <div>
          <h1>南向提供商管理</h1>
          <p>使用后端静态 ADMIN_TOKEN 登录</p>
        </div>
      </div>
      <n-alert v-if="error" type="error" :title="error" style="margin-bottom: 12px" />
      <n-form @submit.prevent="submit">
        <n-form-item label="管理员 Token">
          <n-input
            v-model:value="token"
            type="password"
            show-password-on="click"
            placeholder="Authorization Bearer Token"
            size="large"
            @keydown.enter="submit"
          >
            <template #prefix>
              <n-icon><KeyOutline /></n-icon>
            </template>
          </n-input>
        </n-form-item>
        <n-button type="primary" block size="large" :loading="loading" :disabled="!canSubmit" @click="submit">
          进入控制台
        </n-button>
      </n-form>
    </div>
  </div>
</template>

<style scoped>
.login-wrap {
  min-height: 100vh;
  display: grid;
  place-items: center;
  background:
    radial-gradient(1200px 500px at 10% -10%, rgba(45, 212, 191, 0.18), transparent 50%),
    #10161c;
}
.panel {
  width: 420px;
  max-width: calc(100vw - 32px);
  padding: 28px;
  border-radius: 12px;
  background: #f7f9fb;
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.28);
}
.logo-row {
  display: flex;
  gap: 12px;
  align-items: center;
  margin-bottom: 20px;
}
.logo-row h1 {
  margin: 0;
  font-size: 18px;
}
.logo-row p {
  margin: 4px 0 0;
  color: #667085;
  font-size: 12px;
}
.mark {
  width: 12px;
  height: 28px;
  border-radius: 3px;
  background: linear-gradient(180deg, #2dd4bf, #0f766e);
}
</style>
