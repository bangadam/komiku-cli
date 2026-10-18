#!/usr/bin/env python3
"""Deterministic browser acceptance test for the online reader.

Starts the loopback Komiku fixture (real HTTP pages and images), an isolated
HOME/library, and the Vite dev server, then drives the reader through every
mode. Nothing touches the live site, the user's library, or ports it does not
own.

Usage:
    python3 gui/e2e/reader_browser_test.py            # headless
    python3 gui/e2e/reader_browser_test.py --headed   # watch it run
"""
from __future__ import annotations

import argparse
import json
import os
import signal
import socket
import subprocess
import sys
import time
from pathlib import Path

from playwright.sync_api import Page, sync_playwright

E2E = Path(__file__).resolve().parent
GUI = E2E.parent
REPO = GUI.parent

results: list[tuple[str, bool, str]] = []


def check(name: str, ok: bool, detail: str = "") -> None:
    results.append((name, bool(ok), detail))
    print(f"[{'PASS' if ok else 'FAIL'}] {name}" + (f" — {detail}" if detail and not ok else ""))


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_for(url: str, timeout: float = 90.0) -> None:
    import urllib.request

    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2) as response:
                if response.status < 500:
                    return
        except Exception:
            time.sleep(0.3)
    raise RuntimeError(f"{url} did not become ready")


def start_fixture() -> tuple[subprocess.Popen, dict]:
    process = subprocess.Popen(
        [sys.executable, "-B", str(E2E / "reader_fixture.py"), "--image-delay", "0.01", "--slow-delay", "0.2"],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        preexec_fn=os.setsid,
    )
    assert process.stdout is not None
    line = process.stdout.readline()
    if not line:
        stderr = process.stderr.read() if process.stderr else ""
        raise RuntimeError(f"fixture did not start: {stderr[:400]}")
    return process, json.loads(line)


def record_url(library: str, slug: str, url: str) -> None:
    """Give the series a recorded source URL, as a real download would."""
    target = Path(library) / slug / ".pack.json"
    target.write_text(json.dumps({
        "version": 1,
        "series": slug,
        "series_url": url,
        "mappings": [{"volume": 1, "start": 1, "end": 2, "provenance": "download-mapping"}],
        "chapters": [],
    }, indent=2))


def start_vite(home: str, port: int) -> subprocess.Popen:
    env = {
        **os.environ,
        "HOME": home,
        "KOMIKU_CLI_BIN": str(REPO / "komiku-cli"),
        "KOMIKU_CLI_CWD": str(REPO),
    }
    process = subprocess.Popen(
        ["pnpm", "dev", "--host", "127.0.0.1", "--port", str(port)],
        cwd=str(GUI),
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        preexec_fn=os.setsid,
    )
    wait_for(f"http://127.0.0.1:{port}/")
    return process


def stop(process: subprocess.Popen) -> None:
    try:
        os.killpg(os.getpgid(process.pid), signal.SIGTERM)
    except ProcessLookupError:
        pass


