// Command goexec runs the SQL lines of stdin through the executor (a differential testing aid
// mirroring tools/port/pyexec.py).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	_ "github.com/cottrellashley/sqlglot-go/dialects"
	"github.com/cottrellashley/sqlglot-go/executor"
)

func main() {
	tables := map[string]any{
		"t": executor.NewTable([]string{"a", "b"}, [][]any{{1, "x"}, {2, "y"}, {2, "z"}, {nil, "w"}}, nil),
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		sql := strings.TrimRight(sc.Text(), "\n")
		if sql == "" {
			continue
		}
		res, err := executor.Execute(sql, executor.Options{Tables: tables})
		if err != nil {
			fmt.Println("ERROR", err)
			continue
		}
		rows := make([][]any, len(res.Rows))
		for i, r := range res.Rows {
			rows[i] = make([]any, len(r))
			for j, v := range r {
				switch v.(type) {
				case nil, int, float64, string, bool:
					rows[i][j] = v
				default:
					rows[i][j] = executor.Str(v)
				}
			}
		}
		b, _ := json.Marshal(rows)
		fmt.Println(strings.ReplaceAll(strings.ReplaceAll(string(b), ",", ", "), "1.0,", "1.0,"))
	}
}
