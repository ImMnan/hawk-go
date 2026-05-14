package pkg

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

func InitLogger() {
	level := parseLogLevel(os.Getenv("HAWK_LOG_LEVEL"))
	format := strings.ToLower(strings.TrimSpace(os.Getenv("HAWK_LOG_FORMAT")))
	if format == "" {
		format = "json"
	}

	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey && attr.Value.Kind() == slog.KindTime {
				return slog.String(slog.TimeKey, attr.Value.Time().UTC().Format(time.RFC3339Nano))
			}

			if attr.Value.Kind() == slog.KindAny {
				if err, ok := attr.Value.Any().(error); ok && err != nil {
					return slog.String(attr.Key, err.Error())
				}
			}

			return attr
		},
	}
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else if format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		format = "json"
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	logger := slog.New(handler).With("service", "hawk")
	slog.SetDefault(logger)

	slog.Info("logger initialized", "format", format, "level", level.String())
}

func parseLogLevel(raw string) slog.Level {
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
