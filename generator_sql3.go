package sqlglot

// Generator methods translated from sqlglot/generator.py (table_parts .. after_limit_modifiers).

import (
	"fmt"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
)

func (g *Generator) tableParts(e *exp.Expr) string {
	var parts []string
	for _, k := range []string{"catalog", "db", "this"} {
		if v := e.Arg(k); v != nil {
			parts = append(parts, g.SQLAny(v))
		}
	}
	return strings.Join(parts, ".")
}

//gen:dispatch g.TableSQL(e, " AS ")
func (g *Generator) tableSQL(e *exp.Expr, sep string) string {
	table := g.TableParts(e)
	only := iff(e.Bool("only"), "ONLY ", "")
	partition := pfx(" ", g.SQLArg(e, "partition"))
	version := pfx(" ", g.SQLArg(e, "version"))
	alias := pfx(sep, g.SQLArg(e, "alias"))

	sample := g.SQLArg(e, "sample")
	postAlias, preAlias := "", ""
	if g.D.AliasPostTablesample {
		preAlias = sample
	} else {
		postAlias = sample
	}
	if g.D.AliasPostVersion {
		preAlias += version
	} else {
		postAlias += version
	}

	hints := g.Expressions(e, ExprsOpts{Key: "hints", Sep: " "})
	hints = iff(hints != "" && g.C.TableHints, " "+hints, "")
	pivots := g.Expressions(e, ExprsOpts{Key: "pivots", Sep: EmptySep, Flat: true})
	joins := g.IndentSkip(g.Expressions(e, ExprsOpts{Key: "joins", Sep: EmptySep, Flat: true}), true, false)
	laterals := g.Expressions(e, ExprsOpts{Key: "laterals", Sep: EmptySep})

	fileFormat := g.SQLArg(e, "format")
	pattern := g.SQLArg(e, "pattern")
	if fileFormat != "" {
		pattern = pfx(", PATTERN => ", pattern)
		fileFormat = " (FILE_FORMAT => " + fileFormat + pattern + ")"
	} else if pattern != "" {
		fileFormat = " (PATTERN => " + pattern + ")"
	}

	ordinality := ""
	if e.Bool("ordinality") {
		ordinality = " WITH ORDINALITY" + alias
		alias = ""
	}

	if when := g.SQLArg(e, "when"); when != "" {
		if g.C.HistoricalDataPostAlias {
			alias = alias + " " + when
		} else {
			table = table + " " + when
		}
	}

	changes := pfx(" ", g.SQLArg(e, "changes"))

	if rowsFrom := g.ExprsKey(e, "rows_from"); rowsFrom != "" {
		table = "ROWS FROM " + g.Wrap(rowsFrom)
	}

	indexed := ""
	if v := e.Arg("indexed"); v != nil {
		indexed = iff(exp.Truthy(v), " INDEXED BY "+g.SQLAny(v), " NOT INDEXED")
	}

	return only + table + changes + partition + fileFormat + preAlias + alias + indexed + hints + pivots + postAlias +
		joins + laterals + ordinality
}

func (g *Generator) tablefromrowsSQL(e *exp.Expr) string {
	table := g.Func("TABLE", e.This())
	alias := pfx(" AS ", g.SQLArg(e, "alias"))
	sample := g.SQLArg(e, "sample")
	pivots := g.Expressions(e, ExprsOpts{Key: "pivots", Sep: EmptySep, Flat: true})
	joins := g.IndentSkip(g.Expressions(e, ExprsOpts{Key: "joins", Sep: EmptySep, Flat: true}), true, false)
	return table + alias + pivots + sample + joins
}

//gen:dispatch g.TablesampleSQL(e, "")
func (g *Generator) tablesampleSQL(e *exp.Expr, tablesampleKeyword string) string {
	if len(e.Expressions()) > 0 {
		g.Unsupported("Multiple sample sets are not supported")
		return ""
	}
	method := g.SQLArg(e, "method")
	method = iff(method != "" && g.C.TablesampleWithMethod, method+" ", "")
	numerator := g.SQLArg(e, "bucket_numerator")
	denominator := g.SQLArg(e, "bucket_denominator")
	field := pfx(" ON ", g.SQLArg(e, "bucket_field"))
	bucket := iff(numerator != "", "BUCKET "+numerator+" OUT OF "+denominator+field, "")
	seed := g.SQLArg(e, "seed")
	seed = iff(seed != "", " "+g.C.TablesampleSeedKeyword+" ("+seed+")", "")

	size := g.SQLArg(e, "size")
	if size != "" && g.C.TablesampleSizeIsRows {
		size += " ROWS"
	}
	percent := g.SQLArg(e, "percent")
	if percent != "" && !g.D.TablesampleSizeIsPercent {
		percent += " PERCENT"
	}
	expr := bucket + percent + size
	if g.C.TablesampleRequiresParens {
		expr = "(" + expr + ")"
	}
	if tablesampleKeyword == "" {
		tablesampleKeyword = g.C.TablesampleKeywords
	}
	return " " + tablesampleKeyword + " " + method + expr + seed
}

