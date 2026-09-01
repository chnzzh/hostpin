#!/usr/bin/env python3
"""Browser acceptance test for encrypted SQLite export/import and live reload."""

from __future__ import annotations

import os
import tempfile
from pathlib import Path

from playwright.sync_api import sync_playwright


BASE_URL = os.environ.get("HOSTPIN_E2E_BASE_URL", "http://127.0.0.1:18084")
ADMIN_PASSWORD = "hostpin-backup-admin-password"
BACKUP_PASSPHRASE = "hostpin portable backup passphrase"


def field(page, text: str):
    return page.locator(f'label:has-text("{text}") input').first


def main() -> None:
    download_fd, raw_download_path = tempfile.mkstemp(prefix="hostpin-e2e-", suffix=".hostpin-backup")
    os.close(download_fd)
    download_path = Path(raw_download_path)
    try:
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=True)
            context = browser.new_context(locale="en-US", viewport={"width": 1440, "height": 1000})
            page = context.new_page()
            page.goto(f"{BASE_URL}/setup")
            page.wait_for_load_state("networkidle")
            field(page, "SITE NAME").fill("BACKUP SOURCE SITE")
            field(page, "PASSWORD").fill(ADMIN_PASSWORD)
            field(page, "CONFIRM PASSWORD").fill(ADMIN_PASSWORD)
            field(page, "ENROLLMENT PIN").fill("246810backup")
            page.get_by_role("button", name="COMPLETE SETUP").click()
            page.wait_for_url("**/admin**")

            page.goto(f"{BASE_URL}/admin/backups")
            page.wait_for_load_state("networkidle")
            page.get_by_role("heading", name="Backup & restore").wait_for()
            assert "SQLITE" in page.locator(".backup-status-strip").inner_text()
            page.screenshot(path="/tmp/hostpin-backup-restore.png", full_page=True)
            page.set_viewport_size({"width": 390, "height": 844})
            page.evaluate("window.scrollTo(0, 0)")
            page.screenshot(path="/tmp/hostpin-backup-restore-mobile.png", full_page=True)
            assert page.get_by_role("button", name="EXPORT ENCRYPTED BACKUP").is_visible()
            assert page.get_by_role("button", name="IMPORT AND REPLACE").is_visible()
            page.set_viewport_size({"width": 1440, "height": 1000})

            field(page, "CURRENT ADMIN PASSWORD").first.fill(ADMIN_PASSWORD)
            field(page, "BACKUP PASSPHRASE (12+)").fill(BACKUP_PASSPHRASE)
            field(page, "REPEAT BACKUP PASSPHRASE").fill(BACKUP_PASSPHRASE)
            with page.expect_download() as download_info:
                page.get_by_role("button", name="EXPORT ENCRYPTED BACKUP").click()
            download_info.value.save_as(download_path)
            assert download_path.stat().st_size > 256
            page.get_by_text("Encrypted backup exported as").wait_for()

            page.goto(f"{BASE_URL}/admin/settings")
            page.wait_for_load_state("networkidle")
            field(page, "SITE NAME").fill("MUTATED AFTER EXPORT")
            page.get_by_role("button", name="SAVE SETTINGS").click()
            page.get_by_text("Settings saved.").wait_for()

            page.goto(f"{BASE_URL}/admin/backups")
            page.wait_for_load_state("networkidle")
            page.locator('input[type="file"]').set_input_files(download_path)
            page.locator('.restore-operation label:has-text("BACKUP PASSPHRASE") input').fill(BACKUP_PASSPHRASE)
            page.locator('.restore-operation label:has-text("CURRENT ADMIN PASSWORD") input').fill(ADMIN_PASSWORD)
            field(page, "TYPE RESTORE").fill("RESTORE")
            page.get_by_role("button", name="IMPORT AND REPLACE").click()
            page.wait_for_url("**/login?restored=1", timeout=90_000)

            field(page, "USERNAME").fill("admin")
            field(page, "PASSWORD").fill(ADMIN_PASSWORD)
            page.get_by_role("button", name="SIGN IN").click()
            page.wait_for_url("**/admin**")
            page.goto(f"{BASE_URL}/admin/settings")
            page.wait_for_load_state("networkidle")
            assert field(page, "SITE NAME").input_value() == "BACKUP SOURCE SITE"
            context.close()
            browser.close()
        print("backup/restore browser flow passed")
    finally:
        download_path.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
