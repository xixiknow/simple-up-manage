# 生产环境升级 Runbook：单实例 → 蓝绿不停机更新

> 本文档是给运维（人或 AI 助手）的可执行迁移手册。目标：把生产从
> 「docker-compose.yml 单 backend 容器直绑 8080」迁移到
> 「宿主机 Nginx → Caddy 切换层 → app-blue/app-green 蓝绿 + 界面一键更新」。
> 迁移过程会有一次约 1-2 分钟的计划内停机（Step 5-6），之后所有更新永久零停机。
>
> 每一步都有 ✅ 验证命令；任何验证失败，先按「故障处理」排查，不要带着问题往下走。

## 0. 架构与原理（先读再动手）

```
用户 → 宿主机 Nginx (:80/:443)              [一次性配置，之后永不改动]
        └→ Caddy 容器 sum-proxy (127.0.0.1:8080)
            ├→ sum-app-blue:8080    活跃实例（compose 初始创建）
            └→ sum-app-green:8080   待命实例（应用自更新时创建）
postgres(sum) / redis 不变，仅端口改绑 127.0.0.1
```

- 版本身份：git 短 sha 经 CI 注入二进制，`GET /health` 返回 `{"status":"ok","version":"<sha>"}`。
- 更新流程（用户在管理界面点「更新」或调 API）：活跃实例通过 docker.sock 拉新镜像 →
  在对侧颜色创建新容器（克隆自身 env/volumes/网络）→ 等新容器 `/health` 就绪且版本
  匹配 → 改写 `deploy/caddy/Caddyfile` 的 upstream → `docker exec sum-proxy caddy reload`
  平滑切换 → 旧容器 SIGTERM（应用收信号即停全部定时任务、排空在途请求最长 330s）。
- 失败安全：新容器健康检查不过 → 不切换、自动清理新容器，旧实例继续服务。

## 1. 预检（只读，不改任何东西）

```bash
cd <仓库目录>                          # 生产机上现有 checkout
git fetch && git log --oneline -3      # 确认可拿到本次升级提交

docker ps --format '{{.Names}}\t{{.Ports}}\t{{.Image}}'
docker compose ls                      # 记录当前 compose 项目名
```

需要确认三件事，后面要用：

```bash
# a) 旧栈的 compose 项目名（决定新栈如何复用数据卷，见 Step 3）
docker inspect <旧backend容器名> --format '{{index .Config.Labels "com.docker.compose.project"}}'

# b) 数据卷现状
docker volume ls | grep -E 'pgdata|logbodies'

# c) 旧 backend 实际生效的密钥（若旧栈用 .env 覆盖过，直接看 .env；否则是 compose 里的默认值）
grep -E 'ADMIN_TOKEN|APP_ENCRYPT_KEY' .env 2>/dev/null || echo "使用 docker-compose.yml 默认值"
```

✅ 8080 当前被旧 backend 占用（`docker ps` 可见 `0.0.0.0:8080->8080`）。

## 2. 备份

```bash
docker exec $(docker ps -qf name=postgres) pg_dump -U sum simple_up_manage | gzip > ~/sum-backup-$(date +%F).sql.gz
ls -lh ~/sum-backup-*.sql.gz
```

✅ 备份文件 > 0 且 gzip 完整（`gzip -t`）。

## 3. 拉代码 + 校准项目名

```bash
git pull --ff-only
git log --oneline -1    # 应为本 runbook 对应的发布提交
```

⚠️ **数据卷复用是本步的关键**：`deploy/compose.prod.yml` 顶部固定了
`name: simple-up-manage`。如果 Step 1-a 查到的旧项目名不是 `simple-up-manage`
（例如服务器目录叫别的名字），二选一：

- 编辑 `deploy/compose.prod.yml`，把 `name:` 改成旧项目名（推荐，docker volume
  `simple-up-manage_pgdata` 会被原样复用）；或
- 所有 compose 命令加 `-p <旧项目名>`。

✅ `grep '^name:' deploy/compose.prod.yml` 输出与旧项目名一致。

## 4. 配置密钥与宿主机 Nginx

```bash
cp 旧.env的值 → deploy/.env     # 至少包含两行：
# ADMIN_TOKEN=<Step 1-c 的值>
# APP_ENCRYPT_KEY=<Step 1-c 的值>
chmod 600 deploy/.env
```

宿主机 Nginx 站点（复制 `deploy/nginx.conf.example` 到
`/etc/nginx/conf.d/sum.conf`，改 `server_name`）：

```bash
sudo cp deploy/nginx.conf.example /etc/nginx/conf.d/sum.conf
sudo vi /etc/nginx/conf.d/sum.conf      # 改 server_name；保留全部 proxy_* 参数
sudo nginx -t && sudo nginx -s reload
```

⚠️ `proxy_buffering off` / `proxy_read_timeout 360s` / `client_max_body_size 512m`
是为 LLM 流式请求与大请求体调过的，**不要省略**（Nginx 默认 60s 超时/1m 体积会截断请求）。

