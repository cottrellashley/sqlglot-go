package sqlglot

// Generator methods translated from sqlglot/generator.py (ceil_floor .. renameindex_sql).

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
	"github.com/cottrellashley/sqlglot-go/trie"
)

func (g *Generator) ceilFloor(e *exp.Expr) string {
	if toClause := g.SQLArg(e, "to"); toClause != "" {
		return e.Kind().SQLName() + "(" + g.SQLArg(e, "this") + " TO " + toClause + ")"
	}
	return g.FunctionFallbackSQL(e)
}

func (g *Generator) functionFallbackSQL(e *exp.Expr) string {
	var args []any
	for _, spec := range e.Kind().Args() {
		switch v := e.Arg(spec.Key).(type) {
		case nil:
		case []*exp.Expr:
			for _, x := range v {
				args = append(args, x)
			}
		case []any:
			args = append(args, v...)
		default:
			args = append(args, v)
		}
	}
	return g.Func(g.FunctionName(e), args...)
}

func (g *Generator) functionName(e *exp.Expr) string {
	if key := g.D.OriginalNameMetaKey; key != "" {
		if s, ok := e.MetaGet(key).(string); ok && s != "" {
			return s
		}
	}
	return e.Kind().SQLName()
}

// formatTime mirrors format_time(expression, inverse_time_mapping=None, inverse_time_trie=None).
func (g *Generator) formatTime(e *exp.Expr, inverseTimeMapping map[string]string, inverseTimeTrie *trie.Node[rune]) string {
	if len(inverseTimeMapping) == 0 {
		inverseTimeMapping = g.D.InverseTimeMapping
	}
	if inverseTimeTrie == nil {
		inverseTimeTrie = g.D.InverseTimeTrie
	}
	return FormatTime(g.SQLArg(e, "format"), inverseTimeMapping, inverseTimeTrie)
}

func (g *Generator) tagSQL(e *exp.Expr) string {
	return pyStr(e.Arg("prefix")) + g.SQL(e.This()) + pyStr(e.Arg("postfix"))
}

func (g *Generator) tokenSQL(tokenType tokens.TokenType) string {
	if s, ok := g.C.TokenMapping[tokenType]; ok {
		return s
	}
	return tokenType.String()
}

func (g *Generator) userdefinedfunctionSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	expressions := g.NoIdentify(func() string { return g.Expressions(e, ExprsOpts{}) })
	if e.Bool("wrapped") {
		expressions = g.Wrap(expressions)
	} else {
		expressions = " " + expressions
	}
	if pyStrip(expressions) != "" {
		return this + expressions
	}
	return this
}

func (g *Generator) macrooverloadsSQL(e *exp.Expr) string { return g.Flat(e) }

func (g *Generator) macrooverloadSQL(e *exp.Expr) string {
	params := g.NoIdentify(func() string { return g.Flat(e) })
	body := g.SQLArg(e, "this")
	prefix := iff(e.Bool("is_table"), "TABLE ", "")
	return "(" + params + ") AS " + prefix + body
}

func (g *Generator) joinhintSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + "(" + g.Flat(e) + ")"
}

func (g *Generator) kwargSQL(e *exp.Expr) string { return g.Binary(e, "=>") }

func (g *Generator) whenSQL(e *exp.Expr) string {
	matched := iff(e.Bool("matched"), "MATCHED", "NOT MATCHED")
	source := iff(g.C.MatchedBySource && e.Bool("source"), " BY SOURCE", "")
	condition := pfx(" AND ", g.SQLArg(e, "condition"))

	thenExpression := e.Get("then")
	var then string
	switch {
	case thenExpression.IsKind(exp.Insert):
		var this string
		if g.C.MergeAllByName && thenExpression.This().IsKind(exp.Star) {
			this = "ALL BY NAME"
		} else {
			this = g.SQLArg(thenExpression, "this")
		}
		this = iff(this != "", "INSERT "+this, "INSERT")
		then = g.SQLArg(thenExpression, "expression")
		then = iff(then != "", this+" VALUES "+then, this)
	case thenExpression.IsKind(exp.Update):
		if star, ok := thenExpression.Arg("expressions").(*exp.Expr); ok && star.IsKind(exp.Star) {
			then = iff(g.C.MergeAllByName, "UPDATE ALL BY NAME", "UPDATE "+g.SQLArg(thenExpression, "expressions"))
		} else if exprs := thenExpression.Expressions(); g.C.MergeAllByName && len(exprs) == 1 && exprs[0].IsKind(exp.Star) {
			then = "UPDATE ALL BY NAME"
		} else {
			expressionsSQL := g.Expressions(thenExpression, ExprsOpts{})
			then = iff(expressionsSQL != "", "UPDATE SET"+g.Sep()+expressionsSQL, "UPDATE")
		}
	default:
		then = g.SQL(thenExpression)
	}

	if thenExpression.IsAny(exp.Insert, exp.Update) {
		where := g.SQLArg(thenExpression, "where")
		if where != "" && !g.C.SupportsMergeWhere {
			kind := iff(thenExpression.IsKind(exp.Insert), "INSERT", "UPDATE")
			g.Unsupported("WHERE clause in MERGE " + kind + " is not supported")
			where = ""
		}
		then += where
	}
	return "WHEN " + matched + source + condition + " THEN " + then
}

func (g *Generator) whensSQL(e *exp.Expr) string {
	return g.Expressions(e, ExprsOpts{Sep: " ", NoIndent: true})
}

func (g *Generator) mergeSQL(e *exp.Expr) string {
	table := e.This()
	tableAlias := ""
	if hints := table.List("hints"); len(hints) > 0 && table.Alias() != "" && hints[0].IsKind(exp.WithTableHint) {
		// T-SQL syntax is MERGE ... <target_table> [WITH (<merge_hint>)] [[AS] table_alias]
		tableAlias = " AS " + g.SQL(table.Get("alias").Pop())
	}
	this := g.SQL(table)
	using := "USING " + g.SQLArg(e, "using")
	whens := g.SQLArg(e, "whens")
	on := pfx("ON ", g.SQLArg(e, "on"))
	if on == "" {
		on = around("USING (", g.ExprsKey(e, "using_cond"), ")")
	}
	if returning := g.SQLArg(e, "returning"); returning != "" {
		whens += returning
	}
	sep := g.Sep()
	return g.PrependCtes(e, "MERGE INTO "+this+tableAlias+sep+using+sep+on+sep+whens)
}

func (g *Generator) tocharSQL(e *exp.Expr) string {
	g.CheckUnsupportedArgs(e, "format")
	return g.SQL(exp.NewCast(e.This(), exp.DTypeText))
}

func (g *Generator) tonumberSQL(e *exp.Expr) string {
	g.CheckUnsupportedArgs(e, "default")
	if !g.C.SupportsToNumber {
		g.Unsupported("Unsupported TO_NUMBER function")
		return g.TonumberCastSQL(e)
	}
	fmt := e.Get("format")
	if fmt == nil {
		g.Unsupported("Conversion format is required for TO_NUMBER")
		return g.TonumberCastSQL(e)
	}
	return g.Func("TO_NUMBER", e.This(), fmt)
}

func (g *Generator) tonumberCastSQL(e *exp.Expr) string {
	if e.Bool("safe") {
		return g.SQL(exp.New(exp.TryCast, "this", e.This(), "to", exp.MustDataTypeBuild("DOUBLE", nil, false)))
	}
	return g.SQL(exp.NewCast(e.This(), exp.DTypeDouble))
}

func (g *Generator) dictpropertySQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	kind := g.SQLArg(e, "kind")
	settingsSQL := g.Expressions(e, ExprsOpts{Key: "settings", Sep: " "})
	args := "()"
	if settingsSQL != "" {
		args = "(" + g.SepWith("") + settingsSQL + g.SegWith(")", "")
	}
	return this + "(" + kind + args + ")"
}

func (g *Generator) dictrangeSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	max := g.SQLArg(e, "max")
	min := g.SQLArg(e, "min")
	return this + "(MIN " + min + " MAX " + max + ")"
}

func (g *Generator) dictsubpropertySQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " " + g.SQLArg(e, "value")
}

func (g *Generator) aggregatekeypropertySQL(e *exp.Expr) string {
	return "AGGREGATE KEY (" + g.Flat(e) + ")"
}

func (g *Generator) duplicatekeypropertySQL(e *exp.Expr) string {
	return "DUPLICATE KEY (" + g.Flat(e) + ")"
}

