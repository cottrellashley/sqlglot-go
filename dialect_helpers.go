package sqlglot

// Generator and parser helpers translated from sqlglot/dialects/dialect.py (rename_func ..
// jarowinkler_similarity). Dialects compose these into their TRANSFORMS and FUNCTIONS tables.

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// flattenArgs mirrors flatten(expression.args.values()).
func flattenArgs(e *exp.Expr) []any {
	var out []any
	for _, a := range e.Args() {
		switch v := a.Value.(type) {
		case []*exp.Expr:
			for _, x := range v {
				out = append(out, x)
			}
		case []any:
			out = append(out, v...)
		case []string:
			for _, x := range v {
				out = append(out, x)
			}
		default:
			out = append(out, v)
		}
	}
	return out
}

// RenameFunc mirrors rename_func(name): NAME(<all args>).
func RenameFunc(name string) GenFunc {
	return func(g *Generator, e *exp.Expr) string { return g.Func(name, flattenArgs(e)...) }
}

func BracketToElementAtSQL(g *Generator, e *exp.Expr) string {
	index := seqGet(ApplyIndexOffset(e.This(), e.Expressions(), 1-e.Int("offset"), g.D), 0)
	return g.Func("ELEMENT_AT", e.This(), index)
}

func int32Range(v any) (int64, bool) {
	n, ok := pyIntValue(v)
	return n, ok && n >= 0 && n <= 1<<31-1
}

func ArraySliceToSliceSQL(g *Generator, e *exp.Expr) string {
	start := e.Get("start")
	end := e.Get("end")
	// Preserve unmarked slices and bounds that require runtime normalization or narrowing.
	if b, ok := e.Arg("is_length").(bool); ok && !b && e.Arg("step") == nil && start.IsInt() && end != nil && end.IsInt() {
		s, okS := int32Range(start.MustToPy())
		en, okE := int32Range(end.MustToPy())
		if okS && okE {
			startValue := max(s, 1)
			return g.Func("SLICE", e.This(), exp.LiteralNumber(startValue), exp.LiteralNumber(max(en-startValue+1, 0)))
		}
	}
	return RenameFunc("SLICE")(g, e)
}

var ApproxCountDistinctSQL = UnsupportedArgs(func(g *Generator, e *exp.Expr) string {
	return g.Func("APPROX_COUNT_DISTINCT", e.This())
}, "accuracy")

// IfSQL mirrors if_sql(name="IF", false_value=None).
func IfSQL(name string, falseValue any) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		f := e.Arg("false")
		if !exp.Truthy(f) {
			f = falseValue
		}
		return g.Func(name, e.This(), e.Arg("true"), f)
	}
}

// wrappedJSONPathExpression mirrors WRAPPED_JSON_PATH_EXPRESSIONS.
func wrappedJSONPathExpression(e *exp.Expr) bool {
	return e.IsAny(exp.Binary, exp.Predicate, exp.Not, exp.Concat)
}

// ArrowJSONExtractSQL mirrors arrow_json_extract_sql(self, expression, op=None).
func ArrowJSONExtractSQL(g *Generator, e *exp.Expr, op string) string {
	this := e.This()
	if g.C.JsonTypeRequiredForExtraction && this.IsKind(exp.Literal) && this.IsString() {
		this.Replace(exp.NewCast(this, exp.DTypeJson))
	}
	if wrappedJSONPathExpression(e.Expression()) {
		e.Set("expression", exp.NewParen(e.Expression(), exp.WithCopy(false)))
	}
	if op == "" {
		op = iff(e.IsKind(exp.JSONExtract), "->", "->>")
	}
	return g.Binary(e, op)
}

func InlineArraySQL(g *Generator, e *exp.Expr) string {
	return "[" + g.Expressions(e, ExprsOpts{Dynamic: true, NewLine: true, SkipFirst: true, SkipLast: true}) + "]"
}

func InlineArrayUnlessQuery(g *Generator, e *exp.Expr) string {
	elem := seqGet(e.Expressions(), 0)
	// value_constructor=True keeps scalar subqueries as array elements. In [(SELECT …)], the
	// subquery supplies one scalar value; in ARRAY(SELECT …) (BigQuery), the query supplies rows.
	if len(e.Expressions()) == 1 && elem.Is(exp.Query) && !e.Bool("value_constructor") {
		return g.Func("ARRAY", elem)
	}
	return InlineArraySQL(g, e)
}

func NoIlikeSQL(g *Generator, e *exp.Expr) string {
	return g.LikeSQL(exp.New(exp.Like,
		"this", exp.New(exp.Lower, "this", e.This()),
		"expression", exp.New(exp.Lower, "this", e.Expression()),
		"negate", e.Arg("negate")))
}

func NoParenCurrentDateSQL(g *Generator, e *exp.Expr) string {
	if zone := g.SQLArg(e, "this"); zone != "" {
		return "CURRENT_DATE AT TIME ZONE " + zone
	}
	return "CURRENT_DATE"
}

func NoRecursiveCteSQL(g *Generator, e *exp.Expr) string {
	if e.Bool("recursive") {
		g.Unsupported("Recursive CTEs are unsupported")
		e.Set("recursive", false)
	}
	return g.WithSQL(e)
}

func NoTablesampleSQL(g *Generator, e *exp.Expr) string {
	g.Unsupported("TABLESAMPLE unsupported")
	return g.SQL(e.This())
}

func NoPivotSQL(g *Generator, e *exp.Expr) string {
	g.Unsupported("PIVOT unsupported")
	return ""
}

func NoTrycastSQL(g *Generator, e *exp.Expr) string { return g.CastSQL(e, "") }

func NoCommentColumnConstraintSQL(g *Generator, e *exp.Expr) string {
	g.Unsupported("CommentColumnConstraint unsupported")
	return ""
}

func NoMapFromEntriesSQL(g *Generator, e *exp.Expr) string {
	g.Unsupported("MAP_FROM_ENTRIES unsupported")
	return ""
}

// isPercentile mirrors isinstance(x, exp.PERCENTILES).
func isPercentile(e *exp.Expr) bool {
	return e.IsAny(exp.PercentileCont, exp.PercentileDisc)
}

func NoFilterSQL(g *Generator, e *exp.Expr) string {
	// Rewrite `AGG(x) FILTER (WHERE cond)` into an equivalent conditional aggregation, i.e. wrap
	// the input values in an IF, which works because aggregate functions skip NULLs
	agg := e.This()
	// IGNORE NULLS can wrap the aggregate, but the condition still belongs to its arguments
	if agg.IsKind(exp.IgnoreNulls) {
		agg = agg.This()
	}
	var aggArg *exp.Expr
	if agg.IsKind(exp.Anonymous) {
		aggArg = seqGet(agg.Expressions(), 0)
	} else {
		aggArg = agg.This()
	}
	cond := e.Expression().This()

	if agg.IsKind(exp.WithinGroup) {
		// Ordered-set aggregates take their input from the ORDER BY key, so the
		// condition has to wrap that instead of the aggregate's own argument
		if aggArg.IsKind(exp.Mode) || isPercentile(aggArg) {
			for _, ordered := range agg.Expression().Expressions() {
				key := ordered.This()
				key.Replace(exp.New(exp.If, "this", cond.Copy(), "true", key.Copy(), "false", exp.NewNull()))
			}
			return g.SQL(e.This())
		}
		// Besides the ordered-set aggregates, these are the only functions that take WITHIN GROUP
		// and the input values as arguments, so anything else can't be rewritten correctly
		if aggArg.IsAny(exp.ArrayAgg, exp.GroupConcat) {
			aggArg = aggArg.This()
		} else {
			g.Unsupported("Unable to rewrite FILTER into the aggregate's arguments")
			return g.SQL(e.This())
		}
	}

	// Folding the condition into COUNT_IF's predicate avoids an IF that returns a predicate, which
	// dialects without a boolean type (e.g. T-SQL) reject
	if agg.IsKind(exp.CountIf) && !aggArg.IsKind(exp.Distinct) {
		return g.SQL(exp.New(exp.CountIf, "this", exp.NewAnd(cond, aggArg)))
	}

	// `COUNT(*/t.*) FILTER (WHERE cond)` counts qualifying rows, but a star can't be an IF
	// argument. Each dialect generates CountIf in its own way instead, e.g. COUNT_IF or COUNTIF.
	if agg.IsKind(exp.Count) && (aggArg == nil || aggArg.IsStar()) {
		return g.SQL(exp.New(exp.CountIf, "this", cond))
	}

	// `DISTINCT` and `ORDER BY` are part of the aggregate's own argument list, so the
	// condition has to wrap the values underneath them rather than the whole clause
	if aggArg.IsKind(exp.Order) {
		aggArg = aggArg.This()
	}
	var targets []*exp.Expr
	if aggArg.IsKind(exp.Distinct) {
		targets = aggArg.Expressions()
	} else {
		targets = []*exp.Expr{aggArg}
	}
	for _, target := range targets {
		target.Replace(exp.New(exp.If, "this", cond.Copy(), "true", target.Copy(), "false", exp.NewNull()))
	}

	// In dialects where ARRAY_AGG keeps NULLs (e.g. BigQuery, which even fails on them), the
	// rows that don't satisfy the condition have to be dropped explicitly
	if agg.IsKind(exp.ArrayAgg) && g.D.ArrayAggIncludesNulls != nil && *g.D.ArrayAggIncludesNulls {
		// IGNORE NULLS already drops them, so no extra FILTER (WHERE x IS NOT NULL) is needed
		agg.Set("nulls_excluded", nil)
		if agg == e.This() {
			return g.SQL(exp.New(exp.IgnoreNulls, "this", agg))
		}
	}
	return g.SQL(e.This())
}

