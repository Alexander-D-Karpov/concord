package main

import (
	"strings"
	"testing"
)

// A frame renders as ceil(h/2) half-block rows, each a real block glyph that resets
// SGR state at the end so colors don't bleed into the surrounding TUI.
func TestRenderFrameHalfBlocksDims(t *testing.T) {
	f := &videoFrame{w: 4, h: 4, px: make([]byte, 16)}
	rows := renderFrameHalfBlocks(f)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows for h=4, got %d", len(rows))
	}
	for _, r := range rows {
		if !strings.Contains(r, "▀") { // ▀
			t.Fatalf("half-block row must contain the upper-half block: %q", r)
		}
		if !strings.HasSuffix(r, "\x1b[0m") {
			t.Fatalf("row must reset SGR at end: %q", r)
		}
	}
}

// Odd heights round up (the last row's bottom pixel is treated as background).
func TestRenderFrameHalfBlocksOddHeight(t *testing.T) {
	f := &videoFrame{w: 2, h: 3, px: make([]byte, 6)}
	if rows := renderFrameHalfBlocks(f); len(rows) != 2 {
		t.Fatalf("want 2 rows for h=3, got %d", len(rows))
	}
}

// The VU meter fills proportionally to RMS.
func TestRenderVU(t *testing.T) {
	if got := renderVU(0, 10); strings.Count(got, "█") != 0 {
		t.Fatalf("rms=0 must be empty bar: %q", got)
	}
	if got := renderVU(1, 10); strings.Count(got, "█") != 10 {
		t.Fatalf("rms=1 must fill width: %q", got)
	}
	if n := strings.Count(renderVU(0.5, 10), "█"); n < 4 || n > 6 {
		t.Fatalf("rms=0.5 should be ~half filled, got %d", n)
	}
}

// The waveform is exactly the requested width in runes.
func TestRenderWaveformWidth(t *testing.T) {
	pcm := make([]byte, audioFrameSamples*2)
	if n := len([]rune(renderWaveform(pcm, 32))); n != 32 {
		t.Fatalf("waveform width = %d runes, want 32", n)
	}
}
