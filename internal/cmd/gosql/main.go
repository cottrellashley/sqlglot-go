// Command gosql transpiles each stdin line (JSON output per line), for differential testing
// against the Python implementation.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	_ "github.com/cottrellashley/sqlglot-go/dialects"
)

func main() {
	read, write := "", ""
	if len(os.Args) > 1 {
		read = os.Args[1]
	}
	write = read
	if len(os.Args) > 2 {
		write = os.Args[2]
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		out, err := sqlglot.Transpile(line, read, write)
		if err != nil {
			fmt.Println("ERROR", err)
			continue
		}
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(out)
		fmt.Print(b.String())
	}
}
