package sqlglot

// Port of sqlglot/optimizer/pushdown_projections.py.

import (
	"math/big"
	"strconv"

	"github.com/cottrellashley/sqlglot-go/exp"
)

// PushdownProjections removes unused projections and CTEs while preserving all outermost
// outputs. The expression must be qualified, with projection stars expanded. journal may be
// nil.
func PushdownProjections(e *exp.Expr, journal *Journal) *exp.Expr {
	reachability := ProjectionReachabilityOf(e, true)
	PruneProjections(reachability, 0, journal, true)
	return e
}

var groupingConstructs = []exp.Kind{exp.Cube, exp.GroupingSets, exp.Paren, exp.Rollup, exp.Tuple}

type ordinalRef struct {
	node      *exp.Expr // the GROUP BY literal
	selection *exp.Expr // its pre-prune projection
}

// ProjectionReachability records which root outputs reach each scope and output column
// through dependency edges (bit i of a bitset represents root output i).
type ProjectionReachability struct {
	Scopes          []*Scope
	Live            map[*Scope]*big.Int
	Selections      map[*Scope][]*big.Int
	IsAgg           map[*Scope]bool
	GroupByOrdinals map[*Scope][]ordinalRef
	SetNames        map[*Scope][]string
}

type dependencyNode struct {
	requiredBy   *big.Int
	dependencies []*dependencyNode
}

func newDependencyNode() *dependencyNode { return &dependencyNode{requiredBy: new(big.Int)} }

