package komiku

import (
	"strings"
	"testing"
)

const rankingFixture = `<html><body>
<div class="tab-populer">
<button class="tab tab-rank-btn curr" onclick="filterRank(this,'harian')">Harian</button>
</div>
<div class="ls4w rank-panel" id="rank-harian">
<article class="ls4">
<div class="ls4v">
<a href="/manga/black-clover-indonesia/">
<img class="lazy" src="/asset/img/lazy.jpg" data-src="https://thumbnail.komiku.to/uploads/manga/black-clover-indonesia/manga_thumbnail-Manga-Black-Clover.jpg?resize=240,280" alt="Baca  Black Clover">
</a>
<span class="rank-num">1</span>
</div>
<div class="ls4j">
<h4><a href="/manga/black-clover-indonesia/" title="Baca Komik Black Clover">Black Clover</a></h4>
<span class="ls4s">Aksi · 51,246 views</span><br>
<a href="/black-clover-chapter-393/" class="ls24" title="Komik Black Clover Chapter 393">Chapter 393</a>
</div>
</article>
<article class="ls4">
<div class="ls4v">
<a href="/manga/overgeared-new/">
<img class="lazy" src="/asset/img/lazy.jpg" data-src="https://thumbnail.komiku.to/uploads/manga/overgeared-new/manga_thumbnail-Manhua-Overgeared-New.jpg?resize=240,280">
</a>
<span class="rank-num">2</span>
</div>
<div class="ls4j">
<h4><a href="/manga/overgeared-new/" title="Baca Komik Overgeared New">Overgeared New</a></h4>
<span class="ls4s">Fantasi · 20,860 views</span><br>
<a href="/overgeared-new-chapter-344/" class="ls24">Chapter 344</a>
</div>
</article>
</div>
<div class="ls4w rank-panel" id="rank-mingguan" style="display:none">
<article class="ls4">
<div class="ls4v">
<a href="/manga/ao-ashi/">
<img class="lazy" src="/asset/img/lazy.jpg" data-src="https://thumbnail.komiku.to/uploads/manga/ao-ashi/manga_thumbnail-Ao-Ashi-Cover.jpg">
</a>
<span class="rank-num">1</span>
</div>
<div class="ls4j">
<h4><a href="/manga/ao-ashi/" title="Baca Komik Ao Ashi">Ao Ashi</a></h4>
<span class="ls4s">Drama · 423,159 views</span><br>
<a href="/ao-ashi-chapter-410/" class="ls24">Chapter 410</a>
</div>
</article>
</div>
</section></body></html>`

func TestExtractRankingPicksRequestedPanel(t *testing.T) {
	daily, err := ExtractRanking([]byte(rankingFixture), "harian", "https://komiku.org/p/ranking/")
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 2 {
		t.Fatalf("want 2 daily entries, got %d", len(daily))
	}
	first := daily[0]
	if first.Title != "Black Clover" || first.Rank != 1 || first.Slug != "black-clover-indonesia" {
		t.Fatalf("unexpected first entry: %+v", first)
	}
	if first.Views != 51246 {
		t.Fatalf("want 51246 views, got %d", first.Views)
	}
	if first.Chapter != "Chapter 393" {
		t.Fatalf("want chapter label, got %q", first.Chapter)
	}
	if first.URL != "https://komiku.org/manga/black-clover-indonesia/" {
		t.Fatalf("want absolute URL, got %q", first.URL)
	}
	if first.Type != "Manga" {
		t.Fatalf("want type from cover filename, got %q", first.Type)
	}
	if len(first.Genres) != 1 || first.Genres[0] != "Aksi" {
		t.Fatalf("want genre Aksi, got %v", first.Genres)
	}
	// The lazy placeholder must lose to data-src, and the .to host is
	// unreachable from some networks.
	if !strings.HasPrefix(first.Cover, "https://thumbnail.komiku.org/") {
		t.Fatalf("want normalized cover, got %q", first.Cover)
	}

	weekly, err := ExtractRanking([]byte(rankingFixture), "mingguan", "https://komiku.org/p/ranking/")
	if err != nil {
		t.Fatal(err)
	}
	if len(weekly) != 1 || weekly[0].Title != "Ao Ashi" {
		t.Fatalf("weekly panel not isolated: %+v", weekly)
	}
}

