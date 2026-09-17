# simple-up-manage backend

Southbound LLM gateway + admin API. Existing sub2api is the intended consumer.

## Run locally（无需 Docker）

默认读仓库根目录的 `config.local.yaml`（SQLite `data/local.db`，Redis 可空）。

```powershell
# 在仓库根目录
.\scripts\dev-backend.ps1
```

或：

```powershell
cd backend
$env:CONFIG_FILE = "..\config.local.yaml"
go run ./cmd/server
```

Cursor 调试：运行配置 **Backend (SQLite)**。

Postgres 仍可用：把 `database_url` 换成 `postgres://...`。Redis 可选；不配则窗口指标和粘滞走内存。

Env 覆盖 YAML：`LISTEN`、`ADMIN_TOKEN`、`APP_ENCRYPT_KEY`、`DATABASE_URL`、`REDIS_URL`、`CONFIG_FILE`。

GORM auto-migrates on boot. Health: `GET http://localhost:8080/health`.

Admin calls need `Authorization: Bearer <ADMIN_TOKEN>`. Gateway calls need `Authorization: Bearer sk-...` (a consumer key).

CORS allows the Vite origin `http://localhost:5173`.

## Layout

```
cmd/server          entrypoint
internal/config     yaml + env
internal/crypto     AES-GCM for platform keys
internal/domain     GORM models
internal/handler    admin + gateway
internal/picker     BandPicker (price gate + quality band + effective cost)
internal/metrics    sliding-window success / cache / TTFT
internal/ops        probe / balance / billing / price gate / model catalog
internal/catalog    models.dev sync (OpenAI / Anthropic / Grok / 智谱 / 月之暗面 / Deepseek)
internal/upstream   HTTP client + response parsers
internal/jobs       background tickers
```

Scheduler: hard-filter by price threshold / health / balance, then pick the lowest effective cost inside the near-best quality band. Anthropic sticky and limited 5xx failover are on by default.

Scheduled health probes run on `jobs.probe_interval` ticks. Each key is checked at
most once per fixed time window (`probe_interval_sec`, or the global interval when
zero), so a probe's response time does not add another full tick to its cadence.
Windows align to clock boundaries; an interval is a window size, not a minimum
elapsed delay after the previous probe finishes. Recent requests still suppress
scheduled probes for their configured interval. Disabled keys are excluded.
The 60-minute health history retains gray cells for minutes without a completed
health probe or request.

Repeated scheduled health probe failures back off from one to fifteen minutes;
explicit credential, quota and capability failures use longer retry delays and
honor `Retry-After`. Manual health checks remain available. Balance and billing
jobs keep their existing schedules. A balance query confirming positive or
unlimited credit clears existing `key_quota_exhausted` routing circuits for that
provider's keys. Other failure reasons and failures recorded after the query
started remain in effect; zero, unknown balances and failed queries never clear
these circuits.

Responses SSE `keepalive` events are accepted as metadata, without counting as
output or completion. Request log prefixes and error text are sanitized for
PostgreSQL UTF-8 storage, and final writes finish before the request handler
returns. Stale in-flight logs recover a successful result only when their latest
recorded attempt succeeded. Rejected routes include a candidate snapshot and a
`no_available_route` code with a 30-second `Retry-After` header.
