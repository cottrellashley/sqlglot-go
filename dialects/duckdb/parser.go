package duckdb

// Port of sqlglot/parsers/duckdb.py.

import (
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/internal/ghelp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func seqGet(xs []*exp.Expr, i int) *exp.Expr {
	if i < 0 {
		i += len(xs)
	}
	if i < 0 || i >= len(xs) {
		return nil
	}
	return xs[i]
}

// buildListSort mirrors _build_list_sort(name, desc=False). Orders are case-insensitive
// strings, e.g. LIST_SORT(x, 'desc', 'nulls first'), and NULLs default to last.
func buildListSort(name string, desc bool) sqlglot.FuncBuilder {
	return func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
		var orders []*exp.Expr
		if desc {
			orders = append([]*exp.Expr{exp.LiteralString("DESC")}, args[min(1, len(args)):]...)
		} else if len(args) > 1 {
			orders = args[1:]
		}
		orderName, ok1 := listSortOrderName(seqGet(orders, 0), "ASC")
		nullOrderName, ok2 := listSortOrderName(seqGet(orders, 1), "NULLS LAST")
		valid := ok1 && ok2 && (orderName == "ASC" || orderName == "DESC") &&
			(nullOrderName == "NULLS FIRST" || nullOrderName == "NULLS LAST")
		if !valid {
			return exp.New(exp.Anonymous, "this", name, "expressions", args)
		}
		var asc any
		if orderName == "DESC" {
			asc = exp.NewFalse()
		}
		return exp.New(exp.ArraySort,
			"this", seqGet(args, 0),
			"asc", asc,
			"nulls_first", exp.NewBoolean(nullOrderName == "NULLS FIRST"))
	}
}

func listSortOrderName(order *exp.Expr, def string) (string, bool) {
	if order == nil {
		return def, true
	}
	if order.IsString() {
		return strings.ToUpper(order.Name()), true
	}
	return "", false
}

func buildArrayPrepend(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	return exp.New(exp.ArrayPrepend, "this", seqGet(args, 1), "expression", seqGet(args, 0))
}

func buildDateDiff(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	unit := seqGet(args, 0)
	// DATE_DIFF counts the unit boundaries crossed, e.g. one month from 2026-01-31 to 2026-02-01,
	// except for WEEK, which counts whole 7-day spans
	var boundary any
	if unit.IsAny(exp.Literal, exp.Var) {
		if n := strings.ToUpper(unit.Name()); n != "WEEK" && n != "WEEKS" && n != "W" {
			boundary = true
		}
	}
	return exp.New(exp.DateDiff, "this", seqGet(args, 2), "expression", seqGet(args, 1), "unit", unit,
		"date_part_boundary", boundary)
}

func buildGenerateSeries(endExclusive bool) sqlglot.FuncBuilder {
	return func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
		// Check https://duckdb.org/docs/sql/functions/nested.html#range-functions
		if len(args) == 1 {
			// DuckDB uses 0 as a default for the series' start when it's omitted
			args = append([]*exp.Expr{exp.LiteralNumber("0")}, args...)
		}
		genSeries := exp.FromArgList(exp.GenerateSeries, args)
		genSeries.Set("is_end_exclusive", endExclusive)
		return genSeries
	}
}

func buildMakeTimestamp(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
	if len(args) == 1 {
		return exp.New(exp.UnixToTime, "this", seqGet(args, 0), "scale", exp.UnixToTimeMicros())
	}
	return exp.New(exp.TimestampFromParts,
		"year", seqGet(args, 0),
		"month", seqGet(args, 1),
		"day", seqGet(args, 2),
		"hour", seqGet(args, 3),
		"min", seqGet(args, 4),
		"sec", seqGet(args, 5))
}

func convertTextType(dtype *exp.Expr) *exp.Expr {
	dtype.Set("expressions", nil)
	return dtype
}

var (
	parseRegexpLike      = sqlglot.BinaryRangeParser(exp.RegexpLike, false)
	parseRegexpFullMatch = sqlglot.BinaryRangeParser(exp.RegexpFullMatch, false)
)

