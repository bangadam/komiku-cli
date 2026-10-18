#!/usr/bin/env python3
"""Real loopback Komiku pages and an isolated library for reader E2E tests.

Run with --help for startup options. This serves upstream HTML/images only;
it does not replace the Go CLI, Vite bridge, or reader UI.
"""
from __future__ import annotations

import argparse
from collections import Counter
from dataclasses import dataclass
import html
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import math
import os
from pathlib import Path
import struct
import sys
import tempfile
import threading
from urllib.parse import urlsplit
import zlib


WIDTH, HEIGHT = 800, 1200
MIN_RESUME_SIZE = 10 * 1024  # store.MinResumeSize; every generated PNG exceeds it.
CHAPTERS = {"reader-fixture": {"001": 3, "002": 4, "003": 5, "004": 5, "79-2": 3},
            "reader-second": {"001": 2, "002": 3}}

# Tiny built-in bitmap font keeps the fixture standard-library-only, yet the
# labels are actual high-contrast pixels (not metadata or inaccessible alt text).
FONT = dict(zip(
    "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ.-/ ",
    (
        "01110/10001/10011/10101/11001/10001/01110",
        "00100/01100/00100/00100/00100/00100/01110",
        "01110/10001/00001/00010/00100/01000/11111",
        "11110/00001/00001/01110/00001/00001/11110",
        "00010/00110/01010/10010/11111/00010/00010",
        "11111/10000/10000/11110/00001/00001/11110",
        "01110/10000/10000/11110/10001/10001/01110",
        "11111/00001/00010/00100/01000/01000/01000",
        "01110/10001/10001/01110/10001/10001/01110",
        "01110/10001/10001/01111/00001/00001/01110",
        "01110/10001/10001/11111/10001/10001/10001",
        "11110/10001/10001/11110/10001/10001/11110",
        "01111/10000/10000/10000/10000/10000/01111",
        "11110/10001/10001/10001/10001/10001/11110",
        "11111/10000/10000/11110/10000/10000/11111",
        "11111/10000/10000/11110/10000/10000/10000",
        "01111/10000/10000/10111/10001/10001/01111",
        "10001/10001/10001/11111/10001/10001/10001",
        "11111/00100/00100/00100/00100/00100/11111",
        "00111/00010/00010/00010/10010/10010/01100",
        "10001/10010/10100/11000/10100/10010/10001",
        "10000/10000/10000/10000/10000/10000/11111",
        "10001/11011/10101/10101/10001/10001/10001",
        "10001/11001/10101/10011/10001/10001/10001",
        "01110/10001/10001/10001/10001/10001/01110",
        "11110/10001/10001/11110/10000/10000/10000",
        "01110/10001/10001/10001/10101/10010/01101",
        "11110/10001/10001/11110/10100/10010/10001",
        "01111/10000/10000/01110/00001/00001/11110",
        "11111/00100/00100/00100/00100/00100/00100",
        "10001/10001/10001/10001/10001/10001/01110",
        "10001/10001/10001/10001/10001/01010/00100",
        "10001/10001/10001/10101/10101/11011/10001",
        "10001/10001/01010/00100/01010/10001/10001",
        "10001/10001/01010/00100/00100/00100/00100",
        "11111/00001/00010/00100/01000/10000/11111",
        "00000/00000/00000/00000/00000/00110/00110",
        "00000/00000/00000/11111/00000/00000/00000",
        "00001/00010/00010/00100/01000/01000/10000",
        "00000/00000/00000/00000/00000/00000/00000",
    ),
))


def png_chunk(kind: bytes, data: bytes) -> bytes:
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))


