package pyrt

// Stand-ins for the standard library modules and tests.helpers functions used by the
// converted test modules.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/internal/testutil"
)

// FileV is an open text file.
type FileV struct {
	Path string
	data string
	read bool
}

// PyEnter implements the context manager protocol.
func (f *FileV) PyEnter() Value { return f }

// PyExit implements the context manager protocol.
func (f *FileV) PyExit() {}

// PyAttr implements Attrer.
func (f *FileV) PyAttr(name string) (Value, bool) {
	switch name {
	case "read":
		return Func(name, func(a Args) Value { return f.consume() }), true
	case "readlines":
		return Func(name, func(a Args) Value {
			var out []Value
			for _, l := range strings.SplitAfter(f.consume(), "\n") {
				if l != "" {
					out = append(out, l)
				}
			}
			return &List{out}
		}), true
	case "name":
		return f.Path, true
	}
	return nil, false
}

func (f *FileV) consume() string {
	if f.read {
		return ""
	}
	f.read = true
	return f.data
}

// With runs body inside the context manager cm (`with cm as x: body`).
func With(cm Value, body func(Value)) {
	v := norm(cm)
	entered := v
	if e, ok := v.(interface{ PyEnter() Value }); ok {
		entered = e.PyEnter()
	}
	if x, ok := v.(interface{ PyExit() }); ok {
		defer x.PyExit()
	}
	body(entered)
}

func jsonLoads(s string) Value {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var parse func() Value
	parse = func() Value {
		tok, err := dec.Token()
		if err != nil {
			Raise(ValueError, "invalid JSON: %v", err)
		}
		switch x := tok.(type) {
		case json.Delim:
			switch x {
			case '{':
				d := NewDict()
				for dec.More() {
					k, _ := dec.Token()
					d.Set(k.(string), parse())
				}
				dec.Token()
				return d
			case '[':
				l := &List{}
				for dec.More() {
					l.V = append(l.V, parse())
				}
				dec.Token()
				return l
			}
		case json.Number:
			if i, ok := new(big.Int).SetString(x.String(), 10); ok {
				if i.IsInt64() {
					return int(i.Int64())
				}
				return i
			}
			f, _ := x.Float64()
			return f
		case string, bool, nil:
			return x
		}
		Raise(ValueError, "invalid JSON token %v", tok)
		return nil
	}
	return parse()
}

func jsonDumps(v Value, sortKeys bool) string {
	var b bytes.Buffer
	var dump func(v Value)
	dump = func(v Value) {
		switch x := norm(v).(type) {
		case nil:
			b.WriteString("null")
		case bool:
			if x {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}
		case int, *big.Int:
			b.WriteString(Str(x))
		case float64:
			switch {
			case math.IsNaN(x):
				b.WriteString("NaN")
			case math.IsInf(x, 1):
				b.WriteString("Infinity")
			case math.IsInf(x, -1):
				b.WriteString("-Infinity")
			default:
				b.WriteString(floatRepr(x))
			}
		case string:
			enc, _ := json.Marshal(x)
			b.Write(asciiJSON(enc))
		case *List:
			dumpSeq(&b, x.V, dump)
		case Tuple:
			dumpSeq(&b, x, dump)
		case *Dict:
			idx := make([]int, len(x.keys))
			for i := range idx {
				idx[i] = i
			}
			if sortKeys {
				sort.SliceStable(idx, func(i, j int) bool { return Str(x.keys[idx[i]]) < Str(x.keys[idx[j]]) })
			}
			b.WriteByte('{')
			for n, i := range idx {
				if n > 0 {
					b.WriteString(", ")
				}
				k := x.keys[i]
				if _, ok := k.(string); !ok {
					k = Str(k)
				}
				dump(k)
				b.WriteString(": ")
				dump(x.vals[i])
			}
			b.WriteByte('}')
		default:
			Raise(TypeError, "Object of type %s is not JSON serializable", TypeName(v))
		}
	}
	dump(v)
	return b.String()
}

func dumpSeq(b *bytes.Buffer, items []Value, dump func(Value)) {
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		dump(it)
	}
	b.WriteByte(']')
}

// asciiJSON escapes non-ASCII characters like Python's json.dumps(ensure_ascii=True).
func asciiJSON(enc []byte) []byte {
	var out bytes.Buffer
	for _, r := range string(enc) {
		switch {
		case r < 0x80:
			out.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&out, `\u%04x`, r)
		default:
			r -= 0x10000
			fmt.Fprintf(&out, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		}
	}
	return out.Bytes()
}

