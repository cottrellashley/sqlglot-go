package sqlglot

import (
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
	"github.com/cottrellashley/sqlglot-go/trie"
)

// FromArgList returns a FuncBuilder mirroring `exp.Kind.from_arg_list`.
func FromArgList(kind exp.Kind) FuncBuilder {
	return func(args []*exp.Expr, _ *Dialect) *exp.Expr { return exp.FromArgList(kind, args) }
}

// BinaryRangeParser mirrors parser.binary_range_parser.
func BinaryRangeParser(kind exp.Kind, reverseArgs bool) RangeParser {
	return func(p *Parser, this *exp.Expr) *exp.Expr {
		expression := p.ParseBitwise()
		if reverseArgs {
			this, expression = expression, this
		}
		return p.ParseEscape(p.Expr(kind, "this", this, "expression", expression))
	}
}

// ---------------------------------------------------------------------------------------
// module-level builders (parser.build_*)

// BuildVarMap mirrors build_var_map.
func BuildVarMap(args []*exp.Expr, _ *Dialect) *exp.Expr {
	if len(args) == 1 && args[0].IsStar() {
		return exp.New(exp.StarMap, "this", args[0])
	}
	var keys, values []any
	for i := 0; i+1 < len(args); i += 2 {
		keys = append(keys, args[i])
		values = append(values, args[i+1])
	}
	return exp.New(exp.VarMap,
		"keys", exp.NewArray(append(keys, exp.WithCopy(false))...),
		"values", exp.NewArray(append(values, exp.WithCopy(false))...))
}

// BuildLike mirrors build_like.
func BuildLike(args []*exp.Expr, _ *Dialect) *exp.Expr {
	like := exp.New(exp.Like, "this", seqGet(args, 1), "expression", seqGet(args, 0))
	if len(args) > 2 {
		return exp.New(exp.Escape, "this", like, "expression", seqGet(args, 2))
	}
	return like
}

// BuildSortArray mirrors build_sort_array.
func BuildSortArray(args []*exp.Expr, _ *Dialect) *exp.Expr {
	asc := seqGet(args, 1)
	var nullsFirst *exp.Expr
	if asc == nil {
		nullsFirst = exp.NewTrue()
	} else {
		nullsFirst = asc.Copy()
	}
	return exp.New(exp.ArraySort, "this", seqGet(args, 0), "asc", asc, "nulls_first", nullsFirst)
}

// BuildArraySortNullsLast mirrors build_array_sort_nulls_last.
func BuildArraySortNullsLast(args []*exp.Expr, _ *Dialect) *exp.Expr {
	comparator := seqGet(args, 1)
	var nullsFirst *exp.Expr
	if comparator == nil {
		nullsFirst = exp.NewFalse()
	}
	return exp.New(exp.ArraySort, "this", seqGet(args, 0), "expression", comparator, "nulls_first", nullsFirst)
}

// BuildLogarithm mirrors build_logarithm.
func BuildLogarithm(args []*exp.Expr, d *Dialect) *exp.Expr {
	this, expression := seqGet(args, 0), seqGet(args, 1)
	if expression != nil {
		if d.LogBaseFirst == nil || !*d.LogBaseFirst {
			this, expression = expression, this
		}
		return exp.New(exp.Log, "this", this, "expression", expression)
	}
	if d.Parser.LogDefaultsToLn {
		return exp.New(exp.Ln, "this", this)
	}
	return exp.New(exp.Log, "this", this)
}

// BuildHex mirrors build_hex.
func BuildHex(args []*exp.Expr, d *Dialect) *exp.Expr {
	arg := seqGet(args, 0)
	if d.HexLowercase {
		return exp.New(exp.LowerHex, "this", arg)
	}
	return exp.New(exp.Hex, "this", arg)
}

// BuildLower mirrors build_lower.
func BuildLower(args []*exp.Expr, _ *Dialect) *exp.Expr {
	arg := seqGet(args, 0)
	if arg.Is(exp.Hex) {
		return exp.New(exp.LowerHex, "this", arg.This())
	}
	return exp.New(exp.Lower, "this", arg)
}

// BuildUpper mirrors build_upper.
func BuildUpper(args []*exp.Expr, _ *Dialect) *exp.Expr {
	arg := seqGet(args, 0)
	if arg.Is(exp.Hex) {
		return exp.New(exp.Hex, "this", arg.This())
	}
	return exp.New(exp.Upper, "this", arg)
}

// BuildExtractJSONWithPath mirrors build_extract_json_with_path.
func BuildExtractJSONWithPath(kind exp.Kind) FuncBuilder {
	return func(args []*exp.Expr, d *Dialect) *exp.Expr {
		e := exp.New(kind, "this", seqGet(args, 0), "expression", d.ToJSONPath(seqGet(args, 1)))
		if len(args) > 2 && kind == exp.JSONExtract {
			e.Set("expressions", args[2:])
		}
		if kind == exp.JSONExtractScalar {
			e.Set("scalar_only", d.JsonExtractScalarScalarOnly)
		}
		return e
	}
}

// BuildMod mirrors build_mod.
func BuildMod(args []*exp.Expr, _ *Dialect) *exp.Expr {
	this, expression := seqGet(args, 0), seqGet(args, 1)
	if this.Is(exp.Binary) {
		this = exp.New(exp.Paren, "this", this)
	}
	if expression.Is(exp.Binary) {
		expression = exp.New(exp.Paren, "this", expression)
	}
	return exp.New(exp.Mod, "this", this, "expression", expression)
}

// BuildPad mirrors build_pad.
func BuildPad(args []*exp.Expr, isLeft bool) *exp.Expr {
	return exp.New(exp.Pad, "this", seqGet(args, 0), "expression", seqGet(args, 1), "fill_pattern", seqGet(args, 2), "is_left", isLeft)
}

// BuildConvertTimezone mirrors build_convert_timezone.
func BuildConvertTimezone(args []*exp.Expr, defaultSourceTZ string) *exp.Expr {
	if len(args) == 2 {
		var sourceTZ *exp.Expr
		if defaultSourceTZ != "" {
			sourceTZ = exp.LiteralString(defaultSourceTZ)
		}
		return exp.New(exp.ConvertTimezone, "source_tz", sourceTZ, "target_tz", seqGet(args, 0), "timestamp", seqGet(args, 1))
	}
	return exp.FromArgList(exp.ConvertTimezone, args)
}

// BuildTrim mirrors build_trim.
func BuildTrim(args []*exp.Expr, isLeft, reverseArgs bool) *exp.Expr {
	this, expression := seqGet(args, 0), seqGet(args, 1)
	if expression != nil && reverseArgs {
		this, expression = expression, this
	}
	position := "TRAILING"
	if isLeft {
		position = "LEADING"
	}
	return exp.New(exp.Trim, "this", this, "expression", expression, "position", position)
}

// BuildCoalesce mirrors build_coalesce.
func BuildCoalesce(args []*exp.Expr, isNvl, isNull any) *exp.Expr {
	rest := []*exp.Expr{}
	if len(args) > 1 {
		rest = args[1:]
	}
	return exp.New(exp.Coalesce, "this", seqGet(args, 0), "expressions", rest, "is_nvl", isNvl, "is_null", isNull)
}

// BuildLocateStrposition mirrors build_locate_strposition.
func BuildLocateStrposition(args []*exp.Expr, _ *Dialect) *exp.Expr {
	return exp.New(exp.StrPosition, "this", seqGet(args, 1), "substr", seqGet(args, 0), "position", seqGet(args, 2))
}

func buildArrayFn(kind exp.Kind) FuncBuilder {
	return func(args []*exp.Expr, d *Dialect) *exp.Expr {
		return exp.New(kind, "this", seqGet(args, 0), "expression", seqGet(args, 1), "null_propagation", d.ArrayFuncsPropagatesNulls)
	}
}

