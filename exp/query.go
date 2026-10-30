package exp

import (
	"fmt"
)

// ---------------------------------------------------------------------------------------
// generic builder helpers (mirroring _apply_builder & friends)

func isWrongExpression(v any, into Kind) bool {
	e, ok := v.(*Expr)
	return ok && e != nil && !e.Is(into)
}

func applyBuilder(expression any, instance *Expr, arg string, o buildOpts, prefix string, into Kind, intoArg string) *Expr {
	if into != InvalidKind && isWrongExpression(expression, into) {
		if intoArg == "" {
			intoArg = "this"
		}
		expression = New(into, intoArg, expression)
	}
	inst := MaybeCopy(instance, o.copy)
	var e *Expr
	if into != InvalidKind {
		e = MustMaybeParse(expression, o.parseOptions(into), prefix, false)
	} else {
		e = MustMaybeParse(expression, o.parseOptions(), prefix, false)
	}
	inst.Set(arg, e)
	return inst
}

func applyChildListBuilder(expressions []any, instance *Expr, arg string, o buildOpts, prefix string, into Kind, properties map[string]any) *Expr {
	inst := MaybeCopy(instance, o.copy)
	var parsed []*Expr
	if properties == nil {
		properties = map[string]any{}
	}
	var propOrder []string
	for _, expression := range expressions {
		if expression == nil {
			continue
		}
		if x, ok := expression.(*Expr); ok && x == nil {
			continue
		}
		if isWrongExpression(expression, into) {
			expression = New(into, "expressions", []*Expr{expression.(*Expr)})
		}
		e := MustMaybeParse(expression, o.parseOptions(into), prefix, false)
		for _, a := range e.args {
			if a.Key == "expressions" {
				parsed = append(parsed, a.Value.([]*Expr)...)
			} else {
				if _, ok := properties[a.Key]; !ok {
					propOrder = append(propOrder, a.Key)
				}
				properties[a.Key] = a.Value
			}
		}
	}
	if existing := inst.Get(arg); o.append && existing != nil {
		parsed = append(append([]*Expr(nil), existing.Expressions()...), parsed...)
	}
	child := New(into, "expressions", parsed)
	for k, v := range properties {
		if !contains(propOrder, k) {
			child.Set(k, v)
		}
	}
	for _, k := range propOrder {
		child.Set(k, properties[k])
	}
	inst.Set(arg, child)
	return inst
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func applyListBuilder(expressions []any, instance *Expr, arg string, o buildOpts, prefix string, into Kind) *Expr {
	inst := MaybeCopy(instance, o.copy)
	var parsed []*Expr
	for _, expression := range expressions {
		if expression == nil {
			continue
		}
		if x, ok := expression.(*Expr); ok && x == nil {
			continue
		}
		var intoKinds []Kind
		if into != InvalidKind {
			intoKinds = []Kind{into}
		}
		parsed = append(parsed, MustMaybeParse(expression, o.parseOptions(intoKinds...), prefix, false))
	}
	if existing := inst.List(arg); o.append && len(existing) > 0 {
		parsed = append(append([]*Expr(nil), existing...), parsed...)
	}
	inst.Set(arg, parsed)
	return inst
}

func applyConjunctionBuilder(expressions []any, instance *Expr, arg string, into Kind, o buildOpts) *Expr {
	var filtered []any
	for _, e := range expressions {
		if e == nil {
			continue
		}
		if s, ok := e.(string); ok && s == "" {
			continue
		}
		if x, ok := e.(*Expr); ok && x == nil {
			continue
		}
		filtered = append(filtered, e)
	}
	if len(filtered) == 0 {
		return instance
	}
	inst := MaybeCopy(instance, o.copy)
	if existing := inst.Get(arg); o.append && existing != nil {
		if into != InvalidKind {
			filtered = append([]any{existing.This()}, filtered...)
		} else {
			filtered = append([]any{existing}, filtered...)
		}
	}
	node := combine(filtered, And, o)
	if into != InvalidKind {
		inst.Set(arg, New(into, "this", node))
	} else {
		inst.Set(arg, node)
	}
	return inst
}

func applyCTEBuilder(instance *Expr, alias, as any, recursive, materialized, scalar any, o buildOpts) *Expr {
	aliasExpr := MustMaybeParse(alias, o.parseOptions(TableAlias), "", false)
	asExpr := MustMaybeParse(as, o.parseOptions(), "", o.copy)
	if Truthy(scalar) && !asExpr.Is(Subquery) {
		asExpr = New(Subquery, "this", asExpr)
	}
	cte := New(CTE, "this", asExpr, "alias", aliasExpr, "materialized", materialized, "scalar", scalar)
	props := map[string]any{}
	if Truthy(recursive) {
		props["recursive"] = recursive
	}
	return applyChildListBuilder([]any{cte}, instance, "with_", o, "", With, props)
}

// ---------------------------------------------------------------------------------------
// Query builders

// Subquery mirrors Query.subquery(alias, copy).
func (e *Expr) Subquery(alias any, copy bool) *Expr {
	inst := MaybeCopy(e, copy)
	var a *Expr
	switch x := alias.(type) {
	case *Expr:
		a = x
	case string:
		if x != "" {
			a = New(TableAlias, "this", ToIdentifier(x))
		}
	}
	return New(Subquery, "this", inst, "alias", a)
}

// Limit mirrors Query.limit(expression).
func (e *Expr) Limit(expression any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	return applyBuilder(expression, e, "limit", o, "LIMIT", Limit, "expression")
}

// Offset mirrors Query.offset(expression).
func (e *Expr) Offset(expression any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	return applyBuilder(expression, e, "offset", o, "OFFSET", Offset, "expression")
}

// OrderBy mirrors Query.order_by(*expressions).
func (e *Expr) OrderBy(args ...any) *Expr {
	pos, o := splitArgs(args)
	return applyChildListBuilder(pos, e, "order", o, "ORDER BY", Order, nil)
}

// Where mirrors Query.where / Delete.where / Update.where.
func (e *Expr) Where(args ...any) *Expr {
	pos, o := splitArgs(args)
	if implWhere[e.kind] == Query {
		for i, x := range pos {
			if w, ok := x.(*Expr); ok && w != nil && w.Is(Where) {
				pos[i] = w.This()
			}
		}
	}
	return applyConjunctionBuilder(pos, e, "where", Where, o)
}

// CTEOptions are the optional arguments of With.
type CTEOptions struct {
	Recursive    any
	Materialized any
	Scalar       any
}

// With mirrors Query.with_(alias, as_, ...).
func (e *Expr) With(alias, as any, opts ...Option) *Expr {
	return e.WithCTE(alias, as, CTEOptions{}, opts...)
}

// WithCTE mirrors Query.with_ with recursive/materialized/scalar flags.
func (e *Expr) WithCTE(alias, as any, cte CTEOptions, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	scalar := cte.Scalar
	if implWithU[e.kind] != Query {
		scalar = nil
	}
	return applyCTEBuilder(e, alias, as, cte.Recursive, cte.Materialized, scalar, o)
}

var implWithU = implWith

func setOperation(kind Kind, args []any) *Expr {
	pos, o := splitArgs(args)
	if len(pos) < 2 {
		panic(fmt.Sprintf("At least two expressions are required by `%s`.", kind.Key()))
	}
	var result *Expr
	for _, x := range pos {
		e := MustMaybeParse(x, o.parseOptions(), "", o.copy)
		if result == nil {
			result = e
			continue
		}
		result = New(kind, "this", result, "expression", e, "distinct", o.distinct)
	}
	return result
}

// Union mirrors Query.union(*expressions, distinct=True).
func (e *Expr) Union(args ...any) *Expr { return setOperation(Union, append([]any{e}, args...)) }

// Intersect mirrors Query.intersect.
func (e *Expr) Intersect(args ...any) *Expr {
	return setOperation(Intersect, append([]any{e}, args...))
}

// Except mirrors Query.except_.
func (e *Expr) Except(args ...any) *Expr { return setOperation(Except, append([]any{e}, args...)) }

// NewUnion mirrors exp.union(*expressions).
func NewUnion(args ...any) *Expr { return setOperation(Union, args) }

// NewIntersect mirrors exp.intersect(*expressions).
func NewIntersect(args ...any) *Expr { return setOperation(Intersect, args) }

// NewExcept mirrors exp.except_(*expressions).
func NewExcept(args ...any) *Expr { return setOperation(Except, args) }

// Select mirrors Select.select / SetOperation.select / Subquery.select.
func (e *Expr) Select(args ...any) *Expr {
	pos, o := splitArgs(args)
	switch implSelect[e.kind] {
	case SetOperation:
		inst := MaybeCopy(e, o.copy)
		inner := append(append([]any(nil), pos...), WithCopy(false), WithAppend(o.append), WithDialect(o.dialect))
		inst.This().Unnest().Select(inner...)
		inst.Expression().Unnest().Select(inner...)
		return inst
	case Subquery:
		inst := MaybeCopy(e, o.copy)
		inner := inst.Unnest()
		if implSelect[inner.kind] != InvalidKind && implSelect[inner.kind] != Query {
			inner.Select(append(append([]any(nil), pos...), WithCopy(false), WithAppend(o.append), WithDialect(o.dialect))...)
		}
		return inst
	case Select:
		return applyListBuilder(pos, e, "expressions", o, "", InvalidKind)
	}
	panic("Query objects must implement `select`")
}

// From mirrors Select.from_ / Update.from_.
func (e *Expr) From(expression any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	if implFrom[e.kind] == Update && !Truthy(expression) {
		return MaybeCopy(e, o.copy)
	}
	return applyBuilder(expression, e, "from_", o, "FROM", From, "")
}

// GroupBy mirrors Select.group_by.
func (e *Expr) GroupBy(args ...any) *Expr {
	pos, o := splitArgs(args)
	if len(pos) == 0 {
		return MaybeCopy(e, o.copy)
	}
	return applyChildListBuilder(pos, e, "group", o, "GROUP BY", Group, nil)
}

// SortBy mirrors Select.sort_by.
func (e *Expr) SortBy(args ...any) *Expr {
	pos, o := splitArgs(args)
	return applyChildListBuilder(pos, e, "sort", o, "SORT BY", Sort, nil)
}

// ClusterBy mirrors Select.cluster_by.
func (e *Expr) ClusterBy(args ...any) *Expr {
	pos, o := splitArgs(args)
	return applyChildListBuilder(pos, e, "cluster", o, "CLUSTER BY", Cluster, nil)
}

// LateralView mirrors Select.lateral.
func (e *Expr) LateralView(args ...any) *Expr {
	pos, o := splitArgs(args)
	return applyListBuilder(pos, e, "laterals", o, "LATERAL VIEW", Lateral)
}

// JoinOptions are the optional arguments of Join.
type JoinOptions struct {
	On        any // ExpOrStr or []any
	Using     any // ExpOrStr or []any
	JoinType  string
	JoinAlias any
}

// Join mirrors Select.join(expression, on=..., using=..., join_type=..., join_alias=...).
func (e *Expr) Join(expression any, jo JoinOptions, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	parse := func(v any, prefix string, into ...Kind) (*Expr, error) {
		return MaybeParse(v, o.parseOptions(into...), prefix, false)
	}
	ex, err := parse(expression, "JOIN", Join)
	if err != nil {
		ex, err = parse(expression, "", Join, AnyExpression)
		if err != nil {
			panic(err)
		}
	}
	join := ex
	if !ex.Is(Join) {
		join = New(Join, "this", ex)
	}
	if join.This().Is(Select) {
		join.This().Replace(join.This().Subquery(nil, true))
	}
	if jo.JoinType != "" {
		nj, err := parse(fmt.Sprintf("FROM _ %s JOIN _", jo.JoinType), "")
		if err != nil {
			panic(err)
		}
		newJoin := nj.Find(Join)
		if m := newJoin.TextUpper("method"); m != "" {
			join.Set("method", m)
		}
		if s := newJoin.TextUpper("side"); s != "" {
			join.Set("side", s)
		}
		if k := newJoin.TextUpper("kind"); k != "" {
			join.Set("kind", k)
		}
	}
	if Truthy(jo.On) {
		ons := ensureAnyList(jo.On)
		join.Set("on", NewAnd(append(ons, WithDialect(o.dialect), WithCopy(o.copy))...))
	}
	if Truthy(jo.Using) {
		usings := ensureAnyList(jo.Using)
		lo := o
		join = applyListBuilder(usings, join, "using", lo, "", Identifier)
	}
	if Truthy(jo.JoinAlias) {
		join.Set("this", NewTableAliasOf(join.This(), jo.JoinAlias, nil))
	}
	return applyListBuilder([]any{join}, e, "joins", o, "", InvalidKind)
}

func ensureAnyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		return toAnys(x)
	case []*Expr:
		return toAnys(x)
	case nil:
		return nil
	}
	return []any{v}
}

