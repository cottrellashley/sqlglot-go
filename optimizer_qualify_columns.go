package sqlglot

// Port of sqlglot/optimizer/qualify_columns.py.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// QualifyColumnsOptions are the keyword arguments of qualify_columns.
type QualifyColumnsOptions struct {
	NoExpandAliasRefs         bool // expand_alias_refs=False
	NoExpandStars             bool // expand_stars=False
	AllowPartialQualification bool
	Dialect                   any
}

// QualifyColumns mirrors qualify_columns.qualify_columns: it rewrites the AST to have fully
// qualified columns. schema may be a Schema or anything accepted by EnsureSchema.
func QualifyColumns(e *exp.Expr, schemaArg any, o QualifyColumnsOptions) *exp.Expr {
	schema, ok := schemaArg.(Schema)
	if !ok || schema == nil {
		s, err := EnsureSchema(schemaArg, o.Dialect)
		if err != nil {
			panic(err)
		}
		schema = s
	}
	annotator := NewTypeAnnotator(schema, nil, nil, nil, true)
	dialect := schema.Dialect()
	if dialect == nil {
		dialect = BaseDialectClass().NewInstance()
	}
	pseudocolumns := dialect.Pseudocolumns
	expandAliasRefs := !o.NoExpandAliasRefs

	// Qualified scopes that still lack types, in traversal order (sources precede dependents)
	var unannotatedScopes []*Scope
	for _, scope := range TraverseScope(e) {
		if dialect.PreferCteAliasColumn {
			PushdownCteAliasColumns(scope)
		}
		scopeExpression := scope.Expression
		isSelect := scopeExpression.IsKind(exp.Select)
		separatePseudocolumns(scope, pseudocolumns)
		resolver := NewResolver(scope, schema)
		qualifyComprehensions(scope, resolver)
		popTableColumnAliases(scope.Ctes())
		popTableColumnAliases(scope.DerivedTables())
		usingColumnTables, usingColumns := expandUsing(scope, resolver)

		if dialect.ForceEarlyAliasRefExpansion && expandAliasRefs {
			expandAliasRefsInScope(scope, resolver, dialect, dialect.ExpandOnlyGroupAliasRef)
		}
		convertColumnsToDots(scope, resolver)
		qualifyColumnsInScope(scope, resolver, o.AllowPartialQualification)
		// Refresh classification caches: a column just qualified in place may have been cached
		// as external
		scope.ClearColumnCache()

		if expandAliasRefs {
			expandAliasRefsInScope(scope, resolver, dialect, false)
		}
		if isSelect {
			if !o.NoExpandStars {
				expandStars(scope, resolver, usingColumnTables, usingColumns, pseudocolumns, annotator, &unannotatedScopes)
			}
			QualifyOutputs(scope, dialect)
		}
		expandGroupBy(scope, dialect)
		// DISTINCT ON and ORDER BY follow the same rules (tested in DuckDB, Postgres, ClickHouse)
		// https://www.postgresql.org/docs/current/sql-select.html#SQL-DISTINCT
		expandOrderByAndDistinctOn(scope, resolver)

		if dialect.AnnotateAllScopes {
			annotator.AnnotateScope(scope)
		} else if dialect.SupportsStructStarExpansion {
			unannotatedScopes = append(unannotatedScopes, scope)
		}
	}
	return e
}

