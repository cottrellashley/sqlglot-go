package pyrt

import (
	"reflect"
	"sort"
	"strings"
	"unicode"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/dialects/duckdb"
	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

var globals = map[string]Value{}

// KnownDialects are the dialect names of sqlglot (Dialects enum); the ones that are not
// registered in Go are reported as skipped rather than failed.
var KnownDialects = map[string]bool{}

func init() {
	for _, d := range strings.Fields("athena bigquery clickhouse databricks dax doris dremio drill druid duckdb dune " +
		"fabric hive materialize mysql oracle postgres presto prql redshift risingwave singlestore snowflake solr " +
		"spark spark2 sqlite starrocks tableau teradata trino tsql exasol") {
		KnownDialects[d] = true
	}
}

func wsControlCharsToDuck() Value {
	keys := make([]rune, 0, len(duckdb.WSControlCharsToDuck))
	for k := range duckdb.WSControlCharsToDuck {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	d := NewDict()
	for _, k := range keys {
		d.Set(string(k), duckdb.WSControlCharsToDuck[k])
	}
	return d
}

// LoggerV is a Python logger (all sqlglot loggers are named "sqlglot").
type LoggerV struct{ Name string }

// G looks up a module-level name: a builtin, or an imported name given by its qualified
// Python path (e.g. "sqlglot.optimizer.annotate_types.annotate_types").
func G(name string) Value {
	if v, ok := globals[name]; ok {
		return v
	}
	if v, ok := builtins[name]; ok {
		return v
	}
	parts := strings.Split(name, ".")
	last := parts[len(parts)-1]
	if len(parts) > 1 {
		switch {
		case last == "logger":
			return &LoggerV{"sqlglot"}
		case parts[0] == "sqlglot" && len(parts) >= 3 && parts[1] == "dialects" && unicode.IsUpper(rune(last[0])):
			if c, err := sqlglot.GetDialectClass(strings.ToLower(last)); err == nil {
				return &DialectClassV{c}
			}
			if KnownDialects[strings.ToLower(last)] {
				skipEvents++
				return &SkippedV{strings.ToLower(last)}
			}
		case parts[0] == "sqlglot" && len(parts) >= 3 && parts[1] == "dialects" && parts[2] == "dialect" && last == "Dialect":
			return dialectClassValue
		case name == "sqlglot.parser":
			return &Module{Name: name, Attrs: map[string]Value{"Parser": baseConfig("Parser")}}
		case name == "sqlglot.generator":
			return &Module{Name: name, Attrs: map[string]Value{"Generator": baseConfig("Generator")}}
		case name == "sqlglot.tokens":
			return &Module{Name: name, Attrs: map[string]Value{"Tokenizer": baseConfig("Tokenizer")}}
		case parts[0] == "sqlglot" && len(parts) == 4 && (parts[1] == "parsers" || parts[1] == "generators" || parts[1] == "tokenizers"):
			// e.g. sqlglot.parsers.snowflake.SnowflakeParser
			if c, err := sqlglot.GetDialectClass(parts[2]); err == nil {
				for _, part := range []string{"Parser", "Generator", "Tokenizer"} {
					if strings.HasSuffix(last, part) {
						v, _ := (&DialectClassV{c}).pyAttrConfig(part)
						return v
					}
				}
			} else if KnownDialects[parts[2]] {
				skipEvents++
				return &SkippedV{parts[2]}
			}
		}
		if v, ok := globals[last]; ok {
			return v
		}
		if name == "sqlglot.dialects.duckdb.WS_CONTROL_CHARS_TO_DUCK" {
			return wsControlCharsToDuck()
		}
		if name == "tests.helpers.is_compiled" {
			return Func("is_compiled", func(a Args) Value { return false })
		}
		if v, ok := builtins[last]; ok && parts[0] != "sqlglot" {
			return v
		}
	}
	if v, ok := sqlglotModule.lookup(last); ok {
		return v
	}
	NotPorted("global name %q", name)
	return nil
}

// Def registers a module-level value (used by converted test modules).
func Def(name string, v Value) { globals[name] = v }

// DialectAvailable reports whether a dialect name (optionally with settings) is registered.
func DialectAvailable(name string) bool {
	n := strings.TrimSpace(strings.Split(name, ",")[0])
	if n == "" || n == "sqlglot" {
		return true
	}
	_, err := sqlglot.GetDialectClass(n)
	return err == nil
}

func parseOpts(a Args, from int) sqlglot.ParserOptions {
	o := sqlglot.ParserOptions{ErrorLevel: errs.Unset}
	if l, ok := a.Kwarg("error_level"); ok && l != nil {
		o.ErrorLevel = l.(errs.ErrorLevel)
	}
	if v, ok := a.Kwarg("error_message_context"); ok {
		o.ErrorMessageContext = mustInt(v)
	}
	if v, ok := a.Kwarg("max_errors"); ok {
		o.MaxErrors = mustInt(v)
	}
	return o
}

func readDialect(a Args, pos int) any {
	d := a.Get(pos, "read", nil)
	if d == nil {
		d, _ = a.Kwarg("dialect")
	}
	return dialectArg(d)
}

func intoKinds(a Args) []exp.Kind {
	into, ok := a.Kwarg("into")
	if !ok || into == nil {
		return nil
	}
	return kindsOf(into)
}

// kindsOf converts a class or a tuple/list of classes into kinds.
func kindsOf(v Value) []exp.Kind {
	switch x := norm(v).(type) {
	case Tuple:
		return kinds(x)
	case *List:
		return kinds(x.V)
	}
	return kinds([]Value{v})
}

func parseOne(a Args) Value {
	return Guard(func() Value {
		e, err := sqlglot.ParseOneOpts(Str(a.Pos[0]), readDialect(a, 1), parseOpts(a, 2), intoKinds(a)...)
		check(err)
		return e
	})
}

var sqlglotModule = &Module{Name: "sqlglot", Attrs: map[string]Value{}}

func init() {
	m := sqlglotModule.Attrs
	m["parse_one"] = Func("parse_one", parseOne)
	m["parse"] = Func("parse", func(a Args) Value {
		return Guard(func() Value {
			d, err := sqlglot.GetDialect(readDialect(a, 1))
			check(err)
			var es []*exp.Expr
			if into := intoKinds(a); len(into) > 0 {
				es, err = d.ParseInto(into, Str(a.Pos[0]), parseOpts(a, 2))
			} else {
				es, err = d.Parse(Str(a.Pos[0]), parseOpts(a, 2))
			}
			check(err)
			return norm(es)
		})
	})
	m["transpile"] = Func("transpile", func(a Args) Value {
		return Guard(func() Value {
			var gopts []exp.GenOption
			var rest []KW
			for _, kw := range a.KW {
				switch kw.K {
				case "read", "write", "identity", "error_level":
				default:
					rest = append(rest, kw)
				}
			}
			for _, o := range genOptions(Args{KW: rest}, -1) {
				if g, ok := o.(exp.GenOption); ok {
					gopts = append(gopts, g)
				}
			}
			read := dialectArg(a.Get(1, "read", nil))
			write := dialectArg(a.Get(2, "write", nil))
			out, err := sqlglot.TranspileOpts(Str(a.Pos[0]), read, write, parseOpts(a, -1), gopts...)
			check(err)
			return norm(out)
		})
	})
	m["tokenize"] = Func("tokenize", func(a Args) Value {
		return Guard(func() Value {
			toks, err := sqlglot.Tokenize(Str(a.Pos[0]), readDialect(a, 1))
			check(err)
			out := make([]Value, len(toks))
			for i, t := range toks {
				out[i] = &TokenV{t}
			}
			return &List{out}
		})
	})
	m["exp"] = expModule
	m["expressions"] = expModule
	m["Dialect"] = dialectClassValue
	m["Dialects"] = &Module{Name: "Dialects", Get: func(name string) (Value, bool) {
		return strings.ToLower(name), true
	}}
	m["ErrorLevel"] = &Module{Name: "ErrorLevel", Attrs: map[string]Value{
		"IGNORE": errs.Ignore, "WARN": errs.Warn, "RAISE": errs.Raise, "IMMEDIATE": errs.Immediate,
	}}
	for _, c := range []*ExcClass{ParseError, TokenError, UnsupportedError, OptimizeError, SchemaError, ExecuteError, SqlglotError} {
		m[c.Name] = c
	}
	m["TokenType"] = &Module{Name: "TokenType", Get: func(name string) (Value, bool) {
		t, ok := tokens.TokenTypeByName(name)
		return t, ok
	}}
	m["select"] = expModule.Attrs["select"]
	m["condition"] = expModule.Attrs["condition"]
	m["column"] = expModule.Attrs["column"]
	m["table"] = expModule.Attrs["table_"]
	m["alias"] = expModule.Attrs["alias_"]
	m["and_"] = expModule.Attrs["and_"]
	m["or_"] = expModule.Attrs["or_"]
	m["not_"] = expModule.Attrs["not_"]
	m["func"] = expModule.Attrs["func"]
	m["cast"] = expModule.Attrs["cast"]
	m["maybe_parse"] = expModule.Attrs["maybe_parse"]
	m["from_"] = expModule.Attrs["from_"]
	m["union"] = expModule.Attrs["union"]
	m["intersect"] = expModule.Attrs["intersect"]
	m["except_"] = expModule.Attrs["except_"]

	m["annotate_types"] = Func("annotate_types", func(a Args) Value {
		return Guard(func() Value {
			e, _ := norm(a.Pos[0]).(*exp.Expr)
			o := sqlglot.AnnotateOptions{Dialect: dialectArg(a.Get(-1, "dialect", nil))}
			if s := a.Get(1, "schema", nil); s != nil {
				o.Schema = toSchema(s)
			}
			if ow, ok := a.Kwarg("overwrite_types"); ok && !Truth(ow) {
				o.NoOverwriteTypes = true
			}
			if _, ok := a.Kwarg("expression_metadata"); ok {
				NotPorted("annotate_types(expression_metadata=...)")
			}
			if _, ok := a.Kwarg("coerces_to"); ok {
				NotPorted("annotate_types(coerces_to=...)")
			}
			out, err := sqlglot.AnnotateTypesOpts(e, o)
			check(err)
			return out
		})
	})
	m["normalize_identifiers"] = Func("normalize_identifiers", func(a Args) Value {
		return Guard(func() Value {
			store := false
			if s, ok := a.Kwarg("store_original_column_identifiers"); ok {
				store = Truth(s)
			}
			return sqlglot.NormalizeIdentifiersOpts(toGo(a.Pos[0]), dialectArg(a.Get(1, "dialect", nil)), store)
		})
	})
	m["simplify"] = Func("simplify", func(a Args) Value {
		return Guard(func() Value {
			e, _ := norm(a.Pos[0]).(*exp.Expr)
			cp := Truth(a.Get(1, "constant_propagation", false))
			coalesce := Truth(a.Get(-1, "coalesce_simplification", false))
			return sqlglot.SimplifyOpts(e, cp, coalesce, dialectArg(a.Get(-1, "dialect", nil)))
		})
	})
	m["canonicalize"] = Func("canonicalize", func(a Args) Value {
		return Guard(func() Value {
			e, _ := norm(a.Pos[0]).(*exp.Expr)
			return sqlglot.Canonicalize(e, dialectArg(a.Get(1, "dialect", nil)))
		})
	})
	m["MappingSchema"] = Func("MappingSchema", func(a Args) Value {
		return Guard(func() Value {
			o := sqlglot.MappingSchemaOptions{Dialect: dialectArg(a.Get(2, "dialect", nil))}
			if n, ok := a.Kwarg("normalize"); ok && !Truth(n) {
				o.NoNormalize = true
			}
			if v := a.Get(1, "visible", nil); v != nil {
				o.Visible = toSchemaMapping(v)
			}
			var schema any
			if s := a.Get(0, "schema", nil); s != nil {
				schema = toSchemaMapping(s)
			}
			s, err := sqlglot.NewMappingSchema(schema, o)
			check(err)
			return &SchemaV{s}
		})
	})
	m["ensure_schema"] = Func("ensure_schema", func(a Args) Value {
		return Guard(func() Value { return toSchema(a.Pos[0]) })
	})
}

// toSchemaMapping converts a (nested) dict into a SchemaMapping.
func toSchemaMapping(v Value) *sqlglot.SchemaMapping {
	d, ok := norm(v).(*Dict)
	if !ok {
		Raise(TypeError, "expected a dict schema, got %s", TypeName(v))
	}
	m := sqlglot.NewOrderedMap[any]()
	for i, k := range d.keys {
		val := d.vals[i]
		switch x := norm(val).(type) {
		case *Dict:
			m.Set(Str(k), toSchemaMapping(x))
		case nil:
			m.Set(Str(k), nil)
		default:
			m.Set(Str(k), toGo(x))
		}
	}
	return m
}

func toSchema(v Value) any {
	switch x := norm(v).(type) {
	case *SchemaV:
		return x.S
	case *Dict:
		return toSchemaMapping(x)
	}
	return toGo(v)
}

// SchemaV wraps a MappingSchema.
type SchemaV struct{ S *sqlglot.MappingSchema }

// PyAttr implements Attrer.
func (s *SchemaV) PyAttr(name string) (Value, bool) {
	switch name {
	case "column_names":
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				names, err := s.S.ColumnNames(toGo(a.Pos[0]), Truth(a.Get(1, "only_visible", false)), dialectArg(a.Get(-1, "dialect", nil)), boolPtr(a.Get(-1, "normalize", nil)))
				check(err)
				return norm(names)
			})
		}), true
	case "get_column_type":
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				t, err := s.S.GetColumnType(toGo(a.Pos[0]), toGo(a.Pos[1]), dialectArg(a.Get(-1, "dialect", nil)), boolPtr(a.Get(-1, "normalize", nil)))
				check(err)
				return t
			})
		}), true
	case "add_table":
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				var cols any
				if c := a.Get(1, "column_mapping", nil); c != nil {
					if d, ok := c.(*Dict); ok {
						cols = toSchemaMapping(d)
					} else {
						cols = toGo(c)
					}
				}
				check(s.S.AddTable(toGo(a.Pos[0]), cols, dialectArg(a.Get(-1, "dialect", nil)), boolPtr(a.Get(-1, "normalize", nil)), Truth(a.Get(-1, "match_depth", true))))
				return nil
			})
		}), true
	case "depth":
		return Func(name, func(a Args) Value { return s.S.Depth() }), true
	}
	return nil, false
}

