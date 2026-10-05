# Deploy

## 本地开发栈（docker-compose.yml）

单实例 + 本地构建，适合开发自测：

```bash
docker compose up --build   # http://localhost:8080
```

`APP_ENCRYPT_KEY` 必须是 32 字节原文或 64 位 hex（AES-256-GCM）。Schema 由 GORM `AutoMigrate` 启动时建。

## 生产栈（deploy/compose.prod.yml，蓝绿 + 不停机更新）

```
用户 → 宿主机 Nginx (:80/:443)          ← 公网入口，更新时不需要动
        └→ Caddy 容器 (127.0.0.1:8080)   ← 切换层，应用改写 Caddyfile 后 reload
            ├→ sum-app-blue:8080         ← 活跃实例
            └→ sum-app-green:8080        ← 待命实例（由应用按需创建/销毁）
```

- compose 只管理 postgres / redis / proxy 和**初始的** app-blue（`--profile bootstrap`）。
- 此后的版本更新由管理界面一键完成：应用通过挂载的 docker.sock 拉新镜像、在对侧
  颜色创建新实例（克隆自身配置）、健康检查通过后切换 Caddy 上游、旧实例 SIGTERM
  排空（最长 330s）后退出。**全程零停机**。
- 版本号（git 短 sha）由 CI 注入二进制，`/health` 与管理界面侧边栏均可见；
  界面会对 ghcr 的 `latest` 做 digest 对比，有新版本时提示。
- 首次初始化与迁移步骤见 **[PRODUCTION-UPGRADE.md](PRODUCTION-UPGRADE.md)**。

### 环境变量（deploy/.env）

| 变量 | 说明 |
| --- | --- |
| `ADMIN_TOKEN` | 管理界面与 API 的 Bearer token（必填） |
| `APP_ENCRYPT_KEY` | AES-256-GCM 密钥，32 字节原文或 64 位 hex（必填） |
| `BLUE_IMAGE` | 初始实例镜像，默认 `ghcr.io/xixiklow/simple-up-manage:latest` |
| `DOCKER_CONFIG_JSON` | 可选，私有 ghcr 镜像时指向宿主机 `~/.docker/config.json`，并打开 compose.prod.yml 中对应挂载行 |

### 运维约束（自更新依赖它们，不要绕开）

- **日常不要运行 `docker compose --profile bootstrap up -d`**：这会把旧颜色的
  实例重新拉起来，与新实例形成双实例（定时任务双跑）。管理 postgres/redis 时用
  `docker compose -f deploy/compose.prod.yml up -d`（不带 profile）。
- 应用容器挂载了 `/var/run/docker.sock`（宿主等价权限）。这是界面自更新的前提；
  触发入口仅有 `POST /api/v1/admin/system/update`（admin token 鉴权）。
- 数据库迁移保持**加列式**约定：新实例迁移时旧实例仍在服务，删除列/改类型会破坏
  蓝绿切换。
- Recovery worker 用数据库 lease 跨实例协调，双实例重叠窗口（数秒到排空期）是安全的；
  其余 ticker 在 SIGTERM 瞬间即停止（`main.go` 收到信号先 `close(stop)`），不会双跑。
- `deploy/caddy/state.json` 记录当前活跃颜色与版本历史（供界面回滚），在
  .gitignore 中，只存在于生产机。

### 手动指定版本 / 回滚（与界面按钮等价）

```bash
curl -X POST http://127.0.0.1:8080/api/v1/admin/system/update \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"target":"<git-sha-或-latest-或-previous>"}'

# 查看进度
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://127.0.0.1:8080/api/v1/admin/system/update/status
```
