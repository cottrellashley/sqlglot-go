package sqlglot

import (
	"fmt"
	"slices"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
)

// ApplyIndexOffset mirrors exp.apply_index_offset: it shifts a single integer array index by
// offset when `this` is (or may be) an array.
func ApplyIndexOffset(this *exp.Expr, expressions []*exp.Expr, offset int, d *Dialect) []*exp.Expr {
	if offset == 0 || len(expressions) != 1 {
		return expressions
	}
	expression := expressions[0]
	if this.Type() == nil {
		AnnotateTypes(this, d)
	}
	if t := this.Type().DataTypeThis(); t != exp.DTypeUnknown && t != exp.DTypeArray {
		return expressions
	}
	if expression.Type() == nil {
		AnnotateTypes(expression, d)
	}
	if exp.DataTypeIntegerTypes.Has(expression.Type().DataTypeThis()) {
		Logger.Info(fmt.Sprintf("Applying array index offset (%d)", offset))
		expression = Simplify(expression.Plus(offset), nil)
		return []*exp.Expr{expression}
	}
	return expressions
}

// RenameTable mirrors exp.rename_table: it builds an ALTER TABLE ... RENAME TO ... expression.
func RenameTable(oldName, newName any, dialect any) *exp.Expr {
	return exp.New(exp.Alter,
		"this", exp.ToTable(oldName, dialect),
		"kind", "TABLE",
		"actions", []*exp.Expr{exp.New(exp.AlterRename, "this", exp.ToTable(newName, dialect))},
	)
}

// RenameColumn mirrors exp.rename_column: it builds an ALTER TABLE ... RENAME COLUMN ...
// expression. exists adds IF EXISTS when non-nil and true.
func RenameColumn(tableName, oldColumnName, newColumnName any, exists *bool, dialect any) *exp.Expr {
	var ex any
	if exists != nil {
		ex = *exists
	}
	return exp.New(exp.Alter,
		"this", exp.ToTable(tableName, dialect),
		"kind", "TABLE",
		"actions", []*exp.Expr{exp.New(exp.RenameColumn,
			"this", exp.ToColumn(oldColumnName, nil, dialect),
			"to", exp.ToColumn(newColumnName, nil, dialect),
			"exists", ex,
		)},
	)
}

// FindTables mirrors exp.find_tables: the tables referenced in a query, excluding CTE
// references.
func FindTables(expression *exp.Expr) []*exp.Expr {
	var out []*exp.Expr
	seen := map[*exp.Expr]bool{}
	for _, scope := range TraverseScope(expression) {
		for _, table := range scope.Tables() {
			if table.Is(exp.Table) && table.Name() != "" && !scope.CteSources.Has(table.Name()) && !seen[table] {
				seen[table] = true
				out = append(out, table)
			}
		}
	}
	return out
}

// NormalizeTableName mirrors exp.normalize_table_name: the case normalized table name,
// without quotes.
func NormalizeTableName(table any, dialect any, copy bool) string {
	t := exp.ToTableNoCopy(table, dialect)
	if copy {
		t = t.Copy()
	}
	var parts []string
	for _, p := range NormalizeIdentifiers(t, dialect).Parts() {
		parts = append(parts, p.Name())
	}
	return strings.Join(parts, ".")
}

// ReplaceTables mirrors exp.replace_tables: it replaces the tables of an expression according
// to mapping (table name -> new table name), annotating each replacement with the original
// name as a comment.
func ReplaceTables(expression *exp.Expr, mapping map[string]string, dialect any, copy bool) *exp.Expr {
	normalized := map[string]string{}
	for k, v := range mapping {
		normalized[NormalizeTableName(k, dialect, true)] = v
	}
	return expression.Transform(func(node *exp.Expr) *exp.Expr {
		if node.Is(exp.Table) && node.MetaGet("replace") != false {
			original := NormalizeTableName(node, dialect, true)
			if newName := normalized[original]; newName != "" {
				var kv []any
				for _, a := range node.Args() {
					if !slices.Contains(exp.TableParts, a.Key) {
						kv = append(kv, a.Key, a.Value)
					}
				}
				table := exp.ToTable(newName, dialect, kv...)
				table.AddComments([]string{original}, false)
				return table
			}
		}
		return node
	}, copy)
}

// ExpandSource provides the query that Expand substitutes for a table: a query expression, or
// a function returning one (evaluated on demand).
type ExpandSource = any

// Expand mirrors exp.expand: it transforms an expression by expanding all referenced sources
// into subqueries.
func Expand(expression *exp.Expr, sources map[string]ExpandSource, dialect any, copy bool) *exp.Expr {
	normalized := map[string]ExpandSource{}
	for k, v := range sources {
		normalized[NormalizeTableName(k, dialect, true)] = v
	}
	var expand func(node *exp.Expr) *exp.Expr
	expand = func(node *exp.Expr) *exp.Expr {
		if node.Is(exp.Table) {
			name := NormalizeTableName(node, dialect, true)
			var source *exp.Expr
			switch s := normalized[name].(type) {
			case *exp.Expr:
				source = s
			case func() *exp.Expr:
				source = s()
			}
			if source != nil {
				alias := node.Alias()
				if alias == "" {
					alias = name
				}
				subquery := source.Subquery(alias, true)
				subquery.Comments = []string{"source: " + name}
				return subquery.Transform(expand, false)
			}
		}
		return node
	}
	return expression.Transform(expand, copy)
}
