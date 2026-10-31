package snowflake

// Port of sqlglot/parsers/snowflake.py.

import (
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

type fnArgs = []*exp.Expr

func seqGet(xs []*exp.Expr, i int) *exp.Expr {
	if i < 0 {
		i += len(xs)
	}
	if i < 0 || i >= len(xs) {
		return nil
	}
	return xs[i]
}

// orExpr is Python's `a or b` for optional expressions.
func orExpr(vals ...*exp.Expr) *exp.Expr {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// buildApproxTopK normalizes APPROX_TOP_K arguments to match Snowflake semantics: k defaults
// to 1 if omitted.
func buildApproxTopK(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	if len(args) == 1 {
		args = append(args, exp.LiteralNumber(1))
	}
	return exp.FromArgList(exp.ApproxTopK, args)
}

func buildToNumber(args []*exp.Expr, safe ...bool) *exp.Expr {
	secondArg := seqGet(args, 1)
	var format, precision, scale *exp.Expr
	if secondArg != nil && secondArg.IsNumber() {
		precision = secondArg
		scale = orExpr(seqGet(args, 2), exp.LiteralNumber(0))
	} else {
		format = secondArg
		precision = orExpr(seqGet(args, 2), exp.LiteralNumber(38))
		scale = orExpr(seqGet(args, 3), exp.LiteralNumber(0))
	}
	return exp.New(exp.ToNumber,
		"this", seqGet(args, 0),
		"format", format,
		"precision", precision,
		"scale", scale,
		"safe", len(safe) > 0 && safe[0])
}

func buildDateFromParts(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	return exp.New(exp.DateFromParts,
		"year", seqGet(args, 0),
		"month", seqGet(args, 1),
		"day", seqGet(args, 2),
		"allow_overflow", true)
}

// TimestampTypes are the timestamp types handled by _build_datetime.
var TimestampTypes = map[exp.DType]string{
	exp.DTypeTimestamp:    "TO_TIMESTAMP",
	exp.DTypeTimestampltz: "TO_TIMESTAMP_LTZ",
	exp.DTypeTimestampntz: "TO_TIMESTAMP_NTZ",
	exp.DTypeTimestamptz:  "TO_TIMESTAMP_TZ",
}

func buildDatetime(name string, kind exp.DType, safeArg ...bool) sqlglot.FuncBuilder {
	safe := len(safeArg) > 0 && safeArg[0]
	return func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
		value := seqGet(args, 0)
		scaleOrFmt := seqGet(args, 1)
		intValue := value != nil && helper.IsInt(value.Name())
		intScaleOrFmt := scaleOrFmt != nil && scaleOrFmt.IsInt()

		if value.IsAny(exp.Literal, exp.Neg) || (value != nil && scaleOrFmt != nil) {
			// Converts calls like `TO_TIME('01:02:03')` into casts
			if len(args) == 1 && value.IsString() && !intValue {
				var cast *exp.Expr
				if safe {
					cast = exp.New(exp.TryCast, "this", value, "to", kind.IntoExpr(), "requires_string", true)
				} else {
					cast = exp.NewCast(value, kind)
				}
				if safe && kind == exp.DTypeDate {
					cast.Set("probe_date_format", true)
				}
				return cast
			}
			// Handles `TO_TIMESTAMP(str, fmt)` and `TO_TIMESTAMP(num, scale)` as special
			// cases so we can transpile them, since they're relatively common
			if _, ok := TimestampTypes[kind]; ok {
				if !safe && (intScaleOrFmt || (intValue && scaleOrFmt == nil)) {
					// TRY_TO_TIMESTAMP('integer') is not parsed into exp.UnixToTime as it's not
					// easily transpilable. Numeric-looking strings with format strings use
					// StrToTime, not UnixToTime.
					unixExpr := exp.New(exp.UnixToTime, "this", value, "scale", scaleOrFmt)
					unixExpr.Set("target_type", kind.IntoExpr())
					return unixExpr
				}
				if scaleOrFmt != nil && !intScaleOrFmt {
					// Format string provided (e.g., 'YYYY-MM-DD'), use StrToTime
					strtotimeExpr := sqlglot.BuildFormattedTime(exp.StrToTime, "", nil)(args, d)
					strtotimeExpr.Set("safe", safe)
					strtotimeExpr.Set("target_type", kind.IntoExpr())
					return strtotimeExpr
				}
			}
		}
		// Handle DATE/TIME with format strings - allow int_value if a format string is provided
		hasFormatString := scaleOrFmt != nil && !intScaleOrFmt
		if (kind == exp.DTypeDate || kind == exp.DTypeTime) && (!intValue || hasFormatString) {
			klass := exp.TsOrDsToTime
			if kind == exp.DTypeDate {
				klass = exp.TsOrDsToDate
			}
			formattedExp := sqlglot.BuildFormattedTime(klass, "", nil)(args, d)
			formattedExp.Set("safe", safe)
			return formattedExp
		}
		return exp.New(exp.Anonymous, "this", name, "expressions", args)
	}
}

func buildBitwise(kind exp.Kind, name string) sqlglot.FuncBuilder {
	return func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
		if len(args) == 3 {
			// Special handling for bitwise operations with padside argument
			if kind == exp.BitwiseAnd || kind == exp.BitwiseOr || kind == exp.BitwiseXor {
				return exp.New(kind, "this", seqGet(args, 0), "expression", seqGet(args, 1), "padside", seqGet(args, 2))
			}
			return exp.New(exp.Anonymous, "this", name, "expressions", args)
		}
		result := sqlglot.BinaryFromFunction(kind)(args, d)
		// Snowflake specifies INT128 for bitwise shifts
		if kind == exp.BitwiseLeftShift || kind == exp.BitwiseRightShift {
			result.Set("requires_int128", true)
		}
		return result
	}
}

// https://docs.snowflake.com/en/sql-reference/functions/div0
func buildIfFromDiv0(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	lhs := exp.Wrap(seqGet(args, 0), exp.Binary)
	rhs := exp.Wrap(seqGet(args, 1), exp.Binary)
	cond := exp.New(exp.EQ, "this", rhs, "expression", exp.LiteralNumber(0)).And(
		exp.New(exp.Is, "this", lhs, "expression", exp.NewNull()).Not())
	return exp.New(exp.If, "this", cond, "true", exp.LiteralNumber(0), "false", exp.New(exp.Div, "this", lhs, "expression", rhs))
}

// https://docs.snowflake.com/en/sql-reference/functions/div0null
func buildIfFromDiv0null(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	lhs := exp.Wrap(seqGet(args, 0), exp.Binary)
	rhs := exp.Wrap(seqGet(args, 1), exp.Binary)
	// Returns 0 when divisor is 0 OR NULL
	cond := exp.New(exp.EQ, "this", rhs, "expression", exp.LiteralNumber(0)).Or(
		exp.New(exp.Is, "this", rhs, "expression", exp.NewNull()))
	return exp.New(exp.If, "this", cond, "true", exp.LiteralNumber(0), "false", exp.New(exp.Div, "this", lhs, "expression", rhs))
}

// https://docs.snowflake.com/en/sql-reference/functions/zeroifnull
func buildIfFromZeroifnull(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	cond := exp.New(exp.Is, "this", seqGet(args, 0), "expression", exp.New(exp.Null))
	return exp.New(exp.If, "this", cond, "true", exp.LiteralNumber(0), "false", seqGet(args, 0))
}

func buildSearch(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	kv := []any{"this", seqGet(args, 0), "expression", seqGet(args, 1)}
	for _, arg := range args[min(2, len(args)):] {
		if arg.IsKind(exp.Kwarg) {
			kv = append(kv, strings.ToLower(arg.Name()), arg)
		}
	}
	return exp.New(exp.Search, kv...)
}

// https://docs.snowflake.com/en/sql-reference/functions/nullifzero
func buildIfFromNullifzero(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	cond := exp.New(exp.EQ, "this", seqGet(args, 0), "expression", exp.LiteralNumber(0))
	return exp.New(exp.If, "this", cond, "true", exp.New(exp.Null), "false", seqGet(args, 0))
}

func buildRegexpReplace(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	regexpReplace := exp.FromArgList(exp.RegexpReplace, args)
	if !regexpReplace.Bool("replacement") {
		regexpReplace.Set("replacement", exp.LiteralString(""))
	}
	return regexpReplace
}

func buildRegexpLike(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	return exp.New(exp.RegexpLike,
		"this", seqGet(args, 0),
		"expression", seqGet(args, 1),
		"flag", seqGet(args, 2),
		"full_match", true)
}

