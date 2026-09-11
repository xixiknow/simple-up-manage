import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  getRateNoticeUnreadCount,
  listRateNotices,
  markAllRateNoticesRead,
  markRateNoticeRead,
} from '@/api/admin'
import type { RateChangeNotice } from '@/api/types'
import { errText } from '@/utils/format'

const LIST_PAGE_SIZE = 50

export const useRateNoticesStore = defineStore('rateNotices', () => {
  const unread = ref(0)
  const items = ref<RateChangeNotice[]>([])
  const loading = ref(false)
  const error = ref('')
  const pendingIds = new Set<number>()

  const hasUnread = computed(() => unread.value > 0)

  function applyUnread(value: unknown) {
    const n = Number(value)
    unread.value = Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
  }

  async function fetchUnread() {
    try {
      const data = await getRateNoticeUnreadCount()
      applyUnread(data?.unread)
    } catch {
      /* keep last known count; 401 still logs out via http */
    }
  }

  async function fetchList() {
    loading.value = true
    error.value = ''
    try {
      const res = await listRateNotices({ page: 1, page_size: LIST_PAGE_SIZE })
      items.value = res.items
    } catch (e) {
      error.value = errText(e, '加载失败')
    } finally {
      loading.value = false
    }
  }

  async function markRead(id: number) {
    const item = items.value.find((row) => row.id === id)
    if (item?.read_at || pendingIds.has(id)) return
    pendingIds.add(id)
    try {
      const notice = await markRateNoticeRead(id)
      items.value = items.value.map((row) => (row.id === id ? { ...row, ...notice } : row))
      if (unread.value > 0) unread.value -= 1
    } finally {
      pendingIds.delete(id)
    }
  }

  async function markAllRead() {
    if (!hasUnread.value && items.value.every((row) => row.read_at)) return
    const data = await markAllRateNoticesRead()
    const now = new Date().toISOString()
    items.value = items.value.map((row) => (row.read_at ? row : { ...row, read_at: now }))
    applyUnread(data?.unread)
  }

  function reset() {
    unread.value = 0
    items.value = []
    loading.value = false
    error.value = ''
  }

  return {
    unread,
    items,
    loading,
    error,
    hasUnread,
    fetchUnread,
    fetchList,
    markRead,
    markAllRead,
    reset,
  }
})
