#!/usr/bin/env python3
import json
import atexit
import base64
import hashlib
import hmac
import os
import pathlib
import struct
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timedelta, timezone
from urllib.parse import urlparse

from playwright.sync_api import sync_playwright


BASE_URL = os.environ.get("HOSTPIN_E2E_URL", "http://127.0.0.1:18082")
ROOT = pathlib.Path(__file__).resolve().parents[2]


def require_status(response, expected: int, label: str):
    if response.status != expected:
        raise RuntimeError(f"{label} returned {response.status}, expected {expected}: {response.text()[:1000]}")
    return response


def admin_request(context, csrf: str, method: str, path: str, data=None):
    headers = {"X-CSRF-Token": csrf} if method not in {"GET", "HEAD"} else {}
    return context.request.fetch(BASE_URL + path, method=method, headers=headers, data=data)


def totp_code(secret: str, at: float | None = None) -> str:
    normalized = secret.strip().upper()
    normalized += "=" * ((8 - len(normalized) % 8) % 8)
    key = base64.b32decode(normalized)
    counter = int(at or time.time()) // 30
    digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    value = struct.unpack(">I", digest[offset : offset + 4])[0] & 0x7FFFFFFF
    return f"{value % 1_000_000:06d}"


def exercise_alert_crud(context, csrf: str, node_id: str) -> None:
    payload = {
        "name": "Browser CPU sustained",
        "metric": "cpu",
        "operator": ">",
        "threshold": 80,
        "recovery_threshold": 70,
        "duration_seconds": 5,
        "cooldown_seconds": 60,
        "severity": "warning",
        "scope": {"node_ids": [node_id]},
        "enabled": True,
    }
    created = require_status(
        admin_request(context, csrf, "POST", "/api/v1/admin/alerts/rules", payload),
        201,
        "alert rule creation",
    ).json()["data"]
    malformed = admin_request(context, csrf, "PUT", "/api/v1/admin/alerts/rules/not-a-number", payload)
    require_status(malformed, 400, "malformed alert rule update")
    payload["name"] = "Browser CPU updated"
    require_status(
        admin_request(context, csrf, "PUT", f"/api/v1/admin/alerts/rules/{created['id']}", payload),
        200,
        "alert rule update",
    )
    require_status(
        admin_request(context, csrf, "DELETE", f"/api/v1/admin/alerts/rules/{created['id']}"),
        204,
        "alert rule deletion",
    )


