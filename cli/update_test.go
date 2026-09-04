package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func updateFixtureServer(t *testing.T, imageHits *int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manga/fixture/":
			fmt.Fprint(w, `<a href="/prefix-chapter-1/"></a><a href="/prefix-chapter-2/"></a><a href="/prefix-chapter-3/"></a>`)
		default:
			if strings.HasPrefix(r.URL.Path, "/prefix-chapter-") {
				fmt.Fprintf(w, `<img class="klazy" src="%s/page.jpg">`, server.URL)
				return
			}
			if r.URL.Path == "/page.jpg" {
				mu.Lock()
				*imageHits++
				mu.Unlock()
				_, _ = w.Write(append([]byte{0xff, 0xd8}, make([]byte, 11*1024)...))
				return
			}
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	return server
}

func TestSubscribeUnsubscribeAndList(t *testing.T) {
	root := t.TempDir()
	subsPath := filepath.Join(root, "subs.json")
	now := time.Unix(1000, 0)
	deps := Dependencies{ConfigPath: filepath.Join(root, "config.json"), Now: func() time.Time { return now }}.WithSubscriptionsPath(subsPath)

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"subscribe", "https://komiku.org/manga/fixture/"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("subscribe code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "subscribed: fixture") {
		t.Fatalf("subscribe output=%s", stdout.String())
	}
	file, _ := LoadSubscriptions(subsPath)
	if len(file.Subscriptions) != 1 || file.Subscriptions[0].Slug != "fixture" {
		t.Fatalf("persisted subs=%#v", file)
	}

	// Dedup: subscribing again is a no-op.
	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"subscribe", "https://komiku.org/manga/fixture/"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("dedup code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already subscribed") {
		t.Fatalf("dedup output=%s", stdout.String())
	}
	file, _ = LoadSubscriptions(subsPath)
	if len(file.Subscriptions) != 1 {
		t.Fatalf("dedup created duplicate: %#v", file)
	}

	// List.
	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"subs"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("subs code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fixture\thttps://komiku.org/manga/fixture/") {
		t.Fatalf("subs output=%s", stdout.String())
	}

	// List JSON.
	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"subs", "--json"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("subs json code=%d stderr=%s", code, stderr.String())
	}
	var listed SubscriptionsFile
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
		t.Fatalf("invalid JSON: %v %s", err, stdout.String())
	}
	if len(listed.Subscriptions) != 1 || listed.Subscriptions[0].Slug != "fixture" {
		t.Fatalf("json subs=%#v", listed)
	}

	// Unsubscribe by slug.
	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"unsubscribe", "fixture"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("unsubscribe code=%d stderr=%s", code, stderr.String())
	}
	file, _ = LoadSubscriptions(subsPath)
	if len(file.Subscriptions) != 0 {
		t.Fatalf("unsubscribe did not remove: %#v", file)
	}

	// Unsubscribe missing → error.
	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"unsubscribe", "fixture"}, &stdout, &stderr, deps)
	if code == 0 {
		t.Fatalf("unsubscribe missing should fail")
	}
}

func TestUpdateDryRunReportsNewChaptersWithoutDownloading(t *testing.T) {
	var imageHits int
	server := updateFixtureServer(t, &imageHits)
	defer server.Close()
	root := t.TempDir()
	subsPath := filepath.Join(root, "subs.json")
	configPath := filepath.Join(root, "config.json")
	deps := Dependencies{
		HTTP:       server.Client(),
		ConfigPath: configPath,
		Now:        func() time.Time { return time.Unix(2000, 0) },
	}.WithSubscriptionsPath(subsPath)

	// Seed subscription with a loopback URL (ValidateSeriesURL allows loopback).
	seriesURL := server.URL + "/manga/fixture/"
	file := SubscriptionsFile{Subscriptions: []Subscription{{SeriesURL: seriesURL, Slug: "fixture"}}}
	if err := SaveSubscriptions(subsPath, file); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"update", "--check"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("update --check code=%d stderr=%s", code, stderr.String())
	}
	if imageHits != 0 {
		t.Fatalf("dry run downloaded images: hits=%d", imageHits)
	}
	out := stdout.String()
	if !strings.Contains(out, "fixture  new=3 downloaded=0 skipped=0 failed=0") {
		t.Fatalf("dry run output=%s", out)
	}
	if !strings.Contains(out, "new chapters: 1, 2, 3") {
		t.Fatalf("dry run did not list new chapters: %s", out)
	}
}

func TestUpdateDownloadsNewChaptersAndSkipsDone(t *testing.T) {
	var imageHits int
	server := updateFixtureServer(t, &imageHits)
	defer server.Close()
	root := t.TempDir()
	subsPath := filepath.Join(root, "subs.json")
	configPath := filepath.Join(root, "config.json")
	seriesURL := server.URL + "/manga/fixture/"
	deps := Dependencies{
		HTTP:       server.Client(),
		ConfigPath: configPath,
		Now:        func() time.Time { return time.Unix(3000, 0) },
	}.WithSubscriptionsPath(subsPath)

	file := SubscriptionsFile{Subscriptions: []Subscription{{SeriesURL: seriesURL, Slug: "fixture"}}}
	if err := SaveSubscriptions(subsPath, file); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"update", "--out", root, "--delay", "0s"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("update code=%d stderr=%s", code, stderr.String())
	}
	if imageHits != 3 {
		t.Fatalf("expected 3 image fetches, got %d", imageHits)
	}

	// Second run: all chapters done, should fetch nothing.
	imageHits = 0
	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"update", "--out", root, "--delay", "0s", "--json"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("second update code=%d stderr=%s", code, stderr.String())
	}
	var report UpdateReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v %s", err, stdout.String())
	}
	if len(report.Series) != 1 || report.Series[0].Downloaded != 0 || report.Series[0].Skipped != 3 {
		t.Fatalf("second update report=%#v", report)
	}
	if imageHits != 0 {
		t.Fatalf("second run refetched: hits=%d", imageHits)
	}

	// last_check timestamp persisted.
	persisted, _ := LoadSubscriptions(subsPath)
	if persisted.Subscriptions[0].LastCheck.IsZero() {
		t.Fatalf("last_check not persisted: %#v", persisted)
	}
}

func TestUpdateNoSubscriptionsIsHelpful(t *testing.T) {
	root := t.TempDir()
	subsPath := filepath.Join(root, "subs.json")
	configPath := filepath.Join(root, "config.json")
	deps := Dependencies{ConfigPath: configPath}.WithSubscriptionsPath(subsPath)

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"update"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("update no-subs code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no subscriptions") {
		t.Fatalf("expected helpful message, got: %s", stdout.String())
	}
}

func TestSubsEmptyPrintsPlaceholder(t *testing.T) {
	root := t.TempDir()
	subsPath := filepath.Join(root, "subs.json")
	deps := Dependencies{}.WithSubscriptionsPath(subsPath)
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"subs"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("subs code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no subscriptions") {
		t.Fatalf("expected placeholder, got: %s", stdout.String())
	}
	_ = os.Stat
}