// Having mirrors Select.having.
func (e *Expr) Having(args ...any) *Expr {
	pos, o := splitArgs(args)
	return applyConjunctionBuilder(pos, e, "having", Having, o)
}

// Window mirrors Select.window.
func (e *Expr) Window(args ...any) *Expr {
	pos, o := splitArgs(args)
	return applyListBuilder(pos, e, "windows", o, "", Window)
}

// Qualify mirrors Select.qualify.
func (e *Expr) Qualify(args ...any) *Expr {
	pos, o := splitArgs(args)
	return applyConjunctionBuilder(pos, e, "qualify", Qualify, o)
}

// Distinct mirrors Select.distinct(*ons, distinct=True).
func (e *Expr) Distinct(args ...any) *Expr {
	pos, o := splitArgs(args)
	inst := MaybeCopy(e, o.copy)
	var on *Expr
	if len(pos) > 0 {
		var ons []*Expr
		for _, x := range pos {
			if Truthy(x) {
				ons = append(ons, MustMaybeParse(x, ParseOptions{ErrorLevel: -1}, "", o.copy))
			}
		}
		on = New(Tuple, "expressions", ons)
	}
	if o.distinct {
		inst.Set("distinct", New(Distinct, "on", on))
	} else {
		inst.Set("distinct", nil)
	}
	return inst
}

