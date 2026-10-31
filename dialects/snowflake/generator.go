package snowflake

// Port of sqlglot/generators/snowflake.py.

import (
	"fmt"
	"slices"
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
	"github.com/cottrellashley/sqlglot-go/internal/ghelp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

type gen = sqlglot.Generator

func buildDatediff(args []*exp.Expr) *exp.Expr {
	return exp.New(exp.DateDiff,
		"this", seqGet(args, 2),
		"expression", seqGet(args, 1),
		"unit", sqlglot.MapDatePart(seqGet(args, 0), nil),
		"date_part_boundary", true)
}

func buildDateTimeAdd(kind exp.Kind, args []*exp.Expr) *exp.Expr {
	return exp.New(kind,
		"this", seqGet(args, 2),
		"expression", seqGet(args, 1),
		"unit", sqlglot.MapDatePart(seqGet(args, 0), nil))
}

func regexpilikeSQL(g *gen, e *exp.Expr) string {
	flag := e.Text("flag")
	if !strings.Contains(flag, "i") {
		flag += "i"
	}
	return g.Func("REGEXP_LIKE", e.This(), e.Expression(), exp.LiteralString(flag))
}

// unqualifyPivotColumns unqualifies the columns referenced in UNPIVOT and in ANY ORDER BY
// <column>, which Snowflake doesn't allow to be qualified.
func unqualifyPivotColumns(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Pivot) {
		if e.Bool("unpivot") {
			e = sqlglot.UnqualifyColumns(e)
		} else {
			for _, field := range e.Fields() {
				var fieldExpr *exp.Expr
				if field != nil {
					fieldExpr = seqGet(field.Expressions(), 0)
				}
				if fieldExpr.IsKind(exp.PivotAny) {
					unqualified := sqlglot.UnqualifyColumns(fieldExpr)
					field.SetAt("expressions", unqualified, 0, true)
				}
			}
		}
	}
	return e
}

func arrayGenerateRange(args []*exp.Expr) *exp.Expr {
	return exp.New(exp.GenerateSeries,
		"start", seqGet(args, 0),
		"end", seqGet(args, 1),
		"step", seqGet(args, 2),
		"is_end_exclusive", true)
}

func unnestGenerateDateArray(unnest *exp.Expr) {
	generateDateArray := unnest.Expressions()[0]
	start, end, step := generateDateArray.Get("start"), generateDateArray.Get("end"), generateDateArray.Get("step")
	if start == nil || end == nil || !step.IsKind(exp.Interval) || step.Name() != "1" {
		return
	}
	unit := step.Get("unit")
	unnestAlias := unnest.Get("alias")
	var sequenceValueName any = "value"
	if unnestAlias != nil {
		unnestAlias = unnestAlias.Copy()
		if c := seqGet(unnestAlias.Columns(), 0); c != nil {
			sequenceValueName = c
		}
	}
	// We'll add the next sequence value to the starting date and project the result
	dateAdd := buildDateTimeAdd(exp.DateAdd, []*exp.Expr{unit, exp.NewCast(sequenceValueName, "int"), exp.NewCast(start, "date")})
	// We use DATEDIFF to compute the number of sequence values needed
	numberSequence := arrayGenerateRange([]*exp.Expr{exp.LiteralNumber(0), buildDatediff([]*exp.Expr{unit, start, end}).Plus(1)})
	unnest.Set("expressions", []*exp.Expr{numberSequence})

	unnestParent := unnest.Parent()
	if unnestParent.IsKind(exp.Join) {
		sel := unnestParent.Parent()
		if sel.IsKind(exp.Select) {
			replaceColumnName := ""
			switch x := sequenceValueName.(type) {
			case string:
				replaceColumnName = x
			case *exp.Expr:
				replaceColumnName = x.Name()
			}
			if scope := sqlglot.BuildScope(sel); scope != nil {
				for _, column := range scope.Columns() {
					if strings.EqualFold(column.Name(), replaceColumnName) {
						if column.Parent().IsKind(exp.Select) {
							column.Replace(dateAdd.As(replaceColumnName))
						} else {
							column.Replace(dateAdd)
						}
					}
				}
			}
			lateral := exp.New(exp.Lateral, "this", unnestParent.This().Pop())
			unnestParent.Replace(exp.New(exp.Join, "this", lateral))
		}
	} else {
		unnest.Replace(exp.NewSelect(dateAdd.As(sequenceValueName)).From(unnest.Copy()).Subquery(unnestAlias, true))
	}
}

func transformGenerateDateArray(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Select) {
		for _, generateDateArray := range e.FindAllList(exp.GenerateDateArray) {
			parent := generateDateArray.Parent()
			// If GENERATE_DATE_ARRAY is used directly as an array (e.g passed into ARRAY_LENGTH),
			// the transformed Snowflake query is the following (it'll be unnested properly on the
			// next iteration due to copy):
			// SELECT ref(GENERATE_DATE_ARRAY(...)) -> SELECT ref((SELECT ARRAY_AGG(*) FROM UNNEST(GENERATE_DATE_ARRAY(...))))
			if !parent.IsKind(exp.Unnest) {
				unnest := exp.New(exp.Unnest, "expressions", []*exp.Expr{generateDateArray.Copy()})
				generateDateArray.Replace(
					exp.NewSelect(exp.New(exp.ArrayAgg, "this", exp.New(exp.Star))).From(unnest).Subquery(nil, true))
			}
			if parent.IsKind(exp.Unnest) && parent.Parent().IsAny(exp.From, exp.Join) && len(parent.Expressions()) == 1 {
				unnestGenerateDateArray(parent)
			}
		}
	}
	return e
}

var arraySortSQL = sqlglot.UnsupportedArgs(func(g *gen, e *exp.Expr) string {
	// NULLs default to last when ascending and first when descending
	asc := e.Get("asc")
	nullsFirst := e.Get("nulls_first")
	switch {
	case asc == nil && nullsFirst.Equal(exp.NewFalse()):
		nullsFirst = nil
	case asc == nil && nullsFirst != nil:
		asc = exp.NewTrue()
	case asc.Equal(exp.NewFalse()) && nullsFirst.Equal(exp.NewTrue()):
		nullsFirst = nil
	}
	return g.Func("ARRAY_SORT", e.This(), asc, nullsFirst)
}, sqlglot.ArgDiag("expression", "Snowflake's ARRAY_SORT does not support a comparator."))

func regexpextractSQL(g *gen, e *exp.Expr) string {
	// Other dialects don't support all of the following parameters, so we need to generate
	// default values as necessary to ensure the transpilation is correct
	group := e.Get("group")
	// To avoid generating all these default values, we set group to None if it's 0 (also
	// default value) which doesn't trigger the following chain
	if group != nil && group.Name() == "0" {
		group = nil
	}
	parameters := e.Get("parameters")
	if parameters == nil && group != nil {
		parameters = exp.LiteralString("c")
	}
	occurrence := e.Get("occurrence")
	if occurrence == nil && parameters != nil {
		occurrence = exp.LiteralNumber(1)
	}
	position := e.Get("position")
	if position == nil && occurrence != nil {
		position = exp.LiteralNumber(1)
	}
	name := "REGEXP_SUBSTR_ALL"
	if e.IsKind(exp.RegexpExtract) {
		name = "REGEXP_SUBSTR"
	}
	return g.Func(name, e.This(), e.Expression(), position, occurrence, parameters, group)
}

