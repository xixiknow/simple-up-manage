<script setup lang="ts">
import { computed, h, onMounted, reactive, ref } from 'vue'
import { NButton, NSpace, NTag, NTooltip, useDialog, useMessage } from 'naive-ui'
import type { DataTableColumns, FormInst, FormRules, SelectOption } from 'naive-ui'
import {
  createConsumerKey,
  createRouteGroup,
  deleteConsumerKey,
  deleteRouteGroup,
  getConsumerKeySecret,
  listConsumerKeys,
  listRouteGroups,
  testConsumerKey,
  updateConsumerKey,
  updateRouteGroup,
} from '@/api/admin'
import {
  PROTOCOL_LABEL,
  PROTOCOL_OPTIONS,
  STATUS_OPTIONS,
  type ConsumerKey,
  type ConsumerKeyPayload,
  type EnableStatus,
  type Protocol,
  type RouteGroup,
  type RouteGroupPayload,
} from '@/api/types'
import RouteGroupKeyPicker from '@/components/RouteGroupKeyPicker.vue'
import StatusTag from '@/components/StatusTag.vue'
import { copyText, errText, formatMoney, formatRate, formatTime } from '@/utils/format'

const message = useMessage()
const dialog = useDialog()

const keysLoading = ref(false)
const groupsLoading = ref(false)
const error = ref('')
const items = ref<ConsumerKey[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const groups = ref<RouteGroup[]>([])
const testingId = ref<number | null>(null)
const copyingId = ref<number | null>(null)
const pausingId = ref<number | null>(null)

const showKeyForm = ref(false)
const keySaving = ref(false)
const editingKey = ref<ConsumerKey | null>(null)
const keyFormRef = ref<FormInst | null>(null)
const keyForm = reactive<ConsumerKeyPayload & { route_group_ids: number[] }>({
  name: '',
  status: 'enabled',
  quota_usd: 0,
  rpm: 60,
  route_group_ids: [],
})

const revealShow = ref(false)
const revealed = ref('')

const showGroupForm = ref(false)
const groupSaving = ref(false)
const editingGroup = ref<RouteGroup | null>(null)
const groupFormRef = ref<FormInst | null>(null)
const groupForm = reactive({
  name: '',
  protocol: '' as Protocol | '',
  models: [] as string[],
  rate_min: null as number | null,
  rate_max: null as number | null,
  description: '',
  status: 'enabled' as EnableStatus,
})

const pickerShow = ref(false)
const pickerGroup = ref<RouteGroup | null>(null)

const routeGroupById = computed(() => Object.fromEntries(groups.value.map((g) => [g.id, g])))

const routeGroupOptions = computed<SelectOption[]>(() =>
  groups.value.map((g) => ({
    label: g.name,
    value: g.id,
    disabled: g.status !== 'enabled',
    group: g,
  })),
)

const protocolFormOptions = [{ label: '不限', value: '' }, ...PROTOCOL_OPTIONS]

function protocolLabel(p?: Protocol | '' | null) {
  return p ? PROTOCOL_LABEL[p] : '不限'
}

function rateRangeText(g: RouteGroup) {
  const min = g.rate_min ?? null
  const max = g.rate_max ?? null
  if (min === null && max === null) return '不限'
  return `[${min === null ? '0' : `×${formatRate(min)}`}, ${max === null ? '∞' : `×${formatRate(max)}`})`
}

function renderGroupOption(option: SelectOption) {
  const g = option.group as RouteGroup
  const parts = [g.protocol ? PROTOCOL_LABEL[g.protocol] : '协议不限', `${g.member_count} 把 Key`]
  if (g.models?.length) parts.push(g.models.join(', '))
  if (g.status !== 'enabled') parts.push('已停用')
  return h('div', { class: 'rg-option' }, [
    h('span', { class: 'rg-option-name' }, g.name),
    h('span', { class: ['rg-option-meta', g.member_count === 0 ? 'warn' : ''] }, parts.join(' · ')),
  ])
}

const formKeySummary = computed(() => {
  const ids = keyForm.route_group_ids
  if (!ids.length) return null
  const union = new Set<number>()
  const empty: string[] = []
  for (const id of ids) {
    const g = routeGroupById.value[id]
    if (!g) continue
    if (g.member_count === 0) empty.push(g.name)
    for (const k of g.key_ids) union.add(k)
  }
  return { keys: union.size, empty }
})

const keyRules: FormRules = {
  name: { required: true, message: '请输入名称', trigger: 'blur' },
}

const groupRules: FormRules = {
  name: { required: true, message: '请输入名称', trigger: 'blur' },
}

async function loadKeys() {
  keysLoading.value = true
  error.value = ''
  try {
    const res = await listConsumerKeys({ page: page.value, page_size: pageSize.value })
    items.value = res.items
    total.value = res.total
  } catch (e) {
    error.value = errText(e)
    items.value = []
  } finally {
    keysLoading.value = false
  }
}

async function loadGroups() {
  groupsLoading.value = true
  try {
    groups.value = await listRouteGroups()
  } catch (e) {
    groups.value = []
    if (!error.value) error.value = errText(e)
  } finally {
    groupsLoading.value = false
  }
}

async function reloadAll() {
  await Promise.all([loadKeys(), loadGroups()])
}

function openCreateKey() {
  editingKey.value = null
  Object.assign(keyForm, { name: '', status: 'enabled' as EnableStatus, quota_usd: 0, rpm: 60, route_group_ids: [] })
  showKeyForm.value = true
}

function openEditKey(row: ConsumerKey) {
  editingKey.value = row
  Object.assign(keyForm, {
    name: row.name,
    status: row.status,
    quota_usd: row.quota_usd,
    rpm: row.rpm,
    route_group_ids: (row.route_groups ?? []).map((g) => g.id),
  })
  showKeyForm.value = true
}

async function saveKey() {
  await keyFormRef.value?.validate()
  keySaving.value = true
  try {
    const payload: ConsumerKeyPayload = {
      name: keyForm.name.trim(),
      status: keyForm.status,
      quota_usd: Number(keyForm.quota_usd) || 0,
      rpm: Number(keyForm.rpm) || 0,
      route_group_ids: keyForm.route_group_ids,
    }
    if (editingKey.value) {
      await updateConsumerKey(editingKey.value.id, payload)
      message.success('已保存')
    } else {
      const created = await createConsumerKey(payload)
      if (created.key) {
        revealed.value = created.key
        revealShow.value = true
      } else {
        message.success('已创建')
      }
    }
    showKeyForm.value = false
    await reloadAll()
  } catch (e) {
    message.error(errText(e, '保存失败'))
  } finally {
    keySaving.value = false
  }
}

async function copyKey(row: ConsumerKey) {
  copyingId.value = row.id
  try {
    const { key } = await getConsumerKeySecret(row.id)
    const ok = await copyText(key)
    if (ok) message.success('已复制完整密钥')
    else message.warning('复制失败，请手动选择')
  } catch (e) {
    message.error(errText(e, '读取密钥失败'))
  } finally {
    copyingId.value = null
  }
}

async function copyRevealed() {
  const ok = await copyText(revealed.value)
  if (ok) message.success('已复制')
  else message.warning('复制失败，请手动选择')
}

async function togglePause(row: ConsumerKey) {
  const next: EnableStatus = row.status === 'enabled' ? 'disabled' : 'enabled'
  pausingId.value = row.id
  try {
    await updateConsumerKey(row.id, { name: row.name, status: next, quota_usd: row.quota_usd, rpm: row.rpm })
    message.success(next === 'disabled' ? '已暂停' : '已启用')
    await loadKeys()
  } catch (e) {
    message.error(errText(e, '更新状态失败'))
  } finally {
    pausingId.value = null
  }
}

async function runTest(row: ConsumerKey) {
  if (row.status !== 'enabled') {
    message.warning('请先启用该密钥再测试')
    return
  }
  testingId.value = row.id
  try {
    const out = await testConsumerKey(row.id)
    if (out.success) {
      message.success(`测试成功 · ${out.model || ''} · ${out.duration_ms}ms`)
    } else {
      message.error(`测试失败${out.status ? ` (${out.status})` : ''}：${out.error || '未知错误'}`)
    }
    await loadKeys()
  } catch (e) {
    message.error(errText(e, '测试失败'))
  } finally {
    testingId.value = null
  }
}

function confirmDeleteKey(row: ConsumerKey) {
  dialog.warning({
    title: '删除 API 密钥',
    content: `确认删除「${row.name}」(${row.key_preview})？`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await deleteConsumerKey(row.id)
        message.success('已删除')
        await loadKeys()
      } catch (e) {
        message.error(errText(e, '删除失败'))
      }
    },
  })
}