// https://docs.starrocks.io/docs/sql-reference/sql-statements/table_bucket_part_index/CREATE_TABLE/
//
//gen:dispatch g.UniquekeypropertySQL(e, "UNIQUE KEY")
func (g *Generator) uniquekeypropertySQL(e *exp.Expr, prefix string) string {
	return prefix + " (" + g.Flat(e) + ")"
}

// https://docs.starrocks.io/docs/sql-reference/sql-statements/data-definition/CREATE_TABLE/#distribution_desc
func (g *Generator) distributedbypropertySQL(e *exp.Expr) string {
	expressions := g.Flat(e)
	if expressions != "" {
		expressions = " " + g.Wrap(expressions)
	}
	buckets := g.SQLArg(e, "buckets")
	kind := g.SQLArg(e, "kind")
	buckets = pfx(" BUCKETS ", buckets)
	order := g.SQLArg(e, "order")
	return "DISTRIBUTED BY " + kind + expressions + buckets + order
}

func (g *Generator) onclusterSQL(e *exp.Expr) string { return "" }

func (g *Generator) clusteredbypropertySQL(e *exp.Expr) string {
	expressions := g.FlatKey(e, "expressions")
	sortedBy := around(" SORTED BY (", g.FlatKey(e, "sorted_by"), ")")
	buckets := g.SQLArg(e, "buckets")
	return "CLUSTERED BY (" + expressions + ")" + sortedBy + " INTO " + buckets + " BUCKETS"
}

func (g *Generator) anyvalueSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	if having := g.SQLArg(e, "having"); having != "" {
		this = this + " HAVING " + iff(e.Bool("max"), "MAX", "MIN") + " " + having
	}
	return g.Func("ANY_VALUE", this)
}

func (g *Generator) querytransformSQL(e *exp.Expr) string {
	transform := g.Func("TRANSFORM", e.Expressions())
	rowFormatBefore := pfx(" ", g.SQLArg(e, "row_format_before"))
	recordWriter := pfx(" RECORDWRITER ", g.SQLArg(e, "record_writer"))
	using := " USING " + g.SQLArg(e, "command_script")
	schema := pfx(" AS ", g.SQLArg(e, "schema"))
	rowFormatAfter := pfx(" ", g.SQLArg(e, "row_format_after"))
	recordReader := pfx(" RECORDREADER ", g.SQLArg(e, "record_reader"))
	return transform + rowFormatBefore + recordWriter + using + schema + rowFormatAfter + recordReader
}

func (g *Generator) indexconstraintoptionSQL(e *exp.Expr) string {
	if s := g.SQLArg(e, "key_block_size"); s != "" {
		return "KEY_BLOCK_SIZE = " + s
	}
	if s := g.SQLArg(e, "using"); s != "" {
		return "USING " + s
	}
	if s := g.SQLArg(e, "parser"); s != "" {
		return "WITH PARSER " + s
	}
	if s := g.SQLArg(e, "comment"); s != "" {
		return "COMMENT " + s
	}
	if visible := e.Arg("visible"); visible != nil {
		return iff(exp.Truthy(visible), "VISIBLE", "INVISIBLE")
	}
	if s := g.SQLArg(e, "engine_attr"); s != "" {
		return "ENGINE_ATTRIBUTE = " + s
	}
	if s := g.SQLArg(e, "secondary_engine_attr"); s != "" {
		return "SECONDARY_ENGINE_ATTRIBUTE = " + s
	}
	g.Unsupported("Unsupported index constraint option.")
	return ""
}

func (g *Generator) checkcolumnconstraintSQL(e *exp.Expr) string {
	enforced := iff(e.Bool("enforced"), " ENFORCED", "")
	return "CHECK (" + g.SQLArg(e, "this") + ")" + enforced
}

func (g *Generator) indexcolumnconstraintSQL(e *exp.Expr) string {
	kind := g.SQLArg(e, "kind")
	kind = iff(kind != "", kind+" INDEX", "INDEX")
	this := pfx(" ", g.SQLArg(e, "this"))
	indexType := pfx(" USING ", g.SQLArg(e, "index_type"))
	expressions := around(" (", g.Flat(e), ")")
	options := pfx(" ", g.Expressions(e, ExprsOpts{Key: "options", Sep: " "}))
	return kind + this + indexType + expressions + options
}

func (g *Generator) nvl2SQL(e *exp.Expr) string {
	if g.C.Nvl2Supported {
		return g.FunctionFallbackSQL(e)
	}
	c := exp.New(exp.Case).When(e.This().IsOp(exp.NewNull()).Not(exp.WithCopy(false)), e.Arg("true"), exp.WithCopy(false))
	if elseCond := e.Get("false"); elseCond != nil {
		c.Else(elseCond, exp.WithCopy(false))
	}
	return g.SQL(c)
}

func (g *Generator) nthvalueSQL(e *exp.Expr) string {
	if b, ok := e.Arg("from_first").(bool); ok && !b {
		g.Unsupported("NTH_VALUE FROM LAST is not supported")
	}
	return g.FunctionFallbackSQL(e)
}

func (g *Generator) comprehensionSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	expr := g.SQLArg(e, "expression")
	position := pfx(", ", g.SQLArg(e, "position"))
	iterator := g.SQLArg(e, "iterator")
	condition := pfx(" IF ", g.SQLArg(e, "condition"))
	return this + " FOR " + expr + position + " IN " + iterator + condition
}

func (g *Generator) columnprefixSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + "(" + g.SQLArg(e, "expression") + ")"
}

func (g *Generator) opclassSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " " + g.SQLArg(e, "expression")
}

func (g *Generator) mlSQL(e *exp.Expr, name string) string {
	model := "MODEL " + g.SQLArg(e, "this")
	var exprSQL any
	if expr := e.Expression(); expr != nil {
		s := g.SQLArg(e, "expression")
		if expr.IsKind(exp.Table) {
			s = "TABLE " + s
		}
		exprSQL = s
	}
	var parameters any
	if s := g.SQLArg(e, "params_struct"); s != "" {
		parameters = s
	}
	return g.Func(name, model, exprSQL, parameters)
}

func (g *Generator) predictSQL(e *exp.Expr) string { return g.MlSQL(e, "PREDICT") }

func (g *Generator) generateembeddingSQL(e *exp.Expr) string {
	return g.MlSQL(e, iff(e.Bool("is_text"), "GENERATE_TEXT_EMBEDDING", "GENERATE_EMBEDDING"))
}

func (g *Generator) generatetextSQL(e *exp.Expr) string { return g.MlSQL(e, "GENERATE_TEXT") }

func (g *Generator) generatetableSQL(e *exp.Expr) string { return g.MlSQL(e, "GENERATE_TABLE") }

func (g *Generator) generateboolSQL(e *exp.Expr) string { return g.MlSQL(e, "GENERATE_BOOL") }

func (g *Generator) generateintSQL(e *exp.Expr) string { return g.MlSQL(e, "GENERATE_INT") }

func (g *Generator) generatedoubleSQL(e *exp.Expr) string { return g.MlSQL(e, "GENERATE_DOUBLE") }

func (g *Generator) mltranslateSQL(e *exp.Expr) string { return g.MlSQL(e, "TRANSLATE") }

func (g *Generator) mlforecastSQL(e *exp.Expr) string { return g.MlSQL(e, "FORECAST") }

func tableArgSQL(g *Generator, e *exp.Expr, key string) string {
	s := g.SQLArg(e, key)
	if e.Get(key).IsKind(exp.Table) {
		s = "TABLE " + s
	}
	return s
}

func (g *Generator) aiforecastSQL(e *exp.Expr) string {
	return g.Func("FORECAST", tableArgSQL(g, e, "this"), e.Arg("data_col"), e.Arg("timestamp_col"), e.Arg("model"),
		e.Arg("id_cols"), e.Arg("horizon"), e.Arg("forecast_end_timestamp"), e.Arg("confidence_level"),
		e.Arg("output_historical_time_series"), e.Arg("context_window"))
}

func (g *Generator) featuresattimeSQL(e *exp.Expr) string {
	return g.Func("FEATURES_AT_TIME", tableArgSQL(g, e, "this"), e.Arg("time"), e.Arg("num_rows"), e.Arg("ignore_feature_nulls"))
}

func (g *Generator) vectorsearchSQL(e *exp.Expr) string {
	return g.Func("VECTOR_SEARCH", tableArgSQL(g, e, "this"), e.Arg("column_to_search"), tableArgSQL(g, e, "query_table"),
		e.Arg("query_column_to_search"), e.Arg("top_k"), e.Arg("distance_type"), e.Arg("options"))
}

func (g *Generator) forinSQL(e *exp.Expr) string {
	return "FOR " + g.SQLArg(e, "this") + " DO " + g.SQLArg(e, "expression")
}

