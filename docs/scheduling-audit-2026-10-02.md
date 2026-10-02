# 调度算法审核报告(2026-10-02)

- 审核对象:`simple-up-manage` 网关调度算法(代码 + 线上请求日志交叉验证)
- 代码版本:HEAD `62ab20b`(2026-09-30)+ 工作区未提交改动(集中在 admin/intel/前端,不触及调度核心)。日志时段 2026-09-30 00:00 ~ 10-02 00:19 与该版本基本对应。
- 数据材料:`request-logs-2026-09-30_2026-10-02` 导出包(10,519 请求 / 10,936 上游尝试 / 每请求首决策全量候选快照 441,798 行)。分析脚本见 `.zcode/audit-tmp/`(parse.py、analyze1-4.py)。
- 部署形态(Redis / 实例数)未能确认,相关发现的严重级按单实例口径评定,并在条目中标注多实例差异。

## 一、总体结论

调度算法整体设计是健全的:生产运行 `stable_latency` 模式(trace 中 441,798/441,798 候选行确认),「可靠池 → 最快者 → 会话绑定 → 挑战者确认 → 探索」的主干按设计工作——5021 个会话中 94% 全程粘住单一 key,池内选键 96.7% 命中最低 effective cost,熔断/恢复的租约、代数校验、跨进程串行化机制完备,测试覆盖面广。两天 97.92% 成功率、failover 额外救回 268 个请求。

但数据暴露出一个系统性盲区:**熔断与可靠池都以「15 分钟窗口 + 按维度统计」为前提,而线上最大的损失来源——30 秒首 token 超时(FTT)——恰好是稀疏、分散、永不聚簇的失败**,两者错位导致持续劣化的上游可以连续数日留在轮换里。全部 509 次 FTT 尝试各烧满 30 秒,合计约 **4.2 小时**客户端等待,398 个请求(3.8%)被波及。其次是单 key 模型零冗余、stable 模式统计无全模型桶回退(比 legacy 模式倒退)、以及一组配置面板失效旋钮。

## 二、发现清单

| # | 级别 | 类型 | 发现 |
|---|------|------|------|
| 1 | **P1** | 数据+代码 | FTT 30 秒黑洞:稀疏失败永不触发熔断,劣化上游长期留驻轮换 |
| 2 | **P1** | 数据 | 单 key 模型零冗余:claude-opus-5-5 上游半死期间 17 连败,每请求 30s+502 |
| 3 | **P2** | 数据+代码 | 稀疏维度统计失效:低频模型长期在无数据状态下把流量送进 40%-70% 成功率的 key |
| 4 | **P2** | 数据 | 探索流量偏向最差 key:77% 探索打给同一对不可靠 key |
| 5 | **P2** | 代码 | stable 模式忽略 `StickyAnthropic/StickyOpenAI` 配置 |
| 6 | **P2** | 代码 | legacy 冷却路径整体休眠:`CooldownSec/FailureThreshold/FailureWindowSec` 为死配置 |
| 7 | P3 | 数据 | 配置债:6 key 费率漂移、7 key 余额≤0、4 key 残留半开电路,合计占候选评估的 12% |
| 8 | P3 | 代码 | 杂项:FinishCheck 双重钳制、429 无同 key 重试、运算符优先级写法 |
| 9 | P3 | 代码 | 运行时状态进程内存化(多实例部署会失真,待确认部署形态) |

## 三、数据证据

### 1. FTT 30 秒黑洞(P1)

- 全窗口 **509 次** `first_token_timeout` 尝试,每次恰好 30,000ms(`proxyFirstTokenWait`),合计 254.5 分钟客户端纯等待。398 个请求被波及:147 个最终失败、251 个被 failover 救回(但均摊时长 54.8s,是正常请求的 6-10 倍)。
- FTT 按 (key, protocol, model, path, stream) 维度计入熔断,阈值为 60 秒窗口 3 次。实测最大问题 key `mdkj.lol-gpt-0.045`(3 个同名 key_id:2/3/8,attempt 级成功率 0.855-0.907)在 10-01 20:00-00:30 持续每 10 分钟 2-11 次 FTT——**分散在不同 model 维度上,60 秒内从未凑满 3 次,熔断全程未开门**。工作马 `ai.hexuan.cc-gpt-0.03` 也有每小时 13-14 次(约 6%)的背景 FTT。
- 可靠池同样拦不住:可靠判定按请求维度算,大流量维度上整体窗口成功率仍 ≥0.9(mdkj-0.045 首决策快照 mean_succ=0.937、reliable_share=0.74),于是会话继续 `reuse`,新会话继续 `initial_selection` 选中它。
- 关联代码:`gateway.go:763-783`(FTT→`first_token_timeout`,scope=key_model,failOver)、`routinghealth/store.go:265-280`(60s/3 次窗口)、`stable.go:207-237`(可靠池 0.9 阈值)。

