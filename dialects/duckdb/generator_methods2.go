package duckdb

// DuckDBGenerator methods, part 2 (sqlglot/generators/duckdb.py: tablesample_sql ..
// arrayszip_sql).

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/internal/ghelp"
)

func tablesampleSQL(sup func(g *gen, e *exp.Expr, kw string) string) func(g *gen, e *exp.Expr, kw string) string {
	return func(g *gen, e *exp.Expr, tablesampleKeyword string) string {
		if !e.Parent().IsKind(exp.Select) {
			// This sample clause only applies to a single source, not the entire resulting relation
			tablesampleKeyword = "TABLESAMPLE"
		}
		if e.Bool("size") {
			method := e.Get("method")
			if method != nil && strings.ToUpper(method.Name()) != "RESERVOIR" {
				g.Unsupported(fmt.Sprintf("Sampling method %s is not supported with a discrete sample count, "+
					"defaulting to reservoir sampling", method.SQL()))
				e.Set("method", exp.NewVar("RESERVOIR"))
			}
		}
		return sup(g, e, tablesampleKeyword)
	}
}

func inSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if unnest := e.Get("unnest"); unnest != nil {
			return g.SQL(replace(inUnnestTemplate, kv{"arr": unnest.Expressions()[0], "value": e.This()}))
		}
		return sup(g, e)
	}
}

func intervalSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		// DuckDB truncates INTERVAL '1.5' DAY to 1 day, but keeps the fraction in INTERVAL '1.5 DAY'
		this := e.This()
		unit := e.Unit()
		if e.Bool("fraction_in_string") && this != nil && unit.IsKind(exp.Var) {
			return "INTERVAL '" + this.Name() + " " + g.SQL(unit) + "'"
		}
		return sup(g, e)
	}
}

func joinSQL(sup func(g *gen, e *exp.Expr, joinOp string) string) func(g *gen, e *exp.Expr, joinOp string) string {
	return func(g *gen, e *exp.Expr, joinOp string) string {
		kind, side := e.Text("kind"), e.Text("side")
		if kind == "MARK" || kind == "SINGLE" || (side == "RIGHT" && e.IsSemiOrAntiJoin()) {
			var parts []string
			for _, p := range []string{side, kind} {
				if p != "" {
					parts = append(parts, p)
				}
			}
			return sup(g, e, "JOIN BY (TYPE "+strings.Join(parts, "_")+")")
		}
		if !e.Bool("using") && !e.Bool("on") && !e.Bool("nearest") && e.Text("method") == "" &&
			(kind == "" || kind == "INNER" || kind == "OUTER") {
			// Some dialects support `LEFT/INNER JOIN UNNEST(...)` without an explicit ON clause
			// DuckDB doesn't, but we can just add a dummy ON clause that is always true
			if e.This().IsKind(exp.Unnest) {
				return sup(g, e.On(exp.NewTrue()), "")
			}
			e.Set("side", nil)
			e.Set("kind", nil)
		}
		return sup(g, e, joinOp)
	}
}

func countifSQL(g *gen, e *exp.Expr) string {
	if ghelp.VersionGE(g.D.Version, 1, 2) {
		this := e.This()
		if e.Bool("zero_on_all_null") && !this.IsKind(exp.Distinct) {
			// DuckDB >= 1.2's COUNT_IF returns NULL when the condition is NULL on all rows,
			// so we wrap the condition in IS TRUE to preserve count-like semantics
			e = exp.New(exp.CountIf, "this", exp.NewParen(this).IsOp(exp.NewTrue()))
		}
		return g.FunctionFallbackSQL(e)
	}
	// https://github.com/tobymao/sqlglot/pull/4749
	return sqlglot.CountIfToSum(g, e)
}

func bracketSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if ghelp.VersionGE(g.D.Version, 1, 2) {
			return sup(g, e)
		}
		// https://duckdb.org/2025/02/05/announcing-duckdb-120.html#breaking-changes
		this := e.This()
		if this.IsKind(exp.Array) {
			this.Replace(exp.NewParen(this))
		}
		bracket := sup(g, e)
		if !e.Bool("returns_list_for_maps") {
			if this.Type() == nil {
				this = sqlglot.AnnotateTypes(this, g.D)
			}
			if this.IsType(exp.DTypeMap) {
				bracket = "(" + bracket + ")[1]"
			}
		}
		return bracket
	}
}

func withingroupSQL(g *gen, e *exp.Expr) string {
	fn := e.This()
	// For ARRAY_AGG, DuckDB requires ORDER BY inside the function, not in WITHIN GROUP
	// Transform: ARRAY_AGG(x) WITHIN GROUP (ORDER BY y) -> ARRAY_AGG(x ORDER BY y)
	if fn.IsKind(exp.ArrayAgg) {
		order := e.Expression()
		if !order.IsKind(exp.Order) {
			return g.SQL(fn)
		}
		// Save the original column for FILTER clause (before wrapping with Order)
		originalThis := fn.This()
		// Move ORDER BY inside ARRAY_AGG by wrapping its argument with Order
		fn.Set("this", exp.New(exp.Order, "this", fn.This().Copy(), "expressions", order.Expressions()))
		// Generate the ARRAY_AGG function with ORDER BY and add FILTER clause if needed
		// Use original_this (not the Order-wrapped version) for the FILTER condition
		arrayAggSQL := g.FunctionFallbackSQL(fn)
		return g.AddArrayaggNullFilter(arrayAggSQL, fn, originalThis)
	}
	// For other functions (like PERCENTILES), use existing logic
	expressionSQL := g.SQLArg(e, "expression")
	if fn.IsAny(exp.PercentileCont, exp.PercentileDisc) {
		// Make the order key the first arg and slide the fraction to the right
		// https://duckdb.org/docs/sql/aggregates#ordered-set-aggregate-functions
		if orderCol := e.Find(exp.Ordered); orderCol != nil {
			fn.Set("expression", fn.This())
			fn.Set("this", orderCol.This())
		}
	}
	this := strings.TrimRight(g.SQLArg(e, "this"), ")")
	return this + expressionSQL + ")"
}

func lengthSQL(g *gen, e *exp.Expr) string {
	arg := e.This()
	// Dialects like BQ and Snowflake also accept binary values as args, so
	// DDB will attempt to infer the type or resort to case/when resolution
	if !e.Bool("binary") || arg.IsString() {
		return g.Func("LENGTH", arg)
	}
	if arg.Type() == nil {
		arg = sqlglot.AnnotateTypes(arg, g.D)
	}
	if arg.IsTypeIn(exp.DataTypeTextTypes) {
		return g.Func("LENGTH", arg)
	}
	// We need these casts to make duckdb's static type checker happy
	blob := exp.NewCast(arg, exp.DTypeVarbinary)
	varchar := exp.NewCast(arg, exp.DTypeVarchar)
	c := exp.NewCase(exp.New(exp.Anonymous, "this", "TYPEOF", "expressions", []*exp.Expr{arg})).
		When(exp.LiteralString("BLOB"), exp.New(exp.ByteLength, "this", blob)).
		Else(exp.New(exp.Anonymous, "this", "LENGTH", "expressions", []*exp.Expr{varchar}))
	return g.SQL(c)
}

func bitlengthSQL(g *gen, e *exp.Expr) string {
	arg := e.This()
	if !isBinary(arg) {
		return g.Func("BIT_LENGTH", arg)
	}
	blob := exp.NewCast(arg, exp.DTypeVarbinary)
	return g.SQL(exp.New(exp.ByteLength, "this", blob).Times(exp.LiteralNumber(8)))
}

