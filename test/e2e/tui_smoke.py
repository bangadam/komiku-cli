#!/usr/bin/env python3
"""Real PTY smoke for the no-argument TUI and its local HTTP fixture."""

from __future__ import annotations

import argparse
import errno
import fcntl
import json
import os
import pty
import re
import select
import struct
import subprocess
import tempfile
import termios
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlencode, urlparse

ANSI = re.compile(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[()][0-2A-Z])")
CHAPTER = re.compile(r"^/actual-series-chapter-(1|2|3|271-5)/$")
IMAGE = re.compile(r"^/images/(1|2|3|271-5)/(\d+)\.jpg$")

AUDIT_LOG = re.compile(r"run-[0-9]{8}T[0-9]{6}\.[0-9]+Z(?:-[0-9]{3})?\.log")


class Evidence:
    def __init__(self, path: Path | None, root: Path, origin: str) -> None:
        self.path = path
        self.root = str(root)
        self.origin = origin
        self.lines: list[str] = []

    def record(self, output: str, title: str, observed: str, needles: list[str], assertions: list[str]) -> None:
        if self.path is None:
            return
        for needle in needles:
            if needle not in observed:
                raise AssertionError(f"evidence item was not observed: {needle!r}")
        self.lines.extend([f"[[render:{output}]]", f"[{title}]", "capture_method: ANSI-stripped real PTY transcript-rendered PNG", "observed:"])
        self.lines.extend(self.clean(needle) for needle in needles)
        self.lines.append("assertions:")
        self.lines.extend("PASS " + self.clean(item) for item in assertions)
        self.lines.extend(["[[/render]]", ""])

    def clean(self, value: str) -> str:
        value = value.replace(self.root, "$TUI_E2E_ROOT").replace(self.origin, "http://127.0.0.1:<PORT>")
        return AUDIT_LOG.sub("run-<TIMESTAMP>.log", value)

    def write(self) -> None:
        if self.path is None:
            return
        self.path.parent.mkdir(parents=True, exist_ok=True)
        temporary = self.path.with_suffix(self.path.suffix + ".tmp")
        temporary.write_text("\n".join(self.lines))
        temporary.replace(self.path)


class FixtureState:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.requests: list[tuple[float, str, int]] = []

    def record(self, chapter: str, page: int) -> None:
        with self.lock:
            self.requests.append((time.monotonic(), chapter, page))

    def clear(self) -> None:
        with self.lock:
            self.requests.clear()

    def count(self) -> int:
        with self.lock:
            return len(self.requests)

    def saw(self, chapter: str, page: int | None = None) -> bool:
        with self.lock:
            return any(ch == chapter and (page is None or number == page) for _, ch, number in self.requests)


