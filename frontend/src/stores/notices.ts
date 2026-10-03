import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  getNoticeUnreadCount,
  listNotices,
  markAllNoticesRead,
  markNoticeRead,
} from '@/api/admin'
import type { Notice, NoticeKind } from '@/api/types'
import { errText } from '@/utils/format'

const LIST_PAGE_SIZE = 50

export const useNoticesStore = defineStore('notices', () => {
  const unread = ref(0)
  const byKind = ref<Record<string, number>>({})
  const items = ref<Notice[]>([])
  const kind = ref<NoticeKind | ''>('')
  const loading = ref(false)
  const error = ref('')
  const pendingIds = new Set<number>()

  const hasUnread = computed(() => unread.value > 0)

  function applyUnread(data?: { unread?: unknown; by_kind?: Record<string, number> } | null) {
    const n = Number(data?.unread)
    unread.value = Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
    byKind.value = { ...(data?.by_kind ?? {}) }
  }

  async function fetchUnread() {
    try {
      applyUnread(await getNoticeUnreadCount())
    } catch {
      /* keep last known count; 401 still logs out via http */
    }
  }

  async function fetchList() {
    loading.value = true
    error.value = ''
    try {
      const res = await listNotices({
        page: 1,
        page_size: LIST_PAGE_SIZE,
        kind: kind.value || undefined,
      })
      items.value = res.items
    } catch (e) {
      error.value = errText(e, '加载失败')
    } finally {
      loading.value = false
    }
  }

  async function setKind(next: NoticeKind | '') {
    if (kind.value === next) return
    kind.value = next
    await fetchList()
  }

  async function markRead(id: number) {
    const item = items.value.find((row) => row.id === id)
    if (item?.read_at || pendingIds.has(id)) return
    pendingIds.add(id)
    try {
      const notice = await markNoticeRead(id)
      items.value = items.value.map((row) => (row.id === id ? { ...row, ...notice } : row))
      if (unread.value > 0) unread.value -= 1
      const k = item?.kind
      if (k && (byKind.value[k] ?? 0) > 0) {
        byKind.value = { ...byKind.value, [k]: byKind.value[k] - 1 }
      }
    } finally {
      pendingIds.delete(id)
    }
  }

  async function markAllRead() {
    if (!hasUnread.value && items.value.every((row) => row.read_at)) return
    const data = await markAllNoticesRead(kind.value || undefined)
    const now = new Date().toISOString()
    items.value = items.value.map((row) => (row.read_at ? row : { ...row, read_at: now }))
    applyUnread(data)
  }

  function reset() {
    unread.value = 0
    byKind.value = {}
    items.value = []
    kind.value = ''
    loading.value = false
    error.value = ''
  }

  return {
    unread,
    byKind,
    items,
    kind,
    loading,
    error,
    hasUnread,
    fetchUnread,
    fetchList,
    setKind,
    markRead,
    markAllRead,
    reset,
  }
})
