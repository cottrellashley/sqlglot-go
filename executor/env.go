package executor

// Port of sqlglot/executor/env.py: the functions available to executed expressions.

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// Func is an executor function: it receives the evaluated arguments.
type Func func(args ...any) any

func nullIfAny(f Func) Func {
	return func(args ...any) any {
		for _, a := range args {
			if a == nil {
				return nil
			}
		}
		return f(args...)
	}
}

// nullIfFirst returns NULL when one of the first n arguments is NULL.
func nullIfFirst(n int, f Func) Func {
	return func(args ...any) any {
		for i := 0; i < n && i < len(args); i++ {
			if args[i] == nil {
				return nil
			}
		}
		return f(args...)
	}
}

func values(v any) []any {
	switch x := v.(type) {
	case rangeValues:
		return x
	case []any:
		return x
	case valueSet:
		return x.items
	}
	typeErrorf("'%s' object is not iterable", pyTypeName(v))
	return nil
}

// filterNulls mirrors env.filter_nulls: the aggregation receives the non-NULL values.
func filterNulls(f func([]any) any, emptyNull bool) Func {
	return func(args ...any) any {
		var filtered []any
		for _, v := range values(args[0]) {
			if v != nil {
				filtered = append(filtered, v)
			}
		}
		if len(filtered) == 0 && emptyNull {
			return nil
		}
		return f(filtered)
	}
}

// valueSet is a Python set (COUNT(DISTINCT x) aggregates over set(x)).
type valueSet struct{ items []any }

func newValueSet(items []any) valueSet {
	seen := map[string]bool{}
	var out []any
	for _, it := range items {
		k := HashKey(it)
		if !seen[k] {
			seen[k] = true
			out = append(out, it)
		}
	}
	return valueSet{out}
}

func sqlAnd(left, right func() any) any {
	l := left()
	if l != nil && !Truthy(l) {
		return false
	}
	r := right()
	if r != nil && !Truthy(r) {
		return false
	}
	if l == nil || r == nil {
		return nil
	}
	return true
}

func sqlOr(left, right func() any) any {
	l := left()
	if l != nil && Truthy(l) {
		return true
	}
	r := right()
	if r != nil && Truthy(r) {
		return true
	}
	if l == nil || r == nil {
		return nil
	}
	return false
}

func sqlNot(args ...any) any {
	if args[0] == nil {
		return nil
	}
	return !Truthy(args[0])
}

func sqlIn(args ...any) any {
	value := args[0]
	if value == nil {
		return nil
	}
	hasNull := false
	for _, c := range args[1:] {
		if c == nil {
			hasNull = true
		} else if Equal(value, c) {
			return true
		}
	}
	if hasNull {
		return nil
	}
	return false
}

func strPosition(args ...any) any {
	this, substr := Str(args[0]), Str(args[1])
	runes := []rune(this)
	start := 0
	if len(args) > 2 && args[2] != nil {
		start = asInt(args[2]) - 1
		if start < 0 {
			start = max(0, len(runes)+start)
		}
	}
	if start > len(runes) {
		return 0
	}
	i := strings.Index(string(runes[start:]), substr)
	if i < 0 {
		return 0
	}
	return utf8.RuneCountInString(string(runes[start:])[:i]) + start + 1
}

func substring(args ...any) any {
	this := []rune(asString(args[0]))
	if len(args) < 2 || args[1] == nil {
		return string(this)
	}
	start := asInt(args[1])
	switch {
	case start == 0:
		return ""
	case start < 0:
		start = len(this) + start
	default:
		start--
	}
	end := len(this)
	if len(args) > 2 && args[2] != nil {
		end = start + asInt(args[2])
	}
	return string(pySlice(this, start, end))
}

// pySlice mirrors s[start:end] with Python's clamping of negative and large indices.
func pySlice[T any](s []T, start, end int) []T {
	n := len(s)
	norm := func(i int) int {
		if i < 0 {
			i += n
			if i < 0 {
				i = 0
			}
		}
		if i > n {
			i = n
		}
		return i
	}
	start, end = norm(start), norm(end)
	if start >= end {
		return []T{}
	}
	return s[start:end]
}

func asString(v any) string {
	s, ok := v.(string)
	if !ok {
		typeErrorf("expected a str, got '%s'", pyTypeName(v))
	}
	return s
}

func asInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case *big.Int:
		return int(x.Int64())
	}
	typeErrorf("'%s' object cannot be interpreted as an integer", pyTypeName(v))
	return 0
}

