package pyrt

import (
	"reflect"
	"regexp"
	"sort"
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

// ArgsView is Expression.args.
type ArgsView struct{ e *exp.Expr }

func argsAttr(v *ArgsView, name string) Value {
	switch name {
	case "get":
		return method(name, func(a Args) Value {
			k := Str(a.Pos[0])
			for _, arg := range v.e.Args() {
				if arg.Key == k {
					return norm(arg.Value)
				}
			}
			return a.Get(1, "default", nil)
		})
	case "items":
		return method(name, func(a Args) Value {
			var out []Value
			for _, arg := range v.e.Args() {
				out = append(out, Tuple{arg.Key, norm(arg.Value)})
			}
			return &List{out}
		})
	case "keys":
		return method(name, func(a Args) Value { return &List{Iter(v)} })
	case "values":
		return method(name, func(a Args) Value {
			var out []Value
			for _, arg := range v.e.Args() {
				out = append(out, norm(arg.Value))
			}
			return &List{out}
		})
	case "pop":
		return method(name, func(a Args) Value {
			k := Str(a.Pos[0])
			val := norm(v.e.Arg(k))
			v.e.Set(k, nil)
			if val == nil && len(a.Pos) > 1 {
				return a.Pos[1]
			}
			return val
		})
	}
	Raise(AttributeError, "'dict' object has no attribute '%s'", name)
	return nil
}

func goErr(err error) *PyErr {
	switch err.(type) {
	case *errs.ParseError:
		return &PyErr{Class: ParseError, Msg: err.Error(), Go: err}
	case *errs.TokenError:
		return &PyErr{Class: TokenError, Msg: err.Error(), Go: err}
	case *errs.UnsupportedError:
		return &PyErr{Class: UnsupportedError, Msg: err.Error(), Go: err}
	case *errs.OptimizeError:
		return &PyErr{Class: OptimizeError, Msg: err.Error(), Go: err}
	case *errs.SchemaError:
		return &PyErr{Class: SchemaError, Msg: err.Error(), Go: err}
	case *errs.ExecuteError:
		return &PyErr{Class: ExecuteError, Msg: err.Error(), Go: err}
	}
	msg := err.Error()
	if m := unknownDialectRe.FindStringSubmatch(msg); m != nil {
		if KnownDialects[m[1]] {
			return &PyErr{Class: UnportedDialectError, Msg: m[1], Go: err}
		}
		return &PyErr{Class: ValueError, Msg: msg, Go: err}
	}
	for _, c := range []*ExcClass{ValueError, TypeError, KeyError, IndexError, AttributeError, NotImplementedError, RuntimeError, RecursionError} {
		if strings.HasPrefix(msg, c.Name+": ") {
			return &PyErr{Class: c, Msg: strings.TrimPrefix(msg, c.Name+": "), Go: err}
		}
	}
	return &PyErr{Class: Exception, Msg: msg, Go: err}
}

var unknownDialectRe = regexp.MustCompile(`^Unknown dialect '([^']*)'`)

// check raises err as a Python exception.
func check(err error) {
	if err != nil {
		panic(goErr(err))
	}
}

// Guard converts Go panics raised by the sqlglot API into Python exceptions. Operations that
// need a dialect that is not ported return a *SkippedV.
func Guard(f func() Value) (res Value) {
	defer func() {
		if r := recover(); r != nil {
			switch x := r.(type) {
			case *PyErr:
				if x.Class == UnportedDialectError {
					skipEvents++
					res = &SkippedV{x.Msg}
					return
				}
				panic(x)
			case error:
				if e := goErr(x); e.Class == UnportedDialectError {
					skipEvents++
					res = &SkippedV{e.Msg}
					return
				}
				panic(goErr(x))
			case string:
				panic(&PyErr{Class: Exception, Msg: x})
			}
			panic(r)
		}
	}()
	return f()
}

func kinds(vals []Value) []exp.Kind {
	var out []exp.Kind
	for _, v := range vals {
		switch x := v.(type) {
		case exp.Kind:
			out = append(out, x)
		case *BuiltinType:
			if x != ExprType {
				Raise(TypeError, "expected an expression class, got %s", x.Name)
			}
			out = append(out, exp.AnyExpression)
		case Tuple:
			out = append(out, kinds(x)...)
		default:
			Raise(TypeError, "expected an expression class, got %s", TypeName(v))
		}
	}
	return out
}

// genOptions converts sql(...) keyword arguments into exp.GenOptions.
func genOptions(a Args, dialectPos int) []any {
	var out []any
	if d := a.Get(dialectPos, "dialect", nil); d != nil {
		out = append(out, dialectArg(d))
	}
	for _, kw := range a.KW {
		switch kw.K {
		case "dialect":
		case "pretty":
			out = append(out, exp.Pretty(Truth(kw.V)))
		case "identify":
			if s, ok := kw.V.(string); ok {
				out = append(out, exp.Identify(s))
			} else {
				out = append(out, exp.Identify(Truth(kw.V)))
			}
		case "normalize":
			out = append(out, exp.NormalizeIdentifiers(Truth(kw.V)))
		case "unsupported_level":
			out = append(out, exp.UnsupportedLevel(kw.V.(errs.ErrorLevel)))
		case "comments":
			out = append(out, exp.Comments(Truth(kw.V)))
		case "normalize_functions":
			if s, ok := kw.V.(string); ok {
				out = append(out, exp.NormalizeFunctions(s))
			} else {
				out = append(out, exp.NormalizeFunctions(Truth(kw.V)))
			}
		case "leading_comma":
			out = append(out, exp.LeadingComma(Truth(kw.V)))
		case "max_text_width":
			out = append(out, exp.MaxTextWidth(mustInt(kw.V)))
		case "pad":
			out = append(out, exp.PadWidth(mustInt(kw.V)))
		case "indent":
			out = append(out, exp.IndentWidth(mustInt(kw.V)))
		case "max_unsupported":
			out = append(out, exp.MaxUnsupported(mustInt(kw.V)))
		case "copy":
			if !Truth(kw.V) {
				out = append(out, exp.NoCopy())
			}
		default:
			NotPorted("sql(%s=...)", kw.K)
		}
	}
	return out
}

// dialectArg converts a dialect argument (name, Dialect or class) for the Go API.
func dialectArg(v Value) any {
	switch x := norm(v).(type) {
	case nil:
		return nil
	case string:
		return x
	case *sqlglot.Dialect, *sqlglot.DialectClass:
		return x
	case *DialectClassV:
		return x.C
	case *DialectV:
		return x.D
	}
	return Str(v)
}

// buildOptions converts builder keyword arguments (copy, dialect, append, ...) to exp Options.
func buildOptions(a Args, skip ...string) []any {
	var out []any
	for _, kw := range a.KW {
		skipped := false
		for _, s := range skip {
			if s == kw.K {
				skipped = true
			}
		}
		if skipped {
			continue
		}
		switch kw.K {
		case "copy":
			out = append(out, exp.WithCopy(Truth(kw.V)))
		case "dialect":
			out = append(out, exp.WithDialect(dialectArg(kw.V)))
		case "append":
			out = append(out, exp.WithAppend(Truth(kw.V)))
		case "wrap":
			out = append(out, exp.WithWrap(Truth(kw.V)))
		case "distinct":
			out = append(out, exp.WithDistinct(Truth(kw.V)))
		case "quoted":
			out = append(out, exp.WithQuoted(Truth(kw.V)))
		case "error_level":
			out = append(out, exp.WithErrorLevel(kw.V.(errs.ErrorLevel)))
		default:
			out = append(out, exp.WithArg(kw.K, toGo(kw.V)))
		}
	}
	return out
}

func posArgs(a Args) []any {
	out := make([]any, len(a.Pos))
	for i, p := range a.Pos {
		out[i] = toGo(p)
	}
	return out
}

func builderArgs(a Args, skip ...string) []any {
	return append(posArgs(a), buildOptions(a, skip...)...)
}

func exprMethod(e *exp.Expr, name string, f func(a Args) Value) *Fn {
	return &Fn{Name: e.ClassName() + "." + name, F: func(a Args) Value { return Guard(func() Value { return norm(f(a)) }) }}
}

func exprAttr(e *exp.Expr, name string) Value {
	switch name {
	// properties
	case "args":
		return &ArgsView{e}
	case "this":
		return norm(e.Arg("this"))
	case "expression":
		return norm(e.Arg("expression"))
	case "expressions":
		if l := e.List("expressions"); l != nil {
			return norm(l)
		}
		return &List{}
	case "name":
		return e.Name()
	case "alias":
		return e.Alias()
	case "alias_or_name":
		return e.AliasOrName()
	case "output_name":
		return e.OutputName()
	case "alias_column_names":
		return norm(e.AliasColumnNames())
	case "key":
		return e.Key()
	case "parent":
		return norm(e.Parent())
	case "arg_key":
		return nilIfEmpty(e.ArgKey())
	case "index":
		if e.Index() < 0 {
			return nil
		}
		return e.Index()
	case "depth":
		return e.Depth()
	case "comments":
		if e.Comments == nil {
			return nil
		}
		return norm(append([]string(nil), e.Comments...))
	case "meta":
		return metaDict(e)
	case "type":
		return norm(e.Type())
	case "is_string":
		return e.IsString()
	case "is_number":
		return e.IsNumber()
	case "is_int":
		return e.IsInt()
	case "is_star":
		return e.IsStar()
	case "is_negative":
		return e.IsNegative()
	case "is_leaf":
		return exprMethod(e, name, func(a Args) Value { return e.IsLeaf() })
	case "selects":
		return norm(e.Selects())
	case "named_selects":
		return norm(e.NamedSelects())
	case "ctes":
		return norm(e.Ctes())
	case "parts":
		return norm(e.Parts())
	case "table":
		if e.IsKind(exp.Column) {
			return e.TableName()
		}
	case "db":
		return e.DB()
	case "catalog":
		return e.Catalog()
	case "quoted":
		return e.Quoted()
	case "unit":
		return norm(e.Unit())
	case "to":
		return norm(e.To())
	case "left":
		if e.Is(exp.SetOperation) {
			return norm(e.This())
		}
		return norm(e.Arg("this"))
	case "right":
		if e.Is(exp.SetOperation) {
			return norm(e.Expression())
		}
		return norm(e.Arg("expression"))
	case "same_parent":
		return e.SameParent()
	case "parent_select":
		return norm(e.ParentSelect())
	case "is_wrapper":
		return e.IsWrapper()
	case "is_limit_all":
		return e.IsLimitAll()
	case "is_semi_or_anti_join":
		return e.IsSemiOrAntiJoin()
	case "recursive":
		return e.Recursive()
	case "unpivot":
		return e.Unpivot()
	case "columns":
		return norm(e.Columns())
	case "constraints":
		return norm(e.Constraints())
	case "actions":
		return norm(e.Actions())
	case "fields":
		return norm(e.Fields())
	case "return_type":
		return norm(e.ReturnType())
	case "aliases":
		return norm(e.Expressions())
	case "side":
		if e.IsKind(exp.Join) {
			return e.TextUpper("side")
		}
		return norm(e.Arg("side"))
	case "kind":
		switch {
		case e.IsKind(exp.Join), e.IsKind(exp.Undrop):
			return e.TextUpper("kind")
		case e.IsAny(exp.Alter, exp.Create, exp.Drop):
			if k, ok := e.Arg("kind").(string); ok && k != "" {
				return strings.ToUpper(k)
			}
			return norm(e.Arg("kind"))
		}
		return norm(e.Arg("kind"))
	case "method":
		if e.IsKind(exp.Join) {
			return e.TextUpper("method")
		}
	case "keys":
		if e.IsAny(exp.Map, exp.VarMap) {
			return norm(e.Keys())
		}
	case "values":
		if e.IsAny(exp.Map, exp.VarMap) {
			return norm(e.Values())
		}
	case "is_data_type":
		return e.IsKind(exp.DataType)

	// methods
	case "sql":
		return exprMethod(e, name, func(a Args) Value {
			sql, err := e.Generate(genOptions(a, 0)...)
			check(err)
			return sql
		})
	case "find":
		return exprMethod(e, name, func(a Args) Value {
			if bfs, ok := a.Kwarg("bfs"); ok && !Truth(bfs) {
				for n := range e.FindAllWalk(false, kinds(a.Pos)...) {
					return n
				}
				return nil
			}
			return e.Find(kinds(a.Pos)...)
		})
	case "find_all":
		return exprMethod(e, name, func(a Args) Value {
			bfs := true
			if b, ok := a.Kwarg("bfs"); ok {
				bfs = Truth(b)
			}
			var out []Value
			for n := range e.FindAllWalk(bfs, kinds(a.Pos)...) {
				out = append(out, n)
			}
			return &List{out}
		})
	case "find_ancestor":
		return exprMethod(e, name, func(a Args) Value { return e.FindAncestor(kinds(a.Pos)...) })
	case "assert_is":
		return exprMethod(e, name, func(a Args) Value {
			k := kinds(a.Pos)[0]
			if !e.Is(k) {
				Raise(AssertionError, "%s is not %s.", e.SQL(), k.Name())
			}
			return e
		})
	case "copy", "__deepcopy__":
		return exprMethod(e, name, func(a Args) Value { return e.Copy() })
	case "text":
		return exprMethod(e, name, func(a Args) Value { return e.Text(Str(a.Pos[0])) })
	case "to_py":
		return exprMethod(e, name, func(a Args) Value {
			v, err := e.ToPy()
			if err != nil {
				Raise(ValueError, "%s", err.Error())
			}
			return fromGo(v)
		})
	case "is_type":
		return exprMethod(e, name, func(a Args) Value {
			var dts []any
			for _, p := range a.Pos {
				dts = append(dts, toGo(p))
			}
			if c, ok := a.Kwarg("check_nullable"); ok && Truth(c) {
				return e.IsTypeCheckNullable(dts...)
			}
			return e.IsType(dts...)
		})
	case "walk", "bfs", "dfs":
		return exprMethod(e, name, func(a Args) Value {
			bfs := name != "dfs"
			if name == "walk" {
				if b, ok := a.Kwarg("bfs"); ok {
					bfs = Truth(b)
				} else if len(a.Pos) > 0 {
					bfs = Truth(a.Pos[0])
				}
			}
			var prune func(*exp.Expr) bool
			if p, ok := a.Kwarg("prune"); ok && p != nil {
				prune = func(n *exp.Expr) bool { return Truth(Call(p, n)) }
			}
			var out []Value
			for n := range e.Walk(bfs, prune) {
				out = append(out, n)
			}
			return &List{out}
		})
	case "iter_expressions":
		return exprMethod(e, name, func(a Args) Value { return norm(e.IterExpressions()) })
	case "transform":
		return exprMethod(e, name, func(a Args) Value {
			fun := a.Pos[0]
			extra := a.Pos[1:]
			copy := true
			var kws []Value
			for _, kw := range a.KW {
				if kw.K == "copy" {
					copy = Truth(kw.V)
				} else {
					kws = append(kws, kw)
				}
			}
			return e.Transform(func(n *exp.Expr) *exp.Expr {
				r := Call(fun, append(append([]Value{n}, extra...), kws...)...)
				x, _ := norm(r).(*exp.Expr)
				return x
			}, copy)
		})
	case "replace":
		return exprMethod(e, name, func(a Args) Value {
			n, _ := norm(a.Pos[0]).(*exp.Expr)
			return e.Replace(n)
		})
	case "pop":
		return exprMethod(e, name, func(a Args) Value { return e.Pop() })
	case "set":
		return exprMethod(e, name, func(a Args) Value {
			idx := -1
			if i := a.Get(2, "index", nil); i != nil {
				idx = mustInt(i)
			}
			overwrite := true
			if o, ok := a.Kwarg("overwrite"); ok {
				overwrite = Truth(o)
			}
			e.SetAt(Str(a.Pos[0]), toGo(a.Get(1, "value", nil)), idx, overwrite)
			return nil
		})
	case "append":
		return exprMethod(e, name, func(a Args) Value { e.Append(Str(a.Pos[0]), toGo(a.Pos[1])); return nil })
	case "unnest":
		return exprMethod(e, name, func(a Args) Value { return e.Unnest() })
	case "unalias":
		return exprMethod(e, name, func(a Args) Value { return e.Unalias() })
	case "flatten":
		return exprMethod(e, name, func(a Args) Value {
			unnest := true
			if u, ok := a.Kwarg("unnest"); ok {
				unnest = Truth(u)
			}
			return norm(e.Flatten(unnest))
		})
	case "root":
		return exprMethod(e, name, func(a Args) Value { return e.Root() })
	case "to_s":
		return exprMethod(e, name, func(a Args) Value { return e.ToS() })
	case "dump":
		return exprMethod(e, name, func(a Args) Value { NotPorted("Expr.dump"); return nil })
	case "add_comments":
		return exprMethod(e, name, func(a Args) Value {
			var cs []string
			for _, c := range Iter(a.Pos[0]) {
				cs = append(cs, Str(c))
			}
			prepend := false
			if p, ok := a.Kwarg("prepend"); ok {
				prepend = Truth(p)
			}
			e.AddComments(cs, prepend)
			return nil
		})
	case "pop_comments":
		return exprMethod(e, name, func(a Args) Value { return norm(e.PopComments()) })
	case "update_positions":
		return exprMethod(e, name, func(a Args) Value { NotPorted("Expr.update_positions"); return nil })
	case "error_messages":
		return exprMethod(e, name, func(a Args) Value { return norm(e.ErrorMessages(nil)) })
	case "to_column":
		return exprMethod(e, name, func(a Args) Value {
			copy := true
			if c, ok := a.Kwarg("copy"); ok {
				copy = Truth(c)
			}
			return e.ToColumnExpr(copy)
		})
	case "to_dot":
		return exprMethod(e, name, func(a Args) Value {
			include := true
			if c, ok := a.Kwarg("include_dots"); ok {
				include = Truth(c)
			}
			return e.ToDot(include)
		})

	// builders
	case "select":
		return exprMethod(e, name, func(a Args) Value { return e.Select(builderArgs(a)...) })
	case "from_":
		return exprMethod(e, name, func(a Args) Value { return e.From(toGo(a.Pos[0]), toOptions(buildOptions(a))...) })
	case "where":
		return exprMethod(e, name, func(a Args) Value { return e.Where(builderArgs(a)...) })
	case "group_by":
		return exprMethod(e, name, func(a Args) Value { return e.GroupBy(builderArgs(a)...) })
	case "order_by":
		return exprMethod(e, name, func(a Args) Value { return e.OrderBy(builderArgs(a)...) })
	case "sort_by":
		return exprMethod(e, name, func(a Args) Value { return e.SortBy(builderArgs(a)...) })
	case "cluster_by":
		return exprMethod(e, name, func(a Args) Value { return e.ClusterBy(builderArgs(a)...) })
	case "having":
		return exprMethod(e, name, func(a Args) Value { return e.Having(builderArgs(a)...) })
	case "qualify":
		return exprMethod(e, name, func(a Args) Value { return e.Qualify(builderArgs(a)...) })
	case "window":
		return exprMethod(e, name, func(a Args) Value { return e.Window(builderArgs(a)...) })
	case "lateral":
		return exprMethod(e, name, func(a Args) Value { return e.LateralView(builderArgs(a)...) })
	case "distinct":
		return exprMethod(e, name, func(a Args) Value { return e.Distinct(builderArgs(a)...) })
	case "hint":
		if e.IsKind(exp.Join) {
			return e.TextUpper("hint")
		}
		return exprMethod(e, name, func(a Args) Value { return e.Hint(builderArgs(a)...) })
	case "limit":
		return exprMethod(e, name, func(a Args) Value { return e.Limit(toGo(a.Pos[0]), toOptions(buildOptions(a))...) })
	case "offset":
		return exprMethod(e, name, func(a Args) Value { return e.Offset(toGo(a.Pos[0]), toOptions(buildOptions(a))...) })
	case "with_":
		return exprMethod(e, name, func(a Args) Value {
			var cte exp.CTEOptions
			if r, ok := a.Kwarg("recursive"); ok {
				cte.Recursive = toGo(r)
			}
			if m, ok := a.Kwarg("materialized"); ok {
				cte.Materialized = toGo(m)
			}
			if s, ok := a.Kwarg("scalar"); ok {
				cte.Scalar = toGo(s)
			}
			return e.WithCTE(toGo(a.Get(0, "alias", nil)), toGo(a.Get(1, "as_", nil)), cte,
				toOptions(buildOptions(a, "alias", "as_", "recursive", "materialized", "scalar"))...)
		})
	case "join":
		return exprMethod(e, name, func(a Args) Value {
			var jo exp.JoinOptions
			if v, ok := a.Kwarg("on"); ok {
				jo.On = toGo(v)
			}
			if v, ok := a.Kwarg("using"); ok {
				jo.Using = toGo(v)
			}
			if v, ok := a.Kwarg("join_type"); ok {
				jo.JoinType = Str(v)
			}
			if v, ok := a.Kwarg("join_alias"); ok {
				jo.JoinAlias = toGo(v)
			}
			return e.Join(toGo(a.Pos[0]), jo, toOptions(buildOptions(a, "on", "using", "join_type", "join_alias"))...)
		})
	case "on":
		return exprMethod(e, name, func(a Args) Value { return e.On(builderArgs(a)...) })
	case "using":
		return exprMethod(e, name, func(a Args) Value { return e.Using(builderArgs(a)...) })
	case "union":
		return exprMethod(e, name, func(a Args) Value { return e.Union(builderArgs(a)...) })
	case "intersect":
		return exprMethod(e, name, func(a Args) Value { return e.Intersect(builderArgs(a)...) })
	case "except_":
		return exprMethod(e, name, func(a Args) Value { return e.Except(builderArgs(a)...) })
	case "subquery":
		return exprMethod(e, name, func(a Args) Value {
			copy := true
			if c, ok := a.Kwarg("copy"); ok {
				copy = Truth(c)
			}
			return e.Subquery(toGo(a.Get(0, "alias", nil)), copy)
		})
	case "as_":
		return exprMethod(e, name, func(a Args) Value {
			return e.As(toGo(a.Get(0, "alias", nil)), toOptions(buildOptions(a, "alias"))...)
		})
	case "and_":
		return exprMethod(e, name, func(a Args) Value { return e.And(builderArgs(a)...) })
	case "or_":
		return exprMethod(e, name, func(a Args) Value { return e.Or(builderArgs(a)...) })
	case "not_":
		return exprMethod(e, name, func(a Args) Value { return e.Not(toOptions(buildOptions(a))...) })
	case "eq":
		return exprMethod(e, name, func(a Args) Value { return e.Eq(toGo(a.Pos[0])) })
	case "neq":
		return exprMethod(e, name, func(a Args) Value { return e.Neq(toGo(a.Pos[0])) })
	case "is_":
		return exprMethod(e, name, func(a Args) Value { return e.IsOp(toGo(a.Pos[0])) })
	case "like":
		return exprMethod(e, name, func(a Args) Value { return e.Like(toGo(a.Pos[0])) })
	case "ilike":
		return exprMethod(e, name, func(a Args) Value { return e.ILike(toGo(a.Pos[0])) })
	case "rlike":
		return exprMethod(e, name, func(a Args) Value { return e.RLike(toGo(a.Pos[0])) })
	case "isin":
		return exprMethod(e, name, func(a Args) Value { return e.IsIn(builderArgs(a)...) })
	case "between":
		return exprMethod(e, name, func(a Args) Value {
			return e.Between(toGo(a.Pos[0]), toGo(a.Pos[1]), toGo(a.Get(2, "symmetric", nil)))
		})
	case "asc":
		return exprMethod(e, name, func(a Args) Value { return e.Asc(Truth(a.Get(0, "nulls_first", true))) })
	case "desc":
		return exprMethod(e, name, func(a Args) Value { return e.Desc(Truth(a.Get(0, "nulls_first", false))) })
	case "div":
		return exprMethod(e, name, func(a Args) Value {
			return e.DivOp(toGo(a.Pos[0]), Truth(a.Get(1, "typed", false)), Truth(a.Get(2, "safe", false)))
		})
	case "lock":
		return exprMethod(e, name, func(a Args) Value {
			return e.Lock(Truth(a.Get(0, "update", true)), Truth(a.Get(1, "copy", true)))
		})
	case "returning":
		return exprMethod(e, name, func(a Args) Value { return e.Returning(toGo(a.Pos[0]), toOptions(buildOptions(a))...) })
	case "ctas":
		return exprMethod(e, name, func(a Args) Value { NotPorted("Expr.ctas"); return nil })
	case "set_kwargs":
		return exprMethod(e, name, func(a Args) Value {
			var kv []any
			for _, kw := range a.KW {
				kv = append(kv, kw.K, toGo(kw.V))
			}
			return e.SetKwargs(kv...)
		})
	case "__class__":
		return e.Kind()
	}
	Raise(AttributeError, "'%s' object has no attribute '%s'", e.ClassName(), name)
	return nil
}

func toOptions(xs []any) []exp.Option {
	out := make([]exp.Option, 0, len(xs))
	for _, x := range xs {
		if o, ok := x.(exp.Option); ok {
			out = append(out, o)
		}
	}
	return out
}

func metaDict(e *exp.Expr) Value {
	m := e.Meta()
	d := NewDict()
	for _, k := range sortedKeys(m) {
		d.Set(k, fromGo(m[k]))
	}
	return d
}

// fromGo converts a Go API value into a Python value.
func fromGo(v any) Value {
	switch x := v.(type) {
	case tokens.TokenSet:
		out := NewSet()
		for _, t := range x.Types() {
			out.Add(t)
		}
		return out
	case sqlglot.StringSet:
		out := NewSet()
		for _, k := range sortedKeys(x) {
			out.Add(k)
		}
		return out
	case []exp.Kind:
		out := make(Tuple, len(x))
		for i, k := range x {
			out[i] = k
		}
		return out
	case []any:
		out := make([]Value, len(x))
		for i, it := range x {
			out[i] = fromGo(it)
		}
		return &List{out}
	case map[string]any:
		d := NewDict()
		for _, k := range sortedKeys(x) {
			d.Set(k, fromGo(x[k]))
		}
		return d
	case int64:
		return int(x)
	case float32:
		return float64(x)
	}
	// maps with string keys become dicts (sorted keys), other slices lists
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() == reflect.String {
			keys := make([]string, 0, rv.Len())
			for _, k := range rv.MapKeys() {
				keys = append(keys, k.String())
			}
			sort.Strings(keys)
			d := NewDict()
			for _, k := range keys {
				d.Set(k, fromGo(rv.MapIndex(reflect.ValueOf(k).Convert(rv.Type().Key())).Interface()))
			}
			return d
		}
	case reflect.Slice:
		if rv.Type().Elem().Kind() != reflect.Uint8 {
			out := make([]Value, rv.Len())
			for i := range out {
				out[i] = fromGo(rv.Index(i).Interface())
			}
			return &List{out}
		}
	}
	return norm(v)
}

