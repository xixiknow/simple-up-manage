# 南向提供商管理 · 管理控制台

Vue 3 + Vite + TypeScript + Vue Router + Pinia + Naive UI 前端。对接 `/api/v1/admin/*`，通过 Vite 代理到 `http://localhost:8080`。

## 运行

```bash
cd frontend
npm i
npm run dev
```

浏览器打开提示的本地地址（默认 `http://localhost:5173`）。登录页填写后端 `ADMIN_TOKEN`，会写入 `localStorage`，请求头为 `Authorization: Bearer <token>`。

生产构建：

```bash
npm run build
npm run preview
```

## 页面

| 路由 | 说明 |
| --- | --- |
| `/login` | 管理员 Token 登录 |
| `/upstreams` | 提供商 CRUD；行内管理 Key（倍率、探测、刷余额、同步倍率） |
| `/prices` | 价格阈值 CRUD + Key 倍率总览 |
| `/route-groups` | 路由分组；参考倍率区间，漂出则标红并调度跳过 |
| `/scheduler` | 调度权重与 Explain；含 `route_rate_drift` |
| `/logs` | 请求记录筛选与分页 |
| `/consumers` | 消费 Key CRUD；明文仅创建后弹窗展示一次 |

## 约定

- 列表响应 `{ ok, data: { items, total, page, page_size } }`；若后端直接返回数组，前端会归一化。
- 提供商 Key 列表只展示 `key_preview`，`api_key` 仅出现在创建/编辑表单。
- `401` 会清 token 并跳转登录页。

## 提供商与请求记录

- 提供商按一家一组展示 Key，名称和共享余额跨行合并；每页 10 / 20 / 50 家。搜索、快捷筛选和手动刷新重新计算异常优先顺序，自动刷新保留当前顺序。
- 提供商汇总通过 `include_summary=true` 获取；Key 使用 `upstream_ids` 批量过滤并读完接口分页。筛选选项通过 `/keys?view=options` 获取轻量数据。
- 提供商页面每次刷新通过 `/keys?view=rates` 比较所有 Key 的倍率，包括未显示在当前页的 Key；降价显示绿色消息，涨价显示警告，包含提供商、Key 和变动前后倍率。手动同步也会提醒，同一变化去重；首次读取只建立基线。提醒跟随页面的自动刷新开关，关闭页面后不保留变化历史。
- 请求类型以原始请求的 `stream` 为准，历史证据不足时显示“未知”。请求结束后耗时固定，打开中的明细独立轮询至结束。
- 请求记录使用服务端分页；`snapshot_id` 限制历史页的最大 ID，`snapshot_at` 限制成功/失败筛选的完成时间。查询、重置和手动刷新建立新快照，第一页实时刷新接收新记录。

## 浏览器回归

安装 Python Playwright 及 Chromium 后，针对运行中的本地服务执行：

```powershell
python tests/ui_regression.py --url http://127.0.0.1:8081
```

脚本在浏览器中拦截管理接口，使用 150 家提供商、单家 151 把 Key 和 53 条请求记录；不写入服务端数据。截图输出到 `frontend/data/ui-regression/`。
