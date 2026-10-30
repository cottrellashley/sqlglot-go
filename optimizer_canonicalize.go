package sqlglot

// Port of sqlglot/optimizer/canonicalize.py.

import (
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// Canonicalize mirrors optimizer.canonicalize.canonicalize: it converts SQL into a standard
// form (e.g. string + string becomes CONCAT, redundant casts are removed).
func Canonicalize(e *exp.Expr, dialect any) *exp.Expr {
	d := MustDialect(dialect)
	return exp.ReplaceTree(e, func(node *exp.Expr) *exp.Expr {
		if !isCanonicalizeType(node) {
			return node
		}
		node = AddTextToConcat(node)
		node = ReplaceDateFuncs(node, d)
		node = CoerceType(node, d.PromoteToInferredDatetimeType)
		node = RemoveRedundantCasts(node)
		node = CanonicalizeEnsureBools(node, replaceIntPredicate)
		node = RemoveAscendingOrder(node)
		return node
	}, nil)
}

func isCoercibleDateOp(e *exp.Expr) bool {
	return e.IsAny(exp.Add, exp.Sub, exp.EQ, exp.NEQ, exp.GT, exp.GTE, exp.LT, exp.LTE, exp.NullSafeEQ, exp.NullSafeNEQ)
}

// isCanonicalizeType mirrors isinstance(expression, _CANONICALIZE_TYPES).
func isCanonicalizeType(e *exp.Expr) bool {
	return isCoercibleDateOp(e) || e.IsAny(exp.Date, exp.TsOrDsToDate, exp.Timestamp, exp.Between, exp.Extract,
		exp.DateAdd, exp.DateSub, exp.DateTrunc, exp.DateDiff, exp.Cast, exp.Connector, exp.Not, exp.If, exp.Where,
		exp.Having, exp.Ordered)
}

// AddTextToConcat mirrors canonicalize.add_text_to_concat.
func AddTextToConcat(node *exp.Expr) *exp.Expr {
	if node.IsKind(exp.Add) && node.Type() != nil && exp.DataTypeTextTypes.Has(node.Type().DataTypeThis()) {
		node = exp.New(exp.Concat, "expressions", []*exp.Expr{node.Left(), node.Right()},
			// All known dialects, i.e. Redshift and T-SQL, that support
			// concatenating strings with the + operator do not coalesce NULLs.
			"coalesce", false)
	}
	return node
}

// ReplaceDateFuncs mirrors canonicalize.replace_date_funcs.
func ReplaceDateFuncs(node *exp.Expr, d *Dialect) *exp.Expr {
	if node.IsAny(exp.Date, exp.TsOrDsToDate) && len(node.Expressions()) == 0 && !node.Bool("zone") &&
		node.This().IsString() && helper.IsISODate(node.This().Name()) {
		return exp.NewCast(node.This(), exp.DTypeDate)
	}
	if node.IsKind(exp.Timestamp) && !node.Bool("zone") {
		if node.Type() == nil {
			node = AnnotateTypes(node, d)
		}
		var to any = exp.DTypeTimestamp
		if t := node.Type(); t != nil {
			to = t
		}
		return exp.NewCast(node.This(), to)
	}
	return node
}

// CoerceType mirrors canonicalize.coerce_type.
func CoerceType(node *exp.Expr, promoteToInferredDatetimeType bool) *exp.Expr {
	switch {
	case isCoercibleDateOp(node):
		coerceDate(node.Left(), node.Right(), promoteToInferredDatetimeType)
	case node.IsKind(exp.Between):
		coerceDate(node.This(), node.Get("low"), promoteToInferredDatetimeType)
	case node.IsKind(exp.Extract) && !node.Expression().IsTypeIn(exp.DataTypeTemporalTypes):
		replaceCast(node.Expression(), exp.DTypeDatetime)
	case node.IsAny(exp.DateAdd, exp.DateSub, exp.DateTrunc):
		coerceTimeunitArg(node.This(), node.Unit())
	case node.IsKind(exp.DateDiff):
		coerceDatediffArgs(node)
	}
	return node
}

// RemoveRedundantCasts mirrors canonicalize.remove_redundant_casts.
func RemoveRedundantCasts(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Cast) && e.This().Type() != nil && e.To().Equal(e.This().Type()) {
		return exp.NewParen(e.This(), exp.WithCopy(false))
	}
	if e.IsAny(exp.Date, exp.TsOrDsToDate) && e.This().Type() != nil &&
		e.This().Type().DataTypeThis() == exp.DTypeDate && len(e.This().Type().Expressions()) == 0 {
		return exp.NewParen(e.This(), exp.WithCopy(false))
	}
	return e
}