func chrSQL(g *gen, e *exp.Expr, name string) string {
	arg := e.Expressions()[0]
	if arg.IsTypeIn(exp.DataTypeRealTypes) {
		arg = exp.NewCast(arg, exp.DTypeInt)
	}
	return g.Func("CHR", arg)
}

func collateSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if !e.Expression().IsString() {
			return sup(g, e)
		}
		raw := e.Expression().Name()
		if raw == "" {
			return g.SQL(e.This())
		}
		var parts []string
		for _, part := range strings.Split(raw, "-") {
			lower := strings.ToLower(part)
			if !snowflakeCollationDefaults[lower] {
				if snowflakeCollationUnsupported[lower] {
					g.Unsupported(fmt.Sprintf("Snowflake collation specifier '%s' has no DuckDB equivalent", part))
				}
				parts = append(parts, lower)
			}
		}
		if len(parts) == 0 {
			return g.SQL(e.This())
		}
		return sup(g, exp.New(exp.Collate, "this", e.This(), "expression", exp.NewVar(strings.Join(parts, "."))))
	}
}

// validateRegexpFlags validates and filters regexp flags for DuckDB compatibility. It returns
// "" (None) if no valid flags remain.
func validateRegexpFlags(g *gen, flags *exp.Expr, supportedFlags string) string {
	if flags == nil {
		return ""
	}
	if !flags.IsString() {
		g.Unsupported("Non-literal regexp flags are not fully supported in DuckDB")
		return ""
	}
	flagStr := flags.Str("this")
	var unsupported []string
	seen := map[rune]bool{}
	for _, f := range flagStr {
		if !strings.ContainsRune(supportedFlags, f) && !seen[f] {
			seen[f] = true
			unsupported = append(unsupported, string(f))
		}
	}
	if len(unsupported) > 0 {
		sort.Strings(unsupported)
		quoted := make([]string, len(unsupported))
		for i, u := range unsupported {
			quoted[i] = "'" + u + "'"
		}
		g.Unsupported(fmt.Sprintf("Regexp flags [%s] are not supported in this context", strings.Join(quoted, ", ")))
	}
	var b strings.Builder
	for _, f := range flagStr {
		if strings.ContainsRune(supportedFlags, f) {
			b.WriteRune(f)
		}
	}
	return b.String()
}

func optLiteralString(s string) *exp.Expr {
	if s == "" {
		return nil
	}
	return exp.LiteralString(s)
}

func regexpcountSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	pattern := e.Expression()
	position := e.Get("position")
	parameters := e.Get("parameters")
	// Validate flags - only "ims" flags are supported for embedded patterns
	validatedFlags := validateRegexpFlags(g, parameters, "ims")
	if position != nil {
		this = exp.New(exp.Substring, "this", this, "start", position)
	}
	// Handle empty pattern: Snowflake returns 0, DuckDB would match between every character.
	// Checked before the flags are embedded, which would make the pattern non-empty
	isEmpty := exp.New(exp.EQ, "this", pattern.Copy(), "expression", exp.LiteralString(""))
	// Embed flags in pattern (REGEXP_EXTRACT_ALL doesn't support flags argument)
	if validatedFlags != "" {
		pattern = exp.New(exp.Concat, "expressions", []*exp.Expr{exp.LiteralString("(?" + validatedFlags + ")"), pattern})
	}
	result := exp.NewCase(nil).
		When(isEmpty, exp.LiteralNumber(0)).
		Else(exp.New(exp.Length, "this", exp.New(exp.Anonymous, "this", "REGEXP_EXTRACT_ALL", "expressions", []*exp.Expr{this, pattern})))
	return g.SQL(result)
}

