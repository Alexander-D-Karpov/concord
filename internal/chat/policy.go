package chat

import (
	"regexp"
	"strings"
)

// wordTokenizer splits text into maximal runs of word characters (Unicode letters,
// numbers, and underscore) alternating with runs of everything else, so delimiters
// are preserved exactly on rejoin.
var wordTokenizer = regexp.MustCompile(`[\p{L}\p{N}_]+|[^\p{L}\p{N}_]+`)

// censorWords masks each configured filter word in content with "***", matching
// whole words case-insensitively and Unicode-aware (so "badword"/"BadWord" and
// Cyrillic words are masked, but the substring "badwordy" is not). It works by
// tokenizing into word / non-word runs and replacing any word run that equals a
// filter word, which preserves the original spacing and handles adjacent filtered
// words. Multi-word filter entries are not matched (single words only). Returns
// content unchanged when there are no filters.
func censorWords(content string, filters []string) string {
	if len(filters) == 0 {
		return content
	}
	set := make(map[string]struct{}, len(filters))
	for _, w := range filters {
		if w != "" {
			set[strings.ToLower(w)] = struct{}{}
		}
	}
	if len(set) == 0 {
		return content
	}

	tokens := wordTokenizer.FindAllString(content, -1)
	for i, tok := range tokens {
		if _, ok := set[strings.ToLower(tok)]; ok {
			tokens[i] = "***"
		}
	}
	return strings.Join(tokens, "")
}