func fixtureMeta(meta map[string]string) *Dict {
	d := NewDict()
	for _, k := range sortedKeys(meta) {
		d.Set(k, meta[k])
	}
	return d
}

func init() {
	testdata := filepath.Dir(testutil.FixturesDir())
	path := &Module{Name: "os.path", Attrs: map[string]Value{
		"dirname":  Func("dirname", func(a Args) Value { return filepath.Dir(Str(a.Pos[0])) }),
		"basename": Func("basename", func(a Args) Value { return filepath.Base(Str(a.Pos[0])) }),
		"exists": Func("exists", func(a Args) Value {
			_, err := os.Stat(Str(a.Pos[0]))
			return err == nil
		}),
		"join": Func("join", func(a Args) Value {
			parts := make([]string, len(a.Pos))
			for i, p := range a.Pos {
				parts[i] = Str(p)
			}
			return filepath.Join(parts...)
		}),
	}}
	globals["os"] = &Module{Name: "os", Attrs: map[string]Value{"path": path, "environ": NewDict()}}
	globals["os.path"] = path
	globals["__file__"] = filepath.Join(testdata, "test.py")
	globals["sys"] = &Module{Name: "sys", Attrs: map[string]Value{
		"version_info": Tuple{3, 12, 0, "final", 0},
		"maxsize":      math.MaxInt64,
	}}
	globals["sys.version_info"] = globals["sys"].(*Module).Attrs["version_info"]

	jsonModule := &Module{Name: "json", Attrs: map[string]Value{
		"loads": Func("loads", func(a Args) Value { return jsonLoads(Str(a.Pos[0])) }),
		"load": Func("load", func(a Args) Value {
			return jsonLoads(Str(M(a.Pos[0], "read")))
		}),
		"dumps": Func("dumps", func(a Args) Value {
			return jsonDumps(a.Pos[0], Truth(a.Get(-1, "sort_keys", false)))
		}),
	}}
	globals["json"] = jsonModule

	builtin("open", func(a Args) Value {
		p := Str(a.Pos[0])
		b, err := os.ReadFile(p)
		if err != nil {
			Raise(FileNotFoundError, "[Errno 2] No such file or directory: '%s'", p)
		}
		return &FileV{Path: p, data: string(b)}
	})

	// tests.helpers
	globals["FIXTURES_DIR"] = testutil.FixturesDir()
	globals["load_sql_fixtures"] = Func("load_sql_fixtures", func(a Args) Value {
		var out []Value
		for _, s := range testutil.LoadSQLFixtures(Str(a.Pos[0])) {
			out = append(out, s)
		}
		return &Iterator{items: out}
	})
	globals["load_sql_fixture_pairs"] = Func("load_sql_fixture_pairs", func(a Args) Value {
		var out []Value
		for _, p := range testutil.LoadSQLFixturePairs(Str(a.Pos[0])) {
			out = append(out, Tuple{fixtureMeta(p.Meta), p.SQL, p.Expected})
		}
		return &Iterator{items: out}
	})
	globals["_extract_meta"] = Func("_extract_meta", func(a Args) Value {
		sql, meta := testutil.ExtractMeta(Str(a.Pos[0]))
		return Tuple{sql, fixtureMeta(meta)}
	})
	globals["string_to_bool"] = Func("string_to_bool", func(a Args) Value {
		switch x := norm(a.Pos[0]).(type) {
		case nil:
			return false
		case bool:
			return x
		}
		return testutil.StringToBool(Str(a.Pos[0]))
	})
	globals["assert_logger_contains"] = Func("assert_logger_contains", func(a Args) Value {
		level := Str(a.Get(2, "level", "error"))
		var msgs []string
		for _, c := range Iter(A(A(a.Pos[1], level), "call_args_list")) {
			msgs = append(msgs, Str(Item(Item(c, 0), 0)))
		}
		output := strings.Join(msgs, "\n")
		if !strings.Contains(output, Str(a.Pos[0])) {
			failLog("Expected '%s' not in %s", Str(a.Pos[0]), output)
		}
		return nil
	})
	for _, name := range []string{"TPCH_SCHEMA", "TPCDS_SCHEMA"} {
		file := filepath.Join(testdata, strings.ToLower(name)+".json")
		globals[name] = &LazyV{f: func() Value {
			b, err := os.ReadFile(file)
			if err != nil {
				panic(err)
			}
			return jsonLoads(string(b))
		}}
	}
}

// BytesV is a Python bytes value.
type BytesV []byte