func dateTruncToTime(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	trunc := sqlglot.DateTruncToTime(args, d)
	trunc.Set("unit", sqlglot.MapDatePart(trunc.Get("unit"), nil))
	trunc.Set("input_type_preserved", true)
	return trunc
}

func buildTrunc(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	trunc := sqlglot.BuildTrunc(args, d, true, "", false, true)
	if trunc.IsKind(exp.DateTrunc) {
		trunc.Set("input_type_preserved", true)
	}
	return trunc
}

func buildRegexpExtract(kind exp.Kind) sqlglot.FuncBuilder {
	return func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
		kv := []any{
			"this", seqGet(args, 0),
			"expression", seqGet(args, 1),
			"position", seqGet(args, 2),
			"occurrence", seqGet(args, 3),
			"parameters", seqGet(args, 4),
			"group", orExpr(seqGet(args, 5), exp.LiteralNumber(0)),
		}
		if kind == exp.RegexpExtract {
			kv = append(kv, "null_if_pos_overflow", d.RegexpExtractPositionOverflowReturnsNull)
		}
		return exp.New(kind, kv...)
	}
}

// buildTimestampFromParts supports TIMESTAMP_FROM_PARTS(year, month, day, hour, minute, second
// [, nanosecond] [, time_zone]) and the Snowflake specific TIMESTAMP_FROM_PARTS(date, time).
func buildTimestampFromParts(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	if len(args) == 2 {
		return exp.New(exp.TimestampFromParts, "this", seqGet(args, 0), "expression", seqGet(args, 1))
	}
	return exp.FromArgList(exp.TimestampFromParts, args)
}

// buildRound unwraps Snowflake's named parameters: EXPR => this, SCALE => decimals,
// ROUNDING_MODE => truncate. Arguments are either all named or all positional.
func buildRound(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	kwargMap := map[string]string{"EXPR": "this", "SCALE": "decimals", "ROUNDING_MODE": "truncate"}
	positionalKeys := []string{"this", "decimals", "truncate"}
	roundArgs := map[string]*exp.Expr{}
	var order []string
	set := func(k string, v *exp.Expr) {
		if _, ok := roundArgs[k]; !ok {
			order = append(order, k)
		}
		roundArgs[k] = v
	}
	positionalIdx := 0
	for _, arg := range args {
		if arg.IsKind(exp.Kwarg) {
			if roundKey, ok := kwargMap[strings.ToUpper(arg.This().Name())]; ok {
				set(roundKey, arg.Expression())
			}
		} else if positionalIdx < len(positionalKeys) {
			set(positionalKeys[positionalIdx], arg)
			positionalIdx++
		}
	}
	var kv []any
	for _, k := range order {
		kv = append(kv, k, roundArgs[k])
	}
	expression := exp.New(exp.Round, kv...)
	expression.Set("casts_non_integer_decimals", true)
	return expression
}

func buildArraySort(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	asc := seqGet(args, 1)
	nullsFirst := seqGet(args, 2)
	if nullsFirst == nil && (asc == nil || asc.IsKind(exp.Boolean)) {
		nullsFirst = exp.NewBoolean(asc != nil && !asc.Bool("this"))
	}
	return exp.New(exp.ArraySort, "this", seqGet(args, 0), "asc", asc, "nulls_first", nullsFirst)
}

// buildGenerator unwraps Snowflake's named parameters: ROWCOUNT => rowcount,
// TIMELIMIT => timelimit.
func buildGenerator(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	kwargMap := map[string]string{"ROWCOUNT": "rowcount", "TIMELIMIT": "timelimit"}
	positionalKeys := []string{"rowcount", "timelimit"}
	genArgs := map[string]*exp.Expr{}
	var order []string
	set := func(k string, v *exp.Expr) {
		if _, ok := genArgs[k]; !ok {
			order = append(order, k)
		}
		genArgs[k] = v
	}
	for i, arg := range args {
		if arg.IsKind(exp.Kwarg) {
			if genKey, ok := kwargMap[strings.ToUpper(arg.This().Name())]; ok {
				set(genKey, arg.Expression())
			}
		} else if i < len(positionalKeys) {
			set(positionalKeys[i], arg)
		}
	}
	var kv []any
	for _, k := range order {
		kv = append(kv, k, genArgs[k])
	}
	return exp.New(exp.Generator, kv...)
}

func castOrAnonymous(name string, dtype exp.DType) sqlglot.FuncBuilder {
	return func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
		if len(args) == 1 {
			return exp.NewCast(args[0], dtype)
		}
		return exp.New(exp.Anonymous, "this", name, "expressions", args)
	}
}

// buildObjectConstruct builds OBJECT_CONSTRUCT as a StarMap or a Struct of key/value pairs.
func buildObjectConstruct(args []*exp.Expr) *exp.Expr {
	expression := sqlglot.BuildVarMap(args, nil)
	if expression.IsKind(exp.StarMap) {
		return expression
	}
	keys, values := expression.Keys(), expression.Values()
	var exprs []*exp.Expr
	for i := 0; i < len(keys) && i < len(values); i++ {
		exprs = append(exprs, exp.New(exp.PropertyEQ, "this", keys[i], "expression", values[i]))
	}
	return exp.New(exp.Struct, "expressions", exprs)
}

// RankingWindowFunctionsWithFrame are the window functions that get a default frame.
var RankingWindowFunctionsWithFrame = []exp.Kind{exp.FirstValue, exp.LastValue, exp.NthValue}

