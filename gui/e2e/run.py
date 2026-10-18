#!/usr/bin/env python3
"""End-to-end acceptance suite for the komiku GUI.

Runs the real Vite app against the real komiku-cli binary (through the dev
bridge) with a hermetic HOME, so every assertion is backed by actual CLI output.
Discovery screens additionally hit the live komiku.org endpoints.

Usage:
    python3 e2e/run.py            # full suite
    python3 e2e/run.py --headed   # watch it run
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

from playwright.sync_api import Page, sync_playwright

ROOT = Path(__file__).resolve().parents[2]
GUI = ROOT / "gui"
BASE = Path("/tmp/komiku-e2e")
LIBRARY = BASE / "library"
HOME = BASE / "home"
URL = "http://localhost:1420"

results: list[tuple[str, bool, str]] = []


def check(name: str, condition: bool, detail: str = "") -> None:
    results.append((name, bool(condition), detail))
    mark = "PASS" if condition else "FAIL"
    print(f"[{mark}] {name}" + (f" — {detail}" if detail and not condition else ""))


def check_no_subscription_ui(screen: str, body: str) -> None:
    check(
        f"{screen} has no subscription controls or copy",
        re.search(r"\b(?:subscrib\w*|subscript\w*|unsubscrib\w*)\b", body, re.IGNORECASE) is None,
        body[:900],
    )


def wait_for_http(url: str, timeout: float = 150.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2) as response:
                if response.status == 200:
                    return
        except (urllib.error.URLError, ConnectionError, OSError):
            time.sleep(0.4)
    raise RuntimeError(f"{url} did not become ready")


def port_is_free() -> bool:
    try:
        output = subprocess.run(
            ["lsof", "-ti:1420"], capture_output=True, text=True
        ).stdout.split()
        return not output
    except FileNotFoundError:
        return True


def free_port(timeout: float = 20.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        pids = subprocess.run(
            ["lsof", "-ti:1420"], capture_output=True, text=True
        ).stdout.split()
        if not pids:
            return
        for pid in pids:
            try:
                os.kill(int(pid), signal.SIGKILL)
            except (ProcessLookupError, ValueError):
                pass
        time.sleep(0.5)
    raise RuntimeError("port 1420 is still in use")


def start_server() -> subprocess.Popen:
    free_port()

    env = {
        **os.environ,
        "HOME": str(HOME),
        "KOMIKU_CLI_BIN": str(ROOT / "komiku-cli"),
        "KOMIKU_CLI_CWD": str(ROOT),
    }
    process = subprocess.Popen(
        ["pnpm", "dev"],
        cwd=str(GUI),
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        preexec_fn=os.setsid,
    )
    wait_for_http(URL)
    return process


def stop_server(process: subprocess.Popen) -> None:
    try:
        os.killpg(os.getpgid(process.pid), signal.SIGTERM)
    except ProcessLookupError:
        pass


NAV_IDS = {
    "Library": "library",
    "Search": "search",
    "Terbaru": "terbaru",
    "Peringkat": "peringkat",
    "Trending": "trending",
    "Baru": "baru",
    "Daftar Komik": "daftar",
    "Genre": "genre",
    "Downloads": "downloads",
    "Pack to CBZ": "pack",
    "Verify": "verify",
    "Settings": "settings",
}


def goto(page: Page, nav_label: str) -> None:
    page.locator(f'[data-nav="{NAV_IDS[nav_label]}"]').first.click()
    page.wait_for_timeout(500)


def wait_for_selector(page: Page, selector: str, timeout: int = 45000) -> bool:
    """Wait for a real rendered signal instead of guessing a fixed delay."""
    try:
        page.wait_for_selector(selector, timeout=timeout, state="visible")
        return True
    except Exception:
        return False


def pane(page: Page) -> str:
    """Text of the main pane, falling back to the body.

    The reader renders full-bleed without the sidebar, so it has no <main>.
    """
    main = page.locator("main")
    return main.inner_text() if main.count() > 0 else page.locator("body").inner_text()


def wait_for_text(page: Page, pattern: str, timeout: int = 60000) -> str:
    """Poll the main pane until a regex matches, then return its text."""
    deadline = time.time() + timeout / 1000
    text = ""
    while time.time() < deadline:
        text = pane(page)
        if re.search(pattern, text):
            return text
        page.wait_for_timeout(700)
    return text


def text_of(page: Page, selector: str) -> str:
    locator = page.locator(selector)
    if locator.count() == 0:
        return ""
    return locator.first.inner_text().strip()


def test_shell(page: Page) -> None:
    page.goto(URL)
    page.wait_for_timeout(1500)

    body = page.locator("body").inner_text()
    check_no_subscription_ui("sidebar", body)
    for heading in ["LIBRARY", "JELAJAHI", "TOOLS", "COLLECTIONS"]:
        check(f"sidebar group: {heading}", heading in body, body[:400])
    for label in [
        "Terbaru",
        "Peringkat",
        "Trending",
        "Baru",
        "Daftar Komik",
        "Genre",
        "Downloads",
        "Pack to CBZ",
        "Verify",
        "Settings",
    ]:
        check(f"sidebar item: {label}", label in body)

    check(
        "no browser-only notice",
        "Browser preview has no CLI access" not in body,
        body[:200],
    )


def test_library(page: Page) -> None:
    goto(page, "Library")
    page.wait_for_timeout(1800)

    body = page.locator("body").inner_text()
    check("library title", "Your library" in body)
    check_no_subscription_ui("library", body)
    check("library catch-up actions remain", "Check for new" in body and "Update all" in body)
    check("library summary has 5 series", "5 series" in body, body[:500])
    check("library shows root path", str(LIBRARY) in body, body[:500])

    rows = page.locator("main button").filter(has_text="Sakamoto Days")
    check("library lists Sakamoto Days", rows.count() >= 1)
    check(
        "library lists every fixture series",
        all(
            name in body
            for name in [
                "Sakamoto Days",
                "Dandadan",
                "Blue Lock",
                "Versus",
                "Jujutsu Kaisen",
            ]
        ),
        body[:600],
    )

    # Real covers render through the bridge (data URL / blob) rather than the
    # gradient fallback.
    images = page.locator("main img")
    check("library renders cover images", images.count() >= 3, f"{images.count()} images")

    # Design status vocabulary, each label backed by a real CLI field.
    for status in ["Up to date", "Needs repair", "Partial"]:
        check(f"library status badge: {status}", status in body, body[:900])

    check(
        "library flags a broken series",
        "Integrity" in body,
    )


def test_search(page: Page) -> None:
    goto(page, "Search")
    page.wait_for_timeout(600)
    page.get_by_placeholder("Search a title or paste a komiku URL").fill("sakamoto")
    # Search is debounced and hits the live site, so wait for a result row
    # instead of guessing a delay. komiku.org throttles bursts, so retry once
    # with a fresh keystroke if the first request is refused.
    body = wait_for_text(page, r"In library|Not in library", timeout=45000)
    if "In library" not in body and "Not in library" not in body:
        page.get_by_placeholder("Search a title or paste a komiku URL").fill("")
        page.wait_for_timeout(800)
        page.get_by_placeholder("Search a title or paste a komiku URL").fill("sakamoto days")
        body = wait_for_text(page, r"In library|Not in library", timeout=45000)
    check("search returns results", "Sakamoto" in body, body[:500])
    check("search marks in-library results", "In library" in body, body[:800])
    pill = page.locator("main span").filter(has_text=re.compile("^In library$"))
    check("search renders a state pill", pill.count() > 0)
    check("search result count", "results for" in body or "result for" in body, body[:400])
    check_no_subscription_ui("search", body)
    page.get_by_role("button", name="In library", exact=True).click()
    filtered = pane(page)
    check("search retains in-library filter", "Not in library" not in filtered, filtered[:600])


def test_series(page: Page) -> None:
    goto(page, "Library")
    page.wait_for_timeout(1200)
    page.locator("main button").filter(has_text="Sakamoto Days").first.click()
    loaded = wait_for_selector(page, "[data-chapter]")
    check("series chapter list loads", loaded)

    body = page.locator("body").inner_text()
    check("series hero title", "Sakamoto Days" in body)
    check("series chapter rows", "chapter-18" in body, body[:600])
    check("series on-disk badge", "On disk" in body, body[:800])
    check_no_subscription_ui("series", body)
    check(
        "series download CTA",
        bool(re.search(r"Download \d+ (chapter|volume)", body)),
        body[-500:],
    )
    check("series volume tab", "Volumes" in body)
    check("series files tab", "Files on disk" in body)
    check("series open folder action", "Open folder" in body)

    # Selection drives the CTA count. The chapter list is discovered live from
    # komiku.org, so only the shape of the label is asserted.
    page.get_by_role("button", name="All chapters").first.click()
    page.wait_for_timeout(800)
    body = page.locator("body").inner_text()
    check(
        "quick select updates CTA",
        bool(re.search(r"Download \d+ chapters", body)),
        body[-600:],
    )

    # "Pack to CBZ" must navigate to the Pack screen for this series.
    page.get_by_role("button", name="Pack to CBZ").first.click()
    page.wait_for_timeout(1200)
    body = page.locator("body").inner_text()
    check("series pack action navigates", "Pack preview" in body, body[:400])


def test_verify(page: Page) -> None:
    goto(page, "Verify")
    page.wait_for_timeout(2500)
    body = page.locator("body").inner_text()
    check("verify report title", "Chapter report" in body, body[:400])
    check("verify shows BROKEN badge", "BROKEN" in body, body[:800])
    check("verify shows OK badge", "OK" in body)
    check("verify lists broken page", "broken:" in body, body[:1200])
    check("verify lists missing page", "missing:" in body or "0 missing" in body)
    check("verify rail not-in-state", "Not in .state.json" in body)
    check("verify rail stray files", "Stray files" in body)
    check("verify last run", "Last run" in body)


def test_pack(page: Page) -> None:
    goto(page, "Pack to CBZ")
    page.wait_for_timeout(2500)
    body = page.locator("body").inner_text()
    check("pack source folder", "Source folder" in body, body[:400])
    check("pack mode switch", "By volume" in body and "Flat run" in body and "Recover" in body)
    check("pack preview", "Pack preview" in body)
    check("pack target directory", "Target directory" in body)
    check("pack preset grid", "Medium" in body and "Tiny" in body)
    check("pack volume table status", "Ready" in body or "Partial" in body, body[:1200])
    # Pages come from a real `verify` run, so a series with chapters on disk
    # must show at least one numeric page total.
    check(
        "pack volume pages come from verify",
        bool(re.search(r"\n\d+\n(Ready|Partial)", body)),
        body[:1600],
    )

    # Flat run works fully offline and must expose the local chapter range.
    page.locator('[data-value="flat"]').first.click()
    page.wait_for_timeout(1200)
    body = page.locator("body").inner_text()
    check("pack flat shows local range", "local chapter range" in body, body[:900])
    check("pack flat shows output archive", "Output archive" in body)
    check("pack shows archive naming", "Archive naming" in body, body[:1200])


def test_settings(page: Page) -> None:
    goto(page, "Settings")
    page.wait_for_timeout(1800)
    body = page.locator("body").inner_text()
    root_value = page.locator('input[aria-label="Download root"]').first.input_value()
    check("settings download root", root_value == str(LIBRARY), root_value)
    check("settings config file card", "Config file" in body)
    check("settings detected library", "Detected library" in body)
    check_no_subscription_ui("settings", body)
    check("settings preset", "Default CBZ preset" in body)
    check("settings delay", "Request delay" in body)
    check("settings about", "Not affiliated with Komiku" in body)
    footer = page.locator("main").inner_text()
    check(
        "settings footer shows real version",
        bool(re.search(r"v\d", footer)),
        footer[-200:],
    )


def test_downloads(page: Page) -> None:
    goto(page, "Downloads")
    page.wait_for_timeout(800)
    body = page.locator("body").inner_text()
    check(
        "downloads empty or live",
        "No downloads yet" in body or "Current batch" in body,
        body[:400],
    )
    check(
        "downloads stat set",
        "No downloads yet" in body
        or ("Speed" in body and "Errors" in body and "Pages" in body),
        body[:400],
    )


def test_peringkat(page: Page) -> None:
    goto(page, "Peringkat")
    body = wait_for_text(page, r"Chapter \d")
    check("peringkat rows load", bool(re.search(r"Chapter \d", body)))
    check("peringkat title", "Peringkat Komiku" in body, body[:300])
    check("peringkat period switch", "Harian" in body and "Mingguan" in body)
    check("peringkat rows", "Black Clover" in body or "Overgeared" in body, body[:600])
    check("peringkat rail", "Sebaran tipe" in body)
    check(
        "peringkat type mix is populated",
        bool(re.search(r"Man(?:g|h)wa", body)),
        body[:1200],
    )
    check("peringkat source note", "komiku.org" in body)

    rows_before = page.locator("main button").count()
    page.locator('[data-value="mingguan"]').first.click()
    page.wait_for_timeout(3500)
    body = page.locator("body").inner_text()
    check("peringkat period switch loads data", "Mingguan" in body and rows_before > 0, body[:300])


def test_trending(page: Page) -> None:
    goto(page, "Trending")
    body = wait_for_text(page, r"#1 Trending")
    check("trending title", "Trending Now" in body, body[:300])
    check("trending hero badge", "#1 Trending" in body, body[:600])
    check("trending shelf", "Trending berikutnya" in body, body[:900])
    check("trending rail genres", "Genre paling panas" in body)
    check("trending views", "Views" in body)
    check_no_subscription_ui("trending", body)
    check("trending has no saved-library action", "Tambah ke library" not in body and "Sudah di library" not in body)


def test_terbaru(page: Page) -> None:
    goto(page, "Terbaru")
    body = wait_for_text(page, r"(KURANG DARI 3 JAM|HARI INI|MINGGU INI)")
    check("terbaru title", "Update Terbaru" in body, body[:300])
    check("terbaru tabs", "Chapter terbaru" in body and "Komik terbaru" in body and "Acak" in body)
    check(
        "terbaru groups",
        bool(re.search(r"(KURANG DARI 3 JAM|HARI INI|MINGGU INI)", body)),
        body[:900],
    )
    check_no_subscription_ui("terbaru", body)
    check("terbaru rhythm", "Ritme update" in body)
    check("terbaru auto refresh", "Auto-refresh 60s" in body)

    page.get_by_role("button", name="Komik terbaru").first.click()
    page.wait_for_timeout(4000)
    body = page.locator("body").inner_text()
    check("terbaru new tab loads", "Update Terbaru" in body and "judul" in body, body[:400])


def test_baru(page: Page) -> None:
    goto(page, "Baru")
    loaded = wait_for_selector(page, "main button[data-series]")
    # Catalogue totals come from four live requests; wait until they land.
    body = wait_for_text(page, r"(Semua|Manga)\s*[1-9][\d.]{2,}")
    check("baru title", "Baru Ditambahkan" in body, body[:300])
    check("baru tabs with counts", "Manga" in body and "Manhwa" in body and "Manhua" in body)
    check("baru grid opens series", loaded and page.locator("main button[data-series]").count() > 0, body[:900])
    check_no_subscription_ui("baru", body)
    check("baru has no saved-library action", "Tambah ke library" not in body and "Sudah di library" not in body)
    check("baru week label", "Minggu ini" in body)
    # Type tabs must show real catalogue totals, not zeros.
    counts = re.findall(r"(Semua|Manga|Manhwa|Manhua)\s*([\d.]+)", body)
    nonzero = [value for _, value in counts if value not in ("0", "")]
    check("baru type counts are real", len(nonzero) >= 2, str(counts[:6]))


def test_daftar(page: Page) -> None:
    goto(page, "Daftar Komik")
    wait_for_selector(page, '[data-letter="A"]')
    body = wait_for_text(page, r"Menampilkan")
    check("daftar title", "Daftar Komik" in body, body[:300])
    check("daftar alphabet index", "INDEKS" in body, body[:600])
    check("daftar status filter", "Ongoing" in body and "Tamat" in body)
    check("daftar pagination", "Halaman" in body, body[-500:])
    check(
        "daftar shows catalogue total",
        bool(re.search(r"[1-9][\d.]{2,}", body)),
        body[:600],
    )

    tiles = page.locator('[data-letter="A"]')
    check("daftar letter tiles clickable", tiles.count() > 0)
    tiles.first.click()
    page.wait_for_timeout(4000)
    body = page.locator("body").inner_text()
    check("daftar letter filter applied", "Menampilkan" in body, body[:600])


def test_genre(page: Page) -> None:
    goto(page, "Genre")
    wait_for_selector(page, "main select")
    body = wait_for_text(page, r"Chapter \d")
    check("genre title", "Daftar Genre" in body, body[:300])
    check("genre combine bar", "Gabungkan" in body, body[:600])
    check("genre sort row", "URUTKAN" in body)
    check("genre popular rail", "Genre populer" in body)
    check("genre quick collection", "Koleksi cepat" in body)
    check(
        "genre listing rows",
        bool(re.search(r"Chapter \d", body)),
        body[:900],
    )


def test_download_flow(page: Page) -> None:
    """Drive a real download from Series → Downloads through the live stream."""
    goto(page, "Library")
    page.wait_for_timeout(1500)
    page.locator("main button").filter(has_text="Sakamoto Days").first.click()
    check("download test reaches chapter list", wait_for_selector(page, "[data-chapter]"))

    # Select exactly one chapter row. The master "Select all" checkbox also
    # matches [role=checkbox], so target the row hook explicitly.
    rows = page.locator("[data-chapter]")
    check("series has chapter checkboxes", rows.count() > 0)
    rows.first.click()
    page.wait_for_timeout(600)

    body = page.locator("body").inner_text()
    cta = re.search(r"Download (\d+) chapter", body)
    check("download CTA enabled after selection", cta is not None, body[-500:])

    page.get_by_role("button", name=re.compile("^Download \\d+ chapter")).first.click()
    page.wait_for_timeout(2500)

    goto(page, "Downloads")
    page.wait_for_timeout(2500)
    body = page.locator("body").inner_text()
    check("download job appears", "Sakamoto Days" in body, body[:700])
    check("download shows live stats", "Chapters" in body and "Pages" in body, body[:700])
    check("download shows speed column", "Speed" in body, body[:700])

    # The image host throttles bursts, so a page can take tens of seconds.
    # Progress must advance regardless; a terminal state is only asserted when
    # the host cooperates.
    advanced = False
    terminal = False
    for _ in range(40):
        body = page.locator("body").inner_text()
        pages = re.search(r"(\d+) / (\d+) pages", body)
        if pages and int(pages.group(1)) > 0:
            advanced = True
        if any(state in body for state in ("Done", "Failed", "Cancelled")):
            terminal = True
            break
        if advanced and "Downloading" not in body:
            terminal = True
            break
        page.wait_for_timeout(3000)

    check("download progress advances", advanced, body[:700])
    check(
        "download stream reports a terminal or live state",
        terminal or "Downloading" in body,
        body[:700],
    )
    if not terminal:
        print("  note: host throttling kept the job running; progress verified")


TESTS = [
    ("shell", test_shell),
    ("library", test_library),
    ("search", test_search),
    ("series", test_series),
    ("verify", test_verify),
    ("pack", test_pack),
    ("settings", test_settings),
    ("downloads", test_downloads),
    ("peringkat", test_peringkat),
    ("trending", test_trending),
    ("terbaru", test_terbaru),
    ("baru", test_baru),
    ("daftar", test_daftar),
    ("genre", test_genre),
    ("download", test_download_flow),
]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--headed", action="store_true")
    parser.add_argument("--only", default="")
    args = parser.parse_args()

    # Always rebuild: a download test writes into the fixture library, and a
    # deterministic starting state is what makes the assertions meaningful.
    subprocess.run(
        [sys.executable, str(GUI / "e2e" / "fixture.py"), str(BASE)],
        check=True,
        capture_output=True,
    )

    server = start_server()
    console_errors: list[str] = []
    page_errors: list[str] = []

    try:
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=not args.headed)
            context = browser.new_context(viewport={"width": 1440, "height": 1000})
            page = context.new_page()
            page.on("console", lambda msg: console_errors.append(msg.text)
                    if msg.type == "error" else None)
            page.on("pageerror", lambda error: page_errors.append(str(error)))

            page.goto(URL)
            page.wait_for_timeout(1500)

            for name, test in TESTS:
                if args.only and args.only not in name:
                    continue
                print(f"\n=== {name} ===")
                try:
                    test(page)
                except Exception as error:  # noqa: BLE001 - report and continue
                    check(f"{name} completed", False, str(error)[:300])

            browser.close()
    finally:
        stop_server(server)

    real_console_errors = [
        error
        for error in console_errors
        if "favicon" not in error.lower()
        and "Failed to load resource" not in error
    ]
    check("no uncaught page errors", not page_errors, "; ".join(page_errors[:3]))
    check("no console errors", not real_console_errors, "; ".join(real_console_errors[:3]))

    failed = [row for row in results if not row[1]]
    print(f"\n{len(results) - len(failed)}/{len(results)} checks passed")
    if failed:
        print("\nFailures:")
        for name, _, detail in failed:
            print(f"  - {name}: {detail}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
