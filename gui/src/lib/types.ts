/** Top-level navigation targets rendered by App.tsx. */
export type Screen =
  | "library"
  | "search"
  | "series"
  | "reader"
  | "terbaru"
  | "peringkat"
  | "trending"
  | "baru"
  | "daftar"
  | "genre"
  | "downloads"
  | "pack"
  | "settings"
  | "verify"

/** Collection shortcuts in the sidebar, applied to the library list. */
export type LibraryFilter = "all" | "attention"

/**
 * Visual status of a series in the library list. The labels follow the design
 * system: Up to date / N new / Downloading / Needs repair / Partial / Empty.
 */
export type SeriesStatus =
  | "complete"
  | "partial"
  | "broken"
  | "downloading"
  | "new"