// PropertySQL mirrors dialect.property_sql (string keys).
func PropertySQL(g *Generator, e *exp.Expr) string {
	return g.PropertyName(e, true) + "=" + g.SQLArg(e, "value")
}

// StrpositionSQL mirrors strposition_sql(self, expression, func_name="STRPOS",
// supports_position=False, supports_occurrence=False, use_ansi_position=True).
func StrpositionSQL(g *Generator, e *exp.Expr, funcName string, supportsPosition, supportsOccurrence, useAnsiPosition bool) string {
	str := e.This()
	substr := e.Get("substr")
	position := e.Get("position")
	occurrence := e.Get("occurrence")
	zero := exp.LiteralNumber(0)
	one := exp.LiteralNumber(1)

	if supportsOccurrence && occurrence != nil && supportsPosition && position == nil {
		position = one
	}
	transpilePosition := position != nil && !supportsPosition
	if transpilePosition {
		str = exp.New(exp.Substring, "this", str, "start", position)
	}
	var fn *exp.Expr
	if funcName == "POSITION" && useAnsiPosition {
		fn = exp.New(exp.Anonymous, "this", funcName, "expressions", []*exp.Expr{exp.New(exp.In, "this", substr, "field", str)})
	} else {
		var args []*exp.Expr
		if funcName == "LOCATE" || funcName == "CHARINDEX" {
			args = []*exp.Expr{substr, str}
		} else {
			args = []*exp.Expr{str, substr}
		}
		if supportsPosition {
			args = append(args, position)
		}
		if occurrence != nil {
			if supportsOccurrence {
				args = append(args, occurrence)
			} else {
				g.Unsupported(funcName + " does not support the occurrence parameter.")
			}
		}
		fn = exp.New(exp.Anonymous, "this", funcName, "expressions", nonNilList(args))
	}
	if transpilePosition {
		funcWithOffset := exp.New(exp.Sub, "this", fn.Plus(position), "expression", one)
		funcWrapped := exp.New(exp.If, "this", fn.Eq(zero), "true", zero, "false", funcWithOffset)
		return g.SQL(funcWrapped)
	}
	return g.SQL(fn)
}

func StructExtractSQL(g *Generator, e *exp.Expr) string {
	return g.SQLArg(e, "this") + "." + g.SQL(exp.ToIdentifier(e.Expression().Name()))
}

// ArrayAppendSQL mirrors array_append_sql(name, swap_params=False).
func ArrayAppendSQL(name string, swapParams bool) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		this := e.This()
		element := e.Expression()
		args := func(this *exp.Expr) []any {
			if swapParams {
				return []any{element, this}
			}
			return []any{this, element}
		}
		funcSQL := g.Func(name, args(this)...)
		sourceNullPropagation := e.Bool("null_propagation")
		targetNullPropagation := g.D.ArrayFuncsPropagatesNulls
		// No transpilation needed when source and target have matching NULL semantics
		if sourceNullPropagation == targetNullPropagation {
			return funcSQL
		}
		// Source propagates NULLs, target doesn't: wrap in conditional to return NULL explicitly
		if sourceNullPropagation {
			return g.SQL(exp.New(exp.If,
				"this", exp.New(exp.Is, "this", this, "expression", exp.NewNull()),
				"true", exp.NewNull(),
				"false", funcSQL))
		}
		// Source doesn't propagate NULLs, target does: use COALESCE to convert NULL to empty array
		this = exp.New(exp.Coalesce, "expressions", []*exp.Expr{this, exp.New(exp.Array, "expressions", []*exp.Expr{})})
		return g.Func(name, args(this)...)
	}
}

// GenerateSeriesSQL mirrors generate_series_sql(func_name, exclusive_func_name=None).
func GenerateSeriesSQL(funcName, exclusiveFuncName string) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		start, end, step := e.Get("start"), e.Get("end"), e.Get("step")
		if e.Bool("is_end_exclusive") {
			if exclusiveFuncName != "" {
				return g.Func(exclusiveFuncName, start, end, step)
			}
			// The last value is one before the end in the direction of the step
			var adjustedEnd *exp.Expr
			if n, ok := intLiteralValue(step); ok && n < 0 {
				adjustedEnd = exp.New(exp.Add, "this", end, "expression", exp.LiteralNumber(1))
			} else {
				adjustedEnd = exp.New(exp.Sub, "this", end, "expression", exp.LiteralNumber(1))
			}
			return g.Func(funcName, start, adjustedEnd, step)
		}
		return g.Func(funcName, start, end, step)
	}
}

// intLiteralValue returns the value of an integer literal (e.is_int and e.to_py()).
func intLiteralValue(e *exp.Expr) (int64, bool) {
	if e == nil || !e.IsInt() {
		return 0, false
	}
	v, err := e.ToPy()
	if err != nil {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return int64(x), true
	case *big.Int:
		if x.IsInt64() {
			return x.Int64(), true
		}
	}
	return 0, false
}

// ArrayConcatSQL mirrors array_concat_sql(name).
func ArrayConcatSQL(name string) GenFunc {
	buildFuncCall := func(g *Generator, funcName string, args []*exp.Expr) string {
		if g.C.ArrayConcatIsVarLen {
			return g.Func(funcName, args)
		}
		if len(args) == 1 {
			// Single arg gets empty array to preserve semantics
			return g.Func(funcName, args[0], exp.New(exp.Array, "expressions", []*exp.Expr{}))
		}
		// Snowflake/PostgreSQL/Redshift require binary nesting: ARRAY_CAT(a, ARRAY_CAT(b, c))
		result := g.Func(funcName, args[len(args)-2], args[len(args)-1])
		for i := len(args) - 3; i >= 0; i-- {
			result = funcName + "(" + g.SQL(args[i]) + ", " + result + ")"
		}
		return result
	}
	return func(g *Generator, e *exp.Expr) string {
		this := e.This()
		exprs := e.Expressions()
		allArgs := append([]*exp.Expr{this}, exprs...)
		sourceNullPropagation := e.Bool("null_propagation")
		targetNullPropagation := g.D.ArrayFuncsPropagatesNulls
		// Skip wrapper when source and target have matching NULL semantics, or when the first
		// argument is an array literal (which can never be NULL), or for single-argument calls
		if sourceNullPropagation == targetNullPropagation || this.IsKind(exp.Array) || len(exprs) == 0 {
			return buildFuncCall(g, name, allArgs)
		}
		// Case 1: Source propagates NULLs, target doesn't (Snowflake → DuckDB)
		if sourceNullPropagation {
			var combined *exp.Expr
			for _, arg := range allArgs {
				check := exp.New(exp.Is, "this", arg.Copy(), "expression", exp.NewNull())
				if combined == nil {
					combined = check
				} else {
					combined = exp.New(exp.Or, "this", combined, "expression", check)
				}
			}
			funcSQL := buildFuncCall(g, name, allArgs)
			return g.SQL(exp.New(exp.If, "this", combined, "true", exp.NewNull(), "false", funcSQL))
		}
		// Case 2: Source doesn't propagate NULLs, target does (DuckDB → Snowflake)
		var wrapped []*exp.Expr
		for _, arg := range allArgs {
			wrapped = append(wrapped, exp.New(exp.Coalesce, "expressions",
				[]*exp.Expr{arg.Copy(), exp.New(exp.Array, "expressions", []*exp.Expr{})}))
		}
		return buildFuncCall(g, name, wrapped)
	}
}

