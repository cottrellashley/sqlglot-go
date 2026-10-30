# Porting log (sqlglot v30.27.0 -> Go)

Reference checkout: ../ref/sqlglot (git 5c101a2)

## Conventions
- exp kinds: Python class `exp.Foo` -> `exp.Foo` (kind constant); `_Foo` -> `exp.FooBase`.
- Construction: `exp.Foo(this=x, expression=y)` -> `exp.New(exp.Foo, "this", x, "expression", y)`;
  in the parser `self.expression(exp.Foo(...))` -> `p.Expr(exp.Foo, ...)`.
- TokenType.L_PAREN -> tokens.LParen (CamelCase of member name).
- exp.DType.BIGINT -> exp.DTypeBigint.
- Class attributes FOO_BAR -> config field FooBar (ParserConfig/GeneratorConfig/DialectClass).
- Parser `self._parse_foo(...)` -> `p.ParseFoo(...)` (virtual, via C.Methods; impl is `parseFoo`).
- Generator `self.foo_sql(e)` -> `g.FooSQL(e)` (kind methods dispatch via per-kind table).
- Python `self._curr/_prev/_next/_index` -> `p.Curr/Prev/Next/Index`; truthiness `tok.Ok()`.
- `_match(T)` -> `p.Match(T)`; `_match(T, advance=False)` -> `p.Check(T)`.

## Status
- [x] exp (AST, kinds, builders)
- [x] tokens (differentially tested against Python on 1059 inputs)
- [ ] parser
- [ ] generator
- [ ] dialect helpers / transforms / jsonpath / time
- [ ] snowflake, duckdb
- [ ] optimizer
- [ ] tests
