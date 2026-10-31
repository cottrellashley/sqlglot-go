package pyrt

import (
	"sort"
	"strings"
	"unicode"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

// Attrer is implemented by Go objects exposing Python attributes.
type Attrer interface {
	PyAttr(name string) (Value, bool)
}

// A is getattr(v, name).
func A(v Value, name string) Value {
	v = norm(v)
	switch x := v.(type) {
	case *SkippedV:
		skipEvents++
		return x
	case *Module:
		if r, ok := x.lookup(name); ok {
			return r
		}
		Raise(AttributeError, "module '%s' has no attribute '%s'", x.Name, name)
	case *exp.Expr:
		return exprAttr(x, name)
	case exp.Kind:
		return kindAttr(x, name)
	case exp.DType:
		return dtypeAttr(x, name)
	case *List:
		return listAttr(x, name)
	case *Dict:
		return dictAttr(x, name)
	case *Set:
		return setAttr(x, name)
	case string:
		return strAttr(x, name)
	case *ArgsView:
		return argsAttr(x, name)
	case *PyErr:
		return errAttr(x, name)
	case *ExcClass:
		if name == "__name__" {
			return x.Name
		}
	case errs.ErrorLevel:
		if name == "name" {
			return x.String()
		}
	case tokens.TokenType:
		switch name {
		case "name", "value":
			return x.String()
		}
	case Attrer:
		if r, ok := x.PyAttr(name); ok {
			return r
		}
	case nil:
		Raise(AttributeError, "'NoneType' object has no attribute '%s'", name)
	}
	if name == "__class__" {
		return typeOf(v)
	}
	Raise(AttributeError, "'%s' object has no attribute '%s'", TypeName(v), name)
	return nil
}

// HasAttr is hasattr(v, name).
func HasAttr(v Value, name string) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if e, isErr := r.(*PyErr); isErr && e.Class.IsSubclass(AttributeError) {
				ok = false
				return
			}
			panic(r)
		}
	}()
	A(v, name)
	return true
}

// M calls the method name of v.
func M(v Value, name string, args ...Value) Value { return Call(A(v, name), args...) }

// Call calls a Python callable.
func Call(f Value, args ...Value) Value {
	switch x := norm(f).(type) {
	case *SkippedV:
		skipEvents++
		return x
	case *Fn:
		a := buildArgs(args)
		if !x.checksSkips {
			if sk := firstSkipped(a); sk != nil {
				skipEvents++
				return sk
			}
		}
		return norm(x.F(a))
	case exp.Kind:
		return construct(x, buildArgs(args))
	case *ExcClass:
		a := buildArgs(args)
		msg := ""
		if len(a.Pos) > 0 {
			msg = Str(a.Pos[0])
		}
		return &PyErr{Class: x, Msg: msg}
	case interface{ PyCall(Args) Value }:
		return norm(x.PyCall(buildArgs(args)))
	}
	Raise(TypeError, "'%s' object is not callable", TypeName(f))
	return nil
}

func firstSkipped(a Args) *SkippedV {
	for _, p := range a.Pos {
		if s, ok := p.(*SkippedV); ok {
			return s
		}
	}
	for _, kw := range a.KW {
		if s, ok := kw.V.(*SkippedV); ok {
			return s
		}
	}
	return nil
}

// SetA is setattr(v, name, val).
func SetA(v Value, name string, val Value) {
	switch x := norm(v).(type) {
	case *exp.Expr:
		switch name {
		case "type":
			x.SetType(toGo(val))
			return
		case "parent":
			p, _ := norm(val).(*exp.Expr)
			x.SetParent(p)
			return
		case "comments":
			var cs []string
			for _, c := range Iter(val) {
				cs = append(cs, Str(c))
			}
			x.Comments = cs
			return
		case "meta":
			NotPorted("setting Expr.meta")
		}
		x.Set(name, toGo(val))
		return
	case interface{ PySetAttr(string, Value) bool }:
		if x.PySetAttr(name, val) {
			return
		}
	}
	Raise(AttributeError, "can't set attribute '%s' of '%s'", name, TypeName(v))
}

// toGo converts a Python value into the representation expected by the Go API.
func toGo(v Value) any {
	switch x := norm(v).(type) {
	case *SkippedV:
		skipNow(x)
	case *List:
		return seqToGo(x.V)
	case Tuple:
		return seqToGo(x)
	case *Dict:
		m := map[string]any{}
		for i, k := range x.keys {
			m[Str(k)] = toGo(x.vals[i])
		}
		return m
	case *Set:
		return seqToGo(x.Items())
	case BytesV:
		return []byte(x)
	}
	return norm(v)
}

