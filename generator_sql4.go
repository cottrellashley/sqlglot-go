package sqlglot

// Generator methods translated from sqlglot/generator.py (select_sql .. use_sql).

import (
	"fmt"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
)

func (g *Generator) selectSQL(e *exp.Expr) string {
	into := e.Get("into")
	if !g.C.SupportsSelectInto && into != nil {
		into.Pop()
	}

	hint := g.SQLArg(e, "hint")
	distinct := pfx(" ", g.SQLArg(e, "distinct"))
	kind := g.SQLArg(e, "kind")

	top := ""
	if limit := e.Get("limit"); limit.IsKind(exp.Limit) && g.C.LimitIsTop {
		top = g.LimitSQL(limit, true)
		limit.Pop()
	}

	expressions := g.Expressions(e, ExprsOpts{})

	if kind != "" {
		if contains(g.C.SelectKinds, kind) {
			kind = " AS " + kind
		} else {
			if kind == "STRUCT" {
				var items []*exp.Expr
				for _, x := range e.Expressions() {
					if x.IsKind(exp.Alias) {
						items = append(items, exp.New(exp.PropertyEQ, "this", x.Arg("alias"), "expression", x.This()))
					} else {
						items = append(items, x)
					}
				}
				expressions = g.Expressions(nil, ExprsOpts{Sqls: []any{g.SQL(exp.New(exp.Struct, "expressions", items))}})
			}
			kind = ""
		}
	}

	operationModifiers := pfx(g.Sep(), g.Expressions(e, ExprsOpts{Key: "operation_modifiers", Sep: " "}))

	exclude := e.List("exclude")
	if !g.C.StarExcludeRequiresDerivedTable && len(exclude) > 0 {
		excludeSQL := g.ExprsList(exclude, ExprsOpts{Flat: true})
		expressions = expressions + g.Seg("EXCLUDE") + " (" + excludeSQL + ")"
	}

	// We use LIMIT_IS_TOP as a proxy for whether DISTINCT should go first because tsql and Teradata
	// are the only dialects that use LIMIT_IS_TOP and both place DISTINCT first.
	topDistinct := iff(g.C.LimitIsTop, distinct+hint+top, top+hint+distinct)
	expressions = pfx(g.Sep(), expressions)
	sql := g.QueryModifiers(e,
		"SELECT"+topDistinct+operationModifiers+kind+expressions,
		g.SQLArg(e, "into"),
		g.SQLArg(e, "from_"),
	)

	// If both the CTE and SELECT clauses have comments, generate the latter earlier
	if e.Bool("with_") {
		sql = g.MaybeComment(sql, e)
		e.PopComments()
	}

	sql = g.PrependCtes(e, sql)

	if g.C.StarExcludeRequiresDerivedTable && len(exclude) > 0 {
		e.Set("exclude", nil)
		subquery := e.Subquery(nil, false)
		star := exp.New(exp.Star, "except_", exclude)
		sql = g.SQL(exp.NewSelect(star).From(subquery, exp.WithCopy(false)))
	}

	if !g.C.SupportsSelectInto && into != nil {
		tableKind := ""
		if into.Bool("temporary") {
			tableKind = " TEMPORARY"
		} else if g.C.SupportsUnloggedTables && into.Bool("unlogged") {
			tableKind = " UNLOGGED"
		}
		sql = "CREATE" + tableKind + " TABLE " + g.SQL(into.This()) + " AS " + sql
	}
	return sql
}

func (g *Generator) schemaSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	sql := g.SchemaColumnsSQL(e)
	if this != "" && sql != "" {
		return this + " " + sql
	}
	return iff(this != "", this, sql)
}

func (g *Generator) schemaColumnsSQL(e *exp.Expr) string {
	if len(e.Expressions()) > 0 {
		return "(" + g.SepWith("") + g.Expressions(e, ExprsOpts{}) + g.SegWith(")", "")
	}
	return ""
}

func (g *Generator) starSQL(e *exp.Expr) string {
	except := g.FlatKey(e, "except_")
	if except != "" {
		except = g.Seg(g.C.StarExcept) + " (" + except + ")"
	}
	replace := g.FlatKey(e, "replace")
	if replace != "" {
		replace = g.Seg("REPLACE") + " (" + replace + ")"
	}
	rename := g.FlatKey(e, "rename")
	if rename != "" {
		rename = g.Seg("RENAME") + " (" + rename + ")"
	}
	ilike := g.SQLArg(e, "ilike")
	if ilike != "" {
		ilike = g.Seg("ILIKE") + " " + ilike
	}
	return "*" + ilike + except + replace + rename
}

func (g *Generator) parameterSQL(e *exp.Expr) string {
	return g.C.ParameterToken + g.SQLArg(e, "this")
}

func (g *Generator) sessionparameterSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	kind := sfx(e.Text("kind"), ".")
	return "@@" + kind + this
}

func (g *Generator) placeholderSQL(e *exp.Expr) string {
	if e.Bool("this") {
		return g.C.NamedPlaceholderToken + e.Name()
	}
	return "?"
}

//gen:dispatch g.SubquerySQL(e, " AS ")
func (g *Generator) subquerySQL(e *exp.Expr, sep string) string {
	alias := pfx(sep, g.SQLArg(e, "alias"))
	sample := g.SQLArg(e, "sample")
	if g.D.AliasPostTablesample && sample != "" {
		alias = sample + alias
		// Set to None so it's not generated again by self.query_modifiers()
		e.Set("sample", nil)
	}
	pivots := g.Expressions(e, ExprsOpts{Key: "pivots", Sep: EmptySep, Flat: true})
	sql := g.QueryModifiers(e, g.Wrap(e), alias, pivots)
	return g.PrependCtes(e, sql)
}

func (g *Generator) qualifySQL(e *exp.Expr) string {
	this := g.Indent(g.SQLArg(e, "this"))
	return g.Seg("QUALIFY") + g.Sep() + this
}

func (g *Generator) unnestSQL(e *exp.Expr) string {
	args := g.Flat(e)
	aliasNode := e.Get("alias")
	offset := e.Arg("offset")

	if g.C.UnnestWithOrdinality {
		if off, ok := offset.(*exp.Expr); ok && aliasNode != nil {
			aliasNode.Append("columns", off)
			e.Set("offset", nil)
		}
	}

	var alias string
	if aliasNode != nil && g.D.UnnestColumnOnly {
		if columns := aliasNode.Columns(); len(columns) > 0 {
			alias = g.SQL(columns[0])
		}
	} else {
		alias = g.SQL(aliasNode)
	}
	alias = pfx(" AS ", alias)

	var suffix string
	if g.C.UnnestWithOrdinality {
		suffix = iff(exp.Truthy(offset), " WITH ORDINALITY"+alias, alias)
	} else if off, ok := offset.(*exp.Expr); ok {
		suffix = alias + " WITH OFFSET AS " + g.SQL(off)
	} else if exp.Truthy(offset) {
		suffix = alias + " WITH OFFSET"
	} else {
		suffix = alias
	}
	return "UNNEST(" + args + ")" + suffix
}

