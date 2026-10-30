package duckdb

// DuckDBGenerator methods, part 3 (sqlglot/generators/duckdb.py: lower_sql .. uuid_sql).

import (
	"fmt"
	"math/big"
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/internal/pydec"
)

func lowerSQL(g *gen, e *exp.Expr) string {
	return genWithCastToBlob(g, e, g.Func("LOWER", castToVarchar(e.This())))
}

func upperSQL(g *gen, e *exp.Expr) string {
	return genWithCastToBlob(g, e, g.Func("UPPER", castToVarchar(e.This())))
}

func reverseSQL(g *gen, e *exp.Expr) string {
	return genWithCastToBlob(g, e, g.Func("REVERSE", castToVarchar(e.This())))
}

func leftRightSQL(g *gen, e *exp.Expr, funcName string) string {
	arg := e.This()
	length := e.Expression()
	binary := isBinary(arg)
	var result *exp.Expr
	if binary {
		// LEFT/RIGHT(blob, n) becomes UNHEX(LEFT/RIGHT(HEX(blob), n * 2))
		// Each byte becomes 2 hex chars, so multiply length by 2
		hexArg := exp.New(exp.Hex, "this", arg)
		hexLength := exp.New(exp.Mul, "this", length, "expression", exp.LiteralNumber(2))
		result = exp.New(exp.Unhex, "this", exp.New(exp.Anonymous, "this", funcName, "expressions", []*exp.Expr{hexArg, hexLength}))
	} else {
		result = exp.New(exp.Anonymous, "this", funcName, "expressions", []*exp.Expr{arg, length})
	}
	if e.Bool("negative_length_returns_empty") {
		empty := exp.LiteralString("")
		if binary {
			empty = exp.New(exp.Unhex, "this", empty)
		}
		result = exp.NewCase(nil).When(length.Lt(exp.LiteralNumber(0)), empty).Else(result)
	}
	return g.SQL(result)
}

func rtrimmedlengthSQL(g *gen, e *exp.Expr) string {
	return g.Func("LENGTH", exp.New(exp.Trim, "this", e.This(), "position", "TRAILING"))
}

func stuffSQL(g *gen, e *exp.Expr) string {
	base := e.This()
	start := e.Get("start")
	length := e.Get("length")
	insertion := e.Expression()
	binary := isBinary(base)
	var left, right *exp.Expr
	if binary {
		// DuckDB's SUBSTRING doesn't accept BLOB; operate on the HEX string instead
		// (each byte = 2 hex chars), then UNHEX back to BLOB
		base = exp.New(exp.Hex, "this", base)
		insertion = exp.New(exp.Hex, "this", insertion)
		left = exp.New(exp.Substring,
			"this", base.Copy(),
			"start", exp.LiteralNumber(1),
			"length", start.Copy().Minus(exp.LiteralNumber(1)).Times(exp.LiteralNumber(2)))
		right = exp.New(exp.Substring,
			"this", base.Copy(),
			"start", start.Plus(length).Minus(exp.LiteralNumber(1)).Times(exp.LiteralNumber(2)).Plus(exp.LiteralNumber(1)))
	} else {
		left = exp.New(exp.Substring,
			"this", base.Copy(),
			"start", exp.LiteralNumber(1),
			"length", start.Copy().Minus(exp.LiteralNumber(1)))
		right = exp.New(exp.Substring, "this", base.Copy(), "start", start.Plus(length))
	}
	result := exp.New(exp.DPipe, "this", exp.New(exp.DPipe, "this", left, "expression", insertion), "expression", right)
	if binary {
		result = exp.New(exp.Unhex, "this", result)
	}
	return g.SQL(result)
}

func randSQL(g *gen, e *exp.Expr) string {
	if e.This() != nil {
		g.Unsupported("RANDOM with seed is not supported in DuckDB")
	}
	lower, upper := e.Get("lower"), e.Get("upper")
	if lower != nil && upper != nil {
		// scale DuckDB's [0,1) to the specified range
		rangeSize := exp.NewParen(upper.Minus(lower))
		scaled := exp.New(exp.Add, "this", lower, "expression", exp.NewFunc("random").Times(rangeSize))
		// For now we assume that if bounds are set, return type is BIGINT. Snowflake/Teradata
		return g.SQL(exp.NewCast(scaled, exp.DTypeBigint))
	}
	// Default DuckDB behavior - just return RANDOM() as float
	return "RANDOM()"
}

func bytelengthSQL(g *gen, e *exp.Expr) string {
	arg := e.This()
	// Check if it's a text type (handles both literals and annotated expressions)
	if arg.IsTypeIn(exp.DataTypeTextTypes) {
		return g.Func("OCTET_LENGTH", exp.New(exp.Encode, "this", arg))
	}
	// Default: pass through as-is (conservative for DuckDB, handles binary and unannotated)
	return g.Func("OCTET_LENGTH", arg)
}

func base64encodeSQL(g *gen, e *exp.Expr) string {
	// DuckDB TO_BASE64 requires BLOB input
	// Snowflake BASE64_ENCODE accepts both VARCHAR and BINARY - for VARCHAR it implicitly
	// encodes UTF-8 bytes. We add ENCODE unless the input is a binary type.
	result := e.This()
	// Check if input is a string type - ENCODE only accepts VARCHAR
	if result.IsTypeIn(exp.DataTypeTextTypes) {
		result = exp.New(exp.Encode, "this", result)
	}
	result = exp.New(exp.ToBase64, "this", result)
	maxLineLength := e.Get("max_line_length")
	alphabet := e.Get("alphabet")
	// Handle custom alphabet by replacing standard chars with custom ones
	result = applyBase64AlphabetReplacements(result, alphabet, false)
	// Handle max_line_length by inserting newlines every N characters
	lineLength := 0
	if maxLineLength.IsKind(exp.Literal) && maxLineLength.IsNumber() {
		lineLength = toInt(maxLineLength.MustToPy())
	}
	if lineLength > 0 {
		newline := exp.New(exp.Chr, "expressions", []*exp.Expr{exp.LiteralNumber(10)})
		result = exp.New(exp.Trim,
			"this", exp.New(exp.RegexpReplace,
				"this", result,
				"expression", exp.LiteralString(fmt.Sprintf("(.{%d})", lineLength)),
				"replacement", exp.New(exp.Concat, "expressions", []*exp.Expr{exp.LiteralString(`\1`), newline.Copy()})),
			"expression", newline,
			"position", "TRAILING")
	}
	return g.SQL(result)
}

