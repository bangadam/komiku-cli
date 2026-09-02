package komiku

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

var legacyHostPattern = regexp.MustCompile(`^image[0-9]+\.komiku\.to$`)

type SleepFunc func(context.Context, time.Duration) error

type Limiter struct {
	mu         sync.Mutex
	delay      time.Duration
	next       time.Time
	now        func() time.Time
	sleep      SleepFunc
	paused     bool
	generation uint64
	changed    chan struct{}
}

func NewLimiter(delay time.Duration, now func() time.Time, sleep SleepFunc) *Limiter {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepContext
	}
	return &Limiter{delay: delay, now: now, sleep: sleep, changed: make(chan struct{})}
}

func (l *Limiter) Wait(ctx context.Context) error {
	for {
		if err := l.AwaitResumed(ctx); err != nil {
			return err
		}
		l.mu.Lock()
		now := l.now()
		at := now
		if l.next.After(at) {
			at = l.next
		}
		l.next = at.Add(l.delay)
		generation := l.generation
		l.mu.Unlock()
		if delay := at.Sub(now); delay > 0 {
			if err := l.sleep(ctx, delay); err != nil {
				return err
			}
		}
		l.mu.Lock()
		valid := !l.paused && l.generation == generation
		l.mu.Unlock()
		if valid {
			return ctx.Err()
		}
	}
}

func (l *Limiter) Pause() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.paused {
		return
	}
	l.paused = true
	l.generation++
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *Limiter) Resume() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.paused {
		return
	}
	l.paused = false
	l.generation++
	l.next = l.now()
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *Limiter) AwaitResumed(ctx context.Context) error {
	for {
		l.mu.Lock()
		if !l.paused {
			l.mu.Unlock()
			return ctx.Err()
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type Client struct {
	HTTP      *http.Client
	UserAgent string
	BaseURL   string
	Limiter   *Limiter
	Sleep     SleepFunc
}

func NewClient(httpClient *http.Client, imageDelay time.Duration) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		HTTP:      httpClient,
		UserAgent: DefaultUserAgent,
		BaseURL:   DefaultBaseURL,
		Limiter:   NewLimiter(imageDelay, nil, nil),
		Sleep:     sleepContext,
	}
}

func (c *Client) fetchHTML(ctx context.Context, target string) ([]byte, error) {
	data, status, statusText, err := c.fetchPage(ctx, target)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("GET %s: %s", target, statusText)
	}
	return data, nil
}

// fetchPage performs a GET and returns the body with the HTTP status, so
// callers can distinguish missing pages (404) from other failures.
func (c *Client) fetchPage(ctx context.Context, target string) ([]byte, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	statusText := resp.Status
	const maxHTML = 16 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHTML+1))
	if err != nil {
		return nil, resp.StatusCode, statusText, err
	}
	if len(data) > maxHTML {
		return nil, resp.StatusCode, statusText, fmt.Errorf("GET %s: HTML exceeds %d bytes", target, maxHTML)
	}
	return data, resp.StatusCode, statusText, nil
}

func (c *Client) ChapterImages(ctx context.Context, chapterURL string) ([]string, error) {
	data, err := c.fetchHTML(ctx, chapterURL)
	if err != nil {
		return nil, err
	}
	return ExtractImageURLs(data), nil
}

type ImageFormat string

const (
	JPEG ImageFormat = "jpg"
	PNG  ImageFormat = "png"
	WEBP ImageFormat = "webp"
)

func DetectImage(header []byte) (ImageFormat, bool) {
	switch {
	case len(header) >= 2 && header[0] == 0xff && header[1] == 0xd8:
		return JPEG, true
	case len(header) >= 8 && string(header[:8]) == "\x89PNG\r\n\x1a\n":
		return PNG, true
	case len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP":
		return WEBP, true
	default:
		return "", false
	}
}

func ImageExtension(rawURL string, format ImageFormat) string {
	u, err := url.Parse(rawURL)
	if err == nil {
		ext := strings.ToLower(path.Ext(u.Path))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp":
			return ext
		}
	}
	return "." + string(format)
}

func EncodeImageURL(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(html.UnescapeString(rawURL)))
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported image URL scheme %q", u.Scheme)
	}
	segments := strings.Split(u.Path, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	u.RawPath = strings.Join(segments, "/")
	return u.String(), nil
}

func RewriteLegacyURL(encodedURL string) (string, bool) {
	u, err := url.Parse(encodedURL)
	if err != nil || u.Scheme != "https" || !legacyHostPattern.MatchString(strings.ToLower(u.Hostname())) {
		return encodedURL, false
	}
	u.Host = "img.komiku.org"
	return u.String(), true
}

type SaveImageFunc func(extension string, body io.Reader) error

var errSaveImage = errors.New("save image")

func (c *Client) DownloadImage(ctx context.Context, rawURL, chapterURL string, save SaveImageFunc) error {
	encoded, err := EncodeImageURL(rawURL)
	if err != nil {
		return err
	}
	fallback, hasFallback := RewriteLegacyURL(encoded)
	backoff := [...]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := c.fetchImage(ctx, encoded, chapterURL, save); err == nil {
			return nil
		} else {
			lastErr = err
			if errors.Is(err, errSaveImage) {
				return err
			}
		}
		if hasFallback {
			if err := c.fetchImage(ctx, fallback, chapterURL, save); err == nil {
				return nil
			} else {
				lastErr = err
				if errors.Is(err, errSaveImage) {
					return err
				}
			}
		}
		if attempt < len(backoff) {
			if err := c.sleep()(ctx, backoff[attempt]); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("image %s failed after 4 attempts: %w", encoded, lastErr)
}

func (c *Client) fetchImage(ctx context.Context, target, referer string, save SaveImageFunc) error {
	if c.Limiter != nil {
		if err := c.Limiter.Wait(ctx); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("Referer", referer)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return fmt.Errorf("GET %s: %s", target, resp.Status)
	}
	reader := bufio.NewReader(resp.Body)
	header, _ := reader.Peek(12)
	format, valid := DetectImage(header)
	if !valid {
		_, _ = io.Copy(io.Discard, io.LimitReader(reader, 32<<10))
		return fmt.Errorf("GET %s: invalid image payload", target)
	}
	if err := save(ImageExtension(target, format), reader); err != nil {
		return fmt.Errorf("%w: %v", errSaveImage, err)
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return DefaultUserAgent
}

func (c *Client) sleep() SleepFunc {
	if c.Sleep != nil {
		return c.Sleep
	}
	return sleepContext
}