// Ctas mirrors Select.ctas(table, properties).
func (e *Expr) Ctas(table any, properties OrderedMap, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	inst := MaybeCopy(e, o.copy)
	tableExpr := MustMaybeParse(table, o.parseOptions(Table), "", false)
	var props *Expr
	if len(properties) > 0 {
		props = PropertiesFromDict(properties)
	}
	return New(Create, "this", tableExpr, "kind", "TABLE", "expression", inst, "properties", props)
}

// Lock mirrors Select.lock(update=True).
func (e *Expr) Lock(update bool, copy bool) *Expr {
	inst := MaybeCopy(e, copy)
	inst.Set("locks", []*Expr{New(Lock, "update", update)})
	return inst
}

// Hint mirrors Select.hint(*hints).
func (e *Expr) Hint(args ...any) *Expr {
	pos, o := splitArgs(args)
	inst := MaybeCopy(e, o.copy)
	var hints []*Expr
	for _, h := range pos {
		hints = append(hints, MustMaybeParse(h, o.parseOptions(), "", o.copy))
	}
	inst.Set("hint", New(Hint, "expressions", hints))
	return inst
}

// Returning mirrors DML.returning(expression).
func (e *Expr) Returning(expression any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	return applyBuilder(expression, e, "returning", o, "RETURNING", Returning, "")
}

