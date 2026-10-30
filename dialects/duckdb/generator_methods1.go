package duckdb

// DuckDBGenerator methods, part 1 (sqlglot/generators/duckdb.py: templates, datatype_sql ..
// timestamptzfromparts_sql).

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/internal/ghelp"
)

// Template for ZIPF transpilation - placeholders get replaced with actual parameters
var zipfTemplate = lazyParse(`
        WITH rand AS (SELECT :random_expr AS r),
        weights AS (
            SELECT i, 1.0 / POWER(i, :s) AS w
            FROM RANGE(1, :n + 1) AS t(i)
        ),
        cdf AS (
            SELECT i, SUM(w) OVER (ORDER BY i) / SUM(w) OVER () AS p
            FROM weights
        )
        SELECT MIN(i)
        FROM cdf
        WHERE p >= (SELECT r FROM rand)
        `)

// Template for NORMAL transpilation using Box-Muller transform
// mean + (stddev * sqrt(-2 * ln(u1)) * cos(2 * pi * u2))
var normalTemplate = lazyParse(":mean + (:stddev * SQRT(-2 * LN(GREATEST(:u1, 1e-10))) * COS(2 * PI() * :u2))")

// Template for generating a seeded pseudo-random value in [0, 1) from a hash
var seededRandomTemplate = lazyParse("(ABS(HASH(:seed)) % 1000000) / 1000000.0")

// Template for MAP_CAT transpilation - Snowflake semantics:
// 1. Returns NULL if either input is NULL
// 2. For duplicate keys, prefers non-NULL value (COALESCE(m2[k], m1[k]))
// 3. Filters out entries with NULL values from the result
var mapcatTemplate = lazyParse(`
        CASE
            WHEN :map1 IS NULL OR :map2 IS NULL THEN NULL
            ELSE MAP_FROM_ENTRIES(LIST_FILTER(LIST_TRANSFORM(
                LIST_DISTINCT(LIST_CONCAT(MAP_KEYS(:map1), MAP_KEYS(:map2))),
                __k -> STRUCT_PACK(key := __k, value := COALESCE(:map2[__k], :map1[__k]))
            ), __x -> __x.value IS NOT NULL))
        END
        `)

// Mappings for EXTRACT/DATE_PART transpilation
// Maps Snowflake specifiers unsupported in DuckDB to strftime format codes
var extractStrftimeMappings = map[string][2]string{
	"WEEKISO":       {"%V", "INTEGER"},
	"YEAROFWEEK":    {"%G", "INTEGER"},
	"YEAROFWEEKISO": {"%G", "INTEGER"},
	"NANOSECOND":    {"%n", "BIGINT"},
}

// Maps epoch-based specifiers to DuckDB epoch functions
var extractEpochMappings = map[string]string{
	"EPOCH_SECOND":      "EPOCH",
	"EPOCH_MILLISECOND": "EPOCH_MS",
	"EPOCH_MICROSECOND": "EPOCH_US",
	"EPOCH_NANOSECOND":  "EPOCH_NS",
}

// Template for BITMAP_CONSTRUCT_AGG transpilation
//
// Snowflake's BITMAP_CONSTRUCT_AGG aggregates integers into a compact binary bitmap.
// Supports values in range 0-32767, this version returns NULL if any value is out of range
// See: https://docs.snowflake.com/en/sql-reference/functions/bitmap_construct_agg
//
// Format 1 - Small bitmap (< 5 unique values): Length of 10 bytes
//
//	Bytes 0-1: Count of values as 2-byte big-endian integer
//	Bytes 2-9: Up to 4 values, each as 2-byte little-endian integers, zero-padded to 8 bytes
//
// Format 2 - Large bitmap (>= 5 unique values): Length of 10 + (2 * count) bytes
//
//	Bytes 0-9: Fixed header 0x08 followed by 9 zero bytes
//	Bytes 10+: Each value as 2-byte little-endian integer (no padding)
var bitmapConstructAggTemplate = lazyParse(`
        SELECT CASE
            WHEN l IS NULL OR LENGTH(l) = 0 THEN NULL
            WHEN LENGTH(l) != LENGTH(LIST_FILTER(l, __v -> __v BETWEEN 0 AND 32767)) THEN NULL
            WHEN LENGTH(l) < 5 THEN UNHEX(PRINTF('%04X', LENGTH(l)) || h || REPEAT('00', GREATEST(0, 4 - LENGTH(l)) * 2))
            ELSE UNHEX('08000000000000000000' || h)
        END
        FROM (
            SELECT l, COALESCE(LIST_REDUCE(
                LIST_TRANSFORM(l, __x -> PRINTF('%02X%02X', CAST(__x AS INT) & 255, (CAST(__x AS INT) >> 8) & 255)),
                (__a, __b) -> __a || __b, ''
            ), '') AS h
            FROM (SELECT LIST_SORT(LIST_DISTINCT(LIST(:arg) FILTER(NOT :arg IS NULL))) AS l)
        )
        `)

// Template for RANDSTR transpilation - placeholders get replaced with actual parameters
var randstrTemplate = lazyParse(`
        SELECT LISTAGG(
            SUBSTRING(
                '` + randstrCharPool + `',
                1 + CAST(FLOOR(random_value * 62) AS INT),
                1
            ),
            ''
        )
        FROM (
            SELECT (ABS(HASH(i + :seed)) % 1000) / 1000.0 AS random_value
            FROM RANGE(:length) AS t(i)
        )
        `)

