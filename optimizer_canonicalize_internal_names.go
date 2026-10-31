package sqlglot

// Port of sqlglot/optimizer/canonicalize_internal_names.py.

import (
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// CanonicalizeInternalNames rewrites a query to a canonical structural form.
//
// It preserves data-contract names (base-table names and columns, top-level output aliases)
// and canonicalizes internal names (table aliases, CTE/subquery names, internal column
// aliases) to sequential _tN / _cN. For set operations the top-level output is the leftmost
// leaf SELECT.
//
// The expression must already have been processed by Qualify (or equivalent), so that columns
// are bound to sources and aliases exist on every projection / subquery / CTE.
func CanonicalizeInternalNames(expression *exp.Expr) *exp.Expr {
	// Skip non-queries for now (e.g., UPDATE ... SET x = s.x FROM (SELECT ...) AS s)
	if !expression.Is(exp.Query) {
		return expression
	}

	// Top-level output scopes: their aliases are the query's data contract. Regular UNION takes
	// names from the left branch; UNION BY NAME takes names from the union of all branches, so
	// both sides of a by_name SetOperation contribute.
	outputScopeExprs := map[*exp.Expr]bool{}
	stack := []*exp.Expr{expression}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if node.Is(exp.SetOperation) {
			// Access the args directly instead of the left/right properties, because set
			// operation operands aren't guaranteed to be Query nodes, e.g. in
			// VALUES (1) UNION ALL SELECT 1
			stack = append(stack, node.This().Unnest())
			if node.Bool("by_name") {
				stack = append(stack, node.Expression().Unnest())
			}
		} else {
			outputScopeExprs[node] = true
		}
	}

	nextTable := helper.NameSequence("_t")
	nextColumn := helper.NameSequence("_c")

	canon := func(ident *exp.Expr, name string) {
		ident.Set("this", name)
		ident.Set("quoted", true)
	}

	// Keyed by the source's Table / Scope or by its alias holder.
	scopeTable := map[any]string{}
	scopeOutputs := map[*exp.Expr]*OrderedMap[string]{}
	outputsOf := func(e *exp.Expr) *OrderedMap[string] {
		if m, ok := scopeOutputs[e]; ok {
			return m
		}
		return NewOrderedMap[string]()
	}

	// Shared across scopes so a Table referenced from multiple scopes (LATERAL/UNNEST) gets
	// consistent column names
	tableColumns := map[*exp.Expr]map[string]string{}

	// Shared across UDTF sources so chained correlated UNNESTs get consistent names for their
	// alias columns
	udtfColumns := map[*exp.Expr]map[string]string{}

	for _, scope := range TraverseScope(expression) {
		scopeExpr := scope.Expression
		isOutputScope := outputScopeExprs[scopeExpr]

		columnsBySource := map[string][]*exp.Expr{}
		for _, col := range scope.Columns() {
			columnsBySource[col.TableName()] = append(columnsBySource[col.TableName()], col)
		}
		for _, tableCol := range scope.TableColumns() {
			columnsBySource[tableCol.Name()] = append(columnsBySource[tableCol.Name()], tableCol)
		}

		// Include qualified stars (e.g. `t.*`)
		for _, star := range scope.Stars() {
			if star.IsKind(exp.Column) && star.TableName() != "" {
				columnsBySource[star.TableName()] = append(columnsBySource[star.TableName()], star)
			}
		}

		// The first value is the source's canonical name (used in WITH "_tN" AS, and as the
		// physical table name in Table.this), shared across every reference to the same source
		// within this scope. The second is the per-reference canonical alias (used as the alias
		// on each Table node and as the qualifier on its column refs). The first reference reuses
		// the source's canonical name for compactness; subsequent refs get fresh _tN names so a
		// self-join like `cte AS x JOIN cte AS y` produces unique aliases.
		tableMap := map[string][2]string{}
		refCanonTaken := map[string]bool{}

		for sourceName, source := range scope.Sources.All() {
			sourceCols := columnsBySource[sourceName]

			var aliasHolder *exp.Expr
			isBaseSource := false
			var canonT string
			var childOutput *OrderedMap[string]
			var nameMap map[string]string
			var src *exp.Expr

			switch s := source.(type) {
			case *exp.Expr:
				if !s.Is(exp.Table) {
					continue
				}
				isBaseSource = true
				canonT = scopeTable[s]
				if canonT == "" {
					canonT = nextTable()
					scopeTable[s] = canonT
				}
				childOutput = NewOrderedMap[string]()
				if tableColumns[s] == nil {
					tableColumns[s] = map[string]string{}
				}
				nameMap = tableColumns[s]
				src = s.Expression()
			case *Scope:
				// Key by the expression so a recursive CTE's self-reference, i.e., a separate
				// Scope whose expression is the same object as the CTE's base branch, shares the
				// base branch's output mapping.
				srcExpr := s.Expression
				childOutput = outputsOf(srcExpr)

				parent := srcExpr.Parent()
				if parent.IsAny(exp.CTE, exp.Subquery) {
					aliasHolder = parent
				} else if cte := parent.Parent(); parent.Is(exp.SetOperation) && cte.IsKind(exp.CTE) &&
					cte.Parent().IsKind(exp.With) && cte.Parent().Bool("recursive") {
					aliasHolder = cte
				} else if s.IsUDTF() {
					aliasHolder = srcExpr
				}

				// A UDTF (unlike a CTE/subquery) can be a source in multiple scopes, so keep its
				// alias holder and share its column map across them
				isUDTFSource := aliasHolder != nil && aliasHolder == srcExpr

				var tableKey any = s
				if aliasHolder != nil {
					tableKey = aliasHolder
				}
				canonT = scopeTable[tableKey]
				if canonT == "" {
					canonT = nextTable()
					scopeTable[tableKey] = canonT
				} else if !isUDTFSource {
					aliasHolder = nil // already renamed on a previous encounter
				}

				if isUDTFSource {
					if udtfColumns[srcExpr] == nil {
						udtfColumns[srcExpr] = map[string]string{}
					}
					nameMap = udtfColumns[srcExpr]
				} else {
					nameMap = map[string]string{}
				}
				src = srcExpr
			default:
				continue
			}

			refAlias := canonT
			if refCanonTaken[canonT] {
				refAlias = nextTable()
			} else {
				refCanonTaken[canonT] = true
			}
			tableMap[sourceName] = [2]string{canonT, refAlias}

			// UNNEST struct fields are physical columns (preserve); the element alias is not
			structFieldNames := map[string]bool{}
			if src.IsKind(exp.Unnest) && len(src.Expressions()) > 0 && src.Expressions()[0].Type() != nil {
				if elementTypes := src.Expressions()[0].Type().Expressions(); len(elementTypes) > 0 &&
					elementTypes[0] != nil && elementTypes[0].IsType(exp.DTypeStruct) {
					for _, cd := range elementTypes[0].Expressions() {
						structFieldNames[cd.Name()] = true
					}
				}
			}

			for _, srcCol := range sourceCols {
				// BigQuery whole-row struct ref (`SELECT t FROM t`): the identifier IS the table
				// alias, so rename it to this reference's alias.
				if srcCol.IsKind(exp.TableColumn) {
					canon(srcCol.This(), refAlias)
					continue
				}

				oldName := srcCol.Name()
				preserveCol := isBaseSource || structFieldNames[oldName]
				canonCol, ok := nameMap[oldName]
				if !ok {
					if preserveCol {
						canonCol = oldName
					} else if c, _ := childOutput.Get(oldName); c != "" {
						canonCol = c
					} else {
						canonCol = nextColumn()
					}
					nameMap[oldName] = canonCol
				}

				// Base-table and UNNEST struct-field column refs name physical columns => preserve
				// verbatim (including quote state). Scope-sourced refs are internal handles
				// pointing at CTE/subquery aliases (injected unquoted via exp.to_identifier), so
				// canonicalize them.
				if !preserveCol {
					canon(srcCol.This(), canonCol)
				}

				if tableID := srcCol.Get("table"); tableID != nil {
					canon(tableID, refAlias)
				}
			}

			if aliasHolder != nil {
				if alias := aliasHolder.Get("alias"); alias != nil {
					if alias.This().IsKind(exp.Identifier) {
						// The CTE/Subquery name itself uses the source's canonical name, not
						// the reference alias: it's the source's identity, not a reference.
						canon(alias.This(), canonT)
					}
					if columns := alias.Columns(); len(columns) > 0 {
						renamed := make([]*exp.Expr, len(columns))
						for i, c := range columns {
							name, ok := nameMap[c.Name()]
							if !ok {
								name = c.Name()
							}
							renamed[i] = exp.ToIdentifierQuoted(name, c.Quoted())
						}
						alias.Set("columns", renamed)
					}
				}

				// UNNEST stores its offset column outside the alias, including under LATERAL.
				unnest := aliasHolder
				if aliasHolder.IsKind(exp.Lateral) {
					unnest = aliasHolder.This()
				}
				if unnest.IsKind(exp.Unnest) {
					if offsetID := unnest.Get("offset"); offsetID.IsKind(exp.Identifier) {
						if name, ok := nameMap[offsetID.Name()]; ok {
							canon(offsetID, name)
						}
					}
				}
			}
		}

		// PIVOT output column names are bound to the IN-clause literals, so only the alias
		// (which qualifies column refs like `my_pivot.x`) is canonicalized.
		for _, pivot := range scope.Pivots() {
			pivotAlias := pivot.Get("alias")
			if pivotAlias == nil || !pivotAlias.This().IsKind(exp.Identifier) {
				continue
			}
			pivotCols := columnsBySource[pivotAlias.This().Name()]
			if len(pivotCols) == 0 {
				continue
			}
			canonT := nextTable()
			canon(pivotAlias.This(), canonT)
			for _, pivotCol := range pivotCols {
				if tableID := pivotCol.Get("table"); tableID != nil {
					canon(tableID, canonT)
				}
			}
		}

		for _, table := range scope.Tables() {
			entry, ok := tableMap[table.AliasOrName()]
			if !ok {
				continue
			}
			sourceCanon, refAlias := entry[0], entry[1]

			// Real tables (qualified with db/catalog) keep their physical name; CTE/subquery
			// references get the source's canonical _tN.
			if table.This().IsKind(exp.Identifier) && !table.Bool("db") {
				canon(table.This(), sourceCanon)
			}

			if alias := table.Get("alias"); alias != nil {
				// The reference alias is unique per Table node so multiple references to the
				// same source can coexist.
				if alias.This().IsKind(exp.Identifier) {
					canon(alias.This(), refAlias)
				}
				if columns := alias.Columns(); len(columns) > 0 {
					tc := tableColumns[table]
					renamed := make([]*exp.Expr, len(columns))
					for i, c := range columns {
						name, ok := tc[c.Name()]
						if !ok {
							name = c.Name()
						}
						renamed[i] = exp.ToIdentifierQuoted(name, c.Quoted())
					}
					alias.Set("columns", renamed)
				}
			}
		}

		outputMap := NewOrderedMap[string]()
		if scopeExpr.IsKind(exp.Select) {
			for _, sel := range scopeExpr.Selects() {
				// Sets default name for subquery projections, both aliased and unaliased
				if sel.IsAny(exp.Alias, exp.Subquery) && sel.Alias() != "" {
					oldAlias := sel.Alias()
					newName := oldAlias
					if !isOutputScope {
						newName = nextColumn()
						sel.Set("alias", exp.ToIdentifierQuoted(newName, true))
					}
					outputMap.Set(oldAlias, newName)
				}
			}
		} else if scopeExpr.Is(exp.SetOperation) && len(scope.SetOperationScopes) > 0 {
			// Regular UNION names come from the left branch. UNION BY NAME folds in
			// right-branch names too (any column unique to the right still appears in the
			// output).
			outputMap = outputsOf(scope.SetOperationScopes[0].Expression).Copy()
			if scopeExpr.Bool("by_name") {
				for k, v := range outputsOf(scope.SetOperationScopes[1].Expression).All() {
					if !outputMap.Has(k) {
						outputMap.Set(k, v)
					}
				}
			}
		} else if scope.IsUDTF() && len(scope.SubqueryScopes) > 0 {
			outputMap = outputsOf(scope.SubqueryScopes[0].Expression).Copy()
		}

		scopeOutputs[scopeExpr] = outputMap

		// Unqualified alias references (ORDER BY a, HAVING a > 5, ...)
		for col := range FindAllInScope(scopeExpr, exp.Column) {
			if col.TableName() == "" {
				if name, ok := outputMap.Get(col.Name()); ok {
					canon(col.This(), name)
				}
			}
		}

		// UBN matches branches by original alias. When both branches are internal and aliased
		// to distinct _cN, matching originals land on different slots and UBN splits them into
		// disjoint output columns, dropping data. Align the right branch's canonicals to the
		// left's. No-op when both branches preserved their aliases (top-level UBN).
		if scopeExpr.Is(exp.SetOperation) && scopeExpr.Bool("by_name") {
			leftScope, rightScope := scope.SetOperationScopes[0], scope.SetOperationScopes[1]
			leftOut := outputsOf(leftScope.Expression)
			rightOut := outputsOf(rightScope.Expression)

			rename := map[string]string{}
			for origName, leftCanon := range leftOut.All() {
				if rightCanon, _ := rightOut.Get(origName); rightCanon != "" && rightCanon != leftCanon {
					rename[rightCanon] = leftCanon
				}
			}

			if len(rename) > 0 {
				renameStack := []*exp.Expr{rightScope.Expression}
				for len(renameStack) > 0 {
					node := renameStack[len(renameStack)-1]
					renameStack = renameStack[:len(renameStack)-1]
					if node.IsKind(exp.Select) {
						for _, sel := range node.Selects() {
							if sel.IsKind(exp.Alias) {
								if aid := sel.Get("alias"); aid.IsKind(exp.Identifier) {
									if name, ok := rename[aid.Name()]; ok {
										canon(aid, name)
									}
								}
							}
						}
					} else if node.Is(exp.SetOperation) {
						renameStack = append(renameStack, node.This().Unnest(), node.Expression().Unnest())
					} else {
						continue
					}

					for col := range FindAllInScope(node, exp.Column) {
						if col.TableName() == "" {
							if name, ok := rename[col.Name()]; ok {
								canon(col.This(), name)
							}
						}
					}
				}

				renamed := NewOrderedMap[string]()
				for k, v := range rightOut.All() {
					if r, ok := rename[v]; ok {
						v = r
					}
					renamed.Set(k, v)
				}
				scopeOutputs[rightScope.Expression] = renamed
			}
		}
	}

	return expression
}
