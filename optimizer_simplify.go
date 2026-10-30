package sqlglot

// Port of sqlglot/optimizer/simplify.py.

import (
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/internal/pydate"
	"github.com/cottrellashley/sqlglot-go/internal/pydec"
)

// Final means that an expression should not be simplified.
const simplifyFinal = "final"

// Simplify mirrors simplify(expression, dialect=dialect): it rewrites sqlglot AST to simplify
// expressions (boolean logic, arithmetic, dates, ...).
func Simplify(e *exp.Expr, dialect any) *exp.Expr {
	return SimplifyOpts(e, false, false, dialect)
}

// SimplifyOpts mirrors simplify(expression, constant_propagation, coalesce_simplification,
// dialect).
func SimplifyOpts(e *exp.Expr, constantPropagation, coalesceSimplification bool, dialect any) *exp.Expr {
	return NewSimplifier(dialect, true).Simplify(e, constantPropagation, coalesceSimplification)
}

// simplifyAbort is raised by helpers to signal UnsupportedUnit, ValueError or OverflowError,
// which the `catch` decorated methods turn into "no change".
type simplifyAbort struct{ msg string }

func abortSimplify(format string, args ...any) { panic(simplifyAbort{fmt.Sprintf(format, args...)}) }

// catchAbort runs fn and returns fallback when it aborts (the @catch decorator).
func catchAbort(fallback *exp.Expr, fn func() *exp.Expr) (out *exp.Expr) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(simplifyAbort); ok {
				out = fallback
				return
			}
			panic(r)
		}
	}()
	return fn()
}

// SimplifyFlatten mirrors simplify.flatten: A AND (B AND C) -> A AND B AND C.
func SimplifyFlatten(e *exp.Expr) *exp.Expr {
	if e.Is(exp.Connector) {
		for _, a := range e.Args() {
			node, ok := a.Value.(*exp.Expr)
			if !ok || node == nil {
				continue
			}
			if child := node.Unnest(); child.IsKind(e.Kind()) {
				node.Replace(child)
			}
		}
	}
	return e
}

// factorTier mirrors FACTOR_TIER (Parser.FACTOR values).
func isFactorTier(e *exp.Expr) bool {
	return e.IsAny(exp.IntDiv, exp.Distance, exp.DistanceNd, exp.Mod, exp.Div, exp.Mul)
}

func isPureMulChain(node *exp.Expr) bool {
	for isFactorTier(node) {
		if !node.IsKind(exp.Mul) {
			return false
		}
		node = node.This()
	}
	return true
}

// SimplifyParens mirrors simplify.simplify_parens.
func SimplifyParens(e *exp.Expr, d *Dialect) *exp.Expr {
	if !e.IsKind(exp.Paren) {
		return e
	}
	this := e.This()
	parent := e.Parent()
	parentIsPredicate := parent.Is(exp.Predicate)
	if this.IsKind(exp.Select) {
		return e
	}
	if parent.IsAny(exp.SubqueryPredicate, exp.Bracket) {
		return e
	}
	if d.RequiresParenthesizedStructAccess && parent.IsKind(exp.Dot) && parent.Right().IsAny(exp.Identifier, exp.Star) {
		return e
	}
	if this.IsAny(exp.Predicate, exp.Not) {
		if parentIsPredicate || parent.IsAny(exp.Neg, exp.BitwiseNot) || (parent.Is(exp.Binary) && !parent.Is(exp.Connector)) {
			return e
		}
		return this
	}
	if !parent.IsAny(exp.Condition, exp.Binary) ||
		parent.IsKind(exp.Paren) ||
		!this.Is(exp.Binary) ||
		(this.Is(exp.Add) && parent.Is(exp.Add)) ||
		(this.Is(exp.Mul) && parent.Is(exp.Mul) && (parent.This() == e || isPureMulChain(this.This()))) ||
		(this.Is(exp.Mul) && parent.IsAny(exp.Add, exp.Sub)) {
		return this
	}
	return e
}

// PropagateConstants mirrors simplify.propagate_constants: x = 5 AND y = x + 2 becomes
// x = 5 AND y = 5 + 2 for non-nullable columns.
func PropagateConstants(e *exp.Expr, root bool) *exp.Expr {
	if e.IsKind(exp.And) && (root || !e.SameParent()) && Normalized(e, true) {
		type entry struct {
			col      *exp.Expr
			constant *exp.Expr
		}
		mapping := map[exp.ExprKey]entry{}
		for node := range WalkInScope(e, func(n *exp.Expr) bool { return n.IsKind(exp.If) }) {
			if node.IsKind(exp.EQ) {
				l, r := node.Left(), node.Right()
				// Substituting the constant is only an identity when the column can't be NULL
				if l.IsKind(exp.Column) && r.IsKind(exp.Literal) && l.MetaGet("nonnull") == true {
					mapping[l.MapKey()] = entry{l, r}
				}
			}
		}
		if len(mapping) > 0 {
			for _, column := range slices.Collect(FindAllInScope(e, exp.Column)) {
				parent := column.Parent()
				ent, ok := mapping[column.MapKey()]
				if ok && column != ent.col && !(parent.IsKind(exp.Is) && parent.Expression().IsKind(exp.Null)) {
					column.Replace(ent.constant.Copy())
				}
			}
		}
	}
	return e
}

func isNonnullConstant(e *exp.Expr) bool {
	return e.IsAny(exp.Literal, exp.Boolean) || isDateLiteral(e)
}

func isConstant(e *exp.Expr) bool {
	x := e
	if e.IsKind(exp.Neg) {
		x = e.This()
	}
	return x.IsAny(exp.Literal, exp.Boolean, exp.Null) || isDateLiteral(x)
}

type dateRange [2]pydate.Value

func (s *Simplifier) datetruncRange(d pydate.Value, unit string) (dateRange, bool) {
	floor := datetimeFloor(d, unit, s.D)
	if !d.Equal(floor) {
		// This will always be False, except for NULL values.
		return dateRange{}, false
	}
	return dateRange{floor, mustAddInterval(floor, unit, 1)}, true
}

func datetruncEqExpression(left *exp.Expr, drange dateRange, targetType *exp.Expr) *exp.Expr {
	return exp.NewAnd(left.Gte(dateLiteral(drange[0], targetType)), left.Lt(dateLiteral(drange[1], targetType)), exp.WithCopy(false))
}

func parenthesizeNestedConnector(e *exp.Expr, parent *exp.Expr) *exp.Expr {
	if e.Is(exp.Connector) && (parent.IsKind(exp.Not) || (parent.Is(exp.Connector) && parent.Kind() != e.Kind())) {
		return exp.NewParen(e, exp.WithCopy(false))
	}
	return e
}

// AlwaysTrue mirrors simplify.always_true.
func AlwaysTrue(e *exp.Expr) bool {
	return (e.Is(exp.Boolean) && e.Bool("this")) || (e.Is(exp.Literal) && e.IsNumber() && !IsZero(e))
}

// AlwaysFalse mirrors simplify.always_false.
func AlwaysFalse(e *exp.Expr) bool { return IsFalse(e) || IsNull(e) || IsZero(e) }

// IsZero mirrors simplify.is_zero.
func IsZero(e *exp.Expr) bool {
	if !e.Is(exp.Literal) {
		return false
	}
	v, err := e.ToPy()
	if err != nil {
		return false
	}
	r, ok := pyNumber(v)
	return ok && r.Sign() == 0
}

// IsFalse mirrors simplify.is_false.
func IsFalse(e *exp.Expr) bool { return e.IsKind(exp.Boolean) && !e.Bool("this") }

// IsNull mirrors simplify.is_null.
func IsNull(e *exp.Expr) bool { return e.IsKind(exp.Null) }

func isComplement(a, b *exp.Expr) bool { return b.IsKind(exp.Not) && b.This().Equal(a) }

// pyNumber converts a to_py() number into a rational.
func pyNumber(v any) (*big.Rat, bool) {
	switch x := v.(type) {
	case int:
		return new(big.Rat).SetInt64(int64(x)), true
	case *big.Int:
		return new(big.Rat).SetInt(x), true
	case exp.Decimal:
		return x.Rat, true
	}
	return nil, false
}

// pyNum is a Python numeric value: an exact integer or a Decimal.
type numVal struct {
	isInt bool
	i     *big.Int
	d     pydec.Dec
}

func toPyNum(v any) (numVal, bool) {
	switch x := v.(type) {
	case int:
		return numVal{isInt: true, i: big.NewInt(int64(x))}, true
	case *big.Int:
		return numVal{isInt: true, i: x}, true
	case exp.Decimal:
		d, ok := pydec.Parse(x.Text)
		return numVal{d: d}, ok
	}
	return numVal{}, false
}

func (n numVal) dec() pydec.Dec {
	if n.isInt {
		return pydec.FromInt(n.i)
	}
	return n.d
}

func (n numVal) rat() *big.Rat {
	if n.isInt {
		return new(big.Rat).SetInt(n.i)
	}
	return n.d.Rat()
}

func (n numVal) literal() *exp.Expr {
	if n.isInt {
		return exp.LiteralNumber(n.i)
	}
	return exp.LiteralNumber(n.d.String())
}

