# simple-up-manage

Southbound LLM gateway for an existing [sub2api](../sub2api) deployment. This service:

- Manages many upstreams (mostly other sub2api instances)
- Issues consumer keys for the northbound sub2api
- Transparently proxies OpenAI + Anthropic streaming
- Tracks balances, probes, price thresholds, and request logs

Multi-factor scheduling (scoring, sticky sessions, failover, weights) is **not** in this phase. Traffic uses the first enabled PlatformKey whose upstream is enabled and whose `protocols` match the request. Price thresholds and low balance are display/status only.

## 本地调试（无需 Docker）

仓库已带 `config.local.yaml`：SQLite 文件库 + 不连 Redis。调度窗口指标走内存，功能完整。

两个终端：

```powershell
# 终端 1：API :8080
.\scripts\dev-backend.ps1

# 终端 2：管控台 :5173
.\scripts\dev-frontend.ps1
```

或在 Cursor / VS Code 里用 **Backend (SQLite)** 调试后端断点，管控台另开 `npm run dev`。

- 管控台：http://localhost:5173
- 登录 Token：`dev-admin`
- 数据文件：`backend/data/local.db`（已 gitignore）

## Docker 部署

前后端打进同一个镜像：先 `npm run build` 管控台，再编译 Go，由后端托管静态文件。

```bash
docker compose up --build
```

- 管控台: `http://localhost:8080`
- API: `http://localhost:8080`
- Health: `GET /health` → `{"status":"ok"}`
- Admin: `Authorization: Bearer <ADMIN_TOKEN>`
- Gateway: `Authorization: Bearer sk-...`

CI（语法检查 + 构建镜像）见 `.github/workflows/ci.yml`。推到 `main` 后镜像发布到 `ghcr.io/xixiknow/simple-up-manage`。

See [backend/README.md](backend/README.md)、[deploy/README.md](deploy/README.md)、[docs/sub2api-integration.md](docs/sub2api-integration.md)。

## Admin API (`/api/v1/admin/*`)

All admin responses:

```json
{ "ok": true, "data": ... }
{ "ok": false, "error": { "code": "...", "message": "..." } }
```

List endpoints wrap `{ "items": [], "total": 0, "page": 1, "page_size": 20 }`. Platform keys never return decrypted `api_key`; only `key_preview`.

| Method | Path | Notes |
|--------|------|--------|
| GET/POST | `/api/v1/admin/upstreams` | POST `{name, base_url, kind, protocols[], note, status}`; `kind` in `sub2api` / `new_api` / `openai_compat` / `anthropic_compat` |
| GET/PUT/DELETE | `/api/v1/admin/upstreams/:id` | |
| GET/POST | `/api/v1/admin/upstreams/:id/keys` | POST `{name, api_key, status, concurrency, rate_multiplier, billing_group}` |
| PUT/DELETE | `/api/v1/admin/keys/:id` | PUT `api_key` optional; `rate_multiplier` 默认 1；`billing_group` 仅 new-api |
| POST | `/api/v1/admin/keys/:id/probe` | `{deep?: bool}` |
| POST | `/api/v1/admin/keys/:id/refresh-balance` | sub2api: `GET /v1/usage`; new-api: `GET /v1/dashboard/billing/{subscription,usage}` (fallback `GET /api/usage/token/`) |
| POST | `/api/v1/admin/keys/:id/refresh-billing` | sub2api: `GET /v1/sub2api/billing`; new-api: `GET /api/pricing` `group_ratio[<group name>]` |
| POST | `/api/v1/admin/keys/:id/fetch-models` | `GET /v1/models` on the upstream, stores `last_models` / `last_models_at`; returns `{models, count, fetched_at}` |
| POST | `/api/v1/admin/upstreams/:id/fetch-models` | fetch-models for every enabled key of the upstream; returns `{ok, failed, models, models_count}` |
| GET | `/api/v1/admin/keys` | all keys + upstream name / balance / health / `last_models` / `rate_multiplier` |
| GET/POST | `/api/v1/admin/price-thresholds` | |
| PUT/DELETE | `/api/v1/admin/price-thresholds/:id` | |
| GET | `/api/v1/admin/price-overview` | each key vs thresholds, `in_range` / `out_of_range` |
| GET/POST | `/api/v1/admin/consumer-keys` | POST returns raw key once |
| PUT/DELETE | `/api/v1/admin/consumer-keys/:id` | |
| GET | `/api/v1/admin/balances` | |
| POST | `/api/v1/admin/balances/refresh` | all sub2api keys |
| GET | `/api/v1/admin/status` | health matrix |
| POST | `/api/v1/admin/probes/run` | light probe all enabled keys |
| GET | `/api/v1/admin/request-logs` | `upstream_id`, `key_id`, `success`, `model`, `page`, `page_size`, `from`, `to` |
| GET | `/api/v1/admin/scheduler` | 调度权重与探测模型 |
| PUT | `/api/v1/admin/scheduler` | |
| GET | `/api/v1/admin/scheduler/explain` | |
| GET | `/api/v1/admin/model-catalog` | 已同步的 models.dev 厂商模型（OpenAI / Anthropic / Grok / 智谱 / 月之暗面 / Deepseek） |
| POST | `/api/v1/admin/model-catalog/sync` | 从 https://models.dev/api.json 同步；空探测槽位会填该厂商最便宜聊天模型 |