func regexpreplaceSQL(g *gen, e *exp.Expr) string {
	subject := e.This()
	pattern := e.Expression()
	replacement := e.Get("replacement")
	if replacement == nil {
		replacement = exp.LiteralString("")
	}
	position := e.Get("position")
	occurrence := e.Get("occurrence")
	modifiers := e.Get("modifiers")
	validatedFlags := validateRegexpFlags(g, modifiers, "cimsg")

	// Handle occurrence (only literals supported)
	if occurrence != nil && !occurrence.IsInt() {
		g.Unsupported("REGEXP_REPLACE with non-literal occurrence")
	} else {
		occ := 0
		if occurrence != nil && occurrence.IsInt() {
			occ = toInt(occurrence.MustToPy())
		}
		if occ > 1 {
			g.Unsupported(fmt.Sprintf("REGEXP_REPLACE occurrence=%d not supported", occ))
		} else if occ == 0 && !strings.Contains(validatedFlags, "g") && !e.Bool("single_replace") {
			// flag duckdb to do either all or none, single_replace check is for duckdb round trip
			validatedFlags += "g"
		}
	}
	// Handle position (only literals supported)
	var prefix *exp.Expr
	if position != nil && !position.IsInt() {
		g.Unsupported("REGEXP_REPLACE with non-literal position")
	} else if position != nil && position.IsInt() && toInt(position.MustToPy()) > 1 {
		pos := toInt(position.MustToPy())
		prefix = exp.New(exp.Substring, "this", subject, "start", exp.LiteralNumber(1), "length", exp.LiteralNumber(pos-1))
		subject = exp.New(exp.Substring, "this", subject, "start", exp.LiteralNumber(pos))
	}
	exprs := []*exp.Expr{subject, pattern, replacement}
	if validatedFlags != "" {
		exprs = append(exprs, exp.LiteralString(validatedFlags))
	}
	result := exp.New(exp.Anonymous, "this", "REGEXP_REPLACE", "expressions", exprs)
	if prefix != nil {
		result = exp.New(exp.Concat, "expressions", []*exp.Expr{prefix, result})
	}
	return g.SQL(result)
}

func regexplikeSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	pattern := e.Expression()
	flag := e.Get("flag")
	if e.Bool("full_match") {
		validatedFlags := validateRegexpFlags(g, flag, "cims")
		return g.Func("REGEXP_FULL_MATCH", this, pattern, optLiteralString(validatedFlags))
	}
	return g.Func("REGEXP_MATCHES", this, pattern, flag)
}

var levenshteinSQL = sqlglot.UnsupportedArgs(func(g *gen, e *exp.Expr) string {
	this := e.This()
	expr := e.Expression()
	maxDist := e.Get("max_dist")
	if maxDist == nil {
		return g.Func("LEVENSHTEIN", this, expr)
	}
	// Emulate Snowflake semantics: if distance > max_dist, return max_dist
	levenshtein := exp.New(exp.Levenshtein, "this", this, "expression", expr)
	return g.SQL(exp.New(exp.Least, "this", levenshtein, "expressions", []*exp.Expr{maxDist}))
}, "ins_cost", "del_cost", "sub_cost")

// padSQL handles RPAD/LPAD for VARCHAR and BINARY types.
func padSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		stringArg := e.This()
		fillArg := e.Get("fill_pattern")
		if fillArg == nil {
			fillArg = exp.LiteralString(" ")
		}
		if isBinary(stringArg) || isBinary(fillArg) {
			lengthArg := e.Expression()
			inputLen := exp.New(exp.ByteLength, "this", stringArg)
			charsNeeded := lengthArg.Minus(inputLen)
			padCount := exp.New(exp.Greatest, "this", exp.LiteralNumber(0), "expressions", []*exp.Expr{charsNeeded}, "ignore_nulls", true)
			repeatExpr := exp.New(exp.Repeat, "this", fillArg, "times", padCount)
			left, right := stringArg, repeatExpr
			if e.Bool("is_left") {
				left, right = right, left
			}
			return g.SQL(exp.New(exp.DPipe, "this", left, "expression", right))
		}
		// For VARCHAR: Delegate to parent class (handles PAD_FILL_PATTERN_IS_REQUIRED)
		return sup(g, e)
	}
}

