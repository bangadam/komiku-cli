#!/usr/bin/env python3
"""Standard-library integration tests against the actual Go reader executable.

No browser driver, frontend mocks, external websites, reused HOME, port killing,
or existing-library deletion. The default builds the CLI in an owned temporary
folder; --binary uses an already built executable. See --help for examples.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import queue
import signal
import struct
import subprocess
import tempfile
import threading
import time
import unittest
from urllib.error import HTTPError
from urllib.parse import quote
from urllib.request import ProxyHandler, Request, build_opener
import zlib

from reader_fixture import CHAPTERS, HEIGHT, MIN_RESUME_SIZE, WIDTH, Options, ReaderFixture


REPO = Path(__file__).resolve().parents[2]
HTTP = build_opener(ProxyHandler({}))


def fetch(url: str, method: str = "GET") -> tuple[int, bytes]:
    try:
        with HTTP.open(Request(url, method=method), timeout=10) as response:
            return response.status, response.read()
    except HTTPError as error:
        with error:
            return error.code, error.read()


def status(fixture: ReaderFixture) -> dict:
    code, body = fetch(fixture.info["statusURL"])
    if code != 200:
        raise AssertionError(f"status endpoint returned {code}: {body!r}")
    return json.loads(body)


def validate_png(data: bytes) -> None:
    """Decode every PNG chunk and full scanline data, not just magic bytes."""
    assert len(data) > MIN_RESUME_SIZE, len(data)
    assert data[:8] == b"\x89PNG\r\n\x1a\n"
    offset, compressed, kinds = 8, bytearray(), []
    while offset < len(data):
        length = struct.unpack_from(">I", data, offset)[0]
        kind = data[offset + 4:offset + 8]
        body = data[offset + 8:offset + 8 + length]
        crc = struct.unpack_from(">I", data, offset + 8 + length)[0]
        assert zlib.crc32(kind + body) == crc, f"bad {kind!r} CRC"
        kinds.append(kind)
        if kind == b"IHDR":
            assert struct.unpack(">IIBBBBB", body) == (WIDTH, HEIGHT, 8, 2, 0, 0, 0)
        if kind == b"IDAT":
            compressed.extend(body)
        offset += length + 12
    assert offset == len(data), "trailing or truncated data"
    assert kinds[0] == b"IHDR" and kinds[-1] == b"IEND"
    decoder = zlib.decompressobj()
    scanlines = decoder.decompress(bytes(compressed)) + decoder.flush()
    assert decoder.eof and not decoder.unused_data and not decoder.unconsumed_tail
    stride = WIDTH * 3 + 1
    assert len(scanlines) == HEIGHT * stride
    assert all(scanlines[row * stride] == 0 for row in range(HEIGHT))
    # Labels occupy the centre of this otherwise light page.
    assert len(set(scanlines[420 * stride:824 * stride])) > 6


class IsolatedTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory(prefix="reader-protocol-")
        self.addCleanup(self.temp.cleanup)

    def fixture(self, **options) -> ReaderFixture:
        fixture = ReaderFixture(Options(**options), Path(self.temp.name)).start()
        self.addCleanup(fixture.close)
        return fixture


class FixtureTests(IsolatedTest):
    def test_real_pngs_and_isolated_configuration(self) -> None:
        fixture = self.fixture(image_delay=0)
        self.assertEqual(fixture.server.server_address[0], "127.0.0.1")
        self.assertGreater(fixture.server.server_address[1], 0)
        for config_path in fixture.config_paths:
            config = json.loads(config_path.read_text())
            self.assertEqual(config["output_root"], str(fixture.library))
        for series, done in (("reader-fixture", [1, 2]), ("reader-second", [1])):
            directory = fixture.library / series
            self.assertEqual(json.loads((directory / ".state.json").read_text()), {"done": done})
            for image in directory.rglob("*.png"):
                validate_png(image.read_bytes())
            for raw, count in CHAPTERS[series].items():
                code, body = fetch(f"{fixture.base_url}/{series}-chapter-{raw}/")
                self.assertEqual(code, 200)
                self.assertEqual(body.count(b'class="reader-page klazy"'), count)
                code, image = fetch(fixture.base_url + fixture.image_path(series, raw, 1))
                self.assertEqual(code, 200)
                validate_png(image)
        another = self.fixture(image_delay=0)
        self.assertNotEqual(another.root, fixture.root)
        self.assertNotEqual(another.server.server_address, fixture.server.server_address)

    def test_status_read_only_routes_and_one_time_failure(self) -> None:
        fixture = self.fixture(page3_failures=1, image_delay=0)
        before = status(fixture)
        self.assertEqual(status(fixture), before)
        for method in ("POST", "PUT", "PATCH", "DELETE"):
            self.assertEqual(fetch(fixture.info["statusURL"], method)[0], 405)
        self.assertEqual(status(fixture), before)
        path = fixture.image_path("reader-fixture", "003", 3)
        self.assertEqual(fetch(fixture.base_url + path)[0], 503)
        self.assertEqual(fetch(fixture.base_url + path)[0], 200)
        after = status(fixture)
        self.assertEqual(after["requests"], {path: 2})
        self.assertEqual(after["failure_count"], 1)
        self.assertTrue(after["failed"])
        self.assertEqual(after["remaining_page3_failures"], 0)
        # Neither query parameters nor methods mutate the failure/offline setup.
        self.assertEqual(json.loads(fetch(fixture.info["statusURL"] + "?offline=1")[1]), after)
        for path in ("/etc/passwd", "/%2e%2e/etc/passwd", "/__fixture/config", quote(str(fixture.config_paths[0]))):
            self.assertEqual(fetch(fixture.base_url + path)[0], 404)

    def test_upstream_offline_is_startup_only(self) -> None:
        fixture = self.fixture(offline=True)
        self.assertEqual(fetch(fixture.info["seriesURL"])[0], 503)
        self.assertEqual(fetch(fixture.base_url + fixture.image_path("reader-fixture", "003", 1))[0], 503)
        self.assertTrue(status(fixture)["offline"])
        self.assertEqual(status(fixture)["total_requests"], 2)


def cli_environment(fixture: ReaderFixture) -> dict[str, str]:
    env = {**os.environ, **fixture.environment, "NO_PROXY": "127.0.0.1,localhost",
           "no_proxy": "127.0.0.1,localhost"}
    for key in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
        env.pop(key, None)
    return env


class ReaderProcess:
    """Consume NDJSON without buffering until process exit; own only this PID."""

    def __init__(self, binary: Path, fixture: ReaderFixture, chapter: str, *flags: str,
                 series_url: str | None = None):
        self.stderr = tempfile.TemporaryFile(mode="w+t", encoding="utf-8", dir=fixture.root)
        self.process = subprocess.Popen(
            [str(binary), "read", series_url or fixture.info["seriesURL"],
             "--ch", chapter, "--progress", *flags],
            cwd=fixture.root, env=cli_environment(fixture), stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE, stderr=self.stderr, text=True, encoding="utf-8", bufsize=1)
        self.lines: queue.Queue[str | None] = queue.Queue()
        self.events: list[dict] = []

        def read_stdout() -> None:
            try:
                for line in self.process.stdout:
                    self.lines.put(line)
            finally:
                self.lines.put(None)

        self.thread = threading.Thread(target=read_stdout, daemon=True)
        self.thread.start()

    def event(self, timeout: float = 10) -> dict | None:
        try:
            line = self.lines.get(timeout=timeout)
        except queue.Empty as error:
            raise AssertionError(f"CLI emitted no NDJSON within {timeout:.1f}s") from error
        if line is None:
            return None
        event = json.loads(line)
        if not isinstance(event, dict) or "kind" not in event:
            raise AssertionError(f"not an NDJSON event: {line!r}")
        self.events.append(event)
        return event

    def finish(self, timeout: float = 40) -> tuple[int, list[dict], str]:
        deadline = time.monotonic() + timeout
        while self.event(max(0.01, deadline - time.monotonic())) is not None:
            if time.monotonic() >= deadline:
                raise AssertionError("CLI did not finish before its deadline")
        code = self.process.wait(timeout=max(0.01, deadline - time.monotonic()))
        self.stderr.seek(0)
        return code, self.events, self.stderr.read()

    def close(self) -> None:
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()  # Only the child created above, never any port owner.
                self.process.wait(timeout=5)
        self.thread.join(timeout=5)
        self.process.stdout.close()
        self.stderr.close()


class ReaderProtocolTests(IsolatedTest):
    binary: Path

    def launch(self, fixture: ReaderFixture, chapter: str, *flags: str, **kwargs) -> ReaderProcess:
        child = ReaderProcess(self.binary, fixture, chapter, *flags, **kwargs)
        self.addCleanup(child.close)
        return child

    def read(self, fixture: ReaderFixture, chapter: str, *flags: str, **kwargs) -> list[dict]:
        code, events, stderr = self.launch(fixture, chapter, *flags, **kwargs).finish()
        self.assertEqual(code, 0, stderr)
        self.assertTrue(events, stderr)
        self.assertEqual(events[0]["kind"], "report", "first NDJSON must render before downloading")
        self.assertEqual(events[-1]["kind"], "report", "last NDJSON must be the authoritative report")
        return events

    def assert_metadata(self, report: dict, fixture: ReaderFixture, chapter: str,
                        series: str = "reader-fixture") -> None:
        raw_ids = list(CHAPTERS[series])
        numbers = [float(raw.replace("-", ".")) for raw in raw_ids]
        number = float(chapter.replace("-", "."))
        index = raw_ids.index(chapter)
        self.assertEqual(report["series"], series)
        self.assertEqual(report["series_url"], f"{fixture.base_url}/manga/{series}/")
        self.assertEqual(report["chapter"]["number"], number)
        self.assertEqual(report["chapter"]["url"], f"{fixture.base_url}/{series}-chapter-{chapter}/")
        self.assertEqual(report["chapter_index"], index + 1)
        self.assertEqual(report["chapter_total"], len(raw_ids))
        self.assertEqual([row["number"] for row in report["chapters"]], numbers)
        for raw, entry in zip(raw_ids, report["chapters"]):
            self.assertEqual(entry["url"], f"{fixture.base_url}/{series}-chapter-{raw}/")
            self.assertIn("done", entry)
        self.assertEqual((report.get("prev") or {}).get("number"), numbers[index - 1] if index else None)
        self.assertEqual((report.get("next") or {}).get("number"), numbers[index + 1] if index + 1 < len(numbers) else None)
        count = CHAPTERS[series][chapter]
        self.assertEqual(report["page_count"], count)
        self.assertEqual([page["index"] for page in report["pages"]], list(range(1, count + 1)))

    def assert_local(self, page: dict, fixture: ReaderFixture) -> None:
        self.assertTrue(page["cached"])
        path = Path(page["local"])
        self.assertTrue(path.is_absolute())
        self.assertTrue(path.resolve().is_relative_to(fixture.library))
        self.assertGreater(page["bytes"], MIN_RESUME_SIZE)
        self.assertEqual(page["bytes"], path.stat().st_size)
        validate_png(path.read_bytes())

    def test_local_library_and_second_series(self) -> None:
        fixture = self.fixture(image_delay=0)
        result = subprocess.run([str(self.binary), "info", fixture.info["seriesURL"], "--json"],
                                cwd=fixture.root, env=cli_environment(fixture), capture_output=True,
                                text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        info = json.loads(result.stdout)
        self.assertEqual([row["number"] for row in info["chapters"]], [1, 2, 3, 4, 79.2])
        self.assertEqual(info["done_count"], 2)
        for series, url in (("reader-fixture", fixture.info["seriesURL"]),
                            ("reader-second", fixture.info["secondSeriesURL"])):
            report = self.read(fixture, "first", series_url=url)[-1]
            self.assert_metadata(report, fixture, "001", series)
            self.assertEqual(report["source"], "library")
            for page in report["pages"]:
                self.assert_local(page, fixture)
                self.assertIn(series, Path(page["local"]).parts)
        self.assertFalse(any(path.startswith("/images/") for path in status(fixture)["requests"]))

    def test_partial_cache_offline_and_missing_only_retry(self) -> None:
        fixture = self.fixture(page3_failures=4, image_delay=0.02)
        events = self.read(fixture, "3")
        self.assertGreaterEqual(sum(event["kind"] == "report" for event in events), 2)
        first, final = events[0], events[-1]
        for report in (first, final):
            self.assert_metadata(report, fixture, "003")
        self.assertTrue(all(not page["cached"] for page in first["pages"]))
        cached_events = [event for event in events if event["kind"] == "page_cached"]
        self.assertEqual({event["page"] for event in cached_events}, {1, 2, 4, 5})
        for event in cached_events:
            self.assertTrue(Path(event["local"]).is_absolute())
            self.assertEqual(event["bytes"], Path(event["local"]).stat().st_size)
        self.assertEqual([event["page"] for event in events if event["kind"] == "page_failed"], [3])
        for page in final["pages"]:
            self.assertEqual(page["url"], fixture.base_url + fixture.image_path("reader-fixture", "003", page["index"]))
            if page["index"] == 3:
                self.assertFalse(page["cached"])
                self.assertFalse(page.get("local"))
                self.assertTrue(page.get("error"))
            else:
                self.assert_local(page, fixture)
        cache_dir = Path(final["cache_dir"])
        self.assertTrue(cache_dir.resolve().is_relative_to(fixture.library))
        manifest = json.loads((cache_dir / "manifest.json").read_text())
        self.assertGreaterEqual(manifest["version"], 1)
        failed_status = status(fixture)
        self.assertEqual(failed_status["failure_count"], 4)

        offline = self.read(fixture, "3", "--offline")[-1]
        self.assertEqual(status(fixture), failed_status, "offline must not issue any HTTP request")
        self.assert_metadata(offline, fixture, "003")
        self.assertEqual([page["index"] for page in offline["pages"] if page["cached"]], [1, 2, 4, 5])
        self.assertTrue(offline["pages"][2].get("error"))
        self.assertEqual(offline["pages"][3]["local"], final["pages"][3]["local"])

        repaired_events = self.read(fixture, "3")
        self.assertEqual([event["page"] for event in repaired_events if event["kind"] == "page_cached"], [3])
        repaired = repaired_events[-1]
        self.assert_metadata(repaired, fixture, "003")
        for page in repaired["pages"]:
            self.assert_local(page, fixture)
            self.assertFalse(page.get("error"))
        after_retry = status(fixture)
        for page in range(1, 6):
            path = fixture.image_path("reader-fixture", "003", page)
            self.assertEqual(after_retry["requests"][path], 5 if page == 3 else 1)
        offline_complete = self.read(fixture, "3", "--offline")[-1]
        self.assert_metadata(offline_complete, fixture, "003")
        self.assertTrue(all(page["cached"] for page in offline_complete["pages"]))
        self.assertEqual(offline_complete["bytes"], sum(page["bytes"] for page in offline_complete["pages"]))
        self.assertEqual(status(fixture), after_retry)
        # Reader caching never marks a library download done.
        self.assertEqual(json.loads((fixture.library / "reader-fixture" / ".state.json").read_text()), {"done": [1, 2]})

    def test_initial_report_precedes_images_and_cancel_has_final_report(self) -> None:
        fixture = self.fixture(slow_delay=5, image_delay=0)
        child = self.launch(fixture, "4")
        initial = child.event(timeout=5)
        self.assertIsNotNone(initial)
        self.assertEqual(initial["kind"], "report")
        self.assert_metadata(initial, fixture, "004")
        self.assertTrue(all(not page["cached"] for page in initial["pages"]))
        self.assertIsNone(child.process.poll(), "initial report must arrive before CLI exits")
        self.assertFalse(any(path.startswith("/images/") for path in status(fixture)["completed"]))
        # Wait for the actual Go HTTP request, not a timer-based guess, then
        # cancel precisely this subprocess while its image response is pending.
        deadline = time.monotonic() + 5
        first_image = fixture.image_path("reader-fixture", "004", 1)
        while not status(fixture)["requests"].get(first_image):
            self.assertLess(time.monotonic(), deadline, "CLI never requested the slow image")
            time.sleep(0.01)
        child.process.send_signal(signal.SIGTERM)
        code, events, stderr = child.finish(timeout=8)
        self.assertNotEqual(code, 0)
        self.assertIn("cancel", stderr.lower())
        self.assertGreaterEqual(len(events), 2)
        self.assertEqual(events[-1]["kind"], "report")
        self.assert_metadata(events[-1], fixture, "004")
        before = status(fixture)
        self.assertEqual(before["requests"].get(first_image), 1)
        offline = self.read(fixture, "4", "--offline")[-1]
        self.assert_metadata(offline, fixture, "004")
        self.assertTrue(all(not page["cached"] and page.get("error") for page in offline["pages"]))
        self.assertEqual(status(fixture)["requests"], before["requests"])
        self.assertFalse(list(fixture.library.rglob("*.part")))

    def test_remote_only_fractional_identity_and_offline_manifest(self) -> None:
        fixture = self.fixture(image_delay=0)
        report = self.read(fixture, "latest", "--cache=false")[-1]
        self.assert_metadata(report, fixture, "79-2")
        self.assertEqual(report["chapter"]["display"], "79.2")
        self.assertTrue(all(page.get("url") and not page["cached"] for page in report["pages"]))
        self.assertFalse(any(path.startswith("/images/") for path in status(fixture)["requests"]))
        cached = self.read(fixture, "79.2")[-1]
        self.assert_metadata(cached, fixture, "79-2")
        for page in cached["pages"]:
            self.assert_local(page, fixture)
        before = status(fixture)
        offline = self.read(fixture, "latest", "--offline")[-1]
        self.assert_metadata(offline, fixture, "79-2")
        self.assertTrue(all(page["cached"] for page in offline["pages"]))
        self.assertEqual(status(fixture), before)

    def test_offline_without_manifest_uses_only_library(self) -> None:
        fixture = self.fixture(offline=True)
        before = status(fixture)
        report = self.read(fixture, "first", "--offline")[-1]
        self.assertEqual(report["source"], "library")
        self.assertEqual(report["chapter"]["number"], 1)
        self.assertEqual(report["page_count"], 3)
        for page in report["pages"]:
            self.assert_local(page, fixture)
        code, _events, stderr = self.launch(fixture, "3", "--offline").finish()
        self.assertNotEqual(code, 0, "unavailable offline chapter must not appear complete")
        self.assertTrue(stderr.strip())
        self.assertEqual(status(fixture), before)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter,
                                     epilog="""Examples:
  python3 gui/e2e/reader_protocol_test.py --fixture-only
  python3 gui/e2e/reader_protocol_test.py
  python3 gui/e2e/reader_protocol_test.py --binary /absolute/path/to/komiku-cli

