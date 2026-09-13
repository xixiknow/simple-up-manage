export type UpstreamKind = 'sub2api' | 'new_api' | 'openai_compat' | 'anthropic_compat'

/** Kinds whose upstream exposes balance / rate endpoints (刷余额、同步倍率). */
export const BILLING_KINDS: readonly UpstreamKind[] = ['sub2api', 'new_api']
export type Protocol = 'openai' | 'anthropic'
export type EnableStatus = 'enabled' | 'disabled'
export type HealthStatus =
  | 'healthy'
  | 'degraded'
  | 'down'
  | 'cooldown'
  | 'low_balance'
  | 'disabled'

export type Upstream = {
  summary?: {
    key_count: number
    abnormal_count: number
    health_counts: Partial<Record<HealthStatus, number>>
    last_request_at?: string | null
  }
  id: number
  name: string
  base_url: string
  kind: UpstreamKind
  protocols: Protocol[]
  status: EnableStatus
  note?: string
  /** Shared in-flight cap for every key of this provider. 0 = unlimited. */
  concurrency: number
  health_status: HealthStatus
  cooldown_until?: string | null
  last_error?: string | null
  last_balance?: number | null
  last_balance_at?: string | null
}

export type PulseState = 'ok' | 'degraded' | 'mix' | 'bad' | 'empty'

export type HealthPulseCell = {
  start: string
  state: PulseState
  ok: number
  fail: number
  last_latency_ms?: number
  latency_p50_ms?: number
  score?: number
}

export type ChannelScoreMeta = {
  samples: number
  success: number
  latency_p50?: number
  cache?: number | null
  low_sample?: boolean
  terms: string[]
}

export type PlatformKey = {
  id: number
  upstream_id: number
  upstream_name?: string
  upstream_kind?: UpstreamKind
  name: string
  /** Operator-chosen middle segment of `{provider}-{tag}-{rate}`. */
  name_tag?: string
  key_preview: string
  status: EnableStatus
  rpm_limit: number
  max_concurrency: number
  last_balance?: number | null
  last_balance_at?: string | null
  last_request_at?: string | null
  last_error?: string | null
  last_probe_at?: string | null
  /** Scheduled probe cadence in seconds. 0 follows the global job interval. */
  probe_interval_sec?: number
  health_status: HealthStatus
  health_pulse?: HealthPulseCell[]
  cache_rate?: number | null
  cache_samples?: number
  /** Window quality 0–100; omitted when there are no samples. */
  channel_score?: number | null
  channel_score_meta?: ChannelScoreMeta
  /** Upstream price multiplier of this key (default 1). */
  rate_multiplier: number
  /** Set when the rate was last synced from sub2api / new-api; null after a manual edit. */
  rate_synced_at?: string | null
  billing_unsupported?: boolean
  /** new-api only: the token's group name used to look up group_ratio. */
  billing_group?: string
  new_api_user_id?: number
  route_groups?: RouteGroupRef[]
  last_models?: string[]
  last_models_at?: string | null
  models_count?: number
}

export type ModelsOutcome = {
  success: boolean
  status_code: number
  latency_ms: number
  models: string[]
  count: number
  fetched_at: string
  error?: string
  message?: string
}

export type UpstreamModelsOutcome = {
  ok: number
  failed: number
  models: string[]
  models_count: number
  message: string
}

export type RouteGroupRef = {
  id: number
  name: string
  protocol?: Protocol | ''
}

export type RouteGroup = {
  id: number
  name: string
  protocol: Protocol | ''
  models: string[]
  rate_min?: number | null
  rate_max?: number | null
  description?: string
  status: EnableStatus
  member_count: number
  consumer_count: number
  drift_count: number
  key_ids: number[]
  consumers: RouteGroupRef[]
  created_at?: string
  updated_at?: string
}

export type RouteGroupPayload = {
  name?: string
  protocol?: Protocol | ''
  models?: string[]
  rate_min?: number | null
  rate_max?: number | null
  clear_rate?: boolean
  description?: string
  status?: EnableStatus
  key_ids?: number[]
}

export type RouteCandidate = {
  id: number
  name: string
  key_preview: string
  status: EnableStatus
  health_status: HealthStatus
  last_balance?: number | null
  upstream_id: number
  upstream_name: string
  upstream_kind: UpstreamKind
  protocols: Protocol[]
  rate_multiplier: number
  billing_group?: string
  route_group_ids: number[]
}

export type ConsumerKey = {
  id: number
  name: string
  key_preview: string
  key?: string
  status: EnableStatus
  quota_usd: number
  quota_used: number
  rpm: number
  last_used_at?: string | null
  route_groups?: RouteGroupRef[]
}

export type ConsumerKeyTestResult = {
  success: boolean
  status: number
  model: string
  duration_ms: number
  error?: string
}

export type RequestLog = {
  id: number
  request_id: string
  consumer_key_id?: number | null
  upstream_id?: number | null
  platform_key_id?: number | null
  protocol: Protocol
  model: string
  path: string
  client_ip?: string
  status_code: number
  success: boolean
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  ttft_ms: number
  duration_ms: number
  in_flight?: boolean
  stream?: boolean
  stream_known?: boolean
  cost_usd?: number
  error_message?: string
  failure_scope?: string
  failure_action?: string
  created_at: string
  upstream_name?: string
  consumer_name?: string
}

