package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bangadam/komiku-cli/komiku"
)

const catalogStatusFixture = `<html><body>
<section class="filter-tabs">
<a href="https://komiku.org/daftar-komik/" class="active"> Semua </a>
<a href="https://komiku.org/daftar-komik/?tipe=manga"> Manga </a>
</section>
<nav class="alphabet-nav">
<a href="https://komiku.org/daftar-komik/" class="active"> All </a>
<a href="https://komiku.org/daftar-komik/?huruf=%23"> # <span class="count">66</span> </a>
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
<article class="manga-card">
<a href="/manga/b-finished-series/">
<img class="lazy" src="https://komiku.org/asset/img/lazy.jpg" data-src="https://thumbnail.komiku.to/img/upload/y.jpg?resize=160,220" alt=" B Finished ">
</a>
<div>
<h4><a href="/manga/b-finished-series/"> B Finished Series </a></h4>
<p class="meta"> Manga • Action<br> Status: Tamat </p>
</div>
</article>
</div>
<div class="pagination">
<span class="current">1</span>
<a href="https://komiku.org/daftar-komik/?halaman=2">2</a>
</div>
<div class="page-info"> (7,894 komik) </div>
<div class="page-info"> Halaman 1 dari 158 </div>
</body></html>`

func TestCatalogStatusFiltersCurrentPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(catalogStatusFixture))
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	dependencies := Dependencies{
		HTTP:       server.Client(),
		BaseURL:    server.URL,
		ConfigPath: filepath.Join(root, "missing-config.json"),
	}

	for _, test := range []struct {
		status    string
		wantTitle string
		wantState string
	}{
		{"completed", "B Finished Series", "Completed"},
		{"ongoing", "A Revenge Guide for the Terminally", "Ongoing"},
	} {
		var stdout, stderr bytes.Buffer
		code := Main(context.Background(), []string{"catalog", "--json", "--status", test.status}, &stdout, &stderr, dependencies)
		if code != 0 {
			t.Fatalf("status=%s code=%d stderr=%s", test.status, code, stderr.String())
		}
		var report komiku.CatalogReport
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("status=%s decode: %v", test.status, err)
		}
		if len(report.Series) != 1 {
			t.Fatalf("status=%s want 1 series, got %d", test.status, len(report.Series))
		}
		if report.Series[0].Title != test.wantTitle || report.Series[0].Status != test.wantState {
			t.Fatalf("status=%s got %q/%q, want %q/%q", test.status, report.Series[0].Title, report.Series[0].Status, test.wantTitle, test.wantState)
		}
		if report.Total != 1 {
			t.Fatalf("status=%s total=%d, want the filtered count 1", test.status, report.Total)
		}
		if report.Status != test.status {
			t.Fatalf("status=%s echoed %q", test.status, report.Status)
		}
	}
}

func TestCatalogRejectsUnknownStatusBeforeFetching(t *testing.T) {
	dependencies := Dependencies{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Error("unknown status must fail before any HTTP request")
			return nil, nil
		})},
		ConfigPath: filepath.Join(t.TempDir(), "missing-config.json"),
	}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"catalog", "--status", "bogus"}, &stdout, &stderr, dependencies)
	if code == 0 || !strings.Contains(stderr.String(), "unknown status") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
