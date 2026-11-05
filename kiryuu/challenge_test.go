package kiryuu

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFetchHTMLSolvesCloudflareChallengeOnce(t *testing.T) {
	challenged, solved := 0, 0
	var sawCookie, sawUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") == "" {
			challenged++
			w.Header().Set("Cf-Mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<title>Just a moment...</title>")
			return
		}
		sawCookie = r.Header.Get("Cookie")
		sawUserAgent = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, "<html>chapter</html>")
	}))
	defer server.Close()

	client := NewClient(server.Client(), 0)
	client.Solver = func(_ context.Context, target string) (SolvedSession, error) {
		solved++
		parsed, err := url.Parse(target)
		if err != nil {
			t.Fatalf("parse target: %v", err)
		}
		return SolvedSession{UserAgent: "solver-ua", Cookie: "cf_clearance=abc", Host: parsed.Hostname()}, nil
	}

	data, err := client.fetchHTML(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchHTML: %v", err)
	}
	if string(data) != "<html>chapter</html>" {
		t.Fatalf("body = %q", data)
	}
	if challenged != 1 || solved != 1 {
		t.Fatalf("challenged=%d solved=%d, want 1/1", challenged, solved)
	}
	if sawCookie != "cf_clearance=abc" {
		t.Fatalf("retry cookie = %q", sawCookie)
	}
	if sawUserAgent != "solver-ua" {
		t.Fatalf("retry user agent = %q, want the solver identity", sawUserAgent)
	}
}

func TestFetchHTMLSolvesChallengeDetectedByBodyMarker(t *testing.T) {
	challenged := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") == "" {
			challenged++
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<title>Tunggu sebentar...</title>")
			return
		}
		_, _ = io.WriteString(w, "<html>chapter</html>")
	}))
	defer server.Close()

	client := NewClient(server.Client(), 0)
	client.Solver = func(context.Context, string) (SolvedSession, error) {
		return SolvedSession{Cookie: "cf_clearance=abc"}, nil
	}
	data, err := client.fetchHTML(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchHTML: %v", err)
	}
	if string(data) != "<html>chapter</html>" || challenged != 1 {
		t.Fatalf("body=%q challenged=%d, want one solved challenge", data, challenged)
	}
}

func TestIsCloudflareChallenge(t *testing.T) {
	tests := []struct {
		name   string
		header string
		body   string
		want   bool
	}{
		{name: "header", header: "challenge", body: "<html>ok</html>", want: true},
		{name: "header case", header: "Challenge", body: "<html>ok</html>", want: true},
		{name: "english marker", body: "<title>Just a moment...</title>", want: true},
		{name: "indonesian marker", body: "<title>Tunggu sebentar...</title>", want: true},
		{name: "script marker", body: `<script src="https://challenges.cloudflare.com/turnstile"></script>`, want: true},
		{name: "plain page", body: "<html>chapter</html>"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			if test.header != "" {
				headers.Set("Cf-Mitigated", test.header)
			}
			if got := isCloudflareChallenge(headers, []byte(test.body)); got != test.want {
				t.Fatalf("isCloudflareChallenge = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFetchHTMLChallengeWithoutSolverStays403(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<title>Just a moment...</title>")
	}))
	defer server.Close()

	client := NewClient(server.Client(), 0)
	_, err := client.fetchHTML(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the plain 403", err)
	}
}

func TestFetchHTMLReportsSolverFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<title>Tunggu sebentar...</title>")
	}))
	defer server.Close()

	client := NewClient(server.Client(), 0)
	var sleeps []time.Duration
	client.Sleep = func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		return nil
	}
	client.Solver = func(context.Context, string) (SolvedSession, error) {
		return SolvedSession{}, errors.New("camoufox missing")
	}
	_, err := client.fetchHTML(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "cloudflare challenge not solved") {
		t.Fatalf("err = %v, want a solver failure hint", err)
	}
	if !strings.Contains(err.Error(), "camoufox missing") {
		t.Fatalf("err = %v, want the solver error preserved", err)
	}
	if want := []time.Duration{500 * time.Millisecond, time.Second}; !reflect.DeepEqual(sleeps, want) {
		t.Fatalf("retry backoffs = %v, want %v", sleeps, want)
	}
}

func TestClientSendsCachedSessionOnlyToItsHost(t *testing.T) {
	var cookies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookies = append(cookies, r.Header.Get("Cookie"))
		_, _ = io.WriteString(w, "<html>ok</html>")
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(server.Client(), 0)
	client.Session = &SolvedSession{UserAgent: "solver-ua", Cookie: "cf_clearance=abc", Host: "example.invalid"}
	if _, err := client.fetchHTML(context.Background(), server.URL); err != nil {
		t.Fatalf("fetchHTML: %v", err)
	}
	if cookies[0] != "" {
		t.Fatalf("cookie leaked to a different host: %q", cookies[0])
	}

	client.Session = &SolvedSession{UserAgent: "solver-ua", Cookie: "cf_clearance=abc", Host: parsed.Hostname()}
	if _, err := client.fetchHTML(context.Background(), server.URL); err != nil {
		t.Fatalf("fetchHTML: %v", err)
	}
	if cookies[1] != "cf_clearance=abc" {
		t.Fatalf("cookie for the solved host = %q", cookies[1])
	}
}

func TestClientSendsSolvedUserAgentWithSessionCookie(t *testing.T) {
	var userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, "<html>ok</html>")
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(server.Client(), 0)
	client.Session = &SolvedSession{UserAgent: "solver-ua", Cookie: "cf_clearance=abc", Host: parsed.Hostname()}
	if _, err := client.fetchHTML(context.Background(), server.URL); err != nil {
		t.Fatalf("fetchHTML: %v", err)
	}
	if userAgent != "solver-ua" {
		t.Fatalf("user agent = %q, want the solver identity", userAgent)
	}
}
