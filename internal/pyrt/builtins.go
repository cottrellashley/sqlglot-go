package pyrt

import (
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
)

// BuiltinType is a builtin class such as str or int (callable and usable with isinstance).
type BuiltinType struct {
	Name  string
	Check func(Value) bool
	New   func(Args) Value
}

// PyCall implements calling the type.
func (t *BuiltinType) PyCall(a Args) Value { return t.New(a) }

// PyAttr implements Attrer.
func (t *BuiltinType) PyAttr(name string) (Value, bool) {
	if name == "__name__" {
		return t.Name, true
	}
	if v, ok := typeAttrs[t][name]; ok {
		return v, true
	}
	return nil, false
}

// typeAttrs holds class attributes (static methods) of builtin types.
var typeAttrs = map[*BuiltinType]map[string]Value{}

var builtins = map[string]Value{}

func builtin(name string, f func(a Args) Value) { builtins[name] = Func(name, f) }

// IsInstance is isinstance(v, cls).
func IsInstance(v, cls Value) bool {
	v = norm(v)
	checkSkipped(v)
	switch c := cls.(type) {
	case Tuple:
		for _, x := range c {
			if IsInstance(v, x) {
				return true
			}
		}
		return false
	case exp.Kind:
		e, ok := v.(*exp.Expr)
		return ok && e.Is(c)
	case *ExcClass:
		e, ok := v.(*PyErr)
		return ok && e.Class.IsSubclass(c)
	case *BuiltinType:
		return c.Check(v)
	case *DialectClassV:
		d, ok := v.(*DialectV)
		return ok && d.D.DialectClass == c.C
	}
	NotPorted("isinstance(_, %s)", TypeName(cls))
	return false
}

