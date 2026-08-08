package unfurl

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
)

// LinkPreview is the extracted metadata for a URL; it is also the value cached
// (as JSON) under the unfurl cache key.
type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
	SiteName    string `json:"site_name"`
	Favicon     string `json:"favicon"`
}

// Service fetches URLs and builds link previews, backed by an optional cache
// (nil-safe) and an SSRF-hardened HTTP client.
type Service struct {
	cache  *cache.Cache
	client *http.Client
}

const (
	// unfurlCacheTTL is how long a preview is cached, keyed by raw URL.
	unfurlCacheTTL = 1 * time.Hour
	// unfurlMaxBodySize caps how many bytes of the response body are parsed,
	// protecting against oversized pages.
	unfurlMaxBodySize = 512 * 1024
	// unfurlTimeout bounds the whole fetch, including redirects.
	unfurlTimeout = 10 * time.Second
	// unfurlMaxRedirect is the maximum number of redirects followed before failing.
	unfurlMaxRedirect = 5
)

// allowedPorts is the SSRF allow-list of destination ports ("" means the
// scheme's default); connections to any other port are refused at dial time.
var allowedPorts = map[string]bool{"": true, "80": true, "443": true, "8080": true}

// NewService builds a Service whose HTTP client is hardened against SSRF: at dial
// time it enforces allowedPorts, resolves the host, and rejects the connection if
// any resolved IP is blocked (see isBlockedIP), and it limits redirects to
// unfurlMaxRedirect while disallowing non-http(s) redirect schemes. cacheClient
// may be nil to disable caching.
func NewService(cacheClient *cache.Cache) *Service {
	dialer := &net.Dialer{Timeout: 5 * time.Second}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if !allowedPorts[port] {
				return nil, fmt.Errorf("port %s not allowed", port)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if isBlockedIP(ip.IP) {
					return nil, fmt.Errorf("resolved address is not allowed")
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}

	return &Service{
		cache: cacheClient,
		client: &http.Client{
			Timeout:   unfurlTimeout,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= unfurlMaxRedirect {
					return fmt.Errorf("too many redirects")
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return fmt.Errorf("disallowed redirect scheme")
				}
				return nil
			},
		},
	}
}

// isBlockedIP reports whether ip must not be dialed for SSRF safety: it blocks
// nil, loopback, link-local, multicast, unspecified and private ranges, the
// 169.254.169.254 cloud metadata address, and IPv6 unique-local (fc00::/7)
// addresses.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	if ip.Equal(net.ParseIP("169.254.169.254")) {
		return true
	}
	if v6 := ip.To16(); v6 != nil && ip.To4() == nil && (v6[0]&0xfe) == 0xfc {
		return true
	}
	return false
}

// cacheKey returns the cache key for a preview, namespacing the raw URL under
// "unfurl:".
func (s *Service) cacheKey(rawURL string) string { return "unfurl:" + rawURL }

// Unfurl returns a link preview for rawURL. It rejects non-http(s) URLs, serves a
// cached result when present, and otherwise fetches the page with a bot
// User-Agent. Non-HTML responses (or HTTP errors) short-circuit: a >=400 status
// is an error, while a non-text/html body yields a bare preview holding only the
// URL. On success it parses OpenGraph/meta tags, defaults a missing favicon to
// /favicon.ico on the host, and caches the result for unfurlCacheTTL. Note: the
// SSRF checks live in the HTTP client's dialer (see NewService), not here.
func (s *Service) Unfurl(ctx context.Context, rawURL string) (*LinkPreview, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid or non-http(s) URL")
	}

	if s.cache != nil {
		var cached LinkPreview
		if err := s.cache.Get(ctx, s.cacheKey(rawURL), &cached); err == nil {
			return &cached, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ConcordBot/1.0 (+https://concord.akarpov.ru)")
	req.Header.Set("Accept", "text/html")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		return &LinkPreview{URL: rawURL}, nil
	}

	body := io.LimitReader(resp.Body, unfurlMaxBodySize)
	preview, err := parseOGTags(body, rawURL)
	if err != nil {
		return nil, err
	}

	if preview.Favicon == "" {
		preview.Favicon = parsed.Scheme + "://" + parsed.Host + "/favicon.ico"
	}

	if s.cache != nil {
		_ = s.cache.Set(ctx, s.cacheKey(rawURL), preview, unfurlCacheTTL)
	}

	return preview, nil
}
