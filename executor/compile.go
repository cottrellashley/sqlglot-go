package executor

// The expression compiler. sqlglot's Python executor generates Python source for every
// expression (sqlglot/generators/python.py) and evaluates it with the functions of env.py;
// this compiler produces Go closures with the same semantics.

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
)

// code is a compiled expression, evaluated in a context.
type code func(c *Context) any

// scopeValue is the value of the `scope` variable passed to subquery functions: the readers
// of the current context.
type scopeValue map[string]reader

type compiler struct {
	e      *Executor
	lambda map[string]int // lambda parameters in scope -> index in the frame
	frame  *[]any
}

type compileError struct{ msg string }

func (e *compileError) Error() string { return e.msg }

func compileErrorf(format string, args ...any) {
	panic(&compileError{fmt.Sprintf(format, args...)})
}

func (cp *compiler) function(name string) Func {
	if f, ok := cp.e.env[name]; ok {
		return f
	}
	compileErrorf("name '%s' is not defined", name)
	return nil
}

func call(f Func, args []code) code {
	return func(c *Context) any {
		vals := make([]any, len(args))
		for i, a := range args {
			vals[i] = a(c)
		}
		return f(vals...)
	}
}

func constant(v any) code { return func(*Context) any { return v } }

// renameArgs mirrors _rename: the argument values in insertion order, without NULL and boolean
// values (Generator.format_args drops them).
func (cp *compiler) renameArgs(e *exp.Expr) []code {
	args := e.Args()
	var vals []any
	for _, a := range args {
		vals = append(vals, a.Value)
	}
	flatten := len(vals) == 1 || (e.Is(exp.Func) && e.Kind().IsVarLenArgs())
	var out []code
	for _, v := range vals {
		switch x := v.(type) {
		case nil, bool:
		case *exp.Expr:
			out = append(out, cp.compile(x))
		case []*exp.Expr:
			if flatten {
				for _, it := range x {
					out = append(out, cp.compile(it))
				}
			} else {
				items := make([]code, len(x))
				for i, it := range x {
					items[i] = cp.compile(it)
				}
				out = append(out, listOf(items))
			}
		case string:
			compileErrorf("name '%s' is not defined", x)
		default:
			compileErrorf("cannot compile argument %v of %s", x, e.ClassName())
		}
	}
	return out
}

func listOf(items []code) code {
	return func(c *Context) any {
		out := make([]any, len(items))
		for i, it := range items {
			out[i] = it(c)
		}
		return out
	}
}

func (cp *compiler) rename(e *exp.Expr) code {
	return call(cp.function(strings.ToUpper(e.Key())), cp.renameArgs(e))
}

func numberLiteral(text string) any {
	s := strings.ReplaceAll(text, "_", "")
	if b, ok := new(big.Int).SetString(s, 10); ok {
		return normInt(b)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		compileErrorf("invalid number literal %s", text)
	}
	return f
}

