package sqlglot

import (
	"fmt"

	"github.com/cottrellashley/sqlglot-go/exp"
)

// ApplyIndexOffset mirrors exp.apply_index_offset: it shifts a single integer array index by
// offset when `this` is (or may be) an array.
func ApplyIndexOffset(this *exp.Expr, expressions []*exp.Expr, offset int, d *Dialect) []*exp.Expr {
	if offset == 0 || len(expressions) != 1 {
		return expressions
	}
	expression := expressions[0]
	if this.Type() == nil {
		AnnotateTypes(this, d)
	}
	if t := this.Type().DataTypeThis(); t != exp.DTypeUnknown && t != exp.DTypeArray {
		return expressions
	}
	if expression.Type() == nil {
		AnnotateTypes(expression, d)
	}
	if exp.DataTypeIntegerTypes.Has(expression.Type().DataTypeThis()) {
		Logger.Info(fmt.Sprintf("Applying array index offset (%d)", offset))
		expression = Simplify(expression.Plus(offset), nil)
		return []*exp.Expr{expression}
	}
	return expressions
}
