package kiryuu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	if got := ImageExtension("https://cdn.kiryuu.io/kuma/series/chapter-1/1-aa.webp?x=.jpg", JPEG); got != ".webp" {
		t.Fatalf("URL extension policy changed: %s", got)
	}
	if got := ImageExtension("https://cdn.kiryuu.io/kuma/series/chapter-1/1-aa", JPEG); got != ".jpg" {
		t.Fatalf("magic extension = %s", got)
	}
}

type contentTransport func(*http.Request) (*http.Response, error)

func (f contentTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func contentResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d test", status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestFetchHTMLRetriesTransientTransportErrors(t *testing.T) {
	var attempts int
	client := NewClient(&http.Client{Transport: contentTransport(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("connection reset by peer")
		}
		return contentResponse(req, http.StatusOK, "<html>ok</html>"), nil
	})}, 0)
	var sleeps []time.Duration
	client.Sleep = func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		return nil
	}

	data, err := client.fetchHTML(context.Background(), "https://v7.kiryuu.to/")
	if err != nil {
		t.Fatalf("fetchHTML: %v", err)
	}
	if string(data) != "<html>ok</html>" {
		t.Fatalf("body = %q", data)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if want := []time.Duration{500 * time.Millisecond, time.Second}; !reflect.DeepEqual(sleeps, want) {
		t.Fatalf("retry backoffs = %v, want %v", sleeps, want)
	}
}

func TestFetchHTMLExhaustsTransportRetries(t *testing.T) {
	var attempts int
	client := NewClient(&http.Client{Transport: contentTransport(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("connection reset by peer")
	})}, 0)
	var sleeps []time.Duration
	client.Sleep = func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		return nil
	}

	_, err := client.fetchHTML(context.Background(), "https://v7.kiryuu.to/")
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("err = %v, want the transport error", err)
	}
	if attempts != fetchHTMLRetries+1 {
		t.Fatalf("attempts = %d, want %d", attempts, fetchHTMLRetries+1)
	}
	if want := []time.Duration{500 * time.Millisecond, time.Second}; !reflect.DeepEqual(sleeps, want) {
		t.Fatalf("retry backoffs = %v, want %v", sleeps, want)
	}
}

type contentTimeoutError struct{}

func (contentTimeoutError) Error() string   { return "i/o timeout" }
func (contentTimeoutError) Timeout() bool   { return true }
func (contentTimeoutError) Temporary() bool { return true }

func TestFetchHTMLDoesNotRetryDefiniteFailures(t *testing.T) {
	tests := []struct {
		name         string
		response     func(*http.Request) (*http.Response, error)
		wantAttempts int
		wantErr      string
	}{
		{
			name: "http status is not transient",
			response: func(req *http.Request) (*http.Response, error) {
				return contentResponse(req, http.StatusBadGateway, "bad gateway"), nil
			},
			wantAttempts: 1,
			wantErr:      "502",
		},
		{
			name: "timeout fails fast",
			response: func(*http.Request) (*http.Response, error) {
				return nil, contentTimeoutError{}
			},
			wantAttempts: 1,
			wantErr:      "i/o timeout",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attempts int
			client := NewClient(&http.Client{Transport: contentTransport(func(req *http.Request) (*http.Response, error) {
				attempts++
				return test.response(req)
			})}, 0)
			var sleeps []time.Duration
			client.Sleep = func(_ context.Context, duration time.Duration) error {
				sleeps = append(sleeps, duration)
				return nil
			}

			_, err := client.fetchHTML(context.Background(), "https://v7.kiryuu.to/")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want %q", err, test.wantErr)
			}
			if attempts != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts, test.wantAttempts)
			}
			if len(sleeps) != 0 {
				t.Fatalf("unexpected retry backoffs: %v", sleeps)
			}
		})
	}
}

func TestDownloadImageSendsNoRefererAndValidatesJPEG(t *testing.T) {
	payload := append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("jpeg-body")...)
	var referer, userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer = r.Header.Get("Referer")
		userAgent = r.Header.Get("User-Agent")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client := NewClient(server.Client(), 0)
	var gotExtension string
	var got bytes.Buffer
	err := client.DownloadImage(context.Background(), server.URL+"/page.jpg", func(extension string, body io.Reader) error {
		gotExtension = extension
		_, err := io.Copy(&got, body)
		return err
	})
	if err != nil {
		t.Fatalf("DownloadImage: %v", err)
	}
	if referer != "" {
		t.Fatalf("referer = %q, want no referer header", referer)
	}
	if userAgent != DefaultUserAgent {
		t.Fatalf("user agent = %q, want %q", userAgent, DefaultUserAgent)
	}
	if gotExtension != ".jpg" {
		t.Fatalf("extension = %q, want .jpg", gotExtension)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("saved body = %q, want %q", got.Bytes(), payload)
	}
}

