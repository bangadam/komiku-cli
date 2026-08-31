package main

import (
	"context"
	"flag"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bangadam/komiku-cli/tui"
)

type fixtureTransport struct {
	base *url.URL
}

func (t fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Hostname() != "en.wikipedia.org" {
		return http.DefaultTransport.RoundTrip(request)
	}
	rewritten := request.Clone(request.Context())
	target := *request.URL
	target.Scheme = t.base.Scheme
	target.Host = t.base.Host
	rewritten.URL = &target
	rewritten.Host = t.base.Host
	return http.DefaultTransport.RoundTrip(rewritten)
}

func main() {
	baseURL := flag.String("base-url", "", "fixture server base URL")
	configPath := flag.String("config", "", "fixture config path")
	workers := flag.Int("workers", 2, "download worker count")
	flag.Parse()
	fixtureURL, err := url.Parse(*baseURL)
	if err != nil || fixtureURL.Scheme == "" || fixtureURL.Host == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(tui.Run(ctx, os.Stdin, os.Stdout, os.Stderr, tui.Dependencies{
		HTTP:       &http.Client{Timeout: 10 * time.Second, Transport: fixtureTransport{base: fixtureURL}},
		BaseURL:    *baseURL,
		ConfigPath: *configPath,
		Workers:    *workers,
	}))
}
