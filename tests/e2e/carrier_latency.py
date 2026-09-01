#!/usr/bin/env python3
import json
import os
import pathlib
import subprocess
import tempfile
import time
from datetime import datetime, timezone

from playwright.sync_api import sync_playwright


BASE_URL = os.environ.get("HOSTPIN_E2E_BASE_URL", "http://127.0.0.1:18085")
ROOT = pathlib.Path(__file__).resolve().parents[2]


def require(response, expected: int, label: str):
    if response.status != expected:
        raise RuntimeError(f"{label}: HTTP {response.status} {response.text()[:1000]}")
    return response


def main() -> int:
    with tempfile.TemporaryDirectory(prefix="hostpin-carrier-e2e-") as temporary:
        work = pathlib.Path(temporary)
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=True)
            context = browser.new_context(locale="zh-CN", viewport={"width": 1440, "height": 1000})
            page = context.new_page()
            console_errors: list[str] = []
            request_failures: list[str] = []
            page.on("console", lambda message: console_errors.append(message.text) if message.type == "error" else None)
            page.on("requestfailed", lambda request: request_failures.append(f"{request.method} {request.url}: {request.failure}"))

            require(context.request.post(BASE_URL + "/api/v1/setup", data={
                "username": "admin",
                "password": "carrier-browser-password",
                "enrollment_pin": "246810",
                "site_name": "Hostpin 三网测试",
                "site_description": "三网延迟界面验证",
            }), 201, "setup")

            environment = os.environ.copy()
            environment.update({
                "HOSTPIN_NONINTERACTIVE": "1",
                "HOSTPIN_PIN": "246810",
                "HOSTPIN_AGENT_BINARY": str(work / "hostpin-agent"),
                "HOSTPIN_NODE_NAME": "北京测试节点",
                "HOSTPIN_NODE_GROUP": "三网测试",
                "HOSTPIN_NODE_REGION": "北京",
                "HOSTPIN_NODE_TAGS": "carrier,e2e",
            })
            config_path = work / "agent.json"
            install = subprocess.run([
                str(ROOT / "bin" / "hostpin-agent"), "install",
                "--endpoint", BASE_URL, "--allow-http", "--no-service", "--config", str(config_path),
            ], cwd=ROOT, env=environment, text=True, capture_output=True, timeout=30)
            if install.returncode != 0:
                raise RuntimeError(f"Agent install failed:\n{install.stdout}\n{install.stderr}")
            config = json.loads(config_path.read_text(encoding="utf-8"))

            carrier_response = require(
                context.request.get(BASE_URL + "/api/v1/admin/carrier-probes"), 200, "carrier tasks"
            )
            tasks = carrier_response.json()["data"]
            purposes = [task["purpose"] for task in tasks]
            expected = ["carrier.telecom", "carrier.unicom", "carrier.mobile"]
            if purposes != expected:
                raise RuntimeError(f"unexpected carrier task order: {purposes}")

            now = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
            results = []
            for task, latency, loss in zip(tasks, [31.8, 42.6, 27.4], [0, 25, 0]):
                results.append({
                    "task_id": task["id"], "collected_at": now, "success": True,
                    "latency_ms": latency, "loss_percent": loss,
                })
            require(context.request.post(BASE_URL + "/api/v1/agent/reports", headers={
                "Authorization": "Bearer " + config["token"],
            }, data={
                "identity": {"version": "carrier-e2e", "os": "linux", "arch": "arm64", "hostname": "carrier-e2e"},
                "sample": {
                    "sequence": 1, "collected_at": now, "cpu": 18.2,
                    "memory_total": 1_073_741_824, "memory_used": 402_653_184,
                    "disk_total": 21_474_836_480, "disk_used": 6_442_450_944,
                    "net_rx_bps": 524_288, "net_tx_bps": 131_072,
                    "net_rx_bytes": 10_485_760, "net_tx_bytes": 4_194_304,
                    "uptime_seconds": 86_400, "processes": 53,
                },
                "probe_results": results,
            }), 202, "agent report")

            deadline = time.monotonic() + 10
            while True:
                payload = require(context.request.get(
                    BASE_URL + f"/api/v1/public/probes?purpose=carrier&node_id={config['node_id']}&hours=1"
                ), 200, "public carrier history").json()["data"]
                if len(payload) == 3 and all(item["results"] for item in payload):
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError(f"carrier results were not persisted: {payload}")
                time.sleep(0.2)

            page.goto(BASE_URL + f"/nodes/{config['node_id']}", wait_until="networkidle")
            page.get_by_role("heading", name="三网延迟", exact=True).wait_for(timeout=15_000)
            cards = page.locator(".carrier-grid article")
            if cards.count() != 3:
                raise RuntimeError(f"carrier card count={cards.count()}, want 3")
            if "31.8" not in cards.nth(0).inner_text() or "25.0%" not in cards.nth(1).inner_text():
                raise RuntimeError(f"carrier values did not render: {[cards.nth(i).inner_text() for i in range(3)]}")
            if page.evaluate("document.documentElement.scrollWidth > window.innerWidth + 1"):
                raise RuntimeError("desktop node detail has horizontal overflow")
            page.screenshot(path="/tmp/hostpin-carrier-node.png", full_page=True)

            page.goto(BASE_URL + "/admin/probes", wait_until="networkidle")
            board = page.locator(".carrier-admin-board")
            board.get_by_role("heading", name="三网延迟", exact=True).wait_for()
            if board.locator("article").count() != 3:
                raise RuntimeError("carrier administration board is incomplete")
            board.get_by_role("button", name="设置").first.click()
            page.get_by_label("每轮采样次数").fill("5")
            page.get_by_role("button", name="保存设置").click()
            page.get_by_label("每轮采样次数").wait_for(state="hidden")
            updated = require(
                context.request.get(BASE_URL + "/api/v1/admin/carrier-probes"), 200, "updated carrier tasks"
            ).json()["data"]
            if updated[0]["samples"] != 5:
                raise RuntimeError(f"carrier edit did not persist: {updated[0]}")
            page.screenshot(path="/tmp/hostpin-carrier-admin.png", full_page=True)

            page.set_viewport_size({"width": 390, "height": 844})
            page.goto(BASE_URL + f"/nodes/{config['node_id']}", wait_until="networkidle")
            page.get_by_role("heading", name="三网延迟", exact=True).wait_for()
            if page.evaluate("document.documentElement.scrollWidth > window.innerWidth + 1"):
                raise RuntimeError("mobile node detail has horizontal overflow")
            widths = page.locator(".carrier-grid article").evaluate_all(
                "elements => elements.map((element) => element.getBoundingClientRect().width)"
            )
            if any(width < 300 for width in widths):
                raise RuntimeError(f"mobile carrier cards are too narrow: {widths}")
            page.screenshot(path="/tmp/hostpin-carrier-mobile.png", full_page=True)

            browser.close()
            ignored = [failure for failure in request_failures if "/api/v1/public/live" not in failure]
            if console_errors or ignored:
                raise RuntimeError(json.dumps({"console_errors": console_errors, "request_failures": ignored}, ensure_ascii=False))
    print(json.dumps({"status": "ok", "screenshots": [
        "/tmp/hostpin-carrier-node.png", "/tmp/hostpin-carrier-admin.png", "/tmp/hostpin-carrier-mobile.png",
    ]}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