func pyNumOp(a, b numVal, op byte) numVal {
	if a.isInt && b.isInt {
		r := new(big.Int)
		switch op {
		case '+':
			r.Add(a.i, b.i)
		case '-':
			r.Sub(a.i, b.i)
		case '*':
			r.Mul(a.i, b.i)
		}
		return numVal{isInt: true, i: r}
	}
	da, db := a.dec(), b.dec()
	switch op {
	case '+':
		return numVal{d: da.Add(db)}
	case '-':
		return numVal{d: da.Sub(db)}
	case '*':
		return numVal{d: da.Mul(db)}
	}
	return numVal{d: da.Quo(db)}
}

// cmpValue is a comparable scalar used by eval_boolean (numbers, strings or dates).
type cmpValue struct {
	num  *big.Rat
	str  *string
	date *pydate.Value
}

// compare returns (cmp, ok); ok is false when Python would raise TypeError.
func (a cmpValue) compare(b cmpValue) (int, bool) {
	switch {
	case a.num != nil && b.num != nil:
		return a.num.Cmp(b.num), true
	case a.str != nil && b.str != nil:
		return strings.Compare(*a.str, *b.str), true
	case a.date != nil && b.date != nil:
		return a.date.Compare(*b.date)
	}
	return 0, false
}

func (a cmpValue) equal(b cmpValue) bool {
	if a.date != nil && b.date != nil {
		return a.date.Equal(*b.date)
	}
	c, ok := a.compare(b)
	return ok && c == 0
}

func booleanLiteral(cond bool) *exp.Expr {
	if cond {
		return exp.NewTrue()
	}
	return exp.NewFalse()
}

// evalBoolean mirrors simplify.eval_boolean.
func evalBoolean(e *exp.Expr, a, b cmpValue) *exp.Expr {
	ordered := func(f func(int) bool) *exp.Expr {
		c, ok := a.compare(b)
		if !ok {
			panic(fmt.Errorf("TypeError: '<' not supported between instances"))
		}
		return booleanLiteral(f(c))
	}
	switch {
	case e.IsAny(exp.EQ, exp.Is):
		if e.IsKind(exp.Is) && e.Bool("negate") {
			return booleanLiteral(!a.equal(b))
		}
		return booleanLiteral(a.equal(b))
	case e.Is(exp.NEQ):
		return booleanLiteral(!a.equal(b))
	case e.Is(exp.GT):
		return ordered(func(c int) bool { return c > 0 })
	case e.Is(exp.GTE):
		return ordered(func(c int) bool { return c >= 0 })
	case e.Is(exp.LT):
		return ordered(func(c int) bool { return c < 0 })
	case e.Is(exp.LTE):
		return ordered(func(c int) bool { return c <= 0 })
	}
	return nil
}

func castAsDate(s string) (pydate.Value, bool) {
	v, ok := pydate.FromISOFormat(s)
	if !ok {
		return pydate.Value{}, false
	}
	return v.Date(), true
}

func castAsDatetime(v pydate.Value) pydate.Value { return v.ToDatetime() }

// extractDate mirrors simplify.extract_date.
func extractDate(cast *exp.Expr) (pydate.Value, bool) {
	var to *exp.Expr
	if cast.IsKind(exp.Cast) {
		to = cast.To()
	} else if cast.IsKind(exp.TsOrDsToDate) && !cast.Bool("format") {
		to = exp.DTypeDate.IntoExpr()
	} else {
		return pydate.Value{}, false
	}
	this := cast.This()
	var value pydate.Value
	var str string
	isStr := false
	if this.IsKind(exp.Literal) {
		str = this.Name()
		isStr = true
	} else if this.IsAny(exp.Cast, exp.TsOrDsToDate) {
		v, ok := extractDate(this)
		if !ok {
			return pydate.Value{}, false
		}
		value = v
	} else {
		return pydate.Value{}, false
	}
	if isStr && str == "" {
		return pydate.Value{}, false
	}
	if to.IsType(exp.DTypeDate) {
		if isStr {
			return castAsDate(str)
		}
		if value.IsDatetime {
			return value.Date(), true
		}
		return value, true
	}
	if to.IsTypeIn(exp.DataTypeTemporalTypes) {
		if isStr {
			v, ok := pydate.FromISOFormat(str)
			return v, ok
		}
		return castAsDatetime(value), true
	}
	return pydate.Value{}, false
}

func isDateLiteral(e *exp.Expr) bool {
	_, ok := extractDate(e)
	return ok
}

// extractInterval mirrors simplify.extract_interval.
func extractInterval(e *exp.Expr) (pydate.RelDelta, bool) {
	v, err := e.This().ToPy()
	if err != nil {
		return pydate.RelDelta{}, false
	}
	n, ok := pyToInt(v)
	if !ok {
		return pydate.RelDelta{}, false
	}
	unit := strings.ToLower(e.Text("unit"))
	d, ok := intervalDelta(unit, n)
	return d, ok
}

// pyToInt mirrors helper.to_int for literal values.
func pyToInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case *big.Int:
		if x.IsInt64() {
			return int(x.Int64()), true
		}
	case string:
		d, ok := pydec.Parse(x)
		if !ok {
			return 0, false
		}
		r := d.Rat()
		if !r.IsInt() || !r.Num().IsInt64() {
			return 0, false
		}
		return int(r.Num().Int64()), true
	case exp.Decimal:
		if x.Rat.IsInt() && x.Rat.Num().IsInt64() {
			return int(x.Rat.Num().Int64()), true
		}
	}
	return 0, false
}

func isInterval(e *exp.Expr) bool {
	if !e.IsKind(exp.Interval) {
		return false
	}
	_, ok := extractInterval(e)
	return ok
}

func isExactIntervalMove(op, literal, interval *exp.Expr) bool {
	delta, ok1 := extractInterval(interval)
	value, ok2 := extractDate(literal)
	if !ok1 || !ok2 {
		return false
	}
	if delta.Months == 0 && delta.Years == 0 {
		return true
	}
	if op.IsAny(exp.Sub, exp.DateSub, exp.DatetimeSub) {
		delta = delta.Neg()
	}
	moved, ok := delta.SubFrom(value)
	if !ok {
		abortSimplify("OverflowError")
	}
	// Exact iff `moved` is the only x with x + delta = value: it must map back onto `value`, and the
	// next day must not, since clamping folds the last days of a longer month onto the same date
	back, ok := delta.AddTo(moved)
	if !ok {
		abortSimplify("OverflowError")
	}
	next, ok := delta.AddTo(moved.AddDays(1))
	if !ok {
		abortSimplify("OverflowError")
	}
	return back.Equal(value) && !next.Equal(value)
}

// extractType mirrors simplify.extract_type.
func extractType(es ...*exp.Expr) *exp.Expr {
	var targetType *exp.Expr
	for _, e := range es {
		if e.IsKind(exp.Cast) {
			targetType = e.To()
		} else {
			targetType = e.Type()
		}
		if targetType != nil {
			break
		}
	}
	return targetType
}

// dateLiteral mirrors simplify.date_literal.
func dateLiteral(d pydate.Value, targetType *exp.Expr) *exp.Expr {
	var to any = targetType
	if targetType == nil || !targetType.IsTypeIn(exp.DataTypeTemporalTypes) {
		to = iff(d.IsDatetime, exp.DTypeDatetime, exp.DTypeDate)
	}
	return exp.NewCast(exp.LiteralString(d.String()), to)
}

// intervalDelta mirrors simplify.interval(unit, n); ok is false for UnsupportedUnit.
func intervalDelta(unit string, n int) (pydate.RelDelta, bool) {
	switch unit {
	case "year":
		return pydate.NewRelDelta(n, 0, 0, 0, 0, 0, 0, 0), true
	case "quarter":
		return pydate.NewRelDelta(0, 3*n, 0, 0, 0, 0, 0, 0), true
	case "month":
		return pydate.NewRelDelta(0, n, 0, 0, 0, 0, 0, 0), true
	case "week":
		return pydate.NewRelDelta(0, 0, n, 0, 0, 0, 0, 0), true
	case "day":
		return pydate.NewRelDelta(0, 0, 0, n, 0, 0, 0, 0), true
	case "hour":
		return pydate.NewRelDelta(0, 0, 0, 0, n, 0, 0, 0), true
	case "minute":
		return pydate.NewRelDelta(0, 0, 0, 0, 0, n, 0, 0), true
	case "second":
		return pydate.NewRelDelta(0, 0, 0, 0, 0, 0, n, 0), true
	case "millisecond":
		return pydate.NewRelDelta(0, 0, 0, 0, 0, 0, 0, 1000*n), true
	case "microsecond":
		return pydate.NewRelDelta(0, 0, 0, 0, 0, 0, 0, n), true
	}
	return pydate.RelDelta{}, false
}

func mustInterval(unit string, n int) pydate.RelDelta {
	d, ok := intervalDelta(unit, n)
	if !ok {
		abortSimplify("Unsupported unit: %s", unit)
	}
	return d
}

func mustAddInterval(v pydate.Value, unit string, n int) pydate.Value {
	r, ok := mustInterval(unit, n).AddTo(v)
	if !ok {
		abortSimplify("OverflowError")
	}
	return r
}