// DeleteFrom mirrors Delete.delete(table).
func (e *Expr) DeleteFrom(table any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	return applyBuilder(table, e, "this", o, "", Table, "")
}

// UpdateTable mirrors Update.table(expression).
func (e *Expr) UpdateTable(expression any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	return applyBuilder(expression, e, "this", o, "", Table, "")
}

// UpdateSet mirrors Update.set_(*expressions).
func (e *Expr) UpdateSet(args ...any) *Expr {
	pos, o := splitArgs(args)
	return applyListBuilder(pos, e, "expressions", o, "", InvalidKind)
}

// On mirrors Join.on(*expressions).
func (e *Expr) On(args ...any) *Expr {
	pos, o := splitArgs(args)
	join := applyConjunctionBuilder(pos, e, "on", InvalidKind, o)
	if join.TextUpper("kind") == "CROSS" {
		join.Set("kind", nil)
	}
	return join
}

// Using mirrors Join.using(*expressions).
func (e *Expr) Using(args ...any) *Expr {
	pos, o := splitArgs(args)
	join := applyListBuilder(pos, e, "using", o, "", InvalidKind)
	if join.TextUpper("kind") == "CROSS" {
		join.Set("kind", nil)
	}
	return join
}

// ---------------------------------------------------------------------------------------
// top-level statement builders

