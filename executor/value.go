package executor

// Values follow the semantics of the Python values produced by sqlglot's Python executor:
// nil (None), bool, int, *big.Int, float64, string, Date, Time, time.Time (datetime),
// time.Duration (timedelta), []any (list), map[string]any (dict) and Lambda.

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Date is a calendar date (Python datetime.date).
type Date struct{ time.Time }

// NewDate builds a date.
func NewDate(year int, month time.Month, day int) Date {
	return Date{time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// String mirrors date.isoformat().
func (d Date) String() string { return d.Format("2006-01-02") }

// Time is a time of day (Python datetime.time).
type Time struct {
	Hour, Minute, Second, Microsecond int
}

// String mirrors time.isoformat().
func (t Time) String() string {
	s := fmt.Sprintf("%02d:%02d:%02d", t.Hour, t.Minute, t.Second)
	if t.Microsecond != 0 {
		s += fmt.Sprintf(".%06d", t.Microsecond)
	}
	return s
}

func (t Time) micros() int64 {
	return ((int64(t.Hour)*60+int64(t.Minute))*60+int64(t.Second))*1_000_000 + int64(t.Microsecond)
}

// Tuple is a Python tuple: like a list, but hashable.
type Tuple []any

// Lambda is a SQL lambda function (Python lambda).
type Lambda func(args ...any) any

// typeError is a Python TypeError raised while evaluating.
type typeError struct{ msg string }

func (e *typeError) Error() string { return e.msg }

func typeErrorf(format string, args ...any) { panic(&typeError{fmt.Sprintf(format, args...)}) }

func pyTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int, *big.Int:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case Date:
		return "datetime.date"
	case Time:
		return "datetime.time"
	case time.Time:
		return "datetime.datetime"
	case time.Duration:
		return "datetime.timedelta"
	case []any:
		return "list"
	case Tuple:
		return "tuple"
	case map[string]any:
		return "dict"
	case Lambda:
		return "function"
	}
	return fmt.Sprintf("%T", v)
}

// Truthy mirrors bool(value).
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int:
		return x != 0
	case *big.Int:
		return x.Sign() != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case Tuple:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case time.Duration:
		return x != 0
	}
	return true
}

// numeric returns the value as a number: bools are ints, like in Python.
func numeric(v any) (any, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case int, *big.Int, float64:
		return x, true
	}
	return nil, false
}

func toBig(v any) *big.Int {
	switch x := v.(type) {
	case int:
		return big.NewInt(int64(x))
	case *big.Int:
		return x
	}
	return nil
}

func normInt(b *big.Int) any {
	if b.IsInt64() {
		return int(b.Int64())
	}
	return b
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f
	case float64:
		return x
	}
	return math.NaN()
}

// Equal mirrors value == other.
func Equal(a, b any) bool {
	if na, ok := numeric(a); ok {
		nb, ok := numeric(b)
		if !ok {
			return false
		}
		_, fa := na.(float64)
		_, fb := nb.(float64)
		if fa || fb {
			return toFloat(na) == toFloat(nb)
		}
		return toBig(na).Cmp(toBig(nb)) == 0
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case Date:
		y, ok := b.(Date)
		return ok && x.Equal(y.Time)
	case Time:
		y, ok := b.(Time)
		return ok && x == y
	case time.Time:
		y, ok := b.(time.Time)
		return ok && isNaive(x) == isNaive(y) && x.Equal(y)
	case time.Duration:
		y, ok := b.(time.Duration)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		return ok && seqEqual(x, y)
	case Tuple:
		y, ok := b.(Tuple)
		return ok && seqEqual(x, y)
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !Equal(v, w) {
				return false
			}
		}
		return true
	}
	return false
}

func seqEqual(x, y []any) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if !Equal(x[i], y[i]) {
			return false
		}
	}
	return true
}

func seqCompare(x, y []any) int {
	for i := 0; i < len(x) && i < len(y); i++ {
		if !Equal(x[i], y[i]) {
			return Compare(x[i], y[i])
		}
	}
	return cmpInt64(int64(len(x)), int64(len(y)))
}

