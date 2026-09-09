# 将现有 sub2api 接到 simple-up-manage

本平台是南向 LLM 网关：现有 sub2api 继续对人侧（用户 Key、钱包、分组售价），本服务作为它的**唯一上游**，再向下管理多家上游（多为其他 sub2api）并透明转发。

```
终端用户 → 现有 sub2api → simple-up-manage → 上游 sub2api / OpenAI 兼容 / Anthropic 兼容
```

本期不做多因子调度。转发选路是：第一个 **enabled** 且上游 **enabled**、`protocols` 匹配请求协议的 PlatformKey（按 id 升序）。价格阈值和余额不足只做展示，不拦截转发。

## 1. 在本平台创建消费 Key

管理接口：`POST /api/v1/admin/consumer-keys`（`Authorization: Bearer <ADMIN_TOKEN>`）。

```json
{ "name": "prod-sub2api", "quota_usd": 0, "rpm": 0 }
```

响应里的 `data.key`（`sk-...`）**只出现一次**，填到现有 sub2api 的 Account Credential。`quota_usd = 0` 表示不限额。

## 2. 现有 sub2api 上新增 Account（不改源码）

在现有实例里新增 1～2 个 `api_key` 类型 Account，把原来散落的多家上游 Account 迁入本平台后停用。

| 协议 | Account 平台 | `base_url` | 实际打到的路径 |
|------|----------------|------------|----------------|
| Anthropic | `anthropic` | `https://<this-host>` | `POST /v1/messages` |
| OpenAI | `openai` | `https://<this-host>` | `POST /v1/chat/completions`、`POST /v1/responses`、`POST /v1/images/{generations,edits,variations}` |

Credential 使用上一步下发的消费 Key。鉴权头：`Authorization: Bearer sk-...`（Anthropic 也可用 `x-api-key`）。

本平台同时提供：

- `GET /v1/models` — 合并各上游最近一次轻探拿到的模型 id
- `GET /v1/usage` — 消费 Key 的额度/用量，避免现有侧用量探测报错

`base_url` 不要带 `/v1` 后缀；本服务会把路径接到 host 上。若误填 `https://host/v1`，网关会去掉尾部 `/v1` 再拼接，避免 `/v1/v1/messages`。

## 3. 在本平台登记上游

对每一家真实上游（多为其他 sub2api 部署）：

1. `POST /api/v1/admin/upstreams`
   - `kind`: `sub2api` | `new_api` | `openai_compat` | `anthropic_compat`
   - `protocols`: `["openai"]`、`["anthropic"]` 或两者
   - `base_url`: 上游根地址，例如 `https://other-sub2api.example`
2. `POST /api/v1/admin/upstreams/:id/keys` 写入上游平台 Key（落库 AES-GCM 加密，接口永不回传明文，只给 `key_preview`）。倍率 `rate_multiplier` 是 Key 自身属性（默认 1）；new-api 的 Key 可填 `billing_group`（令牌在 new-api 上所属分组名，空则 `default`）。
3. 对 `kind=sub2api` 的 Key：
   - `POST /api/v1/admin/keys/:id/refresh-balance` → `GET {base_url}/v1/usage`（解析 `quota.remaining` / `remaining` / `balance`）
   - `POST /api/v1/admin/keys/:id/refresh-billing` → `GET {base_url}/v1/sub2api/billing` 的 `effective_rate_multiplier`，写回 Key 的 `rate_multiplier`。404 会标记 unsupported 并退避 24h。
   - `POST /api/v1/admin/keys/:id/probe` 轻探 `GET /v1/models`（失败再试 `/v1/usage`）；`{"deep": true}` 按调度页各厂商探测模型发极小 messages/completions（优先打 Key 已有列表里能命中的最便宜探测模型）。
   对 `kind=new_api` 的 Key（平台 Key 填 new-api 的令牌 `sk-...`）：
   - `refresh-balance` → `GET /v1/dashboard/billing/subscription` + `/usage`（美元；`hard_limit_usd >= 1e8` 视为不限额，清空余额不再判低余额），失败退回 `GET /api/usage/token/`（原始额度 / 500000）。
   - `refresh-billing` → `GET /api/pricing`（new-api 默认公开）取 `group_ratio`。**在 Key 上填写令牌所属的 new-api 分组名**（如 `default` / `vip`，空则 `default`），命中的倍率写入 Key 的 `rate_multiplier`；接口关闭（401/403/404）时标记 unsupported 并退避 24h。
   - 令牌额度耗尽时 new-api 返回 `403 insufficient_user_quota`，网关会把该 Key 标为低余额并切换下一把 Key。
4. `POST /api/v1/admin/keys/:id/fetch-models`（或 `POST /api/v1/admin/upstreams/:id/fetch-models` 批量）拉取上游 `GET /v1/models` 写入 `last_models`。调度时若该 Key 的模型列表非空且不含请求的 `model`，该 Key 会被跳过（`model_not_supported`）；列表为空不过滤。可在「调度」页关闭 `filter_by_models`。
5. 路由分组可设 `rate_min` / `rate_max` 参考区间。成员 Key 的倍率漂出区间时会标红，调度时跳过（`route_rate_drift`）。

后台定时（可配）：余额 1 分钟、billing 1 分钟、轻探 1 分钟。

## 4. 健康检查

`GET https://<this-host>/health` → `{"status":"ok"}`。

现有 sub2api 把本平台当上游探测时，用消费 Key 调 `/v1/models` 和 `/v1/usage` 即可。

## 5. 迁移顺序建议

1. 本平台先配好上游 + PlatformKey，手动 probe / refresh-balance 确认健康。
2. 创建消费 Key。
3. 现有 sub2api 增加指向本平台的 Account，小流量验证流式（SSE）与非流式。
4. 确认请求记录页有成功日志后，再停用旧的多家上游 Account。
