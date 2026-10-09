# 工程约束

## 发版：版本 tag 必须与 commit 同一次推送

打 `v*` 版本 tag 时，tag 和它指向的 commit 必须在同一次 push 中到达远程：

```bash
git tag -a v1.3.3 -m "release v1.3.3"
git push origin main --follow-tags
```

**tag 必须用 `-a` 打成注解 tag**：`--follow-tags` 只跟随注解 tag（`git tag --follow` 同理），轻量 tag（`git tag v1.3.3`）会被静默留在本地——main 到了远程而 tag 没到，效果与"先 push main 再补 tag"完全相同（2026-10-09 的 v1.4.1 曾因此返工）。

禁止先 `git push origin main`、之后再单独补推 tag。

**原因**：CI 由 push main 触发（`.github/workflows/ci.yml` 的 image job），构建时通过 `git tag --points-at HEAD` 解析版本号，并注入到 `build-args: VERSION`、OCI label `org.opencontainers.image.version` 以及镜像标签 `vX.Y.Z`/`X.Y.Z`。若 tag 晚于 main 到达远程，CI 解析不到版本：镜像只会打 `latest`/`main`/`<sha>` 标签，且二进制内 `buildinfo.Version` 是短 sha 而非版本号，selfupdate 的版本对比会误判。

**事故恢复**：tag 补推后，对当次 run 执行 `gh run rerun <run-id>` 重跑即可正确打标（2026-10-08 的 v1.3.2、2026-10-09 的 v1.4.1 均曾因此返工）。注意 rerun 只能在 run **完全结束**后执行（运行中会报 "already running"），首轮 image job 已按无版本号方式发布到 `latest`/`main`/`<sha>` 属预期，rerun 后的产物才带版本标签。

## 发版：版本号约定

### 格式：只允许 `vX.Y.Z` 严格三段

CI 只认 `^v[0-9]+\.[0-9]+\.[0-9]+$`（ci.yml 的 Resolve release version 正则）。`v1.4.0-rc.1`、`v1.4`、`release-1.4` 之类的 tag 会被静默忽略——镜像只打 `latest`/`main`/`<sha>` 标签，`buildinfo.Version` 退化为短 sha，selfupdate 版本对比误判。**不使用预发布后缀**；要发 RC 直接用正式版本号发。

### 递增规则（Semver）

- **PATCH**（v1.3.2 → v1.3.3）：bug 修复、UI 样式微调、无行为变化的依赖升级；
- **MINOR**（v1.3.x → v1.4.0）：新功能、新配置项、向后兼容的行为变化；
- **MAJOR**（v1.x → v2.0.0）：破坏性变更——数据库 schema 不兼容迁移、配置格式变更、端口/卷路径/环境变量改名。

版本只增不减，已发的号不复用。

### 不可变：tag 和镜像都不重打

selfupdate 用 `buildinfo.Version` 做版本比较（`backend/internal/selfupdate/service.go`）。镜像发布后发现 bug，**禁止删 tag 重推**——同版本号两份产物，已部署实例会因"版本相同"跳过更新，或更新到内容不符的镜像。正确做法是 bump PATCH 再发（v1.3.3 有 bug → 发 v1.3.4）。

### 镜像 tag 消费约定

CI 每次 push main 产出：`X.Y.Z`/`vX.Y.Z`（不可变）、`<short-sha>`（不可变）、`latest`/`main`（可变，仅表示 main 最新）。部署 compose/命令一律写死精确版本号；`latest` 只用于本机尝鲜，不用于生产。

### 唯一真相源：git tag

版本号不写进代码、不写进配置。`buildinfo.go` 的 `Version = "dev"` 只是本地构建兜底，正式值永远由 CI 从 tag 注入；人工修改该变量属于违规。

### 操作清单

1. 确认 working tree 干净、CI 绿；
2. 一个 commit 只打一个 tag：`git tag -a v1.3.3 -m "release v1.3.3" && git push origin main --follow-tags`；
3. **push 后立即验证 tag 已到达远程**：`git ls-remote --tags origin | grep v1.3.3`，无输出说明 tag 没上去（大概率是打成了轻量 tag），此时先 `git push origin v1.3.3` 补推再走事故恢复，不要干等 CI；
4. 打完查看 Actions run 日志，确认 "Resolve release version" 步骤输出了解析到的 tag。