func parseISODate(s string) Date {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		t, err = time.Parse("20060102", s)
	}
	if err != nil {
		panic(errValuef("Invalid isoformat string: '%s'", s))
	}
	return Date{t}
}

func parseISODatetime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04Z07:00", "2006-01-02T15:04Z07:00", "2006-01-02 15:04", "2006-01-02T15:04",
		"2006-01-02 15Z07:00", "2006-01-02 15", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	panic(errValuef("Invalid isoformat string: '%s'", s))
}

func parseISOTime(s string) Time {
	for _, layout := range []string{"15:04:05.999999", "15:04:05", "15:04", "15"} {
		if t, err := time.Parse(layout, s); err == nil {
			return Time{t.Hour(), t.Minute(), t.Second(), t.Nanosecond() / 1000}
		}
	}
	panic(errValuef("Invalid isoformat string: '%s'", s))
}

type valueError struct{ msg string }

func (e *valueError) Error() string { return e.msg }

func errValuef(format string, args ...any) error {
	return &valueError{strings.TrimSpace(sprintf(format, args...))}
}

func sprintf(format string, args ...any) string {
	return strings.TrimSpace(fmtSprintf(format, args...))
}

// cast mirrors env.cast.
func cast(args ...any) any {
	this := args[0]
	to := args[1].(exp.DType)
	switch to {
	case exp.DTypeDate:
		switch x := this.(type) {
		case time.Time:
			return NewDate(x.Year(), x.Month(), x.Day())
		case Date:
			return x
		case string:
			return parseISODate(x)
		}
	case exp.DTypeTime:
		switch x := this.(type) {
		case time.Time:
			return Time{x.Hour(), x.Minute(), x.Second(), x.Nanosecond() / 1000}
		case Time:
			return x
		case string:
			return parseISOTime(x)
		}
	case exp.DTypeDatetime, exp.DTypeTimestamp:
		switch x := this.(type) {
		case time.Time:
			return x
		case Date:
			return x.Time
		case string:
			return parseISODatetime(x)
		}
	}
	switch {
	case to == exp.DTypeBoolean:
		return Truthy(this)
	case exp.DataTypeTextTypes.Has(to):
		return Str(this)
	case to == exp.DTypeFloat || to == exp.DTypeDouble:
		return pyFloat(this)
	case exp.DataTypeNumericTypes.Has(to):
		return pyInt(this)
	}
	panic(&notImplementedError{sprintf("Casting %s to '%s' not implemented.", Str(this), string(to))})
}

type notImplementedError struct{ msg string }

func (e *notImplementedError) Error() string { return e.msg }

// pyFloat mirrors float(value).
func pyFloat(v any) any {
	switch x := v.(type) {
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			panic(errValuef("could not convert string to float: %s", Repr(x)))
		}
		return f
	case bool, int, *big.Int, float64:
		n, _ := numeric(x)
		return toFloat(n)
	}
	typeErrorf("float() argument must be a string or a real number, not '%s'", pyTypeName(v))
	return nil
}

// pyInt mirrors int(value).
func pyInt(v any) any {
	switch x := v.(type) {
	case string:
		s := strings.ReplaceAll(strings.TrimSpace(x), "_", "")
		if b, ok := new(big.Int).SetString(s, 10); ok {
			return normInt(b)
		}
		panic(errValuef("invalid literal for int() with base 10: %s", Repr(x)))
	case bool:
		if x {
			return 1
		}
		return 0
	case int, *big.Int:
		return x
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			panic(errValuef("cannot convert float %s to integer", floatRepr(x)))
		}
		b, _ := new(big.Float).SetFloat64(math.Trunc(x)).Int(nil)
		return normInt(b)
	}
	typeErrorf("int() argument must be a string, a bytes-like object or a real number, not '%s'", pyTypeName(v))
	return nil
}

// ordered mirrors env.ordered.
func ordered(this any, desc, nullsFirst bool) any {
	if this == nil {
		rank := 1
		if nullsFirst {
			rank = 0
		}
		return orderedKey{rank: rank, null: true}
	}
	rank := 0
	if nullsFirst {
		rank = 1
	}
	return orderedKey{rank: rank, value: this, reverse: desc}
}

func like(this, pattern string, ignoreCase bool) bool {
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, "_", ".")
	re = strings.ReplaceAll(re, "%", ".*")
	flags := "(?s)"
	if ignoreCase {
		flags = "(?si)"
	}
	return regexp.MustCompile(flags + "^(?:" + re + ")$").MatchString(this)
}