func metaInt(e *exp.Expr, key string) (int, bool) {
	switch v := e.MetaGet(key).(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

func columnErrorContext(column *exp.Expr, sql string, inParens bool) string {
	var b strings.Builder
	line, okLine := metaInt(column.This(), "line")
	col, okCol := metaInt(column.This(), "col")
	if okLine && okCol && line != 0 && col != 0 {
		if inParens {
			fmt.Fprintf(&b, " (Line: %d, Col: %d)", line, col)
		} else {
			fmt.Fprintf(&b, " Line: %d, Col: %d", line, col)
		}
	}
	start, okStart := metaInt(column.This(), "start")
	end, okEnd := metaInt(column.This(), "end")
	if sql != "" && okStart && okEnd {
		formatted, _, _, _ := errs.HighlightSQL([]rune(sql), [][2]int{{start, end}}, errs.ErrorMessageContextDefault)
		b.WriteString("\n  " + formatted)
	}
	return b.String()
}

// ValidateQualifyColumns raises an OptimizeError if any columns aren't qualified.
func ValidateQualifyColumns(e *exp.Expr, sql string) *exp.Expr {
	var allUnqualifiedColumns []*exp.Expr
	for _, scope := range TraverseScope(e) {
		if !scope.Expression.IsKind(exp.Select) {
			continue
		}
		unqualifiedColumns := scope.UnqualifiedColumns()
		if external := scope.ExternalColumns(); len(external) > 0 && !scope.IsCorrelatedSubquery() && len(scope.Pivots()) == 0 {
			column := external[0]
			forTable := ""
			if column.TableName() != "" {
				forTable = fmt.Sprintf(" for table: '%s'", column.TableName())
			}
			msg := fmt.Sprintf("Column '%s' could not be resolved%s.", column.Name(), forTable)
			optimizeErr("%s", msg+columnErrorContext(column, sql, false))
		}
		allUnqualifiedColumns = append(allUnqualifiedColumns, unqualifiedColumns...)
	}
	if len(allUnqualifiedColumns) > 0 {
		first := allUnqualifiedColumns[0]
		optimizeErr("%s", fmt.Sprintf("Ambiguous column '%s'", first.Name())+columnErrorContext(first, sql, true))
	}
	return e
}

func separatePseudocolumns(scope *Scope, pseudocolumns map[string]bool) {
	if len(pseudocolumns) == 0 {
		return
	}
	hasPseudocolumns := false
	scopeExpression := scope.Expression
	for _, column := range scope.Columns() {
		name := strings.ToUpper(column.Name())
		if !pseudocolumns[name] {
			continue
		}
		if name != "LEVEL" || (scopeExpression.IsKind(exp.Select) && scopeExpression.Bool("connect")) {
			var kv []any
			for _, a := range column.Args() {
				kv = append(kv, a.Key, a.Value)
			}
			column.Replace(exp.New(exp.Pseudocolumn, kv...))
			hasPseudocolumns = true
		}
	}
	if hasPseudocolumns {
		scope.ClearCache()
	}
}

// popTableColumnAliases removes table column aliases, e.g. col1 and col2 in
// SELECT ... FROM (SELECT ...) AS foo(col1, col2).
func popTableColumnAliases(derivedTables []*exp.Expr) {
	for _, derivedTable := range derivedTables {
		if derivedTable.Parent().IsKind(exp.With) && derivedTable.Parent().Recursive() {
			continue
		}
		if tableAlias := derivedTable.Get("alias"); tableAlias != nil {
			tableAlias.Set("columns", nil)
		}
	}
}

// usingTables is an ordered set of table names (Python dict used for its key order).
type usingTables struct {
	keys []string
	set  map[string]bool
}

func (u *usingTables) add(k string) {
	if !u.set[k] {
		u.set[k] = true
		u.keys = append(u.keys, k)
	}
}

func expandUsing(scope *Scope, resolver *Resolver) (map[string]*usingTables, []string) {
	columns := NewOrderedMap[string]()
	updateSourceColumns := func(sourceName string) {
		for _, columnName := range resolver.GetSourceColumns(sourceName, false) {
			if !columns.Has(columnName) {
				columns.Set(columnName, sourceName)
			}
		}
	}
	var joins []*exp.Expr
	for j := range scope.FindAll(exp.Join) {
		joins = append(joins, j)
	}
	if len(joins) == 0 {
		return map[string]*usingTables{}, nil
	}
	names := map[string]bool{}
	for _, join := range joins {
		names[join.AliasOrName()] = true
	}
	var ordered []string
	for _, key := range scope.SelectedSources().Keys() {
		if !names[key] {
			ordered = append(ordered, key)
		}
	}
	if len(names) > 0 && len(ordered) == 0 {
		var ns []string
		for n := range names {
			ns = append(ns, "'"+n+"'")
		}
		optimizeErr("Joins {%s} missing source table %s", strings.Join(ns, ", "), scope.Expression.SQL())
	}
	// Mapping of automatically joined column names to an ordered set of source names.
	columnTables := map[string]*usingTables{}
	var columnTablesOrder []string
	// USING columns to list first in star expansion, each join's columns preceding those of
	// earlier joins; empty when they keep their position
	var usingColumns []string

	anyUsing := false
	for _, join := range joins {
		if join.Bool("using") || join.TextUpper("method") == "NATURAL" {
			anyUsing = true
		}
	}
	if !anyUsing {
		return columnTables, usingColumns
	}
	for _, sourceName := range ordered {
		updateSourceColumns(sourceName)
	}
	columnOrder := resolver.Dialect.UsingColumnOrder
	moveUsingColumns := columnOrder != InPlace

	for i, join := range joins {
		sourceTable := ordered[len(ordered)-1]
		if sourceTable != "" {
			updateSourceColumns(sourceTable)
		}
		joinTable := join.AliasOrName()
		ordered = append(ordered, joinTable)
		joinColumns := resolver.GetSourceColumns(joinTable, false)
		using := join.List("using")
		hasUsing := join.Arg("using") != nil
		if !(hasUsing || join.Bool("on") || join.Text("method") != "" || join.Text("side") != "" || join.Text("kind") != "") {
			// `,` has lower precedence than JOIN, so `a, b JOIN c USING (k)` is
			// `a, (b JOIN c USING (k))` and k should only move ahead of b's columns, not a's.
			// That isn't modeled, so after a comma join the USING columns stay in place
			moveUsingColumns = false
		}
		// The order in which the left side outputs its columns
		usingSet := map[string]bool{}
		for _, c := range usingColumns {
			usingSet[c] = true
		}
		leftColumns := append([]string(nil), usingColumns...)
		for _, c := range columns.Keys() {
			if !usingSet[c] {
				leftColumns = append(leftColumns, c)
			}
		}
		if !hasUsing && join.TextUpper("method") == "NATURAL" {
			// A NATURAL JOIN is a USING join over the columns common to both sides, in the order
			// the left side outputs them; when those can't be determined (unknown schema, no
			// common columns), NATURAL stays
			if columns.Len() > 0 && !columns.Has("*") && len(joinColumns) > 0 && !containsStr(joinColumns, "*") {
				using = nil
				for _, columnName := range leftColumns {
					if containsStr(joinColumns, columnName) {
						using = append(using, exp.ToIdentifier(columnName))
					}
				}
				if len(using) > 0 {
					join.Set("method", nil)
				}
			}
		}
		if len(using) == 0 {
			continue
		}
		var conditions []any
		usingIdentifierCount := len(using)
		isSemiOrAntiJoin := join.IsSemiOrAntiJoin()

		if moveUsingColumns {
			var joinKeys []string
			for _, identifier := range using {
				joinKeys = append(joinKeys, identifier.Name())
			}
			if columnOrder == LeftTable {
				var filtered []string
				for _, name := range leftColumns {
					if containsStr(joinKeys, name) {
						filtered = append(filtered, name)
					}
				}
				joinKeys = filtered
			}
			newUsing := append([]string(nil), joinKeys...)
			for _, name := range usingColumns {
				if !containsStr(joinKeys, name) {
					newUsing = append(newUsing, name)
				}
			}
			usingColumns = newUsing
		}

		for _, identifierExpr := range using {
			identifier := identifierExpr.Name()
			table, _ := columns.Get(identifier)
			if table == "" || !containsStr(joinColumns, identifier) {
				if (columns.Len() > 0 && !columns.Has("*")) && len(joinColumns) > 0 {
					optimizeErr("Cannot automatically join: %s", identifier)
				}
			}
			if table == "" {
				table = sourceTable
			}
			var lhs *exp.Expr
			if i == 0 || usingIdentifierCount == 1 {
				lhs = exp.NewColumn(identifier, table)
			} else {
				var coalesceColumns []any
				for _, t := range ordered[:len(ordered)-1] {
					if containsStr(resolver.GetSourceColumns(t, false), identifier) {
						coalesceColumns = append(coalesceColumns, exp.NewColumn(identifier, t))
					}
				}
				if len(coalesceColumns) > 1 {
					lhs = exp.NewFunc("coalesce", coalesceColumns...)
				} else {
					lhs = exp.NewColumn(identifier, table)
				}
			}
			conditions = append(conditions, lhs.Eq(exp.NewColumn(identifier, joinTable)))
			// Do not update the dict if this was a SEMI/ANTI join in order to avoid generating
			// COALESCE columns for this join pair
			if !isSemiOrAntiJoin {
				tables, ok := columnTables[identifier]
				if !ok {
					tables = &usingTables{set: map[string]bool{}}
					columnTables[identifier] = tables
					columnTablesOrder = append(columnTablesOrder, identifier)
				}
				tables.add(table)
				tables.add(joinTable)
			}
		}
		join.Set("using", nil)
		join.Set("on", exp.NewAnd(append(conditions, exp.WithCopy(false))...))
	}

	if len(columnTables) > 0 {
		for _, column := range scope.Columns() {
			if column.TableName() == "" {
				if tables, ok := columnTables[column.Name()]; ok {
					var coalesceArgs []any
					for _, table := range tables.keys {
						coalesceArgs = append(coalesceArgs, exp.NewColumn(column.Name(), table))
					}
					replacement := exp.NewFunc("coalesce", coalesceArgs...)
					if column.Parent().IsKind(exp.Select) {
						// Ensure the USING column keeps its name if it's projected
						replacement = exp.NewAlias(replacement, column.Name(), exp.WithCopy(false))
					} else if column.Parent().IsKind(exp.Struct) {
						// Ensure the USING column keeps its name if it's an anonymous STRUCT field
						replacement = exp.New(exp.PropertyEQ, "this", exp.ToIdentifier(column.Name()), "expression", replacement)
					}
					scope.Replace(column, replacement)
				}
			}
		}
	}
	_ = columnTablesOrder
	return columnTables, usingColumns
}

type aliasRef struct {
	expr  *exp.Expr
	index int
}

// expandAliasRefsInScope expands references to aliases, e.g.
// SELECT y.foo AS bar, bar * 2 AS baz FROM y => SELECT y.foo AS bar, y.foo * 2 AS baz FROM y
func expandAliasRefsInScope(scope *Scope, resolver *Resolver, dialect *Dialect, expandOnlyGroupBy bool) {
	expression := scope.Expression
	if !expression.IsKind(exp.Select) || dialect.DisablesAliasRefExpansion {
		return
	}
	aliasToExpression := map[string]aliasRef{}
	projections := map[string]bool{}
	for _, s := range expression.Selects() {
		projections[s.AliasOrName()] = true
	}
	replaced := false

	replaceColumns := func(node *exp.Expr, resolveTable, literalIndex bool) {
		isGroupBy := node.IsKind(exp.Group)
		isHaving := node.IsKind(exp.Having)
		isQualify := node.IsKind(exp.Qualify)
		if node == nil || (expandOnlyGroupBy && !isGroupBy) {
			return
		}
		var columns []*exp.Expr
		for c := range WalkInScope(node, func(n *exp.Expr) bool { return n.IsStar() }) {
			if c.IsKind(exp.Column) {
				columns = append(columns, c)
			}
		}
		for _, column := range columns {
			// BigQuery's GROUP BY allows alias expansion only for standalone names, e.g:
			//   SELECT FUNC(col) AS col FROM t GROUP BY col --> Can be expanded
			//   SELECT FUNC(col) AS col FROM t GROUP BY FUNC(col)  --> Shouldn't be expanded, will result to FUNC(FUNC(col))
			// This not required for the HAVING clause as it can evaluate expressions using both
			// the alias & the table columns
			if expandOnlyGroupBy && isGroupBy && column.Parent() != node {
				continue
			}
			skipReplace := false
			var table *exp.Expr
			if resolveTable && column.TableName() == "" {
				table = resolver.GetTable(column)
			}
			ref, hasRef := aliasToExpression[column.Name()]
			aliasExpr, i := ref.expr, ref.index
			if !hasRef {
				i = 1
			}
			if aliasExpr != nil {
				// An aggregate alias must not be expanded into a GROUP BY or another (non-window)
				// aggregate.
				skipReplace = FindInScope(aliasExpr, exp.AggFunc) != nil &&
					(isGroupBy || (column.FindAncestor(exp.AggFunc) != nil &&
						!column.FindAncestor(exp.Window, exp.Select).IsKind(exp.Window)))
				// BigQuery's having clause gets confused if an alias matches a source.
				// SELECT x.a, max(x.b) as x FROM x GROUP BY 1 HAVING x > 1;
				// If "HAVING x" is expanded to "HAVING max(x.b)", BQ would blindly replace the "x"
				// reference with the projection MAX(x.b), i.e HAVING MAX(MAX(x.b).b), resulting in
				// the error: "Aggregations of aggregations are not allowed"
				if (isHaving || isQualify) && dialect.ProjectionAliasesShadowSourceNames && !skipReplace {
					for _, n := range aliasExpr.FindAllList(exp.Column) {
						if projections[n.Parts()[0].Name()] {
							skipReplace = true
							break
						}
					}
				}
			}
			if table != nil && (aliasExpr == nil || skipReplace) {
				column.Set("table", table)
			} else if column.TableName() == "" && aliasExpr != nil && !skipReplace {
				// For queries like SELECT x + 1 AS y FROM read_parquet('f') WHERE y > 1, we can't
				// determine whether y in y > 1 refers to the alias or a source column, so we bail
				if resolver.HasUnknownSources() {
					optimizeErr("Cannot resolve '%s' due to missing source schemas", column.Name())
				}
				if (aliasExpr.IsKind(exp.Literal) || aliasExpr.IsNumber()) && (literalIndex || resolveTable) {
					if literalIndex {
						column.Replace(exp.LiteralNumber(i))
						replaced = true
					}
				} else {
					replaced = true
					column = column.Replace(exp.NewParen(aliasExpr))
					if simplified := SimplifyParens(column, dialect); simplified != column {
						column.Replace(simplified)
						column = simplified
					}
					if resolveTable {
						// Qualify any unqualified columns inside the spliced alias expression.
						var inners []*exp.Expr
						for inner := range WalkInScope(column, nil) {
							inners = append(inners, inner)
						}
						for _, inner := range inners {
							if inner.IsKind(exp.Column) && inner.TableName() == "" {
								if innerTable := resolver.GetTable(inner); innerTable != nil {
									inner.Set("table", innerTable)
								}
							}
						}
					}
				}
			}
		}
	}

	for i, projection := range expression.Selects() {
		replaceColumns(projection, false, false)
		if projection.IsKind(exp.Alias) {
			aliasToExpression[projection.Alias()] = aliasRef{projection.This(), i + 1}
		}
	}

	childScope := scope
	parentScope := scope
	onRightSubTree := false
	for parentScope != nil && !parentScope.IsCTE() {
		childScope = parentScope
		parentScope = parentScope.Parent
		if parentScope != nil {
			if parentScope.Expression.IsKind(exp.Union) {
				// Access the arg directly instead of the right property, because set operation
				// operands aren't guaranteed to be Query nodes, e.g. SELECT 1 UNION ALL VALUES (2).
				// Unnest to see through parenthesized operands, whose scope is the inner query
				onRightSubTree = parentScope.Expression.Expression().Unnest() == childScope.Expression
			}
		}
	}
	// We shouldn't expand aliases if they match the recursive CTE's columns and we are in the
	// recursive part (right sub tree) of the CTE
	if parentScope != nil && onRightSubTree {
		if cte := parentScope.Expression.Parent(); cte != nil {
			if with := cte.FindAncestor(exp.With); with != nil && with.Recursive() {
				cols := cte.Get("alias").Columns()
				if len(cols) == 0 {
					cols = cte.This().Selects()
				}
				for _, recursiveCteColumn := range cols {
					delete(aliasToExpression, recursiveCteColumn.OutputName())
				}
			}
		}
	}

	replaceColumns(expression.Get("where"), false, false)
	replaceColumns(expression.Get("group"), false, true)
	replaceColumns(expression.Get("having"), true, false)
	replaceColumns(expression.Get("qualify"), true, false)

	if dialect.SupportsAliasRefsInJoinConditions {
		for _, join := range expression.List("joins") {
			replaceColumns(join, false, false)
		}
	}

	if dialect.ProjectionAliasesShadowSourceNames {
		// In BigQuery's GROUP BY, HAVING and QUALIFY clauses, a qualifier that collides with a
		// projection alias resolves to the projection instead of the source. Such references
		// must be rendered as bare names, so we keep the columns qualified and mark them,
		// deferring to Generator.column_parts
		for _, clause := range []*exp.Expr{expression.Get("group"), expression.Get("having"), expression.Get("qualify")} {
			if clause == nil {
				continue
			}
			for column := range FindAllInScope(clause, exp.Column) {
				if column.TableName() != "" && column.DB() == "" {
					if projections[column.TableName()] {
						column.Set("shadow", true)
					} else {
						column.Set("shadow", nil)
					}
				}
			}
		}
	}
	if replaced {
		scope.ClearCache()
	}
}

func expandGroupBy(scope *Scope, dialect *Dialect) {
	expression := scope.Expression
	group := expression.Get("group")
	if group == nil {
		return
	}
	group.Set("expressions", expandPositionalReferences(scope, group.Expressions(), dialect, nil))
	expression.Set("group", group)
}

func expandOrderByAndDistinctOn(scope *Scope, resolver *Resolver) {
	expression := scope.Expression
	if !expression.Is(exp.Selectable) {
		return
	}
	counts := map[string]int{}
	for _, name := range expression.NamedSelects() {
		counts[name]++
	}
	aliasNames := map[string]bool{}
	for name, count := range counts {
		if count == 1 {
			aliasNames[name] = true
		}
	}
	for _, modifierKey := range []string{"order", "distinct"} {
		modifier := expression.Get(modifierKey)
		if modifier.IsKind(exp.Distinct) {
			modifier = modifier.Get("on")
		}
		if modifier == nil {
			continue
		}
		modifierExpressions := modifier.Expressions()
		if modifierKey == "order" {
			var thisExprs []*exp.Expr
			for _, ordered := range modifierExpressions {
				thisExprs = append(thisExprs, ordered.This())
			}
			modifierExpressions = thisExprs
		}
		expanded := expandPositionalReferences(scope, modifierExpressions, resolver.Dialect, aliasNames)
		for idx, original := range modifierExpressions {
			if idx >= len(expanded) {
				break
			}
			for _, agg := range original.FindAllList(exp.AggFunc) {
				for _, col := range agg.FindAllList(exp.Column) {
					if col.TableName() == "" {
						if table := resolver.GetTable(col); table != nil {
							col.Set("table", table)
							delete(col.Meta(), "dot_parts")
						}
					}
				}
			}
			if original != expanded[idx] {
				original.Replace(expanded[idx])
			}
		}
		if expression.Get("group") != nil {
			selects := map[exp.ExprKey]*exp.Expr{}
			for _, s := range expression.Selects() {
				if aliasNames[s.AliasOrName()] {
					selects[s.This().MapKey()] = s
				}
			}
			for _, node := range modifierExpressions {
				// Remaining ordinals reference duplicate aliases and must stay positional.
				if node.IsInt() {
					continue
				}
				replacement, ok := selects[node.MapKey()]
				if !ok || !replacement.This().Equal(node) {
					continue
				}
				// Normalized expressions can match despite different case-sensitive keys.
				if !sameDotParts(node, replacement.This()) {
					continue
				}
				node.Replace(exp.NewColumn(replacement.AliasOrName()))
			}
		}
	}
}

// sameDotParts compares the original path spellings in structurally equal expressions.
func sameDotParts(left, right *exp.Expr) bool {
	type pair struct{ l, r *exp.Expr }
	stack := []pair{{left, right}}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !dotPartsEqual(p.l.MetaGet("dot_parts"), p.r.MetaGet("dot_parts")) {
			return false
		}
		for _, child := range p.l.IterExpressions() {
			other := p.r.Arg(child.ArgKey())
			if child.Index() >= 0 {
				if l, ok := other.([]*exp.Expr); ok && child.Index() < len(l) {
					stack = append(stack, pair{child, l[child.Index()]})
				}
				continue
			}
			if o, ok := other.(*exp.Expr); ok {
				stack = append(stack, pair{child, o})
			}
		}
	}
	return true
}

func dotPartsEqual(a, b any) bool {
	la, _ := a.([]string)
	lb, _ := b.([]string)
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		if la[i] != lb[i] {
			return false
		}
	}
	return true
}

