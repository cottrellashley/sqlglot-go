package smoke

import (
	"testing"

	sqlglot "github.com/cottrellashley/sqlglot-go"
)

func TestSmoke(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1",
		"SELECT a, b FROM t WHERE x = 1",
		"SELECT a AS b, COUNT(*) FROM x.y JOIN z ON x.a = z.b GROUP BY 1 ORDER BY 2 DESC LIMIT 10",
		"WITH a AS (SELECT 1) SELECT * FROM a UNION ALL SELECT 2",
		"CREATE TABLE t (a INT, b VARCHAR(10))",
		"INSERT INTO t VALUES (1, 'x')",
		"SELECT CASE WHEN a > 1 THEN 'x' ELSE 'y' END FROM t",
		"SELECT CAST(a AS DECIMAL(10, 2)) FROM t",
		"SELECT x::INT, y[1], z -> 'a' FROM t",
	} {
		out, err := sqlglot.Transpile(sql, "", "")
		if err != nil {
			t.Errorf("%s: %v", sql, err)
			continue
		}
		if out[0] != sql {
			t.Errorf("\n got: %s\nwant: %s", out[0], sql)
		}
	}
}