func jsonExtractValueArraySQL(g *gen, e *exp.Expr) string {
	jsonExtract := exp.New(exp.JSONExtract, "this", e.This(), "expression", e.Expression())
	ident := exp.ToIdentifier("x")
	var this *exp.Expr
	if e.IsKind(exp.JSONValueArray) {
		this = exp.NewCast(ident, exp.DTypeVarchar)
	} else {
		this = exp.New(exp.ParseJSON, "this", "TO_JSON("+ident.SQL()+")")
	}
	transformLambda := exp.New(exp.Lambda, "expressions", []*exp.Expr{ident}, "this", this)
	return g.Func("TRANSFORM", jsonExtract, transformLambda)
}

func qualifyUnnestedColumns(e *exp.Expr) *exp.Expr {
	if !e.IsKind(exp.Select) {
		return e
	}
	scope := sqlglot.BuildScope(e)
	if scope == nil {
		return e
	}
	var unnests []*exp.Expr
	for n := range scope.FindAll(exp.Unnest) {
		unnests = append(unnests, n)
	}
	if len(unnests) == 0 {
		return e
	}
	takenSourceNames := map[string]bool{}
	for _, k := range scope.Sources.Keys() {
		takenSourceNames[k] = true
	}
	columnSource := map[string]*exp.Expr{}
	unnestToIdentifier := map[*exp.Expr]*exp.Expr{}
	var unnestIdentifier *exp.Expr
	origExpression := e.Copy()

	for _, unnest := range unnests {
		if !unnest.Parent().IsAny(exp.From, exp.Join) {
			continue
		}
		// Try to infer column names produced by an unnest operator. This is only possible when
		// we can peek into the (statically known) contents of the unnested value.
		var unnestColumns []string
		for _, unnestExpr := range unnest.Expressions() {
			if !unnestExpr.IsKind(exp.Array) {
				continue
			}
			for _, arrayExpr := range unnestExpr.Expressions() {
				if !arrayExpr.IsKind(exp.Struct) || len(arrayExpr.Expressions()) == 0 {
					continue
				}
				allPropertyEQ := true
				for _, se := range arrayExpr.Expressions() {
					if !se.IsKind(exp.PropertyEQ) {
						allPropertyEQ = false
					}
				}
				if !allPropertyEQ {
					continue
				}
				for _, se := range arrayExpr.Expressions() {
					unnestColumns = append(unnestColumns, strings.ToLower(se.This().Name()))
				}
				break
			}
			if len(unnestColumns) > 0 {
				break
			}
		}
		unnestAlias := unnest.Get("alias")
		if unnestAlias == nil {
			aliasName := helper.FindNewNameIn(takenSourceNames, "value")
			takenSourceNames[aliasName] = true
			// Produce a `TableAlias` AST similar to what is produced for BigQuery. This will be
			// corrected later, when we generate SQL for the `Unnest` AST node.
			aliasedUnnest := exp.NewTableAliasOf(unnest, nil, []any{aliasName})
			scope.Replace(unnest, aliasedUnnest)
			unnestIdentifier = aliasedUnnest.Get("alias").Columns()[0]
		} else {
			unnestIdentifier = unnestAlias.This()
			if unnestIdentifier == nil {
				unnestIdentifier = seqGet(unnestAlias.Columns(), 0)
			}
		}
		if !unnestIdentifier.IsKind(exp.Identifier) {
			return origExpression
		}
		unnestToIdentifier[unnest] = unnestIdentifier
		for _, c := range unnestColumns {
			columnSource[c] = unnestIdentifier
		}
	}

	for _, column := range scope.Columns() {
		if column.TableName() != "" {
			continue
		}
		table := columnSource[strings.ToLower(column.Name())]
		if unnestIdentifier != nil && table == nil && scope.Sources.Len() == 1 &&
			strings.ToLower(column.Name()) != strings.ToLower(unnestIdentifier.Name()) {
			unnestAncestor := column.FindAncestor(exp.Unnest, exp.Select)
			if unnestAncestor.IsKind(exp.Unnest) {
				if ancestorIdentifier := unnestToIdentifier[unnestAncestor]; ancestorIdentifier != nil &&
					strings.EqualFold(ancestorIdentifier.Name(), unnestIdentifier.Name()) {
					continue
				}
			}
			table = unnestIdentifier
		}
		if table != nil {
			column.Set("table", table.Copy())
		} else {
			column.Set("table", nil)
		}
	}
	return e
}

func eliminateDotVariantLookup(e *exp.Expr) *exp.Expr {
	if !e.IsKind(exp.Select) {
		return e
	}
	// This transformation is used to facilitate transpilation of BigQuery `UNNEST` operations
	// to Snowflake. It should not affect roundtrip because `Unnest` nodes cannot be produced by
	// Snowflake's parser.
	//
	// Additionally, at the time of writing this, BigQuery is the only dialect that produces a
	// `TableAlias` node that only fills `columns` and not `this`, due to `UNNEST_COLUMN_ONLY`.
	unnestAliases := map[string]bool{}
	for unnest := range sqlglot.FindAllInScope(e, exp.Unnest) {
		unnestAlias := unnest.Get("alias")
		if unnestAlias.IsKind(exp.TableAlias) && unnestAlias.This() == nil && len(unnestAlias.Columns()) == 1 {
			unnestAliases[unnestAlias.Columns()[0].Name()] = true
		}
	}
	if len(unnestAliases) > 0 {
		var columns []*exp.Expr
		for c := range sqlglot.FindAllInScope(e, exp.Column) {
			columns = append(columns, c)
		}
		for _, c := range columns {
			if unnestAliases[c.TableName()] {
				bracket := exp.New(exp.Bracket, "this", c.Get("table"), "expressions", []*exp.Expr{exp.LiteralString(c.Name())})
				if c.Parent() == e {
					// Retain column projection names by using aliases
					c.Replace(exp.NewAlias(bracket, c.This().Copy()))
				} else {
					c.Replace(bracket)
				}
			}
		}
	}
	return e
}

func tryPrefix(e *exp.Expr, name string) string {
	return ghelp.Iff(e.Bool("safe"), "TRY_", "") + name
}

