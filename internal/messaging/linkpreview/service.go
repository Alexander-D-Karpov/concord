package unfurl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"golang.org/x/net/html"
)

type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
	SiteName    string `json:"site_name"`
	Favicon     string `json:"favicon"`
}

type Service struct {
	cache  *cache.Cache
	client *http.Client
}

const (
	unfurlCacheTTL    = 1 * time.Hour
	unfurlMaxBodySize = 512 * 1024
	unfurlTimeout     = 10 * time.Second
)

func NewService(cacheClient *cache.Cache) *Service {
	return &Service{
		cache: cacheClient,
		client: &http.Client{
			Timeout: unfurlTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}
}

func (s *Service) cacheKey(rawURL string) string {
	return "unfurl:" + rawURL
}

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
	defer func() {
		_ = resp.Body.Close()
	}()

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

func parseOGTags(r io.Reader, sourceURL string) (*LinkPreview, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	preview := &LinkPreview{URL: sourceURL}
	var titleFromTag string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "meta":
				handleMeta(n, preview)
			case "title":
				if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
					titleFromTag = n.FirstChild.Data
				}
			case "link":
				handleLink(n, preview, sourceURL)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if preview.Title == "" {
		preview.Title = titleFromTag
	}

	return preview, nil
}

func handleMeta(n *html.Node, p *LinkPreview) {
	var property, name, content string
	for _, a := range n.Attr {
		switch a.Key {
		case "property":
			property = a.Val
		case "name":
			name = a.Val
		case "content":
			content = a.Val
		}
	}

	switch property {
	case "og:title":
		p.Title = content
	case "og:description":
		p.Description = content
	case "og:image":
		p.Image = content
	case "og:site_name":
		p.SiteName = content
	}

	if name == "description" && p.Description == "" {
		p.Description = content
	}
}

func handleLink(n *html.Node, p *LinkPreview, sourceURL string) {
	var rel, href string
	for _, a := range n.Attr {
		switch a.Key {
		case "rel":
			rel = a.Val
		case "href":
			href = a.Val
		}
	}

	if strings.Contains(rel, "icon") && href != "" {
		if strings.HasPrefix(href, "/") {
			if parsed, err := url.Parse(sourceURL); err == nil {
				href = parsed.Scheme + "://" + parsed.Host + href
			}
		}
		p.Favicon = href
	}
}