func parser(c *sqlglot.ParserConfig) {
	c.IsPredicatePrecedence = sqlglot.IsPredicatePrecedenceLow
	c.MapKeysAreArbitraryExpressions = true
	c.PivotColumnNaming = "agg_name_if_aliased_or_multiple"
	c.SupportsDirectedJoin = false
	c.JoinKinds = c.JoinKinds.Without(tokens.StraightJoin)

	c.NoParenFunctions[tokens.Localtime] = exp.Localtime
	c.NoParenFunctions[tokens.Localtimestamp] = exp.Localtimestamp
	c.NoParenFunctions[tokens.CurrentCatalog] = exp.CurrentCatalog
	c.NoParenFunctions[tokens.SessionUser] = exp.SessionUser

	delete(c.Bitwise, tokens.Caret)
	delete(c.ColumnOperators, tokens.Arrow)
	delete(c.ColumnOperators, tokens.Darrow)
	c.JsonOperators = map[tokens.TokenType]sqlglot.ColumnOperator{
		tokens.Arrow:  sqlglot.BuildJSONExtractOp,
		tokens.Darrow: sqlglot.BuildJSONExtractScalarOp,
	}

	c.RangeParsers[tokens.Damp] = sqlglot.BinaryRangeParser(exp.ArrayOverlaps, false)
	c.RangeParsers[tokens.Tilde] = parseTilde

	c.Exponent[tokens.Caret] = exp.Pow
	c.Exponent[tokens.Dstar] = exp.Pow

	c.FunctionsWithAliasedArgs = c.FunctionsWithAliasedArgs.With("STRUCT_PACK")

	c.ShowParsers = map[string]sqlglot.ParseFunc{
		"TABLES":     func(p *sqlglot.Parser) *exp.Expr { return parseShowDuckDB(p, "TABLES") },
		"ALL TABLES": func(p *sqlglot.Parser) *exp.Expr { return parseShowDuckDB(p, "ALL TABLES") },
	}

	delete(c.Functions, "DATE_SUB")
	delete(c.Functions, "GLOB")
	arrayDistinct := func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
		return exp.New(exp.ArrayDistinct, "this", seqGet(args, 0), "check_null", false)
	}
	for k, v := range map[string]sqlglot.FuncBuilder{
		"ARRAY_DISTINCT": arrayDistinct,
		"LIST_DISTINCT":  arrayDistinct,
		"ANY_VALUE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.IgnoreNulls, "this", exp.FromArgList(exp.AnyValue, args))
		},
		"ARRAY_PREPEND":      buildArrayPrepend,
		"ARRAY_REVERSE_SORT": buildListSort("ARRAY_REVERSE_SORT", true),
		"ARRAY_INTERSECT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ArrayIntersect, "expressions", args)
		},
		"ARRAY_SORT": buildListSort("ARRAY_SORT", false),
		"ARRAY_SLICE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.ArraySlice, "this", seqGet(args, 0), "start", seqGet(args, 1), "end", seqGet(args, 2),
				"step", seqGet(args, 3), "is_length", false)
		},
		"BIT_AND":                sqlglot.FromArgList(exp.BitwiseAndAgg),
		"BIT_OR":                 sqlglot.FromArgList(exp.BitwiseOrAgg),
		"BIT_XOR":                sqlglot.FromArgList(exp.BitwiseXorAgg),
		"CURRENT_LOCALTIMESTAMP": sqlglot.FromArgList(exp.Localtimestamp),
		"DATEDIFF":               buildDateDiff,
		"DATE_DIFF":              buildDateDiff,
		"DATE_TRUNC":             sqlglot.DateTruncToTime,
		"DATETRUNC":              sqlglot.DateTruncToTime,
		"DECODE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Decode, "this", seqGet(args, 0), "charset", exp.LiteralString("utf-8"))
		},
		"EDITDIST3": sqlglot.FromArgList(exp.Levenshtein),
		"ENCODE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Encode, "this", seqGet(args, 0), "charset", exp.LiteralString("utf-8"))
		},
		"EPOCH": sqlglot.FromArgList(exp.TimeToUnix),
		"EPOCH_MS": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.UnixToTime, "this", seqGet(args, 0), "scale", exp.UnixToTimeMillis())
		},
		"FROM_HEX":         sqlglot.FromArgList(exp.Unhex),
		"GENERATE_SERIES":  buildGenerateSeries(false),
		"GET_CURRENT_TIME": sqlglot.FromArgList(exp.CurrentTime),
		"GET_BIT": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Getbit, "this", seqGet(args, 0), "expression", seqGet(args, 1), "zero_is_msb", true)
		},
		"JARO_WINKLER_SIMILARITY": sqlglot.FromArgList(exp.JarowinklerSimilarity),
		"JSON":                    sqlglot.FromArgList(exp.ParseJSON),
		"JSON_ARRAY": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.JSONArray, "expressions", args)
		},
		"JSON_EXTRACT_PATH":    sqlglot.BuildExtractJSONWithPath(exp.JSONExtract),
		"JSON_EXTRACT_STRING":  sqlglot.BuildExtractJSONWithPath(exp.JSONExtractScalar),
		"LIST":                 sqlglot.FromArgList(exp.ArrayAgg),
		"LIST_APPEND":          sqlglot.FromArgList(exp.ArrayAppend),
		"LIST_CONCAT":          sqlglot.BuildArrayConcat,
		"LIST_CONTAINS":        sqlglot.FromArgList(exp.ArrayContains),
		"LIST_COSINE_DISTANCE": sqlglot.FromArgList(exp.CosineDistance),
		"LIST_DISTANCE":        sqlglot.FromArgList(exp.EuclideanDistance),
		"LIST_FILTER":          sqlglot.FromArgList(exp.ArrayFilter),
		"LIST_HAS":             sqlglot.FromArgList(exp.ArrayContains),
		"LIST_HAS_ANY":         sqlglot.FromArgList(exp.ArrayOverlaps),
		"LIST_MAX":             sqlglot.FromArgList(exp.ArrayMax),
		"LIST_MIN":             sqlglot.FromArgList(exp.ArrayMin),
		"LIST_PREPEND":         buildArrayPrepend,
		"LIST_REVERSE_SORT":    buildListSort("LIST_REVERSE_SORT", true),
		"LIST_SORT":            buildListSort("LIST_SORT", false),
		"LIST_TRANSFORM":       sqlglot.FromArgList(exp.Transform),
		"LIST_VALUE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.Array, "expressions", args, "value_constructor", len(args) == 1 && args[0].Is(exp.Query))
		},
		"MAKE_DATE":          sqlglot.FromArgList(exp.DateFromParts),
		"MAKE_TIME":          sqlglot.FromArgList(exp.TimeFromParts),
		"MAKE_TIMESTAMP":     buildMakeTimestamp,
		"QUANTILE_CONT":      sqlglot.FromArgList(exp.PercentileCont),
		"QUANTILE_DISC":      sqlglot.FromArgList(exp.PercentileDisc),
		"RANGE":              buildGenerateSeries(true),
		"REGEXP_EXTRACT":     sqlglot.BuildRegexpExtract(exp.RegexpExtract),
		"REGEXP_EXTRACT_ALL": sqlglot.BuildRegexpExtract(exp.RegexpExtractAll),
		"REGEXP_MATCHES":     sqlglot.FromArgList(exp.RegexpLike),
		"REGEXP_REPLACE": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.RegexpReplace, "this", seqGet(args, 0), "expression", seqGet(args, 1),
				"replacement", seqGet(args, 2), "modifiers", seqGet(args, 3), "single_replace", true)
		},
		"SHA256": func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {
			return exp.New(exp.SHA2, "this", seqGet(args, 0), "length", exp.LiteralNumber(256))
		},
		"STRFTIME":           sqlglot.BuildFormattedTime(exp.TimeToStr, "", nil),
		"STRING_SPLIT":       sqlglot.FromArgList(exp.Split),
		"STRING_SPLIT_REGEX": sqlglot.FromArgList(exp.RegexpSplit),
		"STRING_TO_ARRAY":    sqlglot.FromArgList(exp.Split),
		"STRPTIME":           sqlglot.BuildFormattedTime(exp.StrToTime, "", nil),
		"STRUCT_PACK":        sqlglot.FromArgList(exp.Struct),
		"STR_SPLIT":          sqlglot.FromArgList(exp.Split),
		"STR_SPLIT_REGEX":    sqlglot.FromArgList(exp.RegexpSplit),
		"TODAY":              sqlglot.FromArgList(exp.CurrentDate),
		"TIME_BUCKET":        sqlglot.FromArgList(exp.DateBin),
		"TO_TIMESTAMP":       sqlglot.FromArgList(exp.UnixToTime),
		"UNNEST":             sqlglot.FromArgList(exp.Explode),
		"VERSION":            sqlglot.FromArgList(exp.CurrentVersion),
		"XOR":                sqlglot.BinaryFromFunction(exp.BitwiseXor),
	} {
		c.Functions[k] = v
	}

	delete(c.FunctionParsers, "DECODE")
	stringAgg := func(p *sqlglot.Parser) *exp.Expr { return p.ParseStringAgg() }
	c.FunctionParsers["GROUP_CONCAT"] = stringAgg
	c.FunctionParsers["LISTAGG"] = stringAgg
	c.FunctionParsers["STRINGAGG"] = stringAgg
	c.FunctionParsers["APPROX_QUANTILE"] = func(p *sqlglot.Parser) *exp.Expr {
		return p.ParseDistinctArgFunction(exp.ApproxQuantile, 0)
	}
	c.FunctionParsers["QUANTILE"] = func(p *sqlglot.Parser) *exp.Expr { return p.ParseDistinctArgFunction(exp.Quantile, 0) }
	c.FunctionParsers["QUANTILE_CONT"] = func(p *sqlglot.Parser) *exp.Expr {
		return p.ParseDistinctArgFunction(exp.PercentileCont, 0)
	}
	c.FunctionParsers["QUANTILE_DISC"] = func(p *sqlglot.Parser) *exp.Expr {
		return p.ParseDistinctArgFunction(exp.PercentileDisc, 0)
	}

	c.NoParenFunctionParsers["MAP"] = parseMap
	c.NoParenFunctionParsers["@"] = func(p *sqlglot.Parser) *exp.Expr {
		return exp.New(exp.Abs, "this", p.ParseBitwise())
	}

	c.PlaceholderParsers[tokens.Parameter] = func(p *sqlglot.Parser) *exp.Expr {
		if p.Match(tokens.Number) || p.MatchSet(p.C.IdVarTokens) {
			var this any
			if p.Prev.Type == tokens.Identifier {
				this = p.IdentifierExpressionQuoted(nil, true)
			} else {
				this = p.Prev.Text
			}
			return p.Expr(exp.Placeholder, "this", this)
		}
		return nil
	}

	c.TypeConverters = map[exp.DType]func(dt *exp.Expr) *exp.Expr{
		// https://duckdb.org/docs/sql/data_types/numeric
		exp.DTypeDecimal: sqlglot.BuildDefaultDecimalType(intPtr(18), intPtr(3)),
		// https://duckdb.org/docs/sql/data_types/text
		exp.DTypeText: convertTextType,
	}

	c.StatementParsers[tokens.Attach] = func(p *sqlglot.Parser) *exp.Expr { return parseAttachDetach(p, true) }
	c.StatementParsers[tokens.Connect] = func(p *sqlglot.Parser) *exp.Expr {
		this := p.ParseString()
		if this == nil {
			this = p.ParseIdVar(true, nil)
		}
		return p.Expr(exp.ConnectTo, "this", this)
	}
	c.StatementParsers[tokens.Detach] = func(p *sqlglot.Parser) *exp.Expr { return parseAttachDetach(p, false) }
	c.StatementParsers[tokens.Disconnect] = func(p *sqlglot.Parser) *exp.Expr { return p.Expr(exp.Disconnect) }
	c.StatementParsers[tokens.Force] = parseForce
	c.StatementParsers[tokens.Install] = func(p *sqlglot.Parser) *exp.Expr { return parseInstall(p, false) }
	c.StatementParsers[tokens.Show] = func(p *sqlglot.Parser) *exp.Expr { return p.ParseShow() }

	c.SetParsers["VARIABLE"] = func(p *sqlglot.Parser) *exp.Expr { return p.ParseSetItemAssignment("VARIABLE") }

	m := &c.Methods

	m.ParseTriggerExecute = func(p *sqlglot.Parser) *exp.Expr {
		body := p.TryParse(p.ParseStatement, true)
		if p.Curr.Ok() {
			return nil
		}
		return body
	}

	superFunctionProperties := m.ParseFunctionProperties
	m.ParseFunctionProperties = func(p *sqlglot.Parser) *exp.Expr {
		if p.Match(tokens.Table) {
			return exp.New(exp.Properties, "expressions", []*exp.Expr{
				exp.New(exp.ReturnsProperty, "this", exp.New(exp.Schema, "this", exp.NewVar("TABLE")), "is_table", true),
			})
		}
		return superFunctionProperties(p)
	}

	superLambda := m.ParseLambda
	m.ParseLambda = func(p *sqlglot.Parser, alias bool) *exp.Expr {
		index := p.Index
		if !p.MatchTextSeq("LAMBDA") {
			return superLambda(p, alias)
		}
		expressions := p.ParseCSV(p.ParseLambdaArg)
		if !p.Match(tokens.Colon) {
			p.Retreat(index)
			return nil
		}
		this := p.ReplaceLambda(p.ParseAssignment(), expressions)
		return p.Expr(exp.Lambda, "this", this, "expressions", expressions)
	}

	superExpression := m.ParseExpression
	m.ParseExpression = func(p *sqlglot.Parser) *exp.Expr {
		// DuckDB supports prefix aliases, e.g. foo: 1
		if p.Next.Type == tokens.Colon {
			aliasTokens := p.C.AliasTokens
			alias := p.ParseIdVar(true, &aliasTokens)
			p.Match(tokens.Colon)
			comments := append([]string{}, p.PrevComments...)
			this := p.ParseAssignment()
			if this != nil {
				// Moves the comment next to the alias in `alias: expr /* comment */`
				comments = append(comments, this.PopComments()...)
			}
			return p.ExpressionComments(exp.New(exp.Alias, "this", this, "alias", alias), comments)
		}
		return superExpression(p)
	}

	superTable := m.ParseTable
	m.ParseTable = func(p *sqlglot.Parser, o sqlglot.TableOpts) *exp.Expr {
		// DuckDB supports prefix aliases, e.g. FROM foo: bar
		var alias *exp.Expr
		var comments []string
		if p.Next.Type == tokens.Colon {
			aliasTokens := o.AliasTokens
			if aliasTokens == nil {
				t := p.C.TableAliasTokens
				aliasTokens = &t
			}
			alias = p.ParseTableAlias(aliasTokens)
			p.Match(tokens.Colon)
			comments = append([]string{}, p.PrevComments...)
		}
		table := superTable(p, sqlglot.TableOpts{
			Schema:         o.Schema,
			Joins:          o.Joins,
			AliasTokens:    o.AliasTokens,
			ParseBracket:   o.ParseBracket,
			IsDbReference:  o.IsDbReference,
			ParsePartition: o.ParsePartition,
		})
		if table != nil && alias.IsKind(exp.TableAlias) {
			// Moves the comment next to the alias in `alias: table /* comment */`
			comments = append(comments, table.PopComments()...)
			alias.Comments = append(alias.PopComments(), comments...)
			table.Set("alias", alias)
		}
		return table
	}

	superTableAlias := m.ParseTableAlias
	m.ParseTableAlias = func(p *sqlglot.Parser, aliasTokens *tokens.TokenSet) *exp.Expr {
		if (p.CheckTextSeq("NEAREST") && p.Next.Type == tokens.Number) || p.CheckTextSeq("APPROX", "NEAREST") {
			return nil
		}
		return superTableAlias(p, aliasTokens)
	}

	superJoin := m.ParseJoin
	m.ParseJoin = func(p *sqlglot.Parser, skipJoinToken, parseBracket bool, aliasTokens *tokens.TokenSet) *exp.Expr {
		var joinType *exp.Expr
		if p.MatchTextSeq("JOIN", "BY", "(", "TYPE") {
			joinType = p.ParseVar(true, nil, false)
			p.MatchRParen(nil)
			skipJoinToken = true
		}
		join := superJoin(p, skipJoinToken, parseBracket, aliasTokens)
		if join == nil {
			return join
		}
		if joinType != nil {
			parts := strings.Split(strings.TrimSuffix(strings.ToUpper(joinType.Name()), "_JOIN"), "_")
			if parts[0] == "LEFT" || parts[0] == "RIGHT" || parts[0] == "FULL" {
				join.Set("side", parts[0])
				parts = parts[1:]
			}
			kind := strings.Join(parts, " ")
			if kind == "" {
				join.Set("kind", nil)
			} else {
				join.Set("kind", kind)
			}
		}
		approximate := p.MatchTextSeq("APPROX", "NEAREST")
		if approximate || p.MatchTextSeq("NEAREST") {
			count := p.ParseNumber()
			if !p.MatchTextSeq("BY") {
				p.RaiseError("Expected BY after NEAREST count")
			}
			if !p.MatchTexts("DISTANCE", "SIMILARITY") {
				p.RaiseError("Expected DISTANCE or SIMILARITY after BY")
			}
			similarity := strings.ToUpper(p.Prev.Text) == "SIMILARITY"
			join.Set("nearest", p.Expr(exp.Nearest,
				"this", p.ParseDisjunction(),
				"count", count,
				"approximate", approximate,
				"similarity", similarity))
		}
		return join
	}

	superTableSample := m.ParseTableSample
	m.ParseTableSample = func(p *sqlglot.Parser, asModifier bool) *exp.Expr {
		// https://duckdb.org/docs/sql/samples.html
		sample := superTableSample(p, asModifier)
		if sample != nil && !sample.Bool("method") {
			if sample.Bool("size") {
				sample.Set("method", exp.NewVar("RESERVOIR"))
			} else {
				sample.Set("method", exp.NewVar("SYSTEM"))
			}
		}
		return sample
	}

	superBracket := m.ParseBracket
	m.ParseBracket = func(p *sqlglot.Parser, this *exp.Expr) *exp.Expr {
		bracket := superBracket(p, this)
		if !ghelp.VersionGE(p.D.Version, 1, 2) && bracket.IsKind(exp.Bracket) {
			// https://duckdb.org/2025/02/05/announcing-duckdb-120.html#breaking-changes
			bracket.Set("returns_list_for_maps", true)
		}
		return bracket
	}

	m.ParseStructTypes = func(p *sqlglot.Parser, typeRequired bool) *exp.Expr { return p.ParseFieldDef() }

	superPivotColumnNames := m.PivotColumnNames
	m.PivotColumnNames = func(p *sqlglot.Parser, aggregations []*exp.Expr) []string {
		if len(aggregations) == 1 {
			return superPivotColumnNames(p, aggregations)
		}
		return sqlglot.PivotColumnNames(aggregations, sqlglot.MustDialect("duckdb"))
	}

	superPrimary := m.ParsePrimary
	m.ParsePrimary = func(p *sqlglot.Parser) *exp.Expr {
		if p.MatchPair(tokens.Hash, tokens.Number) {
			return exp.New(exp.PositionalColumn, "this", exp.LiteralNumber(p.Prev.Text))
		}
		return superPrimary(p)
	}
}