func hexSQL(g *gen, e *exp.Expr) string {
	c := e.Get("case")
	if c == nil {
		return g.Func("HEX", e.This())
	}
	hexExpr := exp.New(exp.Hex, "this", e.This())
	return g.SQL(exp.NewCase(nil).
		When(c.IsOp(exp.NewNull()), exp.NewNull()).
		When(c.Copy().Eq(0), exp.New(exp.Lower, "this", hexExpr.Copy())).
		Else(hexExpr))
}

func replaceSQL(g *gen, e *exp.Expr) string {
	resultSQL := g.Func("REPLACE",
		castToVarchar(e.This()),
		castToVarchar(e.Expression()),
		castToVarchar(e.Get("replacement")))
	return genWithCastToBlob(g, e, resultSQL)
}

func bitwiseOp(g *gen, e *exp.Expr, op string) string {
	prepareBinaryBitwiseArgs(e)
	return genWithCastToBlob(g, e, g.Binary(e, op))
}

func bitwisexorSQL(g *gen, e *exp.Expr) string {
	prepareBinaryBitwiseArgs(e)
	return genWithCastToBlob(g, e, g.Func("XOR", e.This(), e.Expression()))
}

func objectinsertSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	keySQL := ""
	if key := e.Get("key"); key != nil {
		keySQL = key.Name()
	}
	kvSQL := keySQL + " := " + g.SQLArg(e, "value")
	// If the input struct is empty e.g. transpiling OBJECT_INSERT(OBJECT_CONSTRUCT(), key, value) from Snowflake
	// then we can generate STRUCT_PACK which will build it since STRUCT_INSERT({}, key := value) is not valid DuckDB
	if this.IsKind(exp.Struct) && len(this.Expressions()) == 0 {
		return g.Func("STRUCT_PACK", kvSQL)
	}
	return g.Func("STRUCT_INSERT", this, kvSQL)
}

func mapcatSQL(g *gen, e *exp.Expr) string {
	return g.SQL(replace(mapcatTemplate, kv{"map1": e.This(), "map2": e.Expression()}))
}

func mapcontainskeySQL(g *gen, e *exp.Expr) string {
	return g.Func("ARRAY_CONTAINS", exp.NewFunc("MAP_KEYS", e.Get("key")), e.This())
}

func xDotKey() *exp.Expr {
	return exp.New(exp.Dot, "this", exp.ToIdentifier("x"), "expression", exp.ToIdentifier("key"))
}

func mapdeleteSQL(g *gen, e *exp.Expr) string {
	lambdaExpr := exp.New(exp.Lambda,
		"this", exp.New(exp.In, "this", xDotKey(), "expressions", e.Expressions()).Not(),
		"expressions", []*exp.Expr{exp.ToIdentifier("x")})
	result := exp.NewFunc("MAP_FROM_ENTRIES",
		exp.New(exp.ArrayFilter, "this", exp.NewFunc("MAP_ENTRIES", e.This()), "expression", lambdaExpr))
	return g.SQL(result)
}

func mappickSQL(g *gen, e *exp.Expr) string {
	keysToPick := e.Expressions()
	var lambdaExpr *exp.Expr
	if len(keysToPick) == 1 && keysToPick[0].IsType(exp.DTypeArray) {
		lambdaExpr = exp.New(exp.Lambda,
			"this", exp.NewFunc("ARRAY_CONTAINS", keysToPick[0], xDotKey()),
			"expressions", []*exp.Expr{exp.ToIdentifier("x")})
	} else {
		lambdaExpr = exp.New(exp.Lambda,
			"this", exp.New(exp.In, "this", xDotKey(), "expressions", keysToPick),
			"expressions", []*exp.Expr{exp.ToIdentifier("x")})
	}
	result := exp.NewFunc("MAP_FROM_ENTRIES", exp.NewFunc("LIST_FILTER", exp.NewFunc("MAP_ENTRIES", e.This()), lambdaExpr))
	return g.SQL(result)
}

func mapsizeSQL(g *gen, e *exp.Expr) string { return g.Func("CARDINALITY", e.This()) }

var mapinsertSQL = sqlglot.UnsupportedArgs(func(g *gen, e *exp.Expr) string {
	mapArg := e.This()
	key := e.Get("key")
	value := e.Get("value")
	mapType := mapArg.Type()
	if value != nil {
		if mapType != nil && len(mapType.Expressions()) > 1 {
			// Extract the value type from MAP(key_type, value_type)
			valueType := mapType.Expressions()[1]
			// Cast value to match the map's value type to avoid type conflicts
			value = exp.NewCast(value, valueType)
		}
	}
	// Create a single-entry map for the new key-value pair
	newEntryStruct := exp.New(exp.Struct, "expressions", []*exp.Expr{exp.New(exp.PropertyEQ, "this", key, "expression", value)})
	newEntry := exp.New(exp.ToMap, "this", newEntryStruct)
	// Use MAP_CONCAT to merge the original map with the new entry
	return g.SQL(exp.NewFunc("MAP_CONCAT", mapArg, newEntry))
}, "update_flag")

func startswithSQL(g *gen, e *exp.Expr) string {
	return g.Func("STARTS_WITH", castToVarchar(e.This()), castToVarchar(e.Expression()))
}

