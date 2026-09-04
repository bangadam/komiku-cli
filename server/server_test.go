package server

import (
	"archive/zip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

var validJPEG = append([]byte{0xff, 0xd8}, make([]byte, 11*1024)...)

func writeImage(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeCBZ(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	w := zip.NewWriter(out)
	for name, body := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func newTestServer(t *testing.T, root string) *Server {
	t.Helper()
	srv, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func getJSON(t *testing.T, srv *Server, path string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %s", path, rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("GET %s: invalid JSON %v: %s", path, err, rec.Body.String())
	}
	return m
}

func TestLibraryIndexListsSeriesWithChapters(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "002.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "frieren", "chapter-002"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "sakamoto-days", "chapter-164"), "001.jpg", validJPEG)

	srv := newTestServer(t, root)
	m := getJSON(t, srv, "/api/library")
	series, _ := m["series"].([]any)
	if len(series) != 2 {
		t.Fatalf("expected 2 series, got %v", series)
	}
	first := series[0].(map[string]any)
	if first["slug"] != "frieren" || first["title"] != "Frieren" {
		t.Fatalf("unexpected series: %#v", first)
	}
	if len(first["chapters"].([]any)) != 2 {
		t.Fatalf("expected 2 chapters in frieren: %#v", first)
	}
}

func TestSeriesDetailEnrichesChapterPageCounts(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "frieren", "vol-01", "chapter-001"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "frieren", "vol-01", "chapter-001"), "002.jpg", validJPEG)

	srv := newTestServer(t, root)
	m := getJSON(t, srv, "/api/series/frieren")
	chapters, _ := m["chapters"].([]any)
	if len(chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %v", chapters)
	}
	ch := chapters[0].(map[string]any)
	if ch["pages"].(float64) != 2 || ch["volume"].(float64) != 1 {
		t.Fatalf("chapter detail wrong: %#v", ch)
	}
}

func TestPagesListingForFolder(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "002.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "notes.txt", []byte("not an image"))

	srv := newTestServer(t, root)
	m := getJSON(t, srv, "/api/pages/frieren/chapter-001")
	pages, _ := m["pages"].([]any)
	if len(pages) != 2 || pages[0] != "001.jpg" {
		t.Fatalf("pages not sorted/filtered: %v", pages)
	}
}

func TestPageImageServedFromFolder(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "001.jpg", validJPEG)

	srv := newTestServer(t, root)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/page/frieren/chapter-001?p=001.jpg", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Body.Len() != len(validJPEG) {
		t.Fatalf("body length %d, want %d", rec.Body.Len(), len(validJPEG))
	}
	if rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("content-type %q", rec.Header().Get("Content-Type"))
	}
}

func TestCBZPagesListedAndServed(t *testing.T) {
	root := t.TempDir()
	writeCBZ(t, filepath.Join(root, "jjk", "Jujutsu Kaisen Volume 01.cbz"), map[string][]byte{
		"Chapter 001/001.jpg": validJPEG,
		"Chapter 001/002.jpg": validJPEG,
		"readme.txt":          []byte("ignored"),
	})

	srv := newTestServer(t, root)
	m := getJSON(t, srv, "/api/series/jjk")
	chapters, _ := m["chapters"].([]any)
	if len(chapters) != 1 {
		t.Fatalf("expected 1 cbz chapter, got %v", chapters)
	}
	ch := chapters[0].(map[string]any)
	if ch["source"] != "cbz" || ch["pages"].(float64) != 2 {
		t.Fatalf("cbz detail wrong: %#v", ch)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/page/jjk/Jujutsu%20Kaisen%20Volume%2001.cbz?p=Chapter%20001/001.jpg", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != len(validJPEG) {
		t.Fatalf("cbz body length %d, want %d", rec.Body.Len(), len(validJPEG))
	}
}

func TestReaderHTMLServed(t *testing.T) {
	root := t.TempDir()
	srv := newTestServer(t, root)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !contains(body, "komiku-cli library") || !contains(body, "/api/library") {
		t.Fatalf("reader HTML missing content: %q", body[:min(120, len(body))])
	}
}

func TestPathTraversalRejected(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "001.jpg", validJPEG)
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/page/%2e%2e/frieren/chapter-001?p=001.jpg", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("traversal should be 404, got %d", rec.Code)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// silence unused import if io is not referenced
var _ = io.Discard