✅ `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/` 此刻应为 200（旧
backend 还在 8080 上，Nginx 已能转发到它）。

## 5. 下线旧栈（计划内停机开始）

```bash
docker compose down        # 在仓库根目录执行；不带 -v，pgdata/logbodies 保留
docker ps | grep -E 'backend|8080' || echo "旧栈已下线"
```

✅ 8080 已释放（`ss -ltnp | grep 8080` 无输出）。

## 6. 启动蓝绿栈（停机结束）

```bash
docker compose -f deploy/compose.prod.yml --profile bootstrap up -d
docker compose -f deploy/compose.prod.yml ps
```

首次会拉取 `ghcr.io/xixiklow/simple-up-manage:latest`。若 401（仓库私有）：

```bash
docker login ghcr.io   # 用户名 + PAT(read:packages)
# 或在 deploy/.env 加 DOCKER_CONFIG_JSON=/home/<user>/.docker/config.json，
# 并打开 compose.prod.yml 中 config.json 挂载行的注释
```

✅ 验证（全部满足才算过）：

```bash
curl -s http://127.0.0.1:8080/health
# 期望: {"status":"ok","version":"<短sha>"}

docker ps --format '{{.Names}}\t{{.Status}}' | grep sum-
# 期望: sum-app-blue / sum-proxy Up (healthy)，postgres/redis Up

docker logs sum-app-blue --tail 5    # 应有 "self-update: enabled color=blue peer=sum-app-green"
```

浏览器走 Nginx 域名登录管理界面：侧边栏「版本」行应显示与上面相同的 sha。
若用户原先直连 `http://<ip>:8080`，通知改用 Nginx 域名（8080 已只绑 127.0.0.1）。

## 7. 验证自更新能力（不真正触发更新）

```bash
curl -s -H "Authorization: Bearer $(grep ADMIN_TOKEN deploy/.env | cut -d= -f2)" \
  http://127.0.0.1:8080/api/v1/admin/system/version
```

✅ 期望：`can_self_update: true`、`version` 非 dev、`update_available` 为
true（若 registry 的 latest 比当前新）或 false（已最新）。

## 8. 之后的日常发布（零停机）

1. 推送代码到 main → GitHub Actions 构建并推送 `ghcr.io/xixiklow/simple-up-manage:<sha>`。
2. 管理界面侧边栏/顶栏出现更新提示 → 点开确认 → 「开始更新」。
3. 观察进度（拉镜像 → 建容器 → 等就绪 → 切流量）→ 完成后刷新页面。
4. 出问题点「回滚到此版本」（用 state.json 里的上一版本）。

命令行等价：

```bash
TOKEN=$(grep ADMIN_TOKEN deploy/.env | cut -d= -f2)
curl -X POST http://127.0.0.1:8080/api/v1/admin/system/update \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"target":"latest"}'
watch -n2 "curl -s -H \"Authorization: Bearer $TOKEN\" http://127.0.0.1:8080/api/v1/admin/system/update/status"
```

## 故障处理

| 症状 | 排查 |
| --- | --- |
| 拉镜像 401/403 | `docker login ghcr.io`；或配 `REGISTRY_AUTH_FILE`（见 Step 6） |
| 卡在「等待新实例就绪」 | `docker logs sum-app-green`（看 AutoMigrate 是否报错）；`curl http://127.0.0.1:8080/health` 应仍是旧实例 |
| 更新失败但服务正常 | 设计如此：新容器未就绪时不切流。`docker logs sum-app-blue | grep self-update` 看失败原因，修复后重试 |
| caddy reload 失败 | `docker exec sum-proxy caddy validate --config /etc/caddy/Caddyfile`；`cat deploy/caddy/Caddyfile` 检查 upstream 是否被写坏（可手工改回后 reload） |
| 误开了双实例 | `docker ps | grep sum-app-` 出现两个 Up 的 app → 停掉旧颜色的那个：`docker stop sum-app-<旧色> && docker rm sum-app-<旧色>`（保留 state.json 里记录的活跃色） |
| 界面显示 can_self_update=false | 看 reason：缺 COLOR / 未挂载 docker.sock / 非 postgres。用 `docker exec sum-app-blue env | grep -E 'COLOR|CADDY'` 核对 |
| 服务器重启后 | `unless-stopped` 策略会自动拉起全部容器（活跃 app、proxy、pg、redis）；旧颜色的容器保持停止状态，这是正常的 |

## 已知边界

- `deploy/caddy/state.json` 与 `deploy/caddy/Caddyfile` 在生产机上被应用容器读写，
  属运行时文件；state.json 已 gitignore。
- 迁移后管理 postgres/redis 请用 `docker compose -f deploy/compose.prod.yml up -d`
  （**不带** `--profile bootstrap`，否则旧颜色实例会被重新拉起形成双实例）。
- 应用容器持有 docker.sock（宿主等价权限）。更新入口仅 admin token 鉴权的一个
  POST 接口，所有动作在 `docker logs sum-app-*` 中有 self-update 前缀日志可审计。
