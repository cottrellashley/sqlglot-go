package pyrt

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"testing"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// Method is a method of a converted test class.
type Method func(self *Case, a Args) Value

// Class is a converted unittest.TestCase class.
type Class struct {
	Name    string
	File    string
	Base    *Class
	Attrs   map[string]Value
	Methods map[string]Method
	// Tests lists the test_* methods in definition order (inherited ones included by RunClass).
	Tests []string
}

func (c *Class) method(name string) (Method, bool) {
	for k := c; k != nil; k = k.Base {
		if m, ok := k.Methods[name]; ok {
			return m, true
		}
	}
	return nil, false
}

func (c *Class) attr(name string) (Value, bool) {
	for k := c; k != nil; k = k.Base {
		if v, ok := k.Attrs[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func (c *Class) allTests() []string {
	seen := map[string]bool{}
	var out []string
	for k := c; k != nil; k = k.Base {
		for _, t := range k.Tests {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// Case is the `self` of a running test method.
type Case struct {
	T     *testing.T
	Class *Class
	attrs map[string]Value
	line  int
	label []string
	// skipped counts the validate_all sub-cases skipped because a dialect is not ported.
	skipped int
}

// RunClass runs the test methods of a converted class as subtests.
func RunClass(t *testing.T, c *Class) {
	if d, ok := c.attr("dialect"); ok && d != nil && !DialectAvailable(Str(d)) {
		t.Skipf("dialect %q is not ported yet", Str(d))
	}
	for _, name := range c.allTests() {
		t.Run(name, func(t *testing.T) {
			self := &Case{T: t, Class: c, attrs: map[string]Value{}}
			defer self.recoverTop()
			if m, ok := c.method("setUp"); ok {
				m(self, Args{})
			}
			m, _ := c.method(name)
			m(self, Args{})
			if m, ok := c.method("tearDown"); ok {
				m(self, Args{})
			}
		})
	}
}

func (s *Case) where() string {
	loc := fmt.Sprintf("%s:%d", s.Class.File, s.line)
	if len(s.label) > 0 {
		loc += " " + strings.Join(s.label, " ")
	}
	return loc
}

func (s *Case) report(r any) {
	s.T.Helper()
	switch x := r.(type) {
	case *PyErr:
		switch {
		case x.Class == UnportedDialectError:
			s.skipped++
			s.T.Logf("%s: skipped: dialect %q is not ported", s.where(), x.Msg)
		case x.Class == NotPortedError:
			s.T.Errorf("%s: not ported: %s", s.where(), x.Msg)
		case x.Class == AssertionError:
			s.T.Errorf("%s: %s", s.where(), x.Msg)
		default:
			s.T.Errorf("%s: unexpected %s", s.where(), x.Error())
		}
	case skipSignal:
		panic(x)
	default:
		s.T.Errorf("%s: panic: %v\n%s", s.where(), r, debug.Stack())
	}
}

type skipSignal struct{ msg string }

func (s *Case) recoverTop() {
	if r := recover(); r != nil {
		if sk, ok := r.(skipSignal); ok {
			s.T.Skip(sk.msg)
			return
		}
		s.report(r)
	}
}

// L sets the current Python source line and returns self.
func (s *Case) L(line int) *Case {
	s.line = line
	return s
}

// Do runs one statement; a failure is reported and execution continues with the next one.
func (s *Case) Do(line int, f func()) {
	s.line = line
	defer func() {
		if r := recover(); r != nil {
			s.report(r)
		}
	}()
	f()
}

// guard runs f, reporting failures (used by the Case methods called directly).
func (s *Case) guard(f func() Value) (res Value) {
	defer func() {
		if r := recover(); r != nil {
			s.report(r)
			res = nil
		}
	}()
	return f()
}

// SubTest is `with self.subTest(...)`: failures inside are reported with the parameters.
func (s *Case) SubTest(f func(), args ...Value) {
	a := buildArgs(args)
	var parts []string
	for _, p := range a.Pos {
		parts = append(parts, Str(p))
	}
	for _, kw := range a.KW {
		parts = append(parts, kw.K+"="+Repr(kw.V))
	}
	s.label = append(s.label, "["+strings.Join(parts, ", ")+"]")
	defer func() { s.label = s.label[:len(s.label)-1] }()
	defer func() {
		if r := recover(); r != nil {
			s.report(r)
		}
	}()
	f()
}

func fail(format string, args ...any) {
	panic(&PyErr{Class: AssertionError, Msg: fmt.Sprintf(format, args...)})
}

// Dialect is self.dialect.
func (s *Case) Dialect() Value {
	d, _ := s.Class.attr("dialect")
	return d
}

// PyAttr implements attribute access on self.
func (s *Case) PyAttr(name string) (Value, bool) {
	if v, ok := s.attrs[name]; ok {
		return v, true
	}
	if m, ok := s.Class.method(name); ok {
		return Func(name, func(a Args) Value { return m(s, a) }), true
	}
	if v, ok := s.Class.attr(name); ok {
		return v, true
	}
	if f := s.builtinMethod(name); f != nil {
		return &Fn{Name: name, F: func(a Args) Value {
			if sk := firstSkipped(a); sk != nil {
				skipNow(sk)
			}
			return f(a)
		}, checksSkips: true}, true
	}
	return nil, false
}

// noteSkip records a statement that could not run because of an unported dialect.
func (s *Case) noteSkip(msg string) {
	s.skipped++
	s.T.Logf("%s: skipped: %s", s.where(), msg)
}

// PySetAttr implements `self.x = v`.
func (s *Case) PySetAttr(name string, v Value) bool {
	s.attrs[name] = v
	return true
}

func (s *Case) builtinMethod(name string) func(a Args) Value {
	switch name {
	case "parse_one":
		return func(a Args) Value { return s.parseOne(a) }
	case "validate_identity":
		return func(a Args) Value { return s.validateIdentity(a) }
	case "validate_all":
		return func(a Args) Value { return s.validateAll(a) }
	case "validate_transpile":
		return func(a Args) Value { return s.validateTranspile(a) }
	case "assert_duckdb_sql":
		return func(a Args) Value { return s.assertDuckDBSQL(a) }
	case "assertEqual", "assertEquals":
		return func(a Args) Value { s.assertEqual(a); return nil }
	case "assertNotEqual":
		return func(a Args) Value {
			if Eq(a.Pos[0], a.Pos[1]) {
				fail("%s == %s", Repr(a.Pos[0]), Repr(a.Pos[1]))
			}
			return nil
		}
	case "assertTrue":
		return func(a Args) Value {
			if !Truth(a.Pos[0]) {
				fail("%s is not true%s", Repr(a.Pos[0]), msgSuffix(a))
			}
			return nil
		}
	case "assertFalse":
		return func(a Args) Value {
			if Truth(a.Pos[0]) {
				fail("%s is not false%s", Repr(a.Pos[0]), msgSuffix(a))
			}
			return nil
		}
	case "assertIsNone":
		return func(a Args) Value {
			if norm(a.Pos[0]) != nil {
				fail("%s is not None%s", Repr(a.Pos[0]), msgSuffix(a))
			}
			return nil
		}
	case "assertIsNotNone":
		return func(a Args) Value {
			if norm(a.Pos[0]) == nil {
				fail("unexpectedly None%s", msgSuffix(a))
			}
			return nil
		}
	case "assertIs":
		return func(a Args) Value {
			if !Is(a.Pos[0], a.Pos[1]) {
				fail("%s is not %s", Repr(a.Pos[0]), Repr(a.Pos[1]))
			}
			return nil
		}
	case "assertIsNot":
		return func(a Args) Value {
			if Is(a.Pos[0], a.Pos[1]) {
				fail("unexpectedly identical: %s", Repr(a.Pos[0]))
			}
			return nil
		}
	case "assertIsInstance":
		return func(a Args) Value {
			if !IsInstance(a.Pos[0], a.Pos[1]) {
				fail("%s is not an instance of %s", Repr(a.Pos[0]), Str(a.Pos[1]))
			}
			return nil
		}
	case "assertNotIsInstance":
		return func(a Args) Value {
			if IsInstance(a.Pos[0], a.Pos[1]) {
				fail("%s is an instance of %s", Repr(a.Pos[0]), Str(a.Pos[1]))
			}
			return nil
		}
	case "assertIn":
		return func(a Args) Value {
			if !In(a.Pos[0], a.Pos[1]) {
				fail("%s not found in %s", Repr(a.Pos[0]), Repr(a.Pos[1]))
			}
			return nil
		}
	case "assertNotIn":
		return func(a Args) Value {
			if In(a.Pos[0], a.Pos[1]) {
				fail("%s unexpectedly found in %s", Repr(a.Pos[0]), Repr(a.Pos[1]))
			}
			return nil
		}
	case "assertGreater", "assertGreaterEqual", "assertLess", "assertLessEqual":
		return func(a Args) Value {
			var ok Value
			switch name {
			case "assertGreater":
				ok = Gt(a.Pos[0], a.Pos[1])
			case "assertGreaterEqual":
				ok = Ge(a.Pos[0], a.Pos[1])
			case "assertLess":
				ok = Lt(a.Pos[0], a.Pos[1])
			default:
				ok = Le(a.Pos[0], a.Pos[1])
			}
			if !Truth(ok) {
				fail("%s: %s vs %s", name, Repr(a.Pos[0]), Repr(a.Pos[1]))
			}
			return nil
		}
	case "assertCountEqual":
		return func(a Args) Value {
			x, y := Iter(a.Pos[0]), Iter(a.Pos[1])
			if len(x) != len(y) {
				fail("element counts differ: %s vs %s", Repr(a.Pos[0]), Repr(a.Pos[1]))
			}
			used := make([]bool, len(y))
			for _, it := range x {
				found := false
				for j, o := range y {
					if !used[j] && Eq(it, o) {
						used[j], found = true, true
						break
					}
				}
				if !found {
					fail("element counts differ: %s vs %s", Repr(a.Pos[0]), Repr(a.Pos[1]))
				}
			}
			return nil
		}
	case "assertRegex":
		return func(a Args) Value {
			if !regexp.MustCompile(Str(a.Pos[1])).MatchString(Str(a.Pos[0])) {
				fail("regex %s didn't match %s", Repr(a.Pos[1]), Repr(a.Pos[0]))
			}
			return nil
		}
	case "fail":
		return func(a Args) Value { fail("%s", Str(a.Get(0, "msg", "fail"))); return nil }
	case "skipTest":
		return func(a Args) Value { panic(skipSignal{Str(a.Pos[0])}) }
	case "dialect":
		return nil
	}
	return nil
}

func msgSuffix(a Args) string {
	if m := a.Get(1, "msg", nil); m != nil {
		return " : " + Str(m)
	}
	return ""
}

func (s *Case) assertEqual(a Args) {
	x, y := a.Pos[0], a.Pos[1]
	if !Eq(x, y) {
		xs, ys := Repr(x), Repr(y)
		if sx, ok := x.(string); ok {
			if sy, ok := y.(string); ok {
				xs, ys = sx, sy
			}
		}
		fail("not equal%s:\n  first:  %s\n  second: %s", msgSuffix(Args{Pos: a.Pos[1:], KW: a.KW}), xs, ys)
	}
}

// AssertEqual is self.assertEqual(first, second) with the argument order of Python (the
// first argument is usually the expected value).
func (s *Case) AssertEqual(args ...Value) {
	s.guard(func() Value { s.assertEqual(buildArgs(args)); return nil })
}

func (s *Case) parseOne(a Args) Value {
	kw := append([]KW{}, a.KW...)
	if _, ok := a.Kwarg("read"); !ok {
		kw = append(kw, KW{"read", s.Dialect()})
	}
	return parseOne(Args{Pos: a.Pos, KW: kw})
}

// ParseOne is self.parse_one(sql, **kwargs).
func (s *Case) ParseOne(args ...Value) Value {
	return s.guard(func() Value { return s.parseOne(buildArgs(args)) })
}

func (s *Case) sqlOf(e Value, dialect Value, kws ...KW) string {
	return Str(M(e, "sql", append([]Value{dialect}, kwValues(kws)...)...))
}

func kwValues(kws []KW) []Value {
	out := make([]Value, len(kws))
	for i, k := range kws {
		out[i] = k
	}
	return out
}

func (s *Case) validateIdentity(a Args) Value {
	sql := a.Pos[0]
	writeSQL := a.Get(1, "write_sql", nil)
	pretty := a.Get(2, "pretty", false)
	checkWarning := Truth(a.Get(3, "check_command_warning", false))
	identify := a.Get(4, "identify", false)
	var expression Value
	if checkWarning {
		var out []string
		s.captureLogs(slog.LevelInfo, &out, func() { expression = s.parseOne(Args{Pos: []Value{sql}}) })
		want := fmt.Sprintf("'%s' contains unsupported syntax", truncate(Str(sql), 100))
		if len(out) == 0 || !strings.Contains(out[0], want) {
			fail("expected a command warning %q, got %s", want, Repr(norm(out)))
		}
	} else {
		expression = s.parseOne(Args{Pos: []Value{sql}})
	}
	want := OrV(writeSQL, func() Value { return sql })
	got := s.sqlOf(expression, s.Dialect(), KW{"pretty", pretty}, KW{"identify", identify})
	if !Eq(want, got) {
		fail("validate_identity:\n  got:  %s\n  want: %s", got, Str(want))
	}
	return expression
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// ValidateIdentity is self.validate_identity(sql, write_sql=None, pretty=False,
// check_command_warning=False, identify=False).
func (s *Case) ValidateIdentity(args ...Value) Value {
	return s.guard(func() Value { return s.validateIdentity(buildArgs(args)) })
}

func (s *Case) validateTranspile(a Args) Value {
	expression := s.parseOne(Args{Pos: []Value{a.Pos[0]}})
	writeDialect := a.Get(2, "write_dialect", nil)
	got := s.sqlOf(expression, writeDialect)
	if !Eq(a.Get(1, "write_sql", nil), got) {
		fail("validate_transpile:\n  got:  %s\n  want: %s", got, Str(a.Get(1, "write_sql", nil)))
	}
	return expression
}

// ValidateTranspile is self.validate_transpile(sql, write_sql, write_dialect=None).
func (s *Case) ValidateTranspile(args ...Value) Value {
	return s.guard(func() Value { return s.validateTranspile(buildArgs(args)) })
}

func (s *Case) validateAll(a Args) Value {
	sql := a.Pos[0]
	read := a.Get(1, "read", nil)
	write := a.Get(2, "write", nil)
	pretty := a.Get(3, "pretty", false)
	identify := a.Get(4, "identify", false)
	expression := s.parseOne(Args{Pos: []Value{sql}})
	if read != nil {
		rd := read.(*Dict)
		for i, k := range rd.keys {
			readDialect, readSQL := k, rd.vals[i]
			if !DialectAvailable(Str(readDialect)) {
				s.skipped++
				continue
			}
			s.SubTest(func() {
				parsed := parseOne(Args{Pos: []Value{readSQL, readDialect}})
				got := s.sqlOf(parsed, s.Dialect(), KW{"unsupported_level", errs.Ignore}, KW{"pretty", pretty}, KW{"identify", identify})
				if !Eq(got, sql) {
					fail("validate_all read:\n  got:  %s\n  want: %s", got, Str(sql))
				}
			}, F(Str(readDialect), " -> ", Str(sql)))
		}
	}
	if write != nil {
		wd := write.(*Dict)
		for i, k := range wd.keys {
			writeDialect, writeSQL := k, wd.vals[i]
			if !DialectAvailable(Str(writeDialect)) {
				s.skipped++
				continue
			}
			s.SubTest(func() {
				if writeSQL == UnsupportedError {
					err := catch(func() { s.sqlOf(expression, writeDialect, KW{"unsupported_level", errs.Raise}) })
					if err == nil || !err.Class.IsSubclass(UnsupportedError) {
						fail("expected UnsupportedError, got %v", err)
					}
					return
				}
				got := s.sqlOf(expression, writeDialect, KW{"unsupported_level", errs.Ignore}, KW{"pretty", pretty}, KW{"identify", identify})
				if !Eq(got, writeSQL) {
					fail("validate_all write:\n  got:  %s\n  want: %s", got, Str(writeSQL))
				}
			}, F(Str(sql), " -> ", Str(writeDialect)))
		}
	}
	return expression
}

// ValidateAll is self.validate_all(sql, read=None, write=None, pretty=False, identify=False).
func (s *Case) ValidateAll(args ...Value) Value {
	return s.guard(func() Value { return s.validateAll(buildArgs(args)) })
}

func (s *Case) assertDuckDBSQL(a Args) Value {
	duckdbSQL := s.sqlOf(a.Pos[0], "duckdb")
	if inc, ok := a.Kwarg("includes"); ok && inc != nil {
		for _, f := range Iter(inc) {
			if !strings.Contains(duckdbSQL, Str(f)) {
				fail("%s not found in %s", Repr(f), Repr(duckdbSQL))
			}
		}
	}
	if exc, ok := a.Kwarg("excludes"); ok && exc != nil {
		for _, f := range Iter(exc) {
			if strings.Contains(duckdbSQL, Str(f)) {
				fail("%s unexpectedly found in %s", Repr(f), Repr(duckdbSQL))
			}
		}
	}
	if chars, ok := a.Kwarg("chr_chars"); ok && chars != nil {
		codes := wsControlCharsToDuck().(*Dict)
		for _, ch := range Iter(chars) {
			code, ok := codes.Lookup(ch)
			if !ok {
				fail("missing DuckDB code for %s", Repr(ch))
			}
			if want := "CHR(" + Str(code) + ")"; !strings.Contains(duckdbSQL, want) {
				fail("%s not found in %s", Repr(want), Repr(duckdbSQL))
			}
		}
	}
	return duckdbSQL
}

func catch(f func()) (err *PyErr) {
	defer func() {
		if r := recover(); r != nil {
			switch x := r.(type) {
			case *PyErr:
				err = x
			case error:
				err = goErr(x)
			default:
				err = &PyErr{Class: Exception, Msg: fmt.Sprint(r)}
			}
		}
	}()
	f()
	return nil
}

// RaisesCM is the context manager object of assertRaises (cm.exception).
type RaisesCM struct {
	Exc     *PyErr
	skipped bool
}

// PyAttr implements Attrer.
func (r *RaisesCM) PyAttr(name string) (Value, bool) {
	if name == "exception" {
		if r.skipped {
			return &SkippedV{}, true
		}
		return r.Exc, true
	}
	return nil, false
}

// AssertRaises is `with self.assertRaises(cls) as cm: ...`; f receives cm.
func (s *Case) AssertRaises(f func(cm Value), args ...Value) {
	cm := &RaisesCM{}
	s.assertRaises(cm, f, buildArgs(args).Pos[0])
}

func (s *Case) assertRaises(cm *RaisesCM, f func(cm Value), cls Value) bool {
	before := skipEvents
	err := catch(func() { f(cm) })
	if err == nil {
		if skipEvents > before {
			cm.skipped = true
			s.noteSkip("the statement needs a dialect that is not ported")
			return false
		}
		fail("%s not raised", Str(A(cls, "__name__")))
	}
	if err.Class == UnportedDialectError {
		cm.skipped = true
		s.noteSkip("dialect " + strconvQuote(err.Msg) + " is not ported")
		return false
	}
	if !matchesExc(err, cls) {
		panic(err)
	}
	cm.Exc = err
	return true
}

func strconvQuote(s string) string { return "\"" + s + "\"" }

// AssertRaisesRegex is `with self.assertRaisesRegex(cls, regex) as cm: ...`; f receives cm.
func (s *Case) AssertRaisesRegex(f func(cm Value), args ...Value) {
	a := buildArgs(args)
	regex := a.Get(1, "expected_regex", nil)
	cm := &RaisesCM{}
	if !s.assertRaises(cm, f, a.Pos[0]) {
		return
	}
	re := regexp.MustCompile(Str(regex))
	if !re.MatchString(cm.Exc.Msg) {
		fail("%s does not match %s", Repr(Str(regex)), Repr(cm.Exc.Msg))
	}
}

func matchesExc(err *PyErr, cls Value) bool {
	switch c := cls.(type) {
	case *ExcClass:
		return err.Class.IsSubclass(c)
	case Tuple:
		for _, x := range c {
			if matchesExc(err, x) {
				return true
			}
		}
	}
	return false
}

// LogsCM is the context manager object of assertLogs (cm.output, cm.records).
type LogsCM struct {
	out     []string
	skipped bool
	// partial reports whether some of the logging statements were skipped so far.
	partial func() bool
}

// PyAttr implements Attrer.
func (l *LogsCM) PyAttr(name string) (Value, bool) {
	switch name {
	case "output":
		if l.skipped || (l.partial != nil && l.partial()) {
			return &SkippedV{}, true
		}
		return norm(append([]string{}, l.out...)), true
	case "records":
		NotPorted("assertLogs(...).records")
	}
	return nil, false
}

type captureHandler struct {
	level slog.Level
	out   *[]string
}

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	name := "INFO"
	switch {
	case r.Level >= slog.LevelError:
		name = "ERROR"
	case r.Level >= slog.LevelWarn:
		name = "WARNING"
	case r.Level < slog.LevelInfo:
		name = "DEBUG"
	}
	*h.out = append(*h.out, name+":sqlglot:"+r.Message)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (s *Case) captureLogs(level slog.Level, out *[]string, f func()) {
	prev := sqlglot.Logger
	sqlglot.Logger = slog.New(&captureHandler{level, out})
	defer func() { sqlglot.Logger = prev }()
	f()
}

func levelOf(v Value) slog.Level {
	switch strings.ToUpper(Str(v)) {
	case "DEBUG", "10":
		return slog.LevelDebug
	case "WARNING", "WARN", "30":
		return slog.LevelWarn
	case "ERROR", "40":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// AssertLogs is `with self.assertLogs(logger, level=None) as cm: ...`; f receives cm.
func (s *Case) AssertLogs(f func(cm Value), args ...Value) {
	a := buildArgs(args)
	level := slog.LevelInfo
	if l := a.Get(1, "level", nil); l != nil {
		level = levelOf(l)
	}
	before, beforeEvents := s.skipped, skipEvents
	cm := &LogsCM{partial: func() bool { return s.skipped > before || skipEvents > beforeEvents }}
	err := catch(func() { s.captureLogs(level, &cm.out, func() { f(cm) }) })
	if err != nil {
		if err.Class == UnportedDialectError {
			cm.skipped = true
		}
		panic(err)
	}
	if len(cm.out) == 0 {
		if s.skipped > before || skipEvents > beforeEvents {
			cm.skipped = true
			s.noteSkip("the logging statements need a dialect that is not ported")
			return
		}
		fail("no logs of level %v or higher triggered on sqlglot", level)
	}
}

// AssertNoLogs is `with self.assertNoLogs(logger, level=None): ...`.
func (s *Case) AssertNoLogs(f func(cm Value), args ...Value) {
	a := buildArgs(args)
	level := slog.LevelInfo
	if l := a.Get(1, "level", nil); l != nil {
		level = levelOf(l)
	}
	var out []string
	s.captureLogs(level, &out, func() { f(nil) })
	if len(out) > 0 {
		fail("unexpected logs: %s", strings.Join(out, "; "))
	}
}

// Raise_ is the `raise` statement.
func Raise_(v Value) {
	switch x := v.(type) {
	case *PyErr:
		panic(x)
	case *ExcClass:
		panic(&PyErr{Class: x})
	}
	NotPorted("raise %s", TypeName(v))
}

// Assert is the `assert` statement.
func Assert(cond Value, msg ...Value) {
	if !Truth(cond) {
		m := ""
		if len(msg) > 0 {
			m = Str(msg[0])
		}
		fail("%s", m)
	}
}

// keys used by generated code to keep imports used.
var _ = sort.Strings
var _ = exp.Literal
