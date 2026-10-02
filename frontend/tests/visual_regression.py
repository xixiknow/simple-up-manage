"""Responsive native UI acceptance, using API fixtures only (no backend writes)."""
import argparse
import json
import re
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlparse
from playwright.sync_api import expect, sync_playwright


def run(url, output, channel=None):
    output.mkdir(parents=True, exist_ok=True)
    now = datetime.now(timezone.utc).isoformat()
    upstream = dict(id=1, name="Fixture Provider", base_url="https://fixture.example", kind="new_api", protocols=["openai"], status="enabled", health_status="healthy", concurrency=10, last_balance=240, summary=dict(key_count=1, abnormal_count=0, health_counts=dict(healthy=1)))
    key = dict(id=1, upstream_id=1, upstream_name=upstream["name"], upstream_kind="new_api", name="Fixture Key", name_tag="fixture", key_preview="sk-...fixture", status="enabled", protocols=["openai"], health_status="healthy", rate_multiplier=0.5, route_groups=[], route_group_ids=[], last_models=["gpt-fixture"], models_count=1, probe_enabled=True)
    group = dict(id=1, name="Fixture Group", status="enabled", protocol="openai", key_ids=[], models=[], member_count=0, consumer_count=0)
    writes = []
    meta = dict(generated_at=now, available_from=now, timezone="Asia/Shanghai", range=dict(**{"from": now, "to": now}), data_quality=dict(complete=True))
    overview = dict(meta=meta, requests_started=10, requests_completed=10, requests_success=10, success_rate=1, retry_rate=0, ttft_samples=10, inflight_peak=2, finance=dict(consumption_usd=1, estimated_profit_usd=-0.2, coverage=1), balance=dict(total_known_usd=240, enabled_known_usd=240, unlimited_count=0, unknown_count=0, disabled_count=0, stale_count=0, providers=[]))
    snapshot = dict(schema_version=1, instance_id="fixture", sequence=1, server_time=now, started_at=now, window_seconds=60, window_complete=True, business_inflight=2, business_rpm=10, upstream_inflight=2, upstream_rpm=10)

    def api(route):
        request = route.request
        name = urlparse(request.url).path.removeprefix("/api/v1/admin")
        if request.method in ("POST", "PUT", "DELETE"):
            payload = request.post_data_json or {}
            writes.append((name, payload))
            if name == "/consumer-keys":
                data = dict(id=2, key="sk-fixture-created", **payload)
            elif name == "/route-groups/1/keys":
                group.update(key_ids=payload["key_ids"], member_count=len(payload["key_ids"]))
                data = group
            else:
                data = dict(id=2, **payload)
        elif re.match(r"^/upstreams/\d+/keys$", name):
            data = dict(items=[key], total=1, page=1, page_size=20)
        elif name == "/upstreams":
            data = dict(items=[upstream], total=1)
        elif name in ("/keys", "/route-groups/candidates"):
            data = dict(items=[key], total=1)
        elif name == "/route-groups":
            data = dict(items=[group])
        elif name == "/scheduler":
            data = dict(ranking_mode="adaptive", weight_success=0.45, epsilon=0.08, cooldown_sec=30)
        elif name == "/scheduler/explain":
            data = dict(candidates=[], filtered=[], route_bound=False)
        elif name == "/model-catalog":
            data = dict(model_count=1, vendors=[dict(id="openai", name="OpenAI", protocol="openai", models=[dict(id="gpt-fixture", name="Fixture model")])])
        elif name == "/dashboard/overview":
            data = overview
        elif name == "/dashboard/trends":
            data = dict(meta=meta, granularity="hour", points=[dict(bucket=now, complete=True, requests_started=10, requests_completed=10, success_rate=1, failure_rate=0, ttft_samples=10, inflight_peak=2)])
        elif name == "/dashboard/recommendations":
            data = dict(meta=meta, urgent=[], invest=[], watch=[], demand_boards=[], note="fixture")
        elif name == "/dashboard/live":
            route.fulfill(content_type="text/event-stream", body="event: snapshot\ndata: " + json.dumps(snapshot) + "\n\n")
            return
        else:
            data = dict(items=[], total=0, unread=0, meta=meta)
        route.fulfill(json=dict(ok=True, data=data))

    def no_overflow(page):
        assert page.evaluate("document.documentElement.scrollWidth <= innerWidth"), page.evaluate("({width:innerWidth,scroll:document.documentElement.scrollWidth})")

    def modal_fits(page):
        card = page.locator("dialog[open] .ui-modal").last
        assert card.evaluate("el => { const r=el.getBoundingClientRect(); return r.left >= 11 && r.right <= innerWidth-11 && r.top >= 11 && r.bottom <= innerHeight-11 }")
        footer = card.locator(".modal-foot")
        if footer.count():
            assert footer.evaluate("el => { const r=el.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight }")
        return card

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True, channel=channel)
        context = browser.new_context()
        context.route("**/api/v1/admin/**", api)
        page = context.new_page()
        errors = []
        page.on("pageerror", lambda error: errors.append(str(error)))
        for width, height in [(1440, 1000), (834, 1112), (390, 844)]:
            page.set_viewport_size(dict(width=width, height=height))
            page.goto(url + "/login")
            page.wait_for_load_state("networkidle")
            no_overflow(page)
            page.screenshot(path=str(output / f"login-{width}.png"))
        page.set_viewport_size(dict(width=1440, height=1000))
        page.get_by_label("管理员 Token", exact=True).fill("fixture")
        page.get_by_role("button", name="进入控制台", exact=True).click()
        expect(page.locator(".console-aside")).to_be_visible()

        for width, height in [(1440, 1000), (834, 1112), (390, 844)]:
            page.set_viewport_size(dict(width=width, height=height))
            for path in ["dashboard", "upstreams", "api-keys", "scheduler", "logs"]:
                page.goto(url + "/" + path)
                page.wait_for_load_state("networkidle")
                expect(page.locator(".page-head h2")).to_be_visible()
                no_overflow(page)
                assert page.locator(".n-button, .n-modal, .n-menu, .n-layout-sider").count() == 0
                page.screenshot(path=str(output / f"{path}-{width}.png"), full_page=True)
            print(f"PASS all routes at {width}x{height}", flush=True)

        page.goto(url + "/scheduler")
        page.wait_for_load_state("networkidle")
        page.get_by_label("成功率权重", exact=True).fill("0.7")
        for width, height in [(834, 1112), (760, 844), (761, 844), (390, 844)]:
            page.set_viewport_size(dict(width=width, height=height))
            expect(page.get_by_label("成功率权重", exact=True)).to_have_value("0.7")
        page.get_by_role("button", name="保存配置", exact=True).click()
        page.wait_for_timeout(150)
        assert any(name == "/scheduler" and body["weight_success"] == 0.7 for name, body in writes)

        page.goto(url + "/upstreams")
        page.wait_for_load_state("networkidle")
        page.get_by_role("button", name="新建提供商", exact=True).click()
        modal_fits(page)
        page.get_by_label("名称", exact=True).fill("Draft Provider")
        page.get_by_label("Base URL", exact=True).fill("https://fixture.example")
        page.locator("dialog[open]").get_by_role("combobox", name="类型", exact=True).click()
        page.get_by_role("option", name="new-api", exact=False).click()
        modal_fits(page)
        page.set_viewport_size(dict(width=834, height=1112))
        expect(page.get_by_label("名称", exact=True)).to_have_value("Draft Provider")
        page.set_viewport_size(dict(width=390, height=844))
        page.get_by_role("button", name="保存", exact=True).click()
        expect(page.locator("dialog[open]")).to_have_count(0)
        assert any(name == "/upstreams" and body["name"] == "Draft Provider" for name, body in writes)

        # 手机端：选中提供商打开全屏详情，长表单 Key 编辑弹窗可滚动
        page.goto(url + "/upstreams")
        page.wait_for_load_state("networkidle")
        page.locator(".p-row").first.click()
        expect(page.locator(".ui-drawer .table-cards")).to_be_visible()
        page.get_by_label("编辑 Key Fixture Key", exact=True).click()
        modal_fits(page)
        body = page.locator("dialog[open] .modal-body")
        assert body.evaluate("el => el.scrollHeight > el.clientHeight")
        page.screenshot(path=str(output / "key-form-mobile.png"))
        page.keyboard.press("Escape")
        expect(page.locator("dialog[open] .ui-modal")).to_have_count(0)
        expect(page.locator(".ui-drawer .table-cards")).to_be_visible()

        # 手机端专项：抽屉导航、表格卡片化、筛选抽屉
        page.set_viewport_size(dict(width=390, height=844))
        page.goto(url + "/dashboard")
        page.wait_for_load_state("networkidle")
        assert page.locator(".console-aside").is_hidden()
        page.locator(".nav-toggle").click()
        expect(page.locator(".nav-drawer")).to_be_visible()
        page.screenshot(path=str(output / "nav-drawer-mobile.png"))
        page.locator(".nav-drawer nav button").filter(has_text="请求记录").click()
        expect(page.locator(".nav-drawer")).to_have_count(0)
        expect(page).to_have_url(url + "/logs")

        for path in ["upstreams", "api-keys", "scheduler", "logs"]:
            page.goto(url + "/" + path)
            page.wait_for_load_state("networkidle")
            expect(page.locator(".page-head h2")).to_be_visible()
            if path == "upstreams":
                page.locator(".p-row").first.click()
                expect(page.locator(".ui-drawer .table-cards")).to_be_visible()
                assert page.locator(".ui-drawer .table-scroll").count() == 0, path
            else:
                assert page.locator(".table-cards").count() >= 1, path
                assert page.locator(".table-scroll").count() == 0, path
            no_overflow(page)
        print("PASS mobile nav drawer + table cards", flush=True)

        page.get_by_role("button", name="筛选", exact=False).click()
        expect(page.locator(".filter-stack")).to_be_visible()
        page.locator('.filter-stack input[placeholder="模型名称"]').fill("gpt-test")
        page.locator(".filter-foot").get_by_role("button", name="查询", exact=True).click()
        expect(page.locator(".filter-stack")).to_have_count(0)
        print("PASS mobile filter drawer", flush=True)

        page.set_viewport_size(dict(width=1440, height=1000))
        page.goto(url + "/api-keys")
        page.wait_for_load_state("networkidle")
        page.get_by_role("button", name="选择提供商 Key", exact=True).click()
        expect(page.locator(".key-row")).to_have_count(1)
        modal_fits(page)
        positions = page.locator(".member-cols").evaluate_all("els => els.map(el=>el.getBoundingClientRect().x)")
        assert abs(positions[0] - positions[1]) <= 1, positions
        page.locator(".key-row input[type=checkbox]").check()
        page.get_by_role("button", name="保存成员", exact=True).click()
        expect(page.locator("dialog[open]")).to_have_count(0)
        assert group["key_ids"] == [1]

        page.get_by_role("button", name="新建密钥", exact=True).click()
        modal_fits(page)
        page.get_by_label("名称", exact=True).fill("Fixture Consumer")
        page.locator("dialog[open]").get_by_role("combobox", name="分组", exact=True).click()
        page.get_by_role("searchbox", name="搜索选项").fill("Fixture")
        page.get_by_role("option").filter(has_text="Fixture Group").click()
        page.get_by_role("button", name="保存", exact=True).click()
        expect(page.locator("dialog[open]")).to_contain_text("sk-fixture-created")
        assert any(name == "/consumer-keys" and body["route_group_id"] == 1 for name, body in writes)
        page.get_by_role("button", name="我已保存", exact=True).click()
        assert not errors, errors
        print(json.dumps(dict(result="PASS", viewports=3, routes=5, fixture_writes=len(writes), screenshots=str(output))), flush=True)
        browser.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="http://127.0.0.1:5174")
    parser.add_argument("--channel", default=None)
    parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parents[1] / "data" / "visual-regression")
    args = parser.parse_args()
    run(args.url, args.output, args.channel)
