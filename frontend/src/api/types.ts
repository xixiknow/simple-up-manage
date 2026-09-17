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
  new_api_user_id?: number | null
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
  protocols?: Protocol[]
  effective_protocols?: Protocol[]
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
  /** Independent of Key status: false keeps the key in business routing but skips diagnostic probes. */
  probe_enabled?: boolean
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
  sale_multiplier?: number | null
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
  sale_multiplier?: number | null
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
  route_group_id?: number | null
  route_groups?: RouteGroupRef[]
}

export type ConsumerKeyTestResult = {
  success: boolean
  status: number
  model: string
  duration_ms: number
  error?: string
  skipped?: boolean
  reason?: string
  message?: string
}

export type ExternalProbeRule = 'rp_arithmetic' | 'example_arithmetic' | 'health_manager'

export const EXTERNAL_PROBE_LABEL: Record<ExternalProbeRule, string> = {
  rp_arithmetic: 'RP 算术探测',
  example_arithmetic: '示例加减法探测',
  health_manager: 'health-manager 探测',
}

export type RequestLog = {
  external_probe_rule?: ExternalProbeRule | null
  id: number
  request_id: string
  consumer_key_id?: number | null
  upstream_id?: number | null
  platform_key_id?: number | null
  route_group_id?: number | null
  route_group_name?: string
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
  selection_trace?: string
  created_at: string
  upstream_name?: string
  consumer_name?: string
}

export type RequestLogDetail = RequestLog & {
    attempts?: RequestAttempt[]
  ttft_status?: string
  ttft_event?: string
  bodies?: LogBody[]
  request_headers?: string
  request_body?: string
  request_body_truncated?: boolean
  response_headers?: string
  response_body?: string
  response_body_truncated?: boolean
}

export type LogBody = {
  id: string
  attempt_id?: string
  direction: 'request' | 'response'
  content_type: string
  status: 'saving' | 'complete' | 'partial' | 'omitted' | 'error'
  reason?: string
  received_bytes: number
  saved_bytes: number
  stored_bytes: number
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
  access_token?: string
  new_api_user_id?: number | null
}

export type PlatformKeyPayload = {
  protocols?: Protocol[]
  name?: string
  name_tag: string
  api_key?: string
  status: EnableStatus
  rate_multiplier?: number
  billing_group?: string
  probe_interval_sec?: number
  probe_enabled?: boolean
  rpm_limit?: number
  max_concurrency?: number
}

export type ConsumerKeyPayload = {
  name: string
  status: EnableStatus
  quota_usd: number
  rpm: number
  route_group_id?: number | null
  route_group_ids?: number[]
}

