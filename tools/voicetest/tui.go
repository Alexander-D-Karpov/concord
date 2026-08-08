package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Live TUI dashboard (Bubble Tea) for the throughput harness. Aggregate transport
// stats on the left; the monitored publisher's decoded audio+video on the right.
//
// Rendering pulls from the shared *stats counters and the monitor's *mediaTap, so
// the TUI has no coupling to the bot internals — it just displays snapshots.
// ---------------------------------------------------------------------------

// tuiConfig carries the run parameters shown in the dashboard header and used to label
// the media panel (client count, video/fast-join modes, duration, publisher/monitor
// indices).
type tuiConfig struct {
	clients      int
	video        bool
	fastJoin     bool
	duration     time.Duration
	publisherIdx int
	monitorIdx   int
}

// tickMsg is the Bubble Tea message delivered on each render tick.
type tickMsg time.Time

// tuiTick schedules the next 100ms refresh tick.
func tuiTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// tuiModel is the Bubble Tea model: it holds references to the shared stats and media
// tap plus the throughput sampling state (last byte counts and derived kbps) used to
// render the live dashboard.
type tuiModel struct {
	st  *stats
	tap *mediaTap
	cfg tuiConfig

	start      time.Time
	lastSample time.Time
	lastOut    uint64
	lastIn     uint64
	kbpsOut    float64
	kbpsIn     float64
	w, h       int
	quitting   bool
}

// newTUIModel builds the dashboard model bound to the shared stats and (optional)
// media tap, seeding the throughput-sampling timestamps to now.
func newTUIModel(st *stats, tap *mediaTap, cfg tuiConfig) tuiModel {
	now := time.Now()
	return tuiModel{st: st, tap: tap, cfg: cfg, start: now, lastSample: now}
}

// Init starts the periodic refresh tick.
func (m tuiModel) Init() tea.Cmd { return tuiTick() }

// Update handles Bubble Tea messages: q/ctrl+c/esc quit, window-size updates track the
// viewport, and each tick recomputes in/out kbps from the byte deltas (ignoring
// sub-50ms deltas so the rate doesn't spike) before scheduling the next tick.
func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tickMsg:
		now := time.Time(msg)
		dt := now.Sub(m.lastSample).Seconds()
		if dt >= 0.05 { // ignore sub-tick deltas so throughput doesn't spike
			out, in := m.st.bytesOut.Load(), m.st.bytesIn.Load()
			m.kbpsOut = float64(out-m.lastOut) * 8 / 1000 / dt
			m.kbpsIn = float64(in-m.lastIn) * 8 / 1000 / dt
			m.lastOut, m.lastIn = out, in
			m.lastSample = now
		}
		return m, tuiTick()
	}
	return m, nil
}

// Lipgloss styles for the dashboard: titles, boxed panels, labels/values, and
// OK/warning/footer accents.
var (
	styTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51"))
	styBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	styLabel  = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	styVal    = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	styOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styFooter = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// View renders the full frame: a header with run parameters and elapsed time, the
// transport stats panel, the monitor's media panel beside it when a tap is present, and
// a quit-hint footer. Returns empty once quitting so the alt-screen restores cleanly.
func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	elapsed := time.Since(m.start).Truncate(time.Second)
	header := styTitle.Render("concord voicetest") + styLabel.Render(
		fmt.Sprintf("  clients=%d  video=%s  %s  elapsed %s/%s",
			m.cfg.clients, onOff(m.cfg.video), fastJoinLabel(m.cfg.fastJoin),
			elapsed, m.cfg.duration))

	stats := styBox.Render(m.statsPanel())
	var body string
	if m.tap != nil {
		media := styBox.Render(mediaPanel(m.tap.snapshot(), m.cfg))
		body = lipgloss.JoinHorizontal(lipgloss.Top, stats, "  ", media)
	} else {
		body = stats
	}
	footer := styFooter.Render("press q to quit")
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, footer)
}

// kv formats one label/value line with the label left-padded to a fixed width for
// column alignment.
func kv(label, val string) string {
	return styLabel.Render(fmt.Sprintf("%-9s", label)) + styVal.Render(val)
}

// statsPanel renders the transport counters (audio/video tx-rx, byte totals and live
// kbps, welcomes, pongs, bitrate hints) with errors highlighted when non-zero.
func (m tuiModel) statsPanel() string {
	s := m.st
	errStyle := styOK
	if s.errors.Load() > 0 {
		errStyle = styWarn
	}
	lines := []string{
		styTitle.Render("transport"),
		kv("audio", fmt.Sprintf("tx %-7d rx %-7d", s.audioSent.Load(), s.audioRecv.Load())),
		kv("video", fmt.Sprintf("tx %-7d rx %-7d", s.videoSent.Load(), s.videoRecv.Load())),
		kv("out", fmt.Sprintf("%-8s %6.0f kbps", humanBytes(s.bytesOut.Load()), m.kbpsOut)),
		kv("in", fmt.Sprintf("%-8s %6.0f kbps", humanBytes(s.bytesIn.Load()), m.kbpsIn)),
		kv("welcomes", fmt.Sprintf("%d", s.welcomeOK.Load())),
		kv("pongs", fmt.Sprintf("%d", s.pongRecv.Load())),
		kv("br hints", fmt.Sprintf("%d (last %d)", s.bitrateHints.Load(), s.lastBitrate.Load())),
		styLabel.Render(fmt.Sprintf("%-9s", "errors")) + errStyle.Render(fmt.Sprintf("%d", s.errors.Load())),
	}
	return strings.Join(lines, "\n")
}

