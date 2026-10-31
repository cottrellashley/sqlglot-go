package pyrt

import (
	"context"
	"log/slog"

	sqlglot "github.com/cottrellashley/sqlglot-go"
)

// MockLoggerV is a mock.patch'ed sqlglot logger: it records the messages logged per level.
type MockLoggerV struct {
	calls map[string][]string
}

type mockHandler struct{ m *MockLoggerV }

func (h mockHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h mockHandler) Handle(_ context.Context, r slog.Record) error {
	level := "info"
	switch {
	case r.Level >= slog.LevelError:
		level = "error"
	case r.Level >= slog.LevelWarn:
		level = "warning"
	case r.Level < slog.LevelInfo:
		level = "debug"
	}
	h.m.calls[level] = append(h.m.calls[level], r.Message)
	return nil
}
func (h mockHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h mockHandler) WithGroup(string) slog.Handler      { return h }

// PatchLogger replaces the sqlglot logger by a mock until the returned function is called.
func (s *Case) PatchLogger() (Value, func()) {
	m := &MockLoggerV{calls: map[string][]string{}}
	prev := sqlglot.Logger
	sqlglot.Logger = slog.New(mockHandler{m})
	return m, func() { sqlglot.Logger = prev }
}

// PatchLogMethod is `with mock.patch("...logger.<level>") as m: ...`; f receives the mock method.
func (s *Case) PatchLogMethod(f func(m Value), level Value) {
	logger, restore := s.PatchLogger()
	defer restore()
	f(&MockMethodV{logger.(*MockLoggerV), Str(level)})
}

// PyAttr implements Attrer.
func (m *MockLoggerV) PyAttr(name string) (Value, bool) {
	switch name {
	case "warning", "error", "info", "debug", "warn":
		if name == "warn" {
			name = "warning"
		}
		return &MockMethodV{m, name}, true
	}
	return nil, false
}

// MockMethodV is a mocked logger method (logger.warning, ...).
type MockMethodV struct {
	m     *MockLoggerV
	level string
}

func callRecord(msg string) Value { return Tuple{Tuple{msg}, NewDict()} }

func expectedMessage(a Args) string {
	if len(a.Pos) == 0 {
		return ""
	}
	msg := Str(a.Pos[0])
	if len(a.Pos) > 1 {
		msg = percentFormat(msg, a.Pos[1:])
	}
	return msg
}

// PyAttr implements Attrer.
func (mm *MockMethodV) PyAttr(name string) (Value, bool) {
	calls := mm.m.calls[mm.level]
	switch name {
	case "called":
		return len(calls) > 0, true
	case "call_count":
		return len(calls), true
	case "call_args":
		if len(calls) == 0 {
			return nil, true
		}
		return callRecord(calls[len(calls)-1]), true
	case "call_args_list":
		out := make([]Value, len(calls))
		for i, c := range calls {
			out[i] = callRecord(c)
		}
		return &List{out}, true
	case "assert_not_called":
		return Func(name, func(a Args) Value {
			if len(calls) > 0 {
				failLog("Expected '%s' to not have been called. Called %d times: %v", mm.level, len(calls), calls)
			}
			return nil
		}), true
	case "assert_called", "assert_called_once":
		return Func(name, func(a Args) Value {
			if len(calls) == 0 || (name == "assert_called_once" && len(calls) != 1) {
				failLog("Expected '%s' to have been called (once: %v), called %d times", mm.level, name == "assert_called_once", len(calls))
			}
			return nil
		}), true
	case "assert_called_with", "assert_called_once_with":
		return Func(name, func(a Args) Value {
			want := expectedMessage(a)
			if len(calls) == 0 || calls[len(calls)-1] != want || (name == "assert_called_once_with" && len(calls) != 1) {
				failLog("expected call %s(%q), got %v", mm.level, want, calls)
			}
			return nil
		}), true
	case "assert_any_call":
		return Func(name, func(a Args) Value {
			want := expectedMessage(a)
			for _, c := range calls {
				if c == want {
					return nil
				}
			}
			failLog("%s(%q) call not found in %v", mm.level, want, calls)
			return nil
		}), true
	}
	return nil, false
}

// PyAttr implements Attrer: logging methods log through the sqlglot logger.
func (l *LoggerV) PyAttr(name string) (Value, bool) {
	logAt := func(level slog.Level) Value {
		return Func(name, func(a Args) Value {
			msg := Str(a.Pos[0])
			if len(a.Pos) > 1 {
				msg = percentFormat(msg, a.Pos[1:])
			}
			sqlglot.Logger.Log(context.Background(), level, msg)
			return nil
		})
	}
	switch name {
	case "warning", "warn":
		return logAt(slog.LevelWarn), true
	case "error":
		return logAt(slog.LevelError), true
	case "info":
		return logAt(slog.LevelInfo), true
	case "debug":
		return logAt(slog.LevelDebug), true
	case "name":
		return l.Name, true
	}
	return nil, false
}
