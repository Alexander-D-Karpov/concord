package netinfo

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[A-Za-z]`)

func visibleLen(s string) int {
	// Strip ANSI then count RUNES (not bytes).
	clean := ansiRE.ReplaceAllString(s, "")
	return utf8.RuneCountInString(clean)
}

func padVisibleRight(s string, width int) string {
	vis := visibleLen(s)
	if vis < width {
		s += strings.Repeat(" ", width-vis)
	}
	return s
}

func PrintAccessBanner(a Advertised, title string) {
	green := "\033[32m"
	yellow := "\033[33m"
	cyan := "\033[36m"
	reset := "\033[0m"
	bold := "\033[1m"

	publicLine := "Public: (none)"
	if a.PublicHost != "" {
		publicLine = fmt.Sprintf("Public: %sudp://%s:%d%s", green, a.PublicHost, a.Port, reset)
	}
	lanLine := fmt.Sprintf("LAN:    %sudp://%s:%d%s", cyan, a.LANHost, a.Port, reset)

	lines := []string{
		fmt.Sprintf("%s%s%s", bold, title, reset),
		publicLine,
		lanLine,
	}
	if a.Source != "" {
		lines = append(lines, fmt.Sprintf("Source: %s%s%s", yellow, a.Source, reset))
	}

	for _, n := range a.Notes {
		lines = append(lines, "Note: "+n)
	}
	if isLocalOnly(a.PublicHost) {
		lines = append(lines, "Note: Configured public host is loopback; it won't be reachable from the Internet.")
	}

	// Compute inner width from *visible* rune length.
	contentW := 0
	for _, l := range lines {
		if w := visibleLen(l); w > contentW {
			contentW = w
		}
	}

	inner := contentW + 2
	top := "╔" + strings.Repeat("═", inner) + "╗"
	sep := "╟" + strings.Repeat("─", inner) + "╢"
	bot := "╚" + strings.Repeat("═", inner) + "╝"

	fmt.Println(top)
	for i, l := range lines {
		if i == 1 {
			fmt.Println(sep)
		}
		fmt.Printf("║ %s ║\n", padVisibleRight(l, contentW))
	}
	fmt.Println(bot)
}

func isLocalOnly(h string) bool {
	h = strings.TrimSpace(strings.ToLower(h))
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "[::1]"
}
