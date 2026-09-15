<script setup lang="ts">
import { computed, h, onMounted, reactive, ref } from 'vue'
import { NTag, useMessage } from 'naive-ui'
import type { DataTableColumns, FormInst, FormRules, SelectOption } from 'naive-ui'
import { useRoute } from 'vue-router'
import { explainScheduler, flattenExplain, getModelCatalog, getScheduler, listConsumerKeys, syncModelCatalog, updateScheduler } from '@/api/admin'
import type { CatalogVendor, ConsumerKey, ProbeVendorId, Protocol, SchedulerCandidate, SchedulerSettings } from '@/api/types'
import { PROTOCOL_OPTIONS } from '@/api/types'
import HealthTag from '@/components/HealthTag.vue'
import { errText, formatTime } from '@/utils/format'

const message = useMessage()
const route = useRoute()
const loading = ref(false)
const explaining = ref(false)
const saving = ref(false)
const error = ref('')
const formRef = ref<FormInst | null>(null)
const candidates = ref<SchedulerCandidate[]>([])
const consumers = ref<ConsumerKey[]>([])
const routeBound = ref(false)
const showFiltered = ref(false)

const consumerOptions = computed<SelectOption[]>(() =>
  consumers.value.map((c) => {
    const groups = c.route_groups ?? []
    const suffix = groups.length ? groups.map((g) => g.name).join(' / ') : '不限'
    return { label: `${c.name} · ${suffix}`, value: c.id }
  }),
)

const filteredOutCount = computed(() => candidates.value.filter((c) => c.skip_reason === 'not_in_route_group').length)

const visibleCandidates = computed(() =>
  showFiltered.value ? candidates.value : candidates.value.filter((c) => c.skip_reason !== 'not_in_route_group'),
)

const SKIP_LABEL: Record<string, string> = {
  not_in_route_group: '不在分组',
  model_required: '未指定模型',
  route_model_mismatch: '分组模型不匹配',
  route_protocol_mismatch: '分组协议不匹配',
  route_group_disabled: '分组已停用',
  business_cooldown: '业务熔断中',
  recovery_pending: '等待恢复验证',
  recovery_inflight: '恢复验证进行中',
  excluded: '已排除',
  key_disabled: 'Key 停用',
  upstream_disabled: '提供商停用',
  protocol_mismatch: '协议不匹配',
  model_not_supported: '模型不在提供商列表',
  cooldown: '冷却中',
  low_balance: '余额不足',
  disabled: '已停用',
  down: '故障',
  route_rate_drift: '倍率漂出分组区间',
}

const CIRCUIT_LABEL: Record<string, string> = {
  legacy_health_unverified: '旧故障状态待验证',
  request_scope_failure: '接口请求失败',
  transport_failure: '连接或超时失败',
  invalid_response: '响应协议无效',
  response_failure: '响应未正常完成',
  cooldown_key_model: '接口限流',
  cooldown_key: '认证失败',
  key_quota_exhausted: 'Key 额度不足',
  capability_unsupported: '模型或接口不支持',
}

async function loadConsumers() {
  try {
    const res = await listConsumerKeys({ page: 1, page_size: 200 })
    consumers.value = res.items
  } catch {
    consumers.value = []
  }
}

const form = reactive<SchedulerSettings>({
	 circuit_window_sec: 60,
	 circuit_failure_threshold: 3,
	 circuit_cooldown_sec: 30,
	 circuit_max_cooldown_sec: 300,
  switch_improvement_ratio: 0.20,
  switch_improvement_ms: 2000,
  switch_confirm_sec: 60,
  exploration_ratio: 0.05,
  ranking_mode: 'adaptive',
  weight_success: 0.45,
  weight_cache: 0.3,
  weight_ttft: 0.25,
  epsilon: 0.08,
  window_minutes: 15,
  window_max_samples: 50,
  min_samples: 5,
  prior_success: 0.7,
  ttft_cap_ms: 8000,
  sticky_anthropic: true,
  sticky_openai: false,
  sticky_ttl_sec: 3600,
  failover_max: 2,
  retry_max: 1,
  cooldown_sec: 30,
  failure_window_sec: 60,
  failure_threshold: 8,
  probe_openai_model: 'gpt-4o-mini',
  probe_timeout_sec: 30,
  probe_anthropic_model: 'claude-3-haiku-20240307',
  probe_grok_model: 'grok-3-mini',
  probe_zhipu_model: 'glm-4.5-flash',
  probe_moonshot_model: 'kimi-k2-turbo-preview',
  probe_deepseek_model: 'deepseek-chat',
  filter_by_models: true,
})

