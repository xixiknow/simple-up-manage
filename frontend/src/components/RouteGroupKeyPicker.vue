<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useMessage } from 'naive-ui'
import { listRouteCandidates, setRouteGroupKeys } from '@/api/admin'
import {
  KIND_LABEL,
  PROTOCOL_LABEL,
  PROTOCOL_OPTIONS,
  type Protocol,
  type RouteCandidate,
  type RouteGroup,
} from '@/api/types'
import HealthTag from '@/components/HealthTag.vue'
import { errText, formatMoney, formatRate } from '@/utils/format'

const props = defineProps<{
  group: RouteGroup | null
  groups: RouteGroup[]
}>()

const emit = defineEmits<{ saved: [] }>()
const show = defineModel<boolean>('show', { default: false })

const message = useMessage()
const loading = ref(false)
const saving = ref(false)
const candidates = ref<RouteCandidate[]>([])
const draft = ref<Set<number>>(new Set())
const collapsed = ref<Set<number>>(new Set())

const filters = reactive({
  upstream: null as number | null,
  protocol: null as Protocol | null,
  rateMin: null as number | null,
  rateMax: null as number | null,
  keyword: '',
  onlyUnassigned: false,
})

type UpstreamSection = {
  id: number
  name: string
  kind: string
  protocols: Protocol[]
  keyIds: number[]
  keys: RouteCandidate[]
}

const groupName = computed(() => Object.fromEntries(props.groups.map((g) => [g.id, g.name])))
const original = computed(() => new Set(props.group?.key_ids ?? []))
const added = computed(() => [...draft.value].filter((id) => !original.value.has(id)))
const removed = computed(() => [...original.value].filter((id) => !draft.value.has(id)))
const dirty = computed(() => added.value.length > 0 || removed.value.length > 0)

const upstreamOptions = computed(() => {
  const seen = new Map<number, string>()
  for (const c of candidates.value) seen.set(c.upstream_id, c.upstream_name)
  return [...seen.entries()].map(([value, label]) => ({ label, value }))
})

function fmtRate(v?: number | null) {
  return `×${formatRate(v)}`
}

function rateRangeText(g: RouteGroup | null) {
  if (!g) return ''
  const min = g.rate_min ?? null
  const max = g.rate_max ?? null
  if (min === null && max === null) return ''
  return `${min === null ? '0' : fmtRate(min)} ~ ${max === null ? '∞' : fmtRate(max)}`
}

function inRange(g: RouteGroup | null, rate: number) {
  if (!g) return true
  if (g.rate_min !== null && g.rate_min !== undefined && rate < g.rate_min - 1e-9) return false
  if (g.rate_max !== null && g.rate_max !== undefined && rate > g.rate_max + 1e-9) return false
  return true
}

function resetDraft() {
  draft.value = new Set(props.group?.key_ids ?? [])
}

async function loadCandidates() {
  loading.value = true
  try {
    candidates.value = await listRouteCandidates()
  } catch (e) {
    message.error(errText(e, '加载提供商 Key 失败'))
    candidates.value = []
  } finally {
    loading.value = false
  }
}

watch(show, (v) => {
  if (!v) return
  resetDraft()
  Object.assign(filters, {
    upstream: null,
    protocol: null,
    rateMin: null,
    rateMax: null,
    keyword: '',
    onlyUnassigned: false,
  })
  void loadCandidates()
})

const visibleCandidates = computed(() => {
  const kw = filters.keyword.trim().toLowerCase()
  const current = props.group?.id
  return candidates.value.filter((c) => {
    if (filters.upstream && c.upstream_id !== filters.upstream) return false
    if (filters.protocol && !c.protocols.includes(filters.protocol)) return false
    if (filters.rateMin !== null && c.rate_multiplier < filters.rateMin - 1e-9) return false
    if (filters.rateMax !== null && c.rate_multiplier > filters.rateMax + 1e-9) return false
    if (filters.onlyUnassigned) {
      const others = c.route_group_ids.filter((id) => id !== current)
      if (others.length > 0) return false
    }
    if (kw) {
      const hay = `${c.name} ${c.key_preview} ${c.upstream_name} ${c.billing_group ?? ''}`.toLowerCase()
      if (!hay.includes(kw)) return false
    }
    return true
  })
})

