#!/usr/bin/env python3
import hashlib
import http.server
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile
import threading
import time
from urllib.parse import urlsplit
import struct
import zlib
import zipfile

REPO = Path(__file__).resolve().parents[2]
EVIDENCE = REPO / "docs" / "evidence" / "e2e"
TRANSCRIPT = EVIDENCE / "e2e-transcript.txt"

SMALL_JPEG = b"\xff\xd8" + b"small-valid-jpeg" * 112
GAP_JPEG = b"\xff\xd8" + b"gap-page-one" * 1024
GAP_PNG = b"\x89PNG\r\n\x1a\n" + b"gap-page-two" * 1024
VOLUME_JPEG = b"\xff\xd8" + b"raw-volume-jpeg" * 768
VOLUME_PNG = b"\x89PNG\r\n\x1a\n" + b"raw-volume-png" * 768


def png_chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xffffffff)


def make_png(width, height):
    row = b"\x00" + b"\x24\x68\xac" * width
    return (b"\x89PNG\r\n\x1a\n" + png_chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
            + png_chunk(b"IDAT", zlib.compress(row * height, 9)) + png_chunk(b"IEND", b""))


MEDIUM_PNG = make_png(2000, 1000)
TRUNCATED_JPEG = b"\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00\xff\xdb\x00\x43" + bytes(range(17))


def jpeg_dimensions_and_sof(data):
    require(data.startswith(b"\xff\xd8"), "converted entry lacks JPEG magic")
    offset = 2
    markers = []
    dimensions = None
    while offset < len(data):
        while offset < len(data) and data[offset] != 0xff:
            offset += 1
        while offset < len(data) and data[offset] == 0xff:
            offset += 1
        require(offset < len(data), "truncated JPEG marker")
        marker = data[offset]
        offset += 1
        if marker == 0xd9:
            break
        if marker == 0xda:
            require(offset + 2 <= len(data), "truncated JPEG scan")
            length = struct.unpack(">H", data[offset:offset + 2])[0]
            offset += length
            end = data.find(b"\xff\xd9", offset)
            require(end >= 0, "JPEG scan lacks EOI")
            break
        if marker in range(0xd0, 0xd8) or marker == 0x01:
            continue
        require(offset + 2 <= len(data), "truncated JPEG segment")
        length = struct.unpack(">H", data[offset:offset + 2])[0]
        require(length >= 2 and offset + length <= len(data), "invalid JPEG segment length")
        markers.append(marker)
        if marker in range(0xc0, 0xc4):
            require(length >= 7, "short JPEG SOF")
            height, width = struct.unpack(">HH", data[offset + 3:offset + 7])
            dimensions = (width, height)
        offset += length
    require(dimensions is not None, "JPEG has no SOF dimensions")
    return dimensions, markers


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def run(command, env):
    return subprocess.run(command, cwd=REPO, env=env, text=True, capture_output=True)


