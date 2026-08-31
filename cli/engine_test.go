package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
)

func TestEngineEventsAreOrderedAndFinishAfterAuditClose(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chapter-1/", "/chapter-2/":
			fmt.Fprintf(w, `<img class="klazy" src="%s%s.jpg">`, server.URL, r.URL.Path)
		case "/chapter-1/.jpg", "/chapter-2/.jpg":
			_, _ = w.Write(append([]byte{0xff, 0xd8}, []byte("image")...))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	seriesStore, err := store.Open(t.TempDir(), "series")
	if err != nil {
		t.Fatal(err)
	}
	client := komiku.NewClient(server.Client(), 0)
	jobs := []Job{
		{Chapter: komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: server.URL + "/chapter-1/"}, Flat: true},
		{Chapter: komiku.Chapter{RawID: "2", Display: "2", Number: 2, URL: server.URL + "/chapter-2/"}, Flat: true},
	}
	batch := (&Engine{Client: client, Store: seriesStore, Workers: 2}).Start(context.Background(), jobs)
	var events []Event
	for event := range batch.Events() {
		events = append(events, event)
	}
	summary := batch.Wait()
	if summary.Err != nil || len(summary.Results) != 2 {
		t.Fatalf("summary=%+v", summary)
	}
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event %d sequence=%d", index, event.Sequence)
		}
	}
	if len(events) == 0 || events[len(events)-1].Kind != BatchFinished {
		t.Fatalf("last event=%+v", events)
	}
	data, err := os.ReadFile(summary.AuditPath)
	if err != nil || len(data) == 0 {
		t.Fatalf("audit was not closed before finish: bytes=%d err=%v", len(data), err)
	}
}

func TestEnginePauseStopsNewRequestsAndResumeKeepsPacing(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	var imageHits []time.Time
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chapter/":
			fmt.Fprintf(w, `<img class="klazy" src="%s/1.jpg"><img class="klazy" src="%s/2.jpg"><img class="klazy" src="%s/3.jpg">`, server.URL, server.URL, server.URL)
		case "/1.jpg", "/2.jpg", "/3.jpg":
			mu.Lock()
			imageHits = append(imageHits, time.Now())
			mu.Unlock()
			if r.URL.Path == "/1.jpg" {
				select {
				case <-firstStarted:
				default:
					close(firstStarted)
				}
				<-releaseFirst
			}
			_, _ = w.Write(append([]byte{0xff, 0xd8}, []byte("image")...))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	seriesStore, _ := store.Open(t.TempDir(), "series")
	client := komiku.NewClient(server.Client(), 40*time.Millisecond)
	job := Job{Chapter: komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: server.URL + "/chapter/"}, Flat: true}
	batch := (&Engine{Client: client, Store: seriesStore, Workers: 1}).Start(context.Background(), []Job{job})
	drained := make(chan struct{})
	go func() {
		for range batch.Events() {
		}
		close(drained)
	}()
	<-firstStarted
	batch.Pause()
	close(releaseFirst)
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	hitsWhilePaused := len(imageHits)
	mu.Unlock()
	if hitsWhilePaused != 1 {
		t.Fatalf("new requests started while paused: %d", hitsWhilePaused)
	}
	batch.Resume()
	<-drained
	summary := batch.Wait()
	if summary.Counts[Done] != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(imageHits) != 3 {
		t.Fatalf("image hits=%d", len(imageHits))
	}
	if gap := imageHits[2].Sub(imageHits[1]); gap < 30*time.Millisecond {
		t.Fatalf("resume burst: second gap=%v", gap)
	}
}