// ProjectionReachabilityOf finds which scopes and projections each outermost output reaches
// through dependencies, without changing the query. With wholeQuery, bit 0 stands for all the
// outputs of the outermost query together.
func ProjectionReachabilityOf(e *exp.Expr, wholeQuery bool) ProjectionReachability {
	if !wholeQuery && !e.Is(exp.Query) {
		optimizeErr("projection_reachability requires a query")
	}
	scopes := TraverseScope(e)
	if len(scopes) == 0 {
		if wholeQuery {
			return ProjectionReachability{
				Live: map[*Scope]*big.Int{}, Selections: map[*Scope][]*big.Int{}, IsAgg: map[*Scope]bool{},
				GroupByOrdinals: map[*Scope][]ordinalRef{}, SetNames: map[*Scope][]string{},
			}
		}
		optimizeErr("projection_reachability requires a query scope")
	}
	if wholeQuery && len(scopes) == 1 {
		scope := scopes[0]
		query := scope.Expression
		if query.IsKind(exp.Select) {
			if query.IsStar() {
				optimizeErr("projection_reachability requires star-free selections")
			}
			windows := query.List("windows")
			sel := make([]*big.Int, len(query.Selects()))
			for i := range sel {
				sel[i] = big.NewInt(1)
			}
			agg := false
			for _, s := range query.Selects() {
				if ProjectionHasAggregate(s, windows) {
					agg = true
				}
			}
			return ProjectionReachability{
				Scopes:          scopes,
				Live:            map[*Scope]*big.Int{scope: big.NewInt(1)},
				Selections:      map[*Scope][]*big.Int{scope: sel},
				IsAgg:           map[*Scope]bool{scope: agg},
				GroupByOrdinals: map[*Scope][]ordinalRef{scope: groupByOrdinalRefs(query)},
				SetNames:        map[*Scope][]string{},
			}
		}
	}

	scopeNodes := map[*Scope]*dependencyNode{}
	outputNames := map[*Scope][]string{}
	outputNodes := map[*Scope][]*dependencyNode{}
	outputsByName := map[*Scope]map[string][]*dependencyNode{}
	owners := map[*exp.Expr]*dependencyNode{}
	scopesByExpression := map[*exp.Expr]*Scope{}
	isAgg := map[*Scope]bool{}
	groupByOrdinals := map[*Scope][]ordinalRef{}

	for _, scope := range scopes {
		query := scope.Expression
		if query.IsKind(exp.Select) && query.IsStar() {
			optimizeErr("projection_reachability requires star-free selections")
		}
		scopesByExpression[query] = scope
		scopeNode := newDependencyNode()
		scopeNodes[scope] = scopeNode
		owners[query] = scopeNode
		if query.Is(exp.SetOperation) {
			left, right := scope.SetOperationScopes[0], scope.SetOperationScopes[1]
			if query.Bool("by_name") {
				outputNames[scope] = dedupStrings(append(append([]string(nil), outputNames[left]...), outputNames[right]...))
			} else {
				outputNames[scope] = outputNames[left]
			}
		} else {
			var names []string
			if query.Is(exp.Selectable) {
				for _, s := range query.Selects() {
					names = append(names, s.AliasOrName())
				}
			}
			outputNames[scope] = names
		}
		outputs := make([]*dependencyNode, len(outputNames[scope]))
		for i := range outputs {
			outputs[i] = newDependencyNode()
		}
		outputNodes[scope] = outputs
		outputsByName[scope] = map[string][]*dependencyNode{}
		for i, name := range outputNames[scope] {
			outputsByName[scope][name] = append(outputsByName[scope][name], outputs[i])
			outputs[i].dependencies = append(outputs[i].dependencies, scopeNode)
		}
		if query.IsKind(exp.Select) {
			for i, s := range query.Selects() {
				if i < len(outputs) {
					owners[s] = outputs[i]
				}
			}
		}
	}

	// owner finds the node whose dependencies include this expression's references: it walks up
	// to a registered projection or scope expression.
	owner := func(node *exp.Expr) *dependencyNode {
		for {
			if o, ok := owners[node]; ok {
				return o
			}
			node = node.Parent()
		}
	}

	for _, scope := range scopes {
		query := scope.Expression
		scopeNode := scopeNodes[scope]
		outputs := outputNodes[scope]
		order := query.Get("order")
		keepAll := query.Bool("distinct") || query.IsAny(exp.Intersect, exp.Except) || isSelfReferencingCte(scope) ||
			!query.IsAny(exp.Select, exp.SetOperation)

		for _, child := range scope.SubqueryScopes {
			anchor := child.Expression.Parent()
			o := owner(anchor)
			o.dependencies = append(o.dependencies, scopeNodes[child])
			o.dependencies = append(o.dependencies, outputNodes[child]...)
		}
		if query.IsKind(exp.Subquery) {
			for _, child := range scope.DerivedTableScopes {
				scopeNode.dependencies = append(scopeNode.dependencies, scopeNodes[child])
				scopeNode.dependencies = append(scopeNode.dependencies, outputNodes[child]...)
			}
		}
		if query.Is(exp.SetOperation) {
			left, right := scope.SetOperationScopes[0], scope.SetOperationScopes[1]
			scopeNode.dependencies = append(scopeNode.dependencies, scopeNodes[left], scopeNodes[right])
			byName := query.Bool("by_name")
			if query.Text("kind") != "" || query.Text("side") != "" || (byName && len(scope.OuterColumns) > 0) {
				keepAll = true
			}
			if !byName && len(outputNodes[left]) != len(outputNodes[right]) {
				optimizeErr("Invalid set operation due to column mismatch: %s.", query.SQL())
			}
			for _, branch := range []*Scope{left, right} {
				for i, outputNode := range outputNodes[branch] {
					var targets []*dependencyNode
					if byName {
						targets = outputsByName[scope][outputNames[branch][i]]
					} else {
						targets = []*dependencyNode{outputs[i]}
					}
					for _, output := range targets {
						output.dependencies = append(output.dependencies, outputNode)
						outputNode.dependencies = append(outputNode.dependencies, output)
					}
				}
			}
		}
		if keepAll {
			scopeNode.dependencies = append(scopeNode.dependencies, outputs...)
		} else if order != nil {
			maxOrdinal := 0
			for _, ordered := range order.Expressions() {
				if o := ordered.This(); o.IsKind(exp.Literal) && o.IsInt() {
					if v, err := o.ToPy(); err == nil {
						if n, ok := v.(int); ok && n > maxOrdinal {
							maxOrdinal = n
						}
					}
				}
			}
			scopeNode.dependencies = append(scopeNode.dependencies, outputs[:min(maxOrdinal, len(outputs))]...)
		}
		for _, name := range sortedSet(outputColumnRefs(query, !query.IsKind(exp.Select))) {
			scopeNode.dependencies = append(scopeNode.dependencies, outputsByName[scope][name]...)
		}
		scopeNode.dependencies = append(scopeNode.dependencies, outputs[:min(len(scope.OuterColumns), len(outputs))]...)

		if query.IsKind(exp.Select) {
			windows := query.List("windows")
			groupAll := IsImplicitGroupByAll(query)
			groupByOrdinals[scope] = groupByOrdinalRefs(query)
			ordinals := map[*exp.Expr]bool{}
			for _, r := range groupByOrdinals[scope] {
				ordinals[r.selection] = true
			}
			var firstAggregate *dependencyNode
			var nonAggregates []*dependencyNode
			hasGroupingKey := false
			for i, selection := range query.Selects() {
				if i >= len(outputs) {
					break
				}
				outputNode := outputs[i]
				aggregate, hasColumn, hasSRF := projectionProperties(selection, windows)
				if aggregate {
					if firstAggregate == nil {
						firstAggregate = outputNode
					}
				} else {
					nonAggregates = append(nonAggregates, outputNode)
				}
				if groupAll && !aggregate && hasColumn {
					hasGroupingKey = true
				}
				if ordinals[selection] || (groupAll && !aggregate) || hasSRF {
					scopeNode.dependencies = append(scopeNode.dependencies, outputNode)
				}
			}
			isAgg[scope] = firstAggregate != nil
			if firstAggregate != nil && (!query.Bool("group") || (groupAll && !hasGroupingKey)) {
				for _, n := range nonAggregates {
					n.dependencies = append(n.dependencies, firstAggregate)
				}
			}
		}

		for _, ref := range scope.References() {
			src, _ := scope.Sources.Get(ref.Name)
			source, ok := src.(*Scope)
			if !ok {
				continue
			}
			source = scopesByExpression[source.Expression]
			scopeNode.dependencies = append(scopeNode.dependencies, scopeNodes[source])
			sourceOutputs := outputNodes[source]
			var first *exp.Expr
			if source.Expression.Is(exp.Selectable) {
				first = seqGet(source.Expression.Selects(), 0)
			}
			if scope.SemiOrAntiJoinTables()[ref.Name] || scope.ScansAllSubscopeColumns() || len(scope.Pivots()) > 0 ||
				first.IsKind(exp.QueryTransform) {
				scopeNode.dependencies = append(scopeNode.dependencies, sourceOutputs...)
			}
			scopeNode.dependencies = append(scopeNode.dependencies, sourceOutputs[:min(len(ref.Node.AliasColumnNames()), len(sourceOutputs))]...)
		}

		for _, col := range scope.Columns() {
			key := col.TableName()
			if key == "" {
				key = col.Name()
			}
			src, _ := scope.Sources.Get(key)
			if source, ok := src.(*Scope); ok {
				source = scopesByExpression[source.Expression]
				o := owner(col)
				if col.TableName() != "" {
					o.dependencies = append(o.dependencies, outputsByName[source][col.Name()]...)
				} else {
					o.dependencies = append(o.dependencies, outputNodes[source]...)
				}
			}
		}
		for _, tableColumn := range scope.TableColumns() {
			src, _ := scope.Sources.Get(tableColumn.Name())
			if source, ok := src.(*Scope); ok {
				source = scopesByExpression[source.Expression]
				o := owner(tableColumn)
				o.dependencies = append(o.dependencies, outputNodes[source]...)
			}
		}
	}

	var roots []*Scope
	if e.Is(exp.Query) {
		roots = []*Scope{scopes[len(scopes)-1]}
	} else {
		for _, scope := range scopes {
			if _, ok := scopeNodes[scope.Parent]; scope.Parent == nil || !ok {
				roots = append(roots, scope)
			}
		}
	}
	var pending []*dependencyNode
	queued := map[*dependencyNode]bool{}
	for _, root := range roots {
		rootOutputs := outputNodes[root]
		allRoots := big.NewInt(1)
		if !wholeQuery {
			allRoots = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(len(rootOutputs))), big.NewInt(1))
		}
		scopeNodes[root].requiredBy = new(big.Int).Set(allRoots)
		for i, outputNode := range rootOutputs {
			if wholeQuery {
				outputNode.requiredBy = new(big.Int).Set(allRoots)
			} else {
				outputNode.requiredBy = new(big.Int).Lsh(big.NewInt(1), uint(i))
			}
		}
		pending = append(pending, scopeNodes[root])
		pending = append(pending, rootOutputs...)
	}
	for _, n := range pending {
		queued[n] = true
	}
	for len(pending) > 0 {
		node := pending[0]
		pending = pending[1:]
		delete(queued, node)
		for _, dependency := range node.dependencies {
			requiredBy := new(big.Int).Or(dependency.requiredBy, node.requiredBy)
			if requiredBy.Cmp(dependency.requiredBy) != 0 {
				dependency.requiredBy = requiredBy
				if !queued[dependency] {
					queued[dependency] = true
					pending = append(pending, dependency)
				}
			}
		}
	}

	r := ProjectionReachability{
		Scopes:          scopes,
		Live:            map[*Scope]*big.Int{},
		Selections:      map[*Scope][]*big.Int{},
		IsAgg:           isAgg,
		GroupByOrdinals: groupByOrdinals,
		SetNames:        map[*Scope][]string{},
	}
	for _, scope := range scopes {
		r.Live[scope] = scopeNodes[scope].requiredBy
		var sel []*big.Int
		for _, n := range outputNodes[scope] {
			sel = append(sel, n.requiredBy)
		}
		r.Selections[scope] = sel
		if scope.Expression.Is(exp.SetOperation) {
			r.SetNames[scope] = scope.Expression.NamedSelects()
		}
	}
	return r
}

