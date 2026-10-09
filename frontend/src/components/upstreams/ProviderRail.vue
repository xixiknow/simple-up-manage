<script setup lang="ts">
import { computed, ref } from 'vue'
import HealthTag from '@/components/HealthTag.vue'
import StatusTag from '@/components/StatusTag.vue'
import type { Upstream } from '@/api/types'
import { formatMoney, formatTime } from '@/utils/format'

const props = defineProps<{ providers: Upstream[]; selectedId: number | null; loading?: boolean }>()
const emit = defineEmits<{ select: [id: number]; create: [] }>()

const LOW_BALANCE = 50
const query = ref('')
const quick = ref<'all' | 'abnormal' | 'low' | 'disabled'>('all')

function abnormal(u: Upstream) {
  return u.status === 'enabled' && (u.summary?.abnormal_count ?? 0) > 0
}
function isLow(u: Upstream) {
  return typeof u.last_balance === 'number' && u.last_balance < LOW_BALANCE
}
const kindLabel: Record<string, string> = { new_api: 'NewAPI', sub2api: 'Sub2API', openai_compat: 'OpenAI 兼容', anthropic_compat: 'Anthropic 兼容' }

const quickCounts = computed(() => ({
  all: props.providers.length,
  abnormal: props.providers.filter(abnormal).length,
  low: props.providers.filter((u) => u.status === 'enabled' && isLow(u)).length,
  disabled: props.providers.filter((u) => u.status === 'disabled').length,
}))

const visible = computed(() => {
  const q = query.value.trim().toLowerCase()
  return props.providers.filter((u) =>
    (!q || [u.name, u.base_url, u.note].some((v) => v?.toLowerCase().includes(q)))
    && (quick.value === 'all'
      || (quick.value === 'abnormal' && abnormal(u))
      || (quick.value === 'low' && u.status === 'enabled' && isLow(u))
      || (quick.value === 'disabled' && u.status === 'disabled')))
})

const quickChips = computed(() => [
  { value: 'all', label: `全部 ${quickCounts.value.all}` },
  { value: 'abnormal', label: `异常 ${quickCounts.value.abnormal}` },
  { value: 'low', label: `低余额 ${quickCounts.value.low}` },
  { value: 'disabled', label: `停用 ${quickCounts.value.disabled}` },
] as const)
</script>

<template>
  <aside class="rail">
    <div class="rail-tools">
      <ui-input v-model:value="query" clearable placeholder="搜索名称 / 地址 / 备注" :input-props="{ 'aria-label': '搜索提供商' }" />
      <ui-radio-group :value="quick" size="small" class="quick" @update:value="quick = $event">
        <ui-radio-button v-for="chip in quickChips" :key="chip.value" :value="chip.value">{{ chip.label }}</ui-radio-button>
      </ui-radio-group>
    </div>
    <ui-spin :show="!!props.loading">
      <div class="rail-list">
        <button
          v-for="p in visible"
          :key="p.id"
          type="button"
          class="p-row"
          :class="{ on: p.id === props.selectedId }"
          :aria-current="p.id === props.selectedId ? 'true' : undefined"
          @click="emit('select', p.id)"
        >
          <span class="p-line1">
            <span class="p-name" :title="p.name">{{ p.name }}</span>
            <StatusTag :status="p.status" />
            <span v-if="p.status === 'enabled' && (p.summary?.abnormal_count ?? 0) > 0" class="p-badge">异常 {{ p.summary?.abnormal_count }}</span>
          </span>
          <span class="p-line2">
            <HealthTag
              :status="p.health_status === 'cooldown' && (!p.cooldown_until || new Date(p.cooldown_until).getTime() <= Date.now()) ? 'healthy' : p.health_status"
            />
            <span class="muted">{{ p.summary?.key_count ?? 0 }} 把 Key</span>
            <span class="p-bal" :class="{ low: p.status === 'enabled' && isLow(p) }">
              {{ p.last_balance == null ? (p.last_balance_at ? '不限' : '未知') : formatMoney(p.last_balance) }}
            </span>
          </span>
          <span class="p-line3">
            <span class="muted">{{ kindLabel[p.kind] || p.kind }} · 更新 {{ p.last_balance_at ? formatTime(p.last_balance_at) : '—' }}</span>
          </span>
        </button>
        <div v-if="!visible.length" class="empty">没有匹配的提供商</div>
      </div>
    </ui-spin>
    <div class="rail-foot">
      <span>共 {{ visible.length }} 家</span>
      <span class="muted">异常优先</span>
    </div>
  </aside>
</template>

<style scoped>
.rail {
  width: 300px;
  flex: none;
  display: flex;
  flex-direction: column;
  background: #fff;
  border: 1px solid var(--line, #e1e7df);
  border-radius: 12px;
  overflow: hidden;
  min-height: 0;
}
.rail-tools {
  padding: 12px 12px 10px;
  border-bottom: 1px solid #eef0e9;
  display: flex;
  flex-direction: column;
  gap: 9px;
}
.quick { display: flex; flex-wrap: wrap; gap: 4px 0; height: auto; }
.rail-list { flex: 1; min-height: 280px; max-height: calc(100vh - 320px); overflow-y: auto; }
.p-row {
  width: 100%;
  text-align: left;
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 6px;
  padding: 11px 14px;
  border-bottom: 1px solid #f0f2ec;
  border-radius: 0;
}
.p-row:hover { background: #fafbf5; }
.p-row.on { background: var(--row, #f3f7ee); box-shadow: inset 3px 0 0 var(--green, #174b3d); }
.p-line1 { display: flex; align-items: center; gap: 7px; }
.p-name { font-weight: 650; font-size: 13px; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.p-badge { padding: 2px 8px; border-radius: 5px; font-size: 12px; background: #faece6; color: #a16d50; white-space: nowrap; }
.p-line2 { display: flex; align-items: center; gap: 7px; font-size: 12px; color: #819087; }
.p-bal { margin-left: auto; font-variant-numeric: tabular-nums; font-size: 12px; color: #3c543e; font-weight: 650; }
.p-bal.low { color: #a16d50; }
.p-line3 { font-size: 12px; }
.rail-foot {
  display: flex;
  justify-content: space-between;
  padding: 9px 14px;
  font-size: 12px;
  border-top: 1px solid #eef0e9;
  background: #fbfcf8;
}
.empty { padding: 36px 14px; text-align: center; color: #8c9b84; font-size: 12px; }
@media (max-width: 1100px) {
  .rail { width: 252px; }
}
@media (max-width: 760px) {
  .rail { width: 100%; }
  .rail-list { max-height: none; }
}
</style>
