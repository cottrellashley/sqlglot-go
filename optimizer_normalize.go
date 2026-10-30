package sqlglot

// Ports of sqlglot/optimizer/normalize.py and sqlglot/optimizer/normalize_identifiers.py.

import (
	"fmt"
	"slices"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// Normalize mirrors normalize.normalize(expression, dnf=False, max_distance=128): it rewrites
// connectors into conjunctive (or, with dnf, disjunctive) normal form.
func Normalize(e *exp.Expr, dnf bool, maxDistance int) *exp.Expr {
	simplifier := NewSimplifier(nil, false)
	nodes := slices.Collect(e.Walk(true, func(n *exp.Expr) bool { return n.Is(exp.Connector) }))
	for _, node := range nodes {
		if !node.Is(exp.Connector) {
			continue
		}
		if Normalized(node, dnf) {
			continue
		}
		root := node == e
		original := node.Copy()

		node.Transform(simplifier.RewriteBetween, false)
		distance := NormalizationDistance(node, dnf, maxDistance)
		if distance > maxDistance {
			Logger.Info(fmt.Sprintf("Skipping normalization because distance %d exceeds max %d", distance, maxDistance))
			return e
		}

		var failed error
		var normalized *exp.Expr
		func() {
			defer func() {
				if r := recover(); r != nil {
					if oe, ok := r.(*errs.OptimizeError); ok {
						failed = oe
						return
					}
					panic(r)
				}
			}()
			normalized = whileChanging(node, func(x *exp.Expr) *exp.Expr {
				return DistributiveLaw(x, dnf, maxDistance, simplifier)
			})
		}()
		if failed != nil {
			Logger.Info(failed.Error())
			node.Replace(original)
			if root {
				return original
			}
			return e
		}
		node = node.Replace(normalized)
		if root {
			e = node
		}
	}
	return e
}

// Normalized mirrors normalize.normalized: whether the expression is in CNF (or DNF).
func Normalized(e *exp.Expr, dnf bool) bool {
	ancestor, root := exp.Or, exp.And
	if dnf {
		ancestor, root = exp.And, exp.Or
	}
	for connector := range FindAllInScope(e, root) {
		if connector.FindAncestor(ancestor) != nil {
			return false
		}
	}
	return true
}

// NormalizationDistance mirrors normalize.normalization_distance: the estimated number of
// conjunctions (or disjunctions) the normal form would contain.
func NormalizationDistance(e *exp.Expr, dnf bool, max int) int {
	total := -(len(e.FindAllList(exp.Connector)) + 1)
	stop := false
	predicateLengths(e, dnf, max, 0, func(length int) bool {
		total += length
		if total > max {
			stop = true
			return false
		}
		return true
	})
	_ = stop
	return total
}

// predicateLengths mirrors normalize._predicate_lengths (a generator, here with a yield func
// returning false to stop).
func predicateLengths(e *exp.Expr, dnf bool, max, depth int, yield func(int) bool) bool {
	if depth > max {
		return yield(depth)
	}
	e = e.Unnest()
	if !e.Is(exp.Connector) {
		return yield(1)
	}
	depth++
	left, right := e.Left(), e.Right()
	kind := exp.Or
	if dnf {
		kind = exp.And
	}
	if e.Is(kind) {
		return predicateLengths(left, dnf, max, depth, func(a int) bool {
			return predicateLengths(right, dnf, max, depth, func(b int) bool { return yield(a + b) })
		})
	}
	if !predicateLengths(left, dnf, max, depth, yield) {
		return false
	}
	return predicateLengths(right, dnf, max, depth, yield)
}

// DistributiveLaw mirrors normalize.distributive_law: x OR (y AND z) -> (x OR y) AND (x OR z).
func DistributiveLaw(e *exp.Expr, dnf bool, maxDistance int, simplifier *Simplifier) *exp.Expr {
	if Normalized(e, dnf) {
		return e
	}
	distance := NormalizationDistance(e, dnf, maxDistance)
	if distance > maxDistance {
		panic(&errs.OptimizeError{Message: fmt.Sprintf("Normalization distance %d exceeds max %d", distance, maxDistance)})
	}
	exp.ReplaceChildren(e, func(x *exp.Expr) []*exp.Expr {
		return []*exp.Expr{DistributiveLaw(x, dnf, maxDistance, nil)}
	})
	toExp, fromExp := exp.And, exp.Or
	if dnf {
		toExp, fromExp = exp.Or, exp.And
	}
	if e.Is(fromExp) {
		operands := e.UnnestOperands()
		a, b := operands[0], operands[1]
		fromFunc := iff(fromExp == exp.And, exp.NewAnd, exp.NewOr)
		toFunc := iff(toExp == exp.And, exp.NewAnd, exp.NewOr)
		if simplifier == nil {
			simplifier = NewSimplifier(nil, false)
		}
		if a.Is(toExp) && b.Is(toExp) {
			if len(a.FindAllList(exp.Connector)) > len(b.FindAllList(exp.Connector)) {
				return distribute(a, b, fromFunc, toFunc, simplifier)
			}
			return distribute(b, a, fromFunc, toFunc, simplifier)
		}
		if a.Is(toExp) {
			return distribute(b, a, fromFunc, toFunc, simplifier)
		}
		if b.Is(toExp) {
			return distribute(a, b, fromFunc, toFunc, simplifier)
		}
	}
	return e
}

func distribute(a, b *exp.Expr, fromFunc, toFunc func(args ...any) *exp.Expr, simplifier *Simplifier) *exp.Expr {
	// When `a` and `b` are connectors of the SAME polarity, `b` is distributed across `a`'s
	// children so the existing cross-product handling can simplify them.
	if a.Is(exp.Connector) && a.Is(b.Kind()) {
		exp.ReplaceChildren(a, func(c *exp.Expr) []*exp.Expr {
			return []*exp.Expr{toFunc(
				simplifier.UniqSort(SimplifyFlatten(fromFunc(c, b.Left())), true),
				simplifier.UniqSort(SimplifyFlatten(fromFunc(c, b.Right())), true),
				exp.WithCopy(false),
			)}
		})
		return a
	}
	// Otherwise apply the textbook rule a OR (b.left AND b.right) -> (a OR b.left) AND (a OR b.right)
	return toFunc(
		simplifier.UniqSort(SimplifyFlatten(fromFunc(a, b.Left())), true),
		simplifier.UniqSort(SimplifyFlatten(fromFunc(a, b.Right())), true),
		exp.WithCopy(false),
	)
}

// NormalizeIdentifiers mirrors normalize_identifiers.normalize_identifiers(expression,
// dialect=None): it normalizes identifiers according to the dialect's normalization strategy.
// expression may be a string (parsed as an identifier) or an expression.
func NormalizeIdentifiers(expression any, dialect any) *exp.Expr {
	return NormalizeIdentifiersOpts(expression, dialect, false)
}

// NormalizeIdentifiersOpts is NormalizeIdentifiers with store_original_column_identifiers.
func NormalizeIdentifiersOpts(expression any, dialect any, storeOriginalColumnIdentifiers bool) *exp.Expr {
	d := MustDialect(dialect)
	var e *exp.Expr
	switch x := expression.(type) {
	case string:
		e = exp.ParseIdentifier(x, d)
	case *exp.Expr:
		e = x
	}
	prune := func(n *exp.Expr) bool { return exp.Truthy(n.MetaGet("case_sensitive")) }
	for node := range e.Walk(true, prune) {
		if exp.Truthy(node.MetaGet("case_sensitive")) {
			continue
		}
		// A dot chain is recorded once, at its outermost Dot. Ancestors are visited before
		// their descendants, so none of the names it wraps have been normalized yet
		if storeOriginalColumnIdentifiers && node.IsAny(exp.Column, exp.Dot) && !node.Parent().IsKind(exp.Dot) {
			if node.IsKind(exp.Column) {
				var parts []string
				for _, p := range node.Parts() {
					parts = append(parts, p.Name())
				}
				node.Meta()["dot_parts"] = parts
			} else if !node.IsStar() {
				root := node
				var dotParts []string
				for root.IsKind(exp.Dot) {
					dotParts = append(dotParts, root.Expression().Name())
					root = root.This()
				}
				slices.Reverse(dotParts)
				// The chain may be rooted at a column (j.k), or at an arbitrary expression (f().k)
				if root.IsKind(exp.Column) {
					var parts []string
					for _, p := range root.Parts() {
						parts = append(parts, p.Name())
					}
					dotParts = append(parts, dotParts...)
				}
				root.Meta()["dot_parts"] = dotParts
			}
		}
		if node.IsKind(exp.Identifier) {
			d.NormalizeIdentifier(node)
		}
	}
	return e
}