func TestExtractRankingRejectsMissingPanel(t *testing.T) {
	if _, err := ExtractRanking([]byte(rankingFixture), "bulanan", "https://komiku.org/p/ranking/"); err == nil {
		t.Fatal("want error for a missing panel")
	}
}

func TestParseRankingAndTrendingPeriods(t *testing.T) {
	for input, want := range map[string]string{
		"":          "harian",
		"harian":    "harian",
		"daily":     "harian",
		"MINGGUAN":  "mingguan",
		"bulanan":   "bulanan",
		"total":     "total",
		"  weekly ": "mingguan",
	} {
		got, err := ParseRankingPeriod(input)
		if err != nil || got != want {
			t.Fatalf("ParseRankingPeriod(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := ParseRankingPeriod("yearly"); err == nil {
		t.Fatal("want error for an unknown period")
	}

	for input, want := range map[string]string{
		"":         "daily",
		"24h":      "daily",
		"7d":       "weekly",
		"30d":      "monthly",
		"active":   "active",
		"mingguan": "weekly",
	} {
		got, err := ParseTrendingPeriod(input)
		if err != nil || got != want {
			t.Fatalf("ParseTrendingPeriod(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestExtractTrendingHandlesScalarAndObjectViews(t *testing.T) {
	scalar := []byte(`{"status":true,"period":"daily","total":1422,"items":[
		{"rank":1,"title":"Black Clover","type":"manga","genres":["action"],"publicationStatus":"ongoing",
		 "thumbnail":"https://thumbnail.komiku.to/uploads/manga/black-clover-indonesia/manga_thumbnail-Manga-Black-Clover.jpg","views":51857}]}`)
	report, err := ExtractTrending(scalar, "daily")
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 1422 || len(report.Series) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	entry := report.Series[0]
	if entry.Views != 51857 {
		t.Fatalf("want scalar views, got %d", entry.Views)
	}
	if entry.Slug != "black-clover-indonesia" || entry.URL != "https://komiku.org/manga/black-clover-indonesia/" {
		t.Fatalf("want slug derived from cover, got %q / %q", entry.Slug, entry.URL)
	}
	if entry.Status != "Ongoing" || entry.Type != "Manga" {
		t.Fatalf("want title-cased metadata, got %q / %q", entry.Status, entry.Type)
	}
	if !strings.HasPrefix(entry.Cover, "https://thumbnail.komiku.org/") {
		t.Fatalf("want normalized cover, got %q", entry.Cover)
	}

	object := []byte(`{"status":true,"period":"active","items":[
		{"rank":2,"title":"One Piece","type":"manga","genres":["action"],"publicationStatus":"ongoing",
		 "thumbnail":"https://thumbnail.komiku.org/uploads/manga/komik-one-piece-indo/x.jpg",
		 "views":{"daily":20977,"weekly":425772,"monthly":1336290,"total":1564942}}]}`)
	weekly, err := ExtractTrending(object, "weekly")
	if err != nil {
		t.Fatal(err)
	}
	if weekly.Series[0].Views != 425772 {
		t.Fatalf("want weekly views from object, got %d", weekly.Series[0].Views)
	}
}

func TestExtractTrendingRejectsEmptyPayload(t *testing.T) {
	if _, err := ExtractTrending([]byte(`{"status":false,"items":[]}`), "daily"); err == nil {
		t.Fatal("want error for an empty payload")
	}
}

const feedFixture = `<div class="bge">
  <div class="bgei">
    <a href="https://komiku.org/manga/kamatte-shinsotsu-chan-ga-maikai-sasottekuru/">
      <img src="https://thumbnail.komiku.org/new/img/images/2026/09/14/x.webp?resize=450,235" class="sd rd">
      <div class="tpe1_inf"><b>Manga</b> Romantis</div>
      <span class="up">Up 5</span>
    </a>
  </div>
  <div class="kan">
    <a href="https://komiku.org/manga/kamatte-shinsotsu-chan-ga-maikai-sasottekuru/"><h3>
      Kamatte Shinsotsu-chan ga Maikai Sasottekuru
    </h3></a>
    <span class="judul2"> Romantis | 5 menit lalu  | Berwarna</span>
    <p>Sinopsis.</p>
    <div class="new1">
      <a href="/kamatte-shinsotsu-chan-ga-maikai-sasottekuru-chapter-01/"><span>Awal: </span><span>Chapter 01</span></a>
    </div>
    <div class="new1">
      <a href="/kamatte-shinsotsu-chan-ga-maikai-sasottekuru-chapter-9/"><span>Terbaru: </span><span>Chapter 9</span></a>
    </div>
  </div>
</div>
<div class="bge">
  <div class="bgei">
    <a href="https://komiku.org/manga/defending-the-villains-innocence/">
      <img src="https://thumbnail.komiku.org/new/img/images/y.jpg" class="sd rd">
      <div class="tpe1_inf"><b>Manhwa</b> Fantasi</div>
    </a>
  </div>
  <div class="kan">
    <a href="https://komiku.org/manga/defending-the-villains-innocence/"><h3>
      Defending the Villain’s Innocence
    </h3></a>
    <span class="judul2">Fantasi x • 2 jam • Berwarna</span>
    <div class="new1">
      <a href="/defending-the-villains-innocence-chapter-12/"><span>Terbaru: </span><span>Chapter 12</span></a>
    </div>
  </div>
</div>`

func TestExtractFeedReadsMetaAndLatestChapter(t *testing.T) {
	series, err := ExtractFeed([]byte(feedFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("want 2 entries, got %d", len(series))
	}
	first := series[0]
	if first.Title != "Kamatte Shinsotsu-chan ga Maikai Sasottekuru" {
		t.Fatalf("unexpected title %q", first.Title)
	}
	if first.Type != "Manga" {
		t.Fatalf("want Manga, got %q", first.Type)
	}
	if first.Updated != "5 menit lalu" {
		t.Fatalf("want update time, got %q", first.Updated)
	}
	if first.Chapter != "Chapter 9" {
		t.Fatalf("want latest chapter without prefix, got %q", first.Chapter)
	}
	if first.ChapterURL != "https://komiku.org/kamatte-shinsotsu-chan-ga-maikai-sasottekuru-chapter-9/" {
		t.Fatalf("unexpected chapter URL %q", first.ChapterURL)
	}
	if !first.Color {
		t.Fatal("want color flag")
	}

	second := series[1]
	if len(second.Genres) != 1 || second.Genres[0] != "Fantasi" {
		t.Fatalf("want a single genre, got %v", second.Genres)
	}
	if second.Updated != "2 jam" {
		t.Fatalf("want update time, got %q", second.Updated)
	}
	if second.Chapter != "Chapter 12" {
		t.Fatalf("want chapter 12, got %q", second.Chapter)
	}
}

func TestExtractFeedRejectsEmptyDocument(t *testing.T) {
	if _, err := ExtractFeed([]byte(`<html></html>`)); err == nil {
		t.Fatal("want error for an empty feed")
	}
}

const catalogFixture = `<html><body>
<section class="filter-tabs">
<a href="https://komiku.org/daftar-komik/" class="active"> Semua </a>
<a href="https://komiku.org/daftar-komik/?tipe=manga"> Manga </a>
</section>
<nav class="alphabet-nav">
<a href="https://komiku.org/daftar-komik/" class="active"> All </a>
<a href="https://komiku.org/daftar-komik/?huruf=%23"> # <span class="count">66</span> </a>
<a href="https://komiku.org/daftar-komik/?huruf=A" class=""> A <span class="count">501</span> </a>
<a href="https://komiku.org/daftar-komik/?huruf=B" class=""> B <span class="count">319</span> </a>
</nav>
<div class="manga-grid">
<article class="manga-card">
<a href="/manga/a-revenge-guide-for-the-terminally/">
<img class="lazy" src="https://komiku.org/asset/img/lazy.jpg" data-src="https://thumbnail.komiku.to/img/upload/x.jpg?resize=160,220" alt=" A Revenge Guide ">
</a>
<div>
<h4><a href="/manga/a-revenge-guide-for-the-terminally/"> A Revenge Guide for the Terminally </a></h4>
<p class="meta"> Manhwa • Drama<br> Status: Ongoing </p>
</div>
</article>
</div>
<div class="pagination">
<span class="current">1</span>
<a href="https://komiku.org/daftar-komik/?halaman=2">2</a>
</div>
<div class="page-info"> (7,893 komik) </div>
<div class="page-info"> Halaman 1 dari 158 </div>
</body></html>`

func TestExtractCatalogReadsCardsAlphabetAndPaging(t *testing.T) {
	report, err := ExtractCatalog([]byte(catalogFixture), "https://komiku.org/daftar-komik/")
	if err != nil {
		t.Fatal(err)
	}
	if report.Page != 1 || report.TotalPages != 158 {
		t.Fatalf("want page 1/158, got %d/%d", report.Page, report.TotalPages)
	}
	if report.Total != 7893 {
		t.Fatalf("want 7893 total, got %d", report.Total)
	}
	if len(report.Alphabet) != 3 {
		t.Fatalf("want 3 alphabet tiles, got %+v", report.Alphabet)
	}
	if report.Alphabet[1].Letter != "A" || report.Alphabet[1].Count != 501 {
		t.Fatalf("unexpected alphabet tile: %+v", report.Alphabet[1])
	}
	if len(report.Series) != 1 {
		t.Fatalf("want 1 card, got %d", len(report.Series))
	}
	card := report.Series[0]
	if card.Title != "A Revenge Guide for the Terminally" {
		t.Fatalf("unexpected title %q", card.Title)
	}
	if card.Type != "Manhwa" || card.Status != "Ongoing" {
		t.Fatalf("want Manhwa/Ongoing, got %q/%q", card.Type, card.Status)
	}
	if len(card.Genres) != 1 || card.Genres[0] != "Drama" {
		t.Fatalf("want genre Drama, got %v", card.Genres)
	}
	if !strings.HasPrefix(card.Cover, "https://thumbnail.komiku.org/") {
		t.Fatalf("want normalized cover, got %q", card.Cover)
	}
}

func TestExtractGenresReadsNameSlugAndCount(t *testing.T) {
	fixture := `<div class="gnr-sec" id="g-A"><div class="gnrw">
<a href="/genre/action/" class="gnr" data-nama="action" title="Komik genre Action"><b>Action</b> <i>4,153</i></a>
<a href="/genre/academy/" class="gnr" data-nama="academy" title="Komik genre Academy"><b>Academy</b> <i>2</i></a>
</div></div>`
	genres, err := ExtractGenres([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(genres) != 2 {
		t.Fatalf("want 2 genres, got %d", len(genres))
	}
	if genres[0].Name != "Action" || genres[0].Slug != "action" || genres[0].Count != 4153 {
		t.Fatalf("unexpected first genre: %+v", genres[0])
	}
	if genres[1].Count != 2 {
		t.Fatalf("unexpected second genre: %+v", genres[1])
	}
}

func TestNormalizeStatusFoldsFinishedSpellings(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"Ongoing":   "Ongoing",
		"ongoing":   "Ongoing",
		"End":       "Tamat",
		"Completed": "Tamat",
		"tamat":     "Tamat",
		"Hiatus":    "Hiatus",
	}
	for input, want := range cases {
		if got := normalizeStatus(input); got != want {
			t.Fatalf("normalizeStatus(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeThumbnailSwapsUnreachableHost(t *testing.T) {
	got := normalizeThumbnail("https://thumbnail.komiku.to/uploads/manga/x/y.jpg?resize=240,280")
	want := "https://thumbnail.komiku.org/uploads/manga/x/y.jpg?resize=240,280"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if normalizeThumbnail("") != "" {
		t.Fatal("empty input must stay empty")
	}
}

func TestSlugFromCover(t *testing.T) {
	cases := map[string]string{
		"https://thumbnail.komiku.org/uploads/manga/black-clover-indonesia/x.jpg": "black-clover-indonesia",
		"https://thumbnail.komiku.org/new/img/images/2026/09/14/x.webp":           "",
		"": "",
	}
	for input, want := range cases {
		if got := slugFromCover(input); got != want {
			t.Fatalf("slugFromCover(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestChapterLabelStripsFeedPrefixes(t *testing.T) {
	cases := map[string]string{
		"Terbaru: Chapter 9":       "Chapter 9",
		"Awal: Chapter 01":         "Chapter 01",
		"Chapter 12":               "Chapter 12",
		"  Terbaru:  Chapter 80  ": "Chapter 80",
	}
	for input, want := range cases {
		if got := chapterLabel(input); got != want {
			t.Fatalf("chapterLabel(%q) = %q, want %q", input, got, want)
		}
	}
}
