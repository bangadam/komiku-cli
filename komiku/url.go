package komiku

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateSeriesURL parses a series URL and restricts it to Komiku production
// hosts or deterministic loopback fixtures.
func ValidateSeriesURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid series URL %q: %w", raw, err)
	}
	if parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid series URL %q", raw)
	}

	host := strings.ToLower(parsed.Hostname())
	loopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	productionSubdomain := strings.TrimSuffix(host, ".komiku.org")
	validProductionSubdomain := productionSubdomain != "" && !strings.HasPrefix(productionSubdomain, ".") && !strings.HasSuffix(productionSubdomain, ".") && !strings.Contains(productionSubdomain, "..")
	production := host == "komiku.org" || (strings.HasSuffix(host, ".komiku.org") && validProductionSubdomain)
	scheme := strings.ToLower(parsed.Scheme)
	switch {
	case production && scheme != "https":
		return nil, fmt.Errorf("invalid series URL %q: Komiku production URLs require HTTPS", raw)
	case production && (parsed.Port() != "" && parsed.Port() != "443" || strings.HasSuffix(parsed.Host, ":")):
		return nil, fmt.Errorf("invalid series URL %q: Komiku production URLs allow only HTTPS port 443", raw)
	case loopback && scheme != "http" && scheme != "https":
		return nil, fmt.Errorf("invalid series URL %q: loopback fixtures require HTTP or HTTPS", raw)
	case !production && !loopback:
		return nil, fmt.Errorf("invalid series URL %q: host must be komiku.org, a komiku.org subdomain, or loopback", raw)
	}

	const prefix = "/manga/"
	if !strings.HasPrefix(parsed.Path, prefix) || !strings.HasSuffix(parsed.Path, "/") {
		return nil, fmt.Errorf("invalid series URL %q: path must be /manga/<slug>/", raw)
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, prefix), "/")
	if slug == "" || slug == "." || slug == ".." || strings.Contains(slug, "/") || strings.TrimSpace(slug) != slug {
		return nil, fmt.Errorf("invalid series URL %q: path must be /manga/<slug>/", raw)
	}
	return parsed, nil
}