func seqToGo(items []Value) any {
	allExpr := true
	for _, it := range items {
		if _, ok := norm(it).(*exp.Expr); !ok {
			allExpr = false
			break
		}
	}
	if allExpr {
		out := make([]*exp.Expr, len(items))
		for i, it := range items {
			out[i] = norm(it).(*exp.Expr)
		}
		return out
	}
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = toGo(it)
	}
	return out
}

func toGoList(v Value) []any {
	if v == nil {
		return nil
	}
	items := Iter(v)
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = toGo(it)
	}
	return out
}

func typeOf(v Value) Value {
	switch x := norm(v).(type) {
	case *exp.Expr:
		return x.Kind()
	case *PyErr:
		return x.Class
	}
	return Func(TypeName(v), func(a Args) Value { NotPorted("calling type(%s)", TypeName(v)); return nil })
}

func method(name string, f func(a Args) Value) *Fn { return &Fn{Name: name, F: f} }

func listAttr(l *List, name string) Value {
	switch name {
	case "append":
		return method(name, func(a Args) Value { l.V = append(l.V, a.Pos[0]); return nil })
	case "extend":
		return method(name, func(a Args) Value { l.V = append(l.V, Iter(a.Pos[0])...); return nil })
	case "insert":
		return method(name, func(a Args) Value {
			i := mustInt(a.Pos[0])
			if i < 0 {
				i = max(0, i+len(l.V))
			}
			i = min(i, len(l.V))
			l.V = append(l.V[:i], append([]Value{a.Pos[1]}, l.V[i:]...)...)
			return nil
		})
	case "pop":
		return method(name, func(a Args) Value {
			i := len(l.V) - 1
			if len(a.Pos) > 0 {
				i = index(mustInt(a.Pos[0]), len(l.V))
			}
			if len(l.V) == 0 {
				Raise(IndexError, "pop from empty list")
			}
			v := l.V[i]
			l.V = append(l.V[:i], l.V[i+1:]...)
			return v
		})
	case "remove":
		return method(name, func(a Args) Value {
			for i, it := range l.V {
				if Eq(it, a.Pos[0]) {
					l.V = append(l.V[:i], l.V[i+1:]...)
					return nil
				}
			}
			Raise(ValueError, "list.remove(x): x not in list")
			return nil
		})
	case "index":
		return method(name, func(a Args) Value {
			for i, it := range l.V {
				if Eq(it, a.Pos[0]) {
					return i
				}
			}
			Raise(ValueError, "%s is not in list", Repr(a.Pos[0]))
			return nil
		})
	case "count":
		return method(name, func(a Args) Value {
			n := 0
			for _, it := range l.V {
				if Eq(it, a.Pos[0]) {
					n++
				}
			}
			return n
		})
	case "copy":
		return method(name, func(a Args) Value { return &List{append([]Value(nil), l.V...)} })
	case "clear":
		return method(name, func(a Args) Value { l.V = nil; return nil })
	case "reverse":
		return method(name, func(a Args) Value {
			for i, j := 0, len(l.V)-1; i < j; i, j = i+1, j-1 {
				l.V[i], l.V[j] = l.V[j], l.V[i]
			}
			return nil
		})
	case "sort":
		return method(name, func(a Args) Value {
			l.V = sortValues(l.V, a)
			return nil
		})
	}
	Raise(AttributeError, "'list' object has no attribute '%s'", name)
	return nil
}

func sortValues(items []Value, a Args) []Value {
	out := append([]Value(nil), items...)
	key, _ := a.Kwarg("key")
	rev, _ := a.Kwarg("reverse")
	keys := make([]Value, len(out))
	for i, it := range out {
		if key != nil {
			keys[i] = Call(key, it)
		} else {
			keys[i] = it
		}
	}
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool {
		if Truth(rev) {
			return cmp(keys[idx[j]], keys[idx[i]], "<") < 0
		}
		return cmp(keys[idx[i]], keys[idx[j]], "<") < 0
	})
	res := make([]Value, len(out))
	for i, k := range idx {
		res[i] = out[k]
	}
	return res
}