// datetimeFloor mirrors simplify.datetime_floor.
func datetimeFloor(d pydate.Value, unit string, dialect *Dialect) pydate.Value {
	// Truncate sub-day units — only valid for datetime inputs
	if d.IsDatetime {
		switch unit {
		case "hour":
			return d.Replace(-1, -1, -1, 0, 0, 0)
		case "minute":
			return d.Replace(-1, -1, -1, -1, 0, 0)
		case "second":
			return d.Replace(-1, -1, -1, -1, -1, 0)
		case "millisecond":
			return d.Replace(-1, -1, -1, -1, -1, (d.T.Nanosecond()/1000/1000)*1000)
		case "microsecond":
			return d
		}
	}
	var result pydate.Value
	month := int(d.T.Month())
	switch unit {
	case "year":
		result = d.Replace(1, 1, -1, -1, -1, -1)
	case "quarter":
		switch {
		case month <= 3:
			result = d.Replace(1, 1, -1, -1, -1, -1)
		case month <= 6:
			result = d.Replace(4, 1, -1, -1, -1, -1)
		case month <= 9:
			result = d.Replace(7, 1, -1, -1, -1, -1)
		default:
			result = d.Replace(10, 1, -1, -1, -1, -1)
		}
	case "month":
		result = d.Replace(month, 1, -1, -1, -1, -1)
	case "week":
		// Week truncation respects dialect.WEEK_OFFSET (0=Monday, -1=Sunday)
		offset := ((d.Weekday()-dialect.WeekOffset)%7 + 7) % 7
		result = d.AddDays(-offset)
	case "day":
		result = d
	default:
		abortSimplify("Unsupported unit: %s", unit)
	}
	// For datetime inputs, zero out the time component after date-level truncation
	if result.IsDatetime {
		return result.Replace(-1, -1, 0, 0, 0, 0)
	}
	return result
}

func truncUnit(unit *exp.Expr, d *Dialect) string {
	if unit.IsKind(exp.WeekStart) {
		dow, ok := WeekStartDayToDow[strings.ToUpper(unit.Name())]
		if !ok || dow != WeekOffsetToDow(d.WeekOffset) {
			abortSimplify("Unsupported unit: %s", unit.SQL())
		}
		return "week"
	}
	return strings.ToLower(unit.Name())
}

func dateCeil(d pydate.Value, unit string, dialect *Dialect) pydate.Value {
	floor := datetimeFloor(d, unit, dialect)
	if floor.Equal(d) {
		return d
	}
	return mustAddInterval(floor, unit, 1)
}

// Simplifier mirrors simplify.Simplifier.
type Simplifier struct {
	D                      *Dialect
	AnnotateNewExpressions bool
	annotator              *TypeAnnotator
}

// NewSimplifier mirrors Simplifier(dialect=None, annotate_new_expressions=True).
func NewSimplifier(dialect any, annotateNewExpressions bool) *Simplifier {
	d := MustDialect(dialect)
	schema, err := EnsureSchema(nil, d)
	if err != nil {
		panic(err)
	}
	return &Simplifier{D: d, AnnotateNewExpressions: annotateNewExpressions,
		annotator: NewTypeAnnotator(schema, nil, nil, nil, false)}
}

var complementComparisons = map[exp.Kind]exp.Kind{
	exp.LT: exp.GTE, exp.GT: exp.LTE, exp.LTE: exp.GT, exp.GTE: exp.LT, exp.EQ: exp.NEQ, exp.NEQ: exp.EQ,
}

var complementSubqueryPredicates = map[exp.Kind]exp.Kind{exp.All: exp.Any, exp.Any: exp.All}

var inverseComparisons = map[exp.Kind]exp.Kind{exp.LT: exp.GT, exp.GT: exp.LT, exp.LTE: exp.GTE, exp.GTE: exp.LTE}

var inverseDateOps = map[exp.Kind]exp.Kind{
	exp.DateAdd: exp.Sub, exp.DateSub: exp.Add, exp.DatetimeAdd: exp.Sub, exp.DatetimeSub: exp.Add,
}

var inverseOps = map[exp.Kind]exp.Kind{
	exp.DateAdd: exp.Sub, exp.DateSub: exp.Add, exp.DatetimeAdd: exp.Sub, exp.DatetimeSub: exp.Add,
	exp.Add: exp.Sub, exp.Sub: exp.Add,
}

func isComparison(e *exp.Expr) bool {
	return e.IsAny(exp.LT, exp.LTE, exp.GT, exp.GTE, exp.EQ, exp.NEQ, exp.Is)
}

func isLtLte(e *exp.Expr) bool { return e.IsAny(exp.LT, exp.LTE) }

func isGtGte(e *exp.Expr) bool { return e.IsAny(exp.GT, exp.GTE) }

func isConnectorCombinable(e *exp.Expr) bool {
	return e.IsAny(exp.Boolean, exp.Literal, exp.Null) || isComparison(e)
}

func isSimplifiable(e *exp.Expr) bool {
	return e.IsAny(exp.Binary, exp.Func, exp.Lambda, exp.Predicate, exp.Unary)
}

// onChange mirrors the annotate_types_on_change decorator.
func (s *Simplifier) onChange(original, result *exp.Expr) *exp.Expr {
	if result == nil {
		return nil
	}
	if s.AnnotateNewExpressions && !original.Equal(result) {
		s.annotator.Clear()
		// We annotate this to ensure new children nodes are also annotated
		result = s.annotator.Annotate(result, false)
		// Whatever expression the original expression is transformed into needs to preserve
		// the original type, otherwise the simplification could result in a different schema
		result.SetType(original.Type())
	}
	return result
}

// whileChanging mirrors helper.while_changing.
func whileChanging(e *exp.Expr, fn func(*exp.Expr) *exp.Expr) *exp.Expr {
	for {
		start := e.Hash()
		e = fn(e)
		if e.Hash() == start {
			return e
		}
	}
}

// Simplify mirrors Simplifier.simplify.
func (s *Simplifier) Simplify(e *exp.Expr, constantPropagation, coalesceSimplification bool) *exp.Expr {
	var wheres, joins []*exp.Expr
	expression := e
	prune := func(n *exp.Expr) bool { return n.Is(exp.Condition) || exp.Truthy(n.MetaGet(simplifyFinal)) }
	for node := range e.Walk(true, prune) {
		if exp.Truthy(node.MetaGet(simplifyFinal)) {
			continue
		}
		// group by expressions cannot be simplified, for example
		// select x + 1 + 1 FROM y GROUP BY x + 1 + 1
		// the projection must exactly match the group by key
		if group := node.Get("group"); group != nil && node.IsAny(exp.Query, exp.DerivedTable) {
			groups := map[exp.ExprKey]bool{}
			for _, g := range group.Expressions() {
				groups[g.MapKey()] = true
			}
			group.Meta()[simplifyFinal] = true
			for _, sel := range node.Selects() {
				for n := range sel.Walk(true, nil) {
					if groups[n.MapKey()] {
						sel.Meta()[simplifyFinal] = true
						break
					}
				}
			}
			if having := node.Get("having"); having != nil {
				for n := range having.Walk(true, nil) {
					if groups[n.MapKey()] {
						having.Meta()[simplifyFinal] = true
						break
					}
				}
			}
		}
		switch {
		case node.Is(exp.Condition):
			simplified := whileChanging(node, func(x *exp.Expr) *exp.Expr {
				return s.simplify(x, constantPropagation, coalesceSimplification)
			})
			if node == expression {
				expression = simplified
			}
		case node.IsKind(exp.Where):
			wheres = append(wheres, node)
		case node.IsKind(exp.Join):
			// snowflake match_conditions have very strict ordering rules
			if match := node.Get("match_condition"); match != nil {
				match.Meta()[simplifyFinal] = true
			}
			joins = append(joins, node)
		}
	}
	for _, where := range wheres {
		if AlwaysTrue(where.This()) && !where.Parent().IsKind(exp.Filter) {
			where.Pop()
		}
	}
	for _, join := range joins {
		// Only an inner join can become a CROSS JOIN: a cross join is empty as soon as either
		// side is empty, whereas an outer join keeps the rows of its outer side.
		kind := join.TextUpper("kind")
		if AlwaysTrue(join.Get("on")) && !join.Bool("using") && !join.Bool("method") && join.TextUpper("side") == "" &&
			(kind == "" || kind == "INNER") {
			join.Get("on").Pop()
			join.Set("side", nil)
			join.Set("kind", "CROSS")
		}
	}
	return expression
}

func (s *Simplifier) simplify(expression *exp.Expr, constantPropagation, coalesceSimplification bool) *exp.Expr {
	type post struct {
		node, parent *exp.Expr
	}
	pre := []*exp.Expr{expression}
	var postStack []post

	for len(pre) > 0 {
		original := pre[len(pre)-1]
		pre = pre[:len(pre)-1]
		node := original
		if !isSimplifiable(node) {
			if node.Is(exp.Query) {
				s.Simplify(node, constantPropagation, coalesceSimplification)
			}
			continue
		}
		parent := node.Parent()
		root := node == expression

		node = s.RewriteBetween(node)
		node = s.UniqSort(node, root)
		node = s.AbsorbAndEliminate(node, root)
		node = s.SimplifyConcat(node)
		node = s.SimplifyConditionals(node)
		if constantPropagation {
			node = PropagateConstants(node, root)
		}
		if node != original {
			original.Replace(node)
		}
		children := node.IterExpressions()
		for i := len(children) - 1; i >= 0; i-- {
			if !exp.Truthy(children[i].MetaGet(simplifyFinal)) {
				pre = append(pre, children[i])
			}
		}
		postStack = append(postStack, post{node, parent})
	}

	var node *exp.Expr
	for len(postStack) > 0 {
		p := postStack[len(postStack)-1]
		postStack = postStack[:len(postStack)-1]
		original, parent := p.node, p.parent
		root := original == expression

		// Resets parent, arg_key, index pointers– this is needed because some of the
		// previous transformations mutate the AST, leading to an inconsistent state.
		original.FixParents()

		// Post-order transformations
		node = s.SimplifyNot(original)
		node = SimplifyFlatten(node)
		node = s.SimplifyConnectors(node, root)
		node = s.RemoveComplements(node, root)
		if coalesceSimplification {
			node = s.SimplifyCoalesce(node)
		}
		node.SetParent(parent)
		node = s.SimplifyLiterals(node, root)
		node = s.SimplifyEquality(node)
		node = SimplifyParens(node, s.D)
		node = s.SimplifyDatetrunc(node)
		node = s.SortComparison(node)
		node = s.SimplifyStartswith(node)
		if node != original {
			original.Replace(node)
		}
	}
	if node == nil {
		return expression
	}
	return node
}