func (g *Generator) prewhereSQL(e *exp.Expr) string { return "" }

func (g *Generator) whereSQL(e *exp.Expr) string {
	this := g.Indent(g.SQLArg(e, "this"))
	return g.Seg("WHERE") + g.Sep() + this
}

func (g *Generator) windowSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	partition := g.PartitionBySQL(e)
	order := ""
	if o := e.Get("order"); o != nil {
		order = g.OrderSQL(o, true)
	}
	spec := g.SQLArg(e, "spec")
	alias := g.SQLArg(e, "alias")
	over := g.SQLArg(e, "over")
	if over == "" {
		over = "OVER"
	}
	this = this + " " + iff(e.ArgKey() == "windows", "AS", over)

	first := ""
	if f := e.Arg("first"); f != nil {
		first = iff(exp.Truthy(f), "FIRST", "LAST")
	}

	if partition == "" && order == "" && spec == "" && alias != "" {
		// WINDOW w2 AS w1 is unparseable in DuckDB/Postgres; parens are required.
		if e.ArgKey() == "windows" {
			return this + " (" + alias + ")"
		}
		return this + " " + alias
	}
	var args []any
	for _, a := range []string{alias, first, partition, order, spec} {
		if a != "" {
			args = append(args, a)
		}
	}
	return this + " (" + g.FormatArgsSep(" ", args...) + ")"
}

func (g *Generator) partitionBySQL(e *exp.Expr) string {
	return pfx("PARTITION BY ", g.FlatKey(e, "partition_by"))
}

func (g *Generator) partitionbyexcludingSQL(e *exp.Expr) string {
	return "EXCLUDING " + g.Flat(e)
}

func (g *Generator) windowspecSQL(e *exp.Expr) string {
	kind := g.SQLArg(e, "kind")
	start := joinNonEmpty(" ", g.SQLArg(e, "start"), g.SQLArg(e, "start_side"))
	end := joinNonEmpty(" ", g.SQLArg(e, "end"), g.SQLArg(e, "end_side"))
	if end == "" {
		end = "CURRENT ROW"
	}
	windowSpec := kind + " BETWEEN " + start + " AND " + end
	if exclude := g.SQLArg(e, "exclude"); exclude != "" {
		if g.C.SupportsWindowExclude {
			windowSpec += " EXCLUDE " + exclude
		} else {
			g.Unsupported("EXCLUDE clause is not supported in the WINDOW clause")
		}
	}
	return windowSpec
}

func (g *Generator) withingroupSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	expressionSQL := g.SQLArg(e, "expression")
	if expressionSQL != "" {
		expressionSQL = expressionSQL[1:] // order has a leading space
	}
	return this + " WITHIN GROUP (" + expressionSQL + ")"
}

func (g *Generator) betweenSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	low := g.SQLArg(e, "low")
	high := g.SQLArg(e, "high")
	symmetric := e.Arg("symmetric")
	if exp.Truthy(symmetric) && !g.C.SupportsBetweenFlags {
		return "(" + this + " BETWEEN " + low + " AND " + high + " OR " + this + " BETWEEN " + high + " AND " + low + ")"
	}
	flag := ""
	if exp.Truthy(symmetric) {
		flag = " SYMMETRIC"
	} else if b, ok := symmetric.(bool); ok && !b && g.C.SupportsBetweenFlags {
		flag = " ASYMMETRIC"
	}
	return this + " BETWEEN" + flag + " " + low + " AND " + high
}

// bracketOffsetExpressions mirrors bracket_offset_expressions(expression, index_offset=None);
// indexOffset 0 means None.
func (g *Generator) bracketOffsetExpressions(e *exp.Expr, indexOffset int) []*exp.Expr {
	if e.Bool("json_access") {
		return e.Expressions()
	}
	if indexOffset == 0 {
		indexOffset = g.D.IndexOffset
	}
	return ApplyIndexOffset(e.This(), e.Expressions(), indexOffset-e.Int("offset"), g.D)
}

func (g *Generator) bracketSQL(e *exp.Expr) string {
	expressions := g.BracketOffsetExpressions(e, 0)
	var parts []string
	for _, x := range expressions {
		parts = append(parts, g.SQL(x))
	}
	return g.SQLArg(e, "this") + "[" + strings.Join(parts, ", ") + "]"
}

func (g *Generator) allSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	if !e.This().IsAny(exp.Tuple, exp.Paren) {
		this = g.Wrap(this)
	}
	return "ALL " + this
}

func (g *Generator) anySQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	if t := e.This(); t.IsAny(exp.Select, exp.SetOperation, exp.Paren) {
		if t.IsAny(exp.Select, exp.SetOperation) {
			this = g.Wrap(this)
		}
		return "ANY" + this
	}
	return "ANY " + this
}

func (g *Generator) existsSQL(e *exp.Expr) string { return "EXISTS" + g.Wrap(e) }

func (g *Generator) caseSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	statements := []string{iff(this != "", "CASE "+this, "CASE")}
	for _, x := range e.List("ifs") {
		statements = append(statements, "WHEN "+g.SQLArg(x, "this"))
		statements = append(statements, "THEN "+g.SQLArg(x, "true"))
	}
	if def := g.SQLArg(e, "default"); def != "" {
		statements = append(statements, "ELSE "+def)
	}
	statements = append(statements, "END")
	if g.Pretty && g.TooWide(statements) {
		return g.IndentSkip(strings.Join(statements, "\n"), true, true)
	}
	return strings.Join(statements, " ")
}

func (g *Generator) constraintSQL(e *exp.Expr) string {
	return "CONSTRAINT " + g.SQLArg(e, "this") + " " + g.Flat(e)
}

func (g *Generator) nextvalueforSQL(e *exp.Expr) string {
	order := ""
	if o := e.Get("order"); o != nil {
		order = " OVER (" + g.OrderSQL(o, true) + ")"
	}
	return "NEXT VALUE FOR " + g.SQLArg(e, "this") + order
}

func (g *Generator) extractSQL(e *exp.Expr) string {
	this := e.This()
	if g.C.NormalizeExtractDateParts {
		this = MapDatePart(this, g.D)
	}
	var thisSQL string
	if g.C.ExtractAllowsQuotes {
		thisSQL = g.SQL(this)
	} else if this.IsKind(exp.WeekStart) {
		thisSQL = g.WeekstartName(this)
	} else {
		thisSQL = this.Name()
	}
	expressionSQL := g.SQLArg(e, "expression")
	return "EXTRACT(" + thisSQL + " FROM " + expressionSQL + ")"
}

func (g *Generator) trimSQL(e *exp.Expr) string {
	funcName := "TRIM"
	switch g.SQLArg(e, "position") {
	case "LEADING":
		funcName = "LTRIM"
	case "TRAILING":
		funcName = "RTRIM"
	}
	return g.Func(funcName, e.This(), e.Expression())
}

