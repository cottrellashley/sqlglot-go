package pyrt

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// Truth is bool(v).
func Truth(v Value) bool {
	switch x := norm(v).(type) {
	case *SkippedV:
		skipNow(x)
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
	case *List:
		return len(x.V) > 0
	case Tuple:
		return len(x) > 0
	case *Dict:
		return x.Len() > 0
	case *Set:
		return x.d.Len() > 0
	case *ArgsView:
		return true
	case interface{ PyLen() int }:
		return x.PyLen() > 0
	}
	return true
}

// Not is `not v`.
func Not(v Value) bool { return !Truth(v) }

// Bool is bool(v).
func Bool(v Value) bool { return Truth(v) }

// AndV is `a and b` (b is evaluated lazily).
func AndV(a Value, b func() Value) Value {
	if !Truth(a) {
		return a
	}
	return b()
}

// OrV is `a or b` (b is evaluated lazily).
func OrV(a Value, b func() Value) Value {
	if Truth(a) {
		return a
	}
	return b()
}

// IfV is `a if cond else b`.
func IfV(cond Value, a, b func() Value) Value {
	if Truth(cond) {
		return a()
	}
	return b()
}

func toFloat(v Value) (float64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case int:
		return float64(x), true
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f, true
	case float64:
		return x, true
	}
	return 0, false
}

func toBig(v Value) (*big.Int, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return big.NewInt(1), true
		}
		return big.NewInt(0), true
	case int:
		return big.NewInt(int64(x)), true
	case *big.Int:
		return x, true
	}
	return nil, false
}

func isNumber(v Value) bool {
	switch v.(type) {
	case bool, int, *big.Int, float64:
		return true
	}
	return false
}

func isFloat(v Value) bool { _, ok := v.(float64); return ok }

// Eq is a == b.
func Eq(a, b Value) bool {
	a, b = norm(a), norm(b)
	checkSkipped(a, b)
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if isNumber(a) && isNumber(b) {
		if isFloat(a) || isFloat(b) {
			fa, _ := toFloat(a)
			fb, _ := toFloat(b)
			return fa == fb
		}
		ba, _ := toBig(a)
		bb, _ := toBig(b)
		return ba.Cmp(bb) == 0
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case *exp.Expr:
		y, ok := b.(*exp.Expr)
		return ok && x.Kind() == y.Kind() && x.Equal(y)
	case *List:
		y, ok := b.(*List)
		return ok && seqEq(x.V, y.V)
	case Tuple:
		y, ok := b.(Tuple)
		return ok && seqEq(x, y)
	case *Dict:
		y, ok := b.(*Dict)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for i, k := range x.keys {
			v, ok := y.Lookup(k)
			if !ok || !Eq(x.vals[i], v) {
				return false
			}
		}
		return true
	case *Set:
		y, ok := b.(*Set)
		if !ok || x.d.Len() != y.d.Len() {
			return false
		}
		for _, k := range x.Items() {
			if !y.Has(k) {
				return false
			}
		}
		return true
	case interface{ PyEq(Value) (bool, bool) }:
		if r, ok := x.PyEq(b); ok {
			return r
		}
	}
	if y, ok := b.(interface{ PyEq(Value) (bool, bool) }); ok {
		if r, ok := y.PyEq(a); ok {
			return r
		}
	}
	return a == b
}

func seqEq(a, b []Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !Eq(a[i], b[i]) {
			return false
		}
	}
	return true
}

// Ne is a != b.
func Ne(a, b Value) bool { return !Eq(a, b) }

// Is is `a is b`.
func Is(a, b Value) bool {
	a, b = norm(a), norm(b)
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch x := a.(type) {
	case bool, int, string, exp.Kind, exp.DType, *ExcClass, errs.ErrorLevel:
		return a == b
	case *exp.Expr:
		y, ok := b.(*exp.Expr)
		return ok && x == y
	case *List:
		y, ok := b.(*List)
		return ok && x == y
	case *Dict:
		y, ok := b.(*Dict)
		return ok && x == y
	}
	defer func() { recover() }()
	return a == b
}