// TokenV wraps a token.
type TokenV struct{ T *tokens.Token }

// PyAttr implements Attrer.
func (t *TokenV) PyAttr(name string) (Value, bool) {
	switch name {
	case "token_type":
		return t.T.Type, true
	case "text":
		return t.T.Text, true
	case "line":
		return t.T.Line, true
	case "col":
		return t.T.Col, true
	case "start":
		return t.T.Start, true
	case "end":
		return t.T.End, true
	case "comments":
		return norm(append([]string{}, t.T.Comments...)), true
	}
	return nil, false
}

// DialectClassV is a dialect class (e.g. `Snowflake` imported from sqlglot.dialects).
type DialectClassV struct{ C *sqlglot.DialectClass }

var dialectClassValue = &dialectClassObj{}

type dialectClassObj struct{}

func (dialectClassObj) PyAttr(name string) (Value, bool) {
	switch name {
	case "get_or_raise", "get":
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				d, err := sqlglot.GetDialect(dialectArg(a.Pos[0]))
				if err != nil {
					if name == "get" {
						return nil
					}
					e := goErr(err)
					if e.Class == Exception {
						e.Class = ValueError
					}
					if e.Class == ValueError && suggestsUnportedDialect(e.Msg) {
						e.Class = UnportedDialectError
					}
					panic(e)
				}
				return &DialectV{d}
			})
		}), true
	case "classes":
		NotPorted("Dialect.classes")
	}
	return classField(reflect.ValueOf(sqlglot.BaseDialectClass()).Elem(), name)
}