// RewriteBetween mirrors Simplifier.rewrite_between: x BETWEEN a AND b -> x >= a AND x <= b.
func (s *Simplifier) RewriteBetween(e *exp.Expr) *exp.Expr {
	result := e
	if e.IsKind(exp.Between) {
		negate := e.Parent().IsKind(exp.Not)
		result = exp.NewAnd(
			exp.New(exp.GTE, "this", e.This().Copy(), "expression", e.Get("low")),
			exp.New(exp.LTE, "this", e.This().Copy(), "expression", e.Get("high")),
			exp.WithCopy(false),
		)
		if negate {
			result = exp.NewParen(result, exp.WithCopy(false))
		}
	}
	return s.onChange(e, result)
}

func nullAndTrue(parent *exp.Expr) *exp.Expr {
	return parenthesizeNestedConnector(exp.NewAnd(exp.NewNull(), exp.NewTrue(), exp.WithCopy(false)), parent)
}

// SimplifyNot mirrors Simplifier.simplify_not: NOT NULL -> NULL, NOT (a < b) -> a >= b, ...
func (s *Simplifier) SimplifyNot(e *exp.Expr) *exp.Expr {
	return s.onChange(e, s.simplifyNot(e))
}

func (s *Simplifier) simplifyNot(e *exp.Expr) *exp.Expr {
	if !e.IsKind(exp.Not) {
		return e
	}
	this := e.This()
	if IsNull(this) {
		return nullAndTrue(e.Parent())
	}
	if complement, ok := complementComparisons[this.Kind()]; ok {
		right := this.Expression()
		if right != nil {
			if csp, ok := complementSubqueryPredicates[right.Kind()]; ok {
				right = exp.New(csp, "this", right.This())
			}
		}
		return exp.NewParen(exp.New(complement, "this", this.This(), "expression", right), exp.WithCopy(false))
	}
	if this.IsKind(exp.Paren) {
		condition := this.Unnest()
		if condition.IsKind(exp.And) {
			return exp.NewParen(exp.NewOr(
				exp.NewNot(condition.Left(), exp.WithCopy(false)),
				exp.NewNot(condition.Right(), exp.WithCopy(false)),
				exp.WithCopy(false)), exp.WithCopy(false))
		}
		if condition.IsKind(exp.Or) {
			return exp.NewParen(exp.NewAnd(
				exp.NewNot(condition.Left(), exp.WithCopy(false)),
				exp.NewNot(condition.Right(), exp.WithCopy(false)),
				exp.WithCopy(false)), exp.WithCopy(false))
		}
		if IsNull(condition) {
			return nullAndTrue(e.Parent())
		}
	}
	if AlwaysTrue(this) {
		return exp.NewFalse()
	}
	if IsFalse(this) {
		return exp.NewTrue()
	}
	if this.IsKind(exp.Not) && s.D.SafeToEliminateDoubleNegation {
		inner := this.This()
		if inner.IsType(exp.DTypeBoolean) {
			// double negation: NOT NOT x -> x, if x is BOOLEAN type
			return inner
		}
	}
	return e
}

// SimplifyConnectors mirrors Simplifier.simplify_connectors.
func (s *Simplifier) SimplifyConnectors(e *exp.Expr, root bool) *exp.Expr {
	return s.onChange(e, s.simplifyConnectors(e, root))
}

func (s *Simplifier) simplifyConnectors(e *exp.Expr, root bool) *exp.Expr {
	simplifyConn := func(expression, left, right *exp.Expr) *exp.Expr {
		if expression.IsKind(exp.And) {
			if IsFalse(left) || IsFalse(right) {
				return exp.NewFalse()
			}
			if IsZero(left) || IsZero(right) {
				return exp.NewFalse()
			}
			if (IsNull(left) && IsNull(right)) || (IsNull(left) && AlwaysTrue(right)) || (AlwaysTrue(left) && IsNull(right)) {
				return exp.NewNull()
			}
			if AlwaysTrue(left) && AlwaysTrue(right) {
				return exp.NewTrue()
			}
			if AlwaysTrue(left) {
				return right
			}
			if AlwaysTrue(right) {
				return left
			}
			return s.SimplifyComparison(expression, left, right, false)
		} else if expression.IsKind(exp.Or) {
			if AlwaysTrue(left) || AlwaysTrue(right) {
				return exp.NewTrue()
			}
			if (IsNull(left) && IsNull(right)) || (IsNull(left) && AlwaysFalse(right)) || (AlwaysFalse(left) && IsNull(right)) {
				return exp.NewNull()
			}
			if IsFalse(left) {
				return right
			}
			if IsFalse(right) {
				return left
			}
			return s.SimplifyComparison(expression, left, right, true)
		}
		return nil
	}
	if e.Is(exp.Connector) {
		originalParent := e.Parent()
		e = s.flatSimplify(e, simplifyConn, root)
		// If we reduced a connector to, e.g., a column (t1 AND ... AND tn -> Tk), then we need
		// to ensure that the resulting type is boolean.
		if !e.IsAny(exp.Connector, exp.Boolean) && !e.IsType(exp.DTypeBoolean) {
			for {
				if originalParent.Is(exp.Connector) {
					break
				}
				if !originalParent.IsKind(exp.Paren) {
					e = e.And(exp.NewTrue(), exp.WithCopy(false))
					break
				}
				originalParent = originalParent.Parent()
			}
		}
	}
	return e
}

func (s *Simplifier) comparisonOperands(e *exp.Expr) (*exp.Expr, *exp.Expr) {
	return e.This(), e.Expression()
}

// SimplifyComparison mirrors Simplifier._simplify_comparison (it may return nil).
func (s *Simplifier) SimplifyComparison(expression, left, right *exp.Expr, or bool) *exp.Expr {
	result := s.simplifyComparison(expression, left, right, or)
	if result == nil {
		return nil
	}
	return s.onChange(expression, result)
}

func (s *Simplifier) simplifyComparison(expression, left, right *exp.Expr, or bool) *exp.Expr {
	if !(isComparison(left) && isComparison(right)) {
		return nil
	}
	for _, x := range []*exp.Expr{left, right} {
		if x.IsKind(exp.Is) && x.Bool("negate") {
			return nil
		}
	}
	ll, lr := s.comparisonOperands(left)
	rl, rr := s.comparisonOperands(right)
	largs := newExprSet(ll, lr)
	rargs := newExprSet(rl, rr)
	columns := newExprSet()
	for _, m := range largs.items {
		if rargs.has(m) && !isConstant(m) && m.Find(exp.Rand, exp.Randn) == nil {
			columns.add(m)
		}
	}
	matching := false
	for _, m := range largs.items {
		if rargs.has(m) {
			matching = true
		}
	}
	if !matching || len(columns.items) == 0 {
		return nil
	}
	l := largs.minus(columns).first()
	r := rargs.minus(columns).first()
	if l == nil || r == nil {
		return expression
	}
	var lv, rv cmpValue
	switch {
	case l.IsNumber() && r.IsNumber():
		a, ok1 := pyNumber(l.MustToPy())
		b, ok2 := pyNumber(r.MustToPy())
		if !ok1 || !ok2 {
			return nil
		}
		lv, rv = cmpValue{num: a}, cmpValue{num: b}
	case l.IsString() && r.IsString():
		a, b := l.Name(), r.Name()
		lv, rv = cmpValue{str: &a}, cmpValue{str: &b}
	default:
		a, ok := extractDate(l)
		if !ok {
			return nil
		}
		b, ok := extractDate(r)
		if !ok {
			return nil
		}
		// python won't compare date and datetime, but many engines will upcast
		a, b = castAsDatetime(a), castAsDatetime(b)
		lv, rv = cmpValue{date: &a}, cmpValue{date: &b}
	}

	var falseExpr *exp.Expr
	if left.MetaGet("nonnull") == true && right.MetaGet("nonnull") == true {
		falseExpr = exp.NewFalse()
	}
	cmp := func(a, b cmpValue) int {
		c, ok := a.compare(b)
		if !ok {
			panic(fmt.Errorf("TypeError: can't compare values"))
		}
		return c
	}
	type pair struct {
		e *exp.Expr
		v cmpValue
	}
	for _, perm := range [][2]pair{{{left, lv}, {right, rv}}, {{right, rv}, {left, lv}}} {
		a, av, b, bv := perm[0].e, perm[0].v, perm[1].e, perm[1].v
		if isLtLte(a) && isLtLte(b) {
			if (or && cmp(av, bv) > 0) || (!or && cmp(av, bv) <= 0) {
				return left
			}
			return right
		}
		if isGtGte(a) && isGtGte(b) {
			if (or && cmp(av, bv) < 0) || (!or && cmp(av, bv) >= 0) {
				return left
			}
			return right
		}
		if !or {
			switch {
			case a.IsKind(exp.LT) && isGtGte(b):
				if cmp(av, bv) <= 0 {
					return falseExpr
				}
			case a.IsKind(exp.GT) && isLtLte(b):
				if cmp(av, bv) >= 0 {
					return falseExpr
				}
			case a.IsKind(exp.EQ):
				switch {
				case b.IsKind(exp.LT):
					return iff(cmp(av, bv) >= 0, falseExpr, a)
				case b.IsKind(exp.LTE):
					return iff(cmp(av, bv) > 0, falseExpr, a)
				case b.IsKind(exp.GT):
					return iff(cmp(av, bv) <= 0, falseExpr, a)
				case b.IsKind(exp.GTE):
					return iff(cmp(av, bv) < 0, falseExpr, a)
				case b.IsKind(exp.NEQ):
					return iff(av.equal(bv), falseExpr, a)
				}
			}
		}
	}
	return nil
}

