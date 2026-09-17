# Request Log Archives

Request bodies and each upstream attempt's response text are archived separately
as gzip files. The database keeps a 64 KiB preview and `log_bodies` metadata.
The administrator log drawer reads 256 KiB pages or downloads the stored text.
It identifies incomplete, omitted, failed, and historical captures separately.

Configuration defaults:

| Setting | Default |
| --- | --- |
| `log_bodies_dir` / `LOG_BODIES_DIR` | `data/log-bodies` |
| `log_body_max_bytes` | 67108864 (64 MiB of source text) |
| `log_bodies_max_bytes` | 10737418240 (10 GiB compressed) |
| `jobs.log_retention` | 24h |
| `jobs.log_retention_interval` | 1h |

Mount the directory persistently. Run one application writer per archive
directory; replicas require separate directories and routing to the owning
instance. The current deployment is a single instance. Header credentials remain
redacted. Body text retains its original content, including encoded data in JSON.
Multipart archives contain text fields and file metadata, without uploaded file
bytes. Binary bodies retain a type and size placeholder.

Archive writers share a 32 MiB queue budget. Queue exhaustion, file errors, and
size/quota limits stop capture without failing the proxy request. Incomplete
archives are explicitly marked. Compression failure removes the unusable file.
Cleanup removes expired files and metadata, and expires orphan files. Restart
marks unfinished captures as interrupted and removes temporary files. Graceful
shutdown drains HTTP requests and archive writers; containers need a 330s stop
grace period for the existing 300s request deadline.

`GET /api/v1/admin/request-logs/:id` includes archive metadata and attempt
diagnostics. `GET /api/v1/admin/request-logs/:id/bodies/:body_id` accepts `offset`
and `limit` (maximum 262144) and returns text, next byte offset and EOF. Use
`download=1` for an attachment containing the saved, decompressed text. Both
endpoints require administrator authentication and verify request ownership.

`ttft_status` differentiates measured, pending, no detected output, interruption,
and oversized uninspected events. `ttft_event` identifies the first recognized
content event. Request TTFT includes earlier attempts; attempt TTFT and event
summary times are relative to that attempt. Metadata and empty/encrypted
reasoning are excluded. Completion content counts only when it contains actual
generated text or tool input. Existing missing TTFT and truncated bodies cannot
be reconstructed.

Attempt diagnostics add failure phase/action/message, response header latency,
received bytes, and a bounded event summary. Business first-token timeout is
30s and is independent of probe timeout. On `/v1/responses`, a validated
`response.compaction.compacting` event or `response.output_item.added` with
`item.type=compaction` before the first output allows waiting up to the existing
300s overall request deadline. Time spent on earlier attempts counts toward
that deadline; repeated compaction events never reset it. Client cancellation
still stops the request immediately. Compaction is neither first-token output
nor a successful response, and does not release buffered response headers.

SSE timeouts before output use `first_token_timeout`, `compaction_timeout`, or
`upstream_timeout`; malformed responses retain `invalid_response`. A compaction
timeout has failure phase `compacting`. Before response headers,
`transport_failure` remains the compatible routing action; the diagnostic phase
distinguishes DNS, TCP, TLS, request sending and response-header waiting.

Deploy after the full Go suite, vet, handler/archive race tests, frontend build
and browser checks. Add the persistent mount before replacing the container.
Confirm archive completeness and TTFT event fields using new production logs.
Rolling back the image leaves the additive database columns/table and archive
files intact; the old image does not use or clean up archive files.