// IsNot is `a is not b`.
func IsNot(a, b Value) bool { return !Is(a, b) }

func cmp(a, b Value, op string) int {
	a, b = norm(a), norm(b)
	if isNumber(a) && isNumber(b) {
		if isFloat(a) || isFloat(b) {
			fa, _ := toFloat(a)
			fb, _ := toFloat(b)
			switch {
			case fa < fb:
				return -1
			case fa > fb:
				return 1
			}
			return 0
		}
		ba, _ := toBig(a)
		bb, _ := toBig(b)
		return ba.Cmp(bb)
	}
	switch x := a.(type) {
	case string:
		if y, ok := b.(string); ok {
			return strings.Compare(x, y)
		}
	case *List:
		if y, ok := b.(*List); ok {
			return seqCmp(x.V, y.V, op)
		}
	case Tuple:
		if y, ok := b.(Tuple); ok {
			return seqCmp(x, y, op)
		}
	}
	Raise(TypeError, "'%s' not supported between instances of '%s' and '%s'", op, TypeName(a), TypeName(b))
	return 0
}

func seqCmp(a, b []Value, op string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if !Eq(a[i], b[i]) {
			return cmp(a[i], b[i], op)
		}
	}
	return len(a) - len(b)
}

func exprOp(a, b Value, build func(x *exp.Expr, other any) *exp.Expr) (Value, bool) {
	if x, ok := norm(a).(*exp.Expr); ok {
		return build(x, toGo(b)), true
	}
	return nil, false
}

// Lt is a < b.
func Lt(a, b Value) Value {
	if r, ok := exprOp(a, b, (*exp.Expr).Lt); ok {
		return r
	}
	return cmp(a, b, "<") < 0
}

// Le is a <= b.
func Le(a, b Value) Value {
	if r, ok := exprOp(a, b, (*exp.Expr).Lte); ok {
		return r
	}
	return cmp(a, b, "<=") <= 0
}

// Gt is a > b.
func Gt(a, b Value) Value {
	if r, ok := exprOp(a, b, (*exp.Expr).Gt); ok {
		return r
	}
	return cmp(a, b, ">") > 0
}

// Ge is a >= b.
func Ge(a, b Value) Value {
	if r, ok := exprOp(a, b, (*exp.Expr).Gte); ok {
		return r
	}
	return cmp(a, b, ">=") >= 0
}

func intResult(z *big.Int) Value {
	if z.IsInt64() && z.Int64() >= math.MinInt && z.Int64() <= math.MaxInt {
		return int(z.Int64())
	}
	return z
}

func arith(a, b Value, op string) Value {
	a, b = norm(a), norm(b)
	checkSkipped(a, b)
	if isNumber(a) && isNumber(b) {
		if isFloat(a) || isFloat(b) || op == "/" {
			fa, _ := toFloat(a)
			fb, _ := toFloat(b)
			switch op {
			case "+":
				return fa + fb
			case "-":
				return fa - fb
			case "*":
				return fa * fb
			case "/":
				if fb == 0 {
					Raise(ZeroDivisionError, "division by zero")
				}
				return fa / fb
			case "//":
				return math.Floor(fa / fb)
			case "%":
				m := math.Mod(fa, fb)
				if m != 0 && (m < 0) != (fb < 0) {
					m += fb
				}
				return m
			case "**":
				return math.Pow(fa, fb)
			}
		}
		ba, _ := toBig(a)
		bb, _ := toBig(b)
		z := new(big.Int)
		switch op {
		case "+":
			z.Add(ba, bb)
		case "-":
			z.Sub(ba, bb)
		case "*":
			z.Mul(ba, bb)
		case "//":
			if bb.Sign() == 0 {
				Raise(ZeroDivisionError, "integer division or modulo by zero")
			}
			z = floorDiv(ba, bb)
		case "%":
			if bb.Sign() == 0 {
				Raise(ZeroDivisionError, "integer division or modulo by zero")
			}
			z.Sub(ba, new(big.Int).Mul(floorDiv(ba, bb), bb))
		case "**":
			z.Exp(ba, bb, nil)
		case "<<":
			z.Lsh(ba, uint(bb.Int64()))
		case ">>":
			z.Rsh(ba, uint(bb.Int64()))
		case "&":
			z.And(ba, bb)
		case "|":
			z.Or(ba, bb)
		case "^":
			z.Xor(ba, bb)
		}
		return intResult(z)
	}
	return nil
}

