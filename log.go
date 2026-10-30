package sqlglot

import (
	"log/slog"
	"os"
)

// Logger is the package logger, mirroring logging.getLogger("sqlglot"). Like Python's default
// logging configuration it only emits warnings and errors; replace it (or its handler) to
// capture info messages too.
var Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
