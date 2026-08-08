// Package pagination provides cursor-based and offset-based pagination helpers.
//
// Cursor encodes an opaque position that DecodeCursor reverses; ParseRequest turns
// the first/last/after/before gRPC arguments into a normalized Request with a
// PageInfo result, and ParseOffsetRequest handles simple page/page-size paging.
package pagination
