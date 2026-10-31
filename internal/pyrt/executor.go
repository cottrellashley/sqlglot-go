package pyrt

// Bindings for sqlglot.executor.

import (
	"math/big"
	"time"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/executor"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// ExecTableV wraps an executor table.
type ExecTableV struct{ T *executor.Table }

// toExecValue converts a Python value into an executor value.
func toExecValue(v Value) any {
	switch x := norm(v).(type) {
	case nil, bool, int, *big.Int, float64, string:
		return x
	case exp.DateValue:
		return executor.NewDate(x.Year, time.Month(x.Month), x.Day)
	case exp.TimeValue:
		return executor.Time{Hour: x.Hour, Minute: x.Minute, Second: x.Second, Microsecond: x.Microsecond}
	case exp.NaiveDatetime:
		return x.Time
	case time.Time:
		if x.Location() == time.UTC {
			return x.In(executor.UTC) // an aware UTC datetime
		}
		return x
	case time.Duration:
		return x
	case *List:
		out := make([]any, len(x.V))
		for i, it := range x.V {
			out[i] = toExecValue(it)
		}
		return out
	case Tuple:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = toExecValue(it)
		}
		return out
	case *Dict:
		out := map[string]any{}
		for i, k := range x.keys {
			out[Str(k)] = toExecValue(x.vals[i])
		}
		return out
	}
	NotPorted("executor value of type %s", TypeName(v))
	return nil
}

// fromExecValue converts an executor value into a Python value.
func fromExecValue(v any) Value {
	switch x := v.(type) {
	case executor.Date:
		return exp.DateValue{Year: x.Year(), Month: int(x.Month()), Day: x.Day()}
	case executor.Time:
		return exp.TimeValue{Hour: x.Hour, Minute: x.Minute, Second: x.Second, Microsecond: x.Microsecond}
	case time.Time:
		if x.Location() == time.UTC {
			return exp.NaiveDatetime{Time: x}
		}
		return x.In(time.UTC)
	case []any:
		out := make([]Value, len(x))
		for i, it := range x {
			out[i] = fromExecValue(it)
		}
		return &List{out}
	case map[string]any:
		d := NewDict()
		for _, k := range sortedKeys(x) {
			d.Set(k, fromExecValue(x[k]))
		}
		return d
	}
	return v
}

func execTableArg(v Value) *executor.Table {
	switch x := norm(v).(type) {
	case *ExecTableV:
		return x.T
	}
	Raise(TypeError, "unsupported table value %s", TypeName(v))
	return nil
}

// execTableValue converts a table value: a Table, or rows as dicts (ordered rows, whose names
// the executor normalizes).
func execTableValue(v Value) any {
	l, ok := norm(v).(*List)
	if !ok {
		return execTableArg(v)
	}
	rows := make([]executor.Row, len(l.V))
	for i, r := range l.V {
		d, ok := norm(r).(*Dict)
		if !ok {
			Raise(TypeError, "table rows must be dicts")
		}
		row := sqlglot.NewOrderedMap[any]()
		for j, k := range d.keys {
			row.Set(Str(k), toExecValue(d.vals[j]))
		}
		rows[i] = row
	}
	return rows
}

func execTables(v Value) map[string]any {
	d, ok := norm(v).(*Dict)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for i, k := range d.keys {
		if sub, ok := norm(d.vals[i]).(*Dict); ok {
			out[Str(k)] = execTables(sub)
		} else {
			out[Str(k)] = execTableValue(d.vals[i])
		}
	}
	return out
}

// PyAttr implements Attrer.
func (t *ExecTableV) PyAttr(name string) (Value, bool) {
	switch name {
	case "rows":
		out := make([]Value, len(t.T.Rows))
		for i, r := range t.T.Rows {
			row := make(Tuple, len(r))
			for j, v := range r {
				row[j] = fromExecValue(v)
			}
			out[i] = row
		}
		return &List{out}, true
	case "columns":
		out := make(Tuple, len(t.T.Columns))
		for i, c := range t.T.Columns {
			out[i] = c
		}
		return out, true
	case "to_pylist":
		return Func(name, func(Args) Value {
			var out []Value
			for _, r := range t.T.Rows {
				d := NewDict()
				for j, c := range t.T.Columns {
					d.Set(c, fromExecValue(r[j]))
				}
				out = append(out, d)
			}
			return &List{out}
		}), true
	case "width":
		return len(t.T.Columns), true
	}
	return nil, false
}

// String implements repr().
func (t *ExecTableV) String() string { return t.T.String() }