func (g *Generator) refreshSQL(e *exp.Expr) string {
	g.CheckUnsupportedArgs(e, "partition_start", "partition_end", "force", "mode")
	this := g.SQLArg(e, "this")
	kind := iff(e.This().IsKind(exp.Literal), "", e.Text("kind")+" ")
	return "REFRESH " + kind + this
}

func (g *Generator) toarraySQL(e *exp.Expr) string {
	arg := e.This()
	if arg.Type() == nil {
		arg = AnnotateTypes(arg, g.D)
	}
	if arg.IsType(exp.DTypeArray) {
		return g.SQL(arg)
	}
	condForNull := arg.IsOp(exp.NewNull())
	return g.SQL(exp.NewFunc("IF", condForNull, exp.NewNull(), exp.NewArray(arg, exp.WithCopy(false))))
}

func (g *Generator) tsordstotimeSQL(e *exp.Expr) string {
	this := e.This()
	if timeFormat := g.FormatTime(e, nil, nil); timeFormat != "" {
		return g.SQL(exp.NewCast(exp.New(exp.StrToTime, "this", this, "format", e.Arg("format")), exp.DTypeTime))
	}
	if this.IsKind(exp.TsOrDsToTime) || this.IsType(exp.DTypeTime) {
		return g.SQL(this)
	}
	return g.SQL(exp.NewCast(this, exp.DTypeTime))
}

func (g *Generator) tsordstotimestampSQL(e *exp.Expr) string {
	this := e.This()
	if this.IsKind(exp.TsOrDsToTimestamp) || this.IsType(exp.DTypeTimestamp) {
		return g.SQL(this)
	}
	return g.SQL(exp.NewCast(this, exp.DTypeTimestamp, exp.WithDialect(g.D)))
}

func (g *Generator) tsordstodatetimeSQL(e *exp.Expr) string {
	this := e.This()
	if this.IsKind(exp.TsOrDsToDatetime) || this.IsType(exp.DTypeDatetime) {
		return g.SQL(this)
	}
	return g.SQL(exp.NewCast(this, exp.DTypeDatetime, exp.WithDialect(g.D)))
}

func (g *Generator) tsordstodateSQL(e *exp.Expr) string {
	this := e.This()
	timeFormat := g.FormatTime(e, nil, nil)
	safe := e.Arg("safe")
	if timeFormat != "" && timeFormat != g.D.TimeFormat && timeFormat != g.D.DateFormat {
		return g.SQL(exp.NewCast(exp.New(exp.StrToTime, "this", this, "format", e.Arg("format"), "safe", safe), exp.DTypeDate))
	}
	if this.IsKind(exp.TsOrDsToDate) || this.IsType(exp.DTypeDate) {
		return g.SQL(this)
	}
	if exp.Truthy(safe) {
		return g.SQL(exp.New(exp.TryCast, "this", this, "to", exp.New(exp.DataType, "this", exp.DTypeDate)))
	}
	return g.SQL(exp.NewCast(this, exp.DTypeDate))
}

func (g *Generator) unixdateSQL(e *exp.Expr) string {
	return g.SQL(exp.NewFunc("DATEDIFF", e.This(), exp.NewCast(exp.LiteralString("1970-01-01"), exp.DTypeDate), "day"))
}

func (g *Generator) lastdaySQL(e *exp.Expr) string {
	if g.C.LastDaySupportsDatePart {
		return g.FunctionFallbackSQL(e)
	}
	if unit := e.Get("unit"); unit != nil && strings.ToUpper(unit.Name()) != "MONTH" {
		g.Unsupported("Date parts are not supported in LAST_DAY.")
	}
	return g.Func("LAST_DAY", e.This())
}

func (g *Generator) dateaddSQL(e *exp.Expr) string {
	return g.Func("DATE_ADD", e.This(), e.Expression(), UnitToStr(e, "DAY"))
}

//gen:dispatch g.ArrayinsertSQL(e, 0)
func (g *Generator) arrayinsertSQL(e *exp.Expr, indexOffset int) string {
	this := e.This()
	position := e.Get("position")
	offset := indexOffset - e.Int("offset")
	if offset != 0 {
		if position.IsInt() {
			value := position.MustToPy()
			v, ok := pyIntValue(value)
			if !ok {
				g.Unsupported("ARRAY_INSERT position can only be converted if it's a literal")
			} else if v >= 0 {
				position = exp.LiteralNumber(v + int64(offset))
			} else if offset < 0 && v == -1 {
				// 1-based -1 appends, which a 0-based position can only express as the size
				position = exp.New(exp.ArraySize, "this", this.Copy())
			} else {
				// Negative positions count from the end, so they shift in the opposite
				// direction, e.g. 0-based -1 (before the last element) is 1-based -2
				position = exp.LiteralNumber(v - int64(offset))
			}
		} else {
			g.Unsupported("ARRAY_INSERT position can only be converted if it's a literal")
		}
	}
	return g.Func("ARRAY_INSERT", this, position, e.Expression())
}

// pyIntValue converts a to_py() integer result to int64.
func pyIntValue(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	}
	return 0, false
}

func (g *Generator) arrayanySQL(e *exp.Expr) string {
	if g.C.CanImplementArrayAny {
		filtered := exp.New(exp.ArrayFilter, "this", e.This(), "expression", e.Expression())
		filteredNotEmpty := exp.New(exp.ArraySize, "this", filtered).Neq(0)
		originalIsEmpty := exp.New(exp.ArraySize, "this", e.This()).Eq(0)
		return g.SQL(exp.NewParen(originalIsEmpty.Or(filteredNotEmpty)))
	}
	// SQLGlot's executor supports ARRAY_ANY, so we don't wanna warn for the SQLGlot dialect
	if g.D.ClassName != "Dialect" {
		g.Unsupported("ARRAY_ANY is unsupported")
	}
	return g.FunctionFallbackSQL(e)
}

func (g *Generator) structSQL(e *exp.Expr) string {
	var items []*exp.Expr
	for _, x := range e.Expressions() {
		if x.IsKind(exp.PropertyEQ) {
			var alias any = x.This()
			if x.This().IsString() {
				alias = x.Name()
			}
			items = append(items, exp.NewAlias(x.Expression(), alias))
		} else {
			items = append(items, x)
		}
	}
	e.Set("expressions", items)
	return g.FunctionFallbackSQL(e)
}

func (g *Generator) partitionrangeSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " TO " + g.SQLArg(e, "expression")
}

func (g *Generator) truncatetableSQL(e *exp.Expr) string {
	target := iff(e.Bool("is_database"), "DATABASE", "TABLE")
	tables := " " + g.Expressions(e, ExprsOpts{})
	exists := iff(e.Bool("exists"), " IF EXISTS", "")
	onCluster := pfx(" ", g.SQLArg(e, "cluster"))
	identity := around(" ", g.SQLArg(e, "identity"), " IDENTITY")
	option := pfx(" ", g.SQLArg(e, "option"))
	partition := pfx(" ", g.SQLArg(e, "partition"))
	return "TRUNCATE " + target + exists + tables + onCluster + identity + option + partition
}

// TSQLConvertFormatMapping mirrors TSQL.CONVERT_FORMAT_MAPPING (used by the base convert_sql).
var TSQLConvertFormatMapping = map[string]string{
	"0": "%b %d %Y %-I:%M%p", "1": "%m/%d/%y", "2": "%y.%m.%d", "3": "%d/%m/%y", "4": "%d.%m.%y",
	"5": "%d-%m-%y", "6": "%d %b %y", "7": "%b %d, %y", "8": "%H:%M:%S", "9": "%b %d %Y %-I:%M:%S:%f%p",
	"10": "mm-dd-yy", "11": "yy/mm/dd", "12": "yymmdd", "13": "%d %b %Y %H:%M:ss:%f", "14": "%H:%M:%S:%f",
	"20": "%Y-%m-%d %H:%M:%S", "21": "%Y-%m-%d %H:%M:%S.%f", "22": "%m/%d/%y %-I:%M:%S %p", "23": "%Y-%m-%d",
	"24": "%H:%M:%S", "25": "%Y-%m-%d %H:%M:%S.%f", "100": "%b %d %Y %-I:%M%p", "101": "%m/%d/%Y",
	"102": "%Y.%m.%d", "103": "%d/%m/%Y", "104": "%d.%m.%Y", "105": "%d-%m-%Y", "106": "%d %b %Y",
	"107": "%b %d, %Y", "108": "%H:%M:%S", "109": "%b %d %Y %-I:%M:%S:%f%p", "110": "%m-%d-%Y",
	"111": "%Y/%m/%d", "112": "%Y%m%d", "113": "%d %b %Y %H:%M:%S:%f", "114": "%H:%M:%S:%f",
	"120": "%Y-%m-%d %H:%M:%S", "121": "%Y-%m-%d %H:%M:%S.%f", "126": "%Y-%m-%dT%H:%M:%S.%f",
}

