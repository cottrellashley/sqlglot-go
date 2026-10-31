package sqlglot

// Port of sqlglot/optimizer/scope.py.

import (
	"fmt"
	"iter"
	"slices"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// ScopeType mirrors scope.ScopeType.
type ScopeType int

const (
	ScopeRoot ScopeType = iota + 1
	ScopeSubquery
	ScopeDerivedTable
	ScopeCTE
	ScopeSetOperation
	ScopeUDTF
)

// Source is a scope source: either a *exp.Expr (a Table) or a *Scope.
type Source = any

// SelectedSource is a (node, source) pair of Scope.SelectedSources.
type SelectedSource struct {
	Node   *exp.Expr
	Source Source
}

// Reference is a (name, node) pair of Scope.References.
type Reference struct {
	Name string
	Node *exp.Expr
}

// Scope mirrors scope.Scope: the selection scope of a query (its sources, columns, child
// scopes, ...).
type Scope struct {
	Expression         *exp.Expr
	Sources            *OrderedMap[Source]
	LateralSources     *OrderedMap[Source]
	CteSources         *OrderedMap[Source]
	OuterColumns       []string
	Parent             *Scope
	ScopeType          ScopeType
	SubqueryScopes     []*Scope
	DerivedTableScopes []*Scope
	TableScopes        []*Scope
	CteScopes          []*Scope
	SetOperationScopes []*Scope
	UdtfScopes         []*Scope
	CanBeCorrelated    bool

	collected               bool
	scansAllSubscopeColumns bool
	rawColumns              []*exp.Expr
	tableColumns            []*exp.Expr
	stars                   []*exp.Expr
	derivedTables           []*exp.Expr
	udtfs                   []*exp.Expr
	tables                  []*exp.Expr
	orderedSources          []*exp.Expr
	ctes                    []*exp.Expr
	subqueries              []*exp.Expr
	joinHints               []*exp.Expr
	comprehensions          []*exp.Expr
	semiAntiJoinTables      map[string]bool
	columnIndex             map[*exp.Expr]bool
	selectedSources         *OrderedMap[SelectedSource]
	columns                 []*exp.Expr
	columnsSet              bool
	externalColumns         []*exp.Expr
	externalColumnsSet      bool
	localColumns            []*exp.Expr
	localColumnsSet         bool
	pivots                  []*exp.Expr
	pivotsSet               bool
	references              []Reference
	referencesSet           bool
}

// ScopeOptions are the optional constructor arguments of Scope.
type ScopeOptions struct {
	Sources         *OrderedMap[Source]
	OuterColumns    []string
	Parent          *Scope
	ScopeType       ScopeType // 0 means ROOT
	LateralSources  *OrderedMap[Source]
	CteSources      *OrderedMap[Source]
	CanBeCorrelated bool
}

// NewScope mirrors Scope(expression, ...).
func NewScope(expression *exp.Expr, o ScopeOptions) *Scope {
	s := &Scope{Expression: expression}
	// `x or {}`: empty mappings are replaced by fresh ones, non-empty ones are shared
	orNew := func(m *OrderedMap[Source]) *OrderedMap[Source] {
		if m.Len() == 0 {
			return NewOrderedMap[Source]()
		}
		return m
	}
	s.Sources = orNew(o.Sources)
	s.LateralSources = orNew(o.LateralSources)
	s.CteSources = orNew(o.CteSources)
	s.Sources.Update(s.LateralSources)
	s.Sources.Update(s.CteSources)
	s.OuterColumns = o.OuterColumns
	s.Parent = o.Parent
	s.ScopeType = o.ScopeType
	if s.ScopeType == 0 {
		s.ScopeType = ScopeRoot
	}
	s.CanBeCorrelated = o.CanBeCorrelated
	s.ClearCache()
	return s
}

// ClearColumnCache mirrors Scope.clear_column_cache.
func (s *Scope) ClearColumnCache() {
	s.columns, s.columnsSet = nil, false
	s.externalColumns, s.externalColumnsSet = nil, false
	s.localColumns, s.localColumnsSet = nil, false
}

// ClearCache mirrors Scope.clear_cache.
func (s *Scope) ClearCache() {
	s.collected = false
	s.scansAllSubscopeColumns = false
	s.rawColumns = nil
	s.tableColumns = nil
	s.stars = nil
	s.derivedTables = nil
	s.udtfs = nil
	s.tables = nil
	s.orderedSources = nil
	s.ctes = nil
	s.subqueries = nil
	s.joinHints = nil
	s.comprehensions = nil
	s.semiAntiJoinTables = map[string]bool{}
	s.columnIndex = map[*exp.Expr]bool{}
	s.selectedSources = nil
	s.ClearColumnCache()
	s.pivots, s.pivotsSet = nil, false
	s.references, s.referencesSet = nil, false
}

// BranchOptions are the optional arguments of Scope.branch.
type BranchOptions struct {
	Sources        *OrderedMap[Source]
	CteSources     *OrderedMap[Source]
	LateralSources *OrderedMap[Source]
	OuterColumns   []string
}

// Branch mirrors Scope.branch: it creates a child scope.
func (s *Scope) Branch(expression *exp.Expr, scopeType ScopeType, o BranchOptions) *Scope {
	var sources, lateral *OrderedMap[Source]
	if o.Sources.Len() > 0 {
		sources = o.Sources.Copy()
	}
	if o.LateralSources.Len() > 0 {
		lateral = o.LateralSources.Copy()
	}
	cte := s.CteSources.Copy()
	cte.Update(o.CteSources)
	return NewScope(expression.Unnest(), ScopeOptions{
		Sources:         sources,
		Parent:          s,
		ScopeType:       scopeType,
		CteSources:      cte,
		LateralSources:  lateral,
		CanBeCorrelated: s.CanBeCorrelated || scopeType == ScopeSubquery || scopeType == ScopeUDTF,
		OuterColumns:    o.OuterColumns,
	})
}

// isCollectible mirrors isinstance(node, COLLECTIBLE_TYPES).
func isCollectible(node *exp.Expr) bool {
	return node.IsAny(exp.Column, exp.Dot, exp.Table, exp.Query, exp.UDTF, exp.CTE, exp.Star, exp.TableColumn,
		exp.JoinHint, exp.Comprehension)
}

func (s *Scope) collect() {
	s.tables = nil
	s.orderedSources = nil
	s.ctes = nil
	s.subqueries = nil
	s.derivedTables = nil
	s.udtfs = nil
	s.rawColumns = nil
	s.tableColumns = nil
	s.stars = nil
	s.joinHints = nil
	s.comprehensions = nil
	s.semiAntiJoinTables = map[string]bool{}
	s.columnIndex = map[*exp.Expr]bool{}

	// The inner query of a Subquery-rooted scope is scoped as a derived table by
	// `_traverse_tables`, so it must not also be collected as a subquery
	var innerQuery *exp.Expr
	if s.Expression.IsKind(exp.Subquery) {
		innerQuery = s.Expression.Unnest()
	}

	for node := range s.Walk(nil) {
		if node == s.Expression || !isCollectible(node) {
			continue
		}
		switch {
		case node.Is(exp.Dot) && node.IsStar():
			s.stars = append(s.stars, node)
		case node.IsKind(exp.Column):
			s.columnIndex[node] = true
			if node.This().Is(exp.Star) {
				s.stars = append(s.stars, node)
			} else {
				s.rawColumns = append(s.rawColumns, node)
			}
		case node.Is(exp.Table) && !node.Parent().Is(exp.JoinHint):
			if isSemiOrAntiJoinRHS(node) {
				s.semiAntiJoinTables[node.AliasOrName()] = true
			}
			s.tables = append(s.tables, node)
			s.orderedSources = append(s.orderedSources, node)
		case node.Is(exp.JoinHint):
			s.joinHints = append(s.joinHints, node)
		case node.IsKind(exp.Lateral) || (node.Is(exp.UDTF) && node.Parent().IsAny(exp.From, exp.Join)):
			if isSemiOrAntiJoinRHS(node) {
				s.semiAntiJoinTables[node.AliasOrName()] = true
			}
			s.udtfs = append(s.udtfs, node)
			s.orderedSources = append(s.orderedSources, node)
		case node.Is(exp.CTE):
			s.ctes = append(s.ctes, node)
		case isDerivedTable(node) && isFromOrJoin(node):
			if isSemiOrAntiJoinRHS(node) {
				s.semiAntiJoinTables[node.AliasOrName()] = true
			}
			s.derivedTables = append(s.derivedTables, node)
			s.orderedSources = append(s.orderedSources, node)
		case node.IsAny(exp.Select, exp.SetOperation) && !isFromOrJoin(node) && node != innerQuery:
			s.subqueries = append(s.subqueries, node)
		case node.Is(exp.TableColumn):
			s.tableColumns = append(s.tableColumns, node)
		case node.Is(exp.Comprehension):
			s.comprehensions = append(s.comprehensions, node)
		case node.Is(exp.Star) && (exp.Truthy(node.Arg("except_")) || !node.Parent().Is(exp.Count)):
			s.scansAllSubscopeColumns = true
		}
	}
	s.collected = true
}

func (s *Scope) ensureCollected() {
	if !s.collected {
		s.collect()
	}
}

// Walk mirrors Scope.walk.
func (s *Scope) Walk(prune func(*exp.Expr) bool) iter.Seq[*exp.Expr] {
	return WalkInScope(s.Expression, prune)
}

// Find mirrors Scope.find.
func (s *Scope) Find(kinds ...exp.Kind) *exp.Expr { return FindInScope(s.Expression, kinds...) }

// FindAll mirrors Scope.find_all.
func (s *Scope) FindAll(kinds ...exp.Kind) iter.Seq[*exp.Expr] {
	return FindAllInScope(s.Expression, kinds...)
}

// Replace mirrors Scope.replace.
func (s *Scope) Replace(old, new *exp.Expr) {
	old.Replace(new)
	s.ClearCache()
}

// Tables mirrors Scope.tables.
func (s *Scope) Tables() []*exp.Expr { s.ensureCollected(); return s.tables }

// Comprehensions mirrors Scope.comprehensions.
func (s *Scope) Comprehensions() []*exp.Expr { s.ensureCollected(); return s.comprehensions }

// Ctes mirrors Scope.ctes.
func (s *Scope) Ctes() []*exp.Expr { s.ensureCollected(); return s.ctes }

// DerivedTables mirrors Scope.derived_tables.
func (s *Scope) DerivedTables() []*exp.Expr { s.ensureCollected(); return s.derivedTables }

// Udtfs mirrors Scope.udtfs.
func (s *Scope) Udtfs() []*exp.Expr { s.ensureCollected(); return s.udtfs }

// Subqueries mirrors Scope.subqueries.
func (s *Scope) Subqueries() []*exp.Expr { s.ensureCollected(); return s.subqueries }

// ScansAllSubscopeColumns mirrors Scope.scans_all_subscope_columns.
func (s *Scope) ScansAllSubscopeColumns() bool { s.ensureCollected(); return s.scansAllSubscopeColumns }

// Stars mirrors Scope.stars.
func (s *Scope) Stars() []*exp.Expr { s.ensureCollected(); return s.stars }

// ColumnIndex mirrors Scope.column_index (column node identities).
func (s *Scope) ColumnIndex() map[*exp.Expr]bool { s.ensureCollected(); return s.columnIndex }

// Columns mirrors Scope.columns: all columns in this scope, including correlated columns of
// child subqueries.
func (s *Scope) Columns() []*exp.Expr {
	if !s.columnsSet {
		s.ensureCollected()
		columns := s.rawColumns
		var externalColumns []*exp.Expr
		for _, scope := range s.SubqueryScopes {
			externalColumns = append(externalColumns, scope.ExternalColumns()...)
		}
		for _, scope := range s.UdtfScopes {
			externalColumns = append(externalColumns, scope.ExternalColumns()...)
		}
		for _, scope := range s.DerivedTableScopes {
			if scope.CanBeCorrelated {
				externalColumns = append(externalColumns, scope.ExternalColumns()...)
			}
		}
		namedSelects := map[string]bool{}
		if s.Expression.Is(exp.Query) {
			for _, n := range s.Expression.NamedSelects() {
				namedSelects[n] = true
			}
		}
		s.columns = []*exp.Expr{}
		for _, column := range concatExprs(columns, externalColumns) {
			ancestor := column.FindAncestor(exp.Select, exp.Qualify, exp.Order, exp.Cluster, exp.Having, exp.Hint,
				exp.Table, exp.Star, exp.Distinct)
			if ancestor == nil ||
				column.Text("table") != "" ||
				ancestor.IsKind(exp.Select) ||
				(ancestor.Is(exp.Table) && !ancestor.This().Is(exp.Func)) ||
				(ancestor.IsAny(exp.Order, exp.Cluster, exp.Distinct) &&
					(ancestor.Parent().IsAny(exp.Window, exp.WithinGroup) || !ancestor.Parent().IsKind(exp.Select) ||
						!namedSelects[column.Name()])) ||
				(ancestor.Is(exp.Star) && column.ArgKey() != "except_") {
				s.columns = append(s.columns, column)
			}
		}
		s.columnsSet = true
	}
	return s.columns
}

// TableColumns mirrors Scope.table_columns.
func (s *Scope) TableColumns() []*exp.Expr { s.ensureCollected(); return s.tableColumns }

// SelectedSources mirrors Scope.selected_sources: a mapping of source names to (node, source).
func (s *Scope) SelectedSources() *OrderedMap[SelectedSource] {
	if s.selectedSources == nil {
		result := NewOrderedMap[SelectedSource]()
		for _, ref := range s.References() {
			if s.semiAntiJoinTables[ref.Name] {
				// The RHS table of SEMI/ANTI joins shouldn't be collected as a selected source
				continue
			}
			if result.Has(ref.Name) {
				panic(&errs.OptimizeError{Message: "Alias already used: " + ref.Name})
			}
			if src, ok := s.Sources.Get(ref.Name); ok {
				result.Set(ref.Name, SelectedSource{Node: ref.Node, Source: src})
			}
		}
		s.selectedSources = result
	}
	return s.selectedSources
}

// References mirrors Scope.references.
func (s *Scope) References() []Reference {
	if !s.referencesSet {
		s.ensureCollected()
		s.references = []Reference{}
		// Sources are kept in walk order, which star expansion relies on
		for _, e := range s.orderedSources {
			if e.Is(exp.Table) {
				s.references = append(s.references, Reference{e.AliasOrName(), e})
			} else {
				node := e
				if !exp.Truthy(e.Arg("pivots")) {
					node = e.Unnest()
				}
				s.references = append(s.references, Reference{getSourceAlias(e), node.AssertIs(exp.Selectable)})
			}
		}
		s.referencesSet = true
	}
	return s.references
}

// ExternalColumns mirrors Scope.external_columns: columns that are not in this scope.
func (s *Scope) ExternalColumns() []*exp.Expr {
	if !s.externalColumnsSet {
		if s.Expression.Is(exp.SetOperation) {
			left, right := s.SetOperationScopes[0], s.SetOperationScopes[1]
			s.externalColumns = concatExprs(left.ExternalColumns(), right.ExternalColumns())
		} else {
			local := map[string]bool{}
			for _, ref := range s.References() {
				local[ref.Name] = true
			}
			s.externalColumns = []*exp.Expr{}
			semi := s.SemiOrAntiJoinTables()
			for _, c := range s.Columns() {
				t := c.Text("table")
				if !local[t] && !semi[t] {
					s.externalColumns = append(s.externalColumns, c)
				}
			}
		}
		s.externalColumnsSet = true
	}
	return s.externalColumns
}

// LocalColumns mirrors Scope.local_columns.
func (s *Scope) LocalColumns() []*exp.Expr {
	if !s.localColumnsSet {
		external := map[*exp.Expr]bool{}
		for _, c := range s.ExternalColumns() {
			external[c] = true
		}
		s.localColumns = []*exp.Expr{}
		for _, c := range s.Columns() {
			if !external[c] {
				s.localColumns = append(s.localColumns, c)
			}
		}
		s.localColumnsSet = true
	}
	return s.localColumns
}

// UnqualifiedColumns mirrors Scope.unqualified_columns.
func (s *Scope) UnqualifiedColumns() []*exp.Expr {
	var out []*exp.Expr
	for _, c := range s.Columns() {
		if c.Text("table") == "" {
			out = append(out, c)
		}
	}
	return out
}

// JoinHints mirrors Scope.join_hints.
func (s *Scope) JoinHints() []*exp.Expr { s.ensureCollected(); return s.joinHints }

// Pivots mirrors Scope.pivots.
func (s *Scope) Pivots() []*exp.Expr {
	if !s.pivotsSet {
		s.pivots = []*exp.Expr{}
		for _, ref := range s.References() {
			s.pivots = append(s.pivots, ref.Node.List("pivots")...)
		}
		s.pivotsSet = true
	}
	return s.pivots
}

// SemiOrAntiJoinTables mirrors Scope.semi_or_anti_join_tables.
func (s *Scope) SemiOrAntiJoinTables() map[string]bool {
	s.ensureCollected()
	return s.semiAntiJoinTables
}

// SourceColumns mirrors Scope.source_columns.
func (s *Scope) SourceColumns(sourceName string) []*exp.Expr {
	var out []*exp.Expr
	for _, c := range s.Columns() {
		if c.Text("table") == sourceName {
			out = append(out, c)
		}
	}
	return out
}

func (s *Scope) IsSubquery() bool     { return s.ScopeType == ScopeSubquery }
func (s *Scope) IsDerivedTable() bool { return s.ScopeType == ScopeDerivedTable }
func (s *Scope) IsSetOperation() bool { return s.ScopeType == ScopeSetOperation }
func (s *Scope) IsCTE() bool          { return s.ScopeType == ScopeCTE }
func (s *Scope) IsRoot() bool         { return s.ScopeType == ScopeRoot }
func (s *Scope) IsUDTF() bool         { return s.ScopeType == ScopeUDTF }

// IsCorrelatedSubquery mirrors Scope.is_correlated_subquery.
func (s *Scope) IsCorrelatedSubquery() bool {
	return s.CanBeCorrelated && len(s.ExternalColumns()) > 0
}

// RenameSource mirrors Scope.rename_source.
func (s *Scope) RenameSource(oldName, newName string) {
	if v, ok := s.Sources.Delete(oldName); ok {
		s.Sources.Set(newName, v)
	}
}

// AddSource mirrors Scope.add_source.
func (s *Scope) AddSource(name string, source Source) {
	s.Sources.Set(name, source)
	s.ClearCache()
}

// RemoveSource mirrors Scope.remove_source.
func (s *Scope) RemoveSource(name string) {
	s.Sources.Delete(name)
	s.ClearCache()
}

func (s *Scope) String() string { return "Scope<" + s.Expression.SQL() + ">" }

// Traverse mirrors Scope.traverse: this scope and all child scopes, children first.
func (s *Scope) Traverse() []*Scope {
	stack := []*Scope{s}
	var result []*Scope
	for len(stack) > 0 {
		scope := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		result = append(result, scope)
		stack = append(stack, scope.CteScopes...)
		stack = append(stack, scope.SetOperationScopes...)
		stack = append(stack, scope.TableScopes...)
		stack = append(stack, scope.SubqueryScopes...)
	}
	slices.Reverse(result)
	return result
}

// RefCount mirrors Scope.ref_count: how many times each source (by identity) is referenced.
func (s *Scope) RefCount() map[Source]int {
	counts := map[Source]int{}
	for _, scope := range s.Traverse() {
		for _, ss := range scope.SelectedSources().Values() {
			counts[ss.Source]++
		}
		for name := range scope.semiAntiJoinTables {
			// semi/anti join sources are not actually selected but we still need to
			// increment their ref count to avoid them being optimized away
			if src, ok := scope.Sources.Get(name); ok {
				counts[src]++
			}
		}
	}
	return counts
}

// TraverseScope mirrors scope.traverse_scope: all scopes of a query, children first.
func TraverseScope(expression *exp.Expr) []*Scope {
	if expression.IsAny(exp.Query, exp.DDL, exp.DML) {
		return traverseScope(NewScope(expression, ScopeOptions{}))
	}
	return nil
}

// BuildScope mirrors scope.build_scope: the root scope of a query (nil if not traversable).
func BuildScope(expression *exp.Expr) *Scope {
	scopes := TraverseScope(expression)
	if len(scopes) == 0 {
		return nil
	}
	return scopes[len(scopes)-1]
}

func traverseScope(scope *Scope) []*Scope {
	expression := scope.Expression
	var out []*Scope
	switch {
	case expression.Is(exp.Select):
		out = append(out, traverseSelect(scope)...)
	case expression.Is(exp.SetOperation):
		out = append(out, traverseCtes(scope)...)
		out = append(out, traverseSetOperation(scope)...)
		return out
	case expression.Is(exp.Subquery):
		if scope.IsRoot() {
			out = append(out, traverseSelect(scope)...)
		} else {
			out = append(out, traverseSubqueries(scope)...)
		}
	case expression.Is(exp.Table):
		out = append(out, traverseTables(scope)...)
	case expression.Is(exp.UDTF):
		out = append(out, traverseUdtfs(scope)...)
	case expression.Is(exp.DDL):
		if ddlExpression := expression.Expression(); ddlExpression.Is(exp.Query) {
			out = append(out, traverseCtes(scope)...)
			out = append(out, traverseScope(NewScope(ddlExpression, ScopeOptions{CteSources: scope.CteSources}))...)
		}
		return out
	case expression.Is(exp.DML):
		out = append(out, traverseCtes(scope)...)
		// Bare tables in relation position (e.g. UPDATE ... FROM t, DELETE / MERGE ... USING t)
		// aren't part of any query, so they're scoped as standalone tables
		var relations []*exp.Expr
		if from := expression.Get("from_"); from.IsKind(exp.From) {
			relations = append(relations, from.This())
		}
		switch using := expression.Arg("using").(type) {
		case []*exp.Expr:
			relations = append(relations, using...)
		case *exp.Expr:
			relations = append(relations, using)
		}
		for _, relation := range relations {
			if relation.Is(exp.Table) {
				out = append(out, traverseScope(NewScope(relation, ScopeOptions{CteSources: scope.CteSources}))...)
			}
		}
		for query := range FindAllInScope(expression, exp.Query) {
			// This check ensures we don't yield the CTE/nested queries twice
			if query.Parent().IsAny(exp.CTE, exp.Subquery) {
				continue
			}
			if isFromOrJoin(query) {
				parent := query.Parent()
				if parent.IsKind(exp.Join) && parent.Parent().IsAny(exp.Subquery, exp.Table) {
					// Scoped by the FROM-position relation (wrapper or table) it's joined to
					continue
				}
				// A query in FROM/JOIN position acts like a derived table, so its scope stays
				// rooted at the Subquery wrapper
				out = append(out, traverseScope(NewScope(query, ScopeOptions{CteSources: scope.CteSources}))...)
			} else {
				// Queries in value position (SET, WHERE, USING, ...) are scoped as subqueries
				out = append(out, traverseScope(scope.Branch(query, ScopeSubquery, BranchOptions{}))...)
			}
		}
		return out
	default:
		Logger.Warn(fmt.Sprintf("Cannot traverse scope %s with type '%s'", expression.SQL(),
			expression.Kind().ClassRepr()))
		return out
	}
	return append(out, scope)
}

func traverseSelect(scope *Scope) []*Scope {
	out := traverseCtes(scope)
	out = append(out, traverseTables(scope)...)
	return append(out, traverseSubqueries(scope)...)
}

func traverseSetOperation(scope *Scope) []*Scope {
	var out []*Scope
	var prevScope *Scope
	setOpScopeStack := []*Scope{scope}
	setOp := scope.Expression
	// Access the args directly, because set operation operands aren't guaranteed to be Query nodes
	expressionStack := []*exp.Expr{setOp.Expression(), setOp.This()}

	for len(expressionStack) > 0 {
		expression := expressionStack[len(expressionStack)-1]
		expressionStack = expressionStack[:len(expressionStack)-1]
		setOpScope := setOpScopeStack[len(setOpScopeStack)-1]

		newScope := setOpScope.Branch(expression, ScopeSetOperation, BranchOptions{OuterColumns: setOpScope.OuterColumns})

		// Use the scope's unwrapped expression so parenthesized left operands are descended
		// into iteratively, until the chain's leftmost query has been scoped
		inner := newScope.Expression
		if prevScope == nil && inner.Is(exp.SetOperation) {
			out = append(out, traverseCtes(newScope)...)
			setOpScopeStack = append(setOpScopeStack, newScope)
			expressionStack = append(expressionStack, inner.Expression(), inner.This())
			continue
		}

		children := traverseScope(newScope)
		out = append(out, children...)
		if len(children) == 0 {
			panic(&errs.OptimizeError{Message: "Cannot build a scope for set operation operand: " + pyStr(expression)})
		}
		branchScope := children[len(children)-1]

		if prevScope != nil {
			setOpScopeStack = setOpScopeStack[:len(setOpScopeStack)-1]
			setOpScope.SetOperationScopes = []*Scope{prevScope, branchScope}
			prevScope = setOpScope
			out = append(out, setOpScope)
		} else {
			prevScope = branchScope
		}
	}
	return out
}

func traverseCtes(scope *Scope) []*Scope {
	var out []*Scope
	sources := NewOrderedMap[Source]()
	for _, cte := range scope.Ctes() {
		cteName := cte.Alias()
		// if the scope is a recursive cte, it must be in the form of base_case UNION recursive.
		// thus the recursive scope is the first section of the union.
		if with := scope.Expression.Get("with_"); with != nil && with.Recursive() {
			if union := cte.This(); union.Is(exp.SetOperation) {
				sources.Set(cteName, scope.Branch(union.This(), ScopeCTE, BranchOptions{}))
			}
		}
		children := traverseScope(scope.Branch(cte.This(), ScopeCTE, BranchOptions{
			CteSources:   sources,
			OuterColumns: cte.AliasColumnNames(),
		}))
		out = append(out, children...)
		// append the final child_scope yielded
		if len(children) > 0 {
			child := children[len(children)-1]
			sources.Set(cteName, child)
			scope.CteScopes = append(scope.CteScopes, child)
		}
	}
	scope.Sources.Update(sources)
	scope.CteSources.Update(sources)
	return out
}

// isDerivedTable mirrors scope._is_derived_table.
func isDerivedTable(e *exp.Expr) bool {
	return e.Is(exp.Subquery) && (e.Alias() != "" || exp.Truthy(e.Arg("pivots")) || e.This().IsAny(exp.Select, exp.SetOperation))
}

// isFromOrJoin mirrors scope._is_from_or_join.
func isFromOrJoin(e *exp.Expr) bool {
	parent := e.Parent()
	// Subqueries can be arbitrarily nested
	for parent.IsKind(exp.Subquery) {
		parent = parent.Parent()
	}
	return parent.IsKind(exp.From) || parent.IsKind(exp.Join)
}

func isSemiOrAntiJoinRHS(node *exp.Expr) bool {
	parent := node.Parent()
	return parent.Is(exp.Join) && parent.IsSemiOrAntiJoin()
}

func traverseTables(scope *Scope) []*Scope {
	var out []*Scope
	sources := NewOrderedMap[Source]()

	// Traverse FROMs, JOINs, and LATERALs in the order they are defined
	var expressions []*exp.Expr
	if from := scope.Expression.Get("from_"); from != nil {
		expressions = append(expressions, from.This())
	}
	for _, join := range scope.Expression.List("joins") {
		expressions = append(expressions, join.This())
	}
	if scope.Expression.IsAny(exp.Table, exp.Subquery) {
		// A Subquery-rooted scope scopes its own inner query as a derived table
		expressions = append(expressions, scope.Expression)
	}
	expressions = append(expressions, scope.Expression.List("laterals")...)

	for i := 0; i < len(expressions); i++ {
		expression := expressions[i]
		if expression.Is(exp.Final) {
			expression = expression.This()
		}
		if expression.Is(exp.Table) {
			tableName := expression.Name()
			sourceName := expression.AliasOrName()
			if src, ok := scope.Sources.Get(tableName); ok && expression.DB() == "" {
				// This is a reference to a parent source (e.g. a CTE), not an actual table. If
				// it is pivoted, we also register the pivot's alias as a new table source.
				sources.Set(sourceName, src)
				if pivots := expression.List("pivots"); len(pivots) > 0 {
					sources.Set(pivots[len(pivots)-1].Alias(), expression)
				}
			} else if sources.Has(sourceName) {
				sources.Set(helper.FindNewName(sources.Has, tableName), expression)
			} else {
				sources.Set(sourceName, expression)
			}
			// Make sure to not include the joins twice
			if expression != scope.Expression {
				for _, join := range expression.List("joins") {
					expressions = append(expressions, join.This())
				}
			}
			continue
		}

		if !expression.Is(exp.DerivedTable) {
			continue
		}
		node := expression
		var lateral *OrderedMap[Source]
		var scopeType ScopeType
		var scopes *[]*Scope
		if expression.Is(exp.UDTF) {
			lateral = sources
			scopeType = ScopeUDTF
			scopes = &scope.UdtfScopes
		} else if isDerivedTable(expression) {
			scopeType = ScopeDerivedTable
			scopes = &scope.DerivedTableScopes
			if node != scope.Expression {
				// The scope expression's own joins were already added above
				for _, join := range node.List("joins") {
					expressions = append(expressions, join.This())
				}
			}
		} else {
			// Makes sure we check for possible sources in nested table constructs
			expressions = append(expressions, node.This())
			if node != scope.Expression {
				for _, join := range node.List("joins") {
					expressions = append(expressions, join.This())
				}
			}
			continue
		}

		children := traverseScope(scope.Branch(node, scopeType, BranchOptions{
			LateralSources: lateral,
			OuterColumns:   node.AliasColumnNames(),
		}))
		for _, child := range children {
			out = append(out, child)
			// Tables without aliases will be set as ""
			sources.Set(getSourceAlias(node), child)
		}
		// append the final child_scope yielded
		if len(children) > 0 {
			child := children[len(children)-1]
			*scopes = append(*scopes, child)
			scope.TableScopes = append(scope.TableScopes, child)
		}
	}
	scope.Sources.Update(sources)
	return out
}

func traverseSubqueries(scope *Scope) []*Scope {
	var out []*Scope
	for _, subquery := range scope.Subqueries() {
		children := traverseScope(scope.Branch(subquery, ScopeSubquery, BranchOptions{}))
		out = append(out, children...)
		if len(children) > 0 {
			scope.SubqueryScopes = append(scope.SubqueryScopes, children[len(children)-1])
		}
	}
	return out
}

func traverseUdtfs(scope *Scope) []*Scope {
	var out []*Scope
	var udtfExpressions []*exp.Expr
	if scope.Expression.IsKind(exp.Unnest) {
		udtfExpressions = scope.Expression.Expressions()
	} else if scope.Expression.IsKind(exp.Lateral) {
		udtfExpressions = []*exp.Expr{scope.Expression.This()}
	}
	sources := NewOrderedMap[Source]()
	for _, expression := range udtfExpressions {
		if expression.IsKind(exp.Subquery) {
			children := traverseScope(scope.Branch(expression, ScopeSubquery, BranchOptions{
				OuterColumns: expression.AliasColumnNames(),
			}))
			for _, child := range children {
				out = append(out, child)
				sources.Set(getSourceAlias(expression), child)
			}
			if len(children) > 0 {
				scope.SubqueryScopes = append(scope.SubqueryScopes, children[len(children)-1])
			}
		}
	}
	scope.Sources.Update(sources)
	return out
}

// isScopeBoundary reports whether node (not the walk root) starts a child scope.
func isScopeBoundary(node *exp.Expr) bool {
	if !node.IsAny(exp.CTE, exp.Query) {
		return false
	}
	return node.Is(exp.CTE) ||
		(node.Parent().IsAny(exp.From, exp.Join) && isDerivedTable(node)) ||
		node.Parent().Is(exp.UDTF) ||
		node.IsAny(exp.Select, exp.SetOperation)
}

// WalkInScope mirrors scope.walk_in_scope: a DFS walk that doesn't cross into child scopes.
func WalkInScope(expression *exp.Expr, prune func(*exp.Expr) bool) iter.Seq[*exp.Expr] {
	return func(yield func(*exp.Expr) bool) {
		walkInScope(expression, prune, yield)
	}
}

func walkInScope(expression *exp.Expr, prune func(*exp.Expr) bool, yield func(*exp.Expr) bool) bool {
	if expression == nil {
		return true
	}
	stack := []*exp.Expr{expression}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !yield(node) {
			return false
		}
		if node != expression && isScopeBoundary(node) {
			if node.IsAny(exp.Subquery, exp.UDTF) {
				for _, key := range []string{"joins", "laterals", "pivots"} {
					for _, arg := range node.List(key) {
						if !walkInScope(arg, nil, yield) {
							return false
						}
					}
				}
			}
			continue
		}
		if prune != nil && prune(node) {
			continue
		}
		args := node.Args()
		for i := len(args) - 1; i >= 0; i-- {
			switch v := args[i].Value.(type) {
			case []*exp.Expr:
				for j := len(v) - 1; j >= 0; j-- {
					if v[j] != nil {
						stack = append(stack, v[j])
					}
				}
			case []any:
				for j := len(v) - 1; j >= 0; j-- {
					if x, ok := v[j].(*exp.Expr); ok && x != nil {
						stack = append(stack, x)
					}
				}
			case *exp.Expr:
				stack = append(stack, v)
			}
		}
	}
	return true
}

// FindAllInScope mirrors scope.find_all_in_scope.
func FindAllInScope(expression *exp.Expr, kinds ...exp.Kind) iter.Seq[*exp.Expr] {
	return func(yield func(*exp.Expr) bool) {
		for node := range WalkInScope(expression, nil) {
			if node.IsAny(kinds...) {
				if !yield(node) {
					return
				}
			}
		}
	}
}

// FindInScope mirrors scope.find_in_scope.
func FindInScope(expression *exp.Expr, kinds ...exp.Kind) *exp.Expr {
	for node := range FindAllInScope(expression, kinds...) {
		return node
	}
	return nil
}

func getSourceAlias(e *exp.Expr) string {
	aliasArg := e.Get("alias")
	aliasName := e.Alias()
	if aliasName == "" && aliasArg.IsKind(exp.TableAlias) && len(aliasArg.Columns()) == 1 {
		aliasName = aliasArg.Columns()[0].Name()
	}
	return aliasName
}