func expandPositionalReferences(scope *Scope, expressions []*exp.Expr, dialect *Dialect, aliasNames map[string]bool) []*exp.Expr {
	var newNodes []*exp.Expr
	var ambiguousProjections map[string]bool
	expression := scope.Expression
	if !expression.Is(exp.Selectable) {
		return newNodes
	}
	for _, node := range expressions {
		if node.IsInt() && node.IsKind(exp.Literal) {
			sel := selectByPos(expression, node)
			if aliasNames != nil {
				if aliasNames[sel.Alias()] {
					newNodes = append(newNodes, exp.NewColumn(sel.Get("alias").Copy()))
				} else {
					newNodes = append(newNodes, node)
				}
				continue
			}
			selectExpr := sel.This()
			ambiguous := false
			if dialect.ProjectionAliasesShadowSourceNames {
				if ambiguousProjections == nil {
					// When a projection name is also a source name and it is referenced in the
					// GROUP BY clause, BQ can't understand what the identifier corresponds to
					ambiguousProjections = map[string]bool{}
					for _, s := range expression.Selects() {
						if scope.SelectedSources().Has(s.AliasOrName()) {
							ambiguousProjections[s.AliasOrName()] = true
						}
					}
				}
				for _, column := range selectExpr.FindAllList(exp.Column) {
					if ambiguousProjections[column.Parts()[0].Name()] {
						ambiguous = true
						break
					}
				}
			}
			if selectExpr.IsAny(exp.Constants...) || selectExpr.IsNumber() || selectExpr.Find(exp.Explode, exp.Unnest) != nil || ambiguous {
				newNodes = append(newNodes, node)
			} else {
				newNodes = append(newNodes, selectExpr.Copy())
			}
		} else {
			newNodes = append(newNodes, node)
		}
	}
	return newNodes
}