def run(page: Page, base: str) -> None:
    page.goto(base)
    page.wait_for_selector('[data-nav="library"]', timeout=30000)

    # --- open the series from the library ---------------------------------
    page.locator("main button", has_text="Reader Fixture").first.click()
    page.wait_for_selector("[data-chapter]", timeout=30000)
    check("series page loads the fixture chapters", page.locator("[data-chapter]").count() == 5)

    # --- reader opens the requested chapter -------------------------------
    page.locator('[data-read-chapter="1"]').click()
    page.wait_for_selector("[data-reader-counter]", timeout=45000)
    # Wait for the report itself, not just the chrome: the counter exists while
    # the chapter is still loading.
    page.wait_for_function(
        "() => document.querySelector('[data-reader-counter]').textContent.includes('/')",
        timeout=45000,
    )
    page.wait_for_timeout(600)
    header = page.locator("header").inner_text()
    check("reader opens the requested chapter", "Chapter 1" in header, header.replace("\n", " | "))
    check("reader reports a local source", "Offline" in header, header.replace("\n", " | "))

    counter = page.locator("[data-reader-counter]").inner_text().replace("\n", " ")
    check("reader shows the page counter", counter.strip() == "1 / 3", counter)

    image = page.locator("img[data-page-image]").first
    image.wait_for(state="visible", timeout=30000)
    natural = image.evaluate("e => [e.naturalWidth, e.naturalHeight]")
    check("reader decodes a real page image", natural[0] == 800 and natural[1] == 1200, str(natural))
    check("page image uses a local blob", image.get_attribute("src").startswith("blob:"))

    # --- keyboard and edge navigation ------------------------------------
    page.keyboard.press("ArrowRight")
    page.wait_for_timeout(500)
    check(
        "arrow key advances the page",
        page.locator("[data-reader-counter]").inner_text().strip() == "2 / 3",
        page.locator("[data-reader-counter]").inner_text(),
    )
    page.locator('[data-page-edge="left"]').click()
    page.wait_for_timeout(400)
    check(
        "left edge returns to page 1",
        page.locator("[data-reader-counter]").inner_text().strip() == "1 / 3",
    )

    # --- modes ------------------------------------------------------------
    page.get_by_role("button", name="Spread").click()
    page.wait_for_timeout(700)
    spread = page.locator("[data-reader-counter]").inner_text()
    check("spread shows a page range", "–" in spread, spread)
    check("spread renders two pages", page.locator("img[data-page-image]").count() == 2)

    page.get_by_role("button", name="Strip").click()
    page.wait_for_timeout(900)
    check("strip lays out every page", page.locator("[data-reader-page]").count() == 3)
    page.get_by_role("button", name="Single").click()
    page.wait_for_timeout(400)

    # --- chapter navigation ----------------------------------------------
    page.locator('[data-nav="reader-next-chapter"]').click()
    page.wait_for_selector("[data-reader-counter]", timeout=45000)
    page.wait_for_timeout(1200)
    header = page.locator("header").inner_text()
    check("next chapter navigates", "Chapter 2" in header, header.replace("\n", " | "))

    # --- drawer ------------------------------------------------------------
    page.locator('[data-nav="reader-chapters"]').click()
    page.wait_for_selector("[data-chapter-row]", timeout=30000)
    rows = 0
    for _ in range(30):
        rows = page.locator("[data-chapter-row]").count()
        if rows >= 5:
            break
        page.wait_for_timeout(400)
    check("drawer lists every chapter", rows == 5, str(rows))
    check(
        "drawer marks the current chapter",
        page.locator('[data-chapter-row="2"]').get_attribute("aria-current") == "page",
    )
    page.locator('[data-chapter-filter="disk"]').click()
    page.wait_for_timeout(400)
    check("on-disk filter narrows the list", page.locator("[data-chapter-row]").count() == 2)
    page.locator('[data-chapter-filter="all"]').click()
    page.get_by_placeholder("Cari chapter…").fill("79")
    page.wait_for_timeout(400)
    check(
        "search finds the fractional chapter",
        page.locator('[data-chapter-row="79.2"]').count() == 1,
    )
    page.get_by_role("button", name="Close chapters").click()
    page.wait_for_timeout(300)

    # --- settings ---------------------------------------------------------
    page.locator('[data-nav="reader-settings"]').click()
    page.wait_for_selector("text=Reader settings", timeout=15000)
    for label in ["Reading mode", "Background", "Fit", "Page gap", "Zoom"]:
        check(f"settings has {label}", label in page.locator("body").inner_text())

    page.get_by_role("button", name="Paper").click()
    page.wait_for_timeout(300)
    check(
        "background preference applies",
        page.locator("[data-reader-root]").get_attribute("data-reader-background") == "paper",
    )
    page.get_by_role("switch", name="Right-to-left (manga)").click()
    page.wait_for_timeout(300)
    prefs = json.loads(page.evaluate("() => localStorage.getItem('komiku.reader.prefs')"))
    check("right-to-left toggle persists", prefs["rightToLeft"] is True, json.dumps(prefs))
    page.get_by_role("button", name="Dark").click()
    page.get_by_role("switch", name="Right-to-left (manga)").click()
    page.locator('[aria-label="Close settings"]').click()
    page.wait_for_timeout(300)

    # --- progress persistence --------------------------------------------
    progress = json.loads(page.evaluate("() => localStorage.getItem('komiku.reader.progress')"))
    check(
        "reading progress is stored per chapter",
        any(key.startswith("reader-fixture/") for key in progress),
        json.dumps(progress)[:160],
    )
    page.reload()
    page.wait_for_selector('[data-nav="library"]', timeout=30000)
    check(
        "preferences survive a reload",
        json.loads(page.evaluate("() => localStorage.getItem('komiku.reader.prefs')"))["background"] == "dark",
    )

    # --- failure recovery -------------------------------------------------
    page.locator("main button", has_text="Reader Fixture").first.click()
    page.wait_for_selector("[data-chapter]", timeout=30000)
    page.locator('[data-read-chapter="3"]').click()
    page.wait_for_function(
        "() => document.querySelector('[data-reader-counter]')?.textContent.includes('/')",
        timeout=90000,
    )
    page.wait_for_timeout(3000)
    header = page.locator("header").inner_text()
    check("remote chapter loads into cache", "Chapter 3" in header, header.replace("\n", " | "))
    ready = page.locator('[data-page-ready="true"]').count()
    check("remote pages become visible", ready >= 1, str(ready))

    page.locator('[data-nav="reader-back"]').click()
    page.wait_for_selector("[data-chapter]", timeout=30000)
    check("back returns to the series", "Reader Fixture" in page.locator("body").inner_text())


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--headed", action="store_true")
    args = parser.parse_args()

    fixture = vite = None
    try:
        fixture, info = start_fixture()
        record_url(info["library"], "reader-fixture", info["seriesURL"])
        record_url(info["library"], "reader-second", info["secondSeriesURL"])
        port = free_port()
        vite = start_vite(info["environment"]["HOME"], port)
        base = f"http://127.0.0.1:{port}"

        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=not args.headed)
            context = browser.new_context(viewport={"width": 1440, "height": 900})
            page = context.new_page()
            errors: list[str] = []
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.on("console", lambda message: errors.append(message.text) if message.type == "error" else None)
            run(page, base)
            browser.close()

        check("no page errors", not errors, "; ".join(errors[:3]))
    finally:
        if vite:
            stop(vite)
        if fixture:
            stop(fixture)

    failed = [row for row in results if not row[1]]
    print(f"\n{len(results) - len(failed)}/{len(results)} checks passed")
    for name, _, detail in failed:
        print(f"  - {name}: {detail}")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