// NewSelect mirrors exp.select(*expressions).
func NewSelect(args ...any) *Expr { return New(Select).Select(args...) }

// NewFrom mirrors exp.from_(expression).
func NewFrom(expression any, opts ...Option) *Expr { return New(Select).From(expression, opts...) }

// NewSubqueryFrom mirrors exp.subquery(expression, alias): SELECT ... FROM (expression) AS alias.
func NewSubqueryFrom(expression any, alias any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	expr := MustMaybeParse(expression, o.parseOptions(), "", false).AssertIs(Query).Subquery(alias, o.copy)
	return New(Select).From(expr, WithDialect(o.dialect))
}

// UpdateOptions are the optional arguments of NewUpdate.
type UpdateOptions struct {
	Properties OrderedMap
	Where      any
	From       any
	With       OrderedMap
}

// NewUpdate mirrors exp.update(table, properties, where, from_, with_).
func NewUpdate(table any, uo UpdateOptions, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	u := New(Update, "this", MustMaybeParse(table, o.parseOptions(Table), "", o.copy))
	if len(uo.Properties) > 0 {
		var exprs []*Expr
		for _, kv := range uo.Properties {
			exprs = append(exprs, New(EQ, "this", MustMaybeParse(kv.Key, o.parseOptions(), "", o.copy), "expression", ConvertValue(kv.Value, false)))
		}
		u.Set("expressions", exprs)
	}
	if Truthy(uo.From) {
		u.Set("from_", MustMaybeParse(uo.From, o.parseOptions(From), "FROM", o.copy))
	}
	where := uo.Where
	if w, ok := where.(*Expr); ok && w != nil && w.Is(Condition) {
		where = New(Where, "this", w)
	}
	if Truthy(where) {
		u.Set("where", MustMaybeParse(where, o.parseOptions(Where), "WHERE", o.copy))
	}
	if len(uo.With) > 0 {
		var ctes []*Expr
		for _, kv := range uo.With {
			cte := New(CTE, "this", MustMaybeParse(kv.Value, o.parseOptions(), "", o.copy))
			ctes = append(ctes, NewTableAliasOf(cte, kv.Key, nil))
		}
		u.Set("with_", New(With, "expressions", ctes))
	}
	return u
}

// NewDelete mirrors exp.delete(table, where, returning).
func NewDelete(table any, where any, returning any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	inner := append(opts, WithCopy(false))
	d := New(Delete).DeleteFrom(table, inner...)
	if Truthy(where) {
		d = d.Where(append([]any{where}, toAnys(inner)...)...)
	}
	if Truthy(returning) {
		d = d.Returning(returning, inner...)
	}
	_ = o
	return d
}

// InsertOptions are the optional arguments of NewInsert.
type InsertOptions struct {
	Columns   []any
	Overwrite any
	Returning any
}

// NewInsert mirrors exp.insert(expression, into, columns, overwrite, returning).
func NewInsert(expression any, into any, io InsertOptions, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	expr := MustMaybeParse(expression, o.parseOptions(), "", o.copy)
	this := MustMaybeParse(into, o.parseOptions(Table), "", o.copy)
	if len(io.Columns) > 0 {
		var cols []*Expr
		for _, c := range io.Columns {
			cols = append(cols, ToIdentifierOpts(c, nil, o.copy))
		}
		this = New(Schema, "this", this, "expressions", cols)
	}
	ins := New(Insert, "this", this, "expression", expr, "overwrite", io.Overwrite)
	if Truthy(io.Returning) {
		ins = ins.Returning(io.Returning, append(opts, WithCopy(false))...)
	}
	return ins
}

