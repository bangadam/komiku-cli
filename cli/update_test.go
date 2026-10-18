package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bangadam/komiku-cli/store"
)

func updateFixtureServer(t *testing.T, imageHits *atomic.Int32) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manga/fixture/":
			fmt.Fprint(w, `<a href="/prefix-chapter-1/"></a><a href="/prefix-chapter-2/"></a><a href="/prefix-chapter-3/"></a>`)
		case "/prefix-chapter-1/", "/prefix-chapter-2/", "/prefix-chapter-3/":
			fmt.Fprintf(w, `<img class="klazy" src="%s/page.jpg">`, server.URL)
		case "/page.jpg":
			imageHits.Add(1)
			_, _ = w.Write(append([]byte{0xff, 0xd8}, make([]byte, 11*1024)...))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return server
}

// isolatedCLIConfig keeps legacy-file checks away from the user's config data.
func isolatedCLIConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	path, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

type updateFileSnapshot struct {
	Mode    fs.FileMode
	ModTime time.Time
	Content string
}

func snapshotUpdateFiles(t *testing.T, root string) map[string]updateFileSnapshot {
	t.Helper()
	files := make(map[string]updateFileSnapshot)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		snapshot := updateFileSnapshot{Mode: info.Mode(), ModTime: info.ModTime()}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot.Content = string(data)
		}
		files[path] = snapshot
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestUpdateDryRunReportsMissingChaptersWithoutMutation(t *testing.T) {
	var imageHits atomic.Int32
	server := updateFixtureServer(t, &imageHits)
	defer server.Close()
	root := t.TempDir()
	configPath := isolatedCLIConfig(t)
	if err := SaveFileConfig(configPath, FileConfig{OutputRoot: root}); err != nil {
		t.Fatal(err)
	}
	// The local name differs from the URL slug: the manifest must win.
	dir := filepath.Join(root, "renamed-fixture")
	writeImage(t, filepath.Join(dir, "vol-01", "chapter-001"), "001.jpg", validJPEG)
	if err := storeWrite(t, filepath.Join(dir, ".state.json"), `{"done":[1]}`); err != nil {
		t.Fatal(err)
	}
	seriesURL := server.URL + "/manga/fixture/"
	writeSeriesManifestFixture(t, dir, seriesURL)
	deps := Dependencies{HTTP: server.Client(), BaseURL: server.URL, ConfigPath: configPath}

	for _, asJSON := range []bool{false, true} {
		before := snapshotUpdateFiles(t, root)
		configBefore := snapshotUpdateFiles(t, filepath.Dir(configPath))
		args := []string{"update", "--check"}
		if asJSON {
			args = append(args, "--json")
		}
		var stdout, stderr bytes.Buffer
		if code := Main(context.Background(), args, &stdout, &stderr, deps); code != 0 {
			t.Fatalf("update --check code=%d stderr=%s", code, stderr.String())
		}
		if asJSON {
			var report UpdateReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("invalid JSON: %v %s", err, stdout.String())
			}
			if len(report.Series) != 1 {
				t.Fatalf("report=%+v", report)
			}
			entry := report.Series[0]
			if entry.Slug != "renamed-fixture" || entry.SeriesURL != seriesURL || entry.Downloaded != 0 || entry.Skipped != 1 || entry.Failed != 0 || entry.Error != "" || !reflect.DeepEqual(entry.NewChapters, []string{"2", "3"}) {
				t.Fatalf("report=%+v", report)
			}
		} else if !strings.Contains(stdout.String(), "renamed-fixture  new=2 downloaded=0 skipped=1 failed=0") || !strings.Contains(stdout.String(), "new chapters: 2, 3") {
			t.Fatalf("dry run output=%s", stdout.String())
		}
		if imageHits.Load() != 0 {
			t.Fatalf("dry run downloaded images: hits=%d", imageHits.Load())
		}
		if !reflect.DeepEqual(before, snapshotUpdateFiles(t, root)) || !reflect.DeepEqual(configBefore, snapshotUpdateFiles(t, filepath.Dir(configPath))) {
			t.Fatal("dry run mutated library or config files")
		}
	}
}

