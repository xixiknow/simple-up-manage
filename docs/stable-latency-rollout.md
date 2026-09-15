# Stable latency routing

Implementation baseline: `021ec7c9ce0b79ba698168fc7b79132e724101b1`.

## Behavior

Select `stable_latency` in the scheduler settings to use successful, protocol-valid
upstream attempts. Existing installations keep their current ranking mode until
explicitly changed. Legacy weights and epsilon do not apply in this mode.

Statistics are isolated by key, protocol, model, endpoint and streaming mode. The
window defaults to 15 minutes and 50 completed attempts. Five samples are required
for observed reliability and latency comparisons. Reliable candidates have at
least 90% success and are within five percentage points of the best observed
success rate. When none qualify, the smoothed success prior is `(successes + 3.5) /
(samples + 5)`, and the decision is marked degraded.

Latency comparisons use successful attempts only. Within the fastest P50 plus
`max(500ms, fastest * 0.1)`, effective cost then key ID breaks ties. Bindings survive
small changes in latency and all sub-limit load changes. An improvement must be
at least 20% and 2000ms, persist for 60 seconds, and have three new successful
latency observations after confirmation began. Hard eligibility and reliability
loss bypass this hold. Existing provider/key concurrency reservations still apply.

The exploration ratio defaults to 0.05 and can be disabled with 0. Every twentieth
new-session or unbound-session request can explore the least recently sampled
eligible alternative. Existing sessions never explore. Each candidate has a
one-minute exploration interval; previews do not increment counters. A successful
exploration binds a new session but does not replace the sessionless preferred key.

Session keys use explicit `X-Session-Id`, then `Session_id`, then a successful
`previous_response_id` mapping, then canonical system instructions plus the first
user input. Derived sessions are heuristic; explicit IDs are recommended for
clients that compact or replace conversation prefixes. All state is scoped to
the consumer, protocol, model, endpoint, stream mode and sorted allowed-key set.
Changing group membership therefore invalidates that scope. The state uses Redis
WATCH transactions; local state is used during Redis outages and cannot coordinate
different processes during an outage. Capacity limits remain per process, as in
the previous scheduler.

## Response and data changes

The text endpoints reject non-protocol 2xx responses, including plain text greetings,
HTML and error JSON. Streaming requests require SSE with valid protocol events and
a valid terminal. Tool-only results and refusals are accepted. After any output is
committed, failure ends that stream without replaying it through another key.
Synchronous text JSON is validated before output and capped at 16 MiB; image
passthrough behavior is unchanged. Protocol errors cool down the key/model, without
retrying the same invalid response.

`request_logs.ttft_ms` remains cumulative user waiting time. New `request_attempts`
rows expose individual upstream times via the existing log-detail API's `attempts`
array. Version 2 records exclude client cancellations and local capacity rejections
from reliability statistics. Attempts are inserted once by UUID and expire using
the request-log retention policy. Successful OpenAI input usage is normalized to
exclude cached tokens before calculating the scheduler's cache ratio. An abrupt
process termination can lose an unfinished attempt; no invented failure is backfilled.

Redis routing state uses `sum:v2:stable:*`; old windows and old logs are never used
by the new mode. No destructive data migration or history rewrite is required.
The SQL attempt table is the source of truth and recovers windows after restart.

## Release procedure

1. Back up scheduler settings and deploy the new binary with the existing mode.
   Automatic migration adds `request_attempts` and four settings fields; old
   binaries can ignore these additive changes.
2. Collect at least one full 15-minute window. Verify protocol rejection rates,
   attempt-write errors, and effective sample counts in log details. Use previews
   with the actual consumer, model, endpoint and stream mode.
3. Save `ranking_mode: stable_latency`. Leave `switch_improvement_ratio: 0.2`,
   `switch_improvement_ms: 2000`, `switch_confirm_sec: 60`, `exploration_ratio: 0.05`
   for the first evaluation. Do not change group membership or prices simultaneously.
4. Compare at least 24 hours by consumer/model/endpoint/stream: valid success,
   cumulative user TTFT P50/P90, cost, and switching reasons. Compare the same
   request population, excluding local rejection and client cancellation.
5. Revert only `ranking_mode` to its previous value if two consecutive 15-minute
   windows, each with at least 100 observations, lose over two percentage points
   of success or worsen TTFT P90 over 10% against the matched baseline. This is an
   operator decision, not an automatic rollback job. Keep protocol/data repairs.

Historical logs lack candidate snapshots, so historical decision replay cannot
reproduce every original choice. New trace decisions preserve the initial reason,
previous key, session source, exploration flag and candidates even after success.

## Verification

Use Go 1.25 or newer. Run `go test ./...` from `backend`, then
`TEST_REDIS_ADDR=127.0.0.1:16392 go test -race ./internal/picker ./internal/metrics
./internal/handler ./internal/upstream` against an isolated Redis instance. The
Redis test skips explicitly when the address is absent. Run `npm ci` and
`npm run build` from `frontend`. Never use production Redis for these tests.
