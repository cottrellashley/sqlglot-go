package sqlglot

// Ports of sqlglot/optimizer/optimize_joins.py, eliminate_ctes.py, eliminate_joins.py,
// eliminate_subqueries.py and pushdown_predicates.py.

import (
	"fmt"
	"sort"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// ---------------------------------------------------------------------------------------
// optimize_joins

var joinAttrs = []string{"on", "side", "kind", "using", "method"}

// OptimizeJoins removes cross joins if possible and reorders joins based on predicate
// dependencies.
func OptimizeJoins(e *exp.Expr) *exp.Expr {
	for _, sel := range e.FindAllList(exp.Select) {
		joins := sel.List("joins")
		if !isReorderable(joins) {
			continue
		}
		references := map[string][]*exp.Expr{}
		type crossJoin struct {
			name string
			join *exp.Expr
		}
		var crossJoins []crossJoin
		for _, join := range joins {
			tables := otherTableNames(join)
			if len(tables) > 0 {
				for _, table := range sortedSet(tables) {
					references[table] = append(append([]*exp.Expr(nil), references[table]...), join)
				}
			} else {
				crossJoins = append(crossJoins, crossJoin{join.AliasOrName(), join})
			}
		}
		for _, cj := range crossJoins {
			for _, dep := range references[cj.name] {
				// An ANTI join's ON clause is negated, i.e. NOT EXISTS(a AND b) is not equivalent
				// to b AND NOT EXISTS(a), so its conjuncts can't be extracted
				if dep.TextUpper("kind") == "ANTI" {
					continue
				}
				on := dep.Get("on")
				// Only conjuncts can be extracted, i.e. `a OR b` is not `b AND (a OR TRUE)`
				if on.IsKind(exp.And) {
					if len(otherTableNames(dep)) < 2 {
						continue
					}
					for _, predicate := range on.Flatten(true) {
						if exp.ColumnTableNames(predicate, "")[cj.name] {
							predicate.Replace(exp.NewTrue())
							predicate = exp.NewAnd(cj.join.Get("on"), predicate, exp.WithCopy(false))
							cj.join.On(predicate, exp.WithAppend(false), exp.WithCopy(false))
						}
					}
				}
			}
		}
	}
	e = ReorderJoins(e)
	e = NormalizeJoins(e)
	return e
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ReorderJoins reorders joins by topological sort order based on predicate references.
func ReorderJoins(e *exp.Expr) *exp.Expr {
	for _, from := range e.FindAllList(exp.From) {
		parent := from.Parent()
		if parent == nil {
			optimizeErr("FROM clause without parent expression")
		}
		joins := parent.List("joins")
		if !isReorderable(joins) {
			continue
		}
		joinsByName := map[string]*exp.Expr{}
		dag := map[string]map[string]bool{}
		for _, join := range joins {
			joinsByName[join.AliasOrName()] = join
		}
		for name, join := range joinsByName {
			dag[name] = otherTableNames(join)
		}
		order, err := helper.TSort(dag)
		if err != nil {
			panic(err)
		}
		var reordered []*exp.Expr
		for _, name := range order {
			if name != from.AliasOrName() {
				if j, ok := joinsByName[name]; ok {
					reordered = append(reordered, j)
				}
			}
		}
		parent.Set("joins", reordered)
	}
	return e
}

// NormalizeJoins removes INNER and OUTER from joins as they are optional
// (optimize_joins.normalize).
func NormalizeJoins(e *exp.Expr) *exp.Expr {
	for _, join := range e.FindAllList(exp.Join) {
		if join.TextUpper("kind") == "PASTE" || join.Bool("nearest") {
			continue
		}
		anyAttr := false
		for _, k := range joinAttrs {
			if join.Bool(k) {
				anyAttr = true
			}
		}
		if !anyAttr {
			join.Set("kind", "CROSS")
		}
		if join.TextUpper("kind") == "CROSS" {
			join.Set("on", nil)
		} else {
			if k := join.TextUpper("kind"); k == "INNER" || k == "OUTER" {
				join.Set("kind", nil)
			}
			if !join.Bool("on") && !join.Bool("using") {
				join.Set("on", exp.NewTrue())
			}
		}
	}
	return e
}

func otherTableNames(join *exp.Expr) map[string]bool {
	on := join.Get("on")
	if on == nil {
		return map[string]bool{}
	}
	return exp.ColumnTableNames(on, join.AliasOrName())
}

// isReorderable checks if joins can be reordered without changing query semantics: joins with
// a side, PASTE joins and NEAREST joins can't.
func isReorderable(joins []*exp.Expr) bool {
	for _, join := range joins {
		if join.Text("side") != "" || join.TextUpper("kind") == "PASTE" || join.Bool("nearest") {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------------------
// eliminate_ctes

// EliminateCtes removes unused CTEs from an expression. journal may be nil.
func EliminateCtes(e *exp.Expr, journal *Journal) *exp.Expr {
	root := BuildScope(e)
	if root == nil {
		return e
	}
	refCount := root.RefCount()
	// Traverse the scope tree in reverse so we can remove chains of unused CTEs
	scopes := root.Traverse()
	for i := len(scopes) - 1; i >= 0; i-- {
		scope := scopes[i]
		if !scope.IsCTE() {
			continue
		}
		if refCount[scope] > 0 {
			continue
		}
		cteNode := scope.Expression.Parent()
		if cteNode == nil {
			continue
		}
		withNode := cteNode.Parent()
		if journal != nil && withNode != nil {
			journal.Record(withNode, "expressions")
		}
		cteNode.Pop()
		// Pop the entire WITH clause if this is the last CTE
		if withNode != nil && len(withNode.Expressions()) == 0 {
			if journal != nil && withNode.Parent() != nil {
				journal.Record(withNode.Parent(), "with_")
			}
			withNode.Pop()
		}
		// Decrement the ref count for all sources this CTE selects from
		for _, ss := range scope.SelectedSources().Values() {
			if s, ok := ss.Source.(*Scope); ok {
				refCount[s]--
			}
		}
	}
	return e
}

// ---------------------------------------------------------------------------------------
// eliminate_joins

// EliminateJoins removes unused joins from an expression, when the join condition doesn't
// produce duplicate rows.
func EliminateJoins(e *exp.Expr) *exp.Expr {
	for _, scope := range TraverseScope(e) {
		joins := scope.Expression.List("joins")
		if len(joins) == 0 {
			continue
		}
		if len(scope.UnqualifiedColumns()) > 0 {
			continue
		}
		for i := len(joins) - 1; i >= 0; i-- {
			join := joins[i]
			if join.IsSemiOrAntiJoin() || join.Bool("nearest") {
				continue
			}
			alias := join.AliasOrName()
			if shouldEliminateJoin(scope, join, alias) {
				join.Pop()
				scope.RemoveSource(alias)
			}
		}
	}
	return e
}

func shouldEliminateJoin(scope *Scope, join *exp.Expr, alias string) bool {
	src, _ := scope.Sources.Get(alias)
	inner, ok := src.(*Scope)
	if !ok || joinIsUsed(scope, join, alias) {
		return false
	}
	return (join.TextUpper("side") == "LEFT" && isJoinedOnAllUniqueOutputs(inner, join)) ||
		(!join.Bool("on") && hasSingleOutputRow(inner))
}

func joinIsUsed(scope *Scope, join *exp.Expr, alias string) bool {
	onClauseColumns := map[*exp.Expr]bool{}
	if on := join.Get("on"); on != nil {
		for _, c := range on.FindAllList(exp.Column) {
			onClauseColumns[c] = true
		}
	}
	for _, column := range scope.SourceColumns(alias) {
		if !onClauseColumns[column] {
			return true
		}
	}
	return false
}

func isJoinedOnAllUniqueOutputs(scope *Scope, join *exp.Expr) bool {
	uniqueOutputs := uniqueOutputs(scope)
	if len(uniqueOutputs) == 0 {
		return false
	}
	_, joinKeys, _ := JoinCondition(join)
	for _, c := range joinKeys {
		delete(uniqueOutputs, c.Name())
	}
	return len(uniqueOutputs) == 0
}

// uniqueOutputs determines the output columns of scope that must have a unique combination
// per row.
func uniqueOutputs(scope *Scope) map[string]bool {
	expr := scope.Expression
	set := func(names []string) map[string]bool {
		out := map[string]bool{}
		for _, n := range names {
			out[n] = true
		}
		return out
	}
	if distinct := expr.Get("distinct"); distinct != nil && !(distinct.IsKind(exp.Distinct) && distinct.Bool("on")) {
		return set(expr.NamedSelects())
	}
	if group := expr.Get("group"); group != nil {
		groupedExpressions := map[exp.ExprKey]bool{}
		for _, g := range group.Expressions() {
			groupedExpressions[g.MapKey()] = true
		}
		groupedOutputs := map[exp.ExprKey]bool{}
		unique := map[string]bool{}
		for _, sel := range expr.Selects() {
			output := sel.Unalias()
			if groupedExpressions[output.MapKey()] {
				groupedOutputs[output.MapKey()] = true
				unique[sel.AliasOrName()] = true
			}
		}
		for k := range groupedExpressions {
			if !groupedOutputs[k] {
				return map[string]bool{}
			}
		}
		return unique
	}
	if isLimit1(scope) || hasSingleOutputRow(scope) {
		return set(expr.NamedSelects())
	}
	return map[string]bool{}
}

var setReturningFunctions = []exp.Kind{exp.Anonymous, exp.UDTF, exp.ExplodingGenerateSeries}

func hasSingleOutputRow(scope *Scope) bool {
	expression := scope.Expression
	if !expression.IsKind(exp.Select) {
		return false
	}
	for _, key := range []string{"group", "having", "qualify", "limit", "offset"} {
		if expression.Bool(key) {
			return false
		}
	}
	if expression.Bool("from_") || expression.Bool("where") {
		for _, sel := range expression.Selects() {
			if !sel.Unalias().Is(exp.AggFunc) {
				return false
			}
		}
		return true
	}
	for _, sel := range expression.Selects() {
		if FindInScope(sel, setReturningFunctions...) != nil {
			return false
		}
	}
	return true
}

func isLimit1(scope *Scope) bool {
	limit := scope.Expression.Get("limit")
	return limit.IsKind(exp.Limit) && limit.Expression().Arg("this") == "1"
}

// JoinCondition extracts the join condition from a join expression: (source key, join key,
// remaining predicate).
func JoinCondition(join *exp.Expr) ([]*exp.Expr, []*exp.Expr, *exp.Expr) {
	name := join.AliasOrName()
	on := join.Get("on")
	if on == nil {
		on = exp.NewTrue()
	}
	on = on.Copy()
	var sourceKey, joinKey []*exp.Expr

	extractCondition := func(condition *exp.Expr) {
		operands := condition.UnnestOperands()
		left, right := operands[0], operands[1]
		leftTables := exp.ColumnTableNames(left, "")
		rightTables := exp.ColumnTableNames(right, "")
		if leftTables[name] && !rightTables[name] {
			joinKey = append(joinKey, left)
			sourceKey = append(sourceKey, right)
			condition.Replace(exp.NewTrue())
		} else if rightTables[name] && !leftTables[name] {
			joinKey = append(joinKey, right)
			sourceKey = append(sourceKey, left)
			condition.Replace(exp.NewTrue())
		}
	}

	if Normalized(on, false) {
		if !on.IsKind(exp.And) {
			on = exp.NewAnd(on, exp.NewTrue(), exp.WithCopy(false))
		}
		for _, condition := range on.Flatten(true) {
			if condition.IsKind(exp.EQ) {
				extractCondition(condition)
			}
		}
	} else if Normalized(on, true) {
		var conditions []*exp.Expr
		for _, condition := range on.Flatten(true) {
			var parts []*exp.Expr
			for _, part := range condition.Flatten(true) {
				if part.IsKind(exp.EQ) {
					parts = append(parts, part)
				}
			}
			if len(conditions) == 0 {
				conditions = parts
			} else {
				var temp []*exp.Expr
				for _, p := range parts {
					var cs []*exp.Expr
					for _, c := range conditions {
						if p.Equal(c) {
							cs = append(cs, c)
						}
					}
					if len(cs) > 0 {
						temp = append(temp, p)
						temp = append(temp, cs...)
					}
				}
				conditions = temp
			}
		}
		for _, condition := range conditions {
			extractCondition(condition)
		}
	}
	return sourceKey, joinKey, on
}

// ---------------------------------------------------------------------------------------
// eliminate_subqueries

// EliminateSubqueries rewrites derived tables as CTEs, deduplicating if possible.
func EliminateSubqueries(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Subquery) {
		EliminateSubqueries(e.This())
		return e
	}
	root := BuildScope(e)
	if root == nil {
		return e
	}
	// taken maps names to a *Scope or a table *exp.Expr
	taken := map[string]any{}
	for _, scope := range root.CteScopes {
		if parent := scope.Expression.Parent(); parent != nil {
			taken[parent.Alias()] = scope
		}
	}
	for _, scope := range root.Traverse() {
		for _, name := range scope.Sources.Keys() {
			source, _ := scope.Sources.Get(name)
			if t, ok := source.(*exp.Expr); ok && t.IsKind(exp.Table) {
				taken[t.Name()] = t
			}
		}
	}

	existingCtes := map[exp.ExprKey]string{}
	with := root.Expression.Get("with_")
	var recursive any = false
	if with != nil {
		recursive = with.Arg("recursive")
		for _, cte := range with.Expressions() {
			if _, ok := existingCtes[cte.This().MapKey()]; !ok {
				existingCtes[cte.This().MapKey()] = cte.Alias()
			} else {
				existingCtes[cte.This().MapKey()] = cte.Alias()
			}
		}
	}

	var newCtes []*exp.Expr
	for _, cteScope := range root.CteScopes {
		for _, scope := range cteScope.Traverse() {
			if scope == cteScope {
				continue
			}
			if newCte := eliminateScope(scope, existingCtes, taken); newCte != nil {
				newCtes = append(newCtes, newCte)
			}
		}
		if cteParent := cteScope.Expression.Parent(); cteParent != nil {
			newCtes = append(newCtes, cteParent)
		}
	}
	var scopes []*Scope
	scopes = append(scopes, root.SetOperationScopes...)
	scopes = append(scopes, root.SubqueryScopes...)
	scopes = append(scopes, root.TableScopes...)
	for _, scope := range scopes {
		for _, childScope := range scope.Traverse() {
			if newCte := eliminateScope(childScope, existingCtes, taken); newCte != nil {
				newCtes = append(newCtes, newCte)
			}
		}
	}
	if len(newCtes) > 0 {
		query := e
		if e.Is(exp.DDL) {
			query = e.Expression()
		}
		if !query.Is(exp.Query) {
			query = root.Expression.Unnest()
		}
		query.Set("with_", exp.New(exp.With, "expressions", newCtes, "recursive", recursive))
	}
	return e
}

func eliminateScope(scope *Scope, existingCtes map[exp.ExprKey]string, taken map[string]any) *exp.Expr {
	if scope.IsDerivedTable() {
		return eliminateDerivedTable(scope, existingCtes, taken)
	}
	if scope.IsCTE() {
		return eliminateCte(scope, existingCtes, taken)
	}
	return nil
}

func eliminateDerivedTable(scope *Scope, existingCtes map[exp.ExprKey]string, taken map[string]any) *exp.Expr {
	parentScope := scope.Parent
	if parentScope == nil || len(parentScope.Pivots()) > 0 || parentScope.Expression.IsKind(exp.Lateral) {
		return nil
	}
	exprParent := scope.Expression.Parent()
	if !exprParent.IsKind(exp.Subquery) || exprParent == parentScope.Expression {
		return nil
	}
	toReplace := exprParent.Unwrap()
	name, cte := newCte(scope, existingCtes, taken)
	alias := toReplace.Alias()
	if alias == "" {
		alias = name
	}
	table := exp.NewAlias(exp.NewTable(name), alias)
	table.Set("joins", toReplace.Arg("joins"))
	toReplace.Replace(table)
	return cte
}

func eliminateCte(scope *Scope, existingCtes map[exp.ExprKey]string, taken map[string]any) *exp.Expr {
	parent := scope.Expression.Parent()
	if parent == nil {
		return nil
	}
	name, cte := newCte(scope, existingCtes, taken)
	with := parent.Parent()
	parent.Pop()
	if with != nil && len(with.Expressions()) == 0 {
		with.Pop()
	}
	if scope.Parent == nil {
		return cte
	}
	for _, childScope := range scope.Parent.Traverse() {
		for _, ss := range childScope.SelectedSources().Values() {
			if s, ok := ss.Source.(*Scope); ok && s == scope {
				newTable := exp.NewAlias(exp.NewTable(name), ss.Node.AliasOrName(), exp.WithCopy(false))
				ss.Node.Replace(newTable)
			}
		}
	}
	return cte
}

// newCte returns a new name for the CTE in the root scope and a new CTE instance, or nil if
// it duplicates an existing CTE.
func newCte(scope *Scope, existingCtes map[exp.ExprKey]string, taken map[string]any) (string, *exp.Expr) {
	duplicateCteAlias := existingCtes[scope.Expression.MapKey()]
	parent := scope.Expression.Parent()
	name := ""
	if parent != nil {
		name = parent.Alias()
	}
	isTaken := func(n string) bool { _, ok := taken[n]; return ok && taken[n] != nil }
	if name == "" {
		name = helper.FindNewName(isTaken, "cte")
	}
	if duplicateCteAlias != "" {
		name = duplicateCteAlias
	} else if isTaken(name) {
		name = helper.FindNewName(isTaken, name)
	}
	taken[name] = scope
	if duplicateCteAlias == "" {
		existingCtes[scope.Expression.MapKey()] = name
		return name, exp.New(exp.CTE,
			"this", scope.Expression,
			"alias", exp.New(exp.TableAlias, "this", exp.ToIdentifier(name)))
	}
	return name, nil
}

// ---------------------------------------------------------------------------------------
// pushdown_predicates

// PushdownPredicates rewrites the AST to push down predicates in FROMS and JOINS.
func PushdownPredicates(e *exp.Expr, dialect any) *exp.Expr {
	root := BuildScope(e)
	d := MustDialect(dialect)
	unnestRequiresCrossJoin := d.Name == "athena" || d.Name == "presto" || d.Name == "trino"
	if root == nil {
		return e
	}
	scopeRefCount := root.RefCount()
	scopes := root.Traverse()
	for i := len(scopes) - 1; i >= 0; i-- {
		scope := scopes[i]
		sel := scope.Expression
		where := sel.Get("where")
		joins := sel.List("joins")
		hasNearest := false
		for _, join := range joins {
			if join.Bool("nearest") {
				hasNearest = true
			}
		}
		if hasNearest {
			continue
		}
		if where != nil {
			joinIndex := map[string]int{}
			for i, join := range joins {
				joinIndex[join.AliasOrName()] = i
			}
			lastNullExtending := -1
			for i, join := range joins {
				if s := join.TextUpper("side"); s == "RIGHT" || s == "FULL" {
					lastNullExtending = max(lastNullExtending, i)
				}
			}
			pushdownAllowed := true
			reachable := NewOrderedMap[SelectedSource]()
			selected := scope.SelectedSources()
			for _, k := range selected.Keys() {
				ss, _ := selected.Get(k)
				parent := ss.Node.FindAncestor(exp.Join, exp.From)
				position := -1
				if parent.IsKind(exp.Join) {
					if ss.Node.IsKind(exp.Unnest) && unnestRequiresCrossJoin {
						pushdownAllowed = false
						break
					}
					if p, ok := joinIndex[parent.AliasOrName()]; ok {
						position = p
					}
				}
				if position >= lastNullExtending {
					reachable.Set(k, ss)
				}
			}
			if pushdownAllowed {
				pushdownPredicate(where.This(), reachable, scopeRefCount, d, joinIndex)
			}
		}
		for _, join := range joins {
			name := join.AliasOrName()
			if s := join.TextUpper("side"); s == "RIGHT" || s == "FULL" {
				continue
			}
			if ss, ok := scope.SelectedSources().Get(name); ok {
				sources := NewOrderedMap[SelectedSource]()
				sources.Set(name, ss)
				pushdownPredicate(join.Get("on"), sources, scopeRefCount, d, nil)
			}
		}
	}
	return e
}

func pushdownPredicate(condition *exp.Expr, sources *OrderedMap[SelectedSource], scopeRefCount map[Source]int, d *Dialect, joinIndex map[string]int) {
	if condition == nil {
		return
	}
	condition = condition.Replace(Simplify(condition, d))
	cnfLike := Normalized(condition, false) || !Normalized(condition, true)
	var predicates []*exp.Expr
	kind := exp.Or
	if cnfLike {
		kind = exp.And
	}
	if condition.IsKind(kind) {
		predicates = condition.Flatten(true)
	} else {
		predicates = []*exp.Expr{condition}
	}
	if cnfLike {
		pushdownCNF(predicates, sources, scopeRefCount, joinIndex)
	} else {
		pushdownDNF(predicates, sources, scopeRefCount, joinIndex)
	}
}

func pushdownCNF(predicates []*exp.Expr, sources *OrderedMap[SelectedSource], scopeRefCount map[Source]int, joinIndex map[string]int) {
	for _, predicate := range predicates {
		nodes := nodesForPredicate(predicate, sources, scopeRefCount)
		for _, table := range nodes.Keys() {
			node, _ := nodes.Get(table)
			if node.IsKind(exp.Join) {
				name := node.AliasOrName()
				predicateTables := exp.ColumnTableNames(predicate, name)
				if len(joinIndex) > 0 {
					thisIndex := joinIndex[name]
					all := true
					for t := range predicateTables {
						idx, ok := joinIndex[t]
						if !ok {
							idx = -1
						}
						if idx >= thisIndex {
							all = false
						}
					}
					if all {
						predicate.Replace(exp.NewTrue())
						node.On(predicate, exp.WithCopy(false))
						break
					}
				}
			}
			if node.IsKind(exp.Select) {
				predicate.Replace(exp.NewTrue())
				innerPredicate := replaceAliases(node, predicate)
				if FindInScope(innerPredicate, exp.AggFunc) != nil {
					node.Having(innerPredicate, exp.WithCopy(false))
				} else {
					node.Where(innerPredicate, exp.WithCopy(false))
				}
			}
		}
	}
}

func pushdownDNF(predicates []*exp.Expr, sources *OrderedMap[SelectedSource], scopeRefCount map[Source]int, joinIndex map[string]int) {
	pushdownTables := map[string]bool{}
	for _, a := range predicates {
		aTables := exp.ColumnTableNames(a, "")
		for _, b := range predicates {
			bTables := exp.ColumnTableNames(b, "")
			for t := range aTables {
				if !bTables[t] {
					delete(aTables, t)
				}
			}
		}
		for t := range aTables {
			pushdownTables[t] = true
		}
	}
	conditions := map[string]*exp.Expr{}
	for _, table := range sortedSet(pushdownTables) {
		var tableNode *exp.Expr
		incomplete := false
		for _, predicate := range predicates {
			nodes := nodesForPredicate(predicate, sources, scopeRefCount)
			n, ok := nodes.Get(table)
			if !ok {
				incomplete = true
				break
			}
			tableNode = n
			if c, ok := conditions[table]; ok {
				conditions[table] = exp.NewOr(c, predicate)
			} else {
				conditions[table] = predicate
			}
		}
		if incomplete {
			delete(conditions, table)
			continue
		}
		predicate := conditions[table]
		if tableNode.IsKind(exp.Join) {
			if len(joinIndex) > 0 {
				thisIndex := joinIndex[table]
				predicateTables := exp.ColumnTableNames(predicate, table)
				all := true
				for t := range predicateTables {
					idx, ok := joinIndex[t]
					if !ok {
						idx = -1
					}
					if idx >= thisIndex {
						all = false
					}
				}
				if !all {
					continue
				}
			}
			tableNode.On(predicate, exp.WithCopy(false))
		} else if tableNode.IsKind(exp.Select) {
			innerPredicate := replaceAliases(tableNode, predicate)
			if FindInScope(innerPredicate, exp.AggFunc) != nil {
				tableNode.Having(innerPredicate, exp.WithCopy(false))
			} else {
				tableNode.Where(innerPredicate, exp.WithCopy(false))
			}
		}
	}
}

func nodesForPredicate(predicate *exp.Expr, sources *OrderedMap[SelectedSource], scopeRefCount map[Source]int) *OrderedMap[*exp.Expr] {
	nodes := NewOrderedMap[*exp.Expr]()
	tables := exp.ColumnTableNames(predicate, "")
	whereCondition := predicate.FindAncestor(exp.Join, exp.Where).IsKind(exp.Where)
	for _, table := range sortedSet(tables) {
		ss, ok := sources.Get(table)
		var node *exp.Expr
		var source Source
		if ok {
			node, source = ss.Node, ss.Source
		}
		if node != nil && whereCondition {
			node = node.FindAncestor(exp.Join, exp.From)
		}
		if s, isScope := source.(*Scope); node.IsKind(exp.From) && isScope && s != nil {
			parent := s.Parent
			if parent == nil {
				panic(fmt.Errorf("ValueError: Source node has no parent"))
			}
			if with := parent.Expression.Get("with_"); with != nil && with.Bool("recursive") {
				return NewOrderedMap[*exp.Expr]()
			}
			node = s.Expression
		}
		if node.IsKind(exp.Join) {
			if node.Text("side") != "" {
				var pushable *Scope
				if s, isScope := source.(*Scope); node.TextUpper("side") == "RIGHT" && isScope {
					pushable = s
				}
				if pushable == nil {
					return NewOrderedMap[*exp.Expr]()
				}
				node = pushable.Expression
			} else {
				nodes.Set(table, node)
			}
		}
		if node.IsKind(exp.Select) && len(tables) == 1 {
			hasWindowExpression := false
			for _, sel := range node.Selects() {
				if FindInScope(sel, exp.Window) != nil {
					hasWindowExpression = true
				}
			}
			if !node.Bool("group") && scopeRefCount[source] < 2 && !hasWindowExpression &&
				!node.Bool("limit") && !node.Bool("offset") && !node.Bool("qualify") {
				nodes.Set(table, node)
			}
		}
	}
	return nodes
}

var operatorExpressions = []exp.Kind{exp.Binary, exp.Unary, exp.Predicate}

func replaceAliases(source, predicate *exp.Expr) *exp.Expr {
	aliases := map[string]*exp.Expr{}
	for _, sel := range source.Selects() {
		if sel.IsKind(exp.Alias) {
			aliases[sel.Alias()] = sel.This()
		} else {
			aliases[sel.Name()] = sel
		}
	}
	return predicate.Transform(func(column *exp.Expr) *exp.Expr {
		if column.IsKind(exp.Column) {
			if a, ok := aliases[column.Name()]; ok {
				replaced := a.Copy()
				if replaced.IsAny(operatorExpressions...) && column.Parent().IsAny(operatorExpressions...) {
					replaced = exp.NewParen(replaced, exp.WithCopy(false))
				}
				return replaced
			}
		}
		return column
	}, true)
}
