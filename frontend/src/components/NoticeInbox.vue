<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { NotificationsOutline } from '@/components/ui/icons'
import { useMessage } from '@/components/ui'
import { NOTICE_SOURCE_LABEL, type Notice, type NoticeKind } from '@/api/types'
import { useNoticesStore } from '@/stores/notices'
import { errText, formatRate, formatTime } from '@/utils/format'

const POLL_MS = 15_000

type RateRow = {
  key_name: string
  old_rate?: number
  new_rate?: number
  direction?: 'up' | 'down'
}
type ModelRow = {
  group_name: string
  added?: string[]
  removed?: string[]
}

const message = useMessage()
const store = useNoticesStore()
const open = ref(false)
const markingAll = ref(false)
let timer: number | undefined

const TABS: Array<{ value: NoticeKind | ''; label: string }> = [
  { value: '', label: '全部' },
  { value: 'rate_change', label: '价格变动' },
  { value: 'model_change', label: '模型变化' },
]

function rateRow(item: Notice): RateRow {
  return (item.payload ?? {}) as RateRow
}

function modelRow(item: Notice): ModelRow {
  return (item.payload ?? {}) as ModelRow
}

function kindUnread(kind: NoticeKind): number {
  return store.byKind[kind] ?? 0
}

function startPoll() {
  stopPoll()
  timer = window.setInterval(() => void store.fetchUnread(), POLL_MS)
}

function stopPoll() {
  if (timer != null) {
    window.clearInterval(timer)
    timer = undefined
  }
}

async function onOpen(show: boolean) {
  open.value = show
  if (show) {
    void store.fetchUnread()
    await store.fetchList()
  }
}

async function onTab(value: NoticeKind | '') {
  try {
    await store.setKind(value)
  } catch (e) {
    message.error(errText(e, '切换类型失败'))
  }
}

async function onRow(item: Notice) {
  if (item.read_at) return
  try {
    await store.markRead(item.id)
  } catch (e) {
    message.error(errText(e, '标记已读失败'))
  }
}

async function onMarkAll() {
  if (markingAll.value || !store.hasUnread) return
  markingAll.value = true
  try {
    await store.markAllRead()
  } catch (e) {
    message.error(errText(e, '全部已读失败'))
  } finally {
    markingAll.value = false
  }
}

onMounted(() => {
  void store.fetchUnread()
  startPoll()
})

onUnmounted(() => {
  stopPoll()
  store.reset()
})
</script>

<template>
  <ui-popover
    :show="open"
    trigger="click"
    placement="bottom-end"
    :show-arrow="false"
    raw
    @update:show="onOpen"
  >
    <template #trigger>
      <ui-badge :value="store.unread" :max="99" :show="store.unread > 0">
        <ui-button quaternary circle size="small" aria-label="通知">
          <template #icon>
            <ui-icon size="18"><NotificationsOutline /></ui-icon>
          </template>
        </ui-button>
      </ui-badge>
    </template>
    <div class="inbox">
      <div class="head">
        <strong>通知</strong>
        <ui-button
          text
          size="tiny"
          :disabled="!store.hasUnread"
          :loading="markingAll"
          @click="onMarkAll"
        >
          全部已读
        </ui-button>
      </div>
      <div class="tabs">
        <button
          v-for="tab in TABS"
          :key="tab.value"
          type="button"
          class="tab"
          :class="{ active: store.kind === tab.value }"
          @click="onTab(tab.value)"
        >
          {{ tab.label }}
          <span v-if="tab.value && kindUnread(tab.value as NoticeKind) > 0" class="tab-badge">
            {{ kindUnread(tab.value as NoticeKind) }}
          </span>
        </button>
      </div>
      <ui-spin :show="store.loading">
        <div v-if="store.error" class="err">{{ store.error }}</div>
        <ui-empty v-else-if="!store.loading && !store.items.length" description="暂无通知" />
        <div v-else class="list">
          <button
            v-for="item in store.items"
            :key="item.id"
            type="button"
            class="row"
            :class="{ unread: !item.read_at }"
            @click="onRow(item)"
          >
            <template v-if="item.kind === 'rate_change'">
              <span class="dot" :class="rateRow(item).direction === 'up' ? 'up' : 'down'" />
              <span class="body">
                <span class="title">
                  <span class="name">{{ rateRow(item).key_name || '—' }}</span>
                  <ui-tag size="tiny" :bordered="false">
                    {{ NOTICE_SOURCE_LABEL[item.source] || item.source }}
                  </ui-tag>
                </span>
                <span class="meta">
                  <span class="rate" :class="rateRow(item).direction">
                    ×{{ formatRate(rateRow(item).old_rate ?? 0) }} →
                    ×{{ formatRate(rateRow(item).new_rate ?? 0) }}
                  </span>
                  <span class="time">{{ formatTime(item.created_at) }}</span>
                </span>
              </span>
            </template>
            <template v-else>
              <span
                class="dot"
                :class="(modelRow(item).removed?.length ?? 0) > 0 ? 'down' : 'up'"
              />
              <span class="body">
                <span class="title">
                  <span class="name">{{ modelRow(item).group_name || '—' }}</span>
                  <ui-tag size="tiny" :bordered="false">
                    {{ NOTICE_SOURCE_LABEL[item.source] || item.source }}
                  </ui-tag>
                </span>
                <span class="summary">{{ item.summary }}</span>
                <span class="meta">
                  <span class="models">
                    <span
                      v-for="id in modelRow(item).added ?? []"
                      :key="`a-${id}`"
                      class="chip add"
                    >
                      +{{ id }}
                    </span>
                    <span
                      v-for="id in modelRow(item).removed ?? []"
                      :key="`r-${id}`"
                      class="chip rem"
                    >
                      −{{ id }}
                    </span>
                  </span>
                  <span class="time">{{ formatTime(item.created_at) }}</span>
                </span>
              </span>
            </template>
          </button>
        </div>
      </ui-spin>
    </div>
  </ui-popover>
