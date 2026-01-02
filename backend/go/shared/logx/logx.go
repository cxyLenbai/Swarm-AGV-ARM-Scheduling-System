package logx

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type Logger struct {
	slog *slog.Logger
	env  string
}

func New(service string, env string, version string, level string) Logger {
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey:
				a.Key = "ts"
			case slog.LevelKey:
				a.Key = "level"
			case slog.MessageKey:
				a.Key = "event"
			}
			return a
		},
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)
	base := slog.New(handler).With(
		slog.String("service", service),
		slog.String("env", env),
	)
	if strings.TrimSpace(version) != "" {
		base = base.With(slog.String("version", strings.TrimSpace(version)))
	}

	return Logger{slog: base, env: env}
}

func (l Logger) Info(ctx context.Context, event string, msg string, attrs ...slog.Attr) {
	attrs = append(attrs, slog.String("msg", msg))
	l.slog.LogAttrs(ctx, slog.LevelInfo, event, attrs...)
}

func (l Logger) Warn(ctx context.Context, event string, msg string, attrs ...slog.Attr) {
	attrs = append(attrs, slog.String("msg", msg))
	l.slog.LogAttrs(ctx, slog.LevelWarn, event, attrs...)
}

func (l Logger) Error(ctx context.Context, event string, msg string, attrs ...slog.Attr) {
	attrs = append(attrs, slog.String("msg", msg))
	l.slog.LogAttrs(ctx, slog.LevelError, event, attrs...)
}

func (l Logger) Debug(ctx context.Context, event string, msg string, attrs ...slog.Attr) {
	attrs = append(attrs, slog.String("msg", msg))
	l.slog.LogAttrs(ctx, slog.LevelDebug, event, attrs...)
}

func (l Logger) Env() string { return l.env }

func parseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