func hasBit(v *big.Int, bit uint) bool { return v != nil && v.Bit(int(bit)) == 1 }

// PruneProjections prunes the analyzed tree to the scopes and projections reachable from root
// output index root (0 for reachability computed with wholeQuery).
func PruneProjections(r ProjectionReachability, root int, journal *Journal, removeCtes bool) {
	bit := uint(root)
	for i := len(r.Scopes) - 1; i >= 0; i-- {
		scope := r.Scopes[i]
		if !hasBit(r.Live[scope], bit) {
			if removeCtes && scope.IsCTE() {
				if cteNode := scope.Expression.Parent(); cteNode.IsKind(exp.CTE) {
					withNode := cteNode.Parent()
					if journal != nil && withNode != nil {
						journal.Record(withNode, "expressions")
					}
					cteNode.Pop()
					if withNode != nil && len(withNode.Expressions()) == 0 {
						if journal != nil && withNode.Parent() != nil {
							journal.Record(withNode.Parent(), "with_")
						}
						withNode.Pop()
					}
				}
			}
			continue
		}
		expression := scope.Expression
		if !expression.IsKind(exp.Select) {
			continue
		}
		selects := expression.Selects()
		var subset []*exp.Expr
		for j, selection := range selects {
			if j < len(r.Selections[scope]) && hasBit(r.Selections[scope][j], bit) {
				subset = append(subset, selection)
			}
		}
		if len(subset) == len(selects) {
			continue
		}
		ordinalRefs := r.GroupByOrdinals[scope]
		if len(subset) == 0 {
			placeholder := DefaultSelection(r.IsAgg[scope])
			ancestor := scope
			for ancestor.IsSetOperation() && ancestor.Parent != nil {
				ancestor = ancestor.Parent
				retainedName := ""
				found := false
				for k, name := range r.SetNames[ancestor] {
					if k < len(r.Selections[ancestor]) && hasBit(r.Selections[ancestor][k], bit) {
						retainedName, found = name, true
						break
					}
				}
				if found {
					if r.IsAgg[scope] {
						placeholder.Set("this", exp.New(exp.Max, "this", exp.New(exp.Null)))
					} else {
						placeholder.Set("this", exp.New(exp.Null))
					}
					placeholder.Set("alias", exp.ToIdentifierQuoted(retainedName, true))
					break
				}
			}
			subset = []*exp.Expr{placeholder}
		}
		if journal != nil {
			journal.Record(expression, "expressions")
		}
		expression.Set("expressions", subset)
		if len(ordinalRefs) > 0 {
			newPos := map[*exp.Expr]int{}
			for k, selection := range subset {
				newPos[selection] = k + 1
			}
			for _, ref := range ordinalRefs {
				if pos, ok := newPos[ref.selection]; ok {
					if cur, _ := strconv.Atoi(ref.node.Str("this")); cur != pos {
						if journal != nil {
							journal.Record(ref.node, "this")
						}
						ref.node.Set("this", strconv.Itoa(pos))
					}
				}
			}
		}
	}
}