class FixtureHandler(BaseHTTPRequestHandler):
    server: "FixtureServer"

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def reply(self, body: bytes, content_type: str = "text/html; charset=utf-8") -> None:
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except BrokenPipeError:
            pass

    def do_GET(self) -> None:
        parsed = urlparse(self.path)
        query = parse_qs(parsed.query)
        search = query.get("s")
        if parsed.path == "/" and search:
            if query.get("post_type") != ["manga"]:
                self.reply(f'<span hx-get="/fragment/?s={search[0]}" hx-trigger="revealed"></span>'.encode())
                return
            deferred_query = urlencode({"post_type": "manga", "s": search[0]}).replace("&", "&amp;")
            self.reply(
                f'<div class="daftar"><span hx-get="{self.server.base_url}/fragment/?{deferred_query}" '
                'hx-trigger="revealed" hx-swap="afterend"></span></div>'.encode()
            )
            return
        if parsed.path == "/fragment/" and search == ["actual"]:
            self.reply(
                b'<div class="bge">'
                b'<div class="bgei"><a href="/manga/actual-series/"><img alt="Actual Fixture Series">'
                b'<div class="tpe1_inf"><b>Manga</b> Komedi</div></a></div>'
                b'<div class="kan"><a href="/manga/actual-series/"><h3>Actual Fixture Series</h3></a>'
                b'<div class="new1"><a href="/actual-series-chapter-271-5/">Chapter 271.5</a></div></div>'
                b"</div>"
            )
            return
        if parsed.path == "/fragment/" and search == ["empty"]:
            self.reply(b'<div class="no-results"><p>Tidak ada hasil pencarian yang ditemukan.</p></div>')
            return
        if parsed.path == "/fragment/" and search == ["broken"]:
            self.reply(b'<div class="bge"><a href="/manga/broken/"><img></a></div>')
            return
        if parsed.path == "/manga/actual-series/":
            self.reply(
                b'<a href="/actual-series-chapter-1/">one</a>'
                b'<a href="/actual-series-chapter-2/">two</a>'
                b'<a href="/actual-series-chapter-3/">three</a>'
                b'<a href="/actual-series-chapter-271-5/">extra</a>'
            )
            return
        if parsed.path == "/wiki/List_of_Actual_Series_chapters":
            self.reply(
                b'<section aria-labelledby="Volumes"><h3 id="Volumes">Volumes</h3>'
                b'<table class="wikitable">'
                b'<tr><th scope="row" id="vol1">1</th><td><li>Days 1: one</li><li>Days 2. two</li></td></tr>'
                b'<tr><th scope="row" id="vol2">2</th><td><li>Days 3: three</li></td></tr>'
                b"</table></section>"
            )
            return
        chapter_match = CHAPTER.match(parsed.path)
        if chapter_match:
            chapter = chapter_match.group(1)
            images = "".join(
                f'<img class="klazy" src="{self.server.base_url}/images/{chapter}/{page}.jpg">'
                for page in range(1, 9)
            )
            self.reply(images.encode())
            return
        image_match = IMAGE.match(parsed.path)
        if image_match:
            chapter, page_text = image_match.groups()
            page = int(page_text)
            self.server.state.record(chapter, page)
            time.sleep(0.08)
            self.reply(b"\xff\xd8" + bytes([page]) * 12_000, "image/jpeg")
            return
        self.send_error(404)


class FixtureServer(ThreadingHTTPServer):
    def __init__(self) -> None:
        self.state = FixtureState()
        super().__init__(("127.0.0.1", 0), FixtureHandler)
        self.base_url = f"http://127.0.0.1:{self.server_port}"


class Terminal:
    def __init__(self, argv: list[str], env: dict[str, str], cwd: Path, width: int = 100, height: int = 30) -> None:
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", height, width, 0, 0))
        self.master = master
        self.raw = bytearray()
        self.process = subprocess.Popen(argv, stdin=slave, stdout=slave, stderr=slave, cwd=cwd, env=env, close_fds=True)
        os.close(slave)

    def send(self, value: str | bytes) -> None:
        data = value.encode() if isinstance(value, str) else value
        os.write(self.master, data)

    def pump(self, timeout: float = 0.05) -> None:
        ready, _, _ = select.select([self.master], [], [], timeout)
        if not ready:
            return
        try:
            chunk = os.read(self.master, 65536)
        except OSError as error:
            if error.errno == errno.EIO:
                return
            raise
        self.raw.extend(chunk)

    def text(self) -> str:
        return ANSI.sub("", self.raw.decode("utf-8", "replace")).replace("\r", "")

    def mark(self) -> int:
        self.pump(0)
        return len(self.text())

    def wait_for(self, needle: str, timeout: float = 10, since: int = 0) -> str:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.pump(0.05)
            text = self.text()
            if needle in text[since:]:
                return text
            if self.process.poll() is not None:
                break
        raise AssertionError(f"did not observe {needle!r}; exit={self.process.poll()}\n{self.text()[-4000:]}")

    def wait_predicate(self, predicate, timeout: float = 15) -> None:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.pump(0.05)
            if predicate():
                return
            if self.process.poll() is not None:
                break
        raise AssertionError(f"predicate not reached; exit={self.process.poll()}\n{self.text()[-4000:]}")

    def wait_exit(self, timeout: float = 10) -> int:
        deadline = time.monotonic() + timeout
        while self.process.poll() is None and time.monotonic() < deadline:
            self.pump(0.05)
        if self.process.poll() is None:
            self.process.kill()
            raise AssertionError(f"process did not exit\n{self.text()[-4000:]}")
        for _ in range(4):
            self.pump(0.01)
        return int(self.process.returncode)

    def close(self) -> None:
        if self.process.poll() is None:
            self.process.kill()
            self.process.wait()
        os.close(self.master)


def write_config(config_home: Path, output: Path) -> Path:
    path = config_home / "komiku-cli" / "config.json"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps({"output_root": str(output), "image_delay": "50ms", "preset": "raw"}))
    return path