func generator(c *sqlglot.GeneratorConfig) {
	c.ByteStringFunction = "HEX_DECODE_BINARY"
	c.SelectKinds = []string{}
	c.ParameterToken = "$"
	c.MatchedBySource = false
	c.MergeAllByName = true
	c.SingleStringInterval = true
	c.CastToIntRounds = true
	c.JoinHints = false
	c.TableHints = false
	c.QueryHints = false
	c.SupportsTableCopy = false
	c.CollateIsFunc = true
	c.LimitOnlyLiterals = true
	c.JsonKeyValuePairSep = ","
	c.InsertOverwrite = " OVERWRITE INTO"
	c.StructDelimiter = []string{"(", ")"}
	c.CopyParamsAreWrapped = false
	c.CopyParamsEqRequired = true
	c.StarExcept = "EXCLUDE"
	c.SupportsExplodingProjections = false
	c.ArrayConcatIsVarLen = false
	c.SupportsConvertTimezone = true
	c.ExceptIntersectSupportAllClause = false
	c.SupportsMedian = true
	c.ArraySizeName = "ARRAY_SIZE"
	c.SupportsDecodeCase = true
	c.AlterTableIncludeColumnKeyword = false
	c.AlterTableAddColumnsWrapped = false
	c.AfterHavingModifierTransforms = sqlglot.AfterHavingModifierTransforms()
	c.IsBoolAllowed = false
	c.DirectedJoins = true
	c.SupportsUescape = false
	c.TrySupported = false

	transforms(c.Transforms)

	c.SupportedJsonPathParts = exp.NewKindSet(exp.JSONPathKey, exp.JSONPathRoot, exp.JSONPathSubscript)

	c.TypeMapping[exp.DTypeBigdecimal] = "DOUBLE"
	c.TypeMapping[exp.DTypeJson] = "VARIANT"
	c.TypeMapping[exp.DTypeNested] = "OBJECT"
	c.TypeMapping[exp.DTypeStruct] = "OBJECT"
	c.TypeMapping[exp.DTypeText] = "VARCHAR"

	c.TokenMapping = map[tokens.TokenType]string{tokens.AutoIncrement: "AUTOINCREMENT"}

	c.PropertiesLocation[exp.CredentialsProperty] = exp.PostWith
	c.PropertiesLocation[exp.LocationProperty] = exp.PostWith
	c.PropertiesLocation[exp.PartitionedByProperty] = exp.PostSchema
	c.PropertiesLocation[exp.RowAccessProperty] = exp.PostSchema
	c.PropertiesLocation[exp.SetProperty] = exp.Unsupported
	c.PropertiesLocation[exp.VolatileProperty] = exp.Unsupported

	c.RespectIgnoreNullsUnsupportedExpressions = exp.NewKindSet(exp.ArrayAgg)

	methods2(c)
}

func renameFunc(name string) sqlglot.GenFunc { return sqlglot.RenameFunc(name) }