// Bytes builds a bytes value from a string of code points below 256 (the converter emits
// bytes literals decoded as latin-1).
func Bytes(s string) Value {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		b = append(b, byte(r))
	}
	return BytesV(b)
}

// UnpackStar is `a, *b, c = v`: the target at index star receives a list of the extra items.
func UnpackStar(v Value, star int, targets ...*Value) {
	items := Iter(v)
	fixed := len(targets) - 1
	if len(items) < fixed {
		Raise(ValueError, "not enough values to unpack (expected at least %d, got %d)", fixed, len(items))
	}
	after := len(targets) - star - 1
	for i := 0; i < star; i++ {
		*targets[i] = items[i]
	}
	*targets[star] = &List{append([]Value(nil), items[star:len(items)-after]...)}
	for i := 0; i < after; i++ {
		*targets[star+1+i] = items[len(items)-after+i]
	}
}

func init() {
	// The converted suites run like sqlglot's own (UNITTEST mode): undeclared argument keys
	// raise TypeError.
	exp.StrictArgKeys = true
}

func init() {
	// pickle stands in for Go serialization of expressions: they round-trip through serde JSON.
	globals["pickle"] = &Module{Name: "pickle", Attrs: map[string]Value{
		"dumps": Func("dumps", func(a Args) Value {
			e, ok := norm(a.Pos[0]).(*exp.Expr)
			if !ok {
				NotPorted("pickle.dumps(%s)", TypeName(a.Pos[0]))
			}
			b, err := exp.DumpJSON(e)
			check(err)
			return BytesV(b)
		}),
		"loads": Func("loads", func(a Args) Value {
			return Guard(func() Value {
				b, _ := norm(a.Pos[0]).(BytesV)
				e, err := exp.LoadJSON(b)
				check(err)
				return e
			})
		}),
	}}
}

// PatchCM is `mock.patch(target, value)` used as a context manager, for the module-level
// settings that have a Go equivalent.
type PatchCM struct {
	set     func(Value)
	value   Value
	restore func()
}

// PyEnter implements the context manager protocol.
func (p *PatchCM) PyEnter() Value { p.set(p.value); return p.value }

// PyExit implements the context manager protocol.
func (p *PatchCM) PyExit() { p.restore() }

func init() {
	globals["patch"] = Func("patch", func(a Args) Value {
		target := Str(a.Pos[0])
		switch target {
		case "sqlglot.pretty":
			prev := exp.DefaultPretty
			return &PatchCM{
				set:     func(v Value) { exp.DefaultPretty = Truth(v) },
				value:   a.Get(1, "new", nil),
				restore: func() { exp.DefaultPretty = prev },
			}
		}
		NotPorted("mock.patch(%q)", target)
		return nil
	})
	start := time.Now()
	globals["time"] = &Module{Name: "time", Attrs: map[string]Value{
		"time":         Func("time", func(Args) Value { return float64(time.Now().UnixNano()) / 1e9 }),
		"perf_counter": Func("perf_counter", func(Args) Value { return time.Since(start).Seconds() }),
	}}
}

// tzV is a datetime.timezone.
type tzV struct{ loc *time.Location }

func init() {
	utc := &tzV{time.UTC}
	intArg := func(a Args, i int, name string) int {
		if v := a.Get(i, name, nil); v != nil {
			return mustInt(v)
		}
		return 0
	}
	datetimeModule := &Module{Name: "datetime", Attrs: map[string]Value{
		"timezone": &Module{Name: "datetime.timezone", Attrs: map[string]Value{"utc": utc}},
		"datetime": Func("datetime", func(a Args) Value {
			t := time.Date(mustInt(a.Pos[0]), time.Month(mustInt(a.Pos[1])), mustInt(a.Pos[2]),
				intArg(a, 3, "hour"), intArg(a, 4, "minute"), intArg(a, 5, "second"), intArg(a, 6, "microsecond")*1000, time.UTC)
			if tz, ok := norm(a.Get(7, "tzinfo", nil)).(*tzV); ok {
				return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), tz.loc)
			}
			return exp.NaiveDatetime{Time: t}
		}),
		"date": Func("date", func(a Args) Value {
			return exp.DateValue{Year: mustInt(a.Pos[0]), Month: mustInt(a.Pos[1]), Day: mustInt(a.Pos[2])}
		}),
		"time": Func("time", func(a Args) Value {
			return exp.TimeValue{Hour: intArg(a, 0, "hour"), Minute: intArg(a, 1, "minute"), Second: intArg(a, 2, "second"),
				Microsecond: intArg(a, 3, "microsecond")}
		}),
	}}
	globals["datetime"] = datetimeModule
}