function openCreateGroup() {
  editingGroup.value = null
  Object.assign(groupForm, {
    name: '',
    protocol: '',
    models: [],
    rate_min: null,
    rate_max: null,
    description: '',
    status: 'enabled' as EnableStatus,
  })
  showGroupForm.value = true
}

function openEditGroup(g: RouteGroup) {
  editingGroup.value = g
  Object.assign(groupForm, {
    name: g.name,
    protocol: g.protocol || '',
    models: [...(g.models ?? [])],
    rate_min: g.rate_min ?? null,
    rate_max: g.rate_max ?? null,
    description: g.description ?? '',
    status: g.status,
  })
  showGroupForm.value = true
}

async function saveGroup() {
  await groupFormRef.value?.validate()
  if (groupForm.rate_min !== null && groupForm.rate_max !== null && groupForm.rate_min > groupForm.rate_max) {
    message.warning('参考倍率下限不能大于上限')
    return
  }
  groupSaving.value = true
  try {
    const payload: RouteGroupPayload = {
      name: groupForm.name.trim(),
      protocol: groupForm.protocol,
      models: groupForm.models,
      description: groupForm.description.trim(),
      status: groupForm.status,
    }
    if (groupForm.rate_min === null && groupForm.rate_max === null) {
      payload.clear_rate = true
    } else {
      payload.rate_min = groupForm.rate_min
      payload.rate_max = groupForm.rate_max
    }
    if (editingGroup.value) {
      await updateRouteGroup(editingGroup.value.id, payload)
      message.success('已保存')
    } else {
      await createRouteGroup(payload)
      message.success('已创建，可点「选择提供商 Key」加入成员')
    }
    showGroupForm.value = false
    await loadGroups()
  } catch (e) {
    message.error(errText(e, '保存失败'))
  } finally {
    groupSaving.value = false
  }
}