// VarMapSQL mirrors var_map_sql(self, expression, map_func_name="MAP").
func VarMapSQL(g *Generator, e *exp.Expr, mapFuncName string) string {
	keys := e.Get("keys")
	values := e.Get("values")
	if !keys.IsKind(exp.Array) || !values.IsKind(exp.Array) {
		g.Unsupported("Cannot convert array columns into map.")
		return g.Func(mapFuncName, keys, values)
	}
	var args []any
	kx, vx := keys.Expressions(), values.Expressions()
	for i := 0; i < len(kx) && i < len(vx); i++ {
		args = append(args, g.SQL(kx[i]), g.SQL(vx[i]))
	}
	return g.Func(mapFuncName, args...)
}

func MonthsBetweenSQL(g *Generator, e *exp.Expr) string {
	date1, date2 := e.This(), e.Expression()
	// Cast to DATE to ensure consistent behavior
	date1Cast := exp.NewCast(date1, exp.DTypeDate, exp.WithCopy(false))
	date2Cast := exp.NewCast(date2, exp.DTypeDate, exp.WithCopy(false))
	// Whole months: DATEDIFF('month', date2, date1)
	wholeMonths := exp.New(exp.DateDiff, "this", date1Cast, "expression", date2Cast, "unit", exp.NewVar("month"))
	day1 := exp.New(exp.Day, "this", date1Cast.Copy())
	day2 := exp.New(exp.Day, "this", date2Cast.Copy())
	lastDayOfMonth1 := exp.New(exp.LastDay, "this", date1Cast.Copy())
	lastDayOfMonth2 := exp.New(exp.LastDay, "this", date2Cast.Copy())
	dayOfLastDay1 := exp.New(exp.Day, "this", lastDayOfMonth1)
	dayOfLastDay2 := exp.New(exp.Day, "this", lastDayOfMonth2)
	lastDay1 := exp.New(exp.EQ, "this", day1.Copy(), "expression", dayOfLastDay1)
	lastDay2 := exp.New(exp.EQ, "this", day2.Copy(), "expression", dayOfLastDay2)
	bothLastDay := exp.New(exp.And, "this", lastDay1, "expression", lastDay2)
	// Fractional part: (DAY(date1) - DAY(date2)) / 31.0
	fractional := exp.New(exp.Div,
		"this", exp.New(exp.Paren, "this", exp.New(exp.Sub, "this", day1.Copy(), "expression", day2.Copy())),
		"expression", exp.LiteralNumber("31.0"))
	// If both are last day of month, fractional = 0, else calculate fractional
	fractionalWithCheck := exp.New(exp.If, "this", bothLastDay, "true", exp.LiteralNumber("0"), "false", fractional)
	return g.SQL(exp.New(exp.Add, "this", wholeMonths, "expression", fractionalWithCheck))
}

// BuildFormattedTime mirrors build_formatted_time(exp_class, dialect=None, default=None).
// def_ may be nil, true or a format string.
func BuildFormattedTime(kind exp.Kind, dialectOverride string, def_ any) FuncBuilder {
	return func(args []*exp.Expr, d *Dialect) *exp.Expr {
		target := d.DialectClass
		if dialectOverride != "" {
			target = MustDialectClass(dialectOverride)
		}
		var fmtV any
		if f := seqGet(args, 1); f != nil {
			fmtV = f
		} else if b, ok := def_.(bool); ok && b {
			fmtV = target.TimeFormat
		} else if s, ok := def_.(string); ok && s != "" {
			fmtV = s
		}
		return exp.New(kind, "this", seqGet(args, 0), "format", target.FormatTime(fmtV))
	}
}

// TimeFormat mirrors time_format(dialect): the expression's format, or nil when it equals the
// dialect's default TIME_FORMAT.
func TimeFormat(dialect string) func(g *Generator, e *exp.Expr) any {
	return func(g *Generator, e *exp.Expr) any {
		tf := g.FormatTime(e, nil, nil)
		if tf == "" || tf == MustDialect(dialect).TimeFormat {
			return nil
		}
		return tf
	}
}

// BuildDateDelta mirrors build_date_delta(exp_class, unit_mapping=None, default_unit="DAY",
// supports_timezone=False). defaultUnit "" means None.
func BuildDateDelta(kind exp.Kind, unitMapping map[string]string, defaultUnit string, supportsTimezone bool) FuncBuilder {
	return func(args []*exp.Expr, d *Dialect) *exp.Expr {
		unitBased := len(args) >= 3
		hasTimezone := len(args) == 4
		var this *exp.Expr
		if unitBased {
			this = args[2]
		} else {
			this = seqGet(args, 0)
		}
		var unit *exp.Expr
		if unitBased || defaultUnit != "" {
			if unitBased {
				unit = args[0]
			} else {
				unit = exp.LiteralString(defaultUnit)
			}
			if unitMapping != nil {
				name := unit.Name()
				if m, ok := unitMapping[strings.ToLower(name)]; ok {
					name = m
				}
				unit = exp.NewVar(name)
			}
		}
		e := exp.New(kind, "this", this, "expression", seqGet(args, 1), "unit", unit)
		if supportsTimezone && hasTimezone {
			e.Set("zone", args[len(args)-1])
		}
		return e
	}
}

// BuildDateDeltaWithInterval mirrors build_date_delta_with_interval(expression_class,
// default_unit=None); defaultUnit "" means None.
func BuildDateDeltaWithInterval(kind exp.Kind, defaultUnit string) FuncBuilder {
	return func(args []*exp.Expr, d *Dialect) *exp.Expr {
		if len(args) < 2 {
			return nil
		}
		interval := args[1]
		if !interval.IsKind(exp.Interval) {
			if defaultUnit == "" {
				panic(&errs.ParseError{Message: fmt.Sprintf("INTERVAL expression expected but got '%s'", pyStr(interval))})
			}
			return exp.New(kind, "this", args[0], "expression", interval, "unit", exp.LiteralString(defaultUnit))
		}
		return exp.New(kind, "this", args[0], "expression", interval.This(), "unit", UnitToStr(interval, "DAY"))
	}
}

func DateTruncToTime(args []*exp.Expr, d *Dialect) *exp.Expr {
	unit := seqGet(args, 0)
	this := seqGet(args, 1)
	if this.IsKind(exp.Cast) && this.IsType("date") {
		return exp.New(exp.DateTrunc, "unit", unit, "this", this)
	}
	return exp.New(exp.TimestampTrunc, "this", this, "unit", unit)
}

func DateAddIntervalSQL(dataType, kind string) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		this := g.SQLArg(e, "this")
		interval := exp.New(exp.Interval, "this", e.Expression(), "unit", UnitToVar(e, "DAY"))
		return dataType + "_" + kind + "(" + this + ", " + g.SQL(interval) + ")"
	}
}