func selectByPos(expression, node *exp.Expr) *exp.Expr {
	n, _ := strconv.Atoi(node.Str("this"))
	selects := expression.Selects()
	idx := n - 1
	if idx < 0 {
		idx += len(selects)
	}
	if idx < 0 || idx >= len(selects) {
		optimizeErr("Unknown output column: %s", node.Name())
	}
	sel := selects[idx]
	if !sel.Is(exp.Alias) {
		panic(&errs.OptimizeError{Message: fmt.Sprintf("%s is not Alias.", sel.SQL())})
	}
	return sel
}

func qualifyComprehensions(scope *Scope, resolver *Resolver) {
	converted := false
	for _, comprehension := range scope.Comprehensions() {
		names := map[string]bool{}
		for _, key := range []string{"expression", "position"} {
			if variable := comprehension.Get(key); variable != nil {
				names[variable.Name()] = true
			}
		}
		for _, key := range []string{"this", "condition"} {
			body := comprehension.Get(key)
			if body == nil {
				continue
			}
			var cols []*exp.Expr
			for c := range FindAllInScope(body, exp.Column) {
				cols = append(cols, c)
			}
			for _, column := range cols {
				parts := column.Parts()
				if !names[parts[0].Name()] {
					continue
				}
				// Local table-qualified columns take precedence over lambda struct fields.
				if len(parts) > 1 && scope.SelectedSources().Has(parts[0].Name()) &&
					containsStr(resolver.GetSourceColumns(parts[0].Name(), false), parts[1].Name()) {
					continue
				}
				column.Replace(column.ToDot(false))
				converted = true
			}
		}
	}
	if converted {
		scope.ClearCache()
	}
}