function openPicker(g: RouteGroup) {
  pickerGroup.value = g
  pickerShow.value = true
}

function confirmDeleteGroup(g: RouteGroup) {
  const consumers = g.consumers ?? []
  const lines = [`确认删除分组「${g.name}」？成员 Key 本身不会被删除。`]
  if (consumers.length) {
    lines.push('', `以下 ${consumers.length} 把 API 密钥绑定了该分组，删除后它们将失去这些 Key：`)
    lines.push(consumers.map((c) => `· ${c.name || `#${c.id}`}`).join('\n'))
  }
  dialog.warning({
    title: '删除分组',
    content: () => h('pre', { class: 'dialog-pre' }, lines.join('\n')),
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await deleteRouteGroup(g.id)
        message.success('已删除')
        await reloadAll()
      } catch (e) {
        message.error(errText(e, '删除失败'))
      }
    },
  })
}

const keyColumns = computed((): DataTableColumns<ConsumerKey> => [
  { title: '名称', key: 'name', ellipsis: { tooltip: true }, minWidth: 120 },
  {
    title: '密钥',
    key: 'key_preview',
    width: 220,
    render(row) {
      return h('div', { class: 'key-cell' }, [
        h('span', { class: 'preview' }, row.key_preview || '—'),
        h(
          NButton,
          {
            size: 'tiny',
            quaternary: true,
            loading: copyingId.value === row.id,
            onClick: () => void copyKey(row),
          },
          { default: () => '复制' },
        ),
      ])
    },
  },
  {
    title: '分组',
    key: 'route_groups',
    minWidth: 160,
    render(row) {
      const bound = row.route_groups ?? []
      if (!bound.length) {
        return h(
          NTooltip,
          { trigger: 'hover' },
          {
            trigger: () => h(NTag, { size: 'tiny', bordered: false }, { default: () => '不限' }),
            default: () => '未绑定分组，调度时在所有提供商 Key 里选',
          },
        )
      }
      return h(
        'div',
        { class: 'tag-row' },
        bound.map((g) => {
          const full = routeGroupById.value[g.id]
          const empty = full ? full.member_count === 0 : false
          return h(
            NTooltip,
            { trigger: 'hover' },
            {
              trigger: () =>
                h(NTag, { size: 'tiny', bordered: false, type: empty ? 'warning' : 'info' }, { default: () => g.name }),
              default: () =>
                full
                  ? `${full.member_count} 把 Key · ${full.protocol ? PROTOCOL_LABEL[full.protocol] : '协议不限'}${empty ? ' · 该分组没有成员，请求会 503' : ''}`
                  : g.name,
            },
          )
        }),
      )
    },
  },
  {
    title: '消费明细',
    key: 'quota_used',
    width: 120,
    render(row) {
      return `${formatMoney(row.quota_used)} 元`
    },
  },
  {
    title: '状态',
    key: 'status',
    width: 90,
    render(row) {
      return h(StatusTag, { status: row.status })
    },
  },
  {
    title: '上次使用',
    key: 'last_used_at',
    width: 170,
    render(row) {
      return formatTime(row.last_used_at)
    },
  },
  {
    title: '操作',
    key: 'actions',
    width: 220,
    render(row) {
      return h(NSpace, { size: 4 }, {
        default: () => [
          h(
            NButton,
            { size: 'tiny', loading: testingId.value === row.id, onClick: () => void runTest(row) },
            { default: () => '测试' },
          ),
          h(
            NButton,
            { size: 'tiny', loading: pausingId.value === row.id, onClick: () => void togglePause(row) },
            { default: () => (row.status === 'enabled' ? '暂停' : '启用') },
          ),
          h(NButton, { size: 'tiny', onClick: () => openEditKey(row) }, { default: () => '编辑' }),
          h(
            NButton,
            { size: 'tiny', type: 'error', quaternary: true, onClick: () => confirmDeleteKey(row) },
            { default: () => '删除' },
          ),
        ],
      })
    },
  },
])

