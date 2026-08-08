package logging

import (
	"bytes"
	"testing"
)

// The status bar must repaint below every scrolling log line, and disappear
// cleanly on clear so the shell prompt is not left with a stale bar.
func TestStatusWriterKeepsBarBelowLogs(t *testing.T) {
	var buf bytes.Buffer
	w := &statusWriter{out: &buf, active: true}

	w.set("STATUS-1")
	if got := buf.String(); got != eraseLine+"STATUS-1" {
		t.Fatalf("set: got %q", got)
	}

	// A log write erases the bar, prints the line (scrolls), repaints the bar.
	buf.Reset()
	if _, err := w.Write([]byte("hello log\n")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != eraseLine+"hello log\n"+"STATUS-1" {
		t.Fatalf("write with bar: got %q", got)
	}

	// Clearing erases the bar and stops repainting it.
	buf.Reset()
	w.clear()
	if got := buf.String(); got != eraseLine {
		t.Fatalf("clear: got %q", got)
	}
	buf.Reset()
	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "after\n" {
		t.Fatalf("write after clear must be plain: got %q", got)
	}
}