### 2. 单 key 模型零冗余(P1)

- `claude-opus-5-5` 在 claude 分组内只有 `supeai.cc-claude-0.695` 一个可用 key。10-01 22:33-23:03 上游半死(穿插成功与 FTT),客户端以 ~90 秒节奏重试:17 个请求**首试 30s FTT → failover 重选无候选(唯一 key 已被本请求排除)→ 502**,每次都是完整 30 秒后失败。
- 熔断依旧不触发:每 90 秒 1 次失败 ≪ 3 次/60 秒;诊断探针与业务熔断隔离(a76ad29),探针再健康也不会把 key 摘出轮换。
- 决策层 35 次 `no_available_route` 中 17 次来自这半小时(gpt-image-2 10 次为权限问题、gpt-6.1-sol 7 次)。
- 建议:关键模型配置 ≥2 个 key;或对「唯一候选/排除后零候选」场景立即触发合成恢复检查与同 key 快速重试,避免让每个客户端请求各付一次 30 秒学费。

### 3. 稀疏维度统计失效(P2)

- `gpt-5.5` 在 mdkj 两个 key 上全天持续 40%-70% attempt 成功率(如 key 2:30 尝试 sr=0.400、13 次 FTT;key 3:71 尝试 sr=0.704),10-01 19:00、20:00、23:00 多个小时 0/3、0/6 全败,但流量始终没有切走。
- 根因:stable 模式 `attemptCandidates`(stable.go:146-205)**严格按 (protocol,model,path,stream) 维度查窗口**,低频模型任意 15 分钟窗口内样本 0-3 个 → 全员 `Reliable=false` → degraded 池;质量公式 `(ok+3.5)/(n+5)` 在 n≤3 时区分度极弱,0 样本(0.700)与 1/3 成功(0.643)几乎同分。而 legacy 模式的 `window()`(band.go:404-419)**有回退到 key 全模型桶的逻辑,stable 模式没有**——这是 3ca52c0 切模式时丢掉的保护。
- 对比:同窗口内大流量维度(gpt-5.6-sol 等)工作正常,可靠池在 2chat-0.06(reliable_share 0.949)与 mdkj-0.1(0.986)上表现优异。
- 建议:stable 模式补上低样本回退(维度样本 < MinSamples 时并入 key 全模型桶),或对低频维度拉长统计窗口。

### 4. 探索偏向最差 key(P2)

- 231 次 `exploration` 中 177 次(77%)落在 mdkj-0.045 的 key 2/3——它们因不可靠被池排除 → 采样少 → `LastSampleAt` 最旧 → 又被探索选中,形成「越差越被采样、采样后仍差」的循环。探索本身成功率 0.974 尚可,但它把最差 key 的失败分摊给了无会话流量。
- 建议:探索候选先过一道成功率/健康门槛(如近期 FTT 计数),或探索失败后对该维度加短冷却。

### 5. stable 模式忽略 sticky 配置(P2,代码)

- `stable.go` 的绑定读写(`withStableState`/`CommitSuccess`,403-437 行)不检查 `stickyEnabled()`;`effectiveRankingMode` 在 stable 路径也从不被调用。设置面板的 `StickyAnthropic/StickyOpenAI/StickyTTLSec` 中,前两个在 stable 模式下完全失效(绑定对一切协议生效),`StickyTTLSec` 名义生效但语义变成了绑定 TTL。
- 数据佐证:OpenAI 流量(占 98%)的会话绑定照常工作(session_source=derived 10,922 次,reuse 5,208 次)。若运维想「关掉 OpenAI 粘性」,改配置不会有效果。

### 6. legacy 冷却路径休眠(P2,代码)

- `gateway.go:425`:`if _, scoped := h.Picker.(picker.CircuitController); !scoped && … { h.applyFailure(...) }`——生产唯一的 picker 是 BandPicker 且恒实现 CircuitController,`applyFailure` 永不执行。连带:
  - `CooldownSec=30`、`FailureThreshold=8`、`FailureWindowSec=60` 三个设置项无任何生效路径(其中 `FailureThreshold` 连引用都没有,纯死配置);
  - `KeyModelCooldown` 表唯一的写入方 `CooldownKeyModel` 不可达,表恒空,`band.go:273` 的 `key_model_cooldown` 软过滤永不命中;
  - `MarkLowBalance` 不可达,余额摘除完全依赖账单刷新 worker + key 级熔断。