const groupColumns: DataTableColumns<RouteGroup> = [
  { title: '名称', key: 'name', ellipsis: { tooltip: true }, minWidth: 120 },
  {
    title: '协议',
    key: 'protocol',
    width: 100,
    render(row) {
      return h(NTag, { size: 'tiny', bordered: false, type: row.protocol ? 'info' : 'default' }, { default: () => protocolLabel(row.protocol) })
    },
  },
  {
    title: '模型',
    key: 'models',
    minWidth: 140,
    ellipsis: { tooltip: true },
    render(row) {
      return row.models?.length ? row.models.join(', ') : '不限'
    },
  },
  {
    title: '参考倍率',
    key: 'rate',
    width: 140,
    render(row) {
      return rateRangeText(row)
    },
  },
  {
    title: '成员',
    key: 'member_count',
    width: 90,
    render(row) {
      return h('span', { class: row.member_count === 0 ? 'warn' : '' }, `${row.member_count} 把`)
    },
  },
  {
    title: '绑定密钥',
    key: 'consumer_count',
    width: 100,
    render(row) {
      return `${row.consumer_count}`
    },
  },
  {
    title: '状态',
    key: 'status',
    width: 90,
    render(row) {
      return h(StatusTag, { status: row.status })
    },
  },
  {
    title: '操作',
    key: 'actions',
    width: 240,
    render(row) {
      return h(NSpace, { size: 4 }, {
        default: () => [
          h(NButton, { size: 'tiny', onClick: () => openEditGroup(row) }, { default: () => '编辑' }),
          h(NButton, { size: 'tiny', onClick: () => openPicker(row) }, { default: () => '选择提供商 Key' }),
          h(
            NButton,
            { size: 'tiny', type: 'error', quaternary: true, onClick: () => confirmDeleteGroup(row) },
            { default: () => '删除' },
          ),
        ],
      })
    },
  },
]

