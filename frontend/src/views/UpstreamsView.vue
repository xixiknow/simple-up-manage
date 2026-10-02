<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useViewport } from '@/utils/viewport'
import { RefreshOutline } from '@/components/ui/icons'
import {
  actionMessage,
  allPages,
  createUpstream,
  deleteUpstream,
  fetchUpstreamModels,
  listRouteGroups,
  listUpstreams,
  refreshAllBalances,
  refreshUpstreamBalance,
  updateUpstream,
} from '@/api/admin'
import {
  KIND_OPTIONS,
  PROTOCOL_OPTIONS,
  STATUS_OPTIONS,
  type EnableStatus,
  type PlatformKey,
  type Protocol,
  type RouteGroup,
  type Upstream,
  type UpstreamKind,
  type UpstreamPayload,
} from '@/api/types'
import KeyFormModal from '@/components/upstreams/KeyFormModal.vue'
import ProviderDetail from '@/components/upstreams/ProviderDetail.vue'
import ProviderRail from '@/components/upstreams/ProviderRail.vue'
import ProbeModal from '@/components/ProbeModal.vue'
import { errText } from '@/utils/format'
import type { FormInst, FormRules } from '@/components/ui'
import { useDialog, useMessage } from '@/components/ui'

const message = useMessage()
const dialog = useDialog()
const route = useRoute()
const router = useRouter()
const { phone } = useViewport()

const LIVE_MS = 15_000

const loading = ref(false)
const error = ref('')
const items = ref<Upstream[]>([])
const routeGroups = ref<RouteGroup[]>([])
const selectedId = ref<number | null>(null)
const refreshToken = ref(0)
const detailOpen = ref(false)
const probing = ref(false)
const refreshingBal = ref(false)
const live = ref(true)
const lastRefresh = ref('')
let timer: number | undefined
let loadSequence = 0
let pending = false
let disposed = false
let initialized = false
const orderedIds = ref<number[]>([])

const selected = computed(() => items.value.find((up) => up.id === selectedId.value) ?? null)
const orderedProviders = computed(() => orderedIds.value
  .map((id) => items.value.find((up) => up.id === id))
  .filter((up): up is Upstream => !!up))

async function loadRouteGroups() {
  try {
    routeGroups.value = await listRouteGroups()
  } catch {
    routeGroups.value = []
  }
}

function abnormal(up: Upstream) {
  return up.status === 'enabled' && (up.summary?.abnormal_count ?? 0) > 0
}
function isLowBalance(up: Upstream) {
  return typeof up.last_balance === 'number' && up.last_balance < 50
}
function priority(up: Upstream) {
  if (up.status === 'disabled') return 3
  if (abnormal(up)) return 0
  return isLowBalance(up) ? 1 : 2
}

async function loadProviders(opts?: { silent?: boolean }) {
  if (disposed) return
  const silent = !!opts?.silent
  if (silent && pending) return
  const sequence = ++loadSequence
  pending = true
  if (!silent) loading.value = true
  try {
    const upstreams = await allPages((params) => listUpstreams({ ...params, include_summary: true }))
    if (sequence !== loadSequence) return
    const knownIds = new Set(upstreams.map((up) => up.id))
    // 静默轮询与翻页期间保持既有顺序（避免行跳动）；手动刷新按「异常优先」重排。
    orderedIds.value = initialized && silent
      ? orderedIds.value.filter((id) => knownIds.has(id))
      : upstreams.sort((a, b) => priority(a) - priority(b) || a.id - b.id).map((up) => up.id)
    items.value = upstreams
    initialized = true
    error.value = ''
    lastRefresh.value = formatClock(new Date())
  } catch (e) {
    if (sequence === loadSequence) error.value = errText(e)
  } finally {
    if (sequence === loadSequence) {
      pending = false
      loading.value = false
    }
  }
}