- 当前行为是正确的(新机制全面覆盖了旧机制),但配置面板上的失效旋钮会误导运维。建议删除或显式标注「仅 legacy 模式」。

### 7. 配置债(P3)

- `route_rate_drift`:6 个 key 的 rate_multiplier 落在所属分组费率区间外(sub2.congmingai-0.1、oaii-0.18、ihonghu-0.09、biuapi-0.15、mdkj-0.15、1for-0.08),两天被评估 34,636 次全部拒绝——要么调组区间要么移出成员。
- `low_balance`:7 个 key 上游余额≤0(hzapi-0.065 等),19,049 次拒绝。若已充值,检查账单刷新是否失败。
- 4 个死 key(ggboys-1、biuapi-cursor-0.25、aiportx-0.18、ihonghu-0.09)残留 `half_open` 电路贯穿全部 10,519 次首决策——电路永不清理(它们同时被路由组/余额过滤,暂无实际影响,但属于状态垃圾)。
- 提示:`claude-fable-5-1` 的 cache 命中率为 0(claude-opus-5-5 同上游为 0.945),上游可能未回报 cache 用量或该模型未启用 prompt cache,涉及成本公式中 cache 项的公平性,建议核对。

### 8. 杂项(P3,代码)

- `recovery.go:151-159` FinishCheck 退避双重钳制:`min(b*2,300)` 后又 `>120→300`,240 档永远跳到 300(无害,冗余)。
- 429(`cooldown_key_model`)不设 `retrySame`,与 5xx 行为不一致(两天仅 2 次 429,影响面小,可能是有意为之)。
- `gateway.go:910,912` 的 `code == 402 || code == 403 && isQuotaExhaustedBody(body)` 依赖 Go 运算符优先级,易误读,建议加括号。
- RPM/并发限制实际未配置(候选快照中 rpm_limit/max_concurrency 全为 0,provider_concurrency=5000;两天 capacity busy = 0 次),当前未构成约束。

### 9. 运行时状态的部署边界(P3,代码,待确认)

RPM 窗口、inflight、sticky 内存兜底、stable 绑定(Redis 不可用时)均在进程内存(`band.go:37-42`、`stable.go:127-143`)。单实例部署无碍;多实例时各实例视图分裂,RPM 限制失准、绑定互不相认。若有多实例规划,需把绑定与限流全面落到 Redis。

## 四、验证过但健康的机制

- 会话粘性:5021 会话,4701 个(94%)全程单 key;646 次换 key 中大部分由 `reliability_dropped`(117)与 failover(390)驱动——即换 key 基本发生在「该换」的时候。
- 挑战者切换阈值(≥2000ms 且 ≥20%,60s 确认 + 3 新样本)保守得当,两天 0 次 `latency_improved`——没有抖动性切换。
- 成本效率:池内选键 96.7% 命中最低 effective cost;相对全体可选 key 的均值超额 0.0006 费率;892 次 >0.02 的超额集中在 degraded 池(可靠性换价格的合理代价)。探索均超额 0.019,可接受。
- failover:393 个请求用了第 2 次尝试,救回 268 个;同 key 重试(RetryMax=1)触发 109 次;`ErrUnavailable` 竞态、恢复等待(recovery_wait 5s)未见异常残留。
- 熔断状态机本身(租约/代数/Retry-After 优先/跨进程 SQL 串行化)逻辑严密;2chat 在 gpt-5.6-terra 上的合成恢复检查失败退避(446 次 `recovery_check_backoff` 拒绝)表明恢复机器在被正确使用——问题只在「该开门却没开门」的稀疏失败侧。

## 五、建议优先级

1. **FTT 单独计权**:1 次 FTT(30s 实证失败)按 N 次普通失败计入维度熔断,或引入连续失败 EMA;同时把 FTT 从 30s 硬等改为「首 token 探活 + 提前 failover」可大幅压缩 4.2 小时的客户端等待。
2. **单 key 模型保护**:排除后零候选时立即触发恢复检查;关键模型配置冗余 key。
3. **stable 模式补低样本回退**(对齐 legacy 的全模型桶逻辑),修复 gpt-5.5 类稀疏维度的统计盲区。
4. **探索门槛**:近期失败 key 退出探索候选。
5. **配置治理**:删除/标注 legacy 死配置;stable 模式下隐藏或接通 sticky 开关;清理 drift/low_balance/半开残留。
6. 待确认部署形态;若多实例,把绑定与限流落 Redis。

---
*分析脚本:`.zcode/audit-tmp/`(parse.py 解包、analyze1-4.py 统计);数据口径:requests.jsonl 首决策候选快照(10514 请求有 eligible 快照)、attempts.jsonl 全量 10936 行。材料含敏感内容,本报告未引用任何请求正文。*