func intdiv(args ...any) any {
	this, e := args[0], args[1]
	_, ti := this.(int)
	_, ei := e.(int)
	if !ti || !ei {
		return pyInt(div(this, e))
	}
	a, b := this.(int), e.(int)
	if b == 0 {
		panic(&zeroDivisionError{"integer division or modulo by zero"})
	}
	q := absInt(a) / absInt(b)
	if (a < 0) == (b < 0) {
		return q
	}
	return -q
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

type zeroDivisionError struct{ msg string }

func (e *zeroDivisionError) Error() string { return e.msg }

var timePartSingulars = map[string]bool{
	"MICROSECONDS": true, "MILLISECONDS": true, "SECONDS": true, "MINUTES": true, "HOURS": true,
	"DAYS": true, "WEEKS": true,
}

// interval mirrors env.interval: a timedelta of float(this) units.
func interval(args ...any) any {
	n := toFloat(pyFloat(args[0]))
	unit := strings.ToUpper(Str(args[1]))
	if timePartSingulars[unit+"S"] {
		unit += "S"
	}
	var d float64
	switch unit {
	case "MICROSECONDS":
		d = n * float64(time.Microsecond)
	case "MILLISECONDS":
		d = n * float64(time.Millisecond)
	case "SECONDS":
		d = n * float64(time.Second)
	case "MINUTES":
		d = n * float64(time.Minute)
	case "HOURS":
		d = n * float64(time.Hour)
	case "DAYS":
		d = n * 24 * float64(time.Hour)
	case "WEEKS":
		d = n * 7 * 24 * float64(time.Hour)
	default:
		typeErrorf("'%s' is an invalid keyword argument for __new__()", strings.ToLower(unit))
	}
	return time.Duration(math.Round(d/1000) * 1000)
}

func arrayConcat(args ...any) any {
	var out []any
	for _, a := range args {
		if l, ok := a.([]any); ok {
			out = append(out, l...)
		} else {
			out = append(out, a)
		}
	}
	return out
}

func arrayToString(args ...any) any {
	sep := asString(args[1])
	var null any
	if len(args) > 2 {
		null = args[2]
	}
	var parts []string
	for _, x := range values(args[0]) {
		if x == nil {
			x = null
		}
		if x != nil {
			parts = append(parts, asString(x))
		}
	}
	return strings.Join(parts, sep)
}

func jsonExtract(args ...any) any {
	this := args[0]
	for _, seg := range values(args[1]) {
		key := Str(seg)
		switch x := this.(type) {
		case map[string]any:
			this = x[key]
		case []any:
			if !helper.IsInt(key) {
				panic(&notImplementedError{sprintf("Unable to extract value for %s at %s.", Str(this), key)})
			}
			i, _ := strconv.Atoi(key)
			if i >= 0 && i < len(x) {
				this = x[i]
			} else if i < 0 && -i <= len(x) {
				this = x[len(x)+i]
			} else {
				this = nil
			}
		default:
			panic(&notImplementedError{sprintf("Unable to extract value for %s at %s.", Str(this), key)})
		}
		if this == nil {
			break
		}
	}
	return this
}

func pyRound(args ...any) any {
	this := args[0]
	var decimals any
	if len(args) > 1 {
		decimals = args[1]
	}
	switch x := this.(type) {
	case float64:
		if decimals == nil {
			return pyInt(math.RoundToEven(x))
		}
		n := asInt(decimals)
		f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', max(n, 0), 64), 64)
		if n < 0 {
			p := math.Pow(10, float64(-n))
			f = math.RoundToEven(x/p) * p
		}
		return f
	case int, bool:
		if decimals == nil || asInt(decimals) >= 0 {
			n, _ := numeric(x)
			return n
		}
		p := int(math.Pow(10, float64(-asInt(decimals))))
		v := asInt(x)
		q := float64(v) / float64(p)
		return int(math.RoundToEven(q)) * p
	}
	typeErrorf("type %s doesn't define __round__ method", pyTypeName(this))
	return nil
}

func minMax(less bool) func([]any) any {
	return func(vals []any) any {
		best := vals[0]
		for _, v := range vals[1:] {
			c := Compare(v, best)
			if (less && c < 0) || (!less && c > 0) {
				best = v
			}
		}
		return best
	}
}

func sum(vals []any) any {
	var total any = 0
	for _, v := range vals {
		total = add(total, v)
	}
	return total
}

func fmean(vals []any) any {
	total := 0.0
	for _, v := range vals {
		n, ok := numeric(v)
		if !ok {
			typeErrorf("can't convert type '%s' to numerator/denominator", pyTypeName(v))
		}
		total += toFloat(n)
	}
	return total / float64(len(vals))
}

// ENV is the function table of the executor (sqlglot.executor.env.ENV), keyed by upper case
// function name. Executors copy it when they are created.
var ENV map[string]Func

func init() {
	ENV = map[string]Func{
		// aggregations
		"ARRAYAGG": func(args ...any) any { return append([]any{}, values(args[0])...) },
		"ARRAYUNIQUEAGG": filterNulls(func(acc []any) any {
			return append([]any{}, pySetList(acc)...)
		}, true),
		"AVG":   filterNulls(fmean, true),
		"COUNT": filterNulls(func(acc []any) any { return len(acc) }, false),
		"FIRST": func(args ...any) any {
			if vs := values(args[0]); len(vs) > 0 {
				return vs[0]
			}
			return nil
		},
		"MAX": filterNulls(minMax(false), true),
		"MIN": filterNulls(minMax(true), true),
		"SUM": filterNulls(sum, true),
		// scalar functions
		"ABS": nullIfAny(func(a ...any) any { return pyAbs(a[0]) }),
		"ADD": nullIfAny(func(a ...any) any { return add(a[0], a[1]) }),
		"ARRAYANY": nullIfAny(func(a ...any) any {
			f := a[1].(Lambda)
			for _, e := range values(a[0]) {
				if Truthy(f(e)) {
					return true
				}
			}
			return false
		}),
		"ARRAYCONCAT":   nullIfAny(arrayConcat),
		"ARRAYTOSTRING": nullIfFirst(2, arrayToString),
		"BETWEEN": nullIfAny(func(a ...any) any {
			return Compare(a[1], a[0]) <= 0 && Compare(a[0], a[2]) <= 0
		}),
		"BITWISEAND":        nullIfAny(func(a ...any) any { return bitOp(a[0], a[1], "&") }),
		"BITWISELEFTSHIFT":  nullIfAny(func(a ...any) any { return bitOp(a[0], a[1], "<<") }),
		"BITWISEOR":         nullIfAny(func(a ...any) any { return bitOp(a[0], a[1], "|") }),
		"BITWISERIGHTSHIFT": nullIfAny(func(a ...any) any { return bitOp(a[0], a[1], ">>") }),
		"BITWISEXOR":        nullIfAny(func(a ...any) any { return bitOp(a[0], a[1], "^") }),
		"CAST":              nullIfAny(cast),
		"COALESCE": func(a ...any) any {
			for _, v := range a {
				if v != nil {
					return v
				}
			}
			return nil
		},
		"CONCAT": nullIfAny(func(a ...any) any {
			var b strings.Builder
			for _, v := range a {
				b.WriteString(asString(v))
			}
			return b.String()
		}),
		"SAFECONCAT": nullIfAny(func(a ...any) any {
			var b strings.Builder
			for _, v := range a {
				b.WriteString(Str(v))
			}
			return b.String()
		}),
		"CONCATWS": nullIfAny(func(a ...any) any {
			parts := make([]string, len(a)-1)
			for i, v := range a[1:] {
				parts[i] = asString(v)
			}
			return strings.Join(parts, asString(a[0]))
		}),
		"DATEDIFF": nullIfAny(func(a ...any) any {
			d, ok := sub(a[0], a[1]).(time.Duration)
			if !ok {
				typeErrorf("unsupported operand for DATEDIFF")
			}
			return durationDays(d)
		}),
		"DATESTRTODATE": nullIfAny(func(a ...any) any { return parseISODate(asString(a[0])) }),
		"DIV":           nullIfAny(func(a ...any) any { return div(a[0], a[1]) }),
		"DOT":           nullIfAny(func(a ...any) any { return index(a[0], a[1]) }),
		"EQ":            nullIfAny(func(a ...any) any { return Equal(a[0], a[1]) }),
		"EXTRACT":       nullIfAny(func(a ...any) any { return extract(asString(a[0]), a[1]) }),
		"GT":            nullIfAny(func(a ...any) any { return Compare(a[0], a[1]) > 0 }),
		"GTE":           nullIfAny(func(a ...any) any { return Compare(a[0], a[1]) >= 0 }),
		"IF": func(a ...any) any {
			var f any
			if len(a) > 2 {
				f = a[2]
			}
			if Truthy(a[0]) {
				return a[1]
			}
			return f
		},
		"IN":               sqlIn,
		"INT":              nullIfAny(func(a ...any) any { return pyInt(a[0]) }),
		"INTDIV":           nullIfAny(intdiv),
		"INTERVAL":         nullIfAny(interval),
		"JSONEXTRACT":      nullIfFirst(2, jsonExtract),
		"LEFT":             nullIfAny(func(a ...any) any { return string(pySlice([]rune(asString(a[0])), 0, asInt(a[1]))) }),
		"LENGTH":           nullIfAny(func(a ...any) any { return pyLen(a[0]) }),
		"LIKE":             nullIfAny(func(a ...any) any { return like(asString(a[0]), asString(a[1]), false) }),
		"ILIKE":            nullIfAny(func(a ...any) any { return like(asString(a[0]), asString(a[1]), true) }),
		"LOWER":            nullIfAny(func(a ...any) any { return strings.ToLower(asString(a[0])) }),
		"LT":               nullIfAny(func(a ...any) any { return Compare(a[0], a[1]) < 0 }),
		"LTE":              nullIfAny(func(a ...any) any { return Compare(a[0], a[1]) <= 0 }),
		"MOD":              nullIfAny(func(a ...any) any { return mod(a[0], a[1]) }),
		"MUL":              nullIfAny(func(a ...any) any { return mul(a[0], a[1]) }),
		"NEQ":              nullIfAny(func(a ...any) any { return !Equal(a[0], a[1]) }),
		"NOT":              sqlNot,
		"ORD":              nullIfAny(func(a ...any) any { r, _ := utf8.DecodeRuneInString(asString(a[0])); return int(r) }),
		"POW":              nullIfAny(func(a ...any) any { return pow(a[0], a[1]) }),
		"REVERSE":          nullIfAny(func(a ...any) any { return reverse(a[0]) }),
		"RIGHT":            nullIfAny(func(a ...any) any { r := []rune(asString(a[0])); return string(pySlice(r, -asInt(a[1]), len(r))) }),
		"ROUND":            nullIfAny(pyRound),
		"STRPOSITION":      nullIfFirst(2, strPosition),
		"SUB":              nullIfAny(func(a ...any) any { return sub(a[0], a[1]) }),
		"SUBSTRING":        nullIfFirst(1, substring),
		"TIMESTRTOTIME":    nullIfAny(func(a ...any) any { return parseISODatetime(asString(a[0])) }),
		"UPPER":            nullIfAny(func(a ...any) any { return strings.ToUpper(asString(a[0])) }),
		"YEAR":             nullIfAny(func(a ...any) any { return extract("year", a[0]) }),
		"MONTH":            nullIfAny(func(a ...any) any { return extract("month", a[0]) }),
		"DAY":              nullIfAny(func(a ...any) any { return extract("day", a[0]) }),
		"CURRENTDATETIME":  func(...any) any { return time.Now() },
		"CURRENTTIMESTAMP": func(...any) any { return time.Now() },
		"CURRENTTIME":      func(...any) any { return time.Now() },
		"CURRENTDATE": func(...any) any {
			n := time.Now()
			return NewDate(n.Year(), n.Month(), n.Day())
		},
		"TRIM": nullIfAny(func(a ...any) any {
			s := asString(a[0])
			if len(a) > 1 {
				return strings.Trim(s, asString(a[1]))
			}
			return strings.TrimSpace(s)
		}),
		"STRUCT": func(a ...any) any {
			out := map[string]any{}
			for i := 0; i+1 < len(a); i += 2 {
				if a[i] != nil && a[i+1] != nil {
					out[Str(a[i])] = a[i+1]
				}
			}
			return out
		},
		"MAP": nullIfAny(func(a ...any) any {
			out := map[string]any{}
			if len(a) < 2 {
				return out
			}
			keys, vals := values(a[0]), values(a[1])
			for i := 0; i < len(keys) && i < len(vals); i++ {
				out[Str(keys[i])] = vals[i]
			}
			return out
		}),
		"UNIXTOTIME": nullIfAny(func(a ...any) any {
			f := toFloat(pyFloat(a[0]))
			sec, frac := math.Modf(f)
			return time.Unix(int64(sec), int64(math.Round(frac*1e6))*1000).In(UTC)
		}),
		"STRFTIME": nullIfAny(func(a ...any) any {
			return strftime(parseISODatetime(asString(a[1])), asString(a[0]))
		}),
		"STRTOTIME": nullIfAny(func(a ...any) any { return strptime(asString(a[0]), asString(a[1])) }),
	}
}
