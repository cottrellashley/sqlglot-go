package sqlglot

// Port of sqlglot/optimizer/merge_subqueries.py.

import (
	"sort"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// CopyBudget caps the number of nodes copied by merges: merging copies an inner projection
// into every outer reference to it, so chained merges of repeatedly referenced projections can
// grow the AST exponentially.
type CopyBudget struct {
	expression    *exp.Expr
	maxCopyFactor int // 0 disables the budget
	minCopyBudget int
	remaining     int
	initialized   bool
}

// Default copy budget settings (CopyBudget.MAX_COPY_FACTOR and MIN_COPY_BUDGET).
const (
	MaxCopyFactor = 8
	MinCopyBudget = 1000
)

// NewCopyBudget creates a copy budget; maxCopyFactor <= 0 disables it.
func NewCopyBudget(e *exp.Expr, maxCopyFactor, minCopyBudget int) *CopyBudget {
	return &CopyBudget{expression: e, maxCopyFactor: maxCopyFactor, minCopyBudget: minCopyBudget}
}

func (b *CopyBudget) consume(outerScope, innerScope *Scope, alias string) bool {
	if b.maxCopyFactor <= 0 {
		return true
	}
	if !b.initialized {
		size := 0
		for range b.expression.Walk(true, nil) {
			size++
		}
		b.remaining = max(b.maxCopyFactor*size, b.minCopyBudget)
		b.initialized = true
	}
	references := map[string]int{}
	for _, c := range outerScope.Columns() {
		if c.TableName() == alias {
			references[c.Name()]++
		}
	}
	copies := 0
	for _, projection := range innerScope.Expression.Expressions() {
		count := references[projection.AliasOrName()] - 1
		if count > 0 {
			for range projection.Unalias().Walk(true, nil) {
				copies += count
				if copies > b.remaining {
					return false
				}
			}
		}
	}
	b.remaining -= copies
	return true
}

// MergeSubqueriesOptions are the keyword arguments of merge_subqueries.
type MergeSubqueriesOptions struct {
	LeaveTablesIsolated bool
	// MaxCopyFactor and MinCopyBudget configure the copy budget (zero values mean the
	// defaults); NoCopyBudget disables it (max_copy_factor=None).
	MaxCopyFactor int
	MinCopyBudget int
	NoCopyBudget  bool
}

// MergeSubqueries rewrites the AST to merge derived tables into the outer query. This also
// merges CTEs if they are selected from only once.
func MergeSubqueries(e *exp.Expr, o MergeSubqueriesOptions) *exp.Expr {
	scopes := TraverseScope(e)
	factor, minBudget := o.MaxCopyFactor, o.MinCopyBudget
	if factor == 0 {
		factor = MaxCopyFactor
	}
	if minBudget == 0 {
		minBudget = MinCopyBudget
	}
	if o.NoCopyBudget {
		factor = 0
	}
	budget := NewCopyBudget(e, factor, minBudget)
	e, mergedCtes := MergeCtes(e, o.LeaveTablesIsolated, scopes, budget)
	if mergedCtes {
		scopes = TraverseScope(e)
	}
	return MergeDerivedTables(e, o.LeaveTablesIsolated, scopes, budget)
}

var unmergableArgs = func() map[string]bool {
	keep := map[string]bool{"expressions": true, "from_": true, "joins": true, "where": true, "order": true, "hint": true}
	out := map[string]bool{}
	for _, a := range exp.Select.Args() {
		if !keep[a.Key] {
			out[a.Key] = true
		}
	}
	return out
}()

var safeToReplaceUnwrapped = []exp.Kind{exp.Column, exp.EQ, exp.Func, exp.NEQ, exp.Paren}

type cteSelection struct {
	outer, inner *Scope
	table        *exp.Expr
}

// MergeCtes merges CTEs selected from only once.
func MergeCtes(e *exp.Expr, leaveTablesIsolated bool, scopes []*Scope, budget *CopyBudget) (*exp.Expr, bool) {
	if budget == nil {
		budget = NewCopyBudget(e, MaxCopyFactor, MinCopyBudget)
	}
	if scopes == nil {
		scopes = TraverseScope(e)
	}
	cteSelections := map[*Scope][]cteSelection{}
	var order []*Scope
	for _, outerScope := range scopes {
		for _, ss := range outerScope.SelectedSources().Values() {
			if inner, ok := ss.Source.(*Scope); ok && inner.IsCTE() {
				if _, seen := cteSelections[inner]; !seen {
					order = append(order, inner)
				}
				cteSelections[inner] = append(cteSelections[inner], cteSelection{outerScope, inner, ss.Node})
			}
		}
	}
	merged := false
	for _, k := range order {
		v := cteSelections[k]
		if len(v) != 1 {
			continue
		}
		outerScope, innerScope, table := v[0].outer, v[0].inner, v[0].table
		fromOrJoin := table.FindAncestor(exp.From, exp.Join)
		if !fromOrJoin.IsAny(exp.From, exp.Join) {
			continue
		}
		alias := table.AliasOrName()
		if mergeable(outerScope, innerScope, leaveTablesIsolated, fromOrJoin) && budget.consume(outerScope, innerScope, alias) {
			renameInnerSources(outerScope, innerScope, alias)
			mergeFrom(outerScope, innerScope, table, alias)
			mergeExpressions(outerScope, innerScope, alias)
			mergeOrder(outerScope, innerScope)
			mergeJoins(outerScope, innerScope, fromOrJoin)
			mergeWhere(outerScope, innerScope, fromOrJoin)
			mergeHints(outerScope, innerScope)
			popCte(innerScope)
			outerScope.ClearCache()
			merged = true
		}
	}
	return e, merged
}

// MergeDerivedTables merges derived tables into their outer queries.
func MergeDerivedTables(e *exp.Expr, leaveTablesIsolated bool, scopes []*Scope, budget *CopyBudget) *exp.Expr {
	if budget == nil {
		budget = NewCopyBudget(e, MaxCopyFactor, MinCopyBudget)
	}
	if scopes == nil {
		scopes = TraverseScope(e)
	}
	for _, outerScope := range scopes {
		for _, subquery := range outerScope.DerivedTables() {
			fromOrJoin := subquery.FindAncestor(exp.From, exp.Join)
			if !fromOrJoin.IsAny(exp.From, exp.Join) {
				continue
			}
			alias := subquery.AliasOrName()
			src, ok := outerScope.Sources.Get(alias)
			if !ok {
				panic(&keyError{alias})
			}
			innerScope, ok := src.(*Scope)
			if !ok {
				continue
			}
			if mergeable(outerScope, innerScope, leaveTablesIsolated, fromOrJoin) && budget.consume(outerScope, innerScope, alias) {
				renameInnerSources(outerScope, innerScope, alias)
				mergeFrom(outerScope, innerScope, subquery, alias)
				mergeExpressions(outerScope, innerScope, alias)
				mergeOrder(outerScope, innerScope)
				mergeJoins(outerScope, innerScope, fromOrJoin)
				mergeWhere(outerScope, innerScope, fromOrJoin)
				mergeHints(outerScope, innerScope)
				outerScope.ClearCache()
			}
		}
	}
	return e
}

type keyError struct{ key string }

func (k *keyError) Error() string { return "KeyError: '" + k.key + "'" }

// mergeable reports whether the inner select can be merged into the outer query.
func mergeable(outerScope, innerScope *Scope, leaveTablesIsolated bool, fromOrJoin *exp.Expr) bool {
	innerSelect := innerScope.Expression.Unnest()
	outer := outerScope.Expression
	innerName := fromOrJoin.AliasOrName()

	windowProjectionBlocksMerge := func(windowAliases map[string]bool) bool {
		if len(windowAliases) == 0 {
			return false
		}
		if outer.Bool("where") || outer.Bool("joins") {
			return true
		}
		if IsImplicitGroupByAll(outer) {
			return true
		}
		for _, column := range outerScope.Columns() {
			if column.TableName() == innerName && windowAliases[column.Name()] &&
				column.FindAncestor(exp.Group, exp.Order, exp.Having, exp.AggFunc) != nil {
				return true
			}
		}
		return false
	}

	literalGroupUnmergeable := func(numberLiteralAliases map[string]bool) bool {
		group := outer.Get("group")
		if group == nil || len(numberLiteralAliases) == 0 {
			return false
		}
		grouped := map[string]bool{}
		topLevel := map[*exp.Expr]bool{}
		for _, g := range group.Expressions() {
			topLevel[g.Unnest()] = true
		}
		for _, col := range group.FindAllList(exp.Column) {
			if col.TableName() != innerName || !numberLiteralAliases[col.Name()] {
				continue
			}
			if !topLevel[col] {
				return true
			}
			grouped[col.Name()] = true
		}
		if len(grouped) == 0 {
			return false
		}
		projected := map[string]bool{}
		for _, s := range outer.Selects() {
			if u := s.Unalias(); u.IsKind(exp.Column) && u.TableName() == innerName {
				projected[u.Name()] = true
			}
		}
		for g := range grouped {
			if !projected[g] {
				return true
			}
		}
		return false
	}

	// All columns from the inner select in the ON clause must be from the first FROM table.
	outerSelectJoinsOnInnerSelectJoin := func(projections map[string]*exp.Expr) bool {
		if !fromOrJoin.IsKind(exp.Join) {
			return false
		}
		on := fromOrJoin.Get("on")
		if on == nil {
			return false
		}
		var selections []string
		for c := range FindAllInScope(on, exp.Column) {
			if c.TableName() == innerName {
				selections = append(selections, c.Name())
			}
		}
		innerFrom := innerScope.Expression.Get("from_")
		if innerFrom == nil {
			return false
		}
		innerFromTable := innerFrom.AliasOrName()
		for _, selection := range selections {
			p, ok := projections[selection]
			if !ok {
				panic(&keyError{selection})
			}
			for _, col := range p.FindAllList(exp.Column) {
				if col.TableName() != innerFromTable {
					return true
				}
			}
		}
		return false
	}

	isRecursive := func() bool {
		cte := innerScope.Expression.Parent()
		for node := outer.Parent(); node != nil; node = node.Parent() {
			if node == cte {
				return true
			}
		}
		return false
	}

	literalInOrderBy := func(numberLiteralAliases map[string]bool) bool {
		order := outer.Get("order")
		if order == nil {
			return false
		}
		for _, o := range order.Expressions() {
			if key := o.This().Unnest(); key.IsKind(exp.Column) && key.TableName() == innerName && numberLiteralAliases[key.Name()] {
				return true
			}
		}
		return false
	}

	if !outer.IsKind(exp.Select) || outer.IsStar() || !innerSelect.IsKind(exp.Select) {
		return false
	}
	for _, a := range innerSelect.Args() {
		if unmergableArgs[a.Key] && exp.Truthy(a.Value) {
			return false
		}
	}
	if innerSelect.Get("from_") == nil || len(outerScope.Pivots()) > 0 {
		return false
	}
	for _, j := range outer.List("joins") {
		if j.Bool("nearest") {
			return false
		}
	}
	for _, j := range innerSelect.List("joins") {
		if j.Bool("nearest") {
			return false
		}
	}
	if leaveTablesIsolated && outerScope.SelectedSources().Len() > 1 {
		return false
	}
	if fromOrJoin.IsKind(exp.Join) && innerSelect.Bool("joins") {
		return false
	}
	if side := fromOrJoin.TextUpper("side"); fromOrJoin.IsKind(exp.Join) && innerSelect.Bool("where") &&
		(side == "FULL" || side == "LEFT" || side == "RIGHT") {
		return false
	}
	if fromOrJoin.IsKind(exp.From) && innerSelect.Bool("where") {
		for _, j := range outer.List("joins") {
			if s := j.TextUpper("side"); s == "FULL" || s == "RIGHT" {
				return false
			}
		}
	}
	if innerSelect.Bool("order") && outerScope.IsSetOperation() {
		return false
	}
	if seqGet(innerSelect.Expressions(), 0).IsKind(exp.QueryTransform) {
		return false
	}

	windowAliases := map[string]bool{}
	numberLiteralAliases := map[string]bool{}
	projections := map[string]*exp.Expr{}
	blockers := append([]exp.Kind{exp.AggFunc, exp.Select}, setReturningFunctions...)
	for _, s := range innerSelect.Selects() {
		name := s.AliasOrName()
		projections[name] = s
		if s.Unalias().IsNumber() {
			numberLiteralAliases[name] = true
		}
		for node := range s.Walk(true, nil) {
			if node.IsAny(blockers...) {
				return false
			}
			if node.IsKind(exp.Window) {
				windowAliases[name] = true
			}
		}
	}
	return !outerSelectJoinsOnInnerSelectJoin(projections) &&
		!windowProjectionBlocksMerge(windowAliases) &&
		!literalGroupUnmergeable(numberLiteralAliases) &&
		!literalInOrderBy(numberLiteralAliases) &&
		!(innerScope.IsCTE() && isRecursive())
}

// renameInnerSources renames any sources in the inner query that conflict with names in the
// outer query.
func renameInnerSources(outerScope, innerScope *Scope, alias string) {
	innerTaken := map[string]bool{}
	for _, k := range innerScope.SelectedSources().Keys() {
		innerTaken[k] = true
	}
	outerTaken := map[string]bool{}
	for _, ref := range outerScope.References() {
		outerTaken[ref.Name] = true
	}
	var conflicts []string
	for k := range outerTaken {
		if innerTaken[k] && k != alias {
			conflicts = append(conflicts, k)
		}
	}
	sort.Strings(conflicts)
	taken := map[string]bool{}
	for k := range outerTaken {
		taken[k] = true
	}
	for k := range innerTaken {
		taken[k] = true
	}
	for _, conflict := range conflicts {
		newName := helper.FindNewNameIn(taken, conflict)
		ss, _ := innerScope.SelectedSources().Get(conflict)
		source := ss.Node
		newAlias := exp.ToIdentifier(newName)
		switch {
		case source.IsKind(exp.Table) && source.Alias() != "":
			source.Set("alias", exp.New(exp.TableAlias, "this", newAlias))
		case source.IsKind(exp.Table):
			source.Replace(exp.NewAlias(source, newAlias))
		case source.Parent().IsKind(exp.Subquery):
			source.Parent().Set("alias", exp.New(exp.TableAlias, "this", newAlias))
		}
		for _, column := range innerScope.SourceColumns(conflict) {
			column.Set("table", exp.ToIdentifier(newName))
		}
		innerScope.RenameSource(conflict, newName)
	}
}

// mergeFrom merges the FROM clause of the inner query into the outer query.
func mergeFrom(outerScope, innerScope *Scope, nodeToReplace *exp.Expr, alias string) {
	newSubquery := innerScope.Expression.Get("from_").This()
	newSubquery.Set("joins", nodeToReplace.Arg("joins"))
	nodeToReplace.Replace(newSubquery)
	for _, joinHint := range outerScope.JoinHints() {
		for _, table := range joinHint.FindAllList(exp.Table) {
			if table.AliasOrName() == nodeToReplace.AliasOrName() {
				table.Set("this", exp.ToIdentifier(newSubquery.AliasOrName()))
			}
		}
	}
	outerScope.RemoveSource(alias)
	src, ok := innerScope.Sources.Get(newSubquery.AliasOrName())
	if !ok {
		panic(&keyError{newSubquery.AliasOrName()})
	}
	outerScope.AddSource(newSubquery.AliasOrName(), src)
}

// mergeJoins merges the JOIN clauses of the inner query into the outer query.
func mergeJoins(outerScope, innerScope *Scope, fromOrJoin *exp.Expr) {
	var newJoins []*exp.Expr
	for _, join := range innerScope.Expression.List("joins") {
		newJoins = append(newJoins, join)
		src, ok := innerScope.Sources.Get(join.AliasOrName())
		if !ok {
			panic(&keyError{join.AliasOrName()})
		}
		outerScope.AddSource(join.AliasOrName(), src)
	}
	if len(newJoins) > 0 {
		outerJoins := append([]*exp.Expr(nil), outerScope.Expression.List("joins")...)
		position := 0
		if !fromOrJoin.IsKind(exp.From) {
			idx := -1
			for i, j := range outerJoins {
				if j.Equal(fromOrJoin) {
					idx = i
					break
				}
			}
			if idx < 0 {
				panic(&keyError{"join not in list"})
			}
			position = idx + 1
		}
		merged := append(append(append([]*exp.Expr(nil), outerJoins[:position]...), newJoins...), outerJoins[position:]...)
		outerScope.Expression.Set("joins", merged)
	}
}

// mergeExpressions merges the projections of the inner query into the outer query.
func mergeExpressions(outerScope, innerScope *Scope, alias string) {
	outerColumns := NewOrderedMap[[]*exp.Expr]()
	for _, column := range outerScope.Columns() {
		if column.TableName() == alias {
			cols, _ := outerColumns.Get(column.Name())
			outerColumns.Set(column.Name(), append(cols, column))
		}
	}
	group := outerScope.Expression.Get("group")
	for _, expression := range innerScope.Expression.Expressions() {
		projectionName := expression.AliasOrName()
		if projectionName == "" {
			continue
		}
		columnsToReplace, _ := outerColumns.Get(projectionName)
		if len(columnsToReplace) == 0 {
			continue
		}
		expression = expression.Unalias()
		mustWrapExpression := !expression.IsAny(safeToReplaceUnwrapped...)
		isNumber := expression.IsNumber()
		last := len(columnsToReplace) - 1
		groupOrdinal := 0
		if isNumber && outerScope.Expression.IsKind(exp.Select) {
			for j, s := range outerScope.Expression.Selects() {
				if u := s.Unalias(); u.IsKind(exp.Column) && u.TableName() == alias && u.Name() == projectionName {
					groupOrdinal = j + 1
					break
				}
			}
		}
		for i, column := range columnsToReplace {
			parent := column.Parent()
			if isNumber && group != nil {
				var item *exp.Expr
				for _, g := range group.Expressions() {
					if g.Unnest() == column {
						item = g
						break
					}
				}
				if item != nil {
					item.Replace(exp.LiteralNumber(groupOrdinal))
					continue
				}
			}
			replacement := expression
			if i < last {
				replacement = expression.Copy()
			}
			if parent.IsAny(exp.Unary, exp.Binary) && mustWrapExpression {
				replacement = exp.NewParen(replacement, exp.WithCopy(false))
			}
			if parent.IsKind(exp.Select) && column.Name() != expression.Name() {
				replacement = exp.NewAlias(replacement, column.Name(), exp.WithCopy(false))
			}
			column.Replace(replacement)
		}
	}
	if outerColumns.Len() > 0 {
		for _, scope := range outerScope.Traverse() {
			scope.ClearCache()
		}
	}
}

// mergeWhere merges the WHERE clause of the inner query into the outer query.
func mergeWhere(outerScope, innerScope *Scope, fromOrJoin *exp.Expr) {
	where := innerScope.Expression.Get("where")
	if where == nil || where.This() == nil {
		return
	}
	expression := outerScope.Expression
	if fromOrJoin.IsKind(exp.Join) {
		from := expression.Get("from_")
		sources := map[string]bool{}
		if from != nil {
			sources[from.AliasOrName()] = true
		}
		for _, join := range expression.List("joins") {
			source := join.AliasOrName()
			sources[source] = true
			if source == fromOrJoin.AliasOrName() {
				break
			}
		}
		subset := true
		for t := range exp.ColumnTableNames(where.This(), "") {
			if !sources[t] {
				subset = false
			}
		}
		if subset {
			fromOrJoin.On(where.This(), exp.WithCopy(false))
			fromOrJoin.Set("on", fromOrJoin.Get("on"))
			return
		}
	}
	expression.Where(where.This(), exp.WithCopy(false))
}

// mergeOrder merges the ORDER clause of the inner query into the outer query.
func mergeOrder(outerScope, innerScope *Scope) {
	innerOrder := innerScope.Expression.Get("order")
	if innerOrder == nil {
		return
	}
	for _, arg := range []string{"group", "distinct", "having", "order"} {
		if outerScope.Expression.Bool(arg) {
			return
		}
	}
	if outerScope.SelectedSources().Len() != 1 {
		return
	}
	for _, e := range outerScope.Expression.Expressions() {
		if e.Find(exp.AggFunc) != nil {
			return
		}
	}
	outerScope.Expression.Set("order", innerOrder)
}

func mergeHints(outerScope, innerScope *Scope) {
	innerHint := innerScope.Expression.Get("hint")
	if innerHint == nil {
		return
	}
	if outerHint := outerScope.Expression.Get("hint"); outerHint != nil {
		for _, h := range innerHint.Expressions() {
			outerHint.Append("expressions", h)
		}
	} else {
		outerScope.Expression.Set("hint", innerHint)
	}
}

// popCte removes a CTE from the AST.
func popCte(innerScope *Scope) {
	cte := innerScope.Expression.Parent()
	if cte == nil {
		return
	}
	with := cte.Parent()
	if with == nil {
		return
	}
	if len(with.Expressions()) == 1 {
		with.Pop()
	} else {
		cte.Pop()
	}
}