func (g *Generator) convertConcatArgs(e *exp.Expr) []*exp.Expr {
	args := e.Expressions()
	isWs := e.IsKind(exp.ConcatWs)
	if isWs && len(args) > 0 {
		args = args[1:] // Skip the delimiter
	}
	if g.D.StrictStringConcat && e.Bool("safe") {
		cast := make([]*exp.Expr, len(args))
		for i, a := range args {
			cast[i] = exp.NewCast(a, exp.DTypeText)
		}
		args = cast
	}
	concatCoalesce := iff(isWs, g.D.ConcatWsCoalesce, g.D.ConcatCoalesce)
	if !concatCoalesce && e.Bool("coalesce") {
		wrapped := make([]*exp.Expr, len(args))
		for i, a := range args {
			if a.Type() == nil {
				a = AnnotateTypes(a, g.D)
			}
			if a.IsString() || a.IsType(exp.DTypeArray) {
				wrapped[i] = a
				continue
			}
			wrapped[i] = exp.NewFunc("coalesce", a, exp.LiteralString(""))
		}
		args = wrapped
	}
	return args
}

func (g *Generator) concatSQL(e *exp.Expr) string {
	if g.D.ConcatCoalesce && !e.Bool("coalesce") {
		// Dialect's CONCAT function coalesces NULLs to empty strings, but the expression does not.
		// Transpile to double pipe operators, which typically returns NULL if any args are NULL
		// instead of coalescing them to empty string.
		return ConcatToDpipeSQL(g, e)
	}
	expressions := g.ConvertConcatArgs(e)
	// Some dialects don't allow a single-argument CONCAT call
	if !g.C.SupportsSingleArgConcat && len(expressions) == 1 {
		return g.SQL(expressions[0])
	}
	return g.Func("CONCAT", expressions)
}

func (g *Generator) concatwsSQL(e *exp.Expr) string {
	if g.D.ConcatWsCoalesce && !e.Bool("coalesce") {
		// Dialect's CONCAT_WS function skips NULL args, but the expression does not.
		// Wrap the entire call in a CASE expression that returns NULL if any input IS NULL.
		allArgs := e.Expressions()
		e.Set("coalesce", true)
		var conds []any
		for _, a := range allArgs {
			conds = append(conds, a.IsOp(exp.NewNull()))
		}
		return g.SQL(exp.NewCase(nil).When(exp.NewOr(conds...), exp.NewNull()).Else(e))
	}
	return g.Func("CONCAT_WS", seqGet(e.Expressions(), 0), g.ConvertConcatArgs(e))
}

func (g *Generator) checkSQL(e *exp.Expr) string {
	return "CHECK (" + g.SQLArg(e, "this") + ")"
}

func (g *Generator) foreignkeySQL(e *exp.Expr) string {
	this := pfx(" ", g.SQLArg(e, "this"))
	expressions := around(" (", g.Flat(e), ")")
	reference := pfx(" ", g.SQLArg(e, "reference"))
	del := pfx(" ON DELETE ", g.SQLArg(e, "delete"))
	update := pfx(" ON UPDATE ", g.SQLArg(e, "update"))
	options := pfx(" ", g.Expressions(e, ExprsOpts{Key: "options", Flat: true, Sep: " "}))
	return "FOREIGN KEY" + this + expressions + reference + del + update + options
}

func (g *Generator) primarykeySQL(e *exp.Expr) string {
	this := pfx(" ", g.SQLArg(e, "this"))
	expressions := g.Flat(e)
	include := g.SQLArg(e, "include")
	options := pfx(" ", g.Expressions(e, ExprsOpts{Key: "options", Flat: true, Sep: " "}))
	return "PRIMARY KEY" + this + " (" + expressions + ")" + include + options
}

func (g *Generator) timeserieskeySQL(e *exp.Expr) string {
	g.Unsupported("TIMESERIES primary key columns are not supported")
	return g.SQLArg(e, "this")
}

func (g *Generator) ifSQL(e *exp.Expr) string {
	return g.CaseSQL(exp.New(exp.Case, "ifs", []*exp.Expr{e}, "default", e.Arg("false")))
}

func (g *Generator) matchagainstSQL(e *exp.Expr) string {
	var expressions []any
	if g.C.MatchAgainstTablePrefix != nil && *g.C.MatchAgainstTablePrefix != "" {
		for _, x := range e.Expressions() {
			if x.IsKind(exp.Table) {
				expressions = append(expressions, "TABLE "+g.SQL(x))
			} else {
				expressions = append(expressions, x)
			}
		}
	} else {
		for _, x := range e.Expressions() {
			expressions = append(expressions, x)
		}
	}
	modifier := pfx(" ", argStr(e.Arg("modifier")))
	return g.Func("MATCH", expressions...) + " AGAINST(" + g.SQLArg(e, "this") + modifier + ")"
}

func (g *Generator) jsonkeyvalueSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + g.C.JsonKeyValuePairSep + " " + g.SQLArg(e, "expression")
}

func (g *Generator) jsonpathSQL(e *exp.Expr) string {
	path := strings.TrimLeft(g.Expressions(e, ExprsOpts{Sep: EmptySep, Flat: true}), ".")
	if g.C.QuoteJsonPath {
		path = g.EscapeStr(path, true, "", "", false, false)
		path = g.D.QuoteStart + path + g.D.QuoteEnd
	}
	return path
}

// jsonPathPart mirrors json_path_part(expression) for an int, a string or a JSONPathPart.
func (g *Generator) jsonPathPart(part any) string {
	switch x := part.(type) {
	case *exp.Expr:
		if x.Is(exp.JSONPathPart) {
			transform := g.C.Transforms[x.Kind()]
			if transform == nil {
				g.Unsupported("Unsupported JSONPathPart type " + x.ClassName())
				return ""
			}
			return transform(g, x)
		}
	case int:
		return fmt.Sprint(x)
	case string:
		if g.QuoteJSONPathKeyUsingBrackets && g.C.JsonPathSingleQuoteEscape {
			return "'" + strings.ReplaceAll(x, "'", `\'`) + "'"
		}
		return `"` + strings.ReplaceAll(x, `"`, `\"`) + `"`
	}
	return pyStr(part)
}

func (g *Generator) formatjsonSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " FORMAT JSON"
}

func (g *Generator) formatphraseSQL(e *exp.Expr) string {
	// Output the Teradata column FORMAT override.
	this := g.SQLArg(e, "this")
	format := g.SQLArg(e, "format")
	return this + " (FORMAT " + format + ")"
}

// jsonObjectSQL mirrors _jsonobject_sql(expression, name="").
func (g *Generator) jsonObjectSQL(e *exp.Expr, name string) string {
	nullHandling := pfx(" ", argStr(e.Arg("null_handling")))
	uniqueKeys := ""
	if uk := e.Arg("unique_keys"); uk != nil {
		uniqueKeys = " " + iff(exp.Truthy(uk), "WITH", "WITHOUT") + " UNIQUE KEYS"
	}
	returnType := pfx(" RETURNING ", g.SQLArg(e, "return_type"))
	encoding := pfx(" ENCODING ", g.SQLArg(e, "encoding"))
	if name == "" {
		name = iff(e.IsKind(exp.JSONObject), "JSON_OBJECT", "JSON_OBJECTAGG")
	}
	return g.FuncFull(name, []any{e.Expressions()}, "(", nullHandling+uniqueKeys+returnType+encoding+")", false)
}

