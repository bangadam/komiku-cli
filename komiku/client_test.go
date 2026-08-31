package komiku

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDetectImageAndExtension(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		format ImageFormat
		valid  bool
	}{
		{"jpeg", append([]byte{0xff, 0xd8}, make([]byte, 1798)...), JPEG, true},
		{"png", []byte("\x89PNG\r\n\x1a\nrest"), PNG, true},
		{"webp", []byte("RIFF1234WEBPrest"), WEBP, true},
		{"arbitrary riff", []byte("RIFF1234WAVErest"), "", false},
		{"html", []byte("<html>bad</html>"), "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			format, valid := DetectImage(test.data)
			if format != test.format || valid != test.valid {
				t.Fatalf("DetectImage = %q,%v, want %q,%v", format, valid, test.format, test.valid)
			}
		})
	}
	if got := ImageExtension("https://cdn/picture.webp?x=.jpg", JPEG); got != ".webp" {
		t.Fatalf("URL extension policy changed: %s", got)
	}
	if got := ImageExtension("https://cdn/no-extension", JPEG); got != ".jpg" {
		t.Fatalf("magic extension = %s", got)
	}
}

func TestEncodeImageURLPreservesEscapesUnicodeQueryAndFragment(t *testing.T) {
	got, err := EncodeImageURL("https://cdn.example/a%20b/日本 語.jpg?q=a%20b#frag")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://cdn.example/a%20b/%E6%97%A5%E6%9C%AC%20%E8%AA%9E.jpg?q=a%20b#frag"
	if got != want {
		t.Fatalf("encoded URL = %q, want %q", got, want)
	}
}

func TestRewriteLegacyURLIsNarrow(t *testing.T) {
	got, ok := RewriteLegacyURL("https://image12.komiku.to/a%20b.jpg?q=1")
	if !ok || got != "https://img.komiku.org/a%20b.jpg?q=1" {
		t.Fatalf("rewrite = %q,%v", got, ok)
	}
	for _, raw := range []string{"https://imagex.komiku.to/a.jpg", "http://image1.komiku.to/a.jpg", "https://other.example/a.jpg"} {
		if got, ok := RewriteLegacyURL(raw); ok || got != raw {
			t.Fatalf("unexpected rewrite %q to %q", raw, got)
		}
	}
}

type rewriteTransport struct {
	baseURL string
	mu      sync.Mutex
	paths   []string
	headers []http.Header
	status  map[string][]int
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	originalHost := req.URL.Host
	requestURL, _ := url.Parse(t.baseURL)
	req.URL.Scheme, req.URL.Host = requestURL.Scheme, requestURL.Host
	t.paths = append(t.paths, originalHost+req.URL.EscapedPath())
	t.headers = append(t.headers, req.Header.Clone())
	codes := t.status[originalHost]
	code := http.StatusNotFound
	if len(codes) > 0 {
		code = codes[0]
		t.status[originalHost] = codes[1:]
	}
	if code == http.StatusOK {
		return http.DefaultTransport.RoundTrip(req)
	}
	return &http.Response{StatusCode: code, Status: fmt.Sprintf("%d test", code), Header: make(http.Header), Body: io.NopCloser(strings.NewReader("bad")), Request: req}, nil
}

func TestDownloadImageRetryFallbackHeadersAndBackoff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append([]byte{0xff, 0xd8}, []byte("jpeg")...))
	}))
	defer server.Close()
	transport := &rewriteTransport{baseURL: server.URL, status: map[string][]int{
		"image1.komiku.to": {404},
		"img.komiku.org":   {200},
	}}
	client := NewClient(&http.Client{Transport: transport}, 0)
	var sleeps []time.Duration
	client.Sleep = func(_ context.Context, duration time.Duration) error { sleeps = append(sleeps, duration); return nil }
	var saved bytes.Buffer
	err := client.DownloadImage(context.Background(), "https://image1.komiku.to/a space.jpg?q=1", "https://komiku.org/chapter/", func(ext string, body io.Reader) error {
		if ext != ".jpg" {
			t.Fatalf("extension = %s", ext)
		}
		_, err := io.Copy(&saved, body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sleeps) != 0 {
		t.Fatalf("fallback success slept: %v", sleeps)
	}
	if got, want := transport.paths, []string{"image1.komiku.to/a%20space.jpg", "img.komiku.org/a%20space.jpg"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	for _, header := range transport.headers {
		if header.Get("Referer") != "https://komiku.org/chapter/" || header.Get("User-Agent") == "" {
			t.Fatalf("headers = %v", header)
		}
	}
}

func TestDownloadImageInitialPlusThreeRetriesNoFinalSleep(t *testing.T) {
	var attempts int
	client := NewClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader("<html>invalid</html>")), Header: make(http.Header), Request: req}, nil
	})}, 0)
	var sleeps []time.Duration
	client.Sleep = func(_ context.Context, duration time.Duration) error { sleeps = append(sleeps, duration); return nil }
	err := client.DownloadImage(context.Background(), "https://cdn.example/image", "https://komiku.org/chapter/", func(string, io.Reader) error { return nil })
	if err == nil {
		t.Fatal("expected final failure")
	}
	if attempts != 4 || !reflect.DeepEqual(sleeps, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Fatalf("attempts=%d sleeps=%v", attempts, sleeps)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestLimiterGlobalPacingWithFakeClock(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(0, 0)
	var sleeps []time.Duration
	limiter := NewLimiter(200*time.Millisecond, func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}, func(_ context.Context, duration time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, duration)
		now = now.Add(duration)
		mu.Unlock()
		return nil
	})
	for range 4 {
		if err := limiter.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if want := []time.Duration{200 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond}; !reflect.DeepEqual(sleeps, want) {
		t.Fatalf("limiter sleeps=%v, want %v", sleeps, want)
	}
}

