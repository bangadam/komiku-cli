//go:build online

package komiku

import (
	"context"
	"testing"
	"time"
)

func onlineClient() *Client {
	return NewClient(nil, 100*time.Millisecond)
}

func TestOnlineRankingAllPeriods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := onlineClient()
	for _, period := range []string{"harian", "mingguan", "bulanan", "total"} {
		series, err := client.Ranking(ctx, period)
		if err != nil {
			t.Fatalf("%s: %v", period, err)
		}
		if len(series) < 10 {
			t.Fatalf("%s: want at least 10 entries, got %d", period, len(series))
		}
		for _, entry := range series[:5] {
			if entry.Title == "" || entry.URL == "" || entry.Rank == 0 {
				t.Fatalf("%s: incomplete entry %+v", period, entry)
			}
			if entry.Cover == "" || entry.Views == 0 {
				t.Fatalf("%s: missing cover/views on %q", period, entry.Title)
			}
		}
	}
}

func TestOnlineTrendingWindows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := onlineClient()
	for _, period := range []string{"daily", "weekly", "monthly", "active"} {
		report, err := client.Trending(ctx, period, 10)
		if err != nil {
			t.Fatalf("%s: %v", period, err)
		}
		if len(report.Series) < 5 {
			t.Fatalf("%s: want at least 5 titles, got %d", period, len(report.Series))
		}
		first := report.Series[0]
		if first.Title == "" || first.Views == 0 || first.Slug == "" {
			t.Fatalf("%s: incomplete entry %+v", period, first)
		}
	}
}

func TestOnlineFeedsAndCatalog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	client := onlineClient()

	latest, err := client.LibraryFeed(ctx, "modified", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) < 5 {
		t.Fatalf("latest feed: want at least 5 entries, got %d", len(latest))
	}
	if latest[0].Title == "" || latest[0].Chapter == "" {
		t.Fatalf("latest feed: incomplete entry %+v", latest[0])
	}

	newest, err := client.LibraryFeed(ctx, "date", "manga", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(newest) < 5 {
		t.Fatalf("new feed: want at least 5 entries, got %d", len(newest))
	}

	catalog, err := client.Catalog(ctx, "A", "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.TotalPages < 2 || len(catalog.Alphabet) < 20 {
		t.Fatalf("catalog: unexpected shape page=%d alphabet=%d", catalog.TotalPages, len(catalog.Alphabet))
	}
	if len(catalog.Series) < 20 {
		t.Fatalf("catalog: want at least 20 cards, got %d", len(catalog.Series))
	}

	page2, err := client.Catalog(ctx, "A", "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if page2.Page != 2 {
		t.Fatalf("catalog: want page 2, got %d", page2.Page)
	}
	if page2.Series[0].Title == catalog.Series[0].Title {
		t.Fatal("catalog: page 2 repeated page 1")
	}
}

func TestOnlineGenresAndGenreListing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	client := onlineClient()

	genres, err := client.Genres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(genres) < 20 {
		t.Fatalf("want at least 20 genres, got %d", len(genres))
	}
	var action Genre
	for _, genre := range genres {
		if genre.Slug == "action" {
			action = genre
		}
	}
	if action.Count == 0 {
		t.Fatal("action genre missing a count")
	}

	series, err := client.GenreSeries(ctx, "action", "update", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) < 5 {
		t.Fatalf("want at least 5 genre entries, got %d", len(series))
	}
	for _, entry := range series[:3] {
		if entry.Title == "" || entry.URL == "" {
			t.Fatalf("incomplete genre entry %+v", entry)
		}
	}
}