func spaceSQL(g *gen, e *exp.Expr) string {
	// DuckDB's REPEAT requires BIGINT for the count parameter
	return g.SQL(exp.New(exp.Repeat, "this", exp.LiteralString(" "), "times", exp.NewCast(e.This(), exp.DTypeBigint)))
}

func tablefromrowsSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		// For GENERATOR, unwrap TABLE() - just emit the Generator (becomes RANGE)
		if e.This().IsKind(exp.Generator) {
			// Preserve alias, joins, and other table-level args
			table := exp.New(exp.Table, "this", e.This(), "alias", e.Get("alias"), "joins", e.Arg("joins"))
			return g.SQL(table)
		}
		return sup(g, e)
	}
}

func unnestSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if e.Bool("explode_array") {
			// In BigQuery, UNNESTing a nested array leads to explosion of the top-level array & struct
			// This is transpiled to DDB by transforming "FROM UNNEST(...)" to "FROM (SELECT UNNEST(..., max_depth => 2))"
			e.Append("expressions", exp.New(exp.Kwarg, "this", exp.NewVar("max_depth"), "expression", exp.LiteralNumber(2)))
			// If BQ's UNNEST is aliased, we transform it from a column alias to a table alias in DDB
			alias := e.Get("alias")
			if alias.IsKind(exp.TableAlias) {
				e.Set("alias", nil)
				if columns := alias.List("columns"); len(columns) > 0 {
					alias = exp.New(exp.TableAlias, "this", columns[0])
				}
			}
			unnestSQL := sup(g, e)
			sel := exp.New(exp.Select, "expressions", []any{unnestSQL}).Subquery(alias, true)
			return g.SQL(sel)
		}
		return sup(g, e)
	}
}

func arrayaggSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if e.This().IsKind(exp.Limit) {
			g.Unsupported("LIMIT inside ARRAY_AGG is not supported in DuckDB")
		}
		return sup(g, e)
	}
}

func ignorenullsSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		this := e.This()
		if isIgnoreRespectNullsWindowFunction(this) {
			// DuckDB should render IGNORE NULLS only for the general-purpose
			// window functions that accept it e.g. FIRST_VALUE(... IGNORE NULLS) OVER (...)
			return sup(g, e)
		}
		// For ARRAY_AGG(expr IGNORE NULLS ...), convert IGNORE NULLS to a
		// FILTER(WHERE expr IS NOT NULL) clause by setting nulls_excluded on the ArrayAgg.
		if this.IsKind(exp.ArrayAgg) {
			this.Set("nulls_excluded", true)
			return g.SQL(this)
		}
		if this.IsKind(exp.First) {
			this = exp.New(exp.AnyValue, "this", this.This())
		}
		if !this.IsAny(exp.AnyValue, exp.ApproxQuantiles) {
			g.Unsupported("IGNORE NULLS is not supported for non-window functions.")
		}
		return g.SQL(this)
	}
}

func splitSQL(g *gen, e *exp.Expr) string {
	baseFunc := exp.NewFunc("STR_SPLIT", e.This(), e.Expression())
	caseExpr := exp.NewCase(nil).Else(baseFunc)
	needsCase := false
	if e.Bool("null_returns_null") {
		caseExpr = caseExpr.When(e.Expression().IsOp(exp.NewNull()), exp.NewNull())
		needsCase = true
	}
	if e.Bool("empty_delimiter_returns_whole") {
		// When delimiter is empty string, return input string as single array element
		caseExpr = caseExpr.When(e.Expression().Eq(exp.LiteralString("")), exp.NewArray(e.This()))
		needsCase = true
	}
	if needsCase {
		return g.SQL(caseExpr)
	}
	return g.SQL(baseFunc)
}

func splitpartSQL(g *gen, e *exp.Expr) string {
	stringArg := e.This()
	delimiterArg := e.Get("delimiter")
	partIndexArg := e.Get("part_index")
	if delimiterArg != nil && partIndexArg != nil {
		// Handle Snowflake's "index 0 and 1 both return first element" behavior
		if e.Bool("part_index_zero_as_one") {
			partIndexArg = exp.New(exp.Paren, "this", exp.NewCase(nil).
				When(partIndexArg.Eq(exp.LiteralNumber("0")), exp.LiteralNumber("1")).
				Else(partIndexArg))
		}
		// Use Anonymous to avoid recursion
		baseFuncExpr := exp.New(exp.Anonymous, "this", "SPLIT_PART", "expressions", []*exp.Expr{stringArg, delimiterArg, partIndexArg})
		needsCaseTransform := false
		caseExpr := exp.NewCase(nil).Else(baseFuncExpr)
		if e.Bool("empty_delimiter_returns_whole") {
			// When delimiter is empty string:
			// - Return whole string if part_index is 1 or -1
			// - Return empty string otherwise
			emptyCase := exp.New(exp.Paren, "this", exp.NewCase(nil).
				When(exp.NewOr(partIndexArg.Eq(exp.LiteralNumber("1")), partIndexArg.Eq(exp.LiteralNumber("-1"))), stringArg).
				Else(exp.LiteralString("")))
			caseExpr = caseExpr.When(delimiterArg.Eq(exp.LiteralString("")), emptyCase)
			needsCaseTransform = true
		}
		if needsCaseTransform {
			return g.SQL(caseExpr)
		}
		return g.SQL(baseFuncExpr)
	}
	return g.FunctionFallbackSQL(e)
}

func respectnullsSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if isIgnoreRespectNullsWindowFunction(e.This()) {
			// DuckDB should render RESPECT NULLS only for the general-purpose
			// window functions that accept it e.g. FIRST_VALUE(... RESPECT NULLS) OVER (...)
			return sup(g, e)
		}
		g.Unsupported("RESPECT NULLS is not supported for non-window functions.")
		return g.SQLArg(e, "this")
	}
}

