package unfurl

import (
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

const (
	// maxTitleLen caps preview titles (in runes) before truncation.
	maxTitleLen = 300
	// maxDescLen caps preview descriptions (in runes) before truncation.
	maxDescLen = 1000
)

// parseOGTags walks the HTML in r and builds a LinkPreview, preferring OpenGraph
// (og:*) tags and falling back to Twitter card tags, then the <title> and <meta
// name="description">, and finally the host as site name. rawURL is the base for
// resolving relative image/favicon references (adjusted by any <base href>).
// Title and description are whitespace-collapsed and truncated; image and favicon
// are resolved to absolute http(s) URLs.
func parseOGTags(r io.Reader, rawURL string) (*LinkPreview, error) {
	base, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}

	preview := &LinkPreview{URL: rawURL}

	var (
		htmlTitle    string
		metaDesc     string
		twitterTitle string
		twitterDesc  string
		twitterImage string
		iconHref     string
		iconRel      string
		inHead       bool
	)

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "head":
				inHead = true
				defer func() { inHead = false }()
			case "title":
				if htmlTitle == "" && n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
					htmlTitle = strings.TrimSpace(n.FirstChild.Data)
				}
			case "base":
				if href := attr(n, "href"); href != "" {
					if u, err := base.Parse(href); err == nil {
						base = u
					}
				}
			case "meta":
				key := strings.ToLower(attr(n, "property"))
				if key == "" {
					key = strings.ToLower(attr(n, "name"))
				}
				content := strings.TrimSpace(attr(n, "content"))
				if key == "" || content == "" {
					break
				}
				switch key {
				case "og:title":
					preview.Title = content
				case "og:description":
					preview.Description = content
				case "og:image", "og:image:url", "og:image:secure_url":
					if preview.Image == "" {
						preview.Image = content
					}
				case "og:site_name":
					preview.SiteName = content
				case "description":
					if metaDesc == "" {
						metaDesc = content
					}
				case "twitter:title":
					twitterTitle = content
				case "twitter:description":
					twitterDesc = content
				case "twitter:image", "twitter:image:src":
					if twitterImage == "" {
						twitterImage = content
					}
				}
			case "link":
				rel := strings.ToLower(attr(n, "rel"))
				if !strings.Contains(rel, "icon") || strings.Contains(rel, "mask-icon") {
					break
				}
				href := strings.TrimSpace(attr(n, "href"))
				if href == "" {
					break
				}
				if iconHref == "" || iconPriority(rel) > iconPriority(iconRel) {
					iconHref = href
					iconRel = rel
				}
			case "body":
				if !inHead {
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if preview.Title == "" {
		preview.Title = twitterTitle
	}
	if preview.Title == "" {
		preview.Title = htmlTitle
	}
	if preview.Description == "" {
		preview.Description = twitterDesc
	}
	if preview.Description == "" {
		preview.Description = metaDesc
	}
	if preview.Image == "" {
		preview.Image = twitterImage
	}
	if preview.SiteName == "" {
		preview.SiteName = base.Hostname()
	}

	preview.Title = truncate(collapseSpace(preview.Title), maxTitleLen)
	preview.Description = truncate(collapseSpace(preview.Description), maxDescLen)
	preview.Image = resolveRef(base, preview.Image)
	preview.Favicon = resolveRef(base, iconHref)

	return preview, nil
}

// attr returns the value of the named attribute on n (case-insensitive match),
// or "" if absent.
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

// iconPriority ranks a link rel value so the best favicon wins when a page
// declares several: apple-touch-icon (3) > shortcut icon (2) > icon (1) > other (0).
func iconPriority(rel string) int {
	switch {
	case strings.Contains(rel, "apple-touch-icon"):
		return 3
	case strings.Contains(rel, "shortcut icon"):
		return 2
	case strings.Contains(rel, "icon"):
		return 1
	default:
		return 0
	}
}

// resolveRef resolves ref against base and returns the absolute URL, or "" if ref
// is empty, unparseable, or resolves to a non-http(s) scheme (e.g. data: or
// javascript:), so callers never emit those.
func resolveRef(base *url.URL, ref string) string {
	if ref == "" {
		return ""
	}
	u, err := base.Parse(ref)
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

// collapseSpace replaces every run of whitespace in s with a single space and
// trims the ends, flattening multi-line meta content into one line.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncate shortens s to at most max runes (not bytes, so it is multibyte-safe),
// appending an ellipsis when it cuts; strings already within the limit are
// returned unchanged.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}
