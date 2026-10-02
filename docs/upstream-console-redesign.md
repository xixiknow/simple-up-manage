# 提供商页重设计：master-detail 与批量管理

日期：2026-10-01。替代 `console-visual-plan.md` 中「提供商页为单一大表（rowSpan 合并 + 提供商分页）」的旧布局。

## 背景

提供商数量多、单提供商 Key 数量大时，旧页面不可用：所有可见提供商的 Key 平铺成行（151 把 Key 就是 151 行）、提供商翻页查找、15s 轮询拉全系统 Key 倍率、Key 无搜索/筛选/排序、无批量操作。

## 信息架构

- **左栏（ProviderRail）**：全部提供商不再分页，rail 内滚动；搜索（名称/地址/备注，客户端）、快捷筛选 chips（全部/异常/低余额/停用，带实时计数）；每行显示名称、启停状态、异常徽标、健康、Key 数、余额（低余额红标）、类型与余额更新时间；异常优先排序，静默轮询保持顺序稳定。
- **右栏（ProviderDetail）**：选中提供商后展示
  - 摘要卡：Key 总数、异常数（失败/慢响应/冷却拆分）、余额+更新时间、最近请求；
  - Key 表格：**服务端**分页（20/50/100）、`search`（名称/标识/预览）、`status`、`health_status` 筛选、`sort`/`order` 排序；
  - 勾选后浮出批量操作条：启用/停用（`/keys/batch-status`）、探测（`/probes/run` 的 `key_ids`）、同步倍率（`/keys/batch-billing`）、加入/移出路由分组（`/route-groups/:id/keys/batch`）、删除（`/keys/batch-delete`）。
- **手机端**：rail 全屏，点提供商开全屏 `UiDrawer` 详情（返回按钮关闭），Key 表格沿用卡片模式。
- **深链**：`/upstreams?id=N` 直接选中提供商（仪表盘「查看」按钮跳转即生效），选中状态写入 `?id=`。

## 数据流变化

- 提供商列表仍全量拉取（`include_summary=true`），但**不再拉取全系统 Key 倍率**；倍率变动提醒收窄为「当前选中提供商」（详情页每次加载 diff `view=rates&upstream_id=N`），全局变动继续走顶栏消息收件箱。
- Key 编辑弹窗抽为 `KeyFormModal.vue`；`busy` 单令牌改为按 `op:id` 的 Set，行级操作互不干扰。
- 手动刷新按「异常优先」重排；15s 静默轮询保持既有顺序。

## 后端新增（internal/handler）

- `GET /keys`、`GET /upstreams/:id/keys`：`search`（LOWER name/name_tag/key_preview LIKE）、`status`、`health_status`、`sort`（白名单 id/name/rate_multiplier/health_status/last_request_at）+ `order`（asc/desc），count 同 filter。
- `POST /keys/batch-status` `{ids, status}`：批量启停，逐 Key 刷新健康并重载 picker。
- `POST /keys/batch-delete` `{ids}`：事务内删除并清理路由分组关联。
- `POST /keys/batch-billing` `{ids}`：顺序同步倍率，逐 Key 报告失败原因（上游类型不支持/上游错误/backoff）。
- `POST /probes/run` body 新增 `key_ids`（≤200）：批量探测指定 Key。
- `GET /upstreams` 新增 `search` 参数。

## 测试

- `keys_batch_test.go`：筛选/排序/校验拒绝 + 批量启停/删除（含分组关联清理）。
- `ui_regression.py`：rail 150 家、详情按提供商加载、服务端 search、倍率提醒选中视角、手动同步、批量启停写入断言、Provider 150 分 8 页、`?id=` 深链、手机抽屉卡片、长 Key 名 toast 防溢出。
- `visual_regression.py`：390 档上游页 = 点提供商 → 抽屉 `.table-cards`；Key 编辑弹窗从详情行打开。
