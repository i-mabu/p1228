package logging

import (
	"log/slog"
	"os"
	"strings"
)

// ===== 1. ロガーの生成 =====
func New(level, format string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	options := &slog.HandlerOptions{Level: l}
	if strings.EqualFold(format, "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, options))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, options))
}
