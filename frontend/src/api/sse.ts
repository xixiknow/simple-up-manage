import { TOKEN_KEY } from './http'
import type { DashHeartbeat, DashLiveSnapshot } from './types'

export type LiveStatus = 'connecting' | 'live' | 'reconnecting' | 'paused' | 'unauthorized' | 'stale'

const BACKOFF = [1000, 2000, 4000, 8000, 15000]
const STALE_MS = 35000

export function subscribeDashboardLive(handlers: {
  onSnapshot: (snap: DashLiveSnapshot) => void
  onHeartbeat?: (hb: DashHeartbeat) => void
  onStatus?: (status: LiveStatus) => void
  onUnauthorized?: () => void
}) {
  let aborted = false
  let paused = false
  let controller: AbortController | null = null
  let staleTimer: number | undefined
  let reconnectTimer: number | undefined
  let attempt = 0
  let lastEventAt = 0

  const setStatus = (s: LiveStatus) => handlers.onStatus?.(s)

  const clearTimers = () => {
    if (staleTimer != null) window.clearTimeout(staleTimer)
    if (reconnectTimer != null) window.clearTimeout(reconnectTimer)
    staleTimer = undefined
    reconnectTimer = undefined
  }

  const bumpFresh = (active: AbortController) => {
    if (controller !== active || aborted || paused) return
    lastEventAt = Date.now()
    if (staleTimer != null) window.clearTimeout(staleTimer)
    staleTimer = window.setTimeout(() => {
      if (aborted || paused) return
      setStatus('stale')
      active.abort()
    }, STALE_MS)
  }

  const parseEvents = async (res: Response, active: AbortController) => {
    const reader = res.body?.getReader()
    if (!reader) throw new Error('no body')
    const decoder = new TextDecoder()
    let buf = ''
    try {
      while (!active.signal.aborted) {
        const { value, done } = await reader.read()
        if (done || controller !== active || active.signal.aborted) break
        buf += decoder.decode(value, { stream: true })
        let separator: RegExpExecArray | null
        while ((separator = /\r?\n\r?\n/.exec(buf)) !== null) {
          const block = buf.slice(0, separator.index)
          buf = buf.slice(separator.index + separator[0].length)
          let event = 'message'
          const data: string[] = []
          for (const line of block.split(/\r?\n/)) {
            if (line.startsWith('event:')) event = line.slice(6).trim()
            else if (line.startsWith('data:')) data.push(line.slice(5).trimStart())
          }
          if (!data.length) continue
          bumpFresh(active)
          const raw = data.join('\n')
          try {
            const parsed = JSON.parse(raw)
            if (event === 'snapshot') {
              attempt = 0
              setStatus('live')
              handlers.onSnapshot(parsed as DashLiveSnapshot)
            } else if (event === 'heartbeat') {
              attempt = 0
              if (paused) continue
              setStatus('live')
              handlers.onHeartbeat?.(parsed as DashHeartbeat)
            }
          } catch {
            /* ignore malformed frames */
          }
        }
      }
    } finally {
      reader.releaseLock()
    }
  }

  const connect = async () => {
    if (aborted || paused) return
    controller?.abort()
    const active = new AbortController()
    controller = active
    setStatus(attempt === 0 ? 'connecting' : 'reconnecting')
    bumpFresh(active)
    const token = localStorage.getItem(TOKEN_KEY)
    try {
      const res = await fetch('/api/v1/admin/dashboard/live', {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        signal: active.signal,
      })
      if (controller !== active || aborted || paused) return
      if (res.status === 401) {
        setStatus('unauthorized')
        handlers.onUnauthorized?.()
        aborted = true
        clearTimers()
        active.abort()
        return
      }
      if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`)
      bumpFresh(active)
      await parseEvents(res, active)
      if (!aborted && !paused) throw new Error('stream closed')
    } catch {
      if (aborted || paused || controller !== active) return
      clearTimers()
      scheduleReconnect()
    }
  }

  const scheduleReconnect = () => {
    if (aborted || paused) return
    setStatus('reconnecting')
    const wait = BACKOFF[Math.min(attempt, BACKOFF.length - 1)]
    attempt += 1
    reconnectTimer = window.setTimeout(() => {
      void connect()
    }, wait)
  }

  const stopStream = () => {
    clearTimers()
    controller?.abort()
    controller = null
  }

  void connect()

  return {
    pause() {
      if (paused || aborted) return
      paused = true
      stopStream()
      setStatus('paused')
    },
    resume() {
      if (aborted || !paused) return
      paused = false
      attempt = 0
      void connect()
    },
    stop() {
      aborted = true
      paused = false
      stopStream()
    },
    lastEventAt: () => lastEventAt,
  }
}