const sections = computed<UpstreamSection[]>(() => {
  const byUpstream = new Map<number, RouteCandidate[]>()
  for (const c of visibleCandidates.value) {
    if (!byUpstream.has(c.upstream_id)) byUpstream.set(c.upstream_id, [])
    byUpstream.get(c.upstream_id)!.push(c)
  }
  const out: UpstreamSection[] = []
  for (const [upstreamId, list] of byUpstream) {
    const keys = [...list].sort((a, b) => a.rate_multiplier - b.rate_multiplier || a.id - b.id)
    const first = keys[0]
    out.push({
      id: upstreamId,
      name: first.upstream_name,
      kind: first.upstream_kind,
      protocols: first.protocols,
      keyIds: keys.map((c) => c.id),
      keys,
    })
  }
  return out
})

function kindLabel(kind: string) {
  return (KIND_LABEL as Record<string, string>)[kind] ?? kind
}

function checkedCount(ids: number[]) {
  let n = 0
  for (const id of ids) if (draft.value.has(id)) n++
  return n
}

function allChecked(ids: number[]) {
  return ids.length > 0 && checkedCount(ids) === ids.length
}

function someChecked(ids: number[]) {
  const n = checkedCount(ids)
  return n > 0 && n < ids.length
}

function toggleIds(ids: number[], checked: boolean) {
  const next = new Set(draft.value)
  for (const id of ids) {
    if (checked) next.add(id)
    else next.delete(id)
  }
  draft.value = next
}

function toggleOne(id: number) {
  toggleIds([id], !draft.value.has(id))
}

function toggleCollapse(upstreamId: number) {
  const next = new Set(collapsed.value)
  if (next.has(upstreamId)) next.delete(upstreamId)
  else next.add(upstreamId)
  collapsed.value = next
}

function selectVisible(checked: boolean) {
  toggleIds(
    visibleCandidates.value.map((c) => c.id),
    checked,
  )
}

const visibleSelectedCount = computed(() => visibleCandidates.value.filter((c) => draft.value.has(c.id)).length)

function isDrift(c: RouteCandidate) {
  return draft.value.has(c.id) && !inRange(props.group, c.rate_multiplier)
}

function otherGroups(c: RouteCandidate) {
  return c.route_group_ids.filter((id) => id !== props.group?.id)
}

function summary(ids: number[]) {
  const n = checkedCount(ids)
  return n ? `${n} / ${ids.length} 已选` : `${ids.length} 把`
}

async function saveMembers() {
  if (!props.group) return
  saving.value = true
  try {
    await setRouteGroupKeys(props.group.id, [...draft.value])
    message.success(`已保存：新增 ${added.value.length} / 移除 ${removed.value.length}`)
    show.value = false
    emit('saved')
  } catch (e) {
    message.error(errText(e, '保存失败'))
  } finally {
    saving.value = false
  }
}