func (cp *compiler) compile(e *exp.Expr) code {
	if e == nil {
		return constant(nil)
	}
	switch e.Kind() {
	case exp.Literal:
		if e.IsString() {
			return constant(e.Text("this"))
		}
		return constant(numberLiteral(e.Text("this")))
	case exp.Boolean:
		return constant(e.Bool("this"))
	case exp.Null:
		return constant(nil)
	case exp.Star:
		return constant(1)
	case exp.Alias, exp.Paren:
		return cp.compile(e.This())
	case exp.Identifier:
		if i, ok := cp.lambda[strings.ToLower(e.Name())]; ok {
			frame := cp.frame
			return func(*Context) any { return (*frame)[i] }
		}
		return constant(e.Name())
	case exp.Var:
		if e.Name() == "scope" {
			return func(c *Context) any { return scopeValue(c.scope) }
		}
		if i, ok := cp.lambda[strings.ToLower(e.Name())]; ok {
			frame := cp.frame
			return func(*Context) any { return (*frame)[i] }
		}
		compileErrorf("name '%s' is not defined", e.Name())
	case exp.Column:
		if i, ok := cp.lambda[strings.ToLower(e.Name())]; ok && e.TableName() == "" {
			frame := cp.frame
			return func(*Context) any { return (*frame)[i] }
		}
		table, name := e.TableName(), e.Name()
		return func(c *Context) any {
			r, ok := c.scope[table]
			if !ok {
				panic(&keyError{table})
			}
			return r.get(name)
		}
	case exp.Neg:
		this := cp.compile(e.This())
		return func(c *Context) any { return neg(this(c)) }
	case exp.Not:
		return call(sqlNot, []code{cp.compile(e.This())})
	case exp.And, exp.Or:
		left, right := cp.compile(e.This()), cp.compile(e.Expression())
		if e.IsKind(exp.And) {
			return func(c *Context) any { return sqlAnd(func() any { return left(c) }, func() any { return right(c) }) }
		}
		return func(c *Context) any { return sqlOr(func() any { return left(c) }, func() any { return right(c) }) }
	case exp.Array:
		return listOf(cp.compileList(e.Expressions()))
	case exp.Tuple:
		items := cp.compileList(e.Expressions())
		return func(c *Context) any { return Tuple(listOf(items)(c).([]any)) }
	case exp.Case:
		return cp.compileCase(e)
	case exp.Cast, exp.TryCast:
		to := e.Get("to")
		dtype := to.DataTypeThis()
		this := cp.compile(e.This())
		f := cp.function("CAST")
		return func(c *Context) any { return f(this(c), dtype) }
	case exp.Concat:
		name := "CONCAT"
		if e.Bool("safe") {
			name = "SAFECONCAT"
		}
		return call(cp.function(name), cp.compileList(e.Expressions()))
	case exp.Distinct:
		items := cp.compileList(e.Expressions())
		if len(items) == 1 {
			return func(c *Context) any { return newValueSet(values(items[0](c))) }
		}
		return func(c *Context) any {
			cols := make([][]any, len(items))
			for i, it := range items {
				cols[i] = values(it(c))
			}
			var rows []any
			for r := 0; len(cols) > 0 && r < len(cols[0]); r++ {
				row := make([]any, len(cols))
				ok := true
				for i := range cols {
					if r >= len(cols[i]) || cols[i][r] == nil {
						ok = false
						break
					}
					row[i] = cols[i][r]
				}
				if ok {
					rows = append(rows, Tuple(row))
				}
			}
			return newValueSet(rows)
		}
	case exp.Div:
		return cp.compileDiv(e)
	case exp.DPipe:
		name := "CONCAT"
		switch {
		case e.This().IsType(exp.DTypeArray) || e.Expression().IsType(exp.DTypeArray):
			name = "ARRAYCONCAT"
		case e.Bool("safe"):
			name = "SAFECONCAT"
		}
		return call(cp.function(name), []code{cp.compile(e.This()), cp.compile(e.Expression())})
	case exp.Extract:
		part := strings.ToLower(e.Name())
		return call(cp.function("EXTRACT"), []code{constant(part), cp.compile(e.Expression())})
	case exp.ILike, exp.Like:
		name := "LIKE"
		if e.IsKind(exp.ILike) {
			name = "ILIKE"
		}
		f := call(cp.function(name), []code{cp.compile(e.This()), cp.compile(e.Expression())})
		if e.Bool("negate") {
			return call(sqlNot, []code{f})
		}
		return f
	case exp.In:
		return call(cp.function("IN"), append([]code{cp.compile(e.This())}, cp.compileList(e.Expressions())...))
	case exp.Interval:
		unit := ""
		if u := e.Get("unit"); u != nil {
			unit = u.SQL()
		}
		return call(cp.function("INTERVAL"), []code{cp.compile(e.This()), constant(unit)})
	case exp.Is:
		return cp.compileIs(e)
	case exp.JSONExtract:
		args := []code{cp.compile(e.This()), cp.compile(e.Expression())}
		return call(cp.function("JSONEXTRACT"), append(args, cp.compileList(e.Expressions())...))
	case exp.JSONPath:
		var keys []any
		for _, p := range e.Expressions()[1:] {
			switch p.Kind() {
			case exp.JSONPathKey:
				keys = append(keys, Str(p.Arg("this")))
			case exp.JSONPathSubscript:
				keys = append(keys, Str(p.Arg("this")))
			default:
				compileErrorf("unsupported JSON path part %s", p.ClassName())
			}
		}
		return constant(keys)
	case exp.Lambda:
		return cp.compileLambda(e)
	case exp.Ordered:
		this := cp.compile(e.This())
		desc, nullsFirst := e.Bool("desc"), e.Bool("nulls_first")
		return func(c *Context) any { return ordered(this(c), desc, nullsFirst) }
	case exp.Bracket:
		this := cp.compile(e.This())
		exprs := e.Expressions()
		if len(exprs) == 1 && exprs[0].IsKind(exp.Slice) {
			s := exprs[0]
			lo, hi, step := cp.compile(s.This()), cp.compile(s.Expression()), cp.compile(s.Get("step"))
			return func(c *Context) any { return sliceValue(this(c), lo(c), hi(c), step(c)) }
		}
		if len(exprs) != 1 {
			compileErrorf("unsupported subscript with %d values", len(exprs))
		}
		key := cp.compile(exprs[0])
		return func(c *Context) any { return index(this(c), key(c)) }
	case exp.Anonymous:
		name := strings.ToUpper(e.Name())
		return call(cp.function(name), cp.compileList(e.Expressions()))
	case exp.Between:
		return cp.rename(e)
	}
	if e.Is(exp.Binary) || e.Kind().IsFunction() {
		return cp.rename(e)
	}
	compileErrorf("cannot execute expression %s", e.ClassName())
	return nil
}