func (g *Generator) jsonarraySQL(e *exp.Expr) string {
	nullHandling := pfx(" ", argStr(e.Arg("null_handling")))
	returnType := pfx(" RETURNING ", g.SQLArg(e, "return_type"))
	strict := iff(e.Bool("strict"), " STRICT", "")
	return g.FuncFull("JSON_ARRAY", []any{e.Expressions()}, "(", nullHandling+returnType+strict+")", false)
}

func (g *Generator) jsonarrayaggSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	order := g.SQLArg(e, "order")
	nullHandling := pfx(" ", argStr(e.Arg("null_handling")))
	returnType := pfx(" RETURNING ", g.SQLArg(e, "return_type"))
	strict := iff(e.Bool("strict"), " STRICT", "")
	return g.FuncFull("JSON_ARRAYAGG", []any{this}, "(", order+nullHandling+returnType+strict+")", false)
}

func (g *Generator) jsoncolumndefSQL(e *exp.Expr) string {
	path := pfx(" PATH ", g.SQLArg(e, "path"))
	if nestedSchema := g.SQLArg(e, "nested_schema"); nestedSchema != "" {
		return "NESTED" + path + " " + nestedSchema
	}
	this := g.SQLArg(e, "this")
	kind := pfx(" ", g.SQLArg(e, "kind"))
	formatJSON := iff(e.Bool("format_json"), " FORMAT JSON", "")
	ordinality := iff(e.Bool("ordinality"), " FOR ORDINALITY", "")
	return this + kind + formatJSON + path + ordinality
}

func (g *Generator) jsonschemaSQL(e *exp.Expr) string {
	return g.Func("COLUMNS", e.Expressions())
}

func (g *Generator) jsontableSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	path := pfx(", ", g.SQLArg(e, "path"))
	errorHandling := pfx(" ", argStr(e.Arg("error_handling")))
	emptyHandling := pfx(" ", argStr(e.Arg("empty_handling")))
	schema := g.SQLArg(e, "schema")
	return g.FuncFull("JSON_TABLE", []any{this}, "(", path+errorHandling+emptyHandling+" "+schema+")", false)
}

func (g *Generator) openjsoncolumndefSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	kind := g.SQLArg(e, "kind")
	path := pfx(" ", g.SQLArg(e, "path"))
	asJSON := iff(e.Bool("as_json"), " AS JSON", "")
	return this + " " + kind + path + asJSON
}

func (g *Generator) openjsonSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	path := pfx(", ", g.SQLArg(e, "path"))
	expressions := g.Expressions(e, ExprsOpts{})
	with := ""
	if expressions != "" {
		with = " WITH (" + g.SegWith(g.Indent(expressions), "") + g.SegWith(")", "")
	}
	return "OPENJSON(" + this + path + ")" + with
}

func (g *Generator) inSQL(e *exp.Expr) string {
	query := e.Get("query")
	unnest := e.Get("unnest")
	field := e.Get("field")
	isGlobal := iff(e.Bool("is_global"), " GLOBAL", "")
	var inSQL string
	if query != nil {
		inSQL = g.SQL(query)
	} else if unnest != nil {
		inSQL = g.InUnnestOp(unnest)
	} else if field != nil {
		inSQL = g.SQL(field)
	} else {
		inSQL = "(" + g.Expressions(e, ExprsOpts{Dynamic: true, NewLine: true, SkipFirst: true, SkipLast: true}) + ")"
	}
	return g.SQLArg(e, "this") + isGlobal + " IN " + inSQL
}

func (g *Generator) inUnnestOp(unnest *exp.Expr) string {
	return "(SELECT " + g.SQL(unnest) + ")"
}

func (g *Generator) intervalSQL(e *exp.Expr) string {
	includeKeyword := !g.C.AutoRefreshBareIntervals ||
		!e.FindAncestor(exp.AutoRefreshProperty, exp.Select).IsKind(exp.AutoRefreshProperty)
	intervalKeyword := iff(includeKeyword, "INTERVAL", "")
	unitExpression := e.Get("unit")
	unit := ""
	if unitExpression != nil {
		unit = g.SQL(unitExpression)
	}
	if !g.C.IntervalAllowsPluralForm {
		if s, ok := g.C.TimePartSingulars[unit]; ok {
			unit = s
		}
	}
	unit = pfx(" ", unit)

	if g.C.SingleStringInterval {
		this := ""
		if e.This() != nil {
			this = e.This().Name()
		}
		if this != "" {
			intervalKeyword = sfx(intervalKeyword, " ")
			if unitExpression.IsKind(exp.IntervalSpan) {
				return intervalKeyword + g.SQL(exp.LiteralString(this)) + unit
			}
			return intervalKeyword + g.SQL(exp.LiteralString(this+unit))
		}
		return intervalKeyword + unit
	}

	this := g.SQLArg(e, "this")
	if this != "" {
		if !includeKeyword && e.This().IsString() {
			this = e.This().Name()
		}
		if !g.C.UnwrappedIntervalValues.HasAny(e.This().Kind()) {
			this = "(" + this + ")"
		}
		if includeKeyword {
			this = " " + this
		}
	}
	return intervalKeyword + this + unit
}

func (g *Generator) returnSQL(e *exp.Expr) string { return "RETURN " + g.SQLArg(e, "this") }

func (g *Generator) referenceSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	expressions := around("(", g.Flat(e), ")")
	options := pfx(" ", g.Expressions(e, ExprsOpts{Key: "options", Flat: true, Sep: " "}))
	return "REFERENCES " + this + expressions + options
}

func (g *Generator) anonymousSQL(e *exp.Expr) string {
	// We don't normalize qualified functions such as a.b.foo(), because they can be case-sensitive
	parent := e.Parent()
	isQualified := parent.IsKind(exp.Dot) && e == parent.Expression()
	return g.FuncFull(g.SQLArg(e, "this"), argList(e, "expressions"), "(", ")", isQualified)
}

func (g *Generator) parenSQL(e *exp.Expr) string {
	sql := g.SegWith(g.Indent(g.SQLArg(e, "this")), "")
	return "(" + sql + g.SegWith(")", "")
}

func (g *Generator) negSQL(e *exp.Expr) string {
	// This makes sure we don't convert "- - 5" to "--5", which is a comment
	thisSQL := g.SQLArg(e, "this")
	sep := iff(strings.HasPrefix(thisSQL, "-"), " ", "")
	return "-" + sep + thisSQL
}

func (g *Generator) notSQL(e *exp.Expr) string { return "NOT " + g.SQLArg(e, "this") }