func floorDiv(a, b *big.Int) *big.Int {
	q, m := new(big.Int).QuoRem(a, b, new(big.Int))
	if m.Sign() != 0 && (m.Sign() < 0) != (b.Sign() < 0) {
		q.Sub(q, big.NewInt(1))
	}
	return q
}

// Add is a + b.
func Add(a, b Value) Value {
	if r := arith(a, b, "+"); r != nil {
		return r
	}
	switch x := norm(a).(type) {
	case string:
		if y, ok := b.(string); ok {
			return x + y
		}
	case *List:
		if y, ok := b.(*List); ok {
			return &List{append(append([]Value{}, x.V...), y.V...)}
		}
	case Tuple:
		if y, ok := b.(Tuple); ok {
			return append(append(Tuple{}, x...), y...)
		}
	case *exp.Expr:
		return x.Plus(toGo(b))
	}
	if y, ok := norm(b).(*exp.Expr); ok {
		return y.RPlus(toGo(a))
	}
	Raise(TypeError, "unsupported operand type(s) for +: '%s' and '%s'", TypeName(a), TypeName(b))
	return nil
}

// Sub is a - b.
func Sub(a, b Value) Value {
	if r := arith(a, b, "-"); r != nil {
		return r
	}
	if x, ok := norm(a).(*exp.Expr); ok {
		return x.Minus(toGo(b))
	}
	if y, ok := norm(b).(*exp.Expr); ok {
		return y.RMinus(toGo(a))
	}
	if x, ok := a.(*Set); ok {
		if y, ok := b.(*Set); ok {
			out := NewSet()
			for _, it := range x.Items() {
				if !y.Has(it) {
					out.Add(it)
				}
			}
			return out
		}
	}
	Raise(TypeError, "unsupported operand type(s) for -: '%s' and '%s'", TypeName(a), TypeName(b))
	return nil
}

// Mul is a * b.
func Mul(a, b Value) Value {
	if r := arith(a, b, "*"); r != nil {
		return r
	}
	switch x := norm(a).(type) {
	case string:
		if n, ok := b.(int); ok {
			return strings.Repeat(x, max(n, 0))
		}
	case *List:
		if n, ok := b.(int); ok {
			var out []Value
			for i := 0; i < n; i++ {
				out = append(out, x.V...)
			}
			return &List{out}
		}
	case *exp.Expr:
		return x.Times(toGo(b))
	}
	if y, ok := norm(b).(*exp.Expr); ok {
		return y.RTimes(toGo(a))
	}
	if s, ok := b.(string); ok {
		if n, ok := a.(int); ok {
			return strings.Repeat(s, max(n, 0))
		}
	}
	Raise(TypeError, "unsupported operand type(s) for *: '%s' and '%s'", TypeName(a), TypeName(b))
	return nil
}

// Div is a / b.
func Div(a, b Value) Value {
	if r := arith(a, b, "/"); r != nil {
		return r
	}
	if x, ok := norm(a).(*exp.Expr); ok {
		return x.Over(toGo(b))
	}
	if y, ok := norm(b).(*exp.Expr); ok {
		return y.ROver(toGo(a))
	}
	Raise(TypeError, "unsupported operand type(s) for /: '%s' and '%s'", TypeName(a), TypeName(b))
	return nil
}

// FloorDiv is a // b.
func FloorDiv(a, b Value) Value {
	if r := arith(a, b, "//"); r != nil {
		return r
	}
	if x, ok := norm(a).(*exp.Expr); ok {
		return x.FloorDiv(toGo(b))
	}
	Raise(TypeError, "unsupported operand type(s) for //: '%s' and '%s'", TypeName(a), TypeName(b))
	return nil
}

