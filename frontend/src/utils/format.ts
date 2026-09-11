export function formatTime(value?: string | null) {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

export function formatMoney(value?: number | null, digits = 4) {
  if (value === null || value === undefined || Number.isNaN(value)) return '—'
  return value.toLocaleString('en-US', {
    minimumFractionDigits: 2,
    maximumFractionDigits: digits,
  })
}

export function formatNumber(value?: number | null) {
  if (value === null || value === undefined || Number.isNaN(value)) return '—'
  return value.toLocaleString('en-US')
}

/** Compact token counts for dense table cells: 999 stays exact, 4387 → 4.4k. */
export function formatTokenCount(value?: number | null) {
  if (value === null || value === undefined || Number.isNaN(value)) return '—'
  const n = Math.abs(value)
  if (n < 1000) return String(Math.round(value))
  const k = value / 1000
  const text = Number.isInteger(k) ? String(k) : k.toFixed(1).replace(/\.0$/, '')
  return `${text}k`
}

export function formatDurationMs(ms?: number | null) {
  if (ms == null || ms <= 0 || Number.isNaN(ms)) return '—'
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms)}ms`
}

/** Rate multipliers: up to 4 decimals, trailing zeros trimmed (1 -> "1", 0.09 -> "0.09"). */
export function formatRate(value?: number | null) {
  if (value === null || value === undefined || Number.isNaN(value)) return '—'
  return String(Number(value.toFixed(4)))
}

/** `{provider}-{tag}-{rate}`. Empty tag previews as `xxx`. */
export function composeKeyName(provider: string, tag: string, rate?: number | null) {
  const p = (provider || '').trim()
  const t = (tag || '').trim() || 'xxx'
  const r = formatRate(rate ?? 1)
  return `${p}-${t}-${r === '—' ? '1' : r}`
}

/** Recover the middle segment from `{provider}-{tag}-{rate}` when name_tag is missing. */
export function inferNameTag(fullName: string, provider?: string) {
  let name = (fullName || '').trim()
  const p = (provider || '').trim()
  if (p && name.startsWith(`${p}-`)) name = name.slice(p.length + 1)
  const last = name.lastIndexOf('-')
  if (last > 0 && Number.isFinite(Number(name.slice(last + 1)))) {
    name = name.slice(0, last)
  }
  return name
}

export function formatPercent(value?: number | null, digits = 1) {
  if (value === null || value === undefined || Number.isNaN(value)) return '—'
  return `${(value * 100).toFixed(digits)}%`
}

/** Milliseconds to seconds, always suffixed with s. */
export function formatSeconds(ms?: number | null) {
  if (ms == null || ms < 0 || Number.isNaN(ms)) return '—'
  const s = ms / 1000
  if (s < 10) return `${s.toFixed(2)}s`
  return `${s.toFixed(1)}s`
}

/** Output tokens per second over generation time (duration − TTFT). */
export function formatTps(outputTokens?: number | null, durationMs?: number | null, ttftMs?: number | null) {
  const out = Number(outputTokens) || 0
  const dur = Number(durationMs) || 0
  if (out <= 0 || dur <= 0) return '—'
  const ttft = Number(ttftMs) || 0
  const gen = ttft > 0 ? dur - ttft : dur
  if (gen <= 0) return '—'
  const v = out / (gen / 1000)
  if (!Number.isFinite(v) || v <= 0) return '—'
  const text = v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)
  return `${text} tps`
}

export function errText(e: unknown, fallback = '请求失败') {
  if (e instanceof Error && e.message) return e.message
  return fallback
}

export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}
