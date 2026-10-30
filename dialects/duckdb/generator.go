package duckdb

// The DuckDB generator configuration (class attributes and TRANSFORMS) of
// sqlglot/generators/duckdb.py. The generator methods live in generator_methods*.go.

import (
	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
)

type gen = sqlglot.Generator

func intp(v int) *int { return &v }

func generator(c *sqlglot.GeneratorConfig) {
	c.ParameterToken = "$"
	c.NamedPlaceholderToken = "$"
	c.JoinHints = false
	c.TableHints = false
	c.QueryHints = false
	c.LimitFetch = "LIMIT"
	c.StructDelimiter = []string{"(", ")"}
	c.RenameTableWithDb = false
	c.Nvl2Supported = false
	c.SemiAntiJoinWithSide = false
	c.TablesampleKeywords = "USING SAMPLE"
	c.TablesampleSeedKeyword = "REPEATABLE"
	c.LastDaySupportsDatePart = false
	c.JsonKeyValuePairSep = ","
	c.IgnoreNullsInFunc = true
	c.IgnoreNullsBeforeOrder = false
	c.JsonPathBracketedKeySupported = false
	c.SupportsCreateTableLike = false
	c.MultiArgDistinct = false
	c.CanImplementArrayAny = true
	c.SupportsToNumber = false
	c.SupportsWindowExclude = true
	c.CopyHasIntoKeyword = false
	c.StarExcept = "EXCLUDE"
	c.PadFillPatternIsRequired = true
	c.ArraySizeDimRequired = sqlglot.Bool(false)
	c.NormalizeExtractDateParts = true
	c.SupportsLikeQuantifiers = false
	c.SetAssignmentRequiresVariableKeyword = true
	c.SupportsNearestJoin = true
	c.CastToIntRounds = true
	c.SafeIntDivision = true
	c.SelectKinds = []string{}
	c.SupportsDecodeCase = false
	c.SupportsDropAlterIcebergProperty = false
	c.HistoricalDataPostAlias = true

	c.AfterHavingModifierTransforms = append(sqlglot.AfterHavingModifierTransforms(),
		sqlglot.NamedGenFunc{Name: "sample", Fn: func(g *gen, e *exp.Expr) string { return g.SQLArg(e, "sample") }})

	transforms(c.Transforms)

	c.SupportedJsonPathParts = exp.NewKindSet(exp.JSONPathKey, exp.JSONPathRoot, exp.JSONPathSubscript, exp.JSONPathWildcard)

	for k, v := range map[exp.DType]string{
		exp.DTypeBinary:       "BLOB",
		exp.DTypeBpchar:       "TEXT",
		exp.DTypeChar:         "TEXT",
		exp.DTypeDatetime:     "TIMESTAMP",
		exp.DTypeDecfloat:     "DECIMAL",
		exp.DTypeFloat:        "REAL",
		exp.DTypeJsonb:        "JSON",
		exp.DTypeNchar:        "TEXT",
		exp.DTypeNvarchar:     "TEXT",
		exp.DTypeUint:         "UINTEGER",
		exp.DTypeVarbinary:    "BLOB",
		exp.DTypeRowversion:   "BLOB",
		exp.DTypeVarchar:      "TEXT",
		exp.DTypeTimestampltz: "TIMESTAMPTZ",
		exp.DTypeTimestampntz: "TIMESTAMP",
		exp.DTypeTimestampS:   "TIMESTAMP_S",
		exp.DTypeTimestampMs:  "TIMESTAMP_MS",
		exp.DTypeTimestampNs:  "TIMESTAMP_NS",
		exp.DTypeBigdecimal:   "DECIMAL",
	} {
		c.TypeMapping[k] = v
	}

	c.TypeParamSettings[exp.DTypeBigdecimal] = sqlglot.TypeParamSetting{Defaults: []int{38, 5}, Limits: []*int{intp(38), intp(38)}}
	c.TypeParamSettings[exp.DTypeDecfloat] = sqlglot.TypeParamSetting{Defaults: []int{38, 5}, Limits: []*int{intp(38), intp(38)}}

	c.ReservedKeywords = sqlglot.StringSet{}
	for _, k := range reservedKeywords {
		c.ReservedKeywords[k] = true
	}

	c.UnwrappedIntervalValues = exp.NewKindSet(exp.Literal, exp.Paren)

	// DuckDB doesn't generally support CREATE TABLE .. properties
	// https://duckdb.org/docs/sql/statements/create_table.html
	// There are a few exceptions (e.g. temporary tables) which are supported or
	// can be transpiled to DuckDB, so we explicitly override them accordingly
	for prop := range c.PropertiesLocation {
		c.PropertiesLocation[prop] = exp.Unsupported
	}
	c.PropertiesLocation[exp.LikeProperty] = exp.PostSchema
	c.PropertiesLocation[exp.TemporaryProperty] = exp.PostCreate
	c.PropertiesLocation[exp.ReturnsProperty] = exp.PostAlias
	c.PropertiesLocation[exp.SequenceProperties] = exp.PostExpression
	c.PropertiesLocation[exp.TriggerProperties] = exp.PostExpression
	c.PropertiesLocation[exp.IcebergProperty] = exp.PostCreate

	c.Extra["IGNORE_RESPECT_NULLS_WINDOW_FUNCTIONS"] = []exp.Kind{exp.FirstValue, exp.Lag, exp.LastValue, exp.Lead, exp.NthValue}

	methods(c)
}