func TestDownloadImageRetriesInvalidPayloadThenFails(t *testing.T) {
	var attempts, saves int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, _ = io.WriteString(w, "<html>not an image</html>")
	}))
	defer server.Close()

	client := NewClient(server.Client(), 0)
	var sleeps []time.Duration
	client.Sleep = func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		return nil
	}
	err := client.DownloadImage(context.Background(), server.URL+"/page", func(string, io.Reader) error {
		saves++
		return nil
	})
	if err == nil {
		t.Fatal("expected the invalid payload to fail")
	}
	if attempts != 4 || saves != 0 {
		t.Fatalf("attempts=%d saves=%d, want 4/0", attempts, saves)
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}; !reflect.DeepEqual(sleeps, want) {
		t.Fatalf("backoffs = %v, want %v", sleeps, want)
	}
}

func TestDownloadImageDoesNotRetrySaveFailures(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, _ = w.Write([]byte{0xff, 0xd8, 0x00})
	}))
	defer server.Close()

	client := NewClient(server.Client(), 0)
	var sleeps []time.Duration
	client.Sleep = func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		return nil
	}
	saveErr := errors.New("disk full")
	err := client.DownloadImage(context.Background(), server.URL+"/page.jpg", func(string, io.Reader) error {
		return saveErr
	})
	if !errors.Is(err, errSaveImage) || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want the save failure", err)
	}
	if attempts != 1 || len(sleeps) != 0 {
		t.Fatalf("attempts=%d backoffs=%v, want a single attempt", attempts, sleeps)
	}
}

func TestDownloadImageRetriesMidStreamConnectionReset(t *testing.T) {
	// A connection reset while the body streams surfaces through the save
	// callback, but it is a transport failure, not a disk failure: the retry
	// must re-fetch the image instead of failing the chapter.
	var attempts int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		first := attempts == 1
		mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		if first {
			// Valid JPEG header, then the connection dies mid-body.
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xe0})
			panic(http.ErrAbortHandler)
		}
		_, _ = w.Write([]byte{0xff, 0xd8, 0x00})
	}))
	defer server.Close()

	client := NewClient(server.Client(), 0)
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	var bodies int
	err := client.DownloadImage(context.Background(), server.URL+"/page.jpg", func(_ string, body io.Reader) error {
		_, copyErr := io.Copy(io.Discard, body)
		if copyErr == nil {
			bodies++
		}
		return copyErr
	})
	if err != nil {
		t.Fatalf("err = %v, want success after retrying the reset", err)
	}
	if attempts != 2 || bodies != 1 {
		t.Fatalf("attempts=%d completeBodies=%d, want 2 attempts and 1 completed body", attempts, bodies)
	}
}
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

func TestLimiterPauseBlocksWaitUntilResume(t *testing.T) {
	limiter := NewLimiter(0, nil, nil)
	limiter.Pause()

	done := make(chan error, 1)
	go func() { done <- limiter.Wait(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("Wait returned while paused: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	limiter.Resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait after resume: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not resume")
	}
}

func TestLimiterWaitHonoursCancelledContextWhilePaused(t *testing.T) {
	limiter := NewLimiter(0, nil, nil)
	limiter.Pause()
	defer limiter.Resume()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- limiter.Wait(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait ignored cancellation")
	}
}

func TestLimiterPauseInterruptsInflightDelay(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(0, 0)
	entered := make(chan time.Duration, 4)
	release := make(chan struct{})
	limiter := NewLimiter(100*time.Millisecond, func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}, func(_ context.Context, duration time.Duration) error {
		entered <- duration
		<-release
		mu.Lock()
		now = now.Add(duration)
		mu.Unlock()
		return nil
	})

	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- limiter.Wait(context.Background()) }()
	if delay := <-entered; delay != 100*time.Millisecond {
		t.Fatalf("pacing delay = %v, want 100ms", delay)
	}

	limiter.Pause()
	release <- struct{}{}
	select {
	case err := <-done:
		t.Fatalf("Wait returned after Pause: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	limiter.Resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait after resume: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not resume")
	}
}