// CanonicalizeEnsureBools mirrors canonicalize.ensure_bools(expression, replace_func).
func CanonicalizeEnsureBools(e *exp.Expr, replaceFunc func(*exp.Expr)) *exp.Expr {
	switch {
	case e.Is(exp.Connector):
		replaceFunc(e.Left())
		replaceFunc(e.Right())
	case e.IsKind(exp.Not):
		replaceFunc(e.This())
	// We can't replace num in CASE x WHEN num ..., because it's not the full predicate
	case e.IsKind(exp.If) && !(e.Parent().IsKind(exp.Case) && e.Parent().Bool("this")):
		replaceFunc(e.This())
	case e.IsAny(exp.Where, exp.Having):
		replaceFunc(e.This())
	}
	return e
}

// RemoveAscendingOrder mirrors canonicalize.remove_ascending_order.
func RemoveAscendingOrder(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Ordered) {
		if b, ok := e.Arg("desc").(bool); ok && !b {
			// Convert ORDER BY a ASC to ORDER BY a
			e.Set("desc", nil)
		}
	}
	return e
}

func coerceDate(a, b *exp.Expr, promote bool) {
	for _, pair := range [][2]*exp.Expr{{a, b}, {b, a}} {
		a, b := pair[0], pair[1]
		if b.IsKind(exp.Interval) {
			a = coerceTimeunitArg(a, b.Unit())
		}
		aType := a.Type()
		if aType == nil || !exp.DataTypeTemporalTypes.Has(aType.DataTypeThis()) || b.Type() == nil ||
			!exp.DataTypeTextTypes.Has(b.Type().DataTypeThis()) {
			continue
		}
		var target any = aType
		if promote {
			var bType exp.DType
			if b.IsString() {
				dateText := b.Name()
				if helper.IsISODate(dateText) {
					bType = exp.DTypeDate
				} else if helper.IsISODatetime(dateText) {
					bType = exp.DTypeDatetime
				} else {
					bType = aType.DataTypeThis()
				}
			} else {
				// If b is not a datetime string, we conservatively promote it to a DATETIME,
				// in order to ensure there are no surprising truncations due to downcasting
				bType = exp.DTypeDatetime
			}
			if TypeAnnotatorCoercesTo[aType.DataTypeThis()].Has(bType) {
				target = bType
			}
		}
		if !dataTypeEqual(target, aType) {
			replaceCast(a, target)
		}
		replaceCast(b, target)
	}
}

// dataTypeEqual mirrors `target_type == a_type` where target may be a DType (never equal to
// a DataType node in Python) or a DataType.
func dataTypeEqual(target any, dt *exp.Expr) bool {
	if x, ok := target.(*exp.Expr); ok {
		return x.Equal(dt)
	}
	return false
}

func coerceTimeunitArg(arg *exp.Expr, unit *exp.Expr) *exp.Expr {
	if arg.Type() == nil {
		return arg
	}
	t := arg.Type().DataTypeThis()
	if exp.DataTypeTextTypes.Has(t) {
		dateText := arg.Name()
		isISODate := helper.IsISODate(dateText)
		if isISODate && isDateUnit(unit) {
			return arg.Replace(exp.NewCast(arg.Copy(), exp.DTypeDate))
		}
		// An ISO date is also an ISO datetime, but not vice versa
		if isISODate || helper.IsISODatetime(dateText) {
			return arg.Replace(exp.NewCast(arg.Copy(), exp.DTypeDatetime))
		}
	} else if t == exp.DTypeDate && !isDateUnit(unit) {
		return arg.Replace(exp.NewCast(arg.Copy(), exp.DTypeDatetime))
	}
	return arg
}

// isDateUnit mirrors helper.is_date_unit.
// IsDateUnit mirrors helper.is_date_unit.
func IsDateUnit(e *exp.Expr) bool { return isDateUnit(e) }

func isDateUnit(e *exp.Expr) bool {
	return e != nil && helper.DateUnits[toLowerASCII(e.Name())]
}

func toLowerASCII(s string) string { return pyLower(s) }

func coerceDatediffArgs(node *exp.Expr) {
	for _, e := range []*exp.Expr{node.This(), node.Expression()} {
		if e.Type() == nil || !exp.DataTypeTemporalTypes.Has(e.Type().DataTypeThis()) {
			e.Replace(exp.NewCast(e.Copy(), exp.DTypeDatetime))
		}
	}
}

func replaceCast(node *exp.Expr, to any) {
	node.Replace(exp.NewCast(node.Copy(), to))
}

// replaceIntPredicate mirrors canonicalize._replace_int_predicate (designed for Presto).
func replaceIntPredicate(e *exp.Expr) {
	if e.IsKind(exp.Coalesce) {
		for _, child := range e.IterExpressions() {
			replaceIntPredicate(child)
		}
	} else if e.Type() != nil && exp.DataTypeIntegerTypes.Has(e.Type().DataTypeThis()) {
		e.Replace(e.Neq(0))
	}
}