def open_series(term: Terminal, query: str, expect_results: bool) -> None:
    term.wait_for("komiku-cli / Search")
    mark = term.mark()
    term.send(query)
    term.send("\r")
    if expect_results:
        term.wait_for("Actual Fixture Series  actual-series", since=mark)
        mark = term.mark()
        term.send("\r")
        term.wait_for("komiku-cli / Chapters", since=mark)
    else:
        term.wait_for("komiku-cli / Chapters", since=mark)


def run_production_no_args(binary: Path, server: FixtureServer, root: Path, repo: Path, evidence: Evidence) -> None:
    config_home = root / "production-config"
    output = root / "production-output"
    write_config(config_home, output)
    env = os.environ.copy()
    env.update({"XDG_CONFIG_HOME": str(config_home), "TERM": "xterm-256color"})
    env.pop("NO_COLOR", None)
    term = Terminal([str(binary)], env, repo, width=80)
    try:
        open_series(term, server.base_url + "/manga/actual-series/", expect_results=False)
        chapters_view = term.text()
        for identity in ("ch 1  raw:1", "ch 2  raw:2", "ch 3  raw:3", "ch 271.5  raw:271-5"):
            if identity not in chapters_view:
                raise AssertionError(f"URL fallback chapter list missing {identity!r}\n{chapters_view[-4000:]}")
        evidence.record("01-no-arg-url-list.png", "01 NO-ARG URL CHAPTER LIST", chapters_view,
            ["komiku-cli / Chapters", "ch 1  raw:1", "ch 2  raw:2", "ch 3  raw:3", "ch 271.5  raw:271-5"],
            ["production binary started with no arguments", "loopback /manga/ URL passed strict source policy", "raw extra identity remained visible"])
        term.send("q")
        if term.wait_exit() != 0:
            raise AssertionError(term.text()[-4000:])
    finally:
        term.close()