// Pow is a ** b.
func Pow(a, b Value) Value {
	if r := arith(a, b, "**"); r != nil {
		return r
	}
	if x, ok := norm(a).(*exp.Expr); ok {
		return x.Power(toGo(b))
	}
	Raise(TypeError, "unsupported operand type(s) for **")
	return nil
}

// BitOp is a <op> b for <<, >>, &, |, ^.
func BitOp(a, b Value, op string) Value {
	if r := arith(a, b, op); r != nil {
		return r
	}
	if x, ok := norm(a).(*exp.Expr); ok {
		switch op {
		case "&":
			return x.BitAnd(toGo(b))
		case "|":
			return x.BitOr(toGo(b))
		}
	}
	if x, ok := a.(*Set); ok {
		if y, ok := b.(*Set); ok {
			out := NewSet()
			switch op {
			case "|":
				for _, it := range x.Items() {
					out.Add(it)
				}
				for _, it := range y.Items() {
					out.Add(it)
				}
			case "&":
				for _, it := range x.Items() {
					if y.Has(it) {
						out.Add(it)
					}
				}
			}
			return out
		}
	}
	Raise(TypeError, "unsupported operand type(s) for %s", op)
	return nil
}

// Mod is a % b (numbers or printf-style string formatting).
func Mod(a, b Value) Value {
	if r := arith(a, b, "%"); r != nil {
		return r
	}
	if s, ok := a.(string); ok {
		var args []Value
		if t, ok := b.(Tuple); ok {
			args = t
		} else {
			args = []Value{b}
		}
		return percentFormat(s, args)
	}
	if x, ok := norm(a).(*exp.Expr); ok {
		return x.Modulo(toGo(b))
	}
	Raise(TypeError, "unsupported operand type(s) for %%: '%s' and '%s'", TypeName(a), TypeName(b))
	return nil
}

func percentFormat(s string, args []Value) string {
	var b strings.Builder
	n := 0
	next := func() Value {
		if n >= len(args) {
			Raise(TypeError, "not enough arguments for format string")
		}
		n++
		return args[n-1]
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case '%':
			b.WriteByte('%')
		case 's':
			b.WriteString(Str(next()))
		case 'r':
			b.WriteString(Repr(next()))
		case 'd', 'i':
			b.WriteString(Str(Int(next())))
		default:
			b.WriteString(Format(next(), string(s[i])))
		}
	}
	return b.String()
}

// Neg is -v.
func Neg(v Value) Value {
	switch x := norm(v).(type) {
	case int:
		return -x
	case float64:
		return -x
	case bool:
		if x {
			return -1
		}
		return 0
	case *big.Int:
		return intResult(new(big.Int).Neg(x))
	case *exp.Expr:
		return x.Negate()
	}
	Raise(TypeError, "bad operand type for unary -: '%s'", TypeName(v))
	return nil
}

// Invert is ~v.
func Invert(v Value) Value {
	switch x := norm(v).(type) {
	case int:
		return ^x
	case *exp.Expr:
		return x.Not()
	}
	Raise(TypeError, "bad operand type for unary ~: '%s'", TypeName(v))
	return nil
}

// Int is int(v).
func Int(v Value) Value {
	switch x := norm(v).(type) {
	case bool:
		if x {
			return 1
		}
		return 0
	case int, *big.Int:
		return x
	case float64:
		return int(x)
	case string:
		z, ok := new(big.Int).SetString(strings.TrimSpace(strings.ReplaceAll(x, "_", "")), 10)
		if !ok {
			Raise(ValueError, "invalid literal for int() with base 10: %s", Repr(x))
		}
		return intResult(z)
	}
	Raise(TypeError, "int() argument must be a string or a number, not '%s'", TypeName(v))
	return nil
}