function formatClock(d: Date) {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

function startLive() {
  if (disposed) return
  stopLive()
  timer = window.setInterval(() => {
    void loadProviders({ silent: true })
    refreshToken.value++
  }, LIVE_MS)
}
async function refreshList() {
  await loadProviders()
  refreshToken.value++
}
function stopLive() {
  if (timer != null) {
    window.clearInterval(timer)
    timer = undefined
  }
}

function select(id: number) {
  selectedId.value = id
  if (phone.value) detailOpen.value = true
  if (route.query.id !== String(id)) void router.replace({ query: { ...route.query, id: String(id) } })
}
function onSelect(id: number) {
  select(id)
}

watch(live, (on) => {
  if (on) startLive()
  else stopLive()
})

onMounted(async () => {
  await loadRouteGroups()
  await loadProviders()
  const want = Number(route.query.id) || 0
  if (want && items.value.some((up) => up.id === want)) select(want)
  if (live.value) startLive()
})

onUnmounted(() => {
  disposed = true
  stopLive()
})

/* ---------- 提供商级操作 ---------- */
const showForm = ref(false)
const saving = ref(false)
const editing = ref<Upstream | null>(null)
const formRef = ref<FormInst | null>(null)
const form = reactive<UpstreamPayload>({
  name: '',
  base_url: '',
  kind: 'openai_compat',
  protocols: ['openai'],
  status: 'enabled',
  note: '',
  concurrency: 0,
  access_token: '',
  new_api_user_id: null as number | null,
})
const rules: FormRules = {
  name: { required: true, message: '请输入名称', trigger: 'blur' },
  base_url: { required: true, message: '请输入 Base URL', trigger: 'blur' },
  kind: { required: true, message: '请选择类型', trigger: 'change' },
  protocols: { type: 'array', required: true, min: 1, message: '至少选择一种协议', trigger: 'change' },
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '',
    base_url: '',
    kind: 'openai_compat' as UpstreamKind,
    protocols: ['openai'] as Protocol[],
    status: 'enabled' as EnableStatus,
    note: '',
    concurrency: 0,
    access_token: '', new_api_user_id: null,
  })
  showForm.value = true
}

function openEdit(row: Upstream) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    base_url: row.base_url,
    kind: row.kind,
    protocols: [...(row.protocols || [])],
    status: row.status,
    note: row.note || '',
    concurrency: row.concurrency ?? 0,
    access_token: '', new_api_user_id: row.new_api_user_id || null,
  })
  showForm.value = true
}

async function save() {
  await formRef.value?.validate()
  saving.value = true
  try {
    const payload: UpstreamPayload = {
      name: form.name.trim(),
      base_url: form.base_url.trim(),
      kind: form.kind,
      protocols: form.protocols,
      status: form.status,
      note: form.note?.trim() || undefined,
      concurrency: Number(form.concurrency) || 0,
      access_token: form.access_token?.trim() || undefined,
      new_api_user_id: form.new_api_user_id || undefined,
    }
    if (editing.value) await updateUpstream(editing.value.id, payload)
    else await createUpstreamAndSelect(payload)
    message.success('已保存')
    showForm.value = false
    await loadProviders()
    await loadRouteGroups()
  } catch (e) {
    message.error(errText(e, '保存失败'))
  } finally {
    saving.value = false
  }
}

async function createUpstreamAndSelect(payload: UpstreamPayload) {
  const created = await createUpstream(payload)
  selectedId.value = created.id
}

