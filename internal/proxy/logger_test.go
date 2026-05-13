package proxy

import (
	"context"
	"log/slog"
	"testing"
)

type recordedLog struct {
	level slog.Level
	msg   string
}

type captureHandler struct {
	records *[]recordedLog
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	*h.records = append(*h.records, recordedLog{level: r.Level, msg: r.Message})
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func newCaptureLogger() (*slog.Logger, *[]recordedLog) {
	var records []recordedLog
	return slog.New(&captureHandler{records: &records}), &records
}

func TestGoproxyLogger_BenignDisconnectsDemotedToDebug(t *testing.T) {
	cases := []struct {
		name   string
		format string
		args   []any
	}{
		{
			name:   "broken pipe",
			format: "[%03d] WARN: Cannot write response from mitm'd client: write tcp 10.0.0.1:8080->10.0.0.2:443: write: broken pipe\n",
			args:   []any{123},
		},
		{
			name:   "connection reset",
			format: "[%03d] WARN: Cannot write response from mitm'd client: read tcp 10.0.0.1:8080->10.0.0.2:443: read: connection reset by peer\n",
			args:   []any{124},
		},
		{
			name:   "use of closed",
			format: "[%03d] WARN: Cannot read request from mitm'd client foo: use of closed network connection\n",
			args:   []any{125},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, records := newCaptureLogger()
			g := &goproxyLogger{l: logger}
			g.Printf(tc.format, tc.args...)
			if len(*records) != 1 {
				t.Fatalf("want 1 record, got %d", len(*records))
			}
			if got := (*records)[0].level; got != slog.LevelDebug {
				t.Fatalf("level: got %v, want Debug", got)
			}
		})
	}
}

func TestGoproxyLogger_RealWarningPreserved(t *testing.T) {
	logger, records := newCaptureLogger()
	g := &goproxyLogger{l: logger}
	g.Printf("[%03d] WARN: Cannot sign host certificate with provided CA: %s\n", 42, "boom")
	if len(*records) != 1 {
		t.Fatalf("want 1 record, got %d", len(*records))
	}
	if got := (*records)[0].level; got != slog.LevelWarn {
		t.Fatalf("level: got %v, want Warn", got)
	}
}

func TestGoproxyLogger_InfoGoesToDebug(t *testing.T) {
	logger, records := newCaptureLogger()
	g := &goproxyLogger{l: logger}
	g.Printf("[%03d] INFO: Sending request GET https://example.com\n", 7)
	if len(*records) != 1 {
		t.Fatalf("want 1 record, got %d", len(*records))
	}
	if got := (*records)[0].level; got != slog.LevelDebug {
		t.Fatalf("level: got %v, want Debug", got)
	}
}

func TestGoproxyLogger_UnprefixedGoesToDebug(t *testing.T) {
	logger, records := newCaptureLogger()
	g := &goproxyLogger{l: logger}
	g.Printf("plain message from goproxy\n")
	if len(*records) != 1 {
		t.Fatalf("want 1 record, got %d", len(*records))
	}
	if got := (*records)[0].level; got != slog.LevelDebug {
		t.Fatalf("level: got %v, want Debug", got)
	}
}

func TestGoproxyLogger_NilLoggerNoPanic(t *testing.T) {
	g := &goproxyLogger{l: nil}
	g.Printf("[001] WARN: anything\n")
}