The strict CLI suite expects the manifest-backed reader protocol: first/final
kind=report, complete chapters metadata, original page slots, page_cached.local,
page.error for partial/offline holes, and a final report after cancellation.
Use --fixture-only while that backend contract is under development. No tests
are silently skipped for old binaries. Browser coverage belongs to the official
Browser Use session; this program never starts a browser or Vite.

The build disables toolchain/module downloads (GOTOOLCHAIN=local, GOPROXY=off,
GOSUMDB=off). Use --go /path/to/go if PATH selects an older compiler, or --binary
if the required compiler/dependencies are not locally installed. Only freshly
created temporary trees and subprocesses owned by this run are cleaned up.
""")
    parser.add_argument("--binary", type=Path, help="actual Go CLI executable; otherwise build one in a temporary folder")
    parser.add_argument("--go", default="go", help="installed Go compiler executable used for the isolated build (default: go)")
    parser.add_argument("--fixture-only", action="store_true", help="validate fixture without requiring the new reader backend")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="reader-protocol-build-") as directory:
        if not args.fixture_only:
            binary = args.binary.resolve() if args.binary else Path(directory) / "komiku-cli"
            if args.binary:
                if not binary.is_file() or not os.access(binary, os.X_OK):
                    parser.error(f"not an executable: {binary}")
            else:
                build_env = {**os.environ, "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off"}
                # Version managers may export a GOROOT for another compiler.
                # Let the explicitly selected Go executable locate its own root.
                build_env.pop("GOROOT", None)
                try:
                    result = subprocess.run([args.go, "build", "-o", str(binary), "./cmd/komiku-cli"],
                                            cwd=REPO, env=build_env,
                                            capture_output=True, text=True, timeout=180)
                except (OSError, subprocess.TimeoutExpired) as error:
                    parser.exit(1, f"Go CLI build failed (no reader tests ran): {error}\n")
                if result.returncode:
                    parser.exit(1, f"Go CLI build failed (no reader tests ran):\n{result.stderr}")
            ReaderProtocolTests.binary = binary
        suite = unittest.TestLoader().loadTestsFromTestCase(FixtureTests)
        if not args.fixture_only:
            suite.addTests(unittest.TestLoader().loadTestsFromTestCase(ReaderProtocolTests))
        result = unittest.TextTestRunner(verbosity=2).run(suite)
        return 0 if result.wasSuccessful() else 1


if __name__ == "__main__":
    raise SystemExit(main())
