# Komiku GUI

Desktop client for [`komiku-cli`](../README.md), built with **Tauri 2 + React 19 + TypeScript + Tailwind v4 + shadcn/ui**.

The interface mirrors the design canvas in `komiku-gui.pen` (pen.dev): a dark zinc surface with a single vermilion accent, Inter for UI text, and JetBrains Mono for counts, paths, and logs.

## Screens

| Screen | Mirrors | What it shows |
| --- | --- | --- |
| Library | `library` | Library totals, series list with progress and status, catch-up, integrity, recently packed |
| Search | `search` / `info` | Search box with filters, results with in-library state, URL hint |
| Series detail | `info` / `dl` | Hero metadata, chapter/volume tabs, selection, download setup, estimate |
| Downloads | `dl` | Batch progress, per-chapter jobs with retry state, live activity log |
| Pack to CBZ | `pack` / `info --offline` | One flow: volume selection when a mapping exists, otherwise a single-file pack; presets and output preview |
| Verify | `verify` | Per-chapter integrity report, stray files, not-in-state entries |
| Settings | `config` | Output root, preset, delay, download behavior, config file reference |

## Requirements

- Node 22+ and pnpm
- Rust 1.80+ with Cargo (for the Tauri shell)
- macOS, Linux, or Windows

## Development

```sh
cd gui
pnpm install
pnpm tauri dev      # desktop app with hot reload
pnpm dev            # browser-only frontend on http://localhost:1420
```

## Build

```sh
pnpm tauri build              # full bundle (dmg/app, deb, msi)
pnpm tauri build --no-bundle  # binary only
pnpm build                    # frontend typecheck + production bundle
```

## Project layout

```
gui/
├── src/
│   ├── components/
│   │   ├── ui/           # shadcn primitives (button, badge, card, input, tabs, …)
│   │   ├── sidebar.tsx   # shared app navigation
│   │   └── shared.tsx    # SeriesCover, StatusBadge, PageHeader
│   ├── screens/          # one file per screen
│   ├── lib/
│   │   ├── api.ts            # typed wrappers around every CLI command
│   │   ├── cli.ts            # Tauri bridge (run, stream, cancel)
│   │   ├── download-store.ts # live download state shared across screens
│   │   ├── use-async.ts      # loading/error/reload hook
│   │   ├── types.ts          # navigation and status types
│   │   └── utils.ts          # cn() helper
│   ├── index.css         # design tokens (@theme) + base styles
│   └── App.tsx           # screen routing
└── src-tauri/            # Rust shell and window config
```

## Design tokens

Defined in `src/index.css` under `@theme`. They map directly onto the pen.dev design system:

| Token | Value | Use |
| --- | --- | --- |
| `background` | `#09090B` | App background |
| `surface` | `#0F0F12` | Sidebar and cards |
| `card` | `#131317` | Inputs and nested surfaces |
| `muted` | `#18181B` | Chips, tracks, hover states |
| `border` / `border-subtle` | `#27272A` / `#1E1E22` | Dividers |
| `primary` | `#FF7A45` | Single accent action per screen |
| `success` / `warning` / `danger` / `info` | `#4ADE80` / `#FACC15` / `#F87171` / `#60A5FA` | Status semantics |

## Connecting to the CLI

Every screen calls the real `komiku-cli` binary through the Tauri bridge in
`src-tauri/src/lib.rs`. The bridge resolves the binary from `KOMIKU_CLI_BIN`,
then the repo layout, then `PATH`, and always runs it from `KOMIKU_CLI_CWD`
(defaults to the binary's directory) so a relative `output_root` still resolves.

| Screen | Commands |
| --- | --- |
| Library | `library --json`, `verify`, `update --check`, `update`, `dl` |
| Search | `search --json`, `library --json` |
| Series | `info --json [--volumes]`, `config --json`, `dl --progress` |
| Downloads | `dl --no-tui --progress` (streamed and cancellable) |
| Pack | `library --json`, `info --volumes`, `pack` |
| Verify | `library --json`, `verify --json` |
| Settings | `config --json [--out] [--delay] [--preset]`, `library --json` |

Library catch-up checks and updates series already downloaded under the configured output root.

There is no mock data layer. In a plain browser (`pnpm dev`) the bridge is
unavailable, so each screen shows an explicit notice instead of fake content.

Set `KOMIKU_CLI_TRACE=/tmp/cli.log` to record every invocation the GUI makes.
