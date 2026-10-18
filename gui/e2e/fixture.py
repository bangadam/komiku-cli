#!/usr/bin/env python3
"""Build a hermetic komiku-cli library fixture for the GUI E2E suite.

The fixture is a real library as far as the CLI is concerned: chapter folders
with valid image pages, .state.json files, packed CBZ archives and cover art.
Everything lives under a temp HOME so the tests never touch the real library.
"""
from __future__ import annotations

import json
import os
import shutil
import struct
import subprocess
import sys
import zlib
from pathlib import Path

# A 1x1 JPEG padded to clear store.MinResumeSize (10 KB). Padding sits after
# the EOI marker, which the magic-byte check ignores.
JPEG_1PX = bytes.fromhex(
    "ffd8ffe000104a46494600010100000100010000ffdb004300"
    "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
    "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
    "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
    "ffc0000b080001000101011100ffc400140001000000000000000000000000"
    "00000000ffda0008010100003f00d2cf20ffd9"
)
PNG_1PX = bytes.fromhex(
    "89504e470d0a1a0a0000000d4948445200000001000000010806000000"
    "1f15c4890000000d4944415478da63f8ffff3f0005fe02fea735d5c700"
    "00000049454e44ae426082"
)


def pad(blob: bytes, size: int = 12 * 1024) -> bytes:
    return blob + b"\x00" * max(0, size - len(blob))


def write_pages(chapter_dir: Path, count: int, broken: int = 0, missing=()) -> None:
    chapter_dir.mkdir(parents=True, exist_ok=True)
    for page in range(1, count + 1):
        if page in missing:
            continue
        blob = pad(PNG_1PX if page % 2 == 0 else JPEG_1PX)
        if page <= broken:
            # Valid size, invalid magic bytes: the verify command flags these.
            blob = b"NOTANIMAGE" + b"\x00" * (12 * 1024 - 10)
        (chapter_dir / f"{page:03d}.jpg").write_bytes(blob)


def write_cbz(path: Path, pages: int) -> None:
    """Write a minimal stored (uncompressed) ZIP archive."""
    import zipfile

    path.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(path, "w", zipfile.ZIP_STORED) as archive:
        for page in range(1, pages + 1):
            archive.writestr(f"{page:03d}.jpg", pad(JPEG_1PX, 2048))


def make_cover(path: Path) -> None:
    path.write_bytes(pad(PNG_1PX, 4096))


def build(root: Path) -> dict:
    if root.exists():
        shutil.rmtree(root)
    root.mkdir(parents=True)

    # 1. Complete flat series, fully healthy.
    sakamoto = root / "sakamoto-days"
    sakamoto.mkdir()
    write_pages(sakamoto / "chapter-180", 4)
    write_pages(sakamoto / "chapter-181", 4)
    write_pages(sakamoto / "chapter-182", 4)
    (sakamoto / ".state.json").write_text(json.dumps({"done": [180, 181, 182]}))
    make_cover(sakamoto / "cover.jpg")
    write_cbz(sakamoto / "Sakamoto Days Chapters 180-182.cbz", 12)

    # 2. Mapped series with volume folders and one broken chapter.
    dandadan = root / "dandadan"
    dandadan.mkdir()
    write_pages(dandadan / "vol-01" / "chapter-001", 4)
    write_pages(dandadan / "vol-01" / "chapter-002", 4)
    write_pages(dandadan / "vol-01" / "chapter-003", 4, broken=1)
    (dandadan / ".state.json").write_text(json.dumps({"done": [1, 2, 3]}))
    make_cover(dandadan / "cover.png")

    # 3. Partial series with missing chapters and a stray file.
    blue = root / "blue-lock"
    blue.mkdir()
    write_pages(blue / "chapter-210", 4)
    write_pages(blue / "chapter-211", 4)
    (blue / ".state.json").write_text(json.dumps({"done": [210, 211]}))
    (blue / "chapter-210" / ".DS_Store").write_bytes(b"stray")

    # 4. Series that is missing pages in the middle.
    versus = root / "versus"
    versus.mkdir()
    write_pages(versus / "chapter-01", 6, missing=(3, 4))
    (versus / ".state.json").write_text(json.dumps({"done": [1]}))
    make_cover(versus / "cover.jpg")

    # 5. Partially downloaded series: three chapters on disk, one marked done.
    partial = root / "jujutsu-kaisen"
    partial.mkdir()
    write_pages(partial / "chapter-100", 3)
    write_pages(partial / "chapter-101", 3)
    write_pages(partial / "chapter-102", 3)
    (partial / ".state.json").write_text(json.dumps({"done": [100]}))
    make_cover(partial / "cover.jpg")

    # 6. Empty series folder (download started, nothing finished).
    (root / "20th-century-boys").mkdir()

    return {
        "root": str(root),
        "series": ["sakamoto-days", "dandadan", "blue-lock", "versus"],
    }


def write_config(home: Path, library: Path) -> Path:
    config_dir = home / "Library" / "Application Support" / "komiku-cli"
    config_dir.mkdir(parents=True, exist_ok=True)
    config = {
        "output_root": str(library),
        "image_delay": "200ms",
        "preset": "medium",
    }
    path = config_dir / "config.json"
    path.write_text(json.dumps(config, indent=2))
    return path


def main() -> int:
    base = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("/tmp/komiku-e2e")
    library = base / "library"
    home = base / "home"
    if home.exists():
        shutil.rmtree(home)
    home.mkdir(parents=True)

    info = build(library)
    config_path = write_config(home, library)

    binary = Path(__file__).resolve().parents[2] / "komiku-cli"
    result = subprocess.run(
        [str(binary), "library", "--json"],
        env={**os.environ, "HOME": str(home)},
        capture_output=True,
        text=True,
        cwd=str(binary.parent),
    )
    if result.returncode != 0:
        print(result.stderr, file=sys.stderr)
        return 1
    report = json.loads(result.stdout)
    print(json.dumps({**info, "home": str(home), "config": str(config_path),
                      "cli_series": [s["slug"] for s in report["series"] or []],
                      "cli_total_chapters": report["total_chapters"]}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
