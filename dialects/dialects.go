// Package dialects registers every dialect implementation. Import it for its side effects:
//
//	import _ "github.com/cottrellashley/sqlglot-go/dialects"
//
// Individual dialects can also be imported on their own, e.g.
// github.com/cottrellashley/sqlglot-go/dialects/duckdb.
package dialects

import (
	_ "github.com/cottrellashley/sqlglot-go/dialects/duckdb"
	_ "github.com/cottrellashley/sqlglot-go/dialects/snowflake"
)