// suggestsUnportedDialect reports whether Python's "Unknown dialect" error would suggest a
// dialect that is not ported (the message then depends on the missing dialect).
func suggestsUnportedDialect(msg string) bool {
	m := unknownDialectRe.FindStringSubmatch(msg)
	if m == nil {
		return false
	}
	for d := range KnownDialects {
		if !DialectAvailable(d) && editDistance(d, m[1]) <= 2 {
			return true
		}
	}
	return false
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func (dialectClassObj) PyCall(a Args) Value {
	return &DialectV{sqlglot.BaseDialectClass().NewInstance()}
}

func (dialectClassObj) PyItem(k Value) Value {
	return Guard(func() Value {
		c, err := sqlglot.GetDialectClass(Str(k))
		check(err)
		return &DialectClassV{c}
	})
}

// PyCall instantiates the dialect class.
func (c *DialectClassV) PyCall(a Args) Value {
	return Guard(func() Value {
		settings := map[string]any{}
		for _, kw := range a.KW {
			settings[kw.K] = toGo(kw.V)
		}
		d, err := c.C.NewInstanceWith(settings)
		check(err)
		return &DialectV{d}
	})
}

// PyAttr implements Attrer.
func (c *DialectClassV) PyAttr(name string) (Value, bool) {
	if v, ok := c.pyAttrConfig(name); ok {
		return v, true
	}
	return classField(reflect.ValueOf(c.C).Elem(), name)
}

// PyEq compares dialect classes with classes, instances and names (like the Python metaclass).
func (c *DialectClassV) PyEq(o Value) (bool, bool) {
	switch x := o.(type) {
	case *DialectClassV:
		return x.C == c.C, true
	case *DialectV:
		return x.D.DialectClass == c.C, true
	case string:
		return strings.EqualFold(x, c.C.Name), true
	}
	return false, false
}

// PyHash makes dialect classes hash like their names.
func (c *DialectClassV) PyHash() string { return "s:" + c.C.Name }

// PyHash makes dialects hash like their names.
func (d *DialectV) PyHash() string { return "s:" + d.D.Name }

// DialectV wraps a dialect instance.
type DialectV struct{ D *sqlglot.Dialect }

// PyEq mirrors Dialect.__eq__: dialects compare equal to their name and to equal instances.
func (d *DialectV) PyEq(o Value) (bool, bool) {
	switch x := o.(type) {
	case string:
		other, err := sqlglot.GetDialect(x)
		if err != nil {
			return false, true
		}
		return other.DialectClass == d.D.DialectClass && settingsEq(other.Settings, d.D.Settings), true
	case *DialectV:
		return x.D.DialectClass == d.D.DialectClass && settingsEq(x.D.Settings, d.D.Settings), true
	case *DialectClassV:
		return x.C == d.D.DialectClass, true
	}
	return false, false
}

func settingsEq(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// PyAttr implements Attrer.
func (d *DialectV) PyAttr(name string) (Value, bool) {
	switch name {
	case "parse":
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				es, err := d.D.Parse(Str(a.Pos[0]), parseOpts(a, 1))
				check(err)
				return norm(es)
			})
		}), true
	case "parse_into":
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				es, err := d.D.ParseInto(kinds([]Value{a.Pos[0]}), Str(a.Pos[1]), parseOpts(a, 2))
				check(err)
				return norm(es)
			})
		}), true
	case "generate":
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				e, _ := norm(a.Pos[0]).(*exp.Expr)
				opts := exp.GenOpts(append([]any{d.D}, genOptions(Args{KW: a.KW}, -1)...)...)
				sql, err := d.D.Generate(e, opts)
				check(err)
				return sql
			})
		}), true
	case "transpile":
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				opts := exp.GenOpts(append([]any{d.D}, genOptions(Args{KW: a.KW}, -1)...)...)
				out, err := d.D.Transpile(Str(a.Pos[0]), opts)
				check(err)
				return norm(out)
			})
		}), true
	case "tokenize":
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				toks, err := d.D.Tokenize(Str(a.Pos[0]))
				check(err)
				out := make([]Value, len(toks))
				for i, t := range toks {
					out[i] = &TokenV{t}
				}
				return &List{out}
			})
		}), true
	case "normalize_identifier":
		return Func(name, func(a Args) Value {
			e, _ := norm(a.Pos[0]).(*exp.Expr)
			return d.D.NormalizeIdentifier(e)
		}), true
	case "quote_identifier":
		return Func(name, func(a Args) Value {
			e, _ := norm(a.Pos[0]).(*exp.Expr)
			return d.D.QuoteIdentifier(e, Truth(a.Get(1, "identify", true)))
		}), true
	case "case_sensitive":
		return Func(name, func(a Args) Value { return d.D.CaseSensitive(Str(a.Pos[0])) }), true
	case "can_quote":
		return Func(name, func(a Args) Value {
			e, _ := norm(a.Pos[0]).(*exp.Expr)
			return d.D.CanQuote(e, toGo(a.Get(1, "identify", "safe")))
		}), true
	case "to_json_path":
		return Func(name, func(a Args) Value {
			e, _ := norm(a.Pos[0]).(*exp.Expr)
			return d.D.ToJSONPath(e)
		}), true
	case "version":
		return Tuple{d.D.Version[0], d.D.Version[1], d.D.Version[2]}, true
	case "settings":
		dd := NewDict()
		for _, k := range sortedKeys(d.D.Settings) {
			dd.Set(k, fromGo(d.D.Settings[k]))
		}
		return dd, true
	case "__class__":
		return &DialectClassV{d.D.DialectClass}, true
	}
	return classField(reflect.ValueOf(d.D.DialectClass).Elem(), name)
}

