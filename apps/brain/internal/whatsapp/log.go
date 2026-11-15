package whatsapp

import (
	"context"
	"fmt"
	"log/slog"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// Logger is whatsmeow's logging through the brain's logger. Its info lines
// are the library's chatter, so they are the brain's debug.
func Logger(l *slog.Logger) waLog.Logger { return walog{l: l} }

type walog struct{ l *slog.Logger }

func (w walog) Warnf(msg string, args ...any)  { w.log(slog.LevelWarn, msg, args) }
func (w walog) Errorf(msg string, args ...any) { w.log(slog.LevelError, msg, args) }
func (w walog) Infof(msg string, args ...any)  { w.log(slog.LevelDebug, msg, args) }
func (w walog) Debugf(msg string, args ...any) { w.log(slog.LevelDebug, msg, args) }

func (w walog) Sub(module string) waLog.Logger { return walog{l: w.l.With("module", module)} }

func (w walog) log(level slog.Level, msg string, args []any) {
	if !w.l.Enabled(context.Background(), level) {
		return
	}
	w.l.Log(context.Background(), level, "whatsmeow: "+fmt.Sprintf(msg, args...))
}
