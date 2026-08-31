package komiku

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
)

const DefaultBaseURL = "https://komiku.org/"

var (
	anchorPattern = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*["'][^"']+["'][^>]*>.*?</a>`)
	tagPattern    = regexp.MustCompile(`(?is)<[^>]+>`)
	altPattern    = regexp.MustCompile(`(?is)\balt\s*=\s*["']([^"']+)["']`)
)

type Series struct {
	Title string
	Slug  string
	Href  string
	URL   string
}

func (c *Client) Search(ctx context.Context, query string) ([]Series, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is empty")
	}
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid search base URL %q", baseURL)
	}
	target := base.ResolveReference(&url.URL{Path: "/"})
	values := target.Query()
	values.Set("post_type", "manga")
	values.Set("s", query)
	target.RawQuery = values.Encode()
	data, err := c.fetchHTML(ctx, target.String())
	if err != nil {
		return nil, err
	}
	results, err := ExtractSeriesResults(data, target.String())
	if err != nil || len(results) > 0 {
		return results, err
	}
	if hasNoSearchResults(data) {
		return results, nil
	}
	deferredURL, err := deferredSearchURL(data, target)
	if err != nil {
		return nil, err
	}
	if deferredURL == "" {
		return nil, errors.New("search page did not contain results or a deferred results URL")
	}
	data, err = c.fetchHTML(ctx, deferredURL)
	if err != nil {
		return nil, err
	}
	results, err = ExtractSeriesResults(data, target.String())
	if err != nil || len(results) > 0 {
		return results, err
	}
	if hasNoSearchResults(data) {
		return results, nil
	}
	return nil, errors.New("search fragment did not contain series results or a no-results marker")
}

func deferredSearchURL(data []byte, canonical *url.URL) (string, error) {
	query := canonical.Query().Get("s")
	for _, tag := range tagPattern.FindAll(data, -1) {
		raw := html.UnescapeString(strings.TrimSpace(parseAttrs(tag)["hx-get"]))
		if raw == "" {
			continue
		}
		ref, err := url.Parse(raw)
		if err != nil {
			if strings.Contains(raw, "s=") {
				return "", fmt.Errorf("invalid deferred search URL %q", raw)
			}
			continue
		}
		deferred := canonical.ResolveReference(ref)
		values := deferred.Query()
		if values.Get("s") != query {
			continue
		}
		if values.Get("post_type") != "manga" {
			return "", fmt.Errorf("invalid deferred search URL %q: post_type=manga is required", raw)
		}
		if !allowedDeferredSearchURL(canonical, deferred) {
			return "", fmt.Errorf("invalid deferred search URL %q: origin is not allowed", raw)
		}
		return deferred.String(), nil
	}
	return "", nil
}

func allowedDeferredSearchURL(canonical, deferred *url.URL) bool {
	if deferred.Opaque != "" || deferred.User != nil || deferred.Fragment != "" {
		return false
	}
	scheme := strings.ToLower(deferred.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if strings.EqualFold(deferred.Scheme, canonical.Scheme) && strings.EqualFold(deferred.Host, canonical.Host) {
		return true
	}
	canonicalProduction := strings.EqualFold(canonical.Scheme, "https") &&
		strings.EqualFold(canonical.Hostname(), "komiku.org") &&
		(canonical.Port() == "" || canonical.Port() == "443")
	deferredAPI := scheme == "https" &&
		strings.EqualFold(deferred.Hostname(), "api.komiku.org") &&
		(deferred.Port() == "" || deferred.Port() == "443") &&
		deferred.Path == "/"
	return canonicalProduction && deferredAPI
}

func hasNoSearchResults(data []byte) bool {
	for _, tag := range tagPattern.FindAll(data, -1) {
		if hasClassToken(parseAttrs(tag)["class"], "no-results") {
			return true
		}
	}
	return false
}

func ExtractSeriesResults(data []byte, baseURL string) ([]Series, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid search URL %q", baseURL)
	}
	anchors := anchorPattern.FindAll(data, -1)
	results := make([]Series, 0, len(anchors))
	seen := make(map[string]bool, len(anchors))
	candidates := 0
	for _, anchor := range anchors {
		openEnd := strings.IndexByte(string(anchor), '>')
		if openEnd < 0 {
			continue
		}
		attrs := parseAttrs(anchor[:openEnd+1])
		href := html.UnescapeString(strings.TrimSpace(attrs["href"]))
		ref, err := url.Parse(href)
		if err != nil {
			continue
		}
		resolved := base.ResolveReference(ref)
		if !strings.EqualFold(resolved.Scheme, base.Scheme) || !strings.EqualFold(resolved.Host, base.Host) {
			continue
		}
		parts := strings.Split(strings.Trim(resolved.Path, "/"), "/")
		if len(parts) != 2 || parts[0] != "manga" || parts[1] == "" {
			continue
		}
		candidates++
		if seen[resolved.String()] {
			continue
		}
		title := cleanSearchText(attrs["title"])
		if title == "" {
			body := anchor[openEnd+1:]
			if closeStart := strings.LastIndex(strings.ToLower(string(body)), "</a"); closeStart >= 0 {
				body = body[:closeStart]
			}
			if match := altPattern.FindSubmatch(body); len(match) == 2 {
				title = cleanSearchText(string(match[1]))
			}
			if title == "" {
				title = cleanSearchText(string(tagPattern.ReplaceAll(body, []byte(" "))))
			}
		}
		if title == "" {
			continue
		}
		slug, err := url.PathUnescape(parts[1])
		if err != nil || slug == "" {
			continue
		}
		seen[resolved.String()] = true
		results = append(results, Series{Title: title, Slug: slug, Href: href, URL: resolved.String()})
	}
	if candidates > 0 && len(results) == 0 {
		return nil, errors.New("search results contain series links without usable titles")
	}
	return results, nil
}

func cleanSearchText(value string) string {
	return strings.Join(strings.Fields(html.UnescapeString(value)), " ")
}
