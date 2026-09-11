"""Browser regressions against a running build, with isolated admin API fixtures."""
import argparse
import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import expect, sync_playwright


def run(base_url, output):
    output.mkdir(parents=True, exist_ok=True)
    now = datetime.now(timezone.utc)
    providers = []
    keys = []
    pulses = [dict(start=(now - timedelta(minutes=60-i)).isoformat(), state="bad" if i % 17 == 0 else "ok", ok=0 if i % 17 == 0 else 3, fail=1 if i % 17 == 0 else 0, last_latency_ms=1300) for i in range(60)]
    for i in range(1, 151):
        count = 151 if i == 150 else 2
        health = "down" if i in (1, 3) else "healthy"
        name = f"Provider {i:03}"
        providers.append(dict(id=i, name=name, base_url=f"https://provider-{i}.example.com", kind="sub2api", protocols=["openai", "anthropic"], status="enabled", concurrency=10, last_balance=12.5 if i == 1 else 240.5, last_balance_at=now.isoformat(), summary=dict(key_count=count, abnormal_count=count if health == "down" else 0, health_counts={health: count})))
        for j in range(count):
            keys.append(dict(id=len(keys)+1, upstream_id=i, upstream_name=name, upstream_kind="sub2api", name=f"{name}-key-{j+1}", name_tag=f"key-{j+1}", key_preview="sk-...abc", health_status=health, status="enabled", rate_multiplier=0.3+j/100, channel_score=35 if health == "down" else 93, health_pulse=pulses, route_groups=[], last_models=["gpt-test"], models_count=1, cache_rate=0.6, cache_samples=10))
    logs = [dict(id=i, request_id=f"req-{i}", upstream_id=1, upstream_name="Provider 001", protocol="openai", model=f"model-{i}", path="/v1/chat/completions", status_code=200, success=True, input_tokens=100, output_tokens=30, cache_read_tokens=0, cache_creation_tokens=0, ttft_ms=500, duration_ms=1500, cost_usd=0.001, in_flight=False, stream=i % 2 == 1, stream_known=i != 1, created_at=(now-timedelta(seconds=54-i)).isoformat()) for i in range(1, 54)]
    logs[-1].update(in_flight=True, success=False, created_at=now.isoformat())
    state = dict(requests=[], complete=False, detail_calls=0, delay_page_one=False, held_route=None, fail_provider_once=True, hold_sync=False, held_sync=None, sync_rate=0.123456)

    def api(route):
        parsed = urlparse(route.request.url)
        q = parse_qs(parsed.query)
        name = parsed.path.removeprefix("/api/v1/admin")
        page = int(q.get("page", [1])[0])
        size = min(100, int(q.get("page_size", [20])[0]))
        state["requests"].append((name, q))
        if name == "/upstreams":
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
        browser = p.chromium.launch(headless=True)
        context = browser.new_context(viewport=dict(width=1600, height=1000))
        context.add_init_script("localStorage.setItem('admin_token', 'fixture'); localStorage.setItem('sum-sider-collapsed', '1')")
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
        expect(page.locator(".provider-name-line strong")).to_have_count(10)
        expect(page.locator(".provider-name-line strong").first).to_have_text("Provider 001")
        expect(page.locator(".provider-table tbody tr")).to_have_count(20)
        assert page.locator("td[rowspan='2']").count() >= 20
        assert page.locator(".pulse-cell").count() == 1200
        page.wait_for_timeout(300)
        page.screenshot(path=str(output / "providers-desktop.png"), full_page=True)
        print("provider desktop: 10 groups, 20 keys, merged balances and 1200 pulse cells")

        messages = page.locator(".n-message")
        expect(messages).to_have_count(0)
        keys[0]["rate_multiplier"] = 0.6
        keys[-1]["rate_multiplier"] = 0.2
        page.clock.fast_forward(16000)
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(2)
        expect(messages.filter(has_text="倍率涨价")).to_contain_text("Provider 001-key-1: ×0.3 → ×0.6")
        expect(messages.filter(has_text="倍率降价")).to_contain_text("Provider 150-key-151: ×1.8 → ×0.2")
        page.wait_for_timeout(350)
        page.screenshot(path=str(output / "rate-alerts-desktop.png"), full_page=True)
        page.clock.fast_forward(16000)
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(0)
        print("price alerts: first load quiet, increase and off-page decrease, unchanged refresh quiet")

        # Hold the manual response while polling can already see its new rate.
        state["hold_sync"] = True
        page.get_by_label("Key 操作", exact=True).first.click()
        page.get_by_text("同步倍率", exact=True).click()
        for _ in range(50):
            if state["held_sync"] is not None:
                break
            page.wait_for_timeout(100)
        assert state["held_sync"] is not None
        with page.expect_response(lambda response: "view=rates" in response.url and "page=5&" in response.url):
            page.clock.fast_forward(16000)
        page.wait_for_timeout(200)
        expect(messages).to_have_count(0)
        state["held_sync"].fulfill(json=dict(ok=True, data=keys[0]))
        state["hold_sync"] = False
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(1)
        expect(messages).to_contain_text("倍率降价：Provider 001 / Provider 001-key-1: ×0.6 → ×0.123456")
        page.clock.fast_forward(16000)
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(0)
        page.get_by_label("Key 操作", exact=True).first.click()
        page.get_by_text("同步倍率", exact=True).click()
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(1)
        expect(messages).to_contain_text("已同步倍率：×0.123456")
        page.clock.fast_forward(16000)
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(0)
        print("price alerts: manual sync with concurrent polling notified once; unchanged sync quiet")

        for key in keys[:5]:
            key["rate_multiplier"] += 0.1
        page.clock.fast_forward(16000)
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(4)
        expect(messages.filter(has_text="另有 2 把 Key")).to_be_visible()
        page.clock.fast_forward(16000)
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(0)

        page.get_by_label("搜索提供商").fill("Provider 150")
        page.get_by_role("button", name="查询", exact=True).click()
        expect(page.locator(".provider-name-line strong")).to_have_count(1)
        expect(page.locator(".provider-table tbody tr")).to_have_count(151)
        assert any(name == "/keys" and q.get("page") == ["2"] for name, q in state["requests"])
        page.get_by_role("button", name="重置", exact=True).click()
        expect(page.locator(".provider-name-line strong")).to_have_count(10)
        page.locator(".provider-pagination .n-pagination-item").filter(has_text="2").first.click()
        expect(page.locator(".provider-name-line strong").first).not_to_have_text("Provider 001")
        page.get_by_role("button", name="重置", exact=True).click()
        expect(page.locator(".provider-name-line strong").first).to_have_text("Provider 001")
        print("provider pagination: second page and provider 150 with all 151 keys")

        providers[1]["summary"]["abnormal_count"] = 2
        before = page.locator(".provider-name-line strong").all_text_contents()
        page.clock.fast_forward(16000)
        page.wait_for_timeout(100)
        assert page.locator(".provider-name-line strong").all_text_contents() == before
        print("provider refresh: ordering stays stable")

        page.set_viewport_size(dict(width=390, height=844))
        page.locator('.page-head h2').scroll_into_view_if_needed()
        page.wait_for_timeout(200)
        expect(page.get_by_text("提供商 / 余额", exact=True)).to_be_visible()
        page.screenshot(path=str(output / "providers-mobile.png"), full_page=True)
        assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
        print("provider mobile: merged compact fixed column, no page overflow")

        keys[-1]["name"] = "Provider 150-" + "LongKeyName" * 12
        keys[-1]["rate_multiplier"] = 0.7
        page.clock.fast_forward(16000)
        page.wait_for_load_state("networkidle")
        expect(messages).to_have_count(1)
        expect(messages).to_contain_text("×0.2 → ×0.7")
        page.wait_for_timeout(350)
        assert messages.evaluate_all("els => els.every(el => { const r = el.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth && el.scrollWidth <= el.clientWidth })")
        page.screenshot(path=str(output / "rate-alerts-mobile.png"), full_page=True)
        print("price alerts: batch summary and long key name fit on mobile")

        page.set_viewport_size(dict(width=1600, height=1000))
        page.goto(base_url + "/logs")
        page.wait_for_load_state("networkidle")
        expect(page.locator(".n-data-table tbody tr")).to_have_count(20)
        pagination = page.locator(".n-data-table .n-pagination")
        state["delay_page_one"] = True
        page.clock.fast_forward(4500)
        page.wait_for_timeout(100)
        assert state["held_route"] is not None
        pagination.locator(".n-pagination-item").filter(has_text="2").first.click()
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
        expect(page.locator(".n-data-table tbody tr")).to_have_count(1)
        expect(page.get_by_text("未知", exact=True)).to_be_visible()
        page.get_by_role("button", name="重置", exact=True).click()
        expect(page.get_by_text("model-53", exact=True)).to_be_visible()
        page.get_by_text("model-53", exact=True).click()
        drawer = page.locator(".n-drawer")
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
        pagination.locator(".n-pagination-item").filter(has_text="3").first.click()
        expect(page.get_by_text("model-13", exact=True)).to_be_visible()
        del logs[5:]
        page.clock.fast_forward(4500)
        expect(page.get_by_text("model-5", exact=True)).to_be_visible()
        expect(page.locator(".n-data-table tbody tr")).to_have_count(5)
        print("logs: remote pages, draft filters, unknown type and live detail completion")
        assert not errors, errors
        print(json.dumps(dict(result="PASS", api_requests=len(state["requests"]), screenshots=str(output))))
        browser.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="http://127.0.0.1:8081")
    parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parents[1] / "data" / "ui-regression")
    args = parser.parse_args()
    run(args.url, args.output)
