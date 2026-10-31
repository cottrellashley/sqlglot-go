// Package pyrt is a small runtime with Python semantics used by the test suites that are
// converted mechanically from sqlglot's Python tests (see tools/port/convert_tests.py).
//
// Every Python value is represented by a Value: nil (None), bool, int, float64, string,
// *List, Tuple, *Dict, *Set, *exp.Expr, exp.Kind (an expression class), exp.DType, *Fn
// (functions and bound methods), *Module, *ExcClass, *PyErr and a few sqlglot objects.
// Python exceptions are Go panics carrying a *PyErr.
package pyrt

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
)

// Value is any Python value.
type Value = any

// KW is a keyword argument (name=value) passed to M or Call.
type KW struct {
	K string
	V Value
}

// Kw builds a keyword argument.
func Kw(k string, v Value) KW { return KW{k, v} }

// StarArg is *iterable in a call.
type StarArg struct{ V Value }

// StarStarArg is **mapping in a call.
type StarStarArg struct{ V Value }

// Star marks *iterable in a call or display.
func Star(v Value) StarArg { return StarArg{v} }

// StarStar marks **mapping in a call or dict display.
func StarStar(v Value) StarStarArg { return StarStarArg{v} }

// Args are the evaluated arguments of a call.
type Args struct {
	Pos []Value
	KW  []KW
}

// Get returns the i-th positional argument, or the keyword argument name, or def.
func (a Args) Get(i int, name string, def Value) Value {
	if i >= 0 && i < len(a.Pos) {
		return a.Pos[i]
	}
	for _, kw := range a.KW {
		if kw.K == name {
			return kw.V
		}
	}
	return def
}

// Has reports whether the i-th positional or the named keyword argument was passed.
func (a Args) Has(i int, name string) bool {
	if i >= 0 && i < len(a.Pos) {
		return true
	}
	for _, kw := range a.KW {
		if kw.K == name {
			return true
		}
	}
	return false
}

// Kwarg returns a keyword argument.
func (a Args) Kwarg(name string) (Value, bool) {
	for _, kw := range a.KW {
		if kw.K == name {
			return kw.V, true
		}
	}
	return nil, false
}

func buildArgs(raw []Value) Args {
	var a Args
	for _, r := range raw {
		switch x := r.(type) {
		case KW:
			a.KW = append(a.KW, x)
		case StarArg:
			a.Pos = append(a.Pos, Iter(x.V)...)
		case StarStarArg:
			d, ok := x.V.(*Dict)
			if !ok {
				Raise(TypeError, "argument after ** must be a mapping")
			}
			for i, k := range d.keys {
				a.KW = append(a.KW, KW{Str(k), d.vals[i]})
			}
		default:
			a.Pos = append(a.Pos, norm(r))
		}
	}
	for i := range a.KW {
		a.KW[i].V = norm(a.KW[i].V)
	}
	return a
}

// Fn is a Python callable (function, bound method or builtin).
type Fn struct {
	Name string
	F    func(a Args) Value
	// checksSkips marks functions that handle *SkippedV arguments themselves (assertions).
	checksSkips bool
}

// Func wraps a Go function as a Python callable.
func Func(name string, f func(a Args) Value) *Fn { return &Fn{Name: name, F: f} }

// Lambda wraps a positional-only Go closure as a Python function.
func Lambda(f func(args ...Value) Value) *Fn {
	return &Fn{Name: "<lambda>", F: func(a Args) Value { return f(a.Pos...) }}
}

// Module is a namespace object (a Python module or class with static attributes).
type Module struct {
	Name  string
	Attrs map[string]Value
	Get   func(name string) (Value, bool)
}

func (m *Module) lookup(name string) (Value, bool) {
	if v, ok := m.Attrs[name]; ok {
		return v, true
	}
	if m.Get != nil {
		return m.Get(name)
	}
	return nil, false
}

// List is a Python list.
type List struct{ V []Value }

// NewList builds a list (starred items are expanded).
func NewList(items ...Value) *List { return &List{expand(items)} }

// Tuple is a Python tuple.
type Tuple []Value

// NewTuple builds a tuple (starred items are expanded).
func NewTuple(items ...Value) Tuple { return Tuple(expand(items)) }