// BuildArrayConcat mirrors build_array_concat.
func BuildArrayConcat(args []*exp.Expr, d *Dialect) *exp.Expr {
	rest := []*exp.Expr{}
	if len(args) > 1 {
		rest = args[1:]
	}
	return exp.New(exp.ArrayConcat, "this", seqGet(args, 0), "expressions", rest, "null_propagation", d.ArrayFuncsPropagatesNulls)
}

func castToText(e *exp.Expr) *exp.Expr {
	return exp.New(exp.Cast, "this", e, "to", exp.New(exp.DataType, "this", exp.DTypeText))
}

func restOf(args []*exp.Expr) []*exp.Expr {
	if len(args) > 1 {
		return args[1:]
	}
	return []*exp.Expr{}
}

func orNilBool(b bool) any {
	if b {
		return true
	}
	return nil
}

// ---------------------------------------------------------------------------------------
// base configuration

var (
	rawParserConfig = newBaseParserConfig()
)

// NewParserConfig returns a fresh copy of sqlglot's parser.Parser class configuration.
func NewParserConfig() *ParserConfig { return rawParserConfig.Clone() }

func pf(f func(p *Parser) *exp.Expr) ParseFunc { return f }

func newBaseParserConfig() *ParserConfig {
	c := &ParserConfig{}
	baseParserData(c)
	c.Methods = baseParserMethods()
	c.Extra = map[string]any{}

	// FUNCTIONS
	c.Functions = map[string]FuncBuilder{}
	for _, f := range exp.AllFunctions() {
		for _, name := range f.SQLNames() {
			c.Functions[name] = FromArgList(f)
		}
	}
	for _, name := range []string{"COALESCE", "IFNULL", "NVL"} {
		c.Functions[name] = func(args []*exp.Expr, _ *Dialect) *exp.Expr { return BuildCoalesce(args, nil, nil) }
	}
	arrayAgg := func(args []*exp.Expr, d *Dialect) *exp.Expr {
		return exp.New(exp.ArrayAgg, "this", seqGet(args, 0), "nulls_excluded", orNilBool(d.ArrayAggIncludesNulls == nil))
	}
	uuid := func(args []*exp.Expr, d *Dialect) *exp.Expr {
		return exp.New(exp.Uuid, "is_string", orNilBool(d.UuidIsStringType))
	}
	fns := map[string]FuncBuilder{
		"ARRAY": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			return exp.New(exp.Array, "expressions", nonNilList(args))
		},
		"SORT_ARRAY":   BuildSortArray,
		"ARRAYAGG":     arrayAgg,
		"ARRAY_AGG":    arrayAgg,
		"ARRAY_APPEND": buildArrayFn(exp.ArrayAppend),
		"ARRAY_CAT":    BuildArrayConcat,
		"ARRAY_CONCAT": BuildArrayConcat,
		"ARRAY_INTERSECT": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			return exp.New(exp.ArrayIntersect, "expressions", nonNilList(args))
		},
		"ARRAY_INTERSECTION": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			return exp.New(exp.ArrayIntersect, "expressions", nonNilList(args))
		},
		"ARRAY_PREPEND": buildArrayFn(exp.ArrayPrepend),
		"ARRAY_REMOVE":  buildArrayFn(exp.ArrayRemove),
		"COUNT": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			return exp.New(exp.Count, "this", seqGet(args, 0), "expressions", restOf(args), "big_int", true)
		},
		"CONCAT": func(args []*exp.Expr, d *Dialect) *exp.Expr {
			return exp.New(exp.Concat, "expressions", nonNilList(args), "safe", !d.StrictStringConcat, "coalesce", d.ConcatCoalesce)
		},
		"CONCAT_WS": func(args []*exp.Expr, d *Dialect) *exp.Expr {
			return exp.New(exp.ConcatWs, "expressions", nonNilList(args), "safe", !d.StrictStringConcat, "coalesce", d.ConcatWsCoalesce)
		},
		"CONVERT_TIMEZONE": func(args []*exp.Expr, _ *Dialect) *exp.Expr { return BuildConvertTimezone(args, "") },
		"DATE_TO_DATE_STR": func(args []*exp.Expr, _ *Dialect) *exp.Expr { return castToText(seqGet(args, 0)) },
		"GENERATE_DATE_ARRAY": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			step := seqGet(args, 2)
			if step == nil {
				step = exp.New(exp.Interval, "this", exp.LiteralString(1), "unit", exp.NewVar("DAY"))
			}
			return exp.New(exp.GenerateDateArray, "start", seqGet(args, 0), "end", seqGet(args, 1), "step", step)
		},
		"GENERATE_UUID": uuid,
		"GLOB": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			return exp.New(exp.Glob, "this", seqGet(args, 1), "expression", seqGet(args, 0))
		},
		"GREATEST": func(args []*exp.Expr, d *Dialect) *exp.Expr {
			return exp.New(exp.Greatest, "this", seqGet(args, 0), "expressions", restOf(args), "ignore_nulls", d.LeastGreatestIgnoresNulls)
		},
		"LEAST": func(args []*exp.Expr, d *Dialect) *exp.Expr {
			return exp.New(exp.Least, "this", seqGet(args, 0), "expressions", restOf(args), "ignore_nulls", d.LeastGreatestIgnoresNulls)
		},
		"HEX":                    BuildHex,
		"JSON_EXTRACT":           BuildExtractJSONWithPath(exp.JSONExtract),
		"JSON_EXTRACT_SCALAR":    BuildExtractJSONWithPath(exp.JSONExtractScalar),
		"JSON_EXTRACT_PATH_TEXT": BuildExtractJSONWithPath(exp.JSONExtractScalar),
		"JSON_KEYS": func(args []*exp.Expr, d *Dialect) *exp.Expr {
			return exp.New(exp.JSONKeys, "this", seqGet(args, 0), "expression", d.ToJSONPath(seqGet(args, 1)))
		},
		"LIKE": BuildLike,
		"LOG":  BuildLogarithm,
		"LOG2": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			return exp.New(exp.Log, "this", exp.LiteralNumber(2), "expression", seqGet(args, 0))
		},
		"LOG10": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			return exp.New(exp.Log, "this", exp.LiteralNumber(10), "expression", seqGet(args, 0))
		},
		"LOWER":    BuildLower,
		"LPAD":     func(args []*exp.Expr, _ *Dialect) *exp.Expr { return BuildPad(args, true) },
		"LEFTPAD":  func(args []*exp.Expr, _ *Dialect) *exp.Expr { return BuildPad(args, true) },
		"LTRIM":    func(args []*exp.Expr, _ *Dialect) *exp.Expr { return BuildTrim(args, true, false) },
		"MOD":      BuildMod,
		"RIGHTPAD": func(args []*exp.Expr, _ *Dialect) *exp.Expr { return BuildPad(args, false) },
		"RPAD":     func(args []*exp.Expr, _ *Dialect) *exp.Expr { return BuildPad(args, false) },
		"RTRIM":    func(args []*exp.Expr, _ *Dialect) *exp.Expr { return BuildTrim(args, false, false) },
		"SCOPE_RESOLUTION": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			if len(args) != 2 {
				return exp.New(exp.ScopeResolution, "expression", seqGet(args, 0))
			}
			return exp.New(exp.ScopeResolution, "this", seqGet(args, 0), "expression", seqGet(args, 1))
		},
		"STRPOS":           FromArgList(exp.StrPosition),
		"CHARINDEX":        BuildLocateStrposition,
		"INSTR":            FromArgList(exp.StrPosition),
		"LOCATE":           BuildLocateStrposition,
		"TIME_TO_TIME_STR": func(args []*exp.Expr, _ *Dialect) *exp.Expr { return castToText(seqGet(args, 0)) },
		"TO_HEX":           BuildHex,
		"TS_OR_DS_TO_DATE_STR": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			return exp.New(exp.Substring, "this", castToText(seqGet(args, 0)), "start", exp.LiteralNumber(1), "length", exp.LiteralNumber(10))
		},
		"UNNEST": func(args []*exp.Expr, _ *Dialect) *exp.Expr {
			return exp.New(exp.Unnest, "expressions", nonNilList(args))
		},
		"UPPER": BuildUpper,
		"UUID":  uuid,
		"UUID_STRING": func(args []*exp.Expr, d *Dialect) *exp.Expr {
			return exp.New(exp.Uuid, "this", seqGet(args, 0), "name", seqGet(args, 1), "is_string", orNilBool(d.UuidIsStringType))
		},
		"VAR_MAP": BuildVarMap,
	}
	for k, v := range fns {
		c.Functions[k] = v
	}

	// LAMBDAS
	c.Lambdas = map[tokens.TokenType]LambdaParser{
		tokens.Arrow: func(p *Parser, expressions []*exp.Expr) *exp.Expr {
			return p.Expr(exp.Lambda, "this", p.ReplaceLambda(p.ParseDisjunction(), expressions), "expressions", nonNilList(expressions))
		},
		tokens.Farrow: func(p *Parser, expressions []*exp.Expr) *exp.Expr {
			e := p.ParseDisjunction()
			if e == nil {
				e = p.ParseSelect(SelectOpts{})
			}
			return p.Expr(exp.Kwarg, "this", exp.NewVar(expressions[0].Name()), "expression", e)
		},
	}

	// COLUMN_OPERATORS
	c.ColumnOperators = map[tokens.TokenType]ColumnOperator{
		tokens.Dot: nil,
		tokens.Dotcolon: func(p *Parser, this, to *exp.Expr) *exp.Expr {
			return p.Expr(exp.JSONCast, "this", this, "to", to)
		},
		tokens.Dcolon: func(p *Parser, this, to *exp.Expr) *exp.Expr {
			return p.BuildCast(p.C.StrictCast, "this", this, "to", to)
		},
		tokens.Arrow:      BuildJSONExtractOp,
		tokens.Darrow:     BuildJSONExtractScalarOp,
		tokens.HashArrow:  BuildJSONBExtractOp,
		tokens.DhashArrow: BuildJSONBExtractScalarOp,
		tokens.Placeholder: func(p *Parser, this, key *exp.Expr) *exp.Expr {
			return p.Expr(exp.JSONBContainsTopKey, "this", this, "expression", key)
		},
	}
	c.JsonOperators = map[tokens.TokenType]ColumnOperator{}

	// EXPRESSION_PARSERS
	c.ExpressionParsers = map[exp.Kind]ParseFunc{
		exp.Cluster:               func(p *Parser) *exp.Expr { return p.ParseSort(exp.Cluster, tokens.ClusterBy) },
		exp.Column:                func(p *Parser) *exp.Expr { return p.ParseColumn() },
		exp.ColumnDef:             func(p *Parser) *exp.Expr { return p.ParseColumnDef(p.ParseColumn(), true) },
		exp.Condition:             func(p *Parser) *exp.Expr { return p.ParseDisjunction() },
		exp.DataType:              func(p *Parser) *exp.Expr { return p.ParseTypes(false, true, false, false) },
		exp.AnyExpression:         func(p *Parser) *exp.Expr { return p.ParseExpression() },
		exp.From:                  func(p *Parser) *exp.Expr { return p.ParseFrom(true, false, false) },
		exp.GrantPrincipal:        func(p *Parser) *exp.Expr { return p.ParseGrantPrincipal() },
		exp.GrantPrivilege:        func(p *Parser) *exp.Expr { return p.ParseGrantPrivilege() },
		exp.Group:                 func(p *Parser) *exp.Expr { return p.ParseGroup(false) },
		exp.Having:                func(p *Parser) *exp.Expr { return p.ParseHaving(false) },
		exp.Hint:                  func(p *Parser) *exp.Expr { return p.ParseHintBody() },
		exp.Identifier:            func(p *Parser) *exp.Expr { return p.ParseIdVar(true, nil) },
		exp.Join:                  func(p *Parser) *exp.Expr { return p.ParseJoin(false, false, nil) },
		exp.Lambda:                func(p *Parser) *exp.Expr { return p.ParseLambda(false) },
		exp.Lateral:               func(p *Parser) *exp.Expr { return p.ParseLateral() },
		exp.Limit:                 func(p *Parser) *exp.Expr { return p.ParseLimit(nil, false, false) },
		exp.Offset:                func(p *Parser) *exp.Expr { return p.ParseOffset(nil) },
		exp.Order:                 func(p *Parser) *exp.Expr { return p.ParseOrder(nil, false) },
		exp.Ordered:               func(p *Parser) *exp.Expr { return p.ParseOrdered(nil) },
		exp.Properties:            func(p *Parser) *exp.Expr { return p.ParseProperties(false) },
		exp.PartitionedByProperty: func(p *Parser) *exp.Expr { return p.ParsePartitionedBy() },
		exp.Qualify:               func(p *Parser) *exp.Expr { return p.ParseQualify() },
		exp.Returning:             func(p *Parser) *exp.Expr { return p.ParseReturning() },
		exp.Select:                func(p *Parser) *exp.Expr { return p.ParseSelect(SelectOpts{}) },
		exp.Sort:                  func(p *Parser) *exp.Expr { return p.ParseSort(exp.Sort, tokens.SortBy) },
		exp.Table:                 func(p *Parser) *exp.Expr { return p.ParseTableParts(false, false, false, false) },
		exp.TableAlias:            func(p *Parser) *exp.Expr { return p.ParseTableAlias(nil) },
		exp.Tuple:                 func(p *Parser) *exp.Expr { return p.ParseValue(false) },
		exp.Whens:                 func(p *Parser) *exp.Expr { return p.ParseWhenMatched() },
		exp.Where:                 func(p *Parser) *exp.Expr { return p.ParseWhere(false) },
		exp.Window:                func(p *Parser) *exp.Expr { return p.ParseNamedWindow() },
		exp.With:                  func(p *Parser) *exp.Expr { return p.ParseWith(false) },
	}

	// STATEMENT_PARSERS
	c.StatementParsers = map[tokens.TokenType]ParseFunc{
		tokens.Alter:    func(p *Parser) *exp.Expr { return p.ParseAlter() },
		tokens.Analyze:  func(p *Parser) *exp.Expr { return p.ParseAnalyze() },
		tokens.Begin:    func(p *Parser) *exp.Expr { return p.ParseTransaction() },
		tokens.Cache:    func(p *Parser) *exp.Expr { return p.ParseCache() },
		tokens.Comment:  func(p *Parser) *exp.Expr { return p.ParseComment(true) },
		tokens.Commit:   func(p *Parser) *exp.Expr { return p.ParseCommitOrRollback() },
		tokens.Copy:     func(p *Parser) *exp.Expr { return p.ParseCopy() },
		tokens.Create:   func(p *Parser) *exp.Expr { return p.ParseCreate() },
		tokens.Declare:  func(p *Parser) *exp.Expr { return p.ParseDeclare() },
		tokens.Delete:   func(p *Parser) *exp.Expr { return p.ParseDelete() },
		tokens.Desc:     func(p *Parser) *exp.Expr { return p.ParseDescribe() },
		tokens.Describe: func(p *Parser) *exp.Expr { return p.ParseDescribe() },
		tokens.Drop:     func(p *Parser) *exp.Expr { return p.ParseDrop(false, "") },
		tokens.Grant:    func(p *Parser) *exp.Expr { return p.ParseGrant() },
		tokens.Revoke:   func(p *Parser) *exp.Expr { return p.ParseRevoke() },
		tokens.Insert:   func(p *Parser) *exp.Expr { return p.ParseInsert() },
		tokens.Kill:     func(p *Parser) *exp.Expr { return p.ParseKill() },
		tokens.Load:     func(p *Parser) *exp.Expr { return p.ParseLoad() },
		tokens.Merge:    func(p *Parser) *exp.Expr { return p.ParseMerge() },
		tokens.Pivot:    func(p *Parser) *exp.Expr { return p.ParseSimplifiedPivot(false) },
		tokens.Pragma:   func(p *Parser) *exp.Expr { return p.Expr(exp.Pragma, "this", p.ParseExpression()) },
		tokens.Refresh:  func(p *Parser) *exp.Expr { return p.ParseRefresh() },
		tokens.Rollback: func(p *Parser) *exp.Expr { return p.ParseCommitOrRollback() },
		tokens.Set:      func(p *Parser) *exp.Expr { return p.ParseSet(false, false) },
		tokens.Truncate: func(p *Parser) *exp.Expr { return p.ParseTruncateTable() },
		tokens.Uncache:  func(p *Parser) *exp.Expr { return p.ParseUncache() },
		tokens.Unpivot:  func(p *Parser) *exp.Expr { return p.ParseSimplifiedPivot(true) },
		tokens.Update:   func(p *Parser) *exp.Expr { return p.ParseUpdate() },
		tokens.Use:      func(p *Parser) *exp.Expr { return p.ParseUse() },
		tokens.Semicolon: func(p *Parser) *exp.Expr {
			return exp.New(exp.Semicolon)
		},
	}

	// UNARY_PARSERS
	c.UnaryParsers = map[tokens.TokenType]ParseFunc{
		tokens.Plus:       func(p *Parser) *exp.Expr { return p.ParseUnary() },
		tokens.Not:        func(p *Parser) *exp.Expr { return p.Expr(exp.Not, "this", p.ParseEquality(true)) },
		tokens.Tilde:      func(p *Parser) *exp.Expr { return p.Expr(exp.BitwiseNot, "this", p.ParseUnary()) },
		tokens.Dash:       func(p *Parser) *exp.Expr { return p.Expr(exp.Neg, "this", p.ParseUnary()) },
		tokens.PipeSlash:  func(p *Parser) *exp.Expr { return p.Expr(exp.Sqrt, "this", p.ParseUnary()) },
		tokens.DpipeSlash: func(p *Parser) *exp.Expr { return p.Expr(exp.Cbrt, "this", p.ParseUnary()) },
	}

	// STRING_PARSERS / NUMERIC_PARSERS / PRIMARY_PARSERS
	national := func(p *Parser, t *tokens.Token) *exp.Expr {
		return p.ExpressionTok(exp.New(exp.National, "this", t.Text), t)
	}
	raw := func(p *Parser, t *tokens.Token) *exp.Expr {
		return p.ExpressionTok(exp.New(exp.RawString, "this", t.Text), t)
	}
	c.StringParsers = map[tokens.TokenType]TokenParser{
		tokens.HeredocString:     raw,
		tokens.NationalString:    national,
		tokens.NationalRawString: national,
		tokens.RawString:         raw,
		tokens.String: func(p *Parser, t *tokens.Token) *exp.Expr {
			return p.ExpressionTok(exp.New(exp.Literal, "this", t.Text, "is_string", true), t)
		},
		tokens.UnicodeString: func(p *Parser, t *tokens.Token) *exp.Expr {
			escape := AndThen(p.MatchTextSeq("UESCAPE"), p.ParseString)
			return p.ExpressionTok(exp.New(exp.UnicodeString, "this", t.Text, "escape", escape), t)
		},
	}
	c.NumericParsers = map[tokens.TokenType]TokenParser{
		tokens.BitString: func(p *Parser, t *tokens.Token) *exp.Expr {
			return p.ExpressionTok(exp.New(exp.BitString, "this", t.Text), t)
		},
		tokens.ByteString: func(p *Parser, t *tokens.Token) *exp.Expr {
			return p.ExpressionTok(exp.New(exp.ByteString, "this", t.Text, "is_bytes", orNilBool(p.D.ByteStringIsBytesType)), t)
		},
		tokens.HexString: func(p *Parser, t *tokens.Token) *exp.Expr {
			return p.ExpressionTok(exp.New(exp.HexString, "this", t.Text, "is_integer", orNilBool(p.D.HexStringIsIntegerType)), t)
		},
		tokens.Number: func(p *Parser, t *tokens.Token) *exp.Expr {
			return p.ExpressionTok(exp.New(exp.Literal, "this", t.Text, "is_string", false), t)
		},
	}
	c.PrimaryParsers = map[tokens.TokenType]TokenParser{}
	for k, v := range c.StringParsers {
		c.PrimaryParsers[k] = v
	}
	for k, v := range c.NumericParsers {
		c.PrimaryParsers[k] = v
	}
	c.PrimaryParsers[tokens.Introducer] = func(p *Parser, t *tokens.Token) *exp.Expr { return p.ParseIntroducer(t) }
	c.PrimaryParsers[tokens.Null] = func(p *Parser, _ *tokens.Token) *exp.Expr { return p.Expr(exp.Null) }
	c.PrimaryParsers[tokens.True] = func(p *Parser, _ *tokens.Token) *exp.Expr { return p.Expr(exp.Boolean, "this", true) }
	c.PrimaryParsers[tokens.False] = func(p *Parser, _ *tokens.Token) *exp.Expr { return p.Expr(exp.Boolean, "this", false) }
	c.PrimaryParsers[tokens.SessionParameter] = func(p *Parser, _ *tokens.Token) *exp.Expr { return p.ParseSessionParameter() }
	c.PrimaryParsers[tokens.Star] = func(p *Parser, _ *tokens.Token) *exp.Expr { return p.ParseStarOps() }

	// PLACEHOLDER_PARSERS
	c.PlaceholderParsers = map[tokens.TokenType]ParseFunc{
		tokens.Placeholder: func(p *Parser) *exp.Expr { return p.Expr(exp.Placeholder) },
		tokens.Parameter:   func(p *Parser) *exp.Expr { return p.ParseParameter() },
		tokens.Colon: func(p *Parser) *exp.Expr {
			if p.MatchSet(p.C.ColonPlaceholderTokens) {
				return p.Expr(exp.Placeholder, "this", p.Prev.Text)
			}
			return nil
		},
	}

	// RANGE_PARSERS
	c.RangeParsers = map[tokens.TokenType]RangeParser{
		tokens.AtGt:      BinaryRangeParser(exp.ArrayContainsAll, false),
		tokens.Between:   func(p *Parser, this *exp.Expr) *exp.Expr { return p.ParseBetween(this) },
		tokens.Glob:      BinaryRangeParser(exp.Glob, false),
		tokens.Ilike:     BinaryRangeParser(exp.ILike, false),
		tokens.In:        func(p *Parser, this *exp.Expr) *exp.Expr { return p.ParseIn(this, false) },
		tokens.Irlike:    BinaryRangeParser(exp.RegexpILike, false),
		tokens.Is:        func(p *Parser, this *exp.Expr) *exp.Expr { return p.ParseIs(this) },
		tokens.Like:      BinaryRangeParser(exp.Like, false),
		tokens.LtAt:      BinaryRangeParser(exp.ArrayContainedBy, false),
		tokens.Overlaps:  BinaryRangeParser(exp.Overlaps, false),
		tokens.Rlike:     BinaryRangeParser(exp.RegexpLike, false),
		tokens.SimilarTo: BinaryRangeParser(exp.SimilarTo, false),
		tokens.For:       func(p *Parser, this *exp.Expr) *exp.Expr { return p.ParseComprehension(this) },
		tokens.QmarkAmp:  BinaryRangeParser(exp.JSONBContainsAllTopKeys, false),
		tokens.QmarkPipe: BinaryRangeParser(exp.JSONBContainsAnyTopKeys, false),
		tokens.HashDash:  BinaryRangeParser(exp.JSONBDeleteAtPath, false),
		tokens.AtQmark:   BinaryRangeParser(exp.JSONBPathExists, false),
		tokens.Adjacent:  BinaryRangeParser(exp.Adjacent, false),
		tokens.Operator:  func(p *Parser, this *exp.Expr) *exp.Expr { return p.ParseOperator(this) },
		tokens.AmpLt:     BinaryRangeParser(exp.ExtendsLeft, false),
		tokens.AmpGt:     BinaryRangeParser(exp.ExtendsRight, false),
		tokens.CaretAt:   BinaryRangeParser(exp.StartsWith, false),
	}

	// PIPE_SYNTAX_TRANSFORM_PARSERS
	c.PipeSyntaxTransformParsers = map[string]PipeParser{
		"AGGREGATE": func(p *Parser, q *exp.Expr) *exp.Expr { return p.ParsePipeSyntaxAggregate(q) },
		"AS": func(p *Parser, q *exp.Expr) *exp.Expr {
			return p.BuildPipeCte(q, []*exp.Expr{exp.New(exp.Star)}, p.ParseTableAlias(nil))
		},
		"DISTINCT": func(p *Parser, q *exp.Expr) *exp.Expr {
			p.Advance()
			return q.Distinct(exp.WithCopy(false))
		},
		"EXTEND": func(p *Parser, q *exp.Expr) *exp.Expr { return p.ParsePipeSyntaxExtend(q) },
		"LIMIT":  func(p *Parser, q *exp.Expr) *exp.Expr { return p.ParsePipeSyntaxLimit(q) },
		"ORDER BY": func(p *Parser, q *exp.Expr) *exp.Expr {
			return q.OrderBy(p.ParseOrder(nil, false), exp.WithAppend(false), exp.WithCopy(false))
		},
		"PIVOT":       func(p *Parser, q *exp.Expr) *exp.Expr { return p.ParsePipeSyntaxPivot(q) },
		"SELECT":      func(p *Parser, q *exp.Expr) *exp.Expr { return p.ParsePipeSyntaxSelect(q) },
		"TABLESAMPLE": func(p *Parser, q *exp.Expr) *exp.Expr { return p.ParsePipeSyntaxTablesample(q) },
		"UNPIVOT":     func(p *Parser, q *exp.Expr) *exp.Expr { return p.ParsePipeSyntaxPivot(q) },
		"WHERE": func(p *Parser, q *exp.Expr) *exp.Expr {
			return q.Where(p.ParseWhere(false), exp.WithCopy(false))
		},
	}

	// PROPERTY_PARSERS
	assign := func(kind exp.Kind) PropertyParser {
		return PPE(func(p *Parser) *exp.Expr { return p.ParsePropertyAssignment(kind) })
	}
	expr0 := func(kind exp.Kind, kv ...any) PropertyParser {
		return PPE(func(p *Parser) *exp.Expr { return p.Expr(kind, kv...) })
	}
	stability := func(s string) PropertyParser {
		return PPE(func(p *Parser) *exp.Expr { return p.Expr(exp.StabilityProperty, "this", exp.LiteralString(s)) })
	}
	kwBool := func(kw map[string]any, key string) bool { return exp.Truthy(kw[key]) }
	c.PropertyParsers = map[string]PropertyParser{
		"ALLOWED_VALUES": PPE(func(p *Parser) *exp.Expr {
			return p.Expr(exp.AllowedValuesProperty, "expressions", nonNilList(p.ParseCSV(p.ParsePrimary)))
		}),
		"ALGORITHM":      assign(exp.AlgorithmProperty),
		"AUTO":           PPE(func(p *Parser) *exp.Expr { return p.ParseAutoProperty() }),
		"AUTO_INCREMENT": assign(exp.AutoIncrementProperty),
		"BACKUP": PPE(func(p *Parser) *exp.Expr {
			return p.Expr(exp.BackupProperty, "this", p.ParseVar(true, nil, false))
		}),
		"BLOCKCOMPRESSION": PPE(func(p *Parser) *exp.Expr { return p.ParseBlockcompression() }),
		"CALLED":           PPE(func(p *Parser) *exp.Expr { return p.ParseCalledOnNullInputProperty() }),
		"CHARSET": PPK(func(p *Parser, kw map[string]any) any {
			return nilIfNil(p.ParseCharacterSet(kwBool(kw, "default")))
		}),
		"CHECKSUM":   PPE(func(p *Parser) *exp.Expr { return p.ParseChecksum() }),
		"CLUSTER BY": PPE(func(p *Parser) *exp.Expr { return p.ParseClusterProperty() }),
		"CLUSTERED":  PPE(func(p *Parser) *exp.Expr { return p.ParseClusteredBy() }),
		"COLLATE": PPK(func(p *Parser, kw map[string]any) any {
			return nilIfNil(p.ParsePropertyAssignment(exp.CollateProperty, kwToKV(kw)...))
		}),
		"COMMENT":  assign(exp.SchemaCommentProperty),
		"CONTAINS": PPE(func(p *Parser) *exp.Expr { return p.ParseContainsProperty() }),
		"COPY":     PPE(func(p *Parser) *exp.Expr { return p.ParseCopyProperty() }),
		"DATABLOCKSIZE": PPK(func(p *Parser, kw map[string]any) any {
			return nilIfNil(p.ParseDatablocksize(kw["default"], kw["minimum"], kw["maximum"]))
		}),
		"DATA_DELETION": PPE(func(p *Parser) *exp.Expr { return p.ParseDataDeletionProperty() }),
		"DEFINER":       PPE(func(p *Parser) *exp.Expr { return p.ParseDefiner() }),
		"DETERMINISTIC": stability("IMMUTABLE"),
		"DISTRIBUTED":   PPE(func(p *Parser) *exp.Expr { return p.ParseDistributedProperty() }),
		"DUPLICATE":     PPE(func(p *Parser) *exp.Expr { return p.ParseCompositeKeyProperty(exp.DuplicateKeyProperty) }),
		"DYNAMIC":       expr0(exp.DynamicProperty),
		"DISTKEY":       PPE(func(p *Parser) *exp.Expr { return p.ParseDistkey() }),
		"DISTSTYLE":     assign(exp.DistStyleProperty),
		"EMPTY":         expr0(exp.EmptyProperty),
		"ENGINE":        assign(exp.EngineProperty),
		"ENVIRONMENT": PPE(func(p *Parser) *exp.Expr {
			return p.Expr(exp.EnviromentProperty, "expressions", p.ParseWrappedCSV(p.ParseAssignment, false))
		}),
		"HANDLER":  assign(exp.HandlerProperty),
		"EXECUTE":  assign(exp.ExecuteAsProperty),
		"EXTERNAL": expr0(exp.ExternalProperty),
		"FALLBACK": PPK(func(p *Parser, kw map[string]any) any {
			return nilIfNil(p.ParseFallback(kwBool(kw, "no")))
		}),
		"FORMAT":    assign(exp.FileFormatProperty),
		"FREESPACE": PPE(func(p *Parser) *exp.Expr { return p.ParseFreespace() }),
		"GLOBAL":    expr0(exp.GlobalProperty),
		"HEAP":      expr0(exp.HeapProperty),
		"ICEBERG":   expr0(exp.IcebergProperty),
		"IMMUTABLE": stability("IMMUTABLE"),
		"INHERITS": PPE(func(p *Parser) *exp.Expr {
			return p.Expr(exp.InheritsProperty, "expressions", p.ParseWrappedCSV(func() *exp.Expr { return p.ParseTable(TableOpts{}) }, false))
		}),
		"INPUT": PPE(func(p *Parser) *exp.Expr { return p.Expr(exp.InputModelProperty, "this", p.ParseSchema(nil)) }),
		"JOURNAL": PPK(func(p *Parser, kw map[string]any) any {
			return nilIfNil(p.ParseJournal(kwToKV(kw)...))
		}),
		"LANGUAGE": assign(exp.LanguageProperty),
		"LAYOUT":   PPE(func(p *Parser) *exp.Expr { return p.ParseDictProperty("LAYOUT") }),
		"LIFETIME": PPE(func(p *Parser) *exp.Expr { return p.ParseDictRange("LIFETIME") }),
		"LIKE":     PPE(func(p *Parser) *exp.Expr { return p.ParseCreateLike() }),
		"LOCATION": assign(exp.LocationProperty),
		"LOCK":     PPE(func(p *Parser) *exp.Expr { return p.ParseLocking() }),
		"LOCKING":  PPE(func(p *Parser) *exp.Expr { return p.ParseLocking() }),
		"LOG": PPK(func(p *Parser, kw map[string]any) any {
			return nilIfNil(p.ParseLog(kwBool(kw, "no")))
		}),
		"MATERIALIZED": expr0(exp.MaterializedProperty),
		"MERGEBLOCKRATIO": PPK(func(p *Parser, kw map[string]any) any {
			return nilIfNil(p.ParseMergeblockratio(kwBool(kw, "no"), kwBool(kw, "default")))
		}),
		"MODIFIES":       PPE(func(p *Parser) *exp.Expr { return p.ParseModifiesProperty() }),
		"MULTISET":       expr0(exp.SetProperty, "multi", true),
		"NO":             PPE(func(p *Parser) *exp.Expr { return p.ParseNoProperty() }),
		"ON":             PPE(func(p *Parser) *exp.Expr { return p.ParseOnProperty() }),
		"ORDER BY":       PPE(func(p *Parser) *exp.Expr { return p.ParseOrder(nil, true) }),
		"OUTPUT":         PPE(func(p *Parser) *exp.Expr { return p.Expr(exp.OutputModelProperty, "this", p.ParseSchema(nil)) }),
		"PARTITION":      PPE(func(p *Parser) *exp.Expr { return p.ParsePartitionedOf() }),
		"PARTITION BY":   PPE(func(p *Parser) *exp.Expr { return p.ParsePartitionedBy() }),
		"PARTITIONED BY": PPE(func(p *Parser) *exp.Expr { return p.ParsePartitionedBy() }),
		"PARTITIONED_BY": PPE(func(p *Parser) *exp.Expr { return p.ParsePartitionedBy() }),
		"PRIMARY KEY":    PPE(func(p *Parser) *exp.Expr { return p.ParsePrimaryKey(false, true, false) }),
		"RANGE":          PPE(func(p *Parser) *exp.Expr { return p.ParseDictRange("RANGE") }),
		"READS":          PPE(func(p *Parser) *exp.Expr { return p.ParseReadsProperty() }),
		"REMOTE":         PPE(func(p *Parser) *exp.Expr { return p.ParseRemoteWithConnection() }),
		"RETURNS":        PPE(func(p *Parser) *exp.Expr { return p.ParseReturns() }),
		"STRICT":         expr0(exp.StrictProperty),
		"STREAMING":      expr0(exp.StreamingTableProperty),
		"ROW":            PPE(func(p *Parser) *exp.Expr { return p.ParseRow() }),
		"ROW_FORMAT":     assign(exp.RowFormatProperty),
		"SAMPLE": PPE(func(p *Parser) *exp.Expr {
			return p.Expr(exp.SampleProperty, "this", AndThen(p.MatchTextSeq("BY"), p.ParseBitwise))
		}),
		"SECURE":            expr0(exp.SecureProperty),
		"SECURITY":          PPE(func(p *Parser) *exp.Expr { return p.ParseSqlSecurity() }),
		"SQL SECURITY":      PPE(func(p *Parser) *exp.Expr { return p.ParseSqlSecurity() }),
		"SET":               expr0(exp.SetProperty, "multi", false),
		"SETTINGS":          PPE(func(p *Parser) *exp.Expr { return p.ParseSettingsProperty() }),
		"SHARING":           assign(exp.SharingProperty),
		"SORTKEY":           PPE(func(p *Parser) *exp.Expr { return p.ParseSortkey(false) }),
		"SOURCE":            PPE(func(p *Parser) *exp.Expr { return p.ParseDictProperty("SOURCE") }),
		"STABLE":            stability("STABLE"),
		"STORED":            PPE(func(p *Parser) *exp.Expr { return p.ParseStored() }),
		"SYSTEM_VERSIONING": PPE(func(p *Parser) *exp.Expr { return p.ParseSystemVersioningProperty(false) }),
		"TBLPROPERTIES": PP(func(p *Parser) any {
			var out []*exp.Expr
			for _, x := range p.ParseWrappedProperties() {
				out = append(out, ExprList(x)...)
			}
			return nonNilList(out)
		}),
		"TEMP":      expr0(exp.TemporaryProperty),
		"TEMPORARY": expr0(exp.TemporaryProperty),
		"TO":        PPE(func(p *Parser) *exp.Expr { return p.ParseToTable() }),
		"TRANSIENT": expr0(exp.TransientProperty),
		"TRANSFORM": PPE(func(p *Parser) *exp.Expr {
			return p.Expr(exp.TransformModelProperty, "expressions", p.ParseWrappedCSV(p.ParseExpression, false))
		}),
		"TTL":      PPE(func(p *Parser) *exp.Expr { return p.ParseTtl() }),
		"USING":    assign(exp.FileFormatProperty),
		"UNLOGGED": expr0(exp.UnloggedProperty),
		"VOLATILE": PPE(func(p *Parser) *exp.Expr { return p.ParseVolatileProperty() }),
		"WITH":     PP(func(p *Parser) any { return p.ParseWithProperty() }),
	}

	// CONSTRAINT_PARSERS
	c.ConstraintParsers = map[string]ParseFunc{
		"AUTOINCREMENT":  func(p *Parser) *exp.Expr { return p.ParseAutoIncrement() },
		"AUTO_INCREMENT": func(p *Parser) *exp.Expr { return p.ParseAutoIncrement() },
		"CASESPECIFIC": func(p *Parser) *exp.Expr {
			return p.Expr(exp.CaseSpecificColumnConstraint, "not_", false)
		},
		"CHECK": func(p *Parser) *exp.Expr { return p.ParseCheckConstraint() },
		"COLLATE": func(p *Parser) *exp.Expr {
			e := p.ParseIdentifier()
			if e == nil {
				e = p.ParseColumn()
			}
			return p.Expr(exp.CollateColumnConstraint, "this", e)
		},
		"COMMENT": func(p *Parser) *exp.Expr {
			return p.Expr(exp.CommentColumnConstraint, "this", p.ParseString())
		},
		"COMPRESS": func(p *Parser) *exp.Expr { return p.ParseCompress() },
		"CLUSTERED": func(p *Parser) *exp.Expr {
			return p.Expr(exp.ClusteredColumnConstraint, "this", p.ParseWrappedCSV(func() *exp.Expr { return p.ParseOrdered(nil) }, false))
		},
		"NONCLUSTERED": func(p *Parser) *exp.Expr {
			return p.Expr(exp.NonClusteredColumnConstraint, "this", p.ParseWrappedCSV(func() *exp.Expr { return p.ParseOrdered(nil) }, false))
		},
		"DEFAULT": func(p *Parser) *exp.Expr {
			return p.Expr(exp.DefaultColumnConstraint, "this", p.ParseBitwise())
		},
		"ENCODE": func(p *Parser) *exp.Expr {
			return p.Expr(exp.EncodeColumnConstraint, "this", p.ParseVar(false, nil, false))
		},
		"EPHEMERAL": func(p *Parser) *exp.Expr {
			return p.Expr(exp.EphemeralColumnConstraint, "this", p.ParseBitwise())
		},
		"EXCLUDE": func(p *Parser) *exp.Expr {
			return p.Expr(exp.ExcludeColumnConstraint, "this", p.ParseIndexParams())
		},
		"FOREIGN KEY": func(p *Parser) *exp.Expr { return p.ParseForeignKey() },
		"FORMAT": func(p *Parser) *exp.Expr {
			return p.Expr(exp.DateFormatColumnConstraint, "this", p.ParseVarOrString(false))
		},
		"GENERATED": func(p *Parser) *exp.Expr { return p.ParseGeneratedAsIdentity() },
		"IDENTITY":  func(p *Parser) *exp.Expr { return p.ParseAutoIncrement() },
		"INLINE":    func(p *Parser) *exp.Expr { return p.ParseInline() },
		"LIKE":      func(p *Parser) *exp.Expr { return p.ParseCreateLike() },
		"NOT":       func(p *Parser) *exp.Expr { return p.ParseNotConstraint() },
		"NULL": func(p *Parser) *exp.Expr {
			return p.Expr(exp.NotNullColumnConstraint, "allow_null", true)
		},
		"ON": func(p *Parser) *exp.Expr {
			if p.Match(tokens.Update) {
				if e := p.Expr(exp.OnUpdateColumnConstraint, "this", p.ParseFunction(FunctionOpts{})); e != nil {
					return e
				}
			}
			return p.Expr(exp.OnProperty, "this", p.ParseIdVar(true, nil))
		},
		"PATH": func(p *Parser) *exp.Expr {
			return p.Expr(exp.PathColumnConstraint, "this", p.ParseString())
		},
		"PERIOD":      func(p *Parser) *exp.Expr { return p.ParsePeriodForSystemTime() },
		"PRIMARY KEY": func(p *Parser) *exp.Expr { return p.ParsePrimaryKey(false, false, false) },
		"REFERENCES":  func(p *Parser) *exp.Expr { return p.ParseReferences(false) },
		"TITLE": func(p *Parser) *exp.Expr {
			return p.Expr(exp.TitleColumnConstraint, "this", p.ParseVarOrString(false))
		},
		"TTL": func(p *Parser) *exp.Expr {
			return p.Expr(exp.MergeTreeTTL, "expressions", []*exp.Expr{p.ParseBitwise()})
		},
		"UNIQUE":    func(p *Parser) *exp.Expr { return p.ParseUnique() },
		"UPPERCASE": func(p *Parser) *exp.Expr { return p.Expr(exp.UppercaseColumnConstraint) },
		"WITH": func(p *Parser) *exp.Expr {
			var props []*exp.Expr
			for _, x := range p.ParseWrappedProperties() {
				props = append(props, ExprList(x)...)
			}
			return p.Expr(exp.Properties, "expressions", nonNilList(props))
		},
		"BUCKET":   func(p *Parser) *exp.Expr { return p.ParsePartitionedByBucketOrTruncate() },
		"TRUNCATE": func(p *Parser) *exp.Expr { return p.ParsePartitionedByBucketOrTruncate() },
	}

	// ALTER_PARSERS
	one := func(f func(p *Parser) *exp.Expr) AlterParser {
		return func(p *Parser) []*exp.Expr {
			if e := f(p); e != nil {
				return []*exp.Expr{e}
			}
			return nil
		}
	}
	c.AlterParsers = map[string]AlterParser{
		"ADD":        func(p *Parser) []*exp.Expr { return p.ParseAlterTableAdd() },
		"AS":         one(func(p *Parser) *exp.Expr { return p.ParseSelect(SelectOpts{}) }),
		"ALTER":      one(func(p *Parser) *exp.Expr { return p.ParseAlterTableAlter() }),
		"CLUSTER BY": one(func(p *Parser) *exp.Expr { return p.ParseClusterProperty() }),
		"DELETE":     one(func(p *Parser) *exp.Expr { return p.Expr(exp.Delete, "where", p.ParseWhere(false)) }),
		"DROP":       func(p *Parser) []*exp.Expr { return p.ParseAlterTableDrop() },
		"RENAME":     one(func(p *Parser) *exp.Expr { return p.ParseAlterTableRename() }),
		"SET":        one(func(p *Parser) *exp.Expr { return p.ParseAlterTableSet() }),
		"SWAP": one(func(p *Parser) *exp.Expr {
			return p.Expr(exp.SwapTable, "this", AndThen(p.Match(tokens.With), func() *exp.Expr { return p.ParseTable(TableOpts{Schema: true}) }))
		}),
	}
	c.AlterAlterParsers = map[string]ParseFunc{
		"DISTKEY":   func(p *Parser) *exp.Expr { return p.ParseAlterDiststyle() },
		"DISTSTYLE": func(p *Parser) *exp.Expr { return p.ParseAlterDiststyle() },
		"SORTKEY":   func(p *Parser) *exp.Expr { return p.ParseAlterSortkey(nil) },
		"COMPOUND":  func(p *Parser) *exp.Expr { return p.ParseAlterSortkey(true) },
	}

	// NO_PAREN_FUNCTION_PARSERS
	c.NoParenFunctionParsers = map[string]ParseFunc{
		"ANY":             func(p *Parser) *exp.Expr { return p.Expr(exp.Any, "this", p.ParseBitwise()) },
		"CASE":            func(p *Parser) *exp.Expr { return p.ParseCase() },
		"CONNECT_BY_ROOT": func(p *Parser) *exp.Expr { return p.Expr(exp.ConnectByRoot, "this", p.ParseColumn()) },
		"IF":              func(p *Parser) *exp.Expr { return p.ParseIf() },
	}

	// FUNCTION_PARSERS
	c.FunctionParsers = map[string]ParseFunc{
		"CAST":           func(p *Parser) *exp.Expr { return p.ParseCast(p.C.StrictCast, nil) },
		"CEIL":           func(p *Parser) *exp.Expr { return p.ParseCeilFloor(exp.Ceil) },
		"CONVERT":        func(p *Parser) *exp.Expr { return p.ParseConvert(p.C.StrictCast, nil) },
		"CHAR":           func(p *Parser) *exp.Expr { return p.ParseChar() },
		"CHR":            func(p *Parser) *exp.Expr { return p.ParseChar() },
		"DECODE":         func(p *Parser) *exp.Expr { return p.ParseDecode() },
		"EXTRACT":        func(p *Parser) *exp.Expr { return p.ParseExtract() },
		"FLOOR":          func(p *Parser) *exp.Expr { return p.ParseCeilFloor(exp.Floor) },
		"GAP_FILL":       func(p *Parser) *exp.Expr { return p.ParseGapFill() },
		"INITCAP":        func(p *Parser) *exp.Expr { return p.ParseInitcap() },
		"JSON_OBJECT":    func(p *Parser) *exp.Expr { return p.ParseJsonObject(false) },
		"JSON_OBJECTAGG": func(p *Parser) *exp.Expr { return p.ParseJsonObject(true) },
		"JSON_TABLE":     func(p *Parser) *exp.Expr { return p.ParseJsonTable() },
		"MATCH":          func(p *Parser) *exp.Expr { return p.ParseMatchAgainst() },
		"NORMALIZE":      func(p *Parser) *exp.Expr { return p.ParseNormalize() },
		"OPENJSON":       func(p *Parser) *exp.Expr { return p.ParseOpenJson() },
		"OVERLAY":        func(p *Parser) *exp.Expr { return p.ParseOverlay() },
		"POSITION":       func(p *Parser) *exp.Expr { return p.ParsePosition(false) },
		"SAFE_CAST":      func(p *Parser) *exp.Expr { return p.ParseCast(false, true) },
		"STRING_AGG":     func(p *Parser) *exp.Expr { return p.ParseStringAgg() },
		"SUBSTRING":      func(p *Parser) *exp.Expr { return p.ParseSubstring() },
		"TRIM":           func(p *Parser) *exp.Expr { return p.ParseTrim() },
		"TRY_CAST":       func(p *Parser) *exp.Expr { return p.ParseCast(false, true) },
		"TRY_CONVERT":    func(p *Parser) *exp.Expr { return p.ParseConvert(false, true) },
		"WEIGHT_STRING":  func(p *Parser) *exp.Expr { return p.ParseWeightString() },
		"XMLELEMENT":     func(p *Parser) *exp.Expr { return p.ParseXmlElement() },
		"XMLTABLE":       func(p *Parser) *exp.Expr { return p.ParseXmlTable() },
	}
	for _, name := range exp.ArgMax.SQLNames() {
		c.FunctionParsers[name] = func(p *Parser) *exp.Expr { return p.ParseDistinctArgFunction(exp.ArgMax, 0) }
	}
	for _, name := range exp.ArgMin.SQLNames() {
		c.FunctionParsers[name] = func(p *Parser) *exp.Expr { return p.ParseDistinctArgFunction(exp.ArgMin, 0) }
	}

	// QUERY_MODIFIER_PARSERS
	mod := func(key string, f func(p *Parser) *exp.Expr) ModifierParser {
		return func(p *Parser) (string, any) {
			e := f(p)
			if e == nil {
				return key, nil
			}
			return key, e
		}
	}
	c.QueryModifierParsers = map[tokens.TokenType]ModifierParser{
		tokens.MatchRecognize: mod("match", func(p *Parser) *exp.Expr { return p.ParseMatchRecognize() }),
		tokens.Prewhere:       mod("prewhere", func(p *Parser) *exp.Expr { return p.ParsePrewhere(false) }),
		tokens.Where:          mod("where", func(p *Parser) *exp.Expr { return p.ParseWhere(false) }),
		tokens.GroupBy:        mod("group", func(p *Parser) *exp.Expr { return p.ParseGroup(false) }),
		tokens.Having:         mod("having", func(p *Parser) *exp.Expr { return p.ParseHaving(false) }),
		tokens.Qualify:        mod("qualify", func(p *Parser) *exp.Expr { return p.ParseQualify() }),
		tokens.Window: func(p *Parser) (string, any) {
			w := p.ParseWindowClause()
			if w == nil {
				return "windows", nil
			}
			return "windows", w
		},
		tokens.OrderBy: mod("order", func(p *Parser) *exp.Expr { return p.ParseOrder(nil, false) }),
		tokens.Limit:   mod("limit", func(p *Parser) *exp.Expr { return p.ParseLimit(nil, false, false) }),
		tokens.Fetch:   mod("limit", func(p *Parser) *exp.Expr { return p.ParseLimit(nil, false, false) }),
		tokens.Offset:  mod("offset", func(p *Parser) *exp.Expr { return p.ParseOffset(nil) }),
		tokens.For: func(p *Parser) (string, any) {
			l := p.ParseLocks()
			if len(l) == 0 {
				return "locks", []*exp.Expr{}
			}
			return "locks", l
		},
		tokens.Lock: func(p *Parser) (string, any) {
			l := p.ParseLocks()
			if len(l) == 0 {
				return "locks", []*exp.Expr{}
			}
			return "locks", l
		},
		tokens.TableSample:  mod("sample", func(p *Parser) *exp.Expr { return p.ParseTableSample(true) }),
		tokens.Using:        mod("sample", func(p *Parser) *exp.Expr { return p.ParseTableSample(true) }),
		tokens.ClusterBy:    mod("cluster", func(p *Parser) *exp.Expr { return p.ParseCluster() }),
		tokens.DistributeBy: mod("distribute", func(p *Parser) *exp.Expr { return p.ParseSort(exp.Distribute, tokens.DistributeBy) }),
		tokens.SortBy:       mod("sort", func(p *Parser) *exp.Expr { return p.ParseSort(exp.Sort, tokens.SortBy) }),
		tokens.ConnectBy:    mod("connect", func(p *Parser) *exp.Expr { return p.ParseConnect(true) }),
	}

	// SET_PARSERS
	c.SetParsers = map[string]ParseFunc{
		"GLOBAL":      func(p *Parser) *exp.Expr { return p.ParseSetItemAssignment("GLOBAL") },
		"LOCAL":       func(p *Parser) *exp.Expr { return p.ParseSetItemAssignment("LOCAL") },
		"SESSION":     func(p *Parser) *exp.Expr { return p.ParseSetItemAssignment("SESSION") },
		"TRANSACTION": func(p *Parser) *exp.Expr { return p.ParseSetTransaction(false) },
	}
	c.ShowParsers = map[string]ParseFunc{}

	// TYPE_LITERAL_PARSERS
	c.TypeLiteralParsers = map[exp.DType]TypeLiteralParser{
		exp.DTypeJson: func(p *Parser, this, _ *exp.Expr) *exp.Expr {
			return p.Expr(exp.ParseJSON, "this", this, "is_literal", true)
		},
	}
	c.TypeConverters = map[exp.DType]func(*exp.Expr) *exp.Expr{}
	c.OdbcDatetimeLiterals = map[string]exp.Kind{}
	c.Exponent = map[tokens.TokenType]exp.Kind{}
	c.ProcedureOptions = OptionsType{}

	c.ANALYZEDefaults()
	return c
}