// NewMerge mirrors exp.merge(*when_exprs, into, using, on, returning).
func NewMerge(whens []any, into, using, on any, returning any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	var exprs []*Expr
	for _, w := range whens {
		e := MustMaybeParse(w, o.parseOptions(Whens), "", o.copy)
		if e.Is(When) {
			exprs = append(exprs, e)
		} else {
			exprs = append(exprs, e.Expressions()...)
		}
	}
	m := New(Merge,
		"this", MustMaybeParse(into, o.parseOptions(), "", o.copy),
		"using", MustMaybeParse(using, o.parseOptions(), "", o.copy),
		"on", MustMaybeParse(on, o.parseOptions(), "", o.copy),
		"whens", New(Whens, "expressions", exprs),
	)
	if Truthy(returning) {
		m = m.Returning(returning, append(opts, WithCopy(false))...)
	}
	if u := m.Get("using"); u.Is(Alias) {
		u.Replace(NewTableAliasOf(u.This(), u.Get("alias"), nil))
	}
	return m
}

// ---------------------------------------------------------------------------------------
// operator helpers (Python operator overloads and condition methods)

func (e *Expr) binop(kind Kind, other any, reverse bool) *Expr {
	this := e.Copy()
	o := ConvertValue(other, true)
	if !this.Is(kind) && !o.Is(kind) {
		this = wrap(this, Binary)
		o = wrap(o, Binary)
	}
	if reverse {
		return New(kind, "this", o, "expression", this)
	}
	return New(kind, "this", this, "expression", o)
}

// And mirrors Expression.and_(*expressions).
func (e *Expr) And(args ...any) *Expr { return NewAnd(append([]any{e}, args...)...) }

// Or mirrors Expression.or_(*expressions).
func (e *Expr) Or(args ...any) *Expr { return NewOr(append([]any{e}, args...)...) }

// Not mirrors Expression.not_() / ~expr.
func (e *Expr) Not(opts ...Option) *Expr { return NewNot(e, opts...) }

// As mirrors Expression.as_(alias).
func (e *Expr) As(alias any, opts ...Option) *Expr { return NewAlias(e, alias, opts...) }

// AsTable mirrors Expression.as_(alias, table=True|columns).
func (e *Expr) AsTable(alias any, columns []any, opts ...Option) *Expr {
	return NewTableAliasOf(e, alias, columns, opts...)
}

// IsIn mirrors Expression.isin(*expressions, query=None, unnest=None).
func (e *Expr) IsIn(args ...any) *Expr {
	pos, o := splitArgs(args)
	var query, unnest any
	if o.extra != nil {
		query, unnest = o.extra["query"], o.extra["unnest"]
	}
	var subquery *Expr
	if Truthy(query) {
		subquery = MustMaybeParse(query, o.parseOptions(), "", o.copy)
		if subquery.Is(Query) && implIsin[e.kind] != Tuple {
			subquery = subquery.Subquery(nil, false)
		}
	}
	exprs := make([]*Expr, len(pos))
	for i, x := range pos {
		exprs[i] = ConvertValue(x, o.copy)
	}
	var un *Expr
	if Truthy(unnest) {
		var us []*Expr
		for _, u := range ensureAnyList(unnest) {
			us = append(us, MustMaybeParse(u, o.parseOptions(), "", o.copy))
		}
		un = New(Unnest, "expressions", us)
	}
	return New(In, "this", MaybeCopy(e, o.copy), "expressions", exprs, "query", subquery, "unnest", un)
}

// Between mirrors Expression.between(low, high, symmetric=None).
func (e *Expr) Between(low, high any, symmetric any) *Expr {
	b := New(Between, "this", e.Copy(), "low", ConvertValue(low, true), "high", ConvertValue(high, true))
	if symmetric != nil {
		b.Set("symmetric", symmetric)
	}
	return b
}

// IsOp mirrors Expression.is_(other).
func (e *Expr) IsOp(other any) *Expr { return e.binop(Is, other, false) }

