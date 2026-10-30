package sqlglot

// Generator entry points translated from sqlglot/generator.py (generate, preprocess, ...).
// These live outside generator_core.go so that dialects can override them.

import (
	"strings"
	"unicode"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
)

func (g *Generator) generate(e *exp.Expr, copy bool) string {
	if copy {
		e = e.Copy()
	}
	e = g.Preprocess(e)
	g.UnsupportedMessages = nil
	sql := strings.TrimFunc(g.SQL(e), unicode.IsSpace)
	if g.Pretty {
		sql = strings.ReplaceAll(sql, SentinelLineBreak, "\n")
	}
	if g.UnsupportedLevel == errs.Ignore {
		return sql
	}
	if g.UnsupportedLevel == errs.Warn {
		for _, msg := range g.UnsupportedMessages {
			Logger.Warn(msg)
		}
	} else if g.UnsupportedLevel == errs.Raise && len(g.UnsupportedMessages) > 0 {
		panic(generatePanic{&errs.UnsupportedError{Message: errs.ConcatMessages(g.UnsupportedMessages, g.MaxUnsupported)}})
	}
	return sql
}

func (g *Generator) preprocess(e *exp.Expr) *exp.Expr {
	e = g.moveCtesToTopLevel(e)
	if g.C.EnsureBools {
		e = EnsureBools(e)
	}
	return e
}

func (g *Generator) moveCtesToTopLevel(e *exp.Expr) *exp.Expr {
	if e.Parent() == nil && g.C.ExpressionsWithoutNestedCtes.Has(e.Kind()) {
		nested := false
		for w := range e.FindAll(exp.With) {
			if w.Parent() != e {
				nested = true
				break
			}
		}
		if nested {
			e = MoveCtesToTopLevel(e)
		}
	}
	return e
}
