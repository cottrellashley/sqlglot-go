package duckdb

// Module-level helpers of sqlglot/generators/duckdb.py (lines 1-1585).

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"sync"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

var connectByArgsToSkip = map[string]bool{"connect": true, "where": true, "from_": true, "with_": true, "expressions": true}

// TimezonePattern detects time zones in timestamps of the form [+|-]TT[:tt].
var TimezonePattern = regexp.MustCompile(`:\d{2}.*?[+\-]\d{2}(?::\d{2})?`)

// regexEscapeReplacements are the characters that must be escaped when building regex
// expressions in INITCAP (ordered like the Python dict).
var regexEscapeReplacements = [][2]string{
	{`\`, `\\`},
	{"-", `\-`},
	{"^", `\^`},
	{"[", `\[`},
	{"]", `\]`},
}

func regexEscape(ch string) string {
	for _, r := range regexEscapeReplacements {
		if r[0] == ch {
			return r[1]
		}
	}
	return ch
}

// Used in RANDSTR transpilation
const (
	randstrCharPool = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	randstrSeed     = 123456
)

// WSControlCharsToDuck are whitespace control characters that DuckDB must process with
// `CHR({val})` calls.
var WSControlCharsToDuck = map[rune]int{'\u000b': 11, '\u001c': 28, '\u001d': 29, '\u001e': 30, '\u001f': 31}

func maxBitPosition() *exp.Expr { return exp.LiteralNumber(32768) }

// cs/as/ps are Snowflake defaults; DuckDB already behaves the same way, so they are safe to drop.
var snowflakeCollationDefaults = map[string]bool{"cs": true, "as": true, "ps": true}
var snowflakeCollationUnsupported = map[string]bool{
	"ci": true, "ai": true, "upper": true, "lower": true, "utf8": true, "bin": true, "pi": true, "fl": true,
	"fu": true, "trim": true, "ltrim": true, "rtrim": true,
}

// Window functions that support IGNORE/RESPECT NULLS in DuckDB
func isIgnoreRespectNullsWindowFunction(e *exp.Expr) bool {
	return e.IsAny(exp.FirstValue, exp.Lag, exp.LastValue, exp.Lead, exp.NthValue)
}

// lazyParse returns a function parsing sql once (Python module-level exp.maybe_parse templates).
func lazyParse(sql string) func() *exp.Expr {
	var once sync.Once
	var e *exp.Expr
	return func() *exp.Expr {
		once.Do(func() { e = sqlglot.MustParseOne(sql, nil) })
		return e
	}
}

// SEQ function constants
var (
	seqBase     = lazyParse("(ROW_NUMBER() OVER (ORDER BY 1) - 1)")
	seqUnsigned = lazyParse(":base % :max_val")
	seqSigned   = lazyParse("(CASE WHEN :base % :max_val >= :half THEN :base % :max_val - :max_val ELSE :base % :max_val END)")
)

func isSeqRestricted(e *exp.Expr) bool {
	return e.IsAny(exp.Where, exp.Having, exp.AggFunc, exp.Order, exp.Select)
}

// Maps SEQ expression types to their byte width (suffix indicates bytes: SEQ1=1, SEQ2=2, etc.)
var seqByteWidth = map[exp.Kind]int{exp.Seq1: 1, exp.Seq2: 2, exp.Seq4: 4, exp.Seq8: 8}

func applyBase64AlphabetReplacements(result, alphabet *exp.Expr, reverse bool) *exp.Expr {
	if alphabet.IsKind(exp.Literal) && alphabet.IsString() {
		chars := []rune(alphabet.Str("this"))
		for i, def := range []rune("+/=") {
			if i >= len(chars) {
				break
			}
			newChar := chars[i]
			if newChar != def {
				find, replace := string(def), string(newChar)
				if reverse {
					find, replace = replace, find
				}
				result = exp.New(exp.Replace, "this", result, "expression", exp.LiteralString(find),
					"replacement", exp.LiteralString(replace))
			}
		}
	}
	return result
}

func base64DecodeSQL(g *sqlglot.Generator, e *exp.Expr, toString bool) string {
	input := e.This()
	alphabet := e.Get("alphabet")
	// Handle custom alphabet by replacing non-standard chars with standard ones
	input = applyBase64AlphabetReplacements(input, alphabet, true)
	// FROM_BASE64 returns BLOB
	input = exp.New(exp.FromBase64, "this", input)
	if toString {
		input = exp.New(exp.Decode, "this", input)
	}
	return g.SQL(input)
}

func lastDaySQL(g *sqlglot.Generator, e *exp.Expr) string {
	dateExpr := e.This()
	unitExpr := e.Get("unit")
	if weekStart := sqlglot.WeekUnitToDow(unitExpr); weekStart != 0 {
		// The week's last day precedes its start day; DuckDB DAYOFWEEK: Sunday=0, ..., Saturday=6
		lastDow := weekStart - 1
		dow := exp.NewFunc("EXTRACT", "DAYOFWEEK", dateExpr)
		// Days to the last day of week: (last_dow + 7 - dayofweek) % 7
		daysToLast := exp.New(exp.Mod,
			"this", exp.New(exp.Paren, "this", exp.New(exp.Sub, "this", exp.LiteralNumber(lastDow+7), "expression", dow)),
			"expression", exp.LiteralNumber(7))
		interval := exp.New(exp.Interval, "this", daysToLast, "unit", exp.NewVar("DAY"))
		add := exp.New(exp.Add, "this", dateExpr, "expression", interval)
		return g.SQL(exp.NewCast(add, exp.DTypeDate))
	}
	unit := strings.ToUpper(e.Text("unit"))
	if unit == "" || unit == "MONTH" {
		// Default behavior - use DuckDB's native LAST_DAY
		return g.Func("LAST_DAY", dateExpr)
	}
	if unit == "YEAR" {
		// Last day of year: December 31st of the same year
		year := exp.NewFunc("EXTRACT", "YEAR", dateExpr)
		return g.SQL(exp.NewFunc("MAKE_DATE", year, exp.LiteralNumber(12), exp.LiteralNumber(31)))
	}
	if unit == "QUARTER" {
		year := exp.NewFunc("EXTRACT", "YEAR", dateExpr)
		quarter := exp.NewFunc("EXTRACT", "QUARTER", dateExpr)
		// Calculate last month of quarter: quarter * 3. Quarter can be 1 to 4
		lastMonth := exp.New(exp.Mul, "this", quarter, "expression", exp.LiteralNumber(3))
		firstDayLastMonth := exp.NewFunc("MAKE_DATE", year, lastMonth, exp.LiteralNumber(1))
		return g.SQL(exp.NewFunc("LAST_DAY", firstDayLastMonth))
	}
	g.Unsupported(fmt.Sprintf("Unsupported date part '%s' in LAST_DAY function", e.Text("unit")))
	return g.FunctionFallbackSQL(e)
}

func isNanosecondUnit(unit *exp.Expr) bool {
	return unit.IsAny(exp.Var, exp.Literal) && strings.ToUpper(unit.Name()) == "NANOSECOND"
}

func handleNanosecondDiff(g *sqlglot.Generator, endTime, startTime *exp.Expr) string {
	endNs := exp.NewCast(endTime, exp.DTypeTimestampNs)
	startNs := exp.NewCast(startTime, exp.DTypeTimestampNs)
	// Build expression tree: EPOCH_NS(end) - EPOCH_NS(start)
	return g.SQL(exp.New(exp.Sub, "this", exp.NewFunc("EPOCH_NS", endNs), "expression", exp.NewFunc("EPOCH_NS", startNs)))
}

func toBooleanSQL(g *sqlglot.Generator, e *exp.Expr) string {
	arg := e.This()
	isSafe := e.Bool("safe")
	upper := func() *exp.Expr { return exp.New(exp.Upper, "this", exp.NewCast(arg, exp.DTypeVarchar)) }
	baseCase := exp.NewCase(nil).
		// Handle 'on' -> TRUE (case insensitive)
		When(upper().Eq(exp.LiteralString("ON")), exp.NewTrue()).
		// Handle 'off' -> FALSE (case insensitive)
		When(upper().Eq(exp.LiteralString("OFF")), exp.NewFalse())
	var caseExpr *exp.Expr
	if isSafe {
		// TRY_TO_BOOLEAN: handle 'on'/'off' and use TRY_CAST for everything else
		caseExpr = baseCase.Else(exp.NewFunc("TRY_CAST", arg, exp.DTypeBoolean.IntoExpr()))
	} else {
		// TO_BOOLEAN: handle NaN/INF errors, 'on'/'off', and use regular CAST
		castToReal := exp.NewFunc("TRY_CAST", arg, exp.DTypeFloat.IntoExpr())
		// Check for NaN and INF values
		nanInfCheck := exp.New(exp.Or, "this", exp.NewFunc("ISNAN", castToReal), "expression", exp.NewFunc("ISINF", castToReal))
		caseExpr = baseCase.When(nanInfCheck,
			exp.NewFunc("ERROR", exp.LiteralString("TO_BOOLEAN: Non-numeric values NaN and INF are not supported")),
		).Else(exp.NewCast(arg, exp.DTypeBoolean))
	}
	return g.SQL(caseExpr)
}

// dateSQL is the BigQuery -> DuckDB conversion for the DATE function.
func dateSQL(g *sqlglot.Generator, e *exp.Expr) string {
	this := e.This()
	if zone := g.SQLArg(e, "zone"); zone != "" {
		// BigQuery considers "this" at UTC, converts it to the specified time zone and then
		// keeps only the DATE part
		this = exp.NewCast(this, exp.DTypeTimestamp)
		atUTC := exp.New(exp.AtTimeZone, "this", this, "zone", exp.LiteralString("UTC"))
		this = exp.New(exp.AtTimeZone, "this", atUTC, "zone", e.Get("zone"))
	}
	return g.SQL(exp.NewCast(this, exp.DTypeDate))
}

// timediffSQL is the BigQuery -> DuckDB conversion for the TIME_DIFF function.
func timediffSQL(g *sqlglot.Generator, e *exp.Expr) string {
	unit := e.Unit()
	if isNanosecondUnit(unit) {
		return handleNanosecondDiff(g, e.Expression(), e.This())
	}
	this := exp.NewCast(e.This(), exp.DTypeTime)
	expr := exp.NewCast(e.Expression(), exp.DTypeTime)
	// Although the 2 dialects share similar signatures, BQ seems to inverse the sign of the
	// result so the start/end time operands are flipped
	return g.Func("DATE_DIFF", sqlglot.UnitToStr(e, "DAY"), expr, this)
}

func dateDeltaToBinaryIntervalOp(cast bool) sqlglot.GenFunc {
	baseImpl := sqlglot.DateDeltaToBinaryIntervalOp(cast)
	return func(g *sqlglot.Generator, e *exp.Expr) string {
		unit := e.Unit()
		intervalValue := e.Expression()
		// Handle NANOSECOND unit (DuckDB doesn't support INTERVAL ... NANOSECOND)
		if isNanosecondUnit(unit) {
			if intervalValue.IsKind(exp.Interval) {
				intervalValue = intervalValue.This()
			}
			timestampNs := exp.NewCast(e.This(), exp.DTypeTimestampNs)
			return g.SQL(exp.NewFunc("MAKE_TIMESTAMP_NS",
				exp.New(exp.Add, "this", exp.NewFunc("EPOCH_NS", timestampNs), "expression", intervalValue)))
		}
		// Handle float/decimal interval values as duckDB INTERVAL requires integer expressions
		if intervalValue == nil || intervalValue.IsKind(exp.Interval) {
			return baseImpl(g, e)
		}
		if intervalValue.IsTypeIn(exp.DataTypeRealTypes) {
			e.Set("expression", exp.NewCast(exp.NewFunc("ROUND", intervalValue), "INT"))
		}
		return baseImpl(g, e)
	}
}

func arrayInsertSQL(g *sqlglot.Generator, e *exp.Expr) string {
	this := e.This()
	position := e.Get("position")
	element := e.Expression()
	elementArray := exp.New(exp.Array, "expressions", []*exp.Expr{element})
	indexOffset := e.Int("offset")

	if position == nil || !position.IsInt() {
		g.Unsupported("ARRAY_INSERT can only be transpiled with a literal position")
		return g.Func("ARRAY_INSERT", this, position, element)
	}
	posValue := toInt(position.MustToPy())
	// 1-based -1 appends (Spark/Databricks); the 0-based normalization below would otherwise
	// map it onto the prepend branch.
	appendAtEnd := indexOffset == 1 && posValue == -1
	// Normalize one-based indexing to zero-based for slice calculations
	if posValue > 0 {
		posValue -= indexOffset
	} else if posValue < 0 {
		posValue += indexOffset
	}
	var concat []any
	switch {
	case appendAtEnd:
		concat = []any{this, elementArray}
	case posValue == 0:
		concat = []any{elementArray, this}
	case posValue > 0:
		// Positive position: LIST_CONCAT(arr[1:pos], [elem], arr[pos+1:])
		sliceStart := exp.New(exp.Bracket, "this", this, "expressions",
			[]*exp.Expr{exp.New(exp.Slice, "this", exp.LiteralNumber(1), "expression", exp.LiteralNumber(posValue))})
		sliceEnd := exp.New(exp.Bracket, "this", this, "expressions", []*exp.Expr{exp.New(exp.Slice, "this", exp.LiteralNumber(posValue+1))})
		concat = []any{sliceStart, elementArray, sliceEnd}
	default:
		// Negative position: arr[1:LEN(arr)+pos], [elem], arr[LEN(arr)+pos+1:]
		arrLen := exp.New(exp.Length, "this", this)
		sliceEndPos := arrLen.Plus(exp.LiteralNumber(posValue))
		sliceStartPos := sliceEndPos.Plus(exp.LiteralNumber(1))
		sliceStart := exp.New(exp.Bracket, "this", this, "expressions",
			[]*exp.Expr{exp.New(exp.Slice, "this", exp.LiteralNumber(1), "expression", sliceEndPos)})
		sliceEnd := exp.New(exp.Bracket, "this", this, "expressions", []*exp.Expr{exp.New(exp.Slice, "this", sliceStartPos)})
		concat = []any{sliceStart, elementArray, sliceEnd}
	}
	// All dialects that support ARRAY_INSERT propagate NULLs (Snowflake/Spark/Databricks)
	return g.SQL(exp.New(exp.If,
		"this", exp.New(exp.Is, "this", this, "expression", exp.NewNull()),
		"true", exp.NewNull(),
		"false", g.Func("LIST_CONCAT", concat...)))
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	}
	return 0
}

func arrayRemoveAtSQL(g *sqlglot.Generator, e *exp.Expr) string {
	this := e.This()
	position := e.Get("position")
	if position == nil || !position.IsInt() {
		g.Unsupported("ARRAY_REMOVE_AT can only be transpiled with a literal position")
		return g.Func("ARRAY_REMOVE_AT", this, position)
	}
	posValue := toInt(position.MustToPy())
	var result any
	switch {
	case posValue == 0:
		// Remove first element: arr[2:]
		result = exp.New(exp.Bracket, "this", this, "expressions", []*exp.Expr{exp.New(exp.Slice, "this", exp.LiteralNumber(2))})
	case posValue > 0:
		// Remove at positive position: LIST_CONCAT(arr[1:pos], arr[pos+2:])
		left := exp.New(exp.Bracket, "this", this, "expressions",
			[]*exp.Expr{exp.New(exp.Slice, "this", exp.LiteralNumber(1), "expression", exp.LiteralNumber(posValue))})
		right := exp.New(exp.Bracket, "this", this, "expressions", []*exp.Expr{exp.New(exp.Slice, "this", exp.LiteralNumber(posValue+2))})
		result = g.Func("LIST_CONCAT", left, right)
	case posValue == -1:
		// Remove last element: arr[1:LEN(arr)-1]
		arrLen := exp.New(exp.Length, "this", this)
		sliceEnd := arrLen.Plus(exp.LiteralNumber(-1))
		result = exp.New(exp.Bracket, "this", this, "expressions",
			[]*exp.Expr{exp.New(exp.Slice, "this", exp.LiteralNumber(1), "expression", sliceEnd)})
	default:
		// Remove at negative position: LIST_CONCAT(arr[1:LEN(arr)+pos], arr[LEN(arr)+pos+2:])
		arrLen := exp.New(exp.Length, "this", this)
		sliceEndPos := arrLen.Plus(exp.LiteralNumber(posValue))
		sliceStartPos := sliceEndPos.Plus(exp.LiteralNumber(2))
		left := exp.New(exp.Bracket, "this", this, "expressions",
			[]*exp.Expr{exp.New(exp.Slice, "this", exp.LiteralNumber(1), "expression", sliceEndPos)})
		right := exp.New(exp.Bracket, "this", this, "expressions", []*exp.Expr{exp.New(exp.Slice, "this", sliceStartPos)})
		result = g.Func("LIST_CONCAT", left, right)
	}
	// Snowflake ARRAY_FUNCS_PROPAGATES_NULLS=True, so wrap in NULL check
	return g.SQL(exp.New(exp.If,
		"this", exp.New(exp.Is, "this", this, "expression", exp.NewNull()),
		"true", exp.NewNull(),
		"false", result))
}

var arraySortSQL = sqlglot.UnsupportedArgs(func(g *sqlglot.Generator, e *exp.Expr) string {
	asc, nullsFirst, ok := sqlglot.ArraySortOrder(e)
	if !ok {
		g.Unsupported("LIST_SORT only takes literal orders")
		return g.Func("LIST_SORT", e.This())
	}
	if nullsFirst == nil || !*nullsFirst {
		if asc {
			return g.Func("LIST_SORT", e.This())
		}
		return g.Func("ARRAY_REVERSE_SORT", e.This())
	}
	order := "DESC"
	if asc {
		order = "ASC"
	}
	return g.Func("LIST_SORT", e.This(), exp.LiteralString(order), exp.LiteralString("NULLS FIRST"))
}, sqlglot.ArgDiag("expression", "DuckDB's LIST_SORT does not support a comparator."))

func arrayContainsSQL(g *sqlglot.Generator, e *exp.Expr) string {
	this := e.This()
	expr := e.Expression()
	fn := g.Func("ARRAY_CONTAINS", this, expr)
	if e.Bool("check_null") {
		checkNullInArray := exp.New(exp.Nullif,
			"this", exp.New(exp.NEQ, "this", exp.New(exp.ArraySize, "this", this), "expression", exp.NewFunc("LIST_COUNT", this)),
			"expression", exp.NewFalse())
		return g.SQL(exp.New(exp.If, "this", expr.IsOp(exp.NewNull()), "true", checkNullInArray, "false", fn))
	}
	return fn
}

func arrayOverlapsSQL(g *sqlglot.Generator, e *exp.Expr) string {
	if !e.Bool("null_safe") {
		return g.Binary(e, "&&")
	}
	arr1 := e.This()
	arr2 := e.Expression()
	checkNulls := exp.NewAnd(
		exp.New(exp.NEQ, "this", exp.New(exp.ArraySize, "this", arr1.Copy()), "expression", exp.NewFunc("LIST_COUNT", arr1.Copy())),
		exp.New(exp.NEQ, "this", exp.New(exp.ArraySize, "this", arr2.Copy()), "expression", exp.NewFunc("LIST_COUNT", arr2.Copy())),
		exp.WithCopy(false),
	)
	overlap := exp.New(exp.ArrayOverlaps, "this", arr1.Copy(), "expression", arr2.Copy())
	return g.SQL(exp.NewOr(
		exp.NewParen(overlap, exp.WithCopy(false)),
		exp.NewParen(checkNulls, exp.WithCopy(false)),
		exp.WithCopy(false), exp.WithWrap(false),
	))
}

func structSQL(g *sqlglot.Generator, e *exp.Expr) string {
	ancestorCast := e.FindAncestor(exp.Cast, exp.Select)
	if ancestorCast.IsKind(exp.Select) {
		ancestorCast = nil
	}
	// Empty struct cast works with MAP() since DuckDB can't parse {}
	if len(e.Expressions()) == 0 {
		if ancestorCast.Is(exp.Cast) && ancestorCast.To().IsType(exp.DTypeMap) {
			return "MAP()"
		}
	}
	var args []string
	// BigQuery allows inline construction such as "STRUCT<a STRING, b INTEGER>('str', 1)" which is
	// canonicalized to "ROW('str', 1) AS STRUCT(a TEXT, b INT)" in DuckDB
	isBQInlineStruct := false
	if e.Find(exp.PropertyEQ) == nil && ancestorCast != nil {
		for _, t := range ancestorCast.FindAllList(exp.DataType) {
			if t.IsType(exp.DTypeStruct) {
				isBQInlineStruct = true
				break
			}
		}
	}
	for i, expr := range e.Expressions() {
		isPropertyEQ := expr.IsKind(exp.PropertyEQ)
		this := expr.This()
		value := expr
		if isPropertyEQ {
			value = expr.Expression()
		}
		if isBQInlineStruct {
			args = append(args, g.SQL(value))
			continue
		}
		var key string
		if this.IsKind(exp.Identifier) {
			key = g.SQL(exp.LiteralString(expr.Name()))
		} else if isPropertyEQ {
			key = g.SQL(this)
		} else {
			key = g.SQL(exp.LiteralString(fmt.Sprintf("_%d", i)))
		}
		args = append(args, key+": "+g.SQL(value))
	}
	csvArgs := strings.Join(args, ", ")
	if isBQInlineStruct {
		return "ROW(" + csvArgs + ")"
	}
	return "{" + csvArgs + "}"
}

var jsonConstructorSQL = sqlglot.UnsupportedArgs(func(g *sqlglot.Generator, e *exp.Expr) string {
	if e.Str("null_handling") == "ABSENT ON NULL" {
		g.Unsupported("ABSENT ON NULL is not supported in DuckDB")
	}
	name := "JSON_OBJECT"
	if e.IsKind(exp.JSONArray) {
		name = "JSON_ARRAY"
	}
	return g.Func(name, e.Expressions())
}, "unique_keys", "return_type", "encoding", "strict")

func jsonFormatSQL(g *sqlglot.Generator, e *exp.Expr) string {
	sql := g.Func("TO_JSON", e.This(), e.Arg("options"))
	return "CAST(" + sql + " AS TEXT)"
}

func pow2(bits int) *exp.Expr {
	return exp.LiteralNumber(new(big.Int).Lsh(big.NewInt(1), uint(bits)))
}

func buildSeqExpression(base *exp.Expr, byteWidth int, signed bool) *exp.Expr {
	bits := byteWidth * 8
	maxVal := pow2(bits)
	if signed {
		half := pow2(bits - 1)
		return exp.ReplacePlaceholders(seqSigned().Copy(), nil, map[string]any{"base": base, "max_val": maxVal, "half": half})
	}
	return exp.ReplacePlaceholders(seqUnsigned().Copy(), nil, map[string]any{"base": base, "max_val": maxVal})
}

func seqToRangeInGenerator(e *exp.Expr) *exp.Expr {
	if !e.IsKind(exp.Select) {
		return e
	}
	from := e.Get("from_")
	if !(from != nil && from.This().IsKind(exp.TableFromRows) && from.This().This().IsKind(exp.Generator)) {
		return e
	}
	return e.Transform(func(node *exp.Expr) *exp.Expr {
		if bw, ok := seqByteWidth[node.Kind()]; ok {
			return buildSeqExpression(exp.NewColumn("range"), bw, node.Name() == "1")
		}
		return node
	}, false)
}

// ConnectByToRecursiveCte rewrites START WITH ... CONNECT BY PRIOR into WITH RECURSIVE. It
// falls through unchanged if there are no PRIORs.
func ConnectByToRecursiveCte(e *exp.Expr) *exp.Expr {
	if !e.IsKind(exp.Select) || !e.Bool("connect") {
		return e
	}
	connect := e.Get("connect")
	connectPred := connect.Get("connect")
	priors := connectPred.FindAllList(exp.Prior)
	if len(priors) == 0 {
		return e
	}
	from := e.Get("from_")
	if from == nil || e.Bool("joins") {
		return e
	}
	sourceTable := from.This()
	baseSelectExprs := e.Expressions()
	baseWhere := e.Get("where")
	baseWith := e.Get("with_")

	// LEVEL is a Snowflake pseudo-column: it's always computed as a depth counter in the CTE.
	hasLevel := false
	for _, x := range baseSelectExprs {
		for _, col := range x.FindAllList(exp.Column) {
			if strings.ToUpper(col.Name()) == "LEVEL" {
				hasLevel = true
			}
		}
	}
	hasStar := e.IsStar()

	// CONNECT_BY_ROOT col yields the value of `col` from the START WITH row that begins each branch.
	var rootColNames []string
	var anchorRootCols, innerRootCols []any
	var roots []*exp.Expr
	for _, x := range baseSelectExprs {
		roots = append(roots, x.FindAllList(exp.ConnectByRoot)...)
	}
	for i, root := range roots {
		name := fmt.Sprintf("_connect_by_root_%d", i)
		rootColNames = append(rootColNames, name)
		anchorRootCols = append(anchorRootCols, exp.NewAlias(root.This(), name))
		innerRootCols = append(innerRootCols, exp.NewAlias(exp.NewColumn(name, "_parent_row"), name))
		root.Replace(exp.NewColumn(name))
	}

	// Build the join condition from the full CONNECT BY predicate:
	// PRIOR(col) → _parent_row.col, unqualified cols → _child_row.col.
	qualifyConnectPred := func(node *exp.Expr) *exp.Expr {
		for _, col := range collect(sqlglot.FindAllInScope(node, exp.Column)) {
			table := "_child_row"
			if col.Parent().IsKind(exp.Prior) {
				table = "_parent_row"
			}
			col.Set("table", exp.ToIdentifier(table))
		}
		for _, prior := range collect(sqlglot.FindAllInScope(node, exp.Prior)) {
			prior.Replace(prior.This())
		}
		return node
	}

	// Avoid colliding with any CTE names already on the query.
	taken := map[string]bool{}
	if baseWith != nil {
		for _, cte := range baseWith.Expressions() {
			taken[cte.Alias()] = true
		}
	}
	cteName := helper.FindNewNameIn(taken, "_rootcte")

	// Anchor: project all source columns + seed LEVEL at 1 + bind each root column to its own value.
	anchorSelects := append([]any{exp.New(exp.Star), exp.NewAlias(exp.LiteralNumber(1), "level")}, anchorRootCols...)
	anchor := exp.NewSelect(anchorSelects...).From(sourceTable)
	if start := connect.Get("start"); start != nil {
		anchor = anchor.Where(start)
	}

	// Recursive arm: carry all child columns + increment level + forward each root value.
	innerSelects := append([]any{
		exp.New(exp.Column, "this", exp.New(exp.Star), "table", exp.ToIdentifier("_child_row")),
		exp.NewAlias(exp.NewColumn("level", "_parent_row").Plus(1), "level"),
	}, innerRootCols...)
	innerQuery := exp.NewSelect(innerSelects...).
		From(sourceTable.As("_child_row")).
		Join(exp.ToTable(cteName, nil).As("_parent_row"), exp.JoinOptions{On: qualifyConnectPred(connectPred)})

	// Outer SELECT re-projects from the CTE. Synthetic level/root columns are excluded from any
	// star expansion (level only when not referenced) but kept where explicitly projected.
	var outerSelectExprs []any
	if hasStar {
		var exceptCols []*exp.Expr
		if !hasLevel {
			exceptCols = append(exceptCols, exp.NewColumn("level"))
		}
		for _, name := range rootColNames {
			exceptCols = append(exceptCols, exp.NewColumn(name))
		}
		star := exp.New(exp.Star)
		if len(exceptCols) > 0 {
			star = exp.New(exp.Star, "except_", exceptCols)
		}
		outerSelectExprs = append(outerSelectExprs, star)
		for _, x := range baseSelectExprs {
			if !x.IsStar() {
				outerSelectExprs = append(outerSelectExprs, x)
			}
		}
	} else {
		for _, x := range baseSelectExprs {
			outerSelectExprs = append(outerSelectExprs, x)
		}
	}
	outerQuery := exp.NewSelect(outerSelectExprs...).From(cteName)
	if baseWhere != nil {
		outerQuery = outerQuery.Where(baseWhere.This())
	}
	// Attach the CTE, marking the WITH clause recursive.
	if baseWith != nil {
		outerQuery.Set("with_", baseWith)
	}
	outerQuery = outerQuery.WithCTE(cteName, anchor.Union(innerQuery, exp.WithDistinct(false)),
		exp.CTEOptions{Recursive: true}, exp.WithCopy(false))

	for _, a := range e.Args() {
		if exp.Truthy(a.Value) && !connectByArgsToSkip[a.Key] {
			outerQuery.Set(a.Key, a.Value)
		}
	}
	// Strip stale source table qualifiers in one pass; CTEs are child scopes so
	// find_all_in_scope stays within the outer query only.
	for _, col := range collect(sqlglot.FindAllInScope(outerQuery, exp.Column)) {
		col.Set("table", nil)
	}
	return outerQuery
}

func collect(seq func(func(*exp.Expr) bool)) []*exp.Expr {
	var out []*exp.Expr
	seq(func(e *exp.Expr) bool {
		out = append(out, e)
		return true
	})
	return out
}

func seqSQL(g *sqlglot.Generator, e *exp.Expr, byteWidth int) string {
	// Warn if SEQ is in a restricted context (Select stops search at current scope)
	ancestor := e.FindAncestor(exp.Where, exp.Having, exp.AggFunc, exp.Order, exp.Select)
	if ancestor != nil && (!ancestor.IsAny(exp.Order, exp.Select) || (ancestor.IsKind(exp.Order) && ancestor.Parent().IsKind(exp.Window))) {
		g.Unsupported("SEQ in restricted context is not supported - use CTE or subquery")
	}
	return g.SQL(buildSeqExpression(seqBase().Copy(), byteWidth, e.Name() == "1"))
}

func unixToTimeSQL(g *sqlglot.Generator, e *exp.Expr) string {
	scale := e.Get("scale")
	timestamp := e.This()
	targetType := e.Get("target_type")
	// Check if we need NTZ (naive timestamp in UTC)
	isNtz := targetType != nil && (targetType.DataTypeThis() == exp.DTypeTimestamp || targetType.DataTypeThis() == exp.DTypeTimestampntz)
	if scale.Equal(exp.UnixToTimeMillis()) {
		// EPOCH_MS already returns TIMESTAMP (naive, UTC)
		return g.Func("EPOCH_MS", timestamp)
	}
	if scale.Equal(exp.UnixToTimeMicros()) {
		// MAKE_TIMESTAMP already returns TIMESTAMP (naive, UTC)
		return g.Func("MAKE_TIMESTAMP", timestamp)
	}
	// Other scales: divide and use TO_TIMESTAMP
	if scale != nil && !scale.Equal(exp.UnixToTimeSeconds()) {
		timestamp = exp.New(exp.Div, "this", timestamp, "expression", exp.NewFunc("POW", 10, scale))
	}
	toTimestamp := exp.New(exp.Anonymous, "this", "TO_TIMESTAMP", "expressions", []*exp.Expr{timestamp})
	if isNtz {
		toTimestamp = exp.New(exp.AtTimeZone, "this", toTimestamp, "zone", exp.LiteralString("UTC"))
	}
	return g.SQL(toTimestamp)
}

func isWrappedJSONExtractParent(e *exp.Expr) bool {
	return e.IsAny(exp.Binary, exp.Bracket, exp.In, exp.Not)
}

func arrowJSONExtractSQL(g *sqlglot.Generator, e *exp.Expr) string {
	paths := e.Expressions()
	if e.IsKind(exp.JSONExtract) && len(paths) > 0 {
		// JSON_EXTRACT with several paths (SQLite, MySQL) returns a JSON array of the results,
		// which DuckDB builds from a list of paths
		return g.Func("TO_JSON", g.Func("JSON_EXTRACT", e.This(),
			exp.New(exp.Array, "expressions", append([]*exp.Expr{e.Expression()}, paths...))))
	}
	arrowSQL := sqlglot.ArrowJSONExtractSQL(g, e, "")
	if !e.SameParent() && isWrappedJSONExtractParent(e.Parent()) {
		arrowSQL = g.Wrap(arrowSQL)
	}
	return arrowSQL
}

func implicitDatetimeCast(arg *exp.Expr, typ exp.DType) *exp.Expr {
	if arg.IsKind(exp.Literal) && arg.IsString() {
		ts := arg.Name()
		if typ == exp.DTypeDate && strings.Contains(ts, ":") {
			if TimezonePattern.MatchString(ts) {
				typ = exp.DTypeTimestamptz
			} else {
				typ = exp.DTypeTimestamp
			}
		}
		arg = exp.NewCast(arg, typ)
	}
	return arg
}

func weekTruncStartDow(unit *exp.Expr) int {
	// DuckDB's weeks are ISO 8601, so ISOWEEK maps to its plain WEEK unit
	if unit.IsKind(exp.Literal) && strings.ToUpper(unit.Name()) == "ISOWEEK" {
		return 1
	}
	return sqlglot.WeekUnitToDow(unit)
}

func buildWeekTruncExpression(dateExpr *exp.Expr, startDow int, preserveStartDay, castToDate bool) *exp.Expr {
	shiftDays := 1 - startDow
	if startDow == 7 {
		shiftDays = 1
	}
	truncated := exp.New(exp.DateTrunc, "unit", exp.NewVar("WEEK"), "this", dateExpr)
	if shiftDays == 0 {
		return truncated
	}
	shift := exp.New(exp.Interval, "this", exp.LiteralString(fmt.Sprint(shiftDays)), "unit", exp.NewVar("DAY"))
	shiftedDate := exp.New(exp.DateAdd, "this", dateExpr, "expression", shift)
	truncated.Set("this", shiftedDate)
	if preserveStartDay {
		interval := exp.New(exp.Interval, "this", exp.LiteralString(fmt.Sprint(-shiftDays)), "unit", exp.NewVar("DAY"))
		shiftedBack := exp.New(exp.DateAdd, "this", truncated, "expression", interval)
		if castToDate {
			return exp.NewCast(shiftedBack, exp.DTypeDate, exp.WithCopy(false))
		}
		return shiftedBack
	}
	return truncated
}

func dateDiffSQL(g *sqlglot.Generator, e *exp.Expr) string {
	unit := e.Unit()
	if isNanosecondUnit(unit) {
		return handleNanosecondDiff(g, e.This(), e.Expression())
	}
	this := implicitDatetimeCast(e.This(), exp.DTypeDate)
	expr := implicitDatetimeCast(e.Expression(), exp.DTypeDate)
	// DuckDB's WEEK diff does not respect Monday crossing (week boundaries), it checks
	// (end_day - start_day) / 7, whereas for other units such as MONTH it does respect month boundaries
	datePartBoundary := e.Bool("date_part_boundary")
	// Extract week start day; returns None if day is dynamic (column/placeholder)
	weekStart := sqlglot.WeekUnitToDow(unit)
	if datePartBoundary && weekStart != 0 && this != nil && expr != nil {
		e.Set("unit", exp.LiteralString("WEEK"))
		// Truncate both dates to week boundaries to respect input dialect semantics
		this = buildWeekTruncExpression(this, weekStart, false, true)
		expr = buildWeekTruncExpression(expr, weekStart, false, true)
	}
	return g.Func("DATE_DIFF", sqlglot.UnitToStr(e, "DAY"), expr, this)
}

func generateDatetimeArraySQL(g *sqlglot.Generator, e *exp.Expr) string {
	isGenerateDateArray := e.IsKind(exp.GenerateDateArray)
	typ := exp.DTypeTimestamp
	if isGenerateDateArray {
		typ = exp.DTypeDate
	}
	start := implicitDatetimeCast(e.Get("start"), typ)
	end := implicitDatetimeCast(e.Get("end"), typ)
	// BQ's GENERATE_DATE_ARRAY & GENERATE_TIMESTAMP_ARRAY are transformed to DuckDB'S GENERATE_SERIES
	genSeries := exp.New(exp.GenerateSeries, "start", start, "end", end, "step", e.Arg("step"))
	if isGenerateDateArray {
		// The GENERATE_SERIES result type is TIMESTAMP array, so to match BQ's semantics for
		// GENERATE_DATE_ARRAY we must cast it back to DATE array
		genSeries = exp.NewCast(genSeries, exp.MustDataTypeBuild("ARRAY<DATE>", nil, false))
	}
	return g.SQL(genSeries)
}

func jsonExtractValueArraySQL(g *sqlglot.Generator, e *exp.Expr) string {
	jsonExtract := exp.New(exp.JSONExtract, "this", e.This(), "expression", e.Expression())
	dataType := "ARRAY<JSON>"
	if e.IsKind(exp.JSONValueArray) {
		dataType = "ARRAY<STRING>"
	}
	return g.SQL(exp.NewCast(jsonExtract, exp.MustDataTypeBuild(dataType, nil, false)))
}

func castToVarchar(arg *exp.Expr) *exp.Expr {
	if arg != nil && arg.Type() != nil && !arg.IsTypeIn(exp.DataTypeTextTypes, exp.NewTypeSet(exp.DTypeUnknown)) {
		return exp.NewCast(arg, exp.DTypeVarchar)
	}
	return arg
}

func castToBoolean(arg *exp.Expr) *exp.Expr {
	if arg != nil && !arg.IsType(exp.DTypeBoolean) {
		return exp.NewCast(arg, exp.DTypeBoolean)
	}
	return arg
}

func isBinary(arg *exp.Expr) bool {
	return arg.IsType(exp.DTypeBinary, exp.DTypeVarbinary, exp.DTypeBlob)
}

func blobType() *exp.Expr { return exp.MustDataTypeBuild("BLOB", "duckdb", false) }

func genWithCastToBlob(g *sqlglot.Generator, e *exp.Expr, resultSQL string) string {
	if isBinary(e) {
		resultSQL = g.SQL(exp.New(exp.Cast, "this", resultSQL, "to", blobType()))
	}
	return resultSQL
}

func castToBit(arg *exp.Expr) *exp.Expr {
	if !isBinary(arg) {
		return arg
	}
	if arg.IsKind(exp.HexString) {
		arg = exp.New(exp.Unhex, "this", exp.LiteralString(arg.Str("this")))
	}
	return exp.NewCast(arg, exp.DTypeBit)
}

func prepareBinaryBitwiseArgs(e *exp.Expr) {
	if isBinary(e.This()) {
		e.Set("this", castToBit(e.This()))
	}
	if isBinary(e.Expression()) {
		e.Set("expression", castToBit(e.Expression()))
	}
}

var weekStartDays = []string{"MONDAY", "TUESDAY", "WEDNESDAY", "THURSDAY", "FRIDAY", "SATURDAY", "SUNDAY"}

func dayNavigationSQL(g *sqlglot.Generator, e *exp.Expr) string {
	dateExpr := e.This()
	dayNameExpr := e.Expression()
	// Build ISODOW call for current day of week
	isodowCall := exp.NewFunc("ISODOW", dateExpr)
	var targetDow *exp.Expr
	if dayNameExpr.IsKind(exp.Literal) {
		// Literal day name: lookup target_dow directly
		dayNameStr := strings.ToUpper(dayNameExpr.Name())
		matching := ""
		for _, day := range weekStartDays {
			if strings.HasPrefix(day, dayNameStr) {
				matching = day
				break
			}
		}
		if matching == "" {
			// Unrecognized day name, use fallback
			return g.FunctionFallbackSQL(e)
		}
		targetDow = exp.LiteralNumber(sqlglot.WeekStartDayToDow[matching])
	} else {
		// Non-literal day name: build CASE statement for runtime mapping
		upperDayName := exp.New(exp.Upper, "this", dayNameExpr)
		var ifs []*exp.Expr
		for _, day := range weekStartDays {
			ifs = append(ifs, exp.New(exp.If,
				"this", exp.NewFunc("STARTS_WITH", upperDayName.Copy(), exp.LiteralString(day[:2])),
				"true", exp.LiteralNumber(sqlglot.WeekStartDayToDow[day])))
		}
		targetDow = exp.New(exp.Case, "ifs", ifs)
	}
	var dateWithOffset *exp.Expr
	if e.IsKind(exp.NextDay) {
		// NEXT_DAY: (target_dow - current_dow + 6) % 7 + 1
		daysOffset := exp.NewParen(targetDow.Minus(isodowCall).Plus(6), exp.WithCopy(false)).Modulo(7).Plus(1)
		dateWithOffset = dateExpr.Plus(exp.New(exp.Interval, "this", daysOffset, "unit", exp.NewVar("DAY")))
	} else {
		// PREVIOUS_DAY: (current_dow - target_dow + 6) % 7 + 1
		daysOffset := exp.NewParen(isodowCall.Minus(targetDow).Plus(6), exp.WithCopy(false)).Modulo(7).Plus(1)
		dateWithOffset = dateExpr.Minus(exp.New(exp.Interval, "this", daysOffset, "unit", exp.NewVar("DAY")))
	}
	return g.SQL(exp.NewCast(dateWithOffset, exp.DTypeDate))
}

func anyvalueSQL(g *sqlglot.Generator, e *exp.Expr) string {
	// Transform ANY_VALUE(expr HAVING MAX/MIN having_expr) to ARG_MAX_NULL/ARG_MIN_NULL
	if having := e.This(); having.IsKind(exp.HavingMax) {
		name := "ARG_MIN_NULL"
		if having.Bool("max") {
			name = "ARG_MAX_NULL"
		}
		return g.Func(name, having.This(), having.Expression())
	}
	return g.FunctionFallbackSQL(e)
}

func bitwiseAggSQL(g *sqlglot.Generator, e *exp.Expr) string {
	funcName := "BIT_XOR"
	if e.IsKind(exp.BitwiseOrAgg) {
		funcName = "BIT_OR"
	} else if e.IsKind(exp.BitwiseAndAgg) {
		funcName = "BIT_AND"
	}
	arg := e.This()
	if arg.Type() == nil {
		arg = sqlglot.AnnotateTypes(arg, g.D)
	}
	if arg.IsTypeIn(exp.DataTypeRealTypes, exp.DataTypeTextTypes) {
		if arg.IsTypeIn(exp.DataTypeFloatTypes) {
			// float types need to be rounded first due to precision loss
			arg = exp.NewFunc("ROUND", arg)
		}
		arg = exp.NewCast(arg, exp.DTypeInt)
	}
	return g.Func(funcName, arg)
}

func literalSQLWithWsChr(g *sqlglot.Generator, literal string) string {
	// DuckDB does not support \uXXXX escapes, so we must use CHR() instead of replacing them directly
	has := false
	for _, ch := range literal {
		if _, ok := WSControlCharsToDuck[ch]; ok {
			has = true
			break
		}
	}
	if !has {
		return g.SQL(exp.LiteralString(literal))
	}
	var segments []string
	runes := []rune(literal)
	for i := 0; i < len(runes); {
		_, isWS := WSControlCharsToDuck[runes[i]]
		j := i
		for j < len(runes) {
			_, w := WSControlCharsToDuck[runes[j]]
			if w != isWS {
				break
			}
			j++
		}
		if isWS {
			for _, ch := range runes[i:j] {
				segments = append(segments, g.Func("CHR", exp.LiteralNumber(fmt.Sprint(WSControlCharsToDuck[ch]))))
			}
		} else {
			segments = append(segments, g.SQL(exp.LiteralString(string(runes[i:j]))))
		}
		i = j
	}
	sql := strings.Join(segments, " || ")
	if len(segments) == 1 {
		return sql
	}
	return "(" + sql + ")"
}

func escapeRegexMetachars(g *sqlglot.Generator, delimiters *exp.Expr, delimitersSQL string) string {
	if delimiters == nil {
		return delimitersSQL
	}
	if delimiters.IsString() {
		var b strings.Builder
		for _, ch := range delimiters.Str("this") {
			b.WriteString(regexEscape(string(ch)))
		}
		return literalSQLWithWsChr(g, b.String())
	}
	escapedSQL := delimitersSQL
	for _, r := range regexEscapeReplacements {
		escapedSQL = g.Func("REPLACE", escapedSQL, g.SQL(exp.LiteralString(r[0])), g.SQL(exp.LiteralString(r[1])))
	}
	return escapedSQL
}

func buildCapitalizationSQL(g *sqlglot.Generator, valueToSplit, delimitersSQL string) string {
	// empty string delimiter --> treat value as one word, no need to split
	if delimitersSQL == "''" {
		return "UPPER(LEFT(" + valueToSplit + ", 1)) || LOWER(SUBSTRING(" + valueToSplit + ", 2))"
	}
	delimRegexSQL := "CONCAT('[', " + delimitersSQL + ", ']')"
	splitRegexSQL := "CONCAT('([', " + delimitersSQL + ", ']+|[^', " + delimitersSQL + ", ']+)')"
	// REGEXP_EXTRACT_ALL produces a list of string segments, alternating between delimiter and
	// non-delimiter segments. If the first char is a delimiter, we capitalize even list indexes,
	// otherwise capitalize odd.
	return g.Func("ARRAY_TO_STRING",
		exp.NewCase(nil).When(
			"REGEXP_MATCHES(LEFT("+valueToSplit+", 1), "+delimRegexSQL+")",
			g.Func("LIST_TRANSFORM",
				g.Func("REGEXP_EXTRACT_ALL", valueToSplit, splitRegexSQL),
				"(seg, idx) -> CASE WHEN idx % 2 = 0 THEN UPPER(LEFT(seg, 1)) || LOWER(SUBSTRING(seg, 2)) ELSE seg END",
			),
		).Else(
			g.Func("LIST_TRANSFORM",
				g.Func("REGEXP_EXTRACT_ALL", valueToSplit, splitRegexSQL),
				"(seg, idx) -> CASE WHEN idx % 2 = 1 THEN UPPER(LEFT(seg, 1)) || LOWER(SUBSTRING(seg, 2)) ELSE seg END",
			),
		),
		"''",
	)
}

func initcapSQL(g *sqlglot.Generator, e *exp.Expr) string {
	thisSQL := g.SQLArg(e, "this")
	delimiters := e.Get("expression")
	if delimiters == nil {
		// fallback for manually created exp.Initcap w/o delimiters arg
		delimiters = exp.LiteralString(g.D.InitcapDefaultDelimiterChars)
	}
	delimitersSQL := g.SQL(delimiters)
	escaped := escapeRegexMetachars(g, delimiters, delimitersSQL)
	return buildCapitalizationSQL(g, thisSQL, escaped)
}

func boolxorAggSQL(g *sqlglot.Generator, e *exp.Expr) string {
	return g.SQL(exp.New(exp.EQ,
		"this", exp.New(exp.CountIf, "this", castToBoolean(e.This())),
		"expression", exp.LiteralNumber(1)))
}

func bitshiftSQL(g *sqlglot.Generator, e *exp.Expr) string {
	operator := ">>"
	if e.IsKind(exp.BitwiseLeftShift) {
		operator = "<<"
	}
	resultIsBlob := false
	this := e.This()
	if isBinary(this) {
		resultIsBlob = true
		e.Set("this", exp.NewCast(this, exp.DTypeBit))
	} else if e.Bool("requires_int128") {
		this.Replace(exp.NewCast(this, exp.DTypeInt128))
	}
	resultSQL := g.Binary(e, operator)
	// Wrap in parentheses if parent is a bitwise operator to "fix" DuckDB precedence issue
	if e.Parent().Is(exp.Binary) {
		resultSQL = g.SQL(exp.New(exp.Paren, "this", resultSQL))
	}
	if resultIsBlob {
		resultSQL = g.SQL(exp.New(exp.Cast, "this", resultSQL, "to", blobType()))
	}
	return resultSQL
}

func scaleRoundingSQL(g *sqlglot.Generator, e *exp.Expr, roundingKind exp.Kind) (string, bool) {
	decimals := e.Get("decimals")
	if decimals == nil || e.Arg("to") != nil {
		return "", false
	}
	this := e.This()
	if this.Is(exp.Binary) {
		this = exp.New(exp.Paren, "this", this)
	}
	nInt := decimals
	if !(decimals.IsInt() || decimals.IsTypeIn(exp.DataTypeIntegerTypes)) {
		nInt = exp.NewCast(decimals, exp.DTypeInt)
	}
	pow := exp.New(exp.Pow, "this", exp.LiteralNumber("10"), "expression", nInt)
	rounded := exp.New(roundingKind, "this", exp.New(exp.Mul, "this", this, "expression", pow))
	result := exp.New(exp.Div, "this", rounded, "expression", pow.Copy())
	return roundSQL(g, exp.New(exp.Round, "this", result, "decimals", decimals, "casts_non_integer_decimals", true)), true
}

func ceilFloorSQL(g *sqlglot.Generator, e *exp.Expr) string {
	if sql, ok := scaleRoundingSQL(g, e, e.Kind()); ok {
		return sql
	}
	return g.CeilFloor(e)
}

func regrValSQL(g *sqlglot.Generator, e *exp.Expr) string {
	y, x := e.This(), e.Expression()
	// Determine which argument to check for NULL and which to return based on expression type
	var checkForNull, returnValue *exp.Expr
	returnValueAttr := "this"
	if e.IsKind(exp.RegrValx) {
		// REGR_VALX: check y for NULL, return x
		checkForNull, returnValue, returnValueAttr = y, x, "expression"
	} else {
		// REGR_VALY: check x for NULL, return y
		checkForNull, returnValue = x, y
	}
	resultType := returnValue.Type()
	// If no type info, annotate the expression to infer types
	if resultType == nil || resultType.DataTypeThis() == exp.DTypeUnknown {
		func() {
			defer func() { recover() }()
			annotated := sqlglot.AnnotateTypes(e.Copy(), g.D)
			resultType = annotated.Get(returnValueAttr).Type()
		}()
	}
	// Default to DOUBLE for regression functions if type still unknown
	if resultType == nil || resultType.DataTypeThis() == exp.DTypeUnknown {
		resultType = exp.DTypeDouble.IntoExpr()
	}
	// Cast NULL to the same type as return_value to avoid DuckDB type inference issues
	typedNull := exp.New(exp.Cast, "this", exp.NewNull(), "to", resultType)
	return g.SQL(exp.New(exp.If,
		"this", exp.New(exp.Is, "this", checkForNull.Copy(), "expression", exp.NewNull()),
		"true", typedNull,
		"false", returnValue.Copy()))
}

func maybeCorrNullToFalse(e *exp.Expr) *exp.Expr {
	corr := e
	for corr.IsAny(exp.Window, exp.Filter) {
		corr = corr.This()
	}
	if !corr.IsKind(exp.Corr) || !corr.Bool("null_on_zero_variance") {
		return nil
	}
	corr.Set("null_on_zero_variance", false)
	return e
}

func dateFromPartsSQL(g *sqlglot.Generator, e *exp.Expr) string {
	yearExpr, monthExpr, dayExpr := e.Get("year"), e.Get("month"), e.Get("day")
	if e.Bool("allow_overflow") {
		baseDate := exp.NewFunc("MAKE_DATE", yearExpr, exp.LiteralNumber(1), exp.LiteralNumber(1))
		if monthExpr != nil {
			baseDate = baseDate.Plus(exp.New(exp.Interval, "this", monthExpr.Minus(1), "unit", exp.NewVar("MONTH")))
		}
		if dayExpr != nil {
			baseDate = baseDate.Plus(exp.New(exp.Interval, "this", dayExpr.Minus(1), "unit", exp.NewVar("DAY")))
		}
		return g.SQL(exp.NewCast(baseDate, exp.DTypeDate))
	}
	return g.Func("MAKE_DATE", yearExpr, monthExpr, dayExpr)
}

func roundArg(arg *exp.Expr, roundInput bool) *exp.Expr {
	if roundInput {
		return exp.NewFunc("ROUND", arg, exp.LiteralNumber(0))
	}
	return arg
}

func boolnotSQL(g *sqlglot.Generator, e *exp.Expr) string {
	arg := roundArg(e.This(), e.Bool("round_input"))
	return g.SQL(exp.NewNot(exp.NewParen(arg)))
}

func boolandSQL(g *sqlglot.Generator, e *exp.Expr) string {
	roundInput := e.Bool("round_input")
	left, right := roundArg(e.This(), roundInput), roundArg(e.Expression(), roundInput)
	return g.SQL(exp.NewParen(exp.NewAnd(exp.NewParen(left), exp.NewParen(right), exp.WithWrap(false))))
}

func boolorSQL(g *sqlglot.Generator, e *exp.Expr) string {
	roundInput := e.Bool("round_input")
	left, right := roundArg(e.This(), roundInput), roundArg(e.Expression(), roundInput)
	return g.SQL(exp.NewParen(exp.NewOr(exp.NewParen(left), exp.NewParen(right), exp.WithWrap(false))))
}

func xorSQL(g *sqlglot.Generator, e *exp.Expr) string {
	roundInput := e.Bool("round_input")
	left, right := roundArg(e.This(), roundInput), roundArg(e.Expression(), roundInput)
	return g.SQL(exp.NewOr(
		exp.NewParen(exp.NewAnd(left.Copy(), exp.NewParen(right.Not()), exp.WithWrap(false))),
		exp.NewParen(exp.NewAnd(exp.NewParen(left.Not()), right.Copy(), exp.WithWrap(false))),
		exp.WithWrap(false),
	))
}

func explodeToUnnestSQL(g *sqlglot.Generator, e *exp.Expr) string {
	explode := e.This()
	if explode.IsKind(exp.Inline) {
		// For INLINE, create CROSS JOIN LATERAL (SELECT UNNEST(..., max_depth => 2))
		unnest := exp.New(exp.Unnest, "expressions", []*exp.Expr{
			explode.This(),
			exp.New(exp.Kwarg, "this", exp.NewVar("max_depth"), "expression", exp.LiteralNumber(2)),
		})
		selectExpr := exp.New(exp.Select, "expressions", []*exp.Expr{unnest}).Subquery(nil, true)
		alias := e.Get("alias")
		if alias != nil && alias.This() == nil {
			// we need to provide a table name if not present
			index := "None"
			if e.Index() >= 0 {
				index = fmt.Sprint(e.Index())
			}
			alias.Set("this", exp.ToIdentifier("_u_"+index))
		}
		lateral := exp.New(exp.Lateral, "this", selectExpr, "alias", alias)
		return g.SQL(exp.New(exp.Join, "this", lateral, "kind", "CROSS"))
	}
	// For other cases, use the standard conversion
	return sqlglot.ExplodeToUnnestSQL(g, e)
}

func shaSQL(g *sqlglot.Generator, e *exp.Expr, hashFunc string, isBinaryResult bool) string {
	arg := e.This()
	// For SHA2 variants, check digest length (DuckDB only supports SHA256)
	if hashFunc == "SHA256" {
		length := e.Text("length")
		if length == "" {
			length = "256"
		}
		if length != "256" {
			g.Unsupported("DuckDB only supports SHA256 hashing algorithm.")
		}
	}
	// Cast if type is incompatible with DuckDB
	if arg.Type() != nil && arg.Type().DataTypeThis() != exp.DTypeUnknown && !arg.IsTypeIn(exp.DataTypeTextTypes) && !isBinary(arg) {
		arg = exp.NewCast(arg, exp.DTypeVarchar)
	}
	result := g.Func(hashFunc, arg)
	if isBinaryResult {
		return g.Func("UNHEX", result)
	}
	return result
}