// mediaPanel renders the monitored publisher's decoded media: the live video image
// (half-blocks), an audio waveform + VU meter, and decode stats. Shared by the TUI
// and the headless render-dump.
func mediaPanel(snap mediaSnapshot, cfg tuiConfig) string {
	var b strings.Builder
	b.WriteString(styTitle.Render(fmt.Sprintf("monitor ← bot%d media", cfg.publisherIdx)) + "\n")

	if snap.video != nil {
		for _, row := range renderFrameHalfBlocks(snap.video) {
			b.WriteString(row + "\n")
		}
	} else if cfg.video {
		b.WriteString(styLabel.Render("(waiting for video…)") + "\n")
	}

	rms := pcmRMS(snap.audioPCM)
	b.WriteString(styLabel.Render("vu   ") + vuColor(rms) + "\n")
	b.WriteString(styLabel.Render("wave ") + renderWaveform(snap.audioPCM, videoW-5) + "\n")

	res := "—"
	if snap.video != nil {
		res = fmt.Sprintf("%dx%d", snap.video.w, snap.video.h)
	}
	b.WriteString(styLabel.Render(fmt.Sprintf(
		"decoded  video=%d audio=%d res=%s marker=%d errs=%d",
		snap.videoFrames, snap.audioFrames, res, snap.lastMarker, snap.decodeErrs)))
	return b.String()
}

// vuColor renders the VU meter for the given RMS level, coloring it as a warning once
// the level exceeds 0.7 (near clipping).
func vuColor(rms float64) string {
	bar := renderVU(rms, videoW-5)
	st := styOK
	if rms > 0.7 {
		st = styWarn
	}
	return st.Render(bar)
}

// onOff renders a boolean as "on"/"off" for the header.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// fastJoinLabel describes the join mode in the header: fast-join (debug) versus the
// real membership path.
func fastJoinLabel(fast bool) string {
	if fast {
		return "fast-join(VOICE_DEBUG)"
	}
	return "membership-join"
}

// humanBytes formats a byte count with a binary (KB/MB/GB) unit suffix.
func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// asciiRamp maps palette luminance to a character ramp, so a frame can be rendered
// as plain ASCII for headless verification (no color codes to eyeball in a log).
const asciiRamp = " .:-=+*#%@"

// asciiFrame renders a frame as one string per row using the luminance ramp, so the
// image is legible in a plain log or dump without ANSI color.
func asciiFrame(f *videoFrame) []string {
	if f == nil {
		return nil
	}
	out := make([]string, f.h)
	for y := 0; y < f.h; y++ {
		var sb strings.Builder
		for x := 0; x < f.w; x++ {
			c := palette16[f.px[y*f.w+x]&0xF]
			lum := (int(c[0]) + int(c[1]) + int(c[2])) / 3
			idx := lum * (len(asciiRamp) - 1) / 255
			sb.WriteByte(asciiRamp[idx])
		}
		out[y] = sb.String()
	}
	return out
}

// dumpRender writes a plain-text snapshot of the monitor's media (ANSI half-blocks,
// an ASCII-luminance view, and decode stats) to path. Used for headless verification
// of the end-to-end media path when no interactive terminal is available.
func dumpRender(path string, snap mediaSnapshot, cfg tuiConfig) error {
	var b strings.Builder
	fmt.Fprintf(&b, "== voicetest render dump ==\n")
	fmt.Fprintf(&b, "decoded video=%d audio=%d marker=%d errs=%d rms=%.3f\n",
		snap.videoFrames, snap.audioFrames, snap.lastMarker, snap.decodeErrs, pcmRMS(snap.audioPCM))
	if snap.video != nil {
		fmt.Fprintf(&b, "resolution=%dx%d\n\n[ascii-luminance]\n", snap.video.w, snap.video.h)
		for _, row := range asciiFrame(snap.video) {
			b.WriteString(row + "\n")
		}
		b.WriteString("\n[ansi-halfblocks]\n")
		for _, row := range renderFrameHalfBlocks(snap.video) {
			b.WriteString(row + "\n")
		}
	} else {
		b.WriteString("(no video decoded yet)\n")
	}
	b.WriteString("\n[audio waveform]\n" + renderWaveform(snap.audioPCM, 64) + "\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