// pivotInValueAliases returns the rewritten field.expressions list with PivotAlias wrappers
// injected where the stored column name differs from the target dialect's natural output.
func (g *Generator) pivotInValueAliases(e *exp.Expr) []*exp.Expr {
	columns := e.List("columns")
	fields := e.Fields()
	if len(columns) == 0 || len(fields) != 1 {
		return nil
	}
	pc := g.D.Parser
	tgtIdentify, tgtPrefixed, tgtNaming := any(pc.IdentifyPivotStrings), any(pc.PrefixedPivotColumns), any(pc.PivotColumnNaming)
	argOr := func(k string, def any) any {
		if e.HasArg(k) {
			return e.Arg(k)
		}
		return def
	}
	srcIdentify := argOr("identify_pivot_strings", tgtIdentify)
	srcPrefixed := argOr("prefixed_pivot_columns", tgtPrefixed)
	srcNaming := argOr("pivot_column_naming", tgtNaming)
	if srcIdentify == tgtIdentify && srcPrefixed == tgtPrefixed && srcNaming == tgtNaming {
		return nil
	}

	inExprs := fields[0].Expressions()
	if len(inExprs) == 0 {
		g.Raisef("ZeroDivisionError: integer division or modulo by zero")
	}
	step := len(columns) / len(inExprs)

	// Derive the per-value suffix from the first stored column vs the first IN-list value.
	// This correctly handles dialects (e.g. Spark single-agg) that ignore agg aliases.
	var firstBase string
	if exp.Truthy(srcIdentify) {
		firstBase = inExprs[0].SQL()
	} else {
		firstBase = inExprs[0].AliasOrName()
	}
	firstStored := columns[0].Name()

	// exit if only suffix matches, not prefix. (e.g. BigQuery, which cannot be fixed)
	if !strings.HasPrefix(firstStored, firstBase) {
		return nil
	}
	suffix := firstStored[len(firstBase):]

	// Whether the target dialect would append an agg-name suffix for this pivot.
	// Spark single-agg uniquely drops the agg alias entirely.
	anyAlias := false
	for _, a := range e.Expressions() {
		if a.Alias() != "" {
			anyAlias = true
			break
		}
	}
	targetHasSuffix := (len(e.Expressions()) > 1 || pc.PivotColumnNaming != "agg_name_if_multiple") && anyAlias
	sourceHasSuffix := suffix != ""

	var newExprs []*exp.Expr
	modified := false
	for valIdx, x := range inExprs {
		if x.IsKind(exp.PivotAlias) {
			newExprs = append(newExprs, x)
			continue
		}
		i := valIdx * step
		storedFull := columns[i].Name()
		storedValue := storedFull
		if suffix != "" {
			r := []rune(storedFull)
			n := len([]rune(suffix))
			if n <= len(r) {
				storedValue = string(r[:len(r)-n])
			} else {
				storedValue = ""
			}
		}
		var targetValue string
		if pc.IdentifyPivotStrings {
			targetValue = x.SQL()
		} else {
			targetValue = x.AliasOrName()
		}
		if sourceHasSuffix && !targetHasSuffix {
			// Source had a suffix, but target won't apply one
			newExprs = append(newExprs, exp.New(exp.PivotAlias, "this", x, "alias", exp.ToIdentifierQuoted(storedFull, true)))
			modified = true
		} else if storedValue != targetValue {
			// Value-part mismatch (e.g. Snowflake's literal-style values vs others).
			newExprs = append(newExprs, exp.New(exp.PivotAlias, "this", x, "alias", exp.ToIdentifierQuoted(storedValue, true)))
			modified = true
		} else {
			newExprs = append(newExprs, x)
		}
	}
	if modified {
		return newExprs
	}
	return nil
}

func (g *Generator) pivotSQL(e *exp.Expr) string {
	expressions := g.Flat(e)
	direction := iff(e.Unpivot(), "UNPIVOT", "PIVOT")
	group := g.SQLArg(e, "group")

	if e.Bool("this") {
		this := g.SQLArg(e, "this")
		var sql string
		if expressions == "" {
			sql = "UNPIVOT " + this
		} else {
			on := g.Seg("ON") + " " + expressions
			into := pfx(g.Seg("INTO")+" ", g.SQLArg(e, "into"))
			using := pfx(g.Seg("USING")+" ", g.FlatKey(e, "using"))
			sql = direction + " " + this + on + into + using + group
		}
		return g.PrependCtes(e, sql)
	}

	if !e.Unpivot() {
		// Wrap IN-list values with explicit aliases where the target dialect would differ
		if newFieldExprs := g.PivotInValueAliases(e); newFieldExprs != nil {
			e.Fields()[0].Set("expressions", newFieldExprs)
		}
	}

	alias := g.SQLArg(e, "alias")
	if alias != "" {
		alias = iff(g.C.PivotAliasWithAs, " AS "+alias, " "+alias)
	}
	fields := g.Expressions(e, ExprsOpts{Key: "fields", Sep: " ", Dynamic: true, NewLine: true, SkipFirst: true, SkipLast: true})

	nulls := ""
	if includeNulls := e.Arg("include_nulls"); includeNulls != nil {
		nulls = iff(exp.Truthy(includeNulls), " INCLUDE NULLS ", " EXCLUDE NULLS ")
	}
	defaultOnNull := around(" DEFAULT ON NULL (", g.SQLArg(e, "default_on_null"), ")")
	sql := g.Seg(direction) + nulls + "(" + expressions + " FOR " + fields + defaultOnNull + group + ")" + alias
	return g.PrependCtes(e, sql)
}

func (g *Generator) versionSQL(e *exp.Expr) string {
	this := "FOR " + e.Name()
	kind := e.Text("kind")
	expr := g.SQLArg(e, "expression")
	return this + " " + kind + " " + expr
}