function confirmDelete(row: Upstream) {
  dialog.warning({
    title: '删除提供商',
    content: `确认删除「${row.name}」？其下的 Key 会一并删除。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await deleteUpstream(row.id)
        message.success('已删除')
        if (selectedId.value === row.id) {
          selectedId.value = null
          detailOpen.value = false
          void router.replace({ query: {} })
        }
        await loadProviders()
      } catch (e) {
        message.error(errText(e, '删除失败'))
      }
    },
  })
}

async function refreshAll() {
  refreshingBal.value = true
  try {
    const data = await refreshAllBalances()
    message.success(actionMessage(data, '已刷新余额'))
    await loadProviders({ silent: true })
    refreshToken.value++
  } catch (e) {
    message.error(errText(e, '刷新余额失败'))
  } finally {
    refreshingBal.value = false
  }
}

async function refreshUpstreamOne(up: Upstream) {
  try {
    const data = await refreshUpstreamBalance(up.id)
    message.success(actionMessage(data, `已刷新 ${up.name} 余额`))
    await loadProviders({ silent: true })
    refreshToken.value++
  } catch (e) {
    message.error(errText(e, '刷新余额失败'))
  }
}

const showProbe = ref(false)
const probeTarget = ref<{ name: string; key_id?: number; upstream_id?: number; models?: string[] }>({ name: '全部提供商' })
function openProbe(target: typeof probeTarget.value) {
  if (probing.value) return
  probeTarget.value = target
  showProbe.value = true
}

async function fetchUpstreamModelsOne(up: Upstream) {
  try {
    const data = await fetchUpstreamModels(up.id)
    message.success(actionMessage(data, '已获取模型'))
  } catch (e) {
    message.error(errText(e, '获取模型失败'))
  }
}

async function toggleProvider(up: Upstream) {
  const next: EnableStatus = up.status === 'enabled' ? 'disabled' : 'enabled'
  try {
    await updateUpstream(up.id, { ...up, status: next })
    message.success(next === 'enabled' ? '已启用' : '已停用')
    await loadProviders({ silent: true })
  } catch (e) {
    message.error(errText(e, '更新状态失败'))
  }
}

function onProviderAction(action: 'edit' | 'toggle' | 'delete' | 'probe' | 'refresh-balance' | 'fetch-models' | 'add-key') {
  const up = selected.value
  if (!up) return
  if (action === 'edit') openEdit(up)
  else if (action === 'toggle') void toggleProvider(up)
  else if (action === 'delete') confirmDelete(up)
  else if (action === 'probe') openProbe({ name: up.name, upstream_id: up.id })
  else if (action === 'refresh-balance') void refreshUpstreamOne(up)
  else if (action === 'fetch-models') void fetchUpstreamModelsOne(up)
  else if (action === 'add-key') openKeyForm(null, up)
}

/* ---------- Key 表单 ---------- */
const showKeyForm = ref(false)
const editingKey = ref<PlatformKey | null>(null)
const keyHost = ref<Upstream | null>(null)

function openKeyForm(row: PlatformKey | null, host: Upstream) {
  editingKey.value = row
  keyHost.value = host
  showKeyForm.value = true
}

function onKeySaved() {
  refreshToken.value++
  void loadProviders({ silent: true })
}

function onDetailChanged() {
  void loadProviders({ silent: true })
}

function onKeyEdit(row: PlatformKey) {
  if (!selected.value) return
  openKeyForm(row, selected.value)
}

function onOpenProbe(target: { name: string; key_id?: number; models?: string[] }) {
  openProbe(target)
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <span class="eyebrow">供给 / UPSTREAMS</span>
        <h2>提供商</h2>
        <p>左侧选择提供商，右侧管理它的 Key；列表异常优先，Key 表格支持搜索、筛选、排序与批量操作</p>
      </div>
      <div class="toolbar">
        <ui-switch v-model:value="live" size="small" aria-label="自动刷新" />
        <span class="live-label">自动刷新</span>
        <span v-if="lastRefresh" class="muted refresh-time">{{ lastRefresh }}</span>
        <ui-tooltip>
          <template #trigger><ui-button size="small" quaternary aria-label="刷新列表" :loading="loading" @click="refreshList"><template #icon><ui-icon><RefreshOutline /></ui-icon></template></ui-button></template>
          刷新列表
        </ui-tooltip>
        <ui-button size="small" :loading="refreshingBal" @click="refreshAll">刷新全部余额</ui-button>
        <ui-button type="primary" size="small" @click="openCreate">新建提供商</ui-button>
      </div>
    </div>

    <ui-alert v-if="error" type="error" :title="error" />

    <div class="layout">
      <ProviderRail :providers="orderedProviders" :selected-id="selectedId" :loading="loading" @select="onSelect" @create="openCreate" />

      <div v-if="!phone" class="detail-host">
        <div v-if="!selected" class="detail-empty">← 从左侧选择一家提供商，右侧管理它的 Key</div>
        <ProviderDetail
          v-else
          :provider="selected"
          :groups="routeGroups"
          :refresh-token="refreshToken"
          :probing="probing"
          @provider-action="onProviderAction"
          @key-edit="onKeyEdit"
          @open-probe="onOpenProbe"
          @changed="onDetailChanged"
        />
      </div>
    </div>

    <ui-drawer v-if="phone" :show="detailOpen" width="100vw" placement="left" @update:show="detailOpen = $event">
      <div v-if="selected" class="phone-detail">
        <div class="phone-head">
          <button class="back-btn" type="button" @click="detailOpen = false">← 返回提供商列表</button>
        </div>
        <ProviderDetail
          :provider="selected"
          :groups="routeGroups"
          :refresh-token="refreshToken"
          :probing="probing"
          @provider-action="onProviderAction"
          @key-edit="onKeyEdit"
          @open-probe="onOpenProbe"
          @changed="onDetailChanged"
        />
      </div>
    </ui-drawer>

    <ProbeModal v-model:show="showProbe" :target="probeTarget" @running="probing = $event" @completed="refreshToken++; void loadProviders({ silent: true })" />

    <ui-modal v-model:show="showForm" preset="card" :title="editing ? '编辑提供商' : '新建提供商'" style="width: min(560px, calc(100vw - 24px))">
      <ui-form ref="formRef" :model="form" :rules="rules" label-placement="left" label-width="90">
        <ui-form-item label="名称" path="name">
          <ui-input v-model:value="form.name" placeholder="例如 NewAPI-主池" />
        </ui-form-item>
        <ui-form-item label="Base URL" path="base_url">
          <ui-input v-model:value="form.base_url" placeholder="https://api.example.com" />
        </ui-form-item>
        <ui-form-item label="类型" path="kind">
          <ui-select v-model:value="form.kind" :options="KIND_OPTIONS" />
        </ui-form-item>
        <ui-form-item label="协议" path="protocols">
          <ui-checkbox-group v-model:value="form.protocols">
            <ui-space>
              <ui-checkbox v-for="opt in PROTOCOL_OPTIONS" :key="opt.value" :value="opt.value" :label="opt.label" />
            </ui-space>
          </ui-checkbox-group>
        </ui-form-item>
        <ui-form-item label="状态" path="status">
          <ui-radio-group v-model:value="form.status">
            <ui-radio v-for="opt in STATUS_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</ui-radio>
          </ui-radio-group>
        </ui-form-item>
        <ui-form-item v-if="form.kind === 'new_api'" label="accessToken" path="access_token">
          <div class="rate-field">
            <ui-input v-model:value="form.access_token" type="password" show-password-on="click" placeholder="Cookie（如 session=...）" />
            <div class="muted rate-hint">用于 GET /api/user/self 查余额。填浏览器 Cookie 的 session=...，不要带 Authorization。</div>
          </div>
        </ui-form-item>
        <ui-form-item v-if="form.kind === 'new_api'" label="New-Api-User" path="new_api_user_id">
          <div class="rate-field">
            <ui-input-number v-model:value="form.new_api_user_id" :min="1" :step="1" style="width: 100%" placeholder="例如 2809" />
            <div class="muted rate-hint">对应请求头 new-api-user，与 session 所属用户 id 一致。</div>
          </div>
        </ui-form-item>
        <ui-form-item label="并发" path="concurrency">
          <div class="rate-field">
            <ui-input-number v-model:value="form.concurrency" :min="0" style="width: 100%" />
            <div class="muted rate-hint">该提供商下所有 Key 共享此上限。上游接口无法自动发现，0 表示不限制。</div>
          </div>
        </ui-form-item>
        <ui-form-item label="备注" path="note">
          <ui-input v-model:value="form.note" type="textarea" :rows="2" placeholder="可选" />
        </ui-form-item>
      </ui-form>
      <template #footer>
        <ui-space justify="end">
          <ui-button @click="showForm = false">取消</ui-button>
          <ui-button type="primary" :loading="saving" @click="save">保存</ui-button>
        </ui-space>
      </template>
    </ui-modal>

    <KeyFormModal :show="showKeyForm" :provider="keyHost" :editing="editingKey" @update:show="showKeyForm = $event" @saved="onKeySaved" />
  </div>
</template>

<style scoped>
.page-head { align-items: center; flex-wrap: wrap; }
.live-label { font-size: 13px; color: #546c58; }
.layout { display: flex; gap: 14px; padding-top: 14px; align-items: flex-start; }
.detail-host { flex: 1; min-width: 0; }
.detail-empty {
  display: grid; place-items: center; min-height: 420px;
  border: 1px dashed var(--line, #e1e7df); border-radius: 12px;
  color: #8c9b84; font-size: 12px; background: #fbfcf8;
}
.phone-detail { height: 100%; overflow-y: auto; padding: 14px 14px 40px; }
.phone-head { margin-bottom: 10px; }
.back-btn {
  padding: 7px 12px; border-radius: 8px; background: #fff;
  border: 1px solid var(--line, #e1e7df); font-size: 12px; color: #546c58;
}
.rate-field { width: 100%; }
.rate-hint { margin-top: 6px; font-size: 12px; line-height: 1.5; }
@media (max-width: 760px) {
  .layout { padding-top: 10px; }
}
</style>