// Template for MINHASH transpilation
// Computes k minimum hash values across aggregated data using DuckDB list functions
// Returns JSON matching Snowflake format: {"state": [...], "type": "minhash", "version": 1}
var minhashTemplate = lazyParse(`
        SELECT JSON_OBJECT('state', LIST(min_h ORDER BY seed), 'type', 'minhash', 'version', 1)
        FROM (
            SELECT seed, LIST_MIN(LIST_TRANSFORM(vals, __v -> HASH(CAST(__v AS VARCHAR) || CAST(seed AS VARCHAR)))) AS min_h
            FROM (SELECT LIST(:expr) AS vals), RANGE(0, :k) AS t(seed)
        )
        `)

// Template for MINHASH_COMBINE transpilation
// Combines multiple minhash signatures by taking element-wise minimum
var minhashCombineTemplate = lazyParse(`
        SELECT JSON_OBJECT('state', LIST(min_h ORDER BY idx), 'type', 'minhash', 'version', 1)
        FROM (
            SELECT
                pos AS idx,
                MIN(val) AS min_h
            FROM
                UNNEST(LIST(:expr)) AS _(sig),
                UNNEST(CAST(sig -> 'state' AS UBIGINT[])) WITH ORDINALITY AS t(val, pos)
            GROUP BY pos
        )
        `)

// Template for APPROXIMATE_SIMILARITY transpilation
// Computes multi-way Jaccard similarity: fraction of positions where ALL signatures agree
var approximateSimilarityTemplate = lazyParse(`
        SELECT CAST(SUM(CASE WHEN num_distinct = 1 THEN 1 ELSE 0 END) AS DOUBLE) / COUNT(*)
        FROM (
            SELECT pos, COUNT(DISTINCT h) AS num_distinct
            FROM (
                SELECT h, pos
                FROM UNNEST(LIST(:expr)) AS _(sig),
                     UNNEST(CAST(sig -> 'state' AS UBIGINT[])) WITH ORDINALITY AS s(h, pos)
            )
            GROUP BY pos
        )
        `)

// Template for ARRAYS_ZIP transpilation
// Snowflake pads to longest array; DuckDB LIST_ZIP truncates to shortest
// Uses RANGE + indexing to match Snowflake behavior
var arraysZipTemplate = lazyParse(`
        CASE WHEN :null_check THEN NULL
        WHEN :all_empty_check THEN [:empty_struct]
        ELSE LIST_TRANSFORM(RANGE(0, :max_len), __i -> :transform_struct)
        END
        `)

var uuidV5Template = lazyParse(`
        (SELECT
            LOWER(
                SUBSTR(h, 1, 8) || '-' ||
                SUBSTR(h, 9, 4) || '-' ||
                '5' || SUBSTR(h, 14, 3) || '-' ||
                FORMAT('{:02x}', CAST('0x' || SUBSTR(h, 17, 2) AS INT) & 63 | 128) || SUBSTR(h, 19, 2) || '-' ||
                SUBSTR(h, 21, 12)
            )
        FROM (
            SELECT SUBSTR(SHA1(UNHEX(REPLACE(:namespace, '-', '')) || ENCODE(:name, 'utf8')), 1, 32) AS h
        ))
        `)

// Shared bag semantics outer frame for ARRAY_EXCEPT and ARRAY_INTERSECTION.
// Each element is paired with its 1-based position via LIST_ZIP, then filtered
// by a comparison operator (supplied via :cond) that determines the operation:
//
//	EXCEPT (>):        keep the N-th occurrence only if N > count in arr2
//	INTERSECTION (<=): keep the N-th occurrence only if N <= count in arr2
//
// IS NOT DISTINCT FROM is used for NULL-safe element comparison.
var arrayBagTemplate = lazyParse(`
        CASE
            WHEN :arr1 IS NULL OR :arr2 IS NULL THEN NULL
            ELSE LIST_TRANSFORM(
                LIST_FILTER(
                    LIST_ZIP(:arr1, GENERATE_SERIES(1, LEN(:arr1))),
                    pair -> :cond
                ),
                pair -> pair[0]
            )
        END
        `)

var arrayExceptCondition = lazyParse(
	"LEN(LIST_FILTER(:arr1[1:pair[1]], e -> e IS NOT DISTINCT FROM pair[0]))" +
		" > LEN(LIST_FILTER(:arr2, e -> e IS NOT DISTINCT FROM pair[0]))")

var arrayIntersectionCondition = lazyParse(
	"LEN(LIST_FILTER(:arr1[1:pair[1]], e -> e IS NOT DISTINCT FROM pair[0]))" +
		" <= LEN(LIST_FILTER(:arr2, e -> e IS NOT DISTINCT FROM pair[0]))")

// Set semantics for ARRAY_EXCEPT. Deduplicates arr1 via LIST_DISTINCT, then
// filters out any element that appears at least once in arr2.
var arrayExceptSetTemplate = lazyParse(`
        CASE
            WHEN :arr1 IS NULL OR :arr2 IS NULL THEN NULL
            ELSE LIST_FILTER(
                LIST_DISTINCT(:arr1),
                e -> LEN(LIST_FILTER(:arr2, x -> x IS NOT DISTINCT FROM e)) = 0
            )
        END
        `)