// Like mirrors Expression.like(other).
func (e *Expr) Like(other any) *Expr { return e.binop(Like, other, false) }

// ILike mirrors Expression.ilike(other).
func (e *Expr) ILike(other any) *Expr { return e.binop(ILike, other, false) }

// RLike mirrors Expression.rlike(other).
func (e *Expr) RLike(other any) *Expr { return e.binop(RegexpLike, other, false) }

// Eq mirrors Expression.eq(other).
func (e *Expr) Eq(other any) *Expr { return e.binop(EQ, other, false) }

// Neq mirrors Expression.neq(other).
func (e *Expr) Neq(other any) *Expr { return e.binop(NEQ, other, false) }

// Lt mirrors expr < other.
func (e *Expr) Lt(other any) *Expr { return e.binop(LT, other, false) }

// Lte mirrors expr <= other.
func (e *Expr) Lte(other any) *Expr { return e.binop(LTE, other, false) }

// Gt mirrors expr > other.
func (e *Expr) Gt(other any) *Expr { return e.binop(GT, other, false) }

// Gte mirrors expr >= other.
func (e *Expr) Gte(other any) *Expr { return e.binop(GTE, other, false) }

// Plus mirrors expr + other.
func (e *Expr) Plus(other any) *Expr { return e.binop(Add, other, false) }

// RPlus mirrors other + expr.
func (e *Expr) RPlus(other any) *Expr { return e.binop(Add, other, true) }

// Minus mirrors expr - other.
func (e *Expr) Minus(other any) *Expr { return e.binop(Sub, other, false) }

// RMinus mirrors other - expr.
func (e *Expr) RMinus(other any) *Expr { return e.binop(Sub, other, true) }

// Times mirrors expr * other.
func (e *Expr) Times(other any) *Expr { return e.binop(Mul, other, false) }

// RTimes mirrors other * expr.
func (e *Expr) RTimes(other any) *Expr { return e.binop(Mul, other, true) }

// Over mirrors expr / other (Python __truediv__).
func (e *Expr) Over(other any) *Expr { return e.binop(Div, other, false) }

// ROver mirrors other / expr.
func (e *Expr) ROver(other any) *Expr { return e.binop(Div, other, true) }

// DivOp mirrors Expression.div(other, typed, safe).
func (e *Expr) DivOp(other any, typed, safe bool) *Expr {
	d := e.binop(Div, other, false)
	d.Set("typed", typed)
	d.Set("safe", safe)
	return d
}

// FloorDiv mirrors expr // other.
func (e *Expr) FloorDiv(other any) *Expr { return e.binop(IntDiv, other, false) }

// Modulo mirrors expr % other.
func (e *Expr) Modulo(other any) *Expr { return e.binop(Mod, other, false) }

// Power mirrors expr ** other.
func (e *Expr) Power(other any) *Expr { return e.binop(Pow, other, false) }

// BitAnd mirrors expr & other (logical AND in sqlglot).
func (e *Expr) BitAnd(other any) *Expr { return e.binop(And, other, false) }

// BitOr mirrors expr | other (logical OR in sqlglot).
func (e *Expr) BitOr(other any) *Expr { return e.binop(Or, other, false) }

// Negate mirrors -expr.
func (e *Expr) Negate() *Expr { return New(Neg, "this", wrap(e.Copy(), Binary)) }

// Asc mirrors Expression.asc(nulls_first=True).
func (e *Expr) Asc(nullsFirst bool) *Expr {
	return New(Ordered, "this", e.Copy(), "nulls_first", nullsFirst)
}

// Desc mirrors Expression.desc(nulls_first=False).
func (e *Expr) Desc(nullsFirst bool) *Expr {
	return New(Ordered, "this", e.Copy(), "desc", true, "nulls_first", nullsFirst)
}

// Bracket mirrors expr[other].
func (e *Expr) Bracket(others ...any) *Expr {
	exprs := make([]*Expr, len(others))
	for i, o := range others {
		exprs[i] = ConvertValue(o, true)
	}
	return New(Bracket, "this", e.Copy(), "expressions", exprs)
}
