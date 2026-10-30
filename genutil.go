package sqlglot

import (
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
)

// Small helpers for translating Python generator idioms.

// iff is Python's conditional expression `a if c else b`.
func iff[T any](c bool, a, b T) T {
	if c {
		return a
	}
	return b
}

// pfx is the `f"{prefix}{s}" if s else ""` idiom.
func pfx(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

// sfx is the `f"{s}{suffix}" if s else ""` idiom.
func sfx(s, suffix string) string {
	if s == "" {
		return ""
	}
	return s + suffix
}

// around is the `f"{prefix}{s}{suffix}" if s else ""` idiom.
func around(prefix, s, suffix string) string {
	if s == "" {
		return ""
	}
	return prefix + s + suffix
}

// argStr is `str(v) if v else ""` for a raw argument value (expressions render with the
// default dialect, like Python's str()).
func argStr(v any) string {
	if !exp.Truthy(v) {
		return ""
	}
	return pyStr(v)
}

// joinNonEmpty is `sep.join(filter(None, parts))`.
func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// concatExprs concatenates expression lists into a new slice.
func concatExprs(lists ...[]*exp.Expr) []*exp.Expr {
	var out []*exp.Expr
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// PropertyLocations mirrors the defaultdict(list) returned by Generator.locate_properties.
type PropertyLocations map[exp.PropertiesLocation][]*exp.Expr

// FlatKey is self.expressions(expression, key=key, flat=True).
func (g *Generator) FlatKey(e *exp.Expr, key string) string {
	return g.Expressions(e, ExprsOpts{Key: key, Flat: true})
}

// ExprsKey is self.expressions(expression, key=key).
func (g *Generator) ExprsKey(e *exp.Expr, key string) string {
	return g.Expressions(e, ExprsOpts{Key: key})
}

// newPropertiesWithParent builds exp.Properties(expressions=...) whose parent is set (without
// arg key) to parent, like `props.parent = parent` in Python.
func newPropertiesWithParent(props []*exp.Expr, parent *exp.Expr) *exp.Expr {
	p := exp.New(exp.Properties, "expressions", props)
	if parent != nil {
		p.SetParentInfo(parent, "", -1)
	}
	return p
}

// pyStrip mirrors str.strip() (whitespace).
func pyStrip(s string) string { return strings.TrimSpace(s) }

// pyLStrip mirrors str.lstrip().
func pyLStrip(s string) string { return strings.TrimLeft(s, " \t\n\r\v\f") }

// pyRStrip mirrors str.rstrip().
func pyRStrip(s string) string { return strings.TrimRight(s, " \t\n\r\v\f") }

// argList returns a list argument as []any (expressions or, for mixed lists, raw values).
func argList(e *exp.Expr, key string) []any {
	switch v := e.Arg(key).(type) {
	case []*exp.Expr:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out
	case []any:
		return v
	case []string:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out
	}
	return nil
}
