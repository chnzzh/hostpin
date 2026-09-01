#!/usr/bin/env python3
"""Exercise Hostpin against the official Komari, Carbon, and Pulse bundles.

The server must already be running and initialized. This test deliberately uses
the real theme ZIP files, a real Hostpin Agent process, HTTP and WebSocket RPC2,
the theme management UI, and a private-site login cycle.
"""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import secrets
import subprocess
import tempfile
import time
import uuid
from dataclasses import dataclass
from urllib.parse import urlparse

from playwright.sync_api import BrowserContext, Page, Playwright, sync_playwright


BASE_URL = os.environ.get("HOSTPIN_E2E_URL", "http://127.0.0.1:18082").rstrip("/")
USERNAME = os.environ.get("HOSTPIN_E2E_USERNAME", "admin")
PASSWORD = os.environ.get("HOSTPIN_E2E_PASSWORD", "browser-test-password")
PIN = os.environ.get("HOSTPIN_E2E_PIN", "246810")
ROOT = pathlib.Path(__file__).resolve().parents[2]
ORIGIN = f"{urlparse(BASE_URL).scheme}://{urlparse(BASE_URL).netloc}"


@dataclass(frozen=True)
class ThemeSpec:
    name: str
    short: str
    archive: pathlib.Path
    sha256: str


THEMES = (
    ThemeSpec(
        name="Komari",
        short="komari-default",
        archive=pathlib.Path(os.environ.get("HOSTPIN_KOMARI_DEFAULT_ZIP", "/tmp/hostpin-theme-komari-default.zip")),
        sha256="51d2ca2939c2b41afd8cc4a0213799d8ea18bc5ca034b7713990ea044bb41563",
    ),
    ThemeSpec(
        name="Komari Carbon",
        short="komari-theme-carbon",
        archive=pathlib.Path(os.environ.get("HOSTPIN_CARBON_ZIP", "/tmp/hostpin-theme-carbon.zip")),
        sha256="f34b0aa42e12b5f7a2e25996c3f288585417277cdfefac51d2cd4ea72897a07c",
    ),
    ThemeSpec(
        name="Komari Pulse",
        short="komari-pulse",
        archive=pathlib.Path(os.environ.get("HOSTPIN_PULSE_ZIP", "/tmp/hostpin-theme-pulse.zip")),
        sha256="89a3d47a2d6467fbb674a9ffb17b63754937213fdece58bfa49568792cfc2a2b",
    ),
)


def require_ok(response, label: str) -> dict:
    if not response.ok:
        raise RuntimeError(f"{label} returned HTTP {response.status}: {response.text()[:1000]}")
    if not response.body():
        return {}
    return response.json()


def csrf_headers(context: BrowserContext) -> dict[str, str]:
    for cookie in context.cookies():
        if cookie["name"] == "hostpin_csrf":
            return {"X-CSRF-Token": cookie["value"]}
    raise RuntimeError("administrator CSRF cookie was not issued")


def admin_request(context: BrowserContext, method: str, path: str, data: object | None = None) -> dict:
    response = context.request.fetch(
        BASE_URL + path,
        method=method,
        headers=csrf_headers(context) if method not in {"GET", "HEAD"} else {},
        data=data,
    )
    return require_ok(response, f"{method} {path}")


def verify_archives() -> None:
    for theme in THEMES:
        if not theme.archive.is_file():
            raise RuntimeError(f"theme archive is missing: {theme.archive}")
        digest = hashlib.sha256(theme.archive.read_bytes()).hexdigest()
        if digest != theme.sha256:
            raise RuntimeError(f"{theme.name} checksum is {digest}, expected {theme.sha256}")