// exprSet is a Python set of expressions (structural equality) preserving insertion order.
type exprSet struct {
	items []*exp.Expr
	keys  map[exp.ExprKey]bool
}

func newExprSet(xs ...*exp.Expr) *exprSet {
	s := &exprSet{keys: map[exp.ExprKey]bool{}}
	for _, x := range xs {
		s.add(x)
	}
	return s
}

func (s *exprSet) add(x *exp.Expr) {
	k := x.MapKey()
	if !s.keys[k] {
		s.keys[k] = true
		s.items = append(s.items, x)
	}
}

func (s *exprSet) has(x *exp.Expr) bool { return s.keys[x.MapKey()] }

func (s *exprSet) minus(o *exprSet) *exprSet {
	n := newExprSet()
	for _, x := range s.items {
		if !o.has(x) {
			n.add(x)
		}
	}
	return n
}

func (s *exprSet) first() *exp.Expr {
	if len(s.items) == 0 {
		return nil
	}
	return s.items[0]
}

func (s *exprSet) properSubsetOf(o *exprSet) bool {
	if len(s.items) >= len(o.items) {
		return false
	}
	for _, x := range s.items {
		if !o.has(x) {
			return false
		}
	}
	return true
}

// RemoveComplements mirrors Simplifier.remove_complements: A AND NOT A -> FALSE (for non-null
// A), A OR NOT A -> TRUE.
func (s *Simplifier) RemoveComplements(e *exp.Expr, root bool) *exp.Expr {
	result := e
	if e.IsAny(exp.And, exp.Or) && (root || !e.SameParent()) {
		ops := newExprSet(e.Flatten(true)...)
		for _, op := range ops.items {
			if op.IsKind(exp.Not) && ops.has(op.This()) {
				if e.MetaGet("nonnull") == true {
					result = iff(e.IsKind(exp.And), exp.NewFalse(), exp.NewTrue())
					break
				}
			}
		}
	}
	return s.onChange(e, result)
}

// UniqSort mirrors Simplifier.uniq_sort: it sorts and deduplicates connector operands.
func (s *Simplifier) UniqSort(e *exp.Expr, root bool) *exp.Expr {
	return s.onChange(e, s.uniqSort(e, root))
}

func (s *Simplifier) uniqSort(e *exp.Expr, root bool) *exp.Expr {
	if !(e.Is(exp.Connector) && (root || !e.SameParent())) {
		return e
	}
	flattened := e.Flatten(true)
	type entry struct {
		sql string
		e   *exp.Expr
	}
	var arr []entry
	var deduped []entry
	var resultFunc func(args ...any) *exp.Expr
	if e.IsKind(exp.Xor) {
		resultFunc = exp.NewXor
		// Do not deduplicate XOR as A XOR A != A if A == True
		for _, x := range flattened {
			arr = append(arr, entry{Gen(x, false), x})
		}
	} else {
		resultFunc = iff(e.IsKind(exp.And), exp.NewAnd, exp.NewOr)
		index := map[string]int{}
		for _, x := range flattened {
			sql := Gen(x, false)
			if i, ok := index[sql]; ok {
				deduped[i].e = x
			} else {
				index[sql] = len(deduped)
				deduped = append(deduped, entry{sql, x})
			}
		}
		arr = deduped
	}
	// check if the operands are already sorted, if not sort them
	for i := 1; i < len(arr); i++ {
		if arr[i].sql < arr[i-1].sql {
			sorted := slices.Clone(arr)
			sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].sql < sorted[b].sql })
			args := []any{}
			for _, x := range sorted {
				args = append(args, x.e)
			}
			return resultFunc(append(args, exp.WithCopy(false))...)
		}
	}
	// we didn't have to sort but maybe we need to dedup
	if deduped != nil && len(deduped) < len(flattened) {
		uniqueOperand := flattened[0]
		if len(deduped) == 1 {
			return uniqueOperand.And(exp.NewTrue(), exp.WithCopy(false))
		}
		args := []any{}
		for _, x := range deduped {
			args = append(args, x.e)
		}
		return resultFunc(append(args, exp.WithCopy(false))...)
	}
	return e
}

type pairKey [2]exp.ExprKey

func newPairKey(a, b *exp.Expr) pairKey {
	ka, kb := a.MapKey(), b.MapKey()
	if kb.Hash < ka.Hash || (kb.Hash == ka.Hash && kb.Kind < ka.Kind) {
		ka, kb = kb, ka
	}
	return pairKey{ka, kb}
}

// AbsorbAndEliminate mirrors Simplifier.absorb_and_eliminate:
// absorption: A AND (A OR B) -> A, A OR (A AND B) -> A, ...
// elimination: (A AND B) OR (A AND NOT B) -> A, ...
func (s *Simplifier) AbsorbAndEliminate(e *exp.Expr, root bool) *exp.Expr {
	if e.IsAny(exp.And, exp.Or) && (root || !e.SameParent()) {
		kind := iff(e.IsKind(exp.And), exp.Or, exp.And)
		ops := e.Flatten(true)
		opSet := newExprSet()
		subops := map[exp.ExprKey][]*exprSet{}
		type pairEntry struct{ op, complement *exp.Expr }
		pairs := map[pairKey][]pairEntry{}

		for _, op := range ops {
			opSet.add(op)
			if !op.IsKind(kind) {
				// In cases like: A OR (A AND B)
				subops[op.MapKey()] = append(subops[op.MapKey()], newExprSet(op))
				continue
			}
			// In cases like: (A AND B) OR (A AND B AND C)
			subset := newExprSet(op.Flatten(true)...)
			for _, i := range subset.items {
				subops[i.MapKey()] = append(subops[i.MapKey()], subset)
			}
			operands := op.UnnestOperands()
			a, b := operands[0], operands[1]
			if a.IsKind(exp.Not) && a.This().MetaGet("nonnull") == true {
				k := newPairKey(a.This(), b)
				pairs[k] = append(pairs[k], pairEntry{op, b})
			}
			if b.IsKind(exp.Not) && b.This().MetaGet("nonnull") == true {
				k := newPairKey(a, b.This())
				pairs[k] = append(pairs[k], pairEntry{op, a})
			}
		}

		for _, op := range ops {
			if !op.IsKind(kind) {
				continue
			}
			operands := op.UnnestOperands()
			a, b := operands[0], operands[1]
			// Absorb
			if a.IsKind(exp.Not) && opSet.has(a.This()) && a.This().MetaGet("nonnull") == true {
				a.Replace(iff(kind == exp.And, exp.NewTrue(), exp.NewFalse()))
				continue
			}
			if b.IsKind(exp.Not) && opSet.has(b.This()) && b.This().MetaGet("nonnull") == true {
				b.Replace(iff(kind == exp.And, exp.NewTrue(), exp.NewFalse()))
				continue
			}
			superset := newExprSet(op.Flatten(true)...)
			absorbed := false
			for _, i := range superset.items {
				for _, subset := range subops[i.MapKey()] {
					if subset.properSubsetOf(superset) {
						absorbed = true
						break
					}
				}
				if absorbed {
					break
				}
			}
			if absorbed {
				op.Replace(iff(kind == exp.And, exp.NewFalse(), exp.NewTrue()))
				continue
			}
			// Eliminate
			for _, pe := range pairs[newPairKey(a, b)] {
				op.Replace(pe.complement)
				pe.op.Replace(pe.complement)
			}
		}
	}
	return s.onChange(e, e)
}

// SimplifyEquality mirrors Simplifier.simplify_equality: x + 1 = 3 -> x = 2, ...
func (s *Simplifier) SimplifyEquality(e *exp.Expr) *exp.Expr {
	return s.onChange(e, catchAbort(e, func() *exp.Expr { return s.simplifyEquality(e) }))
}

