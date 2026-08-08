package logging

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// contextKey is a private type for this package's context keys, preventing
// collisions with keys from other packages.
type contextKey string

const (
	// loggerKey stores a request-scoped *zap.Logger in the context.
	loggerKey contextKey = "logger"
	// requestIDKey stores the request ID string in the context.
	requestIDKey contextKey = "request_id"
)

// TraceLevel is a custom log level below zap's Debug (-2 vs -1), used for the
// most verbose "trace"/"vvv" logging.
const TraceLevel = zapcore.Level(-2)

var (
	// globalLogger is the process-wide logger set by Init; L and FromContext fall
	// back to it, and to a no-op logger before Init runs.
	globalLogger *zap.Logger
	// globalLevel is the shared atomic level gating all sinks; SetLevel and the
	// LevelHandler mutate it at runtime.
	globalLevel = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	// colorMsg records whether message-embedded ANSI color is safe (see
	// ColorEnabled); global mutable state set once by Init.
	colorMsg bool
)

// ColorEnabled reports whether ANSI color embedded in a log *message* will
// reach only an interactive terminal. It is true only for console format on a
// stdout TTY with NO_COLOR unset and no file sink — because a message string is
// shared across every sink, so a plain file/JSON sink would otherwise capture
// the escape codes. Use it to gate message-embedded color (e.g. the voice
// stats line); level-tag color is handled per-sink and needs no gating.
func ColorEnabled() bool { return colorMsg }

// isTerminal reports whether f is a character device (a terminal), so color is
// suppressed when output is redirected to a file or pipe. Dependency-free.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Global status-bar state, set once by Init and read by the Status* helpers.
var (
	statusBar    *statusWriter
	statusActive bool // a sticky bottom status bar is drawn (interactive console)
	statusColor  bool // color the bar text (statusActive && NO_COLOR unset)
)

// StatusEnabled reports whether a sticky bottom status bar is in use; callers
// should push live status via SetStatus instead of logging it as a line.
func StatusEnabled() bool { return statusActive }

// StatusColor reports whether the status bar text may carry ANSI color.
func StatusColor() bool { return statusColor }

// SetStatus repaints the sticky bottom status bar in place (no-op off a TTY).
func SetStatus(s string) {
	if statusBar != nil {
		statusBar.set(s)
	}
}

// ClearStatus erases the status bar so shutdown output and the shell prompt
// return cleanly. Safe to call when no bar is active.
func ClearStatus() {
	if statusBar != nil {
		statusBar.clear()
	}
}

// statusWriter keeps a single-line status bar pinned to the bottom of a
// terminal while normal log lines scroll above it. Each log Write erases the
// bar, emits the line (which scrolls), then repaints the bar; set() repaints it
// in place. One mutex serializes all three so terminal control never interleaves.
type statusWriter struct {
	mu     sync.Mutex
	out    io.Writer
	status string
	active bool
}

// eraseLine returns cursor to column 0 and clears to end of line.
const eraseLine = "\r\x1b[K"

func (w *statusWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active && w.status != "" {
		_, _ = io.WriteString(w.out, eraseLine)
		n, err := w.out.Write(p)
		_, _ = io.WriteString(w.out, w.status)
		return n, err
	}
	return w.out.Write(p)
}

// Sync is a no-op: stdout is unbuffered and Sync on a terminal often errors.
func (w *statusWriter) Sync() error { return nil }

// set replaces the bar text and, when active, repaints it in place without
// scrolling any log lines.
func (w *statusWriter) set(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = s
	if w.active {
		_, _ = io.WriteString(w.out, eraseLine+s)
	}
}

// clear erases the currently drawn bar and forgets its text, so later log lines
// print without a stale bar beneath them.
func (w *statusWriter) clear() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active && w.status != "" {
		_, _ = io.WriteString(w.out, eraseLine)
	}
	w.status = ""
}

// ParseLevel maps a level name to a zapcore.Level, accepting the verbosity
// aliases "vvv"/"trace", "vv"/"debug", and "warning". Unknown values fall back
// to InfoLevel.
func ParseLevel(level string) zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace", "vvv":
		return TraceLevel
	case "debug", "vv":
		return zapcore.DebugLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	case "fatal":
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}

// encodeLevel is the default (uncolored) level encoder, rendering the custom
// TraceLevel as "trace" and delegating standard levels to zap's lowercase
// encoder.
func encodeLevel(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	if l == TraceLevel {
		enc.AppendString("trace")
		return
	}
	zapcore.LowercaseLevelEncoder(l, enc)
}

// encodeLevelColor is encodeLevel with ANSI color, used only for an interactive
// stdout sink (warn=yellow, error=red stand out at a glance).
func encodeLevelColor(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	if l == TraceLevel {
		enc.AppendString("\x1b[35mtrace\x1b[0m") // magenta, like the debug family
		return
	}
	zapcore.LowercaseColorLevelEncoder(l, enc)
}