func intPtr(i int) *int { return &i }

func parseTilde(p *sqlglot.Parser, this *exp.Expr) *exp.Expr {
	// ~ and !~ match anywhere in the string since DuckDB 2.0, and the whole string before it
	// https://duckdb.org/docs/2.0/sql/functions/pattern_matching
	if ghelp.VersionGE(p.D.Version, 2, 0) {
		return parseRegexpLike(p, this)
	}
	return parseRegexpFullMatch(p, this)
}

func parseMap(p *sqlglot.Parser) *exp.Expr {
	if p.Check(tokens.LBrace) {
		return p.Expr(exp.ToMap, "this", p.ParseBracket(nil))
	}
	args := p.ParseWrappedCSV(p.ParseAssignment, false)
	return p.Expr(exp.Map, "keys", seqGet(args, 0), "values", seqGet(args, 1))
}

func parseAttachDetach(p *sqlglot.Parser, isAttach bool) *exp.Expr {
	parseAttachOption := func() *exp.Expr {
		return p.Expr(exp.AttachOption,
			"this", p.ParseVar(true, nil, false),
			"expression", p.ParseField(true, nil, false))
	}
	p.Match(tokens.Database)
	exists := p.ParseExists(isAttach)
	this := p.ParseAlias(p.ParsePrimaryOrVar(), true)
	var expressions any
	if p.Check(tokens.LParen) {
		expressions = p.ParseWrappedCSV(parseAttachOption, false)
	}
	if isAttach {
		return p.Expr(exp.Attach, "this", this, "exists", exists, "expressions", expressions)
	}
	return p.Expr(exp.Detach, "this", this, "exists", exists)
}

func parseShowDuckDB(p *sqlglot.Parser, this string) *exp.Expr {
	var from any
	if p.Match(tokens.From) {
		from = p.ParseTable(sqlglot.TableOpts{Schema: true})
	}
	return p.Expr(exp.Show, "this", this, "from_", from)
}

func parseForce(p *sqlglot.Parser) *exp.Expr {
	// FORCE can only be followed by INSTALL or CHECKPOINT
	// In the case of CHECKPOINT, we fallback
	if !p.Match(tokens.Install) {
		return p.ParseAsCommand(p.Prev)
	}
	return parseInstall(p, true)
}

func parseInstall(p *sqlglot.Parser, force bool) *exp.Expr {
	this := p.ParseIdVar(true, nil)
	var from any
	if p.Match(tokens.From) {
		from = p.ParseVarOrString(false)
	}
	return p.Expr(exp.Install, "this", this, "from_", from, "force", force)
}