def page_png(series: str, chapter: str, page: int, count: int) -> bytes:
    """Encode a complete 800x1200 RGB PNG, including CRCs and a real IEND.

    A valid tEXt ancillary chunk supplies minimum resume-file size; no bytes are
    appended after IEND and the IDAT stream decodes to all 1200 scanlines.
    """
    pixels = bytearray(bytes((247, 244, 233)) * (WIDTH * HEIGHT))
    ink = (24, 36, 51)
    accent = (40, 95, 160) if series == "reader-fixture" else (136, 58, 90)

    def rectangle(x: int, y: int, width: int, height: int, color: tuple) -> None:
        row = bytes(color) * width
        for line in range(y, y + height):
            start = (line * WIDTH + x) * 3
            pixels[start:start + width * 3] = row

    def text(value: str, y: int, scale: int, color: tuple = ink) -> None:
        x = (WIDTH - (len(value) * 6 - 1) * scale) // 2
        for character in value:
            for row, bits in enumerate(FONT[character].split("/")):
                for col, bit in enumerate(bits):
                    if bit == "1":
                        rectangle(x + col * scale, y + row * scale, scale, scale, color)
            x += 6 * scale

    rectangle(30, 30, 740, 14, accent)
    rectangle(30, 1156, 740, 14, accent)
    text(series.upper(), 85, 8, accent)
    text("CHAPTER " + chapter.lstrip("0").replace("-", "."), 225, 9)
    text("PAGE", 420, 16)
    text(str(page), 600, 32, accent)
    text(f"{page} / {count}", 970, 12)
    text("READER TEST", 1090, 6)
    scanlines = b"".join(b"\x00" + pixels[y * WIDTH * 3:(y + 1) * WIDTH * 3]
                         for y in range(HEIGHT))
    label = f"{series} chapter {chapter} page {page} of {count}. "
    padding = b"Fixture\x00" + (label.encode("ascii") * 600)[:MIN_RESUME_SIZE + 1024]
    return (b"\x89PNG\r\n\x1a\n"
            + png_chunk(b"IHDR", struct.pack(">IIBBBBB", WIDTH, HEIGHT, 8, 2, 0, 0, 0))
            + png_chunk(b"tEXt", padding)
            + png_chunk(b"IDAT", zlib.compress(scanlines, 6))
            + png_chunk(b"IEND", b""))


@dataclass(frozen=True)
class Options:
    port: int = 0
    page3_failures: int = 0
    image_delay: float = 0.1
    slow_delay: float = 2.0
    offline: bool = False

    def __post_init__(self) -> None:
        if not 0 <= self.port <= 65535:
            raise ValueError("port must be 0 (OS assigned) or 1..65535")
        if self.page3_failures < 0:
            raise ValueError("page3_failures must be nonnegative")
        if any(not math.isfinite(delay) or delay < 0
               for delay in (self.image_delay, self.slow_delay)):
            raise ValueError("delays must be finite and nonnegative")


