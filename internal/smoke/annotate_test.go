package smoke

import (
	"fmt"
	"testing"

	sqlglot "github.com/cottrellashley/sqlglot-go"
)

func TestAnnotateSmoke(t *testing.T) {
	for _, sql := range []string{
		"1 + 2", "1 + 2.5", "'a' || 'b'", "CAST(x AS INT) + 1", "CASE WHEN a THEN 1 ELSE 2.0 END",
		"SUM(1)", "ARRAY(1, 2)", "STRUCT(1 AS a, 'x' AS b)", "DATE_ADD(CAST('2020-01-01' AS DATE), INTERVAL 1 DAY)",
		"COALESCE(NULL, 1)", "x IS NULL", "1 / 2",
	} {
		e := sqlglot.MustParseOne(sql, nil)
		sqlglot.AnnotateTypes(e, nil)
		fmt.Printf("%s => %s\n", sql, e.Type().SQL())
	}
}