func init() {
	execute := Func("execute", func(a Args) Value {
		return Guard(func() Value {
			o := executor.Options{Dialect: dialectArg(a.Get(2, "dialect", a.Get(-1, "read", nil)))}
			if s := a.Get(1, "schema", nil); s != nil {
				o.Schema = toSchema(s)
			}
			if t := a.Get(3, "tables", nil); t != nil {
				o.Tables = execTables(t)
			}
			res, err := executor.Execute(toGo(a.Pos[0]), o)
			check(err)
			return &ExecTableV{res}
		})
	})
	globals["sqlglot.executor.execute"] = execute
	sqlglotModule.Attrs["execute"] = execute
	globals["sqlglot.executor.table.Table"] = &BuiltinType{
		Name:  "Table",
		Check: func(v Value) bool { _, ok := norm(v).(*ExecTableV); return ok },
		New: func(a Args) Value {
			var columns []string
			for _, c := range Iter(a.Get(0, "columns", nil)) {
				columns = append(columns, Str(c))
			}
			var rows [][]any
			if r := a.Get(1, "rows", nil); r != nil {
				for _, row := range Iter(r) {
					var vals []any
					for _, v := range Iter(row) {
						vals = append(vals, toExecValue(v))
					}
					rows = append(rows, vals)
				}
			}
			return &ExecTableV{executor.NewTable(columns, rows, nil)}
		},
	}
}

// ExecTablesV wraps executor tables (ensure_tables).
type ExecTablesV struct{ T *executor.Tables }

// PyAttr implements Attrer.
func (t *ExecTablesV) PyAttr(name string) (Value, bool) {
	if name == "find" {
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				e, _ := norm(a.Pos[0]).(*exp.Expr)
				found := t.T.Find(e)
				if found == nil {
					return nil
				}
				return &ExecTableV{found}
			})
		}), true
	}
	return nil, false
}

// EnvDictV is executor.ENV seen as a Python dict of functions.
type EnvDictV struct{}

// PyItem implements subscripting.
func (EnvDictV) PyItem(k Value) Value {
	f, ok := executor.ENV[Str(k)]
	if !ok {
		Raise(KeyError, "%s", Repr(k))
	}
	return Func(Str(k), func(a Args) Value {
		args := make([]any, len(a.Pos))
		for i, p := range a.Pos {
			args[i] = toExecValue(p)
		}
		return fromExecValue(f(args...))
	})
}

func pyFunc(v Value) executor.Func {
	return func(args ...any) any {
		vals := make([]Value, len(args))
		for i, x := range args {
			vals[i] = fromExecValue(x)
		}
		return toExecValue(Call(v, vals...))
	}
}

// patchDictCM is mock.patch.dict(executor ENV, values).
type patchDictCM struct {
	values *Dict
	saved  map[string]executor.Func
	added  []string
}

// PyEnter implements the context manager protocol.
func (p *patchDictCM) PyEnter() Value {
	p.saved = map[string]executor.Func{}
	for i, k := range p.values.keys {
		name := Str(k)
		if f, ok := executor.ENV[name]; ok {
			p.saved[name] = f
		} else {
			p.added = append(p.added, name)
		}
		executor.ENV[name] = pyFunc(p.values.vals[i])
	}
	return EnvDictV{}
}

// PyExit implements the context manager protocol.
func (p *patchDictCM) PyExit() {
	for k, f := range p.saved {
		executor.ENV[k] = f
	}
	for _, k := range p.added {
		delete(executor.ENV, k)
	}
}

func init() {
	ensureTables := Func("ensure_tables", func(a Args) Value {
		return Guard(func() Value {
			t, err := executor.EnsureTables(execTables(a.Get(0, "d", nil)), dialectArg(a.Get(1, "dialect", nil)))
			check(err)
			return &ExecTablesV{t}
		})
	})
	globals["sqlglot.executor.table.ensure_tables"] = ensureTables
	envModule := &Module{Name: "sqlglot.executor.env", Attrs: map[string]Value{"ENV": EnvDictV{}}}
	globals["sqlglot.executor.env"] = envModule
	patch := globals["patch"]
	mockPatch := &Module{Name: "mock.patch", Attrs: map[string]Value{
		"dict": Func("dict", func(a Args) Value {
			if _, ok := norm(a.Pos[0]).(EnvDictV); !ok {
				NotPorted("mock.patch.dict(%s)", TypeName(a.Pos[0]))
			}
			d, ok := norm(a.Get(1, "values", nil)).(*Dict)
			if !ok {
				NotPorted("mock.patch.dict with %s", TypeName(a.Get(1, "values", nil)))
			}
			return &patchDictCM{values: d}
		}),
	}}
	callablePatch := &callableModule{Module: mockPatch, call: patch}
	mock := &Module{Name: "unittest.mock", Attrs: map[string]Value{"patch": callablePatch}}
	globals["unittest.mock"] = mock
	globals["mock"] = mock
}

// callableModule is a module-like value that can also be called (mock.patch and mock.patch.dict).
type callableModule struct {
	*Module
	call Value
}

// PyCall implements calling.
func (m *callableModule) PyCall(a Args) Value {
	args := append([]Value{}, a.Pos...)
	for _, kw := range a.KW {
		args = append(args, kw)
	}
	return Call(m.call, args...)
}

// PyAttr implements Attrer.
func (m *callableModule) PyAttr(name string) (Value, bool) { return m.Module.lookup(name) }