// convertColumnsToDots converts Column instances that represent STRUCT or JSON field lookups
// into chained Dots, e.g. "col"."field"."field2" -> Dot(Dot(<table>.<column>, field1), field2).
func convertColumnsToDots(scope *Scope, resolver *Resolver) {
	converted := false
	candidates := append(append([]*exp.Expr(nil), scope.Columns()...), scope.Stars()...)
	for _, column := range candidates {
		if column.IsKind(exp.Dot) {
			continue
		}
		isStar := column.This().IsKind(exp.Star)
		var columnTable any
		if t := column.TableName(); t != "" {
			columnTable = t
		}
		dotParts, _ := column.Meta()["dot_parts"].([]string)
		delete(column.Meta(), "dot_parts")
		var rootResolver *Resolver
		tableStr, _ := columnTable.(string)

		if !isStar && column.DB() != "" && scope.SelectedSources().Has(tableStr) {
			parts := column.Parts()
			root, field := parts[0], parts[1]
			for _, candidate := range append([]*Resolver{resolver}, resolver.OuterResolvers()...) {
				if !candidate.Scope.SelectedSources().Has(root.Name()) {
					continue
				}
				if containsStr(candidate.GetSourceColumns(root.Name(), false), field.Name()) {
					rootResolver = candidate
				}
				break
			}
		}

		if tableStr != "" &&
			(!scope.SelectedSources().Has(tableStr) || (isStar && column.DB() != "") || rootResolver != nil) &&
			(rootResolver != nil || isStar || scope.Parent == nil || !scope.Parent.Sources.Has(tableStr) || !scope.IsCorrelatedSubquery()) {
			allParts := column.Parts()
			root, parts := allParts[0], allParts[1:]
			wasQualified := false
			// Correlated stars cannot be deferred; qualified paths use their root source's resolver.
			var resolvers []*Resolver
			if isStar {
				resolvers = append([]*Resolver{resolver}, resolver.OuterResolvers()...)
			} else if rootResolver != nil {
				resolvers = []*Resolver{rootResolver}
			} else {
				resolvers = []*Resolver{resolver}
			}
			var newTable any = columnTable
			for _, sourceResolver := range resolvers {
				selectedSources := sourceResolver.Scope.SelectedSources()
				if column.DB() == "" && selectedSources.Has(column.TableName()) {
					// The star is over a table, so it's expanded as is
					newTable = nil
					break
				}
				if root.IsKind(exp.Identifier) && selectedSources.Has(root.Name()) {
					// The struct is already qualified, but we still need to change the AST
					newTable = root
					root, parts = parts[0], parts[1:]
					wasQualified = true
					break
				}
				if isStar && !sourceResolver.AllColumns()[root.Name()] && sourceResolver.HasUnknownSources() {
					// An unknown source may own the star, so it's preserved instead of inferred
					newTable = nil
					break
				}
				if !root.IsKind(exp.Identifier) {
					panic(&errs.OptimizeError{Message: fmt.Sprintf("%s is not Identifier.", root.SQL())})
				}
				if t := sourceResolver.GetTable(root); t != nil {
					newTable = t
					break
				}
				newTable = nil
			}
			if newTable != nil {
				converted = true
				newColumn := exp.NewColumn(root, newTable)
				if len(dotParts) > 0 {
					// Remove the actual column parts from the rest of dot parts
					skip := 1
					if wasQualified {
						skip = 2
					}
					if skip <= len(dotParts) {
						newColumn.Meta()["dot_parts"] = append([]string(nil), dotParts[skip:]...)
					} else {
						newColumn.Meta()["dot_parts"] = []string{}
					}
				}
				column.Replace(exp.DotBuild(append([]*exp.Expr{newColumn}, parts...)))
			}
		}
	}
	if converted {
		// We want to re-aggregate the converted columns, otherwise they'd be skipped in a
		// `for column in scope.columns` iteration, even though they shouldn't be
		scope.ClearCache()
	}
}

// qualifyPositionalColumn resolves a positional column, returning whether to skip further
// qualification.
func qualifyPositionalColumn(scope *Scope, resolver *Resolver, column *exp.Expr, columnTable string, columnSource Source,
	sourceColumns []string, pivots []*exp.Expr, allowPartialQualification bool) bool {
	position := column.This().This()
	if !resolver.Dialect.SupportsPositionalColumnRefs || !column.This().IsKind(exp.Parameter) ||
		!position.IsKind(exp.Literal) || !position.IsInt() {
		return false
	}
	// Pivots from unrelated sources may share this scope, so prefer an exact output-alias
	// match. For an aliasless chain, fall back to the last operator on the referenced source.
	var scopePivot *exp.Expr
	scopePivots := scope.Pivots()
	for _, pivot := range scopePivots {
		if pivot.Alias() == columnTable {
			scopePivot = pivot
			break
		}
	}
	if scopePivot == nil {
		for i := len(scopePivots) - 1; i >= 0; i-- {
			if p := scopePivots[i]; p.Parent() != nil && p.Parent().AliasOrName() == columnTable {
				scopePivot = p
				break
			}
		}
	}
	if len(pivots) > 0 || scopePivot != nil || len(sourceColumns) == 0 || containsStr(sourceColumns, "*") {
		if scopePivot != nil {
			column.Set("table", exp.ToIdentifier(scopePivot.Alias()))
		}
		return true
	}
	positionValue := 0
	if v, err := position.ToPy(); err == nil {
		positionValue, _ = v.(int)
	}
	var aliasColumns []string
	sourceColumnsIncomplete := false
	if t, ok := columnSource.(*exp.Expr); ok && t.IsKind(exp.Table) {
		aliasColumns = t.AliasColumnNames()
		withoutAliases, err := resolver.Schema.ColumnNames(t, true, nil, nil)
		if err != nil {
			panic(err)
		}
		sourceColumnsIncomplete = len(withoutAliases) == 0 || containsStr(withoutAliases, "*")
	}
	if len(aliasColumns) > 0 && positionValue > len(aliasColumns) && sourceColumnsIncomplete {
		return true
	}
	positionalColumns := resolver.GetSourceColumns(columnTable, true)
	if !(1 <= positionValue && positionValue <= len(positionalColumns)) {
		if allowPartialQualification {
			return true
		}
		optimizeErr("Positional reference $%d is out of range for source '%s'", positionValue, columnTable)
	}
	positionalName := positionalColumns[positionValue-1]
	count := 0
	for _, c := range positionalColumns {
		if c == positionalName {
			count++
		}
	}
	if count > 1 {
		return true
	}
	var positionalIdentifier *exp.Expr
	if s, ok := columnSource.(*Scope); ok {
		sourceExpression := s.Expression
		if !sourceExpression.Is(exp.Query) {
			return true
		}
		sourceSelects := sourceExpression.Selects()
		var selection *exp.Expr
		if positionValue <= len(sourceSelects) {
			selection = sourceSelects[positionValue-1]
		}
		sourceIdentifier := outputIdentifier(selection)
		if sourceIdentifier == nil || sourceIdentifier.Name() != positionalName {
			return true
		}
		positionalIdentifier = sourceIdentifier.Copy()
	} else {
		positionalIdentifier = exp.ToIdentifier(positionalName)
		resolver.Dialect.QuoteIdentifier(positionalIdentifier, false)
	}
	column.Set("this", positionalIdentifier)
	return false
}

