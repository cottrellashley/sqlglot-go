package executor

// Go equivalents of the test_executor.py cases that drive PythonExecutor directly or inspect
// its private state.

import (
	"sort"
	"testing"

	sqlglot "github.com/cottrellashley/sqlglot-go"
)

func mustPlan(t *testing.T, sql string, schema any) *sqlglot.Plan {
	t.Helper()
	optimized, err := sqlglot.Optimize(sql, sqlglot.OptimizeOptions{Schema: schema, LeaveTablesIsolated: true})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := sqlglot.NewPlan(optimized)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func rowsTable(column string, vals ...any) *Table {
	rows := make([][]any, len(vals))
	for i, v := range vals {
		rows[i] = []any{v}
	}
	return NewTable([]string{column}, rows, nil)
}

func TestSubqueryMemoization(t *testing.T) {
	schema := sqlglot.M("x", sqlglot.M("a", "int"), "y", sqlglot.M("b", "int"))
	var xs []any
	for i := 0; i < 12; i++ {
		xs = append(xs, i%3)
	}
	for _, tc := range []struct {
		sql   string
		plans int
	}{
		{"SELECT a FROM x WHERE NOT EXISTS (SELECT 1 FROM y WHERE b = x.a OR b = 9)", 1},
		{"SELECT a FROM x WHERE NOT EXISTS (SELECT 1 FROM y WHERE b = 9 OR EXISTS " +
			"(SELECT 1 FROM y AS y2 WHERE y2.b = x.a OR y2.b = 9))", 2},
	} {
		tables, err := EnsureTables(map[string]any{"x": rowsTable("a", xs...), "y": rowsTable("b", 1, 2)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		e := New(tables, nil)
		if _, err := e.Execute(mustPlan(t, tc.sql, schema), nil); err != nil {
			t.Fatal(err)
		}
		// one plan per subquery, and one run per distinct correlated value of the 12 rows
		if len(e.subqueryPlans) != tc.plans {
			t.Fatalf("%s: %d plans, want %d", tc.sql, len(e.subqueryPlans), tc.plans)
		}
		var sizes []int
		for _, p := range e.subqueryPlans {
			sizes = append(sizes, len(p.cache))
		}
		sort.Ints(sizes)
		for _, n := range sizes {
			if n != 3 {
				t.Fatalf("%s: cache sizes %v, want 3 each", tc.sql, sizes)
			}
		}
	}
}

func TestSubqueryExecutionDoesNotMutateThePlan(t *testing.T) {
	schema := sqlglot.M("x", sqlglot.M("a", "int"), "y", sqlglot.M("b", "int"))
	plan := mustPlan(t, "SELECT a FROM x WHERE NOT EXISTS (SELECT 1 FROM y WHERE b = x.a OR b = 9)", schema)
	before := plan.Expression.SQL()
	tables, _ := EnsureTables(map[string]any{"x": rowsTable("a", 1, 2), "y": rowsTable("b", 2)}, nil)
	if _, err := New(tables, nil).Execute(plan, nil); err != nil {
		t.Fatal(err)
	}
	if after := plan.Expression.SQL(); after != before {
		t.Fatalf("plan changed:\n%s\n%s", before, after)
	}
}

func TestBooleanLogicShortCircuits(t *testing.T) {
	evaluations := 0
	e := New(nil, map[string]Func{"RECORD_EVALUATION": func(...any) any { evaluations++; return true }})
	context := e.context(nil, map[string]*Table{})
	for _, tc := range []struct {
		sql  string
		want bool
	}{{"FALSE AND RECORD_EVALUATION()", false}, {"TRUE OR RECORD_EVALUATION()", true}} {
		expression, err := sqlglot.ParseOne(tc.sql, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := context.eval(e.generate(expression)); got != tc.want {
			t.Fatalf("%s = %v", tc.sql, got)
		}
	}
	if evaluations != 0 {
		t.Fatalf("RECORD_EVALUATION was evaluated %d times", evaluations)
	}
}

func TestAggregationOrder(t *testing.T) {
	plan := mustPlan(t, "SELECT AVG(bill_length_mm) AS avg_bill_length, AVG(bill_depth_mm) AS avg_bill_depth FROM penguins",
		sqlglot.M("penguins", sqlglot.M("bill_length_mm", "DOUBLE", "bill_depth_mm", "DOUBLE")))
	var aliases []string
	for _, agg := range plan.Root.Aggregations {
		aliases = append(aliases, agg.Alias())
	}
	if len(aliases) != 2 || aliases[0] != "avg_bill_length" || aliases[1] != "avg_bill_depth" {
		t.Fatalf("aggregations %v", aliases)
	}
}
