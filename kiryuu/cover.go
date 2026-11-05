package kiryuu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// maxCoverBytes caps a single cover download so a hostile or broken page
// cannot stream an unbounded payload into memory.
const maxCoverBytes = 8 << 20

var (
	coverLDJSONPattern = regexp.MustCompile(`(?is)<script\b[^>]*\btype\s*=\s*["']application/ld\+json["'][^>]*>(.*?)</script>`)
	coverMetaPattern   = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	coverAttrPattern   = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*["']([^"']*)["']`)
)

// CoverURL resolves the cover art a series page advertises. Kiryuu publishes
// it in the ComicSeries JSON-LD block, with a WebPage thumbnail and an
// og:image meta tag as fallbacks.
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
	var cover ldCover
	for _, block := range coverLDJSONPattern.FindAllSubmatch(data, -1) {
		var payload any
		if err := json.Unmarshal(block[1], &payload); err != nil {
			continue
		}
		cover = mergeLDCover(cover, ldCoverFrom(payload))
	}
	for _, candidate := range []string{cover.comicImage, cover.thumbnail} {
		if resolved, ok := resolveAgainst(base, candidate); ok {
			return resolved, nil
		}
	}
	for _, tag := range coverMetaPattern.FindAll(data, -1) {
		attrs := coverAttrs(tag)
		if !strings.EqualFold(attrs["property"], "og:image") && !strings.EqualFold(attrs["name"], "og:image") {
			continue
		}
		if resolved, ok := resolveAgainst(base, attrs["content"]); ok {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("series page %s does not expose a cover image", pageURL)
}

// ldCover is the pair of cover candidates a JSON-LD block may carry.
type ldCover struct {
	comicImage string
	thumbnail  string
}

// ldCoverFrom walks one decoded JSON-LD value. Only @graph is traversed:
// Kiryuu publishes the WebPage and ComicSeries nodes there.
func ldCoverFrom(node any) ldCover {
	var cover ldCover
	switch value := node.(type) {
	case []any:
		for _, item := range value {
			cover = mergeLDCover(cover, ldCoverFrom(item))
		}
	case map[string]any:
		if ldHasType(value["@type"], "ComicSeries") {
			cover.comicImage = ldImageURL(value["image"])
		}
		if raw, ok := value["thumbnailUrl"].(string); ok && cover.thumbnail == "" {
			cover.thumbnail = raw
		}
		if child, ok := value["@graph"]; ok {
			cover = mergeLDCover(cover, ldCoverFrom(child))
		}
	}
	return cover
}

func mergeLDCover(current, next ldCover) ldCover {
	if current.comicImage == "" {
		current.comicImage = next.comicImage
	}
	if current.thumbnail == "" {
		current.thumbnail = next.thumbnail
	}
	return current
}

// ldImageURL accepts both JSON-LD image shapes: a bare URL or an object with
// a url member.
func ldImageURL(node any) string {
	switch value := node.(type) {
	case string:
		return value
	case map[string]any:
		if raw, ok := value["url"].(string); ok {
			return raw
		}
	case []any:
		for _, item := range value {
			if raw := ldImageURL(item); raw != "" {
				return raw
			}
		}
	}
	return ""
}

// ldHasType matches a JSON-LD @type that may be a string or an array.
func ldHasType(node any, wanted string) bool {
	switch value := node.(type) {
	case string:
		return strings.EqualFold(value, wanted)
	case []any:
		for _, item := range value {
			if ldHasType(item, wanted) {
				return true
			}
		}
	}
	return false
}

// coverAttrs parses the attributes of one HTML tag.
func coverAttrs(tag []byte) map[string]string {
	attrs := make(map[string]string, 4)
	for _, match := range coverAttrPattern.FindAllSubmatch(tag, -1) {
		attrs[strings.ToLower(string(match[1]))] = string(match[2])
	}
	return attrs
}

// DownloadCover fetches cover art and returns its bytes plus detected format.
// seriesURL is accepted for call-site compatibility; Kiryuu's CDN needs no
// referer, so it is not sent.
func (c *Client) DownloadCover(ctx context.Context, coverURL, seriesURL string) ([]byte, ImageFormat, error) {
	var buf bytes.Buffer
	var format ImageFormat
	err := c.DownloadImage(ctx, coverURL, func(extension string, body io.Reader) error {
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