// qualifyColumnsInScope disambiguates columns, ensuring each column specifies a source.
func qualifyColumnsInScope(scope *Scope, resolver *Resolver, allowPartialQualification bool) {
	for _, column := range scope.Columns() {
		columnTable := column.TableName()
		columnName := column.Name()
		if columnTable != "" && scope.Sources.Has(columnTable) {
			columnSource, _ := scope.Sources.Get(columnTable)
			sourceColumns := resolver.GetSourceColumns(columnTable, false)
			var pivots []*exp.Expr
			if t, ok := columnSource.(*exp.Expr); ok && t.IsKind(exp.Table) {
				pivots = t.List("pivots")
			}
			if len(pivots) > 0 && len(sourceColumns) > 0 && !containsStr(sourceColumns, "*") {
				// Each operator's input is the previous one's output
				for _, pivot := range pivots {
					sourceColumns, _ = pivot.PivotOutputColumns(sourceColumns)
				}
			}
			if qualifyPositionalColumn(scope, resolver, column, columnTable, columnSource, sourceColumns, pivots, allowPartialQualification) {
				continue
			}
			columnName = column.Name()
			if !allowPartialQualification && len(sourceColumns) > 0 && !containsStr(sourceColumns, columnName) &&
				!containsStr(sourceColumns, "*") {
				optimizeErr("Unknown column: %s", columnName)
			}
		}
		if columnTable == "" {
			if pivots := scope.Pivots(); len(pivots) > 0 && column.FindAncestor(exp.Pivot) == nil {
				if resolver.Dialect.TableAliasesShadowColumnNames {
					shadowed := false
					for _, pivot := range pivots {
						if pivot.Alias() == columnName {
							shadowed = true
							break
						}
					}
					if shadowed {
						scope.Replace(column, exp.New(exp.TableColumn, "this", column.This()))
						continue
					}
				}
				// If the column is under the Pivot expression, we need to qualify it using the
				// name of the pivoted source instead of the pivot's alias
				column.Set("table", exp.ToIdentifier(pivots[len(pivots)-1].Alias()))
				continue
			}
			// column_table can be a '' because bigquery unnest has no table alias
			table := resolver.GetTable(column)
			if table != nil {
				if src, ok := scope.Sources.Get(table.Name()); ok {
					if s, isScope := src.(*Scope); isScope && s.ColumnIndex()[column] {
						continue
					}
				}
			}
			if table != nil {
				column.Set("table", table)
			} else if scope.CanBeCorrelated && resolver.HasUnknownSources() {
				optimizeErr("Cannot resolve '%s' due to missing source schemas", columnName)
			} else if resolver.Dialect.TablesReferenceableAsColumns && len(column.Parts()) == 1 &&
				scope.SelectedSources().Has(columnName) && !resolver.HasUnknownSources() {
				// BigQuery and Postgres allow tables to be referenced as columns, treating them as
				// structs/records
				scope.Replace(column, exp.New(exp.TableColumn, "this", column.This()))
			}
		}
	}

	pivots := scope.Pivots()
	// A chained operator's IN-list may name columns produced by earlier operators, which no
	// source exposes; track the chain's accumulated output to resolve them. Attribution is only
	// unambiguous when all pivots share one parent, i.e. there's a single pivoted source.
	singleChain := len(pivots) > 0 && pivots[0].Parent() == pivots[len(pivots)-1].Parent()
	produced := map[string]bool{}
	pivotedSource := ""
	if singleChain {
		pivotedSource = pivots[len(pivots)-1].Alias()
	}
	var available []string
	if scope.Sources.Has(pivotedSource) {
		available = resolver.GetSourceColumns(pivotedSource, false)
	}
	for _, pivot := range pivots {
		for _, column := range pivot.FindAllList(exp.Column) {
			if column.TableName() != "" {
				continue
			}
			if resolver.AllColumns()[column.Name()] {
				if table := resolver.GetTable(column); table != nil {
					column.Set("table", table)
				}
			} else if singleChain && produced[column.Name()] {
				column.Set("table", exp.ToIdentifier(pivotedSource))
			}
		}
		if singleChain {
			available, _ = pivot.PivotOutputColumns(available)
			for _, c := range available {
				produced[c] = true
			}
		}
	}
}

func expandStructStars(expression *exp.Expr, dialect *Dialect) []*exp.Expr {
	operand := expression.This()
	requiresParens := dialect.RequiresParenthesizedStructAccess
	if requiresParens && !operand.IsKind(exp.Paren) {
		return nil
	}
	root := operand
	for root.IsAny(exp.Dot, exp.Paren) {
		if root.IsKind(exp.Paren) {
			if !requiresParens {
				return nil
			}
		} else if !root.Expression().IsKind(exp.Identifier) {
			return nil
		}
		root = root.This()
	}
	if !root.IsKind(exp.Column) || !operand.IsType(exp.DTypeStruct) {
		return nil
	}
	takenNames := map[string]bool{}
	var newSelections []*exp.Expr
	for _, field := range operand.Type().Expressions() {
		if !field.IsKind(exp.ColumnDef) || !field.This().IsKind(exp.Identifier) {
			return nil
		}
		if takenNames[field.Name()] {
			return nil
		}
		takenNames[field.Name()] = true
		newSelections = append(newSelections, exp.NewAlias(
			exp.New(exp.Dot, "this", operand.Copy(), "expression", field.This().Copy()),
			field.This().Copy(), exp.WithCopy(false)))
	}
	return newSelections
}

// tableRef is an element of the `tables` list of _expand_stars. Python keys the EXCEPT/REPLACE/
// RENAME columns by id(table) of the table name string, so a name taken from the scope's
// selected sources is a different key than an equal name read from a column (CPython only
// shares single-character strings).
type tableRef struct {
	name string
	id   string
}

var tableRefCounter int

func selectedTableRef(name string) tableRef { return tableRef{name, "k:" + name} }

func freshTableRef(name string) tableRef {
	if len([]rune(name)) == 1 && []rune(name)[0] < 256 {
		return tableRef{name, "k:" + name}
	}
	tableRefCounter++
	return tableRef{name, "f:" + strconv.Itoa(tableRefCounter)}
}