func transforms(t map[exp.Kind]sqlglot.GenFunc) {
	t[exp.ApproxDistinct] = renameFunc("APPROX_COUNT_DISTINCT")
	t[exp.ArgMax] = renameFunc("MAX_BY")
	t[exp.ArgMin] = renameFunc("MIN_BY")
	t[exp.Array] = sqlglot.Preprocess([]sqlglot.Transform{sqlglot.InheritStructFieldNames}, nil)
	t[exp.ArrayConcat] = sqlglot.ArrayConcatSQL("ARRAY_CAT")
	t[exp.ArrayAppend] = sqlglot.ArrayAppendSQL("ARRAY_APPEND", false)
	t[exp.ArrayPrepend] = sqlglot.ArrayAppendSQL("ARRAY_PREPEND", false)
	t[exp.ArrayContains] = func(g *gen, e *exp.Expr) string {
		value := e.Expression()
		if ev, ok := e.Arg("ensure_variant").(bool); !ok || ev {
			value = exp.NewCast(e.Expression(), exp.DTypeVariant, exp.WithCopy(false))
		}
		return g.Func("ARRAY_CONTAINS", value, e.This())
	}
	t[exp.ArrayPosition] = func(g *gen, e *exp.Expr) string { return g.Func("ARRAY_POSITION", e.Expression(), e.This()) }
	t[exp.ArrayIntersect] = renameFunc("ARRAY_INTERSECTION")
	t[exp.ArrayOverlaps] = renameFunc("ARRAYS_OVERLAP")
	t[exp.ArraySort] = arraySortSQL
	t[exp.AtTimeZone] = func(g *gen, e *exp.Expr) string { return g.Func("CONVERT_TIMEZONE", e.Get("zone"), e.This()) }
	t[exp.BitwiseOr] = renameFunc("BITOR")
	t[exp.BitwiseXor] = renameFunc("BITXOR")
	t[exp.BitwiseAnd] = renameFunc("BITAND")
	t[exp.BitwiseAndAgg] = renameFunc("BITANDAGG")
	t[exp.BitwiseOrAgg] = renameFunc("BITORAGG")
	t[exp.BitwiseXorAgg] = renameFunc("BITXORAGG")
	t[exp.BitwiseNot] = renameFunc("BITNOT")
	t[exp.BitwiseLeftShift] = renameFunc("BITSHIFTLEFT")
	t[exp.BitwiseRightShift] = renameFunc("BITSHIFTRIGHT")
	t[exp.CurrentTimestamp] = func(g *gen, e *exp.Expr) string {
		if e.Bool("sysdate") {
			return g.Func("SYSDATE")
		}
		return g.FunctionFallbackSQL(e)
	}
	t[exp.CurrentSchemas] = func(g *gen, e *exp.Expr) string { return g.Func("CURRENT_SCHEMAS") }
	t[exp.Localtime] = func(g *gen, e *exp.Expr) string {
		if e.This() != nil {
			return g.Func("CURRENT_TIME", e.This())
		}
		return "CURRENT_TIME"
	}
	t[exp.Localtimestamp] = func(g *gen, e *exp.Expr) string {
		if e.This() != nil {
			return g.Func("CURRENT_TIMESTAMP", e.This())
		}
		return "CURRENT_TIMESTAMP"
	}
	t[exp.DateAdd] = sqlglot.DateDeltaSQL("DATEADD", false)
	t[exp.DateDiff] = sqlglot.DateDeltaSQL("DATEDIFF", false)
	t[exp.DatetimeAdd] = sqlglot.DateDeltaSQL("TIMESTAMPADD", false)
	t[exp.DatetimeDiff] = sqlglot.TimestampdiffSQL
	t[exp.DateStrToDate] = sqlglot.DatestrtodateSQL
	t[exp.Decrypt] = func(g *gen, e *exp.Expr) string {
		return g.Func(tryPrefix(e, "DECRYPT"), e.This(), e.Get("passphrase"), e.Get("aad"), e.Get("encryption_method"))
	}
	t[exp.DecryptRaw] = func(g *gen, e *exp.Expr) string {
		return g.Func(tryPrefix(e, "DECRYPT_RAW"), e.This(), e.Get("key"), e.Get("iv"), e.Get("aad"),
			e.Get("encryption_method"), e.Get("aead"))
	}
	t[exp.DayOfMonth] = renameFunc("DAYOFMONTH")
	t[exp.DayOfWeek] = renameFunc("DAYOFWEEK")
	t[exp.DayOfWeekIso] = renameFunc("DAYOFWEEKISO")
	t[exp.DayOfYear] = renameFunc("DAYOFYEAR")
	t[exp.DotProduct] = renameFunc("VECTOR_INNER_PRODUCT")
	t[exp.Explode] = renameFunc("FLATTEN")
	t[exp.Extract] = func(g *gen, e *exp.Expr) string {
		return g.Func("DATE_PART", sqlglot.MapDatePart(e.This(), g.D), e.Expression())
	}
	t[exp.Filter] = sqlglot.NoFilterSQL
	t[exp.CosineDistance] = renameFunc("VECTOR_COSINE_SIMILARITY")
	t[exp.EuclideanDistance] = renameFunc("VECTOR_L2_DISTANCE")
	t[exp.HandlerProperty] = func(g *gen, e *exp.Expr) string { return "HANDLER = " + g.SQLArg(e, "this") }
	t[exp.FileFormatProperty] = func(g *gen, e *exp.Expr) string {
		return "FILE_FORMAT=(" + g.Expressions(e, sqlglot.ExprsOpts{Key: "expressions", Sep: " "}) + ")"
	}
	t[exp.FromTimeZone] = func(g *gen, e *exp.Expr) string { return g.Func("CONVERT_TIMEZONE", e.Get("zone"), "'UTC'", e.This()) }
	t[exp.GenerateSeries] = func(g *gen, e *exp.Expr) string {
		end := e.Get("end")
		if !e.Bool("is_end_exclusive") {
			end = end.Plus(1)
		}
		return g.Func("ARRAY_GENERATE_RANGE", e.Get("start"), end, e.Get("step"))
	}
	t[exp.GetExtract] = renameFunc("GET")
	t[exp.GroupConcat] = func(g *gen, e *exp.Expr) string {
		sep := ""
		return sqlglot.GroupconcatSQL(g, e, "LISTAGG", &sep, true, false)
	}
	t[exp.If] = sqlglot.IfSQL("IFF", "NULL")
	t[exp.JSONArray] = func(g *gen, e *exp.Expr) string {
		return g.Func("TO_VARIANT", g.Func("ARRAY_CONSTRUCT", e.Expressions()))
	}
	t[exp.JSONExtractArray] = jsonExtractValueArraySQL
	t[exp.JSONExtractScalar] = func(g *gen, e *exp.Expr) string {
		return g.Func("JSON_EXTRACT_PATH_TEXT", e.This(), e.Expression())
	}
	t[exp.JSONKeys] = renameFunc("OBJECT_KEYS")
	t[exp.JSONObject] = func(g *gen, e *exp.Expr) string { return g.Func("OBJECT_CONSTRUCT_KEEP_NULL", e.Expressions()) }
	t[exp.JSONPathRoot] = func(g *gen, e *exp.Expr) string { return "" }
	t[exp.JSONValueArray] = jsonExtractValueArraySQL
	t[exp.Levenshtein] = sqlglot.UnsupportedArgs(renameFunc("EDITDISTANCE"), "ins_cost", "del_cost", "sub_cost")
	t[exp.LocationProperty] = func(g *gen, e *exp.Expr) string { return "LOCATION=" + g.SQLArg(e, "this") }
	t[exp.LogicalAnd] = renameFunc("BOOLAND_AGG")
	t[exp.LogicalOr] = renameFunc("BOOLOR_AGG")
	t[exp.Map] = func(g *gen, e *exp.Expr) string { return sqlglot.VarMapSQL(g, e, "OBJECT_CONSTRUCT") }
	t[exp.ManhattanDistance] = renameFunc("VECTOR_L1_DISTANCE")
	t[exp.MakeInterval] = func(g *gen, e *exp.Expr) string { return sqlglot.NoMakeIntervalSQL(g, e, ", ") }
	t[exp.Max] = sqlglot.MaxOrGreatest
	t[exp.Min] = sqlglot.MinOrLeast
	t[exp.NthValue] = sqlglot.NthValueFromSQL
	t[exp.ParseJSON] = func(g *gen, e *exp.Expr) string { return g.Func(tryPrefix(e, "PARSE_JSON"), e.This()) }
	t[exp.ToBinary] = func(g *gen, e *exp.Expr) string { return g.Func(tryPrefix(e, "TO_BINARY"), e.This(), e.Get("format")) }
	t[exp.ToBoolean] = func(g *gen, e *exp.Expr) string { return g.Func(tryPrefix(e, "TO_BOOLEAN"), e.This()) }
	t[exp.ToDouble] = func(g *gen, e *exp.Expr) string { return g.Func(tryPrefix(e, "TO_DOUBLE"), e.This(), e.Get("format")) }
	t[exp.ToFile] = func(g *gen, e *exp.Expr) string { return g.Func(tryPrefix(e, "TO_FILE"), e.This(), e.Get("path")) }
	t[exp.JSONFormat] = renameFunc("TO_JSON")
	t[exp.PartitionedByProperty] = func(g *gen, e *exp.Expr) string { return "PARTITION BY " + g.SQLArg(e, "this") }
	t[exp.PercentileCont] = sqlglot.Preprocess([]sqlglot.Transform{sqlglot.AddWithinGroupForPercentiles}, nil)
	t[exp.PercentileDisc] = sqlglot.Preprocess([]sqlglot.Transform{sqlglot.AddWithinGroupForPercentiles}, nil)
	t[exp.Pivot] = sqlglot.Preprocess([]sqlglot.Transform{unqualifyPivotColumns}, nil)
	t[exp.RegexpExtract] = regexpextractSQL
	t[exp.RegexpExtractAll] = regexpextractSQL
	t[exp.RegexpILike] = regexpilikeSQL
	t[exp.RowAccessProperty] = rowaccesspropertySQL
	t[exp.Select] = sqlglot.Preprocess([]sqlglot.Transform{
		sqlglot.EliminateWindowClause,
		sqlglot.EliminateDistinctOn,
		sqlglot.ExplodeProjectionToUnnest(0, false),
		sqlglot.EliminateSemiAndAntiJoins,
		transformGenerateDateArray,
		qualifyUnnestedColumns,
		eliminateDotVariantLookup,
	}, nil)
	t[exp.SHA] = renameFunc("SHA1")
	t[exp.SHA1Digest] = renameFunc("SHA1_BINARY")
	t[exp.MD5Digest] = renameFunc("MD5_BINARY")
	t[exp.MD5NumberLower64] = renameFunc("MD5_NUMBER_LOWER64")
	t[exp.MD5NumberUpper64] = renameFunc("MD5_NUMBER_UPPER64")
	t[exp.Hex] = renameFunc("HEX_ENCODE")
	t[exp.LowerHex] = renameFunc("TO_CHAR")
	t[exp.Skewness] = renameFunc("SKEW")
	t[exp.StarMap] = renameFunc("OBJECT_CONSTRUCT")
	t[exp.StartsWith] = renameFunc("STARTSWITH")
	t[exp.EndsWith] = renameFunc("ENDSWITH")
	t[exp.Rand] = func(g *gen, e *exp.Expr) string { return g.Func("RANDOM", e.This()) }
	t[exp.StrPosition] = func(g *gen, e *exp.Expr) string {
		return sqlglot.StrpositionSQL(g, e, "CHARINDEX", true, false, true)
	}
	t[exp.StrToDate] = func(g *gen, e *exp.Expr) string { return g.Func("DATE", e.This(), fmtTime(g, e)) }
	t[exp.StringToArray] = renameFunc("STRTOK_TO_ARRAY")
	t[exp.StrtokToArray] = renameFunc("STRTOK_TO_ARRAY")
	t[exp.Stuff] = renameFunc("INSERT")
	t[exp.StPoint] = renameFunc("ST_MAKEPOINT")
	t[exp.TimeAdd] = sqlglot.DateDeltaSQL("TIMEADD", false)
	t[exp.TimeSlice] = func(g *gen, e *exp.Expr) string {
		return g.Func("TIME_SLICE", e.This(), e.Expression(), sqlglot.UnitToStr(e, "DAY"), e.Arg("kind"))
	}
	t[exp.Timestamp] = sqlglot.NoTimestampSQL
	t[exp.TimestampAdd] = sqlglot.DateDeltaSQL("TIMESTAMPADD", false)
	t[exp.TimestampDiff] = func(g *gen, e *exp.Expr) string { return g.Func("TIMESTAMPDIFF", e.Unit(), e.Expression(), e.This()) }
	t[exp.TimestampTrunc] = sqlglot.TimestamptruncSQL("DATE_TRUNC", false)
	t[exp.TimeStrToTime] = func(g *gen, e *exp.Expr) string { return sqlglot.TimestrtotimeSQL(g, e, false) }
	t[exp.TimeToUnix] = func(g *gen, e *exp.Expr) string { return "EXTRACT(epoch_second FROM " + g.SQLArg(e, "this") + ")" }
	t[exp.ToArray] = renameFunc("TO_ARRAY")
	t[exp.ToChar] = func(g *gen, e *exp.Expr) string { return g.FunctionFallbackSQL(e) }
	t[exp.TsOrDsAdd] = sqlglot.DateDeltaSQL("DATEADD", true)
	t[exp.TsOrDsDiff] = sqlglot.DateDeltaSQL("DATEDIFF", false)
	t[exp.TsOrDsToDate] = func(g *gen, e *exp.Expr) string { return g.Func(tryPrefix(e, "TO_DATE"), e.This(), fmtTime(g, e)) }
	t[exp.TsOrDsToTime] = func(g *gen, e *exp.Expr) string { return g.Func(tryPrefix(e, "TO_TIME"), e.This(), fmtTime(g, e)) }
	t[exp.Unhex] = renameFunc("HEX_DECODE_BINARY")
	t[exp.UnixToTime] = func(g *gen, e *exp.Expr) string { return g.Func("TO_TIMESTAMP", e.This(), e.Get("scale")) }
	t[exp.Uuid] = renameFunc("UUID_STRING")
	t[exp.VarMap] = func(g *gen, e *exp.Expr) string { return sqlglot.VarMapSQL(g, e, "OBJECT_CONSTRUCT") }
	t[exp.Booland] = renameFunc("BOOLAND")
	t[exp.Boolor] = renameFunc("BOOLOR")
	t[exp.WeekOfYear] = renameFunc("WEEKISO")
	t[exp.YearOfWeek] = renameFunc("YEAROFWEEK")
	t[exp.YearOfWeekIso] = renameFunc("YEAROFWEEKISO")
	t[exp.Xor] = renameFunc("BOOLXOR")
	t[exp.ByteLength] = renameFunc("OCTET_LENGTH")
	t[exp.Flatten] = renameFunc("ARRAY_FLATTEN")
	t[exp.ArrayConcatAgg] = func(g *gen, e *exp.Expr) string {
		return g.Func("ARRAY_FLATTEN", exp.New(exp.ArrayAgg, "this", e.This()))
	}
	t[exp.SHA2Digest] = func(g *gen, e *exp.Expr) string {
		return g.Func("SHA2_BINARY", e.This(), orExpr(e.Get("length"), exp.LiteralNumber(256)))
	}
}

