<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
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
  <div class="login">
    <section class="hero">
      <div class="brand">
        <span class="mark">供</span>
        <div>
          <strong>供货商管理</strong>
          <small>ADMIN CONSOLE</small>
        </div>
      </div>
      <h1>每一次调度，<br />都有迹可循。</h1>
      <p>提供商、密钥与请求的统一运维台。</p>
      <div class="login-line"><i />实时运行 · 分组调度 · 请求追踪</div>
    </section>
    <div class="login-panel">
      <span class="eyebrow">ADMIN ACCESS</span>
      <h2>登录管理台</h2>
      <p>使用后端静态 ADMIN_TOKEN 登录</p>
      <ui-alert v-if="error" type="error" :title="error" style="margin-bottom: 12px" />
      <ui-form @submit.prevent="submit">
        <ui-form-item label="管理员 Token">
          <ui-input
            v-model:value="token"
            type="password"
            show-password-on="click"
            placeholder="Authorization Bearer Token"
            size="large"
            @keydown.enter="submit"
          />
        </ui-form-item>
        <ui-button type="primary" block size="large" :loading="loading" :disabled="!canSubmit" @click="submit">
          进入控制台
        </ui-button>
      </ui-form>
      <small>Token 保存在本机浏览器。</small>
    </div>
  </div>
</template>

<style scoped>
.login {
  min-height: 100vh;
  display: grid;
  grid-template-columns: 1.15fr 1fr;
  background: #f7f6f1;
}
.hero {
  display: flex;
  flex-direction: column;
  justify-content: center;
  padding: 64px 10%;
  background: #173f32;
  color: #e2ebd5;
}
.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 72px;
}
.mark {
  display: grid;
  place-items: center;
  width: 40px;
  height: 40px;
  border-radius: 11px;
  background: #d9e8b3;
  color: #244734;
  font-family: Georgia, 'Songti SC', serif;
  font-size: 24px;
}
.brand strong {
  display: block;
  font-size: 20px;
  letter-spacing: 2px;
}
.brand small {
  display: block;
  margin-top: 4px;
  color: #9db5a5;
  font-size: 12px;
  letter-spacing: 1.6px;
}
.hero h1 {
  margin: 0;
  font-family: Georgia, 'Songti SC', 'Microsoft YaHei', serif;
  font-size: 44px;
  font-weight: 500;
  line-height: 1.45;
  letter-spacing: 1px;
}
.hero > p {
  margin: 20px 0 0;
  color: #a5b89a;
  font-size: 14px;
}
.login-line {
  margin-top: 72px;
  color: #a9ba96;
  font-size: 12px;
  letter-spacing: 1px;
}
.login-line i {
  display: inline-block;
  width: 7px;
  height: 7px;
  margin-right: 10px;
  border-radius: 50%;
  background: #c4d79a;
}
.login-panel {
  box-sizing: border-box;
  min-width: 0;
  align-self: center;
  width: 100%;
  max-width: 460px;
  margin: auto;
  padding: 48px 56px;
}
.eyebrow {
  display: block;
  margin-bottom: 12px;
  color: #7c8f80;
  font-size: 12px;
  font-weight: 650;
  letter-spacing: 1.8px;
}
.login-panel h2 {
  margin: 0;
  color: #263b34;
  font-size: 26px;
  font-weight: 650;
}
.login-panel > p {
  margin: 12px 0 28px;
  color: #8a987d;
  font-size: 13px;
}
.login-panel small {
  display: block;
  margin-top: 18px;
  color: #9aa78b;
  font-size: 12px;
  text-align: center;
}
@media (max-width: 1100px) {
  .hero {
    padding: 48px 40px;
  }
  .hero h1 {
    font-size: 36px;
  }
  .brand {
    margin-bottom: 48px;
  }
  .login-line {
    margin-top: 48px;
  }
  .login-panel {
    padding: 40px 36px;
  }
}
@media (max-width: 760px) {
  .login {
    display: block;
  }
  .hero {
    padding: 18px 24px;
  }
  .brand {
    margin-bottom: 0;
  }
  .hero h1,
  .hero > p {
    display: none;
  }
  .login-line {
    display: none;
  }
  .login-panel {
    max-width: none;
    padding: 28px 24px 40px;
  }
}
</style>