// expandStars expands stars to lists of column selections.
func expandStars(scope *Scope, resolver *Resolver, usingColumnTables map[string]*usingTables, usingColumns []string,
	pseudocolumns map[string]bool, annotator *TypeAnnotator, unannotatedScopes *[]*Scope) {
	var newSelections []*exp.Expr
	exceptColumns := map[string]map[string]bool{}
	replaceColumnsMap := map[string]map[string]*exp.Expr{}
	renameColumns := map[string]map[string]string{}
	var ilikePattern *regexp.Regexp
	coalescedColumns := map[string]bool{}
	dialect := resolver.Dialect

	if dialect.SupportsStructStarExpansion {
		for _, structStar := range scope.Stars() {
			if structStar.IsKind(exp.Dot) {
				// The operand's type may come from sources that were qualified but not annotated
				for _, s := range *unannotatedScopes {
					annotator.AnnotateScope(s)
				}
				*unannotatedScopes = nil
				annotator.AnnotateExpression(structStar.This(), scope)
			}
		}
	}

	scopeExpression := scope.Expression
	if !scopeExpression.Is(exp.Selectable) {
		return
	}
	for _, pivot := range scope.Pivots() {
		if !pivot.Bool("unpivot") {
			if cols, ok := pivot.Arg("columns").([]*exp.Expr); ok && len(cols) == 0 && len(pivot.AliasColumnNames()) == 0 {
				return
			}
		}
	}

	for _, expression := range scopeExpression.Selects() {
		var tables []tableRef
		if expression.IsKind(exp.Star) {
			// Only a string literal ILIKE pattern can filter the expansion at optimization time
			if ilike := expression.Get("ilike"); ilike != nil && !ilike.IsString() {
				newSelections = append(newSelections, expression)
				continue
			}
			for _, k := range scope.SelectedSources().Keys() {
				tables = append(tables, selectedTableRef(k))
			}
			addExceptColumns(expression, tables, exceptColumns)
			addReplaceColumns(expression, tables, replaceColumnsMap)
			addRenameColumns(expression, tables, renameColumns)
			ilikePattern = addIlikeColumns(expression, dialect)
		} else if expression.IsStar() {
			if expression.IsKind(exp.Column) {
				if ilike := expression.This().Get("ilike"); ilike != nil && !ilike.IsString() {
					newSelections = append(newSelections, expression)
					continue
				}
				tables = append(tables, freshTableRef(expression.TableName()))
				addExceptColumns(expression.This(), tables, exceptColumns)
				addReplaceColumns(expression.This(), tables, replaceColumnsMap)
				addRenameColumns(expression.This(), tables, renameColumns)
				ilikePattern = addIlikeColumns(expression.This(), dialect)
			} else if expression.IsKind(exp.Dot) {
				var structFields []*exp.Expr
				if dialect.SupportsStructStarExpansion {
					structFields = expandStructStars(expression, dialect)
				}
				if len(structFields) > 0 {
					star := expression.Expression()
					excluded := map[string]bool{}
					for _, x := range star.List("except_") {
						excluded[x.Name()] = true
					}
					replaced := map[string]*exp.Expr{}
					for _, x := range star.List("replace") {
						replaced[x.Alias()] = x
					}
					for _, f := range structFields {
						if excluded[f.Alias()] {
							continue
						}
						if r, ok := replaced[f.Alias()]; ok {
							newSelections = append(newSelections, r)
						} else {
							newSelections = append(newSelections, f)
						}
					}
					continue
				}
			}
		}
		if len(tables) == 0 {
			newSelections = append(newSelections, expression)
			continue
		}

		start := len(newSelections)
		// The selections of the USING columns, which go first, in order
		usingSelections := NewOrderedMap[*exp.Expr]()
		if expression.IsKind(exp.Star) {
			for _, c := range usingColumns {
				usingSelections.Set(c, nil)
			}
		}

		for _, tableRef := range tables {
			table := tableRef.name
			source, hasSource := scope.Sources.Get(table)
			sourceResolver := resolver
			var pivots []*exp.Expr
			pivotsSet := false
			sourceTable := table
			if !hasSource || source == nil {
				// The chain's final alias names the resulting source, but only the underlying
				// source is registered in `scope.sources`, so resolve through the chain's parent.
				// Attribution is only unambiguous for a single chain (all pivots share a parent)
				chain := scope.Pivots()
				var parent *exp.Expr
				if len(chain) > 0 && chain[0].Parent() == chain[len(chain)-1].Parent() && chain[len(chain)-1].Alias() == table {
					parent = chain[len(chain)-1].Parent()
				}
				if parent != nil {
					pivots = chain
					pivotsSet = true
					sourceTable = parent.AliasOrName()
					source, hasSource = scope.Sources.Get(sourceTable)
				}
				if !hasSource || source == nil {
					// Correlated stars, e.g. (SELECT AS STRUCT x.* EXCEPT (a)), expand an outer source
					found := false
					for _, r := range append([]*Resolver{resolver}, resolver.OuterResolvers()...) {
						sourceResolver = r
						source, hasSource = r.Scope.Sources.Get(table)
						if (hasSource && source != nil) || r.HasUnknownSources() {
							found = true
							break
						}
					}
					if !found {
						optimizeErr("Unknown table: %s", table)
					}
				}
				if !hasSource || source == nil {
					// An unknown source may own the star, so it's preserved instead of expanded
					newSelections = append(newSelections, expression)
					break
				}
			}

			columns := sourceResolver.GetSourceColumns(sourceTable, true)
			if len(columns) == 0 {
				columns = scope.OuterColumns
			}
			if len(pseudocolumns) > 0 && dialect.ExcludesPseudocolumnsFromStar {
				var filtered []string
				for _, name := range columns {
					if !pseudocolumns[strings.ToUpper(name)] {
						filtered = append(filtered, name)
					}
				}
				columns = filtered
			}
			// If a source exposes duplicate output names (e.g. a derived table re-exposing
			// colliding star-expanded columns), expanding this star would produce ambiguous
			// projections, so we leave it unexpanded.
			if len(columns) == 0 || containsStr(columns, "*") || len(columns) != len(dedupStrings(columns)) {
				return
			}

			columnsToExclude := exceptColumns[tableRef.id]
			renamedColumns := renameColumns[tableRef.id]
			replacedColumns := replaceColumnsMap[tableRef.id]

			// Preserve case-sensitivity of quoted source columns when expanding stars, so the
			// generated alias isn't folded by dialect normalization
			quotedColumns := map[string]bool{}
			if s, ok := source.(*Scope); ok && s.Expression.Is(exp.Query) {
				for _, sel := range s.Expression.Selects() {
					if isOutputIdentifierQuoted(sel) {
						quotedColumns[sel.OutputName()] = true
					}
				}
			}

			// The operators belong to a specific source, so a star over a source joined
			// alongside it must expand from that source's own columns
			if !pivotsSet {
				var selectedNode *exp.Expr
				if ss, ok := scope.SelectedSources().Get(table); ok {
					selectedNode = ss.Node
				}
				if selectedNode == nil {
					if t, ok := source.(*exp.Expr); ok && t.IsKind(exp.Table) {
						// A pivoted CTE reference is registered under the pivot's alias, a name
						// `references` doesn't know, so it's absent from `selected_sources`
						selectedNode = t
					}
				}
				if selectedNode != nil {
					pivots = selectedNode.List("pivots")
				}
			}
			if len(pivots) > 0 {
				// Each operator consumes the previous one's output, so fold them in order
				pivotColumns := columns
				for _, pivot := range pivots {
					out, _ := pivot.PivotOutputColumns(pivotColumns)
					if len(out) == 0 {
						out = pivot.AliasColumnNames()
					}
					pivotColumns = out
				}
				if len(pivotColumns) > 0 {
					var tableAlias any
					if a := pivots[len(pivots)-1].Alias(); a != "" {
						tableAlias = a
					}
					for _, name := range pivotColumns {
						if !columnsToExclude[name] {
							newSelections = append(newSelections, exp.NewAlias(exp.NewColumn(name, tableAlias), name, exp.WithCopy(false)))
						}
					}
					continue
				}
			}

			for _, name := range columns {
				if columnsToExclude[name] || coalescedColumns[name] {
					continue
				}
				if ilikePattern != nil && !ilikePattern.MatchString(name) {
					continue
				}
				var selection *exp.Expr
				if ut, ok := usingColumnTables[name]; ok && ut.set[table] {
					coalescedColumns[name] = true
					var coalesceArgs []any
					for _, t := range ut.keys {
						coalesceArgs = append(coalesceArgs, exp.NewColumn(name, t))
					}
					selection = exp.NewAlias(exp.NewFunc("coalesce", coalesceArgs...), name, exp.WithCopy(false))
				} else {
					alias := name
					if r, ok := renamedColumns[name]; ok {
						alias = r
					}
					_, isTable := source.(*exp.Expr)
					quoted := quotedColumns[name] || (isTable && dialect.CaseSensitive(name))
					selectionExpr := replacedColumns[name]
					if selectionExpr == nil {
						selectionExpr = exp.NewColumnFields(name, table, nil, nil, nil, &quoted, true)
					}
					if alias != name {
						selection = exp.NewAlias(selectionExpr, alias, exp.WithCopy(false))
					} else {
						selection = selectionExpr
					}
				}
				if v, ok := usingSelections.Get(name); ok && v == nil {
					usingSelections.Set(name, selection)
				} else {
					newSelections = append(newSelections, selection)
				}
			}
		}
		var front []*exp.Expr
		for _, s := range usingSelections.Values() {
			if s != nil {
				front = append(front, s)
			}
		}
		if len(front) > 0 {
			rest := append([]*exp.Expr(nil), newSelections[start:]...)
			newSelections = append(append(newSelections[:start], front...), rest...)
		}
	}
	// Ensures we don't overwrite the initial selections with an empty list
	if len(newSelections) > 0 && scopeExpression.IsKind(exp.Select) {
		scopeExpression.Set("expressions", newSelections)
		// The parent scope sees this scope's columns, so they must reflect the expansions
		scope.ClearCache()
	}
}