func renameFunc(name string) sqlglot.GenFunc { return sqlglot.RenameFunc(name) }

func dateDeltaOp() sqlglot.GenFunc { return dateDeltaToBinaryIntervalOp(true) }

func transforms(t map[exp.Kind]sqlglot.GenFunc) {
	t[exp.AnyValue] = anyvalueSQL
	t[exp.ApproxDistinct] = sqlglot.ApproxCountDistinctSQL
	t[exp.Boolnot] = boolnotSQL
	t[exp.Booland] = boolandSQL
	t[exp.Boolor] = boolorSQL
	t[exp.Array] = sqlglot.Preprocess([]sqlglot.Transform{sqlglot.InheritStructFieldNames}, sqlglot.InlineArrayUnlessQuery)
	t[exp.ArrayAppend] = sqlglot.ArrayAppendSQL("LIST_APPEND", false)
	t[exp.ArrayCompact] = sqlglot.ArrayCompactSQL
	t[exp.ArrayConstructCompact] = func(g *gen, e *exp.Expr) string {
		return g.SQL(exp.New(exp.ArrayCompact, "this", exp.New(exp.Array, "expressions", e.Expressions())))
	}
	t[exp.ArrayConcat] = sqlglot.ArrayConcatSQL("LIST_CONCAT")
	t[exp.ArrayContains] = arrayContainsSQL
	t[exp.ArrayOverlaps] = arrayOverlapsSQL
	t[exp.ArrayFilter] = renameFunc("LIST_FILTER")
	t[exp.ArrayInsert] = arrayInsertSQL
	t[exp.ArrayPosition] = func(g *gen, e *exp.Expr) string {
		if e.Bool("zero_based") {
			return g.SQL(exp.New(exp.Sub,
				"this", exp.New(exp.ArrayPosition, "this", e.This(), "expression", e.Expression()),
				"expression", exp.LiteralNumber(1)))
		}
		return g.Func("ARRAY_POSITION", e.This(), e.Expression())
	}
	t[exp.ArrayRemoveAt] = arrayRemoveAtSQL
	t[exp.ArrayRemove] = sqlglot.RemoveFromArrayUsingFilter
	t[exp.ArraySort] = arraySortSQL
	t[exp.ArrayPrepend] = sqlglot.ArrayAppendSQL("LIST_PREPEND", true)
	t[exp.ArraySum] = renameFunc("LIST_SUM")
	t[exp.ArrayMax] = renameFunc("LIST_MAX")
	t[exp.ArrayMin] = renameFunc("LIST_MIN")
	t[exp.Base64DecodeBinary] = func(g *gen, e *exp.Expr) string { return base64DecodeSQL(g, e, false) }
	t[exp.Base64DecodeString] = func(g *gen, e *exp.Expr) string { return base64DecodeSQL(g, e, true) }
	t[exp.BitwiseAnd] = func(g *gen, e *exp.Expr) string { return bitwiseOp(g, e, "&") }
	t[exp.BitwiseAndAgg] = bitwiseAggSQL
	t[exp.BitwiseCount] = renameFunc("BIT_COUNT")
	t[exp.BitwiseLeftShift] = bitshiftSQL
	t[exp.BitwiseOr] = func(g *gen, e *exp.Expr) string { return bitwiseOp(g, e, "|") }
	t[exp.BitwiseOrAgg] = bitwiseAggSQL
	t[exp.BitwiseRightShift] = bitshiftSQL
	t[exp.BitwiseXorAgg] = bitwiseAggSQL
	t[exp.CommentColumnConstraint] = sqlglot.NoCommentColumnConstraintSQL
	t[exp.CosineDistance] = renameFunc("LIST_COSINE_DISTANCE")
	t[exp.CurrentTime] = func(g *gen, e *exp.Expr) string { return "CURRENT_TIME" }
	t[exp.CurrentSchemas] = func(g *gen, e *exp.Expr) string {
		this := e.This()
		if this == nil {
			this = exp.NewTrue()
		}
		return g.Func("current_schemas", this)
	}
	t[exp.CurrentTimestamp] = func(g *gen, e *exp.Expr) string {
		if e.Bool("sysdate") {
			return g.SQL(exp.New(exp.AtTimeZone, "this", exp.NewVar("CURRENT_TIMESTAMP"), "zone", exp.LiteralString("UTC")))
		}
		return "CURRENT_TIMESTAMP"
	}
	t[exp.CurrentVersion] = renameFunc("version")
	t[exp.Localtime] = sqlglot.UnsupportedArgs(func(g *gen, e *exp.Expr) string { return "LOCALTIME" }, "this")
	t[exp.DayOfMonth] = renameFunc("DAYOFMONTH")
	t[exp.DayOfWeek] = renameFunc("DAYOFWEEK")
	t[exp.DayOfWeekIso] = renameFunc("ISODOW")
	t[exp.DayOfYear] = renameFunc("DAYOFYEAR")
	t[exp.Dayname] = func(g *gen, e *exp.Expr) string {
		if e.Bool("abbreviated") {
			return g.Func("STRFTIME", e.This(), exp.LiteralString("%a"))
		}
		return g.Func("DAYNAME", e.This())
	}
	t[exp.Monthname] = func(g *gen, e *exp.Expr) string {
		if e.Bool("abbreviated") {
			return g.Func("STRFTIME", e.This(), exp.LiteralString("%b"))
		}
		return g.Func("MONTHNAME", e.This())
	}
	t[exp.Date] = dateSQL
	t[exp.DateAdd] = dateDeltaOp()
	t[exp.DateFromParts] = dateFromPartsSQL
	t[exp.DateSub] = dateDeltaOp()
	t[exp.DateDiff] = dateDiffSQL
	t[exp.DateStrToDate] = sqlglot.DatestrtodateSQL
	t[exp.Datetime] = sqlglot.NoDatetimeSQL
	t[exp.DatetimeDiff] = dateDiffSQL
	t[exp.DatetimeSub] = dateDeltaOp()
	t[exp.DatetimeAdd] = dateDeltaOp()
	t[exp.DateToDi] = func(g *gen, e *exp.Expr) string {
		return "CAST(STRFTIME(" + g.SQLArg(e, "this") + ", " + g.D.DateintFormat + ") AS INT)"
	}
	t[exp.Decode] = func(g *gen, e *exp.Expr) string { return sqlglot.EncodeDecodeSQL(g, e, "DECODE", false) }
	t[exp.HexDecodeString] = func(g *gen, e *exp.Expr) string {
		return g.SQL(exp.New(exp.Decode, "this", exp.New(exp.Unhex, "this", e.This())))
	}
	t[exp.DiToDate] = func(g *gen, e *exp.Expr) string {
		return "CAST(STRPTIME(CAST(" + g.SQLArg(e, "this") + " AS TEXT), " + g.D.DateintFormat + ") AS DATE)"
	}
	t[exp.Encode] = func(g *gen, e *exp.Expr) string { return sqlglot.EncodeDecodeSQL(g, e, "ENCODE", false) }
	t[exp.EqualNull] = func(g *gen, e *exp.Expr) string {
		return g.SQL(exp.New(exp.NullSafeEQ, "this", e.This(), "expression", e.Expression()))
	}
	t[exp.EuclideanDistance] = renameFunc("LIST_DISTANCE")
	t[exp.GenerateDateArray] = generateDatetimeArraySQL
	t[exp.GenerateSeries] = sqlglot.GenerateSeriesSQL("GENERATE_SERIES", "RANGE")
	t[exp.GenerateTimestampArray] = generateDatetimeArraySQL
	t[exp.Getbit] = sqlglot.GetbitSQL
	t[exp.GroupConcat] = func(g *gen, e *exp.Expr) string {
		sep := ","
		return sqlglot.GroupconcatSQL(g, e, "LISTAGG", &sep, false, false)
	}
	t[exp.Explode] = renameFunc("UNNEST")
	t[exp.IcebergProperty] = func(g *gen, e *exp.Expr) string { return "" }
	t[exp.IntDiv] = func(g *gen, e *exp.Expr) string { return g.Binary(e, "//") }
	t[exp.IsInf] = renameFunc("ISINF")
	t[exp.IsNan] = renameFunc("ISNAN")
	t[exp.IsNullValue] = func(g *gen, e *exp.Expr) string {
		return g.SQL(exp.NewFunc("JSON_TYPE", e.This()).Eq(exp.LiteralString("NULL")))
	}
	t[exp.IsArray] = func(g *gen, e *exp.Expr) string {
		return g.SQL(exp.NewFunc("JSON_TYPE", e.This()).Eq(exp.LiteralString("ARRAY")))
	}
	t[exp.Ceil] = ceilFloorSQL
	t[exp.Floor] = ceilFloorSQL
	t[exp.JSONBExists] = renameFunc("JSON_EXISTS")
	t[exp.JSONExtract] = arrowJSONExtractSQL
	t[exp.JSONExtractArray] = jsonExtractValueArraySQL
	t[exp.JSONFormat] = jsonFormatSQL
	t[exp.JSONValueArray] = jsonExtractValueArraySQL
	t[exp.Lateral] = explodeToUnnestSQL
	t[exp.LogicalOr] = func(g *gen, e *exp.Expr) string { return g.Func("BOOL_OR", castToBoolean(e.This())) }
	t[exp.LogicalAnd] = func(g *gen, e *exp.Expr) string { return g.Func("BOOL_AND", castToBoolean(e.This())) }
	t[exp.Select] = sqlglot.Preprocess([]sqlglot.Transform{ConnectByToRecursiveCte, seqToRangeInGenerator}, nil)
	t[exp.Seq1] = func(g *gen, e *exp.Expr) string { return seqSQL(g, e, 1) }
	t[exp.Seq2] = func(g *gen, e *exp.Expr) string { return seqSQL(g, e, 2) }
	t[exp.Seq4] = func(g *gen, e *exp.Expr) string { return seqSQL(g, e, 4) }
	t[exp.Seq8] = func(g *gen, e *exp.Expr) string { return seqSQL(g, e, 8) }
	t[exp.BoolxorAgg] = boolxorAggSQL
	t[exp.MakeInterval] = func(g *gen, e *exp.Expr) string { return sqlglot.NoMakeIntervalSQL(g, e, " ") }
	t[exp.Initcap] = initcapSQL
	t[exp.MD5Digest] = func(g *gen, e *exp.Expr) string { return g.Func("UNHEX", g.Func("MD5", e.This())) }
	t[exp.SHA] = func(g *gen, e *exp.Expr) string { return shaSQL(g, e, "SHA1", false) }
	t[exp.SHA1Digest] = func(g *gen, e *exp.Expr) string { return shaSQL(g, e, "SHA1", true) }
	t[exp.SHA2] = func(g *gen, e *exp.Expr) string { return shaSQL(g, e, "SHA256", false) }
	t[exp.SHA2Digest] = func(g *gen, e *exp.Expr) string { return shaSQL(g, e, "SHA256", true) }
	t[exp.MonthsBetween] = sqlglot.MonthsBetweenSQL
	t[exp.NextDay] = dayNavigationSQL
	t[exp.PercentileCont] = renameFunc("QUANTILE_CONT")
	t[exp.PercentileDisc] = renameFunc("QUANTILE_DISC")
	// DuckDB doesn't allow qualified columns inside of PIVOT expressions.
	// See: https://github.com/duckdb/duckdb/blob/671faf92411182f81dce42ac43de8bfb05d9909e/src/planner/binder/tableref/bind_pivot.cpp#L61-L62
	t[exp.Pivot] = sqlglot.Preprocess([]sqlglot.Transform{sqlglot.UnqualifyColumns}, nil)
	t[exp.PreviousDay] = dayNavigationSQL
	t[exp.RegexpILike] = func(g *gen, e *exp.Expr) string {
		return g.Func("REGEXP_MATCHES", e.This(), e.Expression(), exp.LiteralString("i"))
	}
	t[exp.RegexpSplit] = renameFunc("STR_SPLIT_REGEX")
	t[exp.RegrValx] = regrValSQL
	t[exp.RegrValy] = regrValSQL
	t[exp.Return] = func(g *gen, e *exp.Expr) string { return g.SQLArg(e, "this") }
	t[exp.ReturnsProperty] = func(g *gen, e *exp.Expr) string {
		if e.This().IsKind(exp.Schema) {
			return "TABLE"
		}
		return ""
	}
	t[exp.StrToUnix] = func(g *gen, e *exp.Expr) string {
		return g.Func("EPOCH", g.Func("STRPTIME", e.This(), g.FormatTime(e, nil, nil)))
	}
	t[exp.Struct] = structSQL
	t[exp.Transform] = renameFunc("LIST_TRANSFORM")
	t[exp.TimeAdd] = dateDeltaOp()
	t[exp.TimeSub] = dateDeltaOp()
	t[exp.Time] = sqlglot.NoTimeSQL
	t[exp.TimeDiff] = timediffSQL
	t[exp.Timestamp] = sqlglot.NoTimestampSQL
	t[exp.TimestampAdd] = dateDeltaOp()
	t[exp.TimestampDiff] = func(g *gen, e *exp.Expr) string {
		return g.Func("DATE_DIFF", exp.LiteralString(e.Text("unit")), e.Expression(), e.This())
	}
	t[exp.TimestampSub] = dateDeltaOp()
	t[exp.TimeStrToDate] = func(g *gen, e *exp.Expr) string { return g.SQL(exp.NewCast(e.This(), exp.DTypeDate)) }
	t[exp.TimeStrToTime] = func(g *gen, e *exp.Expr) string { return sqlglot.TimestrtotimeSQL(g, e, false) }
	t[exp.TimeStrToUnix] = func(g *gen, e *exp.Expr) string {
		return g.Func("EPOCH", exp.NewCast(e.This(), exp.DTypeTimestamp))
	}
	t[exp.TimeToStr] = func(g *gen, e *exp.Expr) string { return g.Func("STRFTIME", e.This(), g.FormatTime(e, nil, nil)) }
	t[exp.ToBoolean] = toBooleanSQL
	t[exp.ToVariant] = func(g *gen, e *exp.Expr) string {
		return g.SQL(exp.NewCast(e.This(), exp.MustDataTypeBuild("VARIANT", "duckdb", false)))
	}
	t[exp.TimeToUnix] = renameFunc("EPOCH")
	t[exp.TsOrDiToDi] = func(g *gen, e *exp.Expr) string {
		return "CAST(SUBSTR(REPLACE(CAST(" + g.SQLArg(e, "this") + " AS TEXT), '-', ''), 1, 8) AS INT)"
	}
	t[exp.TsOrDsAdd] = dateDeltaOp()
	t[exp.TsOrDsDiff] = func(g *gen, e *exp.Expr) string {
		unit := e.Text("unit")
		if unit == "" {
			unit = "DAY"
		}
		return g.Func("DATE_DIFF", exp.LiteralString(unit),
			exp.NewCast(e.Expression(), exp.DTypeTimestamp), exp.NewCast(e.This(), exp.DTypeTimestamp))
	}
	t[exp.UnixMicros] = func(g *gen, e *exp.Expr) string {
		return g.Func("EPOCH_US", implicitDatetimeCast(e.This(), exp.DTypeDate))
	}
	t[exp.UnixMillis] = func(g *gen, e *exp.Expr) string {
		return g.Func("EPOCH_MS", implicitDatetimeCast(e.This(), exp.DTypeDate))
	}
	t[exp.UnixSeconds] = func(g *gen, e *exp.Expr) string {
		return g.SQL(exp.NewCast(g.Func("EPOCH", implicitDatetimeCast(e.This(), exp.DTypeDate)), exp.DTypeBigint))
	}
	t[exp.UnixToStr] = func(g *gen, e *exp.Expr) string {
		return g.Func("STRFTIME", g.Func("TO_TIMESTAMP", e.This()), g.FormatTime(e, nil, nil))
	}
	t[exp.UnixToTime] = unixToTimeSQL
	t[exp.UnixToTimeStr] = func(g *gen, e *exp.Expr) string { return "CAST(TO_TIMESTAMP(" + g.SQLArg(e, "this") + ") AS TEXT)" }
	t[exp.VariancePop] = renameFunc("VAR_POP")
	t[exp.WeekOfYear] = renameFunc("WEEKOFYEAR")
	t[exp.YearOfWeek] = func(g *gen, e *exp.Expr) string {
		return g.SQL(exp.New(exp.Extract, "this", exp.NewVar("ISOYEAR"), "expression", e.This()))
	}
	t[exp.YearOfWeekIso] = func(g *gen, e *exp.Expr) string {
		return g.SQL(exp.New(exp.Extract, "this", exp.NewVar("ISOYEAR"), "expression", e.This()))
	}
	t[exp.Xor] = xorSQL
	t[exp.JSONObjectAgg] = renameFunc("JSON_GROUP_OBJECT")
	t[exp.JSONArray] = jsonConstructorSQL
	t[exp.JSONObject] = jsonConstructorSQL
	t[exp.JSONBObjectAgg] = renameFunc("JSON_GROUP_OBJECT")
	t[exp.DateBin] = renameFunc("TIME_BUCKET")
	t[exp.LastDay] = lastDaySQL
}

