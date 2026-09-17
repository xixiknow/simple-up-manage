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

Recovery is independent of the exploration ratio. Each routing pool can select
one real recovery validation per 60 seconds, using an atomic SQL time budget.
Previews never consume it. Only an eligible session binding suppresses validation;
a failed/excluded binding cannot block recovery. Candidates rotate by least recent
business recovery admission, then cooldown deadline and key ID. When no normal
candidate remains, a cooled gate can validate without a budget or synthetic check,
but never bypasses an active lease or a failed check's retry deadline. Successful
recovery does not replace the existing preferred/session binding.

A recovery job wakes every 10 seconds and processes up to two checks per batch.
SQL leases enforce at most two simultaneous checks across instances. Each check
has a 30-second timeout and a 45-second lease; a slow batch delays the next scan.
Checks use the exact failed protocol/model/endpoint/stream dimension with a
synthetic `Reply OK.` prompt and a 256-token limit. They share the existing strict
response validator, never replay customer content, never write business samples,
and never close circuits. Success permits real-request validation and is checked
again after five minutes if no suitable request arrives. Failure retries after
30/60/120/300 seconds; a positive Retry-After can extend this up to 24 hours.
Operator disablement and known exhausted provider balances suppress checks.
Disabling probes still permits budgeted real validation after applicable cooldowns.

New circuits persist their request dimension. Historical hash-only circuits are
resolved from retained attempt dimensions; missing history is never guessed and
still permits real validation. Synthetic and real validation leases exclude one
another, including key-wide gates, and stale owners cannot overwrite new outcomes.
Normal probes remain diagnostic and do not grant recovery readiness.

Candidate explanations and request traces include recovery stage, last check,
next check, check error and last business validation time. Stages distinguish
cooldown, queued/checking, failed check, waiting for a suitable request, preserving
an active session, waiting for the pool budget, and business validation in progress.
The schema additions are additive; rolling back keeps the columns and restores
the former exploration-based recovery policy. Recovery does not rewrite historical
attempts or reset the existing ranking window.

Deep probes rotate the least recently probed dimensions from up to 32 distinct
traffic dimensions observed in the last 24 hours. Requests use a synthetic prompt,
never user content. They use the same JSON/event validator as the gateway, a token
budget of 256, and the configured probe timeout. Without traffic, the configured
probe model and protocol's native chat endpoint are used as diagnostics only.
Three consecutive failed probes in one dimension produce a server-log alert.

### 手动探测参数

提供商页的单 Key、单提供商和全部探测入口先打开配置弹窗，再由“开始探测”发送请求。
对话探测支持搜索已获取的模型或手动输入模型 ID、选择自动/OpenAI/Anthropic 协议、编辑提示词
（默认 `hi`，最多 4000 字符）。留空模型沿用自动选模；显式模型只复用该模型的历史接口维度，
不会被其他历史模型覆盖。参数仅影响此次请求，不修改定时探测配置；旧调用省略参数仍使用原合成提示词。

`POST /keys/:id/probe` 与 `POST /probes/run` 新增可选 `model`、`prompt`、`protocol` 字段，
与 `deep:true` 一起提交。批量调用对范围内每把启用且允许探测的 Key 使用相同参数，
不支持显式协议时返回/累计 `protocol_mismatch` 跳过。关闭探测的 Key 仍跳过，
不写失败样本；轻量探测不接受对话参数。对话探测继续使用 256 token 上限、原超时和响应校验。
弹窗展示探测结果及实际模型/接口，失败保留输入以便调整重试。

### 外部探测与 Key 保护

外部请求经过鉴权后按完整消息结构识别以下固定模板，覆盖 `/v1/responses`、
`/v1/chat/completions` 和 `/v1/messages`：

| 规则编号 | 识别条件 |
| --- | --- |
| `rp_arithmetic` | 完整三数算术提示模板及 `RP_ANSWER=N` 输出约定，允许题目数字及加减乘变化 |
| `example_arithmetic` | 完整加减法模板，固定 `3+5=8`、`12-7=5` 示例及最后一道题 |
| `health_manager` | `gpt-health-manager/YYYYMMDD` 客户端标识、系统 `Reply exactly: ok.`、用户 `Reply exactly: ok`，输出上限 16 同时满足 |

只识别单条纯文本用户消息，允许接口原生系统指令；历史对话、续接标识、非文本内容和工具定义
均排除。按整个模板匹配，不搜索正文关键词，也不依赖模型、生产 Key 编号或调用时间。
Chat Completions 可使用一条前置 system/developer 消息；Anthropic 使用 system 字段；
Responses 支持 input 字符串或单条 user 消息。纯文本块按顺序拼接。

`probe_ping` 工具探测、`hi`、身份问询及成套能力测试继续按普通业务处理，
不因探测开关过滤。固定规则无管理配置入口。

命中请求共用 Key 的 `probe_enabled` 开关，只在原路由范围内选择允许探测的 Key。
绑定分组不跨组，未绑定请求在原有全局池中筛选；粘性、同 Key 重试及故障切换遵守相同限制。
每次发送前复核开关，刚关闭的 Key 从本次候选中移除，本地跳过不消耗发送预算、返还该次 RPM 预留、不记录为上游失败；
已发出的请求不追溯取消。开关排除全部候选时返回 HTTP 503、`probe_disabled`；
其他路由不可用原因沿用原错误。

这些仍是外部业务调用：鉴权、限流、额度扣减、费用、业务指标及实际发送后的调度反馈沿用原逻辑，
不把 source 改为平台测试。请求记录增量增加可空索引字段 `external_probe_rule`，
列表和详情返回该字段并显示中文标签；`GET /api/v1/admin/request-logs` 接受同名参数，
`any` 筛选全部已识别外部探测，或传入上述规则编号。其他非空值返回 400。
历史记录不回填，也不将未标记等同于已确认非探测。

生产离线样本（2026-09-16）中三类分别为 979、178、15 条，共 1,172 条；
5 条 `probe_ping` 不纳入。测试只保存脱敏最小模板，不提交原始生产请求或凭据。
离线回放可设置 `EXTERNAL_PROBE_SAMPLE_DIR` 后执行
`go test ./internal/externalprobe -run TestProductionSamples -v`，仅读取文件，不连接生产。
样本存在大正文截断，该结果不代表完整探测覆盖率。

部署时由现有 AutoMigrate 增加可空字段与索引；观察探测标签、正常业务误判及 `probe_disabled`
错误。应用版本回滚时保留字段即可，旧版本忽略此字段。

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