def run_full_fixture(fixture: Path, server: FixtureServer, root: Path, repo: Path, evidence: Evidence) -> None:
    output = root / "full-output"
    config = write_config(root / "full-config", output)
    env = os.environ.copy()
    env.update({"TERM": "dumb", "NO_COLOR": "1"})
    term = Terminal([str(fixture), "--base-url", server.base_url, "--config", str(config), "--workers", "2"], env, repo)
    try:
        term.wait_for("komiku-cli / Search")
        mark = term.mark()
        term.send("empty")
        term.send("\r")
        term.wait_for("[EMPTY] No series found.", since=mark)
        term.send(b"\x15")
        input_mark = term.mark()
        term.send("broken")
        term.wait_for("> broken", since=input_mark)
        term.send("\r")
        term.wait_for("[ERROR] search results contain series links without usable titles", since=input_mark)
        term.send(b"\x15")
        input_mark = term.mark()
        term.send("actual")
        term.wait_for("> actual", since=input_mark)
        term.send("\r")
        term.wait_for("Results (1)", since=input_mark)
        open_mark = term.mark()
        term.send("\r")
        term.wait_for("komiku-cli / Chapters", since=open_mark)
        mark = term.mark()
        term.send("v")
        term.wait_for("[ ] Volume 01  ch 1-2", since=mark)
        grouped_view = term.text()[mark:]
        for label in ("[ ] Volume 02  ch 3-3", "Unmapped / extras", "Volume view source: Wikipedia", "Space toggle chapter/volume", "v flat"):
            if label not in grouped_view:
                raise AssertionError(f"grouped chapter view missing {label!r}\n{grouped_view[-4000:]}")
        header_mark = term.mark()
        term.send(b"\x1b[A")
        term.wait_for("> [ ] Volume 01  ch 1-2", since=header_mark)
        full_mark = term.mark()
        term.send(" ")
        term.wait_for("> [x] Volume 01  ch 1-2", since=full_mark)
        term.wait_for("Selected 2/4", since=full_mark)
        partial_mark = term.mark()
        term.send(b"\x1b[B")
        term.send(" ")
        term.wait_for("[-] Volume 01  ch 1-2", since=partial_mark)
        term.wait_for("Selected 1/4", since=partial_mark)
        flat_mark = term.mark()
        term.send("v")
        term.wait_for("v volumes", since=flat_mark)
        flat_view = term.text()[flat_mark:]
        if "[x] ch 2  raw:2" not in flat_view:
            raise AssertionError(f"aggregate selection changed when returning to flat view\n{flat_view[-4000:]}")

        mark = term.mark()
        term.send("a")
        term.wait_for("Selected 4/4", since=mark)
        mark = term.mark()
        term.send("a")
        term.wait_for("Selected 0/4", since=mark)

        mark = term.mark()
        term.send("/")
        term.wait_for("/ Filter chapters", since=mark)
        mark = term.mark()
        term.send("271.5")
        term.wait_for("/ 271.5", since=mark)
        mark = term.mark()
        term.send("\r")
        term.wait_for("raw:271-5", since=mark)
        term.send(" ")
        term.wait_for("Selected 1/4", since=mark)

        mark = term.mark()
        term.send("r")
        term.wait_for("komiku-cli / Range", since=mark)
        value_mark = term.mark()
        term.send("1-3")
        term.wait_for("> 1-3", since=value_mark)
        term.send("\r")
        term.wait_for("Selected 3 discovered chapter(s) in flat layout.", since=mark)

        mark = term.mark()
        term.send("r")
        term.wait_for("komiku-cli / Range", since=mark)
        term.send(b"\x1b")
        term.wait_for("komiku-cli / Chapters", since=mark)

        mark = term.mark()
        term.send("r")
        term.wait_for("komiku-cli / Range", since=mark)
        for mode in ("[volume]", "[manual]"):
            tab_mark = term.mark()
            term.send("\t")
            term.wait_for(mode, since=tab_mark)
        invalid_mark = term.mark()
        term.send("bad")
        term.send("\r")
        term.wait_for("[ERROR] use mapping | volumes", since=invalid_mark)
        term.send(b"\x15")
        value_mark = term.mark()
        term.send("1:1-2 | 1")
        term.wait_for("1:1-2 | 1", since=value_mark)
        term.send("\r")
        term.wait_for("Selected 2 chapters from 1 volume(s).", since=mark)

        mark = term.mark()
        term.send("r")
        term.wait_for("komiku-cli / Range", since=mark)
        term.send("\t")
        term.wait_for("[volume]", since=mark)
        value_mark = term.mark()
        term.send("1")
        term.wait_for("> 1", since=value_mark)
        term.send("\r")
        term.wait_for("Selected 2 chapters from 1 volume(s).", since=mark)
        evidence.record("02-keyword-range-mapping.png", "02 KEYWORD AGGREGATE VOLUME RANGE AND MAPPING", term.text(),
            ["[EMPTY] No series found.", "[ERROR] search results contain series links without usable titles", "Actual Fixture Series  actual-series", "Results (1)", "[ ] Volume 01  ch 1-2", "> [x] Volume 01  ch 1-2", "[-] Volume 01  ch 1-2", "[ ] Volume 02  ch 3-3", "Unmapped / extras", "Volume view source: Wikipedia", "Selected 3 discovered chapter(s) in flat layout.", "Selected 2 chapters from 1 volume(s).", "Volume 01: ch 1-2"],
            ["canonical post_type=manga search followed a validated same-origin HTMX fragment", "true no-results remained EMPTY", "keyword result selected by keyboard", "Wikipedia headers received focus and exposed unchecked/full/partial aggregate states", "aggregate Space changed only the shared chapter selection", "group-off preserved the selected chapter identity", "flat and mapped range paths were observed", "mapped coverage was shown before download"])

        mark = term.mark()
        term.send("\r")
        term.wait_for("komiku-cli / Download", since=mark)
        live_view = term.text()[mark:]
        for metric in ("Speed ", "ETA ", "Errors "):
            if metric not in live_view:
                raise AssertionError(f"live download screen missing {metric.strip()!r}\n{live_view[-4000:]}")
        pause_mark = term.mark()
        term.send(" ")
        term.wait_for("[PAUSED]", since=pause_mark)
        time.sleep(0.3)
        term.pump(0)
        paused_hits = server.state.count()
        time.sleep(0.2)
        term.pump(0)
        if server.state.count() != paused_hits:
            raise AssertionError("new image request started while batch was paused")
        resume_mark = term.mark()
        term.send(" ")
        term.wait_for("Downloading", since=resume_mark)
        done_mark = term.mark()
        term.wait_for("komiku-cli / Done", timeout=20, since=done_mark)

        pack_mark = term.mark()
        term.send("p")
        term.wait_for("[PACKED]", timeout=15, since=pack_mark)
        evidence.record("03-live-pause-pack.png", "03 LIVE METRICS PAUSE RESUME DONE PACK", term.text(),
            ["komiku-cli / Download", "Speed ", "ETA ", "Errors ", "[PAUSED]", "Downloading", "komiku-cli / Done", "[PACKED]"],
            ["live metrics rendered from engine events", "pause stopped new image requests", "resume completed mapped download", "done summary enabled real pack and CBZ completed"])
        term.send("q")
        if term.wait_exit() != 0:
            raise AssertionError(term.text()[-4000:])
        if b"\x1b" in term.raw:
            raise AssertionError("TERM=dumb / NO_COLOR output contained ANSI escape sequences")
        try:
            term.raw.decode("ascii")
        except UnicodeDecodeError as error:
            raise AssertionError("TERM=dumb / NO_COLOR output contained non-ASCII bytes") from error

        series_dir = output / "actual-series"
        state = json.loads((series_dir / ".state.json").read_text())
        if state.get("done") != [1, 2]:
            raise AssertionError(f"full state={state}")
        archive = series_dir / "actual-series Volume 01.cbz"
        if not archive.is_file() or archive.stat().st_size == 0:
            raise AssertionError(f"archive missing: {archive}")
        logs = sorted(series_dir.glob("run-*.log"))
        if len(logs) != 1 or "summary DONE=2 PART=0 FAIL=0 NOIMG=0" not in logs[0].read_text():
            raise AssertionError(f"full audit={[path.read_text() for path in logs]}")
    finally:
        term.close()


