package sqlglot

// Port of sqlglot/lineage.py.

import (
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// LineageNode is one step of a column's lineage: the expression producing the column within
// its source query, and the nodes it is derived from.
type LineageNode struct {
	Name              string
	Expression        *exp.Expr
	Source            *exp.Expr
	Downstream        []*LineageNode
	SourceName        string
	ReferenceNodeName string
	// Payload holds caller data, typically populated by LineageOptions.OnNode.
	Payload map[string]any
}

func newLineageNode(name string, expression, source *exp.Expr, sourceName, referenceNodeName string) *LineageNode {
	return &LineageNode{
		Name: name, Expression: expression, Source: source,
		SourceName: sourceName, ReferenceNodeName: referenceNodeName, Payload: map[string]any{},
	}
}

// Walk yields the node and its transitive downstream nodes (each once), depth first.
func (n *LineageNode) Walk() iter.Seq[*LineageNode] {
	return func(yield func(*LineageNode) bool) {
		visited := map[*LineageNode]bool{}
		queue := []*LineageNode{n}
		for len(queue) > 0 {
			node := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			if visited[node] {
				continue
			}
			visited[node] = true
			if !yield(node) {
				return
			}
			for i := len(node.Downstream) - 1; i >= 0; i-- {
				queue = append(queue, node.Downstream[i])
			}
		}
	}
}

// LineageOptions are the optional arguments of Lineage.
type LineageOptions struct {
	// Sources maps table names to queries (SQL strings or expressions) used to continue the
	// lineage through tables that are views or other derived relations.
	Sources map[string]any
	Dialect any
	// Scope is a pre-built scope to use instead of qualifying the expression.
	Scope *Scope
	// NoTrimSelects keeps every projection in the node sources (trim_selects=False).
	NoTrimSelects bool
	// NoCopy lets Lineage modify the expression arguments (copy=False).
	NoCopy bool
	// OnNode is called for every node, after its downstream nodes are populated.
	OnNode func(*LineageNode)
	// Qualify sets the qualification options. When nil, the columns are qualified without
	// validation and without quoting all identifiers. Dialect and Schema are always taken
	// from the Lineage arguments.
	Qualify *QualifyOptions
}

type lineageWalk struct {
	dialect     any
	schema      Schema
	trimSelects bool
	cache       map[lineageKey]*LineageNode
	scopeMeta   map[*Scope]*lineageScopeMeta
	onNode      func(*LineageNode)
}

type lineageKey struct {
	column            any
	scope             *Scope
	scopeName         string
	sourceName        string
	referenceNodeName string
}

type lineageScopeMeta struct {
	isStar       bool
	selectByName map[string]*exp.Expr
}

func prepareLineage(sql any, schema any, o LineageOptions) (*Scope, *lineageWalk, error) {
	expression, err := exp.MaybeParse(sql, exp.ParseOptions{Dialect: o.Dialect}, "", !o.NoCopy)
	if err != nil {
		return nil, nil, err
	}
	if len(o.Sources) > 0 {
		sources := map[string]ExpandSource{}
		for k, v := range o.Sources {
			q, err := exp.MaybeParse(v, exp.ParseOptions{Dialect: o.Dialect}, "", !o.NoCopy)
			if err != nil {
				return nil, nil, err
			}
			sources[k] = q
		}
		expression = Expand(expression, sources, o.Dialect, !o.NoCopy)
	}
	s, err := EnsureSchema(schema, o.Dialect)
	if err != nil {
		return nil, nil, err
	}
	scope := o.Scope
	if scope == nil {
		qo := QualifyOptions{NoValidateQualifyColumns: true, NoIdentify: true}
		if o.Qualify != nil {
			qo = *o.Qualify
		}
		qo.Dialect, qo.Schema = o.Dialect, s
		if expression, err = Qualify(expression, qo); err != nil {
			return nil, nil, err
		}
		scope = BuildScope(expression)
	}
	if scope == nil {
		return nil, nil, &errs.SqlglotErr{Message: "Cannot build lineage, sql must be SELECT"}
	}
	if !scope.Expression.Is(exp.Selectable) {
		return nil, nil, &errs.SqlglotErr{Message: "Cannot build lineage, sql must be a query"}
	}
	w := &lineageWalk{
		dialect: o.Dialect, schema: s, trimSelects: !o.NoTrimSelects,
		cache: map[lineageKey]*LineageNode{}, scopeMeta: map[*Scope]*lineageScopeMeta{}, onNode: o.OnNode,
	}
	return scope, w, nil
}

// Lineage builds the lineage graph of one output column of a query: column is a column name
// or *exp.Expr column, sql a SQL string or expression, and schema the schema of the tables
// (a Schema or a nested mapping).
func Lineage(column any, sql any, schema any, o LineageOptions) (node *LineageNode, err error) {
	defer recoverOptimize(&err)
	scope, w, err := prepareLineage(sql, schema, o)
	if err != nil {
		return nil, err
	}
	columnName := NormalizeIdentifiers(column, o.Dialect).Name()
	found := false
	for _, sel := range scope.Expression.Selects() {
		if sel.AliasOrName() == columnName {
			found = true
			break
		}
	}
	if !found {
		return nil, &errs.SqlglotErr{Message: fmt.Sprintf("Cannot find column '%s' in query.", columnName)}
	}
	return w.toNode(columnName, scope, "", nil, "", "", false), nil
}

// LineageAll builds the lineage graph of every output column of a query (Lineage with
// column=None), sharing the work between columns. The result is ordered like the projections.
func LineageAll(sql any, schema any, o LineageOptions) (result *OrderedMap[*LineageNode], err error) {
	defer recoverOptimize(&err)
	scope, w, err := prepareLineage(sql, schema, o)
	if err != nil {
		return nil, err
	}
	result = NewOrderedMap[*LineageNode]()
	for _, sel := range scope.Expression.Selects() {
		name := sel.AliasOrName()
		if name == "" {
			return nil, &errs.SqlglotErr{Message: fmt.Sprintf("Cannot fetch lineage for unnamed projection: %s.", genSQL(sel, o.Dialect))}
		}
		result.Set(name, w.toNode(name, scope, "", nil, "", "", false))
	}
	return result, nil
}

func genSQL(e *exp.Expr, dialect any, opts ...exp.GenOption) string {
	args := []any{dialect}
	for _, o := range opts {
		args = append(args, o)
	}
	s, err := e.Generate(args...)
	if err != nil {
		panic(err)
	}
	return s
}

// toNode mirrors lineage.to_node; column is a name (string) or a projection index (int).
func (w *lineageWalk) toNode(column any, scope *Scope, scopeName string, upstream *LineageNode,
	sourceName, referenceNodeName string, _ bool) *LineageNode {
	key := lineageKey{column, scope, scopeName, sourceName, referenceNodeName}
	if cached, ok := w.cache[key]; ok {
		if upstream != nil {
			upstream.Downstream = append(upstream.Downstream, cached)
		}
		return cached
	}

	selectable := scope.Expression
	var sel *exp.Expr
	switch c := column.(type) {
	case int:
		selects := selectable.Selects()
		if c >= len(selects) {
			panic(&errs.SqlglotErr{Message: fmt.Sprintf("Cannot find column's source with index %d in query: %s", c, genSQL(selectable, w.dialect))})
		}
		sel = selects[c]
	case string:
		meta, ok := w.scopeMeta[scope]
		if !ok {
			meta = &lineageScopeMeta{isStar: selectable.IsStar(), selectByName: map[string]*exp.Expr{}}
			for _, s := range selectable.Selects() {
				if _, seen := meta.selectByName[s.AliasOrName()]; !seen {
					meta.selectByName[s.AliasOrName()] = s
				}
			}
			w.scopeMeta[scope] = meta
		}
		if s, ok := meta.selectByName[c]; ok {
			sel = s
		} else if meta.isStar {
			sel = exp.New(exp.Star)
		} else {
			sel = scope.Expression
		}
	}

	if scope.Expression.IsKind(exp.Subquery) {
		for _, inner := range scope.SubqueryScopes {
			result := w.toNode(column, inner, "", upstream, sourceName, referenceNodeName, false)
			// Skip caching a passed-in upstream returned by an inner set operation: a sibling
			// call at the same key with that node as its upstream would otherwise self-loop on
			// the cache hit.
			if result != upstream {
				w.cache[key] = result
			}
			return result
		}
	}

	if scope.Expression.Is(exp.SetOperation) {
		createdSetop := upstream == nil
		if upstream == nil {
			upstream = newLineageNode(strings.ToUpper(scope.Expression.ClassName()), sel, scope.Expression, "", "")
		}
		index := -1
		if i, ok := column.(int); ok {
			index = i
		} else {
			for i, s := range selectable.Selects() {
				if s.AliasOrName() == column || s.IsStar() {
					index = i
					break
				}
			}
		}
		if index == -1 {
			panic(errs.Valuef("Could not find %v in %s", column, scope.Expression.SQL()))
		}
		for _, s := range scope.SetOperationScopes {
			w.toNode(index, s, "", upstream, sourceName, referenceNodeName, false)
		}
		if createdSetop {
			w.cache[key] = upstream
			if w.onNode != nil {
				w.onNode(upstream)
			}
		}
		return upstream
	}

	var source *exp.Expr
	if w.trimSelects && scope.Expression.IsKind(exp.Select) {
		// For better ergonomics in the node labels, keep only the column we care about:
		//   "x", SELECT x, y FROM foo => "x", SELECT x FROM foo
		source = scope.Expression.Select(sel, exp.WithAppend(false))
	} else {
		source = scope.Expression
	}

	name := fmt.Sprint(column)
	if scopeName != "" {
		name = scopeName + "." + name
	}
	node := newLineageNode(name, sel, source, sourceName, referenceNodeName)
	if upstream != nil {
		upstream.Downstream = append(upstream.Downstream, node)
	}

	subqueryScopes := map[*exp.Expr]*Scope{}
	for _, s := range scope.SubqueryScopes {
		subqueryScopes[s.Expression] = s
	}
	for subquery := range FindAllInScope(sel, exp.Select, exp.SetOperation) {
		subqueryScope, ok := subqueryScopes[subquery]
		if !ok {
			Logger.Warn(fmt.Sprintf("Unknown subquery scope: %s", genSQL(subquery, w.dialect)))
			continue
		}
		for _, n := range subquery.NamedSelects() {
			w.toNode(n, subqueryScope, "", node, "", "", false)
		}
	}

	// if the select is a star add all scope sources as downstreams
	if sel.IsKind(exp.Star) {
		for _, src := range scope.Sources.Values() {
			var srcExpr *exp.Expr
			if s, ok := src.(*Scope); ok {
				srcExpr = s.Expression
			} else {
				srcExpr = src.(*exp.Expr)
			}
			starNode := newLineageNode(genSQL(sel, nil, exp.Comments(false)), srcExpr, srcExpr, "", "")
			node.Downstream = append(node.Downstream, starNode)
			if w.onNode != nil {
				w.onNode(starNode)
			}
		}
	}

	// Find all columns that went into creating this one to list their lineage nodes.
	var sourceColumns []*exp.Expr
	seenColumns := map[exp.ExprKey][]*exp.Expr{}
	addColumn := func(c *exp.Expr) {
		k := c.MapKey()
		for _, other := range seenColumns[k] {
			if other.Equal(c) {
				return
			}
		}
		seenColumns[k] = append(seenColumns[k], c)
		sourceColumns = append(sourceColumns, c)
	}
	for c := range FindAllInScope(sel, exp.Column) {
		addColumn(c)
	}

	var derivedTables []*exp.Expr
	// If the source is a UDTF find columns used in the UDTF to generate the table
	if source.Is(exp.UDTF) {
		for c := range source.FindAll(exp.Column) {
			addColumn(c)
		}
		for _, src := range scope.Sources.Values() {
			if s, ok := src.(*Scope); ok && s.IsDerivedTable() && s.Expression.Parent() != nil {
				derivedTables = append(derivedTables, s.Expression.Parent())
			}
		}
	} else {
		derivedTables = scope.DerivedTables()
	}

	sourceNames := map[string]string{}
	for _, dt := range derivedTables {
		if len(dt.Comments) > 0 && strings.HasPrefix(dt.Comments[0], "source: ") {
			if fields := strings.Fields(dt.Comments[0]); len(fields) > 1 {
				sourceNames[dt.Alias()] = fields[1]
			}
		}
	}
	sourceNameOf := func(table string) string {
		if n := sourceNames[table]; n != "" {
			return n
		}
		return sourceName
	}

	pivots := scope.Pivots()
	if len(pivots) > 0 && pivots[0].Parent() != pivots[len(pivots)-1].Parent() {
		// The scope's pivots only form a chain when they all hang off of the same source;
		// otherwise they can't be folded, so their columns degrade to unresolved leaves
		pivots = nil
	}
	pivotRenames := map[string]string{}
	pivotColumnMapping := NewOrderedMap[[]*exp.Expr]()
	if len(pivots) > 0 {
		pivotRenames, pivotColumnMapping = pivotChainMapping(pivots, scope, w.schema)
	}

	// Python reassigns its reference_node_name parameter in the loop below, and the pivot
	// branch reads the latest value
	refName := referenceNodeName
	for _, c := range sourceColumns {
		table := c.TableName()
		colSource, _ := scope.Sources.Get(table)

		// Lineage-specific single-source inference: when the optimizer could not qualify a
		// column (e.g. no schema was provided), attribute it to the sole named source so the
		// graph remains accurate rather than degrading to a Placeholder leaf.
		if colSource == nil && table == "" {
			var names []string
			for k := range scope.Sources.All() {
				if k != "" {
					names = append(names, k)
				}
			}
			if len(names) == 1 {
				table = names[0]
				colSource, _ = scope.Sources.Get(table)
				c.Set("table", exp.ToIdentifier(table))
			}
		}

		if s, ok := colSource.(*Scope); ok {
			refName = ""
			if s.ScopeType == ScopeDerivedTable {
				if _, named := sourceNames[table]; !named {
					refName = table
				}
			} else if s.ScopeType == ScopeCTE {
				if selected, ok := scope.SelectedSources().Get(table); ok && selected.Node != nil {
					refName = selected.Node.Name()
				}
			}
			// The table itself came from a more specific scope. Recurse into that one using the
			// unaliased column name.
			w.toNode(c.Name(), s, table, node, sourceNameOf(table), refName, false)
		} else if len(pivots) > 0 && pivots[len(pivots)-1].AliasOrName() == c.TableName() {
			// Only the last operator in a chain names the resulting source
			pivotParent := pivots[len(pivots)-1].Parent()
			parentName := ""
			if pivotParent != nil {
				parentName = pivotParent.AliasOrName()
			}
			var downstreamColumns []*exp.Expr
			if cols, ok := pivotColumnMapping.Get(c.Name()); ok {
				downstreamColumns = append(downstreamColumns, cols...)
			} else {
				// The column is not in the pivot, so it must be an implicit column of the
				// pivoted source: adapt the column to be from the implicit pivoted source.
				var colName any = c.This()
				if r, ok := pivotRenames[c.Name()]; ok {
					colName = r
				}
				downstreamColumns = append(downstreamColumns, exp.NewColumn(colName, nilIfEmpty(parentName)))
			}
			for _, downstreamColumn := range downstreamColumns {
				if downstreamColumn.TableName() == "" {
					// Some dialects (e.g. bigquery) don't qualify the IN-list columns, but they
					// can only come from the pivoted source
					downstreamColumn = exp.NewColumn(downstreamColumn.This(), nilIfEmpty(parentName))
				}
				table := downstreamColumn.TableName()
				colSource, _ := scope.Sources.Get(table)
				if t, ok := colSource.(*exp.Expr); ok && t.Is(exp.Table) && t.DB() == "" {
					// A pivoted CTE reference maps to the raw table in scope.sources, so recover
					// the CTE's scope to keep tracing through it
					if cte, ok := scope.CteSources.Get(t.Name()); ok {
						colSource = cte
					}
				}
				if s, ok := colSource.(*Scope); ok {
					w.toNode(downstreamColumn.Name(), s, table, node, sourceNameOf(table), refName, false)
				} else {
					colExpr := lineageLeafExpr(colSource)
					leaf := newLineageNode(genSQL(downstreamColumn, nil, exp.Comments(false)), colExpr, colExpr, "", "")
					node.Downstream = append(node.Downstream, leaf)
					if w.onNode != nil {
						w.onNode(leaf)
					}
				}
			}
		} else {
			// The source is not a scope and the column is not in any pivot: we've reached the
			// end of the line. If a source is not found, this column's lineage is unknown,
			// e.g. because the definition of a source used in the query was not passed in
			// LineageOptions.Sources.
			colExpr := lineageLeafExpr(colSource)
			leaf := newLineageNode(genSQL(c, nil, exp.Comments(false)), colExpr, colExpr, "", "")
			node.Downstream = append(node.Downstream, leaf)
			if w.onNode != nil {
				w.onNode(leaf)
			}
		}
	}

	w.cache[key] = node
	if w.onNode != nil {
		w.onNode(node)
	}
	return node
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func lineageLeafExpr(source Source) *exp.Expr {
	if e, ok := source.(*exp.Expr); ok && e != nil {
		return e
	}
	return exp.New(exp.Placeholder)
}

// prePivotColumns returns the columns the first operator of a pivot chain sees: the
// projections of a derived table or CTE source, or the schema columns of a physical table.
// It returns nil when they can't be determined (e.g. an unexpanded star), since anything
// positional over them would silently shift.
func prePivotColumns(pivot *exp.Expr, scope *Scope, schema Schema) []string {
	parent := pivot.Parent()
	var columns []string
	if parent.Is(exp.DerivedTable) && parent.This().Is(exp.Query) {
		columns = parent.This().NamedSelects()
	} else if parent.Is(exp.Table) {
		var cteSource Source
		if parent.DB() == "" {
			cteSource, _ = scope.CteSources.Get(parent.Name())
		}
		if s, ok := cteSource.(*Scope); ok && s.Expression.Is(exp.Query) {
			columns = s.Expression.NamedSelects()
		} else if schema != nil {
			names, err := schema.ColumnNames(parent, true, nil, nil)
			if err != nil {
				panic(err)
			}
			columns = names
		}
	}
	for _, c := range columns {
		if c == "*" {
			return nil
		}
	}
	return columns
}

// pivotChainMapping folds a chain of (UN)PIVOT operators into a single view of its output,
// since each one consumes the previous one's columns rather than the pivoted source's. It
// returns the composed output name -> pre-chain name renames (from alias column lists), and
// the composed output name -> source columns it derives from.
func pivotChainMapping(pivots []*exp.Expr, scope *Scope, schema Schema) (map[string]string, *OrderedMap[[]*exp.Expr]) {
	available := prePivotColumns(pivots[0], scope, schema)
	renames := map[string]string{}
	mapping := NewOrderedMap[[]*exp.Expr]()

	for _, pivot := range pivots {
		// Renames are positional over the operator's full output, so they can only be applied
		// when the columns going into it are known
		var stepOrder []string
		stepRenames := map[string]string{}
		if len(pivot.AliasColumnNames()) > 0 && len(available) > 0 {
			stepOrder, stepRenames = pivot.PivotOutputColumns(available)
		}
		stepMapping := pivotColumnMapping(pivot)
		if len(stepOrder) > 0 {
			renamed := NewOrderedMap[[]*exp.Expr]()
			for _, post := range stepOrder {
				if cols, ok := stepMapping.Get(stepRenames[post]); ok {
					renamed.Set(post, cols)
				}
			}
			stepMapping = renamed
		}

		// Columns this operator consumed may have been produced by an earlier one, or be
		// alias-list renames of passthroughs; resolve through what we've folded so far
		resolve := func(col *exp.Expr) []*exp.Expr {
			if cols, ok := mapping.Get(col.Name()); ok {
				return cols
			}
			if r, ok := renames[col.Name()]; ok {
				return []*exp.Expr{exp.NewColumn(r)}
			}
			return []*exp.Expr{col}
		}
		composed := NewOrderedMap[[]*exp.Expr]()
		for out, cols := range stepMapping.All() {
			var resolved []*exp.Expr
			for _, col := range cols {
				resolved = append(resolved, resolve(col)...)
			}
			composed.Set(out, resolved)
		}

		// Whatever an earlier operator produced and this one didn't consume passes through,
		// under whatever name this operator's alias column list gives it
		consumed := map[string]bool{}
		for _, cols := range stepMapping.Values() {
			for _, col := range cols {
				consumed[col.Name()] = true
			}
		}
		preToPost := map[string]string{}
		for _, post := range stepOrder {
			preToPost[stepRenames[post]] = post
		}
		for out, cols := range mapping.All() {
			if !consumed[out] {
				name := out
				if post, ok := preToPost[out]; ok {
					name = post
				}
				if !composed.Has(name) {
					composed.Set(name, cols)
				}
			}
		}

		if len(stepOrder) > 0 {
			next := map[string]string{}
			for _, post := range stepOrder {
				pre := stepRenames[post]
				if r, ok := renames[pre]; ok {
					pre = r
				}
				next[post] = pre
			}
			renames = next
		}
		mapping = composed
		if len(available) > 0 {
			available, _ = pivot.PivotOutputColumns(available)
		} else {
			available = nil
		}
	}
	return renames, mapping
}

// pivotColumnMapping maps each (UN)PIVOT output column name to the source columns it's
// derived from.
func pivotColumnMapping(pivot *exp.Expr) *OrderedMap[[]*exp.Expr] {
	mapping := NewOrderedMap[[]*exp.Expr]()

	if pivot.Unpivot() {
		// UNPIVOT((v1, v2) FOR name IN ((a1, a2), (b1, b2))): each value column is derived
		// positionally from the IN-list entries, and the name column from all of them
		var valueColumns []*exp.Expr
		for _, e := range pivot.Expressions() {
			for id := range e.FindAll(exp.Identifier) {
				valueColumns = append(valueColumns, id)
			}
		}
		for _, v := range valueColumns {
			mapping.Set(v.Name(), []*exp.Expr{})
		}
		for _, field := range pivot.Fields() {
			if !field.Is(exp.In) {
				continue
			}
			nameKey := field.This().Name()
			if !mapping.Has(nameKey) {
				mapping.Set(nameKey, []*exp.Expr{})
			}
			for _, entry := range field.Expressions() {
				entryColumns := entry.FindAllList(exp.Column)
				cols, _ := mapping.Get(nameKey)
				mapping.Set(nameKey, append(cols, entryColumns...))
				if len(entryColumns) == len(valueColumns) {
					for i, v := range valueColumns {
						cols, _ := mapping.Get(v.Name())
						mapping.Set(v.Name(), append(cols, entryColumns[i]))
					}
				} else {
					for _, v := range valueColumns {
						cols, _ := mapping.Get(v.Name())
						mapping.Set(v.Name(), append(cols, entryColumns...))
					}
				}
			}
		}
		return mapping
	}

	// For each aggregation function, the pivot creates a new column for each field in the
	// category combined with the aggregation. The columns are ordered cat_a_value_sum, cat_a,
	// b_value_sum, b: the aggregation 'SUM(value) AS value_sum' belongs to the column indices
	// 0 and 2, and 'MAX(price)' to the indices 1 and 3. Only the columns used in the
	// aggregations matter for the lineage, so look the pivot column name up by index and map
	// it to the columns used in the aggregation.
	//
	// Example: PIVOT (SUM(value) AS value_sum, MAX(price)) FOR category IN ('a' AS cat_a, 'b')
	pivotColumns := pivot.List("columns")
	aggs := pivot.Expressions()
	for i, agg := range aggs {
		aggCols := agg.FindAllList(exp.Column)
		for colIndex := i; colIndex < len(pivotColumns); colIndex += len(aggs) {
			mapping.Set(pivotColumns[colIndex].Name(), aggCols)
		}
	}
	return mapping
}

// GraphHTMLOptions configures LineageNode.ToHTML.
type GraphHTMLOptions struct {
	// NoImports omits the vis.js script and stylesheet tags (imports=False).
	NoImports bool
	// Options override the top-level vis.js network options.
	Options map[string]any
}

// GraphHTML renders a lineage graph with vis.js (https://visjs.github.io/vis-network/docs/network/).
type GraphHTML struct {
	Nodes   []map[string]any
	Edges   []map[string]any
	Imports bool
	Options *OrderedMap[any]
}

// ToHTML mirrors Node.to_html: an HTML page rendering the lineage graph.
func (n *LineageNode) ToHTML(dialect any, o GraphHTMLOptions) *GraphHTML {
	ids := map[*LineageNode]int{}
	idOf := func(node *LineageNode) int {
		if id, ok := ids[node]; ok {
			return id
		}
		ids[node] = len(ids) + 1
		return ids[node]
	}
	g := &GraphHTML{Imports: !o.NoImports, Options: defaultGraphOptions()}
	for k, v := range o.Options {
		g.Options.Set(k, v)
	}
	for node := range n.Walk() {
		var label, title string
		group := 0
		if node.Expression.Is(exp.Table) {
			label = "FROM " + node.Expression.This().SQL()
			title = fmt.Sprintf("<pre>SELECT %s FROM %s</pre>", node.Name, node.Expression.This().SQL())
			group = 1
		} else {
			label = genSQL(node.Expression, dialect, exp.Pretty(true))
			target := node.Expression
			source := node.Source.Transform(func(e *exp.Expr) *exp.Expr {
				if e == target {
					return exp.New(exp.Tag, "this", e, "prefix", "<b>", "postfix", "</b>")
				}
				return e
			}, false)
			title = "<pre>" + genSQL(source, dialect, exp.Pretty(true)) + "</pre>"
		}
		id := idOf(node)
		g.Nodes = append(g.Nodes, map[string]any{"id": id, "label": label, "title": title, "group": group})
		for _, d := range node.Downstream {
			g.Edges = append(g.Edges, map[string]any{"from": id, "to": idOf(d)})
		}
	}
	return g
}

func defaultGraphOptions() *OrderedMap[any] {
	o := NewOrderedMap[any]()
	o.Set("height", "500px")
	o.Set("width", "100%")
	o.Set("layout", map[string]any{"hierarchical": map[string]any{"enabled": true, "nodeSpacing": 200, "sortMethod": "directed"}})
	o.Set("interaction", map[string]any{"dragNodes": false, "selectable": false})
	o.Set("physics", map[string]any{"enabled": false})
	o.Set("edges", map[string]any{"arrows": "to"})
	o.Set("nodes", map[string]any{"font": "20px monaco", "shape": "box", "widthConstraint": map[string]any{"maximum": 300}})
	return o
}

func orderedJSON(keys []string, get func(string) any) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(get(k))
		b.Write(kb)
		b.WriteString(": ")
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.String()
}

// String renders the HTML.
func (g *GraphHTML) String() string {
	var nodes []string
	for _, n := range g.Nodes {
		nodes = append(nodes, orderedJSON([]string{"id", "label", "title", "group"}, func(k string) any { return n[k] }))
	}
	var edges []string
	for _, e := range g.Edges {
		edges = append(edges, orderedJSON([]string{"from", "to"}, func(k string) any { return e[k] }))
	}
	options := orderedJSON(g.Options.Keys(), func(k string) any { v, _ := g.Options.Get(k); return v })
	imports := ""
	if g.Imports {
		imports = `<script type="text/javascript" src="https://unpkg.com/vis-data@latest/peer/umd/vis-data.min.js"></script>
  <script type="text/javascript" src="https://unpkg.com/vis-network@latest/peer/umd/vis-network.min.js"></script>
  <link rel="stylesheet" type="text/css" href="https://unpkg.com/vis-network/styles/vis-network.min.css" />`
	}
	return fmt.Sprintf(`<div>
  <div id="sqlglot-lineage"></div>
  %s
  <script type="text/javascript">
    var nodes = new vis.DataSet([%s])
    nodes.forEach(row => row["title"] = new DOMParser().parseFromString(row["title"], "text/html").body.childNodes[0])

    new vis.Network(
        document.getElementById("sqlglot-lineage"),
        {
            nodes: nodes,
            edges: new vis.DataSet([%s])
        },
        %s,
    )
  </script>
</div>`, imports, strings.Join(nodes, ", "), strings.Join(edges, ", "), options)
}