func (s *Simplifier) simplifyEquality(e *exp.Expr) *exp.Expr {
	if !isComparison(e) {
		return e
	}
	l, r := e.Left(), e.Right()
	if _, ok := inverseOps[l.Kind()]; !ok {
		return e
	}
	var aPred, bPred func(*exp.Expr) bool
	if r.IsNumber() {
		aPred, bPred = (*exp.Expr).IsNumber, (*exp.Expr).IsNumber
	} else if isDateLiteral(r) {
		aPred, bPred = isDateLiteral, isInterval
	} else {
		return e
	}
	var a, b *exp.Expr
	if _, ok := inverseDateOps[l.Kind()]; ok {
		a, b = l.This(), l.Interval()
	} else {
		a, b = l.Left(), l.Right()
	}
	if !aPred(a) && bPred(b) {
	} else if !aPred(b) && bPred(a) {
		if l.IsKind(exp.Sub) {
			k := e.Kind()
			if inv, ok := inverseComparisons[k]; ok {
				k = inv
			}
			return exp.New(k, "this", b, "expression", exp.New(exp.Sub, "this", a, "expression", r))
		}
		a, b = b, a
	} else {
		return e
	}
	if b.IsKind(exp.Interval) && !isExactIntervalMove(l, r, b) {
		return e
	}
	return exp.New(e.Kind(), "this", a, "expression", exp.New(inverseOps[l.Kind()], "this", r, "expression", b))
}

func isInverseDateOp(e *exp.Expr) bool {
	_, ok := inverseDateOps[e.Kind()]
	return ok
}

// SimplifyLiterals mirrors Simplifier.simplify_literals: constant folding of numbers, strings
// and dates.
func (s *Simplifier) SimplifyLiterals(e *exp.Expr, root bool) *exp.Expr {
	return s.onChange(e, catchAbort(e, func() *exp.Expr { return s.simplifyLiterals(e, root) }))
}

func (s *Simplifier) simplifyLiterals(e *exp.Expr, root bool) *exp.Expr {
	if e.Is(exp.Binary) && !e.Is(exp.Connector) {
		return s.flatSimplify(e, s.simplifyBinary, root)
	}
	if e.IsKind(exp.Neg) && e.This().IsKind(exp.Neg) {
		return e.This().This()
	}
	if isInverseDateOp(e) {
		if r := s.simplifyBinary(e, e.This(), e.Interval()); r != nil {
			return r
		}
		return e
	}
	return e
}

func (s *Simplifier) simplifyIntegerCast(x *exp.Expr) *exp.Expr {
	var this *exp.Expr
	if x.IsKind(exp.Cast) && x.This().IsKind(exp.Cast) {
		this = s.simplifyIntegerCast(x.This())
	} else {
		this = x.This()
	}
	if x.IsKind(exp.Cast) && this.IsInt() {
		if n, ok := pyNumber(this.MustToPy()); ok {
			to := x.To().DataTypeThis()
			// Remove the (up)cast from small (byte-sized) integers in predicates which is side-effect free
			if (n.Cmp(big.NewRat(-128, 1)) >= 0 && n.Cmp(big.NewRat(127, 1)) <= 0 && exp.DataTypeSignedIntegerTypes.Has(to)) ||
				(n.Sign() >= 0 && n.Cmp(big.NewRat(255, 1)) <= 0 && exp.DataTypeUnsignedIntegerTypes.Has(to)) {
				return this
			}
		}
	}
	return x
}

func (s *Simplifier) simplifyBinary(expression, a, b *exp.Expr) *exp.Expr {
	if isComparison(expression) {
		a = s.simplifyIntegerCast(a)
		b = s.simplifyIntegerCast(b)
	}
	if expression.IsKind(exp.Is) {
		var c *exp.Expr
		not := false
		if b.IsKind(exp.Not) {
			c, not = b.This(), true
		} else {
			c = b
		}
		if expression.Bool("negate") {
			not = !not
		}
		if IsNull(c) {
			if a.IsKind(exp.Literal) {
				return booleanLiteral(not)
			}
			if IsNull(a) {
				return booleanLiteral(!not)
			}
		}
	} else if expression.IsAny(exp.NullSafeEQ, exp.NullSafeNEQ, exp.PropertyEQ) {
		return nil
	} else if (IsNull(a) || IsNull(b)) && expression.Parent().IsKind(exp.If) {
		return exp.NewNull()
	}

	switch {
	case a.IsNumber() && b.IsNumber():
		na, ok1 := toPyNum(a.MustToPy())
		nb, ok2 := toPyNum(b.MustToPy())
		if !ok1 || !ok2 {
			return nil
		}
		if expression.IsKind(exp.Add) {
			return pyNumOp(na, nb, '+').literal()
		}
		if expression.IsKind(exp.Mul) {
			return pyNumOp(na, nb, '*').literal()
		}
		// We only simplify Sub, Div if a and b have the same parent because they're not associative
		if expression.IsKind(exp.Sub) {
			if a.Parent() == b.Parent() {
				return pyNumOp(na, nb, '-').literal()
			}
			return nil
		}
		if expression.IsKind(exp.Div) {
			// engines have differing int div behavior so intdiv is not safe
			if (na.isInt && nb.isInt) || a.Parent() != b.Parent() || nb.rat().Sign() == 0 {
				return nil
			}
			return pyNumOp(na, nb, '/').literal()
		}
		return evalBoolean(expression, cmpValue{num: na.rat()}, cmpValue{num: nb.rat()})
	case a.IsString() && b.IsString():
		x, y := a.Str("this"), b.Str("this")
		return evalBoolean(expression, cmpValue{str: &x}, cmpValue{str: &y})
	case isDateLiteral(a) && b.IsKind(exp.Interval):
		date, ok1 := extractDate(a)
		delta, ok2 := extractInterval(b)
		if ok1 && ok2 {
			if expression.IsAny(exp.Add, exp.DateAdd, exp.DatetimeAdd) {
				v, ok := delta.AddTo(date)
				if !ok {
					abortSimplify("OverflowError")
				}
				return dateLiteral(v, extractType(a))
			}
			if expression.IsAny(exp.Sub, exp.DateSub, exp.DatetimeSub) {
				v, ok := delta.SubFrom(date)
				if !ok {
					abortSimplify("OverflowError")
				}
				return dateLiteral(v, extractType(a))
			}
		}
	case a.IsKind(exp.Interval) && isDateLiteral(b):
		delta, ok1 := extractInterval(a)
		date, ok2 := extractDate(b)
		// you cannot subtract a date from an interval
		if ok1 && ok2 && expression.IsKind(exp.Add) {
			v, ok := delta.AddTo(date)
			if !ok {
				abortSimplify("OverflowError")
			}
			return dateLiteral(v, extractType(b))
		}
	case isDateLiteral(a) && isDateLiteral(b):
		if expression.Is(exp.Predicate) {
			x, _ := extractDate(a)
			y, _ := extractDate(b)
			return evalBoolean(expression, cmpValue{date: &x}, cmpValue{date: &y})
		}
	}
	return nil
}

// SimplifyCoalesce mirrors Simplifier.simplify_coalesce.
func (s *Simplifier) SimplifyCoalesce(e *exp.Expr) *exp.Expr {
	return s.onChange(e, s.simplifyCoalesce(e))
}

func (s *Simplifier) simplifyCoalesce(e *exp.Expr) *exp.Expr {
	// COALESCE(x) -> x
	if e.IsKind(exp.Coalesce) && (len(e.Expressions()) == 0 || isNonnullConstant(e.This())) &&
		// COALESCE is also used as a Spark partitioning hint
		!e.Parent().IsKind(exp.Hint) {
		return e.This()
	}
	if s.D.CoalesceComparisonNonStandard {
		return e
	}
	if !isComparison(e) {
		return e
	}
	var coalesce, other *exp.Expr
	if e.Left().IsKind(exp.Coalesce) {
		coalesce, other = e.Left(), e.Right()
	} else if e.Right().IsKind(exp.Coalesce) {
		coalesce, other = e.Right(), e.Left()
	} else {
		return e
	}
	// This transformation is valid for non-constants, but it really only does anything if they
	// are both constants.
	if !isConstant(other) {
		return e
	}
	// Find the first non-NULL constant arg
	argIndex := -1
	var arg *exp.Expr
	for i, x := range coalesce.Expressions() {
		if isNonnullConstant(x) {
			argIndex, arg = i, x
			break
		}
	}
	if argIndex < 0 {
		return e
	}
	coalesce.Set("expressions", slices.Clone(coalesce.Expressions()[:argIndex]))
	// Remove the COALESCE function. This is an optimization, skipping a simplify iteration,
	// since we already remove COALESCE at the top of this function.
	this := coalesce
	if len(coalesce.Expressions()) == 0 {
		this = coalesce.This()
	}
	// The constant takes the COALESCE's side of the comparison
	substituted := e.Copy()
	substituted.Set(iff(coalesce == e.Left(), "this", "expression"), arg.Copy())
	// This expression is more complex than when we started, but it will get simplified further
	return exp.NewParen(exp.NewOr(
		exp.NewAnd(this.IsOp(exp.NewNull()).Not(exp.WithCopy(false)), e.Copy(), exp.WithCopy(false)),
		exp.NewAnd(this.IsOp(exp.NewNull()), substituted, exp.WithCopy(false)),
		exp.WithCopy(false)), exp.WithCopy(false))
}

// SimplifyConcat mirrors Simplifier.simplify_concat: it reduces all groups that contain
// string literals by concatenating them.
func (s *Simplifier) SimplifyConcat(e *exp.Expr) *exp.Expr {
	return s.onChange(e, s.simplifyConcat(e))
}

