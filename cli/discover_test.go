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

	"github.com/bangadam/komiku-cli/kiryuu"
)

// catalogStatusFixture serves one ongoing and one completed Kiryuu card. The
// server ignores the status filter, so the CLI must filter the page itself.
func catalogStatusFixture() string {
	return `<html><body><div class="manga-grid">` +
		kiryuuCard("/manga/a-revenge-guide-for-the-terminally/", "A Revenge Guide for the Terminally", "Ongoing") +
		kiryuuCard("/manga/b-finished-series/", "B Finished Series", "Completed") +
		`</div></body></html>`
}

func TestCatalogStatusFiltersCurrentPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(catalogStatusFixture()))
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
		var report kiryuu.CatalogReport
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
