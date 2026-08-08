package gifprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Result is one GIF in a search result: the full URL plus a smaller PreviewURL and
// its pixel dimensions.
type Result struct {
	ID         string
	Title      string
	URL        string
	PreviewURL string
	Width      int
	Height     int
}

// Page is a page of GIF results; NextOffset is the opaque cursor to pass as offset
// for the next page ("" when there are no more).
type Page struct {
	Results    []Result
	NextOffset string
}

// Provider abstracts a GIF search backend so implementations (e.g. Tenor) are
// interchangeable. Enabled reports whether the provider is usable.
type Provider interface {
	Search(ctx context.Context, query string, limit int, offset string) (*Page, error)
	Enabled() bool
}

// TenorProvider implements Provider against Google's Tenor v2 search API.
type TenorProvider struct {
	apiKey string
	client *http.Client
}

// NewTenorProvider builds a TenorProvider with the given API key (empty means
// disabled) and an 8s HTTP timeout.
func NewTenorProvider(apiKey string) *TenorProvider {
	return &TenorProvider{
		apiKey: apiKey,
		client: &http.Client{Timeout: 8 * time.Second},
	}
}

// Enabled reports whether an API key is configured; when false, GIF search is
// disabled and Search should not be called.
func (p *TenorProvider) Enabled() bool { return p.apiKey != "" }

// Search queries Tenor for GIFs matching query and maps the response into a Page.
// limit is clamped to 1..50 (default 20); offset is Tenor's "pos" cursor. It errors
// on an empty query, a non-200 response, or a decode failure, and maps each result
// to its full gif URL plus tinygif preview (with dims when present).
func (p *TenorProvider) Search(ctx context.Context, query string, limit int, offset string) (*Page, error) {
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	q := url.Values{}
	q.Set("key", p.apiKey)
	q.Set("q", query)
	q.Set("limit", strconv.Itoa(limit))
	if offset != "" {
		q.Set("pos", offset)
	}

	endpoint := "https://tenor.googleapis.com/v2/search?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gif provider request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gif provider returned status %d", resp.StatusCode)
	}

	var body struct {
		Next    string `json:"next"`
		Results []struct {
			ID           string `json:"id"`
			ContentDesc  string `json:"content_description"`
			MediaFormats struct {
				Gif struct {
					URL  string `json:"url"`
					Dims []int  `json:"dims"`
				} `json:"gif"`
				TinyGif struct {
					URL string `json:"url"`
				} `json:"tinygif"`
			} `json:"media_formats"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode gif response: %w", err)
	}

	page := &Page{NextOffset: body.Next}
	for _, r := range body.Results {
		res := Result{
			ID:         r.ID,
			Title:      r.ContentDesc,
			URL:        r.MediaFormats.Gif.URL,
			PreviewURL: r.MediaFormats.TinyGif.URL,
		}
		if len(r.MediaFormats.Gif.Dims) == 2 {
			res.Width = r.MediaFormats.Gif.Dims[0]
			res.Height = r.MediaFormats.Gif.Dims[1]
		}
		page.Results = append(page.Results, res)
	}
	return page, nil
}
