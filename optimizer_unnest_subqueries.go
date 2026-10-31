package sqlglot

// Port of sqlglot/optimizer/unnest_subqueries.py.

import (
	"slices"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// UnnestSubqueries rewrites the AST to convert some predicates with subqueries into joins:
// scalar subqueries become cross joins, and correlated or vectorized subqueries become a group
// by so that they're not a many to many left join.
func UnnestSubqueries(e *exp.Expr) *exp.Expr {
	nextAliasName := helper.NameSequence("_u_")
	for _, scope := range TraverseScope(e) {
		sel := scope.Expression
		parent := sel.ParentSelect()
		if parent == nil {
			continue
		}
		if external := scope.ExternalColumns(); len(external) > 0 {
			if scope.ScopeType != ScopeSetOperation {
				decorrelate(sel, parent, external, nextAliasName)
			}
		} else if scope.ScopeType == ScopeSubquery {
			unnestSubquery(sel, parent, nextAliasName)
		}
	}
	return e
}

func containsExpr(xs []*exp.Expr, x *exp.Expr) bool {
	for _, y := range xs {
		if y.Equal(x) {
			return true
		}
	}
	return false
}

func unnestSubquery(sel, parentSelect *exp.Expr, nextAliasName func() string) {
	if len(sel.Selects()) > 1 {
		return
	}
	predicate := sel.FindAncestor(exp.Condition)
	if predicate == nil ||
		(predicate.Is(exp.Func) && predicate.Parent().IsAny(exp.Table, exp.From, exp.Join)) ||
		parentSelect != predicate.ParentSelect() ||
		!parentSelect.Bool("from_") ||
		(predicate.IsKind(exp.In) && isNegated(predicate)) {
		return
	}
	clause := predicate.FindAncestor(exp.Having, exp.Where, exp.Join, exp.Select)
	if clause.IsKind(exp.Join) {
		k := clause.TextUpper("kind")
		if clause.Parent() != parentSelect || clause.Text("side") != "" || (k != "" && k != "INNER") ||
			clause.Text("method") != "" || clause.Bool("match_condition") || !predicate.IsAny(exp.In, exp.Any) {
			return
		}
	}

	if sel.Is(exp.SetOperation) {
		innerAlias := nextAliasName()
		var projections []any
		for _, s := range sel.Selects() {
			projections = append(projections, exp.NewAlias(exp.NewColumn(s.AliasOrName(), innerAlias), s.AliasOrName()))
		}
		sel = exp.NewSelect(projections...).From(sel.Subquery(innerAlias, true))
	}

	alias := nextAliasName()
	if !predicate.IsAny(exp.In, exp.Any) {
		column := exp.NewColumn(sel.Selects()[0].AliasOrName(), alias)
		var clauseParentSelect *exp.Expr
		if clause != nil {
			clauseParentSelect = clause.ParentSelect()
		}
		anyAgg := false
		for _, s := range parentSelect.Selects() {
			if FindInScope(s, exp.AggFunc) != nil {
				anyAgg = true
			}
		}
		if (clause.IsKind(exp.Having) && clauseParentSelect == parentSelect) ||
			((clause == nil || clauseParentSelect != parentSelect) && (parentSelect.Bool("group") || anyAgg)) {
			if isPostAggregation(sel, parentSelect) {
				column = exp.New(exp.Max, "this", column)
			}
		} else if !sel.Parent().IsKind(exp.Subquery) {
			return
		}
		joinType := "CROSS"
		var onClause any
		if predicate.IsKind(exp.Exists) {
			column = column.IsOp(exp.NewNull()).Not()
			joinType = "LEFT"
			onClause = exp.NewTrue()
		}
		replaceCondition(sel.Parent(), column)
		parentSelect.Join(sel, exp.JoinOptions{On: onClause, JoinType: joinType, JoinAlias: alias}, exp.WithCopy(false))
		return
	}

	if FindInScope(sel, exp.Limit, exp.Offset) != nil {
		return
	}
	if predicate.IsKind(exp.Any) {
		predicate = predicate.FindAncestor(exp.EQ)
		if predicate == nil || parentSelect != predicate.ParentSelect() {
			return
		}
	}
	if clause.IsKind(exp.Join) {
		ancestor := predicate.Parent()
		for ancestor.IsAny(exp.And, exp.Paren) {
			ancestor = ancestor.Parent()
		}
		if ancestor != clause {
			return
		}
		joins := parentSelect.List("joins")
		idx := -1
		for i, j := range joins {
			if j.Equal(clause) {
				idx = i
				break
			}
		}
		for _, join := range joins[idx+1:] {
			if s := join.TextUpper("side"); s == "RIGHT" || s == "FULL" {
				return
			}
		}
	}

	column := otherOperand(predicate)
	value := sel.Selects()[0]
	joinKey := exp.NewColumn(value.Alias(), alias)
	joinKeyNotNull := joinKey.IsOp(exp.NewNull()).Not()
	if clause.IsKind(exp.Join) {
		replaceCondition(predicate, exp.NewTrue())
		parentSelect.Where(joinKeyNotNull, exp.WithCopy(false))
	} else {
		replaceCondition(predicate, joinKeyNotNull)
	}

	if group := sel.Get("group"); group != nil {
		groupExprs := group.Expressions()
		sameSet := true
		for _, g := range groupExprs {
			if !g.Equal(value.This()) {
				sameSet = false
			}
		}
		if len(groupExprs) == 0 || !sameSet {
			sel = exp.NewSelect(exp.NewAlias(exp.NewColumn(value.Alias(), "_q"), value.Alias())).
				From(sel.Subquery("_q", false), exp.WithCopy(false)).
				GroupBy(exp.NewColumn(value.Alias(), "_q"), exp.WithCopy(false))
		}
	} else if FindInScope(value.This(), exp.AggFunc) == nil {
		groupKey := value.This()
		if groupKey != nil && groupKey.IsInt() {
			groupKey = groupKey.Plus(exp.LiteralNumber(0))
		}
		sel = sel.GroupBy(groupKey, exp.WithCopy(false))
	}
	parentSelect.Join(sel, exp.JoinOptions{On: column.Eq(joinKey), JoinType: "LEFT", JoinAlias: alias}, exp.WithCopy(false))
}

type decorrelateKey struct {
	key, column, predicate *exp.Expr
}

func decorrelate(sel, parentSelect *exp.Expr, externalColumns []*exp.Expr, nextAliasName func() string) {
	where := sel.Get("where")
	if where == nil || where.Find(exp.Or) != nil || sel.Find(exp.Limit, exp.Offset, exp.Fetch) != nil {
		return
	}
	parentPredicate := sel.FindAncestor(exp.Predicate)
	if parentPredicate != nil && parentPredicate.ParentSelect() != parentSelect {
		return
	}
	if parentPredicate.IsAny(exp.In, exp.Any, exp.All) &&
		(sel.Bool("group") || sel.Bool("having") || FindInScope(sel, exp.AggFunc) != nil) {
		return
	}

	onClauseJoinIndex := -1
	var onClauseAncestor *exp.Expr
	if parentPredicate != nil {
		onClauseAncestor = parentPredicate.FindAncestor(exp.Join, exp.Select)
	}
	if k := onClauseAncestor.TextUpper("kind"); onClauseAncestor.IsKind(exp.Join) && onClauseAncestor.Parent() == parentSelect &&
		onClauseAncestor.Bool("on") && onClauseAncestor.Text("side") == "" && (k == "" || k == "INNER") &&
		onClauseAncestor.Text("method") == "" && !onClauseAncestor.Bool("match_condition") {
		available := map[string]bool{}
		if parentFrom := parentSelect.Get("from_"); parentFrom != nil {
			available[parentFrom.This().AliasOrName()] = true
		}
		for index, join := range parentSelect.List("joins") {
			if join == onClauseAncestor {
				all := true
				for _, column := range externalColumns {
					if !available[column.TableName()] {
						all = false
					}
				}
				if all {
					onClauseJoinIndex = index
				}
				break
			}
			if !join.IsSemiOrAntiJoin() {
				available[join.AliasOrName()] = true
			}
		}
	}
	if onClauseAncestor.IsKind(exp.Join) && onClauseJoinIndex < 0 {
		return
	}

	if parentPredicate.IsKind(exp.Exists) {
		if sel.Bool("having") || sel.Bool("qualify") {
			return
		}
		group := sel.Get("group")
		allAgg := true
		for _, projection := range sel.Selects() {
			if FindInScope(projection, exp.AggFunc) == nil {
				allAgg = false
			}
		}
		if group == nil || (group.Bool("all") && allAgg) {
			if hasAggregateProjection(sel) {
				replaceCondition(parentPredicate, exp.NewTrue())
				return
			}
		} else if !isPlainGroup(group) {
			return
		}
	}

	if parentSelect.Get("group") != nil && isPostAggregation(sel, parentSelect) {
		return
	}

	tableAlias := nextAliasName()
	var keys []decorrelateKey
	eqCount := 0
	externalIDs := map[*exp.Expr]bool{}
	for _, column := range externalColumns {
		if column.FindAncestor(exp.Where) != where {
			return
		}
		predicate := column.FindAncestor(exp.Predicate)
		var ancestor *exp.Expr
		if predicate != nil {
			ancestor = predicate.Parent()
		}
		for ancestor.IsAny(exp.And, exp.Paren) {
			ancestor = ancestor.Parent()
		}
		if ancestor != where {
			return
		}
		if !predicate.Is(exp.Binary) {
			return
		}
		key := predicate.Expression()
		inLeft := false
		for n := range predicate.This().Walk(true, nil) {
			if n == column {
				inLeft = true
				break
			}
		}
		if !inLeft {
			key = predicate.This()
		}
		keys = append(keys, decorrelateKey{key, column, predicate})
		externalIDs[column] = true
		if predicate.IsKind(exp.EQ) {
			eqCount++
		}
	}
	if eqCount == 0 || (len(keys) > eqCount && !parentPredicate.IsKind(exp.Exists)) {
		return
	}

	isSubqueryProjection := false
	for _, s := range parentSelect.Selects() {
		if node := s.Unalias(); node.IsKind(exp.Subquery) && node == sel.Parent() {
			isSubqueryProjection = true
		}
	}

	value := sel.Selects()[0]
	keyAliases := NewOrderedMap[string]()
	keyExprs := map[string]*exp.Expr{}
	keyID := func(k *exp.Expr) string { mk := k.MapKey(); return mk.Kind.Name() + ":" + strconvU(mk.Hash) }
	var groupBy []*exp.Expr
	for _, k := range keys {
		key, predicate := k.key, k.predicate
		other := predicate.This()
		if key == predicate.This() {
			other = predicate.Expression()
		}
		for _, c := range key.FindAllList(exp.Column) {
			if externalIDs[c] {
				return
			}
		}
		for _, c := range other.FindAllList(exp.Column) {
			if !externalIDs[c] {
				return
			}
		}
		id := keyID(key)
		if key.Equal(value.This()) && predicate.IsKind(exp.EQ) {
			if _, ok := keyExprs[id]; !ok {
				keyExprs[id] = key
			}
			keyAliases.Set(id, value.Alias())
			groupBy = append(groupBy, key)
		} else {
			if !keyAliases.Has(id) {
				keyExprs[id] = key
				keyAliases.Set(id, nextAliasName())
			}
			if predicate.IsKind(exp.EQ) && !containsExpr(groupBy, key) {
				groupBy = append(groupBy, key)
			}
		}
	}
	aliasOf := func(k *exp.Expr) string { a, _ := keyAliases.Get(keyID(k)); return a }

	if parentPredicate == nil && !isSubqueryProjection {
		return
	}
	if parentPredicate.IsKind(exp.In) && isNegated(parentPredicate) {
		return
	}

	aggKind := exp.ArrayAgg
	if isSubqueryProjection {
		aggKind = exp.Max
	}
	if !value.IsKind(exp.Subquery) && FindInScope(value, exp.AggFunc) == nil && !containsExpr(groupBy, value.This()) {
		sel.Select(exp.NewAlias(exp.New(aggKind, "this", value.This()), value.Alias(), exp.WithQuoted(false)),
			exp.WithAppend(false), exp.WithCopy(false))
	}
	if parentPredicate.IsKind(exp.Exists) {
		sel.Set("expressions", []*exp.Expr{})
		sel.Set("group", nil)
		sel.Set("distinct", nil)
		sel.Set("order", nil)
	}
	for _, key := range groupBy {
		if parentPredicate.IsKind(exp.Exists) || !key.Equal(value.This()) {
			sel.Select(exp.NewAlias(key, aliasOf(key)), exp.WithCopy(false))
		}
	}

	var arrayKeys []*exp.Expr
	for _, id := range keyAliases.Keys() {
		if !containsExpr(groupBy, keyExprs[id]) {
			arrayKeys = append(arrayKeys, keyExprs[id])
		}
	}
	useStruct := len(arrayKeys) > 1
	arrayAlias := ""
	if len(arrayKeys) > 0 {
		if useStruct {
			arrayAlias = nextAliasName()
		} else {
			arrayAlias = aliasOf(arrayKeys[0])
		}
		var arrayItem *exp.Expr
		if useStruct {
			var props []*exp.Expr
			for _, key := range arrayKeys {
				props = append(props, exp.New(exp.PropertyEQ, "this", exp.ToIdentifier(aliasOf(key)), "expression", key.Copy()))
			}
			arrayItem = exp.New(exp.Struct, "expressions", props)
		} else {
			arrayItem = arrayKeys[0].Copy()
		}
		sel.Select(exp.NewAlias(exp.New(exp.ArrayAgg, "this", arrayItem), arrayAlias, exp.WithQuoted(false)), exp.WithCopy(false))
	}

	alias := exp.NewColumn(value.Alias(), tableAlias)
	other := otherOperand(parentPredicate)
	var opType exp.Kind
	if parentPredicate != nil {
		opType = parentPredicate.Parent().Kind()
	}

	switch {
	case parentPredicate.IsKind(exp.Exists):
		alias = exp.NewColumn(aliasOf(groupBy[0]), tableAlias)
		parentPredicate = replaceCondition(parentPredicate, "NOT "+alias.SQL()+" IS NULL")
	case parentPredicate.IsKind(exp.All):
		predicate := exp.New(opType, "this", other, "expression", exp.NewColumn("_x"))
		parentPredicate = replaceCondition(parentPredicate.Parent(), "ARRAY_ALL("+alias.SQL()+", _x -> "+predicate.SQL()+")")
	case parentPredicate.IsKind(exp.Any):
		if containsExpr(groupBy, value.This()) {
			predicate := exp.New(opType, "this", other, "expression", alias)
			parentPredicate = replaceCondition(parentPredicate.Parent(), predicate)
		} else {
			predicate := exp.New(opType, "this", other, "expression", exp.NewColumn("_x"))
			parentPredicate = replaceCondition(parentPredicate, "ARRAY_ANY("+alias.SQL()+", _x -> "+predicate.SQL()+")")
		}
	case parentPredicate.IsKind(exp.In):
		if containsExpr(groupBy, value.This()) {
			parentPredicate = replaceCondition(parentPredicate, other.SQL()+" = "+alias.SQL())
		} else {
			parentPredicate = replaceCondition(parentPredicate, "ARRAY_ANY("+alias.SQL()+", _x -> _x = "+parentPredicate.This().SQL()+")")
		}
	default:
		var replacement *exp.Expr = alias
		if FindInScope(value, exp.Count) != nil {
			removeAggs := func(node *exp.Expr) *exp.Expr {
				if node.IsKind(exp.Count) {
					return exp.LiteralNumber(0)
				} else if node.Is(exp.AggFunc) {
					return exp.NewNull()
				}
				return node
			}
			replacement = exp.New(exp.Coalesce, "this", alias, "expressions", []*exp.Expr{value.This().Transform(removeAggs, true)})
		}
		if isSubqueryProjection && sel.Parent().Alias() != "" {
			replacement = exp.NewAlias(replacement, sel.Parent().Alias())
		}
		sel.Parent().Replace(replacement)
	}

	var arrayPredicates []any
	for _, k := range keys {
		key, predicate := k.key, k.predicate
		predicate.Replace(exp.NewTrue())
		if containsExpr(groupBy, key) {
			key.Replace(exp.NewColumn(aliasOf(key), tableAlias))
		} else {
			if useStruct {
				key.Replace(exp.NewColumn(aliasOf(key), "_x"))
			} else {
				key.Replace(exp.ToIdentifier("_x"))
			}
			arrayPredicates = append(arrayPredicates, predicate)
		}
	}
	if len(arrayPredicates) > 0 {
		right := exp.New(exp.ArrayAny,
			"this", exp.NewColumn(arrayAlias, tableAlias),
			"expression", exp.New(exp.Lambda,
				"this", exp.NewAnd(append(arrayPredicates, exp.WithCopy(false))...),
				"expressions", []*exp.Expr{exp.ToIdentifier("_x")}))
		parentPredicate = replaceCondition(parentPredicate, exp.NewParen(exp.NewAnd(parentPredicate.Copy(), right, exp.WithCopy(false))))
	}

	var onPredicates []any
	for _, k := range keys {
		if containsExpr(groupBy, k.key) {
			onPredicates = append(onPredicates, k.predicate)
		}
	}
	var groupArgs []any
	for _, g := range groupBy {
		groupArgs = append(groupArgs, g)
	}
	groupArgs = append(groupArgs, exp.WithCopy(false))
	parentSelect.Join(sel.GroupBy(groupArgs...), exp.JoinOptions{On: onPredicates, JoinType: "LEFT", JoinAlias: tableAlias}, exp.WithCopy(false))

	if onClauseJoinIndex >= 0 {
		joins := slices.Clone(parentSelect.List("joins"))
		last := joins[len(joins)-1]
		joins = joins[:len(joins)-1]
		parentSelect.Set("joins", joins)
		parentSelect.SetAt("joins", last, onClauseJoinIndex, false)
	}
}

func strconvU(u uint64) string {
	const digits = "0123456789abcdef"
	if u == 0 {
		return "0"
	}
	var b []byte
	for u > 0 {
		b = append([]byte{digits[u%16]}, b...)
		u /= 16
	}
	return string(b)
}

func isNegated(e *exp.Expr) bool {
	parent := e.Parent()
	for parent.IsKind(exp.Paren) {
		parent = parent.Parent()
	}
	return parent.IsKind(exp.Not)
}

func replaceCondition(e *exp.Expr, condition any) *exp.Expr {
	return e.Replace(exp.NewCondition(condition))
}

func isPlainGroup(group *exp.Expr) bool {
	for _, arg := range []string{"grouping_sets", "cube", "rollup", "totals"} {
		if group.Bool(arg) {
			return false
		}
	}
	for _, e := range group.Expressions() {
		if e.IsAny(exp.Rollup, exp.Cube, exp.GroupingSets) || (e.IsKind(exp.Tuple) && len(e.Expressions()) == 0) {
			return false
		}
	}
	return true
}

func isPostAggregation(expression, sel *exp.Expr) bool {
	ancestor := expression.FindAncestor(exp.AggFunc, exp.Filter, exp.Select)
	if ancestor.IsKind(exp.Filter) {
		ancestor = ancestor.This().Unnest()
	}
	if ancestor.Is(exp.AggFunc) {
		for ancestor.Parent() != sel && ancestor.Parent() != nil && ancestor.ArgKey() == "this" &&
			!ancestor.Parent().IsAny(exp.Func, exp.Window) {
			ancestor = ancestor.Parent()
		}
		if !(ancestor.ArgKey() == "this" && ancestor.Parent().IsKind(exp.Window)) {
			return false
		}
	}
	node := expression
	for node != nil && node.Parent() != sel {
		node = node.Parent()
	}
	if node == nil {
		return false
	}
	switch node.ArgKey() {
	case "with_", "from_", "laterals", "joins", "where":
		return false
	}
	return true
}

func hasAggregateProjection(sel *exp.Expr) bool {
	windows := sel.List("windows")
	for _, projection := range sel.Selects() {
		if ProjectionHasAggregate(projection, windows) {
			return true
		}
	}
	return false
}

func otherOperand(e *exp.Expr) *exp.Expr {
	switch {
	case e.IsKind(exp.In):
		return e.This()
	case e.IsAny(exp.Any, exp.All):
		return otherOperand(e.Parent())
	case e.Is(exp.Binary):
		if e.This().IsAny(exp.Subquery, exp.Any, exp.Exists, exp.All) {
			return e.Expression()
		}
		return e.This()
	}
	return nil
}
