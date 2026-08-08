package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// ---------------------------------------------------------------------------
// Terminal renderers for the monitored client's live media.
//
// These are pure string producers so they are deterministic and unit-testable; the
// TUI just places their output. Video uses ANSI 24-bit half-blocks (one character
// cell = two vertically-stacked pixels), audio uses block sparklines / a VU bar.
// ---------------------------------------------------------------------------

// renderFrameHalfBlocks renders a videoFrame as ceil(h/2) rows of upper-half blocks
// (▀): the glyph's foreground is the top pixel, background the bottom pixel, so each
// character cell shows two vertical pixels at full 24-bit color.
func renderFrameHalfBlocks(f *videoFrame) []string {
	if f == nil || f.w <= 0 || f.h <= 0 {
		return nil
	}
	rows := (f.h + 1) / 2
	out := make([]string, rows)
	var sb strings.Builder
	for r := 0; r < rows; r++ {
		sb.Reset()
		yTop := r * 2
		yBot := yTop + 1
		for x := 0; x < f.w; x++ {
			top := palette16[f.px[yTop*f.w+x]&0xF]
			bot := [3]uint8{0, 0, 0}
			if yBot < f.h {
				bot = palette16[f.px[yBot*f.w+x]&0xF]
			}
			fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀",
				top[0], top[1], top[2], bot[0], bot[1], bot[2])
		}
		sb.WriteString("\x1b[0m")
		out[r] = sb.String()
	}
	return out
}

// waveformBlocks maps an amplitude bucket 0..8 to a rising block glyph.
var waveformBlocks = []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// renderWaveform draws s16le PCM as a width-column block sparkline, each column the
// peak of the samples that fall in it.
func renderWaveform(pcm []byte, width int) string {
	if width <= 0 {
		return ""
	}
	n := len(pcm) / 2
	if n == 0 {
		return strings.Repeat(" ", width)
	}
	per := n / width
	if per < 1 {
		per = 1
	}
	var sb strings.Builder
	for c := 0; c < width; c++ {
		start := c * per
		var peak float64
		for i := start; i < start+per && i < n; i++ {
			a := math.Abs(float64(int16(binary.LittleEndian.Uint16(pcm[i*2:]))) / 32768.0)
			if a > peak {
				peak = a
			}
		}
		idx := int(peak*float64(len(waveformBlocks)-1) + 0.5)
		if idx > len(waveformBlocks)-1 {
			idx = len(waveformBlocks) - 1
		}
		sb.WriteRune(waveformBlocks[idx])
	}
	return sb.String()
}

// renderVU returns a width-wide horizontal meter filled proportionally to rms (0..1).
func renderVU(rms float64, width int) string {
	if width <= 0 {
		return ""
	}
	if rms < 0 {
		rms = 0
	}
	fill := int(rms*float64(width) + 0.5)
	if fill > width {
		fill = width
	}
	return strings.Repeat("█", fill) + strings.Repeat("·", width-fill)
}
