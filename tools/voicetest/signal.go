package main

import (
	"encoding/binary"
	"fmt"
	"math"
)

// ---------------------------------------------------------------------------
// Synthetic but REAL media signals.
//
// The publisher bot emits these through the real encrypt→relay path so the
// monitor can decrypt and render actual moving content — not fake random bytes.
// Everything is sized to fit one UDP payload (protocol.MaxUDPPayload = 1200) so no
// fragmentation is needed, and uses no native/cgo codecs.
// ---------------------------------------------------------------------------

// --- Audio: 16 kHz mono s16le PCM tone ---

const (
	audioSampleRate   = 16000                                 // synthetic tone sample rate (Hz)
	audioFrameMs      = 20                                    // samples-per-frame time base (ms)
	audioFrameSamples = audioSampleRate * audioFrameMs / 1000 // 320 samples → 640 bytes per frame
)

// toneSource generates a continuous tone whose frequency slowly sweeps and whose
// amplitude has a tremolo envelope, as 20ms s16le PCM frames. Phase is carried
// across frames so there are no clicks; it is stateful, so call frame() in order.
type toneSource struct{ phase float64 }

// frame returns the n-th 20ms s16le PCM frame, advancing the carried phase so
// successive frames join without clicks. n drives the slow frequency sweep and tremolo,
// so it should increase monotonically across calls.
func (t *toneSource) frame(n int) []byte {
	buf := make([]byte, audioFrameSamples*2)
	// Frequency sweeps ~220..660 Hz; amplitude breathes 0.3..0.8 so the VU meter
	// visibly rises and falls.
	freq := 220.0 + 220.0*(0.5+0.5*math.Sin(float64(n)*0.02))
	amp := 0.3 + 0.5*(0.5+0.5*math.Sin(float64(n)*0.05))
	step := 2 * math.Pi * freq / audioSampleRate
	for i := 0; i < audioFrameSamples; i++ {
		t.phase += step
		if t.phase > 2*math.Pi {
			t.phase -= 2 * math.Pi
		}
		v := int16(amp * math.Sin(t.phase) * 32767)
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	return buf
}

// pcmRMS returns the RMS amplitude (0..1) of an s16le buffer, for the VU meter.
func pcmRMS(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		s := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		f := float64(s) / 32768.0
		sum += f * f
	}
	return math.Sqrt(sum / float64(n))
}

// --- Video: 48x27 16-color palette-indexed animated pattern ---

const (
	videoW = 48 // synthetic video frame width in pixels
	videoH = 27 // synthetic video frame height in pixels
)

// palette16 is a fixed 16-entry RGB palette shared by sender and monitor (indices
// travel on the wire; the palette is compiled in on both ends).
var palette16 = [16][3]uint8{
	{0, 0, 0}, {40, 40, 40}, {128, 128, 128}, {235, 235, 235},
	{220, 40, 40}, {235, 130, 30}, {235, 220, 40}, {150, 220, 40},
	{40, 200, 60}, {40, 200, 160}, {40, 200, 235}, {40, 110, 235},
	{90, 40, 220}, {200, 40, 220}, {235, 90, 180}, {150, 90, 40},
}

// videoFrame holds palette indices (0..15), row-major, len w*h.
type videoFrame struct {
	w, h int
	px   []byte
}

// videoSource renders an animated test pattern: moving vertical rainbow bars with a
// bouncing white box, plus a frame marker packed into the top-left 4 pixels so the
// monitor can prove end-to-end integrity of the decoded content.
type videoSource struct{}

// frame renders the n-th animated test frame: scrolling rainbow bars, a bouncing white
// box, and a 16-bit integrity marker packed into the top-left pixels (written last so
// the pattern can't overwrite it). n is the frame index driving the animation.
func (v *videoSource) frame(n int) *videoFrame {
	px := make([]byte, videoW*videoH)
	for y := 0; y < videoH; y++ {
		for x := 0; x < videoW; x++ {
			px[y*videoW+x] = byte(((x / 3) + (n / 2)) % 16)
		}
	}
	// bouncing white box
	const bw, bh = 10, 6
	bx := bounce(n, videoW-bw)
	by := bounce(n/2, videoH-bh)
	for y := by; y < by+bh && y < videoH; y++ {
		for x := bx; x < bx+bw && x < videoW; x++ {
			px[y*videoW+x] = 3
		}
	}
	// integrity marker (written last so the pattern never clobbers it)
	writeMarker(px, n)
	return &videoFrame{w: videoW, h: videoH, px: px}
}

// writeMarker packs the low 16 bits of n into the first four palette-index pixels (4
// bits each), so a decoded frame can be matched back to the frame the publisher sent.
func writeMarker(px []byte, n int) {
	m := n & 0xFFFF
	px[0] = byte((m >> 12) & 0xF)
	px[1] = byte((m >> 8) & 0xF)
	px[2] = byte((m >> 4) & 0xF)
	px[3] = byte(m & 0xF)
}

// frameMarker reads the 16-bit frame marker from a decoded frame (-1 if too small).
func frameMarker(f *videoFrame) int {
	if f == nil || len(f.px) < 4 {
		return -1
	}
	return int(f.px[0])<<12 | int(f.px[1])<<8 | int(f.px[2])<<4 | int(f.px[3])
}

// bounce maps a monotonically increasing n to a value that ping-pongs across [0, span]
// (triangle wave), used to animate the box position. Returns 0 for a non-positive span.
func bounce(n, span int) int {
	if span <= 0 {
		return 0
	}
	p := n % (2 * span)
	if p < span {
		return p
	}
	return 2*span - p
}

// encodeFrame packs a frame to wire bytes: [w][h] then 4-bit palette indices, two
// pixels per byte.
func encodeFrame(f *videoFrame) []byte {
	out := make([]byte, 2+(len(f.px)+1)/2)
	out[0] = byte(f.w)
	out[1] = byte(f.h)
	for i, p := range f.px {
		if i%2 == 0 {
			out[2+i/2] = (p & 0xF) << 4
		} else {
			out[2+i/2] |= p & 0xF
		}
	}
	return out
}

// decodeFrame is the inverse of encodeFrame, rejecting malformed/truncated input.
func decodeFrame(b []byte) (*videoFrame, error) {
	if len(b) < 2 {
		return nil, fmt.Errorf("frame too short: %d bytes", len(b))
	}
	w, h := int(b[0]), int(b[1])
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("bad frame dims %dx%d", w, h)
	}
	n := w * h
	need := 2 + (n+1)/2
	if len(b) < need {
		return nil, fmt.Errorf("truncated frame: need %d bytes, have %d", need, len(b))
	}
	px := make([]byte, n)
	for i := 0; i < n; i++ {
		by := b[2+i/2]
		if i%2 == 0 {
			px[i] = by >> 4
		} else {
			px[i] = by & 0xF
		}
	}
	return &videoFrame{w: w, h: h, px: px}, nil
}