func (g *Generator) tupleSQL(e *exp.Expr) string {
	return "(" + g.Expressions(e, ExprsOpts{Dynamic: true, NewLine: true, SkipFirst: true, SkipLast: true}) + ")"
}

// updateFromJoinsSQL returns (join_sql, from_sql) for UPDATE ... FROM, rewriting FROM into
// joins for dialects whose UPDATE doesn't support FROM.
func (g *Generator) updateFromJoinsSQL(e *exp.Expr) (string, string) {
	fromExpr := e.Get("from_")
	if g.C.UpdateStatementSupportsFrom || fromExpr == nil {
		return "", g.SQLArg(e, "from_")
	}
	// Qualify unqualified columns in SET clause with the target table
	// MySQL requires qualified column names in multi-table UPDATE to avoid ambiguity
	if target := e.This(); target.IsKind(exp.Table) {
		targetName := exp.ToIdentifier(target.AliasOrName())
		for _, eq := range e.Expressions() {
			if col := eq.This(); col.IsKind(exp.Column) && col.TableName() == "" {
				col.Set("table", targetName)
			}
		}
	}
	table := fromExpr.This()
	nestedJoins := table.List("joins")
	if len(nestedJoins) > 0 {
		table.Set("joins", nil)
	}
	joinSQL := g.SQL(exp.New(exp.Join, "this", table, "on", exp.NewTrue()))
	for _, nested := range nestedJoins {
		if !nested.Bool("on") && !nested.Bool("using") {
			nested.Set("on", exp.NewTrue())
		}
		joinSQL += g.SQL(nested)
	}
	return joinSQL, ""
}

func (g *Generator) updateSQL(e *exp.Expr) string {
	hint := g.SQLArg(e, "hint")
	this := g.SQLArg(e, "this")
	joinSQL, fromSQL := g.UpdateFromJoinsSQL(e)
	setSQL := g.Flat(e)
	whereSQL := g.SQLArg(e, "where")
	returning := g.SQLArg(e, "returning")
	order := g.SQLArg(e, "order")
	limit := g.SQLArg(e, "limit")
	var expressionSQL string
	if g.C.ReturningEnd {
		expressionSQL = fromSQL + whereSQL + returning
	} else {
		expressionSQL = returning + fromSQL + whereSQL
	}
	options := around(" OPTION(", g.ExprsKey(e, "options"), ")")
	sql := "UPDATE" + hint + " " + this + joinSQL + " SET " + setSQL + expressionSQL + order + limit + options
	return g.PrependCtes(e, sql)
}

//gen:dispatch g.ValuesSQL(e, true)
func (g *Generator) valuesSQL(e *exp.Expr, valuesAsTable bool) string {
	valuesAsTable = valuesAsTable && g.C.ValuesAsTable

	// The VALUES clause is still valid in an `INSERT INTO ..` statement, for example
	if valuesAsTable || e.FindAncestor(exp.From, exp.Join) == nil {
		args := g.Expressions(e, ExprsOpts{})
		alias := g.SQLArg(e, "alias")
		values := "VALUES" + g.Seg("") + args
		if g.C.WrapDerivedValues && (alias != "" || e.Parent().IsAny(exp.From, exp.Table)) {
			values = "(" + values + ")"
		}
		values = g.QueryModifiers(e, values)
		if alias != "" {
			return values + " AS " + alias
		}
		return values
	}

	// Converts `VALUES...` expression into a series of select unions.
	aliasNode := e.Get("alias")
	var columnNames []*exp.Expr
	if aliasNode != nil {
		columnNames = aliasNode.Columns()
	}
	var selects []*exp.Expr
	for i, tup := range e.Expressions() {
		row := tup.Expressions()
		if i == 0 && len(columnNames) > 0 {
			var aliased []*exp.Expr
			for j, value := range row {
				if j >= len(columnNames) {
					break
				}
				aliased = append(aliased, exp.NewAlias(value, columnNames[j]))
			}
			row = aliased
		}
		selects = append(selects, exp.New(exp.Select, "expressions", row))
	}

	if g.Pretty {
		// This may result in poor performance for large-cardinality `VALUES` tables, due to
		// the deep nesting of the resulting exp.Unions.
		query := selects[0]
		for _, s := range selects[1:] {
			query = exp.NewUnion(query, s, exp.WithDistinct(false), exp.WithCopy(false))
		}
		var alias any
		if aliasNode != nil {
			alias = aliasNode.This()
		}
		return g.SubquerySQL(query.Subquery(alias, false), " AS ")
	}

	alias := ""
	if aliasNode != nil {
		alias = " AS " + g.SQLArg(aliasNode, "this")
	}
	var parts []string
	for _, s := range selects {
		parts = append(parts, g.SQL(s))
	}
	return "(" + strings.Join(parts, " UNION ALL ") + ")" + alias
}

func (g *Generator) varSQL(e *exp.Expr) string { return g.SQLArg(e, "this") }

func (g *Generator) intoSQL(e *exp.Expr) string {
	g.CheckUnsupportedArgs(e, "expressions")
	temporary := iff(e.Bool("temporary"), " TEMPORARY", "")
	unlogged := iff(e.Bool("unlogged"), " UNLOGGED", "")
	return g.Seg("INTO") + iff(temporary != "", temporary, unlogged) + " " + g.SQLArg(e, "this")
}

func (g *Generator) fromSQL(e *exp.Expr) string {
	return g.Seg("FROM") + " " + g.SQLArg(e, "this")
}

