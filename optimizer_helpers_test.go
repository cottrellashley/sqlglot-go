package sqlglot_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/internal/testutil"
)

// optimizerSchema mirrors TestOptimizer.setUp's schema.
func optimizerSchema() *sqlglot.SchemaMapping {
	nn := func() *exp.Expr { return exp.MustDataTypeBuild("INT", nil, false, "nullable", false) }
	M := sqlglot.M
	return M(
		"x", M("a", "INT", "b", "INT"),
		// non-nullable columns, for rewrites that are only valid when a value can't be NULL
		"nn", M("a", nn(), "b", nn()),
		"y", M("b", "INT", "c", "INT"),
		"z", M("b", "INT", "c", "INT"),
		"w", M("d", "TEXT", "e", "TEXT"),
		"temporal", M("d", "DATE", "t", "DATETIME"),
		"structs", M(
			"one", "STRUCT<a_1 INT, b_1 VARCHAR>",
			"nested_0", "STRUCT<a_1 INT, nested_1 STRUCT<a_2 INT, nested_2 STRUCT<a_3 INT>>>",
			"quoted", `STRUCT<"foo bar" INT>`,
		),
		"t_bool", M("a", "BOOLEAN", "b", "BOOLEAN"),
		"comparisons", M("a", "INT", "b", "INT", "c", "BOOLEAN"),
		"unpivotable", M("id", "INT", "jan", "INT", "feb", "INT", "north", "INT", "south", "INT"),
		"pivotable", M("id", "INT", "cat", "TEXT", "val", "INT", "kind", "TEXT", "amt", "INT"),
	)
}

// jsonSchema converts a JSON object (in key order) into a SchemaMapping.
func jsonSchema(t *testing.T, s string) *sqlglot.SchemaMapping {
	dec := json.NewDecoder(strings.NewReader(s))
	var parse func() any
	parse = func() any {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("bad schema json %q: %v", s, err)
		}
		if d, ok := tok.(json.Delim); ok && d == '{' {
			m := sqlglot.NewOrderedMap[any]()
			for dec.More() {
				k, _ := dec.Token()
				m.Set(fmt.Sprint(k), parse())
			}
			dec.Token()
			return m
		} else if ok && d == '[' {
			var xs []any
			for dec.More() {
				xs = append(xs, parse())
			}
			dec.Token()
			return xs
		}
		return tok
	}
	m, _ := parse().(*sqlglot.SchemaMapping)
	return m
}

type fixtureOpts struct {
	Schema                   any
	Dialect                  string
	LeaveTablesIsolated      *bool
	ValidateQualifyColumns   *bool
	CanonicalizeTableAliases *bool
}

// checkFile mirrors TestOptimizer.check_file: it runs fn over the (input, expected) pairs of an
// optimizer fixture file and compares the generated SQL.
func checkFile(t *testing.T, file string, pretty bool, schema any, fn func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error)) {
	t.Helper()
	for i, p := range testutil.LoadSQLFixturePairs("optimizer/" + file + ".sql") {
		title := p.Meta["title"]
		if title == "" {
			title = fmt.Sprintf("%d, %s", i+1, p.SQL)
		}
		o := fixtureOpts{Schema: schema, Dialect: p.Meta["dialect"]}
		if s, ok := p.Meta["schema"]; ok {
			o.Schema = jsonSchema(t, s)
		}
		boolMeta := func(k string) *bool {
			if v, ok := p.Meta[k]; ok {
				b := testutil.StringToBool(v)
				return &b
			}
			return nil
		}
		o.LeaveTablesIsolated = boolMeta("leave_tables_isolated")
		o.ValidateQualifyColumns = boolMeta("validate_qualify_columns")
		o.CanonicalizeTableAliases = boolMeta("canonicalize_table_aliases")
		t.Run(title, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic: %v", r)
				}
			}()
			var d any
			if o.Dialect != "" {
				if !sqlglot.HasDialect(o.Dialect) {
					t.Skipf("dialect %q is not implemented", o.Dialect)
				}
				d = o.Dialect
			}
			e, err := sqlglot.ParseOne(p.SQL, d)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			optimized, err := fn(e, o)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			actual, err := optimized.Generate(d, exp.Pretty(pretty))
			if err != nil {
				t.Fatalf("generate error: %v", err)
			}
			if actual != p.Expected {
				t.Errorf("\n  sql: %s\n  got: %s\n want: %s", p.SQL, actual, p.Expected)
			}
			for node := range optimized.Walk(true, nil) {
				for _, a := range node.Args() {
					if child, ok := a.Value.(*exp.Expr); ok && child != nil {
						if child.ArgKey() != a.Key || child.Parent() != node {
							t.Errorf("broken parent pointers at %s.%s", node.Key(), a.Key)
						}
					}
				}
			}
		})
	}
}