func arraytostringSQL(g *gen, e *exp.Expr) string {
	null := e.Get("null")
	if e.Bool("null_is_empty") {
		x := exp.ToIdentifier("x")
		listTransform := exp.New(exp.Transform,
			"this", e.This().Copy(),
			"expression", exp.New(exp.Lambda,
				"this", exp.New(exp.Coalesce, "this", exp.NewCast(x, "TEXT"), "expressions", []*exp.Expr{exp.LiteralString("")}),
				"expressions", []*exp.Expr{x}))
		arrayToString := exp.New(exp.ArrayToString, "this", listTransform, "expression", e.Expression())
		if e.Bool("null_delim_is_null") {
			return g.SQL(exp.NewCase(nil).
				When(e.Expression().Copy().IsOp(exp.NewNull()), exp.NewNull()).
				Else(arrayToString))
		}
		return g.SQL(arrayToString)
	}
	if null != nil {
		x := exp.ToIdentifier("x")
		return g.SQL(exp.New(exp.ArrayToString,
			"this", exp.New(exp.Transform,
				"this", e.This(),
				"expression", exp.New(exp.Lambda,
					"this", exp.New(exp.Coalesce, "this", exp.NewCast(x, "TEXT"), "expressions", []*exp.Expr{null}),
					"expressions", []*exp.Expr{x})),
			"expression", e.Expression()))
	}
	return g.Func("ARRAY_TO_STRING", e.This(), e.Expression())
}

func concatwsSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		// DuckDB-specific: handle binary types using DPipe (||) operator
		exprs := e.Expressions()
		var separator *exp.Expr
		var args []*exp.Expr
		if len(exprs) > 0 {
			separator, args = exprs[0], exprs[1:]
		}
		anyBinary := isBinary(separator)
		for _, arg := range args {
			anyBinary = anyBinary || isBinary(arg)
		}
		if anyBinary {
			result := args[0]
			for _, arg := range args[1:] {
				result = exp.New(exp.DPipe, "this", exp.New(exp.DPipe, "this", result, "expression", separator), "expression", arg)
			}
			return g.SQL(result)
		}
		isArray := func(arg *exp.Expr) bool { return arg.IsKind(exp.Array) || arg.IsType(exp.DTypeArray) }
		hasArray := false
		for _, arg := range args {
			hasArray = hasArray || isArray(arg)
		}
		if e.Bool("flatten") && hasArray {
			var arrays []*exp.Expr
			for _, arg := range args {
				if isArray(arg) {
					arg = exp.NewCast(arg, "TEXT[]")
				} else {
					if (arg.Type() == nil || arg.IsType(exp.DTypeUnknown)) && !arg.IsAny(exp.Constants...) {
						g.Unsupported("Cannot transpile CONCAT_WS with unknown argument types to DuckDB.")
					}
					arg = exp.NewArray(exp.NewCast(arg, exp.DTypeText))
				}
				arrays = append(arrays, arg)
			}
			array := arrays[0]
			if len(arrays) > 1 {
				array = exp.New(exp.ArrayConcat, "this", arrays[0], "expressions", arrays[1:])
			}
			return g.Func("CONCAT_WS", separator, g.Func("ARRAY_TO_STRING", array, separator))
		}
		return sup(g, e)
	}
}

func regexpExtractSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	group := e.Get("group")
	params := e.Get("parameters")
	position := e.Get("position")
	occurrence := e.Get("occurrence")
	nullIfPosOverflow := e.Bool("null_if_pos_overflow")
	// Handle Snowflake's 'e' flag: it enables capture group extraction
	// In DuckDB, this is controlled by the group parameter directly
	if params != nil && params.IsString() && strings.Contains(params.Name(), "e") {
		params = exp.LiteralString(strings.ReplaceAll(params.Name(), "e", ""))
	}
	validatedFlags := validateRegexpFlags(g, params, "cims")
	// Strip default group when no following params (DuckDB default is same as group=0)
	if validatedFlags == "" && group != nil && group.Name() == fmt.Sprint(g.D.RegexpExtractDefaultGroup) {
		group = nil
	}
	flagsExpr := optLiteralString(validatedFlags)
	// use substring to handle position argument
	if position != nil && (!position.IsInt() || toInt(position.MustToPy()) > 1) {
		this = exp.New(exp.Substring, "this", this, "start", position)
		if nullIfPosOverflow {
			this = exp.New(exp.Nullif, "this", this, "expression", exp.LiteralString(""))
		}
	}
	isExtractAll := e.IsKind(exp.RegexpExtractAll)
	nonSingleOccurrence := occurrence != nil && (!occurrence.IsInt() || toInt(occurrence.MustToPy()) > 1)
	name := "REGEXP_EXTRACT"
	if isExtractAll || nonSingleOccurrence {
		name = "REGEXP_EXTRACT_ALL"
	}
	var exprs []*exp.Expr
	for _, x := range []*exp.Expr{this, e.Expression(), group, flagsExpr} {
		if x != nil {
			exprs = append(exprs, x)
		}
	}
	result := exp.New(exp.Anonymous, "this", name, "expressions", exprs)
	if isExtractAll && nonSingleOccurrence {
		// Array slicing for REGEXP_EXTRACT_ALL with occurrence
		result = exp.New(exp.Bracket, "this", result, "expressions", []*exp.Expr{exp.New(exp.Slice, "this", occurrence)})
	} else if nonSingleOccurrence {
		// ARRAY_EXTRACT for REGEXP_EXTRACT with occurrence > 1
		result = exp.New(exp.Anonymous, "this", "ARRAY_EXTRACT", "expressions", []*exp.Expr{result, occurrence})
	}
	return g.SQL(result)
}

func regexpinstrSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	pattern := e.Expression()
	position := e.Get("position")
	origOcc := e.Get("occurrence")
	occurrence := origOcc
	if occurrence == nil {
		occurrence = exp.LiteralNumber(1)
	}
	option := e.Get("option")
	parameters := e.Get("parameters")
	validatedFlags := validateRegexpFlags(g, parameters, "ims")
	// Checked before the flags are embedded, which would make the pattern non-empty
	isEmpty := pattern.Copy().Eq(exp.LiteralString(""))
	if validatedFlags != "" {
		pattern = exp.New(exp.Concat, "expressions", []*exp.Expr{exp.LiteralString("(?" + validatedFlags + ")"), pattern})
	}
	// Handle starting position offset
	posOffset := exp.LiteralNumber(0)
	if position != nil && (!position.IsInt() || toInt(position.MustToPy()) > 1) {
		this = exp.New(exp.Substring, "this", this, "start", position)
		posOffset = position.Minus(exp.LiteralNumber(1))
	}
	// Helper: LIST_SUM(LIST_TRANSFORM(list[1:end], x -> LENGTH(x)))
	sumLengths := func(funcName string, end *exp.Expr) *exp.Expr {
		lst := exp.New(exp.Bracket,
			"this", exp.New(exp.Anonymous, "this", funcName, "expressions", []*exp.Expr{this, pattern}),
			"expressions", []*exp.Expr{exp.New(exp.Slice, "this", exp.LiteralNumber(1), "expression", end)},
			"offset", 1)
		transform := exp.New(exp.Anonymous, "this", "LIST_TRANSFORM", "expressions", []*exp.Expr{
			lst,
			exp.New(exp.Lambda, "this", exp.New(exp.Length, "this", exp.ToIdentifier("x")), "expressions", []*exp.Expr{exp.ToIdentifier("x")}),
		})
		return exp.New(exp.Coalesce,
			"this", exp.New(exp.Anonymous, "this", "LIST_SUM", "expressions", []*exp.Expr{transform}),
			"expressions", []*exp.Expr{exp.LiteralNumber(0)})
	}
	// Position = 1 + sum(split_lengths[1:occ]) + sum(match_lengths[1:occ-1]) + offset
	basePos := exp.LiteralNumber(1).
		Plus(sumLengths("STRING_SPLIT_REGEX", occurrence)).
		Plus(sumLengths("REGEXP_EXTRACT_ALL", occurrence.Minus(exp.LiteralNumber(1)))).
		Plus(posOffset)
	// option=1: add match length for end position
	if option != nil && option.IsInt() && toInt(option.MustToPy()) == 1 {
		matchAtOcc := exp.New(exp.Bracket,
			"this", exp.New(exp.Anonymous, "this", "REGEXP_EXTRACT_ALL", "expressions", []*exp.Expr{this, pattern}),
			"expressions", []*exp.Expr{occurrence},
			"offset", 1)
		basePos = basePos.Plus(exp.New(exp.Coalesce, "this", exp.New(exp.Length, "this", matchAtOcc), "expressions", []*exp.Expr{exp.LiteralNumber(0)}))
	}
	// NULL checks for all provided arguments
	var nullChecks []any
	for _, arg := range []*exp.Expr{e.This(), e.Expression(), position, origOcc, option, parameters} {
		if arg != nil {
			nullChecks = append(nullChecks, arg.Copy().IsOp(exp.New(exp.Null)))
		}
	}
	matches := exp.New(exp.Anonymous, "this", "REGEXP_EXTRACT_ALL", "expressions", []*exp.Expr{this, pattern})
	return g.SQL(exp.NewCase(nil).
		When(exp.NewOr(nullChecks...), exp.New(exp.Null)).
		When(isEmpty, exp.LiteralNumber(0)).
		When(exp.New(exp.Length, "this", matches).Lt(occurrence), exp.LiteralNumber(0)).
		Else(basePos))
}

var numbertostrSQL = sqlglot.UnsupportedArgs(func(g *gen, e *exp.Expr) string {
	if f := e.Get("format"); f != nil && f.IsInt() {
		return g.Func("FORMAT", "'{:,."+f.Name()+"f}'", e.This())
	}
	g.Unsupported("Only integer formats are supported by NumberToStr")
	return g.FunctionFallbackSQL(e)
}, "culture")

func autoincrementcolumnconstraintSQL(g *gen, e *exp.Expr) string {
	g.Unsupported("The AUTOINCREMENT column constraint is not supported by DuckDB")
	return ""
}

func aliasesSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if this := e.This(); this.IsKind(exp.Posexplode) {
			return posexplodeSQL(g, this)
		}
		return sup(g, e)
	}
}

func posexplodeSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	parent := e.Parent()
	// The default Spark aliases are "pos" and "col", unless specified otherwise
	pos, col := exp.ToIdentifier("pos"), exp.ToIdentifier("col")
	if parent.IsKind(exp.Aliases) {
		// Column case: SELECT POSEXPLODE(col) [AS (a, b)]
		exprs := parent.Expressions()
		if len(exprs) != 2 {
			g.Raisef("ValueError: not enough values to unpack (expected 2, got %d)", len(exprs))
		}
		pos, col = exprs[0], exprs[1]
	} else if parent.IsKind(exp.Table) {
		// Table case: SELECT * FROM POSEXPLODE(col) [AS (a, b)]
		if alias := parent.Get("alias"); alias != nil {
			if columns := alias.List("columns"); len(columns) > 0 {
				if len(columns) != 2 {
					g.Raisef("ValueError: not enough values to unpack (expected 2, got %d)", len(columns))
				}
				pos, col = columns[0], columns[1]
			}
			alias.Pop()
		}
	}
	// Translate POSEXPLODE to UNNEST + GENERATE_SUBSCRIPTS
	// Note: In Spark pos is 0-indexed, but in DuckDB it's 1-indexed, so we subtract 1 from GENERATE_SUBSCRIPTS
	unnestSQL := g.SQL(exp.New(exp.Unnest, "expressions", []*exp.Expr{this}, "alias", col))
	genSubscripts := g.SQL(exp.New(exp.Alias,
		"this", exp.New(exp.Anonymous, "this", "GENERATE_SUBSCRIPTS", "expressions", []*exp.Expr{this, exp.LiteralNumber(1)}).Minus(exp.LiteralNumber(1)),
		"alias", pos))
	posexplodeSQL := g.FormatArgs(genSubscripts, unnestSQL)
	if parent.IsKind(exp.From) || (parent != nil && parent.Parent().IsKind(exp.From)) {
		// SELECT * FROM POSEXPLODE(col) -> SELECT * FROM (SELECT GENERATE_SUBSCRIPTS(...), UNNEST(...))
		return g.SQL(exp.New(exp.Subquery, "this", exp.New(exp.Select, "expressions", []any{posexplodeSQL})))
	}
	return posexplodeSQL
}