// fmtTime is self.format_time(expression): nil when Python returns None.
func fmtTime(g *gen, e *exp.Expr) any {
	if s := g.FormatTime(e, nil, nil); s != "" {
		return s
	}
	return nil
}

func rowaccesspropertySQL(g *gen, e *exp.Expr) string {
	if e.This() == nil {
		return "ROW ACCESS"
	}
	on := ""
	if len(e.Expressions()) > 0 {
		on = " ON (" + g.Expressions(e, sqlglot.ExprsOpts{Flat: true}) + ")"
	}
	return "WITH ROW ACCESS POLICY " + g.SQLArg(e, "this") + on
}

var unsupportedValuesExpressions = []exp.Kind{exp.Map, exp.StarMap, exp.Struct, exp.VarMap}

func methods2(c *sqlglot.GeneratorConfig) {
	m := c.SQLMethods
	sup := func(k exp.Kind) sqlglot.GenFunc { return m[k] }

	m[exp.DynamicIdentifier] = func(g *gen, e *exp.Expr) string {
		this := g.Func("IDENTIFIER", e.This())
		if e.HasArg("expressions") {
			// `IDENTIFIER(...)` invoked as a function, e.g. `IDENTIFIER('my_func')(1, 2)`
			return g.FuncNoNormalize(this, e.Expressions())
		}
		return this
	}

	c.Methods.WithProperties = func(g *gen, properties *exp.Expr) string {
		return g.Properties(properties, g.SepWith(""), " ", "", false)
	}

	superValues := c.Methods.ValuesSQL
	c.Methods.ValuesSQL = func(g *gen, e *exp.Expr, valuesAsTable bool) string {
		if e.Find(unsupportedValuesExpressions...) != nil {
			valuesAsTable = false
		}
		return superValues(g, e, valuesAsTable)
	}

	superDatatype := sup(exp.DataType)
	m[exp.DataType] = func(g *gen, e *exp.Expr) string {
		// Check if this is a FLOAT type nested inside a VECTOR type
		// VECTOR only accepts FLOAT (not DOUBLE), INT, and STRING as element types
		// https://docs.snowflake.com/en/sql-reference/data-types-vector
		if e.IsType(exp.DTypeDouble) {
			if parent := e.Parent(); parent.IsKind(exp.DataType) && parent.IsType(exp.DTypeVector) {
				// Preserve FLOAT for VECTOR types instead of mapping to synonym DOUBLE
				return "FLOAT"
			}
		}
		expressions := e.Expressions()
		if len(expressions) > 0 && e.IsTypeIn(exp.DataTypeStructTypes) {
			for _, fieldType := range expressions {
				// The correct syntax is OBJECT [ (<key> <value_type [NOT NULL] [, ...]) ]
				if fieldType.IsKind(exp.DataType) {
					return "OBJECT"
				}
				if fieldType.IsKind(exp.ColumnDef) && fieldType.This() != nil && fieldType.This().IsString() {
					// Doing OBJECT('foo' VARCHAR) is invalid snowflake Syntax. Moreover, besides
					// converting 'foo' into an identifier, we also need to quote it because these
					// keys are case-sensitive.
					fieldType.This().Replace(exp.ToIdentifierQuoted(fieldType.Name(), true))
				}
			}
		}
		return superDatatype(g, e)
	}

	m[exp.ToNumber] = func(g *gen, e *exp.Expr) string {
		precision, scale := e.Get("precision"), e.Get("scale")
		defaultPrecision := precision.IsKind(exp.Literal) && precision.Name() == "38"
		defaultScale := scale.IsKind(exp.Literal) && scale.Name() == "0"
		if defaultPrecision && defaultScale {
			precision, scale = nil, nil
		} else if defaultScale {
			scale = nil
		}
		return g.Func(tryPrefix(e, "TO_NUMBER"), e.This(), e.Get("format"), precision, scale)
	}

	m[exp.TimestampFromParts] = func(g *gen, e *exp.Expr) string {
		if milli := e.Get("milli"); milli != nil {
			e.Set("nano", milli.Pop().Times(exp.LiteralNumber(1000000)))
		}
		return renameFunc("TIMESTAMP_FROM_PARTS")(g, e)
	}

	superCast := c.Methods.CastSQL
	c.Methods.CastSQL = func(g *gen, e *exp.Expr, safePrefix string) string {
		if e.IsType(exp.DTypeGeography) {
			return g.Func("TO_GEOGRAPHY", e.This())
		}
		if e.IsType(exp.DTypeGeometry) {
			return g.Func("TO_GEOMETRY", e.This())
		}
		return superCast(g, e, safePrefix)
	}

	superTrycast := sup(exp.TryCast)
	m[exp.TryCast] = func(g *gen, e *exp.Expr) string {
		value := e.This()
		if value.Type() == nil {
			value = sqlglot.AnnotateTypes(value, g.D)
		}
		// Snowflake requires that TRY_CAST's value be a string. If TRY_CAST is being roundtripped
		// (since Snowflake is the only dialect that sets "requires_string") or if we can deduce
		// that the value is a string, then we can generate TRY_CAST
		if e.Bool("requires_string") || value.IsTypeIn(exp.DataTypeTextTypes) {
			return superTrycast(g, e)
		}
		return g.CastSQL(e, "")
	}

	superLog := sup(exp.Log)
	m[exp.Log] = func(g *gen, e *exp.Expr) string {
		if e.Expression() == nil {
			return g.Func("LN", e.This())
		}
		return superLog(g, e)
	}

	m[exp.Greatest] = func(g *gen, e *exp.Expr) string {
		name := ghelp.Iff(e.Bool("ignore_nulls"), "GREATEST_IGNORE_NULLS", "GREATEST")
		return g.Func(name, append([]any{e.This()}, toAnys(e.Expressions())...)...)
	}
	m[exp.Least] = func(g *gen, e *exp.Expr) string {
		name := ghelp.Iff(e.Bool("ignore_nulls"), "LEAST_IGNORE_NULLS", "LEAST")
		return g.Func(name, append([]any{e.This()}, toAnys(e.Expressions())...)...)
	}

	m[exp.Generator] = func(g *gen, e *exp.Expr) string {
		var args []any
		if rowcount := e.Get("rowcount"); rowcount != nil {
			args = append(args, exp.New(exp.Kwarg, "this", exp.NewVar("ROWCOUNT"), "expression", rowcount))
		}
		if timelimit := e.Get("timelimit"); timelimit != nil {
			args = append(args, exp.New(exp.Kwarg, "this", exp.NewVar("TIMELIMIT"), "expression", timelimit))
		}
		return g.Func("GENERATOR", args...)
	}

	m[exp.Unnest] = unnestSQL
	m[exp.Undrop] = func(g *gen, e *exp.Expr) string {
		rename := ghelp.Pfx(" RENAME TO ", g.SQLArg(e, "rename"))
		return "UNDROP " + strings.ToUpper(e.Text("kind")) + " " + g.SQLArg(e, "this") + rename
	}
	m[exp.Show] = showSQL
	m[exp.RowAccessProperty] = rowaccesspropertySQL
	m[exp.Describe] = describeSQL
	m[exp.GeneratedAsIdentityColumnConstraint] = func(g *gen, e *exp.Expr) string {
		start := ghelp.Pfx(" START ", argText(e, "start"))
		increment := ghelp.Pfx(" INCREMENT ", argText(e, "increment"))
		orderClause := ""
		if order := e.Arg("order"); order != nil {
			orderClause = ghelp.Iff(exp.Truthy(order), " ORDER", " NOORDER")
		}
		return "AUTOINCREMENT" + start + increment + orderClause
	}
	m[exp.Struct] = structSQL
	m[exp.ApproxQuantile] = sqlglot.UnsupportedArgs(func(g *gen, e *exp.Expr) string {
		return g.Func("APPROX_PERCENTILE", e.This(), e.Get("quantile"))
	}, "weight", "accuracy")
	m[exp.AlterSet] = func(g *gen, e *exp.Expr) string {
		exprs := ghelp.Pfx(" ", g.Expressions(e, sqlglot.ExprsOpts{Flat: true}))
		fileFormat := ghelp.Around(" STAGE_FILE_FORMAT = (", g.Expressions(e, sqlglot.ExprsOpts{Key: "file_format", Flat: true, Sep: " "}), ")")
		copyOptions := ghelp.Around(" STAGE_COPY_OPTIONS = (", g.Expressions(e, sqlglot.ExprsOpts{Key: "copy_options", Flat: true, Sep: " "}), ")")
		tag := ghelp.Pfx(" TAG ", g.Expressions(e, sqlglot.ExprsOpts{Key: "tag", Flat: true}))
		return "SET" + exprs + fileFormat + copyOptions + tag
	}
	m[exp.StrToTime] = func(g *gen, e *exp.Expr) string {
		// target_type is stored as a DataType instance
		targetType := e.Get("target_type")
		typeEnum := exp.DTypeTimestamp
		if targetType.IsKind(exp.DataType) {
			typeEnum = targetType.DataTypeThis()
		} else if e.Type() != nil {
			typeEnum = e.Type().DataTypeThis()
		}
		funcName, ok := TimestampTypes[typeEnum]
		if !ok {
			funcName = "TO_TIMESTAMP"
		}
		return g.Func(tryPrefix(e, funcName), e.This(), fmtTime(g, e))
	}
	m[exp.TimestampSub] = func(g *gen, e *exp.Expr) string {
		return g.SQL(exp.New(exp.TimestampAdd, "this", e.This(), "expression", e.Expression().Times(-1), "unit", e.Unit()))
	}
	m[exp.JSONExtract] = func(g *gen, e *exp.Expr) string {
		this := e.This()
		// JSON strings are valid coming from other dialects such as BQ so for these cases we
		// PARSE_JSON preemptively
		if !this.IsAny(exp.ParseJSON, exp.JSONExtract) && !e.Bool("requires_json") {
			this = exp.New(exp.ParseJSON, "this", this)
		}
		return g.Func("GET_PATH", this, e.Expression())
	}
	m[exp.TimeToStr] = func(g *gen, e *exp.Expr) string {
		this := e.This()
		if this.IsString() {
			this = exp.NewCast(this, exp.DTypeTimestamp)
		}
		return g.Func("TO_CHAR", this, fmtTime(g, e))
	}
	m[exp.DateSub] = func(g *gen, e *exp.Expr) string {
		if value := e.Expression(); value != nil {
			value.Replace(value.Times(-1))
		} else {
			g.Unsupported("DateSub cannot be transpiled if the subtracted count is unknown")
		}
		return sqlglot.DateDeltaSQL("DATEADD", false)(g, e)
	}
	superSelect := sup(exp.Select)
	m[exp.Select] = func(g *gen, e *exp.Expr) string {
		limit, offset := e.Get("limit"), e.Get("offset")
		if offset != nil && (limit == nil || (limit.IsKind(exp.Limit) && limit.IsLimitAll())) {
			e.Limit(exp.NewNull(), exp.WithCopy(false))
		}
		return superSelect(g, e)
	}
	superCreateable := c.Methods.CreateableSQL
	c.Methods.CreateableSQL = func(g *gen, e *exp.Expr, locations sqlglot.PropertyLocations) string {
		isMaterialized := e.Find(exp.MaterializedProperty)
		copyGrantsProperty := e.Find(exp.CopyGrantsProperty)
		if strings.ToUpper(e.Text("kind")) == "VIEW" && isMaterialized != nil && copyGrantsProperty != nil {
			// For materialized views, COPY GRANTS is located *before* the columns list. This is in
			// contrast to normal views where COPY GRANTS is located *after* the columns list. We
			// default CopyGrantsProperty to POST_SCHEMA which means we need to output it POST_NAME
			// if a materialized view is detected
			// ref: https://docs.snowflake.com/en/sql-reference/sql/create-materialized-view#syntax
			// ref: https://docs.snowflake.com/en/sql-reference/sql/create-view#syntax
			postSchema := locations[exp.PostSchema]
			if i := slices.Index(postSchema, copyGrantsProperty); i >= 0 {
				locations[exp.PostSchema] = append(postSchema[:i:i], postSchema[i+1:]...)
			}
			thisName := g.SQLArg(e.This(), "this")
			copyGrants := g.SQL(copyGrantsProperty)
			thisSchema := ghelp.Pfx(g.Sep(), g.SchemaColumnsSQL(e.This()))
			return thisName + g.Sep() + copyGrants + thisSchema
		}
		return superCreateable(g, e, locations)
	}
	superArrayagg := sup(exp.ArrayAgg)
	m[exp.ArrayAgg] = func(g *gen, e *exp.Expr) string {
		this := e.This()
		// If an ORDER BY clause is present, we need to remove it from ARRAY_AGG and add it later
		// as part of the WITHIN GROUP clause
		var order *exp.Expr
		if this.IsKind(exp.Order) {
			order = this
			e.Set("this", order.This().Pop())
		}
		exprSQL := superArrayagg(g, e)
		if order != nil {
			exprSQL = g.SQL(exp.New(exp.WithinGroup, "this", exprSQL, "expression", order))
		}
		return exprSQL
	}
	m[exp.ArrayDistinct] = func(g *gen, e *exp.Expr) string {
		if e.Bool("check_null") {
			return g.Func("ARRAY_DISTINCT", e.This())
		}
		return g.Func("ARRAY_DISTINCT", exp.New(exp.ArrayCompact, "this", e.This()))
	}
	m[exp.ArrayToString] = func(g *gen, e *exp.Expr) string { return g.Func("ARRAY_TO_STRING", e.This(), e.Expression()) }
	m[exp.Array] = arraySQL
	superCurrentdate := sup(exp.CurrentDate)
	m[exp.CurrentDate] = func(g *gen, e *exp.Expr) string {
		zone := g.SQLArg(e, "this")
		if zone == "" {
			return superCurrentdate(g, e)
		}
		expr := exp.New(exp.Cast,
			"this", exp.New(exp.ConvertTimezone, "target_tz", zone, "timestamp", exp.New(exp.CurrentTimestamp)),
			"to", exp.New(exp.DataType, "this", exp.DTypeDate))
		return g.SQL(expr)
	}
	superDot := sup(exp.Dot)
	m[exp.Dot] = func(g *gen, e *exp.Expr) string {
		this := e.This()
		if this.Type() == nil {
			this = sqlglot.AnnotateTypes(this, g.D)
		}
		if !this.IsKind(exp.Dot) && this.IsType(exp.DTypeStruct) {
			// Generate colon notation for the top level STRUCT
			return g.SQL(this) + ":" + g.SQLArg(e, "expression")
		}
		return superDot(g, e)
	}
	m[exp.ModelAttribute] = func(g *gen, e *exp.Expr) string {
		return g.SQLArg(e, "this") + "!" + g.SQLArg(e, "expression")
	}
	m[exp.Format] = func(g *gen, e *exp.Expr) string {
		if strings.ToLower(e.Name()) == "%s" && len(e.Expressions()) == 1 {
			return g.Func("TO_CHAR", e.Expressions()[0])
		}
		return g.FunctionFallbackSQL(e)
	}
	m[exp.SplitPart] = func(g *gen, e *exp.Expr) string {
		// Set part_index to 1 if missing
		if !e.Bool("delimiter") {
			e.Set("delimiter", exp.LiteralString(" "))
		}
		if !e.Bool("part_index") {
			e.Set("part_index", exp.LiteralNumber(1))
		}
		return renameFunc("SPLIT_PART")(g, e)
	}
	m[exp.Uniform] = func(g *gen, e *exp.Expr) string {
		gen := e.Get("gen")
		// From Databricks UNIFORM(min, max, seed) -> Wrap gen in RANDOM(seed)
		if seed := e.Get("seed"); seed != nil {
			gen = exp.New(exp.Rand, "this", seed)
		}
		// No gen argument (from Databricks 2-arg UNIFORM(min, max)) -> Add RANDOM()
		if gen == nil {
			gen = exp.New(exp.Rand)
		}
		return g.Func("UNIFORM", e.This(), e.Expression(), gen)
	}
	superWindow := sup(exp.Window)
	m[exp.Window] = func(g *gen, e *exp.Expr) string {
		spec := e.Get("spec")
		this := e.This()
		ranking := this.IsAny(RankingWindowFunctionsWithFrame...) ||
			(this.IsAny(exp.RespectNulls, exp.IgnoreNulls) && this.This().IsAny(RankingWindowFunctionsWithFrame...))
		if ranking && spec != nil && spec.TextUpper("kind") == "ROWS" && spec.TextUpper("start") == "UNBOUNDED" &&
			spec.TextUpper("start_side") == "PRECEDING" && spec.TextUpper("end") == "UNBOUNDED" &&
			spec.TextUpper("end_side") == "FOLLOWING" {
			// omit the default window from window ranking functions
			e.Set("spec", nil)
		}
		return superWindow(g, e)
	}
	superWithingroup := sup(exp.WithinGroup)
	m[exp.WithinGroup] = func(g *gen, e *exp.Expr) string {
		// Snowflake's MODE doesn't support the ordered-set syntax, i.e. it only accepts the value
		// to aggregate as an argument: MODE(<expr>)
		if e.This().IsKind(exp.Mode) && e.This().This() == nil {
			if order := e.Expression(); order.IsKind(exp.Order) && len(order.Expressions()) == 1 {
				return g.SQL(exp.New(exp.Mode, "this", order.Expressions()[0].This()))
			}
		}
		return superWithingroup(g, e)
	}
}

