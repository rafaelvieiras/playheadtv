// Package logging is the backend's leveled log. It uses the same level names as
// the client (error, warn, info, debug, trace) and is selected with the
// PLAYHEADTV_LOG_LEVEL environment variable.
//
// With the variable unset nothing changes: every message is written through the
// standard library logger exactly as before (error, warn and info alike; debug
// and trace are dropped). With it set, messages are filtered by level and
// written by a structured (slog text) handler on stderr.
package logging

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
)

// Level is a severity, most severe first.
type Level int

const (
	LevelError Level = iota
	LevelWarn
	LevelInfo
	LevelDebug
	LevelTrace
)

// EnvVar is the environment variable that selects the level.
const EnvVar = "PLAYHEADTV_LOG_LEVEL"

var levelNames = []string{"error", "warn", "info", "debug", "trace"}

func (l Level) String() string {
	if l < LevelError || l > LevelTrace {
		return "unknown"
	}
	return levelNames[l]
}

// ParseLevel reads a level name, case-insensitively and ignoring surrounding
// spaces. ok is false for anything that is not a level name.
func ParseLevel(s string) (level Level, ok bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	for i, n := range levelNames {
		if n == t {
			return Level(i), true
		}
	}
	return LevelInfo, false
}

// slogTrace is slog's level for trace (below debug).
const slogTrace = slog.LevelDebug - 4

func (l Level) slog() slog.Level {
	switch l {
	case LevelError:
		return slog.LevelError
	case LevelWarn:
		return slog.LevelWarn
	case LevelInfo:
		return slog.LevelInfo
	case LevelDebug:
		return slog.LevelDebug
	default:
		return slogTrace
	}
}

var (
	configured atomic.Bool
	level      atomic.Int32
	logger     atomic.Pointer[slog.Logger]
)

// Setup applies the value of PLAYHEADTV_LOG_LEVEL, writing to w (stderr when
// nil). An empty value leaves the logger as it was (today's output). An
// invalid value is reported through note and also leaves it as it was.
func Setup(value string, w io.Writer) (note string) {
	if strings.TrimSpace(value) == "" {
		reset()
		return ""
	}
	l, ok := ParseLevel(value)
	if !ok {
		reset()
		return fmt.Sprintf("ignoring invalid %s %q (want one of: %s)", EnvVar, value, strings.Join(levelNames, ", "))
	}
	if w == nil {
		w = os.Stderr
	}
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: slogTrace})
	logger.Store(slog.New(handler))
	level.Store(int32(l))
	configured.Store(true)
	return ""
}

func reset() {
	configured.Store(false)
	logger.Store(nil)
}

// Configured reports whether a level was set, and which.
func Configured() (Level, bool) {
	return Level(level.Load()), configured.Load()
}

// Enabled reports whether a message at l would be written.
func Enabled(l Level) bool {
	if !configured.Load() {
		return l <= LevelInfo
	}
	return l <= Level(level.Load())
}

func logf(l Level, format string, args ...any) {
	if !configured.Load() {
		if l <= LevelInfo {
			log.Printf(format, args...)
		}
		return
	}
	if l > Level(level.Load()) {
		return
	}
	if lg := logger.Load(); lg != nil {
		lg.Log(context.Background(), l.slog(), fmt.Sprintf(format, args...))
	}
}

// Errorf logs a failure.
func Errorf(format string, args ...any) { logf(LevelError, format, args...) }

// Warnf logs something unexpected that was handled.
func Warnf(format string, args ...any) { logf(LevelWarn, format, args...) }

// Infof logs a lifecycle summary.
func Infof(format string, args ...any) { logf(LevelInfo, format, args...) }

// Debugf logs step-by-step detail. Dropped unless the level is set to debug or trace.
func Debugf(format string, args ...any) { logf(LevelDebug, format, args...) }

// Tracef logs very chatty detail. Dropped unless the level is set to trace.
func Tracef(format string, args ...any) { logf(LevelTrace, format, args...) }