// projectionProperties reports whether a projection aggregates rows, reads columns, or contains
// a set-returning function.
func projectionProperties(selection *exp.Expr, windows []*exp.Expr) (aggregate, hasColumn, hasSRF bool) {
	hasAggregateOrWindow := false
	kinds := append([]exp.Kind{exp.AggFunc, exp.Window, exp.Column}, setReturningFunctions...)
	for node := range FindAllInScope(selection, kinds...) {
		if node.IsAny(exp.AggFunc, exp.Window) {
			hasAggregateOrWindow = true
		}
		if node.IsKind(exp.Column) {
			hasColumn = true
		}
		if node.IsAny(setReturningFunctions...) {
			hasSRF = true
		}
	}
	aggregate = hasAggregateOrWindow && ProjectionHasAggregate(selection, windows)
	return aggregate, hasColumn, hasSRF
}

func outputColumnRefs(expression *exp.Expr, scoped bool) map[string]bool {
	refs := map[string]bool{}
	for _, arg := range []string{"order", "sort", "distribute", "cluster"} {
		node := expression.Get(arg)
		if node == nil {
			continue
		}
		var columns []*exp.Expr
		if scoped {
			for c := range FindAllInScope(node, exp.Column) {
				columns = append(columns, c)
			}
		} else {
			columns = node.FindAllList(exp.Column)
		}
		for _, c := range columns {
			if c.TableName() == "" {
				refs[c.Name()] = true
			}
		}
	}
	return refs
}

