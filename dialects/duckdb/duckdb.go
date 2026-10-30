// Package duckdb implements the DuckDB dialect (a port of sqlglot/dialects/duckdb.py and the
// matching tokenizer, parser, generator and typing modules). Importing the package registers
// the dialect under the name "duckdb".
package duckdb

import (
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func init() { sqlglot.Register("duckdb", New) }

// New builds the DuckDB dialect class.
func New() *sqlglot.DialectClass {
	c := sqlglot.NewDialectClass("duckdb", sqlglot.BaseDialectClass())
	c.ClassName = "DuckDB"
	c.NullOrdering = "nulls_are_last"
	c.SupportsUserDefinedTypes = true
	c.IndexOffset = 1
	c.ConcatCoalesce = true
	c.ConcatWsCoalesce = true
	c.SupportsOrderByAll = true
	c.SupportsLimitAll = true
	c.SupportsFixedSizeArrays = true
	c.TablesReferenceableAsColumns = true
	c.QueryResultsAreStructs = true
	c.StrictJsonPathSyntax = false
	c.NumbersCanBeUnderscoreSeparated = true
	c.UuidIsStringType = false
	c.UsingColumnOrder = sqlglot.InPlace

	// https://duckdb.org/docs/sql/introduction.html#creating-a-new-table
	c.NormalizationStrategy = sqlglot.CaseInsensitive
	c.AsciiOnlyNormalization = true

	c.DatePartMapping["DAYOFWEEKISO"] = "ISODOW"
	delete(c.DatePartMapping, "WEEKDAY")

	c.InverseTimeMappingOverrides = map[string]string{
		"%e":       "%-d", // BigQuery's space-padded day (%e) -> DuckDB's no-padding day (%-d)
		"%:z":      "%z",  // In DuckDB %z can represent +/-HH:MM, +/-HHMM, or +/-HH.
		"%-z":      "%z",
		"%f_zero":  "%n",
		"%f_one":   "%n",
		"%f_two":   "%n",
		"%f_three": "%g",
		"%f_four":  "%n",
		"%f_five":  "%n",
		"%f_seven": "%n",
		"%f_eight": "%n",
		"%f_nine":  "%n",
	}

	baseToJSONPath := c.Methods.ToJSONPath
	c.Methods.ToJSONPath = func(d *sqlglot.Dialect, path *exp.Expr) *exp.Expr {
		if path.IsKind(exp.Literal) {
			// DuckDB also supports the JSON pointer syntax, where every path starts with a `/`.
			// Additionally, it allows accessing the back of lists using the `[#-i]` syntax.
			// This check ensures we'll avoid trying to parse these as JSON paths.
			pathText := path.Name()
			if strings.HasPrefix(pathText, "/") || strings.Contains(pathText, "[#") {
				return path
			}
		}
		return baseToJSONPath(d, path)
	}

	c.UnescapedSequences = map[string]string{`\a`: "a", `\v`: "v"}

	tokenizer(c.Tokenizer)
	c.Parser = sqlglot.NewParserConfig()
	parser(c.Parser)
	c.Generator = sqlglot.NewGeneratorConfig()
	generator(c.Generator)
	c.ExpressionMetadata = sqlglot.BaseExpressionMetadata()
	typing(c.ExpressionMetadata)
	return c
}

func tokenizer(t *tokens.Config) {
	t.NumericEscapes = map[string]tokens.NumericEscape{"x": {Base: 16, MinDigits: 1, MaxDigits: 2, MaxValue: 0xFF}, "0": {Base: 8, MinDigits: 1, MaxDigits: 3, MaxValue: 0o777}}
	t.NumericEscapesAreBytes = true
	t.DropUnknownEscapes = true
	t.ByteStrings = []tokens.Delim{{Start: "e'", End: "'"}, {Start: "E'", End: "'"}}
	t.ByteStringEscapes = []string{"'", `\`}
	t.HeredocStrings = []tokens.Delim{tokens.Q("$")}
	t.HeredocTagIsIdentifier = true
	t.HeredocStringAlternative = tokens.Parameter

	for k, v := range map[string]tokens.TokenType{
		"//":           tokens.Div,
		"**":           tokens.Dstar,
		"^@":           tokens.CaretAt,
		"@>":           tokens.AtGt,
		"<@":           tokens.LtAt,
		"ATTACH":       tokens.Attach,
		"BINARY":       tokens.Varbinary,
		"BITSTRING":    tokens.Bit,
		"BPCHAR":       tokens.Text,
		"CHAR":         tokens.Text,
		"CONNECT":      tokens.Connect,
		"DATETIME":     tokens.Timestampntz,
		"DETACH":       tokens.Detach,
		"DISCONNECT":   tokens.Disconnect,
		"FORCE":        tokens.Force,
		"INSTALL":      tokens.Install,
		"INT8":         tokens.Bigint,
		"LOGICAL":      tokens.Boolean,
		"MACRO":        tokens.Function,
		"ONLY":         tokens.Only,
		"PIVOT_WIDER":  tokens.Pivot,
		"POSITIONAL":   tokens.Positional,
		"RESET":        tokens.Command,
		"ROW":          tokens.Struct,
		"SIGNED":       tokens.Int,
		"STRING":       tokens.Text,
		"SUMMARIZE":    tokens.Summarize,
		"TIMESTAMP":    tokens.Timestampntz,
		"TIMESTAMP_S":  tokens.TimestampS,
		"TIMESTAMP_MS": tokens.TimestampMs,
		"TIMESTAMP_NS": tokens.TimestampNs,
		"TIMESTAMP_US": tokens.Timestamp,
		"UBIGINT":      tokens.Ubigint,
		"UINTEGER":     tokens.Uint,
		"USMALLINT":    tokens.Usmallint,
		"UTINYINT":     tokens.Utinyint,
		"VARCHAR":      tokens.Text,
	} {
		t.Keywords[k] = v
	}
	delete(t.Keywords, "/*+")
	delete(t.Keywords, "SORT BY")

	t.SingleTokens['$'] = tokens.Parameter
	t.VarSingleTokens = map[rune]bool{'$': true}
	t.Commands = t.Commands.Without(tokens.Show)
}