## Gateway API (consumer key)

| Method | Path |
|--------|------|
| POST | `/v1/messages` |
| POST | `/v1/chat/completions` |
| POST | `/v1/responses` |
| POST | `/v1/images/generations` |
| POST | `/v1/images/edits` (multipart/form-data) |
| POST | `/v1/images/variations` (multipart/form-data) |
| GET | `/v1/models` |
| GET | `/v1/usage` |

SSE is forwarded as-is. Non-streaming bodies are streamed through unmodified (no size cap); the first 16MB is inspected for `usage`. Request logs are written after completion (best-effort usage + TTFT parse).

Image endpoints route as protocol `openai`. Multipart bodies are capped at 64MB (413 above that); `model` is read from the form field.

## new-api upstreams

Register the upstream with `kind: new_api` and the new-api token (`sk-...`) as the platform key. Relay endpoints are forwarded unchanged (`/v1/models`, `/v1/chat/completions`, `/v1/messages`, `/v1/responses`, `/v1/images/*`; new-api answers `/v1/images/variations` with 501).

- Balance: `GET /v1/dashboard/billing/subscription` + `/usage` (USD; `hard_limit_usd >= 1e8` means unlimited and clears `last_balance`). Falls back to `GET /api/usage/token/` (raw quota / 500000).
- Rate multiplier: `GET /api/pricing` is public by default; fill **the key's `billing_group`** with the new-api group of the token (e.g. `default`, `vip`; empty means `default`). The matching `group_ratio` is written to the key's `rate_multiplier`. If the endpoint is disabled (401/403/404) the key is marked `billing_unsupported` with a 24h backoff.
- Quota exhausted: new-api returns `403` with code `insufficient_user_quota` / `pre_consume_token_quota_failed`; the gateway marks the key `low_balance` and fails over to the next key.

## Model-based routing

Each platform key stores the model ids last returned by its upstream's `GET /v1/models` (`last_models`, refreshed by the light probe and by the admin fetch-models actions). When the scheduler setting `filter_by_models` is on (default), a key whose list is non-empty and does not contain the requested `model` is skipped with reason `model_not_supported`. Keys with an empty list are never filtered. Matching is case-insensitive and exact.

## Route groups and rate range

Each platform key has its own `rate_multiplier` (default 1; synced from sub2api / new-api when available). A route group may set optional `rate_min` / `rate_max`. Members whose multiplier falls outside that range stay in the group but are skipped at pick time with reason `route_rate_drift`.