func functions(c *sqlglot.ParserConfig) {
	for k, v := range map[string]sqlglot.FuncBuilder{
		"CHARINDEX": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.StrPosition, "this", seqGet(args, 1), "substr", seqGet(args, 0), "position", seqGet(args, 2), "clamp_position", true)
		},
		"ADD_MONTHS": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.AddMonths, "this", seqGet(args, 0), "expression", seqGet(args, 1), "preserve_end_of_month", true)
		},
		"APPROX_PERCENTILE": sqlglot.FromArgList(exp.ApproxQuantile),
		"CURRENT_TIME": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Localtime, "this", seqGet(args, 0))
		},
		"APPROX_TOP_K":    buildApproxTopK,
		"ARRAY_CONSTRUCT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr { return exp.New(exp.Array, "expressions", args) },
		"ARRAY_CONTAINS": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ArrayContains, "this", seqGet(args, 1), "expression", seqGet(args, 0), "ensure_variant", false, "check_null", true)
		},
		"ARRAY_DISTINCT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ArrayDistinct, "this", seqGet(args, 0), "check_null", true)
		},
		"ARRAY_GENERATE_RANGE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.GenerateSeries, "start", seqGet(args, 0), "end", seqGet(args, 1), "step", seqGet(args, 2), "is_end_exclusive", true)
		},
		"ARRAY_EXCEPT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ArrayExcept, "this", seqGet(args, 0), "expression", seqGet(args, 1), "is_multiset", true)
		},
		"ARRAY_INTERSECTION": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ArrayIntersect, "expressions", args, "is_multiset", true)
		},
		"ARRAY_POSITION": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ArrayPosition, "this", seqGet(args, 1), "expression", seqGet(args, 0), "zero_based", true)
		},
		"ARRAY_SLICE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ArraySlice, "this", seqGet(args, 0), "start", seqGet(args, 1), "end", seqGet(args, 2), "zero_based", true)
		},
		"ARRAY_SORT":    buildArraySort,
		"ARRAY_FLATTEN": sqlglot.FromArgList(exp.Flatten),
		"ARRAY_TO_STRING": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ArrayToString, "this", seqGet(args, 0), "expression", seqGet(args, 1), "null_is_empty", true, "null_delim_is_null", true)
		},
		"ARRAYS_OVERLAP": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ArrayOverlaps, "this", seqGet(args, 0), "expression", seqGet(args, 1), "null_safe", true)
		},
		"BITAND":  buildBitwise(exp.BitwiseAnd, "BITAND"),
		"BIT_AND": buildBitwise(exp.BitwiseAnd, "BITAND"),
		"BITNOT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.BitwiseNot, "this", seqGet(args, 0))
		},
		"BIT_NOT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.BitwiseNot, "this", seqGet(args, 0))
		},
		"BITXOR":         buildBitwise(exp.BitwiseXor, "BITXOR"),
		"BIT_XOR":        buildBitwise(exp.BitwiseXor, "BITXOR"),
		"BITOR":          buildBitwise(exp.BitwiseOr, "BITOR"),
		"BIT_OR":         buildBitwise(exp.BitwiseOr, "BITOR"),
		"BITSHIFTLEFT":   buildBitwise(exp.BitwiseLeftShift, "BITSHIFTLEFT"),
		"BIT_SHIFTLEFT":  buildBitwise(exp.BitwiseLeftShift, "BIT_SHIFTLEFT"),
		"BITSHIFTRIGHT":  buildBitwise(exp.BitwiseRightShift, "BITSHIFTRIGHT"),
		"BIT_SHIFTRIGHT": buildBitwise(exp.BitwiseRightShift, "BIT_SHIFTRIGHT"),
		"BITANDAGG":      sqlglot.FromArgList(exp.BitwiseAndAgg),
		"BITAND_AGG":     sqlglot.FromArgList(exp.BitwiseAndAgg),
		"BIT_AND_AGG":    sqlglot.FromArgList(exp.BitwiseAndAgg),
		"BIT_ANDAGG":     sqlglot.FromArgList(exp.BitwiseAndAgg),
		"BITORAGG":       sqlglot.FromArgList(exp.BitwiseOrAgg),
		"BITOR_AGG":      sqlglot.FromArgList(exp.BitwiseOrAgg),
		"BIT_OR_AGG":     sqlglot.FromArgList(exp.BitwiseOrAgg),
		"BIT_ORAGG":      sqlglot.FromArgList(exp.BitwiseOrAgg),
		"BITXORAGG":      sqlglot.FromArgList(exp.BitwiseXorAgg),
		"BITXOR_AGG":     sqlglot.FromArgList(exp.BitwiseXorAgg),
		"BIT_XOR_AGG":    sqlglot.FromArgList(exp.BitwiseXorAgg),
		"BIT_XORAGG":     sqlglot.FromArgList(exp.BitwiseXorAgg),
		"BITMAP_OR_AGG":  sqlglot.FromArgList(exp.BitmapOrAgg),
		"BOOLAND": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Booland, "this", seqGet(args, 0), "expression", seqGet(args, 1), "round_input", true)
		},
		"BOOLOR": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Boolor, "this", seqGet(args, 0), "expression", seqGet(args, 1), "round_input", true)
		},
		"BOOLNOT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Boolnot, "this", seqGet(args, 0), "round_input", true)
		},
		"BOOLXOR": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Xor, "this", seqGet(args, 0), "expression", seqGet(args, 1), "round_input", true)
		},
		"CORR": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Corr, "this", seqGet(args, 0), "expression", seqGet(args, 1), "null_on_zero_variance", true)
		},
		"COUNT_IF": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.CountIf, "this", seqGet(args, 0), "zero_on_all_null", true)
		},
		"DATE":            buildDatetime("DATE", exp.DTypeDate),
		"DATEFROMPARTS":   buildDateFromParts,
		"DATE_FROM_PARTS": buildDateFromParts,
		"DATE_TRUNC":      dateTruncToTime,
		"DATEADD": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.DateAdd, "this", seqGet(args, 2), "expression", seqGet(args, 1), "unit", sqlglot.MapDatePart(seqGet(args, 0), nil))
		},
		"DATEDIFF": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.DateDiff, "this", seqGet(args, 2), "expression", seqGet(args, 1), "unit", sqlglot.MapDatePart(seqGet(args, 0), nil), "date_part_boundary", true)
		},
		"DAYNAME": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Dayname, "this", seqGet(args, 0), "abbreviated", true)
		},
		"DAYOFWEEKISO": sqlglot.FromArgList(exp.DayOfWeekIso),
		"DIV0":         buildIfFromDiv0,
		"DIV0NULL":     buildIfFromDiv0null,
		"EDITDISTANCE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Levenshtein, "this", seqGet(args, 0), "expression", seqGet(args, 1), "max_dist", seqGet(args, 2))
		},
		"FLATTEN":   sqlglot.FromArgList(exp.Explode),
		"GENERATOR": buildGenerator,
		"GET":       sqlglot.FromArgList(exp.GetExtract),
		"GETDATE":   sqlglot.FromArgList(exp.CurrentTimestamp),
		"GET_PATH": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.JSONExtract, "this", seqGet(args, 0), "expression", d.ToJSONPath(seqGet(args, 1)), "requires_json", true)
		},
		"GREATEST_IGNORE_NULLS": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Greatest, "this", seqGet(args, 0), "expressions", args[min(1, len(args)):], "ignore_nulls", true)
		},
		"LEAST_IGNORE_NULLS": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Least, "this", seqGet(args, 0), "expressions", args[min(1, len(args)):], "ignore_nulls", true)
		},
		"LEFT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Left, "this", seqGet(args, 0), "expression", seqGet(args, 1), "negative_length_returns_empty", true)
		},
		"HEX_DECODE_BINARY": sqlglot.FromArgList(exp.Unhex),
		"HEX_ENCODE":        sqlglot.FromArgList(exp.Hex),
		"IFF":               sqlglot.FromArgList(exp.If),
		"JAROWINKLER_SIMILARITY": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.JarowinklerSimilarity, "this", seqGet(args, 0), "expression", seqGet(args, 1), "case_insensitive", true, "integer_scale", true)
		},
		"MD5_HEX":            sqlglot.FromArgList(exp.MD5),
		"MD5_BINARY":         sqlglot.FromArgList(exp.MD5Digest),
		"MD5_NUMBER_LOWER64": sqlglot.FromArgList(exp.MD5NumberLower64),
		"MD5_NUMBER_UPPER64": sqlglot.FromArgList(exp.MD5NumberUpper64),
		"MONTHNAME": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Monthname, "this", seqGet(args, 0), "abbreviated", true)
		},
		"LAST_DAY": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.LastDay, "this", seqGet(args, 0), "unit", sqlglot.MapDatePart(seqGet(args, 1), nil))
		},
		"LEN": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Length, "this", seqGet(args, 0), "binary", true)
		},
		"LENGTH": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Length, "this", seqGet(args, 0), "binary", true)
		},
		"LOCALTIMESTAMP":   sqlglot.FromArgList(exp.CurrentTimestamp),
		"NULLIFZERO":       buildIfFromNullifzero,
		"OBJECT_CONSTRUCT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr { return buildObjectConstruct(args) },
		"OBJECT_KEYS":      sqlglot.FromArgList(exp.JSONKeys),
		"OCTET_LENGTH":     sqlglot.FromArgList(exp.ByteLength),
		"PARSE_URL": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ParseUrl, "this", seqGet(args, 0), "permissive", seqGet(args, 1))
		},
		"REGEXP_EXTRACT_ALL": buildRegexpExtract(exp.RegexpExtractAll),
		"REGEXP_LIKE":        buildRegexpLike,
		"REGEXP_REPLACE":     buildRegexpReplace,
		"REGEXP_SUBSTR":      buildRegexpExtract(exp.RegexpExtract),
		"REGEXP_SUBSTR_ALL":  buildRegexpExtract(exp.RegexpExtractAll),
		"RANDOM": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Rand, "this", seqGet(args, 0), "lower", exp.LiteralNumber(-9.223372036854776e+18), "upper", exp.LiteralNumber(9.223372036854776e+18))
		},
		"REPLACE": sqlglot.BuildReplaceWithOptionalReplacement,
		"RIGHT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Right, "this", seqGet(args, 0), "expression", seqGet(args, 1), "negative_length_returns_empty", true)
		},
		"RLIKE":       buildRegexpLike,
		"ROUND":       buildRound,
		"SHA1_BINARY": sqlglot.FromArgList(exp.SHA1Digest),
		"SHA1_HEX":    sqlglot.FromArgList(exp.SHA),
		"SHA2_BINARY": sqlglot.FromArgList(exp.SHA2Digest),
		"SHA2_HEX":    sqlglot.FromArgList(exp.SHA2),
		"SPLIT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Split, "this", seqGet(args, 0), "expression", seqGet(args, 1), "null_returns_null", true, "empty_delimiter_returns_whole", true)
		},
		"SQUARE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Pow, "this", seqGet(args, 0), "expression", exp.LiteralNumber(2))
		},
		"STDDEV_SAMP": sqlglot.FromArgList(exp.Stddev),
		"SYSDATE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.CurrentTimestamp, "this", seqGet(args, 0), "sysdate", true)
		},
		"TABLE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.TableFromRows, "this", seqGet(args, 0))
		},
		"TIMEADD": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.TimeAdd, "this", seqGet(args, 2), "expression", seqGet(args, 1), "unit", sqlglot.MapDatePart(seqGet(args, 0), nil))
		},
		"TIMEDIFF": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.DateDiff, "this", seqGet(args, 2), "expression", seqGet(args, 1), "unit", sqlglot.MapDatePart(seqGet(args, 0), nil), "date_part_boundary", true)
		},
		"TIME_FROM_PARTS": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.TimeFromParts, "hour", seqGet(args, 0), "min", seqGet(args, 1), "sec", seqGet(args, 2), "nano", seqGet(args, 3), "overflow", true)
		},
		"TIMESTAMPADD": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.DateAdd, "this", seqGet(args, 2), "expression", seqGet(args, 1), "unit", sqlglot.MapDatePart(seqGet(args, 0), nil))
		},
		"TIMESTAMPDIFF": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.DateDiff, "this", seqGet(args, 2), "expression", seqGet(args, 1), "unit", sqlglot.MapDatePart(seqGet(args, 0), nil), "date_part_boundary", true)
		},
		"TIMESTAMPFROMPARTS":       buildTimestampFromParts,
		"TIMESTAMP_FROM_PARTS":     buildTimestampFromParts,
		"TIMESTAMPNTZFROMPARTS":    buildTimestampFromParts,
		"TIMESTAMP_NTZ_FROM_PARTS": buildTimestampFromParts,
		"TRUNC":                    buildTrunc,
		"TRUNCATE":                 buildTrunc,
		"TRY_DECRYPT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Decrypt, "this", seqGet(args, 0), "passphrase", seqGet(args, 1), "aad", seqGet(args, 2), "encryption_method", seqGet(args, 3), "safe", true)
		},
		"TRY_DECRYPT_RAW": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.DecryptRaw, "this", seqGet(args, 0), "key", seqGet(args, 1), "iv", seqGet(args, 2), "aad", seqGet(args, 3), "encryption_method", seqGet(args, 4), "aead", seqGet(args, 5), "safe", true)
		},
		"TRY_PARSE_JSON": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ParseJSON, "this", seqGet(args, 0), "safe", true)
		},
		"TRY_TO_BINARY": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ToBinary, "this", seqGet(args, 0), "format", seqGet(args, 1), "safe", true)
		},
		"TRY_TO_BOOLEAN": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ToBoolean, "this", seqGet(args, 0), "safe", true)
		},
		"TRY_TO_DATE":    buildDatetime("TRY_TO_DATE", exp.DTypeDate /* safe */, true),
		"TRY_TO_DECIMAL": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr { return buildToNumber(args /* safe */, true) },
		"TRY_TO_NUMBER":  func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr { return buildToNumber(args /* safe */, true) },
		"TRY_TO_NUMERIC": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr { return buildToNumber(args /* safe */, true) },
		"TRY_TO_DOUBLE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ToDouble, "this", seqGet(args, 0), "format", seqGet(args, 1), "safe", true)
		},
		"TRY_TO_FILE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ToFile, "this", seqGet(args, 0), "path", seqGet(args, 1), "safe", true)
		},
		"TRY_TO_TIME":              buildDatetime("TRY_TO_TIME", exp.DTypeTime /* safe */, true),
		"TRY_TO_TIMESTAMP":         buildDatetime("TRY_TO_TIMESTAMP", exp.DTypeTimestamp /* safe */, true),
		"TRY_TO_TIMESTAMP_LTZ":     buildDatetime("TRY_TO_TIMESTAMP_LTZ", exp.DTypeTimestampltz /* safe */, true),
		"TRY_TO_TIMESTAMP_NTZ":     buildDatetime("TRY_TO_TIMESTAMP_NTZ", exp.DTypeTimestampntz /* safe */, true),
		"TRY_TO_TIMESTAMP_TZ":      buildDatetime("TRY_TO_TIMESTAMP_TZ", exp.DTypeTimestamptz /* safe */, true),
		"TO_CHAR":                  sqlglot.BuildTimetostrOrTochar,
		"TO_DATE":                  buildDatetime("TO_DATE", exp.DTypeDate),
		"TO_DECIMAL":               func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr { return buildToNumber(args) },
		"TO_NUMBER":                func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr { return buildToNumber(args) },
		"TO_NUMERIC":               func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr { return buildToNumber(args) },
		"TO_TIME":                  buildDatetime("TO_TIME", exp.DTypeTime),
		"TO_TIMESTAMP":             buildDatetime("TO_TIMESTAMP", exp.DTypeTimestamp),
		"TO_TIMESTAMP_LTZ":         buildDatetime("TO_TIMESTAMP_LTZ", exp.DTypeTimestampltz),
		"TO_TIMESTAMP_NTZ":         buildDatetime("TO_TIMESTAMP_NTZ", exp.DTypeTimestampntz),
		"TO_TIMESTAMP_TZ":          buildDatetime("TO_TIMESTAMP_TZ", exp.DTypeTimestamptz),
		"TO_GEOGRAPHY":             castOrAnonymous("TO_GEOGRAPHY", exp.DTypeGeography),
		"TO_GEOMETRY":              castOrAnonymous("TO_GEOMETRY", exp.DTypeGeometry),
		"TO_VARCHAR":               sqlglot.BuildTimetostrOrTochar,
		"TO_JSON":                  sqlglot.FromArgList(exp.JSONFormat),
		"VECTOR_COSINE_SIMILARITY": sqlglot.FromArgList(exp.CosineDistance),
		"VECTOR_INNER_PRODUCT":     sqlglot.FromArgList(exp.DotProduct),
		"VECTOR_L1_DISTANCE":       sqlglot.FromArgList(exp.ManhattanDistance),
		"VECTOR_L2_DISTANCE":       sqlglot.FromArgList(exp.EuclideanDistance),
		"ZEROIFNULL":               buildIfFromZeroifnull,
		"LIKE":                     sqlglot.BuildLikeFor(exp.Like, false),
		"ILIKE":                    sqlglot.BuildLikeFor(exp.ILike, false),
		"SEARCH":                   buildSearch,
		"SKEW":                     sqlglot.FromArgList(exp.Skewness),
		"SPLIT_PART": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.SplitPart, "this", seqGet(args, 0), "delimiter", seqGet(args, 1), "part_index", seqGet(args, 2), "part_index_zero_as_one", true, "empty_delimiter_returns_whole", true)
		},
		"STRTOK": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Strtok, "this", seqGet(args, 0), "delimiter", orExpr(seqGet(args, 1), exp.LiteralString(" ")), "part_index", orExpr(seqGet(args, 2), exp.LiteralNumber("1")))
		},
		"STRTOK_TO_ARRAY": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.StrtokToArray, "this", seqGet(args, 0), "expression", orExpr(seqGet(args, 1), exp.LiteralString(" ")))
		},
		"SYSTIMESTAMP": sqlglot.FromArgList(exp.CurrentTimestamp),
		"IDENTIFIER":   sqlglot.FromArgList(exp.DynamicIdentifier),
		"UNICODE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Unicode, "this", seqGet(args, 0), "empty_is_zero", true)
		},
		"WEEKISO":    sqlglot.FromArgList(exp.WeekOfYear),
		"WEEKOFYEAR": sqlglot.FromArgList(exp.Week),
	} {
		c.Functions[k] = v
	}
	delete(c.Functions, "PREDICT")
}