// This transpiles T-SQL's CONVERT function
// https://learn.microsoft.com/en-us/sql/t-sql/functions/cast-and-convert-transact-sql?view=sql-server-ver16
func (g *Generator) convertSQL(e *exp.Expr) string {
	to := e.This()
	value := e.Expression()
	style := e.Get("style")
	safe := e.Arg("safe")
	strict := e.Bool("strict")
	if to == nil || value == nil {
		return ""
	}
	toThis, _ := to.Arg("this").(exp.DType)
	// Retrieve length of datatype and override to default if not specified
	if seqGet(to.Expressions(), 0) == nil && g.C.ParameterizableTextTypes.Has(toThis) {
		to = exp.MustDataTypeBuild(toThis, nil, false, "expressions", []*exp.Expr{exp.LiteralNumber(30)}, "nested", false)
	}
	var transformed *exp.Expr
	castKind := iff(strict, exp.Cast, exp.TryCast)

	// Check whether a conversion with format (T-SQL calls this 'style') is applicable
	if style.IsKind(exp.Literal) && style.IsInt() {
		styleValue := style.Name()
		convertedStyle, ok := TSQLConvertFormatMapping[styleValue]
		if !ok {
			g.Unsupported("Unsupported T-SQL 'style' value: " + styleValue)
		}
		var fmtE *exp.Expr
		if ok {
			fmtE = exp.LiteralString(convertedStyle)
		} else {
			fmtE = exp.LiteralString("None")
		}
		switch {
		case toThis == exp.DTypeDate:
			transformed = exp.New(exp.StrToDate, "this", value, "format", fmtE)
		case toThis == exp.DTypeDatetime || toThis == exp.DTypeDatetime2:
			transformed = exp.New(exp.StrToTime, "this", value, "format", fmtE)
		case g.C.ParameterizableTextTypes.Has(toThis):
			transformed = exp.New(castKind, "this", exp.New(exp.TimeToStr, "this", value, "format", fmtE), "to", to, "safe", safe)
		case toThis == exp.DTypeText:
			transformed = exp.New(exp.TimeToStr, "this", value, "format", fmtE)
		}
	}
	if transformed == nil {
		transformed = exp.New(castKind, "this", value, "to", to, "safe", safe)
	}
	return g.SQL(transformed)
}

func (g *Generator) jsonPathKeySQL(e *exp.Expr) string {
	this := e.Arg("this")
	if w, ok := this.(*exp.Expr); ok && w.IsKind(exp.JSONPathWildcard) {
		s := g.JsonPathPart(w)
		return pfx(".", s)
	}
	s, _ := this.(string)
	quoted := e.Bool("quoted")
	if !(quoted && g.C.JsonPathKeyQuotedForcesBrackets) && g.C.SafeJsonPathKeyRe.MatchString(s) {
		return "." + s
	}
	part := g.JsonPathPart(this)
	if g.QuoteJSONPathKeyUsingBrackets && g.C.JsonPathBracketedKeySupported {
		return "[" + part + "]"
	}
	return "." + part
}

func (g *Generator) jsonPathSubscriptSQL(e *exp.Expr) string {
	this := g.JsonPathPart(e.Arg("this"))
	return around("[", this, "]")
}

func (g *Generator) embedIgnoreNulls(e *exp.Expr, text string) string {
	this := e.This()
	if g.C.RespectIgnoreNullsUnsupportedExpressions.HasAny(this.Kind()) {
		g.Unsupported("RESPECT/IGNORE NULLS is not supported for " + this.Key() + " in " + g.D.ClassName)
		return g.SQL(this)
	}
	if g.C.IgnoreNullsInFunc && !exp.Truthy(e.MetaGet("inline")) {
		if g.C.IgnoreNullsBeforeOrder {
			// The first modifier here will be the one closest to the AggFunc's arg
			mods := slices.Collect(FindAllInScope(e, exp.HavingMax, exp.Order, exp.Limit))
			rank := func(x *exp.Expr) int {
				if x.IsKind(exp.HavingMax) {
					return 0
				}
				if x.IsKind(exp.Order) {
					return 1
				}
				return 2
			}
			slices.SortStableFunc(mods, func(a, b *exp.Expr) int { return rank(a) - rank(b) })
			if len(mods) > 0 {
				mod := mods[0]
				inner := exp.New(e.Kind(), "this", mod.This().Copy())
				inner.Meta()["inline"] = true
				mod.This().Replace(inner)
				return g.SQL(e.This())
			}
		}
		if aggFunc := e.Find(exp.AggFunc); aggFunc != nil {
			s := g.SQLNoComment(aggFunc)
			if s != "" {
				_, size := utf8.DecodeLastRuneInString(s)
				s = s[:len(s)-size]
			}
			return g.MaybeCommentWith(s+" "+text+")", aggFunc.Comments)
		}
	}
	return g.SQLArg(e, "this") + " " + text
}

func (g *Generator) copyparameterSQL(e *exp.Expr) string {
	option := g.SQLArg(e, "this")
	if len(e.Expressions()) > 0 {
		upper := strings.ToUpper(option)
		// Snowflake FILE_FORMAT options are separated by whitespace
		sep := iff(upper == "FILE_FORMAT", " ", ", ")
		// Databricks copy/format options do not set their list of values with EQ
		op := iff(upper == "COPY_OPTIONS" || upper == "FORMAT_OPTIONS", " ", " = ")
		values := g.Expressions(e, ExprsOpts{Flat: true, Sep: sep})
		return option + op + "(" + values + ")"
	}
	value := g.SQLArg(e, "expression")
	if value == "" {
		return option
	}
	op := iff(g.C.CopyParamsEqRequired, " = ", " ")
	return option + op + value
}

func (g *Generator) credentialsSQL(e *exp.Expr) string {
	credExpr := e.Arg("credentials")
	var credentials string
	if c, ok := credExpr.(*exp.Expr); ok && c.IsKind(exp.Literal) {
		// Redshift case: CREDENTIALS <string>
		credentials = pfx("CREDENTIALS ", g.SQLArg(e, "credentials"))
	} else {
		// Snowflake case: CREDENTIALS = (...)
		credentials = g.Expressions(e, ExprsOpts{Key: "credentials", Flat: true, Sep: " "})
		credentials = iff(credExpr != nil, "CREDENTIALS = ("+credentials+")", "")
	}
	storage := pfx("STORAGE_INTEGRATION = ", g.SQLArg(e, "storage"))
	encryption := around(" ENCRYPTION = (", g.Expressions(e, ExprsOpts{Key: "encryption", Flat: true, Sep: " "}), ")")
	iamRole := pfx("IAM_ROLE ", g.SQLArg(e, "iam_role"))
	region := pfx(" REGION ", g.SQLArg(e, "region"))
	return credentials + storage + encryption + iamRole + region
}

func (g *Generator) copySQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	this = iff(g.C.CopyHasIntoKeyword, " INTO "+this, " "+this)

	credentials := g.SQLArg(e, "credentials")
	if credentials != "" {
		credentials = g.Seg(credentials)
	}
	files := g.FlatKey(e, "files")
	kind := ""
	if files != "" {
		kind = g.Seg(iff(e.Bool("kind"), "FROM", "TO"))
	}
	sep := iff(g.D.CopyParamsAreCsv, ", ", " ")
	params := g.Expressions(e, ExprsOpts{Key: "params", Sep: sep, NewLine: true, SkipLast: true, SkipFirst: true,
		NoIndent: !g.C.CopyParamsAreWrapped})
	if params != "" {
		if g.C.CopyParamsAreWrapped {
			params = " WITH (" + params + ")"
		} else if !g.Pretty && (files != "" || credentials != "") {
			params = " " + params
		}
	}
	return "COPY" + this + kind + " " + files + credentials + params
}

func (g *Generator) semicolonSQL(e *exp.Expr) string { return "" }

func (g *Generator) datadeletionpropertySQL(e *exp.Expr) string {
	onSQL := iff(e.Bool("on"), "ON", "OFF")
	var filterCol, retentionPeriod any
	if s := g.SQLArg(e, "filter_column"); s != "" {
		filterCol = "FILTER_COLUMN=" + s
	}
	if s := g.SQLArg(e, "retention_period"); s != "" {
		retentionPeriod = "RETENTION_PERIOD=" + s
	}
	if filterCol != nil || retentionPeriod != nil {
		onSQL = g.Func("ON", filterCol, retentionPeriod)
	}
	return "DATA_DELETION=" + onSQL
}

func (g *Generator) maskingpolicycolumnconstraintSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	expressions := around(" USING (", g.Flat(e), ")")
	return "MASKING POLICY " + this + expressions
}

func (g *Generator) gapfillSQL(e *exp.Expr) string {
	this := "TABLE " + g.SQLArg(e, "this")
	args := []any{this}
	for _, a := range e.Args() {
		if a.Key != "this" {
			args = append(args, a.Value)
		}
	}
	return g.Func("GAP_FILL", args...)
}

func (g *Generator) scopeResolution(rhs, scopeName string) string {
	var scope any
	if scopeName != "" {
		scope = scopeName
	}
	return g.Func("SCOPE_RESOLUTION", scope, rhs)
}

func (g *Generator) scoperesolutionSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	var expr string
	if x := e.Expression(); x.Is(exp.Func) {
		// T-SQL's CLR functions are case sensitive
		expr = g.SQLArg(x, "this") + "(" + g.FormatArgs(x.Expressions()) + ")"
	} else {
		expr = g.SQLArg(e, "expression")
	}
	return g.ScopeResolution(expr, this)
}

func (g *Generator) parsejsonSQL(e *exp.Expr) string {
	if g.C.ParseJsonName == nil {
		return g.SQL(e.This())
	}
	return g.Func(*g.C.ParseJsonName, e.This(), e.Expression())
}

func (g *Generator) randSQL(e *exp.Expr) string {
	lower := g.SQLArg(e, "lower")
	upper := g.SQLArg(e, "upper")
	if lower != "" && upper != "" {
		return "(" + upper + " - " + lower + ") * " + g.Func("RAND", e.This()) + " + " + lower
	}
	return g.Func("RAND", e.This())
}

func (g *Generator) changesSQL(e *exp.Expr) string {
	information := "INFORMATION => " + g.SQLArg(e, "information")
	atBefore := pfx(g.Seg(""), g.SQLArg(e, "at_before"))
	end := pfx(g.Seg(""), g.SQLArg(e, "end"))
	return "CHANGES (" + information + ")" + atBefore + end
}

func (g *Generator) padSQL(e *exp.Expr) string {
	prefix := iff(e.Bool("is_left"), "L", "R")
	var fillPattern any
	if s := g.SQLArg(e, "fill_pattern"); s != "" {
		fillPattern = s
	}
	if fillPattern == nil && g.C.PadFillPatternIsRequired {
		fillPattern = "' '"
	}
	return g.Func(prefix+"PAD", e.This(), e.Expression(), fillPattern)
}

func (g *Generator) summarizeSQL(e *exp.Expr) string {
	table := iff(e.Bool("table"), " TABLE", "")
	return "SUMMARIZE" + table + " " + g.SQL(e.This())
}

func (g *Generator) explodinggenerateseriesSQL(e *exp.Expr) string {
	var kv []any
	for _, a := range e.Args() {
		kv = append(kv, a.Key, a.Value)
	}
	generateSeries := exp.New(exp.GenerateSeries, kv...)
	parent := e.Parent()
	if parent.IsAny(exp.Alias, exp.TableAlias) {
		parent = parent.Parent()
	}
	if g.C.SupportsExplodingProjections && !parent.IsAny(exp.Table, exp.Unnest) {
		return g.SQL(exp.New(exp.Unnest, "expressions", []*exp.Expr{generateSeries}))
	}
	if parent.IsKind(exp.Select) {
		g.Unsupported("GenerateSeries projection unnesting is not supported.")
	}
	return g.SQL(generateSeries)
}

func (g *Generator) converttimezoneSQL(e *exp.Expr) string {
	if g.C.SupportsConvertTimezone {
		return g.FunctionFallbackSQL(e)
	}
	sourceTz := e.Get("source_tz")
	targetTz := e.Get("target_tz")
	timestamp := e.Get("timestamp")
	if sourceTz != nil && timestamp != nil {
		timestamp = exp.New(exp.AtTimeZone, "this", exp.NewCast(timestamp, exp.DTypeTimestampntz), "zone", sourceTz)
	}
	return g.SQL(exp.New(exp.AtTimeZone, "this", timestamp, "zone", targetTz))
}

func (g *Generator) jsonSQL(e *exp.Expr) string {
	this := pfx(" ", g.SQLArg(e, "this"))
	withSQL := ""
	if w := e.Arg("with_"); w != nil {
		withSQL = iff(exp.Truthy(w), " WITH", " WITHOUT")
	}
	uniqueSQL := iff(e.Bool("unique"), " UNIQUE KEYS", "")
	return "JSON" + this + withSQL + uniqueSQL
}

func (g *Generator) jsonvalueSQL(e *exp.Expr) string {
	path := g.SQLArg(e, "path")
	returning := pfx(" RETURNING ", g.SQLArg(e, "returning"))
	onCondition := pfx(" ", g.SQLArg(e, "on_condition"))
	return g.Func("JSON_VALUE", e.This(), path+returning+onCondition)
}

func (g *Generator) skipjsoncolumnSQL(e *exp.Expr) string {
	regexp := iff(e.Bool("regexp"), " REGEXP", "")
	return "SKIP" + regexp + " " + g.SQL(e.Expression())
}

func (g *Generator) conditionalinsertSQL(e *exp.Expr) string {
	elseKW := iff(e.Bool("else_"), "ELSE ", "")
	condition := g.SQLArg(e, "expression")
	condition = iff(condition != "", "WHEN "+condition+" THEN ", elseKW)
	insert := g.SQLArg(e, "this")
	if len(insert) >= len("INSERT") {
		insert = insert[len("INSERT"):]
	} else {
		insert = ""
	}
	return condition + pyStrip(insert)
}

func (g *Generator) multitableinsertsSQL(e *exp.Expr) string {
	kind := g.SQLArg(e, "kind")
	source := g.SQLArg(e, "source")
	var sql string
	if kind != "" {
		expressions := g.Seg(g.Expressions(e, ExprsOpts{Sep: " "}))
		sql = "INSERT " + kind + expressions + g.Seg(source)
	} else {
		// If kind isn't present, we have Hive's FROM-first form:
		// FROM x INSERT ... SELECT ... [INSERT ... SELECT ...]
		var parts []string
		for _, x := range e.Expressions() {
			parts = append(parts, g.SQL(x))
		}
		sql = "FROM " + source + g.Seg(strings.Join(parts, g.Sep()))
	}
	return g.PrependCtes(e, sql)
}

func (g *Generator) onconditionSQL(e *exp.Expr) string {
	// Static options like "NULL ON ERROR" are stored as strings, in contrast to "DEFAULT <expr> ON ERROR"
	var empty, errorS string
	if x, ok := e.Arg("empty").(*exp.Expr); ok {
		empty = "DEFAULT " + pyStr(x) + " ON EMPTY"
	} else {
		empty = g.SQLArg(e, "empty")
	}
	if x, ok := e.Arg("error").(*exp.Expr); ok {
		errorS = "DEFAULT " + pyStr(x) + " ON ERROR"
	} else {
		errorS = g.SQLArg(e, "error")
	}
	if errorS != "" && empty != "" {
		if g.D.OnConditionEmptyBeforeError {
			errorS = empty + " " + errorS
		} else {
			errorS = errorS + " " + empty
		}
		empty = ""
	}
	null := g.SQLArg(e, "null")
	return empty + errorS + null
}

func (g *Generator) jsonextractquoteSQL(e *exp.Expr) string {
	scalar := iff(e.Bool("scalar"), " ON SCALAR STRING", "")
	return g.SQLArg(e, "option") + " QUOTES" + scalar
}

func (g *Generator) jsonexistsSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	path := g.SQLArg(e, "path")
	passing := pfx(" PASSING ", g.ExprsKey(e, "passing"))
	onCondition := pfx(" ", g.SQLArg(e, "on_condition"))
	path = path + passing + onCondition
	return g.Func("JSON_EXISTS", this, path)
}

