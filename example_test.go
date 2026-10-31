package sqlglot_test

import (
	"fmt"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	_ "github.com/cottrellashley/sqlglot-go/dialects" // registers duckdb and snowflake
	"github.com/cottrellashley/sqlglot-go/exp"
)

func ExampleTranspile() {
	out, err := sqlglot.Transpile(
		"SELECT IFF(x > 1, 'big', 'small') AS size, ARRAY_SIZE(tags) AS n FROM t QUALIFY ROW_NUMBER() OVER (ORDER BY x) = 1",
		"snowflake", "duckdb")
	if err != nil {
		panic(err)
	}
	fmt.Println(out[0])
	// Output:
	// SELECT CASE WHEN x > 1 THEN 'big' ELSE 'small' END AS size, ARRAY_LENGTH(tags) AS n FROM t QUALIFY ROW_NUMBER() OVER (ORDER BY x) = 1
}

func ExampleTranspile_pretty() {
	out, _ := sqlglot.Transpile("SELECT a, b FROM x WHERE a > 1", "duckdb", "snowflake", exp.Pretty(true))
	fmt.Println(out[0])
	// Output:
	// SELECT
	//   a,
	//   b
	// FROM x
	// WHERE
	//   a > 1
}

func ExampleParseOne() {
	e, err := sqlglot.ParseOne("SELECT a, b + 1 AS c FROM d", "duckdb")
	if err != nil {
		panic(err)
	}
	for c := range e.FindAll(exp.Column) {
		fmt.Println(c.AliasOrName())
	}
	// Output:
	// a
	// b
}

func ExampleNewSelect() {
	q := exp.NewSelect("a", "b").From("x").Where("a > 1").Where("b < 2")
	fmt.Println(q.SQL())
	// Output:
	// SELECT a, b FROM x WHERE a > 1 AND b < 2
}

func ExampleOptimize() {
	schema := sqlglot.M("x", sqlglot.M("a", "INT", "b", "INT"), "y", sqlglot.M("b", "INT", "c", "INT"))
	e, err := sqlglot.Optimize("SELECT a, c FROM x JOIN y ON x.b = y.b WHERE 1 + 1 = 2", sqlglot.OptimizeOptions{Schema: schema})
	if err != nil {
		panic(err)
	}
	fmt.Println(e.SQL())
	// Output:
	// SELECT "x"."a" AS "a", "y"."c" AS "c" FROM "x" AS "x" JOIN "y" AS "y" ON "x"."b" = "y"."b"
}

func ExampleLineage() {
	node, err := sqlglot.Lineage("total", "SELECT a + b AS total FROM (SELECT x.a, x.b FROM x) AS s",
		sqlglot.M("x", sqlglot.M("a", "INT", "b", "INT")), sqlglot.LineageOptions{})
	if err != nil {
		panic(err)
	}
	for n := range node.Walk() {
		fmt.Println(n.Name)
	}
	// Output:
	// total
	// s.a
	// x.a
	// s.b
	// x.b
}

func ExampleDiff() {
	source, _ := sqlglot.ParseOne("SELECT a + b FROM t", nil)
	target, _ := sqlglot.ParseOne("SELECT a - b FROM t", nil)
	edits, _ := sqlglot.Diff(source, target, sqlglot.DiffOptions{DeltaOnly: true})
	for _, edit := range edits {
		switch e := edit.(type) {
		case sqlglot.Remove:
			fmt.Println("remove", e.Expression.SQL())
		case sqlglot.Insert:
			fmt.Println("insert", e.Expression.SQL())
		case sqlglot.Move:
			fmt.Println("move", e.Source.SQL())
		}
	}
	// Output:
	// remove a + b
	// insert a - b
	// move a
	// move b
}
