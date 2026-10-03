import { del, get, getList, post, put } from './http'
import type { ListResult } from './types'

export type IntelQuestionKind = 'candy' | 'pelican'
export type IntelVerdict = 'correct' | 'incorrect' | 'success' | 'invalid' | 'error'

/** 糖果题：黑袋子摸糖果组合题，正解 21。 */
export const CANDY_PROMPT = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4`

export const CANDY_CONTRACT = '只输出最终整数，不要解释。'

/** 鹈鹕题：让模型画"鹈鹕骑自行车"的 SVG 动画，产出独立 HTML 作品。 */
export const PELICAN_PROMPT = '创建一个 HTML，内容是 SVG 绘制一个鹈鹕骑自行车的 2D 动画，你不需要任何测试，不要有任何限制'

export const PELICAN_CONTRACT = '所有账号使用相同交付约定：直接返回独立 HTML，不使用 Markdown 代码块或外部依赖。只输出 HTML，不要解释。'

export type IntelPlanIntervalOption = { label: string; value: number }

export const INTEL_INTERVAL_OPTIONS: IntelPlanIntervalOption[] = [
  { label: '仅手动测试', value: 0 },
  { label: '每 5 分钟', value: 5 },
  { label: '每 10 分钟', value: 10 },
  { label: '每 15 分钟', value: 15 },
  { label: '每 30 分钟', value: 30 },
  { label: '每 1 小时', value: 60 },
  { label: '每 3 小时', value: 180 },
  { label: '每 6 小时', value: 360 },
  { label: '每 12 小时', value: 720 },
  { label: '每天', value: 1440 },
]

export type IntelTestPlan = {
  id: number
  name: string
  route_group_id: number
  model: string
  question_kind: IntelQuestionKind
  prompt: string
  protocol: '' | 'openai' | 'anthropic'
  interval_minutes: number
  parallel: number
  enabled: boolean
  quarantine_enabled: boolean
  /** 触发隔离所需最少有效样本数（0 = 默认 3） */
  quarantine_min_samples: number
  /** 正确率阈值百分比，低于该值移出分组（0 = 默认 50） */
  quarantine_threshold: number
  last_run_at?: string | null
  next_run_at?: string | null
  created_at: string
  updated_at: string
}

export type IntelPlanStats = {
  samples: number
  success: number
  accuracy: number
  avg_latency_ms: number
}

export type IntelTestRun = {
  id: number
  plan_id: number
  status: 'running' | 'finished'
  /** full = 全量（含隔离中的 Key）；quarantine = 仅到期的隔离复测 */
  scope: 'full' | 'quarantine'
  total: number
  done: number
  success: number
  started_at: string
  finished_at?: string | null
  created_at: string
}

export type IntelPlanItem = IntelTestPlan & {
  group_name: string
  last_run?: IntelTestRun | null
  running: boolean
  stats: IntelPlanStats
  quarantined_count: number
}

export type IntelTestResult = {
  id: number
  run_id: number
  plan_id: number
  route_group_id: number
  upstream_id: number
  platform_key_id: number
  key_name: string
  upstream_name: string
  model: string
  question_kind: IntelQuestionKind
  protocol: string
  verdict: IntelVerdict
  status_code: number
  latency_ms: number
  tokens: number
  answer_preview: string
  output_size: number
  has_output: boolean
  error_message: string
  created_at: string
}

export type IntelVerdictPoint = {
  id: number
  run_id: number
  verdict: IntelVerdict
  latency_ms: number
  answer_preview: string
  error_message: string
  created_at: string
}

export type IntelQuarantineInfo = {
  status: 'quarantined' | 'restored'
  pass_streak: number
  backoff_sec: number
  next_test_at?: string | null
  quarantine_count: number
  reason: string
  quarantined_at?: string | null
  restored_at?: string | null
  last_tested_at?: string | null
}

export type IntelKeyStat = {
  platform_key_id: number
  upstream_id: number
  key_name: string
  upstream_name: string
  samples: number
  success: number
  accuracy: number
  avg_latency_ms: number
  last_verdict: IntelVerdict | ''
  last_answer: string
  last_at?: string | null
  history: IntelVerdictPoint[]
  quarantine?: IntelQuarantineInfo | null
}

export type IntelPlanPayload = {
  name?: string
  route_group_id: number
  model: string
  question_kind: IntelQuestionKind
  prompt?: string
  protocol?: '' | 'openai' | 'anthropic'
  interval_minutes?: number
  parallel?: number
  enabled?: boolean
  quarantine_enabled?: boolean
  quarantine_min_samples?: number
  quarantine_threshold?: number
}

export const INTEL_KIND_LABEL: Record<IntelQuestionKind, string> = {
  candy: '糖果题',
  pelican: '鹈鹕题',
}

export function listIntelPlans() {
  return get<{ items: IntelPlanItem[] }>('/intel-tests/plans').then(data => data.items ?? [])
}

export function createIntelPlan(payload: IntelPlanPayload) {
  return post<IntelTestPlan>('/intel-tests/plans', payload)
}

export function updateIntelPlan(id: number, payload: IntelPlanPayload) {
  return put<IntelTestPlan>(`/intel-tests/plans/${id}`, payload)
}

export function deleteIntelPlan(id: number) {
  return del(`/intel-tests/plans/${id}`)
}

export function runIntelPlan(id: number) {
  return post<IntelTestRun>(`/intel-tests/plans/${id}/run`)
}

export function listIntelRuns(planId: number, params?: { page?: number; page_size?: number }) {
  return getList<IntelTestRun>('/intel-tests/runs', { plan_id: planId, ...params })
}

export function listIntelResults(params: { plan_id?: number; run_id?: number; verdict?: string; has_output?: boolean; page?: number; page_size?: number }) {
  return getList<IntelTestResult>('/intel-tests/results', params)
}

export function getIntelResultOutput(id: number) {
  return get<{ result_id: number; output_text: string }>(`/intel-tests/results/${id}/output`)
}

export function getIntelPlanSummary(id: number) {
  return get<{ items: IntelKeyStat[]; success_verdict: string; question_kind: IntelQuestionKind }>(`/intel-tests/plans/${id}/summary`)
}

export type { ListResult }