func TestUpdateDownloadsMissingChaptersAndSkipsDone(t *testing.T) {
	var imageHits atomic.Int32
	server := updateFixtureServer(t, &imageHits)
	defer server.Close()
	root := t.TempDir()
	configPath := isolatedCLIConfig(t)
	if err := SaveFileConfig(configPath, FileConfig{OutputRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{
		HTTP:       server.Client(),
		BaseURL:    server.URL,
		ConfigPath: configPath,
		Now:        func() time.Time { return time.Unix(3000, 0) },
	}
	writeImage(t, filepath.Join(root, "fixture", "chapter-001"), "001.jpg", validJPEG)
	if err := storeWrite(t, filepath.Join(root, "fixture", ".state.json"), `{"done":[1]}`); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	args := []string{"update", "--out", root, "--delay", "0s", "--json"}
	if code := Main(context.Background(), args, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("update code=%d stderr=%s", code, stderr.String())
	}
	var report UpdateReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v %s", err, stdout.String())
	}
	if len(report.Series) != 1 || report.Series[0].SeriesURL != server.URL+"/manga/fixture/" || report.Series[0].Downloaded != 2 || report.Series[0].Skipped != 1 || report.Series[0].Failed != 0 || report.Series[0].Error != "" || !reflect.DeepEqual(report.Series[0].NewChapters, []string{"2", "3"}) {
		t.Fatalf("update report=%+v", report)
	}
	if imageHits.Load() != 2 {
		t.Fatalf("expected 2 image fetches, got %d", imageHits.Load())
	}
	done, err := store.ReadDone(root, "fixture")
	if err != nil || !reflect.DeepEqual(done, []float64{1, 2, 3}) {
		t.Fatalf("done=%v err=%v", done, err)
	}
	for _, chapter := range []string{"001", "002", "003"} {
		if !store.ValidResumeFile(filepath.Join(root, "fixture", "chapter-"+chapter, "001.jpg")) {
			t.Fatalf("chapter %s is missing its downloaded page", chapter)
		}
	}
	original, err := os.ReadFile(filepath.Join(root, "fixture", "chapter-001", "001.jpg"))
	if err != nil || !bytes.Equal(original, validJPEG) {
		t.Fatalf("existing chapter changed: %v", err)
	}

	// A second run neither refetches completed pages nor rewrites local state.
	before := snapshotUpdateFiles(t, root)
	stdout.Reset()
	stderr.Reset()
	if code := Main(context.Background(), args, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("second update code=%d stderr=%s", code, stderr.String())
	}
	report = UpdateReport{}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v %s", err, stdout.String())
	}
	if len(report.Series) != 1 || report.Series[0].Downloaded != 0 || report.Series[0].Skipped != 3 || len(report.Series[0].NewChapters) != 0 || report.Series[0].Error != "" {
		t.Fatalf("second update report=%+v", report)
	}
	if imageHits.Load() != 2 || !reflect.DeepEqual(before, snapshotUpdateFiles(t, root)) {
		t.Fatal("second update fetched pages or changed library files")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(configPath), "subscriptions.json")); !os.IsNotExist(err) {
		t.Fatalf("update created a legacy subscription file: %v", err)
	}
}

func TestUpdateEmptyLibraryIsReadOnly(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprintf("exists=%t", exists), func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "library")
			if exists {
				if err := os.Mkdir(output, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			deps := Dependencies{ConfigPath: isolatedCLIConfig(t), HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("empty library made an HTTP request")
				return nil, fmt.Errorf("unexpected HTTP request")
			})}}
			before := snapshotUpdateFiles(t, root)
			for _, extra := range [][]string{nil, {"--json"}, {"--check"}, {"--check", "--json"}} {
				args := append([]string{"update", "--out", output}, extra...)
				var stdout, stderr bytes.Buffer
				if code := Main(context.Background(), args, &stdout, &stderr, deps); code != 0 {
					t.Fatalf("update code=%d stderr=%s", code, stderr.String())
				}
				if strings.Contains(strings.Join(extra, " "), "--json") {
					if strings.TrimSpace(stdout.String()) != `{"series":null}` {
						t.Fatalf("empty JSON contract changed: %s", stdout.String())
					}
				} else if !strings.Contains(stdout.String(), "no series downloaded yet") {
					t.Fatalf("expected helpful message, got: %s", stdout.String())
				}
				if !reflect.DeepEqual(before, snapshotUpdateFiles(t, root)) {
					t.Fatal("empty update mutated the library")
				}
				if _, err := os.Stat(filepath.Dir(deps.ConfigPath)); !os.IsNotExist(err) {
					t.Fatalf("update created a config directory: %v", err)
				}
			}
		})
	}
}

func TestUpdateIgnoresLegacySubscriptionData(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed=%t", malformed), func(t *testing.T) {
			var imageHits atomic.Int32
			server := updateFixtureServer(t, &imageHits)
			defer server.Close()
			root := t.TempDir()
			configPath := isolatedCLIConfig(t)
			if err := SaveFileConfig(configPath, FileConfig{OutputRoot: root}); err != nil {
				t.Fatal(err)
			}
			legacy := fmt.Sprintf(`{"subscriptions":[{"slug":"remote-only","series_url":%q},{"slug":"fixture","series_url":%q}]}`, server.URL+"/manga/remote-only/", server.URL+"/manga/wrong-fixture/")
			if malformed {
				legacy = "not JSON"
			}
			if err := storeWrite(t, filepath.Join(filepath.Dir(configPath), "subscriptions.json"), legacy); err != nil {
				t.Fatal(err)
			}
			if err := storeWrite(t, filepath.Join(root, "fixture", ".state.json"), `{"done":[1,2,3]}`); err != nil {
				t.Fatal(err)
			}
			// Default config resolution must not consult the adjacent legacy file.
			deps := Dependencies{HTTP: server.Client(), BaseURL: server.URL}
			before := snapshotUpdateFiles(t, root)
			configBefore := snapshotUpdateFiles(t, filepath.Dir(configPath))
			for _, check := range []bool{true, false} {
				args := []string{"update", "--json"}
				if check {
					args = append(args, "--check")
				}
				var stdout, stderr bytes.Buffer
				if code := Main(context.Background(), args, &stdout, &stderr, deps); code != 0 {
					t.Fatalf("update code=%d stderr=%s", code, stderr.String())
				}
				var report UpdateReport
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
					t.Fatal(err)
				}
				if len(report.Series) != 1 || report.Series[0].Slug != "fixture" || report.Series[0].SeriesURL != server.URL+"/manga/fixture/" || report.Series[0].Skipped != 3 || report.Series[0].Error != "" {
					t.Fatalf("legacy data affected update: %+v", report)
				}
				if !reflect.DeepEqual(before, snapshotUpdateFiles(t, root)) || !reflect.DeepEqual(configBefore, snapshotUpdateFiles(t, filepath.Dir(configPath))) {
					t.Fatal("update changed existing library or legacy config data")
				}
			}
			if imageHits.Load() != 0 {
				t.Fatalf("unexpected image fetches: %d", imageHits.Load())
			}
		})
	}
}