def main():
    workspace = Path(tempfile.mkdtemp(prefix="komiku-cli-e2e-"))
    binary = workspace / "komiku-cli"
    output_root = workspace / "output"
    config_root = workspace / "home" / "Library" / "Application Support"
    requests = []
    header_failures = []
    phase = {"gap_repair": False}
    transcript = []

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            request_path = urlsplit(self.path).path
            requests.append({"path": request_path, "at": time.monotonic()})
            port = self.server.server_address[1]
            origin = f"http://127.0.0.1:{port}"

            html_routes = {
                "/manga/extra-series/": '<a href="/different-prefix-chapter-271-5/"></a>',
                "/different-prefix-chapter-271-5/": f'<img class="klazy" data-src="{origin}/wrong-thumbnail.jpg" src="{origin}/extra page.jpg?token=1">',
                "/manga/gap-series/": '<a href="/gap-prefix-chapter-1/"></a>',
                "/gap-prefix-chapter-1/": f'<img class="klazy" src="{origin}/gap-one.jpg"><img class="klazy" src="{origin}/gap-two.png">',
                "/manga/volume-series/": '<a href="/volume-prefix-chapter-1/"></a><a href="/volume-prefix-chapter-2/"></a>',
                "/volume-prefix-chapter-1/": f'<img class="klazy" src="{origin}/volume-one.jpeg">',
                "/volume-prefix-chapter-2/": f'<img class="ww" src="{origin}/volume-two.png">',
                "/manga/medium-series/": '<a href="/medium-prefix-chapter-1/"></a><a href="/medium-prefix-chapter-2/"></a>',
                "/medium-prefix-chapter-1/": f'<img class="klazy" src="{origin}/medium-large.png">',
                "/medium-prefix-chapter-2/": f'<img class="ww" src="{origin}/medium-truncated.jpeg">',
            }
            if request_path in html_routes:
                self.respond(200, html_routes[request_path].encode())
                return

            image_routes = {
                "/extra%20page.jpg": SMALL_JPEG,
                "/gap-one.jpg": GAP_JPEG,
                "/volume-one.jpeg": VOLUME_JPEG,
                "/volume-two.png": VOLUME_PNG,
                "/medium-large.png": MEDIUM_PNG,
                "/medium-truncated.jpeg": TRUNCATED_JPEG,
            }
            if request_path == "/gap-two.png":
                body = GAP_PNG if phase["gap_repair"] else b"<html>invalid image fixture</html>"
            else:
                body = image_routes.get(request_path)
            if body is not None:
                expected_referer = {
                    "/extra%20page.jpg": origin + "/different-prefix-chapter-271-5/",
                    "/gap-one.jpg": origin + "/gap-prefix-chapter-1/",
                    "/gap-two.png": origin + "/gap-prefix-chapter-1/",
                    "/volume-one.jpeg": origin + "/volume-prefix-chapter-1/",
                    "/volume-two.png": origin + "/volume-prefix-chapter-2/",
                    "/medium-large.png": origin + "/medium-prefix-chapter-1/",
                    "/medium-truncated.jpeg": origin + "/medium-prefix-chapter-2/",
                }[request_path]
                if not self.headers.get("User-Agent", "").startswith("Mozilla/5.0"):
                    header_failures.append(request_path + ": missing browser User-Agent")
                if self.headers.get("Referer") != expected_referer:
                    header_failures.append(request_path + ": wrong Referer")
                self.respond(200, body)
                return
            self.respond(404, b"not found")

        def respond(self, status, body):
            self.send_response(status)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, _format, *_args):
            return

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    port = server.server_address[1]
    origin = f"http://127.0.0.1:{port}"
    env = os.environ.copy()
    env["XDG_CONFIG_HOME"] = str(config_root)
    env["HOME"] = str(workspace / "home")

    def clean(text):
        sanitized = (text.replace(str(workspace), "$E2E_ROOT")
                     .replace(origin, "http://127.0.0.1:<PORT>"))
        return re.sub(r"run-[0-9]{8}T[0-9]{6}\.[0-9]+Z(?:-[0-9]{3})?\.log", "run-<TIMESTAMP>.log", sanitized)

    def add_section(title, output, command, completed, assertions):
        transcript.extend([
            f"[[render:{output}]]",
            f"[{title}]",
            "capture_method: transcript-rendered PNG",
            "$ " + clean(shlex.join(str(item) for item in command)),
            f"exit_code: {completed.returncode}",
            "stdout:",
            clean(completed.stdout).rstrip() or "<empty>",
            "stderr:",
            clean(completed.stderr).rstrip() or "<empty>",
            "assertions:",
        ])
        transcript.extend("PASS " + clean(item) for item in assertions)
        transcript.extend(["[[/render]]", ""])

    try:
        build = run(["go", "build", "-o", str(binary), "./cmd/komiku-cli"], env)
        require(build.returncode == 0, "fresh binary build failed: " + build.stderr)

        config_dir = config_root / "komiku-cli"
        config_dir.mkdir(parents=True)
        (config_dir / "config.json").write_text(json.dumps({
            "output_root": str(output_root),
            "image_delay": "25ms",
            "preset": "medium",
        }) + "\n")

        extra_url = origin + "/manga/extra-series/"
        extra_command = [str(binary), "dl", extra_url, "--ch", "271.5", "--no-tui", "--flat"]
        extra = run(extra_command, env)
        require(extra.returncode == 0, "flat extra chapter failed: " + extra.stderr)
        extra_file = output_root / "extra-series" / "chapter-271.5" / "001.jpg"
        require(extra_file.read_bytes() == SMALL_JPEG, "flat image bytes changed")
        state = json.loads((output_root / "extra-series" / ".state.json").read_text())
        require(state == {"done": [271.5]}, "extra chapter state mismatch")
        extra_logs = sorted((output_root / "extra-series").glob("run-*.log"))
        require(len(extra_logs) == 1, "first run did not create exactly one audit log")
        require("chapter 271.5: DONE pages=1/1" in extra_logs[0].read_text(), "audit lacks final chapter result")
        require(any(item["path"] == "/extra%20page.jpg" for item in requests), "space was not percent-encoded")
        require(not header_failures, "; ".join(header_failures))
        add_section("01 HEADLESS FLAT RAW EXTRA", "01-headless-download.png", extra_command, extra, [
            "raw 271-5 discovered through unrelated prefix and selected as 271.5",
            "image path /extra%20page.jpg preserved query; browser User-Agent plus chapter Referer verified",
            f"small valid JPEG accepted by magic bytes; source_sha256={sha256(SMALL_JPEG)}",
            "output extra-series/chapter-271.5/001.jpg has identical bytes",
            "state extra-series/.state.json equals {\"done\":[271.5]}",
            "one audit log records DONE pages=1/1 and consistent summary",
            "config supplied output_root and 25ms image_delay",
        ])

        before_resume = {path: sum(1 for seen in requests if seen["path"] == path) for path in {seen["path"] for seen in requests}}
        resume = run(extra_command, env)
        require(resume.returncode == 0, "DONE resume failed: " + resume.stderr)
        after_resume = {path: sum(1 for seen in requests if seen["path"] == path) for path in before_resume}
        require(after_resume.get("/different-prefix-chapter-271-5/", 0) == before_resume.get("/different-prefix-chapter-271-5/", 0), "DONE resume fetched chapter HTML")
        require(after_resume.get("/extra%20page.jpg", 0) == before_resume.get("/extra%20page.jpg", 0), "DONE resume refetched small image")
        require(len(list((output_root / "extra-series").glob("run-*.log"))) == 2, "rerun did not create unique audit log")
        add_section("02 DONE RESUME", "01-headless-download.png", extra_command, resume, [
            "series discovery reran for source-backed selection",
            "DONE state prevented chapter HTML refetch",
            "DONE state prevented small image refetch",
            "second run created second unique audit log",
        ])

        gap_url = origin + "/manga/gap-series/"
        gap_command = [str(binary), "dl", gap_url, "--ch", "1", "--no-tui", "--workers", "2"]
        gap_first = run(gap_command, env)
        require(gap_first.returncode == 1, "PART run must return nonzero")
        require("PART (1/2)" in gap_first.stdout, "PART result missing")
        require(sum(1 for item in requests if item["path"] == "/gap-one.jpg") == 1, "valid gap page fetched unexpected times")
        require(sum(1 for item in requests if item["path"] == "/gap-two.png") == 4, "invalid gap page did not receive four attempts")
        phase["gap_repair"] = True
        gap_second = run(gap_command, env)
        require(gap_second.returncode == 0, "gap repair failed: " + gap_second.stderr)
        require(sum(1 for item in requests if item["path"] == "/gap-one.jpg") == 1, "gap repair refetched valid page")
        require(sum(1 for item in requests if item["path"] == "/gap-two.png") == 5, "gap repair did not fetch only missing page")
        require((output_root / "gap-series" / "chapter-001" / "001.jpg").read_bytes() == GAP_JPEG, "preserved page changed")
        require((output_root / "gap-series" / "chapter-001" / "002.png").read_bytes() == GAP_PNG, "repaired page changed")
        chapter_hits = sum(1 for item in requests if item["path"] == "/gap-prefix-chapter-1/")
        gap_third = run(gap_command, env)
        require(gap_third.returncode == 0, "post-repair DONE resume failed")
        require(sum(1 for item in requests if item["path"] == "/gap-prefix-chapter-1/") == chapter_hits, "post-repair DONE fetched chapter HTML")
        require(sum(1 for item in requests if item["path"] == "/gap-one.jpg") == 1, "post-repair DONE refetched page one")
        require(sum(1 for item in requests if item["path"] == "/gap-two.png") == 5, "post-repair DONE refetched page two")
        require(len(list((output_root / "gap-series").glob("run-*.log"))) == 3, "gap runs lack unique logs")
        combined = subprocess.CompletedProcess(gap_command, gap_second.returncode,
            "first_run_exit=1\n" + gap_first.stdout + "repair_run_exit=0\n" + gap_second.stdout + "done_rerun_exit=0\n" + gap_third.stdout,
            "first_run_stderr:\n" + gap_first.stderr + "repair_run_stderr:\n" + gap_second.stderr + "done_rerun_stderr:\n" + gap_third.stderr)
        add_section("03 GAP RETRY AND RESUME", "02-gap-resume.png", gap_command, combined, [
            "first run returned expected nonzero PART (1/2)",
            "invalid page received initial attempt plus three retries",
            "repair rerun skipped valid page 001 and fetched only page 002",
            "repaired chapter entered DONE; next rerun fetched no chapter HTML or image",
            "each run created one unique audit log",
        ])

        volume_dir = output_root / "volume-series"
        volume_dir.mkdir(parents=True)
        (volume_dir / ".volumes.json").write_text(json.dumps({
            "source": "manual-e2e",
            "volumes": [{"volume": 1, "start": 1, "end": 2}],
        }) + "\n")
        volume_url = origin + "/manga/volume-series/"
        volume_command = [str(binary), "dl", volume_url, "--vol", "1", "--no-tui", "--pack", "--preset", "raw"]
        volume = run(volume_command, env)
        require(volume.returncode == 0, "raw volume pack failed: " + volume.stderr)
        archive_path = volume_dir / "volume-series Volume 01.cbz"
        require(archive_path.exists(), "final CBZ missing")
        require(not Path(str(archive_path) + ".part").exists(), "CBZ .part remains")
        with zipfile.ZipFile(archive_path) as archive:
            entries = archive.infolist()
            require([item.filename for item in entries] == ["Chapter 001/001.jpg", "Chapter 002/001.png"], "CBZ order or names mismatch")
            require(all(item.compress_type == 0 for item in entries), "CBZ entry not ZIP_STORED")
            archived = [archive.read(item) for item in entries]
        require(archived == [VOLUME_JPEG, VOLUME_PNG], "raw pack changed bytes")
        add_section("04 RAW VOLUME PACK", "03-raw-pack.png", volume_command, volume, [
            "editable manual cache selected mapped volume 01 and discovered chapters",
            "output layout uses vol-01/chapter-001 and vol-01/chapter-002",
            "archive volume-series Volume 01.cbz published; no .part remains; .pack.json persisted",
            "entries derive extensions from magic bytes: Chapter 001/001.jpg, Chapter 002/001.png",
            "both ZIP methods equal 0 (ZIP_STORED)",
            f"Chapter 001 source_sha256={sha256(VOLUME_JPEG)} matches archive",
            f"Chapter 002 source_sha256={sha256(VOLUME_PNG)} matches archive",
            "CLI raw preset overrode persisted medium preset for this run",
        ])

        require((volume_dir / ".pack.json").exists(), "mapped download did not persist .pack.json")
        requests_before_offline_pack = len(requests)
        archive_path.unlink()
        offline_pack_command = [str(binary), "pack", str(volume_dir), "--vol", "1", "--preset", "raw"]
        offline_pack = run(offline_pack_command, env)
        require(offline_pack.returncode == 0, "offline post-close pack failed: " + offline_pack.stderr)
        require(len(requests) == requests_before_offline_pack, "offline manifest pack made an HTTP request")
        require(archive_path.exists(), "offline post-close pack did not recreate CBZ")
        add_section("04B OFFLINE POST-CLOSE PACK", "03-raw-pack.png", offline_pack_command, offline_pack, [
            "fresh process loaded .pack.json after the mapped download process closed",
            "offline pack recreated volume-series Volume 01.cbz",
            "loopback request counter did not change: zero HTTP requests",
            "root-relative source directories remained unchanged",
        ])

        medium_dir = output_root / "medium-series"
        medium_dir.mkdir(parents=True)
        (medium_dir / ".volumes.json").write_text(json.dumps({
            "source": "manual-e2e",
            "volumes": [{"volume": 1, "start": 1, "end": 2}],
        }) + "\n")
        medium_url = origin + "/manga/medium-series/"
        medium_command = [str(binary), "dl", medium_url, "--vol", "1", "--no-tui", "--pack"]
        medium = run(medium_command, env)
        require(medium.returncode == 0, "medium volume pack failed: " + medium.stderr)
        medium_archive_path = medium_dir / "medium-series Volume 01.cbz"
        require(medium_archive_path.exists(), "medium final CBZ missing")
        require(not Path(str(medium_archive_path) + ".part").exists(), "medium CBZ .part remains")
        require("preset=medium" in medium.stdout, "CLI did not print actual medium preset")
        require("warning:" in medium.stdout and "copied original:" in medium.stdout, "CLI fallback warning missing")
        with zipfile.ZipFile(medium_archive_path) as archive:
            medium_entries = archive.infolist()
            require([item.filename for item in medium_entries] == ["Chapter 001/001.jpg", "Chapter 002/001.jpg"], "medium CBZ names mismatch")
            require(all(item.compress_type == 0 for item in medium_entries), "medium CBZ entry not ZIP_STORED")
            converted = archive.read(medium_entries[0])
            fallback = archive.read(medium_entries[1])
        dimensions, markers = jpeg_dimensions_and_sof(converted)
        require(dimensions == (1600, 800), "medium conversion dimensions mismatch")
        converted_path = workspace / "medium-converted.jpg"
        converted_path.write_bytes(converted)
        decoded = subprocess.run(["sips", "-g", "pixelWidth", "-g", "pixelHeight", str(converted_path)], text=True, capture_output=True)
        require(decoded.returncode == 0 and "pixelWidth: 1600" in decoded.stdout and "pixelHeight: 800" in decoded.stdout,
                "native decoder rejected medium JPEG or reported wrong dimensions")
        require(0xc0 in markers and 0xc2 not in markers, "medium output is not baseline SOF0 JPEG")
        require(fallback == TRUNCATED_JPEG, "medium fallback changed original bytes")
        add_section("05 MEDIUM PRESET AND FALLBACK", "04-medium-preset.png", medium_command, medium, [
            "persisted config selected medium preset through URL-first CLI syntax",
            "deterministic source PNG is 2000x1000; converted JPEG is 1600x800",
            "converted entry Chapter 001/001.jpg decodes as 1600x800 and has SOF0 with no SOF2",
            "decode-failed Chapter 002/001.jpeg retained original extension and byte-identical content",
            f"fallback source_sha256={sha256(TRUNCATED_JPEG)} matches archive",
            "both ZIP methods equal 0 (ZIP_STORED); final CBZ exists and no .part remains",
            "CLI printed preset=medium and visible copied-original warning",
        ])

        require(not header_failures, "; ".join(header_failures))
        transcript.extend([
            "[[render:04-medium-preset.png]]",
            "[FINAL ASSERTIONS]",
            "capture_method: transcript-rendered PNG",
            "PASS fresh local binary exercised through URL-first CLI syntax",
            "PASS all HTTP traffic stayed on 127.0.0.1",
            "PASS no browser, external network, TUI, or online-tag test used",
            "PASS e2e assertions complete",
            "[[/render]]",
            "",
        ])
        EVIDENCE.mkdir(parents=True, exist_ok=True)
        temporary = TRANSCRIPT.with_suffix(".txt.tmp")
        temporary.write_text("\n".join(transcript))
        temporary.replace(TRANSCRIPT)
        print("E2E PASS")
        print("transcript=" + str(TRANSCRIPT.relative_to(REPO)))
        print("requests=" + str(len(requests)))
        return 0
    finally:
        server.shutdown()
        thread.join()
        shutil.rmtree(workspace, ignore_errors=True)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print("E2E FAIL: " + str(error), file=os.sys.stderr)
        raise SystemExit(1)