func (g *Generator) addArrayaggNullFilter(arrayAggSQL string, arrayAggExpr, columnExpr *exp.Expr) string {
	// Add a NULL FILTER on the column to mimic the results going from a dialect that excludes nulls
	// on ARRAY_AGG (e.g Spark) to one that doesn't (e.g. DuckDB)
	includes := g.D.ArrayAggIncludesNulls != nil && *g.D.ArrayAggIncludesNulls
	if !(includes && arrayAggExpr.Bool("nulls_excluded")) {
		return arrayAggSQL
	}
	parent := arrayAggExpr.Parent()
	if parent.IsKind(exp.Filter) {
		parentCond := parent.Expression().This()
		parentCond.Replace(parentCond.And(columnExpr.IsOp(exp.NewNull()).Not()))
	} else if columnExpr.Find(exp.Column) != nil {
		// Do not add the filter if the input is not a column (e.g. literal, struct etc)
		// DISTINCT is already present in the agg function, do not propagate it to FILTER as well
		var thisSQL string
		if columnExpr.IsKind(exp.Distinct) {
			thisSQL = g.Expressions(columnExpr, ExprsOpts{})
		} else {
			thisSQL = g.SQL(columnExpr)
		}
		arrayAggSQL = arrayAggSQL + " FILTER(WHERE " + thisSQL + " IS NOT NULL)"
	}
	return arrayAggSQL
}

func (g *Generator) arrayaggSQL(e *exp.Expr) string {
	arrayAgg := g.FunctionFallbackSQL(e)
	columnExpr := e.This()
	if columnExpr.IsKind(exp.Order) {
		columnExpr = columnExpr.This()
	}
	return g.AddArrayaggNullFilter(arrayAgg, e, columnExpr)
}

func (g *Generator) sliceSQL(e *exp.Expr) string {
	step := g.SQLArg(e, "step")
	// An omitted end is written as `-` before a step, since `::` is the cast operator
	end := g.SQL(e.Expression())
	if end == "" && step != "" {
		end = "-"
	}
	begin := g.SQL(e.This())
	sql := iff(step != "", end+":"+step, end)
	if sql != "" {
		return begin + ":" + sql
	}
	return begin + ":"
}

func (g *Generator) applySQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " APPLY(" + g.SQLArg(e, "expression") + ")"
}

func (g *Generator) grantOrRevokeSQL(e *exp.Expr, keyword, preposition, grantOptionPrefix, grantOptionSuffix string) string {
	privilegesSQL := g.FlatKey(e, "privileges")
	kind := pfx(" ", g.SQLArg(e, "kind"))
	securable := pfx(" ", g.SQLArg(e, "securable"))
	principals := g.FlatKey(e, "principals")
	if !e.Bool("grant_option") {
		grantOptionPrefix, grantOptionSuffix = "", ""
	}
	// cascade for revoke only
	cascade := pfx(" ", g.SQLArg(e, "cascade"))
	return keyword + " " + grantOptionPrefix + privilegesSQL + " ON" + kind + securable + " " + preposition + " " + principals +
		grantOptionSuffix + cascade
}

func (g *Generator) grantSQL(e *exp.Expr) string {
	return g.GrantOrRevokeSQL(e, "GRANT", "TO", "", " WITH GRANT OPTION")
}

func (g *Generator) revokeSQL(e *exp.Expr) string {
	return g.GrantOrRevokeSQL(e, "REVOKE", "FROM", "GRANT OPTION FOR ", "")
}

func (g *Generator) grantprivilegeSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + around("(", g.Flat(e), ")")
}

func (g *Generator) grantprincipalSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	kind := sfx(g.SQLArg(e, "kind"), " ")
	return kind + this
}

func (g *Generator) columnsSQL(e *exp.Expr) string {
	f := g.FunctionFallbackSQL(e)
	if e.Bool("unpack") {
		f = "*" + f
	}
	return f
}

func (g *Generator) overlaySQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	expr := g.SQLArg(e, "expression")
	fromSQL := g.SQLArg(e, "from_")
	forSQL := pfx(" FOR ", g.SQLArg(e, "for_"))
	return "OVERLAY(" + this + " PLACING " + expr + " FROM " + fromSQL + forSQL + ")"
}

func (g *Generator) todoubleSQL(e *exp.Expr) string {
	g.CheckUnsupportedArgs(e, "format")
	castKind := iff(e.Bool("safe"), exp.TryCast, exp.Cast)
	return g.SQL(exp.New(castKind, "this", e.This(), "to", exp.DTypeDouble.IntoExpr()))
}

func (g *Generator) stringSQL(e *exp.Expr) string {
	this := e.This()
	if zone := e.Get("zone"); zone != nil {
		// This is a BigQuery specific argument for STRING(<timestamp_expr>, <time_zone>)
		// BigQuery stores timestamps internally as UTC, so ConvertTimezone is used with UTC
		// set for source_tz to transpile the time conversion before the STRING cast
		this = exp.New(exp.ConvertTimezone, "source_tz", exp.LiteralString("UTC"), "target_tz", zone, "timestamp", this)
	}
	return g.SQL(exp.NewCast(this, exp.DTypeVarchar))
}

func (g *Generator) medianSQL(e *exp.Expr) string {
	if !g.C.SupportsMedian {
		return g.SQL(exp.New(exp.PercentileCont, "this", e.This(), "expression", exp.LiteralNumber(0.5)))
	}
	return g.FunctionFallbackSQL(e)
}

func (g *Generator) overflowtruncatebehaviorSQL(e *exp.Expr) string {
	filler := pfx(" ", g.SQLArg(e, "this"))
	withCount := iff(e.Bool("with_count"), "WITH COUNT", "WITHOUT COUNT")
	return "TRUNCATE" + filler + " " + withCount
}

func (g *Generator) unixsecondsSQL(e *exp.Expr) string {
	if g.C.SupportsUnixSeconds {
		return g.FunctionFallbackSQL(e)
	}
	startTs := exp.NewCast(exp.LiteralString("1970-01-01 00:00:00+00"), exp.DTypeTimestamptz)
	return g.SQL(exp.New(exp.TimestampDiff, "this", e.This(), "expression", startTs, "unit", exp.NewVar("SECONDS")))
}

func (g *Generator) arraysortSQL(e *exp.Expr) string {
	asc := e.Get("asc")
	nullsFirst := e.Get("nulls_first")
	if (asc != nil && !asc.Equal(exp.NewTrue())) || (nullsFirst != nil && !nullsFirst.Equal(exp.NewFalse())) {
		g.Unsupported("ARRAY_SORT only sorts ascending with NULLs last")
	}
	return g.Func("ARRAY_SORT", e.This(), e.Expression())
}

func (g *Generator) arraysizeSQL(e *exp.Expr) string {
	dim := e.Expression()
	// For dialects that don't support the dimension arg, we can safely transpile it's default value (1st dimension)
	if dim != nil && g.C.ArraySizeDimRequired == nil {
		if !(dim.IsInt() && dim.Name() == "1") {
			g.Unsupported("Cannot transpile dimension argument for ARRAY_LENGTH")
		}
		dim = nil
	}
	// If dimension is required but not specified, default initialize it
	if g.C.ArraySizeDimRequired != nil && *g.C.ArraySizeDimRequired && dim == nil {
		dim = exp.LiteralNumber(1)
	}
	return g.Func(g.C.ArraySizeName, e.This(), dim)
}

func (g *Generator) attachSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	existsSQL := iff(e.Bool("exists"), " IF NOT EXISTS", "")
	expressions := around(" (", g.Expressions(e, ExprsOpts{}), ")")
	return "ATTACH" + existsSQL + " " + this + expressions
}

func (g *Generator) detachSQL(e *exp.Expr) string {
	kind := pfx(" ", g.SQLArg(e, "kind"))
	// the DATABASE keyword is required if IF EXISTS is set for DuckDB
	// ref: https://duckdb.org/docs/stable/sql/statements/attach.html#detach-syntax
	exists := iff(e.Bool("exists"), " IF EXISTS", "")
	if exists != "" && kind == "" {
		kind = " DATABASE"
	}
	this := pfx(" ", g.SQLArg(e, "this"))
	cluster := pfx(" ", g.SQLArg(e, "cluster"))
	permanent := iff(e.Bool("permanent"), " PERMANENTLY", "")
	sync := iff(e.Bool("sync"), " SYNC", "")
	return "DETACH" + kind + exists + this + cluster + permanent + sync
}

func (g *Generator) attachoptionSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + pfx(" ", g.SQLArg(e, "expression"))
}

func (g *Generator) watermarkcolumnconstraintSQL(e *exp.Expr) string {
	return "WATERMARK FOR " + g.SQLArg(e, "this") + " AS " + g.SQLArg(e, "expression")
}

func (g *Generator) encodepropertySQL(e *exp.Expr) string {
	encode := iff(e.Bool("key"), "KEY ENCODE", "ENCODE")
	encode = encode + " " + g.SQLArg(e, "this")
	if properties := e.Get("properties"); properties != nil {
		encode = encode + " " + g.Properties(properties, "", ", ", "", true)
	}
	return encode
}