func (s *Simplifier) simplifyConcat(e *exp.Expr) *exp.Expr {
	if !e.IsAny(exp.Concat, exp.DPipe) || (e.IsKind(exp.ConcatWs) && !e.Expressions()[0].IsString()) {
		return e
	}
	var sepExpr *exp.Expr
	var expressions []*exp.Expr
	sep := ""
	concatType := exp.Concat
	if e.IsKind(exp.ConcatWs) {
		sepExpr, expressions = e.Expressions()[0], e.Expressions()[1:]
		sep = sepExpr.Name()
		concatType = exp.ConcatWs
	} else {
		expressions = e.Expressions()
	}
	safe, coalesce := e.Arg("safe"), e.Arg("coalesce")
	items := expressions
	if len(items) == 0 {
		items = e.Flatten(false)
	}
	var newArgs []*exp.Expr
	for i := 0; i < len(items); {
		isString := items[i].IsString()
		j := i
		for j < len(items) && items[j].IsString() == isString {
			j++
		}
		if isString {
			var parts []string
			for _, x := range items[i:j] {
				parts = append(parts, x.Name())
			}
			newArgs = append(newArgs, exp.LiteralString(strings.Join(parts, sep)))
		} else {
			newArgs = append(newArgs, items[i:j]...)
		}
		i = j
	}
	if len(newArgs) == 1 && newArgs[0].IsString() {
		return newArgs[0]
	}
	if concatType == exp.ConcatWs {
		newArgs = append([]*exp.Expr{sepExpr}, newArgs...)
	} else if e.IsKind(exp.DPipe) {
		result := newArgs[0]
		for _, y := range newArgs[1:] {
			result = exp.New(exp.DPipe, "this", result, "expression", y, "safe", safe)
		}
		return result
	}
	return exp.New(concatType, "expressions", newArgs, "safe", safe, "coalesce", coalesce)
}

// SimplifyConditionals mirrors Simplifier.simplify_conditionals: it simplifies expressions
// like IF and CASE.
func (s *Simplifier) SimplifyConditionals(e *exp.Expr) *exp.Expr {
	return s.onChange(e, s.simplifyConditionals(e))
}

func (s *Simplifier) simplifyConditionals(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Case) {
		this := e.This()
		// Iterate the live list like Python does: popping an element skips the next one
		for i := 0; i < len(e.List("ifs")); i++ {
			c := e.List("ifs")[i]
			cond := c.This()
			if this != nil {
				// Convert CASE x WHEN matching_value ... to CASE WHEN x = matching_value ...
				cond = cond.Replace(this.Pop().Eq(cond))
			}
			if AlwaysTrue(cond) {
				if c.Index() == 0 {
					return exp.NewParen(c.Get("true"), exp.WithCopy(false))
				}
				e.Set("default", c.Get("true"))
				e.Set("ifs", slices.Clone(e.List("ifs")[:c.Index()]))
				break
			}
			if AlwaysFalse(cond) {
				c.Pop()
				if len(e.List("ifs")) == 0 {
					def := e.Get("default")
					if def == nil {
						def = exp.NewNull()
					}
					return exp.NewParen(def, exp.WithCopy(false))
				}
			}
		}
	} else if e.IsKind(exp.If) && !e.Parent().IsKind(exp.Case) {
		if AlwaysTrue(e.This()) {
			return exp.NewParen(e.Get("true"), exp.WithCopy(false))
		}
		if AlwaysFalse(e.This()) {
			f := e.Get("false")
			if f == nil {
				f = exp.NewNull()
			}
			return exp.NewParen(f, exp.WithCopy(false))
		}
	}
	return e
}

// SimplifyStartswith mirrors Simplifier.simplify_startswith: STARTSWITH('foo', 'f') -> TRUE.
func (s *Simplifier) SimplifyStartswith(e *exp.Expr) *exp.Expr {
	result := e
	if e.IsKind(exp.StartsWith) && e.This().IsString() && e.Expression().IsString() {
		result = exp.ConvertValue(strings.HasPrefix(e.Name(), e.Expression().Name()), true)
	}
	return s.onChange(e, result)
}

func isDatetruncPredicate(left, right *exp.Expr) bool {
	return left.IsAny(exp.DateTrunc, exp.TimestampTrunc) && isDateLiteral(right)
}

// SimplifyDatetrunc mirrors Simplifier.simplify_datetrunc: it simplifies expressions like
// DATE_TRUNC('year', x) >= CAST('2021-01-01' AS DATE).
func (s *Simplifier) SimplifyDatetrunc(e *exp.Expr) *exp.Expr {
	return s.onChange(e, catchAbort(e, func() *exp.Expr { return s.simplifyDatetrunc(e) }))
}

func (s *Simplifier) simplifyDatetrunc(e *exp.Expr) *exp.Expr {
	comparison := e.Kind()
	isTruncComparison := comparison == exp.In || comparison == exp.LT || comparison == exp.GT ||
		comparison == exp.LTE || comparison == exp.GTE || comparison == exp.EQ || comparison == exp.NEQ
	if e.IsAny(exp.DateTrunc, exp.TimestampTrunc) {
		this := e.This()
		var truncType *exp.Expr
		if e.IsTypeIn(exp.DataTypeTemporalTypes) {
			truncType = e.Type()
		} else {
			truncType = extractType(this)
		}
		if date, ok := extractDate(this); ok && e.Unit() != nil {
			return dateLiteral(datetimeFloor(date, truncUnit(e.Unit(), s.D), s.D), truncType)
		}
	} else if !isTruncComparison {
		return e
	}

	if e.Is(exp.Binary) {
		l, r := e.Left(), e.Right()
		if !isDatetruncPredicate(l, r) || !l.IsAny(exp.DateTrunc, exp.TimestampTrunc) {
			return e
		}
		truncArg := l.This()
		unit := truncUnit(l.Get("unit"), s.D)
		date, ok := extractDate(r)
		if !ok {
			return e
		}
		targetType := extractType(r)
		var simplified *exp.Expr
		switch comparison {
		case exp.LT:
			floor := datetimeFloor(date, unit, s.D)
			v := date
			if !date.Equal(floor) {
				v = mustAddInterval(floor, unit, 1)
			}
			simplified = truncArg.Lt(dateLiteral(v, targetType))
		case exp.GT:
			simplified = truncArg.Gte(dateLiteral(mustAddInterval(datetimeFloor(date, unit, s.D), unit, 1), targetType))
		case exp.LTE:
			simplified = truncArg.Lt(dateLiteral(mustAddInterval(datetimeFloor(date, unit, s.D), unit, 1), targetType))
		case exp.GTE:
			simplified = truncArg.Gte(dateLiteral(dateCeil(date, unit, s.D), targetType))
		case exp.EQ:
			if drange, ok := s.datetruncRange(date, unit); ok {
				simplified = datetruncEqExpression(truncArg, drange, targetType)
			}
		case exp.NEQ:
			if drange, ok := s.datetruncRange(date, unit); ok {
				simplified = exp.NewOr(truncArg.Lt(dateLiteral(drange[0], targetType)),
					truncArg.Gte(dateLiteral(drange[1], targetType)), exp.WithCopy(false))
			}
		default:
			return e
		}
		if simplified == nil {
			return e
		}
		return parenthesizeNestedConnector(simplified, e.Parent())
	}

	if e.IsKind(exp.In) {
		l := e.This()
		rs := e.Expressions()
		all := len(rs) > 0
		for _, r := range rs {
			if !isDatetruncPredicate(l, r) {
				all = false
				break
			}
		}
		if all && l.IsAny(exp.DateTrunc, exp.TimestampTrunc) {
			unit := truncUnit(l.Get("unit"), s.D)
			var ranges []dateRange
			for _, r := range rs {
				date, ok := extractDate(r)
				if !ok {
					return e
				}
				if drange, ok := s.datetruncRange(date, unit); ok {
					ranges = append(ranges, drange)
				}
			}
			if len(ranges) == 0 {
				return e
			}
			ranges = mergeDateRanges(ranges)
			targetType := extractType(rs...)
			var ors []any
			for _, drange := range ranges {
				ors = append(ors, datetruncEqExpression(l, drange, targetType))
			}
			simplified := exp.NewOr(append(ors, exp.WithCopy(false))...)
			return parenthesizeNestedConnector(simplified, e.Parent())
		}
	}
	return e
}

// mergeDateRanges mirrors helper.merge_ranges for date ranges.
func mergeDateRanges(ranges []dateRange) []dateRange {
	less := func(a, b pydate.Value) bool {
		c, ok := a.Compare(b)
		if !ok {
			panic(fmt.Errorf("TypeError: can't compare dates"))
		}
		return c < 0
	}
	sorted := slices.Clone(ranges)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i][0].Equal(sorted[j][0]) {
			return less(sorted[i][0], sorted[j][0])
		}
		return less(sorted[i][1], sorted[j][1])
	})
	merged := []dateRange{sorted[0]}
	for _, r := range sorted[1:] {
		last := &merged[len(merged)-1]
		if !less(last[1], r[0]) {
			if less(last[1], r[1]) {
				last[1] = r[1]
			}
		} else {
			merged = append(merged, r)
		}
	}
	return merged
}