func (g *Generator) aliasSQL(e *exp.Expr) string {
	alias := pfx(" AS ", g.SQLArg(e, "alias"))
	return g.SQLArg(e, "this") + alias
}

func (g *Generator) pivotaliasSQL(e *exp.Expr) string {
	alias := e.Get("alias")
	parent := e.Parent()
	pivot := parent.Parent()
	if pivot.IsKind(exp.Pivot) && pivot.Unpivot() {
		identifierAlias := alias.IsKind(exp.Identifier)
		literalAlias := alias.IsKind(exp.Literal)
		if identifierAlias && !g.C.UnpivotAliasesAreIdentifiers {
			alias.Replace(exp.LiteralString(alias.OutputName()))
		} else if !identifierAlias && literalAlias && g.C.UnpivotAliasesAreIdentifiers {
			alias.Replace(exp.ToIdentifier(alias.OutputName()))
		}
	}
	return g.AliasSQL(e)
}

func (g *Generator) aliasesSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " AS (" + g.Flat(e) + ")"
}

func (g *Generator) atindexSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " AT " + g.SQLArg(e, "expression")
}

func (g *Generator) attimezoneSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " AT TIME ZONE " + g.SQLArg(e, "zone")
}

func (g *Generator) fromtimezoneSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " AT TIME ZONE " + g.SQLArg(e, "zone") + " AT TIME ZONE 'UTC'"
}

func (g *Generator) fromiso8601dateSQL(e *exp.Expr) string {
	return g.SQL(exp.NewCast(e.This(), exp.DTypeDate))
}

func (g *Generator) fromiso8601timestampSQL(e *exp.Expr) string {
	return g.SQL(exp.NewCast(e.This(), exp.DTypeTimestamptz))
}

func (g *Generator) fromiso8601timestampnanosSQL(e *exp.Expr) string {
	return g.SQL(exp.NewCast(e.This(), exp.DTypeTimestamptz))
}

func (g *Generator) addSQL(e *exp.Expr) string { return g.Binary(e, "+") }

// ConnectorStack is the explicit stack used by connector_sql (strings and expressions).
type ConnectorStack = []any

//gen:dispatch g.AndSQL(e, nil)
func (g *Generator) andSQL(e *exp.Expr, stack *ConnectorStack) string {
	return g.ConnectorSQL(e, "AND", stack)
}

//gen:dispatch g.OrSQL(e, nil)
func (g *Generator) orSQL(e *exp.Expr, stack *ConnectorStack) string {
	return g.ConnectorSQL(e, "OR", stack)
}

//gen:dispatch g.XorSQL(e, nil)
func (g *Generator) xorSQL(e *exp.Expr, stack *ConnectorStack) string {
	return g.ConnectorSQL(e, "XOR", stack)
}

// connectorMethod is getattr(self, f"{node.key}_sql") for connectors.
func (g *Generator) connectorMethod(node *exp.Expr, stack *ConnectorStack) string {
	switch node.Kind() {
	case exp.And:
		return g.AndSQL(node, stack)
	case exp.Or:
		return g.OrSQL(node, stack)
	case exp.Xor:
		return g.XorSQL(node, stack)
	}
	return g.ConnectorSQL(node, strings.ToUpper(node.Key()), stack)
}

//gen:dispatch g.ConnectorSQL(e, "", nil)
func (g *Generator) connectorSQL(e *exp.Expr, op string, stack *ConnectorStack) string {
	if stack != nil {
		*stack = append(*stack, e.Right())
		if len(e.Comments) > 0 && g.Comments {
			op = g.MaybeCommentWith(op, e.Comments)
		}
		*stack = append(*stack, op, e.Left())
		return op
	}

	st := ConnectorStack{e}
	var sqls []string
	ops := map[string]bool{}
	for len(st) > 0 {
		node := st[len(st)-1]
		st = st[:len(st)-1]
		if n, ok := node.(*exp.Expr); ok && n.Is(exp.Connector) {
			ops[g.connectorMethod(n, &st)] = true
			continue
		}
		var sql string
		switch n := node.(type) {
		case string:
			sql = n
		case *exp.Expr:
			sql = g.SQL(n)
		}
		if len(sqls) > 0 && ops[sqls[len(sqls)-1]] {
			sqls[len(sqls)-1] += " " + sql
		} else {
			sqls = append(sqls, sql)
		}
	}
	sep := iff(g.Pretty && g.TooWide(sqls), "\n", " ")
	return strings.Join(sqls, sep)
}

func (g *Generator) bitwiseandSQL(e *exp.Expr) string { return g.Binary(e, "&") }

func (g *Generator) bitwiseleftshiftSQL(e *exp.Expr) string { return g.Binary(e, "<<") }

func (g *Generator) bitwisenotSQL(e *exp.Expr) string { return "~" + g.SQLArg(e, "this") }

func (g *Generator) bitwiseorSQL(e *exp.Expr) string { return g.Binary(e, "|") }

func (g *Generator) bitwiserightshiftSQL(e *exp.Expr) string { return g.Binary(e, ">>") }

func (g *Generator) bitwisexorSQL(e *exp.Expr) string { return g.Binary(e, "^") }

//gen:dispatch g.CastSQL(e, "")
func (g *Generator) castSQL(e *exp.Expr, safePrefix string) string {
	formatSQL := pfx(" FORMAT ", g.SQLArg(e, "format"))
	toSQL := pfx(" ", g.SQLArg(e, "to"))
	action := pfx(" ", g.SQLArg(e, "action"))
	def := g.SQLArg(e, "default")
	def = iff(def != "", " DEFAULT "+def+" ON CONVERSION ERROR", "")
	return safePrefix + "CAST(" + g.SQLArg(e, "this") + " AS" + toSQL + def + formatSQL + action + ")"
}

// Base implementation that excludes safe, zone, and target_type metadata args
func (g *Generator) strtotimeSQL(e *exp.Expr) string {
	return g.Func("STR_TO_TIME", e.This(), e.Get("format"))
}

// Base implementation that excludes the safe and default_year metadata args
func (g *Generator) strtodateSQL(e *exp.Expr) string {
	return g.Func("STR_TO_DATE", e.This(), e.Get("format"))
}

func (g *Generator) parsedatetimeSQL(e *exp.Expr) string {
	return g.Func("PARSE_DATETIME", e.This(), e.Get("format"), e.Get("zone"))
}

func (g *Generator) currentdateSQL(e *exp.Expr) string {
	if zone := g.SQLArg(e, "this"); zone != "" {
		return "CURRENT_DATE(" + zone + ")"
	}
	return "CURRENT_DATE"
}

func (g *Generator) collateSQL(e *exp.Expr) string {
	if g.C.CollateIsFunc {
		return g.FunctionFallbackSQL(e)
	}
	return g.Binary(e, "COLLATE")
}

func (g *Generator) commandSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " " + pyStrip(e.Text("expression"))
}