func outputIdentifier(selection *exp.Expr) *exp.Expr {
	var identifier *exp.Expr
	if selection.IsKind(exp.Alias) {
		identifier = selection.Get("alias")
	} else if selection.IsKind(exp.Column) {
		identifier = selection.This()
	}
	if identifier.IsKind(exp.Identifier) {
		return identifier
	}
	return nil
}

func isOutputIdentifierQuoted(selection *exp.Expr) bool {
	identifier := outputIdentifier(selection)
	return identifier != nil && identifier.Quoted()
}

func addIlikeColumns(expression *exp.Expr, dialect *Dialect) *regexp.Regexp {
	ilike := expression.Get("ilike")
	if ilike == nil {
		return nil
	}
	pattern := []rune(ilike.Name())
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '\\' && dialect.StarIlikeBackslashEscape && i+1 < len(pattern):
			i++
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		case c == '%':
			b.WriteString(".*")
		case c == '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return regexp.MustCompile(`(?i)\A(?:` + b.String() + `)\z`)
}

func addExceptColumns(expression *exp.Expr, tables []tableRef, exceptColumns map[string]map[string]bool) {
	except := expression.List("except_")
	if len(except) == 0 {
		return
	}
	columns := map[string]bool{}
	qualifiedColumns := map[string]map[string]bool{}
	for _, e := range except {
		// A qualified exclusion only applies to the source it references
		if e.IsKind(exp.Column) && e.TableName() != "" {
			if qualifiedColumns[e.TableName()] == nil {
				qualifiedColumns[e.TableName()] = map[string]bool{}
			}
			qualifiedColumns[e.TableName()][e.Name()] = true
		} else {
			columns[e.Name()] = true
		}
	}
	for _, table := range tables {
		tableColumns := columns
		if tq := qualifiedColumns[table.name]; len(tq) > 0 {
			tableColumns = map[string]bool{}
			for c := range columns {
				tableColumns[c] = true
			}
			for c := range tq {
				tableColumns[c] = true
			}
		}
		if len(tableColumns) > 0 {
			exceptColumns[table.id] = tableColumns
		}
	}
}

func addRenameColumns(expression *exp.Expr, tables []tableRef, renameColumns map[string]map[string]string) {
	rename := expression.List("rename")
	if len(rename) == 0 {
		return
	}
	columns := map[string]string{}
	for _, e := range rename {
		columns[e.This().Name()] = e.Alias()
	}
	for _, table := range tables {
		renameColumns[table.id] = columns
	}
}

func addReplaceColumns(expression *exp.Expr, tables []tableRef, replaceColumns map[string]map[string]*exp.Expr) {
	replace := expression.List("replace")
	if len(replace) == 0 {
		return
	}
	columns := map[string]*exp.Expr{}
	for _, e := range replace {
		columns[e.Alias()] = e
	}
	for _, table := range tables {
		replaceColumns[table.id] = columns
	}
}

// QualifyOutputs ensures all output columns are aliased. scopeOrExpression is a *Scope or an
// *exp.Expr.
func QualifyOutputs(scopeOrExpression any, dialect *Dialect) {
	var scope *Scope
	switch x := scopeOrExpression.(type) {
	case *Scope:
		scope = x
	case *exp.Expr:
		scope = BuildScope(x)
		if scope == nil {
			return
		}
	}
	expression := scope.Expression
	if !expression.Is(exp.Selectable) {
		return
	}
	var newSelections []*exp.Expr
	selects := expression.Selects()
	n := max(len(selects), len(scope.OuterColumns))
	for i := 0; i < n; i++ {
		var selection *exp.Expr
		if i < len(selects) {
			selection = selects[i]
		}
		aliasedColumn := ""
		if i < len(scope.OuterColumns) {
			aliasedColumn = scope.OuterColumns[i]
		}
		if selection == nil || selection.IsKind(exp.QueryTransform) {
			break
		}
		if selection.IsKind(exp.Subquery) {
			if selection.OutputName() == "" {
				aliasIdentifier := exp.ToIdentifier(fmt.Sprintf("_col_%d", i))
				dialect.NormalizeIdentifier(aliasIdentifier)
				selection.Set("alias", exp.New(exp.TableAlias, "this", aliasIdentifier))
			}
		} else if !selection.IsAny(exp.Alias, exp.Aliases) && !selection.IsStar() {
			unwrapped := selection.Unnest()
			var sourceIdentifier *exp.Expr
			if unwrapped.IsKind(exp.Column) {
				sourceIdentifier = unwrapped.This()
			} else if unwrapped.IsKind(exp.Dot) {
				sourceIdentifier = unwrapped.Expression()
			}
			name := selection.OutputName()
			if name == "" {
				name = fmt.Sprintf("_col_%d", i)
			}
			selection = exp.NewAlias(selection, name, exp.WithCopy(false))
			if sourceIdentifier.IsKind(exp.Identifier) {
				// The alias copies the exact spelling of an existing identifier, so folding it
				// here would desync it from other occurrences of that identifier; its casing is
				// `normalize_identifiers`' concern, which has already run (or was skipped
				// deliberately) by this point
				if sourceIdentifier.Quoted() {
					selection.Get("alias").Set("quoted", true)
				}
			} else {
				dialect.NormalizeIdentifier(selection.Get("alias"))
			}
		}
		if aliasedColumn != "" {
			selection.Set("alias", exp.ToIdentifier(aliasedColumn))
		}
		newSelections = append(newSelections, selection)
	}
	if len(newSelections) > 0 && expression.IsKind(exp.Select) {
		expression.Set("expressions", newSelections)
	}
}

// QuoteIdentifiers makes sure all identifiers that need to be quoted are quoted.
func QuoteIdentifiers(e *exp.Expr, dialect any, identify bool) *exp.Expr {
	d := MustDialect(dialect)
	// `quote_identifier` only mutates identifiers in place, so `transform` is avoided here
	for node := range e.Walk(true, nil) {
		if node.IsKind(exp.Identifier) {
			d.QuoteIdentifier(node, identify)
		}
	}
	return e
}

// PushdownCteAliasColumns pushes down the CTE alias columns into the projection. This is
// useful in Snowflake where the CTE alias columns can be referenced in the HAVING.
func PushdownCteAliasColumns(scope *Scope) {
	for _, cte := range scope.Ctes() {
		if len(cte.AliasColumnNames()) > 0 && cte.This().IsKind(exp.Select) {
			var newExpressions []*exp.Expr
			aliases := cte.Get("alias").Columns()
			projections := cte.This().Expressions()
			for i := 0; i < len(aliases) && i < len(projections); i++ {
				alias, projection := aliases[i], projections[i]
				if projection.IsKind(exp.Alias) {
					projection.Set("alias", alias.Copy())
				} else {
					projection = exp.NewAlias(projection, alias)
				}
				newExpressions = append(newExpressions, projection)
			}
			cte.This().Set("expressions", newExpressions)
		}
	}
}