export type RequestLogDetail = RequestLog & {
  request_headers?: string
  request_body?: string
  request_body_truncated?: boolean
  response_headers?: string
  response_body?: string
  response_body_truncated?: boolean
}

export type RateChangeDirection = 'up' | 'down'
export type RateChangeSource = 'billing' | 'manual'

export type RateChangeNotice = {
  id: number
  platform_key_id: number
  upstream_id: number
  key_name: string
  upstream_name: string
  old_rate: number
  new_rate: number
  direction: RateChangeDirection
  source: RateChangeSource
  read_at?: string | null
  created_at: string
}

export type RateNoticeUnreadCount = {
  unread: number
}

export type ListResult<T> = {
  items: T[]
  total: number
  page: number
  page_size: number
}

export type ListParams = {
  page?: number
  page_size?: number
}

export type StatusMatrixCell = {
  upstream_id: number
  upstream_name: string
  key_id: number
  key_name?: string
  key_preview?: string
  health_status: HealthStatus
  last_error?: string | null
  last_probe_at?: string | null
}

export type UpstreamPayload = {
  name: string
  base_url: string
  kind: UpstreamKind
  protocols: Protocol[]
  status: EnableStatus
  note?: string
  concurrency?: number
}

export type PlatformKeyPayload = {
  name?: string
  name_tag: string
  api_key?: string
  access_token?: string
  new_api_user_id?: number
  status: EnableStatus
  rate_multiplier?: number
  billing_group?: string
  probe_interval_sec?: number
  rpm_limit?: number
  max_concurrency?: number
}

export type ConsumerKeyPayload = {
  name: string
  status: EnableStatus
  quota_usd: number
  rpm: number
  route_group_ids?: number[]
}

export type SchedulerSettings = {
  ranking_mode: 'adaptive' | 'fixed_order' | 'cache_affinity' | 'load_balance'
  weight_success: number
  weight_cache: number
  weight_ttft: number
  epsilon: number
  window_minutes: number
  window_max_samples: number
  min_samples: number
  prior_success: number
  ttft_cap_ms: number
  sticky_anthropic: boolean
  sticky_openai: boolean
  sticky_ttl_sec: number
  failover_max: number
  retry_max: number
  cooldown_sec: number
  failure_window_sec: number
  failure_threshold: number
  probe_openai_model: string
  probe_anthropic_model: string
  probe_grok_model: string
  probe_zhipu_model: string
  probe_moonshot_model: string
  probe_deepseek_model: string
  filter_by_models: boolean
}

export type ProbeVendorId = 'openai' | 'anthropic' | 'grok' | 'zhipu' | 'moonshot' | 'deepseek'

export type CatalogModel = {
  id: string
  name: string
  protocol: Protocol
  input_cost?: number
  output_cost?: number
  release_date?: string
}

export type CatalogVendor = {
  id: ProbeVendorId
  name: string
  protocol: Protocol
  models: CatalogModel[]
}

export type ModelCatalog = {
  source: string
  synced_at?: string | null
  model_count: number
  vendors: CatalogVendor[]
  updated?: string[]
  message?: string
}

export type SchedulerCandidate = {
  key_id: number
  key_name: string
  key_preview: string
  upstream_id: number
  upstream_name: string
  eligible: boolean
  skip_reason?: string
  quality: number
  in_band: boolean
  selected: boolean
  rate: number
  effective_cost: number
  success_rate: number
  cache_rate: number
  ttft_p50: number
  samples: number
  health_status: HealthStatus
  last_balance?: number | null
  ranking_mode: SchedulerSettings['ranking_mode']
  affinity_hit: boolean
  current_rpm: number
  key_inflight: number
  provider_inflight: number
  rpm_limit: number
  max_concurrency: number
  provider_concurrency: number
}

export type SchedulerExplain = {
  settings: SchedulerSettings
  protocol: Protocol
  model: string
  consumer_key_id?: number
  route_bound?: boolean
  candidates: SchedulerCandidate[]
}

export type RequestLogQuery = ListParams & {
  snapshot_id?: number
  snapshot_at?: string
  upstream_id?: number
  key_id?: number
  success?: boolean
  model?: string
  from?: string
  to?: string
}

export const KIND_LABEL: Record<UpstreamKind, string> = {
  sub2api: 'sub2api',
  new_api: 'new-api',
  openai_compat: 'OpenAI 兼容',
  anthropic_compat: 'Anthropic 兼容',
}

export const PROTOCOL_LABEL: Record<Protocol, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
}

export const STATUS_LABEL: Record<EnableStatus, string> = {
  enabled: '启用',
  disabled: '停用',
}

export const HEALTH_LABEL: Record<HealthStatus, string> = {
  healthy: '健康',
  degraded: '降级',
  down: '故障',
  cooldown: '冷却',
  low_balance: '余额不足',
  disabled: '已停用',
}

export const KIND_OPTIONS = (Object.keys(KIND_LABEL) as UpstreamKind[]).map((value) => ({
  label: KIND_LABEL[value],
  value,
}))

export const PROTOCOL_OPTIONS = (Object.keys(PROTOCOL_LABEL) as Protocol[]).map((value) => ({
  label: PROTOCOL_LABEL[value],
  value,
}))

export const STATUS_OPTIONS = (Object.keys(STATUS_LABEL) as EnableStatus[]).map((value) => ({
  label: STATUS_LABEL[value],
  value,
}))

export const RATE_CHANGE_SOURCE_LABEL: Record<RateChangeSource, string> = {
  billing: '同步',
  manual: '手动',
}