func showParser(this string, terse, iceberg bool) sqlglot.ParseFunc {
	return func(p *sqlglot.Parser) *exp.Expr { return parseShowSnowflake(p, this, terse, iceberg) }
}

var stagedFileSingleTokens = tokens.NewSet(tokens.Dot, tokens.Mod, tokens.Slash)

// FlattenColumns are the output columns of FLATTEN.
var FlattenColumns = []string{"SEQ", "KEY", "PATH", "INDEX", "VALUE", "THIS"}

var schemaKinds = map[string]bool{
	"OBJECTS": true, "TABLES": true, "VIEWS": true, "SEQUENCES": true, "UNIQUE KEYS": true, "IMPORTED KEYS": true,
}

var nonTableCreatables = map[string]bool{"STORAGE INTEGRATION": true, "TAG": true, "WAREHOUSE": true, "STREAMLIT": true}

var undropObjects = sqlglot.OptionsType{
	"ACCOUNT":   {},
	"DATABASE":  {},
	"NOTEBOOK":  {},
	"SCHEMA":    {},
	"SNAPSHOT":  {},
	"STREAMLIT": {},
	"TABLE":     {},
	"TAG":       {},
	"TYPE":      {},
	"DYNAMIC":   {{"TABLE"}},
	"EXTERNAL":  {{"VOLUME"}},
	"ICEBERG":   {{"TABLE"}},
}

