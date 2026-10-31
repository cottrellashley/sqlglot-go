# sqlglot-go

A Go port of [SQLGlot](https://github.com/tobymao/sqlglot), the SQL parser, transpiler,
optimizer and engine.

It is a line-by-line port of SQLGlot v30.27.0 (upstream commit `5c101a2`): the same
tokenizer, recursive descent parser, generator, expression tree, optimizer rules and dialect
architecture, so behavior and output match the Python library, and SQLGlot's own test suites
run against it (see [Testing](#testing)).

| Area | Status |
|---|---|
| Tokenizer, parser, generator, expression tree and builders | ported |
| Dialects | **Snowflake** and **DuckDB**, plus the base dialect. The other 32 SQLGlot dialects are not ported yet |
| Optimizer | every rule, plus `Optimize` with the default rule order |
| Schema, type annotation, transforms, JSONPath, time formats | ported |
| Lineage, semantic diff, anonymize, serde | ported |
| Planner and executor | ported (the executor interprets expressions instead of generating Python code) |
| CLI (`python -m sqlglot`) | ported as `cmd/sqlglot` |

## Install

```sh
go get github.com/cottrellashley/sqlglot-go
```

Requires Go 1.23 or newer. There are no dependencies outside the standard library.

Dialects register themselves when their package is imported. Import the bundle to get all of them:

```go
import (
	sqlglot "github.com/cottrellashley/sqlglot-go"
	_ "github.com/cottrellashley/sqlglot-go/dialects" // snowflake, duckdb
)
```

## Usage

### Transpile between dialects

```go
out, err := sqlglot.Transpile(
	"SELECT IFF(x > 1, 'big', 'small') AS size, ARRAY_SIZE(tags) AS n FROM t QUALIFY ROW_NUMBER() OVER (ORDER BY x) = 1",
	"snowflake", "duckdb")
// out[0]: SELECT CASE WHEN x > 1 THEN 'big' ELSE 'small' END AS size, ARRAY_LENGTH(tags) AS n
//         FROM t QUALIFY ROW_NUMBER() OVER (ORDER BY x) = 1

pretty, _ := sqlglot.Transpile("SELECT a, b FROM x WHERE a > 1", "duckdb", "snowflake", exp.Pretty(true))
```

`exp.Pretty`, `exp.Identify`, `exp.UnsupportedLevel` and the other generator options mirror the
keyword arguments of `Expression.sql()`.

### Parse, inspect and build expressions

```go
e, err := sqlglot.ParseOne("SELECT a, b + 1 AS c FROM d", "duckdb")
for c := range e.FindAll(exp.Column) {
	fmt.Println(c.AliasOrName()) // a, b
}

q := exp.NewSelect("a", "b").From("x").Where("a > 1").Where("b < 2")
q.SQL() // SELECT a, b FROM x WHERE a > 1 AND b < 2
sql, err := q.Generate("snowflake")
```

Expression nodes are `*exp.Expr` values tagged with a kind (`exp.Select`, `exp.Column`, ...);
`e.Is(kind)` follows SQLGlot's class hierarchy (`e.Is(exp.Func)` is `isinstance(e, exp.Func)`).
Traversal (`Walk`, `BFS`, `DFS`, `Find`, `FindAll`, `FindAncestor`), mutation (`Set`,
`Replace`, `Transform`) and the builder methods follow the Python API.

### Optimize

```go
schema := sqlglot.M("x", sqlglot.M("a", "INT", "b", "INT"), "y", sqlglot.M("b", "INT", "c", "INT"))
e, err := sqlglot.Optimize("SELECT a, c FROM x JOIN y ON x.b = y.b WHERE 1 + 1 = 2",
	sqlglot.OptimizeOptions{Schema: schema})
// SELECT "x"."a" AS "a", "y"."c" AS "c" FROM "x" AS "x" JOIN "y" AS "y" ON "x"."b" = "y"."b"
```

Individual rules are exported too (`Qualify`, `AnnotateTypes`, `Simplify`, `PushdownPredicates`,
`MergeSubqueries`, `UnnestSubqueries`, ...), and `OptimizeOptions.Rules` takes any sequence of
`Rule` values.

### Lineage, diff and execution

```go
node, err := sqlglot.Lineage("total", "SELECT a + b AS total FROM (SELECT x.a, x.b FROM x) AS s",
	sqlglot.M("x", sqlglot.M("a", "INT", "b", "INT")), sqlglot.LineageOptions{})
for n := range node.Walk() {
	fmt.Println(n.Name) // total, s.a, x.a, s.b, x.b
}

edits, err := sqlglot.Diff(source, target, sqlglot.DiffOptions{DeltaOnly: true}) // Insert/Remove/Move/Update/Keep

result, err := executor.Execute(
	"SELECT region, SUM(amount) AS total FROM sales GROUP BY region ORDER BY total DESC",
	executor.Options{Tables: map[string]any{"sales": salesTable}})
```

These snippets are runnable examples in [example_test.go](example_test.go) and
[executor/example_test.go](executor/example_test.go).

### Command line

```sh
go install github.com/cottrellashley/sqlglot-go/cmd/sqlglot@latest
sqlglot --read snowflake --write duckdb "SELECT IFF(a > 1, 'x', 'y') FROM t"
echo "select 1" | sqlglot --no-pretty -
sqlglot --parse "SELECT a FROM t"
```

## Architecture

The packages map onto SQLGlot's modules:

| Go | Python |
|---|---|
| `tokens` | `sqlglot/tokens.py`, `tokenizer_core.py` |
| `exp` | `sqlglot/expressions/` (kinds, builders, serde) |
| `sqlglot` (root) | `parser.py`, `generator.py`, `dialects/dialect.py`, `transforms.py`, `schema.py`, `optimizer/`, `lineage.py`, `diff.py`, `anonymize.py`, `planner.py`, `time.py`, `jsonpath.py` |
| `dialects/<name>` | `sqlglot/dialects/<name>.py` with its `parsers/`, `generators/`, `tokenizers/` and `typing/` modules |
| `executor` | `sqlglot/executor/` |
| `errs`, `helper`, `trie` | `errors.py`, `helper.py`, `trie.py` |
| `cmd/sqlglot` | `__main__.py` |

Python's class hierarchy of dialects becomes data. A `*DialectClass` holds the dialect's
settings (`NORMALIZATION_STRATEGY`, `NULL_ORDERING`, ... become fields such as
`NormalizationStrategy` and `NullOrdering`) and three tables:

- `Tokenizer` (`*tokens.Config`): keywords, quotes, identifiers, comments, escapes.
- `Parser` (`*ParserConfig`): `Functions`, `FunctionParsers`, `StatementParsers`,
  `RangeParsers`, ... (the Python `FUNCTIONS`, `FUNCTION_PARSERS`, ... dictionaries), and
  `Methods` for parser methods a dialect overrides (`_parse_table` becomes
  `Methods.ParseTable`).
- `Generator` (`*GeneratorConfig`): `Transforms` and `SQLMethods` (the per-expression
  generation functions; `TRANSFORMS` and the `*_sql` methods), `TypeMapping`, and the flags.

A dialect is built by cloning its parent's class (`NewDialectClass(name, parent)`) and
editing those tables, exactly like a Python subclass overriding class attributes. Overriding a
generation function while calling the parent's version is done by capturing the previous
entry before replacing it.

## Adding a dialect

Adding a dialect is cheap, and works like it does in SQLGlot: declare how the dialect differs
from its parent, and nothing else. A minimal dialect:

```go
package mydialect

import (
	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func init() { sqlglot.Register("mydialect", New) }

// New builds the dialect class. Derive from another dialect to inherit its behavior,
// e.g. sqlglot.MustDialectClass("duckdb").
func New() *sqlglot.DialectClass {
	c := sqlglot.NewDialectClass("mydialect", sqlglot.BaseDialectClass())
	c.ClassName = "MyDialect"
	c.NormalizationStrategy = sqlglot.Uppercase

	// tokenizer: identifiers quoted with backticks, an extra keyword
	c.Tokenizer.Identifiers = []tokens.Delim{tokens.Q("`")}
	c.Tokenizer.Keywords["MINUS"] = tokens.Except

	// parser: a function name that maps to an existing expression
	c.Parser.Functions["STRLEN"] = sqlglot.FromArgList(exp.Length)

	// generator: how an expression is written in this dialect
	c.Generator.Transforms[exp.Length] = sqlglot.RenameFunc("STRLEN")
	c.Generator.TypeMapping[exp.DTypeText] = "STRING"
	return c
}
```

Then `sqlglot.Transpile(sql, "duckdb", "mydialect")` works, and importing the package registers
it. To ship it with the library, add the package to `dialects/dialects.go`.

- To port one of SQLGlot's other dialects, translate its Python modules
  (`dialects/x.py`, `parsers/x.py`, `generators/x.py`, `tokenizers/x.py`, `typing/x.py`)
  section by section. The naming conventions in [PORTING.md](PORTING.md) make that mechanical,
  and `dialects/duckdb` and `dialects/snowflake` are complete worked examples. The converted
  test suite for the dialect already exists in `tests/dialects`: it starts running as soon as the
  dialect is registered (until then its cases are skipped).
- Dialects that need expressions SQLGlot doesn't have can register new kinds with
  `exp.RegisterKind` (the equivalent of subclassing `exp.Func`); function kinds without a
  generator entry are generated as `NAME(args...)`.
- A dialect class can also be used without registering it: `sqlglot.ParseOne(sql, myClass)`.

See [dialect_extension_test.go](dialect_extension_test.go) for tested examples of custom
dialects, patched parsers and registered kinds.

## Testing

```sh
go test ./...
```

There are three layers of tests:

1. **SQLGlot's own test suites, converted to Go.** `tools/port/convert_tests.py` translates
   the Python `unittest` modules (`tests/dialects/test_*.py` and the core suites `test_parser`,
   `test_transpile`, `test_expressions`, `test_optimizer`, `test_executor`, `test_lineage`, ...)
   statement by statement into Go code that runs on `internal/pyrt`, a small runtime with
   Python semantics bound to the Go API. That keeps SQLGlot's assertions, inputs and expected
   outputs exactly as upstream wrote them: 14,296 statements in 53 files, all of which convert.
   803 test methods pass, and 26 are skipped with a documented reason:
   - Most test Python-only mechanics, such as lazy imports, inline subclassing of Python
     classes, monkeypatching of private methods and generated Python source. Each of these
     skips names the Go-native test that covers the same behavior, when there is one.
   - Four compare results against a live DuckDB database.

   The test classes of the 30 dialects that aren't ported are skipped as a whole. Within the
   passing methods, statements that need an unported dialect are skipped individually and logged.
2. **SQLGlot's fixture files** (`testdata/fixtures`, copied from upstream): identity and pretty
   round trips and every optimizer fixture (including TPC-H and TPC-DS) run through native Go
   tests that mirror `check_file`.
3. **Go-native tests** for Go-specific API surface (custom dialects, registered kinds, custom
   schemas, the CLI, executor internals) and runnable examples.

Regenerate the converted suites from a SQLGlot checkout with:

```sh
tools/port/convert_all.sh /path/to/sqlglot
```

`internal/cmd/gosql` with `tools/port/pysql.py`, and `internal/cmd/goexec` with
`tools/port/pyexec.py`, give line-by-line differential comparisons against the Python library.

## Differences from SQLGlot

- **Errors** are returned as Go errors of the `errs` types (`*errs.ParseError`,
  `*errs.OptimizeError`, ...). Builder methods that mirror Python's chaining API
  (`exp.NewSelect(...).Where(...)`) panic on invalid input, as Python raises.
- **Iteration order.** Where Python's output order depends on hashing (a few `set()`
  iterations in lineage), the Go port uses a stable first-seen order, which is one of the
  orders Python produces. The executor's `list(set(...))` results follow CPython's set order
  exactly for numbers and `None`.
- **The executor** compiles expressions to Go closures with the semantics of the Python code that
  SQLGlot's executor generates. Values are Go types (`int`, `float64`, `string`,
  `executor.Date`, `time.Time`, ...).
- **Not applicable to Go:** the mypyc extension and Python entry-point plugin discovery
  (`test_sqlglotc.py`, `test_dialect_entry_points.py`, `test_dialect_imports.py`, `test_docs.py`).

## License

MIT, like SQLGlot. See [LICENSE](LICENSE): the original SQLGlot copyright notice is
retained, and the Go port is covered by the same terms.