// ANALYZEDefaults fills ANALYZE_EXPRESSION_PARSERS.
func (c *ParserConfig) ANALYZEDefaults() {
	c.AnalyzeExpressionParsers = map[string]ParseFunc{
		"ALL":      func(p *Parser) *exp.Expr { return p.ParseAnalyzeColumns() },
		"COMPUTE":  func(p *Parser) *exp.Expr { return p.ParseAnalyzeStatistics() },
		"DELETE":   func(p *Parser) *exp.Expr { return p.ParseAnalyzeDelete() },
		"DROP":     func(p *Parser) *exp.Expr { return p.ParseAnalyzeHistogram() },
		"ESTIMATE": func(p *Parser) *exp.Expr { return p.ParseAnalyzeStatistics() },
		"LIST":     func(p *Parser) *exp.Expr { return p.ParseAnalyzeList() },
		"PREDICATE": func(p *Parser) *exp.Expr {
			return p.ParseAnalyzeColumns()
		},
		"UPDATE":   func(p *Parser) *exp.Expr { return p.ParseAnalyzeHistogram() },
		"VALIDATE": func(p *Parser) *exp.Expr { return p.ParseAnalyzeValidate() },
	}
}

// finalize computes derived parser tables (SHOW_TRIE, SET_TRIE) after customization.
func (c *ParserConfig) finalize(d *DialectClass) {
	c.ShowTrie = wordTrie(c.ShowParsers)
	c.SetTrie = wordTrie(c.SetParsers)
}

