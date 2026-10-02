<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { CashOutline } from '@/components/ui/icons'
import { useMessage } from '@/components/ui'
import { RATE_CHANGE_SOURCE_LABEL, type RateChangeNotice } from '@/api/types'
import { useRateNoticesStore } from '@/stores/rateNotices'
import { errText, formatRate, formatTime } from '@/utils/format'

const POLL_MS = 15_000

const message = useMessage()
const store = useRateNoticesStore()
const open = ref(false)
const markingAll = ref(false)
let timer: number | undefined

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

async function onRow(item: RateChangeNotice) {
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
        <ui-button quaternary circle size="small" aria-label="价格变动">
          <template #icon>
            <ui-icon size="18"><CashOutline /></ui-icon>
          </template>
        </ui-button>
      </ui-badge>
    </template>
    <div class="inbox">
      <div class="head">
        <strong>价格变动</strong>
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
      <ui-spin :show="store.loading">
        <div v-if="store.error" class="err">{{ store.error }}</div>
        <ui-empty v-else-if="!store.loading && !store.items.length" description="暂无价格变动" />
        <div v-else class="list">
          <button
            v-for="item in store.items"
            :key="item.id"
            type="button"
            class="row"
            :class="{ unread: !item.read_at }"
            @click="onRow(item)"
          >
            <span class="dot" :class="item.direction" />
            <span class="body">
              <span class="title">
                <span class="name">{{ item.key_name || '—' }}</span>
                <ui-tag size="tiny" :bordered="false">
                  {{ RATE_CHANGE_SOURCE_LABEL[item.source] || item.source }}
                </ui-tag>
              </span>
              <span class="meta">
                <span class="rate" :class="item.direction">
                  ×{{ formatRate(item.old_rate) }} → ×{{ formatRate(item.new_rate) }}
                </span>
                <span class="time">{{ formatTime(item.created_at) }}</span>
              </span>
            </span>
          </button>
        </div>
      </ui-spin>
    </div>
  </ui-popover>
</template>

<style scoped>
.inbox {
  width: min(360px, calc(100vw - 24px));
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
