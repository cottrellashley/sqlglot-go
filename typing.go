package sqlglot

// Port of sqlglot/typing/__init__.py: the type annotation metadata of the base dialect.

import "github.com/cottrellashley/sqlglot-go/exp"

// AnnotateByNumericArg mirrors typing.annotate_by_numeric_arg: math functions (e.g. FLOOR)
// that preserve numeric and unknown types, and return INT otherwise.
func AnnotateByNumericArg(a *TypeAnnotator, e *exp.Expr) *exp.Expr {
	this := e.This()
	if this.IsTypeIn(exp.NewTypeSet(exp.DTypeUnknown), exp.DataTypeNumericTypes) {
		a.SetType(e, typeOrNil(this))
	} else {
		a.SetType(e, exp.DTypeInt)
	}
	return e
}

// AnnotateIntAsDouble mirrors typing.annotate_int_as_double: math functions that return DOUBLE
// for integers and strings, and the input type otherwise.
func AnnotateIntAsDouble(a *TypeAnnotator, e *exp.Expr) *exp.Expr {
	this := e.This()
	if this.IsTypeIn(exp.DataTypeIntegerTypes, exp.DataTypeTextTypes) {
		a.SetType(e, exp.DTypeDouble)
	} else {
		a.SetType(e, typeOrNil(this))
	}
	return e
}

func annBinary(a *TypeAnnotator, e *exp.Expr) *exp.Expr { return a.AnnotateBinary(e) }

func annUnary(a *TypeAnnotator, e *exp.Expr) *exp.Expr { return a.AnnotateUnary(e) }

// annByArgs builds `lambda self, e: self._annotate_by_args(e, *args, promote=..., array=...)`.
func annByArgs(promote, array bool, args ...string) func(*TypeAnnotator, *exp.Expr) *exp.Expr {
	anys := make([]any, len(args))
	for i, a := range args {
		anys[i] = a
	}
	return func(a *TypeAnnotator, e *exp.Expr) *exp.Expr { return a.AnnotateByArgs(e, anys, promote, array) }
}

func annUDF(a *TypeAnnotator, e *exp.Expr) *exp.Expr {
	t, err := a.Schema.GetUDFType(e, nil, nil)
	if err != nil {
		panic(err)
	}
	return a.SetType(e, t)
}

func annCase(a *TypeAnnotator, e *exp.Expr) *exp.Expr {
	var args []any
	for _, ifExpr := range e.List("ifs") {
		args = append(args, ifExpr.Get("true"))
	}
	args = append(args, "default")
	return a.AnnotateByArgs(e, args, false, false)
}

func annBigIntOrInt(a *TypeAnnotator, e *exp.Expr) *exp.Expr {
	return a.SetType(e, iff(e.Bool("big_int"), exp.DTypeBigint, exp.DTypeInt))
}

func annFixedType(sql string) func(*TypeAnnotator, *exp.Expr) *exp.Expr {
	return func(a *TypeAnnotator, e *exp.Expr) *exp.Expr {
		return a.SetType(e, exp.MustDataTypeBuild(sql, nil, false))
	}
}

// BaseExpressionMetadata returns a fresh copy of sqlglot.typing.EXPRESSION_METADATA.
func BaseExpressionMetadata() map[exp.Kind]*ExprMetadata {
	m := map[exp.Kind]*ExprMetadata{}
	baseTypingData(m)
	return m
}