// TimestamptruncSQL mirrors timestamptrunc_sql(func="DATE_TRUNC", zone=False).
func TimestamptruncSQL(funcName string, zone bool) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		args := []any{WeekstartUnitToStr(g, e, "DAY"), e.This()}
		if zone {
			args = append(args, e.Arg("zone"))
		}
		return g.Func(funcName, args...)
	}
}

func NoTimestampSQL(g *Generator, e *exp.Expr) string {
	zone := e.Get("zone")
	if zone == nil {
		var target any = exp.DTypeTimestamp
		if t := AnnotateTypes(e, g.D).Type(); t != nil {
			target = t
		}
		return g.SQL(exp.NewCast(e.This(), target))
	}
	if Timezones[strings.ToLower(zone.Name())] {
		return g.SQL(exp.New(exp.AtTimeZone, "this", exp.NewCast(e.This(), exp.DTypeTimestamp), "zone", zone))
	}
	return g.Func("TIMESTAMP", e.This(), zone)
}

func NoTimeSQL(g *Generator, e *exp.Expr) string {
	// Transpile BQ's TIME(timestamp, zone) to CAST(TIMESTAMPTZ <timestamp> AT TIME ZONE <zone> AS TIME)
	this := exp.NewCast(e.This(), exp.DTypeTimestamptz)
	expr := exp.NewCast(exp.New(exp.AtTimeZone, "this", this, "zone", e.Arg("zone")), exp.DTypeTime)
	return g.SQL(expr)
}

func NoDatetimeSQL(g *Generator, e *exp.Expr) string {
	this := e.This()
	expr := e.Expression()
	if Timezones[strings.ToLower(expr.Name())] {
		// Transpile BQ's DATETIME(timestamp, zone) to CAST(TIMESTAMPTZ <timestamp> AT TIME ZONE <zone> AS TIMESTAMP)
		this = exp.NewCast(this, exp.DTypeTimestamptz)
		this = exp.NewCast(exp.New(exp.AtTimeZone, "this", this, "zone", expr), exp.DTypeTimestamp)
		return g.SQL(this)
	}
	this = exp.NewCast(this, exp.DTypeDate)
	expr = exp.NewCast(expr, exp.DTypeTime)
	return g.SQL(exp.NewCast(exp.New(exp.Add, "this", this, "expression", expr), exp.DTypeTimestamp))
}

func LeftToSubstringSQL(g *Generator, e *exp.Expr) string {
	return g.SQL(exp.New(exp.Substring, "this", e.This(), "start", exp.LiteralNumber(1), "length", e.Expression()))
}

func RightToSubstringSQL(g *Generator, e *exp.Expr) string {
	start := exp.New(exp.Length, "this", e.This()).Minus(exp.NewParen(e.Expression().Minus(1)))
	return g.SQL(exp.New(exp.Substring, "this", e.This(), "start", start))
}

// TimestrtotimeSQL mirrors timestrtotime_sql(self, expression, include_precision=False).
func TimestrtotimeSQL(g *Generator, e *exp.Expr, includePrecision bool) string {
	builder := iff(e.Bool("zone"), exp.DTypeTimestamptz, exp.DTypeTimestamp)
	datatype := builder.IntoExpr()
	if e.This().IsKind(exp.Literal) && includePrecision {
		if precision := SubsecondPrecision(e.This().Name()); precision > 0 {
			datatype = exp.MustDataTypeBuild(datatype.Arg("this"), nil, false,
				"expressions", []*exp.Expr{exp.New(exp.DataTypeParam, "this", exp.LiteralNumber(precision))})
		}
	}
	return g.SQL(exp.NewCast(e.This(), datatype, exp.WithDialect(g.D)))
}

func DatestrtodateSQL(g *Generator, e *exp.Expr) string {
	return g.SQL(exp.NewCast(e.This(), exp.DTypeDate))
}

// EncodeDecodeSQL mirrors encode_decode_sql(self, expression, name, replace=True). Used for
// Presto and DuckDB which use functions that don't support charset, and assume utf-8.
func EncodeDecodeSQL(g *Generator, e *exp.Expr, name string, replace bool) string {
	if charset := e.Get("charset"); charset != nil {
		if n := strings.ToLower(charset.Name()); n != "utf-8" && n != "utf8" {
			g.Unsupported("Expected utf-8 character set, got " + pyStr(charset) + ".")
		}
	}
	var rep any
	if replace {
		rep = e.Arg("replace")
	}
	return g.Func(name, e.This(), rep)
}

func MinOrLeast(g *Generator, e *exp.Expr) string {
	return RenameFunc(iff(len(e.Expressions()) > 0, "LEAST", "MIN"))(g, e)
}

func MaxOrGreatest(g *Generator, e *exp.Expr) string {
	return RenameFunc(iff(len(e.Expressions()) > 0, "GREATEST", "MAX"))(g, e)
}

func CountIfToSum(g *Generator, e *exp.Expr) string {
	cond := e.This()
	if cond.IsKind(exp.Distinct) {
		cond = cond.Expressions()[0]
		g.Unsupported("DISTINCT is not supported when converting COUNT_IF to SUM")
	}
	return g.Func("sum", exp.NewFunc("if", cond, 1, 0))
}

// TrimSQL mirrors trim_sql(self, expression, default_trim_type="").
func TrimSQL(g *Generator, e *exp.Expr, defaultTrimType string) string {
	removeChars := g.SQLArg(e, "expression")
	// Use TRIM/LTRIM/RTRIM syntax if the expression isn't database-specific
	if removeChars == "" {
		return g.TrimSQL(e)
	}
	target := g.SQLArg(e, "this")
	trimType := g.SQLArg(e, "position")
	if trimType == "" {
		trimType = defaultTrimType
	}
	collation := g.SQLArg(e, "collation")
	trimType = sfx(trimType, " ")
	removeChars = sfx(removeChars, " ")
	fromPart := iff(trimType != "" || removeChars != "", "FROM ", "")
	collation = pfx(" COLLATE ", collation)
	return "TRIM(" + trimType + removeChars + fromPart + target + collation + ")"
}

func ConcatToDpipeSQL(g *Generator, e *exp.Expr) string {
	args := g.ConvertConcatArgs(e)
	if len(args) == 0 {
		g.Raisef("TypeError: reduce() of empty iterable with no initial value")
	}
	result := args[0]
	for _, a := range args[1:] {
		result = exp.New(exp.DPipe, "this", result, "expression", a)
	}
	return g.SQL(result)
}

func ConcatWsToDpipeSQL(g *Generator, e *exp.Expr) string {
	all := e.Expressions()
	delim, rest := all[0], all[1:]
	if len(rest) == 0 {
		g.Raisef("TypeError: reduce() of empty iterable with no initial value")
	}
	result := rest[0]
	for _, y := range rest[1:] {
		result = exp.New(exp.DPipe, "this", result, "expression", exp.New(exp.DPipe, "this", delim, "expression", y))
	}
	return g.SQL(result)
}

var RegexpExtractSQL = UnsupportedArgs(func(g *Generator, e *exp.Expr) string {
	group := e.Get("group")
	// Do not render group if it's the default value for this dialect
	if group != nil && group.Name() == fmt.Sprint(g.D.RegexpExtractDefaultGroup) {
		group = nil
	}
	return g.Func(e.Kind().SQLName(), e.This(), e.Expression(), group)
}, "position", "occurrence", "parameters")

var RegexpReplaceSQL = UnsupportedArgs(func(g *Generator, e *exp.Expr) string {
	return g.Func("REGEXP_REPLACE", e.This(), e.Expression(), e.Arg("replacement"))
}, "position", "occurrence", "modifiers")