const RANKING_OPTIONS = [
  { label: '稳定首字优先', value: 'stable_latency' },
  { label: '自适应', value: 'adaptive' },
  { label: '固定顺序', value: 'fixed_order' },
  { label: '缓存亲和', value: 'cache_affinity' },
  { label: '负载均衡', value: 'load_balance' },
]

const EMPTY_VENDORS: CatalogVendor[] = [
  { id: 'openai', name: 'OpenAI', protocol: 'openai', models: [] },
  { id: 'anthropic', name: 'Anthropic', protocol: 'anthropic', models: [] },
  { id: 'grok', name: 'Grok', protocol: 'openai', models: [] },
  { id: 'zhipu', name: '智谱', protocol: 'openai', models: [] },
  { id: 'moonshot', name: '月之暗面', protocol: 'openai', models: [] },
  { id: 'deepseek', name: 'Deepseek', protocol: 'openai', models: [] },
]

const catalogVendors = ref<CatalogVendor[]>(EMPTY_VENDORS)
const catalogSyncedAt = ref<string | null>(null)
const catalogCount = ref(0)
const catalogSource = ref('models.dev')
const syncingCatalog = ref(false)

const PROBE_FIELD: Record<ProbeVendorId, keyof SchedulerSettings> = {
  openai: 'probe_openai_model',
  anthropic: 'probe_anthropic_model',
  grok: 'probe_grok_model',
  zhipu: 'probe_zhipu_model',
  moonshot: 'probe_moonshot_model',
  deepseek: 'probe_deepseek_model',
}

function vendorOptions(vendor: CatalogVendor): SelectOption[] {
  return vendor.models.map((m) => ({
    label: m.name && m.name !== m.id ? `${m.name} · ${m.id}` : m.id,
    value: m.id,
  }))
}

function probeValue(vendor: CatalogVendor) {
  return form[PROBE_FIELD[vendor.id]] as string
}

function setProbeValue(vendor: CatalogVendor, value: string) {
  ;(form[PROBE_FIELD[vendor.id]] as string) = value
}

function applyCatalog(data: { vendors?: CatalogVendor[]; synced_at?: string | null; model_count?: number; source?: string }) {
  catalogVendors.value = data.vendors?.length ? data.vendors : EMPTY_VENDORS
  catalogSyncedAt.value = data.synced_at ?? null
  catalogCount.value = data.model_count ?? catalogVendors.value.reduce((n, v) => n + v.models.length, 0)
  if (data.source) catalogSource.value = data.source
}

async function loadCatalog() {
  try {
    applyCatalog(await getModelCatalog())
  } catch {
    catalogVendors.value = EMPTY_VENDORS
  }
}

async function syncCatalog(showToast = true) {
  syncingCatalog.value = true
  try {
    const data = await syncModelCatalog()
    applyCatalog(data)
    assignSettings(await getScheduler())
    if (showToast) message.success(data.message || `已同步 ${catalogCount.value} 个模型`)
  } catch (e) {
    message.error(errText(e, '同步模型库失败'))
  } finally {
    syncingCatalog.value = false
  }
}

const query = reactive({
  path: '/v1/responses',
  stream: true,
  session: '',
  protocol: 'anthropic' as Protocol,
  model: '',
  consumer: null as number | null,
})

const rules: FormRules = {
  weight_success: { required: true, type: 'number', message: '必填', trigger: 'blur' },
  epsilon: { required: true, type: 'number', message: '必填', trigger: 'blur' },
}