def enroll_agent(context: BrowserContext, directory: pathlib.Path) -> tuple[subprocess.Popen, str]:
    token = f"hp_a_{secrets.token_urlsafe(9)}.{secrets.token_urlsafe(32)}"
    install_id = str(uuid.uuid4())
    request = {
        "pin": PIN,
        "install_id": install_id,
        "token": token,
        "identity": {
            "version": "theme-compat",
            "os": "darwin",
            "arch": "arm64",
            "hostname": "theme-compat-fixture",
            "cpu_name": "compatibility fixture",
            "cpu_cores": 2,
        },
        "metadata": {
            "name": "theme-compat-edge",
            "group": "theme-qa",
            "region": "Singapore",
            "country_code": "SG",
            "tags": ["carbon", "pulse", "e2e"],
            "currency": "USD",
            "traffic_limit_type": "sum",
            "traffic_reset_day": 1,
        },
        "config": {
            "collect_interval_seconds": 3,
            "persist_interval_seconds": 60,
            "enable_gpu": False,
            "auto_update": False,
            "config_version": 1,
        },
    }
    payload = require_ok(
        context.request.post(BASE_URL + "/api/v1/enrollments", data=request),
        "agent enrollment",
    )
    node_id = payload["node_id"]
    config = {
        "endpoint": BASE_URL,
        "node_id": node_id,
        "install_id": install_id,
        "token": token,
        "agent": payload["config"],
        "metadata": {"name": "theme-compat-edge", "group": "theme-qa", "tags": ["carbon", "pulse", "e2e"]},
    }
    config_path = directory / "agent.json"
    descriptor = os.open(config_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as output:
        json.dump(config, output)
        output.write("\n")

    admin_request(
        context,
        "POST",
        "/api/v1/admin/probes",
        {
            "name": "Hostpin health",
            "type": "http",
            "target": BASE_URL + "/healthz",
            "interval_seconds": 5,
            "timeout_seconds": 3,
            "expected_status": 200,
            "node_ids": [node_id],
            "enabled": True,
        },
    )
    process = subprocess.Popen(
        [str(ROOT / "bin" / "hostpin-agent"), "run", "--config", str(config_path), "--log-level", "warn"],
        cwd=ROOT,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        text=True,
    )
    return process, node_id


def wait_for_live_agent(context: BrowserContext, node_id: str) -> None:
    deadline = time.monotonic() + 20
    body = ""
    while time.monotonic() < deadline:
        response = context.request.post(
            BASE_URL + "/api/rpc2",
            data={"jsonrpc": "2.0", "id": 1, "method": "common:getNodesLatestStatus"},
        )
        if response.ok:
            payload = response.json()
            body = response.text()
            if payload.get("result", {}).get(node_id, {}).get("online") is True:
                return
        time.sleep(0.5)
    raise RuntimeError(f"Agent {node_id} did not become live: {body[:1000]}")


def wait_for_dom_text(page: Page, value: str, timeout: int = 20_000) -> None:
    page.wait_for_function(
        "value => document.body?.textContent?.includes(value)",
        arg=value,
        timeout=timeout,
    )


def upload_and_activate(page: Page, theme: ThemeSpec) -> None:
    page.goto(BASE_URL + "/admin/themes", wait_until="networkidle")
    page.get_by_role("heading", name="Themes", exact=True).wait_for()
    form = page.locator("form").filter(has_text="Upload ZIP")
    form.locator('input[type="file"]').set_input_files(str(theme.archive))
    form.locator('input:not([type="file"])').fill(theme.sha256)
    with page.expect_response(lambda response: response.url.endswith("/api/v1/admin/themes/upload")) as pending:
        form.get_by_role("button", name="Validate and install").click()
    upload = pending.value
    if upload.status != 201:
        raise RuntimeError(f"uploading {theme.name} returned HTTP {upload.status}: {upload.text()[:1000]}")
    card = page.locator(
        "article.theme-card",
        has=page.get_by_role("heading", name=theme.name, exact=True),
    )
    card.get_by_role("heading", name=theme.name, exact=True).wait_for(timeout=15_000)
    activate = card.get_by_role("button", name="Activate")
    if activate.count():
        with page.expect_response(lambda response: response.url.endswith(f"/themes/{theme.short}/activate")) as pending:
            activate.click()
        if pending.value.status != 200:
            raise RuntimeError(f"activating {theme.name} returned HTTP {pending.value.status}")
    card.get_by_text("Active", exact=True).wait_for(timeout=10_000)
    card.get_by_role("button", name="Configure").click()
    drawer = page.locator("form.theme-config-drawer")
    drawer.get_by_text("Theme configuration", exact=True).wait_for()
    if theme.short == "komari-theme-carbon":
        drawer.locator("select").first.select_option("table")
        drawer.locator('input[type="checkbox"]').first.uncheck()
        drawer.locator('input[type="number"]').first.fill("6")
        page.screenshot(path="/tmp/hostpin-theme-managed-config.png", full_page=True)
    with page.expect_response(lambda response: response.url.endswith(f"/themes/{theme.short}/settings")) as pending:
        drawer.get_by_role("button", name="Save settings").click()
    if pending.value.status != 200:
        raise RuntimeError(f"saving {theme.name} settings returned HTTP {pending.value.status}")
    drawer.wait_for(state="hidden")
    public_settings = page.context.request.get(BASE_URL + "/api/public").json()["data"]["theme_settings"]
    if theme.short == "komari-theme-carbon" and (
        public_settings.get("defaultView") != "table"
        or public_settings.get("showUptime") is not False
        or public_settings.get("defaultChartHours") != 6
    ):
        raise RuntimeError(f"managed Carbon settings were not published: {public_settings}")


def rpc_contract(context: BrowserContext, page: Page, node_id: str) -> dict:
    calls = [
        ("common:getNodes", {}),
        ("common:getNodesLatestStatus", {}),
        ("common:getNodeRecentStatus", {"uuid": node_id}),
        ("common:getRecords", {"uuid": node_id, "hours": 1}),
        ("public:getNodesInformation", {}),
        ("public:getNodesLatestStatus", {}),
        ("public:getPublicSettings", {}),
        ("public:listMetricDefinitions", {}),
        (
            "public:queryMetrics",
            {
                "entity_ids": [node_id],
                "metric_keys": ["cpu.usage", "memory.used", "ping.latency_ms"],
                "hours": 1,
                "fill_empty": True,
            },
        ),
        ("public:getPingMetricStats", {"uuid": node_id, "hours": 1}),
    ]
    batch = [
        {"jsonrpc": "2.0", "id": index + 1, "method": method, "params": params}
        for index, (method, params) in enumerate(calls)
    ]
    payload = require_ok(context.request.post(BASE_URL + "/api/rpc2", data=batch), "RPC2 batch")
    failures = [item for item in payload if item.get("error")]
    if failures:
        raise RuntimeError(f"RPC2 compatibility failures: {json.dumps(failures, ensure_ascii=False)}")
    metric_result = payload[8].get("result", {})
    metric_series = metric_result.get("series", [])
    if metric_result.get("count") != len(metric_series) or not metric_series:
        raise RuntimeError(f"RPC2 metric series wrapper is incompatible: {metric_result}")
    system_points = [
        point
        for item in metric_series
        if item.get("metric_key") == "cpu.usage"
        for point in item.get("points", [])
    ]
    if not system_points or any(
        not isinstance(point, dict) or "time" not in point or "value" not in point
        for point in system_points
    ):
        raise RuntimeError(f"RPC2 metric points must be {{time, value}} objects: {metric_result}")
    ping_result = payload[9].get("result", {})
    ping_stats = ping_result.get("stats", [])
    required_ping_fields = {"entity_id", "task_id", "total", "valid", "loss", "avg", "latest", "p50", "p99", "stddev"}
    if not ping_stats or any(not required_ping_fields.issubset(item) for item in ping_stats):
        raise RuntimeError(f"RPC2 Ping metric stats are incompatible: {ping_result}")
    single_status = require_ok(
        context.request.post(
            BASE_URL + "/api/rpc2",
            data={
                "jsonrpc": "2.0",
                "id": 80,
                "method": "common:getNodesLatestStatus",
                "params": {"uuid": node_id},
            },
        ),
        "RPC2 single latest status",
    )
    if single_status.get("result", {}).get("client") != node_id:
        raise RuntimeError(f"RPC2 single latest status wrapper is incompatible: {single_status}")
    missing_method = require_ok(
        context.request.post(
            BASE_URL + "/api/rpc2",
            data={"jsonrpc": "2.0", "id": 81, "method": "public:notARealMethod"},
        ),
        "RPC2 unknown method",
    )
    if missing_method.get("error", {}).get("code") != -32601:
        raise RuntimeError(f"RPC2 unknown-method code is incompatible: {missing_method}")

    for path in (
        "/api/public",
        "/api/version",
        "/api/nodes",
        f"/api/recent/{node_id}",
        f"/api/records/load?uuid={node_id}&hours=1",
        f"/api/records/ping?uuid={node_id}&hours=1",
        "/api/task/ping",
    ):
        require_ok(context.request.get(BASE_URL + path), f"GET {path}")

    websocket_result = page.evaluate(
        """async ({base, nodeID}) => {
          const call = (url, body) => new Promise((resolve, reject) => {
            const socket = new WebSocket(url)
            const timer = setTimeout(() => { socket.close(); reject(new Error(`timeout: ${url}`)) }, 8000)
            socket.onerror = () => { clearTimeout(timer); reject(new Error(`websocket failed: ${url}`)) }
            socket.onopen = () => socket.send(JSON.stringify(body))
            socket.onmessage = (event) => { clearTimeout(timer); socket.close(); resolve(JSON.parse(event.data)) }
          })
          const scheme = base.startsWith('https:') ? 'wss:' : 'ws:'
          const host = new URL(base).host
          const rpc = await call(`${scheme}//${host}/api/rpc2`, {
            jsonrpc: '2.0', id: 91, method: 'common:getNodesLatestStatus', params: { uuid: nodeID },
          })
          const clients = await call(`${scheme}//${host}/api/clients`, { type: 'subscribe' })
          return { rpc, clients }
        }""",
        {"base": BASE_URL, "nodeID": node_id},
    )
    if websocket_result.get("rpc", {}).get("error"):
        raise RuntimeError(f"RPC2 WebSocket failed: {websocket_result['rpc']}")
    if websocket_result.get("clients", {}).get("status") != "success":
        raise RuntimeError(f"Komari clients WebSocket failed: {websocket_result['clients']}")
    return {"rpc_methods": len(calls) + 2, "rest_routes": 7, "websockets": 2}


def exercise_public_theme(playwright: Playwright, theme: ThemeSpec, node_id: str) -> dict:
    browser = playwright.chromium.launch(headless=True)
    context = browser.new_context(viewport={"width": 1440, "height": 1000})
    page = context.new_page()
    console_errors: list[str] = []
    failed_requests: list[str] = []
    external_failures: list[str] = []
    websocket_urls: list[str] = []
    frames: list[str] = []

    page.on("console", lambda message: console_errors.append(message.text) if message.type == "error" else None)
    def request_failed(request) -> None:
        message = f"{request.method} {request.url}: {request.failure}"
        if request.url.startswith(ORIGIN):
            failed_requests.append(message)
        else:
            external_failures.append(message)

    page.on("requestfailed", request_failed)

    def websocket_opened(socket) -> None:
        websocket_urls.append(socket.url)
        socket.on("framereceived", lambda payload: frames.append(str(payload)))

    page.on("websocket", websocket_opened)
    response = page.goto(BASE_URL + "/", wait_until="domcontentloaded")
    if response is None or response.status != 200:
        raise RuntimeError(f"{theme.name} root page did not return HTTP 200")
    wait_for_dom_text(page, "theme-compat-edge")
    page.wait_for_timeout(3500)
    page.screenshot(path=f"/tmp/hostpin-{theme.short}-home.png", full_page=True)

    contract = rpc_contract(context, page, node_id)
    if theme.short == "komari-pulse":
        rows = page.locator("tbody tr").filter(has_text="theme-compat-edge")
        if rows.count() == 0:
            raise RuntimeError("Komari Pulse did not render a clickable node row")
        row = rows.last
        row.locator("td").first.click(position={"x": 12, "y": 16})
        page.get_by_text("CPU model", exact=True).wait_for(timeout=15_000)
        page.wait_for_timeout(1500)
    elif theme.short == "komari-default":
        page.get_by_text("theme-compat-edge", exact=True).first.click()
        page.wait_for_url("**/instance/**", timeout=15_000)
        try:
            wait_for_dom_text(page, "theme-compat-edge", 15_000)
        except Exception as reason:
            raise RuntimeError(
                f"Komari Web node detail failed at {page.url}: {page.locator('body').inner_text()[:3000]} "
                f"console={console_errors} requests={failed_requests}"
            ) from reason
        page.wait_for_timeout(2000)
    else:
        page.goto(BASE_URL + f"/node/{node_id}", wait_until="domcontentloaded")
        wait_for_dom_text(page, "theme-compat-edge", 15_000)
        page.get_by_text("Hardware", exact=True).wait_for(timeout=15_000)
        page.wait_for_timeout(2000)
    page.screenshot(path=f"/tmp/hostpin-{theme.short}-node.png", full_page=True)

    page.goto(BASE_URL + f"/ping?uuid={node_id}", wait_until="domcontentloaded")
    page.wait_for_timeout(2500)
    if not page.locator("body").inner_text().strip():
        raise RuntimeError(f"{theme.name} Ping route rendered an empty page")

    ignored = [item for item in failed_requests if "/api/v1/public/live" not in item]
    blocking_console = [
        item
        for item in console_errors
        if "api.frankfurter.app" not in item
        and not ("net::ERR_FAILED" in item and external_failures)
    ]
    if blocking_console or ignored:
        raise RuntimeError(
            f"{theme.name} browser errors: "
            + json.dumps({"console": blocking_console, "requests": ignored}, ensure_ascii=False)
        )
    context.close()
    browser.close()
    return {
        **contract,
        "theme_websockets": sorted(set(websocket_urls)),
        "frames": len(frames),
        "external_warnings": external_failures,
        "screenshots": [
            f"/tmp/hostpin-{theme.short}-home.png",
            f"/tmp/hostpin-{theme.short}-node.png",
        ],
    }


def exercise_private_login(playwright: Playwright, theme: ThemeSpec, node_id: str) -> None:
    browser = playwright.chromium.launch(headless=True)
    context = browser.new_context(viewport={"width": 1200, "height": 850})
    page = context.new_page()
    page.goto(BASE_URL + "/login", wait_until="domcontentloaded")
    page.locator('input[type="password"]').first.wait_for(timeout=15_000)
    login = context.request.post(
        BASE_URL + "/api/login",
        data={"username": USERNAME, "password": PASSWORD},
    )
    require_ok(login, f"{theme.name} Komari login")
    page.goto(BASE_URL + "/", wait_until="domcontentloaded")
    wait_for_dom_text(page, "theme-compat-edge", 15_000)
    require_ok(context.request.get(BASE_URL + f"/api/recent/{node_id}"), "private recent records")
    context.close()
    browser.close()


def main() -> int:
    verify_archives()
    agent: subprocess.Popen | None = None
    node_id: str | None = None
    revocation_error = ""
    report: dict[str, object] = {"status": "ok", "themes": {}}
    with tempfile.TemporaryDirectory(prefix="hostpin-theme-agent-") as raw_agent_dir:
        with sync_playwright() as playwright:
            admin_browser = playwright.chromium.launch(headless=True)
            admin_context = admin_browser.new_context(viewport={"width": 1440, "height": 1000})
            login = admin_context.request.post(
                BASE_URL + "/api/v1/auth/login",
                data={"username": USERNAME, "password": PASSWORD},
            )
            require_ok(login, "administrator login")
            original_settings = admin_request(admin_context, "GET", "/api/v1/admin/settings")["data"]
            admin_page = admin_context.new_page()
            try:
                agent, node_id = enroll_agent(admin_context, pathlib.Path(raw_agent_dir))
                wait_for_live_agent(admin_context, node_id)
                admin_page.goto(BASE_URL + "/admin/themes", wait_until="networkidle")
                with admin_page.expect_response(lambda response: response.url.endswith("/api/v1/admin/themes/market")) as pending:
                    admin_page.get_by_role("button", name="Theme market").click()
                if pending.value.status != 200:
                    raise RuntimeError(f"theme market returned HTTP {pending.value.status}")
                market_drawer = admin_page.locator("section.market-drawer")
                market_drawer.get_by_role("heading", name="Theme market", exact=True).wait_for()
                market_drawer.get_by_text("Komari Carbon", exact=True).first.wait_for(timeout=15_000)
                admin_page.screenshot(path="/tmp/hostpin-theme-market.png", full_page=True)
                market_drawer.get_by_role("button", name="Close").click()
                for theme in THEMES:
                    upload_and_activate(admin_page, theme)
                    report["themes"][theme.short] = exercise_public_theme(playwright, theme, node_id)
                    current = admin_request(admin_context, "GET", "/api/v1/admin/settings")["data"]
                    current["private"] = True
                    admin_request(admin_context, "PUT", "/api/v1/admin/settings", current)
                    exercise_private_login(playwright, theme, node_id)
                    current["private"] = False
                    admin_request(admin_context, "PUT", "/api/v1/admin/settings", current)
            finally:
                try:
                    admin_request(admin_context, "PUT", "/api/v1/admin/settings", original_settings)
                except Exception as reason:
                    report["settings_restore_error"] = str(reason)
                if agent is not None and agent.poll() is None:
                    try:
                        if node_id:
                            admin_request(admin_context, "DELETE", f"/api/v1/admin/nodes/{node_id}")
                            agent.wait(timeout=10)
                        else:
                            raise RuntimeError("theme Agent node ID was unavailable")
                    except Exception as reason:
                        revocation_error = f"active Agent stream was not revoked after node deletion: {reason}"
                        agent.terminate()
                        try:
                            agent.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            agent.kill()
                if agent is not None and agent.returncode not in {None, 0, 1, -15}:
                    report["agent_stderr"] = (agent.stderr.read() if agent.stderr else "")[-4000:]
                admin_context.close()
                admin_browser.close()

    if revocation_error:
        raise RuntimeError(revocation_error)
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
