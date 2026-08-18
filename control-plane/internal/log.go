// SPDX-License-Identifier: MIT

package internal

import (
	"io"
	"log/slog"
	"strings"
)

// InitSlog wires log/slog to the text handler at the requested level
// (LOGLEVEL env var), writing to dst. Idempotent.
func InitSlog(level string, dst io.Writer) {
	var lvl slog.Level
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		lvl = slog.LevelDebug
	case "WARN", "WARNING":
		lvl = slog.LevelWarn
	case "ERROR":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(dst, &slog.HandlerOptions{Level: lvl})))
}
