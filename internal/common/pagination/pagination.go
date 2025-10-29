package pagination

import (
	"encoding/base64"
	"encoding/json"
)

type Cursor struct {
	ID        string
	Timestamp int64
}

func (c *Cursor) Encode() string {
	data, _ := json.Marshal(c)
	return base64.URLEncoding.EncodeToString(data)
}

func DecodeCursor(encoded string) (*Cursor, error) {
	data, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}

	var cursor Cursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return nil, err
	}

	return &cursor, nil
}

type PageInfo struct {
	HasNextPage     bool
	HasPreviousPage bool
	StartCursor     string
	EndCursor       string
	TotalCount      int
}

type Request struct {
	First  int
	After  string
	Last   int
	Before string
}

func ParseRequest(first, last *int, after, before *string) Request {
	req := Request{}

	if first != nil {
		req.First = *first
	} else {
		req.First = 50
	}

	if last != nil {
		req.Last = *last
	}

	if after != nil {
		req.After = *after
	}

	if before != nil {
		req.Before = *before
	}

	return req
}

type OffsetPagination struct {
	Page     int
	PageSize int
	Offset   int
}

func ParseOffsetRequest(page, pageSize *int) OffsetPagination {
	p := 1
	if page != nil && *page > 0 {
		p = *page
	}

	ps := 50
	if pageSize != nil && *pageSize > 0 && *pageSize <= 100 {
		ps = *pageSize
	}

	return OffsetPagination{
		Page:     p,
		PageSize: ps,
		Offset:   (p - 1) * ps,
	}
}