def exercise_access_controls(playwright, browser, context, csrf: str, node_id: str) -> None:
    setup = require_status(
        admin_request(context, csrf, "POST", "/api/v1/admin/security/totp/setup"),
        200,
        "TOTP setup",
    ).json()
    confirmed = require_status(
        admin_request(
            context,
            csrf,
            "POST",
            "/api/v1/admin/security/totp/confirm",
            {"setup_token": setup["setup_token"], "code": totp_code(setup["secret"])},
        ),
        200,
        "TOTP confirmation",
    ).json()
    if len(confirmed.get("recovery_codes", [])) != 10:
        raise RuntimeError(f"TOTP did not return ten one-time recovery codes: {confirmed}")
    me = require_status(context.request.get(BASE_URL + "/api/v1/auth/me"), 200, "TOTP status").json()
    if not me.get("two_factor_enabled"):
        raise RuntimeError(f"TOTP confirmation was not persisted: {me}")

    totp_login = playwright.request.new_context()
    recovery_login = playwright.request.new_context()
    recovery_reuse = playwright.request.new_context()
    try:
        require_status(
            totp_login.post(
                BASE_URL + "/api/v1/auth/login",
                data={"username": "admin", "password": "browser-test-password"},
            ),
            401,
            "TOTP-required login",
        )
        require_status(
            totp_login.post(
                BASE_URL + "/api/v1/auth/login",
                data={
                    "username": "admin",
                    "password": "browser-test-password",
                    "totp_code": totp_code(setup["secret"]),
                },
            ),
            200,
            "TOTP-authenticated login",
        )
        recovery_code = confirmed["recovery_codes"][0]
        require_status(
            recovery_login.post(
                BASE_URL + "/api/v1/auth/login",
                data={
                    "username": "admin",
                    "password": "browser-test-password",
                    "recovery_code": recovery_code,
                },
            ),
            200,
            "recovery-code login",
        )
        require_status(
            recovery_reuse.post(
                BASE_URL + "/api/v1/auth/login",
                data={
                    "username": "admin",
                    "password": "browser-test-password",
                    "recovery_code": recovery_code,
                },
            ),
            401,
            "consumed recovery code",
        )
    finally:
        totp_login.dispose()
        recovery_login.dispose()
        recovery_reuse.dispose()

    require_status(
        admin_request(
            context,
            csrf,
            "DELETE",
            "/api/v1/admin/security/totp",
            {"password": "browser-test-password"},
        ),
        200,
        "TOTP disable",
    )

    key_result = require_status(
        admin_request(context, csrf, "POST", "/api/v1/admin/api-keys", {"name": "browser-e2e", "expires_in_days": 1}),
        201,
        "API key creation",
    ).json()
    key_context = playwright.request.new_context(
        extra_http_headers={"Authorization": f"Bearer {key_result['token']}"}
    )
    try:
        require_status(key_context.get(BASE_URL + "/api/v1/admin/settings"), 200, "API key authentication")
        require_status(
            admin_request(context, csrf, "DELETE", f"/api/v1/admin/api-keys/{key_result['key']['id']}"),
            204,
            "API key revocation",
        )
        require_status(key_context.get(BASE_URL + "/api/v1/admin/settings"), 401, "revoked API key")
    finally:
        key_context.dispose()

    secondary = playwright.request.new_context()
    try:
        require_status(
            secondary.post(
                BASE_URL + "/api/v1/auth/login",
                data={"username": "admin", "password": "browser-test-password"},
            ),
            200,
            "secondary session login",
        )
        sessions = require_status(
            context.request.get(BASE_URL + "/api/v1/admin/security/sessions"), 200, "session listing"
        ).json()["data"]
        if len(sessions) < 2 or sum(1 for session in sessions if session.get("current")) != 1:
            raise RuntimeError(f"session inventory did not distinguish the current session: {sessions}")
        require_status(
            admin_request(context, csrf, "POST", "/api/v1/admin/security/sessions/revoke-others"),
            200,
            "other-session revocation",
        )
        require_status(secondary.get(BASE_URL + "/api/v1/admin/settings"), 401, "revoked browser session")
    finally:
        secondary.dispose()

    expires_at = (datetime.now(timezone.utc) + timedelta(minutes=10)).isoformat().replace("+00:00", "Z")
    share_result = require_status(
        admin_request(
            context,
            csrf,
            "POST",
            "/api/v1/admin/share-links",
            {"node_ids": [node_id], "expires_at": expires_at},
        ),
        201,
        "share-link creation",
    ).json()
    share = share_result["link"]
    share_url = share_result["url"]
    token = urlparse(share_url).path.rsplit("/", 1)[-1]
    anonymous = playwright.request.new_context()
    share_context = browser.new_context(viewport={"width": 1100, "height": 800})
    try:
        shared = require_status(
            anonymous.get(BASE_URL + f"/api/v1/public/share/{token}"), 200, "anonymous share-link access"
        ).json()
        if [item["node"]["id"] for item in shared.get("nodes", [])] != [node_id]:
            raise RuntimeError(f"share link did not enforce its explicit node set: {shared}")
        share_page = share_context.new_page()
        share_page.goto(share_url, wait_until="networkidle")
        share_page.get_by_text("browser-edge", exact=True).wait_for(timeout=15_000)

        settings_response = require_status(
            context.request.get(BASE_URL + "/api/v1/admin/settings"), 200, "read site settings"
        ).json()["data"]
        private_settings = dict(settings_response)
        private_settings["private"] = True
        require_status(
            admin_request(context, csrf, "PUT", "/api/v1/admin/settings", private_settings),
            200,
            "enable private site",
        )
        require_status(anonymous.get(BASE_URL + "/api/v1/public/nodes"), 401, "private public-node API")
        require_status(
            anonymous.get(BASE_URL + f"/api/v1/public/share/{token}"), 200, "private-site share-link access"
        )
        settings_response["private"] = False
        require_status(
            admin_request(context, csrf, "PUT", "/api/v1/admin/settings", settings_response),
            200,
            "restore public site",
        )
        require_status(
            admin_request(context, csrf, "DELETE", f"/api/v1/admin/share-links/{share['id']}"),
            204,
            "share-link revocation",
        )
        require_status(
            anonymous.get(BASE_URL + f"/api/v1/public/share/{token}"), 404, "revoked share link"
        )
    finally:
        share_context.close()
        anonymous.dispose()


