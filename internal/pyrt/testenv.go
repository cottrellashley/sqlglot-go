package pyrt

// Stand-ins for the test environment of sqlglot's suites: process pools (run synchronously),
// functools.partial, and DuckDB connections (the Go suites do not execute SQL, so statements
// that need DuckDB are skipped).

import (
	"os"
)

// SkipStatementError skips the statement that raises it (e.g. one that executes SQL).
var SkipStatementError = &ExcClass{"SkipStatementError", BaseException}

func skipStatement(reason string) {
	panic(&PyErr{Class: SkipStatementError, Msg: reason})
}

const noDuckDB = "executing SQL needs DuckDB, which the Go suites do not use"

// FutureV is the result of ProcessPoolExecutor.submit.
type FutureV struct {
	result Value
	err    any
}

// PyAttr implements Attrer.
func (f *FutureV) PyAttr(name string) (Value, bool) {
	switch name {
	case "result":
		return Func(name, func(Args) Value {
			if f.err != nil {
				panic(f.err)
			}
			return f.result
		}), true
	case "done":
		return Func(name, func(Args) Value { return true }), true
	}
	return nil, false
}

// PoolV is a ProcessPoolExecutor that runs submitted calls synchronously.
type PoolV struct{}

// PyAttr implements Attrer.
func (p *PoolV) PyAttr(name string) (Value, bool) {
	switch name {
	case "submit":
		return Func(name, func(a Args) Value {
			f := &FutureV{}
			args := append([]Value{}, a.Pos[1:]...)
			for _, kw := range a.KW {
				args = append(args, kw)
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						f.err = r
					}
				}()
				f.result = Call(a.Pos[0], args...)
			}()
			return f
		}), true
	case "shutdown":
		return Func(name, func(Args) Value { return nil }), true
	}
	return nil, false
}

// DuckDBConnV is a DuckDB connection; executing SQL skips the statement.
type DuckDBConnV struct{}

// PyAttr implements Attrer.
func (c *DuckDBConnV) PyAttr(name string) (Value, bool) {
	switch name {
	case "execute", "sql", "query":
		return Func(name, func(Args) Value { skipStatement(noDuckDB); return nil }), true
	case "close":
		return Func(name, func(Args) Value { return nil }), true
	}
	return nil, false
}

// PyEnter implements the context manager protocol.
func (c *DuckDBConnV) PyEnter() Value { return c }

// PyExit implements the context manager protocol.
func (c *DuckDBConnV) PyExit() {}

// TempDirV is tempfile.TemporaryDirectory().
type TempDirV struct{ path string }

// PyEnter implements the context manager protocol.
func (d *TempDirV) PyEnter() Value { return d.path }

// PyExit implements the context manager protocol.
func (d *TempDirV) PyExit() { os.RemoveAll(d.path) }

// PySetAttr implements setting module attributes (e.g. sqlglot.schema = ...).
func (m *Module) PySetAttr(name string, v Value) bool {
	if m.Attrs == nil {
		m.Attrs = map[string]Value{}
	}
	m.Attrs[name] = v
	return true
}

func init() {
	globals["ProcessPoolExecutor"] = Func("ProcessPoolExecutor", func(Args) Value { return &PoolV{} })
	globals["get_context"] = Func("get_context", func(Args) Value { return nil })
	globals["wait"] = Func("wait", func(Args) Value { return nil })
	globals["as_completed"] = Func("as_completed", func(a Args) Value { return &Iterator{items: Iter(a.Pos[0])} })
	globals["partial"] = Func("partial", func(a Args) Value {
		fn := a.Pos[0]
		bound := append([]Value{}, a.Pos[1:]...)
		boundKW := append([]KW{}, a.KW...)
		return Func("partial", func(b Args) Value {
			args := append(append([]Value{}, bound...), b.Pos...)
			kws := map[string]int{}
			var merged []KW
			for _, kw := range append(append([]KW{}, boundKW...), b.KW...) {
				if i, ok := kws[kw.K]; ok {
					merged[i] = kw
					continue
				}
				kws[kw.K] = len(merged)
				merged = append(merged, kw)
			}
			for _, kw := range merged {
				args = append(args, kw)
			}
			return Call(fn, args...)
		})
	})
	globals["duckdb"] = &Module{Name: "duckdb", Attrs: map[string]Value{
		"connect": Func("connect", func(Args) Value { return &DuckDBConnV{} }),
	}}
	globals["assert_frame_equal"] = Func("assert_frame_equal", func(Args) Value { skipStatement(noDuckDB); return nil })
	globals["tempfile"] = &Module{Name: "tempfile", Attrs: map[string]Value{
		"TemporaryDirectory": Func("TemporaryDirectory", func(Args) Value {
			dir, err := os.MkdirTemp("", "sqlglot-test")
			if err != nil {
				panic(err)
			}
			return &TempDirV{dir}
		}),
	}}
}
