import type { ListResult } from './types'

export const TOKEN_KEY = 'admin_token'
const BASE = '/api/v1/admin'

export type ApiErrorBody = {
  code: string
  message: string
}

export class ApiError extends Error {
  code: string
  status: number

  constructor(message: string, code: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.status = status
  }
}

let onUnauthorized: (() => void) | null = null

export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

export function toQuery(params?: Record<string, unknown>): string {
  if (!params) return ''
  const sp = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    sp.set(key, String(value))
  }
  const q = sp.toString()
  return q ? `?${q}` : ''
}

export function normalizeList<T>(data: unknown): ListResult<T> {
  if (Array.isArray(data)) {
    return { items: data as T[], total: data.length, page: 1, page_size: data.length || 20 }
  }
  if (data && typeof data === 'object') {
    const rec = data as Record<string, unknown>
    if (Array.isArray(rec.items)) {
      return {
        items: rec.items as T[],
        total: Number(rec.total ?? rec.items.length),
        page: Number(rec.page ?? 1),
        page_size: Number(rec.page_size ?? rec.items.length ?? 20),
      }
    }
  }
  return { items: [], total: 0, page: 1, page_size: 20 }
}

export async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  query?: Record<string, unknown>,
): Promise<T> {
  const token = localStorage.getItem(TOKEN_KEY)
  const headers: Record<string, string> = {}
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(`${BASE}${path}${toQuery(query)}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })

  if (res.status === 401) {
    onUnauthorized?.()
    throw new ApiError('未授权，请重新登录', 'unauthorized', 401)
  }

  const text = await res.text()
  let json: unknown = null
  if (text) {
    try {
      json = JSON.parse(text)
    } catch {
      throw new ApiError(text || `HTTP ${res.status}`, 'invalid_json', res.status)
    }
  }

  const wrapped = json as {
    ok?: boolean
    data?: T
    error?: { code?: string; message?: string }
  } | null

  if (wrapped && wrapped.ok === false) {
    throw new ApiError(
      wrapped.error?.message || '请求失败',
      wrapped.error?.code || 'error',
      res.status,
    )
  }

  if (!res.ok) {
    throw new ApiError(
      wrapped?.error?.message || text || `HTTP ${res.status}`,
      wrapped?.error?.code || 'http_error',
      res.status,
    )
  }

  if (wrapped && wrapped.ok === true) {
    return wrapped.data as T
  }

  return json as T
}

export function get<T>(path: string, query?: Record<string, unknown>) {
  return request<T>('GET', path, undefined, query)
}

export async function download(path: string, filename: string) {
  const token = localStorage.getItem(TOKEN_KEY)
  const res = await fetch(`${BASE}${path}`, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
  if (res.status === 401) onUnauthorized?.()
  if (!res.ok) throw new ApiError('正文下载失败', 'download_failed', res.status)
  const url = URL.createObjectURL(await res.blob())
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export function post<T>(path: string, body?: unknown, query?: Record<string, unknown>) {
  return request<T>('POST', path, body ?? {}, query)
}

export function put<T>(path: string, body?: unknown) {
  return request<T>('PUT', path, body ?? {})
}

export function del<T>(path: string) {
  return request<T>('DELETE', path)
}

export async function getList<T>(path: string, query?: Record<string, unknown>): Promise<ListResult<T>> {
  const data = await get<unknown>(path, query)
  return normalizeList<T>(data)
}