func minhashSQL(g *gen, e *exp.Expr) string {
	k := e.This()
	exprs := e.Expressions()
	if len(exprs) != 1 || exprs[0].IsKind(exp.Star) {
		g.Unsupported("MINHASH with multiple expressions or * requires manual query restructuring")
		return g.Func("MINHASH", append([]any{k}, toAnySlice(exprs)...)...)
	}
	return "(" + g.SQL(replace(minhashTemplate, kv{"expr": exprs[0], "k": k})) + ")"
}

func toAnySlice(es []*exp.Expr) []any {
	out := make([]any, len(es))
	for i, x := range es {
		out[i] = x
	}
	return out
}

func minhashcombineSQL(g *gen, e *exp.Expr) string {
	return "(" + g.SQL(replace(minhashCombineTemplate, kv{"expr": e.This()})) + ")"
}

func approximatesimilaritySQL(g *gen, e *exp.Expr) string {
	return "(" + g.SQL(replace(approximateSimilarityTemplate, kv{"expr": e.This()})) + ")"
}

func arrayuniqueaggSQL(g *gen, e *exp.Expr) string {
	return g.SQL(exp.New(exp.Filter,
		"this", exp.NewFunc("LIST", exp.New(exp.Distinct, "expressions", []*exp.Expr{e.This()})),
		"expression", exp.New(exp.Where, "this", e.This().Copy().IsOp(exp.NewNull()).Not())))
}

func arrayconcataggSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	if this.IsKind(exp.Limit) {
		g.Unsupported("LIMIT in ARRAY_CONCAT_AGG cannot be transpiled to DuckDB")
		this = this.This()
	}
	inner := this
	if this.IsKind(exp.Order) {
		inner = this.This()
	}
	return g.Func("FLATTEN", exp.New(exp.Filter,
		"this", exp.New(exp.ArrayAgg, "this", this),
		"expression", exp.New(exp.Where, "this", inner.Copy().IsOp(exp.NewNull()).Not())))
}

func arraydistinctSQL(g *gen, e *exp.Expr) string {
	arr := e.This()
	fn := g.Func("LIST_DISTINCT", arr)
	if e.Bool("check_null") {
		addNullToArray := exp.NewFunc("LIST_APPEND", exp.NewFunc("LIST_DISTINCT", exp.New(exp.ArrayCompact, "this", arr)), exp.New(exp.Null))
		return g.SQL(exp.New(exp.If,
			"this", exp.New(exp.NEQ, "this", exp.New(exp.ArraySize, "this", arr), "expression", exp.NewFunc("LIST_COUNT", arr)),
			"true", addNullToArray,
			"false", fn))
	}
	return fn
}

func arrayintersectSQL(g *gen, e *exp.Expr) string {
	if e.Bool("is_multiset") && len(e.Expressions()) == 2 {
		return arrayBagSQL(g, arrayIntersectionCondition(), e.Expressions()[0], e.Expressions()[1])
	}
	return g.FunctionFallbackSQL(e)
}

func arrayexceptSQL(g *gen, e *exp.Expr) string {
	arr1, arr2 := e.This(), e.Expression()
	if e.Bool("is_multiset") {
		return arrayBagSQL(g, arrayExceptCondition(), arr1, arr2)
	}
	return g.SQL(replace(arrayExceptSetTemplate, kv{"arr1": arr1, "arr2": arr2}))
}

