package komiku

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// maxCoverBytes caps a single cover download so a hostile or broken page
// cannot stream an unbounded payload into memory.
const maxCoverBytes = 8 << 20

var (
	metaTagPattern = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	imgTagPattern  = regexp.MustCompile(`(?is)<img\b[^>]*>`)
)

// CoverURL resolves the cover art a series page advertises. Komiku publishes
// it as an og:image meta tag; older layouts only carry an itemprop image.
func (c *Client) CoverURL(ctx context.Context, seriesURL string) (string, error) {
	if _, err := ValidateSeriesURL(seriesURL); err != nil {
		return "", err
	}
	data, err := c.fetchHTML(ctx, seriesURL)
	if err != nil {
		return "", err
	}
	return ExtractCoverURL(data, seriesURL)
}

// ExtractCoverURL reads the cover URL out of a series page.
func ExtractCoverURL(data []byte, pageURL string) (string, error) {
	base, err := url.Parse(pageURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid series URL %q", pageURL)
	}
	for _, tag := range metaTagPattern.FindAll(data, -1) {
		attrs := parseAttrs(tag)
		if !strings.EqualFold(attrs["property"], "og:image") && !strings.EqualFold(attrs["name"], "og:image") {
			continue
		}
		if cover, ok := resolveCoverURL(base, attrs["content"]); ok {
			return cover, nil
		}
	}
	for _, tag := range imgTagPattern.FindAll(data, -1) {
		attrs := parseAttrs(tag)
		if !strings.EqualFold(attrs["itemprop"], "image") {
			continue
		}
		if cover, ok := resolveCoverURL(base, attrs["src"]); ok {
			return cover, nil
		}
	}
	return "", fmt.Errorf("series page %s does not expose a cover image", pageURL)
}

func resolveCoverURL(base *url.URL, raw string) (string, bool) {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return "", false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	// thumbnail.komiku.to no longer answers; the .org mirror serves the same
	// files, so rewrite instead of failing the download.
	if strings.EqualFold(resolved.Hostname(), "thumbnail.komiku.to") {
		resolved.Host = "thumbnail.komiku.org"
	}
	return resolved.String(), true
}

// DownloadCover fetches cover art and returns its bytes plus detected format.
// It reuses the image downloader so the request limiter, legacy-host rewrite,
// and payload validation behave exactly like chapter pages.
func (c *Client) DownloadCover(ctx context.Context, coverURL, seriesURL string) ([]byte, ImageFormat, error) {
	var buf bytes.Buffer
	var format ImageFormat
	err := c.DownloadImage(ctx, coverURL, seriesURL, func(extension string, body io.Reader) error {
		if _, err := io.Copy(&buf, io.LimitReader(body, maxCoverBytes+1)); err != nil {
			return fmt.Errorf("%w: read cover: %v", errSaveImage, err)
		}
		if buf.Len() > maxCoverBytes {
			return fmt.Errorf("%w: cover exceeds %d bytes", errSaveImage, maxCoverBytes)
		}
		format = ImageFormat(strings.TrimPrefix(extension, "."))
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if format == "" {
		return nil, "", fmt.Errorf("cover %s: unsupported image format", coverURL)
	}
	return buf.Bytes(), format, nil
}