class ReaderFixture:
    """Owns only a fresh mkdtemp tree and an OS-assigned loopback listener.

    close() closes this listener, never kills processes or removes directories.
    Standalone runs retain their tree for inspection after Ctrl-C. Tests may
    place it inside their own TemporaryDirectory for scoped cleanup.
    """

    def __init__(self, options: Options | None = None, temp_parent: Path | None = None):
        self.options = options or Options()
        self.root = Path(tempfile.mkdtemp(prefix="komiku-reader-", dir=temp_parent)).resolve()
        self.home = self.root / "home"
        self.library = self.root / "library"
        self.home.mkdir()
        self.library.mkdir()
        self._lock = threading.Lock()
        self._closed = threading.Event()
        self._requests: Counter[str] = Counter()
        self._completed: Counter[str] = Counter()
        self._failed_count = 0
        self._thread: threading.Thread | None = None
        self._routes: dict[str, tuple[str, bytes]] = {}
        self._images: set[str] = set()
        self.config_paths = self._configure()
        self.server = ThreadingHTTPServer(("127.0.0.1", self.options.port), self._handler())
        self.server.daemon_threads = True
        self.base_url = f"http://127.0.0.1:{self.server.server_address[1]}"
        try:
            self._build()
        except BaseException:
            self.server.server_close()
            raise

    @staticmethod
    def image_path(series: str, chapter: str, page: int) -> str:
        return f"/images/{series}/chapter-{chapter}/{page:03d}.png"

    def _configure(self) -> list[Path]:
        config = {"output_root": str(self.library), "image_delay": "0ms", "preset": "raw"}
        # Go os.UserConfigDir on Darwin, Linux and Windows respectively.
        directories = [self.home / "Library" / "Application Support",
                       self.home / ".config", self.home / "AppData" / "Roaming"]
        paths = []
        for directory in directories:
            path = directory / "komiku-cli" / "config.json"
            path.parent.mkdir(parents=True)
            path.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
            paths.append(path)
        return paths

    @property
    def environment(self) -> dict[str, str]:
        return {"HOME": str(self.home), "USERPROFILE": str(self.home),
                "XDG_CONFIG_HOME": str(self.home / ".config"),
                "XDG_CACHE_HOME": str(self.home / ".cache"),
                "APPDATA": str(self.home / "AppData" / "Roaming")}

    def _build(self) -> None:
        for series, chapters in CHAPTERS.items():
            series_dir = self.library / series
            series_dir.mkdir()
            done = [1, 2] if series == "reader-fixture" else [1]
            (series_dir / ".state.json").write_text(json.dumps({"done": done}) + "\n",
                                                     encoding="utf-8")
            anchors = []
            for chapter, count in chapters.items():
                chapter_path = f"/{series}-chapter-{chapter}/"
                display = chapter.lstrip("0").replace("-", ".")
                anchors.append(f'<a href="{chapter_path}">Chapter {display}</a>')
                images = []
                for page in range(1, count + 1):
                    path = self.image_path(series, chapter, page)
                    data = page_png(series, chapter, page, count)
                    self._routes[path] = ("image/png", data)
                    self._images.add(path)
                    # komiku.ExtractImageURLs requires class klazy (or ww) and
                    # an absolute HTTP(S) src, not data-src or a relative URL.
                    images.append(f'<img class="reader-page klazy" '
                                  f'src="{self.base_url}{path}" width="{WIDTH}" height="{HEIGHT}" '
                                  f'alt="{html.escape(series)} chapter {display} page {page}">')
                    if chapter in {f"{number:03d}" for number in done}:
                        local = series_dir / f"chapter-{chapter}"
                        local.mkdir(exist_ok=True)
                        (local / f"{page:03d}.png").write_bytes(data)
                self._routes[chapter_path] = self._html(series, "\n".join(images))
            # The upstream series contains a decoy cover and reversed anchors,
            # just as a real latest-first chapter listing does.
            cover_path = self.image_path(series, "001", 1)
            (series_dir / "cover.png").write_bytes(self._routes[cover_path][1])
            content = (f'<h1>{html.escape(series)}</h1><img class="cover" '
                       f'src="{self.base_url}{cover_path}" alt="cover">'
                       + "\n".join(reversed(anchors)))
            self._routes[f"/manga/{series}/"] = self._html(series, content)
        self._routes["/"] = self._html("Reader fixture", "\n".join(
            f'<a href="/manga/{series}/">{series}</a>' for series in CHAPTERS))

    @staticmethod
    def _html(title: str, content: str) -> tuple[str, bytes]:
        return ("text/html; charset=utf-8",
                (f'<!doctype html><html><head><meta charset="utf-8"><title>{html.escape(title)}'
                 f'</title></head><body>{content}</body></html>').encode("utf-8"))

    @property
    def info(self) -> dict:
        config = self.config_paths[0 if sys.platform == "darwin" else 2 if os.name == "nt" else 1]
        return {"pid": os.getpid(), "host": "127.0.0.1", "port": self.server.server_address[1],
                "root": str(self.root), "home": str(self.home), "library": str(self.library),
                "config": str(config), "configPaths": [str(path) for path in self.config_paths],
                "environment": self.environment, "baseURL": self.base_url,
                "seriesURL": f"{self.base_url}/manga/reader-fixture/",
                "secondSeriesURL": f"{self.base_url}/manga/reader-second/",
                "statusURL": f"{self.base_url}/__fixture/status",
                "chapters": CHAPTERS, "options": vars(self.options)}

    def status(self) -> dict:
        with self._lock:
            return {"requests": dict(sorted(self._requests.items())),
                    "completed": dict(sorted(self._completed.items())),
                    "total_requests": sum(self._requests.values()),
                    "failed": self._failed_count > 0, "failure_count": self._failed_count,
                    "remaining_page3_failures": max(0, self.options.page3_failures - self._failed_count),
                    "offline": self.options.offline}

    def _handler(self) -> type[BaseHTTPRequestHandler]:
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, _format, *args) -> None:
                pass  # stdout is exclusively the startup JSON; no request noise.

            def _send(self, status: int, content_type: str, data: bytes) -> bool:
                try:
                    self.send_response(status)
                    self.send_header("Content-Type", content_type)
                    self.send_header("Content-Length", str(len(data)))
                    self.send_header("Cache-Control", "no-store")
                    self.send_header("Access-Control-Allow-Origin", "*")
                    self.send_header("X-Content-Type-Options", "nosniff")
                    self.end_headers()
                    self.wfile.write(data)
                    return True
                except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                    return False  # Expected when the real reader cancels a request.

            def do_GET(self) -> None:
                path = urlsplit(self.path).path
                if path == "/__fixture/status":
                    # Polling status never consumes failures or changes counters.
                    self._send(200, "application/json", json.dumps(fixture.status()).encode())
                    return
                with fixture._lock:
                    fixture._requests[path] += 1
                    fail = (path == fixture.image_path("reader-fixture", "003", 3)
                            and fixture._failed_count < fixture.options.page3_failures)
                    if fail and not fixture.options.offline:
                        fixture._failed_count += 1
                if fixture.options.offline:
                    self._send(503, "text/plain", b"Fixture upstream is offline\n")
                    return
                if fail:
                    self._send(503, "text/plain", b"Deterministic page 3 failure\n")
                    return
                resource = fixture._routes.get(path)
                if resource is None:
                    self._send(404, "text/plain", b"Unknown fixture route\n")
                    return
                if path in fixture._images:
                    delay = (fixture.options.slow_delay if "/chapter-004/" in path
                             else fixture.options.image_delay)
                    if fixture._closed.wait(delay):
                        return
                if self._send(200, *resource):
                    with fixture._lock:
                        fixture._completed[path] += 1

            def _read_only(self) -> None:
                self._send(405, "text/plain", b"Read-only fixture; configure at startup\n")

            do_POST = do_PUT = do_PATCH = do_DELETE = _read_only

        return Handler

    def start(self) -> "ReaderFixture":
        if self._closed.is_set():
            raise RuntimeError("fixture has already closed")
        if self._thread is None:
            self._thread = threading.Thread(target=self.server.serve_forever,
                                            kwargs={"poll_interval": 0.05}, daemon=True)
            self._thread.start()
        return self

    def close(self) -> None:
        self._closed.set()
        if self._thread is not None:
            self.server.shutdown()
            self._thread.join(timeout=5)
        self.server.server_close()

    def __enter__(self) -> "ReaderFixture":
        return self.start()

    def __exit__(self, *_args) -> None:
        self.close()