func (g *Generator) groupingsetsSQL(e *exp.Expr) string {
	return "GROUPING SETS " + g.Wrap(g.Expressions(e, ExprsOpts{NoIndent: true}))
}

func (g *Generator) rollupSQL(e *exp.Expr) string {
	expressions := g.Expressions(e, ExprsOpts{NoIndent: true})
	if expressions != "" {
		return "ROLLUP " + g.Wrap(expressions)
	}
	return "WITH ROLLUP"
}

func (g *Generator) rollupindexSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	columns := g.Flat(e)
	fromSQL := pfx(" FROM ", g.SQLArg(e, "from_index"))
	propertiesSQL := ""
	if properties := e.Get("properties"); properties != nil {
		propertiesSQL = " " + g.Properties(properties, "PROPERTIES", ", ", "", true)
	}
	return this + "(" + columns + ")" + fromSQL + propertiesSQL
}

func (g *Generator) rolluppropertySQL(e *exp.Expr) string {
	return "ROLLUP (" + g.Flat(e) + ")"
}

func (g *Generator) cubeSQL(e *exp.Expr) string {
	expressions := g.Expressions(e, ExprsOpts{NoIndent: true})
	if expressions != "" {
		return "CUBE " + g.Wrap(expressions)
	}
	return "WITH CUBE"
}

func (g *Generator) groupSQL(e *exp.Expr) string {
	modifier := ""
	if all, ok := e.Arg("all").(bool); ok {
		modifier = iff(all, " ALL", " DISTINCT")
	}
	groupBy := g.OpExpressions("GROUP BY"+modifier, e, false)

	groupingSets := g.ExprsKey(e, "grouping_sets")
	cube := g.ExprsKey(e, "cube")
	rollup := g.ExprsKey(e, "rollup")

	segIf := func(s string) string {
		if s == "" {
			return ""
		}
		return g.Seg(s)
	}
	groupings := joinNonEmpty(g.C.GroupingsSep,
		segIf(groupingSets),
		segIf(cube),
		segIf(rollup),
		iff(e.Bool("totals"), g.Seg("WITH TOTALS"), ""),
	)

	if len(e.Expressions()) > 0 && groupings != "" && pyStrip(groupings) != "WITH CUBE" && pyStrip(groupings) != "WITH ROLLUP" {
		addSeparator := true
		if groupingSets != "" {
			if g.C.SupportsGroupingSetsAsSuffix {
				addSeparator = false
			} else {
				g.Unsupported("GROUPING SETS without a comma after GROUP BY expressions is not supported")
			}
		}
		if addSeparator {
			groupBy += g.C.GroupingsSep
		}
	}
	return groupBy + groupings
}

func (g *Generator) havingSQL(e *exp.Expr) string {
	this := g.Indent(g.SQLArg(e, "this"))
	return g.Seg("HAVING") + g.Sep() + this
}

func (g *Generator) connecttoSQL(e *exp.Expr) string {
	return "CONNECT " + g.SQLArg(e, "this")
}

func (g *Generator) disconnectSQL(e *exp.Expr) string { return "DISCONNECT" }

func (g *Generator) connectSQL(e *exp.Expr) string {
	start := g.SQLArg(e, "start")
	if start != "" {
		start = g.Seg("START WITH " + start)
	}
	nocycle := iff(e.Bool("nocycle"), " NOCYCLE", "")
	connect := g.Seg("CONNECT BY" + nocycle + " " + g.SQLArg(e, "connect"))
	return start + connect
}

func (g *Generator) priorSQL(e *exp.Expr) string {
	return "PRIOR " + g.SQLArg(e, "this")
}

//gen:dispatch g.JoinSQL(e, "")
func (g *Generator) joinSQL(e *exp.Expr, joinOp string) string {
	side := e.TextUpper("side")
	if kind := e.TextUpper("kind"); !g.C.SemiAntiJoinWithSide && (kind == "SEMI" || kind == "ANTI") {
		side = ""
	}
	opSQL := joinNonEmpty(" ",
		e.TextUpper("method"),
		iff(e.Bool("global_"), "GLOBAL", ""),
		side,
		e.TextUpper("kind"),
		iff(g.C.JoinHints, e.TextUpper("hint"), ""),
		iff(e.Bool("directed") && g.C.DirectedJoins, "DIRECTED", ""),
	)
	matchCond := around(" MATCH_CONDITION (", g.SQLArg(e, "match_condition"), ")")
	nearest := pfx(" ", g.SQLArg(e, "nearest"))
	onSQL := g.SQLArg(e, "on")
	using := e.List("using")

	if onSQL == "" && len(using) > 0 {
		var cols []string
		for _, c := range using {
			cols = append(cols, g.SQL(c))
		}
		onSQL = joinNonEmpty(", ", cols...)
	}

	this := e.This()
	thisSQL := g.SQL(this)

	if exprs := g.Expressions(e, ExprsOpts{}); exprs != "" {
		thisSQL = thisSQL + "," + g.Seg(exprs)
	}

	if onSQL != "" {
		onSQL = g.IndentSkip(onSQL, true, false)
		space := iff(g.Pretty, g.Seg(strings.Repeat(" ", g.Pad)), " ")
		if len(using) > 0 {
			onSQL = space + "USING (" + onSQL + ")"
		} else {
			onSQL = space + "ON " + onSQL
		}
	} else if opSQL == "" && nearest == "" {
		if this.IsKind(exp.Lateral) && this.Arg("cross_apply") != nil {
			return " " + thisSQL
		}
		return ", " + thisSQL
	}

	if joinOp != "" {
		opSQL = joinOp
	} else if opSQL != "STRAIGHT_JOIN" {
		opSQL = iff(opSQL != "", opSQL+" JOIN", "JOIN")
	}

	pivots := g.Expressions(e, ExprsOpts{Key: "pivots", Sep: EmptySep, Flat: true})
	return g.Seg(opSQL) + " " + thisSQL + matchCond + nearest + onSQL + pivots
}