func exprFn(kind exp.Kind) sqlglot.ParseFunc {
	return func(p *sqlglot.Parser) *exp.Expr { return p.Expr(kind) }
}

var describeQualifierParsers = map[string]sqlglot.ParseFunc{
	"API":         exprFn(exp.ApiProperty),
	"APPLICATION": exprFn(exp.ApplicationProperty),
	"CATALOG":     exprFn(exp.CatalogProperty),
	"COMPUTE":     exprFn(exp.ComputeProperty),
	"DATABASE": func(p *sqlglot.Parser) *exp.Expr {
		if p.Curr.Ok() && strings.ToUpper(p.Curr.Text) == "ROLE" {
			return p.Expr(exp.DatabaseProperty)
		}
		return nil
	},
	"DYNAMIC":      exprFn(exp.DynamicProperty),
	"EXTERNAL":     exprFn(exp.ExternalProperty),
	"HYBRID":       exprFn(exp.HybridProperty),
	"ICEBERG":      exprFn(exp.IcebergProperty),
	"MASKING":      exprFn(exp.MaskingProperty),
	"MATERIALIZED": exprFn(exp.MaterializedProperty),
	"NETWORK":      exprFn(exp.NetworkProperty),
	"ROW": func(p *sqlglot.Parser) *exp.Expr {
		if p.MatchTextSeq("ACCESS") {
			return p.Expr(exp.RowAccessProperty)
		}
		return nil
	},
	"SECURITY": func(p *sqlglot.Parser) *exp.Expr {
		if p.Curr.Ok() && strings.ToUpper(p.Curr.Text) == "INTEGRATION" {
			return p.Expr(exp.SecurityIntegrationProperty)
		}
		return nil
	},
}

func parser(c *sqlglot.ParserConfig) {
	c.IdentifyPivotStrings = true
	c.TypedLambdaArgs = true
	c.DefaultSamplingMethod = "BERNOULLI"
	c.ColonIsVariantExtract = true
	c.JsonExtractRequiresJsonExpression = true
	c.SupportsNthValueFromModifier = true

	c.TypeTokens = c.TypeTokens.With(tokens.File)
	c.StructTypeTokens = c.StructTypeTokens.With(tokens.File)
	c.NestedTypeTokens = c.NestedTypeTokens.With(tokens.File)

	c.IdVarTokens = c.IdVarTokens.With(tokens.Except, tokens.Integration, tokens.Lateral, tokens.MatchCondition,
		tokens.Package, tokens.Policy, tokens.Pool, tokens.Role, tokens.Rollback, tokens.Rule, tokens.Using, tokens.Volume)
	c.FuncTokens = c.FuncTokens.With(tokens.Rollback)
	c.AliasTokens = c.AliasTokens.With(tokens.Integration, tokens.Lateral, tokens.Package, tokens.Policy,
		tokens.Pool, tokens.Role, tokens.Rollback, tokens.Rule, tokens.Using, tokens.Volume)
	c.TableAliasTokens = c.TableAliasTokens.With(tokens.Anti, tokens.Integration, tokens.Package, tokens.Policy,
		tokens.Pool, tokens.Role, tokens.Rollback, tokens.Rule, tokens.Semi, tokens.Volume, tokens.Window).Without(tokens.MatchCondition)
	c.ColonPlaceholderTokens = c.IdVarTokens.With(tokens.Number)

	c.NoParenFunctions[tokens.Localtime] = exp.Localtime
	// LOCALTIMESTAMP is a Snowflake alias of CURRENT_TIMESTAMP, like the parenthesised form in
	// FUNCTION_PARSERS. Only Snowflake treats it as such, so it must not map to Localtimestamp.
	c.NoParenFunctions[tokens.Localtimestamp] = exp.CurrentTimestamp
	c.NoParenFunctions[tokens.CurrentTime] = exp.Localtime

	c.RangeParsers[tokens.Rlike] = func(p *sqlglot.Parser, this *exp.Expr) *exp.Expr {
		return p.Expr(exp.RegexpLike, "this", this, "expression", p.ParseBitwise(), "full_match", true)
	}

	functions(c)

	c.FunctionParsers["DATE_PART"] = parseDatePart
	c.FunctionParsers["DIRECTORY"] = parseDirectory
	c.FunctionParsers["OBJECT_CONSTRUCT_KEEP_NULL"] = parseObjectConstructKeepNull
	c.FunctionParsers["LISTAGG"] = parseListagg
	c.FunctionParsers["SEMANTIC_VIEW"] = parseSemanticView
	c.FunctionParsers["SUBSTR"] = func(p *sqlglot.Parser) *exp.Expr { return p.ParseSubstring() }
	delete(c.FunctionParsers, "TRIM")

	c.Timestamps = c.Timestamps.Without(tokens.Time)

	c.AlterParsers["MODIFY"] = func(p *sqlglot.Parser) []*exp.Expr { return []*exp.Expr{p.ParseAlterTableAlter()} }
	c.AlterParsers["SESSION"] = func(p *sqlglot.Parser) []*exp.Expr { return []*exp.Expr{p.ParseAlterSession()} }
	c.AlterParsers["UNSET"] = func(p *sqlglot.Parser) []*exp.Expr {
		return []*exp.Expr{p.Expr(exp.Set,
			"tag", p.MatchTextSeq("TAG"),
			"expressions", p.ParseCSV(func() *exp.Expr { return p.ParseIdVar(true, nil) }),
			"unset", true)}
	}

	delete(c.StatementParsers, tokens.Pivot)
	delete(c.StatementParsers, tokens.Unpivot)
	c.StatementParsers[tokens.Get] = parseGet
	c.StatementParsers[tokens.List] = parseListStage
	c.StatementParsers[tokens.Put] = parsePut
	c.StatementParsers[tokens.Show] = func(p *sqlglot.Parser) *exp.Expr { return p.ParseShow() }
	c.StatementParsers[tokens.Undrop] = parseUndrop

	c.PropertyParsers["CREDENTIALS"] = sqlglot.PropertyParser{Fn: func(p *sqlglot.Parser, kw map[string]any) any {
		return parseCredentialsProperty(p)
	}}
	c.PropertyParsers["FILE_FORMAT"] = sqlglot.PropertyParser{Fn: func(p *sqlglot.Parser, kw map[string]any) any {
		return parseFileFormatProperty(p)
	}}
	c.PropertyParsers["LOCATION"] = sqlglot.PropertyParser{Fn: func(p *sqlglot.Parser, kw map[string]any) any {
		return parseLocationProperty(p)
	}}
	c.PropertyParsers["ROW"] = sqlglot.PropertyParser{Fn: func(p *sqlglot.Parser, kw map[string]any) any {
		if p.MatchTextSeq("ACCESS", "POLICY") {
			return parseRowAccessPolicy(p)
		}
		return p.ParseRow()
	}}
	c.PropertyParsers["TAG"] = sqlglot.PropertyParser{Fn: func(p *sqlglot.Parser, kw map[string]any) any { return parseTag(p) }}
	c.PropertyParsers["USING"] = sqlglot.PropertyParser{Fn: func(p *sqlglot.Parser, kw map[string]any) any {
		if !p.MatchTextSeq("TEMPLATE") {
			return nil
		}
		return p.Expr(exp.UsingTemplateProperty, "this", p.ParseStatement())
	}}

	// https://docs.snowflake.com/en/sql-reference/data-types-numeric#number
	c.TypeConverters = map[exp.DType]func(dt *exp.Expr) *exp.Expr{
		exp.DTypeDecimal: sqlglot.BuildDefaultDecimalType(intp(38), intp(0)),
	}

	c.ShowParsers = map[string]sqlglot.ParseFunc{
		"DATABASES":            showParser("DATABASES", false, false),
		"SCHEMAS":              showParser("SCHEMAS", false, false),
		"OBJECTS":              showParser("OBJECTS", false, false),
		"TABLES":               showParser("TABLES", false, false),
		"VIEWS":                showParser("VIEWS", false, false),
		"PRIMARY KEYS":         showParser("PRIMARY KEYS", false, false),
		"IMPORTED KEYS":        showParser("IMPORTED KEYS", false, false),
		"UNIQUE KEYS":          showParser("UNIQUE KEYS", false, false),
		"SEQUENCES":            showParser("SEQUENCES", false, false),
		"STAGES":               showParser("STAGES", false, false),
		"COLUMNS":              showParser("COLUMNS", false, false),
		"USERS":                showParser("USERS", false, false),
		"FILE FORMATS":         showParser("FILE FORMATS", false, false),
		"FUNCTIONS":            showParser("FUNCTIONS", false, false),
		"PROCEDURES":           showParser("PROCEDURES", false, false),
		"WAREHOUSES":           showParser("WAREHOUSES", false, false),
		"ICEBERG TABLES":       showParser("TABLES", false, true),
		"TERSE ICEBERG TABLES": showParser("TABLES", true, true),
		"TERSE DATABASES":      showParser("DATABASES", true, false),
		"TERSE SCHEMAS":        showParser("SCHEMAS", true, false),
		"TERSE OBJECTS":        showParser("OBJECTS", true, false),
		"TERSE TABLES":         showParser("TABLES", true, false),
		"TERSE VIEWS":          showParser("VIEWS", true, false),
		"TERSE SEQUENCES":      showParser("SEQUENCES", true, false),
		"TERSE USERS":          showParser("USERS", true, false),
		// TERSE has no semantic effect for KEYS, so we do not set the terse AST arg
		"TERSE PRIMARY KEYS":  showParser("PRIMARY KEYS", false, false),
		"TERSE IMPORTED KEYS": showParser("IMPORTED KEYS", false, false),
		"TERSE UNIQUE KEYS":   showParser("UNIQUE KEYS", false, false),
	}

	delete(c.SchemaUnnamedConstraints, "EXCLUDE")

	c.ConstraintParsers["WITH"] = parseWithConstraint
	c.ConstraintParsers["MASKING"] = parseWithConstraint
	c.ConstraintParsers["PROJECTION"] = parseWithConstraint
	c.ConstraintParsers["TAG"] = parseWithConstraint

	c.Creatables = c.Creatables.With(tokens.Integration, tokens.Package, tokens.Policy, tokens.Pool, tokens.Role,
		tokens.Rule, tokens.Volume)

	c.Lambdas[tokens.Arrow] = func(p *sqlglot.Parser, expressions []*exp.Expr) *exp.Expr {
		params := make([]*exp.Expr, len(expressions))
		for i, e := range expressions {
			if e.IsKind(exp.Cast) {
				params[i] = e.This()
			} else {
				params[i] = e
			}
		}
		return p.Expr(exp.Lambda,
			"this", p.ReplaceLambda(p.ParseAssignment(), expressions),
			"expressions", params)
	}

	c.ColumnOperators[tokens.Exclamation] = func(p *sqlglot.Parser, this, attr *exp.Expr) *exp.Expr {
		return p.Expr(exp.ModelAttribute, "this", this, "expression", attr)
	}

	if c.Extra == nil {
		c.Extra = map[string]any{}
	}
	c.Extra["DESCRIBE_QUALIFIER_PARSERS"] = describeQualifierParsers
	c.Extra["FLATTEN_COLUMNS"] = FlattenColumns

	methods(&c.Methods)
}

