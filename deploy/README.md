# Deploy

Docker Compose stack: PostgreSQL 16, Redis 7, and a single app image.

The image builds the Vue admin UI and the Go gateway together. The binary serves the SPA from `/app/web` (`STATIC_DIR`).

## Bring up

From the repository root:

```bash
docker compose up --build
```

- Admin UI: `http://localhost:8080`
- API / gateway: `http://localhost:8080`

Override secrets with env:

```bash
ADMIN_TOKEN=... APP_ENCRYPT_KEY=... docker compose up --build
```

`APP_ENCRYPT_KEY` must be 32 raw bytes or 64 hex characters (AES-256-GCM).

Copy `deploy/config.example.yaml` if you run the binary outside Compose.

Schema is created with GORM `AutoMigrate` on boot.

Recovery workers use `RECOVERY_CONCURRENCY=8` by default (1–64). Configure the same
value on every replica: database leases enforce the shared global limit. The
application polls due recovery gates every second and fills freed slots immediately.
The additive `routing_circuits.generation` column migrates on startup. Replace the
application after draining in-flight requests; avoid mixing old/new recovery writers
during rollout. Rollback keeps the column and already closed circuits; future
recoveries return to the old policy.