func TestUpdateSkipsDirectoriesWithoutDownloadedState(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"unrelated", "empty-chapter/chapter-001", "empty-volume/vol-01", "state-directory/.state.json", "archive-directory/empty.cbz"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		"cover-only/cover.jpg":                  "cover",
		"cache-only/.volumes.json":              `{"volumes":[]}`,
		"empty-state/.state.json":               `{"done":[]}`,
		"invalid-state/.state.json":             "not JSON",
		"empty-archive/empty.cbz":               "",
		"unrelated-pages/chapter-001/notes.txt": "not a page",
		"unrelated-pages/chapter-001/cover.jpg": "not a numbered page",
		"empty-chapter/chapter-001/001.jpg":     "",
		"invalid-manifest/.pack.json":           "not JSON",
	} {
		if err := storeWrite(t, filepath.Join(root, path), content); err != nil {
			t.Fatal(err)
		}
	}
	writeSeriesManifestFixture(t, filepath.Join(root, "metadata-only"), "https://komiku.org/manga/metadata-only/")
	deps := Dependencies{ConfigPath: isolatedCLIConfig(t), HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("metadata-only directory made an HTTP request")
		return nil, fmt.Errorf("unexpected HTTP request")
	})}}
	before := snapshotUpdateFiles(t, root)
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), []string{"update", "--out", root, "--check", "--json"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("update code=%d stderr=%s", code, stderr.String())
	}
	var report UpdateReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || len(report.Series) != 0 {
		t.Fatalf("unexpected report=%+v err=%v", report, err)
	}
	if !reflect.DeepEqual(before, snapshotUpdateFiles(t, root)) {
		t.Fatal("checking metadata-only directories mutated files")
	}
}

func TestUpdateCheckWithoutStateDoesNotCreateIt(t *testing.T) {
	var imageHits atomic.Int32
	server := updateFixtureServer(t, &imageHits)
	defer server.Close()
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "fixture", "chapter-001"), "001.jpg", validJPEG)
	before := snapshotUpdateFiles(t, root)
	deps := Dependencies{HTTP: server.Client(), BaseURL: server.URL, ConfigPath: isolatedCLIConfig(t)}
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), []string{"update", "--out", root, "--check", "--json"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("update code=%d stderr=%s", code, stderr.String())
	}
	var report UpdateReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Series) != 1 || report.Series[0].Error != "" || len(report.Series[0].NewChapters) != 3 {
		t.Fatalf("report=%+v", report)
	}
	if imageHits.Load() != 0 || !reflect.DeepEqual(before, snapshotUpdateFiles(t, root)) {
		t.Fatal("check downloaded or wrote files without state")
	}
}

func TestUpdateReportsInvalidStateWithoutOverwritingIt(t *testing.T) {
	var imageHits atomic.Int32
	server := updateFixtureServer(t, &imageHits)
	defer server.Close()
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "broken", "chapter-001"), "001.jpg", validJPEG)
	if err := storeWrite(t, filepath.Join(root, "broken", ".state.json"), "not JSON"); err != nil {
		t.Fatal(err)
	}
	if err := storeWrite(t, filepath.Join(root, "fixture", ".state.json"), `{"done":[1,2,3]}`); err != nil {
		t.Fatal(err)
	}
	before := snapshotUpdateFiles(t, root)
	deps := Dependencies{HTTP: server.Client(), BaseURL: server.URL, ConfigPath: isolatedCLIConfig(t)}
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), []string{"update", "--out", root, "--json"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("update code=%d stderr=%s", code, stderr.String())
	}
	var report UpdateReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Series) != 2 || report.Series[0].Slug != "broken" || !strings.Contains(report.Series[0].Error, "decode state") || report.Series[1].Slug != "fixture" || report.Series[1].Skipped != 3 || report.Series[1].Error != "" {
		t.Fatalf("report=%+v", report)
	}
	if !reflect.DeepEqual(before, snapshotUpdateFiles(t, root)) {
		t.Fatal("update changed files after a state error")
	}
}