func intp(v int) *int { return &v }

func parseDirectory(p *sqlglot.Parser) *exp.Expr {
	table := p.ParseTableParts(false, false, false, false)
	this := table
	if table.IsKind(exp.Table) {
		this = table.This()
	}
	return p.Expr(exp.DirectoryStage, "this", this)
}

func parseTag(p *sqlglot.Parser) *exp.Expr {
	return p.Expr(exp.Tags, "expressions", p.ParseWrappedCSV(func() *exp.Expr { return propertyExpr(p.ParseProperty()) }, false))
}

func propertyExpr(v any) *exp.Expr {
	switch x := v.(type) {
	case *exp.Expr:
		return x
	case []*exp.Expr:
		if len(x) > 0 {
			return x[0]
		}
	}
	return nil
}

func parseWithConstraint(p *sqlglot.Parser) *exp.Expr {
	if p.Prev.Type != tokens.With {
		p.Retreat(p.Index - 1)
	}
	if p.MatchTextSeq("MASKING", "POLICY") {
		policy := p.ParseColumn()
		if policy.IsKind(exp.Column) {
			policy = policy.ToDot(true)
		}
		var expressions any
		if p.Match(tokens.Using) {
			expressions = p.ParseWrappedCSV(func() *exp.Expr { return p.ParseIdVar(true, nil) }, false)
		}
		return p.Expr(exp.MaskingPolicyColumnConstraint, "this", policy, "expressions", expressions)
	}
	if p.MatchTextSeq("PROJECTION", "POLICY") {
		policy := p.ParseColumn()
		if policy.IsKind(exp.Column) {
			policy = policy.ToDot(true)
		}
		return p.Expr(exp.ProjectionPolicyColumnConstraint, "this", policy)
	}
	if p.Match(tokens.Tag) {
		return parseTag(p)
	}
	return nil
}

func parseRowAccessPolicy(p *sqlglot.Parser) *exp.Expr {
	var policy *exp.Expr
	var expressions any
	// GET_DDL outputs #unknown_policy when the user lacks privileges to see the policy name
	if p.Match(tokens.Hash) {
		policy = p.ParseVar(true, nil, false)
		if policy != nil {
			policy = exp.New(exp.Var, "this", "#"+policy.Name())
		}
	} else {
		policy = p.ParseColumn()
		if policy.IsKind(exp.Column) {
			policy = policy.ToDot(true)
		}
		if !p.Match(tokens.On) {
			p.RaiseError("Expected ON after ROW ACCESS POLICY name")
		}
		expressions = p.ParseWrappedCSV(func() *exp.Expr { return p.ParseIdVar(true, nil) }, false)
	}
	return p.Expr(exp.RowAccessProperty, "this", policy, "expressions", expressions)
}

func parseListagg(p *sqlglot.Parser) *exp.Expr {
	expression := p.ParseStringAgg()
	if expression.Get("separator") == nil {
		expression.Set("separator", exp.LiteralString(""))
	}
	return expression
}

func parseObjectConstructKeepNull(p *sqlglot.Parser) *exp.Expr {
	index := p.Index
	column := p.ParseColumn()
	if column != nil && column.IsStar() {
		return p.Expr(exp.JSONObject, "expressions", []*exp.Expr{column})
	}
	p.Retreat(index)
	return p.ParseJsonObject(false)
}

// https://docs.snowflake.com/en/sql-reference/functions/date_part.html
// https://docs.snowflake.com/en/sql-reference/functions-date-time.html#label-supported-date-time-parts
func parseDatePart(p *sqlglot.Parser) *exp.Expr {
	this := p.ParseVar(false, nil, false)
	if this == nil {
		this = p.ParseType(true, false)
	}
	if this == nil {
		return nil
	}
	// Handle both syntaxes: DATE_PART(part, expr) and DATE_PART(part FROM expr)
	var expression *exp.Expr
	if p.MatchSet(tokens.NewSet(tokens.From, tokens.Comma)) {
		expression = p.ParseBitwise()
	}
	return p.Expr(exp.Extract, "this", sqlglot.MapDatePart(this, p.D), "expression", expression)
}

func parseShowSnowflake(p *sqlglot.Parser, this string, terse, iceberg bool) *exp.Expr {
	var scope *exp.Expr
	var scopeKind any
	history := p.MatchTextSeq("HISTORY")
	var like *exp.Expr
	if p.Match(tokens.Like) {
		like = p.ParseString()
	}
	if p.Match(tokens.In) {
		switch {
		case p.MatchTextSeq("ACCOUNT"):
			scopeKind = "ACCOUNT"
		case p.MatchTextSeq("CLASS"):
			scopeKind = "CLASS"
			scope = p.ParseTableParts(false, false, false, false)
		case p.MatchTextSeq("APPLICATION"):
			kind := "APPLICATION"
			if p.MatchTextSeq("PACKAGE") {
				kind += " PACKAGE"
			}
			scopeKind = kind
			scope = p.ParseTableParts(false, false, false, false)
		case p.MatchSet(p.C.DbCreatables):
			scopeKind = p.PrevTextUpper()
			if p.Curr.Ok() {
				scope = p.ParseTableParts(false, false, false, false)
			}
		case p.Curr.Ok():
			if schemaKinds[this] {
				scopeKind = "SCHEMA"
			} else {
				scopeKind = "TABLE"
			}
			scope = p.ParseTableParts(false, false, false, false)
		}
	}
	var startsWith any = false
	if p.MatchTextSeq("STARTS", "WITH") {
		startsWith = p.ParseString()
	}
	limit := p.ParseLimit(nil, false, false)
	var from *exp.Expr
	if p.Match(tokens.From) {
		from = p.ParseString()
	}
	var privileges any = false
	if p.MatchTextSeq("WITH", "PRIVILEGES") {
		privileges = p.ParseCSV(func() *exp.Expr { return p.ParseVar(true, nil, true) })
	}
	return p.Expr(exp.Show,
		"terse", terse,
		"iceberg", iceberg,
		"this", this,
		"history", history,
		"like", like,
		"scope", scope,
		"scope_kind", scopeKind,
		"starts_with", startsWith,
		"limit", limit,
		"from_", from,
		"privileges", privileges)
}