// arraysliceSQL normalizes Spark's SLICE (length based) and Snowflake's ARRAY_SLICE (0-indexed,
// exclusive end) to DuckDB's ARRAY_SLICE (1-indexed, inclusive end).
func arraysliceSQL(g *gen, e *exp.Expr) string {
	start, end := e.Get("start"), e.Get("end")
	if e.Bool("zero_based") {
		if start != nil {
			start = exp.NewCase(nil).
				When(exp.New(exp.GTE, "this", start.Copy(), "expression", exp.LiteralNumber(0)),
					exp.New(exp.Add, "this", start.Copy(), "expression", exp.LiteralNumber(1))).
				Else(start)
		}
		if end != nil && !e.Bool("is_length") {
			end = exp.NewCase(nil).
				When(exp.New(exp.LT, "this", end.Copy(), "expression", exp.LiteralNumber(0)),
					exp.New(exp.Sub, "this", end.Copy(), "expression", exp.LiteralNumber(1))).
				Else(end)
		}
	}
	if e.Bool("is_length") && start != nil && end != nil {
		if !start.IsInt() || pyNegative(start) {
			size := exp.New(exp.ArraySize, "this", e.This().Copy())
			start = exp.NewCase(nil).
				When(start.Lt(size.Negate()), size.Plus(1)).
				When(start.Lt(0), size.Plus(start).Plus(1)).
				Else(start)
		}
		if start.IsInt() && end.IsInt() {
			end = exp.LiteralNumber(toInt(start.MustToPy()) + toInt(end.MustToPy()) - 1)
		} else {
			end = start.Plus(exp.NewCast(end, "BIGINT")).Minus(1)
		}
	}
	return g.Func("ARRAY_SLICE", e.This(), start, end, e.Get("step"))
}

// pyNegative is `expr.to_py() < 0` for an integer literal.
func pyNegative(e *exp.Expr) bool { return toInt(e.MustToPy()) < 0 }

func arrayszipSQL(g *gen, e *exp.Expr) string {
	args := e.Expressions()
	if len(args) == 0 {
		// Return [{}] - using MAP([], []) since DuckDB can't represent empty structs
		return g.SQL(exp.NewArray(exp.New(exp.Map, "keys", exp.NewArray(), "values", exp.NewArray())))
	}
	// Build placeholder values for template
	var lengths []*exp.Expr
	for _, arg := range args {
		lengths = append(lengths, exp.New(exp.Length, "this", arg))
	}
	maxLen := lengths[0]
	if len(lengths) > 1 {
		maxLen = exp.New(exp.Greatest, "this", lengths[0], "expressions", slices.Clone(lengths[1:]))
	}
	// Empty struct with same schema: {'$1': NULL, '$2': NULL, ...}
	var emptyArgs []any
	for i := range args {
		emptyArgs = append(emptyArgs, exp.New(exp.PropertyEQ, "this", exp.LiteralString(fmt.Sprintf("$%d", i+1)), "expression", exp.New(exp.Null)))
	}
	emptyStruct := exp.NewFunc("STRUCT", emptyArgs...)
	// Struct for transform: {'$1': COALESCE(arr1, [])[__i + 1], ...}
	// COALESCE wrapping handles NULL arrays - prevents invalid NULL[i] syntax
	index := exp.NewColumn("__i").Plus(1)
	var transformArgs []any
	for i, arg := range args {
		transformArgs = append(transformArgs, exp.New(exp.PropertyEQ,
			"this", exp.LiteralString(fmt.Sprintf("$%d", i+1)),
			"expression", exp.NewFunc("COALESCE", arg, exp.NewArray()).Bracket(index)))
	}
	transformStruct := exp.NewFunc("STRUCT", transformArgs...)
	var nullChecks, emptyChecks []any
	for _, arg := range args {
		nullChecks = append(nullChecks, arg.IsOp(exp.New(exp.Null)))
		emptyChecks = append(emptyChecks, exp.New(exp.EQ, "this", exp.New(exp.Length, "this", arg), "expression", exp.LiteralNumber(0)))
	}
	result := replace(arraysZipTemplate, kv{
		"null_check":       exp.NewOr(nullChecks...),
		"all_empty_check":  exp.NewAnd(emptyChecks...),
		"empty_struct":     emptyStruct,
		"max_len":          maxLen,
		"transform_struct": transformStruct,
	})
	return g.SQL(result)
}