func TestEngineCancelKeepsDoneAndRecordsActivePart(t *testing.T) {
	activeStarted := make(chan struct{})
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chapter-1/":
			fmt.Fprintf(w, `<img class="klazy" src="%s/done.jpg">`, server.URL)
		case "/chapter-2/":
			fmt.Fprintf(w, `<img class="klazy" src="%s/active.jpg">`, server.URL)
		case "/done.jpg":
			_, _ = w.Write(append([]byte{0xff, 0xd8}, []byte("done")...))
		case "/active.jpg":
			close(activeStarted)
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	seriesStore, _ := store.Open(root, "series")
	client := komiku.NewClient(server.Client(), 0)
	jobs := []Job{
		{Chapter: komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: server.URL + "/chapter-1/"}, Flat: true},
		{Chapter: komiku.Chapter{RawID: "2", Display: "2", Number: 2, URL: server.URL + "/chapter-2/"}, Flat: true},
	}
	batch := (&Engine{Client: client, Store: seriesStore, Workers: 2}).Start(context.Background(), jobs)
	doneFinished := false
	cancelled := false
	for event := range batch.Events() {
		if event.Kind == ChapterFinished && event.Chapter.RawID == "1" && event.Result.Status == Done {
			doneFinished = true
		}
		if doneFinished && !cancelled {
			<-activeStarted
			batch.Cancel()
			cancelled = true
		}
	}
	summary := batch.Wait()
	if !summary.Cancelled || len(summary.Results) != 2 || summary.Results[0].Status != Done || summary.Results[1].Status != Part {
		t.Fatalf("summary=%+v", summary)
	}
	stateData, err := os.ReadFile(filepath.Join(root, "series", ".state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state store.State
	if err := json.Unmarshal(stateData, &state); err != nil {
		t.Fatalf("state is not atomic JSON: %v", err)
	}
	if len(state.Done) != 1 || state.Done[0] != 1 {
		t.Fatalf("state=%+v", state)
	}
	if _, err := os.Stat(summary.AuditPath); err != nil {
		t.Fatalf("audit missing: %v", err)
	}
}

func TestBatchControlAndCompletionDoNotDependOnImmediateEventConsumption(t *testing.T) {
	var imageHits atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chapter/":
			for page := 1; page <= 80; page++ {
				fmt.Fprintf(w, `<img class="klazy" src="%s/%03d.jpg">`, server.URL, page)
			}
		default:
			imageHits.Add(1)
			_, _ = w.Write(append([]byte{0xff, 0xd8}, []byte("image")...))
		}
	}))
	defer server.Close()
	seriesStore, _ := store.Open(t.TempDir(), "series")
	client := komiku.NewClient(server.Client(), 0)
	job := Job{Chapter: komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: server.URL + "/chapter/"}, Flat: true}
	batch := (&Engine{Client: client, Store: seriesStore, Workers: 1}).Start(context.Background(), []Job{job})

	deadline := time.Now().Add(2 * time.Second)
	for imageHits.Load() < 63 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if imageHits.Load() < 63 {
		go func() {
			for range batch.Events() {
			}
		}()
		t.Fatalf("fixture did not create event pressure: hits=%d", imageHits.Load())
	}
	time.Sleep(20 * time.Millisecond)
	controlDone := make(chan struct{})
	go func() {
		batch.Pause()
		batch.Resume()
		close(controlDone)
	}()
	select {
	case <-controlDone:
	case <-time.After(time.Second):
		go func() {
			for range batch.Events() {
			}
		}()
		<-controlDone
		t.Fatal("control method blocked behind a delayed event consumer")
	}

	waitDone := make(chan Summary, 1)
	go func() { waitDone <- batch.Wait() }()
	var summary Summary
	select {
	case summary = <-waitDone:
	case <-time.After(3 * time.Second):
		go func() {
			for range batch.Events() {
			}
		}()
		summary = <-waitDone
		t.Fatalf("batch completion depended on event consumption: %+v", summary)
	}
	var events []Event
	for event := range batch.Events() {
		events = append(events, event)
	}
	if summary.Counts[Done] != 1 || len(events) <= cap(batch.events) || events[len(events)-1].Kind != BatchFinished {
		t.Fatalf("summary=%+v events=%d last=%+v", summary, len(events), events[len(events)-1])
	}
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event %d sequence=%d", index, event.Sequence)
		}
	}
}
