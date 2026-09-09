import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { ApiError, TOKEN_KEY } from '@/api/http'
import { getStatus } from '@/api/admin'

export const useAuthStore = defineStore('auth', () => {
  const token = ref(localStorage.getItem(TOKEN_KEY) || '')
  const isAuthed = computed(() => !!token.value)

  async function login(raw: string) {
    const next = raw.trim()
    if (!next) throw new Error('请输入管理员 Token')
    localStorage.setItem(TOKEN_KEY, next)
    token.value = next
    try {
      await getStatus()
    } catch (e) {
      logout()
      if (e instanceof ApiError && e.status === 401) {
        throw new Error('Token 无效')
      }
      throw e
    }
  }

  function logout() {
    localStorage.removeItem(TOKEN_KEY)
    token.value = ''
  }

  return { token, isAuthed, login, logout }
})