func construct(k exp.Kind, a Args) Value {
	if len(a.Pos) > 0 {
		NotPorted("positional arguments to %s()", k.Name())
	}
	var kv []any
	for _, kw := range a.KW {
		kv = append(kv, kw.K, toGo(kw.V))
	}
	return Guard(func() Value { return exp.New(k, kv...) })
}

func kindAttr(k exp.Kind, name string) Value {
	switch name {
	case "__name__":
		return k.Name()
	case "key":
		return k.Key()
	case "arg_types":
		d := NewDict()
		for _, spec := range k.Args() {
			d.Set(spec.Key, spec.Required)
		}
		return d
	case "is_var_len_args":
		return k.IsVarLenArgs()
	case "sql_names":
		return Func(name, func(a Args) Value { return norm(k.SQLNames()) })
	case "sql_name":
		return Func(name, func(a Args) Value { return k.SQLName() })
	case "from_arg_list":
		return Func(name, func(a Args) Value {
			var args []*exp.Expr
			for _, it := range Iter(a.Pos[0]) {
				x, _ := norm(it).(*exp.Expr)
				args = append(args, x)
			}
			return Guard(func() Value { return exp.FromArgList(k, args) })
		})
	}
	switch k {
	case exp.Literal:
		switch name {
		case "number":
			return Func(name, func(a Args) Value { return exp.LiteralNumber(toGo(a.Pos[0])) })
		case "string":
			return Func(name, func(a Args) Value { return exp.LiteralString(Str(a.Pos[0])) })
		}
	case exp.DataType:
		switch name {
		case "build":
			return Func(name, func(a Args) Value {
				udt := Truth(a.Get(2, "udt", false))
				var kv []any
				for _, kw := range a.KW {
					switch kw.K {
					case "dialect", "udt", "copy":
					default:
						kv = append(kv, kw.K, toGo(kw.V))
					}
				}
				d, err := exp.DataTypeBuild(toGo(a.Pos[0]), dialectArg(a.Get(1, "dialect", nil)), udt, kv...)
				check(err)
				return d
			})
		case "Type":
			return dtypeModule
		case "TEXT_TYPES":
			return typeSet(exp.DataTypeTextTypes)
		case "INTEGER_TYPES":
			return typeSet(exp.DataTypeIntegerTypes)
		case "REAL_TYPES":
			return typeSet(exp.DataTypeRealTypes)
		case "NUMERIC_TYPES":
			return typeSet(exp.DataTypeNumericTypes)
		case "TEMPORAL_TYPES":
			return typeSet(exp.DataTypeTemporalTypes)
		case "NESTED_TYPES":
			return typeSet(exp.DataTypeNestedTypes)
		case "FLOAT_TYPES":
			return typeSet(exp.DataTypeFloatTypes)
		case "ARRAY_TYPES":
			return typeSet(exp.DataTypeArrayTypes)
		case "STRUCT_TYPES":
			return typeSet(exp.DataTypeStructTypes)
		case "BINARY_TYPES":
			return typeSet(exp.DataTypeBinaryTypes)
		}
	}
	Raise(AttributeError, "type object '%s' has no attribute '%s'", k.Name(), name)
	return nil
}

func typeSet(s exp.TypeSet) *Set {
	out := NewSet()
	for _, d := range exp.AllDTypes {
		if s.Has(d) {
			out.Add(d)
		}
	}
	return out
}

var dtypeModule = &Module{Name: "DType", Get: func(name string) (Value, bool) {
	for _, d := range exp.AllDTypes {
		if dtypeName(d) == name {
			return d, true
		}
	}
	return nil, false
}}

func dtypeName(d exp.DType) string { return string(d) }

func dtypeAttr(d exp.DType, name string) Value {
	switch name {
	case "value", "name":
		return string(d)
	case "into_expr":
		return Func(name, func(a Args) Value { return d.IntoExpr() })
	}
	Raise(AttributeError, "'DType' object has no attribute '%s'", name)
	return nil
}
