import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { getSystemVersion, getUpdateStatus, postSystemUpdate } from '@/api/admin'
import type { SystemVersion, UpdateStatus } from '@/api/admin'
import { errText } from '@/utils/format'

export const ACTIVE_PHASES = ['pulling', 'creating', 'waiting', 'switching'] as const

export const useSystemStore = defineStore('system', () => {
  const info = ref<SystemVersion | null>(null)
  const status = ref<UpdateStatus | null>(null)
  const starting = ref(false)
  const error = ref('')

  const updateAvailable = computed(() => info.value?.update_available === true)
  const version = computed(() => info.value?.version || '')
  const updating = computed(() => ACTIVE_PHASES.includes((status.value?.phase ?? 'idle') as never))
  const canSelfUpdate = computed(() => info.value?.can_self_update === true)

  async function fetchVersion() {
    try {
      info.value = await getSystemVersion()
    } catch {
      /* 版本信息获取失败不打扰用户；401 已由 http 统一处理 */
    }
  }

  async function fetchStatus() {
    try {
      const next = await getUpdateStatus()
      // done/failed 超过 15 分钟视为历史记录，回到空闲展示
      if ((next.phase === 'done' || next.phase === 'failed') && next.updated_at) {
        const age = Date.now() - new Date(next.updated_at).getTime()
        if (age > 15 * 60 * 1000) {
          status.value = { ...next, phase: 'idle' }
          return
        }
      }
      status.value = next
    } catch {
      /* 保留上次状态 */
    }
  }

  async function startUpdate(target: string) {
    starting.value = true
    error.value = ''
    try {
      await postSystemUpdate(target)
      await fetchStatus()
      return true
    } catch (e) {
      error.value = errText(e, '触发更新失败')
      return false
    } finally {
      starting.value = false
    }
  }

  function reset() {
    info.value = null
    status.value = null
    starting.value = false
    error.value = ''
  }

  return {
    info,
    status,
    starting,
    error,
    updateAvailable,
    version,
    updating,
    canSelfUpdate,
    fetchVersion,
    fetchStatus,
    startUpdate,
    reset,
  }
})