def stop_agent(process: subprocess.Popen | None) -> None:
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()


def main() -> int:
    console_errors: list[str] = []
    request_failures: list[str] = []
    agent = None
    probe_agent = None
    with tempfile.TemporaryDirectory(prefix="hostpin-agent-e2e-") as agent_dir:
        config_path = pathlib.Path(agent_dir) / "agent.json"
        probe_dir = pathlib.Path(agent_dir) / "probe"
        probe_dir.mkdir()
        probe_config_path = probe_dir / "agent.json"
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=True)
            locale_scenarios = (
                ("zh-CN", "zh-CN", None),
                ("en-GB", "en-US", None),
                ("en-GB", "en-US", "zh-CN"),
            )
            for browser_locale, expected_locale, legacy_locale in locale_scenarios:
                locale_context = browser.new_context(locale=browser_locale)
                try:
                    if legacy_locale:
                        locale_context.add_init_script(
                            f"localStorage.setItem('hostpin-locale', {json.dumps(legacy_locale)})"
                        )
                    locale_page = locale_context.new_page()
                    locale_page.goto(BASE_URL, wait_until="networkidle")
                    actual_locale = locale_page.locator("html").get_attribute("lang")
                    if actual_locale != expected_locale:
                        raise RuntimeError(
                            f"Browser locale {browser_locale} selected {actual_locale}, expected {expected_locale}"
                        )
                    saved_locale = locale_page.evaluate("localStorage.getItem('hostpin-locale-preference')")
                    if saved_locale is not None:
                        raise RuntimeError(f"Automatic locale detection persisted an explicit preference: {saved_locale}")
                    if browser_locale == "zh-CN" and legacy_locale is None:
                        locale_page.evaluate(
                            """() => {
                                Object.defineProperty(navigator, 'languages', {
                                    configurable: true,
                                    value: ['en-US'],
                                });
                                window.dispatchEvent(new Event('languagechange'));
                            }"""
                        )
                        locale_page.locator('html[lang="en-US"]').wait_for()
                finally:
                    locale_context.close()

            context = browser.new_context(viewport={"width": 1440, "height": 1000})
            page = context.new_page()
            page.on("console", lambda message: console_errors.append(message.text) if message.type == "error" else None)
            page.on("requestfailed", lambda request: request_failures.append(f"{request.method} {request.url}: {request.failure}"))

            page.goto(BASE_URL, wait_until="networkidle")
            page.wait_for_url("**/setup")
            page.get_by_role("button", name="EN").click()
            stored_locales = page.evaluate(
                "[localStorage.getItem('hostpin-locale'), localStorage.getItem('hostpin-locale-preference')]"
            )
            if stored_locales != ["en-US", "en-US"]:
                raise RuntimeError("Manual language selection was not persisted")
            page.get_by_label("SITE NAME").fill("Hostpin Browser QA")
            page.get_by_label("DESCRIPTION").fill("Browser-verified self-hosted telemetry.")
            page.get_by_label("USERNAME").fill("admin")
            page.get_by_label("PASSWORD 12+ CHARACTERS").fill("browser-test-password")
            page.get_by_label("CONFIRM PASSWORD").fill("browser-test-password")
            page.get_by_label("ENROLLMENT PIN").fill("246810")
            page.get_by_role("button", name="COMPLETE SETUP").click()
            page.wait_for_url("**/admin**", timeout=20_000)
            page.get_by_role("heading", name="Overview", exact=True).wait_for()

            page.goto(f"{BASE_URL}/admin/settings", wait_until="networkidle")
            page.get_by_role("heading", name="System settings", exact=True).wait_for()
            page.get_by_role("button", name="Generate temporary PIN", exact=True).click()
            temporary_pin_value = page.get_by_test_id("temporary-pin-value")
            temporary_pin_value.wait_for()
            temporary_pin = temporary_pin_value.inner_text().strip()
            if len(temporary_pin) != 8 or not temporary_pin.isdigit():
                raise RuntimeError(f"Temporary enrollment PIN was malformed: {temporary_pin!r}")
            page.locator('.temporary-pin-status[data-status="active"]').wait_for()
            page.locator("#enrollment").screenshot(path="/tmp/hostpin-temporary-pin.png")
            temporary_pin_mobile = context.new_page()
            temporary_pin_mobile.set_viewport_size({"width": 390, "height": 844})
            temporary_pin_mobile.goto(f"{BASE_URL}/admin/settings", wait_until="networkidle")
            temporary_pin_mobile.locator("#enrollment").screenshot(path="/tmp/hostpin-temporary-pin-mobile.png")
            temporary_pin_mobile.close()

            environment = os.environ.copy()
            environment.update(
                {
                    "HOSTPIN_NONINTERACTIVE": "1",
                    "HOSTPIN_PIN": temporary_pin,
                    "HOSTPIN_AGENT_BINARY": str(pathlib.Path(agent_dir) / "hostpin-agent"),
                    "HOSTPIN_NODE_NAME": "browser-edge",
                    "HOSTPIN_NODE_GROUP": "qa",
                    "HOSTPIN_NODE_REGION": "Singapore",
                    "HOSTPIN_NODE_TAGS": "browser,e2e",
                }
            )
            install = subprocess.run(
                [
                    str(ROOT / "bin" / "hostpin-agent"),
                    "install",
                    "--endpoint",
                    BASE_URL,
                    "--allow-http",
                    "--no-service",
                    "--config",
                    str(config_path),
                ],
                cwd=ROOT,
                env=environment,
                text=True,
                capture_output=True,
                timeout=30,
            )
            if install.returncode != 0:
                raise RuntimeError(f"Agent install failed: {install.stdout}\n{install.stderr}")
            with config_path.open(encoding="utf-8") as source:
                monitor_config = json.load(source)
            node_id = monitor_config["node_id"]
            csrf = next(cookie["value"] for cookie in context.cookies() if cookie["name"] == "hostpin_csrf")
            temporary_status = context.request.get(BASE_URL + "/api/v1/admin/enrollment/temporary-pin")
            if temporary_status.status != 200 or temporary_status.json().get("data", {}).get("status") != "used":
                raise RuntimeError(f"Temporary PIN was not consumed by enrollment: {temporary_status.status} {temporary_status.text()}")

            reuse_dir = pathlib.Path(agent_dir) / "temporary-pin-reuse"
            reuse_dir.mkdir()
            reuse_environment = environment.copy()
            reuse_environment.update(
                {
                    "HOSTPIN_AGENT_BINARY": str(reuse_dir / "hostpin-agent"),
                    "HOSTPIN_NODE_NAME": "temporary-pin-reuse",
                }
            )
            reuse_install = subprocess.run(
                [
                    str(ROOT / "bin" / "hostpin-agent"),
                    "install",
                    "--endpoint",
                    BASE_URL,
                    "--allow-http",
                    "--no-service",
                    "--config",
                    str(reuse_dir / "agent.json"),
                ],
                cwd=ROOT,
                env=reuse_environment,
                text=True,
                capture_output=True,
                timeout=30,
            )
            if reuse_install.returncode == 0:
                raise RuntimeError("One-use temporary PIN enrolled a second node")
            forged = context.request.post(
                BASE_URL + "/api/v1/admin/probes",
                data={"name": "forged", "type": "tcp", "target": "127.0.0.1:1", "interval_seconds": 60, "timeout_seconds": 2, "enabled": True},
            )
            if forged.status != 403:
                raise RuntimeError(f"CSRF-free admin write returned {forged.status}, expected 403")
            probe = context.request.post(
                BASE_URL + "/api/v1/admin/probes",
                headers={"X-CSRF-Token": csrf},
                data={
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
            if probe.status != 201:
                raise RuntimeError(f"Probe creation failed: {probe.status} {probe.text()}")
            probe_payload = {
                "name": "Missing probe",
                "type": "tcp",
                "target": "127.0.0.1:9",
                "interval_seconds": 60,
                "timeout_seconds": 2,
                "enabled": True,
            }
            require_status(
                admin_request(context, csrf, "PUT", "/api/v1/admin/probes/not-a-number", probe_payload),
                400,
                "malformed probe update",
            )
            require_status(
                admin_request(context, csrf, "PUT", "/api/v1/admin/probes/9223372036854775807", probe_payload),
                404,
                "missing probe update",
            )

            def report_traffic(sequence: int, rx_bytes: int, tx_bytes: int) -> None:
                response = context.request.post(
                    BASE_URL + "/api/v1/agent/reports",
                    headers={"Authorization": f"Bearer {monitor_config['token']}"},
                    data={
                        "identity": {"version": "traffic-e2e", "os": "darwin", "arch": "arm64", "hostname": "browser-edge"},
                        "sample": {
                            "sequence": sequence,
                            "boot_id": "traffic-e2e-boot",
                            "cpu": 12.5,
                            "memory_total": 1_000_000,
                            "memory_used": 500_000,
                            "disk_total": 2_000_000,
                            "disk_used": 750_000,
                            "net_rx_bytes": rx_bytes,
                            "net_tx_bytes": tx_bytes,
                            "uptime_seconds": 600,
                        },
                    },
                )
                if response.status != 202:
                    raise RuntimeError(f"Traffic report {sequence} failed: {response.status} {response.text()}")

            report_traffic(1, 1_000, 500)
            report_traffic(2, 1_600, 800)
            traffic_snapshot = context.request.get(BASE_URL + f"/api/v1/public/nodes/{node_id}")
            if traffic_snapshot.status != 200:
                raise RuntimeError(f"Traffic snapshot failed: {traffic_snapshot.status} {traffic_snapshot.text()}")
            traffic_metric = traffic_snapshot.json()["data"]["metric"]
            if traffic_metric.get("monthly_rx_bytes") != 600 or traffic_metric.get("monthly_tx_bytes") != 300:
                raise RuntimeError(f"Traffic deltas were not accumulated: {traffic_metric}")

            exercise_alert_crud(context, csrf, node_id)
            exercise_access_controls(playwright, browser, context, csrf, node_id)

            probe_environment = environment.copy()
            probe_environment.update(
                {
                    "HOSTPIN_PIN": "246810",
                    "HOSTPIN_AGENT_BINARY": str(probe_dir / "hostpin-agent"),
                    "HOSTPIN_NODE_NAME": "browser-router",
                    "HOSTPIN_NODE_GROUP": "qa-edge",
                    "HOSTPIN_NODE_REGION": "Private LAN",
                    "HOSTPIN_NODE_TAGS": "router,nat,e2e",
                    "HOSTPIN_PROBE_PUBLIC": "true",
                }
            )
            probe_install = subprocess.run(
                [
                    str(ROOT / "bin" / "hostpin-agent"),
                    "install",
                    "--probe-node",
                    "--endpoint",
                    BASE_URL,
                    "--allow-http",
                    "--no-service",
                    "--config",
                    str(probe_config_path),
                ],
                cwd=ROOT,
                env=probe_environment,
                text=True,
                capture_output=True,
                timeout=30,
            )
            if probe_install.returncode != 0:
                raise RuntimeError(f"Probe Node install failed: {probe_install.stdout}\n{probe_install.stderr}")
            with probe_config_path.open(encoding="utf-8") as source:
                probe_config = json.load(source)
            if probe_config.get("role") != "probe":
                raise RuntimeError(f"Probe Node role was not persisted: {probe_config}")

            parsed_base = urlparse(BASE_URL)
            target_host = parsed_base.hostname or "127.0.0.1"
            if ":" in target_host:
                target_host = f"[{target_host}]"
            target_port = parsed_base.port or (443 if parsed_base.scheme == "https" else 80)
            latency_target = context.request.post(
                BASE_URL + "/api/v1/admin/latency/targets",
                headers={"X-CSRF-Token": csrf},
                data={
                    "name": "Browser edge route",
                    "type": "tcp",
                    "target": f"{target_host}:{target_port}",
                    "target_node_id": node_id,
                    "interval_seconds": 5,
                    "timeout_seconds": 2,
                    "samples": 3,
                    "public": True,
                    "enabled": True,
                },
            )
            if latency_target.status != 201:
                raise RuntimeError(f"Latency target creation failed: {latency_target.status} {latency_target.text()}")

            agent = subprocess.Popen(
                [str(pathlib.Path(agent_dir) / "hostpin-agent"), "run", "--config", str(config_path)],
                cwd=ROOT,
                env=environment,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            atexit.register(stop_agent, agent)
            probe_agent = subprocess.Popen(
                [str(probe_dir / "hostpin-agent"), "run", "--config", str(probe_config_path)],
                cwd=ROOT,
                env=probe_environment,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            atexit.register(stop_agent, probe_agent)

            page.goto(BASE_URL, wait_until="networkidle")
            page.get_by_text("browser-edge", exact=True).wait_for(timeout=20_000)
            footer_gap = page.evaluate(
                """() => {
                    const layout = document.querySelector('.public-layout');
                    const footer = document.querySelector('.public-footer');
                    if (!layout || !footer) return -1;
                    return Math.round(layout.getBoundingClientRect().bottom - footer.getBoundingClientRect().bottom);
                }"""
            )
            if footer_gap > 1:
                raise RuntimeError(f"Public footer left {footer_gap}px of blank layout below it")
            page.screenshot(path="/tmp/hostpin-overview.png", full_page=True)
            page.get_by_text("browser-edge", exact=True).click()
            page.wait_for_url("**/nodes/**")
            page.get_by_text("Resource utilization", exact=True).wait_for()
            page.get_by_text("Service probes", exact=True).wait_for(timeout=15_000)
            page.get_by_text("Hostpin health", exact=True).wait_for(timeout=15_000)
            page.get_by_text("Monthly traffic / SUM", exact=True).wait_for()
            traffic_strip = page.locator(".traffic-strip")
            if "↓" not in traffic_strip.inner_text() or "↑" not in traffic_strip.inner_text():
                raise RuntimeError(f"Traffic direction totals did not render: {traffic_strip.inner_text()}")
            page.screenshot(path="/tmp/hostpin-node.png", full_page=True)
            appearance_button = page.get_by_role("button", name="Appearance: system")
            appearance_button.click()
            page.locator('html[data-appearance="dark"]').wait_for()
            page.screenshot(path="/tmp/hostpin-node-dark.png", full_page=True)
            page.get_by_role("button", name="Appearance: dark").click()
            page.locator('html[data-appearance="light"]').wait_for()

            page.goto(f"{BASE_URL}/latency", wait_until="networkidle")
            page.locator(".latency-matrix").wait_for()
            page.locator(".latency-matrix").get_by_text("browser-router", exact=True).wait_for(timeout=20_000)
            route_row = page.locator(".latency-matrix tbody tr").filter(has_text="browser-edge")
            route_cell = route_row.get_by_role("button")
            route_cell.wait_for(timeout=25_000)
            deadline = time.monotonic() + 25
            while time.monotonic() < deadline and "NO SAMPLE" in route_cell.inner_text():
                time.sleep(0.5)
            if "NO SAMPLE" in route_cell.inner_text():
                raise RuntimeError(f"Probe Node did not produce a latency result: {route_cell.inner_text()}")
            route_cell.click()
            page.get_by_role("heading", name="browser-router → browser-edge", exact=True).wait_for(timeout=15_000)
            page.screenshot(path="/tmp/hostpin-latency.png", full_page=True)

            page.goto(f"{BASE_URL}/admin/latency", wait_until="networkidle")
            page.get_by_role("heading", name="Latency nodes", exact=True).wait_for()
            page.get_by_text("browser-router", exact=True).wait_for()
            target_row = page.locator("tbody tr").filter(has_text="browser-edge").last
            target_row.wait_for()
            if "3 samples / 5s" not in target_row.inner_text():
                raise RuntimeError(f"Latency target settings did not render: {target_row.inner_text()}")
            page.get_by_text("NAT / CGNAT safe", exact=True).wait_for()
            page.screenshot(path="/tmp/hostpin-latency-admin.png", full_page=True)

            page.goto(f"{BASE_URL}/admin/nodes", wait_until="networkidle")
            page.get_by_role("heading", name="Nodes", exact=True).wait_for()
            page.get_by_text("browser-edge", exact=True).wait_for()
            node_row = page.locator("tbody tr").filter(has_text="browser-edge")
            node_row.get_by_role("button", name="Edit").click()
            page.get_by_text("Agent collection", exact=True).wait_for()
            page.get_by_text("Traffic correction", exact=True).wait_for()
            page.get_by_label("Unit").select_option("B")
            page.get_by_label("Corrected download").fill("900")
            page.get_by_label("Corrected upload").fill("450")
            page.get_by_role("button", name="Apply correction").click()
            page.get_by_text("Correction applied; later traffic in this period will continue accumulating.", exact=True).wait_for()
            if "900 B" not in page.locator(".traffic-correction-editor").inner_text():
                raise RuntimeError(f"Corrected traffic total did not render: {page.locator('.traffic-correction-editor').inner_text()}")
            page.get_by_label("Unit").select_option("GiB")
            page.get_by_label("Corrected download").fill("2")
            page.get_by_label("Corrected upload").fill("1")
            page.get_by_role("button", name="Apply correction").click()
            page.get_by_text("↓ 2.0 GiB", exact=False).wait_for()
            page.get_by_label("Live interval (seconds)").fill("4")
            page.get_by_label("Monthly traffic quota (GiB)").fill("100")
            page.get_by_label("Traffic mode").select_option("max")
            page.get_by_label("Use as a latency measurement point").check()
            page.get_by_role("button", name="Save node").click()
            page.get_by_text("Agent collection", exact=True).wait_for(state="hidden", timeout=15_000)
            quota_snapshot = context.request.get(BASE_URL + f"/api/v1/public/nodes/{node_id}")
            quota_bytes = quota_snapshot.json()["data"]["node"].get("traffic_limit") if quota_snapshot.status == 200 else None
            if quota_bytes != 107_374_182_400:
                raise RuntimeError(f"100 GiB quota was stored as {quota_bytes!r} bytes")
            config_deadline = time.monotonic() + 25
            applied_interval = 0
            while time.monotonic() < config_deadline:
                with config_path.open(encoding="utf-8") as source:
                    applied_interval = json.load(source)["agent"]["collect_interval_seconds"]
                if applied_interval == 4:
                    break
                time.sleep(0.5)
            if applied_interval != 4:
                raise RuntimeError(f"Connected Agent did not apply dynamic interval: {applied_interval}")
            shared_latency_result = None
            latency_deadline = time.monotonic() + 35
            while time.monotonic() < latency_deadline:
                latency_overview = context.request.get(BASE_URL + "/api/v1/public/latency")
                if latency_overview.status == 200:
                    latency_data = latency_overview.json()["data"]
                    shared_node = next(
                        (item for item in latency_data.get("probe_nodes", []) if item.get("id") == node_id),
                        None,
                    )
                    shared_latency_result = next(
                        (item for item in latency_data.get("latest", []) if item.get("probe_node_id") == node_id),
                        None,
                    )
                    if shared_node and shared_node.get("role") == "monitor" and shared_latency_result:
                        break
                time.sleep(0.5)
            if not shared_latency_result:
                raise RuntimeError("Latency-enabled monitor did not produce a matrix result")
            page.goto(f"{BASE_URL}/admin/latency", wait_until="networkidle")
            shared_measurement_row = page.locator(".latency-node-table tbody tr").filter(has_text="browser-edge")
            shared_measurement_row.get_by_text("Monitor + latency", exact=False).wait_for(timeout=15_000)
            shared_measurement_row.get_by_role("button", name="Disable latency", exact=True).wait_for()
            page.goto(BASE_URL, wait_until="networkidle")
            quota_card = page.locator("a.node-card").filter(has_text="browser-edge")
            quota_card.get_by_text("Monthly used", exact=True).wait_for()
            usage_meter = quota_card.get_by_role("meter", name="Monthly traffic used")
            usage_meter.wait_for()
            if "GiB" not in quota_card.locator(".traffic-usage").inner_text():
                raise RuntimeError(f"Monthly traffic usage did not render in GiB: {quota_card.inner_text()}")
            if usage_meter.get_attribute("aria-valuenow") != "2":
                raise RuntimeError(f"2% monthly usage rendered as {usage_meter.get_attribute('aria-valuenow')!r}")
            page.screenshot(path="/tmp/hostpin-overview-traffic-grid.png", full_page=True)
            page.get_by_role("button", name="Table view").click()
            quota_row = page.locator(".node-table tbody tr").filter(has_text="browser-edge")
            quota_row.locator(".traffic-usage.compact").wait_for()
            if "GiB" not in quota_row.locator(".traffic-usage").inner_text():
                raise RuntimeError(f"Table monthly traffic usage did not render in GiB: {quota_row.inner_text()}")
            page.screenshot(path="/tmp/hostpin-overview-traffic-table.png", full_page=True)
            page.get_by_role("button", name="Grid view").click()
            page.goto(f"{BASE_URL}/nodes/{node_id}", wait_until="networkidle")
            page.get_by_text("Monthly traffic / MAX", exact=True).wait_for()
            page.get_by_role("meter", name="Monthly traffic quota used").wait_for()
            page.get_by_text("Reset on UTC day 1", exact=True).wait_for()
            page.goto(f"{BASE_URL}/admin/alerts", wait_until="networkidle")
            page.get_by_role("heading", name="Alert rules", exact=True).wait_for()
            try:
                page.get_by_text("Node offline", exact=True).wait_for(timeout=10_000)
            except Exception as reason:
                raise RuntimeError(
                    f"Alert rules did not render at {page.url}:\n{page.locator('body').inner_text()}\n"
                    f"console={console_errors}\nrequests={request_failures}"
                ) from reason
            page.goto(f"{BASE_URL}/admin/security", wait_until="networkidle")
            page.get_by_role("heading", name="Access security", exact=True).wait_for()
            page.get_by_text("This session", exact=True).wait_for()
            page.goto(f"{BASE_URL}/admin/settings", wait_until="networkidle")
            page.get_by_role("heading", name="System settings", exact=True).wait_for()
            page.get_by_text("Weak enrollment PIN", exact=True).wait_for()
            page.locator('.temporary-pin-status[data-status="used"]').wait_for()
            page.get_by_role("button", name="Revoke now", exact=True).click()
            page.locator('.temporary-pin-status[data-status="revoked"]').wait_for()

            stop_agent(agent)
            agent = None
            page.goto(BASE_URL, wait_until="networkidle")
            page.locator("a.node-card.offline").filter(has_text="browser-edge").wait_for(timeout=20_000)

            mobile = context.new_page()
            for viewport_width in (320, 390, 430, 560):
                mobile.set_viewport_size({"width": viewport_width, "height": 844})
                mobile.goto(BASE_URL, wait_until="networkidle")
                mobile.get_by_text("browser-edge", exact=True).wait_for(timeout=15_000)
                mobile_nav = mobile.get_by_role("navigation", name="Global navigation")
                node_link = mobile_nav.get_by_role("link", name="Nodes")
                latency_link = mobile_nav.get_by_role("link", name="Latency")
                account_link = mobile.locator(".public-header .text-link")
                node_box = node_link.bounding_box()
                latency_box = latency_link.bounding_box()
                account_box = account_link.bounding_box()
                if not node_box or not latency_box or not account_box:
                    raise RuntimeError(f"Mobile header links were not measurable at {viewport_width}px")
                if abs(node_box["y"] - latency_box["y"]) > 1:
                    raise RuntimeError(f"Nodes and Latency split across rows at {viewport_width}px: {node_box}, {latency_box}")
                if account_box["y"] + account_box["height"] > node_box["y"] + 1:
                    raise RuntimeError(f"Account link left the top row at {viewport_width}px: {account_box}, nav={node_box}")
                if abs(node_box["width"] - latency_box["width"]) > 1 or node_box["x"] < -1 or latency_box["x"] + latency_box["width"] > viewport_width + 1:
                    raise RuntimeError(f"Mobile primary navigation was not two equal columns at {viewport_width}px: {node_box}, {latency_box}")
            mobile.set_viewport_size({"width": 390, "height": 844})
            mobile.goto(BASE_URL, wait_until="networkidle")
            mobile.get_by_text("browser-edge", exact=True).wait_for(timeout=15_000)
            mobile.screenshot(path="/tmp/hostpin-mobile.png", full_page=True)
            mobile.goto(f"{BASE_URL}/latency", wait_until="networkidle")
            mobile.locator(".latency-mobile-matrix").wait_for()
            mobile_route = mobile.locator(".latency-mobile-matrix").get_by_role("button", name="browser-router to browser-edge latency")
            mobile_route.wait_for()
            mobile_route_text = mobile_route.inner_text().upper()
            if "NO SAMPLE" in mobile_route_text or "% LOSS" not in mobile_route_text:
                raise RuntimeError(f"Mobile latency route did not expose its result: {mobile_route.inner_text()}")
            mobile.screenshot(path="/tmp/hostpin-latency-mobile.png", full_page=True)
            mobile.close()

            stop_agent(probe_agent)
            probe_agent = None
            browser.close()

    stop_agent(agent)
    stop_agent(probe_agent)
    ignored_failures = [item for item in request_failures if "/api/v1/public/live" not in item]
    if console_errors or ignored_failures:
        print(json.dumps({"console_errors": console_errors, "request_failures": ignored_failures}, indent=2), file=sys.stderr)
        return 1
    print(json.dumps({"status": "ok", "screenshots": ["/tmp/hostpin-overview.png", "/tmp/hostpin-overview-traffic-grid.png", "/tmp/hostpin-overview-traffic-table.png", "/tmp/hostpin-node.png", "/tmp/hostpin-node-dark.png", "/tmp/hostpin-latency.png", "/tmp/hostpin-latency-admin.png", "/tmp/hostpin-temporary-pin.png", "/tmp/hostpin-temporary-pin-mobile.png", "/tmp/hostpin-mobile.png", "/tmp/hostpin-latency-mobile.png"]}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