func wordTrie(m map[string]ParseFunc) *trie.Node[string] {
	t := &trie.Node[string]{}
	for k := range m {
		t.Add(strings.Split(k, " "))
	}
	return t
}

func (p *Parser) parsePartitionedByBucketOrTruncate() *exp.Expr {
	if !p.Check(tokens.LParen) {
		p.Retreat(p.Index - 1)
		return nil
	}
	kind := exp.PartitionByTruncate
	if p.PrevTextUpper() == "BUCKET" {
		kind = exp.PartitionedByBucket
	}
	args := p.ParseWrappedCSV(func() *exp.Expr {
		if e := p.ParsePrimary(); e != nil {
			return e
		}
		return p.ParseColumn()
	}, false)
	this, expression := seqGet(args, 0), seqGet(args, 1)
	if this.Is(exp.Literal) {
		this, expression = expression, this
	}
	return p.Expr(kind, "this", this, "expression", expression)
}

// JSON operator builders (parser.build_json_extract & friends).

// BuildJSONExtractOp mirrors build_json_extract.
func BuildJSONExtractOp(p *Parser, this, path *exp.Expr) *exp.Expr {
	return p.Expr(exp.JSONExtract, "this", this, "expression", p.D.ToJSONPath(path), "only_json_types", p.C.JsonArrowsRequireJsonType)
}

// BuildJSONExtractScalarOp mirrors build_json_extract_scalar.
func BuildJSONExtractScalarOp(p *Parser, this, path *exp.Expr) *exp.Expr {
	return p.Expr(exp.JSONExtractScalar, "this", this, "expression", p.D.ToJSONPath(path),
		"only_json_types", p.C.JsonArrowsRequireJsonType, "scalar_only", p.D.JsonExtractScalarScalarOnly)
}

// BuildJSONBExtractOp mirrors build_jsonb_extract.
func BuildJSONBExtractOp(p *Parser, this, path *exp.Expr) *exp.Expr {
	return p.Expr(exp.JSONBExtract, "this", this, "expression", path)
}

// BuildJSONBExtractScalarOp mirrors build_jsonb_extract_scalar.
func BuildJSONBExtractScalarOp(p *Parser, this, path *exp.Expr) *exp.Expr {
	return p.Expr(exp.JSONBExtractScalar, "this", this, "expression", path)
}

// BuildJSONBContainsTopKeyOp mirrors build_jsonb_contains_top_key.
func BuildJSONBContainsTopKeyOp(p *Parser, this, key *exp.Expr) *exp.Expr {
	return p.Expr(exp.JSONBContainsTopKey, "this", this, "expression", key)
}