// PivotColumnNames mirrors pivot_column_names(aggregations, dialect).
func PivotColumnNames(aggregations []*exp.Expr, dialect *Dialect) []string {
	var names []string
	for _, agg := range aggregations {
		if agg.IsKind(exp.Alias) {
			names = append(names, agg.Alias())
			continue
		}
		// This case corresponds to aggregations without aliases being used as suffixes
		// (e.g. col_avg(foo)). We need to unquote identifiers because they're going to
		// be quoted in the base parser's `_parse_pivot` method, due to `to_identifier`.
		// Otherwise, we'd end up with `col_avg(`foo`)` (notice the double quotes).
		unquoted := agg.Transform(func(node *exp.Expr) *exp.Expr {
			if node.IsKind(exp.Identifier) {
				return exp.New(exp.Identifier, "this", node.Name(), "quoted", false)
			}
			return node
		}, true)
		names = append(names, unquoted.SQL(dialect, exp.NormalizeFunctions("lower")))
	}
	return names
}

// BinaryFromFunction mirrors binary_from_function(expr_type).
func BinaryFromFunction(kind exp.Kind) FuncBuilder {
	return func(args []*exp.Expr, d *Dialect) *exp.Expr {
		return exp.New(kind, "this", seqGet(args, 0), "expression", seqGet(args, 1))
	}
}

// BuildTimestampTrunc is used to represent DATE_TRUNC in Doris, Postgres and Starrocks dialects.
func BuildTimestampTrunc(args []*exp.Expr, d *Dialect) *exp.Expr {
	return exp.New(exp.TimestampTrunc, "this", seqGet(args, 1), "unit", seqGet(args, 0), "zone", seqGet(args, 2))
}

// BuildTrunc mirrors build_trunc(args, dialect, date_trunc_unabbreviate=True,
// default_date_trunc_unit=None, date_trunc_requires_part=True, fractions_supported=False).
func BuildTrunc(args []*exp.Expr, d *Dialect, dateTruncUnabbreviate bool, defaultDateTruncUnit string,
	dateTruncRequiresPart, fractionsSupported bool) *exp.Expr {
	this := seqGet(args, 0)
	second := seqGet(args, 1)
	if this != nil && this.Type() == nil {
		this = AnnotateTypes(this, d)
	}
	if second != nil && second.Type() == nil {
		second = AnnotateTypes(second, d)
	}
	// Date truncation
	if (this != nil && this.IsTypeIn(exp.DataTypeTemporalTypes) && (second != nil || defaultDateTruncUnit != "")) ||
		(second != nil && second.IsTypeIn(exp.DataTypeTextTypes)) {
		unit := second
		if unit == nil {
			unit = exp.LiteralString(defaultDateTruncUnit)
		}
		return exp.New(exp.DateTrunc, "this", this, "unit", unit, "unabbreviate", dateTruncUnabbreviate)
	}
	// Numeric truncation
	if (this != nil && this.IsTypeIn(exp.DataTypeNumericTypes)) ||
		(second != nil && second.IsTypeIn(exp.DataTypeNumericTypes)) ||
		(!dateTruncRequiresPart && second == nil) {
		return exp.New(exp.Trunc, "this", this, "decimals", second, "fractions_supported", fractionsSupported)
	}
	return exp.New(exp.Anonymous, "this", "TRUNC", "expressions", args)
}

func AnyValueToMaxSQL(g *Generator, e *exp.Expr) string { return g.Func("MAX", e.This()) }

func BoolXorSQL(g *Generator, e *exp.Expr) string {
	a := g.SQL(e.Left())
	b := g.SQL(e.Right())
	return "(" + a + " AND (NOT " + b + ")) OR ((NOT " + a + ") AND " + b + ")"
}

func IsParseJSON(e *exp.Expr) bool {
	return e.IsKind(exp.ParseJSON) || (e.IsKind(exp.Cast) && e.IsType("json"))
}

func IsnullToIsNull(args []*exp.Expr, d *Dialect) *exp.Expr {
	this := seqGet(args, 0)
	if this.IsAny(exp.Predicate, exp.Connector, exp.Not) {
		this = exp.New(exp.Paren, "this", this)
	}
	return exp.New(exp.Paren, "this", exp.New(exp.Is, "this", this, "expression", exp.NewNull()))
}

// GeneratedAsIdentityColumnConstraintSQL mirrors dialect.generatedasidentitycolumnconstraint_sql.
func GeneratedAsIdentityColumnConstraintSQL(g *Generator, e *exp.Expr) string {
	start := g.SQLArg(e, "start")
	if start == "" {
		start = "1"
	}
	increment := g.SQLArg(e, "increment")
	if increment == "" {
		increment = "1"
	}
	return "IDENTITY(" + start + ", " + increment + ")"
}

func ArgMaxOrMinNoCount(name string) GenFunc {
	return UnsupportedArgs(func(g *Generator, e *exp.Expr) string {
		return g.Func(name, e.This(), e.Expression())
	}, "count")
}

func TsOrDsAddCast(e *exp.Expr) *exp.Expr {
	this := e.This().Copy()
	returnType := e.ReturnType()
	if returnType.IsType(exp.DTypeDate) {
		// If we need to cast to a DATE, we cast to TIMESTAMP first to make sure we
		// can truncate timestamp strings, because some dialects can't cast them to DATE
		this = exp.NewCast(this, exp.DTypeTimestamp)
	}
	e.This().Replace(exp.NewCast(this, returnType))
	return e
}

// RemoveTsOrDsToDate mirrors remove_ts_or_ds_to_date(to_sql=None, args=("this",)).
func RemoveTsOrDsToDate(toSQL GenFunc, args ...string) GenFunc {
	if len(args) == 0 {
		args = []string{"this"}
	}
	return func(g *Generator, e *exp.Expr) string {
		for _, key := range args {
			if arg := e.Get(key); arg.IsAny(exp.TsOrDsToDate, exp.TsOrDsToTimestamp) && !arg.Bool("format") {
				e.Set(key, arg.This())
			}
		}
		if toSQL != nil {
			return toSQL(g, e)
		}
		return g.FunctionFallbackSQL(e)
	}
}

// DateDeltaSQL mirrors date_delta_sql(name, cast=False).
func DateDeltaSQL(name string, cast bool) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		if cast && e.IsKind(exp.TsOrDsAdd) {
			e = TsOrDsAddCast(e)
		}
		return g.Func(name, UnitToVar(e, "DAY"), e.Expression(), e.This())
	}
}

func DateDeltaToBinarySQL(g *Generator, e *exp.Expr, binary *exp.Expr) string {
	parent := e.Parent()
	if parent.IsAny(exp.Binary, exp.Unary, exp.AtTimeZone) &&
		!(parent.IsKind(exp.Paren) || (parent.IsAny(exp.Add, exp.Sub) && parent.This() == e)) {
		return g.SQL(exp.NewParen(binary, exp.WithCopy(false)))
	}
	return g.SQL(binary)
}

// isDatetimeAdd mirrors isinstance(expression, DATETIME_ADD).
func isDatetimeAdd(e *exp.Expr) bool {
	return e.IsAny(exp.DateAdd, exp.TimeAdd, exp.DatetimeAdd, exp.TsOrDsAdd, exp.TimestampAdd)
}

// DateDeltaToBinaryIntervalOp mirrors date_delta_to_binary_interval_op(cast=True).
func DateDeltaToBinaryIntervalOp(cast bool) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		this := e.This()
		unit := UnitToVar(e, "DAY")
		var toType any
		if cast {
			if e.IsKind(exp.TsOrDsAdd) {
				toType = e.ReturnType()
			} else if this.IsString() {
				// Cast string literals (i.e function parameters) to the appropriate type for +/- interval to work
				if e.IsAny(exp.DatetimeAdd, exp.DatetimeSub, exp.TimestampAdd, exp.TimestampSub) {
					toType = exp.DTypeDatetime
				} else {
					toType = exp.DTypeDate
				}
			}
		}
		if toType != nil {
			this = exp.NewCast(this, toType)
		}
		expr := e.Expression()
		interval := expr
		if !expr.IsKind(exp.Interval) {
			interval = exp.New(exp.Interval, "this", expr, "unit", unit)
		}
		var binary *exp.Expr
		if isDatetimeAdd(e) {
			binary = this.Plus(interval)
		} else {
			binary = this.Minus(interval)
		}
		return DateDeltaToBinarySQL(g, e, binary)
	}
}