func (g *Generator) nearestSQL(e *exp.Expr) string {
	if !g.C.SupportsNearestJoin {
		g.Unsupported("NEAREST joins are not supported")
	}
	approximate := iff(e.Bool("approximate"), "APPROX ", "")
	metric := iff(e.Bool("similarity"), "SIMILARITY", "DISTANCE")
	return approximate + "NEAREST " + g.SQLArg(e, "count") + " BY " + metric + " " + g.SQLArg(e, "this")
}

//gen:dispatch g.LambdaSQL(e, "->", true)
func (g *Generator) lambdaSQL(e *exp.Expr, arrowSep string, wrap bool) string {
	args := g.Flat(e)
	if wrap && strings.Contains(args, ",") {
		args = "(" + args + ")"
	}
	return args + " " + arrowSep + " " + g.SQLArg(e, "this")
}

func (g *Generator) lateralOp(e *exp.Expr) string {
	op := ""
	// https://www.mssqltips.com/sqlservertip/1958/sql-server-cross-apply-and-outer-apply/
	if crossApply, ok := e.Arg("cross_apply").(bool); ok {
		op = iff(crossApply, "INNER JOIN ", "LEFT JOIN ")
	}
	return op + "LATERAL"
}

func (g *Generator) lateralSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")

	if e.Bool("view") {
		alias := e.Get("alias")
		columns := g.FlatKey(alias, "columns")
		table := pfx(" ", alias.Name())
		columns = pfx(" AS ", columns)
		opSQL := g.Seg("LATERAL VIEW" + iff(e.Bool("outer"), " OUTER", ""))
		return opSQL + g.Sep() + this + table + columns
	}

	tableAlias := e.Get("alias")
	offset := e.This().Arg("offset")
	if off, ok := offset.(*exp.Expr); ok && g.C.UnnestWithOrdinality && tableAlias != nil && e.This().IsKind(exp.Unnest) &&
		off.IsKind(exp.Identifier) {
		// UNNEST ... WITH ORDINALITY stores the ordinality column's name in Unnest.offset
		tableAlias = tableAlias.Copy()
		tableAlias.Append("columns", off.Copy())
	}

	alias := pfx(" AS ", g.SQL(tableAlias))
	ordinality := ""
	if e.Bool("ordinality") {
		ordinality = " WITH ORDINALITY" + alias
		alias = ""
	}
	return g.LateralOp(e) + " " + this + alias + ordinality
}

//gen:dispatch g.LimitSQL(e, false)
func (g *Generator) limitSQL(e *exp.Expr, top bool) string {
	this := g.SQLArg(e, "this")
	if e.IsLimitAll() && !g.D.SupportsLimitAll {
		return this
	}
	var args []*exp.Expr
	for _, k := range []string{"offset", "expression"} {
		if v := e.Get(k); v != nil {
			if g.C.LimitOnlyLiterals {
				v = g.SimplifyUnlessLiteral(v)
			}
			args = append(args, v)
		}
	}
	var parts []string
	anyNonNumber := false
	for _, a := range args {
		parts = append(parts, g.SQL(a))
		if !a.IsNumber() {
			anyNonNumber = true
		}
	}
	argsSQL := strings.Join(parts, ", ")
	if top && anyNonNumber {
		argsSQL = "(" + argsSQL + ")"
	}
	expressions := g.Flat(e)
	limitOptions := g.SQLArg(e, "limit_options")
	expressions = pfx(" BY ", expressions)
	return this + g.Seg(iff(top, "TOP", "LIMIT")) + " " + argsSQL + limitOptions + expressions
}

func (g *Generator) offsetSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	value := e.Expression()
	if g.C.LimitOnlyLiterals {
		value = g.SimplifyUnlessLiteral(value)
	}
	expressions := pfx(" BY ", g.Flat(e))
	return this + g.Seg("OFFSET") + " " + g.SQL(value) + expressions
}

func (g *Generator) simplifyUnlessLiteral(e *exp.Expr) *exp.Expr {
	if !e.IsKind(exp.Literal) {
		e = Simplify(e, g.D)
	}
	return e
}

func (g *Generator) setitemSQL(e *exp.Expr) string {
	kind := g.SQLArg(e, "kind")
	if !g.C.SetAssignmentRequiresVariableKeyword && kind == "VARIABLE" {
		kind = ""
	} else {
		kind = sfx(kind, " ")
	}
	this := g.SQLArg(e, "this")
	expressions := g.Expressions(e, ExprsOpts{})
	collate := pfx(" COLLATE ", g.SQLArg(e, "collate"))
	global := iff(e.Bool("global_"), "GLOBAL ", "")
	return global + kind + this + expressions + collate
}

func (g *Generator) setSQL(e *exp.Expr) string {
	expressions := " " + g.Flat(e)
	tag := iff(e.Bool("tag"), " TAG", "")
	return iff(e.Bool("unset"), "UNSET", "SET") + tag + expressions
}