// Init builds the process logger and installs it as the global logger, returning
// it. It configures the level, a console or JSON encoder, and up to two sinks
// (stdout and, if enableFile, an appended file at filePath). It also decides,
// from TTY/NO_COLOR/format/sink combination, whether message-embedded color and
// the sticky status bar are safe, recording that in package globals. Call once
// at startup; it mutates global state and is not safe to call concurrently.
func Init(level, format, output string, enableFile bool, filePath string) (*zap.Logger, error) {
	globalLevel.SetLevel(ParseLevel(level))

	baseCfg := zap.NewProductionEncoderConfig()
	baseCfg.TimeKey = "timestamp"
	baseCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	baseCfg.EncodeLevel = encodeLevel

	isJSON := format == "json"
	// newEncoder builds a sink encoder; colorLevel colors only the level tag and
	// is enabled per-sink (never for files/JSON).
	newEncoder := func(colorLevel bool) zapcore.Encoder {
		cfg := baseCfg
		if colorLevel {
			cfg.EncodeLevel = encodeLevelColor
		}
		if isJSON {
			return zapcore.NewJSONEncoder(cfg)
		}
		return zapcore.NewConsoleEncoder(cfg)
	}

	stdoutSink := output == "stdout" || output == ""
	fileSink := enableFile && filePath != ""
	tty := isTerminal(os.Stdout)
	noColor := os.Getenv("NO_COLOR") != ""
	colorStdout := stdoutSink && !isJSON && tty && !noColor
	// Message-embedded ANSI is safe only when the interactive stdout is the sole
	// sink (a file/JSON sink shares the same message string).
	colorMsg = colorStdout && !fileSink
	// A sticky bottom status bar needs an interactive console; it writes only to
	// stdout, so it is fine alongside a plain file sink.
	statusActive = stdoutSink && !isJSON && tty
	statusColor = statusActive && !noColor

	var cores []zapcore.Core

	if stdoutSink {
		var ws zapcore.WriteSyncer = zapcore.AddSync(os.Stdout)
		if statusActive {
			// Route stdout through the bar so every log line lands above it.
			statusBar = &statusWriter{out: os.Stdout, active: true}
			ws = zapcore.AddSync(statusBar)
		}
		cores = append(cores, zapcore.NewCore(newEncoder(colorStdout), ws, globalLevel))
	}

	if fileSink {
		file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(
			newEncoder(false),
			zapcore.AddSync(file),
			globalLevel,
		))
	}

	core := zapcore.NewTee(cores...)
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	globalLogger = logger
	return logger, nil
}

// SetLevel changes the shared minimum log level at runtime; it affects every
// sink and every logger derived from the global level.
func SetLevel(level zapcore.Level) {
	globalLevel.SetLevel(level)
}

// Level returns the current global minimum log level.
func Level() zapcore.Level {
	return globalLevel.Level()
}

// Enabled reports whether the given level would currently be logged.
func Enabled(level zapcore.Level) bool {
	return globalLevel.Enabled(level)
}

// TraceEnabled reports whether TraceLevel logging is currently active, letting
// callers skip building expensive trace fields.
func TraceEnabled() bool {
	return globalLevel.Enabled(TraceLevel)
}

// Trace logs msg at the custom TraceLevel. If logger is nil it uses the global
// logger (L). zap has no Trace method, so this wraps logger.Log.
func Trace(logger *zap.Logger, msg string, fields ...zap.Field) {
	if logger == nil {
		logger = L()
	}
	logger.Log(TraceLevel, msg, fields...)
}

// LevelHandler returns an http.Handler (zap's AtomicLevel) that serves the
// current level on GET and updates it on PUT, exposing runtime level control
// over HTTP.
func LevelHandler() http.Handler {
	return globalLevel
}

// FromContext returns the request-scoped logger stored via WithLogger, falling
// back to the global logger and then a no-op logger. Never returns nil.
func FromContext(ctx context.Context) *zap.Logger {
	if logger, ok := ctx.Value(loggerKey).(*zap.Logger); ok {
		return logger
	}
	if globalLogger != nil {
		return globalLogger
	}
	return zap.NewNop()
}

// WithLogger returns a child context carrying logger, retrievable via
// FromContext.
func WithLogger(ctx context.Context, logger *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// WithRequestID returns a child context whose logger has a "request_id" field
// attached, so every log line from that context is correlated by request. Note
// it stores the annotated logger, not the raw ID under requestIDKey.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	logger := FromContext(ctx).With(zap.String("request_id", requestID))
	return WithLogger(ctx, logger)
}

// GetRequestID returns the request ID stored under requestIDKey, or "" if none
// is present.
func GetRequestID(ctx context.Context) string {
	if requestID, ok := ctx.Value(requestIDKey).(string); ok {
		return requestID
	}
	return ""
}

// L returns the global logger, or a no-op logger if Init has not run yet.
func L() *zap.Logger {
	if globalLogger != nil {
		return globalLogger
	}
	return zap.NewNop()
}