// https://github.com/duckdb/duckdb/blob/ff7f24fd8e3128d94371827523dae85ebaf58713/third_party/libpg_query/grammar/keywords/reserved_keywords.list#L1-L77
var reservedKeywords = []string{
	"array", "analyse", "union", "all", "when", "in", "default", "create", "window", "asymmetric",
	"to", "else", "localtime", "from", "end", "select", "current_date", "foreign", "with", "grant",
	"session_user", "or", "except", "references", "fetch", "limit", "group", "leading", "into",
	"collate", "offset", "do", "then", "localtimestamp", "check", "lateral", "current_role", "where",
	"asc", "placing", "desc", "user", "unique", "initially", "column", "both", "some", "as", "any",
	"only", "deferrable", "null", "current_time", "true", "table", "case", "trailing", "variadic",
	"for", "on", "distinct", "false", "not", "constraint", "current_timestamp", "returning",
	"primary", "intersect", "having", "analyze", "current_user", "and", "cast", "symmetric", "using",
	"order", "current_catalog", "describe", "lambda", "pivot", "pivot_longer", "pivot_wider",
	"qualify", "show", "summarize", "unpivot", "anti", "asof", "at", "authorization", "binary", "by",
	"collation", "columns", "concurrently", "cross", "freeze", "full", "generated", "glob", "ilike",
	"inner", "is", "isnull", "join", "left", "like", "map", "natural", "notnull", "outer", "overlaps",
	"positional", "right", "semi", "similar", "struct", "tablesample", "try_cast", "tuple", "unpack",
	"verbose",
}
