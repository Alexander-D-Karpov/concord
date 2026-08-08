package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The full TUI View() path — lipgloss box composition (styBox, JoinHorizontal,
// JoinVertical) wrapping the ANSI half-block video — must render without panicking
// and composite the media inside the layout. The low-level renderer tests and the
// render-dump both BYPASS this path (dumpRender never calls View), so this is the
// only coverage of lipgloss's ANSI-aware width handling of the half-block cells.
func TestTUIViewRendersMediaPanel(t *testing.T) {
	st := &stats{}
	st.bytesOut.Store(123456)
	st.bytesIn.Store(654321)
	st.audioSent.Store(100)
	st.videoRecv.Store(50)

	tap := &mediaTap{}
	tap.putVideo((&videoSource{}).frame(3))
	tap.putAudio((&toneSource{}).frame(0))

	m := newTUIModel(st, tap, tuiConfig{
		clients: 3, video: true, fastJoin: true,
		duration: 10 * time.Second, publisherIdx: 0, monitorIdx: 1,
	})
	// Drive one tick so throughput sampling runs, then render.
	nm, _ := m.Update(tickMsg(time.Now()))
	view := nm.(tuiModel).View()

	if strings.TrimSpace(view) == "" {
		t.Fatal("View() produced empty output")
	}
	for _, want := range []string{"concord voicetest", "transport", "monitor", "▀"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q", want)
		}
	}
	// The 48-wide video frame must be composited (many half-block cells present).
	if n := strings.Count(view, "▀"); n < videoW {
		t.Fatalf("View() has too few half-blocks (%d < %d): video not composited", n, videoW)
	}

	// Optional eyeball dump for manual alignment inspection.
	if p := os.Getenv("VOICETEST_VIEW_DUMP"); p != "" {
		_ = os.WriteFile(p, []byte(view), 0o644)
	}
}

// With no monitor (e.g. a single client, tap == nil) the View must still render the
// stats panel rather than panicking on a nil tap.
func TestTUIViewNoMonitor(t *testing.T) {
	m := newTUIModel(&stats{}, nil, tuiConfig{clients: 1})
	if strings.TrimSpace(m.View()) == "" {
		t.Fatal("View() with no tap should still render the stats panel")
	}
}
