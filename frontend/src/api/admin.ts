import { del, get, getList, post, put } from './http'
import type {
  ConsumerKey,
  ConsumerKeyPayload,
  ConsumerKeyTestResult,
  ListParams,
  ListResult,
  ModelsOutcome,
  PlatformKey,
  Protocol,
  PlatformKeyPayload,
  RequestLog,
  RequestLogDetail,
  RequestLogQuery,
  RouteCandidate,
  RouteGroup,
  RouteGroupPayload,
  SchedulerCandidate,
  SchedulerExplain,
  SchedulerSettings,
  StatusMatrixCell,
  Upstream,
  UpstreamModelsOutcome,
  UpstreamPayload,
  ModelCatalog,
} from './types'

function stripEmptyKey(payload: PlatformKeyPayload): PlatformKeyPayload {
  const next = { ...payload }
  if (!next.api_key) delete next.api_key
  return next
}

export function listUpstreams(params?: ListParams) {
  return getList<Upstream>('/upstreams', params)
}

export function getUpstream(id: number) {
  return get<Upstream>(`/upstreams/${id}`)
}

export function createUpstream(payload: UpstreamPayload) {
  return post<Upstream>('/upstreams', payload)
}

export function updateUpstream(id: number, payload: UpstreamPayload) {
  return put<Upstream>(`/upstreams/${id}`, payload)
}

export function deleteUpstream(id: number) {
  return del(`/upstreams/${id}`)
}

export function listKeys(params?: ListParams & { upstream_id?: number; route_group_id?: number }) {
  if (params?.upstream_id && params.route_group_id === undefined) {
    const { upstream_id, ...rest } = params
    return getList<PlatformKey>(`/upstreams/${upstream_id}/keys`, rest)
  }
  return getList<PlatformKey>('/keys', params)
}

export function createKey(upstreamId: number, payload: PlatformKeyPayload) {
  return post<PlatformKey>(`/upstreams/${upstreamId}/keys`, stripEmptyKey(payload))
}

export function updateKey(id: number, payload: PlatformKeyPayload) {
  return put<PlatformKey>(`/keys/${id}`, stripEmptyKey(payload))
}

export function deleteKey(id: number) {
  return del(`/keys/${id}`)
}

export function probeKey(id: number, deep = true) {
  return post<unknown>(`/keys/${id}/probe`, { deep })
}

export function refreshKeyBalance(id: number) {
  return post<unknown>(`/keys/${id}/refresh-balance`)
}

export function refreshUpstreamBalance(id: number) {
  return post<Upstream>(`/upstreams/${id}/refresh-balance`)
}

export function refreshKeyBilling(id: number) {
  return post<unknown>(`/keys/${id}/refresh-billing`)
}

export function fetchKeyModels(id: number) {
  return post<ModelsOutcome>(`/keys/${id}/fetch-models`)
}

export function fetchUpstreamModels(upstreamId: number) {
  return post<UpstreamModelsOutcome>(`/upstreams/${upstreamId}/fetch-models`)
}

export function listConsumerKeys(params?: ListParams) {
  return getList<ConsumerKey>('/consumer-keys', params)
}

export function createConsumerKey(payload: ConsumerKeyPayload) {
  return post<ConsumerKey>('/consumer-keys', payload)
}

export function updateConsumerKey(id: number, payload: ConsumerKeyPayload) {
  return put<ConsumerKey>(`/consumer-keys/${id}`, payload)
}

export function deleteConsumerKey(id: number) {
  return del(`/consumer-keys/${id}`)
}

export function getConsumerKeySecret(id: number) {
  return get<{ key: string }>(`/consumer-keys/${id}/secret`)
}

export function testConsumerKey(id: number) {
  return post<ConsumerKeyTestResult>(`/consumer-keys/${id}/test`)
}

export async function listRouteGroups() {
  const res = await get<{ items: RouteGroup[] }>('/route-groups')
  return res.items ?? []
}

export function createRouteGroup(payload: RouteGroupPayload) {
  return post<RouteGroup>('/route-groups', payload)
}

export function updateRouteGroup(id: number, payload: RouteGroupPayload) {
  return put<RouteGroup>(`/route-groups/${id}`, payload)
}

export function deleteRouteGroup(id: number) {
  return del(`/route-groups/${id}`)
}

export function setRouteGroupKeys(id: number, keyIds: number[]) {
  return put<RouteGroup>(`/route-groups/${id}/keys`, { key_ids: keyIds })
}

