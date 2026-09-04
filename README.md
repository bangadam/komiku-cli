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
- Cobra command tree with generated help for TUI, download, pack, and config workflows
- Headless mode for scripts and automation
- Headless search (`search`) and series inspection (`info`) with JSON output for scripts
- `--ch all`, `--ch missing`, and `--ch latest:N` selectors for full-series, gap-filling, and tail downloads
- Offline `verify <series-dir>` integrity check (magic bytes, page gaps, stray files)
- `serve` command: a private offline web reader for your downloaded library (folders and CBZ), LAN-accessible
- `subscribe` / `unsubscribe` / `subs` / `update` — library subscriptions with one-command catch-up across every subscribed series

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


## Local web reader

Serve your downloaded library as a private, offline manga reader you open in
any browser — including a phone or tablet on the same network. It reads your
chapter folders **and** packed CBZ archives directly; it never contacts
Komiku.

```sh
komiku-cli serve
```

Open the printed local URL. To read on a phone, bind to all interfaces and
scan the printed LAN URLs:

```sh
komiku-cli serve --addr 0.0.0.0:8080
```

The reader UI is a self-contained single-page app with no external
dependencies: dark theme, keyboard navigation (arrows / escape), and
per-chapter page counts sourced offline. `serve --json` prints the local and
LAN URLs as JSON for scripting.

Path traversal into directories outside the library root is rejected, so the
server only ever exposes content under your configured `--out` directory.

## Subscriptions and catch-up

Track series and download new chapters across your entire library in one
command — ideal for a weekly cron:

```sh
komiku-cli subscribe https://komiku.org/manga/example/
komiku-cli subs                    # list tracked series
komiku-cli unsubscribe example     # stop tracking (slug or URL)
```

Catch up on every subscribed series at once:

```sh
komiku-cli update                  # download new chapters across all subs
komiku-cli update --check          # dry run: report new chapters only
komiku-cli update --json           # machine-readable report
```

`update` discovers each series, skips chapters already marked done, and
downloads only the remainder — the same gap-filling engine as `--ch missing`.
When a series is fully caught up, it reports `new=0 downloaded=0 skipped=N`.
`--check` does a read-only poll (no downloads, no state mutation), so you can
preview what `update` would fetch. Subscriptions persist in your user config
directory alongside `config.json`.

### Library dashboard

One offline command to see everything you own:

```sh
komiku-cli library
```

```text
library: /manga  series=2  chapters=213  done=200  cbz=28  size=8.2G  problems=1
frieren  chapters=12  done=12  cbz=2   size=412.3M  OK [subscribed]
jjk      chapters=201 done=188 cbz=26  size=7.8G    BROKEN(1)
  chapter-107: broken=0 missing=1
```

It scans your download root offline: chapter counts, done progress, packed
CBZ archives, disk usage, subscription status, and per-chapter integrity
(the same broken/missing checks as `verify`). `--json` returns the full
dashboard for scripting. Non-series directories are skipped.

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

Recovery reuses local images. It does not request Komiku pages or download
images. Normal pack runs remain offline.

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

## Disclaimer

This project is an independent client and is not affiliated with Komiku.
Follow the source website's terms and applicable law. Download only content
you are authorized to access.
