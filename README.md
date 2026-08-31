# komiku-cli

A keyboard-first manga downloader and CBZ packer for [Komiku](https://komiku.org), built in Go.

`komiku-cli` provides an interactive terminal interface for searching, selecting, downloading, resuming, and packing manga. It also supports non-interactive downloads and offline repacking for scripts and automation.

## Features

- Interactive Bubble Tea TUI with keyboard-only controls
- Search by title or open a Komiku series URL
- Select chapters, chapter ranges, or mapped Wikipedia volumes
- Resume incomplete downloads without replacing valid pages
- Global request pacing, retries, image validation, and graceful cancellation
- Pack complete volumes into atomic CBZ archives
- Kindle-oriented image presets plus lossless `raw` packing
- Persisted pack metadata for later offline repacking
- One-time recovery for downloads created before pack metadata existed
- Headless mode for scripts and automation

## Requirements

- Go 1.25 or newer
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

The home screen offers two workflows:

1. **Download manga** — choose a storage folder, search for a title, select chapters, then download.
2. **Pack downloaded manga** — choose an existing download and create CBZ archives without downloading images again.

Use a different storage folder for one session:

```sh
komiku-cli --out "/path/to/manga"
```

### Headless download

Download selected chapters without opening the TUI:

```sh
komiku-cli dl https://komiku.org/manga/example/   --ch 1-20,25.5   --no-tui   --flat
```

Download mapped volumes and pack them immediately:

```sh
komiku-cli dl https://komiku.org/manga/example/   --vol 1-3   --no-tui   --pack   --preset medium
```

`--ch` and `--vol` are mutually exclusive. `--pack` requires a mapped `--vol` selection.

## Offline packing

Downloads with `.pack.json` can be packed later with no network requests:

```sh
komiku-cli pack "/path/to/manga/example"
komiku-cli pack "/path/to/manga/example" --vol 1-3 --preset raw
```

Older flat downloads need one Wikipedia lookup to recover volume boundaries:

```sh
komiku-cli pack "/path/to/manga/example"   --recover-wikipedia   --wikipedia-title "Example"
```

Recovery reuses local images. It does not request Komiku pages or download images. Normal pack runs remain offline.

## CBZ presets

| Preset | Longest side | JPEG quality | Source bytes |
| --- | ---: | ---: | --- |
| `medium` | 1600 px | 72 | Re-encoded when decodable |
| `small` | 1400 px | 65 | Re-encoded when decodable |
| `tiny` | 1200 px | 60 | Re-encoded when decodable |
| `raw` | Unchanged | Unchanged | Preserved |

Resizing keeps the original aspect ratio and never enlarges smaller images. Archives are written to temporary `.part` files and published atomically.

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

CLI flags override saved values for the current run. Default values are the current directory, a `200ms` image delay, and the `medium` preset.

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

This project is an independent client and is not affiliated with Komiku. Follow the source website's terms and applicable law. Download only content you are authorized to access.