onMounted(() => {
  void reloadAll()
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h2>API 密钥</h2>
        <p>上表是调用方凭证；下表是路由分组。密钥绑定分组后只在分组成员里选提供商 Key，未绑定则不限</p>
      </div>
    </div>

    <n-alert v-if="error" type="error" :title="error" />

    <n-card size="small" title="我的密钥" :bordered="false">
      <template #header-extra>
        <n-button type="primary" size="small" @click="openCreateKey">新建密钥</n-button>
      </template>
      <n-data-table
        size="small"
        :columns="keyColumns"
        :data="items"
        :loading="keysLoading"
        :scroll-x="1100"
        :pagination="{
          page,
          pageSize,
          itemCount: total,
          showSizePicker: true,
          pageSizes: [20, 50, 100],
          onChange: (p: number) => {
            page = p
            loadKeys()
          },
          onUpdatePageSize: (s: number) => {
            pageSize = s
            page = 1
            loadKeys()
          },
        }"
      />
    </n-card>

    <n-card size="small" title="分组" :bordered="false">
      <template #header-extra>
        <n-button type="primary" size="small" @click="openCreateGroup">新建分组</n-button>
      </template>
      <n-data-table size="small" :columns="groupColumns" :data="groups" :loading="groupsLoading" :scroll-x="980" />
    </n-card>

    <n-modal v-model:show="showKeyForm" preset="card" :title="editingKey ? '编辑 API 密钥' : '新建 API 密钥'" style="width: 480px">
      <n-form ref="keyFormRef" :model="keyForm" :rules="keyRules" label-placement="left" label-width="90">
        <n-form-item label="名称" path="name">
          <n-input v-model:value="keyForm.name" />
        </n-form-item>
        <n-form-item label="额度" path="quota_usd">
          <n-input-number v-model:value="keyForm.quota_usd" :min="0" :step="1" style="width: 100%" />
          <template #feedback>
            <span class="muted">0 表示不限额度。消费明细按实际 token × 价格累计，与额度无关</span>
          </template>
        </n-form-item>
        <n-form-item label="RPM" path="rpm">
          <n-input-number v-model:value="keyForm.rpm" :min="0" :step="1" style="width: 100%" />
        </n-form-item>
        <n-form-item label="分组" path="route_group_ids">
          <div style="width: 100%">
            <n-select
              v-model:value="keyForm.route_group_ids"
              :options="routeGroupOptions"
              :render-label="renderGroupOption"
              multiple
              clearable
              filterable
              placeholder="不选 = 不限，在全部 Key 里调度"
              max-tag-count="responsive"
            >
              <template #action>
                <n-button text size="tiny" @click="openCreateGroup">+ 新建分组</n-button>
              </template>
              <template #empty>
                <div class="muted" style="padding: 6px 0">
                  还没有分组。
                  <n-button text size="tiny" type="primary" @click="openCreateGroup">去新建</n-button>
                </div>
              </template>
            </n-select>
            <div class="muted" style="margin-top: 6px">
              <template v-if="formKeySummary">
                可用提供商 Key 共 <strong>{{ formKeySummary.keys }}</strong> 把
                <span v-if="formKeySummary.empty.length" class="warn">
                  · 「{{ formKeySummary.empty.join('」「') }}」没有成员
                </span>
              </template>
              <template v-else>未绑定：在所有提供商 Key 里按价格/质量调度</template>
            </div>
          </div>
        </n-form-item>
        <n-form-item label="状态" path="status">
          <n-radio-group v-model:value="keyForm.status">
            <n-radio v-for="opt in STATUS_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</n-radio>
          </n-radio-group>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showKeyForm = false">取消</n-button>
          <n-button type="primary" :loading="keySaving" @click="saveKey">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <n-modal v-model:show="revealShow" preset="card" title="请立即保存 API 密钥" style="width: 520px" :mask-closable="false">
      <n-alert type="warning" title="列表只显示预览；完整密钥可用「复制」随时取出" style="margin-bottom: 12px" />
      <div class="reveal-box">{{ revealed }}</div>
      <template #footer>
        <n-space justify="end">
          <n-button @click="copyRevealed">复制</n-button>
          <n-button type="primary" @click="revealShow = false">我已保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <n-modal v-model:show="showGroupForm" preset="card" :title="editingGroup ? '编辑分组' : '新建分组'" style="width: 540px">
      <n-form ref="groupFormRef" :model="groupForm" :rules="groupRules" label-placement="left" label-width="100">
        <n-form-item label="名称" path="name">
          <n-input v-model:value="groupForm.name" placeholder="如 OpenAI-A" />
        </n-form-item>
        <n-form-item label="协议" path="protocol">
          <n-select v-model:value="groupForm.protocol" :options="protocolFormOptions" />
          <template #feedback>
            <span class="muted">仅该协议的请求会用到此分组；不限则两种协议都可</span>
          </template>
        </n-form-item>
        <n-form-item label="模型模式" path="models">
          <n-dynamic-tags v-model:value="groupForm.models" />
          <template #feedback>
            <span class="muted">可选。如 grok-*、deepseek-*；留空表示所有模型。回车添加</span>
          </template>
        </n-form-item>
        <n-form-item label="参考倍率">
          <n-space align="center" :wrap="false">
            <n-input-number v-model:value="groupForm.rate_min" :min="0" :step="0.01" clearable placeholder="下限（含）" style="width: 140px" />
            <span class="muted">~</span>
            <n-input-number v-model:value="groupForm.rate_max" :min="0" :step="0.01" clearable placeholder="上限（不含）" style="width: 140px" />
          </n-space>
          <template #feedback>
            <span class="muted">可选。成员倍率漂出区间会被标红，并在调度时跳过</span>
          </template>
        </n-form-item>
        <n-form-item label="描述" path="description">
          <n-input v-model:value="groupForm.description" type="textarea" :autosize="{ minRows: 1, maxRows: 3 }" />
        </n-form-item>
        <n-form-item label="状态" path="status">
          <n-radio-group v-model:value="groupForm.status">
            <n-radio v-for="opt in STATUS_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</n-radio>
          </n-radio-group>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showGroupForm = false">取消</n-button>
          <n-button type="primary" :loading="groupSaving" @click="saveGroup">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <RouteGroupKeyPicker v-model:show="pickerShow" :group="pickerGroup" :groups="groups" @saved="reloadAll" />
  </div>
</template>

<style scoped>
.key-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}
.tag-row {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.warn {
  color: #d92d20;
}
:deep(.rg-option) {
  display: flex;
  flex-direction: column;
  line-height: 1.3;
  padding: 2px 0;
}
:deep(.rg-option-meta) {
  font-size: 11px;
  color: #667085;
}
:deep(.rg-option-meta.warn) {
  color: #d92d20;
}
:deep(.dialog-pre) {
  margin: 0;
  white-space: pre-wrap;
  font-family: inherit;
  font-size: 13px;
  line-height: 1.6;
}
</style>