// Float is float(v).
func Float(v Value) Value {
	switch x := norm(v).(type) {
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			s := strings.ToLower(strings.TrimSpace(x))
			switch s {
			case "inf", "+inf", "infinity":
				return math.Inf(1)
			case "-inf", "-infinity":
				return math.Inf(-1)
			case "nan":
				return math.NaN()
			}
			Raise(ValueError, "could not convert string to float: %s", Repr(x))
		}
		return f
	}
	if f, ok := toFloat(norm(v)); ok {
		return f
	}
	Raise(TypeError, "float() argument must be a string or a number")
	return nil
}

// Len is len(v).
func Len(v Value) int {
	switch x := norm(v).(type) {
	case string:
		return utf8.RuneCountInString(x)
	case *List:
		return len(x.V)
	case Tuple:
		return len(x)
	case *Dict:
		return x.Len()
	case *Set:
		return x.d.Len()
	case *ArgsView:
		return len(x.e.Args())
	case interface{ PyLen() int }:
		return x.PyLen()
	}
	Raise(TypeError, "object of type '%s' has no len()", TypeName(v))
	return 0
}

// Iter returns the items of an iterable.
func Iter(v Value) []Value {
	switch x := norm(v).(type) {
	case *SkippedV:
		skipNow(x)
	case *List:
		return append([]Value(nil), x.V...)
	case Tuple:
		return x
	case *Dict:
		return append([]Value(nil), x.keys...)
	case *Set:
		return append([]Value(nil), x.Items()...)
	case string:
		out := make([]Value, 0, len(x))
		for _, r := range x {
			out = append(out, string(r))
		}
		return out
	case *exp.Expr:
		if !x.Kind().HasArg("expressions") {
			Raise(TypeError, "'%s' object is not iterable", x.ClassName())
		}
		return Iter(norm(x.Expressions()))
	case *ArgsView:
		var out []Value
		for _, a := range x.e.Args() {
			out = append(out, a.Key)
		}
		return out
	case interface{ PyIter() []Value }:
		return x.PyIter()
	}
	Raise(TypeError, "'%s' object is not iterable", TypeName(v))
	return nil
}

// In is `x in container`.
func In(x, container Value) bool {
	checkSkipped(norm(x), norm(container))
	switch c := norm(container).(type) {
	case string:
		s, ok := x.(string)
		if !ok {
			Raise(TypeError, "'in <string>' requires string as left operand, not %s", TypeName(x))
		}
		return strings.Contains(c, s)
	case *Dict:
		_, ok := c.Lookup(x)
		return ok
	case *Set:
		return c.Has(x)
	case *ArgsView:
		k, _ := x.(string)
		for _, a := range c.e.Args() {
			if a.Key == k {
				return true
			}
		}
		return false
	}
	for _, it := range Iter(container) {
		if Eq(it, x) {
			return true
		}
	}
	return false
}

// NotIn is `x not in container`.
func NotIn(x, container Value) bool { return !In(x, container) }

func index(i, n int) int {
	if i < 0 {
		i += n
	}
	if i < 0 || i >= n {
		Raise(IndexError, "list index out of range")
	}
	return i
}

// Item is v[key].
func Item(v, key Value) Value {
	switch x := norm(v).(type) {
	case *SkippedV:
		return x
	case *List:
		return x.V[index(mustInt(key), len(x.V))]
	case Tuple:
		return x[index(mustInt(key), len(x))]
	case string:
		r := []rune(x)
		return string(r[index(mustInt(key), len(r))])
	case *Dict:
		if val, ok := x.Lookup(key); ok {
			return val
		}
		Raise(KeyError, "%s", Repr(key))
	case *ArgsView:
		k := Str(key)
		for _, a := range x.e.Args() {
			if a.Key == k {
				return norm(a.Value)
			}
		}
		Raise(KeyError, "%s", Repr(key))
	case *exp.Expr:
		return x.Bracket(toGo(key))
	case interface{ PyItem(Value) Value }:
		return x.PyItem(key)
	}
	Raise(TypeError, "'%s' object is not subscriptable", TypeName(v))
	return nil
}

func mustInt(v Value) int {
	switch x := v.(type) {
	case int:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	}
	Raise(TypeError, "indices must be integers, not %s", TypeName(v))
	return 0
}

