package sqlglot

// Port of sqlglot/optimizer/resolver.py.

import (
	"fmt"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
)

type sourceColumnsKey struct {
	name        string
	onlyVisible bool
}

type columnTypeKey struct {
	source any // *exp.Expr or *Scope
	column string
}

type cachedType struct {
	t *exp.Expr
}

// Resolver mirrors resolver.Resolver: a helper for resolving columns. It lazily computes and
// caches the source columns of a scope.
type Resolver struct {
	Scope   *Scope
	Schema  Schema
	Dialect *Dialect

	sourceColumns         *OrderedMap[[]string]
	unambiguousColumns    map[string]string
	allColumns            map[string]bool
	getSourceColumnsCache map[sourceColumnsKey][]string
	columnTypeCache       map[columnTypeKey]cachedType
	outerResolvers        []*Resolver
	outerResolversSet     bool
	selectAliases         map[string]bool
}

// NewResolver mirrors Resolver(scope, schema).
func NewResolver(scope *Scope, schema Schema) *Resolver {
	d := schema.Dialect()
	if d == nil {
		d = BaseDialectClass().NewInstance()
	}
	return &Resolver{
		Scope:                 scope,
		Schema:                schema,
		Dialect:               d,
		getSourceColumnsCache: map[sourceColumnsKey][]string{},
		columnTypeCache:       map[columnTypeKey]cachedType{},
	}
}

func optimizeErr(format string, args ...any) {
	panic(&errs.OptimizeError{Message: fmt.Sprintf(format, args...)})
}

// catchOptimizeError runs f and reports whether it raised an OptimizeError.
func catchOptimizeError(f func()) (failed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(*errs.OptimizeError); ok {
				failed = true
				return
			}
			panic(r)
		}
	}()
	f()
	return false
}

// GetTable mirrors Resolver.get_table: the table for a column or struct root identifier, if
// it can be found or inferred.
func (r *Resolver) GetTable(column *exp.Expr) *exp.Expr {
	columnName := column.Name()
	joinContext := r.getColumnJoinContext(column)
	isSemiOrAntiJoin := joinContext != nil && joinContext.IsSemiOrAntiJoin()
	// A SEMI/ANTI join's own table is excluded from the global lookup below, so skip it here
	tableName := ""
	if !isSemiOrAntiJoin {
		tableName = r.getTableNameFromSources(columnName, nil)
	}
	if tableName == "" && joinContext != nil {
		// Fall-back case: If we couldn't find the `table_name` from ALL of the sources, attempt
		// to disambiguate the column based on other characteristics e.g if this column is in a
		// join condition, we may be able to disambiguate based on the source order.
		catchOptimizeError(func() {
			tableName = r.getTableNameFromSources(columnName, r.getAvailableSourceColumns(joinContext))
		})
	}
	if tableName == "" && !r.AllColumns()[columnName] && column.FindAncestor(exp.Select) == r.Scope.Expression &&
		!(r.Dialect.TablesReferenceableAsColumns && r.Scope.SelectedSources().Has(columnName)) {
		// Skip back-projection for alias refs — _expand_alias_refs handles them.
		ancestor := column.FindAncestor(exp.Alias, exp.Select)
		if !r.SelectAliases()[columnName] || (ancestor.IsKind(exp.Alias) && ancestor.Alias() == columnName) {
			var sourceColumns *OrderedMap[[]string]
			if joinContext != nil {
				sourceColumns = r.getAvailableSourceColumns(joinContext)
			} else {
				sourceColumns = r.getAllSourceColumns()
			}
			tableName = r.getUnknownTVFTable(sourceColumns)
		}
	}
	if tableName == "" {
		return nil
	}
	selected, ok := r.Scope.SelectedSources().Get(tableName)
	if !ok {
		return exp.ToIdentifier(tableName)
	}
	node := selected.Node
	if node.Is(exp.Query) {
		for node != nil && node.Alias() != tableName && node.Parent() != nil {
			node = node.Parent()
		}
	}
	if nodeAlias := node.Get("alias"); nodeAlias != nil {
		return exp.ToIdentifier(nodeAlias.Arg("this"))
	}
	return exp.ToIdentifier(tableName)
}