def run_auto_promote_and_reopen_pack(fixture: Path, binary: Path, server: FixtureServer, root: Path, repo: Path, evidence: Evidence) -> None:
    server.state.clear()
    output = root / "auto-promote-output"
    config = write_config(root / "auto-promote-config", output)
    env = os.environ.copy()
    env.update({"TERM": "dumb", "NO_COLOR": "1"})
    term = Terminal([str(fixture), "--base-url", server.base_url, "--config", str(config), "--workers", "2"], env, repo)
    try:
        open_series(term, server.base_url + "/manga/actual-series/", expect_results=False)
        mark = term.mark()
        term.send("v")
        term.wait_for("[ ] Volume 01  ch 1-2", since=mark)
        term.send(b"\x1b[A")
        term.wait_for("> [ ] Volume 01  ch 1-2", since=mark)
        term.send(" ")
        term.wait_for("> [x] Volume 01  ch 1-2", since=mark)
        term.send("\r")
        term.wait_for("komiku-cli / Download", since=mark)
        term.wait_for("komiku-cli / Done", timeout=20, since=mark)
        done_view = term.text()[mark:]
        if "[PACK DISABLED]" in done_view:
            raise AssertionError(f"auto-promoted Done pack remained disabled\n{done_view[-4000:]}")
        pack_mark = term.mark()
        term.send("p")
        term.wait_for("[PACKED]", timeout=15, since=pack_mark)
        evidence.record("03-live-pause-pack.png", "03B GROUPED AUTO-PROMOTION TO DONE PACK", term.text(),
            ["[ ] Volume 01  ch 1-2", "> [x] Volume 01  ch 1-2", "komiku-cli / Download", "komiku-cli / Done", "[PACKED]"],
            ["grouped header selected the exact complete Wikipedia volume", "Enter promoted default-flat selection to mapped jobs", "Done pack was enabled without an r mapping workflow", ".pack.json was finalized before the process closed"])
        term.send("q")
        if term.wait_exit() != 0:
            raise AssertionError(term.text()[-4000:])
    finally:
        term.close()

    series_dir = output / "actual-series"
    manifest = json.loads((series_dir / ".pack.json").read_text())
    if [item.get("volume") for item in manifest.get("mappings", [])] != [1]:
        raise AssertionError(f"auto-promotion manifest={manifest}")
    archive = series_dir / "actual-series Volume 01.cbz"
    archive.unlink()
    requests_before = server.state.count()
    offline = subprocess.run([str(binary), "pack", str(series_dir), "--vol", "1", "--preset", "raw"], cwd=repo, env=env, text=True, capture_output=True)
    if offline.returncode != 0 or not archive.is_file():
        raise AssertionError(f"post-close offline pack failed: stdout={offline.stdout} stderr={offline.stderr}")
    if server.state.count() != requests_before:
        raise AssertionError("post-close offline pack made an HTTP request")
    evidence.record("03-live-pause-pack.png", "03C POST-CLOSE OFFLINE MANIFEST PACK", offline.stdout,
        ["packed:", "preset=raw"],
        ["fresh process reopened .pack.json", "CBZ was recreated from root-relative sources", "offline pack made zero HTTP requests"])