// SetItem is v[key] = val.
func SetItem(v, key, val Value) {
	switch x := norm(v).(type) {
	case *List:
		x.V[index(mustInt(key), len(x.V))] = val
		return
	case *Dict:
		x.Set(key, val)
		return
	case *ArgsView:
		x.e.Set(Str(key), toGo(val))
		return
	}
	Raise(TypeError, "'%s' object does not support item assignment", TypeName(v))
}

// DelItem is del v[key].
func DelItem(v, key Value) {
	switch x := norm(v).(type) {
	case *List:
		i := index(mustInt(key), len(x.V))
		x.V = append(x.V[:i], x.V[i+1:]...)
		return
	case *Dict:
		if !x.Delete(key) {
			Raise(KeyError, "%s", Repr(key))
		}
		return
	}
	Raise(TypeError, "'%s' object does not support item deletion", TypeName(v))
}

// Slice is v[lo:hi:step] (nil bounds are omitted).
func Slice(v, lo, hi, step Value) Value {
	var items []Value
	var s []rune
	switch x := norm(v).(type) {
	case *List:
		items = x.V
	case Tuple:
		items = x
	case string:
		s = []rune(x)
		items = make([]Value, len(s))
		for i, r := range s {
			items[i] = string(r)
		}
	default:
		Raise(TypeError, "'%s' object is not subscriptable", TypeName(v))
	}
	n := len(items)
	st := 1
	if step != nil {
		st = mustInt(step)
	}
	if st == 0 {
		Raise(ValueError, "slice step cannot be zero")
	}
	clamp := func(b Value, def int) int {
		if b == nil {
			return def
		}
		i := mustInt(b)
		if i < 0 {
			i += n
		}
		if st > 0 {
			return max(0, min(i, n))
		}
		return max(-1, min(i, n-1))
	}
	var out []Value
	if st > 0 {
		for i := clamp(lo, 0); i < clamp(hi, n); i += st {
			out = append(out, items[i])
		}
	} else {
		for i := clamp(lo, n-1); i > clamp(hi, -1); i += st {
			out = append(out, items[i])
		}
	}
	switch v.(type) {
	case Tuple:
		return Tuple(out)
	case string:
		var b strings.Builder
		for _, o := range out {
			b.WriteString(o.(string))
		}
		return b.String()
	}
	return &List{out}
}

// Unpack assigns the items of v to the targets.
func Unpack(v Value, targets ...*Value) {
	items := Iter(v)
	if len(items) != len(targets) {
		if len(items) > len(targets) {
			Raise(ValueError, "too many values to unpack (expected %d)", len(targets))
		}
		Raise(ValueError, "not enough values to unpack (expected %d, got %d)", len(targets), len(items))
	}
	for i, t := range targets {
		*t = items[i]
	}
}

// Str is str(v).
func Str(v Value) string {
	switch x := norm(v).(type) {
	case *SkippedV:
		skipNow(x)
	case nil:
		return "None"
	case string:
		return x
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
	case *exp.Expr:
		return x.SQL()
	case *PyErr:
		return x.Msg
	case exp.DType:
		return "DType." + string(x)
	case exp.Kind:
		return "<class '" + x.Name() + "'>"
	case *List, Tuple, *Dict, *Set:
		return Repr(v)
	case fmt.Stringer:
		return x.String()
	}
	return objRepr(v)
}

func objRepr(v Value) string {
	switch x := v.(type) {
	case *BuiltinType:
		return "<class '" + x.Name + "'>"
	case *ExcClass:
		return "<class '" + x.Name + "'>"
	case *Fn:
		return "<function " + x.Name + ">"
	}
	return "<" + TypeName(v) + " object>"
}