func expand(items []Value) []Value {
	out := make([]Value, 0, len(items))
	for _, it := range items {
		if s, ok := it.(StarArg); ok {
			out = append(out, Iter(s.V)...)
		} else {
			out = append(out, it)
		}
	}
	return out
}

// Dict is an insertion ordered Python dict.
type Dict struct {
	keys  []Value
	vals  []Value
	index map[string]int
	// onSet and onDelete mirror changes into a Go value the dict is a view of (e.g. meta).
	onSet    func(k, v Value)
	onDelete func(k Value)
}

// NewDict builds a dict from alternating keys and values (StarStar items are merged).
func NewDict(kv ...Value) *Dict {
	d := &Dict{index: map[string]int{}}
	for i := 0; i < len(kv); i++ {
		if s, ok := kv[i].(StarStarArg); ok {
			src, _ := s.V.(*Dict)
			if src != nil {
				for j, k := range src.keys {
					d.Set(k, src.vals[j])
				}
			}
			continue
		}
		d.Set(kv[i], kv[i+1])
		i++
	}
	return d
}

// Set assigns d[k] = v.
func (d *Dict) Set(k, v Value) {
	if d.onSet != nil {
		d.onSet(k, v)
	}
	h := HashKey(k)
	if i, ok := d.index[h]; ok {
		d.vals[i] = v
		return
	}
	d.index[h] = len(d.keys)
	d.keys = append(d.keys, k)
	d.vals = append(d.vals, v)
}

// Lookup returns d[k].
func (d *Dict) Lookup(k Value) (Value, bool) {
	if i, ok := d.index[HashKey(k)]; ok {
		return d.vals[i], true
	}
	return nil, false
}

// Delete removes k.
func (d *Dict) Delete(k Value) bool {
	h := HashKey(k)
	i, ok := d.index[h]
	if !ok {
		return false
	}
	if d.onDelete != nil {
		d.onDelete(k)
	}
	d.keys = append(d.keys[:i], d.keys[i+1:]...)
	d.vals = append(d.vals[:i], d.vals[i+1:]...)
	d.index = map[string]int{}
	for j, key := range d.keys {
		d.index[HashKey(key)] = j
	}
	return true
}

// Keys returns the keys in insertion order.
func (d *Dict) Keys() []Value { return d.keys }

// Values returns the values in insertion order.
func (d *Dict) Values() []Value { return d.vals }

// Len returns the number of items.
func (d *Dict) Len() int { return len(d.keys) }

// Set is a Python set (iteration follows insertion order).
type Set struct{ d *Dict }

// NewSet builds a set.
func NewSet(items ...Value) *Set {
	s := &Set{NewDict()}
	for _, it := range expand(items) {
		s.Add(it)
	}
	return s
}

// Add inserts v.
func (s *Set) Add(v Value) { s.d.Set(v, true) }

// Has reports whether v is in the set.
func (s *Set) Has(v Value) bool { _, ok := s.d.Lookup(v); return ok }

// Items returns the elements.
func (s *Set) Items() []Value { return s.d.keys }

// ExcClass is a Python exception class.
type ExcClass struct {
	Name string
	Base *ExcClass
}

// IsSubclass reports whether c is base or derives from it.
func (c *ExcClass) IsSubclass(base *ExcClass) bool {
	for x := c; x != nil; x = x.Base {
		if x == base {
			return true
		}
	}
	return false
}

// Exception classes.
var (
	BaseException       = &ExcClass{"BaseException", nil}
	Exception           = &ExcClass{"Exception", BaseException}
	ValueError          = &ExcClass{"ValueError", Exception}
	TypeError           = &ExcClass{"TypeError", Exception}
	KeyError            = &ExcClass{"KeyError", Exception}
	IndexError          = &ExcClass{"IndexError", Exception}
	AttributeError      = &ExcClass{"AttributeError", Exception}
	AssertionError      = &ExcClass{"AssertionError", Exception}
	NotImplementedError = &ExcClass{"NotImplementedError", Exception}
	RuntimeError        = &ExcClass{"RuntimeError", Exception}
	RecursionError      = &ExcClass{"RecursionError", RuntimeError}
	ZeroDivisionError   = &ExcClass{"ZeroDivisionError", Exception}
	StopIteration       = &ExcClass{"StopIteration", Exception}
	OSError             = &ExcClass{"OSError", Exception}
	FileNotFoundError   = &ExcClass{"FileNotFoundError", OSError}
	SqlglotError        = &ExcClass{"SqlglotError", Exception}
	UnsupportedError    = &ExcClass{"UnsupportedError", SqlglotError}
	ParseError          = &ExcClass{"ParseError", SqlglotError}
	TokenError          = &ExcClass{"TokenError", SqlglotError}
	OptimizeError       = &ExcClass{"OptimizeError", SqlglotError}
	SchemaError         = &ExcClass{"SchemaError", SqlglotError}
	ExecuteError        = &ExcClass{"ExecuteError", SqlglotError}
	// NotPortedError marks Python features the runtime does not implement.
	NotPortedError = &ExcClass{"NotPortedError", Exception}
	// UnportedDialectError is raised when a statement needs a dialect that is not ported yet.
	UnportedDialectError = &ExcClass{"UnportedDialectError", BaseException}
)