func (g *Generator) commentSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	kind := pyStr(e.Arg("kind"))
	materialized := iff(e.Bool("materialized"), " MATERIALIZED", "")
	existsSQL := iff(e.Bool("exists"), " IF EXISTS ", " ")
	expressionSQL := g.SQLArg(e, "expression")
	return "COMMENT" + existsSQL + "ON" + materialized + " " + kind + " " + this + " IS " + expressionSQL
}

func (g *Generator) mergetreettlactionSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	del := iff(e.Bool("delete"), " DELETE", "")
	recompress := pfx(" RECOMPRESS ", g.SQLArg(e, "recompress"))
	toDisk := pfx(" TO DISK ", g.SQLArg(e, "to_disk"))
	toVolume := pfx(" TO VOLUME ", g.SQLArg(e, "to_volume"))
	return this + del + recompress + toDisk + toVolume
}

func (g *Generator) mergetreettlSQL(e *exp.Expr) string {
	where := g.SQLArg(e, "where")
	group := g.SQLArg(e, "group")
	aggregates := g.ExprsKey(e, "aggregates")
	if aggregates != "" {
		aggregates = g.Seg("SET") + g.Seg(aggregates)
	}
	if where == "" && group == "" && aggregates == "" && len(e.Expressions()) == 1 {
		return "TTL " + g.Flat(e)
	}
	return "TTL" + g.Seg(g.Expressions(e, ExprsOpts{})) + where + group + aggregates
}

func (g *Generator) transactionSQL(e *exp.Expr) string {
	return "BEGIN" + pfx(" ", g.ExprsKey(e, "modes"))
}

func (g *Generator) commitSQL(e *exp.Expr) string {
	chain := ""
	if c := e.Arg("chain"); c != nil {
		chain = iff(exp.Truthy(c), " AND CHAIN", " AND NO CHAIN")
	}
	return "COMMIT" + chain
}

func (g *Generator) rollbackSQL(e *exp.Expr) string {
	return "ROLLBACK" + pfx(" TO ", argStr(e.Arg("savepoint")))
}

func (g *Generator) altercolumnSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	exists := ""
	if e.Bool("exists") {
		if g.C.SupportsAlterColumnIfExists {
			exists = " IF EXISTS"
		} else {
			g.Unsupported("ALTER COLUMN IF EXISTS is not supported by this dialect")
		}
	}

	if dtype := g.SQLArg(e, "dtype"); dtype != "" {
		collate := pfx(" COLLATE ", g.SQLArg(e, "collate"))
		using := pfx(" USING ", g.SQLArg(e, "using"))
		alterSetType := sfx(g.C.AlterSetType, " ")
		nullConstraint := g.AlterColumnNullConstraintSQL(e)
		return "ALTER COLUMN" + exists + " " + this + " " + alterSetType + dtype + collate + using + nullConstraint
	}

	if def := g.SQLArg(e, "default"); def != "" {
		return "ALTER COLUMN" + exists + " " + this + " SET DEFAULT " + def
	}
	if comment := g.SQLArg(e, "comment"); comment != "" {
		return "ALTER COLUMN" + exists + " " + this + " COMMENT " + comment
	}
	if visible := e.Arg("visible"); exp.Truthy(visible) {
		return "ALTER COLUMN" + exists + " " + this + " SET " + pyStr(visible)
	}

	allowNull := e.Arg("allow_null")
	drop := e.Bool("drop")
	if !drop && !exp.Truthy(allowNull) {
		g.Unsupported("Unsupported ALTER COLUMN syntax")
	}
	if allowNull != nil {
		keyword := iff(drop, "DROP", "SET")
		return "ALTER COLUMN" + exists + " " + this + " " + keyword + " NOT NULL"
	}
	return "ALTER COLUMN" + exists + " " + this + " DROP DEFAULT"
}

func (g *Generator) alterColumnNullConstraintSQL(e *exp.Expr) string {
	allowNull := e.Arg("allow_null")
	if allowNull == nil {
		return ""
	}
	if !g.C.SupportsAlterColumnNullability {
		g.Unsupported("ALTER COLUMN cannot set nullability along with a type")
		return ""
	}
	return iff(exp.Truthy(allowNull), " NULL", " NOT NULL")
}

func (g *Generator) modifycolumnSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	if renameFrom := g.SQLArg(e, "rename_from"); renameFrom != "" {
		if !g.C.SupportsChangeColumn {
			g.Unsupported("CHANGE COLUMN is not supported in this dialect")
		}
		return "CHANGE COLUMN " + renameFrom + " " + this
	}
	if !g.C.SupportsModifyColumn {
		g.Unsupported("MODIFY COLUMN is not supported in this dialect")
	}
	return "MODIFY COLUMN " + this
}

func (g *Generator) alterindexSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	visibleSQL := iff(e.Bool("visible"), "VISIBLE", "INVISIBLE")
	return "ALTER INDEX " + this + " " + visibleSQL
}

func (g *Generator) alterdiststyleSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	if !e.This().IsKind(exp.Var) {
		this = "KEY DISTKEY " + this
	}
	return "ALTER DISTSTYLE " + this
}

func (g *Generator) altersortkeySQL(e *exp.Expr) string {
	compound := iff(e.Bool("compound"), " COMPOUND", "")
	this := g.SQLArg(e, "this")
	expressions := around("(", g.Flat(e), ")")
	return "ALTER" + compound + " SORTKEY " + iff(this != "", this, expressions)
}

//gen:dispatch g.AlterrenameSQL(e, true)
func (g *Generator) alterrenameSQL(e *exp.Expr, includeTo bool) string {
	if !g.C.RenameTableWithDb {
		// Remove db from tables
		e = e.Transform(func(n *exp.Expr) *exp.Expr {
			if n.IsKind(exp.Table) {
				return exp.NewTable(n.This())
			}
			return n
		}, true).AssertIs(exp.AlterRename)
	}
	this := g.SQLArg(e, "this")
	toKW := iff(includeTo, " TO", "")
	return "RENAME" + toKW + " " + this
}

func (g *Generator) renamecolumnSQL(e *exp.Expr) string {
	exists := iff(e.Bool("exists"), " IF EXISTS", "")
	oldColumn := g.SQLArg(e, "this")
	newColumn := g.SQLArg(e, "to")
	return "RENAME COLUMN" + exists + " " + oldColumn + " TO " + newColumn
}

func (g *Generator) altersetSQL(e *exp.Expr) string {
	exprs := g.Flat(e)
	if g.C.AlterSetWrapped {
		exprs = "(" + exprs + ")"
	}
	return "SET " + exprs
}