func TestLimiterGloballyPacesConcurrentRetryAndLegacyFallback(t *testing.T) {
	const delay = 100 * time.Millisecond
	type sleepRequest struct {
		duration time.Duration
		release  chan struct{}
	}

	var clockMu sync.Mutex
	now := time.Unix(0, 0)
	nowFunc := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	limiterSleeps := make(chan sleepRequest)
	limiter := NewLimiter(delay, nowFunc, func(_ context.Context, duration time.Duration) error {
		request := sleepRequest{duration: duration, release: make(chan struct{})}
		limiterSleeps <- request
		<-request.release
		return nil
	})

	var transportMu sync.Mutex
	var starts []time.Time
	retryAttempts := 0
	startEvents := make(chan struct{}, 4)
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clockMu.Lock()
		started := now
		clockMu.Unlock()
		transportMu.Lock()
		starts = append(starts, started)
		status := http.StatusOK
		body := []byte{0xff, 0xd8, 1}
		switch req.URL.Host {
		case "image1.komiku.to":
			status = http.StatusNotFound
			body = []byte("missing")
		case "retry.example":
			retryAttempts++
			if retryAttempts == 1 {
				status = http.StatusServiceUnavailable
				body = []byte("retry")
			}
		}
		transportMu.Unlock()
		startEvents <- struct{}{}
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d test", status),
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    req,
		}, nil
	})
	client := NewClient(&http.Client{Transport: transport}, 0)
	client.Limiter = limiter
	var backoffsMu sync.Mutex
	var backoffs []time.Duration
	client.Sleep = func(_ context.Context, duration time.Duration) error {
		backoffsMu.Lock()
		backoffs = append(backoffs, duration)
		backoffsMu.Unlock()
		return nil
	}

	urls := []string{"https://image1.komiku.to/legacy.jpg", "https://retry.example/retry.jpg"}
	var workers sync.WaitGroup
	workers.Add(len(urls))
	errorsCh := make(chan error, len(urls))
	for _, imageURL := range urls {
		go func() {
			defer workers.Done()
			errorsCh <- client.DownloadImage(context.Background(), imageURL, "https://komiku.org/chapter/", func(_ string, body io.Reader) error {
				_, err := io.Copy(io.Discard, body)
				return err
			})
		}()
	}
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	<-startEvents
	startedCount := 1
	for startedCount < 4 {
		request := <-limiterSleeps
		clockMu.Lock()
		now = now.Add(request.duration)
		clockMu.Unlock()
		close(request.release)
		<-startEvents
		startedCount++
	}
	<-done
	transportMu.Lock()
	gotStarts := append([]time.Time(nil), starts...)
	gotRetryAttempts := retryAttempts
	transportMu.Unlock()
	if len(gotStarts) != 4 {
		t.Fatalf("request starts = %v, want four attempts", gotStarts)
	}
	for index := 1; index < len(gotStarts); index++ {
		if spacing := gotStarts[index].Sub(gotStarts[index-1]); spacing < delay {
			t.Fatalf("request %d burst: timestamps=%v spacing=%v, want >= %v", index+1, gotStarts, spacing, delay)
		}
	}
	if gotRetryAttempts != 2 {
		t.Fatalf("retry attempts = %d, want 2", gotRetryAttempts)
	}
	backoffsMu.Lock()
	gotBackoffs := append([]time.Duration(nil), backoffs...)
	backoffsMu.Unlock()
	if !reflect.DeepEqual(gotBackoffs, []time.Duration{time.Second}) {
		t.Fatalf("retry backoffs = %v, want [1s]", gotBackoffs)
	}
}