// UnitToStr mirrors unit_to_str(expression, default="DAY"); def_ "" means None.
func UnitToStr(e *exp.Expr, def_ string) *exp.Expr {
	unit := e.Get("unit")
	if unit == nil {
		if def_ != "" {
			return exp.LiteralString(def_)
		}
		return nil
	}
	if unit.IsKind(exp.WeekStart) {
		// WEEK(<day>) is BigQuery-only syntax, so it degrades to the plain WEEK unit
		return exp.LiteralString("WEEK")
	}
	if unit.IsKind(exp.Placeholder) || !(unit.IsKind(exp.Var) || unit.IsKind(exp.Literal)) {
		return unit
	}
	return exp.LiteralString(unit.Name())
}

// WeekstartUnitToStr mirrors weekstart_unit_to_str(self, expression, default="DAY").
func WeekstartUnitToStr(g *Generator, e *exp.Expr, def_ string) *exp.Expr {
	if unit := e.Get("unit"); unit.IsKind(exp.WeekStart) {
		return exp.LiteralString(g.WeekstartName(unit))
	}
	return UnitToStr(e, def_)
}

// UnitToVar mirrors unit_to_var(expression, default="DAY"); def_ "" means None.
func UnitToVar(e *exp.Expr, def_ string) *exp.Expr {
	unit := e.Get("unit")
	if unit.IsAny(exp.Var, exp.Placeholder, exp.WeekStart, exp.Column) {
		return unit
	}
	value := def_
	if unit != nil {
		value = unit.Name()
	}
	if value == "" {
		return nil
	}
	return exp.New(exp.Var, "this", value)
}

// WeekUnitToDow mirrors week_unit_to_dow(unit): the ISO week start day for week-ish units, or
// 0 (None).
func WeekUnitToDow(unit *exp.Expr) int {
	if unit.IsKind(exp.Var) {
		if n := strings.ToUpper(unit.Name()); n == "WEEK" || n == "ISOWEEK" {
			return 1
		}
	}
	// Handle WeekStart expressions with explicit day
	if unit.IsKind(exp.WeekStart) {
		return WeekStartDayToDow[strings.ToUpper(unit.Name())]
	}
	return 0
}

// MapDatePart mirrors map_date_part(part, dialect).
func MapDatePart(part *exp.Expr, dialect *Dialect) *exp.Expr {
	if dialect == nil {
		dialect = BaseDialectClass().NewInstance()
	}
	mapped := ""
	if part != nil && !(part.IsKind(exp.Column) && len(part.Parts()) != 1) {
		mapped = dialect.DatePartMapping[strings.ToUpper(part.Name())]
	}
	if mapped != "" {
		if part.IsString() {
			return exp.LiteralString(mapped)
		}
		return exp.NewVar(mapped)
	}
	return part
}

func NoLastDaySQL(g *Generator, e *exp.Expr) string {
	truncCurrDate := exp.NewFunc("date_trunc", "month", e.This())
	plusOneMonth := exp.NewFunc("date_add", truncCurrDate, 1, "month")
	minusOneDay := exp.NewFunc("date_sub", plusOneMonth, 1, "day")
	return g.SQL(exp.NewCast(minusOneDay, exp.DTypeDate))
}

// MergeWithoutTargetSQL removes table refs from columns in when statements.
func MergeWithoutTargetSQL(g *Generator, e *exp.Expr) string {
	alias := e.This().Get("alias")
	normalize := func(identifier *exp.Expr) (string, bool) {
		if identifier == nil {
			return "", false
		}
		return g.D.NormalizeIdentifier(identifier).Name(), true
	}
	type key struct {
		s  string
		ok bool
	}
	targets := map[key]bool{}
	s, ok := normalize(e.This().This())
	targets[key{s, ok}] = true
	if alias != nil {
		s, ok := normalize(alias.This())
		targets[key{s, ok}] = true
	}
	for _, when := range e.Get("whens").Expressions() {
		// only remove the target table names from certain parts of WHEN MATCHED / WHEN NOT MATCHED
		// they are still valid in the <condition>, the right hand side of each UPDATE and the VALUES part
		// (not the column list) of the INSERT
		then := when.Get("then")
		if then == nil {
			continue
		}
		if then.IsKind(exp.Update) {
			for equals := range FindAllInScope(then, exp.EQ) {
				lhs := equals.This()
				if lhs.IsKind(exp.Column) {
					s, ok := normalize(lhs.Get("table"))
					if targets[key{s, ok}] {
						lhs.Replace(exp.NewColumn(lhs.This()))
					}
				}
			}
		}
		if then.IsKind(exp.Insert) {
			if columnList := then.This(); columnList.IsKind(exp.Tuple) {
				for _, column := range columnList.Expressions() {
					s, ok := normalize(column.Get("table"))
					if targets[key{s, ok}] {
						column.Replace(exp.NewColumn(column.This()))
					}
				}
			}
		}
	}
	return g.MergeSQL(e)
}

// BuildJSONExtractPath mirrors build_json_extract_path(expr_type, zero_based_indexing=True,
// arrow_req_json_type=False, json_type=None); jsonType "" means None.
func BuildJSONExtractPath(kind exp.Kind, zeroBasedIndexing, arrowReqJSONType bool, jsonType string) FuncBuilder {
	return func(args []*exp.Expr, d *Dialect) *exp.Expr {
		segments := []*exp.Expr{exp.New(exp.JSONPathRoot)}
		for _, arg := range restOf(args) {
			if !arg.IsKind(exp.Literal) {
				// We use the fallback parser because we can't really transpile non-literals safely
				var rest any
				if len(args) > 2 {
					rest = append([]*exp.Expr(nil), args[2:]...)
				}
				return exp.New(kind, "this", seqGet(args, 0), "expression", seqGet(args, 1), "expressions", rest)
			}
			text := arg.Name()
			if helper.IsInt(text) && (!arrowReqJSONType || !arg.IsString()) {
				n, _ := helper.ToInt(text)
				index := int(n.Int64())
				if !zeroBasedIndexing {
					index--
				}
				segments = append(segments, exp.New(exp.JSONPathSubscript, "this", index))
			} else {
				segments = append(segments, exp.New(exp.JSONPathKey, "this", text))
			}
		}
		kv := []any{"this", seqGet(args, 0), "expression", exp.New(exp.JSONPath, "expressions", segments)}
		if !kind.IsAny(exp.JSONBExtract, exp.JSONBExtractScalar) {
			kv = append(kv, "only_json_types", arrowReqJSONType)
		}
		if jsonType != "" {
			kv = append(kv, "json_type", jsonType)
		}
		return exp.New(kind, kv...)
	}
}

// JSONExtractSegments mirrors json_extract_segments(name, quoted_index=True, op=None).
func JSONExtractSegments(name string, quotedIndex bool, op string) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		path := e.Expression()
		if !path.IsKind(exp.JSONPath) {
			return RenameFunc(name)(g, e)
		}
		var segments []string
		for _, segment := range path.Expressions() {
			s := g.SQL(segment)
			if s != "" {
				if segment.Is(exp.JSONPathPart) && (quotedIndex || !segment.IsKind(exp.JSONPathSubscript)) {
					s = g.EscapeStr(s, true, "", "", false, false)
					s = g.D.QuoteStart + s + g.D.QuoteEnd
				}
				segments = append(segments, s)
			}
		}
		if op != "" {
			return strings.Join(append([]string{g.SQL(e.This())}, segments...), " "+op+" ")
		}
		args := []any{e.This()}
		for _, s := range segments {
			args = append(args, s)
		}
		return g.Func(name, args...)
	}
}

