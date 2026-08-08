package pagination

import (
	"encoding/base64"
	"encoding/json"
)

// Cursor identifies a position in a result set by record ID and timestamp,
// enabling stable keyset pagination. It is serialized to an opaque base64 token
// for clients.
type Cursor struct {
	ID        string
	Timestamp int64
}

// Encode serializes the cursor to a URL-safe base64 JSON token. The marshal
// error is ignored because a Cursor of plain scalars cannot fail to marshal.
func (c *Cursor) Encode() string {
	data, _ := json.Marshal(c)
	return base64.URLEncoding.EncodeToString(data)
}

// DecodeCursor parses a token produced by Cursor.Encode, returning an error if
// it is not valid base64 or does not contain a JSON Cursor.
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

// PageInfo describes a returned page in the Relay connection style: whether
// neighbours exist and the cursors bounding this page.
type PageInfo struct {
	HasNextPage     bool
	HasPreviousPage bool
	StartCursor     string
	EndCursor       string
	TotalCount      int
}

// Request is a normalized cursor-pagination request: First/After page forward,
// Last/Before page backward.
type Request struct {
	First  int
	After  string
	Last   int
	Before string
}

// ParseRequest normalizes optional GraphQL-style pagination arguments into a
// Request, defaulting First to 50 when neither first nor last is supplied. Nil
// pointers are treated as "not provided".
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

// OffsetPagination is a normalized offset/limit request. Offset is precomputed
// as (Page-1)*PageSize for direct use in a SQL OFFSET.
type OffsetPagination struct {
	Page     int
	PageSize int
	Offset   int
}

// ParseOffsetRequest normalizes optional page and pageSize arguments, defaulting
// to page 1 and page size 50 and clamping pageSize to the range 1..100. It then
// derives the SQL Offset from the resulting page and size.
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