func baseConfig(part string) Value {
	v, _ := (&DialectClassV{sqlglot.BaseDialectClass()}).pyAttrConfig(part)
	return v
}

// PyAttr exposes the Parser/Generator/Tokenizer configuration of a dialect class.
func (c *DialectClassV) pyAttrConfig(name string) (Value, bool) {
	switch name {
	case "Generator":
		return &ConfigV{reflect.ValueOf(c.C.Generator).Elem(), c.C.Generator.Extra}, true
	case "Parser":
		return &ConfigV{reflect.ValueOf(c.C.Parser).Elem(), c.C.Parser.Extra}, true
	case "Tokenizer":
		return &ConfigV{reflect.ValueOf(c.C.Tokenizer).Elem(), nil}, true
	}
	return nil, false
}

// ConfigV exposes the class attributes of a parser/generator/tokenizer configuration.
type ConfigV struct {
	v     reflect.Value
	extra map[string]any
}

// PyAttr implements Attrer.
func (c *ConfigV) PyAttr(name string) (Value, bool) {
	if v, ok := c.extra[name]; ok {
		return fromGo(v), true
	}
	return classField(c.v, name)
}

// classField reads an UPPER_SNAKE Python class attribute from the Go struct field CamelCase.
func classField(v reflect.Value, name string) (Value, bool) {
	if name == "" || !unicode.IsUpper(rune(name[0])) {
		return nil, false
	}
	var b strings.Builder
	for _, part := range strings.Split(strings.ToLower(name), "_") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	f := v.FieldByName(b.String())
	if !f.IsValid() {
		return nil, false
	}
	return fromGo(f.Interface()), true
}