// BigQuery's `x IN UNNEST(arr)` NULL semantics:
//
//	NULL IN UNNEST([1, 2])  -> NULL
//	3 IN UNNEST([1, NULL])  -> NULL
//	3 IN UNNEST([1, 2])     -> FALSE
//	1 IN UNNEST(NULL)       -> FALSE (not NULL)
//	1 IN UNNEST([])         -> FALSE
//
// The default `IN (SELECT UNNEST(...))` rewrite creates a correlated subquery
// that DuckDB rejects inside non-inner joins, so a CASE expression is used instead.
var inUnnestTemplate = lazyParse(`
        CASE
            WHEN :arr IS NULL OR ARRAY_LENGTH(:arr) = 0 THEN FALSE
            WHEN ARRAY_CONTAINS(:arr, :value) THEN TRUE
            WHEN :value IS NULL OR ARRAY_LENGTH(:arr) <> LIST_COUNT(:arr) THEN NULL
            ELSE FALSE
        END
        `)

var strtokToArrayTemplate = lazyParse(`
        CASE WHEN :delimiter IS NULL THEN NULL
        ELSE LIST_FILTER(
            REGEXP_SPLIT_TO_ARRAY(:string, CASE WHEN :delimiter = '' THEN '.^' ELSE CONCAT('[', :escaped, ']') END),
            x -> NOT x = ''
        ) END
        `)

// Template for STRTOK function transpilation. DuckDB itself doesn't have a strtok function.
var strtokTemplate = lazyParse(`
        CASE
            WHEN :delimiter = '' AND :string = '' THEN NULL
            WHEN :delimiter = '' AND :part_index = 1 THEN :string
            WHEN :delimiter = '' THEN NULL
            WHEN :part_index < 0 THEN NULL
            WHEN :string IS NULL OR :delimiter IS NULL OR :part_index IS NULL THEN NULL
            ELSE :base_func
        END
        `)

// Snowflake AUTO detects 3 DATE formats: YYYY-MM-DD (ISO-8601), MM/DD/YYYY, DD-MON-YYYY.
const (
	trycastDateSlashFmt = "%m/%d/%Y"
	trycastDateMonFmt   = "%d-%b-%Y"
)

type kv = map[string]any

func replace(t func() *exp.Expr, kwargs kv) *exp.Expr {
	return exp.ReplacePlaceholders(t(), nil, kwargs)
}

// fmtTime is self.format_time(expression): nil when Python returns None.
func fmtTime(g *gen, e *exp.Expr) any {
	if s := g.FormatTime(e, nil, nil); s != "" {
		return s
	}
	return nil
}

func datatypeSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if e.IsType(exp.DTypeArray) {
			return g.Expressions(e, sqlglot.ExprsOpts{Flat: true}) + "[" + g.Expressions(e, sqlglot.ExprsOpts{Key: "values", Flat: true}) + "]"
		}
		// Modifiers are not supported for TIME, [TIME | TIMESTAMP] WITH TIME ZONE
		if e.IsType(exp.DTypeTime, exp.DTypeTimetz, exp.DTypeTimestamptz, exp.DTypeTimestampltz) {
			e.Set("expressions", nil)
		}
		return sup(g, e)
	}
}

func arrayBagSQL(g *gen, condition, arr1, arr2 *exp.Expr) string {
	cond := exp.New(exp.Paren, "this", exp.ReplacePlaceholders(condition, nil, kv{"arr1": arr1, "arr2": arr2}))
	return g.SQL(replace(arrayBagTemplate, kv{"arr1": arr1, "arr2": arr2, "cond": cond}))
}

// timesliceSQL transforms Snowflake's TIME_SLICE to DuckDB's time_bucket.
func timesliceSQL(g *gen, e *exp.Expr) string {
	dateExpr := e.This()
	sliceLength := e.Expression()
	unit := e.Unit()
	kind := strings.ToUpper(e.Text("kind"))
	// Create INTERVAL expression: INTERVAL 'N' UNIT
	intervalExpr := exp.New(exp.Interval, "this", sliceLength, "unit", unit)
	// Create base time_bucket expression
	timeBucketExpr := exp.NewFunc("time_bucket", intervalExpr, dateExpr)
	// Check if we need the end of the slice (default is start)
	if kind != "END" {
		return g.SQL(timeBucketExpr)
	}
	// For 'END', add the interval to get end of slice
	addExpr := exp.New(exp.Add, "this", timeBucketExpr, "expression", intervalExpr.Copy())
	// If input is DATE type, cast result back to DATE to preserve type
	if dateExpr.IsType(exp.DTypeDate) {
		return g.SQL(exp.NewCast(addExpr, exp.DTypeDate))
	}
	return g.SQL(addExpr)
}