def main() -> int:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""Examples:
  python3 gui/e2e/reader_fixture.py
  python3 gui/e2e/reader_fixture.py --fail-page3-once
  python3 gui/e2e/reader_fixture.py --page3-failures 4 --slow-delay 5

One flushed JSON line announces paths, seriesURL, secondSeriesURL, statusURL,
and environment. Apply ALL environment entries to the Vite child so its real
CLI uses this HOME/library (HOME is sufficient on macOS). Open seriesURL through
the real GUI, not this upstream HTML as a mock UI. GET /__fixture/status returns
per-path requests/completed counts and failed/failure_count; status polling does
not count. No HTTP endpoint changes configuration or exposes filesystem paths.

The real Go image client retries four times. --fail-page3-once tests transport
recovery; --page3-failures 4 leaves page 3 absent after the first CLI read, then
allows the next read to repair it. Chapter 004 images wait --slow-delay seconds
each for cancellation tests. --offline rejects upstream reads but keeps status
available. The second series has different page counts/labels for state isolation.

Every run creates a NEW private temporary tree, never reuses or deletes a real
library, and retains it after Ctrl-C for inspection. Only this process's listener
is closed. An occupied --port fails normally; no other process is terminated.
""")
    parser.add_argument("--port", type=int, default=0, help="loopback port; 0 lets the OS choose (default)")
    failures = parser.add_mutually_exclusive_group()
    failures.add_argument("--fail-page3-once", action="store_true", help="fail exactly the first GET of chapter 003 page 3")
    failures.add_argument("--page3-failures", type=int, default=0, metavar="N", help="fail the first N GETs of chapter 003 page 3")
    parser.add_argument("--image-delay", type=float, default=0.1, metavar="SECONDS", help="normal image response delay (default: 0.1)")
    parser.add_argument("--slow-delay", type=float, default=2.0, metavar="SECONDS", help="chapter 004 image delay (default: 2)")
    parser.add_argument("--offline", action="store_true", help="serve 503 for all upstream routes; status remains available")
    parser.add_argument("--temp-parent", type=Path, help="existing parent for a fresh mkdtemp tree, never an existing library")
    args = parser.parse_args()
    try:
        options = Options(args.port, 1 if args.fail_page3_once else args.page3_failures,
                          args.image_delay, args.slow_delay, args.offline)
        with ReaderFixture(options, args.temp_parent) as fixture:
            print(json.dumps(fixture.info), flush=True)
            try:
                threading.Event().wait()
            except KeyboardInterrupt:
                pass
    except (OSError, ValueError) as error:
        parser.exit(1, f"reader fixture: {error}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
