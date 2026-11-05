package kiryuu

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateSeriesURL parses a series URL and restricts it to Kiryuu production
// hosts or deterministic loopback fixtures. Kiryuu rotates numbered subdomains
// (v1, v2, …), so every kiryuu.to subdomain is accepted.
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
	productionSubdomain := strings.TrimSuffix(host, ".kiryuu.to")
	validProductionSubdomain := productionSubdomain != "" && !strings.HasPrefix(productionSubdomain, ".") && !strings.HasSuffix(productionSubdomain, ".") && !strings.Contains(productionSubdomain, "..")
	production := host == "kiryuu.to" || (strings.HasSuffix(host, ".kiryuu.to") && validProductionSubdomain)
	scheme := strings.ToLower(parsed.Scheme)
	switch {
	case production && scheme != "https":
		return nil, fmt.Errorf("invalid series URL %q: Kiryuu production URLs require HTTPS", raw)
	case production && (parsed.Port() != "" && parsed.Port() != "443" || strings.HasSuffix(parsed.Host, ":")):
		return nil, fmt.Errorf("invalid series URL %q: Kiryuu production URLs allow only HTTPS port 443", raw)
	case loopback && scheme != "http" && scheme != "https":
		return nil, fmt.Errorf("invalid series URL %q: loopback fixtures require HTTP or HTTPS", raw)
	case !production && !loopback:
		return nil, fmt.Errorf("invalid series URL %q: host must be kiryuu.to, a kiryuu.to subdomain, or loopback", raw)
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
