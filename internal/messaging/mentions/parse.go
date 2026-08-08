package mentions

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mentionRE matches an @handle of 1-32 word characters, capturing the handle
// without the leading @.
var mentionRE = regexp.MustCompile(`@([a-zA-Z0-9_]{1,32})`)

// Parser resolves @-mentions to user IDs against the users table.
type Parser struct {
	pool *pgxpool.Pool
}

// NewParser builds a Parser over the given connection pool.
func NewParser(pool *pgxpool.Pool) *Parser { return &Parser{pool: pool} }

// Parse extracts @handles from content and resolves them to user IDs, merging
// them with clientHints (deduplicated, hints kept first). Handles are matched
// case-insensitively against non-deleted users. If content has no @handles it
// returns clientHints unchanged; on query error it returns clientHints together
// with the error so the caller can still fall back to the hints.
func (p *Parser) Parse(ctx context.Context, content string, clientHints []uuid.UUID) ([]uuid.UUID, error) {
	matches := mentionRE.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return clientHints, nil
	}
	handles := make([]string, 0, len(matches))
	for _, m := range matches {
		handles = append(handles, strings.ToLower(m[1]))
	}
	rows, err := p.pool.Query(ctx,
		`SELECT id FROM users WHERE lower(handle) = ANY($1) AND deleted_at IS NULL`, handles)
	if err != nil {
		return clientHints, err
	}
	defer rows.Close()

	seen := make(map[uuid.UUID]bool)
	for _, id := range clientHints {
		seen[id] = true
	}
	result := append([]uuid.UUID{}, clientHints...)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			continue
		}
		if !seen[id] {
			result = append(result, id)
			seen[id] = true
		}
	}
	return result, rows.Err()
}