function assignSettings(s: SchedulerSettings) {
  Object.assign(form, s)
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    assignSettings(await getScheduler())
  } catch (e) {
    error.value = errText(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  await formRef.value?.validate()
  saving.value = true
  try {
    assignSettings(await updateScheduler({ ...form }))
    message.success('调度配置已保存')
    await loadExplain()
  } catch (e) {
    message.error(errText(e, '保存失败'))
  } finally {
    saving.value = false
  }
}

async function loadExplain() {
  explaining.value = true
  try {
    const data = await explainScheduler({
      path: query.protocol === 'anthropic' ? '/v1/messages' : query.path,
      stream: query.stream,
      session: query.session || undefined,
      protocol: query.protocol,
      model: query.model.trim() || undefined,
      consumer_key_id: query.consumer ?? undefined,
    })
    if (data.settings) assignSettings(data.settings)
    candidates.value = flattenExplain(data)
    routeBound.value = !!data.route_bound
  } catch (e) {
    message.error(errText(e, '解释失败'))
    candidates.value = []
  } finally {
    explaining.value = false
  }
}

function pct(n: number) {
  return `${(n * 100).toFixed(1)}%`
}

const columns: DataTableColumns<SchedulerCandidate> = [
	{ title: 'Key', key: 'key_name', width: 190, fixed: 'left', ellipsis: { tooltip: true } },
	{ title: '提供商', key: 'upstream_name', width: 160, ellipsis: { tooltip: true } },
	{ title: '探测时间', key: 'probe_at', width: 165, render: row => row.probe_at ? new Date(row.probe_at).toLocaleString() : '—' },
	{ title: '探测状态', key: 'probe_status', width: 110, render: row => ({healthy:'正常',degraded:'慢响应',down:'失败',unknown:'暂无探测'}[row.probe_status ?? 'unknown'] ?? row.probe_status) },
	{ title: '探测模型', key: 'probe_model', width: 150, ellipsis: { tooltip: true } },
	{ title: '探测接口', key: 'probe_path', width: 175, ellipsis: { tooltip: true } },
	{ title: '探测模式', key: 'probe_stream', width: 85, render: row => row.probe_path ? (row.probe_stream ? '流式' : '非流式') : '—' },
	{ title: '业务熔断', key: 'circuit_state', width: 100, render: row => ({closed:'未熔断',open:'熔断',half_open:'恢复验证'}[row.circuit_state ?? 'closed'] ?? row.circuit_state) },
	{ title: '影响范围', key: 'circuit_scope', width: 120, render: row => row.circuit_scope === 'key' ? '整个 Key' : row.circuit_scope === 'request' ? '当前模型与接口' : '—' },
	{ title: '熔断原因', key: 'circuit_reason', width: 155, ellipsis: { tooltip: true }, render: row => CIRCUIT_LABEL[row.circuit_reason ?? ''] ?? row.circuit_reason ?? '—' },
	{ title: '冷却 / 租约截止', key: 'circuit_until', width: 165, render: row => row.circuit_until ? new Date(row.circuit_until).toLocaleString() : '—' },
  { title: '选路原因', key: 'decision_reason', width: 140, ellipsis: { tooltip: true }, render: row => row.decision_reason === 'recovery_validation' ? '恢复验证' : row.decision_reason },
  { title: '延迟样本', key: 'latency_samples', width: 85 },
  {
    title: '选中',
    key: 'selected',
    width: 70,
    render(row) {
      return row.selected ? h(NTag, { type: 'success', size: 'small', bordered: false }, { default: () => '是' }) : '—'
    },
  },
  {
    title: '带内',
    key: 'in_band',
    width: 70,
    render(row) {
      return row.in_band ? h(NTag, { type: 'info', size: 'small', bordered: false }, { default: () => '近优' }) : '—'
    },
  },
  {
    title: '健康',
    key: 'health_status',
    width: 110,
    render(row) {
      return h(HealthTag, { status: row.health_status })
    },
  },
  {
    title: '质量',
    key: 'quality',
    width: 80,
    render(row) {
      return row.eligible ? row.quality.toFixed(3) : '—'
    },
  },
  {
    title: '有效成本',
    key: 'effective_cost',
    width: 90,
    render(row) {
      return row.eligible ? row.effective_cost.toFixed(3) : '—'
    },
  },
  {
    title: '倍率',
    key: 'rate',
    width: 70,
    render(row) {
      return row.rate ? row.rate.toFixed(3) : '—'
    },
  },
  {
    title: '成功',
    key: 'success_rate',
    width: 70,
    render(row) {
      return row.samples ? pct(row.success_rate) : '—'
    },
  },
  {
    title: '缓存',
    key: 'cache_rate',
    width: 70,
    render(row) {
      return row.samples ? pct(row.cache_rate) : '—'
    },
  },
  {
    title: 'TTFT p50',
    key: 'ttft_p50',
    width: 90,
    render(row) {
      return row.ttft_p50 ? `${row.ttft_p50}ms` : '—'
    },
  },
  { title: '样本', key: 'samples', width: 60 },
  {
    title: '运行负载',
    key: 'key_inflight',
    width: 135,
    render(row) {
      return `RPM ${row.current_rpm}/${row.rpm_limit || '不限'} · 并发 ${row.key_inflight}/${row.max_concurrency || '不限'}`
    },
  },
  {
    title: '原因',
    key: 'skip_reason',
    render(row) {
      if (row.selected) return '选中'
      if (row.in_band) return '带内未选'
      if (row.skip_reason) return SKIP_LABEL[row.skip_reason] ?? row.skip_reason
      return row.eligible ? '质量带外' : '—'
    },
  },
]

onMounted(async () => {
  const wanted = Number(route.query.consumer)
  await Promise.all([load(), loadConsumers(), loadCatalog()])
  if (wanted && consumers.value.some((c) => c.id === wanted)) query.consumer = wanted
  const explain = loadExplain()
  if (!catalogCount.value) await syncCatalog(false)
  await explain
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h2>调度</h2>
        <p>{{ RANKING_OPTIONS.find(option => option.value === form.ranking_mode)?.label }}</p>
      </div>
      <n-button type="primary" size="small" :loading="saving" @click="save">保存配置</n-button>
    </div>

    <n-alert v-if="error" type="error" :title="error" />

    <n-form ref="formRef" :model="form" :rules="rules" label-placement="left" label-width="128">
      <div class="cards">
        <n-card v-if="form.ranking_mode !== 'stable_latency'" size="small" title="质量评分" :bordered="false" :loading="loading">
          <p class="muted card-hint">三项是相对比例。没有真实调用时不计缓存，剩余权重按比例放大。</p>
          <div class="grid">
            <n-form-item label="成功率权重" path="weight_success">
              <n-input-number v-model:value="form.weight_success" :min="0" :max="1" :step="0.05" style="width: 100%" />
            </n-form-item>
            <n-form-item label="缓存率权重" path="weight_cache">
              <n-input-number v-model:value="form.weight_cache" :min="0" :max="1" :step="0.05" style="width: 100%" />
            </n-form-item>
            <n-form-item label="TTFT 权重" path="weight_ttft">
              <n-input-number v-model:value="form.weight_ttft" :min="0" :max="1" :step="0.05" style="width: 100%" />
            </n-form-item>
          </div>
        </n-card>
        <n-card v-if="form.ranking_mode === 'stable_latency'" size="small" title="稳定首字优先" :bordered="false">
          <div class="grid">
            <n-form-item label="最小改善比例"><n-input-number v-model:value="form.switch_improvement_ratio" :min="0.01" :max="1" :step="0.05" /></n-form-item>
            <n-form-item label="最小改善 ms"><n-input-number v-model:value="form.switch_improvement_ms" :min="1" :step="500" /></n-form-item>
            <n-form-item label="改善确认秒数"><n-input-number v-model:value="form.switch_confirm_sec" :min="1" :step="10" /></n-form-item>
          </div>
        </n-card>
        <n-card size="small" title="观测窗口" :bordered="false" :loading="loading">
          <div class="grid">
            <n-form-item label="窗口分钟">
              <n-input-number v-model:value="form.window_minutes" :min="1" :max="180" style="width: 100%" />
            </n-form-item>
            <n-form-item label="窗口样本">
              <n-input-number v-model:value="form.window_max_samples" :min="10" :max="500" style="width: 100%" />
            </n-form-item>
            <n-form-item label="最少样本">
              <n-input-number v-model:value="form.min_samples" :min="1" :max="50" style="width: 100%" />
            </n-form-item>
            <n-form-item v-if="form.ranking_mode !== 'stable_latency'" label="TTFT 上限 ms">
              <n-input-number v-model:value="form.ttft_cap_ms" :min="500" :max="30000" style="width: 100%" />
            </n-form-item>
          </div>
        </n-card>
        <n-card size="small" title="选路策略" :bordered="false" :loading="loading">
          <div class="grid">
            <n-form-item label="候选排序">
              <n-select v-model:value="form.ranking_mode" :options="RANKING_OPTIONS" style="width: 100%" />
            </n-form-item>
            <n-form-item v-if="form.ranking_mode !== 'stable_latency'" label="近优带宽 ε" path="epsilon">
              <n-input-number v-model:value="form.epsilon" :min="0.01" :max="0.5" :step="0.01" style="width: 100%" />
            </n-form-item>
            <n-form-item label="按模型列表过滤">
              <n-space align="center" size="small">
                <n-switch v-model:value="form.filter_by_models" />
                <span class="muted">Key 已获取的模型列表非空且不含请求 model 时跳过该 Key</span>
              </n-space>
            </n-form-item>
          </div>
        </n-card>
        <n-card size="small" title="故障与粘滞" :bordered="false" :loading="loading">
          <div class="grid">
			<n-form-item label="探索 / 恢复比例"><n-input-number v-model:value="form.exploration_ratio" :min="0" :max="0.05" :step="0.01" style="width: 100%" /></n-form-item>
            <n-form-item label="故障转移次数">
              <n-input-number v-model:value="form.failover_max" :min="1" :max="5" style="width: 100%" />
            </n-form-item>
            <n-form-item label="故障重试次数">
              <n-input-number v-model:value="form.retry_max" :min="0" :max="5" style="width: 100%" />
            </n-form-item>
            <n-form-item label="熔断初始冷却秒">
              <n-input-number v-model:value="form.circuit_cooldown_sec" :min="1" :max="600" style="width: 100%" />
            </n-form-item>
            <n-form-item label="熔断最长冷却秒">
              <n-input-number v-model:value="form.circuit_max_cooldown_sec" :min="form.circuit_cooldown_sec" :max="3600" style="width: 100%" />
            </n-form-item>
            <n-form-item label="业务失败窗口秒">
              <n-input-number v-model:value="form.circuit_window_sec" :min="1" :max="3600" style="width: 100%" />
            </n-form-item>
            <n-form-item label="连续失败请求数">
              <n-input-number v-model:value="form.circuit_failure_threshold" :min="1" :max="100" style="width: 100%" />
            </n-form-item>
            <n-form-item label="粘滞 TTL 秒">
              <n-input-number v-model:value="form.sticky_ttl_sec" :min="60" :max="86400" style="width: 100%" />
            </n-form-item>
            <n-form-item label="Anthropic 粘滞">
              <n-switch v-model:value="form.sticky_anthropic" />
            </n-form-item>
            <n-form-item label="OpenAI 粘滞">
              <n-switch v-model:value="form.sticky_openai" />
            </n-form-item>
          </div>
        </n-card>
      </div>
    </n-form>

    <n-card size="small" title="探测模型" :bordered="false" :loading="loading">
      <template #header-extra>
        <n-space align="center" size="small">
          <span class="muted">
            {{ catalogSource }}
            ·
            {{ catalogCount ? `${catalogCount} 个模型` : '未同步' }}
            <template v-if="catalogSyncedAt"> · {{ formatTime(catalogSyncedAt) }}</template>
          </span>
          <n-button size="small" :loading="syncingCatalog" @click="syncCatalog()">同步模型库</n-button>
        </n-space>
      </template>
      <n-form :model="form" label-placement="left" label-width="128" class="grid">
        <n-form-item v-for="vendor in catalogVendors" :key="vendor.id" :label="vendor.name">
          <n-select
            :value="probeValue(vendor)"
            filterable
            :options="vendorOptions(vendor)"
            :fallback-option="(value: string) => ({ label: value, value })"
            :placeholder="vendor.models.length ? '从模型库选择' : '请先同步模型库'"
            :loading="syncingCatalog"
            @update:value="(value: string) => setProbeValue(vendor, value)"
          />
        </n-form-item>
        <n-form-item label="探测超时（秒）">
          <n-input-number v-model:value="form.probe_timeout_sec" :min="1" :max="300" :precision="0" :step="5" style="width: 100%" />
        </n-form-item>
      </n-form>
    </n-card>

    <n-card size="small" title="候选解释" :bordered="false">
      <n-space style="margin-bottom: 12px" align="center">
        <n-select v-if="query.protocol === 'openai'" v-model:value="query.path" :options="[{ label: 'Responses', value: '/v1/responses' }, { label: 'Chat Completions', value: '/v1/chat/completions' }]" style="width: 180px" />
        <n-checkbox v-model:checked="query.stream">流式</n-checkbox>
        <n-input v-model:value="query.session" placeholder="会话 ID（可选）" style="width: 200px" />
        <n-select v-model:value="query.protocol" :options="PROTOCOL_OPTIONS" style="width: 150px" />
        <n-input v-model:value="query.model" placeholder="模型，如 claude-sonnet-4" style="width: 240px" />
        <n-select
          v-model:value="query.consumer"
          :options="consumerOptions"
          clearable
          filterable
          placeholder="以 API 密钥视角（可选）"
          style="width: 260px"
        />
        <n-button size="small" :loading="explaining" @click="loadExplain">预览选路</n-button>
        <n-checkbox v-if="filteredOutCount" v-model:checked="showFiltered" size="small">
          显示被分组过滤的 {{ filteredOutCount }} 把
        </n-checkbox>
      </n-space>
      <n-alert v-if="query.consumer && !routeBound" type="info" :bordered="false" style="margin-bottom: 8px">
        该 API 密钥未绑定分组，在全部 Key 里调度
      </n-alert>
      <n-alert
        v-else-if="query.consumer && routeBound && !visibleCandidates.length"
        type="warning"
        :bordered="false"
        style="margin-bottom: 8px"
      >
        该 API 密钥绑定的分组里没有匹配此协议/模型的 Key，请求会返回 503
      </n-alert>
      <n-data-table
        :scroll-x="3300"
        size="small"
        :columns="columns"
        :data="visibleCandidates"
        :loading="explaining"
        :row-class-name="(row: SchedulerCandidate) => (row.selected ? 'row-selected' : '')"
      />
    </n-card>
  </div>
</template>

<style scoped>
.cards {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-bottom: 10px;
}
.card-hint {
  margin: 0 0 8px;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(280px, 100%), 1fr));
  gap: 0 16px;
}
@media (max-width: 640px) {
  :deep(.n-card-header) { flex-wrap: wrap; gap: 8px; }
  :deep(.n-card-header__main) { flex-shrink: 0; }
  :deep(.n-card-header__extra) { margin-left: 0; max-width: 100%; }
  :deep(.n-form-item) { flex-direction: column; }
  :deep(.n-form-item-label) { width: auto !important; justify-content: flex-start; padding-bottom: 6px; }
  :deep(.n-form-item-blank), :deep(.n-input-number) { width: 100%; min-width: 0; }
  :deep(.n-card__content) { padding: 12px; }
}
:deep(.row-selected td) {
  background: #ecfdf5 !important;
}
</style>