func (g *Generator) alterSQL(e *exp.Expr) string {
	actions := e.Actions()
	var actionsSQL string
	if !g.D.AlterTableAddRequiredForEachColumn && len(actions) > 0 && actions[0].IsKind(exp.ColumnDef) {
		actionsSQL = "ADD " + g.FlatKey(e, "actions")
	} else {
		var list []any
		for _, action := range actions {
			var actionSQL string
			if action.IsAny(exp.ColumnDef, exp.Schema) {
				actionSQL = g.AddColumnSQL(action)
			} else {
				actionSQL = g.SQL(action)
				if action.Is(exp.Query) {
					actionSQL = "AS " + actionSQL
				}
			}
			list = append(list, actionSQL)
		}
		actionsSQL = strings.TrimLeft(g.FormatArgs(list...), "\n")
	}

	iceberg := iff(e.Bool("iceberg") && g.C.SupportsDropAlterIcebergProperty, "ICEBERG ", "")
	exists := iff(e.Bool("exists"), " IF EXISTS", "")
	onCluster := pfx(" ", g.SQLArg(e, "cluster"))
	only := iff(e.Bool("only"), " ONLY", "")
	options := e.List("options")
	var nonPartition, partition []*exp.Expr
	for _, o := range options {
		if g.C.AlterPartitionOptions.HasAny(o.Kind()) {
			partition = append(partition, o)
		} else {
			nonPartition = append(nonPartition, o)
		}
	}
	optionsSQL := g.ExprsList(nonPartition, ExprsOpts{})
	if optionsSQL != "" && actionsSQL != "" {
		optionsSQL = ", " + optionsSQL
	}
	partitionsSQL := g.ExprsList(partition, ExprsOpts{Sep: " ", Flat: true})
	if partitionsSQL != "" && (actionsSQL != "" || optionsSQL != "") {
		partitionsSQL = " " + partitionsSQL
	}
	kind := g.SQLArg(e, "kind")
	notValid := iff(e.Bool("not_valid"), " NOT VALID", "")
	check := iff(e.Bool("check"), " WITH CHECK", "")
	cascade := iff(e.Bool("cascade") && g.D.AlterTableSupportsCascade, " CASCADE", "")
	this := pfx(" ", g.SQLArg(e, "this"))
	return "ALTER " + iceberg + kind + exists + only + this + onCluster + check + g.Sep() + actionsSQL + notValid +
		optionsSQL + partitionsSQL + cascade
}

func (g *Generator) altersessionSQL(e *exp.Expr) string {
	itemsSQL := g.Flat(e)
	keyword := iff(e.Bool("unset"), "UNSET", "SET")
	return keyword + " " + itemsSQL
}

func (g *Generator) addColumnSQL(e *exp.Expr) string {
	columnText := ""
	if g.C.AlterTableIncludeColumnKeyword {
		if e.IsKind(exp.ColumnDef) {
			columnText = " COLUMN"
		} else if e.IsKind(exp.Schema) {
			columnText = " COLUMNS"
		}
	}
	var sql string
	if e.IsKind(exp.Schema) && !g.C.AlterTableAddColumnsWrapped {
		sql = g.Flat(e)
	} else {
		sql = g.SQL(e)
	}
	return "ADD" + columnText + " " + sql
}

func (g *Generator) droppartitionSQL(e *exp.Expr) string {
	expressions := g.Expressions(e, ExprsOpts{})
	exists := iff(e.Bool("exists"), " IF EXISTS ", " ")
	return "DROP" + exists + expressions
}

func (g *Generator) dropprimarykeySQL(e *exp.Expr) string { return "DROP PRIMARY KEY" }

func (g *Generator) addconstraintSQL(e *exp.Expr) string {
	return "ADD " + g.Expressions(e, ExprsOpts{NoIndent: true})
}

func (g *Generator) replacecolumnsSQL(e *exp.Expr) string {
	return "REPLACE COLUMNS (" + g.Flat(e) + ")"
}

func (g *Generator) addpartitionSQL(e *exp.Expr) string {
	exists := iff(e.Bool("exists"), "IF NOT EXISTS ", "")
	location := pfx(" ", g.SQLArg(e, "location"))
	return "ADD " + exists + g.SQL(e.This()) + location
}

func (g *Generator) distinctSQL(e *exp.Expr) string {
	this := g.Flat(e)
	if !g.C.MultiArgDistinct && len(e.Expressions()) > 1 {
		c := exp.NewCase(nil)
		for _, arg := range e.Expressions() {
			c = c.When(arg.IsOp(exp.NewNull()), exp.NewNull())
		}
		this = g.SQL(c.Else("(" + this + ")"))
	}
	this = pfx(" ", this)
	on := pfx(" ON ", g.SQLArg(e, "on"))
	return "DISTINCT" + this + on
}

func (g *Generator) ignorenullsSQL(e *exp.Expr) string {
	return g.EmbedIgnoreNulls(e, "IGNORE NULLS")
}

func (g *Generator) respectnullsSQL(e *exp.Expr) string {
	return g.EmbedIgnoreNulls(e, "RESPECT NULLS")
}

func (g *Generator) havingmaxSQL(e *exp.Expr) string {
	thisSQL := g.SQLArg(e, "this")
	expressionSQL := g.SQLArg(e, "expression")
	kind := iff(e.Bool("max"), "MAX", "MIN")
	return thisSQL + " HAVING " + kind + " " + expressionSQL
}

func (g *Generator) intdivSQL(e *exp.Expr) string {
	return g.TruncatedDivSQL(e.This(), e.Expression())
}

func (g *Generator) truncatedDivSQL(this, divisor *exp.Expr) string {
	div := exp.New(exp.Div, "this", this, "expression", divisor)
	if g.C.CastToIntRounds {
		div = exp.New(exp.Trunc, "this", div)
	}
	return g.SQL(exp.New(exp.Cast, "this", div, "to", exp.New(exp.DataType, "this", exp.DTypeBigint)))
}

func (g *Generator) dpipeSQL(e *exp.Expr) string {
	if g.D.StrictStringConcat && e.Bool("safe") {
		var args []any
		for _, x := range e.Flatten(true) {
			args = append(args, exp.NewCast(x, exp.DTypeText))
		}
		return g.Func("CONCAT", args...)
	}
	return g.Binary(e, "||")
}

func (g *Generator) divSQL(e *exp.Expr) string {
	l, r := e.Left(), e.Right()

	if !g.D.SafeDivision && e.Bool("safe") {
		r.Replace(exp.New(exp.Nullif, "this", r.Copy(), "expression", exp.LiteralNumber(0)))
	}

	if g.D.TypedDivision && !e.Bool("typed") {
		if !l.IsTypeIn(exp.DataTypeRealTypes) && !r.IsTypeIn(exp.DataTypeRealTypes) {
			l.Replace(exp.NewCast(l.Copy(), exp.DTypeDouble))
		}
	} else if !g.D.TypedDivision && e.Bool("typed") {
		if l.IsTypeIn(exp.DataTypeIntegerTypes) && r.IsTypeIn(exp.DataTypeIntegerTypes) {
			// Native integer division depends on the target's operand types, which may differ
			// from the source's (e.g. FLOOR(int) is DOUBLE in DuckDB), unless the operands are
			// integer literals or casts
			allIntOrCast := (l.IsInt() || l.IsKind(exp.Cast)) && (r.IsInt() || r.IsKind(exp.Cast))
			if allIntOrCast && (e.Bool("safe") || !g.C.SafeIntDivision) {
				return g.SQL(exp.New(exp.IntDiv, "this", l, "expression", r))
			}
			return g.TruncatedDivSQL(l, r)
		}
	}
	return g.Binary(e, "/")
}

