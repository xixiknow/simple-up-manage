# 调度算法优化方案(2026-10-02 修订版,已批准)

依据:[scheduling-audit-2026-10-02.md](./scheduling-audit-2026-10-02.md) 审核结论与运营反馈(「key 实际可用却被 no healthy key 锁死」「熔断是否太敏感」「恢复是否太慢」)。通俗版说明见 [scheduling-explained.md](./scheduling-explained.md)。

核心目标:**让坏消息传得快(该锁的锁),好消息传得也快(该放的放)。**

## 一、熔断健康门重设计(响应「太敏感 / 恢复太慢」)

### 诊断结论(代码 + 数据核实)

「no healthy key in bound route groups 但 key 实际可用」由三个机制叠加造成:

1. **恢复检查自带 30 秒超时**(`ops/recovery.go` 两处),而检查用的是出错维度的真实模型——推理模型 TTFT p95 达 22~27 秒,检查自己经常超时,被误判为「key 还是坏的」。
2. 检查失败退避 30→60→120→300 秒,期间**真实流量恢复验证也被拒绝**(`routinghealth/store.go` AdmitRequest 要求 `CheckOK` 或 `NextCheckAt` 已过)——一个假探针把真流量也否决了。
3. 开启探针的 key 完全依赖合成检查(`picker/routing_health.go`:`waiting_check` 不允许真流量验证);真流量预算 1 次/分钟只惠及未开探针的 key。

另有两处阈值错配:
- 固定阈值「60 秒内 3 次」对热点账号(8 单/分钟)相当于 6% 失败率即开门——太敏感;
- 对冷维度(90 秒 1 单)则永远凑不齐 3 次——死上游永不摘(opus-5-5 事故)。

数据佐证:2chat 在 gpt-5.6-terra 上 448 次请求目睹 `recovery_check_backoff`;17 连败的 opus-5-5 事故窗口。

### 改动

| 编号 | 改动 | 位置 |
|---|---|---|
| R1a | 恢复检查超时 30s → 60s(新配置 `recovery_check_timeout_sec`,默认 60) | ops/recovery.go |
| R1b | 检查失败退避:首次失败不退避(立即重试),后续 30→60→90 封顶(原 300);顺带删除 `>120→300` 双重钳制 | routinghealth/recovery.go FinishCheck |
| R1c | 解除连坐:检查失败只推迟合成检查,**不再阻止真流量恢复验证**(AdmitRequest 移除 `!CheckOK && NextCheckAt 未到` 条件;ClaimCheck 保留) | routinghealth/store.go |
| R2a | 真流量恢复预算 1 次/分/分组 → `recovery_budget_per_min`(默认 3)次/分 | routinghealth/recovery.go RecoverySlot |
| R2b | 分组内没有健康候选时,`waiting_check`/`check_failed` 的独苗也允许真流量验证(不再死等合成检查;`checking`/`validating` 仍排除) | picker/routing_health.go applyRoutingHealth |
| R3a | 维度开门阈值随流量:`max(基础阈值3, min(8, 60秒内尝试数×circuit_rate_factor))`,新配置 `circuit_rate_factor`(默认 0.15,0=关闭回退旧行为) | routinghealth/store.go Observe |
| R3b | 成功率兜底冷维度:60 秒窗口尝试 ≥8 且成功率 <0.6 → 开门 | 同上 |
| R3c | FTT 一次记 `ftt_failure_weight`(默认 2)次失败:两次慢失败或一次慢失败+一次普通失败即开门;单次意外慢失败不开门(避免加重误锁,实施时从 3 调低) | 同上 |

新状态字段:`RoutingCircuit.AttemptTimes`(JSON,60 秒窗口内全部非中性尝试时间戳,封顶 120 条),支撑 R3a/R3b。

## 二、「有 key 却报无路由」的修正说明

原计划设想「池内无人有 ≥5 延迟样本时 `fastestCandidate` 返回 -1 → 503」。**实施前复核代码推翻了这一假设**:`fastestCandidate` 中定义最快值的成员永远不会被自己的过滤器剔除,因此只要存在合格候选,它必然返回结果;-1 只在「零合格候选」时出现。数据中 30 个 no-route 请求的「30 行合格候选」全部来自**第一次决策**(当时还有候选),真正报无路由的是失败后被排除/熔断的**后续决策**。结论:该场景的正确修复是下面的「最后一搏」,不再需要单独的池空降级逻辑。

## 三、重试与换人

- failover/同 key 重试的首 token 等待 30s → `failover_first_token_wait_sec`(默认 10s,仅非首试生效)。
- **最后一搏**:failover 后零候选且最后一次失败属长等待类(first_token_timeout / transport_failure / upstream_timeout)时,清空本次请求的排除表再重选一次(等效于给最后失败的 key 一次 10s 快速自证);整个请求最多一次,受 300s 总 deadline 约束。实施修正:快速 5xx(request_scope_failure)与 invalid_response 不触发——它们已有同 key 快速重试兜底,重放只会拖慢失败(测试 TestGatewayCountsFailedRequestsNotRetries 暴露了这一点)。

## 四、挑选决策

- `attemptCandidates` 低样本回退:维度样本 < MinSamples 时,改用该 key「同 protocol/path/stream、不限模型」的总账统计(取样本更多者),修复低频模型永远无成绩单的问题(对齐 legacy 模式 `window()` 的回退,3ca52c0 迁移时遗失)。
- 探索门槛:已有样本 ≥3 且成功率 <0.8 的候选不参与探索试单,试单机会转给真正无数据的 key。

## 五、配置与卫生

- 新配置项全部进 `SchedulerSettings` + 管理面板(ftt_failure_weight、circuit_rate_factor、recovery_budget_per_min、recovery_check_timeout_sec、failover_first_token_wait_sec),可在线回滚。
- legacy 死参数(`FailureThreshold`、`CooldownSec`、`FailureWindowSec`)与 sticky 开关语义问题:另行小步处理,不与本批混合。

## 验收指标

| 指标 | 基线(09-30~10-02) | 目标 |
|---|---|---|
| 好 key 冤锁时长(开门→回归) | 最长 300s+循环 | ≤60s |
| recovery_check_backoff 目睹次数 / 2 天 | 448 | <50 |
| FTT 客户端总等待 | 254.5 min / 2 天 | 降 50%+ |
| 冷维度死上游摘除时间 | 永不 | ≤2 次失败 |
| 成功率 / 单 key 会话占比 / 池内成本最优占比 | 97.9% / 94% / 96.7% | 不回退 |

## 实施顺序

1. R1 + R2(恢复链路,~60 行)
2. 三(重试与换人,~50 行)
3. R3 + 四(阈值/记重/回退/探索,~80 行 + 1 个状态字段)
4. 五(前端暴露配置)