func (g *Generator) querybandSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	update := iff(e.Bool("update"), " UPDATE", "")
	scope := pfx(" FOR ", g.SQLArg(e, "scope"))
	return "QUERY_BAND = " + this + update + scope
}

func (g *Generator) pragmaSQL(e *exp.Expr) string {
	return "PRAGMA " + g.SQLArg(e, "this")
}

func (g *Generator) lockSQL(e *exp.Expr) string {
	if !g.C.LockingReadsSupported {
		g.Unsupported("Locking reads using 'FOR UPDATE/SHARE' are not supported")
		return ""
	}
	key := e.Bool("key")
	var lockType string
	if e.Bool("update") {
		lockType = iff(key, "FOR NO KEY UPDATE", "FOR UPDATE")
	} else {
		lockType = iff(key, "FOR KEY SHARE", "FOR SHARE")
	}
	expressions := pfx(" OF ", g.Flat(e))
	wait := ""
	if w := e.Arg("wait"); w != nil {
		if we, ok := w.(*exp.Expr); ok && we.IsKind(exp.Literal) {
			wait = " WAIT " + g.SQL(we)
		} else {
			wait = iff(exp.Truthy(w), " NOWAIT", " SKIP LOCKED")
		}
	}
	return lockType + expressions + wait
}

func (g *Generator) literalSQL(e *exp.Expr) string {
	text := e.Str("this")
	if e.IsString() {
		text = g.D.QuoteStart + g.EscapeStr(text, true, "", "", false, false) + g.D.QuoteEnd
	}
	return text
}

