// Package logging initializes the structured zap logger and adds a few Concord
// extras: a custom Trace level below debug, runtime level control over HTTP, and
// a sticky terminal status line.
//
// Init builds the logger from config. SetStatus/StatusEnabled render a live status
// bar, but only when stdout is a TTY — do not print directly to stdout while a
// status bar is active. The package keeps global mutable logger and level state;
// prefer passing the *zap.Logger down over reaching for the global L().
package logging