func parseUndrop(p *sqlglot.Parser) *exp.Expr {
	start := p.Prev
	kind := p.ParseVarFromOptions(undropObjects, false)
	if kind == nil {
		return p.ParseAsCommand(start)
	}
	isDB := kind.Name() == "ACCOUNT" || kind.Name() == "DATABASE" || kind.Name() == "SCHEMA"
	this := p.ParseTableParts(false, isDB, false, false)
	var rename *exp.Expr
	if p.MatchTextSeq("RENAME", "TO") {
		rename = p.ParseTableParts(false, false, false, false)
	}
	return p.Expr(exp.Undrop, "this", this, "kind", kind.Name(), "rename", rename)
}

func parsePut(p *sqlglot.Parser) *exp.Expr {
	if p.Curr.Type != tokens.String && p.Curr.Type != tokens.UriStart {
		return p.ParseAsCommand(p.Prev)
	}
	this := p.ParseString()
	if this == nil {
		this = exp.LiteralString(parseLocationPath(p).Name())
	}
	return p.Expr(exp.Put, "this", this, "target", parseLocationPath(p), "properties", p.ParseProperties(false))
}

func parseGet(p *sqlglot.Parser) *exp.Expr {
	start := p.Prev
	// If we detect GET( then we need to parse a function, not a statement
	if p.Match(tokens.LParen) {
		p.Retreat(p.Index - 2)
		return p.ParseExpression()
	}
	target := parseLocationPath(p)
	// Parse as command if unquoted file path
	if p.Curr.Type == tokens.UriStart {
		return p.ParseAsCommand(start)
	}
	return p.Expr(exp.Get, "this", p.ParseString(), "target", target, "properties", p.ParseProperties(false))
}

func parseListStage(p *sqlglot.Parser) *exp.Expr {
	var this *exp.Expr
	if p.Curr.Ok() {
		this = parseLocationPath(p)
	}
	var pattern *exp.Expr
	if p.MatchTextSeq("PATTERN", "=") {
		pattern = p.ParseString()
		if pattern == nil {
			p.RaiseError("Expected string after PATTERN =")
		}
	}
	return p.Expr(exp.ListStage, "this", this, "pattern", pattern)
}

func parseLocationProperty(p *sqlglot.Parser) *exp.Expr {
	p.Match(tokens.Eq)
	return p.Expr(exp.LocationProperty, "this", parseLocationPath(p))
}

func parseLocationPath(p *sqlglot.Parser) *exp.Expr {
	start := p.Curr
	p.AdvanceAny(true)
	// We avoid consuming a comma token because external tables like @foo and @bar can be joined
	// in a query with a comma separator, as well as closing paren in case of subqueries
	for p.IsConnected() && !p.Check(tokens.Comma) && !p.Check(tokens.LParen) && !p.Check(tokens.RParen) {
		p.AdvanceAny(true)
	}
	return exp.NewVar(p.FindSQL(start, p.Prev))
}

func parseFileFormatProperty(p *sqlglot.Parser) *exp.Expr {
	p.Match(tokens.Eq)
	var expressions []*exp.Expr
	if p.Check(tokens.LParen) {
		expressions = p.ParseWrappedOptions()
	} else {
		expressions = []*exp.Expr{p.ParseFormatName()}
	}
	return p.Expr(exp.FileFormatProperty, "expressions", expressions)
}

func parseCredentialsProperty(p *sqlglot.Parser) *exp.Expr {
	return p.Expr(exp.CredentialsProperty, "expressions", p.ParseWrappedOptions())
}

func parseSemanticView(p *sqlglot.Parser) *exp.Expr {
	kv := []any{"this", p.ParseTableParts(false, false, false, false)}
	for p.Curr.Ok() && !p.Check(tokens.RParen) {
		if p.MatchTexts("DIMENSIONS", "METRICS", "FACTS") {
			keyword := strings.ToLower(p.Prev.Text)
			kv = append(kv, keyword, p.ParseCSV(func() *exp.Expr { return p.ParseAlias(p.ParseDisjunction(), true) }))
		} else if p.MatchTextSeq("WHERE") {
			kv = append(kv, "where", p.ParseExpression())
		} else {
			p.RaiseError("Expecting ) or encountered unexpected keyword")
			break
		}
	}
	return p.Expr(exp.SemanticView, kv...)
}

var describeQualifierNames = func() sqlglot.StringSet {
	s := sqlglot.StringSet{}
	for k := range describeQualifierParsers {
		s[k] = true
	}
	return s
}()

func matchTextSeqNoAdvance(p *sqlglot.Parser, texts ...string) bool {
	index := p.Index
	ok := p.MatchTextSeq(texts...)
	p.Retreat(index)
	return ok
}