// escapeStr mirrors Generator.escape_str(text, escape_backslash=True, delimiter=None,
// escaped_delimiter=None, is_byte_string=False, is_bytes=False); "" means None for the
// delimiters.
func (g *Generator) escapeStr(text string, escapeBackslash bool, delimiter, escapedDelimiter string, isByteString, isBytes bool) string {
	supports := g.D.StringsSupportEscapedSequences
	if isByteString {
		supports = g.D.ByteStringsSupportEscapedSequences
	}
	if supports {
		var b strings.Builder
		for _, r := range text {
			ch := string(r)
			if escapeBackslash || ch != `\` {
				if esc, ok := g.D.EscapedSequences[ch]; ok {
					b.WriteString(esc)
					continue
				}
			}
			b.WriteString(ch)
		}
		text = b.String()
		if _, ok := g.D.Tokenizer.NumericEscapes["x"]; isBytes && ok {
			// Escape high bytes so they aren't re-encoded as UTF-8 characters.
			text = highByteRe.ReplaceAllStringFunc(text, func(m string) string {
				r := []rune(m)[0]
				return fmt.Sprintf(`\x%02x`, r)
			})
		}
	}
	if delimiter == "" {
		delimiter = g.D.QuoteEnd
	}
	if escapedDelimiter == "" {
		escapedDelimiter = g.EscapedQuoteEnd
	}
	return strings.ReplaceAll(g.ReplaceLineBreaks(text), delimiter, escapedDelimiter)
}

func (g *Generator) loaddataSQL(e *exp.Expr) string {
	isOverwrite := e.Bool("overwrite")
	overwrite := iff(isOverwrite, " OVERWRITE", "")
	this := g.SQLArg(e, "this")

	if files := e.Get("files"); files != nil {
		filesSQL := "FILES" + g.Wrap(g.Flat(files))
		if isOverwrite {
			this = " " + this
		} else if e.Bool("temp") {
			this = " INTO TEMP TABLE " + this
		} else {
			this = " INTO TABLE " + this
		}
		return "LOAD DATA" + overwrite + this + " FROM " + filesSQL
	}

	local := iff(e.Bool("local"), " LOCAL", "")
	inpath := " INPATH " + g.SQLArg(e, "inpath")
	this = " INTO TABLE " + this
	partition := pfx(" ", g.SQLArg(e, "partition"))
	inputFormat := pfx(" INPUTFORMAT ", g.SQLArg(e, "input_format"))
	serde := pfx(" SERDE ", g.SQLArg(e, "serde"))
	return "LOAD DATA" + local + inpath + overwrite + this + partition + inputFormat + serde
}

func (g *Generator) nullSQL(e *exp.Expr) string { return "NULL" }

func (g *Generator) booleanSQL(e *exp.Expr) string {
	return iff(e.Bool("this"), "TRUE", "FALSE")
}

func (g *Generator) boolandSQL(e *exp.Expr) string {
	return "((" + g.SQLArg(e, "this") + ") AND (" + g.SQLArg(e, "expression") + "))"
}

func (g *Generator) boolorSQL(e *exp.Expr) string {
	return "((" + g.SQLArg(e, "this") + ") OR (" + g.SQLArg(e, "expression") + "))"
}

//gen:dispatch g.OrderSQL(e, false)
func (g *Generator) orderSQL(e *exp.Expr, flat bool) string {
	this := sfx(g.SQLArg(e, "this"), " ")
	siblings := iff(e.Bool("siblings"), "SIBLINGS ", "")
	return g.OpExpressions(this+"ORDER "+siblings+"BY", e, this != "" || flat)
}

func (g *Generator) withfillSQL(e *exp.Expr) string {
	fromSQL := pfx(" FROM ", g.SQLArg(e, "from_"))
	toSQL := pfx(" TO ", g.SQLArg(e, "to"))
	stepSQL := pfx(" STEP ", g.SQLArg(e, "step"))
	var vals []string
	for _, x := range e.List("interpolate") {
		if x.IsKind(exp.Alias) {
			vals = append(vals, g.SQLArg(x, "alias")+" AS "+g.SQLArg(x, "this"))
		} else {
			vals = append(vals, g.SQLArg(x, "this"))
		}
	}
	interpolate := ""
	if len(vals) > 0 {
		interpolate = " INTERPOLATE (" + strings.Join(vals, ", ") + ")"
	}
	return "WITH FILL" + fromSQL + toSQL + stepSQL + interpolate
}

func (g *Generator) clusterSQL(e *exp.Expr) string { return g.OpExpressions("CLUSTER BY", e, false) }

func (g *Generator) clusterpropertySQL(e *exp.Expr) string {
	if e.Bool("this") {
		g.Unsupported("Unsupported CLUSTER BY " + g.SQLArg(e, "this"))
		return ""
	}
	return "CLUSTER BY (" + g.Flat(e) + ")"
}

func (g *Generator) distributeSQL(e *exp.Expr) string {
	return g.OpExpressions("DISTRIBUTE BY", e, false)
}

func (g *Generator) sortSQL(e *exp.Expr) string { return g.OpExpressions("SORT BY", e, false) }

func (g *Generator) resolveOrderedForNullOrderingSimulation(e *exp.Expr) *exp.Expr {
	this := e.This()
	if !(this.IsKind(exp.Column) && this.TableName() == "") {
		return nil
	}
	// Only a query's own ORDER BY can reference its projection aliases
	order := e.Parent()
	var ancestor *exp.Expr
	if order.IsKind(exp.Order) {
		ancestor = order.Parent()
	}
	if !ancestor.IsKind(exp.Select) {
		return nil
	}
	columnName := this.Name()
	var matched []*exp.Expr
	for _, p := range ancestor.Selects() {
		if p.OutputName() == columnName {
			if p.IsKind(exp.Alias) {
				matched = append(matched, p.This())
			} else {
				matched = append(matched, p)
			}
		}
	}
	var match *exp.Expr
	if len(matched) == 1 {
		match = matched[0]
	}
	// Skip the substitution when it would be identical to the existing reference
	if match.IsKind(exp.Column) && match.TableName() == "" && match.Name() == columnName {
		return nil
	}
	return match
}

func (g *Generator) orderedSQL(e *exp.Expr) string {
	descV := e.Arg("desc")
	desc := exp.Truthy(descV)
	asc := !desc

	nullsFirst := e.Bool("nulls_first")
	nullsLast := !nullsFirst
	nullsAreLarge := g.D.NullOrdering == "nulls_are_large"
	nullsAreSmall := g.D.NullOrdering == "nulls_are_small"
	nullsAreLast := g.D.NullOrdering == "nulls_are_last"

	this := g.SQLArg(e, "this")

	sortOrder := ""
	if desc {
		sortOrder = " DESC"
	} else if b, ok := descV.(bool); ok && !b {
		sortOrder = " ASC"
	}
	nullsSortChange := ""
	if nullsFirst && ((asc && nullsAreLarge) || (desc && nullsAreSmall) || nullsAreLast) {
		nullsSortChange = " NULLS FIRST"
	} else if nullsLast && ((asc && nullsAreSmall) || (desc && nullsAreLarge)) && !nullsAreLast {
		nullsSortChange = " NULLS LAST"
	}

	// If the NULLS FIRST/LAST clause is unsupported, we add another sort key to simulate it
	nullOrderingSupported := g.C.NullOrderingSupported
	if nullsSortChange != "" && !(nullOrderingSupported != nil && *nullOrderingSupported) {
		window := e.FindAncestor(exp.Window, exp.Select)
		var windowThis, spec *exp.Expr
		if window.IsKind(exp.Window) {
			windowThis = window.This()
			if windowThis.IsAny(exp.IgnoreNulls, exp.RespectNulls) {
				windowThis = windowThis.This()
			}
			spec = window.Get("spec")
		}

		// Some window functions (e.g. LAST_VALUE, RANK) support NULLS FIRST/LAST
		// without a spec or with a ROWS spec, but not with RANGE
		if !(windowThis != nil && g.C.WindowFuncsWithNullOrdering.HasAny(windowThis.Kind()) &&
			(spec == nil || strings.ToUpper(spec.Text("kind")) == "ROWS")) {
			if windowThis != nil && spec != nil {
				g.Unsupported(fmt.Sprintf("'%s' translation not supported in window function %s",
					pyStrip(nullsSortChange), windowThis.Kind().SQLName()))
				nullsSortChange = ""
			} else if nullOrderingSupported != nil && !*nullOrderingSupported &&
				((asc && nullsSortChange == " NULLS LAST") || (desc && nullsSortChange == " NULLS FIRST")) {
				// BigQuery does not allow these ordering/nulls combinations when used under
				// an aggregation func or under a window containing one
				ancestor := e.FindAncestor(exp.AggFunc, exp.Window, exp.Select)
				if ancestor.IsKind(exp.Window) {
					ancestor = ancestor.This()
				}
				if ancestor.Is(exp.AggFunc) {
					g.Unsupported(fmt.Sprintf("'%s' translation not supported for aggregate function %s with %s sort order",
						pyStrip(nullsSortChange), ancestor.Kind().SQLName(), sortOrder))
					nullsSortChange = ""
				}
			} else if nullOrderingSupported == nil {
				if e.This().IsInt() {
					g.Unsupported(fmt.Sprintf("'%s' translation not supported with positional ordering", pyStrip(nullsSortChange)))
				} else if !e.This().IsKind(exp.Rand) {
					resolved := g.ResolveOrderedForNullOrderingSimulation(e)
					target := this
					if resolved != nil {
						target = g.SQL(resolved)
					}
					nullSortOrder := iff(nullsSortChange == " NULLS FIRST", " DESC", "")
					this = "CASE WHEN " + target + " IS NULL THEN 1 ELSE 0 END" + nullSortOrder + ", " + target
				}
				nullsSortChange = ""
			}
		}
	}

	withFill := pfx(" ", g.SQLArg(e, "with_fill"))
	return this + sortOrder + nullsSortChange + withFill
}

func (g *Generator) matchrecognizemeasureSQL(e *exp.Expr) string {
	windowFrame := sfx(g.SQLArg(e, "window_frame"), " ")
	return windowFrame + g.SQLArg(e, "this")
}

func (g *Generator) matchrecognizeSQL(e *exp.Expr) string {
	partition := g.PartitionBySQL(e)
	order := g.SQLArg(e, "order")
	measures := g.ExprsKey(e, "measures")
	if measures != "" {
		measures = g.Seg("MEASURES" + g.Seg(measures))
	}
	rows := g.SQLArg(e, "rows")
	if rows != "" {
		rows = g.Seg(rows)
	}
	after := g.SQLArg(e, "after")
	if after != "" {
		after = g.Seg(after)
	}
	pattern := g.SQLArg(e, "pattern")
	if pattern != "" {
		pattern = g.Seg("PATTERN (" + pattern + ")")
	}
	var defs []any
	for _, d := range e.List("define") {
		defs = append(defs, g.SQLArg(d, "alias")+" AS "+g.SQLArg(d, "this"))
	}
	definitions := g.Expressions(nil, ExprsOpts{Sqls: defs})
	define := ""
	if definitions != "" {
		define = g.Seg("DEFINE" + g.Seg(definitions))
	}
	body := partition + order + measures + rows + after + pattern + define
	alias := pfx(" ", g.SQLArg(e, "alias"))
	return g.Seg("MATCH_RECOGNIZE") + " " + g.Wrap(body) + alias
}

func (g *Generator) queryModifiers(e *exp.Expr, sqls ...string) string {
	limitV := e.Get("limit")
	limit := limitV
	if g.C.LimitFetch == "LIMIT" && limitV.IsKind(exp.Fetch) {
		count := limitV.Get("count")
		// "FETCH FIRST ROWS ONLY" without a count means one row per the SQL
		// standard; emitting a bare "LIMIT" here would produce invalid SQL.
		var c *exp.Expr
		if count != nil {
			c = count.Copy()
		} else {
			c = exp.LiteralNumber(1)
		}
		limit = exp.New(exp.Limit, "expression", c)
	} else if g.C.LimitFetch == "FETCH" && limitV.IsKind(exp.Limit) && !limitV.IsLimitAll() {
		limit = exp.New(exp.Fetch, "direction", "FIRST", "count", exp.MaybeCopy(limitV.Expression(), true))
	}

	parts := append([]string{}, sqls...)
	for _, j := range e.List("joins") {
		parts = append(parts, g.SQL(j))
	}
	parts = append(parts, g.SQLArg(e, "match"))
	for _, l := range e.List("laterals") {
		parts = append(parts, g.SQL(l))
	}
	parts = append(parts,
		g.SQLArg(e, "prewhere"),
		g.SQLArg(e, "where"),
		g.SQLArg(e, "connect"),
		g.SQLArg(e, "group"),
		g.SQLArg(e, "having"),
	)
	for _, t := range g.C.AfterHavingModifierTransforms {
		parts = append(parts, t.Fn(g, e))
	}
	parts = append(parts, g.SQLArg(e, "order"))
	parts = append(parts, g.OffsetLimitModifiers(e, limit.IsKind(exp.Fetch), limit)...)
	parts = append(parts, g.AfterLimitModifiers(e)...)
	parts = append(parts, g.SQLArg(e, "for_"), g.OptionsModifier(e))
	return joinNonEmpty("", parts...)
}

func (g *Generator) optionsModifier(e *exp.Expr) string {
	return pfx(" ", g.ExprsKey(e, "options"))
}

func (g *Generator) forclauseSQL(e *exp.Expr) string {
	kind := pyStr(e.Arg("kind"))
	if kind == "BROWSE" {
		return g.Sep() + "FOR BROWSE"
	}
	// FOR XML/JSON always carry at least AUTO/PATH. An empty rendering means
	// the target dialect doesn't support QueryOption, so we drop the clause.
	options := g.ExprsKey(e, "expressions")
	if options == "" {
		return ""
	}
	return g.Sep() + "FOR " + kind + g.Seg(options)
}

func (g *Generator) queryoptionSQL(e *exp.Expr) string {
	g.Unsupported("Unsupported query option.")
	return ""
}

func (g *Generator) offsetLimitModifiers(e *exp.Expr, fetch bool, limit *exp.Expr) []string {
	if fetch {
		return []string{g.SQLArg(e, "offset"), g.SQL(limit)}
	}
	return []string{g.SQL(limit), g.SQLArg(e, "offset")}
}

func (g *Generator) afterLimitModifiers(e *exp.Expr) []string {
	locks := pfx(" ", g.Expressions(e, ExprsOpts{Key: "locks", Sep: " "}))
	return []string{locks, g.SQLArg(e, "sample")}
}
