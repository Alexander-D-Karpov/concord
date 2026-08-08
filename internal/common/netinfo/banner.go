package netinfo

import (
	"fmt"
)

// PrintAccessBanner writes a boxed, human-readable summary of the advertised
// access addresses (public/LAN endpoints, detection source, and any notes) to
// stdout at startup, so an operator can see how to reach the service.
func PrintAccessBanner(a Advertised, serviceName string) {
	fmt.Println("╔══════════════════════════════════════════════════════════════════════════╗")
	fmt.Printf("║ %-72s ║\n", serviceName)
	fmt.Println("╟──────────────────────────────────────────────────────────────────────────╢")
	if a.PublicHost != "" {
		fmt.Printf("║ Public: udp://%-58s ║\n", fmt.Sprintf("%s:%d", a.PublicHost, a.Port))
	}
	if a.LANHost != "" {
		fmt.Printf("║ LAN:    udp://%-58s ║\n", fmt.Sprintf("%s:%d", a.LANHost, a.Port))
	}
	fmt.Printf("║ Source: %-64s ║\n", a.Source)
	for _, note := range a.Notes {
		wrapped := wrapText(note, 70)
		for _, line := range wrapped {
			fmt.Printf("║ Note: %-66s ║\n", line)
		}
	}
	fmt.Println("╚══════════════════════════════════════════════════════════════════════════╝")
}

// wrapText hard-wraps text into lines of at most width runes so long notes fit
// inside the banner box. It splits purely by byte length (no word boundaries).
func wrapText(text string, width int) []string {
	if len(text) <= width {
		return []string{text}
	}
	var lines []string
	for len(text) > width {
		lines = append(lines, text[:width])
		text = text[width:]
	}
	if len(text) > 0 {
		lines = append(lines, text)
	}
	return lines
}