func (g *Generator) includepropertySQL(e *exp.Expr) string {
	include := "INCLUDE " + g.SQLArg(e, "this")
	if columnDef := g.SQLArg(e, "column_def"); columnDef != "" {
		include = include + " " + columnDef
	}
	if alias := g.SQLArg(e, "alias"); alias != "" {
		include = include + " AS " + alias
	}
	return include
}

func (g *Generator) xmlelementSQL(e *exp.Expr) string {
	prefix := iff(e.Bool("evalname"), "EVALNAME", "NAME")
	name := prefix + " " + g.SQLArg(e, "this")
	return g.Func("XMLELEMENT", name, e.Expressions())
}

func (g *Generator) xmlkeyvalueoptionSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + around("(", g.SQLArg(e, "expression"), ")")
}

func (g *Generator) partitionbyrangepropertySQL(e *exp.Expr) string {
	partitions := g.ExprsKey(e, "partition_expressions")
	create := g.ExprsKey(e, "create_expressions")
	return "PARTITION BY RANGE " + g.Wrap(partitions) + " " + g.Wrap(create)
}

func (g *Generator) partitionbyrangepropertydynamicSQL(e *exp.Expr) string {
	start := g.SQLArg(e, "start")
	end := g.SQLArg(e, "end")
	every := e.Get("every")
	if every.IsKind(exp.Interval) && every.This().IsString() {
		every.This().Replace(exp.LiteralNumber(every.Name()))
	}
	return "START " + g.Wrap(start) + " END " + g.Wrap(end) + " EVERY " + g.Wrap(g.SQL(every))
}

func (g *Generator) unpivotcolumnsSQL(e *exp.Expr) string {
	return "NAME " + g.SQLArg(e, "this") + " VALUE " + g.Flat(e)
}

func (g *Generator) analyzesampleSQL(e *exp.Expr) string {
	return "SAMPLE " + g.SQLArg(e, "sample") + " " + g.SQLArg(e, "kind")
}

func (g *Generator) analyzestatisticsSQL(e *exp.Expr) string {
	kind := g.SQLArg(e, "kind")
	option := pfx(" ", g.SQLArg(e, "option"))
	this := pfx(" ", g.SQLArg(e, "this"))
	columns := pfx(" ", g.Expressions(e, ExprsOpts{}))
	return kind + option + " STATISTICS" + this + columns
}

func (g *Generator) analyzehistogramSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	columns := g.Expressions(e, ExprsOpts{})
	inner := pfx(" ", g.SQLArg(e, "expression"))
	updateOptions := around(" ", g.SQLArg(e, "update_options"), " UPDATE")
	return this + " HISTOGRAM ON " + columns + inner + updateOptions
}

func (g *Generator) analyzedeleteSQL(e *exp.Expr) string {
	return "DELETE" + pfx(" ", g.SQLArg(e, "kind")) + " STATISTICS"
}

func (g *Generator) analyzelistchainedrowsSQL(e *exp.Expr) string {
	return "LIST CHAINED ROWS" + g.SQLArg(e, "expression")
}

func (g *Generator) analyzevalidateSQL(e *exp.Expr) string {
	kind := g.SQLArg(e, "kind")
	this := pfx(" ", g.SQLArg(e, "this"))
	inner := g.SQLArg(e, "expression")
	return "VALIDATE " + kind + this + inner
}

func (g *Generator) analyzeSQL(e *exp.Expr) string {
	options := pfx(" ", g.Expressions(e, ExprsOpts{Key: "options", Sep: " "}))
	kind := pfx(" ", g.SQLArg(e, "kind"))
	tables := pfx(" ", g.FlatKey(e, "tables"))
	mode := pfx(" ", g.SQLArg(e, "mode"))
	properties := pfx(" ", g.SQLArg(e, "properties"))
	partition := pfx(" ", g.SQLArg(e, "partition"))
	inner := pfx(" ", g.SQLArg(e, "expression"))
	return "ANALYZE" + options + kind + tables + partition + mode + inner + properties
}

func (g *Generator) xmltableSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	namespaces := around("XMLNAMESPACES(", g.ExprsKey(e, "namespaces"), "), ")
	passing := g.ExprsKey(e, "passing")
	if passing != "" {
		passing = g.Sep() + "PASSING" + g.Seg(passing)
	}
	columns := g.ExprsKey(e, "columns")
	if columns != "" {
		columns = g.Sep() + "COLUMNS" + g.Seg(columns)
	}
	byRef := iff(e.Bool("by_ref"), g.Sep()+"RETURNING SEQUENCE BY REF", "")
	return "XMLTABLE(" + g.SepWith("") + g.Indent(namespaces+this+passing+byRef+columns) + g.SegWith(")", "")
}

func (g *Generator) xmlnamespaceSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	if e.This().IsKind(exp.Alias) {
		return this
	}
	return "DEFAULT " + this
}

func (g *Generator) exportSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	connection := around("WITH CONNECTION ", g.SQLArg(e, "connection"), " ")
	options := g.SQLArg(e, "options")
	return "EXPORT DATA " + connection + options + " AS " + this
}

func (g *Generator) declareSQL(e *exp.Expr) string {
	replace := iff(e.Bool("replace"), "OR REPLACE ", "")
	return "DECLARE " + replace + g.Flat(e)
}

func (g *Generator) declareitemSQL(e *exp.Expr) string {
	variables := g.ExprsKey(e, "this")
	def := g.SQLArg(e, "default")
	def = iff(def != "", " "+g.C.DeclareDefaultAssignment+" "+def, "")
	kind := g.SQLArg(e, "kind")
	if e.Get("kind").IsKind(exp.Schema) {
		kind = "TABLE " + kind
	}
	kind = pfx(" ", kind)
	return variables + kind + def
}

func (g *Generator) recursivewithsearchSQL(e *exp.Expr) string {
	kind := g.SQLArg(e, "kind")
	this := g.SQLArg(e, "this")
	set := g.SQLArg(e, "expression")
	to := pfx(" TO ", g.SQLArg(e, "to"))
	def := pfx(" DEFAULT ", g.SQLArg(e, "default"))
	using := pfx(" USING ", g.SQLArg(e, "using"))
	kindSQL := iff(kind == "CYCLE", kind, "SEARCH "+kind+" FIRST BY")
	return kindSQL + " " + this + " SET " + set + to + def + using
}

func (g *Generator) parameterizedaggSQL(e *exp.Expr) string {
	params := g.FlatKey(e, "params")
	return g.Func(e.Name(), e.Expressions()) + "(" + params + ")"
}

func (g *Generator) anonymousaggfuncSQL(e *exp.Expr) string {
	return g.Func(e.Name(), e.Expressions())
}

func (g *Generator) combinedaggfuncSQL(e *exp.Expr) string { return g.AnonymousaggfuncSQL(e) }

func (g *Generator) combinedparameterizedaggSQL(e *exp.Expr) string {
	return g.ParameterizedaggSQL(e)
}

func (g *Generator) showSQL(e *exp.Expr) string {
	g.Unsupported("Unsupported SHOW statement")
	return ""
}

func (g *Generator) installSQL(e *exp.Expr) string {
	g.Unsupported("Unsupported INSTALL statement")
	return ""
}

func (g *Generator) liststageSQL(e *exp.Expr) string {
	pattern := pfx(" PATTERN = ", g.SQLArg(e, "pattern"))
	return "LIST " + g.SQLArg(e, "this") + pattern
}

func (g *Generator) getPutSQL(e *exp.Expr) string {
	// Snowflake GET/PUT statements:
	//   PUT <file> <internalStage> <properties>
	//   GET <internalStage> <file> <properties>
	propsSQL := ""
	if props := e.Get("properties"); props != nil {
		propsSQL = g.Properties(props, " ", " ", "", false)
	}
	this := g.SQLArg(e, "this")
	target := g.SQLArg(e, "target")
	if e.IsKind(exp.Put) {
		return "PUT " + this + " " + target + propsSQL
	}
	return "GET " + target + " " + this + propsSQL
}

func (g *Generator) translatecharactersSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	expr := g.SQLArg(e, "expression")
	withError := iff(e.Bool("with_error"), " WITH ERROR", "")
	return "TRANSLATE(" + this + " USING " + expr + withError + ")"
}