// addmonthsSQL handles float months, end-of-month preservation and type preservation.
func addmonthsSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	if this.Type() == nil {
		this = sqlglot.AnnotateTypes(this, g.D)
	}
	if this.IsTypeIn(exp.DataTypeTextTypes) {
		this = exp.New(exp.Cast, "this", this, "to", exp.New(exp.DataType, "this", exp.DTypeTimestamp))
	}
	// Detect float/decimal months to apply rounding (Snowflake behavior)
	// DuckDB INTERVAL syntax doesn't support non-integer expressions, so use TO_MONTHS
	monthsExpr := e.Expression()
	if monthsExpr.Type() == nil {
		monthsExpr = sqlglot.AnnotateTypes(monthsExpr, g.D)
	}
	var intervalOrToMonths *exp.Expr
	if monthsExpr.IsType(exp.DTypeFloat, exp.DTypeDouble, exp.DTypeDecimal) {
		// Float/decimal case: Round and use TO_MONTHS(CAST(ROUND(value) AS INT))
		intervalOrToMonths = exp.NewFunc("TO_MONTHS", exp.NewCast(exp.NewFunc("ROUND", monthsExpr), "INT"))
	} else {
		// Integer case: standard INTERVAL N MONTH syntax
		intervalOrToMonths = exp.New(exp.Interval, "this", monthsExpr, "unit", exp.NewVar("MONTH"))
	}
	dateAddExpr := exp.New(exp.Add, "this", this, "expression", intervalOrToMonths)
	// Apply end-of-month preservation if Snowflake flag is set
	// CASE WHEN LAST_DAY(date) = date THEN LAST_DAY(result) ELSE result END
	resultExpr := dateAddExpr
	if e.Bool("preserve_end_of_month") {
		resultExpr = exp.NewCase(nil).
			When(exp.New(exp.EQ, "this", exp.NewFunc("LAST_DAY", this), "expression", this), exp.NewFunc("LAST_DAY", dateAddExpr)).
			Else(dateAddExpr)
	}
	// DuckDB's DATE_ADD function returns TIMESTAMP/DATETIME by default, even when the input is DATE
	// We need to cast the result back to the original type when the input is DATE or TIMESTAMPTZ
	if this.IsType(exp.DTypeDate, exp.DTypeTimestamptz) {
		return g.SQL(exp.New(exp.Cast, "this", resultExpr, "to", this.Type()))
	}
	return g.SQL(resultExpr)
}

func formatSQL(g *gen, e *exp.Expr) string {
	if strings.ToLower(e.Name()) == "%s" && len(e.Expressions()) == 1 {
		return g.Func("FORMAT", "'{}'", e.Expressions()[0])
	}
	return g.FunctionFallbackSQL(e)
}

func datetruncSQL(g *gen, e *exp.Expr) string {
	date := e.This()
	weekStart := weekTruncStartDow(e.Get("unit"))
	unit := sqlglot.UnitToStr(e, "DAY")
	var result string
	if weekStart != 0 {
		result = g.SQL(buildWeekTruncExpression(date, weekStart, true, true))
	} else {
		result = g.Func("DATE_TRUNC", unit, date)
	}
	if e.Bool("input_type_preserved") && date.IsTypeIn(exp.DataTypeTemporalTypes) {
		return g.SQL(exp.New(exp.Cast, "this", result, "to", date.Type()))
	}
	return result
}

func datetimetruncSQL(g *gen, e *exp.Expr) string {
	this := exp.NewCast(e.This(), exp.DTypeDatetime)
	if weekStart := weekTruncStartDow(e.Get("unit")); weekStart != 0 {
		return g.SQL(buildWeekTruncExpression(this, weekStart, true, false))
	}
	return g.Func("DATE_TRUNC", sqlglot.UnitToStr(e, "DAY"), this)
}

func timestamptruncSQL(g *gen, e *exp.Expr) string {
	zone := e.Get("zone")
	timestamp := e.This()
	weekStart := weekTruncStartDow(e.Get("unit"))
	// The week start emulation below is exact, so avoid weekstart_unit_to_str's degrade warning
	var unit *exp.Expr
	if weekStart != 0 {
		unit = sqlglot.UnitToStr(e, "DAY")
	} else {
		unit = sqlglot.WeekstartUnitToStr(g, e, "DAY")
	}
	dateUnit := sqlglot.IsDateUnit(unit) || weekStart != 0
	truncExpr := func(this *exp.Expr) *exp.Expr {
		if weekStart != 0 {
			return buildWeekTruncExpression(this, weekStart, true, false)
		}
		return exp.NewFunc("DATE_TRUNC", unit, this)
	}
	if dateUnit && zone != nil {
		// BigQuery's TIMESTAMP_TRUNC with timezone truncates in the target timezone and returns as UTC.
		// Double AT TIME ZONE needed for BigQuery compatibility:
		// 1. First AT TIME ZONE: ensures truncation happens in the target timezone
		// 2. Second AT TIME ZONE: converts the DATE result back to TIMESTAMPTZ (preserving time component)
		timestamp = exp.New(exp.AtTimeZone, "this", timestamp, "zone", zone)
		trunced := truncExpr(timestamp)
		if trunced.IsKind(exp.DateAdd) {
			// Parenthesize so the trailing AT TIME ZONE binds to the whole shifted expression
			trunced = exp.New(exp.Paren, "this", trunced)
		}
		return g.SQL(exp.New(exp.AtTimeZone, "this", trunced, "zone", zone))
	}
	result := g.SQL(truncExpr(timestamp))
	if e.Bool("input_type_preserved") {
		if timestamp.Type() != nil && timestamp.IsType(exp.DTypeTime, exp.DTypeTimetz) {
			dummyDate := exp.New(exp.Cast, "this", exp.LiteralString("1970-01-01"), "to", exp.New(exp.DataType, "this", exp.DTypeDate))
			dateTime := exp.New(exp.Add, "this", dummyDate, "expression", timestamp)
			result = g.Func("DATE_TRUNC", unit, dateTime)
			return g.SQL(exp.New(exp.Cast, "this", result, "to", timestamp.Type()))
		}
		if timestamp.IsTypeIn(exp.DataTypeTemporalTypes) {
			return g.SQL(exp.New(exp.Cast, "this", result, "to", timestamp.Type()))
		}
	}
	return result
}

func trimSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		e.This().Replace(castToVarchar(e.This()))
		if expr := e.Expression(); expr != nil {
			expr.Replace(castToVarchar(expr))
		}
		return genWithCastToBlob(g, e, sup(g, e))
	}
}

func roundSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	decimals := e.Get("decimals")
	truncate := e.Get("truncate")
	// DuckDB requires the scale (decimals) argument to be an INT
	// Some dialects (e.g., Snowflake) allow non-integer scales and cast to an integer internally
	if decimals != nil && e.Bool("casts_non_integer_decimals") {
		if !(decimals.IsInt() || decimals.IsTypeIn(exp.DataTypeIntegerTypes)) {
			decimals = exp.NewCast(decimals, exp.DTypeInt)
		}
	}
	fn := "ROUND"
	if truncate != nil {
		switch truncate.Arg("this") {
		// BigQuery uses ROUND_HALF_EVEN; Snowflake uses HALF_TO_EVEN
		case "ROUND_HALF_EVEN", "HALF_TO_EVEN":
			fn = "ROUND_EVEN"
			truncate = nil
		// BigQuery uses ROUND_HALF_AWAY_FROM_ZERO; Snowflake uses HALF_AWAY_FROM_ZERO
		case "ROUND_HALF_AWAY_FROM_ZERO", "HALF_AWAY_FROM_ZERO":
			truncate = nil
		}
	}
	return g.Func(fn, this, decimals, truncate)
}

func trycastSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		to := e.To()
		toType := to.Arg("this")
		src := e.This()
		dtype, isDType := toType.(exp.DType)
		if e.Bool("null_on_text_overflow") && isDType && exp.DataTypeTextTypes.Has(dtype) && len(to.Expressions()) > 0 {
			return g.SQL(exp.NewCase(nil).
				When(exp.New(exp.LTE, "this", exp.NewFunc("LENGTH", src), "expression", to.Expressions()[0].This()), exp.NewCast(src, "TEXT")).
				Else(exp.New(exp.Null)))
		} else if isDType && dtype == exp.DTypeDate && e.Bool("probe_date_format") {
			slashStrptime := exp.NewCast(exp.NewFunc("TRY_STRPTIME", src, exp.LiteralString(trycastDateSlashFmt)), "DATE")
			monStrptime := exp.NewCast(exp.NewFunc("TRY_STRPTIME", src, exp.LiteralString(trycastDateMonFmt)), "DATE")
			return g.SQL(exp.NewCase(nil).
				When(exp.NewFunc("CONTAINS", src, exp.LiteralString("/")), slashStrptime).
				When(exp.New(exp.RegexpLike, "this", src, "expression", exp.LiteralString("[A-Za-z]")), monStrptime).
				Else(exp.New(exp.TryCast, "this", src, "to", to)))
		} else if iv, ok := toType.(*exp.Expr); ok && iv.IsKind(exp.Interval) && iv.Unit() != nil && e.Bool("requires_string") {
			unit := iv.Unit()
			intervalType := exp.MustDataTypeBuild("INTERVAL", nil, false)
			if unit.IsKind(exp.IntervalSpan) {
				g.Unsupported("TRY_CAST to INTERVAL with span (e.g. HOUR TO MINUTE) is not supported in DuckDB")
				return g.SQL(exp.New(exp.TryCast, "this", src, "to", intervalType))
			}
			return g.SQL(exp.New(exp.TryCast,
				"this", exp.New(exp.DPipe, "this", src, "expression", exp.LiteralString(" "+unit.Name())),
				"to", intervalType))
		}
		return sup(g, e)
	}
}

const regexMetaEscapePattern = `([\[\]^.\-*+?(){}|$\\])`

func strtokSQL(g *gen, e *exp.Expr) string {
	stringArg := e.This()
	delimiterArg := e.Get("delimiter")
	partIndexArg := e.Get("part_index")
	if delimiterArg != nil && partIndexArg != nil {
		// Escape regex chars and build character class at runtime using REGEXP_REPLACE
		escapedDelimiter := exp.New(exp.Anonymous, "this", "REGEXP_REPLACE", "expressions", []*exp.Expr{
			delimiterArg,
			exp.LiteralString(regexMetaEscapePattern), // Escape problematic regex chars
			exp.LiteralString(`\\\1`),                 // Replace with escaped version using $1 backreference
			exp.LiteralString("g"),                    // Global flag
		})
		// CASE WHEN delimiter = '' THEN '' ELSE CONCAT('[', escaped_delimiter, ']') END
		regexPattern := exp.NewCase(nil).
			When(delimiterArg.Eq(exp.LiteralString("")), exp.LiteralString("")).
			Else(exp.NewFunc("CONCAT", exp.LiteralString("["), escapedDelimiter, exp.LiteralString("]")))
		// STRTOK skips empty strings, so we need to filter them out
		// LIST_FILTER(REGEXP_SPLIT_TO_ARRAY(string, pattern), x -> x != '')[index]
		splitArray := exp.NewFunc("REGEXP_SPLIT_TO_ARRAY", stringArg, regexPattern)
		x := exp.ToIdentifier("x")
		isEmpty := x.Eq(exp.LiteralString(""))
		filteredArray := exp.NewFunc("LIST_FILTER", splitArray,
			exp.New(exp.Lambda, "this", exp.NewNot(isEmpty.Copy()), "expressions", []*exp.Expr{x.Copy()}))
		baseFunc := exp.New(exp.Bracket, "this", filteredArray, "expressions", []*exp.Expr{partIndexArg}, "offset", 1)
		// Use template with the built regex pattern
		return g.SQL(replace(strtokTemplate, kv{
			"string":     stringArg,
			"delimiter":  delimiterArg,
			"part_index": partIndexArg,
			"base_func":  baseFunc,
		}))
	}
	return g.FunctionFallbackSQL(e)
}