func toAnys(es []*exp.Expr) []any {
	out := make([]any, len(es))
	for i, e := range es {
		out[i] = e
	}
	return out
}

// argText is f"{expression.args.get(key)}" for an optional argument (str() of the value).
func argText(e *exp.Expr, key string) string {
	switch v := e.Arg(key).(type) {
	case nil:
		return ""
	case *exp.Expr:
		if v == nil {
			return ""
		}
		return v.SQL()
	default:
		if !exp.Truthy(v) {
			return ""
		}
		return fmt.Sprint(v)
	}
}

func unnestSQL(g *gen, e *exp.Expr) string {
	unnestAlias := e.Get("alias")
	offset := e.Get("offset")
	var unnestAliasColumns []*exp.Expr
	if unnestAlias != nil {
		unnestAliasColumns = unnestAlias.Columns()
	}
	value := seqGet(unnestAliasColumns, 0)
	if value == nil {
		value = exp.ToIdentifier("value")
	}
	indexCol := exp.ToIdentifier("index")
	if offset != nil {
		indexCol = offset.Pop()
	}
	columns := []*exp.Expr{
		exp.ToIdentifier("seq"),
		exp.ToIdentifier("key"),
		exp.ToIdentifier("path"),
		indexCol,
		value,
		exp.ToIdentifier("this"),
	}
	if unnestAlias != nil {
		unnestAlias.Set("columns", columns)
	} else {
		unnestAlias = exp.New(exp.TableAlias, "this", "_u", "columns", columns)
	}
	tableInput := g.SQL(e.Expressions()[0])
	if !strings.HasPrefix(tableInput, "INPUT =>") {
		tableInput = "INPUT => " + tableInput
	}
	parent := e.Parent()
	explode := "TABLE(FLATTEN(" + tableInput + "))"
	if parent.IsKind(exp.Lateral) {
		explode = "FLATTEN(" + tableInput + ")"
	}
	alias := ghelp.Pfx(" AS ", g.SQL(unnestAlias))
	valuePrefix := ""
	if !parent.IsAny(exp.From, exp.Join, exp.Lateral) {
		valuePrefix = g.SQL(value) + " FROM "
	}
	return valuePrefix + explode + alias
}

