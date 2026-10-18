# komiku-cli

A keyboard-first manga downloader and CBZ packer for
[Komiku](https://komiku.org), built in Go.

`komiku-cli` provides an interactive terminal interface for searching,
selecting, downloading, resuming, and packing manga. It also supports
non-interactive downloads and offline repacking for scripts and automation.

## Features

- Interactive Bubble Tea TUI styled with Lip Gloss and operated entirely by keyboard
- Search by title or open a Komiku series URL
- Select chapters, chapter ranges, or mapped Wikipedia volumes
- Resume incomplete downloads without replacing valid pages
- Global request pacing, retries, image validation, and graceful cancellation
- Pack complete volumes into atomic CBZ archives
- Kindle-oriented image presets plus lossless `raw` packing
- Persisted pack metadata for later offline repacking
- One-time recovery for downloads created before pack metadata existed
- Flat packing of unmapped chapter runs into a single CBZ (`pack --flat`), no volume mapping or network needed
- Cobra command tree with generated help for TUI, download, pack, and config workflows
- Headless mode for scripts and automation
- Headless search (`search`) and series inspection (`info`) with JSON output for scripts
- Cover art saved locally from the series page (`cover`, `cover --all`, or automatically after `dl`)
- `--ch all`, `--ch missing`, and `--ch latest:N` selectors for full-series, gap-filling, and tail downloads
- Offline `verify <series-dir>` integrity check (magic bytes, page gaps, stray files)
- `update` — one-command catch-up across downloaded series in your local library
- Headless discovery: `ranking`, `trending`, `latest`, `new`, `random`, `catalog`, `genres`, and `genre <name>` with JSON output
- Desktop GUI (Tauri + React) in `gui/` that drives the real binary, including a browser dev bridge and a Playwright E2E suite

## Requirements
- Network access for search and downloads
- A terminal with interactive input for the TUI

## Installation

Install the latest version with Go:

```sh
go install github.com/bangadam/komiku-cli/cmd/komiku-cli@latest
```

Or build from source:

```sh
git clone https://github.com/bangadam/komiku-cli.git
cd komiku-cli
go build -o komiku-cli ./cmd/komiku-cli
```

Place the resulting binary somewhere on your `PATH`.

## Quick start

Launch the TUI:

```sh
komiku-cli
```

The sidebar exposes four workflows:

1. `Search`: find a title or open a Komiku URL, select chapters, then download.
2. `To CBZ`: pack an existing download without fetching images again.
3. `Downloads`: inspect local download status and choose a series to pack.
4. `Settings`: change the download folder and CBZ preset.

Use a different storage folder for one session:

```sh
komiku-cli --out "/path/to/manga"
```

Inspect the generated command and flag reference:

```sh
komiku-cli --help
komiku-cli dl --help
```

### Headless download

Download selected chapters without opening the TUI:

```sh
komiku-cli dl https://komiku.org/manga/example/   --ch 1-20,25.5   --no-tui   --flat
```

Download mapped volumes and pack them immediately:

```sh
komiku-cli dl https://komiku.org/manga/example/ \
  --vol 1-3 --no-tui --pack --preset medium
```

`--ch` and `--vol` are mutually exclusive. `--pack` requires a mapped `--vol` selection.

### Search and inspect series

Find a series from the terminal and copy its URL, or pipe it into `jq`:

```sh
komiku-cli search "sakamoto days"
komiku-cli search "sakamoto days" --json | jq -r '.[0].URL'
```

Inspect what a series offers and what you already have locally before
downloading:

```sh
komiku-cli info https://komiku.org/manga/example/
```

Output marks every chapter `[x]` when it is already complete in the
configured download root, or `[ ]` when it is still missing:

```text
example  chapters=271 done=200
[x] 1  https://komiku.org/example-chapter-1/
[ ] 2  https://komiku.org/example-chapter-2/
...
```

`info --json` returns the same data as a machine-readable report.

### Cover art

Cover art comes from the series page on Komiku itself, saved next to the
chapters as `cover.jpg` (or `.png` / `.webp`):

```sh
komiku-cli cover https://komiku.org/manga/example/
komiku-cli cover --all          # backfill every series in the library
komiku-cli cover --all --force  # refresh existing covers
```

`dl` saves the cover automatically after a finished download. Pass `--cover=false`
to skip it. Covers are decoration: a failed cover fetch never fails a download.

The GUI reads the saved files and shows them in the library grid, the series
hero, and search results; the **Fetch covers** button on the library screen runs
the `--all` backfill.

Google Images is intentionally not used as a source: it blocks non-browser
clients, and its results are not reliably the official cover for the edition
being read.

### Catch-up and gap filling

Download every chapter a series offers, or only the ones you are missing:

```sh
komiku-cli dl https://komiku.org/manga/example/ --ch all --no-tui
komiku-cli dl https://komiku.org/manga/example/ --ch missing --no-tui
```

`--ch missing` re-checks the series page, skips every chapter already marked
done, and downloads only the remainder — ideal for a weekly cron job:

```cron
0 9 * * mon  komiku-cli dl https://komiku.org/manga/example/ --ch missing --no-tui --json
```

When nothing is missing, the command exits 0 and reports the series as
already complete, so cron jobs stay quiet. `dl --json` prints a structured
report (requested/started chapters, per-chapter status, page counts, audit
log path) instead of the human-readable summary lines.


## Library catch-up

Download missing chapters across series already downloaded to your local
library in one command:

```sh
komiku-cli update                  # catch up on downloaded library series
komiku-cli update --check          # dry run: report missing chapters only
komiku-cli update --json           # machine-readable report
komiku-cli update --out /path/to/manga  # use a different library root
```

`update` scans the configured download root, discovers each local series,
skips chapters already marked done, and downloads only the remainder — the
same gap-filling engine as `--ch missing`. When a series is fully caught up,
it reports `new=0 downloaded=0 skipped=N`.
`--check` does a read-only poll (no downloads, no state mutation), so you can
preview what `update` would fetch. An empty library reports no series to update.

### Library dashboard

One offline command to see everything you own:

```sh
komiku-cli library
```

```text
library: /manga  series=2  chapters=213  done=200  cbz=28  size=8.2G  problems=1
frieren  chapters=12  done=12  cbz=2   size=412.3M  OK
jjk      chapters=201 done=188 cbz=26  size=7.8G    BROKEN(1)
  chapter-107: broken=0 missing=1
```

It scans your download root offline: chapter counts, done progress, packed
CBZ archives, disk usage, and per-chapter integrity
(the same broken/missing checks as `verify`). `--json` returns the full
dashboard for scripting. Non-series directories are skipped.

## Discovery

Browse komiku.org without downloading anything. Every command supports
`--json` for scripts and for the desktop GUI.

```sh
komiku-cli ranking --period harian        # harian | mingguan | bulanan | total
komiku-cli trending --period 7d --limit 20  # 24h | 7d | 30d | active
komiku-cli latest --type manga --page 2   # newest chapters
komiku-cli new --type manhwa              # newest titles
komiku-cli random                         # random selection
komiku-cli catalog --letter A --page 2    # A–Z catalogue, with alphabet counts
komiku-cli genres                         # genre index with title counts
komiku-cli genre action --sort rating     # update | date | rating | random
komiku-cli latest --genre action --genre2 fantasy   # combine two genres
```

Each command returns the same series shape: title, slug, URL, cover,
type, genres, status, rank, views, latest chapter and update time. Cover
URLs are rewritten from `thumbnail.komiku.to` to `thumbnail.komiku.org`,
because the `.to` host does not resolve on every network.

## Offline packing

Downloads with `.pack.json` can be packed later with no network requests:

```sh
komiku-cli pack "/path/to/manga/example"
komiku-cli pack "/path/to/manga/example" --vol 1-3 --preset raw
```

Older flat downloads need one Wikipedia lookup to recover volume boundaries:

```sh
komiku-cli pack "/path/to/manga/example" \
  --recover-wikipedia --wikipedia-title "Example"
```

If you already know the mapping — for example because the desktop app is
showing it — pass it with `--volume-map` and the pack stays offline. The
mapping is saved as `.pack.json`, so later runs need neither the flag nor a
lookup:

```sh
komiku-cli pack "/path/to/manga/example" \
  --volume-map "1:1-5,2:6-14,3:15-23" --vol 1-3 --preset raw
```

`info --offline` prints the same mapping from disk (provider cache first, then
`.pack.json`) and never touches the network, which is what the desktop app
uses to render the volume list before packing:

```sh
komiku-cli info "https://komiku.org/manga/example/" --offline --volumes --json
```

Volume mappings come from **MangaDex by default**. It is queried without a
language filter, because MangaDex spreads volume labels across scanlation
languages and an English-only query drops most of them. Pick another provider
with `--volume-source`:

| Value | Behaviour |
| --- | --- |
| `mangadex` (default) | MangaDex aggregate endpoint only |
| `auto` | MangaDex, then Wikipedia list/main article/search, then Fandom |
| `wikipedia` | English Wikipedia only (one page fetch for `--recover-wikipedia`) |
| `fandom` | Community Fandom wiki only |

`--volume-source` works on `dl`, `pack`, and `info`. An explicit value wins
over an existing `.volumes.json` cache; without the flag the cache is
authoritative, so editing that file pins a manual mapping.

MangaDex coverage is uneven — it can stop several volumes short or have gaps
(e.g. Dandadan has 1-10 and 23 but nothing between). When that happens the
error names the missing volumes and the covered range, so you can retry with
`--volume-source auto` or write the mapping yourself.

Chapters no published volume covers yet (for example a weekly run of the
newest chapters) can be packed into a single CBZ without any volume mapping
or network access. Missing chapters are tolerated and packed around; the
archive name spans the first-to-last chapter on disk, so gaps are invisible
to the name. Fractional chapters (1.1, 2.5, ...) have no volume to group
them and are packed inline between their integer neighbours:

```sh
komiku-cli pack "/path/to/manga/newest" \
  --flat --series "Sakamoto Days" --preset raw
```

This writes `Sakamoto Days Chapters 256-275.cbz` (a single chapter becomes
`... Chapter 256.cbz`) and no `.pack.json`; rerun it after downloading more
chapters to pack the wider range.

Recovery and flat packing reuse local images. Neither requests Komiku pages
nor downloads images. Normal pack runs remain offline.

## CBZ presets

| Preset | Longest side | JPEG quality | Source bytes |
| --- | ---: | ---: | --- |
| `medium` | 1600 px | 72 | Re-encoded when decodable |
| `small` | 1400 px | 65 | Re-encoded when decodable |
| `tiny` | 1200 px | 60 | Re-encoded when decodable |
| `raw` | Unchanged | Unchanged | Preserved |

Resizing keeps the original aspect ratio and never enlarges smaller images.
Archives are written to temporary `.part` files and published atomically.

## Configuration

Set the default download location:

```sh
komiku-cli config --out "/path/to/manga"
```

Inspect the current configuration:

```sh
komiku-cli config
```

Configuration fields:

```json
{
  "output_root": "/path/to/manga",
  "image_delay": "200ms",
  "preset": "medium"
}
```

CLI flags override saved values for the current run. Default values are the
current directory, a `200ms` image delay, and the `medium` preset.

## Download safety

- Only validated Komiku series URLs are accepted in production.
- Existing pages are reused only after size and image-magic validation.
- A chapter is marked complete only after every expected page succeeds.
- `q` stops downloads and packing at a safe boundary.
- Pack manifests use root-relative paths and reject unsafe source traversal.
- CBZ output is atomic; failed packs do not publish partial archives.

## Development

Run the standard checks:

```sh
go test ./...
go test -race ./cli ./tui
go vet ./...
```

Build the CLI:

```sh
go build -o /tmp/komiku-cli ./cmd/komiku-cli
```

Run the live discovery tests (they hit komiku.org):

```sh
go test ./komiku/ -tags online -run TestOnline
```

### Desktop GUI

```sh
cd gui
pnpm install
pnpm desktop     # Tauri window
pnpm dev         # browser at :1420, backed by the real CLI through a dev bridge
```

`pnpm dev` serves the same CLI operations the Tauri build invokes over IPC, so
the browser renders real library and discovery data. The bridge lives in
`gui/vite-plugin-cli-bridge.ts`, is `apply: "serve"` only, and is tree-shaken
out of a production bundle.

End-to-end tests drive the real binary through the UI with a hermetic `HOME`:

```sh
cd gui
python3 e2e/run.py            # full suite
python3 e2e/run.py --headed   # watch it run
python3 e2e/fixture.py /tmp/komiku-e2e   # rebuild the fixture library
```

The fixture creates a real library (chapter folders with valid image pages,
`.state.json`, CBZ archives, cover art) so the GUI is exercised against actual
CLI output rather than mocks.

## Disclaimer

This project is an independent client and is not affiliated with Komiku.
Follow the source website's terms and applicable law. Download only content
you are authorized to access.