func unicodestringSQL(g *gen, e *exp.Expr) string {
	// DuckDB has no Unicode escape strings and doesn't read \u in a string, so the escapes
	// are decoded into the characters, e.g. U&'d\0061t\0061' and U&'d!+000061ta' UESCAPE '!'
	// are both 'data'
	escapeName := `\`
	if escape := e.Get("escape"); escape != nil {
		escapeName = escape.Name()
	}
	escapeChar := regexp.QuoteMeta(escapeName)
	pattern := regexp.MustCompile(escapeChar + `(?:\+([0-9a-fA-F]{6})|([0-9a-fA-F]{4})|(` + escapeChar + `))`)
	text := e.Name()
	var runes []rune
	last := 0
	for _, m := range pattern.FindAllStringSubmatchIndex(text, -1) {
		runes = append(runes, []rune(text[last:m[0]])...)
		last = m[1]
		if m[6] >= 0 {
			runes = append(runes, []rune(text[m[6]:m[7]])...)
			continue
		}
		hex := ""
		if m[2] >= 0 {
			hex = text[m[2]:m[3]]
		} else {
			hex = text[m[4]:m[5]]
		}
		n, _ := strconv.ParseInt(hex, 16, 32)
		runes = append(runes, rune(n))
	}
	runes = append(runes, []rune(text[last:])...)
	// A character outside the BMP can be written as a UTF-16 surrogate pair
	var out []rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if utf16.IsSurrogate(r) && i+1 < len(runes) {
			if c := utf16.DecodeRune(r, runes[i+1]); c != '�' {
				out = append(out, c)
				i++
				continue
			}
		}
		out = append(out, r)
	}
	return g.SQL(exp.LiteralString(string(out)))
}

func bytestringSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if !e.Bool("is_bytes") {
			return sup(g, e)
		}
		// Each char is a byte. Casting to BLOB decodes \xHH, so everything but printable ASCII
		// (including the backslash itself) is written as such
		var b strings.Builder
		for _, ch := range e.Str("this") {
			if ch >= 0x20 && ch < 0x7f && ch != '\\' {
				b.WriteRune(ch)
			} else {
				fmt.Fprintf(&b, "\\x%02x", ch)
			}
		}
		return g.SQL(exp.NewCast(exp.LiteralString(b.String()), exp.DTypeBinary))
	}
}

// bitmapbucketnumberSQL transpiles BITMAP_BUCKET_NUMBER function from Snowflake to DuckDB.
func bitmapbucketnumberSQL(g *gen, e *exp.Expr) string {
	value := e.This()
	positiveFormula := value.Minus(1).FloorDiv(32768).Plus(1)
	nonPositiveFormula := value.FloorDiv(32768)
	// CASE WHEN value > 0 THEN ((value - 1) // 32768) + 1 ELSE value // 32768 END
	caseExpr := exp.NewCase(nil).
		When(exp.New(exp.GT, "this", value, "expression", exp.LiteralNumber(0)), positiveFormula).
		Else(nonPositiveFormula)
	return g.SQL(caseExpr)
}

// bitmapbitpositionSQL transpiles Snowflake's BITMAP_BIT_POSITION to a DuckDB CASE expression.
func bitmapbitpositionSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	return g.SQL(exp.New(exp.Mod,
		"this", exp.New(exp.Paren, "this", exp.New(exp.If,
			"this", exp.New(exp.GT, "this", this, "expression", exp.LiteralNumber(0)),
			"true", this.Minus(exp.LiteralNumber(1)),
			"false", exp.New(exp.Abs, "this", this))),
		"expression", maxBitPosition()))
}

func bitmapconstructaggSQL(g *gen, e *exp.Expr) string {
	return "(" + g.SQL(replace(bitmapConstructAggTemplate, kv{"arg": e.This()})) + ")"
}

func unsupportedFallback(msg string) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		g.Unsupported(msg)
		return g.FunctionFallbackSQL(e)
	}
}

func decryptSQL(g *gen, e *exp.Expr) string {
	funcName := ghelp.Iff(e.Bool("safe"), "TRY_DECRYPT", "DECRYPT")
	g.Unsupported(funcName + " is not supported in DuckDB")
	return g.FunctionFallbackSQL(e)
}

func decryptrawSQL(g *gen, e *exp.Expr) string {
	funcName := ghelp.Iff(e.Bool("safe"), "TRY_DECRYPT_RAW", "DECRYPT_RAW")
	g.Unsupported(funcName + " is not supported in DuckDB")
	return g.FunctionFallbackSQL(e)
}

func jarowinklersimilaritySQL(g *gen, e *exp.Expr) string {
	this := e.This()
	expr := e.Expression()
	if e.Bool("case_insensitive") {
		this = exp.New(exp.Upper, "this", this)
		expr = exp.New(exp.Upper, "this", expr)
	}
	result := exp.NewFunc("JARO_WINKLER_SIMILARITY", this, expr)
	if e.Bool("integer_scale") {
		result = exp.NewCast(result.Times(100), "INTEGER")
	}
	return g.SQL(result)
}

// randstrSQL transpiles Snowflake's RANDSTR using a deterministic hash-based random.
func randstrSQL(g *gen, e *exp.Expr) string {
	length := e.This()
	generator := e.Get("generator")
	var seedValue *exp.Expr
	if generator != nil {
		if generator.IsKind(exp.Rand) {
			// If it's RANDOM(), use its seed if available, otherwise use RANDOM() itself
			seedValue = generator.This()
			if seedValue == nil {
				seedValue = generator
			}
		} else {
			// Const/int or other expression - use as seed directly
			seedValue = generator
		}
	} else {
		// No generator specified, use default seed (arbitrary but deterministic)
		seedValue = exp.LiteralNumber(randstrSeed)
	}
	return "(" + g.SQL(replace(randstrTemplate, kv{"seed": seedValue, "length": length})) + ")"
}

var reduceSQL = sqlglot.UnsupportedArgs(func(g *gen, e *exp.Expr) string {
	return g.Func("list_reduce", e.This(), e.Get("merge"), e.Get("initial"))
}, "finish")

func hashRandomExpr(gen *exp.Expr) *exp.Expr {
	// (ABS(HASH(seed)) % 1000000) / 1000000.0
	return exp.New(exp.Div,
		"this", exp.New(exp.Paren, "this", exp.New(exp.Mod,
			"this", exp.New(exp.Abs, "this", exp.New(exp.Anonymous, "this", "HASH", "expressions", []*exp.Expr{gen})),
			"expression", exp.LiteralNumber(1000000))),
		"expression", exp.LiteralNumber("1000000.0"))
}

// zipfSQL transpiles Snowflake's ZIPF to DuckDB using CDF-based inverse sampling.
func zipfSQL(g *gen, e *exp.Expr) string {
	s := e.This()
	n := e.Get("elementcount")
	gen := e.Get("gen")
	var randomExpr *exp.Expr
	if !gen.IsKind(exp.Rand) {
		randomExpr = hashRandomExpr(gen.Copy())
	} else {
		// Use RANDOM() for non-deterministic output
		randomExpr = exp.New(exp.Rand)
	}
	return "(" + g.SQL(replace(zipfTemplate, kv{"s": s, "n": n, "random_expr": randomExpr})) + ")"
}

// tobinarySQL handles TO_BINARY and TRY_TO_BINARY.
func tobinarySQL(g *gen, e *exp.Expr) string {
	value := e.This()
	formatArg := e.Get("format")
	isSafe := e.Bool("safe")
	if formatArg == nil && !isBinary(e) {
		return g.Func(ghelp.Iff(isSafe, "TRY_TO_BINARY", "TO_BINARY"), value)
	}
	// Snowflake defaults to HEX encoding when no format is specified
	f := "HEX"
	if formatArg != nil {
		f = strings.ToUpper(formatArg.Name())
	}
	var result string
	switch f {
	case "UTF-8", "UTF8":
		// DuckDB ENCODE always uses UTF-8, no charset parameter needed
		result = g.Func("ENCODE", value)
	case "BASE64":
		result = g.Func("FROM_BASE64", value)
	case "HEX":
		result = g.Func("UNHEX", value)
	default:
		if isSafe {
			return g.SQL(exp.NewNull())
		}
		g.Unsupported(fmt.Sprintf("format %s is not supported", f))
		result = g.Func("TO_BINARY", value)
	}
	if isSafe {
		return "TRY(" + result + ")"
	}
	return result
}

func tonumberSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		precision, scale := e.Get("precision"), e.Get("scale")
		if e.Get("format") == nil && precision != nil && scale != nil {
			to := exp.MustDataTypeBuild(fmt.Sprintf("DECIMAL(%s, %s)", precision.Name(), scale.Name()), "duckdb", false)
			if e.Bool("safe") {
				return g.SQL(exp.New(exp.TryCast, "this", e.This(), "to", to))
			}
			return g.SQL(exp.NewCast(e.This(), to, exp.WithDialect("duckdb")))
		}
		return sup(g, e)
	}
}

// greatestLeastSQL handles GREATEST/LEAST functions with dialect-aware NULL behavior.
func greatestLeastSQL(g *gen, e *exp.Expr) string {
	allArgs := append([]*exp.Expr{e.This()}, e.Expressions()...)
	fallbackSQL := g.FunctionFallbackSQL(e)
	if e.Bool("ignore_nulls") {
		// DuckDB/PostgreSQL behavior: use native GREATEST/LEAST (ignores NULLs)
		return fallbackSQL
	}
	// return NULL if any argument is NULL
	var checks []any
	for _, arg := range allArgs {
		checks = append(checks, arg.IsOp(exp.NewNull()))
	}
	checks = append(checks, exp.WithCopy(false))
	caseExpr := exp.NewCase(nil).When(exp.NewOr(checks...), exp.NewNull(), exp.WithCopy(false))
	caseExpr.Set("default", fallbackSQL)
	return g.SQL(caseExpr)
}

func generatorSQL(g *gen, e *exp.Expr) string {
	// Transpile Snowflake GENERATOR to DuckDB range()
	rowcount := e.Get("rowcount")
	if e.Bool("time_limit") {
		g.Unsupported("GENERATOR TIMELIMIT parameter is not supported in DuckDB")
	}
	if rowcount == nil {
		g.Unsupported("GENERATOR without ROWCOUNT is not supported in DuckDB")
		return g.Func("range", exp.LiteralNumber(0))
	}
	return g.Func("range", rowcount)
}

func lambdaSQL(sup func(g *gen, e *exp.Expr, arrowSep string, wrap bool) string) func(g *gen, e *exp.Expr, arrowSep string, wrap bool) string {
	return func(g *gen, e *exp.Expr, arrowSep string, wrap bool) string {
		prefix := ""
		if ghelp.VersionGE(g.D.Version, 1, 3) {
			prefix = "LAMBDA "
			arrowSep = ":"
			wrap = false
		}
		return prefix + sup(g, e, arrowSep, wrap)
	}
}

func showSQL(g *gen, e *exp.Expr) string {
	return "SHOW " + e.Name() + ghelp.Pfx(" FROM ", g.SQLArg(e, "from_"))
}

func soundexSQL(g *gen, e *exp.Expr) string {
	g.Unsupported("SOUNDEX is not supported in DuckDB")
	return g.Func("SOUNDEX", e.This())
}

func placeholderSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		if e.This().IsKind(exp.Identifier) {
			return "$" + g.SQLArg(e, "this")
		}
		return sup(g, e)
	}
}

func installSQL(g *gen, e *exp.Expr) string {
	force := ghelp.Iff(e.Bool("force"), "FORCE ", "")
	this := g.SQLArg(e, "this")
	fromClause := ""
	if from := e.Get("from_"); from != nil {
		// Python formats the expression with str(), i.e. the default dialect
		fromClause = " FROM " + from.SQL()
	}
	return force + "INSTALL " + this + fromClause
}

func approxtopkSQL(g *gen, e *exp.Expr) string {
	g.Unsupported("APPROX_TOP_K cannot be transpiled to DuckDB due to incompatible return types. ")
	return g.FunctionFallbackSQL(e)
}

func strpositionSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	substr := e.Get("substr")
	position := e.Get("position")
	// For BINARY/BLOB: DuckDB's STRPOS doesn't support BLOB types
	// Convert to HEX strings, use STRPOS, then convert hex position to byte position
	if isBinary(this) {
		// Build expression: STRPOS(HEX(haystack), HEX(needle))
		hexStrpos := exp.New(exp.StrPosition, "this", exp.New(exp.Hex, "this", this), "substr", exp.New(exp.Hex, "this", substr))
		return g.SQL(exp.NewCast(hexStrpos.Plus(1).Over(2), exp.DTypeInt))
	}
	// For VARCHAR: handle clamp_position
	if e.Bool("clamp_position") && position != nil {
		e = e.Copy()
		e.Set("position", exp.New(exp.If,
			"this", exp.New(exp.LTE, "this", position, "expression", exp.LiteralNumber(0)),
			"true", exp.LiteralNumber(1),
			"false", position.Copy()))
	}
	return sqlglot.StrpositionSQL(g, e, "STRPOS", false, false, true)
}

func substringSQL(g *gen, e *exp.Expr) string {
	if e.Bool("zero_start") {
		start := e.Get("start")
		length := e.Get("length")
		if start != nil {
			start = exp.New(exp.If, "this", start.Eq(0), "true", exp.LiteralNumber(1), "false", start)
		}
		if length != nil {
			length = exp.New(exp.If, "this", length.Lt(0), "true", exp.LiteralNumber(0), "false", length)
		}
		return g.Func("SUBSTRING", e.This(), start, length)
	}
	return g.FunctionFallbackSQL(e)
}

func strtotimeSQL(g *gen, e *exp.Expr) string {
	// Check if target_type requires TIMESTAMPTZ (for LTZ/TZ variants)
	targetType := e.Get("target_type")
	needsTZ := targetType != nil && (targetType.DataTypeThis() == exp.DTypeTimestampltz || targetType.DataTypeThis() == exp.DTypeTimestamptz)
	value, formattedTime := strptimeDefaultYear(g, e)
	if e.Bool("safe") {
		castType := ghelp.Iff(needsTZ, exp.DTypeTimestamptz, exp.DTypeTimestamp)
		return g.SQL(exp.NewCast(g.Func("TRY_STRPTIME", value, formattedTime), castType))
	}
	baseSQL := g.Func("STRPTIME", value, formattedTime)
	if needsTZ {
		return g.SQL(exp.NewCast(baseSQL, exp.New(exp.DataType, "this", exp.DTypeTimestamptz)))
	}
	return baseSQL
}

func strtodateSQL(g *gen, e *exp.Expr) string {
	value, formattedTime := strptimeDefaultYear(g, e)
	functionName := ghelp.Iff(e.Bool("safe"), "TRY_STRPTIME", "STRPTIME")
	return g.SQL(exp.NewCast(g.Func(functionName, value, formattedTime), exp.New(exp.DataType, "this", exp.DTypeDate)))
}

func strptimeDefaultYear(g *gen, e *exp.Expr) (any, any) {
	var value any = e.This()
	formattedTime := fmtTime(g, e)
	if defaultYear := e.Get("default_year"); defaultYear != nil {
		value = exp.New(exp.DPipe, "this", exp.LiteralString(defaultYear.Name()+" "), "expression", value)
		formattedTime = exp.New(exp.DPipe, "this", exp.LiteralString("%Y "), "expression", formattedTime)
	}
	return value, formattedTime
}

func parsedatetimeSQL(g *gen, e *exp.Expr) string {
	value, formattedTime := strptimeDefaultYear(g, e)
	return g.Func("STRPTIME", value, formattedTime)
}

func parsetimeSQL(g *gen, e *exp.Expr) string {
	formattedTime := fmtTime(g, e)
	return g.SQL(exp.NewCast(g.Func("STRPTIME", e.This(), formattedTime), exp.New(exp.DataType, "this", exp.DTypeTime)))
}

func tsordstotimeSQL(g *gen, e *exp.Expr) string {
	this := e.This()
	timeFormat := g.FormatTime(e, nil, nil)
	safe := e.Bool("safe")
	timeType := exp.MustDataTypeBuild("TIME", "duckdb", false)
	castKind := ghelp.Iff(safe, exp.TryCast, exp.Cast)
	if timeFormat != "" {
		funcName := ghelp.Iff(safe, "TRY_STRPTIME", "STRPTIME")
		strptime := exp.New(exp.Anonymous, "this", funcName, "expressions", []any{this, timeFormat})
		return g.SQL(exp.New(castKind, "this", strptime, "to", timeType))
	}
	if this.IsKind(exp.TsOrDsToTime) || this.IsType(exp.DTypeTime) {
		return g.SQL(this)
	}
	return g.SQL(exp.New(castKind, "this", this, "to", timeType))
}

func currentdateSQL(g *gen, e *exp.Expr) string {
	if e.This() == nil {
		return "CURRENT_DATE"
	}
	expr := exp.New(exp.Cast,
		"this", exp.New(exp.AtTimeZone, "this", exp.New(exp.CurrentTimestamp), "zone", e.This()),
		"to", exp.New(exp.DataType, "this", exp.DTypeDate))
	return g.SQL(expr)
}

func checkjsonSQL(g *gen, e *exp.Expr) string {
	arg := e.This()
	return g.SQL(exp.NewCase(nil).
		When(exp.NewOr(arg.IsOp(exp.New(exp.Null)), arg.Eq(""), exp.NewFunc("json_valid", arg)), exp.NewNull()).
		Else(exp.LiteralString("Invalid JSON")))
}

func parsejsonSQL(g *gen, e *exp.Expr) string {
	arg := e.This()
	if e.Bool("safe") {
		return g.SQL(exp.NewCase(nil).
			When(exp.NewFunc("json_valid", arg), exp.NewCast(arg.Copy(), "JSON")).
			Else(exp.NewNull()))
	}
	return g.Func("JSON", arg)
}

func unicodeSQL(g *gen, e *exp.Expr) string {
	if e.Bool("empty_is_zero") {
		return g.SQL(exp.NewCase(nil).
			When(e.This().Eq(exp.LiteralString("")), exp.LiteralNumber(0)).
			Else(exp.New(exp.Anonymous, "this", "UNICODE", "expressions", []*exp.Expr{e.This()})))
	}
	return g.Func("UNICODE", e.This())
}

func stripnullvalueSQL(g *gen, e *exp.Expr) string {
	return g.SQL(exp.NewCase(nil).
		When(exp.NewFunc("json_type", e.This()).Eq("NULL"), exp.NewNull()).
		Else(e.This()))
}

func truncSQL(g *gen, e *exp.Expr) string {
	decimals := e.Get("decimals")
	if e.Bool("fractions_supported") && decimals != nil && !decimals.IsType(exp.DTypeInt) {
		decimals = exp.NewCast(decimals, exp.DTypeInt, exp.WithDialect("duckdb"))
	}
	return g.Func("TRUNC", e.This(), decimals)
}

// normalSQL transpiles Snowflake's NORMAL(mean, stddev, gen) using the Box-Muller transform.
func normalSQL(g *gen, e *exp.Expr) string {
	mean := e.This()
	stddev := e.Get("stddev")
	gen := e.Get("gen")
	// Build two uniform random values [0, 1) for Box-Muller transform
	var u1, u2 *exp.Expr
	if gen.IsKind(exp.Rand) && gen.This() == nil {
		u1, u2 = exp.New(exp.Rand), exp.New(exp.Rand)
	} else {
		// Seeded: derive two values using HASH with different inputs
		seed := gen
		if gen.IsKind(exp.Rand) {
			seed = gen.This()
		}
		u1 = replace(seededRandomTemplate, kv{"seed": seed})
		u2 = replace(seededRandomTemplate, kv{"seed": exp.New(exp.Add, "this", seed.Copy(), "expression", exp.LiteralNumber(1))})
	}
	return g.SQL(replace(normalTemplate, kv{"mean": mean, "stddev": stddev, "u1": u1, "u2": u2}))
}

// uniformSQL transpiles Snowflake's UNIFORM(min, max, gen).
func uniformSQL(g *gen, e *exp.Expr) string {
	minVal := e.This()
	maxVal := e.Expression()
	gen := e.Get("gen")
	if gen == nil {
		gen = e.Get("seed")
	}
	if gen == nil {
		gen = exp.New(exp.Rand)
	}
	// Determine if result should be integer (both bounds are integers).
	isIntResult := minVal.IsInt() && maxVal.IsInt()
	// Build the random value expression [0, 1)
	var randomExpr *exp.Expr
	if !gen.IsKind(exp.Rand) {
		randomExpr = hashRandomExpr(gen)
	} else {
		randomExpr = exp.New(exp.Rand)
	}
	// Build: min + random * (max - min [+ 1 for int])
	rangeExpr := exp.New(exp.Sub, "this", maxVal, "expression", minVal)
	if isIntResult {
		rangeExpr = exp.New(exp.Add, "this", rangeExpr, "expression", exp.LiteralNumber(1))
	}
	result := exp.New(exp.Add,
		"this", minVal,
		"expression", exp.New(exp.Mul, "this", randomExpr, "expression", exp.New(exp.Paren, "this", rangeExpr)))
	if isIntResult {
		result = exp.New(exp.Cast, "this", exp.New(exp.Floor, "this", result), "to", exp.DTypeBigint.IntoExpr())
	}
	return g.SQL(result)
}

func timefrompartsSQL(g *gen, e *exp.Expr) string {
	nano := e.Get("nano")
	// Snowflake's TIME_FROM_PARTS supports overflow
	if e.Bool("overflow") {
		hour, minute, sec := e.Get("hour"), e.Get("min"), e.Get("sec")
		// Check if values are within normal ranges - use MAKE_TIME for efficiency
		if nano == nil && hour.IsInt() && minute.IsInt() && sec.IsInt() {
			h, m, s := toInt(hour.MustToPy()), toInt(minute.MustToPy()), toInt(sec.MustToPy())
			if 0 <= h && h <= 23 && 0 <= m && m <= 59 && 0 <= s && s <= 59 {
				return renameFunc("MAKE_TIME")(g, e)
			}
		}
		// Overflow or nanoseconds detected - use INTERVAL arithmetic
		if nano != nil {
			sec = sec.Plus(nano.Pop().Over(exp.LiteralNumber("1000000000.0")))
		}
		totalSeconds := hour.Times(exp.LiteralNumber(3600)).Plus(minute.Times(exp.LiteralNumber(60))).Plus(sec)
		return g.SQL(exp.New(exp.Add,
			"this", exp.New(exp.Cast, "this", exp.LiteralString("00:00:00"), "to", exp.DTypeTime.IntoExpr()),
			"expression", exp.New(exp.Interval, "this", totalSeconds, "unit", exp.NewVar("SECOND"))))
	}
	// Default: MAKE_TIME
	if nano != nil {
		e.Set("sec", e.Get("sec").Plus(nano.Pop().Over(exp.LiteralNumber("1000000000.0"))))
	}
	return renameFunc("MAKE_TIME")(g, e)
}

// extractSQL transpiles EXTRACT/DATE_PART, handling specifiers not natively supported.
func extractSQL(sup sqlglot.GenFunc) sqlglot.GenFunc {
	return func(g *gen, e *exp.Expr) string {
		this := e.This()
		datetimeExpr := e.Expression()
		// TIMESTAMPTZ extractions may produce different results between Snowflake and DuckDB
		// because Snowflake applies server timezone while DuckDB uses local timezone
		if datetimeExpr.IsType(exp.DTypeTimestamptz, exp.DTypeTimestampltz) {
			g.Unsupported("EXTRACT from TIMESTAMPTZ / TIMESTAMPLTZ may produce different results due to timezone handling differences")
		}
		partName := strings.ToUpper(this.Name())
		if m, ok := extractStrftimeMappings[partName]; ok {
			f, castType := m[0], m[1]
			// Problem: strftime doesn't accept TIME and there's no NANOSECOND function
			// So, for NANOSECOND with TIME, fallback to MICROSECOND * 1000
			if partName == "NANOSECOND" && datetimeExpr.IsType(exp.DTypeTime, exp.DTypeTimetz) {
				g.Unsupported("Parameter NANOSECOND is not supported with TIME type in DuckDB")
				return g.SQL(exp.NewCast(
					exp.New(exp.Mul,
						"this", exp.New(exp.Extract, "this", exp.NewVar("MICROSECOND"), "expression", datetimeExpr),
						"expression", exp.LiteralNumber(1000)),
					exp.MustDataTypeBuild(castType, "duckdb", false)))
			}
			// For NANOSECOND, cast to TIMESTAMP_NS to preserve nanosecond precision
			strftimeInput := datetimeExpr
			if partName == "NANOSECOND" {
				strftimeInput = exp.NewCast(datetimeExpr, exp.DTypeTimestampNs)
			}
			return g.SQL(exp.NewCast(
				exp.New(exp.Anonymous, "this", "STRFTIME", "expressions", []*exp.Expr{strftimeInput, exp.LiteralString(f)}),
				exp.MustDataTypeBuild(castType, "duckdb", false)))
		}
		if funcName, ok := extractEpochMappings[partName]; ok {
			result := exp.New(exp.Anonymous, "this", funcName, "expressions", []*exp.Expr{datetimeExpr})
			// EPOCH returns float, cast to BIGINT for integer result
			if partName == "EPOCH_SECOND" {
				result = exp.NewCast(result, exp.MustDataTypeBuild("BIGINT", "duckdb", false))
			}
			return g.SQL(result)
		}
		return sup(g, e)
	}
}

func timestampfrompartsSQL(g *gen, e *exp.Expr) string {
	// Check if this is the date/time expression form: TIMESTAMP_FROM_PARTS(date_expr, time_expr)
	dateExpr, timeExpr := e.This(), e.Expression()
	if dateExpr != nil && timeExpr != nil {
		// In DuckDB, DATE + TIME produces TIMESTAMP
		return g.SQL(exp.New(exp.Add, "this", dateExpr, "expression", timeExpr))
	}
	// Component-based form: TIMESTAMP_FROM_PARTS(year, month, day, hour, minute, second, ...)
	sec := e.Get("sec")
	if sec == nil {
		return renameFunc("MAKE_TIMESTAMP")(g, e)
	}
	milli := e.Get("milli")
	if milli != nil {
		sec = sec.Plus(milli.Pop().Over(exp.LiteralNumber("1000.0")))
	}
	nano := e.Get("nano")
	if nano != nil {
		sec = sec.Plus(nano.Pop().Over(exp.LiteralNumber("1000000000.0")))
	}
	if milli != nil || nano != nil {
		e.Set("sec", sec)
	}
	if zone := e.Get("zone"); zone != nil {
		return g.SQL(exp.New(exp.AtTimeZone, "this", e, "zone", zone.Pop()))
	}
	return renameFunc("MAKE_TIMESTAMP")(g, e)
}

var timestampltzfrompartsSQL = sqlglot.UnsupportedArgs(func(g *gen, e *exp.Expr) string {
	// Pop nano so rename_func only passes args that MAKE_TIMESTAMP accepts
	if nano := e.Get("nano"); nano != nil {
		nano.Pop()
	}
	timestamp := renameFunc("MAKE_TIMESTAMP")(g, e)
	return "CAST(" + timestamp + " AS TIMESTAMPTZ)"
}, "nano")

var timestamptzfrompartsSQL = sqlglot.UnsupportedArgs(func(g *gen, e *exp.Expr) string {
	// Extract zone before popping
	zone := e.Get("zone")
	// Pop zone and nano so rename_func only passes args that MAKE_TIMESTAMP accepts
	if zone != nil {
		zone = zone.Pop()
	}
	if nano := e.Get("nano"); nano != nil {
		nano.Pop()
	}
	timestamp := renameFunc("MAKE_TIMESTAMP")(g, e)
	if zone != nil {
		// Use AT TIME ZONE to apply the explicit timezone
		return timestamp + " AT TIME ZONE " + g.SQL(zone)
	}
	return timestamp
}, "nano")

func afterLimitModifiers(g *gen, e *exp.Expr) []string {
	locks := g.Expressions(e, sqlglot.ExprsOpts{Key: "locks", Sep: " "})
	return []string{ghelp.Pfx(" ", locks)}
}
