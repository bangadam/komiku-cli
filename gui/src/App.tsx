import { useState } from "react"

import { Sidebar } from "@/components/sidebar"
import { EmptyState } from "@/components/states"
import { NewScreen } from "@/screens/baru"
import { CatalogScreen } from "@/screens/daftar"
import { DownloadsScreen } from "@/screens/downloads"
import { GenreScreen } from "@/screens/genre"
import { LibraryScreen } from "@/screens/library"
import { PackScreen } from "@/screens/pack"
import { RankingScreen } from "@/screens/peringkat"
import { ReaderScreen } from "@/screens/reader"
import { SearchScreen } from "@/screens/search"
import { SeriesScreen } from "@/screens/series"
import { SettingsScreen } from "@/screens/settings"
import { LatestScreen } from "@/screens/terbaru"
import { TrendingScreen } from "@/screens/trending"
import { VerifyScreen } from "@/screens/verify"
import { getLibrary, type LibraryReport } from "@/lib/api"
import { hasCliBridge } from "@/lib/cli"
import type { LibraryFilter, Screen } from "@/lib/types"
import { useAsync } from "@/lib/use-async"

export default function App() {
  const [screen, setScreen] = useState<Screen>("library")
  const [libraryFilter, setLibraryFilter] = useState<LibraryFilter>("all")
  const [series, setSeries] = useState<{ slug: string; url?: string } | null>(
    null
  )
  // The chapter the reader should open; "latest" when the user just wants to read.
  const [reader, setReader] = useState<{
    slug: string
    url: string
    chapter?: string
  } | null>(null)

  // One library scan feeds every screen and the sidebar.
  const library = useAsync<LibraryReport | null>(
    () => (hasCliBridge ? getLibrary() : Promise.resolve(null)),
    []
  )

  function librarySeriesUrl(slug: string): string | undefined {
    return library.data?.series?.find((item) => item.slug === slug)?.series_url
  }

  function openSeries(slug: string, url?: string) {
    setSeries({ slug, url })
    setScreen("series")
  }

  function openReader(slug: string, url: string, chapter?: string) {
    setReader({ slug, url, chapter })
    setScreen("reader")
  }

  function openCollection(filter: LibraryFilter) {
    setLibraryFilter(filter)
    setScreen("library")
  }

  if (screen === "reader" && reader) {
    return (
      <div className="h-full w-full overflow-hidden bg-background">
        <ReaderScreen
          slug={reader.slug}
          seriesURL={reader.url}
          initialChapter={reader.chapter}
          onBack={() => setScreen("series")}
          onOpenSeries={openSeries}
        />
      </div>
    )
  }

  return (
    <div className="flex h-full w-full overflow-hidden bg-background">
      <Sidebar
        active={screen}
        filter={libraryFilter}
        onSelect={setScreen}
        onCollection={openCollection}
        library={library.data}
      />
      <main className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {screen === "library" && (
          <LibraryScreen
            onOpenSeries={openSeries}
            onOpenDownloads={() => setScreen("downloads")}
            library={library}
            filter={libraryFilter}
            onFilterChange={setLibraryFilter}
          />
        )}
        {screen === "search" && (
          <SearchScreen onOpenSeries={openSeries} library={library.data} />
        )}
        {screen === "series" &&
          (series ? (
            <SeriesScreen
              slug={series.slug}
              url={series.url ?? librarySeriesUrl(series.slug)}
              onBack={() => setScreen("library")}
              onOpenPack={() => setScreen("pack")}
              onRead={(chapter) =>
                openReader(
                  series.slug,
                  series.url ??
                    librarySeriesUrl(series.slug) ??
                    `https://komiku.org/manga/${series.slug}/`,
                  chapter
                )
              }
            />
          ) : (
            <EmptyState
              title="No series selected"
              hint="Pick a series from the library or search."
            />
          ))}
        {screen === "terbaru" && <LatestScreen onOpenSeries={openSeries} />}
        {screen === "peringkat" && <RankingScreen onOpenSeries={openSeries} />}
        {screen === "trending" && <TrendingScreen onOpenSeries={openSeries} />}
        {screen === "baru" && <NewScreen onOpenSeries={openSeries} />}
        {screen === "daftar" && <CatalogScreen onOpenSeries={openSeries} />}
        {screen === "genre" && <GenreScreen onOpenSeries={openSeries} />}
        {screen === "downloads" && <DownloadsScreen />}
        {screen === "pack" && <PackScreen library={library} />}
        {screen === "settings" && <SettingsScreen library={library} />}
        {screen === "verify" && <VerifyScreen library={library} />}
      </main>
    </div>
  )
}