export type SchedulerSettings = {
	circuit_window_sec: number
	circuit_failure_threshold: number
	circuit_cooldown_sec: number
	circuit_max_cooldown_sec: number
  switch_improvement_ratio: number
  switch_improvement_ms: number
  switch_confirm_sec: number
  exploration_ratio: number
  probe_timeout_sec: number
  ranking_mode: 'adaptive' | 'fixed_order' | 'cache_affinity' | 'load_balance' | 'stable_latency'
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
	probe_status?: string
	probe_at?: number
	probe_model?: string
	probe_path?: string
	probe_stream?: boolean
	circuit_state?: string
	circuit_scope?: string
	circuit_reason?: string
	circuit_until?: number
	recovery?: boolean
	recovery_status?: string
	recovery_check_at?: string
	recovery_next_check_at?: string
	recovery_check_error?: string
	recovery_last_at?: string
  latency_samples?: number
  reliable?: boolean
  decision_reason?: string
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

export type RequestAttempt = {
  id: string
  platform_key_id: number
  result: string
  ttft_ms: number
  duration_ms: number
  status_code: number
  started_at: string
  ttft_status?: string
  ttft_event?: string
  failure_action?: string
  failure_phase?: string
  error_message?: string
  headers_ms?: number
  received_bytes?: number
  event_summary?: string
}

export type SchedulerDecision = {
  reason: string
  previous_key_id: number
  selected_key_id: number
  session_source: string
  exploration: boolean
  degraded: boolean
  candidates: SchedulerCandidate[]
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
  external_probe_rule?: ExternalProbeRule | 'any'
  consumer_key_id?: number
  snapshot_id?: number
  snapshot_at?: string
  upstream_id?: number
  key_id?: number
  route_group_id?: number
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

export type DashRange = { from: string; to: string }

export type DashGap = { from: string; to: string; reason: string }

export type DashDataQuality = {
  complete: boolean
  gaps?: DashGap[]
  unmeasured?: Record<string, number>
  overflow?: boolean
  queue_depth?: number
  window_warmup?: boolean
}

export type DashMeta = {
  generated_at: string
  available_from: string
  timezone: string
  range: DashRange
  data_quality: DashDataQuality
}

export type DashFinance = {
  known_revenue_usd?: number | null
  known_estimated_cost_usd?: number | null
  consumption_usd?: number | null
  reported_consumption_usd?: number | null
  estimated_consumption_usd?: number | null
  unknown_consumption?: number
  covered_revenue_usd?: number | null
  covered_cost_usd?: number | null
  estimated_profit_usd?: number | null
  margin?: number | null
  coverage?: number | null
  known_partial?: boolean
}

export type DashProviderBalance = {
  id: number
  name: string
  enabled: boolean
  balance_usd?: number | null
  balance_at?: string | null
  kind: string
  unlimited: boolean
  unknown: boolean
  stale: boolean
}

export type DashBalance = {
  total_known_usd?: number | null
  enabled_known_usd?: number | null
  unlimited_count: number
  unknown_count: number
  disabled_count: number
  stale_count: number
  providers: DashProviderBalance[]
  refreshed_at?: string | null
}

export type DashOverview = {
  meta: DashMeta
  requests_started: number
  requests_completed: number
  requests_success: number
  success_rate?: number | null
  retry_rate?: number | null
  provider_success_rate?: number | null
  ttft_p50_ms?: number | null
  ttft_p95_ms?: number | null
  ttft_samples: number
  ttft_approx: boolean
  ttft_overflow: boolean
  inflight_mean?: number | null
  inflight_peak: number
  finance: DashFinance
  balance: DashBalance
}

export type DashTrendPoint = {
  bucket: string
  complete: boolean
  requests_started: number
  requests_completed: number
  failure_rate?: number | null
  success_rate?: number | null
  retry_rate?: number | null
  provider_success_rate?: number | null
  ttft_p50_ms?: number | null
  ttft_p95_ms?: number | null
  ttft_samples: number
  inflight_mean?: number | null
  inflight_peak: number
  consumption_usd?: number | null
  covered_profit_usd?: number | null
  known_revenue_usd?: number | null
}

export type DashTrends = {
  meta: DashMeta
  granularity: string
  points: DashTrendPoint[]
}

export type DashRankingRow = {
  id: number
  name: string
  requests_completed: number
  success_rate?: number | null
  known_revenue_usd?: number | null
  covered_revenue_usd?: number | null
  covered_cost_usd?: number | null
  estimated_profit_usd?: number | null
  margin?: number | null
  coverage?: number | null
  consumption_usd?: number | null
  excluded: boolean
  exclude_reason?: string
}

export type DashRankings = {
  meta: DashMeta
  dimension: string
  items: DashRankingRow[]
  total: number
  page: number
  page_size: number
}

export type DashUrgentItem = {
  provider_id: number
  name: string
  balance_usd?: number | null
  hours_left?: number | null
  consumed_24h_usd?: number | null
  coverage?: number | null
  reason: string
  insufficient: boolean
  zero_consumption: boolean
  health_note?: string
}

export type DashInvestItem = {
  provider_id: number
  name: string
  profit_per_cost?: number | null
  margin?: number | null
  coverage?: number | null
  samples: number
  success_rate?: number | null
  ttft_p95_ms?: number | null
  cost_multiplier?: number | null
  balance_usd?: number | null
  reason: string
}

export type DashWatchItem = {
  provider_id: number
  name: string
  reason: string
  demand_key?: string
}

export type DashDemandBoard = {
  key: string
  label: string
  weight: number
  items: DashInvestItem[]
}

export type DashRecommendations = {
  meta: DashMeta
  urgent: DashUrgentItem[]
  invest: DashInvestItem[]
  watch: DashWatchItem[]
  demand_boards: DashDemandBoard[]
  common_coverage?: number | null
  note: string
}

export type DashSettings = {
  renewal_horizon_hours: number
  min_quality_samples: number
  min_success_rate: number
  min_ttft_samples: number
  max_ttft_p95_ms: number
  min_finance_coverage: number
  min_common_demand_coverage: number
  updated_at?: string
}

export type DashLiveSnapshot = {
  schema_version: number
  instance_id: string
  sequence: number
  server_time: string
  started_at: string
  window_seconds: number
  window_complete: boolean
  business_inflight: number
  business_rpm: number
  upstream_inflight: number
  upstream_rpm: number
}

export type DashHeartbeat = {
  instance_id: string
  server_time: string
}
