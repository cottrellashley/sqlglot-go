// Package snowflake implements the Snowflake dialect (a port of sqlglot/dialects/snowflake.py
// and the matching tokenizer, parser, generator and typing modules). Importing the package
// registers the dialect under the name "snowflake".
package snowflake

import (
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func init() { sqlglot.Register("snowflake", New) }

// timeMappingBase is the Snowflake TIME_MAPPING before case variants are expanded (ordered).
var timeMappingBase = [][2]string{
	{"YYYY", "%Y"},
	{"YY", "%y"},
	{"MMMM", "%B"},
	{"MON", "%b"},
	{"MM", "%m"},
	{"DD", "%d"},
	{"DY", "%a"},
	{"HH24", "%H"},
	{"HH12", "%I"},
	{"MI", "%M"},
	{"SS", "%S"},
	{"FF", "%f_nine"}, // %f_ internal representation with precision specified
	{"FF0", "%f_zero"},
	{"FF1", "%f_one"},
	{"FF2", "%f_two"},
	{"FF3", "%f_three"},
	{"FF4", "%f_four"},
	{"FF5", "%f_five"},
	{"FF6", "%f"},
	{"FF7", "%f_seven"},
	{"FF8", "%f_eight"},
	{"FF9", "%f_nine"},
	{"TZHTZM", "%z"},
	{"TZH:TZM", "%:z"}, // internal representation for ±HH:MM
	{"TZH", "%-z"},     // internal representation ±HH
	// Seems like Snowflake treats AM/PM in the format string as equivalent,
	// only the time (stamp) value's AM/PM affects the output
	{"AM", "%p"},
	{"PM", "%p"},
}

// caseVariants mirrors product(*zip(key.upper(), key.lower())): every upper/lower combination
// of key, in itertools.product order.
func caseVariants(key string) []string {
	up, low := []rune(strings.ToUpper(key)), []rune(strings.ToLower(key))
	out := []string{""}
	for i := range up {
		var next []string
		for _, prefix := range out {
			next = append(next, prefix+string(up[i]), prefix+string(low[i]))
		}
		out = next
	}
	return out
}

// New builds the Snowflake dialect class.
func New() *sqlglot.DialectClass {
	c := sqlglot.NewDialectClass("snowflake", sqlglot.BaseDialectClass())
	c.ClassName = "Snowflake"
	// https://docs.snowflake.com/en/sql-reference/identifiers-syntax
	c.NormalizationStrategy = sqlglot.Uppercase
	// https://docs.snowflake.com/en/sql-reference/data-types-text#escape-sequences
	c.UnescapedSequences = map[string]string{`\a`: "a", `\v`: "v"}
	c.NullOrdering = "nulls_are_large"
	c.TimeFormat = "'YYYY-MM-DD HH24:MI:SS'"
	c.SupportsUserDefinedTypes = false
	c.PreferCteAliasColumn = true
	c.SupportsPositionalColumnRefs = true
	c.TablesampleSizeIsPercent = true
	c.CopyParamsAreCsv = false
	c.ArrayAggIncludesNulls = nil
	c.ArrayFuncsPropagatesNulls = true
	c.AlterTableAddRequiredForEachColumn = false
	c.AlterTableDropRequiredForEachColumn = false
	c.TryCastRequiresString = sqlglot.Bool(true)
	c.SupportsAliasRefsInJoinConditions = true
	c.LeastGreatestIgnoresNulls = false
	c.UuidIsStringType = true
	c.StarIlikeBackslashEscape = true

	// https://docs.snowflake.com/en/sql-reference/functions/split_to_table
	c.DefaultFunctionsColumnNames = map[exp.Kind][]string{
		exp.SplitToTable:       {"seq", "index", "value"},
		exp.StrtokSplitToTable: {"seq", "index", "value"},
	}

	// https://docs.snowflake.com/en/en/sql-reference/functions/initcap
	c.InitcapDefaultDelimiterChars = " \t\n\r\f\v!?@\"^#$&~_,.:;+\\-*%/|\\[\\](){}<>"

	c.InverseTimeMappingOverrides = map[string]string{
		"%d":  "DD",
		"%-d": "DD",
		"%a":  "DY",
		// in TIME_MAPPING we map '"T"' with the double quotes to 'T', and we want to prevent 'T'
		// from being mapped back to '"T"' so that 'AUTO' doesn't become 'AU"T"O'
		"T": "T",
	}

	c.TimeMapping = map[string]string{}
	c.TimeMappingOrder = nil
	for _, kv := range timeMappingBase {
		for _, variant := range caseVariants(kv[0]) {
			if _, ok := c.TimeMapping[variant]; !ok {
				c.TimeMappingOrder = append(c.TimeMappingOrder, variant)
			}
			c.TimeMapping[variant] = kv[1]
		}
	}
	c.TimeMapping[`"T"`] = "T"
	c.TimeMappingOrder = append(c.TimeMappingOrder, `"T"`)

	c.DatePartMapping["ISOWEEK"] = "WEEKISO"
	// The base Dialect maps EPOCH_SECOND -> EPOCH, but EPOCH_SECOND must stay distinct: it returns
	// BIGINT (EPOCH returns DOUBLE) and DuckDB's EPOCH() returns a float, so EPOCH_SECOND is cast
	// to BIGINT there.
	c.DatePartMapping["EPOCH_SECOND"] = "EPOCH_SECOND"
	c.DatePartMapping["EPOCH_SECONDS"] = "EPOCH_SECOND"

	c.Pseudocolumns = map[string]bool{"LEVEL": true}

	baseCanQuote := c.Methods.CanQuote
	c.Methods.CanQuote = func(d *sqlglot.Dialect, identifier *exp.Expr, identify any) bool {
		// This disables quoting DUAL in SELECT ... FROM DUAL, because Snowflake treats an
		// unquoted DUAL keyword in a special way and does not map it to a user-defined table
		return baseCanQuote(d, identifier, identify) && !(identifier.Parent().IsKind(exp.Table) &&
			!identifier.Bool("quoted") && strings.ToLower(identifier.Name()) == "dual")
	}

	delete(c.JSONPathTokenizer.SingleTokens, '$')

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
	t.StringEscapes = []string{`\`, "'"}
	t.NumericEscapes = map[string]tokens.NumericEscape{
		"x": {Base: 16, MinDigits: 2, MaxDigits: 2, MaxValue: 0xFF},
		"u": {Base: 16, MinDigits: 4, MaxDigits: 4, MaxValue: 0xFFFF},
		"0": {Base: 8, MinDigits: 1, MaxDigits: 3, MaxValue: 0xFF},
	}
	t.DropUnknownEscapes = true
	t.HexStrings = []tokens.Delim{{Start: "x'", End: "'"}, {Start: "X'", End: "'"}}
	t.RawStrings = []tokens.Delim{tokens.Q("$$")}
	t.Comments = []tokens.Delim{{Start: "--"}, {Start: "//"}, {Start: "/*", End: "*/"}}
	t.NestedComments = false

	for k, v := range map[string]tokens.TokenType{
		"(+)":                 tokens.JoinMarker,
		"BYTEINT":             tokens.Int,
		"FILE://":             tokens.UriStart,
		"FILE FORMAT":         tokens.FileFormat,
		"GET":                 tokens.Get,
		"INTEGRATION":         tokens.Integration,
		"LS":                  tokens.List,
		"MATCH_CONDITION":     tokens.MatchCondition,
		"MATCH_RECOGNIZE":     tokens.MatchRecognize,
		"MINUS":               tokens.Except,
		"NCHAR VARYING":       tokens.Varchar,
		"PACKAGE":             tokens.Package,
		"POLICY":              tokens.Policy,
		"POOL":                tokens.Pool,
		"PUT":                 tokens.Put,
		"UNDROP":              tokens.Undrop,
		"REMOVE":              tokens.Command,
		"RM":                  tokens.Command,
		"ROLE":                tokens.Role,
		"RULE":                tokens.Rule,
		"SAMPLE":              tokens.TableSample,
		"SEMANTIC VIEW":       tokens.SemanticView,
		"SQL_DOUBLE":          tokens.Double,
		"SQL_VARCHAR":         tokens.Varchar,
		"STAGE":               tokens.Stage,
		"STORAGE INTEGRATION": tokens.StorageIntegration,
		"STREAMLIT":           tokens.Streamlit,
		"TAG":                 tokens.Tag,
		"TIMESTAMP_TZ":        tokens.Timestamptz,
		"TOP":                 tokens.Top,
		"VOLUME":              tokens.Volume,
		"WAREHOUSE":           tokens.Warehouse,
		// https://docs.snowflake.com/en/sql-reference/data-types-numeric#float
		// FLOAT is a synonym for DOUBLE in Snowflake
		"FLOAT": tokens.Double,
	} {
		t.Keywords[k] = v
	}
	delete(t.Keywords, "/*+")
	delete(t.Keywords, "GLOB")
	delete(t.Keywords, "RETURNING")

	t.SingleTokens['$'] = tokens.Parameter
	t.SingleTokens['!'] = tokens.Exclamation
	t.VarSingleTokens = map[rune]bool{'$': true}
	t.Commands = t.Commands.Without(tokens.Show)
}
