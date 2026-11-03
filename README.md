# komiku-cli

A keyboard-first manga downloader and CBZ packer for
[Komiku](https://komiku.org), written in Go.

`komiku-cli` searches Komiku, downloads chapters, tracks what you already have,
and packs finished series into CBZ archives you can read offline. Use the
interactive terminal UI or run the same commands headlessly from scripts. A
desktop GUI in `gui/` drives the real binary.

## Features

- Interactive TUI (Bubble Tea + Lip Gloss) operated entirely by keyboard
- Search by title, or open a Komiku series URL directly
- Select chapters by number, range, or mapped volume
- Resume interrupted downloads without re-fetching valid pages
- Paced requests with retries, image validation, and safe cancellation
- Atomic CBZ packing with size and quality presets, plus a lossless `raw` mode
- Pack metadata saved to disk, so later repacks need no network
- One-time recovery for flat downloads created before pack metadata existed
- `pack --flat` packs an unmapped chapter run into one CBZ, with no mapping or network
- Cobra command tree with generated `--help` for every command
- Headless downloads with JSON reports for scripts (`dl --no-tui --json`)
- Headless search and series inspection with JSON output (`search`, `info`)
- Discovery commands: `ranking`, `trending`, `latest`, `new`, `random`, `catalog`, `genres`, and `genre <name>`
- Cover art saved into the series folder (`cover`, `cover --all`, or automatically after `dl`)
- Chapter selectors `--ch all`, `--ch missing`, and `--ch latest:N`
- Offline integrity check with `verify <series-dir>`
- One-command catch-up across the library with `update`
- Online and offline chapter page resolution for the desktop reader (`read`), with a local page cache
- Desktop GUI (Tauri + React) in `gui/`, with a browser dev bridge and a Playwright E2E suite

## Requirements

- Network access for search, discovery, downloads, and cover fetches
- A terminal with interactive input for the TUI
- Go 1.26 or newer to install or build from source

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

Put the resulting binary somewhere on your `PATH`.

## Quick start

Launch the TUI:

```sh
komiku-cli
```

`komiku-cli tui` opens the same TUI and accepts `--out`.

The sidebar has four screens:

1. `Search`: find a title or open a Komiku URL, pick chapters, then download.
2. `To CBZ`: pack an existing download without fetching images again.
3. `Downloads`: review local download status and pick a series to pack.
4. `Settings`: change the download folder and the CBZ preset.

Use a different library folder for one session:

```sh
komiku-cli --out "/path/to/manga"
```

Show the command and flag reference:

```sh
komiku-cli --help
komiku-cli dl --help
```

### Headless download

`dl` runs headless only, so pass `--no-tui`. Download selected chapters:

```sh
komiku-cli dl https://komiku.org/manga/example/ --ch 1-20,25.5 --no-tui
```

Download mapped volumes and pack them immediately:

```sh
komiku-cli dl https://komiku.org/manga/example/ \
  --vol 1-3 --no-tui --pack --preset medium
```

Pass exactly one of `--ch` or `--vol`. `--pack` requires a mapped `--vol`
selection. Chapter selections store pages without volume folders.

### Search and inspect series

Find a series from the terminal, or pipe the JSON into `jq`:

```sh
komiku-cli search "sakamoto days"
komiku-cli search "sakamoto days" --json | jq -r '.[0].url'
```

Inspect what a series offers and what you already have locally before
downloading:

```sh
komiku-cli info https://komiku.org/manga/example/
```

Every chapter is marked `[x]` when it is already complete in the configured
library folder, or `[ ]` when it is still missing:

```text
example  chapters=2 done=1
[x] 1  https://komiku.org/example-chapter-1/
[ ] 2  https://komiku.org/example-chapter-2/
```

`info --json` returns the same data as a machine-readable report.

### Cover art

Cover art comes from the series page on Komiku and is saved in the series
folder as `cover.jpg` (or `.jpeg` / `.png` / `.webp`, matching the source URL):

```sh
komiku-cli cover https://komiku.org/manga/example/
komiku-cli cover --all          # backfill every series in the library
komiku-cli cover --all --force  # refresh existing covers
```

`dl` saves the cover after a download batch finishes. Pass `--cover=false` to skip it.
Covers are decoration: a failed cover fetch never fails a download.

The GUI reads the saved files and shows them in the library grid, the series
hero, and search results. The **Fetch covers** button on the library screen runs
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
done, and downloads only the remainder. That makes it a good fit for a weekly
cron job:

```cron
0 9 * * mon  komiku-cli dl https://komiku.org/manga/example/ --ch missing --no-tui --json
```

When nothing is missing, the command prints
`no missing chapters: <series> is already complete (<N> done)` and exits 0, so
cron jobs stay quiet. `dl --json` prints a structured report instead of the
human-readable summary lines: requested and started chapter counts, per-chapter
results with page counts, and the audit log path.

## Library catch-up

Download missing chapters for every series already in your library with one
command:

```sh
komiku-cli update                  # catch up on downloaded library series
komiku-cli update --check          # dry run: report missing chapters only
komiku-cli update --json           # machine-readable report
komiku-cli update --out /path/to/manga  # use a different library root
```

`update` scans the configured library folder, discovers each local series, skips
chapters already marked done, and downloads only the remainder. Each series
prints one line:

```text
example  new=0 downloaded=0 skipped=271 failed=0
```

`--check` polls the series pages without downloading or writing state, so you
can preview what `update` would fetch. An empty library prints
`no series downloaded yet` and exits 0.

### Library dashboard

One offline command shows everything you own:

```sh
komiku-cli library
```

```text
library: /manga  series=2  chapters=213  done=200  cbz=28  size=8.2G  problems=1
frieren  chapters=12   done=12   cbz=2   size=412.3M   OK
jjk      chapters=201  done=188  cbz=26  size=7.8G     BROKEN(1)
  chapter-107: broken=0 missing=1
```

It scans your library offline and reports chapter counts, done progress, packed
CBZ archives, disk usage, and per-chapter integrity (the same broken/missing
checks as `verify`). `--json` returns the full dashboard for scripting, and
directories that are not series are skipped.

## Discovery

Browse komiku.org without downloading anything. Every command supports `--json`
for scripts and for the desktop GUI.

```sh
komiku-cli ranking --period daily          # daily | weekly | monthly | all
komiku-cli trending --period 7d --limit 20 # 24h | 7d | 30d | active
komiku-cli latest --type manga --page 2    # newest chapters
komiku-cli new --type manhwa               # newest titles
komiku-cli random                          # random selection
komiku-cli catalog --letter A --page 2     # A–Z catalogue with per-letter counts
komiku-cli catalog --status ongoing        # filter by publication status
komiku-cli genres                          # genre index with title counts
komiku-cli genre action --sort rating      # update | date | rating | random
komiku-cli latest --genre action --genre2 fantasy   # combine two genres
```

`latest`, `new`, and `random` share the same filters: `--type`, `--genre`,
`--genre2`, and `--page`.

Every series entry has the same shape: title, slug, URL, cover, type, genres,
status, rank, views, latest chapter, and update time. `search --json` prints an
array of entries. The other discovery commands print a report object: `latest`,
`new`, `random`, `genre`, `ranking`, `trending`, and `catalog` put their entries
in `series`, and `genres` returns the genre index. Cover URLs are rewritten from
`thumbnail.komiku.to` to `thumbnail.komiku.org`, because the `.to` host does not
resolve on every network.

## Offline packing

Downloads with `.pack.json` can be packed later with no network requests:

```sh
komiku-cli pack "/path/to/manga/example"
komiku-cli pack "/path/to/manga/example" --vol 1-3 --preset raw
```

Older flat downloads need one recovery run to find volume boundaries:

```sh
komiku-cli pack "/path/to/manga/example" \
  --recover-wikipedia --wikipedia-title "Example"
```

If you already know the mapping (for example, because the desktop app shows
it), pass it with `--volume-map` and the pack stays offline. The mapping is
saved as `.pack.json`, so later runs need neither the flag nor a lookup:

```sh
komiku-cli pack "/path/to/manga/example" \
  --volume-map "1:1-5,2:6-14,3:15-23" --vol 1-3 --preset raw
```

`info --offline` prints the same mapping from disk (provider cache first, then
`.pack.json`) and never touches the network. The desktop app uses it to render
the volume list before packing:

```sh
komiku-cli info "https://komiku.org/manga/example/" --offline --volumes --json
```

Volume mappings come from **MangaDex by default**. The query has no language
filter, because MangaDex spreads volume labels across scanlation languages and
an English-only query drops most of them. Pick another provider with
`--volume-source`:

| Value | Behaviour |
| --- | --- |
| `mangadex` (default) | MangaDex aggregate endpoint only |
| `auto` | MangaDex, then Wikipedia list/main article/search, then Fandom |
| `wikipedia` | English Wikipedia only (`--recover-wikipedia` selects it) |
| `fandom` | Community Fandom wiki only |

`--volume-source` works on `dl`, `pack`, and `info`. An explicit value wins over
an existing `.volumes.json` cache. Without the flag, the cache is authoritative,
so editing that file pins a manual mapping.

MangaDex coverage is uneven: a mapping can stop several volumes short or have
gaps (for example, Dandadan has volumes 1-10 and 23 but nothing in between).
When that happens, `--vol` names the missing volumes and the volumes the mapping
does cover. Retry with `--volume-source auto` to fall back to another provider,
or write the mapping yourself.

Chapters that no published volume covers yet (for example, a weekly run of the
newest chapters) can go into a single CBZ without any volume mapping or network
access. Missing chapters are tolerated and packed around; the archive name spans
the first-to-last chapter on disk, so gaps do not show in the name. Fractional
chapters (1.1, 2.5, ...) have no volume to group them and are packed inline
between their integer neighbours:

```sh
komiku-cli pack "/path/to/manga/newest" \
  --flat --series "Sakamoto Days" --preset raw
```

This writes `Sakamoto Days Chapters 256-275.cbz` (a single chapter becomes
`... Chapter 256.cbz`) and no `.pack.json`. Rerun it after downloading more
chapters to pack the wider range.

Flat packing refuses a series that already has a `.pack.json`; use the normal
`pack` command there.

Recovery may look up a volume mapping online, but neither recovery nor flat
packing requests Komiku chapter pages or downloads images. Normal pack runs stay
offline.

## CBZ presets

| Preset | Longest side | JPEG quality | Source bytes |
| --- | ---: | ---: | --- |
| `medium` | 1600 px | 72 | Re-encoded when decodable |
| `small` | 1400 px | 65 | Re-encoded when decodable |
| `tiny` | 1200 px | 60 | Re-encoded when decodable |
| `raw` | Unchanged | Unchanged | Preserved |

Resizing keeps the original aspect ratio and never enlarges smaller images. If a
page cannot be decoded, the original bytes are copied unchanged. Archives are
written to temporary `.part` files and published atomically.

## Configuration

Set the default download location:

```sh
komiku-cli config --out "/path/to/manga"
```

Inspect the current configuration:

```sh
komiku-cli config
komiku-cli config --json
```

Configuration lives in `komiku-cli/config.json` under your user config directory
(`os.UserConfigDir()`), with these fields:

```json
{
  "output_root": "/path/to/manga",
  "image_delay": "200ms",
  "preset": "medium"
}
```

CLI flags override saved values for the current run. The defaults are the
current directory, a `200ms` image delay, and the `medium` preset.

## Download safety

- Every command that takes a series URL validates it against Komiku first.
- A chapter is marked complete only after every expected page succeeds.
- In the TUI, `q` stops downloads and packing at a safe boundary.
- Pack manifests use root-relative paths and reject unsafe source traversal.
- CBZ output is atomic; a failed pack never publishes a partial archive.

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
`gui/vite-plugin-cli-bridge.ts`, applies only while the dev server runs
(`apply: "serve"`), and never ships in a production build.

End-to-end tests drive the real binary through the UI with a hermetic `HOME`:

```sh
cd gui
python3 e2e/run.py            # full suite
python3 e2e/run.py --headed   # watch it run
python3 e2e/fixture.py /tmp/komiku-e2e   # rebuild the fixture library
```

The fixture creates a real library (chapter folders with valid image pages,
`.state.json`, CBZ archives, cover art), so the GUI is exercised against actual
CLI output rather than mocks.

### Cloudflare challenges

Komiku protects chapter pages with a Cloudflare challenge that rejects plain
HTTP clients with `403 Forbidden`. When a Camoufox-based solver is available,
`komiku-cli` hands the challenged URL to it and retries with the browser
session it returns:

```sh
python3 -m pip install camoufox
komiku-cli config --solver "$PWD/tools/cf-solver/solve.py"
```

`tools/cf-solver/solve.py` ships with the repository. Without an explicit
`--solver` value the binary looks for it next to itself and in the working
tree. The solved `cf_clearance` cookie and the User-Agent it is bound to are
cached in `cf-session.json` next to the config file, so one solve covers later
requests until Cloudflare challenges again. Without a solver, challenged pages
keep failing with the plain `403` error.

## Disclaimer

This project is an independent client and is not affiliated with Komiku.
Follow the source website's terms and applicable law. Download only content
you are authorized to access.

## License

Released under the [MIT License](LICENSE).