function requestClose(next: boolean) {
  if (next) {
    show.value = true
    return
  }
  if (!dirty.value) {
    show.value = false
    return
  }
  if (window.confirm('有未保存的修改，关闭将丢弃。确认关闭？')) {
    show.value = false
  }
}
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    :title="group ? `选择提供商 Key · ${group.name}` : '选择提供商 Key'"
    style="width: 960px"
    :mask-closable="!dirty"
    @update:show="requestClose"
  >
    <div class="picker">
      <div class="picker-head">
        <div class="picker-title">
          <span class="muted">按提供商归组、按倍率升序，勾选整个提供商或单把 Key</span>
        </div>
        <div class="picker-filters">
          <n-select
            v-model:value="filters.upstream"
            :options="upstreamOptions"
            clearable
            size="small"
            placeholder="全部提供商"
            style="width: 150px"
          />
          <n-select
            v-model:value="filters.protocol"
            :options="PROTOCOL_OPTIONS"
            clearable
            size="small"
            placeholder="全部协议"
            style="width: 116px"
          />
          <n-input-group class="rate-range">
            <n-input-number
              v-model:value="filters.rateMin"
              size="small"
              placeholder="倍率 ≥"
              :step="0.01"
              :min="0"
              :show-button="false"
              style="width: 86px"
            />
            <n-input-group-label size="small">~</n-input-group-label>
            <n-input-number
              v-model:value="filters.rateMax"
              size="small"
              placeholder="≤"
              :step="0.01"
              :min="0"
              :show-button="false"
              style="width: 72px"
            />
          </n-input-group>
          <n-input v-model:value="filters.keyword" size="small" clearable placeholder="搜索名称 / 预览" class="kw-input" />
          <n-checkbox v-model:checked="filters.onlyUnassigned" size="small" class="nowrap">只看未归档</n-checkbox>
        </div>
      </div>

      <n-alert v-if="draft.size === 0 && !dirty" type="info" :bordered="false" class="picker-hint">
        该分组还没有成员。绑定它的 API 密钥在保存前拿不到任何提供商 Key。
      </n-alert>

      <div class="member-list">
        <div class="member-toolbar">
          <n-checkbox
            size="small"
            :checked="visibleCandidates.length > 0 && visibleSelectedCount === visibleCandidates.length"
            :indeterminate="visibleSelectedCount > 0 && visibleSelectedCount < visibleCandidates.length"
            :disabled="!visibleCandidates.length"
            @update:checked="selectVisible"
          >
            筛选结果 {{ visibleCandidates.length }} 把<template v-if="visibleSelectedCount"> · 已选 {{ visibleSelectedCount }}</template>
          </n-checkbox>
          <div class="member-cols">
            <span class="col-rate">倍率</span>
            <span class="col-health">健康</span>
            <span class="col-balance">余额</span>
            <span class="col-tags">其他归属</span>
          </div>
        </div>

        <div v-if="loading" class="member-empty muted">加载中…</div>
        <div v-else-if="!sections.length" class="member-empty muted">没有符合筛选条件的 Key</div>

        <section v-for="up in sections" :key="up.id" class="up-section">
          <div class="up-row" @click="toggleCollapse(up.id)">
            <n-checkbox
              size="small"
              :checked="allChecked(up.keyIds)"
              :indeterminate="someChecked(up.keyIds)"
              @update:checked="(v: boolean) => toggleIds(up.keyIds, v)"
              @click.stop
            />
            <span class="chevron" :class="{ open: !collapsed.has(up.id) }">›</span>
            <span class="up-name">{{ up.name }}</span>
            <n-tag size="tiny" :bordered="false">{{ kindLabel(up.kind) }}</n-tag>
            <n-tag v-for="p in up.protocols" :key="p" size="tiny" :bordered="false" type="info">{{ PROTOCOL_LABEL[p] }}</n-tag>
            <span class="spacer" />
            <span class="count" :class="{ on: checkedCount(up.keyIds) > 0 }">{{ summary(up.keyIds) }}</span>
          </div>

          <template v-if="!collapsed.has(up.id)">
            <div
              v-for="c in up.keys"
              :key="c.id"
              class="key-row"
              :class="{ on: draft.has(c.id), drift: isDrift(c) }"
              @click="toggleOne(c.id)"
            >
              <n-checkbox size="small" :checked="draft.has(c.id)" @update:checked="() => toggleOne(c.id)" @click.stop />
              <div class="key-main">
                <span class="key-name">{{ c.name || `#${c.id}` }}</span>
                <span class="preview muted">{{ c.key_preview }}</span>
              </div>
              <div class="member-cols">
                <span class="col-rate">
                  <n-tooltip v-if="isDrift(c)" trigger="hover">
                    <template #trigger>
                      <span class="rate-drift">{{ fmtRate(c.rate_multiplier) }}</span>
                    </template>
                    当前 {{ fmtRate(c.rate_multiplier) }}，超出参考区间 {{ rateRangeText(group) }}，调度时将跳过
                  </n-tooltip>
                  <span v-else>{{ fmtRate(c.rate_multiplier) }}</span>
                </span>
                <span class="col-health"><HealthTag :status="c.health_status" /></span>
                <span class="col-balance mono">{{ formatMoney(c.last_balance) }}</span>
                <span class="col-tags">
                  <n-tag v-for="id in otherGroups(c)" :key="id" size="tiny" :bordered="false" class="other-tag">
                    {{ groupName[id] ?? `#${id}` }}
                  </n-tag>
                </span>
              </div>
            </div>
          </template>
        </section>
      </div>
    </div>
    <template #footer>
      <div class="save-bar" :class="{ dirty }">
        <span class="save-text">
          已选 <strong>{{ draft.size }}</strong> 把
          <template v-if="dirty">
            <span class="dot">·</span>
            新增 <strong class="add">{{ added.length }}</strong>
            <span class="dot">/</span>
            移除 <strong class="del">{{ removed.length }}</strong>
          </template>
          <span v-else class="muted">· 无改动</span>
        </span>
        <n-space size="small">
          <n-button size="small" @click="requestClose(false)">取消</n-button>
          <n-button size="small" type="primary" :disabled="!dirty" :loading="saving" @click="saveMembers">保存成员</n-button>
        </n-space>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.picker-head {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-bottom: 10px;
}
.picker-filters {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.rate-range {
  width: auto;
  flex: 0 0 auto;
}
.kw-input {
  flex: 1 1 160px;
  min-width: 140px;
  max-width: 320px;
}
.nowrap {
  white-space: nowrap;
}
.picker-hint {
  margin-bottom: 10px;
}
.member-list {
  border: 1px solid #e4e7ec;
  border-radius: 10px;
  overflow: auto;
  max-height: 52vh;
  background: #fff;
}
.member-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 14px;
  background: #f8fafc;
  border-bottom: 1px solid #e4e7ec;
  font-size: 12px;
  color: #667085;
  position: sticky;
  top: 0;
  z-index: 1;
}
.member-cols {
  display: grid;
  grid-template-columns: 76px 72px 88px 150px;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  flex: 0 0 auto;
}
.member-toolbar .member-cols {
  font-size: 12px;
}
.col-rate,
.col-balance {
  text-align: right;
}
.col-health {
  display: flex;
  justify-content: center;
}
.col-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  min-width: 0;
  padding-left: 8px;
}
.member-empty {
  padding: 36px 0;
  text-align: center;
}
.up-section + .up-section {
  border-top: 1px solid #eef0f3;
}
.up-row,
.key-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 38px;
}
.up-row {
  padding: 6px 14px;
  background: #f8fafc;
  cursor: pointer;
  user-select: none;
}
.up-row:hover {
  background: #f2f5f9;
}
.chevron {
  display: inline-block;
  width: 12px;
  color: #98a2b3;
  font-size: 16px;
  line-height: 1;
  transition: transform 0.15s;
}
.chevron.open {
  transform: rotate(90deg);
}
.up-name {
  font-weight: 600;
  font-size: 14px;
}
.spacer {
  flex: 1 1 auto;
}
.count {
  font-size: 12px;
  color: #98a2b3;
  font-variant-numeric: tabular-nums;
}
.count.on {
  color: #2563eb;
  font-weight: 500;
}
.key-row {
  padding: 5px 14px 5px 38px;
  border-top: 1px solid #f2f4f7;
  cursor: pointer;
  transition: background 0.1s;
}
.key-row:hover {
  background: #fafbfc;
}
.key-row.on {
  background: #f3f7ff;
}
.key-row.drift {
  background: #fff6f4;
}
.key-main {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  align-items: baseline;
  gap: 10px;
  overflow: hidden;
}
.key-name {
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.other-tag {
  opacity: 0.75;
}
.rate-drift {
  color: #d92d20;
  font-weight: 600;
}
.save-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  width: 100%;
  font-size: 13px;
}
.save-bar.dirty .add {
  color: #16a34a;
}
.save-bar.dirty .del {
  color: #d92d20;
}
.save-text .dot {
  color: #98a2b3;
  margin: 0 4px;
}
</style>