func init() {
	builtins["str"] = &BuiltinType{"str", func(v Value) bool { _, ok := v.(string); return ok }, func(a Args) Value {
		if len(a.Pos) == 0 {
			return ""
		}
		return Str(a.Pos[0])
	}}
	builtins["int"] = &BuiltinType{"int", func(v Value) bool {
		switch v.(type) {
		case int, bool, *big.Int:
			return true
		}
		return false
	}, func(a Args) Value {
		if len(a.Pos) == 0 {
			return 0
		}
		if len(a.Pos) > 1 {
			z, ok := new(big.Int).SetString(Str(a.Pos[0]), mustInt(a.Pos[1]))
			if !ok {
				Raise(ValueError, "invalid literal for int()")
			}
			return intResult(z)
		}
		return Int(a.Pos[0])
	}}
	builtins["float"] = &BuiltinType{"float", func(v Value) bool { _, ok := v.(float64); return ok }, func(a Args) Value {
		if len(a.Pos) == 0 {
			return 0.0
		}
		return Float(a.Pos[0])
	}}
	builtins["bool"] = &BuiltinType{"bool", func(v Value) bool { _, ok := v.(bool); return ok }, func(a Args) Value {
		return len(a.Pos) > 0 && Truth(a.Pos[0])
	}}
	builtins["list"] = &BuiltinType{"list", func(v Value) bool { _, ok := v.(*List); return ok }, func(a Args) Value {
		if len(a.Pos) == 0 {
			return &List{}
		}
		return &List{Iter(a.Pos[0])}
	}}
	builtins["tuple"] = &BuiltinType{"tuple", func(v Value) bool { _, ok := v.(Tuple); return ok }, func(a Args) Value {
		if len(a.Pos) == 0 {
			return Tuple{}
		}
		return Tuple(Iter(a.Pos[0]))
	}}
	builtins["dict"] = &BuiltinType{"dict", func(v Value) bool { _, ok := v.(*Dict); return ok }, func(a Args) Value {
		d := NewDict()
		if len(a.Pos) > 0 {
			if src, ok := a.Pos[0].(*Dict); ok {
				for i, k := range src.keys {
					d.Set(k, src.vals[i])
				}
			} else {
				for _, it := range Iter(a.Pos[0]) {
					kv := Iter(it)
					d.Set(kv[0], kv[1])
				}
			}
		}
		for _, kw := range a.KW {
			d.Set(kw.K, kw.V)
		}
		return d
	}}
	setType := &BuiltinType{"set", func(v Value) bool { _, ok := v.(*Set); return ok }, func(a Args) Value {
		if len(a.Pos) == 0 {
			return NewSet()
		}
		return NewSet(Iter(a.Pos[0])...)
	}}
	builtins["set"] = setType
	builtins["frozenset"] = setType
	builtins["object"] = &BuiltinType{"object", func(v Value) bool { return true }, func(a Args) Value { return &struct{}{} }}
	builtins["type"] = Func("type", func(a Args) Value { return typeOf(a.Pos[0]) })
	builtins["None"] = nil
	builtins["True"] = true
	builtins["False"] = false
	for _, c := range []*ExcClass{Exception, BaseException, ValueError, TypeError, KeyError, IndexError, AttributeError,
		AssertionError, NotImplementedError, RuntimeError, RecursionError, ZeroDivisionError, StopIteration, OSError, FileNotFoundError} {
		builtins[c.Name] = c
	}

	builtin("isinstance", func(a Args) Value { return IsInstance(a.Pos[0], a.Pos[1]) })
	builtin("issubclass", func(a Args) Value {
		switch c := a.Pos[0].(type) {
		case exp.Kind:
			b, ok := a.Pos[1].(exp.Kind)
			return ok && c.IsA(b)
		case *ExcClass:
			b, ok := a.Pos[1].(*ExcClass)
			return ok && c.IsSubclass(b)
		}
		NotPorted("issubclass(%s)", TypeName(a.Pos[0]))
		return nil
	})
	builtin("len", func(a Args) Value { return Len(a.Pos[0]) })
	builtin("repr", func(a Args) Value { return Repr(a.Pos[0]) })
	builtin("hash", func(a Args) Value { return Hash(a.Pos[0]) })
	builtin("id", func(a Args) Value { return fmt.Sprintf("%p", a.Pos[0]) })
	builtin("abs", func(a Args) Value {
		switch x := a.Pos[0].(type) {
		case int:
			if x < 0 {
				return -x
			}
			return x
		case float64:
			return math.Abs(x)
		}
		NotPorted("abs(%s)", TypeName(a.Pos[0]))
		return nil
	})
	builtin("round", func(a Args) Value {
		f, _ := toFloat(a.Pos[0])
		if len(a.Pos) > 1 {
			p := math.Pow(10, float64(mustInt(a.Pos[1])))
			return math.RoundToEven(f*p) / p
		}
		return int(math.RoundToEven(f))
	})
	builtin("ord", func(a Args) Value { return int([]rune(Str(a.Pos[0]))[0]) })
	builtin("chr", func(a Args) Value { return string(rune(mustInt(a.Pos[0]))) })
	builtin("sorted", func(a Args) Value { return &List{sortValues(Iter(a.Pos[0]), a)} })
	builtin("reversed", func(a Args) Value {
		items := Iter(a.Pos[0])
		out := make([]Value, len(items))
		for i, it := range items {
			out[len(items)-1-i] = it
		}
		return &List{out}
	})
	builtin("range", func(a Args) Value {
		start, stop, step := 0, 0, 1
		switch len(a.Pos) {
		case 1:
			stop = mustInt(a.Pos[0])
		case 2:
			start, stop = mustInt(a.Pos[0]), mustInt(a.Pos[1])
		default:
			start, stop, step = mustInt(a.Pos[0]), mustInt(a.Pos[1]), mustInt(a.Pos[2])
		}
		var out []Value
		for i := start; (step > 0 && i < stop) || (step < 0 && i > stop); i += step {
			out = append(out, i)
		}
		return &List{out}
	})
	builtin("enumerate", func(a Args) Value {
		start := 0
		if s := a.Get(1, "start", nil); s != nil {
			start = mustInt(s)
		}
		var out []Value
		for i, it := range Iter(a.Pos[0]) {
			out = append(out, Tuple{start + i, it})
		}
		return &List{out}
	})
	builtin("zip", func(a Args) Value {
		var seqs [][]Value
		n := math.MaxInt
		for _, p := range a.Pos {
			s := Iter(p)
			seqs = append(seqs, s)
			n = min(n, len(s))
		}
		if len(seqs) == 0 {
			return &List{}
		}
		out := make([]Value, n)
		for i := 0; i < n; i++ {
			t := make(Tuple, len(seqs))
			for j, s := range seqs {
				t[j] = s[i]
			}
			out[i] = t
		}
		return &List{out}
	})
	builtin("map", func(a Args) Value {
		var out []Value
		for _, it := range Iter(a.Pos[1]) {
			out = append(out, Call(a.Pos[0], it))
		}
		return &List{out}
	})
	builtin("filter", func(a Args) Value {
		var out []Value
		for _, it := range Iter(a.Pos[1]) {
			if (a.Pos[0] == nil && Truth(it)) || (a.Pos[0] != nil && Truth(Call(a.Pos[0], it))) {
				out = append(out, it)
			}
		}
		return &List{out}
	})
	builtin("iter", func(a Args) Value { return &Iterator{items: Iter(a.Pos[0])} })
	builtin("next", func(a Args) Value {
		it, ok := a.Pos[0].(*Iterator)
		if !ok {
			it = &Iterator{items: Iter(a.Pos[0])}
		}
		if it.pos >= len(it.items) {
			if len(a.Pos) > 1 {
				return a.Pos[1]
			}
			Raise(StopIteration, "")
		}
		it.pos++
		return it.items[it.pos-1]
	})
	builtin("any", func(a Args) Value {
		for _, it := range Iter(a.Pos[0]) {
			if Truth(it) {
				return true
			}
		}
		return false
	})
	builtin("all", func(a Args) Value {
		for _, it := range Iter(a.Pos[0]) {
			if !Truth(it) {
				return false
			}
		}
		return true
	})
	builtin("sum", func(a Args) Value {
		var total Value = 0
		if len(a.Pos) > 1 {
			total = a.Pos[1]
		}
		for _, it := range Iter(a.Pos[0]) {
			total = Add(total, it)
		}
		return total
	})
	minmax := func(name string, less bool) {
		builtin(name, func(a Args) Value {
			items := a.Pos
			if len(items) == 1 {
				items = Iter(items[0])
			}
			if len(items) == 0 {
				if d, ok := a.Kwarg("default"); ok {
					return d
				}
				Raise(ValueError, "%s() arg is an empty sequence", name)
			}
			key, _ := a.Kwarg("key")
			best := items[0]
			for _, it := range items[1:] {
				x, y := it, best
				if key != nil {
					x, y = Call(key, it), Call(key, best)
				}
				c := cmp(x, y, "<")
				if (less && c < 0) || (!less && c > 0) {
					best = it
				}
			}
			return best
		})
	}
	minmax("min", true)
	minmax("max", false)
	builtin("getattr", func(a Args) Value {
		if len(a.Pos) > 2 {
			if !HasAttr(a.Pos[0], Str(a.Pos[1])) {
				return a.Pos[2]
			}
		}
		return A(a.Pos[0], Str(a.Pos[1]))
	})
	builtin("hasattr", func(a Args) Value { return HasAttr(a.Pos[0], Str(a.Pos[1])) })
	builtin("setattr", func(a Args) Value { SetA(a.Pos[0], Str(a.Pos[1]), a.Pos[2]); return nil })
	builtin("callable", func(a Args) Value {
		switch a.Pos[0].(type) {
		case *Fn, exp.Kind, *BuiltinType, *ExcClass:
			return true
		}
		return false
	})
	builtin("print", func(a Args) Value {
		parts := make([]string, len(a.Pos))
		for i, p := range a.Pos {
			parts[i] = Str(p)
		}
		fmt.Println(strings.Join(parts, " "))
		return nil
	})
}

// Iterator is the result of iter().
type Iterator struct {
	items []Value
	pos   int
}

// PyIter implements iteration over the remaining items.
func (it *Iterator) PyIter() []Value {
	rest := it.items[it.pos:]
	it.pos = len(it.items)
	return rest
}
