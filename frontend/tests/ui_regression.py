"""Browser regressions against a running build, with isolated admin API fixtures."""
import argparse
import json
import re
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import expect, sync_playwright


def run(base_url, output, channel=None):
    output.mkdir(parents=True, exist_ok=True)
    now = datetime.now(timezone.utc)
    providers = []
    keys = []
    pulses = [dict(start=(now - timedelta(minutes=60-i)).isoformat(), state="bad" if i % 17 == 0 else "ok", ok=0 if i % 17 == 0 else 3, fail=1 if i % 17 == 0 else 0, last_latency_ms=1300) for i in range(60)]
    for i in range(1, 151):
        count = 151 if i == 150 else 2
        health = "down" if i in (1, 3) else "healthy"
        name = f"Provider {i:03}"
        providers.append(dict(id=i, name=name, base_url=f"https://provider-{i}.example.com", kind="sub2api", protocols=["openai", "anthropic"], status="enabled", health_status=health, concurrency=10, last_balance=12.5 if i == 1 else 240.5, last_balance_at=now.isoformat(), summary=dict(key_count=count, abnormal_count=count if health == "down" else 0, health_counts={health: count})))
        for j in range(count):
            keys.append(dict(id=len(keys)+1, upstream_id=i, upstream_name=name, upstream_kind="sub2api", name=f"{name}-key-{j+1}", name_tag=f"key-{j+1}", key_preview="sk-...abc", health_status=health, status="enabled", rate_multiplier=0.3+j/100, channel_score=35 if health == "down" else 93, health_pulse=pulses, route_groups=[], last_models=["gpt-test"], models_count=1, cache_rate=0.6, cache_samples=10))
    logs = [dict(id=i, request_id=f"req-{i}", upstream_id=1, upstream_name="Provider 001", protocol="openai", model=f"model-{i}", path="/v1/chat/completions", status_code=200, success=True, input_tokens=100, output_tokens=30, cache_read_tokens=0, cache_creation_tokens=0, ttft_ms=500, duration_ms=1500, cost_usd=0.001, in_flight=False, stream=i % 2 == 1, stream_known=i != 1, created_at=(now-timedelta(seconds=54-i)).isoformat()) for i in range(1, 54)]
    logs[-1].update(in_flight=True, success=False, created_at=now.isoformat())
    state = dict(requests=[], writes=[], complete=False, detail_calls=0, delay_page_one=False, held_route=None, fail_provider_once=True, hold_sync=False, held_sync=None, sync_rate=0.123456)

    def api(route):
        parsed = urlparse(route.request.url)
        q = parse_qs(parsed.query)
        name = parsed.path.removeprefix("/api/v1/admin")
        page = int(q.get("page", [1])[0])
        size = min(100, int(q.get("page_size", [20])[0]))
        if route.request.method in ("POST", "PUT", "DELETE"):
            payload = route.request.post_data_json or {}
            state["writes"].append((name, payload))
            if name == "/keys/batch-status":
                ids = set(payload["ids"])
                for key in keys:
                    if key["id"] in ids:
                        key["status"] = payload["status"]
                route.fulfill(json=dict(ok=True, data=dict(updated=len(ids))))
                return
            if name == "/keys/batch-delete":
                ids = set(payload["ids"])
                keys[:] = [key for key in keys if key["id"] not in ids]
                route.fulfill(json=dict(ok=True, data=dict(deleted=len(ids))))
                return
            if name == "/probes/run":
                route.fulfill(json=dict(ok=True, data=dict(ok=len(payload.get("key_ids", [])), failed=0, skipped=0, message="成功")))
                return
        state["requests"].append((name, q))
        key_pattern = re.match(r"^/upstreams/(\d+)/keys$", name)
        if key_pattern:
            uid = int(key_pattern.group(1))
            selected = [key for key in keys if key["upstream_id"] == uid]
            if q.get("search"):
                needle = q["search"][0].lower()
                selected = [key for key in selected if needle in key["name"].lower() or needle in key["key_preview"].lower()]
            if q.get("status"):
                selected = [key for key in selected if key["status"] == q["status"][0]]
            if q.get("health_status"):
                selected = [key for key in selected if key["health_status"] == q["health_status"][0]]
            if q.get("sort") == ["rate_multiplier"]:
                selected = sorted(selected, key=lambda k: k["rate_multiplier"], reverse=q.get("order") == ["desc"])
            data = dict(items=selected[(page-1)*size:page*size], total=len(selected), page=page, page_size=size)
        elif name == "/upstreams":
            if state["fail_provider_once"]:
                state["fail_provider_once"] = False
                route.fulfill(status=503, json=dict(ok=False, error=dict(message="fixture initial failure")))
                return
            data = dict(items=providers[(page-1)*size:page*size], total=len(providers), page=page, page_size=size)
        elif name == "/keys":
            selected = keys
            if "upstream_ids" in q:
                ids = set(map(int, q["upstream_ids"][0].split(",")))
                selected = [key for key in selected if key["upstream_id"] in ids]
            if "upstream_id" in q:
                uid = int(q["upstream_id"][0])
                selected = [key for key in selected if key["upstream_id"] == uid]
            if q.get("view") == ["rates"]:
                selected = [{field: key[field] for field in ("id", "upstream_id", "upstream_name", "name", "rate_multiplier")} for key in selected]
            data = dict(items=selected[(page-1)*size:page*size], total=len(selected), page=page, page_size=size)
        elif name.endswith("/refresh-billing"):
            key = next(key for key in keys if key["id"] == int(name.split("/")[2]))
            key["rate_multiplier"] = state["sync_rate"]
            if state["hold_sync"]:
                state["held_sync"] = route
                return
            data = key
        elif name == "/route-groups":
            data = dict(items=[])
        elif name == "/request-logs":
            if page == 1 and state["delay_page_one"]:
                state["delay_page_one"] = False
                state["held_route"] = route
                return
            selected = list(reversed(logs))
            if q.get("model"):
                selected = [row for row in selected if row["model"] == q["model"][0]]
            data = dict(items=selected[(page-1)*size:page*size], total=len(selected), page=page, page_size=size, snapshot_id=53, snapshot_at=now.isoformat())
        elif name.startswith("/request-logs/"):
            state["detail_calls"] += 1
            row = dict(logs[int(name.rsplit("/", 1)[1])-1])
            if state["complete"]:
                row.update(in_flight=False, success=True, duration_ms=1750)
            data = dict(**row, request_body='{"stream":true}', response_body='data: [DONE]\n\n', request_headers='{}', response_headers='{"Content-Type":"text/event-stream"}')
        else:
            route.fulfill(status=404, json=dict(ok=False, error=dict(message=name)))
            return
        route.fulfill(json=dict(ok=True, data=data))

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True, channel=channel)
        context = browser.new_context(viewport=dict(width=1600, height=1000))
        context.add_init_script("localStorage.setItem('admin_token', 'fixture')")
        context.route("**/api/v1/admin/**", api)
        page = context.new_page()
        expect.set_options(timeout=15000)
        errors = []
        page.on("pageerror", lambda error: errors.append(str(error)))
        page.clock.install()
        page.goto(base_url + "/upstreams")
        page.wait_for_load_state("networkidle")
        expect(page.get_by_text("fixture initial failure", exact=True)).to_be_visible()
        page.clock.fast_forward(16000)
        expect(page.locator(".p-row")).to_have_count(150)
        expect(page.locator(".p-row .p-name").first).to_have_text("Provider 001")
        page.wait_for_timeout(300)
        page.screenshot(path=str(output / "providers-desktop.png"), full_page=True)
        print("provider desktop: rail lists all 150 providers, abnormal first")

        # 选中提供商：右侧详情按服务端分页加载它的 Key
        page.locator(".p-row").filter(has_text="Provider 001").click()
        expect(page.locator(".detail-host .ui-data-table tbody tr")).to_have_count(2)
        assert page.locator(".detail-host .pulse-cell").count() == 120
        print("provider detail: keys load per provider with pulse cells")

        # 服务端 Key 搜索：请求带 search 参数，结果被过滤
        page.get_by_placeholder("搜索 Key 名称 / 标识 / 预览").fill("key-2")
        page.wait_for_load_state("networkidle")
        expect(page.locator(".detail-host .ui-data-table tbody tr")).to_have_count(1)
        assert any(name.endswith("/keys") and q.get("search") == ["key-2"] for name, q in state["requests"])
        page.get_by_placeholder("搜索 Key 名称 / 标识 / 预览").fill("")
        page.wait_for_load_state("networkidle")
        expect(page.locator(".detail-host .ui-data-table tbody tr")).to_have_count(2)

        # 倍率提醒收窄到当前选中提供商；无关提供商变动不提醒
        # （Provider 001 已选中，倍率快照在前面的搜索步骤中已完成预热）
        messages = page.locator(".ui-message")
        expect(messages).to_have_count(0)
        keys[0]["rate_multiplier"] = 0.6
        page.get_by_label("刷新列表", exact=True).click()
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(1)
        expect(messages.filter(has_text="倍率涨价")).to_contain_text("Provider 001-key-1: ×0.3 → ×0.6")
        page.clock.fast_forward(16000)
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(0)
        page.wait_for_timeout(300)
        page.screenshot(path=str(output / "rate-alerts-desktop.png"), full_page=True)
        print("price alerts: scoped to the selected provider, quiet when unchanged")

        # 手动同步倍率：放行挂起的响应后按新倍率提醒一次；无变化时静默
        state["hold_sync"] = True
        page.locator(".detail-host .ui-data-table tbody tr").filter(has=page.locator('.key-name[title="Provider 001-key-1"]')).get_by_label("Key 操作", exact=True).click()
        page.get_by_text("同步倍率", exact=True).click()
        for _ in range(50):
            if state["held_sync"] is not None:
                break
            page.wait_for_timeout(100)
        assert state["held_sync"] is not None
        state["held_sync"].fulfill(json=dict(ok=True, data=keys[0]))
        state["hold_sync"] = False
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(1)
        expect(messages).to_contain_text("倍率降价：Provider 001 / Provider 001-key-1: ×0.6 → ×0.123456")
        page.clock.fast_forward(16000)
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(0)
        page.locator(".detail-host .ui-data-table tbody tr").filter(has=page.locator('.key-name[title="Provider 001-key-1"]')).get_by_label("Key 操作", exact=True).click()
        page.get_by_text("同步倍率", exact=True).click()
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(1)
        expect(messages).to_contain_text("已同步倍率：×0.123456")
        page.clock.fast_forward(16000)
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(0)
        print("price alerts: manual sync notifies once; unchanged sync quiet")

        # 批量启停：勾选后走 /keys/batch-status，fixture 真实应用状态
        boxes = page.locator('.detail-host .ui-data-table tbody input[type=checkbox][aria-label^="选择"]')
        boxes.nth(0).check()
        boxes.nth(1).check()
        batch_bar = page.locator(".batch-bar")
        expect(batch_bar).to_be_visible()
        batch_bar.get_by_role("button", name="停用", exact=True).click()
        assert any(name == "/keys/batch-status" and body["status"] == "disabled" and len(body["ids"]) == 2 for name, body in state["writes"])
        assert keys[0]["status"] == "disabled" and keys[1]["status"] == "disabled"
        expect(batch_bar).not_to_be_visible()
        boxes = page.locator('.detail-host .ui-data-table tbody input[type=checkbox][aria-label^="选择"]')
        boxes.nth(0).check()
        boxes.nth(1).check()
        batch_bar.get_by_role("button", name="启用", exact=True).click()
        assert any(name == "/keys/batch-status" and body["status"] == "enabled" for name, body in state["writes"])
        assert keys[0]["status"] == "enabled" and keys[1]["status"] == "enabled"
        page.screenshot(path=str(output / "provider-batch.png"), full_page=True)
        print("provider batch: enable/disable via /keys/batch-status")

        # 单提供商 151 把 Key：详情独立分页
        page.locator(".p-row").filter(has_text="Provider 150").click()
        expect(page.locator(".detail-host .ui-data-table tbody tr")).to_have_count(20)
        page.locator(".k-pager .ui-pagination-item").filter(has_text="2").first.click()
        page.wait_for_load_state("networkidle")
        expect(page.locator(".detail-host .key-name").first).to_have_text("Provider 150-key-21")
        assert any(name.endswith("/keys") and q.get("page") == ["2"] for name, q in state["requests"])
        print("provider detail: provider 150 paginates its 151 keys")

        # 仪表盘深链：?id= 直接选中提供商
        page.goto(base_url + "/upstreams?id=150")
        page.wait_for_load_state("networkidle")
        expect(page.locator(".detail-host .key-name").first).to_have_text("Provider 150-key-1")
        print("provider deep link: ?id selects the provider")

        # 静默轮询期间列表顺序保持稳定
        providers[1]["summary"]["abnormal_count"] = 2
        before = page.locator(".p-row .p-name").all_text_contents()
        page.clock.fast_forward(16000)
        page.wait_for_timeout(100)
        assert page.locator(".p-row .p-name").all_text_contents() == before
        print("provider refresh: rail ordering stays stable")

        page.set_viewport_size(dict(width=390, height=844))
        page.locator('.page-head h2').scroll_into_view_if_needed()
        page.wait_for_timeout(200)
        expect(page.locator(".p-row").first).to_be_visible()
        assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
        page.screenshot(path=str(output / "providers-mobile.png"), full_page=True)
        print("provider mobile: rail list fits without page overflow")

        # 手机端点提供商 → 全屏抽屉详情 → Key 卡片
        page.locator(".p-row").filter(has_text="Provider 001").click()
        expect(page.locator(".ui-drawer .table-cards")).to_be_visible()
        expect(page.locator(".ui-drawer .table-card").first).to_contain_text("Provider 001")
        assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
        page.screenshot(path=str(output / "providers-mobile-detail.png"), full_page=True)
        print("provider mobile: full-screen detail renders key cards")

        # 长 Key 名倍率提醒 toast 不溢出：桌面端确定性触发，再缩到 390 检查几何
        page.set_viewport_size(dict(width=1600, height=1000))
        page.wait_for_timeout(200)
        with page.expect_response(lambda r: "view=rates" in r.url and "upstream_id=150&" in r.url):
            page.locator(".p-row").filter(has_text="Provider 150").click()
        expect(page.locator(".detail-host .ui-data-table tbody tr").first).to_be_visible()
        keys[-1]["name"] = "Provider 150-" + "LongKeyName" * 12
        keys[-1]["rate_multiplier"] = 0.7
        page.get_by_label("刷新列表", exact=True).click()
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(1)
        expect(messages.filter(has_text="倍率降价")).to_contain_text("×1.8 → ×0.7")
        page.set_viewport_size(dict(width=390, height=844))
        page.wait_for_timeout(350)
        assert messages.evaluate_all("els => els.every(el => { const r = el.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth && el.scrollWidth <= el.clientWidth })")
        page.screenshot(path=str(output / "rate-alerts-mobile.png"), full_page=True)
        print("price alerts: long key name fits on mobile")

        page.set_viewport_size(dict(width=1600, height=1000))
        page.goto(base_url + "/logs")
        page.wait_for_load_state("networkidle")
        expect(page.locator(".ui-data-table tbody tr")).to_have_count(20)
        pagination = page.locator(".ui-data-table .ui-pagination")
        state["delay_page_one"] = True
        page.clock.fast_forward(4500)
        page.wait_for_timeout(100)
        assert state["held_route"] is not None
        pagination.locator(".ui-pagination-item").filter(has_text="2").first.click()
        expect(page.get_by_text("model-33", exact=True)).to_be_visible()
        state["held_route"].fulfill(json=dict(ok=True, data=dict(items=list(reversed(logs))[:20], total=53, page=1, page_size=20, snapshot_id=53, snapshot_at=now.isoformat())))
        page.wait_for_timeout(100)
        expect(page.get_by_text("model-33", exact=True)).to_be_visible()
        page.get_by_placeholder("模型", exact=True).fill("model-1")
        page.clock.fast_forward(4500)
        page.wait_for_timeout(100)
        assert not [q for name, q in state["requests"] if name == "/request-logs" and q.get("model")]
        expect(page.get_by_text("model-33", exact=True)).to_be_visible()
        page.get_by_role("button", name="查询", exact=True).click()
        expect(page.get_by_text("model-1", exact=True)).to_be_visible()
        expect(page.locator(".ui-data-table tbody tr")).to_have_count(1)
        expect(page.get_by_text("未知", exact=True)).to_be_visible()
        page.get_by_role("button", name="重置", exact=True).click()
        expect(page.get_by_text("model-53", exact=True)).to_be_visible()
        page.get_by_text("model-53", exact=True).click()
        drawer = page.locator(".ui-drawer")
        expect(drawer).to_contain_text("进行中")
        state["complete"] = True
        page.clock.fast_forward(4500)
        expect(drawer).to_contain_text("200 · 成功")
        assert state["detail_calls"] >= 2
        page.wait_for_timeout(300)
        page.screenshot(path=str(output / "logs-detail-completed.png"), full_page=True)
        page.set_viewport_size(dict(width=390, height=844))
        page.screenshot(path=str(output / "logs-detail-mobile.png"), full_page=True)
        assert drawer.evaluate("el => el.scrollWidth <= el.clientWidth")
        page.set_viewport_size(dict(width=1600, height=1000))
        before = drawer.locator(".meta-grid").inner_text()
        page.clock.fast_forward(5000)
        assert drawer.locator(".meta-grid").inner_text() == before
        page.keyboard.press("Escape")
        expect(drawer).not_to_be_visible()
        pagination.locator(".ui-pagination-item").filter(has_text="3").first.click()
        expect(page.get_by_text("model-13", exact=True)).to_be_visible()
        del logs[5:]
        page.clock.fast_forward(4500)
        expect(page.get_by_text("model-5", exact=True)).to_be_visible()
        expect(page.locator(".ui-data-table tbody tr")).to_have_count(5)
        print("logs: remote pages, draft filters, unknown type and live detail completion")
        assert not errors, errors
        print(json.dumps(dict(result="PASS", api_requests=len(state["requests"]), screenshots=str(output))))
        browser.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="http://127.0.0.1:8081")
    parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parents[1] / "data" / "ui-regression")
    parser.add_argument("--channel", default=None, help="Installed Chromium channel, e.g. chrome")
    args = parser.parse_args()
    run(args.url, args.output, args.channel)
