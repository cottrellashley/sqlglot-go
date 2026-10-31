// Package errs contains the error types and error levels shared by the tokenizer, parser,
// generator and optimizer. It mirrors sqlglot/errors.py.
package errs

import (
	"fmt"
	"sort"
	"strings"
)

// ErrorLevel controls how the parser and generator react to errors.
type ErrorLevel int

const (
	// Warn logs all errors (the default for the generator).
	Warn ErrorLevel = iota
	// Ignore ignores all errors.
	Ignore
	// Raise collects all errors and raises a single exception (the default for the parser).
	Raise
	// Immediate raises an exception on the first error found.
	Immediate
)

// Unset is used by option structs to mean "use the default level".
const Unset ErrorLevel = -1

func (l ErrorLevel) String() string {
	switch l {
	case Ignore:
		return "IGNORE"
	case Warn:
		return "WARN"
	case Raise:
		return "RAISE"
	case Immediate:
		return "IMMEDIATE"
	}
	return "UNSET"
}

const (
	ANSIUnderline              = "\033[4m"
	ANSIReset                  = "\033[0m"
	ErrorMessageContextDefault = 100
)

// SqlglotError is implemented by every error type of this package.
type SqlglotError interface {
	error
	sqlglotError()
}

// ErrorDetail is a structured description of a single parse error.
type ErrorDetail struct {
	Description    string
	Line           int
	Col            int
	StartContext   string
	Highlight      string
	EndContext     string
	IntoExpression string
}

// ParseError is raised when the parser cannot parse a SQL string.
type ParseError struct {
	Message string
	Errors  []ErrorDetail
}

func (e *ParseError) Error() string { return e.Message }
func (*ParseError) sqlglotError()   {}

// NewParseError mirrors ParseError.new.
func NewParseError(message string, detail ErrorDetail) *ParseError {
	return &ParseError{Message: message, Errors: []ErrorDetail{detail}}
}

// TokenError is raised when the tokenizer fails.
type TokenError struct {
	Message string
	Start   int
	End     int
	HasPos  bool
}

func (e *TokenError) Error() string { return e.Message }
func (*TokenError) sqlglotError()   {}

// UnsupportedError is raised when the generator meets an unsupported construct.
type UnsupportedError struct{ Message string }

func (e *UnsupportedError) Error() string { return e.Message }
func (*UnsupportedError) sqlglotError()   {}

// OptimizeError is raised by optimizer rules.
type OptimizeError struct{ Message string }

func (e *OptimizeError) Error() string { return e.Message }
func (*OptimizeError) sqlglotError()   {}

// SchemaError is raised by schema operations.
type SchemaError struct{ Message string }

func (e *SchemaError) Error() string { return e.Message }
func (*SchemaError) sqlglotError()   {}

// ExecuteError is raised by the executor.
type ExecuteError struct{ Message string }

func (e *ExecuteError) Error() string { return e.Message }
func (*ExecuteError) sqlglotError()   {}

// AssertionError reports a failed invariant check, where sqlglot uses an assert statement
// (for example a transform that cannot handle its input).
type AssertionError struct{ Message string }

func (e *AssertionError) Error() string { return e.Message }

// Unsupportedf builds an UnsupportedError.
func Unsupportedf(format string, args ...any) *UnsupportedError {
	return &UnsupportedError{Message: fmt.Sprintf(format, args...)}
}

// Optimizef builds an OptimizeError.
func Optimizef(format string, args ...any) *OptimizeError {
	return &OptimizeError{Message: fmt.Sprintf(format, args...)}
}

// Schemaf builds a SchemaError.
func Schemaf(format string, args ...any) *SchemaError {
	return &SchemaError{Message: fmt.Sprintf(format, args...)}
}

// HighlightSQL mirrors errors.highlight_sql. Positions are rune offsets (inclusive end).
func HighlightSQL(sql []rune, positions [][2]int, contextLength int) (formatted, startContext, highlight, endContext string) {
	if len(positions) == 0 {
		panic("positions must contain at least one (start, end) tuple")
	}
	sorted := append([][2]int(nil), positions...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i][0] < sorted[j][0] })

	firstHighlightStart := 0
	var parts []string
	previousPartEnd := 0
	slice := func(a, b int) string {
		if a < 0 {
			a = 0
		}
		if b > len(sql) {
			b = len(sql)
		}
		if a >= b {
			return ""
		}
		return string(sql[a:b])
	}

	if sorted[0][0] > 0 {
		firstHighlightStart = sorted[0][0]
		startContext = slice(firstHighlightStart-contextLength, firstHighlightStart)
		parts = append(parts, startContext)
		previousPartEnd = firstHighlightStart
	}

	for _, pos := range sorted {
		highlightStart := max(pos[0], previousPartEnd)
		highlightEnd := pos[1] + 1
		if highlightStart >= highlightEnd {
			continue
		}
		if highlightStart > previousPartEnd {
			parts = append(parts, slice(previousPartEnd, highlightStart))
		}
		parts = append(parts, ANSIUnderline+slice(highlightStart, highlightEnd)+ANSIReset)
		previousPartEnd = highlightEnd
	}

	if previousPartEnd < len(sql) {
		endContext = slice(previousPartEnd, previousPartEnd+contextLength)
		parts = append(parts, endContext)
	}

	formatted = strings.Join(parts, "")
	highlight = slice(firstHighlightStart, previousPartEnd)
	return
}

// ConcatMessages mirrors errors.concat_messages.
func ConcatMessages(errors []string, maximum int) string {
	n := min(maximum, len(errors))
	msg := append([]string(nil), errors[:n]...)
	if remaining := len(errors) - maximum; remaining > 0 {
		msg = append(msg, fmt.Sprintf("... and %d more", remaining))
	}
	return strings.Join(msg, "\n\n")
}

// MergeErrors mirrors errors.merge_errors.
func MergeErrors(errors []*ParseError) []ErrorDetail {
	var out []ErrorDetail
	for _, e := range errors {
		out = append(out, e.Errors...)
	}
	return out
}