func JSONPathKeyOnlyName(g *Generator, e *exp.Expr) string {
	if t, ok := e.Arg("this").(*exp.Expr); ok && t.IsKind(exp.JSONPathWildcard) {
		g.Unsupported("Unsupported wildcard in JSONPathKey expression")
	}
	return e.Name()
}

func FilterArrayUsingUnnest(g *Generator, e *exp.Expr) string {
	cond := e.Expression()
	var alias any
	switch {
	case cond.IsKind(exp.Lambda) && len(cond.Expressions()) == 1:
		alias = cond.Expressions()[0]
		cond = cond.This()
	case cond.Is(exp.Predicate):
		alias = "_u"
	case e.IsKind(exp.ArrayRemove):
		alias = "_u"
		cond = exp.New(exp.NEQ, "this", exp.MustMaybeParse("_u", exp.ParseOptions{}, "", true), "expression", e.Expression())
	default:
		g.Unsupported("Unsupported filter condition")
		return ""
	}
	unnest := exp.New(exp.Unnest, "expressions", []*exp.Expr{e.This()})
	filtered := exp.NewSelect(alias).From(exp.NewTableAliasOf(unnest, nil, []any{alias})).Where(cond)
	return g.SQL(exp.New(exp.Array, "expressions", []*exp.Expr{filtered}))
}

func ArrayCompactSQL(g *Generator, e *exp.Expr) string {
	lambdaID := exp.ToIdentifier("_u")
	cond := exp.New(exp.Is, "this", lambdaID, "expression", exp.NewNull()).Not()
	return g.SQL(exp.New(exp.ArrayFilter, "this", e.This(),
		"expression", exp.New(exp.Lambda, "this", cond, "expressions", []*exp.Expr{lambdaID})))
}

func RemoveFromArrayUsingFilter(g *Generator, e *exp.Expr) string {
	lambdaID := exp.ToIdentifier("_u")
	cond := exp.New(exp.NEQ, "this", lambdaID, "expression", e.Expression())
	filterSQL := g.SQL(exp.New(exp.ArrayFilter, "this", e.This(),
		"expression", exp.New(exp.Lambda, "this", cond, "expressions", []*exp.Expr{lambdaID})))
	// Handle NULL propagation for ArrayRemove
	sourceNullPropagation := e.Bool("null_propagation")
	targetNullPropagation := g.D.ArrayFuncsPropagatesNulls
	// Source propagates NULLs (Snowflake), target doesn't (DuckDB):
	// When removal value is NULL, return NULL instead of applying filter
	if sourceNullPropagation && !targetNullPropagation {
		removalValue := e.Expression()
		// Optimization: skip wrapper if removal value is a non-NULL literal or an array literal
		if (removalValue.IsKind(exp.Literal) && !removalValue.IsKind(exp.Null)) || removalValue.IsKind(exp.Array) {
			return filterSQL
		}
		return g.SQL(exp.New(exp.If,
			"this", exp.New(exp.Is, "this", removalValue, "expression", exp.NewNull()),
			"true", exp.NewNull(),
			"false", filterSQL))
	}
	return filterSQL
}

func ToNumberWithNlsParam(g *Generator, e *exp.Expr) string {
	return g.Func("TO_NUMBER", e.This(), e.Arg("format"), e.Arg("nlsparam"))
}

// BuildDefaultDecimalType mirrors build_default_decimal_type(precision=None, scale=None).
func BuildDefaultDecimalType(precision, scale *int) func(dtype *exp.Expr) *exp.Expr {
	return func(dtype *exp.Expr) *exp.Expr {
		if len(dtype.Expressions()) > 0 || precision == nil {
			return dtype
		}
		params := fmt.Sprint(*precision)
		if scale != nil {
			params += fmt.Sprintf(", %d", *scale)
		}
		return exp.MustDataTypeBuild("DECIMAL("+params+")", nil, false)
	}
}

func BuildTimestampFromParts(args []*exp.Expr, d *Dialect) *exp.Expr {
	if len(args) == 2 {
		// Other dialects don't have the TIMESTAMP_FROM_PARTS(date, time) concept,
		// so we parse this into Anonymous for now instead of introducing complexity
		return exp.New(exp.Anonymous, "this", "TIMESTAMP_FROM_PARTS", "expressions", args)
	}
	return exp.FromArgList(exp.TimestampFromParts, args)
}

func Sha256SQL(g *Generator, e *exp.Expr) string {
	length := e.Text("length")
	if length == "" {
		length = "256"
	}
	return g.Func("SHA"+length, e.This())
}

func Sha2DigestSQL(g *Generator, e *exp.Expr) string { return Sha256SQL(g, e) }

func SequenceSQL(g *Generator, e *exp.Expr) string {
	start, end, step := e.Get("start"), e.Get("end"), e.Get("step")
	var targetType *exp.Expr
	if start.IsKind(exp.Cast) {
		targetType = start.To()
	} else if end.IsKind(exp.Cast) {
		targetType = end.To()
	}
	if start != nil && end != nil {
		if targetType != nil && targetType.IsType("date", "timestamp") {
			if start.IsKind(exp.Cast) && targetType == start.To() {
				end = exp.NewCast(end, targetType)
			} else {
				start = exp.NewCast(start, targetType)
			}
		}
		if e.Bool("is_end_exclusive") {
			stepValue := step
			if stepValue == nil {
				stepValue = exp.LiteralNumber(1)
			}
			end = exp.NewParen(exp.New(exp.Sub, "this", end, "expression", stepValue), exp.WithCopy(false))
			sequenceCall := exp.New(exp.Anonymous, "this", "SEQUENCE", "expressions", nonNilList([]*exp.Expr{start, end, step}))
			zero := exp.LiteralNumber(0)
			shouldReturnEmpty := exp.NewOr(
				exp.New(exp.EQ, "this", stepValue.Copy(), "expression", zero.Copy()),
				exp.NewAnd(
					exp.New(exp.GT, "this", stepValue.Copy(), "expression", zero.Copy()),
					exp.New(exp.GT, "this", start.Copy(), "expression", end.Copy()),
				),
				exp.NewAnd(
					exp.New(exp.LT, "this", stepValue.Copy(), "expression", zero.Copy()),
					exp.New(exp.LT, "this", start.Copy(), "expression", end.Copy()),
				),
			)
			emptyArrayOrSequence := exp.New(exp.If, "this", shouldReturnEmpty,
				"true", exp.New(exp.Array, "expressions", []*exp.Expr{}), "false", sequenceCall)
			return g.SQL(g.SimplifyUnlessLiteral(emptyArrayOrSequence))
		}
	}
	return g.Func("SEQUENCE", start, end, step)
}

// BuildLikeFor mirrors dialect.build_like(expr_type, not_like=False).
func BuildLikeFor(kind exp.Kind, notLike bool) FuncBuilder {
	return func(args []*exp.Expr, d *Dialect) *exp.Expr {
		likeExpr := exp.New(kind, "this", seqGet(args, 0), "expression", seqGet(args, 1))
		if escape := seqGet(args, 2); escape != nil {
			likeExpr = exp.New(exp.Escape, "this", likeExpr, "expression", escape)
		}
		if notLike {
			likeExpr = exp.New(exp.Not, "this", likeExpr)
		}
		return likeExpr
	}
}

// BuildRegexpExtract mirrors build_regexp_extract(expr_type).
func BuildRegexpExtract(kind exp.Kind) FuncBuilder {
	return func(args []*exp.Expr, d *Dialect) *exp.Expr {
		group := seqGet(args, 2)
		if group == nil {
			group = exp.LiteralNumber(d.RegexpExtractDefaultGroup)
		}
		kv := []any{"this", seqGet(args, 0), "expression", seqGet(args, 1), "group", group, "parameters", seqGet(args, 3)}
		if kind == exp.RegexpExtract {
			kv = append(kv, "null_if_pos_overflow", d.RegexpExtractPositionOverflowReturnsNull)
		}
		return exp.New(kind, kv...)
	}
}