// SortComparison mirrors Simplifier.sort_comparison: it puts columns on the left side of
// comparisons.
func (s *Simplifier) SortComparison(e *exp.Expr) *exp.Expr {
	result := e
	if _, ok := complementComparisons[e.Kind()]; ok {
		l, r := e.This(), e.Expression()
		lColumn, rColumn := l.IsKind(exp.Column), r.IsKind(exp.Column)
		lConst, rConst := isConstant(l), isConstant(r)
		if (lColumn && !rColumn) || (rConst && !lConst) || r.Is(exp.SubqueryPredicate) {
			return s.onChange(e, e)
		}
		if (rColumn && !lColumn) || (lConst && !rConst) || Gen(l, false) > Gen(r, false) {
			k := e.Kind()
			if inv, ok := inverseComparisons[k]; ok {
				k = inv
			}
			result = exp.New(k, "this", r, "expression", l)
		}
	}
	return s.onChange(e, result)
}

func (s *Simplifier) flatSimplify(e *exp.Expr, simplifier func(expression, a, b *exp.Expr) *exp.Expr, root bool) *exp.Expr {
	if !(root || !e.SameParent()) {
		return e
	}
	var operands []*exp.Expr
	queue := e.Flatten(false)
	size := len(queue)
	// The pairwise scan below is O(n^2). For connectors, a pair only combines if one side is a
	// constant or both are comparisons; if no operand is combinable the scan is a no-op.
	if e.Is(exp.Connector) && !slices.ContainsFunc(queue, isConnectorCombinable) {
		return e
	}
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		combined := false
		for i, b := range queue {
			result := simplifier(e, a, b)
			if result != nil && result != e {
				// queue.remove(b) removes the first structurally equal element
				idx := slices.IndexFunc(queue, b.Equal)
				if idx < 0 {
					idx = i
				}
				queue = slices.Delete(slices.Clone(queue), idx, idx+1)
				queue = append([]*exp.Expr{result}, queue...)
				combined = true
				break
			}
		}
		if !combined {
			operands = append(operands, a)
		}
	}
	if len(operands) < size {
		result := operands[0]
		for _, b := range operands[1:] {
			result = exp.New(e.Kind(), "this", result, "expression", b)
		}
		return result
	}
	return e
}

// Gen mirrors simplify.gen: a simple, fast, canonical SQL-like string for an expression (used
// for sorting and deduplication).
func Gen(e *exp.Expr, comments bool) string {
	g := &simplifyGen{}
	return g.gen(e, comments)
}

type simplifyGen struct {
	stack []any
	sqls  []string
}

type genList []any

func (g *simplifyGen) push(xs ...any) { g.stack = append(g.stack, xs...) }

func (g *simplifyGen) gen(e *exp.Expr, comments bool) string {
	g.stack = []any{e}
	g.sqls = nil
	for len(g.stack) > 0 {
		node := g.stack[len(g.stack)-1]
		g.stack = g.stack[:len(g.stack)-1]
		switch x := node.(type) {
		case *exp.Expr:
			if x == nil {
				continue
			}
			if comments && len(x.Comments) > 0 {
				g.push(" /*" + strings.Join(x.Comments, ",") + "*/")
			}
			if !g.dispatch(x) {
				if x.Is(exp.Func) {
					g.function(x)
				} else {
					key := strings.ToUpper(x.Key())
					if g.args(x, 0) {
						g.push(key + " ")
					} else {
						g.push(key)
					}
				}
			}
		case genList:
			for i := len(x) - 1; i >= 0; i-- {
				if x[i] != nil {
					g.push(x[i], ",")
				}
			}
			if len(x) > 0 {
				g.stack = g.stack[:len(g.stack)-1]
			}
		case []*exp.Expr:
			l := make(genList, len(x))
			for i, y := range x {
				l[i] = y
			}
			g.push(l)
		case []any:
			g.push(genList(x))
		case []string:
			l := make(genList, len(x))
			for i, y := range x {
				l[i] = y
			}
			g.push(l)
		case nil:
		default:
			g.sqls = append(g.sqls, pyStr(x))
		}
	}
	return strings.Join(g.sqls, "")
}

func (g *simplifyGen) binary(e *exp.Expr, op string) {
	g.push(e.Arg("expression"), op, e.Arg("this"))
}

func (g *simplifyGen) dispatch(e *exp.Expr) bool {
	switch e.Kind() {
	case exp.Add:
		g.binary(e, " + ")
	case exp.Alias:
		g.push(e.Arg("alias"), " AS ", e.Arg("this"))
	case exp.And:
		g.binary(e, " AND ")
	case exp.Anonymous:
		var name string
		switch this := e.Arg("this").(type) {
		case string:
			name = strings.ToUpper(this)
		case *exp.Expr:
			if this.IsKind(exp.Identifier) {
				name = this.Str("this")
				if this.Quoted() {
					name = `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
				} else {
					name = strings.ToUpper(name)
				}
			} else {
				panic(fmt.Errorf("Anonymous.this expects a str or an Identifier, got '%s'.", this.ClassName()))
			}
		}
		g.push(")", e.Arg("expressions"), "(", name)
	case exp.Between:
		g.push(e.Arg("high"), " AND ", e.Arg("low"), " BETWEEN ", e.Arg("this"))
	case exp.Boolean:
		g.push(iff(e.Bool("this"), "TRUE", "FALSE"))
	case exp.Bracket:
		g.args(e, 2)
		g.push("]", e.Arg("expressions"), "[", e.Arg("this"))
	case exp.Column:
		parts := e.Parts()
		for i := len(parts) - 1; i >= 0; i-- {
			g.push(parts[i], ".")
		}
		g.stack = g.stack[:len(g.stack)-1]
	case exp.DataType:
		g.args(e, 1)
		g.push(e.DataTypeThis().Name() + " ")
	case exp.Div:
		g.binary(e, " / ")
	case exp.Dot:
		g.binary(e, ".")
	case exp.EQ:
		g.binary(e, " = ")
	case exp.From:
		g.push(e.Arg("this"), "FROM ")
	case exp.GT:
		g.binary(e, " > ")
	case exp.GTE:
		g.binary(e, " >= ")
	case exp.Identifier:
		if e.Quoted() {
			g.push(`"` + strings.ReplaceAll(e.Str("this"), `"`, `""`) + `"`)
		} else {
			g.push(e.Str("this"))
		}
	case exp.ILike:
		g.binary(e, iff(e.Bool("negate"), " NOT ILIKE ", " ILIKE "))
	case exp.In:
		g.push(")")
		g.args(e, 1)
		g.push("(", " IN ", e.Arg("this"))
	case exp.IntDiv:
		g.binary(e, " DIV ")
	case exp.Is:
		g.binary(e, iff(e.Bool("negate"), " IS NOT ", " IS "))
	case exp.Like:
		g.binary(e, iff(e.Bool("negate"), " NOT Like ", " Like "))
	case exp.Literal:
		if e.IsString() {
			g.push("'" + strings.ReplaceAll(e.Str("this"), "'", "''") + "'")
		} else {
			g.push(e.Str("this"))
		}
	case exp.LT:
		g.binary(e, " < ")
	case exp.LTE:
		g.binary(e, " <= ")
	case exp.Mod:
		g.binary(e, " % ")
	case exp.Mul:
		g.binary(e, " * ")
	case exp.Neg:
		g.push(e.Arg("this"), "-")
	case exp.NEQ:
		g.binary(e, " <> ")
	case exp.Not:
		g.push(e.Arg("this"), "NOT ")
	case exp.Null:
		g.push("NULL")
	case exp.Or:
		g.binary(e, " OR ")
	case exp.Paren:
		g.push(")", e.Arg("this"), "(")
	case exp.Sub:
		g.binary(e, " - ")
	case exp.Subquery:
		g.args(e, 2)
		if alias := e.Arg("alias"); exp.Truthy(alias) {
			g.push(alias)
		}
		g.push(")", e.Arg("this"), "(")
	case exp.Table:
		g.args(e, 4)
		if alias := e.Arg("alias"); exp.Truthy(alias) {
			g.push(alias)
		}
		parts := e.Parts()
		for i := len(parts) - 1; i >= 0; i-- {
			g.push(parts[i], ".")
		}
		g.stack = g.stack[:len(g.stack)-1]
	case exp.TableAlias:
		if columns := e.Columns(); len(columns) > 0 {
			g.push(")", columns, "(")
		}
		g.push(e.Arg("this"), " AS ")
	case exp.Var:
		g.push(e.Arg("this"))
	default:
		return false
	}
	return true
}

func (g *simplifyGen) function(e *exp.Expr) {
	var values genList
	for _, a := range e.Args() {
		values = append(values, a.Value)
	}
	g.push(")", values, "(", e.Kind().SQLName())
}

func (g *simplifyGen) args(e *exp.Expr, argIndex int) bool {
	var kvs genList
	specs := e.Kind().Args()
	if argIndex > len(specs) {
		specs = nil
	} else {
		specs = specs[argIndex:]
	}
	for _, spec := range specs {
		v := e.Arg(spec.Key)
		if v == nil {
			continue
		}
		if s, ok := v.(string); ok {
			v = exp.PyRepr(s)
		}
		kvs = append(kvs, genList{":" + spec.Key, v})
	}
	if len(kvs) > 0 {
		g.push(kvs)
		return true
	}
	return false
}