// Compare mirrors Python's ordering (a < b is Compare(a, b) < 0); it raises a TypeError for
// values that can't be ordered.
func Compare(a, b any) int {
	if na, ok := numeric(a); ok {
		if nb, ok := numeric(b); ok {
			_, fa := na.(float64)
			_, fb := nb.(float64)
			if fa || fb {
				x, y := toFloat(na), toFloat(nb)
				switch {
				case x < y:
					return -1
				case x > y:
					return 1
				}
				return 0
			}
			return toBig(na).Cmp(toBig(nb))
		}
	}
	switch x := a.(type) {
	case string:
		if y, ok := b.(string); ok {
			return strings.Compare(x, y)
		}
	case Date:
		if y, ok := b.(Date); ok {
			return x.Compare(y.Time)
		}
	case Time:
		if y, ok := b.(Time); ok {
			return cmpInt64(x.micros(), y.micros())
		}
	case time.Time:
		if y, ok := b.(time.Time); ok {
			return x.Compare(y)
		}
	case time.Duration:
		if y, ok := b.(time.Duration); ok {
			return cmpInt64(int64(x), int64(y))
		}
	case []any:
		if y, ok := b.([]any); ok {
			return seqCompare(x, y)
		}
	case Tuple:
		if y, ok := b.(Tuple); ok {
			return seqCompare(x, y)
		}
	}
	typeErrorf("'<' not supported between instances of '%s' and '%s'", pyTypeName(a), pyTypeName(b))
	return 0
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// HashKey returns a key that is equal for values that compare equal (Python hash()).
func HashKey(v any) string {
	if n, ok := numeric(v); ok {
		switch x := n.(type) {
		case float64:
			if x == math.Trunc(x) && !math.IsInf(x, 0) && math.Abs(x) < 1e18 {
				return "n" + strconv.FormatInt(int64(x), 10)
			}
			return "f" + strconv.FormatFloat(x, 'g', -1, 64)
		default:
			return "n" + toBig(n).String()
		}
	}
	switch x := v.(type) {
	case nil:
		return "N"
	case string:
		return "s" + strconv.Quote(x)
	case Date:
		return "d" + x.String()
	case Time:
		return "t" + x.String()
	case time.Time:
		return "T" + x.UTC().Format(time.RFC3339Nano)
	case time.Duration:
		return "D" + strconv.FormatInt(int64(x), 10)
	case Tuple:
		return "(" + RowKey(x) + ")"
	case []any:
		typeErrorf("unhashable type: 'list'")
	case map[string]any:
		typeErrorf("unhashable type: 'dict'")
	}
	return fmt.Sprintf("?%v", v)
}

// RowKey returns the hash key of a row (a tuple of values).
func RowKey(row []any) string {
	parts := make([]string, len(row))
	for i, v := range row {
		parts[i] = HashKey(v)
	}
	return strings.Join(parts, "\x00")
}

// Str mirrors str(value).
func Str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(x)
	case *big.Int:
		return x.String()
	case float64:
		return floatRepr(x)
	case string:
		return x
	case Date:
		return x.String()
	case Time:
		return x.String()
	case time.Time:
		s := x.Format("2006-01-02 15:04:05")
		if us := x.Nanosecond() / 1000; us != 0 {
			s += fmt.Sprintf(".%06d", us)
		}
		if !isNaive(x) {
			s += x.Format("-07:00")
		}
		return s
	case time.Duration:
		return durationStr(x)
	case []any, Tuple:
		return Repr(x)
	case map[string]any:
		return Repr(x)
	}
	return fmt.Sprint(v)
}

// Repr mirrors repr(value).
func Repr(v any) string {
	switch x := v.(type) {
	case string:
		return pyStrRepr(x)
	case []any:
		parts := make([]string, len(x))
		for i, it := range x {
			parts[i] = Repr(it)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case Tuple:
		parts := make([]string, len(x))
		for i, it := range x {
			parts[i] = Repr(it)
		}
		if len(x) == 1 {
			return "(" + parts[0] + ",)"
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = pyStrRepr(k) + ": " + Repr(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case Date:
		return fmt.Sprintf("datetime.date(%d, %d, %d)", x.Year(), x.Month(), x.Day())
	}
	return Str(v)
}

func pyStrRepr(s string) string {
	quote := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = `"`
	}
	var b strings.Builder
	b.WriteString(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case string(r) == quote:
			b.WriteString(`\` + quote)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(quote)
	return b.String()
}

// floatRepr mirrors repr(float).
func floatRepr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(e, "e")
	n, _ := strconv.Atoi(exp)
	if f != 0 && (n < -4 || n >= 16) {
		return mant + "e" + exp[:1] + fmt.Sprintf("%02d", abs(n))
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func durationStr(d time.Duration) string {
	micros := d.Microseconds()
	days := micros / 86_400_000_000
	rem := micros % 86_400_000_000
	if rem < 0 {
		days--
		rem += 86_400_000_000
	}
	secs := rem / 1_000_000
	us := rem % 1_000_000
	s := fmt.Sprintf("%d:%02d:%02d", secs/3600, (secs/60)%60, secs%60)
	if us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	if days != 0 {
		plural := "s"
		if days == 1 || days == -1 {
			plural = ""
		}
		s = fmt.Sprintf("%d day%s, %s", days, plural, s)
	}
	return s
}

// PythonError is implemented by the evaluation errors, which mirror Python exceptions.
type PythonError interface {
	error
	// PythonClass is the name of the Python exception class, e.g. "ZeroDivisionError".
	PythonClass() string
}

func (*typeError) PythonClass() string           { return "TypeError" }
func (*keyError) PythonClass() string            { return "KeyError" }
func (*valueError) PythonClass() string          { return "ValueError" }
func (*zeroDivisionError) PythonClass() string   { return "ZeroDivisionError" }
func (*indexError) PythonClass() string          { return "IndexError" }
func (*attributeError) PythonClass() string      { return "AttributeError" }
func (*notImplementedError) PythonClass() string { return "NotImplementedError" }
func (*compileError) PythonClass() string        { return "NameError" }