func ExplodeToUnnestSQL(g *Generator, e *exp.Expr) string {
	this := e.This()
	alias := e.Get("alias")
	var crossJoinExpr *exp.Expr
	if this.IsKind(exp.Posexplode) && alias != nil {
		// Spark's `FROM x LATERAL VIEW POSEXPLODE(y) t AS pos, col` has the following semantics:
		// - The first column is the position and the rest (1 for array, 2 for maps) are the exploded values
		// - The position is 0-based whereas WITH ORDINALITY is 1-based
		// For that matter, we must (1) subtract 1 from the ORDINALITY position and (2) rearrange the columns accordingly
		columns := alias.Columns()
		pos, cols := columns[0], columns[1:]
		selects := []any{exp.NewAlias(pos.Minus(1), pos)}
		for _, c := range cols {
			selects = append(selects, c)
		}
		lateralSubquery := exp.NewSelect(selects...).From(exp.New(exp.Unnest,
			"expressions", []*exp.Expr{this.This()},
			"offset", true,
			"alias", exp.New(exp.TableAlias, "this", alias.This(), "columns", append(append([]*exp.Expr{}, cols...), pos)),
		))
		crossJoinExpr = exp.New(exp.Lateral, "this", lateralSubquery.Subquery(nil, true))
	} else if this.IsKind(exp.Explode) {
		crossJoinExpr = exp.New(exp.Unnest, "expressions", []*exp.Expr{this.This()}, "alias", alias)
	}
	if crossJoinExpr != nil {
		return g.SQL(exp.New(exp.Join, "this", crossJoinExpr, "kind", "cross"))
	}
	return g.LateralSQL(e)
}

func TimestampdiffSQL(g *Generator, e *exp.Expr) string {
	return g.Func("TIMESTAMPDIFF", e.Unit(), e.Expression(), e.This())
}

// NoMakeIntervalSQL mirrors no_make_interval_sql(self, expression, sep=", ").
func NoMakeIntervalSQL(g *Generator, e *exp.Expr, sep string) string {
	var args []any
	for _, a := range e.Args() {
		value, _ := a.Value.(*exp.Expr)
		if value.IsKind(exp.Kwarg) {
			value = value.Expression()
		}
		if value == nil {
			continue
		}
		valueSQL := iff(value.IsString(), value.Name(), "")
		if !value.IsString() {
			valueSQL = g.SQL(value)
		}
		args = append(args, valueSQL+" "+a.Key)
	}
	return "INTERVAL " + g.SQL(exp.LiteralString(g.FormatArgsSep(sep, args...)))
}

func LengthOrCharLengthSQL(g *Generator, e *exp.Expr) string {
	return g.Func(iff(e.Bool("binary"), "LENGTH", "CHAR_LENGTH"), e.This())
}

// GroupconcatSQL mirrors groupconcat_sql(self, expression, func_name="LISTAGG", sep=",",
// within_group=True, on_overflow=False). sep nil means None.
func GroupconcatSQL(g *Generator, e *exp.Expr, funcName string, sep *string, withinGroup, onOverflow bool) string {
	this := e.This()
	sepExpr := e.Get("separator")
	if sepExpr == nil && sep != nil && *sep != "" {
		sepExpr = exp.LiteralString(*sep)
	}
	separator := g.SQL(sepExpr)
	onOverflowSQL := g.SQLArg(e, "on_overflow")
	onOverflowSQL = iff(onOverflow && onOverflowSQL != "", " ON OVERFLOW "+onOverflowSQL, "")

	var limit *exp.Expr
	if this.IsKind(exp.Limit) && this.This() != nil {
		limit = this
		this = limit.This().Pop()
	}
	order := this.Find(exp.Order)
	if order != nil && order.This() != nil {
		this = order.This().Pop()
	}
	var sepArg any
	if separator != "" || onOverflowSQL != "" {
		sepArg = separator + onOverflowSQL
	}
	args := g.FormatArgs(this, sepArg)
	listagg := exp.New(exp.Anonymous, "this", funcName, "expressions", []any{args})
	modifiers := g.SQL(limit)
	if order != nil {
		if withinGroup {
			listagg = exp.New(exp.WithinGroup, "this", listagg, "expression", order)
		} else {
			modifiers = g.SQL(order) + modifiers
		}
	}
	if modifiers != "" {
		listagg.Set("expressions", []any{args + modifiers})
	}
	return g.SQL(listagg)
}

func BuildTimetostrOrTochar(args []*exp.Expr, d *Dialect) *exp.Expr {
	if len(args) == 2 {
		this := args[0]
		if this.Type() == nil {
			AnnotateTypes(this, d)
		}
		if this.IsTypeIn(exp.DataTypeTemporalTypes) {
			return BuildFormattedTime(exp.TimeToStr, "", true)(args, d)
		}
	}
	return exp.FromArgList(exp.ToChar, args)
}

// ArraySortOrder mirrors array_sort_order(expression): (asc, nulls_first) where nullsFirst is
// nil for None; ok is false when the order can't be determined statically.
func ArraySortOrder(e *exp.Expr) (asc bool, nullsFirst *bool, ok bool) {
	ascE := e.Get("asc")
	if ascE == nil {
		ascE = exp.NewTrue()
	}
	nf := e.Get("nulls_first")
	if !ascE.IsKind(exp.Boolean) {
		return false, nil, false
	}
	if nf == nil {
		return ascE.Bool("this"), nil, true
	}
	if nf.IsKind(exp.Boolean) {
		b := nf.Bool("this")
		return ascE.Bool("this"), &b, true
	}
	return false, nil, false
}

func BuildReplaceWithOptionalReplacement(args []*exp.Expr, d *Dialect) *exp.Expr {
	replacement := seqGet(args, 2)
	if replacement == nil {
		replacement = exp.LiteralString("")
	}
	return exp.New(exp.Replace, "this", seqGet(args, 0), "expression", seqGet(args, 1), "replacement", replacement)
}

func RegexpReplaceGlobalModifier(e *exp.Expr) *exp.Expr {
	modifiers := e.Get("modifiers")
	singleReplace := e.Bool("single_replace")
	occurrence := e.Get("occurrence")
	occZero := false
	if n, ok := intLiteralValue(occurrence); ok && n == 0 {
		occZero = true
	}
	if !singleReplace && (occurrence == nil || occZero) {
		if modifiers == nil || modifiers.IsString() {
			// Append 'g' to the modifiers if they are not provided since
			// the semantics of REGEXP_REPLACE from the input dialect
			// is to replace all occurrences of the pattern.
			value := ""
			if modifiers != nil {
				value = modifiers.Name()
			}
			modifiers = exp.LiteralString(value + "g")
		}
	}
	return modifiers
}

func NthValueFromSQL(g *Generator, e *exp.Expr) string {
	this := g.Func("NTH_VALUE", e.This(), e.Arg("offset"))
	fromFirst := e.Arg("from_first")
	if fromFirst == nil {
		return this
	}
	return this + " FROM " + iff(exp.Truthy(fromFirst), "FIRST", "LAST")
}

func GetbitSQL(g *Generator, e *exp.Expr) string {
	value := e.This()
	position := e.Expression()
	if !e.Bool("zero_is_msb") && e.IsTypeIn(exp.DataTypeSignedIntegerTypes, exp.DataTypeUnsignedIntegerTypes) {
		// Use bitwise operations: (value >> position) & 1
		shifted := exp.New(exp.BitwiseRightShift, "this", value, "expression", position)
		masked := exp.New(exp.BitwiseAnd, "this", shifted, "expression", exp.LiteralNumber(1))
		return g.SQL(masked)
	}
	return g.Func("GET_BIT", value, position)
}

func JarowinklerSimilarity(funcName string) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		this, expr := e.This(), e.Expression()
		if e.Bool("case_insensitive") {
			this = exp.New(exp.Upper, "this", this)
			expr = exp.New(exp.Upper, "this", expr)
		}
		return g.Func(funcName, this, expr)
	}
}
