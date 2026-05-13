package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// goproxyLogger adapts a slog.Logger to goproxy's Logger interface.
// Benign client-disconnect warnings are routed to Debug so they
// don't pollute normal logs; real warnings stay at Warn.
type goproxyLogger struct {
	l *slog.Logger
}

func (g *goproxyLogger) Printf(format string, v ...any) {
	if g.l == nil {
		return
	}
	msg := strings.TrimRight(fmt.Sprintf(format, v...), "\n")
	level := slog.LevelDebug
	if strings.Contains(msg, "WARN:") && !isBenignClientDisconnect(msg) {
		level = slog.LevelWarn
	}
	g.l.Log(context.Background(), level, msg, "source", "goproxy")
}

func isBenignClientDisconnect(msg string) bool {
	for _, s := range []string{
		"broken pipe",
		"connection reset by peer",
		"use of closed network connection",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