func (cp *compiler) compileList(es []*exp.Expr) []code {
	out := make([]code, len(es))
	for i, e := range es {
		out[i] = cp.compile(e)
	}
	return out
}

// sliceIndices mirrors slice.indices(n): the start, stop and step of a Python slice.
func sliceIndices(lo, hi, step any, n int) (int, int, int) {
	st := 1
	if step != nil {
		st = asInt(step)
		if st == 0 {
			panic(errValuef("slice step cannot be zero"))
		}
	}
	adjust := func(b any, def int) int {
		if b == nil {
			return def
		}
		i := asInt(b)
		if i < 0 {
			i += n
			if i < 0 {
				if st < 0 {
					return -1
				}
				return 0
			}
		} else if i >= n {
			if st < 0 {
				return n - 1
			}
			return n
		}
		return i
	}
	if st > 0 {
		return adjust(lo, 0), adjust(hi, n), st
	}
	return adjust(lo, n-1), adjust(hi, -1), st
}

func sliceSeq[T any](s []T, lo, hi, step any) []T {
	start, stop, st := sliceIndices(lo, hi, step, len(s))
	out := []T{}
	if st > 0 {
		for i := start; i < stop; i += st {
			out = append(out, s[i])
		}
	} else {
		for i := start; i > stop; i += st {
			out = append(out, s[i])
		}
	}
	return out
}

func sliceValue(v, lo, hi, step any) any {
	switch x := v.(type) {
	case []any:
		return sliceSeq(x, lo, hi, step)
	case string:
		return string(sliceSeq([]rune(x), lo, hi, step))
	}
	typeErrorf("'%s' object is not subscriptable", pyTypeName(v))
	return nil
}

func (cp *compiler) compileCase(e *exp.Expr) code {
	var this code
	if e.This() != nil {
		this = cp.compile(e.This())
	}
	type branch struct{ cond, val code }
	var branches []branch
	for _, i := range e.List("ifs") {
		branches = append(branches, branch{cp.compile(i.This()), cp.compile(i.Get("true"))})
	}
	def := constant(nil)
	if d := e.Get("default"); d != nil {
		def = cp.compile(d)
	}
	return func(c *Context) any {
		for _, b := range branches {
			cond := b.cond(c)
			if this != nil {
				cond = Equal(this(c), cond)
			}
			if Truthy(cond) {
				return b.val(c)
			}
		}
		return def(c)
	}
}

func (cp *compiler) compileDiv(e *exp.Expr) code {
	this, denominator := cp.compile(e.This()), cp.compile(e.Expression())
	if e.Bool("safe") {
		d := denominator
		denominator = func(c *Context) any {
			v := d(c)
			if !Truthy(v) {
				return nil
			}
			return v
		}
	}
	f := cp.function("DIV")
	result := func(c *Context) any { return f(this(c), denominator(c)) }
	if e.Bool("typed") && !(e.This().IsTypeIn(exp.DataTypeRealTypes) || e.Expression().IsTypeIn(exp.DataTypeRealTypes)) {
		toInt := cp.function("INT")
		return func(c *Context) any { return toInt(result(c)) }
	}
	return result
}

func (cp *compiler) compileIs(e *exp.Expr) code {
	this, other := cp.compile(e.This()), cp.compile(e.Expression())
	negate := e.Bool("negate")
	if e.This().IsKind(exp.Literal) {
		return func(c *Context) any { return Equal(this(c), other(c)) != negate }
	}
	return func(c *Context) any { return identical(this(c), other(c)) != negate }
}

// identical mirrors Python's `is` for the singletons it is used with (None, True, False).
func identical(a, b any) bool {
	switch x := b.(type) {
	case nil:
		return a == nil
	case bool:
		y, ok := a.(bool)
		return ok && x == y
	}
	return false
}

func (cp *compiler) compileLambda(e *exp.Expr) code {
	params := e.Expressions()
	frame := make([]any, 0)
	inner := &compiler{e: cp.e, lambda: map[string]int{}, frame: &frame}
	for k, v := range cp.lambda {
		inner.lambda[k] = v
	}
	base := len(cp.lambda)
	for i, p := range params {
		inner.lambda[strings.ToLower(p.Name())] = base + i
	}
	body := inner.compile(e.This())
	outer := cp.frame
	return func(c *Context) any {
		return Lambda(func(args ...any) any {
			var outerFrame []any
			if outer != nil {
				outerFrame = *outer
			}
			saved := frame
			frame = append(append(make([]any, 0, base+len(args)), outerFrame...), args...)
			for len(frame) < base+len(params) {
				frame = append(frame, nil)
			}
			defer func() { frame = saved }()
			return body(c)
		})
	}
}
