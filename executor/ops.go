package executor

// Python operator semantics for executor values.

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"
)

var fmtSprintf = fmt.Sprintf

func unsupported(op string, a, b any) {
	typeErrorf("unsupported operand type(s) for %s: '%s' and '%s'", op, pyTypeName(a), pyTypeName(b))
}

func isFloatValue(a, b any) bool {
	_, fa := a.(float64)
	_, fb := b.(float64)
	return fa || fb
}

// arith applies an integer/float operation to numbers; ok is false for other types.
func arith(a, b any, ints func(x, y *big.Int) *big.Int, floats func(x, y float64) float64) (any, bool) {
	na, ok1 := numeric(a)
	nb, ok2 := numeric(b)
	if !ok1 || !ok2 {
		return nil, false
	}
	if isFloatValue(na, nb) {
		return floats(toFloat(na), toFloat(nb)), true
	}
	return normInt(ints(toBig(na), toBig(nb))), true
}

func add(a, b any) any {
	if r, ok := arith(a, b, func(x, y *big.Int) *big.Int { return new(big.Int).Add(x, y) }, func(x, y float64) float64 { return x + y }); ok {
		return r
	}
	switch x := a.(type) {
	case string:
		if y, ok := b.(string); ok {
			return x + y
		}
	case []any:
		if y, ok := b.([]any); ok {
			return append(append([]any{}, x...), y...)
		}
	case time.Time:
		if y, ok := b.(time.Duration); ok {
			return x.Add(y)
		}
	case Date:
		if y, ok := b.(time.Duration); ok {
			return Date{x.AddDate(0, 0, int(durationDays(y)))}
		}
	case time.Duration:
		switch y := b.(type) {
		case time.Duration:
			return x + y
		case time.Time:
			return y.Add(x)
		case Date:
			return Date{y.AddDate(0, 0, int(durationDays(x)))}
		}
	}
	unsupported("+", a, b)
	return nil
}

// durationDays mirrors timedelta.days (floored).
func durationDays(d time.Duration) int {
	day := 24 * time.Hour
	days := int(d / day)
	if d%day < 0 {
		days--
	}
	return days
}

func sub(a, b any) any {
	if r, ok := arith(a, b, func(x, y *big.Int) *big.Int { return new(big.Int).Sub(x, y) }, func(x, y float64) float64 { return x - y }); ok {
		return r
	}
	switch x := a.(type) {
	case time.Time:
		switch y := b.(type) {
		case time.Time:
			return x.Sub(y)
		case time.Duration:
			return x.Add(-y)
		}
	case Date:
		switch y := b.(type) {
		case Date:
			return x.Sub(y.Time)
		case time.Duration:
			return Date{x.AddDate(0, 0, -durationDays(y))}
		}
	case time.Duration:
		if y, ok := b.(time.Duration); ok {
			return x - y
		}
	}
	unsupported("-", a, b)
	return nil
}

func mul(a, b any) any {
	if r, ok := arith(a, b, func(x, y *big.Int) *big.Int { return new(big.Int).Mul(x, y) }, func(x, y float64) float64 { return x * y }); ok {
		return r
	}
	repeat := func(s any, n any) (any, bool) {
		k, ok := numeric(n)
		if !ok {
			return nil, false
		}
		if _, isInt := k.(int); !isInt {
			return nil, false
		}
		count := max(k.(int), 0)
		switch x := s.(type) {
		case string:
			return strings.Repeat(x, count), true
		case []any:
			var out []any
			for i := 0; i < count; i++ {
				out = append(out, x...)
			}
			if out == nil {
				out = []any{}
			}
			return out, true
		}
		return nil, false
	}
	if r, ok := repeat(a, b); ok {
		return r
	}
	if r, ok := repeat(b, a); ok {
		return r
	}
	if d, ok := a.(time.Duration); ok {
		if n, ok := numeric(b); ok {
			return time.Duration(math.Round(float64(d) * toFloat(n)))
		}
	}
	unsupported("*", a, b)
	return nil
}

// div is Python's true division.
func div(a, b any) any {
	na, ok1 := numeric(a)
	nb, ok2 := numeric(b)
	if ok1 && ok2 {
		if toFloat(nb) == 0 {
			panic(&zeroDivisionError{"division by zero"})
		}
		if !isFloatValue(na, nb) {
			r := new(big.Rat).SetFrac(toBig(na), toBig(nb))
			f, _ := r.Float64()
			return f
		}
		return toFloat(na) / toFloat(nb)
	}
	if d, ok := a.(time.Duration); ok {
		switch y := b.(type) {
		case time.Duration:
			return float64(d) / float64(y)
		default:
			if n, ok := numeric(y); ok {
				return time.Duration(math.Round(float64(d) / toFloat(n)))
			}
		}
	}
	unsupported("/", a, b)
	return nil
}