</template>

<style scoped>
.inbox {
  width: min(380px, calc(100vw - 24px));
  padding: 10px 0 8px;
  background: #fff;
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(15, 23, 32, 0.12);
}
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 2px 14px 8px;
}
.head strong {
  font-size: 13px;
  font-weight: 650;
}
.tabs {
  display: flex;
  gap: 4px;
  padding: 0 14px 8px;
}
.tab {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border: 0;
  border-radius: 999px;
  background: transparent;
  color: #819087;
  font-size: 12px;
  cursor: pointer;
}
.tab:hover {
  background: #f4f7ef;
}
.tab.active {
  background: #e8eedb;
  color: #174b3d;
  font-weight: 600;
}
.tab-badge {
  min-width: 16px;
  height: 16px;
  padding: 0 4px;
  border-radius: 999px;
  background: #a16d50;
  color: #fff;
  font-size: 10px;
  line-height: 16px;
  text-align: center;
}
.err {
  padding: 16px 14px;
  color: #a16d50;
  font-size: 12px;
}
.list {
  max-height: 420px;
  overflow: auto;
}
.row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  width: 100%;
  margin: 0;
  padding: 8px 14px;
  border: 0;
  background: transparent;
  text-align: left;
  cursor: default;
  color: inherit;
  font: inherit;
}
.row:hover {
  background: #f4f7ef;
}
.row.unread {
  background: #f3f7ee;
  cursor: pointer;
}
.row.unread:hover {
  background: #e8eedb;
}
.dot {
  flex: none;
  width: 8px;
  height: 8px;
  margin-top: 5px;
  border-radius: 50%;
  background: #cbd5e1;
}
.dot.up {
  background: #a16d50;
}
.dot.down {
  background: #578049;
}
.row.unread .dot.up,
.row.unread .dot.down {
  box-shadow: 0 0 0 3px rgba(23, 75, 61, 0.12);
}
.body {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  font-weight: 500;
}
.row.unread .name {
  font-weight: 650;
}
.summary {
  font-size: 12px;
  color: #4b5b52;
}
.meta {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  font-size: 12px;
}
.rate {
  font-variant-numeric: tabular-nums;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
}
.rate.up {
  color: #a16d50;
}
.rate.down {
  color: #578049;
}
.models {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  min-width: 0;
}
.chip {
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 0 6px;
  border-radius: 4px;
  font-size: 11px;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
}
.chip.add {
  background: #e8eedb;
  color: #3f6b34;
}
.chip.rem {
  background: #f7e8dd;
  color: #a16d50;
  text-decoration: line-through;
}
.time {
  color: #819087;
  font-size: 11px;
  white-space: nowrap;
}
:deep(.ui-empty) {
  padding: 28px 0;
}
:deep(.ui-spin-content) {
  min-height: 72px;
}
</style>