// OuterResolvers returns the resolvers for the outer scopes a correlated subquery can
// reference, innermost first.
func (r *Resolver) OuterResolvers() []*Resolver {
	if !r.outerResolversSet {
		r.outerResolversSet = true
		scope := r.Scope
		for scope.CanBeCorrelated && scope.Parent != nil {
			scope = scope.Parent
			r.outerResolvers = append(r.outerResolvers, NewResolver(scope, r.Schema))
		}
	}
	return r.outerResolvers
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (r *Resolver) getUnknownTVFTable(sourceColumns *OrderedMap[[]string]) string {
	if !r.HasUnknownSources() || r.Scope.CanBeCorrelated {
		return ""
	}
	var unknownTVFNames []string
	for _, name := range sourceColumns.Keys() {
		columns, _ := sourceColumns.Get(name)
		if len(columns) > 0 && !containsStr(columns, "*") {
			continue
		}
		source, _ := r.Scope.Sources.Get(name)
		// Non-TVF tables are expected to have schemas supplied, so only functions can be unknown
		if t, ok := source.(*exp.Expr); ok && t.IsKind(exp.Table) && t.This().Is(exp.Func) {
			unknownTVFNames = append(unknownTVFNames, name)
		} else {
			return ""
		}
	}
	if len(unknownTVFNames) == 1 {
		return unknownTVFNames[0]
	}
	return ""
}

// HasUnknownSources reports whether some source's columns can't be determined, e.g. a table
// missing from the schema.
func (r *Resolver) HasUnknownSources() bool {
	all := r.getAllSourceColumns()
	for _, columns := range all.Values() {
		if len(columns) == 0 || containsStr(columns, "*") {
			return true
		}
	}
	return false
}

// AllColumns returns all available columns of all sources in this scope.
func (r *Resolver) AllColumns() map[string]bool {
	if r.allColumns == nil {
		r.allColumns = map[string]bool{}
		for _, columns := range r.getAllSourceColumns().Values() {
			for _, c := range columns {
				r.allColumns[c] = true
			}
		}
	}
	return r.allColumns
}

// SelectAliases returns the aliases of the scope's projections.
func (r *Resolver) SelectAliases() map[string]bool {
	if r.selectAliases == nil {
		r.selectAliases = map[string]bool{}
		if e := r.Scope.Expression; e.IsKind(exp.Select) {
			for _, s := range e.Selects() {
				if s.IsKind(exp.Alias) {
					r.selectAliases[s.Alias()] = true
				}
			}
		}
	}
	return r.selectAliases
}

func dedupStrings(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// GetSourceColumnsFromSetOp mirrors Resolver.get_source_columns_from_set_op.
func (r *Resolver) GetSourceColumnsFromSetOp(e *exp.Expr) []string {
	if e.IsKind(exp.Select) {
		return e.NamedSelects()
	}
	if e.IsKind(exp.Subquery) {
		return r.GetSourceColumnsFromSetOp(e.Unnest())
	}
	if !e.Is(exp.SetOperation) {
		optimizeErr("Unknown set operation: %s", e.SQL())
	}
	setOp := e
	var columns []string
	// BigQuery specific set operations modifiers, e.g INNER UNION ALL BY NAME
	if onColumnList := setOp.List("on"); len(onColumnList) > 0 {
		// The resulting columns are the columns in the ON clause:
		// {INNER | LEFT | FULL} UNION ALL BY NAME ON (col1, col2, ...)
		for _, col := range onColumnList {
			columns = append(columns, col.Name())
		}
	} else if side, kind := setOp.Text("side"), setOp.Text("kind"); side != "" || kind != "" {
		// Visit the children UNIONs (if any) in a post-order traversal
		left := r.GetSourceColumnsFromSetOp(setOp.This())
		right := r.GetSourceColumnsFromSetOp(setOp.Expression())
		switch {
		case side == "LEFT":
			columns = left
		case side == "FULL":
			columns = dedupStrings(append(append([]string(nil), left...), right...))
		case kind == "INNER":
			// dict.fromkeys(left).keys() & dict.fromkeys(right).keys() is a set (unordered);
			// left's order is used here
			rightSet := map[string]bool{}
			for _, c := range right {
				rightSet[c] = true
			}
			for _, c := range dedupStrings(left) {
				if rightSet[c] {
					columns = append(columns, c)
				}
			}
		}
	} else {
		columns = setOp.NamedSelects()
	}
	return columns
}

// GetSourceColumns mirrors Resolver.get_source_columns: the source columns for a given source.
func (r *Resolver) GetSourceColumns(name string, onlyVisible bool) []string {
	key := sourceColumnsKey{name, onlyVisible}
	if cached, ok := r.getSourceColumnsCache[key]; ok {
		return cached
	}
	source, ok := r.Scope.Sources.Get(name)
	if !ok {
		optimizeErr("Unknown table: %s", name)
	}
	// A pivoted CTE reference is stored as an exp.Table in the scope sources, but the
	// underlying CTE Scope still holds the column information we need to resolve pre-pivot
	// columns.
	if t, ok := source.(*exp.Expr); ok && t.IsKind(exp.Table) && t.DB() == "" && len(t.List("pivots")) > 0 && r.Scope.CteSources.Has(t.Name()) {
		source, _ = r.Scope.CteSources.Get(t.Name())
	}
	var columns []string
	switch src := source.(type) {
	case *exp.Expr:
		names, err := r.Schema.ColumnNames(src, onlyVisible, nil, nil)
		if err != nil {
			panic(err)
		}
		columns = names
	case *Scope:
		sourceExpr := src.Expression
		switch {
		case sourceExpr.IsAny(exp.Values, exp.Unnest, exp.Lateral):
			columns = append([]string(nil), sourceExpr.NamedSelects()...)
			// in bigquery, unnest structs are automatically scoped as tables, so you can directly
			// select a struct field in a query. This handles the case where the unnest is
			// statically defined.
			if r.Dialect.UnnestColumnOnly && sourceExpr.IsKind(exp.Unnest) {
				if sourceExpr.Type() == nil || sourceExpr.Type().IsType(exp.DTypeUnknown) {
					unnestExpr := seqGet(sourceExpr.Expressions(), 0)
					if unnestExpr.IsKind(exp.Column) && r.Scope.Parent != nil {
						colType := r.getUnnestColumnType(unnestExpr, r.Scope.Parent)
						if colType != nil && colType.IsType(exp.DTypeArray) {
							if elementTypes := colType.Expressions(); len(elementTypes) > 0 {
								sourceExpr.SetType(elementTypes[0].Copy())
							}
						} else if colType != nil {
							sourceExpr.SetType(colType.Copy())
						}
					}
				}
				columns = append(columns, r.structFieldNames(sourceExpr.Type())...)
			} else if sourceExpr.IsKind(exp.Lateral) && sourceExpr.This().IsKind(exp.Explode) {
				explodeCol := sourceExpr.This().This()
				// If the column is unqualified at this point, it couldn't be resolved when this
				// scope's children were qualified; disambiguating it here would require
				// enumerating this very source's columns, i.e recurse without bound
				if explodeCol.IsKind(exp.Column) && explodeCol.TableName() != "" && src.Parent != nil {
					colType := r.getUnnestColumnType(explodeCol, src.Parent)
					columns = append(columns, r.structFieldNames(colType)...)
				}
			} else if sourceExpr.IsKind(exp.Lateral) && sourceExpr.This().Is(exp.Query) {
				columns = sourceExpr.This().NamedSelects()
			}
		case sourceExpr.Is(exp.SetOperation):
			columns = r.GetSourceColumnsFromSetOp(sourceExpr)
		default:
			if !sourceExpr.Is(exp.Selectable) {
				panic(&errs.OptimizeError{Message: fmt.Sprintf("%s is not Selectable.", sourceExpr.SQL())})
			}
			sel := seqGet(sourceExpr.Selects(), 0)
			if sel.IsKind(exp.QueryTransform) {
				// https://spark.apache.org/docs/3.5.1/sql-ref-syntax-qry-select-transform.html
				if schema := sel.Get("schema"); schema != nil {
					for _, c := range schema.Expressions() {
						columns = append(columns, c.Name())
					}
				} else {
					columns = []string{"key", "value"}
				}
			} else {
				columns = sourceExpr.NamedSelects()
			}
		}
	}

	var columnAliases []string
	if selected, ok := r.Scope.SelectedSources().Get(name); ok && selected.Node != nil {
		columnAliases = selected.Node.AliasColumnNames()
	}
	if len(columnAliases) > 0 {
		// If the source's columns are aliased, their aliases shadow the corresponding column
		// names. This can be expensive if there are lots of columns, so only do this if column
		// aliases exist.
		n := max(len(columns), len(columnAliases))
		out := make([]string, n)
		for i := 0; i < n; i++ {
			if i < len(columnAliases) && columnAliases[i] != "" {
				out[i] = columnAliases[i]
			} else if i < len(columns) {
				out[i] = columns[i]
			}
		}
		columns = out
	}
	r.getSourceColumnsCache[key] = columns
	return columns
}

func (r *Resolver) getAllSourceColumns() *OrderedMap[[]string] {
	if r.sourceColumns == nil {
		out := NewOrderedMap[[]string]()
		for _, name := range r.Scope.SelectedSources().Keys() {
			out.Set(name, r.GetSourceColumns(name, false))
		}
		for _, name := range r.Scope.LateralSources.Keys() {
			out.Set(name, r.GetSourceColumns(name, false))
		}
		r.sourceColumns = out
	}
	return r.sourceColumns
}

func (r *Resolver) getTableNameFromSources(columnName string, sourceColumns *OrderedMap[[]string]) string {
	var unambiguous map[string]string
	if sourceColumns == nil || sourceColumns.Len() == 0 {
		// If not supplied, get all sources to calculate unambiguous columns
		if r.unambiguousColumns == nil {
			r.unambiguousColumns = r.getUnambiguousColumns(r.getAllSourceColumns())
		}
		unambiguous = r.unambiguousColumns
	} else {
		unambiguous = r.getUnambiguousColumns(sourceColumns)
	}
	return unambiguous[columnName]
}

func (r *Resolver) getColumnJoinContext(column *exp.Expr) *exp.Expr {
	e := r.Scope.Expression
	if len(e.List("joins")) == 0 || e.Bool("laterals") || e.Bool("pivots") {
		// Feature gap: We currently don't try to disambiguate columns if other sources (e.g
		// laterals, pivots) exist alongside joins
		return nil
	}
	joinAncestor := column.FindAncestor(exp.Join, exp.Select)
	if joinAncestor.IsKind(exp.Join) {
		joinName := joinAncestor.AliasOrName()
		if r.Scope.SelectedSources().Has(joinName) || r.Scope.SemiOrAntiJoinTables()[joinName] {
			// Ensure that the found ancestor is a join that contains an actual source, e.g in
			// Clickhouse `b` is an array expression in `a ARRAY JOIN b`
			return joinAncestor
		}
	}
	return nil
}

// getAvailableSourceColumns returns the source columns available where a column is
// referenced: for columns in JOIN conditions, only the tables joined up to that point.
func (r *Resolver) getAvailableSourceColumns(joinAncestor *exp.Expr) *OrderedMap[[]string] {
	e := r.Scope.Expression
	// Collect tables in order: FROM clause tables + joined tables up to current join
	fromName := e.Get("from_").AliasOrName()
	available := NewOrderedMap[[]string]()
	available.Set(fromName, r.GetSourceColumns(fromName, false))
	joins := e.List("joins")
	for _, join := range joins[:min(joinAncestor.Index()+1, len(joins))] {
		// An earlier SEMI/ANTI join's table is only visible within its own ON clause
		if join == joinAncestor || !join.IsSemiOrAntiJoin() {
			available.Set(join.AliasOrName(), r.GetSourceColumns(join.AliasOrName(), false))
		}
	}
	return available
}

// getUnambiguousColumns maps every column that appears in exactly one source to that source.
func (r *Resolver) getUnambiguousColumns(sourceColumns *OrderedMap[[]string]) map[string]string {
	if sourceColumns == nil || sourceColumns.Len() == 0 {
		return map[string]string{}
	}
	names := sourceColumns.Keys()
	firstTable := names[0]
	firstColumns, _ := sourceColumns.Get(firstTable)
	unambiguous := map[string]string{}
	for _, c := range firstColumns {
		unambiguous[c] = firstTable
	}
	if len(names) == 1 {
		return unambiguous
	}
	// For BigQuery UNNEST_COLUMN_ONLY, build a mapping of original UNNEST aliases from
	// alias.columns[0] to their source names. This is used to resolve shadowing where an UNNEST
	// alias shadows a column name from another table.
	unnestOriginalAliases := map[string]string{}
	if r.Dialect.UnnestColumnOnly {
		for _, sourceName := range r.Scope.Sources.Keys() {
			source, _ := r.Scope.Sources.Get(sourceName)
			var sourceExpr *exp.Expr
			switch s := source.(type) {
			case *Scope:
				sourceExpr = s.Expression
			case *exp.Expr:
				sourceExpr = s.Expression()
			}
			if sourceExpr.IsKind(exp.Unnest) {
				if aliasArg := sourceExpr.Get("alias"); aliasArg != nil && len(aliasArg.Columns()) > 0 {
					unnestOriginalAliases[aliasArg.Columns()[0].Name()] = sourceName
				}
			}
		}
	}
	allColumns := map[string]bool{}
	for c := range unambiguous {
		allColumns[c] = true
	}
	for _, table := range names[1:] {
		columns, _ := sourceColumns.Get(table)
		unique := map[string]bool{}
		for _, c := range columns {
			unique[c] = true
		}
		ambiguous := map[string]bool{}
		for c := range unique {
			if allColumns[c] {
				ambiguous[c] = true
			}
		}
		for _, c := range columns {
			allColumns[c] = true
		}
		for column := range ambiguous {
			if src, ok := unnestOriginalAliases[column]; ok {
				unambiguous[column] = src
				continue
			}
			delete(unambiguous, column)
		}
		for column := range unique {
			if !ambiguous[column] {
				unambiguous[column] = table
			}
		}
	}
	return unambiguous
}

func (r *Resolver) structFieldNames(colType *exp.Expr) []string {
	if colType != nil && colType.IsType(exp.DTypeArray) {
		colType = seqGet(colType.Expressions(), 0)
	}
	var out []string
	if colType != nil && colType.IsType(exp.DTypeStruct) {
		for _, k := range colType.Expressions() {
			out = append(out, k.Name())
		}
	}
	return out
}

// getUnnestColumnType returns the type of a column being unnested/exploded from its source
// scope or table.
func (r *Resolver) getUnnestColumnType(column *exp.Expr, scope *Scope) *exp.Expr {
	var tableName string
	// if column is qualified, use that table, otherwise disambiguate using the resolver
	if column.TableName() != "" {
		tableName = column.TableName()
	} else {
		// use the parent scope's resolver to disambiguate the column
		parentResolver := NewResolver(scope, r.Schema)
		tableIdentifier := parentResolver.GetTable(column)
		if tableIdentifier == nil {
			return nil
		}
		tableName = tableIdentifier.Name()
	}
	source, ok := scope.Sources.Get(tableName)
	if !ok || source == nil {
		return nil
	}
	return r.getColumnTypeFromScope(source, column)
}

// getColumnTypeFromScope returns a column's type from annotated projections or underlying
// table schemas (memoized per (source, column name)).
func (r *Resolver) getColumnTypeFromScope(source Source, column *exp.Expr) *exp.Expr {
	key := columnTypeKey{source, column.Name()}
	if cached, ok := r.columnTypeCache[key]; ok {
		return cached.t
	}
	var result *exp.Expr
	switch src := source.(type) {
	case *exp.Expr:
		// base table - get the column type from schema
		colType, err := r.Schema.GetColumnType(src, column, nil, nil)
		if err != nil {
			panic(err)
		}
		if colType != nil && !colType.IsType(exp.DTypeUnknown) {
			result = colType
		}
	case *Scope:
		if src.Expression.IsKind(exp.Select) {
			for _, projection := range src.Expression.Selects() {
				if projection.AliasOrName() == column.Name() {
					if t := projection.Type(); t != nil && !t.IsType(exp.DTypeUnknown) {
						result = t
					}
					break
				}
			}
		}
		if result == nil {
			for _, nested := range src.Sources.Values() {
				if nestedType := r.getColumnTypeFromScope(nested, column); nestedType != nil && !nestedType.IsType(exp.DTypeUnknown) {
					result = nestedType
					break
				}
			}
		}
	}
	r.columnTypeCache[key] = cachedType{result}
	return result
}