func methods(m *sqlglot.ParserMethods) {
	superDescribe := m.ParseDescribe
	m.ParseDescribe = func(p *sqlglot.Parser) *exp.Expr {
		index := p.Index
		if p.MatchTextsIn(describeQualifierNames) {
			qualifier := describeQualifierParsers[p.PrevTextUpper()](p)
			if qualifier != nil {
				if p.MatchSet(p.C.Creatables) {
					kind := p.PrevTextUpper()
					this := p.ParseTable(sqlglot.TableOpts{Schema: true})
					properties := p.Expr(exp.Properties, "expressions", []*exp.Expr{qualifier})
					postProps := p.ParseProperties(false)
					var expressions any
					if postProps != nil {
						expressions = postProps.Expressions()
					}
					return p.Expr(exp.Describe, "this", this, "kind", kind, "properties", properties, "expressions", expressions)
				}
			}
		}
		p.Retreat(index)
		return superDescribe(p)
	}

	superUse := m.ParseUse
	m.ParseUse = func(p *sqlglot.Parser) *exp.Expr {
		if p.MatchTextSeq("SECONDARY", "ROLES") {
			var this *exp.Expr
			if p.MatchTexts("ALL", "NONE") {
				this = exp.NewVar(p.PrevTextUpper())
			}
			var roles any
			if this == nil {
				roles = p.ParseCSV(func() *exp.Expr { return p.ParseTable(sqlglot.TableOpts{}) })
			}
			return p.Expr(exp.Use, "kind", "SECONDARY ROLES", "this", this, "expressions", roles)
		}
		return superUse(p)
	}

	m.NegateRange = func(p *sqlglot.Parser, this *exp.Expr) *exp.Expr {
		if this == nil {
			return this
		}
		query := this.Get("query")
		if this.IsKind(exp.In) && query.Is(exp.Query) {
			// Snowflake treats `value NOT IN (subquery)` as `VALUE <> ALL (subquery)`, so we do this
			// conversion here to avoid parsing it into `NOT value IN (subquery)` which can produce
			// different results (most likely a SnowFlake bug).
			//
			// https://docs.snowflake.com/en/sql-reference/functions/in
			// Context: https://github.com/tobymao/sqlglot/issues/3890
			return p.Expr(exp.NEQ, "this", this.This(), "expression", exp.New(exp.All, "this", query.Unnest()))
		}
		return p.Expr(exp.Not, "this", this)
	}

	superCopyProperty := m.ParseCopyProperty
	m.ParseCopyProperty = func(p *sqlglot.Parser) *exp.Expr {
		if p.MatchTextSeq("TAGS") {
			return p.Expr(exp.CopyTagsProperty)
		}
		return superCopyProperty(p)
	}

	superPropertyBefore := m.ParsePropertyBefore
	m.ParsePropertyBefore = func(p *sqlglot.Parser) any {
		if prop := superPropertyBefore(p); exp.Truthy(prop) {
			return prop
		}
		if !p.Next.Ok() || p.Next.Type != tokens.Eq {
			return nil
		}
		if seq := p.ParseSequenceProperties(); seq != nil {
			return seq
		}
		return p.ParseKeyValueProperty(func() *exp.Expr { return p.ParsePrimaryOrVar() })
	}

	superWithProperty := m.ParseWithProperty
	m.ParseWithProperty = func(p *sqlglot.Parser) any {
		if p.Match(tokens.Tag) {
			return parseTag(p)
		}
		if p.MatchTextSeq("ROW", "ACCESS", "POLICY") {
			return parseRowAccessPolicy(p)
		}
		return superWithProperty(p)
	}

	superCreate := m.ParseCreate
	m.ParseCreate = func(p *sqlglot.Parser) *exp.Expr {
		expression := superCreate(p)
		if expression.IsKind(exp.Create) && nonTableCreatables[strings.ToUpper(expression.Text("kind"))] {
			// Replace the Table node with the enclosed Identifier
			expression.This().Replace(expression.This().This())
		}
		return expression
	}

	m.ParseBracketKeyValue = func(p *sqlglot.Parser, isMap bool) *exp.Expr {
		if isMap {
			// Keys are strings in Snowflake's objects, see also:
			// - https://docs.snowflake.com/en/sql-reference/data-types-semistructured
			// - https://docs.snowflake.com/en/sql-reference/functions/object_construct
			if s := p.ParseSlice(p.ParseString()); s != nil {
				return s
			}
			return p.ParseAssignment()
		}
		return p.ParseSlice(p.ParseAlias(p.ParseAssignment(), true))
	}

	superLateral := m.ParseLateral
	m.ParseLateral = func(p *sqlglot.Parser) *exp.Expr {
		lateral := superLateral(p)
		if lateral == nil {
			return lateral
		}
		if lateral.This().IsKind(exp.Explode) {
			tableAlias := lateral.Get("alias")
			columns := make([]*exp.Expr, len(FlattenColumns))
			for i, col := range FlattenColumns {
				columns[i] = exp.ToIdentifier(col)
			}
			if tableAlias != nil && !tableAlias.Bool("columns") {
				tableAlias.Set("columns", columns)
			} else if tableAlias == nil {
				cols := make([]any, len(columns))
				for i, c := range columns {
					cols[i] = c
				}
				exp.NewTableAliasOf(lateral, "_flattened", cols, exp.WithCopy(false))
			}
		}
		return lateral
	}

	superTableParts := m.ParseTableParts
	m.ParseTableParts = func(p *sqlglot.Parser, schema, isDbReference, wildcard, fast bool) *exp.Expr {
		// https://docs.snowflake.com/en/user-guide/querying-stage
		var table *exp.Expr
		if p.Check(tokens.String) {
			table = p.ParseString()
		} else if matchTextSeqNoAdvance(p, "@") {
			table = parseLocationPath(p)
		}
		if table != nil {
			var fileFormat, pattern *exp.Expr
			wrapped := p.Match(tokens.LParen)
			for p.Curr.Ok() && wrapped && !p.Match(tokens.RParen) {
				if p.MatchTextSeq("FILE_FORMAT", "=>") {
					fileFormat = p.ParseString()
					if fileFormat == nil {
						fileFormat = superTableParts(p, false, isDbReference, false, false)
					}
				} else if p.MatchTextSeq("PATTERN", "=>") {
					pattern = p.ParseString()
				} else {
					break
				}
				p.Match(tokens.Comma)
			}
			return p.Expr(exp.Table, "this", table, "format", fileFormat, "pattern", pattern)
		}
		return superTableParts(p, schema, isDbReference, false, fast)
	}

	superFunctionCall := m.ParseFunctionCall
	m.ParseFunctionCall = func(p *sqlglot.Parser, o sqlglot.FunctionOpts) *exp.Expr {
		this := superFunctionCall(p, o)
		// Snowflake can invoke a function whose name is dynamically resolved, e.g.
		// `IDENTIFIER('my_func')(1, 2)`. The trailing argument list is the call's arguments.
		//
		// https://docs.snowflake.com/en/sql-reference/identifier-literal
		if this.IsKind(exp.DynamicIdentifier) && p.Check(tokens.LParen) {
			this.Set("expressions", p.ParseWrappedCSV(func() *exp.Expr { return p.ParseLambda(false) }, false))
		}
		return this
	}

	superIdVar := m.ParseIdVar
	m.ParseIdVar = func(p *sqlglot.Parser, anyToken bool, toks *tokens.TokenSet) *exp.Expr {
		if p.MatchTextSeq("IDENTIFIER", "(") {
			identifier := superIdVar(p, anyToken, toks)
			if identifier == nil {
				identifier = p.ParseString()
			}
			p.MatchRParen(nil)
			return p.Expr(exp.DynamicIdentifier, "this", identifier)
		}
		return superIdVar(p, anyToken, toks)
	}

	superLambdaArg := m.ParseLambdaArg
	m.ParseLambdaArg = func(p *sqlglot.Parser) *exp.Expr {
		this := superLambdaArg(p)
		if this == nil {
			return this
		}
		if typ := p.ParseTypes(false, false, true, false); typ != nil {
			return p.Expr(exp.Cast, "this", this, "to", typ)
		}
		return this
	}

	m.ParseFileLocation = func(p *sqlglot.Parser) *exp.Expr {
		// Parse either a subquery or a staged file
		if p.Check(tokens.LParen) {
			return p.ParseSelect(sqlglot.SelectOpts{Table: true, NoSubqueryAlias: true})
		}
		return p.ParseTableParts(false, false, false, false)
	}

	superForeignKey := m.ParseForeignKey
	m.ParseForeignKey = func(p *sqlglot.Parser) *exp.Expr {
		// inlineFK, the REFERENCES columns are implied
		if p.Check(tokens.References) {
			return p.Expr(exp.ForeignKey)
		}
		// outoflineFK, explicitly names the columns
		return superForeignKey(p)
	}

	superSet := m.ParseSet
	m.ParseSet = func(p *sqlglot.Parser, unset, tag bool) *exp.Expr {
		set := superSet(p, unset, tag)
		if set.IsKind(exp.Set) {
			for _, expr := range set.Expressions() {
				if expr.IsKind(exp.SetItem) {
					expr.Set("kind", "VARIABLE")
				}
			}
		}
		return set
	}

	superPosition := m.ParsePosition
	m.ParsePosition = func(p *sqlglot.Parser, haystackFirst bool) *exp.Expr {
		result := superPosition(p, haystackFirst)
		result.Set("clamp_position", true)
		return result
	}

	superSubstring := m.ParseSubstring
	m.ParseSubstring = func(p *sqlglot.Parser) *exp.Expr {
		result := superSubstring(p)
		result.Set("zero_start", true)
		return result
	}

	superBuildCast := m.BuildCast
	m.BuildCast = func(p *sqlglot.Parser, strict bool, kv ...any) *exp.Expr {
		var to, this *exp.Expr
		for i := 0; i+1 < len(kv); i += 2 {
			switch kv[i] {
			case "to":
				to, _ = kv[i+1].(*exp.Expr)
			case "this":
				this, _ = kv[i+1].(*exp.Expr)
			}
		}
		if !strict && to != nil && to.DataTypeThis() == exp.DTypeBoolean {
			return p.Expr(exp.ToBoolean, "this", this, "safe", true)
		}
		cast := superBuildCast(p, strict, kv...)
		if cast.IsKind(exp.TryCast) && to != nil {
			if dt, ok := to.Arg("this").(exp.DType); ok && exp.DataTypeTextTypes.Has(dt) && len(to.Expressions()) > 0 {
				cast.Set("null_on_text_overflow", true)
			} else if to.DataTypeThis() == exp.DTypeDate && cast.This().IsString() {
				cast.Set("probe_date_format", true)
			}
		}
		return cast
	}

	superWindow := m.ParseWindow
	m.ParseWindow = func(p *sqlglot.Parser, this *exp.Expr, alias bool) *exp.Expr {
		result := superWindow(p, this, alias)
		// Set default window frame for ranking functions if not present
		if result.IsKind(exp.Window) && this.IsAny(RankingWindowFunctionsWithFrame...) && result.Get("spec") == nil {
			frame := exp.New(exp.WindowSpec,
				"kind", "ROWS",
				"start", "UNBOUNDED",
				"start_side", "PRECEDING",
				"end", "UNBOUNDED",
				"end_side", "FOLLOWING")
			result.Set("spec", frame)
		}
		return result
	}
}
