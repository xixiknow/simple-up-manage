# Probe and business routing isolation

Diagnostic probes no longer write key health, errors, cooldowns, model lists, or
business attempt statistics. Explicit model discovery and balance synchronization
retain their existing roles. Probe success never closes a business circuit.
The channel list's historical score/pulse remains diagnostic and can contain
probes; scheduler scores use traffic only.

## Business circuits

The shared eligibility policy applies to all ranking modes. The default circuit
opens after three consecutive independent failed requests completing within a
rolling 60-second window. Same-key retries count once, using an internal UUID
unrelated to client-supplied request IDs. A successful request resets that dimension;
cancellation, local resource rejection, and generic invalid client requests are
neutral. Ranking attempt samples still describe individual attempts.

Circuit dimensions are key, protocol, model, endpoint, and streaming mode. Timeouts,
5xx, 404, generic 403, and protocol failures cannot disable the whole provider.
Structured capability errors on 400 allow failover; other 400 responses are passed
through and excluded from reliability. 401 and explicit authentication errors on
403 open a key-wide gate. Upstream-reported quota exhaustion also gates only that
key: it does not invent a zero balance for the entire provider account. Confirmed
account balances continue to gate all associated keys.

429 immediately opens its dimension and honors a positive Retry-After in seconds
or HTTP-date (maximum 24 hours); otherwise initial cooldown is 30 seconds. Ordinary
recovery failures back off 30, 60, 120, 300 seconds, capped at five minutes.
The four new scheduler fields are `circuit_window_sec` (60),
`circuit_failure_threshold` (3), `circuit_cooldown_sec` (30), and
`circuit_max_cooldown_sec` (300). Old `failure_window_sec`, `failure_threshold`, and
`cooldown_sec` remain in the API for compatibility, but are not the new circuit
policy; the admin form edits the new fields.

## Recovery and diagnostics

An expired gate requires one real validation request. All processes reserve its
SQL lease atomically before contacting upstream. The 330-second lease exceeds the
gateway's 300-second request deadline. Neutral completions release the lease;
expired leases can be reclaimed, and old owners cannot close another owner's gate.
State-store errors fail closed. Gate outcomes are committed synchronously, once
per logical request/key/dimension, independently of asynchronous request-log writes.

Recovery and stable-latency exploration share a SQL counter scoped like the routing
pool. With the default 0.05 exploration ratio, every twentieth incoming request may
validate an expired gate; retries and previews never advance this counter. Active
bound sessions do not use this slot while normal candidates exist. When no normal
candidate remains, an expired gate may validate without waiting for a slot, even
when exploration is disabled. An open gate never bypasses its cooldown or lease.
Successful recovery does not replace the existing preferred/session binding.

Deep probes rotate the least recently probed dimensions from up to 32 distinct
traffic dimensions observed in the last 24 hours. Requests use a synthetic prompt,
never user content. They use the same JSON/event validator as the gateway, a token
budget of 256, and the configured probe timeout. Without traffic, the configured
probe model and protocol's native chat endpoint are used as diagnostics only.
Three consecutive failed probes in one dimension produce a server-log alert.

The candidate API adds probe status/time/model/path/mode, circuit state/reason/scope,
cooldown or lease deadline, and a recovery flag. `recovery_validation` is preserved
in selection traces. Group-member mismatches are labeled `model_required`,
`route_model_mismatch`, `route_protocol_mismatch`, or `route_group_disabled`;
nonmembers alone receive `not_in_route_group`.

## Rollout and rollback

Migration adds circuit, deduplication, budget, and migration-marker tables plus
probe dimensions and scheduler fields. A one-time migration seeds expired key-wide
`legacy_health_unverified` gates for old `down` keys. It does not clear explicit
disablement, balances, or existing unexpired cooldowns. Successful recovery clears
the gate, and restarting does not recreate it. Deduplication entries, idle budget
rows, and closed idle circuits follow request-log retention; open gates persist.

Deploy with the existing ranking mode first. Observe at least one 15-minute window
of valid success, user TTFT P50/P90, scoped failure reasons, and recovery admissions
before separately switching to `stable_latency`. Do not change group memberships
or prices during comparison. Retain the prior image and scheduler configuration.
Rolling back to an old image ignores the additive tables and restores its original
probe-driven eligibility, so old erroneous probe exclusions can return. Before a
subsequent forward deployment, review retained circuits from the intervening period.

## Verification

Run `go test ./...` and `go vet ./...` using Go 1.25 or newer. Race-test routinghealth,
picker, handler, ops, and upstream. Set `TEST_REDIS_ADDR` to an isolated Redis for
cross-instance binding tests. Set `TEST_ROUTING_DATABASE_URL` to an isolated
PostgreSQL URL to exercise concurrent circuit updates, leases, and shared budget;
that test creates and drops a unique schema. Never use production for these tests.
Build the frontend and inspect candidate explanations at desktop and mobile widths.