// mod is Python's %, whose result has the sign of the divisor.
func mod(a, b any) any {
	na, ok1 := numeric(a)
	nb, ok2 := numeric(b)
	if !ok1 || !ok2 {
		unsupported("%", a, b)
	}
	if toFloat(nb) == 0 {
		panic(&zeroDivisionError{"integer division or modulo by zero"})
	}
	if isFloatValue(na, nb) {
		x, y := toFloat(na), toFloat(nb)
		r := math.Mod(x, y)
		if r != 0 && (r < 0) != (y < 0) {
			r += y
		}
		return r
	}
	x, y := toBig(na), toBig(nb)
	r := new(big.Int).Mod(x, y) // Euclidean, non-negative
	if r.Sign() != 0 && y.Sign() < 0 {
		r.Add(r, y)
	}
	return normInt(r)
}

func pow(a, b any) any {
	na, ok1 := numeric(a)
	nb, ok2 := numeric(b)
	if !ok1 || !ok2 {
		unsupported("** or pow()", a, b)
	}
	if !isFloatValue(na, nb) && toBig(nb).Sign() >= 0 {
		return normInt(new(big.Int).Exp(toBig(na), toBig(nb), nil))
	}
	return math.Pow(toFloat(na), toFloat(nb))
}

func bitOp(a, b any, op string) any {
	na, ok1 := numeric(a)
	nb, ok2 := numeric(b)
	if !ok1 || !ok2 || isFloatValue(na, nb) {
		unsupported(op, a, b)
	}
	x, y := toBig(na), toBig(nb)
	switch op {
	case "&":
		return normInt(new(big.Int).And(x, y))
	case "|":
		return normInt(new(big.Int).Or(x, y))
	case "^":
		return normInt(new(big.Int).Xor(x, y))
	case "<<":
		return normInt(new(big.Int).Lsh(x, uint(y.Int64())))
	case ">>":
		return normInt(new(big.Int).Rsh(x, uint(y.Int64())))
	}
	return nil
}

func neg(a any) any {
	switch x := a.(type) {
	case bool:
		if x {
			return -1
		}
		return 0
	case int:
		return normInt(new(big.Int).Neg(big.NewInt(int64(x))))
	case *big.Int:
		return normInt(new(big.Int).Neg(x))
	case float64:
		return -x
	case time.Duration:
		return -x
	}
	typeErrorf("bad operand type for unary -: '%s'", pyTypeName(a))
	return nil
}

func pyAbs(a any) any {
	switch x := a.(type) {
	case bool, int, *big.Int:
		n, _ := numeric(x)
		return normInt(new(big.Int).Abs(toBig(n)))
	case float64:
		return math.Abs(x)
	case time.Duration:
		if x < 0 {
			return -x
		}
		return x
	}
	typeErrorf("bad operand type for abs(): '%s'", pyTypeName(a))
	return nil
}

func pyLen(a any) any {
	switch x := a.(type) {
	case string:
		return utf8.RuneCountInString(x)
	case []any:
		return len(x)
	case map[string]any:
		return len(x)
	case rangeValues:
		return len(x)
	}
	typeErrorf("object of type '%s' has no len()", pyTypeName(a))
	return nil
}

func reverse(a any) any {
	switch x := a.(type) {
	case string:
		r := []rune(x)
		for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
			r[i], r[j] = r[j], r[i]
		}
		return string(r)
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[len(x)-1-i] = v
		}
		return out
	}
	typeErrorf("'%s' object is not subscriptable", pyTypeName(a))
	return nil
}

// index mirrors value[key] for lists, strings and dicts.
func index(a, key any) any {
	switch x := a.(type) {
	case []any:
		i := asInt(key)
		if i < 0 {
			i += len(x)
		}
		if i < 0 || i >= len(x) {
			panic(&indexError{"list index out of range"})
		}
		return x[i]
	case string:
		r := []rune(x)
		i := asInt(key)
		if i < 0 {
			i += len(r)
		}
		if i < 0 || i >= len(r) {
			panic(&indexError{"string index out of range"})
		}
		return string(r[i])
	case map[string]any:
		k := Str(key)
		v, ok := x[k]
		if !ok {
			panic(&keyError{k})
		}
		return v
	}
	typeErrorf("'%s' object is not subscriptable", pyTypeName(a))
	return nil
}

type indexError struct{ msg string }

func (e *indexError) Error() string { return e.msg }

// extract mirrors getattr(value, part) for date/time parts.
func extract(part string, v any) any {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case Date:
		t = x.Time
	case Time:
		switch part {
		case "hour":
			return x.Hour
		case "minute":
			return x.Minute
		case "second":
			return x.Second
		case "microsecond":
			return x.Microsecond
		}
		panic(&attributeError{fmt.Sprintf("'datetime.time' object has no attribute '%s'", part)})
	default:
		panic(&attributeError{fmt.Sprintf("'%s' object has no attribute '%s'", pyTypeName(v), part)})
	}
	switch part {
	case "year":
		return t.Year()
	case "month":
		return int(t.Month())
	case "day":
		return t.Day()
	}
	if _, isDate := v.(Date); !isDate {
		switch part {
		case "hour":
			return t.Hour()
		case "minute":
			return t.Minute()
		case "second":
			return t.Second()
		case "microsecond":
			return t.Nanosecond() / 1000
		}
	}
	panic(&attributeError{fmt.Sprintf("'%s' object has no attribute '%s'", pyTypeName(v), part)})
}

type attributeError struct{ msg string }

func (e *attributeError) Error() string { return e.msg }
