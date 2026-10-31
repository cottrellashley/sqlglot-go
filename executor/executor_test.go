package executor_test

import (
	"testing"

	_ "github.com/cottrellashley/sqlglot-go/dialects"
	"github.com/cottrellashley/sqlglot-go/executor"
)

func TestExecuteSmoke(t *testing.T) {
	tables := map[string]any{
		"t": executor.NewTable([]string{"a", "b"}, [][]any{{1, "x"}, {2, "y"}, {2, "z"}, {nil, "w"}}, nil),
	}
	for _, tc := range []struct {
		sql  string
		want string
	}{
		{"SELECT a, b FROM t WHERE a > 1", "[[2 y] [2 z]]"},
		{"SELECT a, COUNT(*) AS c FROM t GROUP BY a ORDER BY a", "[[<nil> 1] [1 1] [2 2]]"},
		{"SELECT SUM(a) AS s, AVG(a) AS m FROM t", "[[5 1.6666666666666667]]"},
		{"SELECT DISTINCT a FROM t ORDER BY a DESC NULLS FIRST", "[[<nil>] [2] [1]]"},
		{"SELECT 1 + 1 AS x", "[[2]]"},
		{"SELECT a FROM t UNION SELECT 3", "[[<nil>] [1] [2] [3]]"},
		{"SELECT x.a, y.b FROM t AS x JOIN t AS y ON x.a = y.a WHERE x.b = 'x'", "[[1 x]]"},
		{"SELECT a FROM t WHERE a IN (SELECT a FROM t WHERE b = 'y')", "[[2] [2]]"},
		{"SELECT b FROM t ORDER BY b LIMIT 2", "[[w] [x]]"},
	} {
		res, err := executor.Execute(tc.sql, executor.Options{Tables: tables})
		if err != nil {
			t.Errorf("%s: %v", tc.sql, err)
			continue
		}
		if got := fmtRows(res.Rows); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.sql, got, tc.want)
		}
	}
}