func dictAttr(d *Dict, name string) Value {
	switch name {
	case "get":
		return method(name, func(a Args) Value {
			if v, ok := d.Lookup(a.Pos[0]); ok {
				return v
			}
			return a.Get(1, "default", nil)
		})
	case "items":
		return method(name, func(a Args) Value {
			out := make([]Value, d.Len())
			for i, k := range d.keys {
				out[i] = Tuple{k, d.vals[i]}
			}
			return &List{out}
		})
	case "keys":
		return method(name, func(a Args) Value { return &List{append([]Value(nil), d.keys...)} })
	case "values":
		return method(name, func(a Args) Value { return &List{append([]Value(nil), d.vals...)} })
	case "pop":
		return method(name, func(a Args) Value {
			if v, ok := d.Lookup(a.Pos[0]); ok {
				d.Delete(a.Pos[0])
				return v
			}
			if len(a.Pos) > 1 {
				return a.Pos[1]
			}
			Raise(KeyError, "%s", Repr(a.Pos[0]))
			return nil
		})
	case "setdefault":
		return method(name, func(a Args) Value {
			if v, ok := d.Lookup(a.Pos[0]); ok {
				return v
			}
			def := a.Get(1, "default", nil)
			d.Set(a.Pos[0], def)
			return def
		})
	case "update":
		return method(name, func(a Args) Value {
			if len(a.Pos) > 0 {
				src := a.Pos[0].(*Dict)
				for i, k := range src.keys {
					d.Set(k, src.vals[i])
				}
			}
			for _, kw := range a.KW {
				d.Set(kw.K, kw.V)
			}
			return nil
		})
	case "copy":
		return method(name, func(a Args) Value {
			n := NewDict()
			for i, k := range d.keys {
				n.Set(k, d.vals[i])
			}
			return n
		})
	}
	Raise(AttributeError, "'dict' object has no attribute '%s'", name)
	return nil
}

func setAttr(s *Set, name string) Value {
	switch name {
	case "add":
		return method(name, func(a Args) Value { s.Add(a.Pos[0]); return nil })
	case "update":
		return method(name, func(a Args) Value {
			for _, it := range Iter(a.Pos[0]) {
				s.Add(it)
			}
			return nil
		})
	}
	Raise(AttributeError, "'set' object has no attribute '%s'", name)
	return nil
}

func strArgs(a Args) []string {
	out := make([]string, len(a.Pos))
	for i, p := range a.Pos {
		out[i] = Str(p)
	}
	return out
}

func strAttr(s string, name string) Value {
	switch name {
	case "value":
		// members of str enums (e.g. Dialects) are represented by their values
		return s
	case "upper":
		return method(name, func(a Args) Value { return strings.ToUpper(s) })
	case "lower":
		return method(name, func(a Args) Value { return strings.ToLower(s) })
	case "strip", "lstrip", "rstrip":
		return method(name, func(a Args) Value {
			cut := " \t\n\r\x0b\x0c"
			if len(a.Pos) > 0 && a.Pos[0] != nil {
				cut = Str(a.Pos[0])
			}
			switch name {
			case "lstrip":
				return strings.TrimLeft(s, cut)
			case "rstrip":
				return strings.TrimRight(s, cut)
			}
			return strings.Trim(s, cut)
		})
	case "split":
		return method(name, func(a Args) Value {
			sep := a.Get(0, "sep", nil)
			maxsplit := -1
			if m := a.Get(1, "maxsplit", nil); m != nil {
				maxsplit = mustInt(m)
			}
			var parts []string
			if sep == nil {
				parts = strings.Fields(s)
				if maxsplit >= 0 && len(parts) > maxsplit+1 {
					NotPorted("str.split(None, maxsplit)")
				}
			} else if maxsplit >= 0 {
				parts = strings.SplitN(s, Str(sep), maxsplit+1)
			} else {
				parts = strings.Split(s, Str(sep))
			}
			out := make([]Value, len(parts))
			for i, p := range parts {
				out[i] = p
			}
			return &List{out}
		})
	case "splitlines":
		return method(name, func(a Args) Value {
			var out []Value
			for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
				out = append(out, l)
			}
			if len(out) > 0 && out[len(out)-1] == "" {
				out = out[:len(out)-1]
			}
			return &List{out}
		})
	case "join":
		return method(name, func(a Args) Value {
			var parts []string
			for _, it := range Iter(a.Pos[0]) {
				str, ok := it.(string)
				if !ok {
					Raise(TypeError, "sequence item: expected str instance, %s found", TypeName(it))
				}
				parts = append(parts, str)
			}
			return strings.Join(parts, s)
		})
	case "replace":
		return method(name, func(a Args) Value {
			n := -1
			if len(a.Pos) > 2 {
				n = mustInt(a.Pos[2])
			}
			return strings.Replace(s, Str(a.Pos[0]), Str(a.Pos[1]), n)
		})
	case "startswith", "endswith":
		return method(name, func(a Args) Value {
			var prefixes []string
			if t, ok := a.Pos[0].(Tuple); ok {
				for _, p := range t {
					prefixes = append(prefixes, Str(p))
				}
			} else {
				prefixes = []string{Str(a.Pos[0])}
			}
			for _, p := range prefixes {
				if name == "startswith" && strings.HasPrefix(s, p) || name == "endswith" && strings.HasSuffix(s, p) {
					return true
				}
			}
			return false
		})
	case "find", "index":
		return method(name, func(a Args) Value {
			i := strings.Index(s, Str(a.Pos[0]))
			if i < 0 && name == "index" {
				Raise(ValueError, "substring not found")
			}
			if i >= 0 {
				i = len([]rune(s[:i]))
			}
			return i
		})
	case "count":
		return method(name, func(a Args) Value { return strings.Count(s, Str(a.Pos[0])) })
	case "format":
		return method(name, func(a Args) Value { return strFormat(s, a) })
	case "title":
		return method(name, func(a Args) Value {
			prev := false
			return strings.Map(func(r rune) rune {
				isL := unicode.IsLetter(r)
				defer func() { prev = isL }()
				if isL && !prev {
					return unicode.ToUpper(r)
				}
				if isL {
					return unicode.ToLower(r)
				}
				return r
			}, s)
		})
	case "capitalize":
		return method(name, func(a Args) Value {
			r := []rune(strings.ToLower(s))
			if len(r) > 0 {
				r[0] = unicode.ToUpper(r[0])
			}
			return string(r)
		})
	case "isdigit", "isnumeric", "isdecimal":
		return method(name, func(a Args) Value {
			if s == "" {
				return false
			}
			for _, r := range s {
				if !unicode.IsDigit(r) {
					return false
				}
			}
			return true
		})
	case "isupper", "islower", "isalpha", "isspace", "isalnum", "isidentifier":
		return method(name, func(a Args) Value { return strPredicate(s, name) })
	case "encode":
		return method(name, func(a Args) Value { return s })
	case "zfill":
		return method(name, func(a Args) Value {
			w := mustInt(a.Pos[0])
			if len(s) >= w {
				return s
			}
			return strings.Repeat("0", w-len(s)) + s
		})
	case "partition":
		return method(name, func(a Args) Value {
			before, after, found := strings.Cut(s, Str(a.Pos[0]))
			if !found {
				return Tuple{s, "", ""}
			}
			return Tuple{before, Str(a.Pos[0]), after}
		})
	}
	Raise(AttributeError, "'str' object has no attribute '%s'", name)
	return nil
}

