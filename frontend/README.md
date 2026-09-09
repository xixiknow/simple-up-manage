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
