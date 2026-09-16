import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'

function loadTS(path, globals = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  })
  const exports = {}
  vm.runInNewContext(outputText, { exports, ...globals })
  return exports
}

const { dashboardRange } = loadTS('../src/utils/dashboardRange.ts')

test('history refresh advances time and rolls over Shanghai midnight', () => {
  const before = dashboardRange('today', null, '', new Date('2026-09-16T15:59:00Z'))
  const after = dashboardRange('today', null, '', new Date('2026-09-16T16:01:00Z'))
  assert.equal(before.from, '2026-09-16T00:00:00+08:00')
  assert.equal(after.from, '2026-09-17T00:00:00+08:00')
  assert.notEqual(before.to, after.to)
  const first = dashboardRange('7d', null, '', new Date('2026-09-16T10:00:00Z'))
  const next = dashboardRange('7d', null, '', new Date('2026-09-16T10:00:30Z'))
  assert.equal(Date.parse(next.to) - Date.parse(first.to), 30000)
  assert.equal(Date.parse(next.from) - Date.parse(first.from), 30000)
})

test('30-day and older custom ranges use retained Shanghai daily buckets', () => {
  const range = dashboardRange('30d', null, '', new Date('2026-09-16T10:00:00Z'))
  assert.equal(range.gran, 'day')
  assert.equal(range.from, '2026-08-18T00:00:00+08:00')
  assert.equal(range.to, '2026-09-17T00:00:00+08:00')
  assert.equal(Date.parse(range.to) - Date.parse(range.from), 30 * 86400000)
  const old = dashboardRange('custom', [Date.parse('2026-01-01T03:00Z'), Date.parse('2026-01-01T04:00Z')], '', new Date('2026-09-16T10:00Z'))
  assert.equal(old.gran, 'day')
  assert.equal(old.from, '2026-01-01T00:00:00+08:00')
  assert.equal(old.to, '2026-01-02T00:00:00+08:00')
})

async function flush() {
  for (let i = 0; i < 12; i++) await Promise.resolve()
}

function liveFixture(status = 200) {
  const timers = new Map()
  const streams = []
  const states = []
  const snapshots = []
  const calls = []
  let id = 0
  let unauthorized = 0
  const { subscribeDashboardLive } = loadTS('../src/api/sse.ts', {
    require: (name) => { assert.equal(name, './http'); return { TOKEN_KEY: 'admin-token' } },
    AbortController, TextDecoder, DOMException,
    window: {
      setTimeout: (callback, delay) => { timers.set(++id, { callback, delay }); return id },
      clearTimeout: (key) => timers.delete(key),
    },
    localStorage: { getItem: () => 'test-token' },
    fetch: async (url, options) => {
      calls.push({ url, options })
      const body = new ReadableStream({
        start(controller) {
          streams.push(controller)
          options.signal.addEventListener('abort', () => controller.error(new DOMException('aborted', 'AbortError')), { once: true })
        },
      })
      return new Response(body, { status })
    },
  })
  const sub = subscribeDashboardLive({
    onSnapshot: (s) => snapshots.push(s),
    onStatus: (s) => states.push(s),
    onUnauthorized: () => unauthorized++,
  })
  return {
    sub, timers, streams, states, snapshots, calls,
    unauthorized: () => unauthorized,
    async fire(delay) {
      const entry = [...timers].find(([, t]) => t.delay === delay)
      assert.ok(entry, `missing ${delay} ms timer`)
      timers.delete(entry[0])
      entry[1].callback()
      await flush()
    },
  }
}

test('SSE heartbeat timeout reconnects instead of ignoring AbortError', async (t) => {
  const f = liveFixture()
  t.after(() => f.sub.stop())
  await flush()
  assert.equal(f.calls.length, 1)
  await f.fire(35000)
  assert.ok(f.states.includes('stale'))
  assert.equal(f.states.at(-1), 'reconnecting')
  await f.fire(1000)
  assert.equal(f.calls.length, 2)
  assert.equal(f.calls[1].options.headers.Authorization, 'Bearer test-token')
  assert.equal(f.calls[1].url.includes('test-token'), false)
})

test('pause and immediate resume do not let an old abort spawn another connection', async (t) => {
  const f = liveFixture()
  t.after(() => f.sub.stop())
  await flush()
  f.sub.pause()
  f.sub.resume()
  await flush()
  assert.equal(f.calls.length, 2)
  assert.deepEqual([...f.timers.values()].map((v) => v.delay), [35000])
  f.sub.stop()
  await flush()
  assert.equal(f.timers.size, 0)
})

test('SSE preserves CRLF and UTF-8 across byte boundaries', async (t) => {
  const f = liveFixture()
  t.after(() => f.sub.stop())
  await flush()
  const bytes = new TextEncoder().encode('event: snapshot\r\ndata: {"instance_id":"测试","business_rpm":9}\r\n\r\n')
  for (const byte of bytes) {
    f.streams[0].enqueue(new Uint8Array([byte]))
    await flush()
  }
  assert.equal(f.snapshots.length, 1)
  assert.equal(f.snapshots[0].instance_id, '测试')
  assert.equal(f.snapshots[0].business_rpm, 9)
  f.streams[0].close()
  await flush()
  await f.fire(1000)
  assert.equal(f.calls.length, 2)
})

test('SSE 401 stops retrying and clears timeout', async () => {
  const f = liveFixture(401)
  await flush()
  assert.equal(f.unauthorized(), 1)
  assert.equal(f.states.at(-1), 'unauthorized')
  assert.equal(f.timers.size, 0)
  f.sub.resume()
  assert.equal(f.calls.length, 1)
})