func (g *Generator) safedivideSQL(e *exp.Expr) string {
	n := exp.Wrap(e.This(), exp.Binary)
	d := exp.Wrap(e.Expression(), exp.Binary)
	return g.SQL(exp.New(exp.If, "this", d.Neq(0), "true", n.Over(d), "false", exp.NewNull()))
}

func (g *Generator) overlapsSQL(e *exp.Expr) string { return g.Binary(e, "OVERLAPS") }

func (g *Generator) distanceSQL(e *exp.Expr) string { return g.Binary(e, "<->") }

func (g *Generator) distancendSQL(e *exp.Expr) string { return g.Binary(e, "<<->>") }

func (g *Generator) dotSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + "." + g.SQLArg(e, "expression")
}

func (g *Generator) eqSQL(e *exp.Expr) string { return g.Binary(e, "=") }

func (g *Generator) propertyeqSQL(e *exp.Expr) string { return g.Binary(e, ":=") }

func (g *Generator) escapeSQL(e *exp.Expr) string {
	this := e.This()
	if this.IsAny(exp.Like, exp.ILike) && this.Expression().IsAny(exp.All, exp.Any) && !g.C.SupportsLikeQuantifiers {
		return g.LikeSQLFull(this, e)
	}
	return g.Binary(e, "ESCAPE")
}

func (g *Generator) globSQL(e *exp.Expr) string { return g.Binary(e, "GLOB") }

func (g *Generator) gtSQL(e *exp.Expr) string { return g.Binary(e, ">") }

func (g *Generator) gteSQL(e *exp.Expr) string { return g.Binary(e, ">=") }

func (g *Generator) isSQL(e *exp.Expr) string {
	negate := e.Bool("negate")
	if !g.C.IsBoolAllowed && e.Expression().IsKind(exp.Boolean) {
		positive := e.Expression().Bool("this") != negate
		if positive {
			return g.SQL(e.This())
		}
		return g.SQL(exp.NewNot(e.This()))
	}
	return g.Binary(e, iff(negate, "IS NOT", "IS"))
}

// likeSQLFull mirrors _like_sql(expression, escape=None).
func (g *Generator) likeSQLFull(e *exp.Expr, escape *exp.Expr) string {
	this := e.This()
	rhs := e.Expression()
	expClass := exp.ILike
	op := "ILIKE"
	if e.IsKind(exp.Like) {
		expClass = exp.Like
		op = "LIKE"
	}
	if e.Bool("negate") {
		op = "NOT " + op
	}

	if rhs.IsAny(exp.All, exp.Any) && !g.C.SupportsLikeQuantifiers {
		exprsNode := rhs.This().Unnest()
		var exprs []*exp.Expr
		if exprsNode.IsKind(exp.Tuple) {
			exprs = exprsNode.Expressions()
		} else {
			exprs = []*exp.Expr{exprsNode}
		}
		makeLike := func(x *exp.Expr) *exp.Expr {
			like := exp.New(expClass, "this", this, "expression", x, "negate", e.Arg("negate"))
			if escape != nil {
				like = exp.New(exp.Escape, "this", like, "expression", escape.Expression().Copy())
			}
			return like
		}
		likeExpr := makeLike(exprs[0])
		for _, x := range exprs[1:] {
			if rhs.IsKind(exp.Any) {
				likeExpr = exp.NewOr(likeExpr, makeLike(x), exp.WithCopy(false))
			} else {
				likeExpr = exp.NewAnd(likeExpr, makeLike(x), exp.WithCopy(false))
			}
		}
		parent := e.Parent()
		if escape != nil {
			parent = escape.Parent()
		}
		if !(parent.Is(likeExpr.Kind()) || parent.IsKind(exp.Paren)) && parent.Is(exp.Condition) {
			likeExpr = exp.NewParen(likeExpr, exp.WithCopy(false))
		}
		return g.SQL(likeExpr)
	}
	return g.Binary(e, op)
}

func (g *Generator) likeSQL(e *exp.Expr) string { return g.LikeSQLFull(e, nil) }

func (g *Generator) ilikeSQL(e *exp.Expr) string { return g.LikeSQLFull(e, nil) }

func (g *Generator) matchSQL(e *exp.Expr) string {
	if e.Bool("kind") || e.Bool("analyzer") {
		g.Unsupported("MATCH variants and USING ANALYZER are not supported in this dialect")
	}
	return g.Binary(e, "MATCH")
}

func (g *Generator) similartoSQL(e *exp.Expr) string { return g.Binary(e, "SIMILAR TO") }

func (g *Generator) ltSQL(e *exp.Expr) string { return g.Binary(e, "<") }

func (g *Generator) lteSQL(e *exp.Expr) string { return g.Binary(e, "<=") }

func (g *Generator) modSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	expr := g.SQLArg(e, "expression")
	sql := this + " " + g.MaybeCommentWith(g.C.ModOperator, e.Comments) + " " + expr
	parent := e.Parent()
	if parent != nil && g.C.ModParenParentTypes.HasAny(parent.Kind()) && parent.Expression() == e {
		return "(" + sql + ")"
	}
	return sql
}

func (g *Generator) mulSQL(e *exp.Expr) string { return g.Binary(e, "*") }

func (g *Generator) neqSQL(e *exp.Expr) string { return g.Binary(e, "<>") }

func (g *Generator) nullsafeeqSQL(e *exp.Expr) string { return g.Binary(e, "IS NOT DISTINCT FROM") }

func (g *Generator) nullsafeneqSQL(e *exp.Expr) string { return g.Binary(e, "IS DISTINCT FROM") }

func (g *Generator) subSQL(e *exp.Expr) string { return g.Binary(e, "-") }

func (g *Generator) trycastSQL(e *exp.Expr) string { return g.CastSQL(e, "TRY_") }

func (g *Generator) jsoncastSQL(e *exp.Expr) string { return g.CastSQL(e, "") }

func (g *Generator) trySQL(e *exp.Expr) string {
	if !g.C.TrySupported {
		g.Unsupported("Unsupported TRY function")
		return g.SQLArg(e, "this")
	}
	return g.Func("TRY", e.This())
}

func (g *Generator) logSQL(e *exp.Expr) string {
	this := e.This()
	expr := e.Expression()
	if lbf := g.D.LogBaseFirst; lbf != nil && !*lbf {
		this, expr = expr, this
	} else if lbf == nil && expr != nil {
		if n := this.Name(); n == "2" || n == "10" {
			return g.Func("LOG"+n, expr)
		}
		g.Unsupported("Unsupported logarithm with base " + g.SQL(this))
	}
	return g.Func("LOG", this, expr)
}

func (g *Generator) useSQL(e *exp.Expr) string {
	kind := pfx(" ", g.SQLArg(e, "kind"))
	this := g.SQLArg(e, "this")
	if this == "" {
		this = g.Flat(e)
	}
	return "USE" + kind + pfx(" ", this)
}
