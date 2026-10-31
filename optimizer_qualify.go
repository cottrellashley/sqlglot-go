package sqlglot

// Ports of sqlglot/optimizer/qualify.py, qualify_tables.py, isolate_table_selects.py,
// helpers.py and journal.py.

import (
	"strings"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// WindowHasAggregate is the meta key caching whether a named window contains an aggregate.
const WindowHasAggregate = "window_has_aggregate"

// IsImplicitGroupByAll reports whether a bare GROUP BY ALL infers its keys from the SELECT
// list, unlike ALL as a grouping-sets modifier (e.g. GROUP BY ALL CUBE (...) or GROUP BY ALL a, b).
func IsImplicitGroupByAll(sel *exp.Expr) bool {
	group := sel.Get("group")
	if group == nil || !group.Bool("all") {
		return false
	}
	return !(len(group.Expressions()) > 0 || group.Bool("cube") || group.Bool("rollup") || group.Bool("grouping_sets"))
}

// ProjectionHasAggregate mirrors helpers.projection_has_aggregate.
func ProjectionHasAggregate(projection *exp.Expr, windows []*exp.Expr) bool {
	windowedAggregates := map[*exp.Expr]bool{}
	remainingWindows := map[string]*exp.Expr{}
	for _, w := range windows {
		remainingWindows[w.Name()] = w
	}
	for node := range WalkInScope(projection, nil) {
		if node.IsKind(exp.Window) {
			target := node.This()
			// parens, FILTER and IGNORE NULLS wrap that function without changing which one it is
			for target != nil && !target.Is(exp.Func) {
				target = target.This()
			}
			if target.Is(exp.AggFunc) {
				windowedAggregates[target] = true
			}
			// WINDOW w AS (...)
			//        ^ this is the node's alias
			name := node.Alias()
			// The loop below handles named window inheritance, e.g.:
			// WINDOW w1 AS (PARTITION BY SUM(x)), w2 AS (w1 ORDER BY 1), w3 AS (w2)
			for name != "" {
				window, ok := remainingWindows[name]
				if !ok {
					break
				}
				delete(remainingWindows, name)
				hasAggregate, cached := window.MetaGet(WindowHasAggregate).(bool)
				if !cached {
					hasAggregate = FindInScope(window, exp.AggFunc) != nil
					window.Meta()[WindowHasAggregate] = hasAggregate
				}
				if hasAggregate {
					return true
				}
				name = window.Alias()
			}
		} else if node.Is(exp.AggFunc) && !windowedAggregates[node] {
			return true
		}
	}
	return false
}

// JournalEntry is one recorded argument mutation: (node, arg key, value before the mutation).
type JournalEntry struct {
	Node   *exp.Expr
	ArgKey string
	Value  any
}

// Journal records argument mutations so that they can be reverted.
type Journal []JournalEntry

// Record records the current value of node.args[argKey] so Revert can restore it. List values
// are copied shallowly.
func (j *Journal) Record(node *exp.Expr, argKey string) {
	value := node.Arg(argKey)
	if l, ok := value.([]*exp.Expr); ok {
		value = append([]*exp.Expr(nil), l...)
	}
	*j = append(*j, JournalEntry{node, argKey, value})
}

// Revert restores every argument recorded from start onwards, newest first, and drops those
// entries.
func (j *Journal) Revert(start int) {
	entries := *j
	for i := len(entries) - 1; i >= start; i-- {
		entries[i].Node.Set(entries[i].ArgKey, entries[i].Value)
	}
	*j = entries[:start]
}

// QualifyOptions are the keyword arguments of qualify.
type QualifyOptions struct {
	Dialect                   any
	DB                        any // string or *exp.Expr identifier
	Catalog                   any
	Schema                    any
	NoExpandAliasRefs         bool // expand_alias_refs=False
	NoExpandStars             bool // expand_stars=False
	IsolateTables             bool
	NoQualifyColumns          bool // qualify_columns=False
	AllowPartialQualification bool
	NoValidateQualifyColumns  bool // validate_qualify_columns=False
	NoQuoteIdentifiers        bool // quote_identifiers=False
	NoIdentify                bool // identify=False
	CanonicalizeTableAliases  bool
	OnQualify                 func(table *exp.Expr)
	SQL                       string
}

// Qualify mirrors optimizer.qualify.qualify: it rewrites the AST to have normalized and
// qualified tables and columns. This step is necessary for all further optimizations.
func Qualify(e *exp.Expr, o QualifyOptions) (out *exp.Expr, err error) {
	defer recoverOptimize(&err)
	schema, err := EnsureSchema(o.Schema, o.Dialect)
	if err != nil {
		return nil, err
	}
	d, err := GetDialect(o.Dialect)
	if err != nil {
		return nil, err
	}
	e = NormalizeIdentifiersOpts(e, d, true)
	e = QualifyTables(e, QualifyTablesOptions{
		DB: o.DB, Catalog: o.Catalog, Dialect: d, OnQualify: o.OnQualify,
		CanonicalizeTableAliases: o.CanonicalizeTableAliases,
	})
	if o.IsolateTables {
		e = IsolateTableSelects(e, schema, nil)
	}
	if !o.NoQualifyColumns {
		e = QualifyColumns(e, schema, QualifyColumnsOptions{
			NoExpandAliasRefs:         o.NoExpandAliasRefs,
			NoExpandStars:             o.NoExpandStars,
			AllowPartialQualification: o.AllowPartialQualification,
		})
	}
	if !o.NoQuoteIdentifiers {
		e = QuoteIdentifiers(e, d, !o.NoIdentify)
	}
	if !o.NoValidateQualifyColumns {
		ValidateQualifyColumns(e, o.SQL)
	}
	return e, nil
}

// MustQualify is Qualify that panics on error.
func MustQualify(e *exp.Expr, o QualifyOptions) *exp.Expr {
	out, err := Qualify(e, o)
	if err != nil {
		panic(err)
	}
	return out
}

// IsolateTableSelects mirrors optimizer.isolate_table_selects.
func IsolateTableSelects(e *exp.Expr, schema Schema, dialect any) *exp.Expr {
	if schema == nil {
		s, err := EnsureSchema(nil, dialect)
		if err != nil {
			panic(err)
		}
		schema = s
	}
	for _, scope := range TraverseScope(e) {
		selected := scope.SelectedSources()
		if selected.Len() == 1 {
			continue
		}
		for _, ss := range selected.Values() {
			source, ok := ss.Source.(*exp.Expr)
			if !ok || !source.IsKind(exp.Table) {
				continue
			}
			names, err := schema.ColumnNames(source, false, nil, nil)
			if err != nil {
				panic(err)
			}
			if len(names) == 0 || source.Parent().IsKind(exp.Subquery) || source.Parent().Parent().IsKind(exp.Table) {
				continue
			}
			if source.Alias() == "" {
				panic(&errs.OptimizeError{Message: "Tables require an alias. Run qualify_tables optimization."})
			}
			source.Replace(
				exp.NewSelect("*").
					From(exp.NewTableAliasOf(source, source.AliasOrName(), nil), exp.WithCopy(false)).
					Subquery(source.Alias(), false))
		}
	}
	return e
}

// QualifyTablesOptions are the keyword arguments of qualify_tables.
type QualifyTablesOptions struct {
	DB                       any // string or identifier
	Catalog                  any
	OnQualify                func(table *exp.Expr)
	Dialect                  any
	CanonicalizeTableAliases bool
}

// QualifyTables mirrors optimizer.qualify_tables.qualify_tables: it rewrites the AST to have
// fully qualified tables. Join constructs such as (t1 JOIN t2) AS t are expanded into
// (SELECT * FROM t1 AS t1, t2 AS t2) AS t.
func QualifyTables(e *exp.Expr, o QualifyTablesOptions) *exp.Expr {
	d := MustDialect(o.Dialect)
	nextAliasName := helper.NameSequence("_")

	parseIdent := func(v any) *exp.Expr {
		if v == nil {
			return nil
		}
		if s, ok := v.(string); ok && s == "" {
			return nil
		}
		if x, ok := v.(*exp.Expr); ok && x == nil {
			return nil
		}
		id := exp.ParseIdentifier(v, d)
		id.Meta()["is_table"] = true
		return NormalizeIdentifiers(id, d)
	}
	db := parseIdent(o.DB)
	catalog := parseIdent(o.Catalog)

	qualify := func(table *exp.Expr) {
		if table.This().IsKind(exp.Identifier) {
			if db != nil && table.Get("db") == nil {
				table.Set("db", db.Copy())
			}
			if catalog != nil && table.Get("catalog") == nil && table.Get("db") != nil {
				table.Set("catalog", catalog.Copy())
			}
		}
	}

	if (db != nil || catalog != nil) && !e.Is(exp.Query) {
		with := e.Get("with_")
		cteNames := map[string]bool{}
		if with != nil {
			for _, cte := range with.Expressions() {
				cteNames[cte.AliasOrName()] = true
			}
		}
		for node := range e.Walk(true, func(n *exp.Expr) bool { return n.Is(exp.Query) }) {
			if node.IsKind(exp.Table) && !cteNames[node.Name()] {
				qualify(node)
			}
		}
	}

	setAlias := func(expression *exp.Expr, canonicalAliases map[string]string, targetAlias string, scope *Scope,
		normalize bool, columns []any) {
		alias := expression.Get("alias")
		if alias == nil {
			alias = exp.New(exp.TableAlias)
		}
		var newAliasName string
		if o.CanonicalizeTableAliases {
			newAliasName = nextAliasName()
			key := alias.Name()
			if key == "" {
				key = targetAlias
			}
			canonicalAliases[key] = newAliasName
		} else if alias.Name() == "" {
			newAliasName = targetAlias
			if newAliasName == "" {
				newAliasName = nextAliasName()
			}
			if normalize && targetAlias != "" {
				newAliasName = NormalizeIdentifiers(newAliasName, d).Name()
			}
		} else {
			return
		}
		alias.Set("this", exp.ToIdentifier(newAliasName))
		if len(columns) > 0 {
			var cols []*exp.Expr
			for _, c := range columns {
				switch x := c.(type) {
				case string:
					cols = append(cols, exp.ToIdentifier(x))
				case *exp.Expr:
					cols = append(cols, x.Copy())
				}
			}
			alias.Set("columns", cols)
		}
		expression.Set("alias", alias)
		if scope != nil {
			scope.RenameSource("", newAliasName)
		}
	}

	// Expanding join constructs exposes new sources, including nested join constructs that only
	// become derived tables afterwards, so the scopes are rebuilt until nothing else is expanded
	// (every construct in a pass expands before the rebuild)
	scopes := TraverseScope(e)
	for {
		expanded := false
		for _, scope := range scopes {
			for _, dt := range scope.DerivedTables() {
				if expandJoinConstruct(dt) {
					expanded = true
				}
			}
		}
		if !expanded {
			break
		}
		scopes = TraverseScope(e)
	}

	for _, scope := range scopes {
		parent := scope.Parent
		localColumns := scope.LocalColumns()
		canonicalAliases := map[string]string{}
		queries := append([]*exp.Expr(nil), scope.Subqueries()...)
		// Subquery wrappers around a DML / DDL query fragment, e.g., a CREATE FUNCTION body or
		// an UPDATE's SET subquery, don't belong to any scope, so they aren't collected above
		if scope.IsRoot() && scope.Expression.IsKind(exp.Subquery) {
			queries = append(queries, scope.Expression.Unnest())
		} else if scope.IsSubquery() {
			queries = append(queries, scope.Expression)
		}
		for _, query := range queries {
			subquery := query.Parent()
			if subquery.IsKind(exp.Subquery) {
				unwrapped := subquery.Unwrap()
				if unwrapped.Parent().IsAny(exp.From, exp.Join) {
					// We can reach this from a wrapped derived table, which must keep its alias
					continue
				}
				if unwrapped.Parent().IsKind(exp.Create) && unwrapped != subquery {
					// Function bodies may require wrapping parentheses, e.g. in BigQuery
					// `... AS ((SELECT 1))` the outer parens delimit the body itself
					unwrapped.Set("this", subquery)
				} else {
					unwrapped.Replace(subquery)
				}
			}
		}

		for _, derivedTable := range scope.DerivedTables() {
			setAlias(derivedTable, canonicalAliases, "", scope, false, nil)
			if pivots := derivedTable.List("pivots"); len(pivots) > 0 {
				setAlias(pivots[len(pivots)-1], canonicalAliases, "", nil, false, nil)
			}
		}

		tableAliases := NewOrderedMap[*exp.Expr]()
		for _, name := range scope.Sources.Keys() {
			source, _ := scope.Sources.Get(name)
			// A source can appear in many scopes, e.g. as a lateral source of a UDTF scope or as
			// a CTE propagated to inner scopes. Deferring to the parent scope when it contains the
			// same source ensures each source is processed once, in the outermost scope
			if parent != nil {
				if ps, ok := parent.Sources.Get(name); ok && sameSource(ps, source) {
					continue
				}
			}
			switch src := source.(type) {
			case *exp.Expr:
				if !src.IsKind(exp.Table) {
					continue
				}
				// When the name is empty, it means that we have a non-table source, e.g. a pivoted cte
				isRealTableSource := name != ""
				var pivot *exp.Expr
				if pivots := src.List("pivots"); len(pivots) > 0 {
					pivot = pivots[len(pivots)-1]
					name = src.Name()
				}
				tableThis := src.This()
				tableAlias := src.Get("alias")
				var functionColumns []any
				if tableThis.Is(exp.Func) {
					defaults, hasDefaults := d.DefaultFunctionsColumnNames[tableThis.Kind()]
					if tableAlias == nil {
						for _, c := range defaults {
							functionColumns = append(functionColumns, c)
						}
					} else if columns := tableAlias.Columns(); len(columns) > 0 {
						for _, c := range columns {
							functionColumns = append(functionColumns, c)
						}
					} else if hasDefaults {
						functionColumns = []any{src.AliasOrName()}
						src.Set("alias", nil)
						name = ""
					}
				}
				target := name
				if target == "" {
					target = src.Name()
				}
				setAlias(src, canonicalAliases, target, nil, true, functionColumns)

				var fqnParts []string
				for _, p := range src.Parts() {
					fqnParts = append(fqnParts, p.Name())
				}
				sourceFQN := strings.Join(fqnParts, ".")
				hadExplicitAlias := tableAlias != nil && tableAlias.Name() != ""
				if !hadExplicitAlias || !tableAliases.Has(sourceFQN) {
					tableAliases.Set(sourceFQN, src.Get("alias").This().Copy())
				}
				if pivot != nil {
					targetAlias := ""
					if pivot.Bool("unpivot") {
						targetAlias = src.Alias()
					}
					setAlias(pivot, canonicalAliases, targetAlias, nil, true, nil)
					// This case corresponds to a pivoted CTE, we don't want to qualify that
					if s, ok := scope.Sources.Get(src.AliasOrName()); ok {
						if _, isScope := s.(*Scope); isScope {
							continue
						}
					}
				}
				if isRealTableSource {
					qualify(src)
					if o.OnQualify != nil {
						o.OnQualify(src)
					}
				}
			case *Scope:
				if !src.IsUDTF() {
					continue
				}
				udtf := src.Expression
				setAlias(udtf, canonicalAliases, "", nil, false, nil)
				tableAlias := udtf.Get("alias")
				if udtf.IsKind(exp.Values) && len(tableAlias.Columns()) == 0 {
					var columnAliases []*exp.Expr
					for _, i := range d.GenerateValuesAliases(udtf) {
						columnAliases = append(columnAliases, NormalizeIdentifiers(i, d))
					}
					tableAlias.Set("columns", columnAliases)
				} else if (udtf.IsKind(exp.TableFromRows) || (udtf.IsKind(exp.Lateral) && udtf.This().Is(exp.UDTF))) &&
					len(tableAlias.Columns()) == 0 {
					if defaults, ok := d.DefaultFunctionsColumnNames[udtf.This().Kind()]; ok && len(defaults) > 0 {
						var cols []*exp.Expr
						for _, c := range defaults {
							cols = append(cols, NormalizeIdentifiers(exp.ToIdentifier(c), d))
						}
						tableAlias.Set("columns", cols)
					}
				}
			}
		}

		for _, table := range scope.Tables() {
			if table.Alias() == "" && table.Parent().IsAny(exp.From, exp.Join) {
				setAlias(table, canonicalAliases, table.Name(), nil, false, nil)
			}
		}

		for _, column := range localColumns {
			columnTable := column.TableName()
			if column.DB() != "" {
				parts := column.Parts()
				var names []string
				for _, p := range parts[:len(parts)-1] {
					names = append(names, p.Name())
				}
				if tableAlias, ok := tableAliases.Get(strings.Join(names, ".")); ok && tableAlias != nil {
					for _, p := range exp.ColumnParts[1:] {
						column.Set(p, nil)
					}
					column.Set("table", tableAlias.Copy())
				}
			} else if len(canonicalAliases) > 0 && columnTable != "" {
				if canonicalTable, ok := canonicalAliases[columnTable]; ok && canonicalTable != "" && canonicalTable != columnTable {
					// Amend existing aliases, e.g. t.c -> _0.c if t is aliased to _0
					column.Set("table", exp.ToIdentifier(canonicalTable))
				}
			}
		}
	}
	return e
}

func sameSource(a, b Source) bool {
	switch x := a.(type) {
	case *exp.Expr:
		y, ok := b.(*exp.Expr)
		return ok && x == y
	case *Scope:
		y, ok := b.(*Scope)
		return ok && x == y
	}
	return false
}

// expandJoinConstruct expands e.g. ((a JOIN b) JOIN c) AS j into a SELECT *, collecting the
// joins of every unaliased wrapper.
func expandJoinConstruct(derivedTable *exp.Expr) bool {
	source := derivedTable.This()
	var joins []*exp.Expr
	for source.IsAny(exp.Subquery, exp.Table) {
		joins = append(append([]*exp.Expr(nil), source.List("joins")...), joins...)
		if source.IsKind(exp.Table) || source.Alias() != "" || !source.This().IsAny(exp.Subquery, exp.Table) {
			break
		}
		source = source.This()
	}
	if source.IsKind(exp.Table) || len(joins) > 0 {
		source.Set("joins", nil)
		derivedTable.This().Replace(exp.NewSelect("*").From(source.Copy(), exp.WithCopy(false)))
		if len(joins) > 0 {
			derivedTable.This().Set("joins", joins)
		} else {
			derivedTable.This().Set("joins", nil)
		}
		return true
	}
	return false
}