// Repr is repr(v).
func Repr(v Value) string {
	switch x := norm(v).(type) {
	case *SkippedV:
		skipNow(x)
	case string:
		return strRepr(x)
	case *exp.Expr:
		return x.Repr()
	case *List:
		return "[" + joinRepr(x.V) + "]"
	case Tuple:
		if len(x) == 1 {
			return "(" + Repr(x[0]) + ",)"
		}
		return "(" + joinRepr(x) + ")"
	case *Dict:
		parts := make([]string, len(x.keys))
		for i, k := range x.keys {
			parts[i] = Repr(k) + ": " + Repr(x.vals[i])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case *Set:
		if x.d.Len() == 0 {
			return "set()"
		}
		return "{" + joinRepr(x.Items()) + "}"
	case exp.DType:
		return "<DType." + string(x) + ": '" + string(x) + "'>"
	case *PyErr:
		return x.Class.Name + "(" + strRepr(x.Msg) + ")"
	case nil, bool, int, *big.Int, float64, exp.Kind:
		return Str(v)
	case fmt.Stringer:
		return x.String()
	}
	return objRepr(v)
}

func joinRepr(items []Value) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = Repr(it)
	}
	return strings.Join(parts, ", ")
}

func strRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case !unicode.IsPrint(r) && r > 0x7f:
			if r <= 0xff {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else if r <= 0xffff {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// floatRepr mirrors Python's repr(float).
func floatRepr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(s, "e")
	e, _ := strconv.Atoi(expStr)
	neg := strings.HasPrefix(mant, "-")
	mant = strings.TrimPrefix(mant, "-")
	digits := strings.Replace(mant, ".", "", 1)
	var out string
	if e < -4 || e >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		sign := "+"
		if e < 0 {
			sign = "-"
			e = -e
		}
		out = fmt.Sprintf("%se%s%02d", m, sign, e)
	} else if e >= 0 {
		if len(digits) <= e+1 {
			out = digits + strings.Repeat("0", e+1-len(digits)) + ".0"
		} else {
			out = digits[:e+1] + "." + digits[e+1:]
		}
	} else {
		out = "0." + strings.Repeat("0", -e-1) + digits
	}
	if neg {
		out = "-" + out
	}
	return out
}

// FV is a formatted value of an f-string: {V!conv:spec}.
type FV struct {
	V    Value
	Conv byte
	Spec string
}

// F builds an f-string from literal strings and FV values.
func F(parts ...Value) string {
	var b strings.Builder
	for _, p := range parts {
		switch x := p.(type) {
		case string:
			b.WriteString(x)
		case FV:
			v := x.V
			switch x.Conv {
			case 'r':
				v = Repr(v)
			case 's':
				v = Str(v)
			}
			b.WriteString(Format(v, x.Spec))
		default:
			b.WriteString(Str(p))
		}
	}
	return b.String()
}

