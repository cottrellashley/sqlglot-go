package sqlglot

import "log/slog"

// Logger is the package logger, mirroring logging.getLogger("sqlglot"). Replace it (or its
// handler) to capture warnings.
var Logger = slog.Default()
