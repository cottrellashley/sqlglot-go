package executor_test

import (
	"fmt"

	"github.com/cottrellashley/sqlglot-go/executor"
)

func ExampleExecute() {
	tables := map[string]any{
		"sales": executor.NewTable([]string{"region", "amount"}, [][]any{
			{"east", 10}, {"west", 5}, {"east", 7}, {"north", nil},
		}, nil),
	}
	result, err := executor.Execute(
		"SELECT region, SUM(amount) AS total FROM sales GROUP BY region ORDER BY total DESC",
		executor.Options{Tables: tables})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Columns)
	for _, row := range result.Rows {
		fmt.Println(executor.Repr(executor.Tuple(row)))
	}
	// Output:
	// [region total]
	// ('east', 17)
	// ('west', 5)
	// ('north', None)
}