func strPredicate(s, name string) bool {
	if s == "" {
		return false
	}
	cased := false
	for _, r := range s {
		switch name {
		case "isupper":
			if unicode.IsLower(r) {
				return false
			}
			cased = cased || unicode.IsUpper(r)
		case "islower":
			if unicode.IsUpper(r) {
				return false
			}
			cased = cased || unicode.IsLower(r)
		case "isalpha":
			if !unicode.IsLetter(r) {
				return false
			}
		case "isspace":
			if !unicode.IsSpace(r) {
				return false
			}
		case "isalnum", "isidentifier":
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !(name == "isidentifier" && r == '_') {
				return false
			}
		}
	}
	if name == "isupper" || name == "islower" {
		return cased
	}
	if name == "isidentifier" {
		r := []rune(s)[0]
		return !unicode.IsDigit(r)
	}
	return true
}

// strFormat implements str.format for {} / {0} / {name} with optional format specs.
func strFormat(s string, a Args) string {
	var b strings.Builder
	auto := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '{' {
			if i+1 < len(s) && s[i+1] == '{' {
				b.WriteByte('{')
				i++
				continue
			}
			j := strings.IndexByte(s[i:], '}')
			if j < 0 {
				Raise(ValueError, "Single '{' encountered in format string")
			}
			field := s[i+1 : i+j]
			i += j
			name, spec, _ := strings.Cut(field, ":")
			var v Value
			if name == "" {
				v = a.Pos[auto]
				auto++
			} else if n := []rune(name)[0]; unicode.IsDigit(n) {
				v = a.Pos[mustInt(Int(name))]
			} else {
				v, _ = a.Kwarg(name)
			}
			b.WriteString(Format(v, spec))
			continue
		}
		if c == '}' && i+1 < len(s) && s[i+1] == '}' {
			i++
		}
		b.WriteByte(c)
	}
	return b.String()
}

func errAttr(e *PyErr, name string) Value {
	switch name {
	case "args":
		return Tuple{e.Msg}
	case "errors":
		if pe, ok := e.Go.(*errs.ParseError); ok {
			var out []Value
			for _, d := range pe.Errors {
				out = append(out, NewDict(
					"description", d.Description,
					"line", d.Line,
					"col", d.Col,
					"start_context", d.StartContext,
					"highlight", d.Highlight,
					"end_context", d.EndContext,
					"into_expression", nilIfEmpty(d.IntoExpression),
				))
			}
			return &List{out}
		}
		return &List{}
	case "__class__":
		return e.Class
	case "start", "end":
		if te, ok := e.Go.(*errs.TokenError); ok && te.HasPos {
			if name == "start" {
				return te.Start
			}
			return te.End
		}
		return nil
	}
	Raise(AttributeError, "'%s' object has no attribute '%s'", e.Class.Name, name)
	return nil
}

func nilIfEmpty(s string) Value {
	if s == "" {
		return nil
	}
	return s
}
