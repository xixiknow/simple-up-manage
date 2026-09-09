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
