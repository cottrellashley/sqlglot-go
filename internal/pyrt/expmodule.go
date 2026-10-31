package pyrt

import (
	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// ExprType is exp.Expr / exp.Expression, the root expression class.
var ExprType = &BuiltinType{"Expression", func(v Value) bool { _, ok := v.(*exp.Expr); return ok }, func(a Args) Value {
	NotPorted("instantiating exp.Expression")
	return nil
}}

var expModule = &Module{Name: "sqlglot.expressions", Attrs: map[string]Value{}, Get: func(name string) (Value, bool) {
	if name == "Expr" || name == "Expression" {
		return ExprType, true
	}
	if k, ok := exp.KindByName(name); ok {
		return k, true
	}
	return nil, false
}}

func boolPtr(v Value) *bool {
	if v == nil {
		return nil
	}
	b := Truth(v)
	return &b
}

func expFn(name string, f func(a Args) Value) {
	expModule.Attrs[name] = Func(name, func(a Args) Value { return Guard(func() Value { return norm(f(a)) }) })
}

func init() {
	m := expModule.Attrs
	m["DType"] = dtypeModule
	m["ALL_FUNCTIONS"] = fromGo(exp.AllFunctions())
	m["TABLE_PARTS"] = norm(exp.TableParts)
	m["COLUMN_PARTS"] = norm(exp.ColumnParts)
	m["SQLGLOT_META"] = exp.SQLGlotMeta
	m["SQLGLOT_ANONYMOUS"] = exp.SQLGlotAnonymous

	expFn("column", func(a Args) Value {
		var fields []any
		if f, ok := a.Kwarg("fields"); ok && f != nil {
			fields = toGoList(f)
		}
		quoted, _ := a.Kwarg("quoted")
		copy := true
		if c, ok := a.Kwarg("copy"); ok {
			copy = Truth(c)
		}
		return exp.NewColumnFields(toGo(a.Get(0, "col", nil)), toGo(a.Get(1, "table", nil)), toGo(a.Get(2, "db", nil)),
			toGo(a.Get(3, "catalog", nil)), fields, boolPtr(quoted), copy)
	})
	expFn("to_identifier", func(a Args) Value {
		copy := true
		if c, ok := a.Kwarg("copy"); ok {
			copy = Truth(c)
		}
		return exp.ToIdentifierOpts(toGo(a.Pos[0]), boolPtr(a.Get(1, "quoted", nil)), copy)
	})
	expFn("to_table", func(a Args) Value {
		var kv []any
		for _, kw := range a.KW {
			if kw.K != "dialect" && kw.K != "copy" {
				kv = append(kv, kw.K, toGo(kw.V))
			}
		}
		return exp.ToTable(toGo(a.Pos[0]), dialectArg(a.Get(1, "dialect", nil)), kv...)
	})
	expFn("to_column", func(a Args) Value {
		var kv []any
		for _, kw := range a.KW {
			if kw.K != "dialect" && kw.K != "copy" && kw.K != "quoted" {
				kv = append(kv, kw.K, toGo(kw.V))
			}
		}
		return exp.ToColumn(toGo(a.Pos[0]), boolPtr(a.Get(1, "quoted", nil)), dialectArg(a.Get(2, "dialect", nil)), kv...)
	})
	expFn("table_", func(a Args) Value {
		quoted, _ := a.Kwarg("quoted")
		return exp.NewTableOpts(toGo(a.Get(0, "table", nil)), toGo(a.Get(1, "db", nil)), toGo(a.Get(2, "catalog", nil)),
			boolPtr(quoted), toGo(a.Get(4, "alias", nil)))
	})
	expFn("select", func(a Args) Value { return exp.NewSelect(builderArgs(a)...) })
	expFn("from_", func(a Args) Value { return exp.NewFrom(toGo(a.Pos[0]), toOptions(buildOptions(a))...) })
	expFn("condition", func(a Args) Value { return exp.NewCondition(toGo(a.Pos[0]), toOptions(buildOptions(a))...) })
	expFn("and_", func(a Args) Value { return exp.NewAnd(builderArgs(a)...) })
	expFn("or_", func(a Args) Value { return exp.NewOr(builderArgs(a)...) })
	expFn("xor", func(a Args) Value { return exp.NewXor(builderArgs(a)...) })
	expFn("not_", func(a Args) Value { return exp.NewNot(toGo(a.Pos[0]), toOptions(buildOptions(a))...) })
	expFn("paren", func(a Args) Value { return exp.NewParen(toGo(a.Pos[0]), toOptions(buildOptions(a))...) })
	expFn("alias_", func(a Args) Value {
		return exp.NewAlias(toGo(a.Pos[0]), toGo(a.Get(1, "alias", nil)), toOptions(buildOptions(a, "alias"))...)
	})
	expFn("cast", func(a Args) Value {
		return exp.NewCast(toGo(a.Pos[0]), toGo(a.Get(1, "to", nil)), toOptions(buildOptions(a, "to"))...)
	})
	expFn("func", func(a Args) Value {
		args := posArgs(Args{Pos: a.Pos[1:]})
		for _, o := range buildOptions(a) {
			args = append(args, o)
		}
		return exp.NewFunc(Str(a.Pos[0]), args...)
	})
	expFn("var", func(a Args) Value { return exp.NewVar(toGo(a.Pos[0])) })
	expFn("maybe_parse", func(a Args) Value {
		o := exp.ParseOptions{Dialect: dialectArg(a.Get(-1, "dialect", nil)), ErrorLevel: errs.Unset}
		if into, ok := a.Kwarg("into"); ok && into != nil {
			o.Into = kindsOf(into)
		}
		prefix := ""
		if p, ok := a.Kwarg("prefix"); ok && p != nil {
			prefix = Str(p)
		}
		copy := Truth(a.Get(-1, "copy", false))
		e, err := exp.MaybeParse(toGo(a.Pos[0]), o, prefix, copy)
		check(err)
		return e
	})
	expFn("parse_identifier", func(a Args) Value {
		return exp.ParseIdentifier(toGo(a.Pos[0]), dialectArg(a.Get(1, "dialect", nil)))
	})
	expFn("true", func(a Args) Value { return exp.NewTrue() })
	expFn("false", func(a Args) Value { return exp.NewFalse() })
	expFn("null", func(a Args) Value { return exp.NewNull() })
	expFn("array", func(a Args) Value { return exp.NewArray(builderArgs(a)...) })
	expFn("tuple_", func(a Args) Value { return exp.NewTuple(builderArgs(a)...) })
	expFn("convert", func(a Args) Value { return exp.ConvertValue(toConvert(a.Pos[0]), Truth(a.Get(1, "copy", false))) })
	expFn("replace_placeholders", func(a Args) Value {
		e, _ := norm(a.Pos[0]).(*exp.Expr)
		var pos []any
		for _, p := range a.Pos[1:] {
			pos = append(pos, toGo(p))
		}
		kw := map[string]any{}
		for _, k := range a.KW {
			kw[k.K] = toGo(k.V)
		}
		return exp.ReplacePlaceholders(e, pos, kw)
	})
	expFn("union", func(a Args) Value { return exp.NewUnion(builderArgs(a)...) })
	expFn("intersect", func(a Args) Value { return exp.NewIntersect(builderArgs(a)...) })
	expFn("except_", func(a Args) Value { return exp.NewExcept(builderArgs(a)...) })
	expFn("case", func(a Args) Value {
		return exp.NewCase(toGo(a.Get(0, "expression", nil)), toOptions(buildOptions(a, "expression"))...)
	})
	expFn("table_name", func(a Args) Value {
		return exp.TableName(toGo(a.Pos[0]), dialectArg(a.Get(1, "dialect", nil)), Truth(a.Get(2, "identify", false)))
	})
	expFn("to_interval", func(a Args) Value { return exp.ToInterval(toGo(a.Pos[0])) })
	expFn("subquery", func(a Args) Value {
		return exp.NewSubqueryFrom(toGo(a.Pos[0]), toGo(a.Get(1, "alias", nil)), toOptions(buildOptions(a, "alias"))...)
	})
	expFn("values", func(a Args) Value {
		alias := ""
		if al := a.Get(1, "alias", nil); al != nil {
			alias = Str(al)
		}
		var cols []string
		if c := a.Get(2, "columns", nil); c != nil {
			for _, x := range Iter(c) {
				cols = append(cols, Str(x))
			}
		}
		return exp.NewValues(toGoList(a.Pos[0]), alias, cols)
	})
	expFn("column_table_names", func(a Args) Value {
		e, _ := norm(a.Pos[0]).(*exp.Expr)
		out := NewSet()
		for _, k := range sortedKeys(exp.ColumnTableNames(e, "")) {
			out.Add(k)
		}
		return out
	})
}

func init() {
	typeAttrs[ExprType] = map[string]Value{
		"load": Func("load", func(a Args) Value {
			return Guard(func() Value {
				var payloads []map[string]any
				for _, p := range Iter(a.Pos[0]) {
					m, ok := toGo(p).(map[string]any)
					if !ok {
						Raise(TypeError, "expected a list of dicts")
					}
					payloads = append(payloads, m)
				}
				e, err := exp.Load(payloads)
				check(err)
				return e
			})
		}),
	}
}

func init() {
	expFn("update", func(a Args) Value {
		uo := exp.UpdateOptions{
			Properties: toOrderedMap(a.Get(1, "properties", nil)),
			Where:      toGo(a.Get(2, "where", nil)),
			From:       toGo(a.Get(3, "from_", nil)),
			With:       toOrderedMap(a.Get(5, "with_", nil)),
		}
		opts := buildOptions(a, "properties", "where", "from_", "with_")
		if d := a.Get(4, "dialect", nil); d != nil && len(a.Pos) > 4 {
			opts = append(opts, exp.WithDialect(dialectArg(d)))
		}
		return exp.NewUpdate(toGo(a.Pos[0]), uo, toOptions(opts)...)
	})
	expFn("delete", func(a Args) Value {
		return exp.NewDelete(toGo(a.Pos[0]), toGo(a.Get(1, "where", nil)), toGo(a.Get(2, "returning", nil)),
			toOptions(buildOptions(a, "where", "returning"))...)
	})
	expFn("insert", func(a Args) Value {
		io := exp.InsertOptions{Overwrite: toGo(a.Get(3, "overwrite", nil)), Returning: toGo(a.Get(4, "returning", nil))}
		if c := a.Get(2, "columns", nil); c != nil {
			io.Columns = toGoList(c)
		}
		return exp.NewInsert(toGo(a.Pos[0]), toGo(a.Get(1, "into", nil)), io,
			toOptions(buildOptions(a, "into", "columns", "overwrite", "returning"))...)
	})
	expFn("merge", func(a Args) Value {
		var whens []any
		for _, w := range a.Pos {
			whens = append(whens, toGo(w))
		}
		into, _ := a.Kwarg("into")
		using, _ := a.Kwarg("using")
		on, _ := a.Kwarg("on")
		returning, _ := a.Kwarg("returning")
		return exp.NewMerge(whens, toGo(into), toGo(using), toGo(on), toGo(returning),
			toOptions(buildOptions(a, "into", "using", "on", "returning"))...)
	})
	expFn("rename_table", func(a Args) Value {
		return sqlglot.RenameTable(toGo(a.Pos[0]), toGo(a.Pos[1]), dialectArg(a.Get(2, "dialect", nil)))
	})
	expFn("rename_column", func(a Args) Value {
		return sqlglot.RenameColumn(toGo(a.Pos[0]), toGo(a.Pos[1]), toGo(a.Pos[2]), boolPtr(a.Get(3, "exists", nil)),
			dialectArg(a.Get(4, "dialect", nil)))
	})
	expFn("find_tables", func(a Args) Value {
		out := NewSet()
		for _, t := range sqlglot.FindTables(norm(a.Pos[0]).(*exp.Expr)) {
			out.Add(t)
		}
		return out
	})
	expFn("normalize_table_name", func(a Args) Value {
		return sqlglot.NormalizeTableName(toGo(a.Pos[0]), dialectArg(a.Get(1, "dialect", nil)), Truth(a.Get(2, "copy", true)))
	})
	expFn("replace_tables", func(a Args) Value {
		mapping := map[string]string{}
		if d, ok := norm(a.Get(1, "mapping", nil)).(*Dict); ok {
			for i, k := range d.keys {
				mapping[Str(k)] = Str(d.vals[i])
			}
		}
		return sqlglot.ReplaceTables(norm(a.Pos[0]).(*exp.Expr), mapping, dialectArg(a.Get(2, "dialect", nil)), Truth(a.Get(3, "copy", true)))
	})
	expFn("expand", func(a Args) Value {
		sources := map[string]sqlglot.ExpandSource{}
		if d, ok := norm(a.Get(1, "sources", nil)).(*Dict); ok {
			for i, k := range d.keys {
				switch v := norm(d.vals[i]).(type) {
				case *exp.Expr:
					sources[Str(k)] = v
				default:
					f := v
					sources[Str(k)] = func() *exp.Expr { return norm(Call(f)).(*exp.Expr) }
				}
			}
		}
		return sqlglot.Expand(norm(a.Pos[0]).(*exp.Expr), sources, dialectArg(a.Get(2, "dialect", nil)), Truth(a.Get(3, "copy", true)))
	})
	expFn("replace_children", func(a Args) Value {
		e := norm(a.Pos[0]).(*exp.Expr)
		f := a.Pos[1]
		rest := a.Pos[2:]
		exp.ReplaceChildren(e, func(child *exp.Expr) []*exp.Expr {
			switch r := norm(Call(f, append([]Value{child}, rest...)...)).(type) {
			case *exp.Expr:
				return []*exp.Expr{r}
			case nil:
				return nil
			default:
				var out []*exp.Expr
				for _, x := range Iter(r) {
					if xe, ok := norm(x).(*exp.Expr); ok {
						out = append(out, xe)
					}
				}
				return out
			}
		})
		return nil
	})
	expFn("replace_tree", func(a Args) Value {
		f := a.Pos[1]
		var prune func(*exp.Expr) bool
		if p := a.Get(2, "prune", nil); p != nil {
			prune = func(n *exp.Expr) bool { return Truth(Call(p, n)) }
		}
		return exp.ReplaceTree(norm(a.Pos[0]).(*exp.Expr), func(n *exp.Expr) *exp.Expr {
			r, _ := norm(Call(f, n)).(*exp.Expr)
			return r
		}, prune)
	})
}

// toConvert converts a Python value for exp.ConvertValue, keeping the distinction between
// tuples (TupleValue), lists and dicts (OrderedMap) that exp.convert relies on.
func toConvert(v Value) any {
	switch x := norm(v).(type) {
	case Tuple:
		out := make(exp.TupleValue, len(x))
		for i, it := range x {
			out[i] = toConvert(it)
		}
		return out
	case *List:
		out := make([]any, len(x.V))
		for i, it := range x.V {
			out[i] = toConvert(it)
		}
		return out
	case *Dict:
		out := make(exp.OrderedMap, 0, x.Len())
		for i, k := range x.keys {
			out = append(out, exp.KV{Key: toConvert(k), Value: toConvert(x.vals[i])})
		}
		return out
	case *DecimalV:
		return exp.LiteralNumber(x.S)
	}
	return toGo(v)
}