func (g *Generator) decodecaseSQL(e *exp.Expr) string {
	if g.C.SupportsDecodeCase {
		return g.Func("DECODE", e.Expressions())
	}
	all := e.Expressions()
	decodeExpr, expressions := all[0], all[1:]
	var ifs []*exp.Expr
	for i := 0; i+1 < len(expressions); i += 2 {
		search, result := expressions[i], expressions[i+1]
		switch {
		case search.IsKind(exp.Literal):
			ifs = append(ifs, exp.New(exp.If, "this", decodeExpr.Eq(search), "true", result))
		case search.IsKind(exp.Null):
			ifs = append(ifs, exp.New(exp.If, "this", decodeExpr.IsOp(exp.NewNull()), "true", result))
		default:
			if search.Is(exp.Binary) {
				search = exp.NewParen(search)
			}
			cond := exp.NewOr(
				decodeExpr.Eq(search),
				exp.NewAnd(decodeExpr.IsOp(exp.NewNull()), search.IsOp(exp.NewNull()), exp.WithCopy(false)),
				exp.WithCopy(false),
			)
			ifs = append(ifs, exp.New(exp.If, "this", cond, "true", result))
		}
	}
	var def any
	if len(expressions)%2 == 1 {
		def = expressions[len(expressions)-1]
	}
	return g.SQL(exp.New(exp.Case, "ifs", ifs, "default", def))
}

func (g *Generator) semanticviewSQL(e *exp.Expr) string {
	this := g.SegWith(g.SQLArg(e, "this"), "")
	section := func(key, kw string) string {
		s := g.Expressions(e, ExprsOpts{Key: key, Dynamic: true, SkipFirst: true, SkipLast: true})
		if s == "" {
			return ""
		}
		return g.Seg(kw + " " + s)
	}
	dimensions := section("dimensions", "DIMENSIONS")
	metrics := section("metrics", "METRICS")
	facts := section("facts", "FACTS")
	where := g.SQLArg(e, "where")
	if where != "" {
		where = g.Seg("WHERE " + where)
	}
	body := g.IndentSkip(this+metrics+dimensions+facts+where, true, false)
	return "SEMANTIC_VIEW(" + body + g.SegWith(")", "")
}

func (g *Generator) getextractSQL(e *exp.Expr) string {
	this := e.This()
	expr := e.Expression()
	if this.Type() == nil || e.Type() == nil {
		this = AnnotateTypes(this, g.D)
	}
	if this.IsType(exp.DTypeArray, exp.DTypeMap) {
		return g.SQL(exp.New(exp.Bracket, "this", this, "expressions", []*exp.Expr{expr}))
	}
	return g.SQL(exp.New(exp.JSONExtract, "this", this, "expression", g.D.ToJSONPath(expr)))
}

func (g *Generator) datefromunixdateSQL(e *exp.Expr) string {
	return g.SQL(exp.New(exp.DateAdd,
		"this", exp.NewCast(exp.LiteralString("1970-01-01"), exp.DTypeDate),
		"expression", e.This(),
		"unit", exp.NewVar("DAY")))
}

func (g *Generator) spaceSQL(e *exp.Expr) string {
	return g.SQL(exp.New(exp.Repeat, "this", exp.LiteralString(" "), "times", e.This()))
}

func (g *Generator) buildpropertySQL(e *exp.Expr) string { return "BUILD " + g.SQLArg(e, "this") }

func (g *Generator) refreshtriggerpropertySQL(e *exp.Expr) string {
	method := g.SQLArg(e, "method")
	kind := e.Arg("kind")
	if !exp.Truthy(kind) {
		return "REFRESH " + method
	}
	every := g.SQLArg(e, "every")
	unit := g.SQLArg(e, "unit")
	every = iff(every != "", " EVERY "+every+" "+unit, "")
	starts := pfx(" STARTS ", g.SQLArg(e, "starts"))
	return "REFRESH " + method + " ON " + pyStr(kind) + every + starts
}

func (g *Generator) modelattributeSQL(e *exp.Expr) string {
	g.Unsupported("The model!attribute syntax is not supported")
	return ""
}

func (g *Generator) directorystageSQL(e *exp.Expr) string { return g.Func("DIRECTORY", e.This()) }

func (g *Generator) uuidSQL(e *exp.Expr) string {
	isString := e.Bool("is_string")
	uuidFuncSQL := g.Func("UUID")
	if isString && !g.D.UuidIsStringType {
		return g.SQL(exp.NewCast(uuidFuncSQL, exp.DTypeVarchar, exp.WithDialect(g.D)))
	}
	return uuidFuncSQL
}

func (g *Generator) initcapSQL(e *exp.Expr) string {
	delimiters := e.Expression()
	if delimiters != nil {
		// do not generate delimiters arg if we are round-tripping from default delimiters
		if delimiters.IsString() && delimiters.Str("this") == g.D.InitcapDefaultDelimiterChars {
			delimiters = nil
		} else if !g.D.InitcapSupportsCustomDelimiters {
			g.Unsupported("INITCAP does not support custom delimiters")
			delimiters = nil
		}
	}
	return g.Func("INITCAP", e.This(), delimiters)
}

func (g *Generator) localtimeSQL(e *exp.Expr) string {
	if this := e.This(); this != nil {
		return g.Func("LOCALTIME", this)
	}
	return "LOCALTIME"
}

func (g *Generator) localtimestampSQL(e *exp.Expr) string {
	if this := e.This(); this != nil {
		return g.Func("LOCALTIMESTAMP", this)
	}
	return "LOCALTIMESTAMP"
}

// WeekStartDayToDow mirrors dialect.WEEK_START_DAY_TO_DOW.
var WeekStartDayToDow = map[string]int{
	"MONDAY": 1, "TUESDAY": 2, "WEDNESDAY": 3, "THURSDAY": 4, "FRIDAY": 5, "SATURDAY": 6, "SUNDAY": 7,
}

// WeekOffsetToDow mirrors dialect.week_offset_to_dow.
func WeekOffsetToDow(offset int) int { return ((offset%7)+7)%7 + 1 }

func (g *Generator) weekstartName(e *exp.Expr) string {
	// WEEK(<day>) is BigQuery-only syntax, so it degrades to the plain WEEK unit
	this := strings.ToUpper(e.This().Name())
	dow, ok := WeekStartDayToDow[this]
	if !ok || dow != WeekOffsetToDow(g.D.WeekOffset) {
		g.Unsupported("WEEK(" + this + ") is not supported; falling back to the default week start day")
	}
	return "WEEK"
}

func (g *Generator) weekstartSQL(e *exp.Expr) string {
	name := g.WeekstartName(e)
	// DateTrunc stores string literal units, whereas TimeUnit expressions store keywords
	if e.Parent().IsKind(exp.DateTrunc) {
		return g.SQL(exp.LiteralString(name))
	}
	return name
}

//gen:dispatch g.ChrSQL(e, "CHR")
func (g *Generator) chrSQL(e *exp.Expr, name string) string {
	this := g.Expressions(e, ExprsOpts{})
	using := pfx(" USING ", g.SQLArg(e, "charset"))
	return g.Func(name, this+using)
}

func (g *Generator) weightstringSQL(e *exp.Expr) string {
	to := pfx(" AS ", g.SQLArg(e, "to"))
	return g.Func("WEIGHT_STRING", g.SQLArg(e, "this")+to)
}

func (g *Generator) blockSQL(e *exp.Expr) string {
	expressions := g.Expressions(e, ExprsOpts{Sep: "; ", Flat: true})
	begin := iff(e.Bool("begin"), "BEGIN ", "")
	return pfx(begin, expressions)
}

func unsupportedStatement(g *Generator, message string) string {
	g.Unsupported(message)
	return ""
}

func (g *Generator) functionspecificationSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported Inline UDFs syntax")
}

func (g *Generator) storedprocedureSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported Stored Procedure syntax")
}

func (g *Generator) ifblockSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported If block syntax")
}

func (g *Generator) casestatementSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported Case statement syntax")
}

func (g *Generator) whileblockSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported While block syntax")
}

func (g *Generator) loopblockSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported Loop block syntax")
}

func (g *Generator) repeatblockSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported Repeat block syntax")
}

func (g *Generator) leaveSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported Leave syntax")
}

func (g *Generator) iterateSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported Iterate syntax")
}

func (g *Generator) executeSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported Execute syntax")
}

func (g *Generator) executesqlSQL(e *exp.Expr) string {
	return unsupportedStatement(g, "Unsupported Execute syntax")
}

func (g *Generator) altermodifysqlsecuritySQL(e *exp.Expr) string {
	return "MODIFY " + g.Expressions(e, ExprsOpts{Sep: " "})
}

func (g *Generator) usingpropertySQL(e *exp.Expr) string {
	return "USING " + pyStr(e.Arg("kind")) + " " + g.SQLArg(e, "this")
}

func (g *Generator) renameindexSQL(e *exp.Expr) string {
	return "RENAME INDEX " + g.SQLArg(e, "this") + " TO " + g.SQLArg(e, "to")
}