export function batchRouteGroupKeys(id: number, body: { add?: number[]; remove?: number[] }) {
  return post<RouteGroup>(`/route-groups/${id}/keys/batch`, body)
}

export function setKeyRouteGroups(keyId: number, routeGroupIds: number[]) {
  return put<PlatformKey>(`/keys/${keyId}/route-groups`, { route_group_ids: routeGroupIds })
}

export async function listRouteCandidates() {
  const res = await get<{ items: RouteCandidate[] }>('/route-groups/candidates')
  return res.items ?? []
}

export function listBalances(params?: ListParams) {
  return getList<PlatformKey>('/balances', params)
}

export function refreshAllBalances() {
  return post<unknown>('/balances/refresh')
}

export function getStatus() {
  return get<unknown>('/status')
}

export function runProbes(body?: { deep?: boolean; upstream_id?: number; key_id?: number }) {
  return post<unknown>('/probes/run', body ?? {})
}

export function listRequestLogs(params?: RequestLogQuery) {
  return getList<RequestLog>('/request-logs', params as Record<string, unknown>)
}

export function getRequestLog(id: number) {
  return get<RequestLogDetail>(`/request-logs/${id}`)
}

export function getScheduler() {
  return get<SchedulerSettings>('/scheduler')
}

export function updateScheduler(payload: SchedulerSettings) {
  return put<SchedulerSettings>('/scheduler', payload)
}

export function explainScheduler(params: {
  protocol: Protocol
  model?: string
  session?: string
  consumer_key_id?: number
}) {
  return get<SchedulerExplain>('/scheduler/explain', params)
}

export function getModelCatalog() {
  return get<ModelCatalog>('/model-catalog')
}

export function syncModelCatalog() {
  return post<ModelCatalog>('/model-catalog/sync')
}

export function flattenExplain(data: unknown): SchedulerCandidate[] {
  if (!data || typeof data !== 'object') return []
  const rec = data as Record<string, unknown>
  const list = rec.candidates ?? rec.items
  if (!Array.isArray(list)) return []
  return list as SchedulerCandidate[]
}

export function flattenStatus(data: unknown): StatusMatrixCell[] {
  const cells: StatusMatrixCell[] = []

  const pushCell = (row: Record<string, unknown>, fallback?: Record<string, unknown>) => {
    const upstreamId = Number(row.upstream_id ?? fallback?.upstream_id ?? fallback?.id ?? 0)
    const keyId = Number(row.key_id ?? row.id ?? 0)
    if (!upstreamId && !keyId) return
    cells.push({
      upstream_id: upstreamId,
      upstream_name: String(row.upstream_name ?? fallback?.upstream_name ?? fallback?.name ?? '—'),
      key_id: keyId,
      key_name: (row.key_name ?? row.name) as string | undefined,
      key_preview: (row.key_preview as string | undefined) ?? undefined,
      health_status: (row.health_status as StatusMatrixCell['health_status']) || 'down',
      last_error: (row.last_error as string | null | undefined) ?? null,
      last_probe_at: (row.last_probe_at as string | null | undefined) ?? null,
    })
  }

  const walk = (value: unknown, parent?: Record<string, unknown>) => {
    if (!value) return
    if (Array.isArray(value)) {
      for (const item of value) walk(item, parent)
      return
    }
    if (typeof value !== 'object') return
    const rec = value as Record<string, unknown>
    if (Array.isArray(rec.items) && rec.items.length) {
      walk(rec.items, rec)
      return
    }
    if (Array.isArray(rec.matrix) && rec.matrix.length) {
      walk(rec.matrix, rec)
      return
    }
    if (Array.isArray(rec.upstreams) && rec.upstreams.length) {
      walk(rec.upstreams, rec)
      return
    }
    if (Array.isArray(rec.keys)) {
      for (const key of rec.keys) {
        if (key && typeof key === 'object') pushCell(key as Record<string, unknown>, rec)
      }
      return
    }
    if ('health_status' in rec || 'key_id' in rec || ('id' in rec && 'upstream_id' in rec)) {
      pushCell(rec, parent)
    }
  }

  walk(data)
  return cells
}

export function actionMessage(data: unknown, fallback: string) {
  if (data && typeof data === 'object') {
    const rec = data as Record<string, unknown>
    if (typeof rec.message === 'string' && rec.message) return rec.message
    if (typeof rec.last_balance === 'number') return `余额 ${rec.last_balance}`
  }
  return fallback
}

export type { ListResult }