func isSelfReferencingCte(scope *Scope) bool {
	cte := scope.Expression.Parent()
	if !cte.IsKind(exp.CTE) || !cte.Parent().IsKind(exp.With) || !cte.Parent().Recursive() {
		return false
	}
	for _, table := range scope.Expression.FindAllList(exp.Table) {
		if table.DB() == "" && table.Name() == cte.Alias() {
			return true
		}
	}
	return false
}

// DefaultSelection is the placeholder projection of a SELECT whose projections are all pruned.
func DefaultSelection(isAgg bool) *exp.Expr {
	if isAgg {
		return exp.NewAlias(exp.New(exp.Max, "this", exp.LiteralNumber(1)), "_")
	}
	return exp.NewAlias("1", "_")
}

// groupByOrdinalRefs maps each GROUP BY integer ordinal to its pre-prune projection, including
// the ordinals nested in a grouping construct such as GROUPING SETS / CUBE / ROLLUP.
func groupByOrdinalRefs(sel *exp.Expr) []ordinalRef {
	group := sel.Get("group")
	if group == nil {
		return nil
	}
	selects := sel.Selects()
	n := len(selects)
	var refs []ordinalRef
	var collect func(nodes []*exp.Expr)
	collect = func(nodes []*exp.Expr) {
		for _, node := range nodes {
			if node.IsAny(groupingConstructs...) {
				collect(node.IterExpressions())
			} else if node.IsInt() && node.IsKind(exp.Literal) {
				if pos, err := strconv.Atoi(node.Str("this")); err == nil && 1 <= pos && pos <= n {
					refs = append(refs, ordinalRef{node, selects[pos-1]})
				}
			}
		}
	}
	collect(group.IterExpressions())
	return refs
}