// Format is format(v, spec) for the common specs.
func Format(v Value, spec string) string {
	if spec == "" {
		return Str(v)
	}
	// [[fill]align][sign][#][0][width][,][.precision][type]
	fill, align := " ", byte(0)
	i := 0
	if len(spec) >= 2 && strings.ContainsRune("<>^=", rune(spec[1])) {
		fill, align, i = spec[:1], spec[1], 2
	} else if len(spec) >= 1 && strings.ContainsRune("<>^=", rune(spec[0])) {
		align, i = spec[0], 1
	}
	sign := byte(0)
	if i < len(spec) && strings.ContainsRune("+- ", rune(spec[i])) {
		sign = spec[i]
		i++
	}
	if i < len(spec) && spec[i] == '0' {
		if align == 0 {
			fill, align = "0", '='
		}
		i++
	}
	width := 0
	for i < len(spec) && spec[i] >= '0' && spec[i] <= '9' {
		width = width*10 + int(spec[i]-'0')
		i++
	}
	comma := false
	if i < len(spec) && (spec[i] == ',' || spec[i] == '_') {
		comma = true
		i++
	}
	prec := -1
	if i < len(spec) && spec[i] == '.' {
		i++
		prec = 0
		for i < len(spec) && spec[i] >= '0' && spec[i] <= '9' {
			prec = prec*10 + int(spec[i]-'0')
			i++
		}
	}
	typ := byte(0)
	if i < len(spec) {
		typ = spec[i]
	}
	var s string
	switch typ {
	case 'd':
		s = Str(Int(v))
	case 'x':
		s = fmt.Sprintf("%x", toInt64(v))
	case 'X':
		s = fmt.Sprintf("%X", toInt64(v))
	case 'b':
		s = fmt.Sprintf("%b", toInt64(v))
	case 'o':
		s = fmt.Sprintf("%o", toInt64(v))
	case 'f', 'F':
		f, _ := toFloat(norm(v))
		if prec < 0 {
			prec = 6
		}
		s = strconv.FormatFloat(f, 'f', prec, 64)
	case 'e':
		f, _ := toFloat(norm(v))
		if prec < 0 {
			prec = 6
		}
		s = strconv.FormatFloat(f, 'e', prec, 64)
	case 's', 0:
		s = Str(v)
		if prec >= 0 && typ == 's' {
			r := []rune(s)
			if len(r) > prec {
				s = string(r[:prec])
			}
		}
		if typ == 0 && isFloat(norm(v)) && prec >= 0 {
			f, _ := toFloat(norm(v))
			s = strconv.FormatFloat(f, 'g', prec, 64)
		}
	default:
		s = Str(v)
	}
	if comma {
		neg := strings.HasPrefix(s, "-")
		s = strings.TrimPrefix(s, "-")
		intPart, frac, hasFrac := strings.Cut(s, ".")
		var b strings.Builder
		for j, r := range intPart {
			if j > 0 && (len(intPart)-j)%3 == 0 {
				b.WriteByte(',')
			}
			b.WriteRune(r)
		}
		s = b.String()
		if hasFrac {
			s += "." + frac
		}
		if neg {
			s = "-" + s
		}
	}
	if sign == '+' && isNumber(norm(v)) && !strings.HasPrefix(s, "-") {
		s = "+" + s
	}
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	pad := strings.Repeat(fill, width-n)
	if align == 0 {
		if isNumber(norm(v)) {
			align = '>'
		} else {
			align = '<'
		}
	}
	switch align {
	case '<':
		return s + pad
	case '>':
		return pad + s
	case '^':
		left := (width - n) / 2
		return strings.Repeat(fill, left) + s + strings.Repeat(fill, width-n-left)
	case '=':
		if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
			return s[:1] + pad + s[1:]
		}
		return pad + s
	}
	return s
}

func toInt64(v Value) int64 {
	switch x := Int(v).(type) {
	case int:
		return int64(x)
	case *big.Int:
		return x.Int64()
	}
	return 0
}

// HashKey returns a key identifying v for dict and set membership.
func HashKey(v Value) string {
	switch x := norm(v).(type) {
	case interface{ PyHash() string }:
		return x.PyHash()
	case nil:
		return "None"
	case bool, int, *big.Int, float64:
		if f, ok := x.(float64); ok && f != math.Trunc(f) {
			return "f:" + floatRepr(f)
		}
		b, ok := toBig(x)
		if !ok {
			f, _ := x.(float64)
			b, _ = new(big.Float).SetFloat64(f).Int(nil)
		}
		return "n:" + b.String()
	case string:
		return "s:" + x
	case Tuple:
		parts := make([]string, len(x))
		for i, it := range x {
			parts[i] = HashKey(it)
		}
		return "t:(" + strings.Join(parts, ",") + ")"
	case *exp.Expr:
		return "e:" + x.ClassName() + ":" + strconv.FormatUint(x.Hash(), 16)
	case exp.Kind:
		return "k:" + x.Name()
	case exp.DType:
		return "d:" + string(x)
	case *ExcClass:
		return "x:" + x.Name
	case *List, *Dict, *Set:
		Raise(TypeError, "unhashable type: '%s'", TypeName(v))
	}
	return fmt.Sprintf("p:%T:%p", v, v)
}

// Hash is hash(v) (only equality of hashes is meaningful).
func Hash(v Value) Value {
	if e, ok := norm(v).(*exp.Expr); ok {
		return HashKey(e)
	}
	return HashKey(v)
}