def run_mid_batch_stop(fixture: Path, server: FixtureServer, root: Path, repo: Path, evidence: Evidence) -> None:
    server.state.clear()
    output = root / "cancel-output"
    config = write_config(root / "cancel-config", output)
    env = os.environ.copy()
    env.update({"TERM": "dumb", "NO_COLOR": "1"})
    term = Terminal([str(fixture), "--base-url", server.base_url, "--config", str(config), "--workers", "1"], env, repo)
    try:
        open_series(term, "actual", expect_results=True)
        mark = term.mark()
        term.send("r")
        term.wait_for("komiku-cli / Range", since=mark)
        for mode in ("[volume]", "[manual]", "[flat]"):
            tab_mark = term.mark()
            term.send("\t")
            term.wait_for(mode, since=tab_mark)
        value_mark = term.mark()
        term.send("all")
        term.wait_for("> all", since=value_mark)
        term.send("\r")
        term.wait_for("Selected 4 discovered chapter(s) in flat layout.", since=mark)
        term.send("\r")
        term.wait_for("komiku-cli / Download", since=mark)
        term.wait_predicate(lambda: server.state.saw("2", 1), timeout=25)
        term.send("q")
        if term.wait_exit(timeout=15) != 0:
            raise AssertionError(term.text()[-4000:])
        cancel_view = term.text()
        for transition in ("[DONE] ch 1", "[PART] ch 2"):
            if transition not in cancel_view:
                raise AssertionError(f"mixed live tail missing {transition!r}\n{cancel_view[-4000:]}")
        if re.search(r"Errors [1-9][0-9]*", cancel_view) is None:
            raise AssertionError(f"live error counter did not change\n{cancel_view[-4000:]}")

        series_dir = output / "actual-series"
        state = json.loads((series_dir / ".state.json").read_text())
        if state.get("done") != [1]:
            raise AssertionError(f"cancel state={state}")
        if server.state.saw("3") or server.state.saw("271-5"):
            raise AssertionError("pending chapters were fetched after safe stop")
        logs = sorted(series_dir.glob("run-*.log"))
        if len(logs) != 1:
            raise AssertionError(f"cancel logs={logs}")
        audit = logs[0].read_text()
        if "chapter 1: DONE" not in audit or "chapter 2: PART" not in audit or "chapter 3" in audit or "chapter 271.5" in audit:
            raise AssertionError(f"cancel audit={audit}")
        evidence.record("04-graceful-stop.png", "04 MID-RUN GRACEFUL STOP", cancel_view,
            ["[DONE] ch 1", "[PART] ch 2", "[STOPPING]", "Errors "],
            ["q requested graceful cancellation", "completed chapter remained DONE", "active chapter became PART", "pending chapters 3 and 271.5 were not fetched", "audit closed before process exit"])
    finally:
        term.close()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--fixture", required=True, type=Path)
    parser.add_argument("--repo", default=Path.cwd(), type=Path)
    parser.add_argument("--evidence-transcript", type=Path)
    args = parser.parse_args()
    binary = args.binary.resolve()
    fixture = args.fixture.resolve()
    repo = args.repo.resolve()
    if not binary.is_file() or not fixture.is_file():
        raise SystemExit("build --binary and --fixture before running the smoke")

    server = FixtureServer()
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="komiku-tui-smoke-") as directory:
            root = Path(directory)
            evidence = Evidence(args.evidence_transcript.resolve() if args.evidence_transcript else None, root, server.base_url)
            run_production_no_args(binary, server, root, repo, evidence)
            run_full_fixture(fixture, server, root, repo, evidence)
            run_auto_promote_and_reopen_pack(fixture, binary, server, root, repo, evidence)
            run_mid_batch_stop(fixture, server, root, repo, evidence)
            evidence.write()
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
    print("TUI PTY smoke passed: no-args, grouped auto-promotion, Done pack, post-close offline pack, pause/resume, and safe stop")


if __name__ == "__main__":
    main()