func showSQL(g *gen, e *exp.Expr) string {
	terse := ghelp.Iff(e.Bool("terse"), "TERSE ", "")
	iceberg := ghelp.Iff(e.Bool("iceberg"), "ICEBERG ", "")
	history := ghelp.Iff(e.Bool("history"), " HISTORY", "")
	like := ghelp.Pfx(" LIKE ", g.SQLArg(e, "like"))
	scope := ghelp.Pfx(" ", g.SQLArg(e, "scope"))
	scopeKind := ghelp.Pfx(" IN ", g.SQLArg(e, "scope_kind"))
	startsWith := ghelp.Pfx(" STARTS WITH ", g.SQLArg(e, "starts_with"))
	limit := g.SQLArg(e, "limit")
	from := ghelp.Pfx(" FROM ", g.SQLArg(e, "from_"))
	privileges := ghelp.Pfx(" WITH PRIVILEGES ", g.Expressions(e, sqlglot.ExprsOpts{Key: "privileges", Flat: true}))
	return "SHOW " + terse + iceberg + e.Name() + history + like + scopeKind + scope + startsWith + limit + from + privileges
}

func describeSQL(g *gen, e *exp.Expr) string {
	kindValue := e.Text("kind")
	if kindValue == "" {
		kindValue = "TABLE"
	}
	var kind string
	if properties := e.Get("properties"); properties != nil {
		qualifier := g.Expressions(properties, sqlglot.ExprsOpts{Sep: " "})
		kind = " " + qualifier + " " + kindValue
	} else {
		kind = " " + kindValue
	}
	this := " " + g.SQLArg(e, "this")
	expressions := ghelp.Pfx(" ", g.Expressions(e, sqlglot.ExprsOpts{Flat: true}))
	return "DESCRIBE" + kind + this + expressions
}