func strtoktoarraySQL(g *gen, e *exp.Expr) string {
	stringArg := e.This()
	delimiterArg := e.Get("expression")
	if delimiterArg == nil {
		delimiterArg = exp.LiteralString(" ")
	}
	escaped := exp.New(exp.RegexpReplace,
		"this", delimiterArg.Copy(),
		"expression", exp.LiteralString(regexMetaEscapePattern),
		"replacement", exp.LiteralString(`\\\1`),
		"modifiers", exp.LiteralString("g"))
	return g.SQL(replace(strtokToArrayTemplate, kv{"string": stringArg, "delimiter": delimiterArg, "escaped": escaped}))
}

func approxquantileSQL(g *gen, e *exp.Expr) string {
	result := g.Func("APPROX_QUANTILE", e.This(), e.Get("quantile"))
	// DuckDB returns integers for APPROX_QUANTILE, cast to DOUBLE if the expected type is a real type
	if e.IsTypeIn(exp.DataTypeRealTypes) {
		result = "CAST(" + result + " AS DOUBLE)"
	}
	return result
}

// approxquantilesSQL maps BigQuery's APPROX_QUANTILES(expr, n) to an APPROX_QUANTILE array.
func approxquantilesSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	var numQuantilesExpr *exp.Expr
	if this.IsKind(exp.Distinct) {
		// APPROX_QUANTILES requires 2 args and DISTINCT node grabs both
		if len(this.Expressions()) < 2 {
			g.Unsupported("APPROX_QUANTILES requires a bucket count argument")
			return g.FunctionFallbackSQL(e)
		}
		numQuantilesExpr = this.Expressions()[1].Pop()
	} else {
		numQuantilesExpr = e.Expression()
	}
	if !numQuantilesExpr.IsKind(exp.Literal) || !numQuantilesExpr.IsInt() {
		g.Unsupported("APPROX_QUANTILES bucket count must be a positive integer")
		return g.FunctionFallbackSQL(e)
	}
	numQuantiles := toInt(numQuantilesExpr.MustToPy())
	if numQuantiles <= 0 {
		g.Unsupported("APPROX_QUANTILES bucket count must be a positive integer")
		return g.FunctionFallbackSQL(e)
	}
	var quantiles []*exp.Expr
	n := pydec.FromInt(big.NewInt(int64(numQuantiles)))
	for i := 0; i <= numQuantiles; i++ {
		quantiles = append(quantiles, exp.LiteralNumber(pydec.FromInt(big.NewInt(int64(i))).Quo(n).String()))
	}
	return g.SQL(exp.New(exp.ApproxQuantile, "this", this, "quantile", exp.New(exp.Array, "expressions", quantiles)))
}

func jsonextractscalarSQL(g *gen, e *exp.Expr) string {
	if e.Bool("scalar_only") {
		jsonValue := exp.New(exp.JSONExtractScalar, "this", renameFunc("JSON_VALUE")(g, e), "expression", "'$'")
		// `->>` binds looser than most operators, so the wrap logic needs the parent
		jsonValue.SetParent(e.Parent())
		e = jsonValue
	}
	return arrowJSONExtractSQL(g, e)
}

func bitwisenotSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	if isBinary(this) {
		e.SetType(exp.DTypeBinary.IntoExpr())
	}
	arg := castToBit(this)
	if this.IsKind(exp.Neg) {
		arg = exp.New(exp.Paren, "this", arg)
	}
	e.Set("this", arg)
	return genWithCastToBlob(g, e, "~"+g.SQLArg(e, "this"))
}

func windowSQL(sup, supFilter sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		this := e.This()
		if this.IsKind(exp.Corr) || (this.IsKind(exp.Filter) && this.This().IsKind(exp.Corr)) {
			return corrSQLWith(g, e, sup, supFilter)
		}
		return sup(g, e)
	}
}

func filterSQL(supWindow, sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if e.This().IsKind(exp.Corr) {
			return corrSQLWith(g, e, supWindow, sup)
		}
		return sup(g, e)
	}
}

func corrSQLWith(g *gen, e *exp.Expr, supWindow, supFilter sqlglot.GenFunc) string {
	if e.IsKind(exp.Corr) && !e.Bool("null_on_zero_variance") {
		return g.Func("CORR", e.This(), e.Expression())
	}
	corrExpr := maybeCorrNullToFalse(e)
	if corrExpr == nil {
		if e.IsKind(exp.Window) {
			return supWindow(g, e)
		}
		if e.IsKind(exp.Filter) {
			return supFilter(g, e)
		}
		corrExpr = e
	}
	return g.SQL(exp.NewCase(nil).When(exp.New(exp.IsNan, "this", corrExpr), exp.NewNull()).Else(corrExpr))
}

func uuidSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		namespace := e.This()
		name := e.Get("name")
		// UUID v5 (namespace + name) - Emulate using SHA1
		if namespace != nil && name != nil {
			return g.SQL(replace(uuidV5Template, kv{"namespace": namespace, "name": name}))
		}
		return sup(g, e)
	}
}