// PyErr is a raised Python exception.
type PyErr struct {
	Class *ExcClass
	Msg   string
	// Go is the underlying Go error, if any (e.g. *errs.ParseError).
	Go error
}

func (e *PyErr) Error() string {
	if e.Msg == "" {
		return e.Class.Name
	}
	return e.Class.Name + ": " + e.Msg
}

// Raise raises a Python exception.
func Raise(c *ExcClass, format string, args ...any) {
	panic(&PyErr{Class: c, Msg: fmt.Sprintf(format, args...)})
}

// NotPorted raises NotPortedError for unsupported Python features.
func NotPorted(format string, args ...any) {
	panic(&PyErr{Class: NotPortedError, Msg: fmt.Sprintf(format, args...)})
}

// None-normalization: typed nil pointers become nil.
func norm(v Value) Value {
	switch x := v.(type) {
	case *LazyV:
		return norm(x.get())
	case *exp.Expr:
		if x == nil {
			return nil
		}
	case []*exp.Expr:
		return exprList(x)
	case []string:
		out := make([]Value, len(x))
		for i, s := range x {
			out[i] = s
		}
		return &List{out}
	}
	return v
}

func exprList(xs []*exp.Expr) *List {
	out := make([]Value, len(xs))
	for i, x := range xs {
		out[i] = norm(x)
	}
	return &List{out}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TypeName is type(v).__name__.
func TypeName(v Value) string {
	switch x := v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case *List:
		return "list"
	case Tuple:
		return "tuple"
	case *Dict:
		return "dict"
	case *Set:
		return "set"
	case *exp.Expr:
		return x.ClassName()
	case exp.Kind:
		return "type"
	case *Fn:
		return "function"
	case BytesV:
		return "bytes"
	case *Module:
		return "module"
	case *PyErr:
		return x.Class.Name
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", v), "*")
}

// SkippedV is the result of an operation that needs a dialect that is not ported yet. Every
// operation on it yields it again, and assertions involving it report the statement as
// skipped instead of failed.
type SkippedV struct{ Dialect string }

// skipEvents counts the operations that were skipped because of unported dialects.
var skipEvents int

func skipNow(s *SkippedV) {
	panic(&PyErr{Class: UnportedDialectError, Msg: s.Dialect})
}

func checkSkipped(vals ...Value) {
	for _, v := range vals {
		if s, ok := v.(*SkippedV); ok {
			skipNow(s)
		}
	}
}

// NP raises NotPortedError (used by generated code for unsupported Python constructs).
func NP(format string, args ...any) Value {
	NotPorted(format, args...)
	return nil
}

// LazyV is a module-level value evaluated on first use.
type LazyV struct {
	f    func() Value
	done bool
	v    Value
}

// Lazy defers the evaluation of a module-level value.
func Lazy(f func() Value) Value { return &LazyV{f: f} }

func (l *LazyV) get() Value {
	if !l.done {
		l.v = l.f()
		l.done = true
	}
	return l.v
}

// Try runs f and returns the raised exception, if any.
func Try(f func()) *PyErr { return catch(f) }

// Matches reports whether err is an instance of the exception class (or tuple of classes).
func Matches(err *PyErr, cls Value) bool { return matchesExc(err, cls) }

// KwDict returns the keyword arguments of a call as a dict (**kwargs).
func KwDict(a Args) Value {
	d := NewDict()
	for _, kw := range a.KW {
		d.Set(kw.K, kw.V)
	}
	return d
}