func structSQL(g *gen, e *exp.Expr) string {
	exprs := e.Expressions()
	if len(exprs) == 1 {
		arg := exprs[0]
		if arg.IsStar() || (arg.IsKind(exp.ILike) && arg.This().IsStar()) {
			// Wildcard syntax: https://docs.snowflake.com/en/sql-reference/data-types-semistructured#object
			return "{" + g.SQL(exprs[0]) + "}"
		}
	}
	var args []any
	for i, x := range exprs {
		if x.IsKind(exp.PropertyEQ) {
			if x.This().IsKind(exp.Identifier) {
				args = append(args, exp.LiteralString(x.Name()))
			} else {
				args = append(args, x.This())
			}
			args = append(args, x.Expression())
		} else {
			args = append(args, exp.LiteralString(fmt.Sprintf("_%d", i)), x)
		}
	}
	return g.Func("OBJECT_CONSTRUCT", args...)
}

func arraySQL(g *gen, e *exp.Expr) string {
	firstExpr := seqGet(e.Expressions(), 0)
	if firstExpr.IsKind(exp.Select) {
		// SELECT AS STRUCT foo AS alias_foo -> ARRAY_AGG(OBJECT_CONSTRUCT('alias_foo', foo))
		if firstExpr.TextUpper("kind") == "STRUCT" {
			var objectConstructArgs []*exp.Expr
			for _, x := range firstExpr.Expressions() {
				// Alias case: SELECT AS STRUCT foo AS alias_foo -> OBJECT_CONSTRUCT('alias_foo', foo)
				// Column case: SELECT AS STRUCT foo -> OBJECT_CONSTRUCT('foo', foo)
				name := x
				if x.IsKind(exp.Alias) {
					name = x.This()
				}
				objectConstructArgs = append(objectConstructArgs, exp.LiteralString(x.AliasOrName()), name)
			}
			arrayAgg := exp.New(exp.ArrayAgg, "this", buildObjectConstruct(objectConstructArgs))
			firstExpr.Set("kind", nil)
			firstExpr.Set("expressions", []*exp.Expr{arrayAgg})
			return g.SQL(firstExpr.Subquery(nil, true))
		}
	}
	return sqlglot.InlineArraySQL(g, e)
}
