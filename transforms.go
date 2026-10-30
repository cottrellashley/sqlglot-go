package sqlglot

// Port of sqlglot/transforms.py: AST rewrites used by dialect generators via Preprocess.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// Transform is an AST transformation (a callable taking and returning an expression).
type Transform func(e *exp.Expr) *exp.Expr

// unsupportedTransform aborts a transform with an UnsupportedError (caught by Preprocess).
func unsupportedTransform(msg string) {
	panic(&errs.UnsupportedError{Message: msg})
}

// Preprocess mirrors transforms.preprocess(transforms, generator=None): it creates a generator
// handler that applies the transforms in sequence before generating SQL.
func Preprocess(transforms []Transform, generator GenFunc) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		expressionType := e.Kind()
		func() {
			defer func() {
				if r := recover(); r != nil {
					if ue, ok := r.(*errs.UnsupportedError); ok {
						g.Unsupported(ue.Error())
						return
					}
					panic(r)
				}
			}()
			for _, t := range transforms {
				e = t(e)
			}
		}()

		if generator != nil {
			return generator(g, e)
		}
		if h := g.C.SQLMethods[e.Kind()]; h != nil {
			return h(g, e)
		}
		if g.C.Transforms[e.Kind()] != nil {
			if expressionType == e.Kind() {
				if e.Is(exp.Func) {
					return g.FunctionFallbackSQL(e)
				}
				// Ensures we don't enter an infinite loop. This can happen when the original expression
				// has the same type as the final expression and there's no _sql method available for it,
				// because then it'd re-enter _to_sql.
				g.Raisef("Expr type %s requires a _sql method in order to be transformed.", e.ClassName())
			}
			return g.C.Transforms[e.Kind()](g, e)
		}
		g.Raisef("Unsupported expression type %s.", e.ClassName())
		return ""
	}
}

// UnnestGenerateDateArrayUsingRecursiveCte mirrors
// transforms.unnest_generate_date_array_using_recursive_cte.
func UnnestGenerateDateArrayUsingRecursiveCte(e *exp.Expr) *exp.Expr {
	if !e.IsKind(exp.Select) {
		return e
	}
	count := 0
	var recursiveCtes []*exp.Expr
	for _, unnest := range e.FindAllList(exp.Unnest) {
		if !unnest.Parent().IsAny(exp.From, exp.Join) || len(unnest.Expressions()) != 1 ||
			!unnest.Expressions()[0].IsKind(exp.GenerateDateArray) {
			continue
		}
		gda := unnest.Expressions()[0]
		start, end, step := gda.Get("start"), gda.Get("end"), gda.Get("step")
		if start == nil || end == nil || !step.IsKind(exp.Interval) {
			continue
		}
		var columnName any = "date_value"
		if alias := unnest.Get("alias"); alias.IsKind(exp.TableAlias) {
			columnName = alias.Columns()[0]
		}
		start = exp.NewCast(start, "date")
		dateAdd := exp.NewFunc("date_add", columnName, exp.LiteralNumber(step.Name()), step.Arg("unit"))
		castDateAdd := exp.NewCast(dateAdd, "date")
		cteName := "_generated_dates" + iff(count != 0, fmt.Sprintf("_%d", count), "")
		baseQuery := exp.NewSelect(start.As(columnName))
		recursiveQuery := exp.NewSelect(castDateAdd).From(cteName).Where(castDateAdd.Lte(exp.NewCast(end, "date")))
		cteQuery := baseQuery.Union(recursiveQuery, exp.WithDistinct(false))
		generateDatesQuery := exp.NewSelect(columnName).From(cteName)
		unnest.Replace(generateDatesQuery.Subquery(cteName, true))
		recursiveCtes = append(recursiveCtes, exp.NewTableAliasOf(exp.New(exp.CTE, "this", cteQuery), cteName, []any{columnName}))
		count++
	}
	if len(recursiveCtes) > 0 {
		with := e.Get("with_")
		if with == nil {
			with = exp.New(exp.With)
		}
		with.Set("recursive", true)
		with.Set("expressions", concatExprs(recursiveCtes, with.Expressions()))
		e.Set("with_", with)
	}
	return e
}

// UnnestGenerateSeries mirrors transforms.unnest_generate_series: it unnests GENERATE_SERIES
// or SEQUENCE table references.
func UnnestGenerateSeries(e *exp.Expr) *exp.Expr {
	this := e.This()
	if e.IsKind(exp.Table) && this.IsKind(exp.GenerateSeries) {
		unnest := exp.New(exp.Unnest, "expressions", []*exp.Expr{this})
		if alias := e.Alias(); alias != "" {
			return exp.NewTableAliasOf(unnest, "_u", []any{alias}, exp.WithCopy(false))
		}
		return unnest
	}
	return e
}

// EliminateDistinctOn mirrors transforms.eliminate_distinct_on: DISTINCT ON is converted to
// a subquery with a ROW_NUMBER() window function.
func EliminateDistinctOn(e *exp.Expr) *exp.Expr {
	if !(e.IsKind(exp.Select) && e.Bool("distinct") && e.Get("distinct").Get("on").IsKind(exp.Tuple)) {
		return e
	}
	rowNumberWindowAlias := helper.FindNewName(containsFn(e.NamedSelects()), "_row_number")
	distinctCols := e.Get("distinct").Pop().Get("on").Expressions()
	window := exp.New(exp.Window, "this", exp.New(exp.RowNumber), "partition_by", distinctCols)
	if order := e.Get("order"); order != nil {
		window.Set("order", order.Pop())
	} else {
		var cols []*exp.Expr
		for _, c := range distinctCols {
			cols = append(cols, c.Copy())
		}
		window.Set("order", exp.New(exp.Order, "expressions", cols))
	}
	e.Select(exp.NewAlias(window, rowNumberWindowAlias), exp.WithCopy(false))

	// We add aliases to the projections so that we can safely reference them in the outer query
	var newSelects []any
	taken := map[string]bool{rowNumberWindowAlias: true}
	selects := e.Selects()
	for _, sel := range selects[:len(selects)-1] {
		if sel.IsStar() {
			newSelects = []any{exp.New(exp.Star)}
			break
		}
		if !sel.IsKind(exp.Alias) {
			name := sel.OutputName()
			if name == "" {
				name = "_col"
			}
			alias := helper.FindNewNameIn(taken, name)
			var quoted *bool
			if sel.IsKind(exp.Column) {
				if q, ok := sel.This().Arg("quoted").(bool); ok {
					quoted = &q
				}
			}
			opts := []exp.Option{}
			if quoted != nil {
				opts = append(opts, exp.WithQuoted(*quoted))
			}
			sel = sel.Replace(exp.NewAlias(sel, alias, opts...))
		}
		taken[sel.OutputName()] = true
		newSelects = append(newSelects, sel.Get("alias"))
	}
	return exp.NewSelect(append(newSelects, exp.WithCopy(false))...).
		From(e.Subquery("_t", false), exp.WithCopy(false)).
		Where(exp.NewColumn(rowNumberWindowAlias).Eq(1), exp.WithCopy(false))
}

func containsFn(xs []string) func(string) bool {
	return func(s string) bool { return slices.Contains(xs, s) }
}

// EliminateQualify mirrors transforms.eliminate_qualify: QUALIFY is converted to a subquery
// with a WHERE clause.
func EliminateQualify(e *exp.Expr) *exp.Expr {
	if !(e.IsKind(exp.Select) && e.Bool("qualify")) {
		return e
	}
	if parent := e.Parent(); parent.IsKind(exp.Insert) && parent.Parent().IsKind(exp.MultitableInserts) {
		// A FROM-first multi-table INSERT branch shares its parent's FROM, so it can't be wrapped
		unsupportedTransform("QUALIFY is not supported in a FROM-first multi-table INSERT branch")
	}
	taken := map[string]bool{}
	for _, n := range e.NamedSelects() {
		taken[n] = true
	}
	for _, sel := range e.Selects() {
		if sel.AliasOrName() == "" {
			alias := helper.FindNewNameIn(taken, "_c")
			sel.Replace(exp.NewAlias(sel, alias))
			taken[alias] = true
		}
	}
	selectAliasOrName := func(sel *exp.Expr) any {
		aliasOrName := sel.AliasOrName()
		identifier := sel.Get("alias")
		if identifier == nil {
			identifier = sel.This()
		}
		if identifier.IsKind(exp.Identifier) {
			if q, ok := identifier.Arg("quoted").(bool); ok {
				return exp.NewColumnFields(aliasOrName, nil, nil, nil, nil, &q, true)
			}
			return exp.NewColumnFields(aliasOrName, nil, nil, nil, nil, nil, true)
		}
		return aliasOrName
	}
	var outer []any
	for _, sel := range e.Selects() {
		outer = append(outer, selectAliasOrName(sel))
	}
	outerSelects := exp.NewSelect(outer...)
	qualifyFilters := e.Get("qualify").Pop().This()
	expressionByAlias := map[string]*exp.Expr{}
	for _, sel := range e.Selects() {
		if sel.IsKind(exp.Alias) {
			expressionByAlias[sel.Alias()] = sel.This()
		}
	}
	candidates := []exp.Kind{exp.Window}
	if !e.IsStar() {
		candidates = append(candidates, exp.Column)
	}
	for _, candidate := range qualifyFilters.FindAllList(candidates...) {
		if candidate.IsKind(exp.Window) {
			if len(expressionByAlias) > 0 {
				for _, column := range candidate.FindAllList(exp.Column) {
					if x, ok := expressionByAlias[column.Name()]; ok && x != nil {
						column.Replace(x)
					}
				}
			}
			alias := helper.FindNewName(containsFn(e.NamedSelects()), "_w")
			e.Select(exp.NewAlias(candidate, alias), exp.WithCopy(false))
			column := exp.NewColumn(alias)
			if candidate.Parent().IsKind(exp.Qualify) {
				qualifyFilters = column
			} else {
				candidate.Replace(column)
			}
		} else if !slices.Contains(e.NamedSelects(), candidate.Name()) && candidate.FindAncestor(exp.Window) == nil {
			// A column that is only read by a window function is computed by the window
			// itself, so projecting it in the subquery is redundant and can even produce
			// invalid SQL, e.g. when the query is grouped
			e.Select(candidate.Copy(), exp.WithCopy(false))
		}
	}
	return outerSelects.From(e.Subquery("_t", false), exp.WithCopy(false)).Where(qualifyFilters, exp.WithCopy(false))
}

// RemovePrecisionParameterizedTypes mirrors transforms.remove_precision_parameterized_types.
func RemovePrecisionParameterizedTypes(e *exp.Expr) *exp.Expr {
	for _, node := range e.FindAllList(exp.DataType) {
		var keep []*exp.Expr
		for _, x := range node.Expressions() {
			if !x.IsKind(exp.DataTypeParam) {
				keep = append(keep, x)
			}
		}
		node.Set("expressions", keep)
	}
	return e
}

// UnqualifyUnnest mirrors transforms.unqualify_unnest: it removes references to unnest table
// aliases (BigQuery doesn't allow them).
func UnqualifyUnnest(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Select) {
		aliases := map[string]bool{}
		for unnest := range FindAllInScope(e, exp.Unnest) {
			if unnest.Parent().IsAny(exp.From, exp.Join) {
				aliases[unnest.Alias()] = true
			}
		}
		if len(aliases) > 0 {
			for _, column := range e.FindAllList(exp.Column) {
				leftmost := column.Parts()[0]
				if leftmost.ArgKey() != "this" && aliases[leftmost.ThisStr()] {
					leftmost.Pop()
				}
			}
		}
	}
	return e
}

// UnnestToExplode mirrors transforms.unnest_to_explode(expression, unnest_using_arrays_zip=True).
func UnnestToExplode(unnestUsingArraysZip bool) Transform {
	unnestZipExprs := func(u *exp.Expr, exprs []*exp.Expr, hasMulti bool) []*exp.Expr {
		if hasMulti {
			if !unnestUsingArraysZip {
				unsupportedTransform("Cannot transpile UNNEST with multiple input arrays")
			}
			// Use INLINE(ARRAYS_ZIP(...)) for multiple expressions
			zip := []*exp.Expr{exp.New(exp.Anonymous, "this", "ARRAYS_ZIP", "expressions", exprs)}
			u.Set("expressions", zip)
			return zip
		}
		return exprs
	}
	udtfType := func(u *exp.Expr, hasMulti bool) exp.Kind {
		if u.Bool("offset") {
			return exp.Posexplode
		}
		return iff(hasMulti, exp.Inline, exp.Explode)
	}
	offsetIdentifier := func(offset *exp.Expr) *exp.Expr {
		if offset.IsKind(exp.Identifier) {
			return offset
		}
		return exp.ToIdentifier("pos")
	}
	return func(e *exp.Expr) *exp.Expr {
		if !e.IsKind(exp.Select) {
			return e
		}
		if from := e.Get("from_"); from != nil && from.This().IsKind(exp.Unnest) {
			unnest := from.This()
			alias := unnest.Get("alias")
			exprs := unnest.Expressions()
			hasMulti := len(exprs) > 1
			this := unnestZipExprs(unnest, exprs, hasMulti)[0]
			var columns []*exp.Expr
			if alias != nil {
				columns = alias.Columns()
			}
			if offset := unnest.Arg("offset"); exp.Truthy(offset) {
				off, _ := offset.(*exp.Expr)
				columns = append([]*exp.Expr{offsetIdentifier(off)}, columns...)
			}
			var tableAlias any
			if alias != nil {
				tableAlias = exp.New(exp.TableAlias, "this", alias.This(), "columns", columns)
			}
			unnest.Replace(exp.New(exp.Table, "this", exp.New(udtfType(unnest, hasMulti), "this", this), "alias", tableAlias))
		}

		joins := e.List("joins")
		for _, join := range slices.Clone(joins) {
			joinExpr := join.This()
			isLateral := joinExpr.IsKind(exp.Lateral)
			unnest := joinExpr
			if isLateral {
				unnest = joinExpr.This()
			}
			if !unnest.IsKind(exp.Unnest) {
				continue
			}
			var alias *exp.Expr
			if isLateral {
				alias = joinExpr.Get("alias")
			} else {
				alias = unnest.Get("alias")
			}
			if alias == nil {
				unsupportedTransform("CROSS JOIN UNNEST to LATERAL VIEW EXPLODE transformation requires an alias")
			}
			exprs := unnest.Expressions()
			// The number of unnest.expressions will be changed by _unnest_zip_exprs, we need to record it here
			hasMulti := len(exprs) > 1
			exprs = unnestZipExprs(unnest, exprs, hasMulti)

			joins = slices.DeleteFunc(joins, func(j *exp.Expr) bool { return j == join })
			e.SetRaw("joins", joins)

			aliasCols := alias.Columns()
			// Spark LATERAL VIEW EXPLODE requires single alias for array/struct and two for Map type column
			if !hasMulti && len(aliasCols) != 1 && len(aliasCols) != 2 {
				unsupportedTransform("CROSS JOIN UNNEST to LATERAL VIEW EXPLODE transformation requires explicit column aliases")
			}
			if offset := unnest.Arg("offset"); exp.Truthy(offset) {
				off, _ := offset.(*exp.Expr)
				aliasCols = append([]*exp.Expr{offsetIdentifier(off)}, aliasCols...)
				alias.SetRaw("columns", aliasCols)
			}
			for i := 0; i < len(exprs) && i < len(aliasCols); i++ {
				e.Append("laterals", exp.New(exp.Lateral,
					"this", exp.New(udtfType(unnest, hasMulti), "this", exprs[i]),
					"view", true,
					"alias", exp.New(exp.TableAlias, "this", alias.This(), "columns", aliasCols),
				))
			}
		}
		return e
	}
}

// ExplodeProjectionToUnnest mirrors transforms.explode_projection_to_unnest(index_offset=0,
// unnest_map=False): it converts explode/posexplode projections into unnests.
func ExplodeProjectionToUnnest(indexOffset int, unnestMap bool) Transform {
	return func(e *exp.Expr) *exp.Expr {
		if !e.IsKind(exp.Select) {
			return e
		}
		takenSelectNames := map[string]bool{}
		for _, n := range e.NamedSelects() {
			takenSelectNames[n] = true
		}
		takenSourceNames := map[string]bool{}
		for _, ref := range NewScope(e, ScopeOptions{}).References() {
			takenSourceNames[ref.Name] = true
		}
		newName := func(names map[string]bool, name string) string {
			name = helper.FindNewNameIn(names, name)
			names[name] = true
			return name
		}

		var arrays []*exp.Expr
		seriesAlias := newName(takenSelectNames, "pos")
		series := exp.NewTableAliasOf(
			exp.New(exp.Unnest, "expressions", []*exp.Expr{exp.New(exp.GenerateSeries, "start", exp.LiteralNumber(indexOffset))}),
			newName(takenSourceNames, "_u"),
			[]any{seriesAlias},
		)

		// we use a copy here because expression.selects is mutated inside the loop
		for _, sel := range slices.Clone(e.Selects()) {
			explode := FindInScope(sel, exp.Explode)
			if explode == nil {
				continue
			}
			if unnestMap && explode.IsKind(exp.Explode) && explode.This().IsType(exp.DTypeMap) &&
				(sel == explode || sel.IsKind(exp.Aliases)) {
				var mapKeyAlias, mapValueAlias any
				if sel.IsKind(exp.Aliases) {
					aliases := sel.Expressions()
					mapKeyAlias, mapValueAlias = aliases[0], aliases[1]
				} else {
					mapKeyAlias = newName(takenSelectNames, "key")
					mapValueAlias = newName(takenSelectNames, "value")
				}
				mapUnnestSource := newName(takenSourceNames, "_u")
				mapKeySelect := sel.Replace(exp.NewColumnFields(mapKeyAlias, mapUnnestSource, nil, nil, nil, nil, true).As(mapKeyAlias))
				expressions := e.Expressions()
				idx := slices.Index(expressions, mapKeySelect)
				expressions = slices.Insert(slices.Clone(expressions), idx+1,
					exp.NewColumnFields(mapValueAlias, mapUnnestSource, nil, nil, nil, nil, true).As(mapValueAlias))
				e.Set("expressions", expressions)
				unnest := exp.NewTableAliasOf(exp.New(exp.Unnest, "expressions", []*exp.Expr{explode.This().Copy()}),
					mapUnnestSource, []any{mapKeyAlias, mapValueAlias})
				if e.Bool("from_") {
					e.Join(unnest, exp.JoinOptions{JoinType: "CROSS"}, exp.WithCopy(false))
				} else {
					e.From(unnest, exp.WithCopy(false))
				}
				continue
			}

			var posAlias, explodeAlias any = "", ""
			var alias *exp.Expr
			switch {
			case sel.IsKind(exp.Alias):
				explodeAlias = sel.Get("alias")
				alias = sel
			case sel.IsKind(exp.Aliases):
				posAlias = sel.Expressions()[0]
				explodeAlias = sel.Expressions()[1]
				alias = sel.Replace(exp.NewAlias(sel.This(), "", exp.WithCopy(false)))
			default:
				alias = sel.Replace(exp.NewAlias(sel, ""))
				explode = FindInScope(alias, exp.Explode)
			}

			isPosexplode := explode.IsKind(exp.Posexplode)
			explodeArg := explode.This()

			if explode.IsKind(exp.ExplodeOuter) {
				bracket := explodeArg.Bracket(0)
				bracket.Set("safe", true)
				bracket.Set("offset", true)
				explodeArg = exp.NewFunc("IF",
					exp.NewFunc("ARRAY_SIZE", exp.NewFunc("COALESCE", explodeArg, exp.New(exp.Array))).Eq(0),
					exp.NewArray(bracket, exp.WithCopy(false)),
					explodeArg)
			}

			// This ensures that we won't use [POS]EXPLODE's argument as a new selection
			if explodeArg.IsKind(exp.Column) {
				takenSelectNames[explodeArg.OutputName()] = true
			}

			unnestSourceAlias := newName(takenSourceNames, "_u")

			if !exp.Truthy(explodeAlias) {
				explodeAlias = newName(takenSelectNames, "col")
				if isPosexplode {
					posAlias = newName(takenSelectNames, "pos")
				}
			}
			if !exp.Truthy(posAlias) {
				posAlias = newName(takenSelectNames, "pos")
			}

			alias.Set("alias", exp.ToIdentifier(explodeAlias))

			seriesTableAlias := series.Get("alias").This()
			col := func(name any, table any) *exp.Expr {
				return exp.NewColumnFields(name, table, nil, nil, nil, nil, true)
			}
			column := exp.New(exp.If,
				"this", col(seriesAlias, seriesTableAlias).Eq(col(posAlias, unnestSourceAlias)),
				"true", col(explodeAlias, unnestSourceAlias))
			explode.Replace(column)

			if isPosexplode {
				expressions := e.Expressions()
				idx := slices.Index(expressions, alias)
				expressions = slices.Insert(slices.Clone(expressions), idx+1,
					exp.New(exp.If,
						"this", col(seriesAlias, seriesTableAlias).Eq(col(posAlias, unnestSourceAlias)),
						"true", col(posAlias, unnestSourceAlias)).As(posAlias))
				e.Set("expressions", expressions)
			}

			if len(arrays) == 0 {
				if e.Bool("from_") {
					e.Join(series, exp.JoinOptions{JoinType: "CROSS"}, exp.WithCopy(false))
				} else {
					e.From(series, exp.WithCopy(false))
				}
			}

			size := exp.New(exp.ArraySize, "this", explodeArg.Copy())
			arrays = append(arrays, size)

			// trino doesn't support left join unnest with on conditions
			// if it did, this would be much simpler
			e.Join(exp.NewTableAliasOf(
				exp.New(exp.Unnest, "expressions", []*exp.Expr{explodeArg.Copy()}, "offset", exp.ToIdentifier(posAlias)),
				unnestSourceAlias, []any{explodeAlias}),
				exp.JoinOptions{JoinType: "CROSS"}, exp.WithCopy(false))

			if indexOffset != 1 {
				size = size.Minus(1)
			}

			e.Where(
				col(seriesAlias, seriesTableAlias).Eq(col(posAlias, unnestSourceAlias)).Or(
					col(seriesAlias, seriesTableAlias).Gt(size).And(col(posAlias, unnestSourceAlias).Eq(size)),
				),
				exp.WithCopy(false),
			)
		}

		if len(arrays) > 0 {
			end := exp.New(exp.Greatest, "this", arrays[0], "expressions", arrays[1:])
			if indexOffset != 1 {
				end = end.Minus(1 - indexOffset)
			}
			series.Expressions()[0].Set("end", end)
		}
		return e
	}
}

// AddWithinGroupForPercentiles mirrors transforms.add_within_group_for_percentiles: it
// transforms percentiles by adding a WITHIN GROUP clause to them.
func AddWithinGroupForPercentiles(e *exp.Expr) *exp.Expr {
	if isPercentile(e) && !e.Parent().IsKind(exp.WithinGroup) && e.Expression() != nil {
		column := e.This().Pop()
		e.Set("this", e.Expression().Pop())
		order := exp.New(exp.Order, "expressions", []*exp.Expr{exp.New(exp.Ordered, "this", column)})
		e = exp.New(exp.WithinGroup, "this", e, "expression", order)
	}
	return e
}

// RemoveWithinGroupForPercentiles mirrors transforms.remove_within_group_for_percentiles.
func RemoveWithinGroupForPercentiles(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.WithinGroup) && isPercentile(e.This()) && e.Expression().IsKind(exp.Order) {
		quantile := e.This().This()
		inputValue := e.Find(exp.Ordered).This()
		return e.Replace(exp.New(exp.ApproxQuantile, "this", inputValue, "quantile", quantile))
	}
	return e
}

// AddRecursiveCteColumnNames mirrors transforms.add_recursive_cte_column_names: it uses column
// aliases if the CTE is recursive (Spark).
func AddRecursiveCteColumnNames(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.With) && e.Recursive() {
		nextName := helper.NameSequence("_c_")
		for _, cte := range e.Expressions() {
			if len(cte.Get("alias").Columns()) == 0 {
				query := cte.This()
				if query.Is(exp.SetOperation) {
					query = query.This()
				}
				var cols []*exp.Expr
				for _, s := range query.Selects() {
					name := s.AliasOrName()
					if name == "" {
						name = nextName()
					}
					cols = append(cols, exp.ToIdentifier(name))
				}
				cte.Get("alias").Set("columns", cols)
			}
		}
	}
	return e
}

// EpochCastToTs mirrors transforms.epoch_cast_to_ts: it replaces 'epoch' casts to temporal
// types with a timestamp literal.
func EpochCastToTs(e *exp.Expr) *exp.Expr {
	if e.IsAny(exp.Cast, exp.TryCast) && strings.ToLower(e.Name()) == "epoch" &&
		exp.DataTypeTemporalTypes.Has(e.To().DataTypeThis()) {
		e.This().Replace(exp.LiteralString("1970-01-01 00:00:00"))
	}
	return e
}

// EliminateSemiAndAntiJoins mirrors transforms.eliminate_semi_and_anti_joins: SEMI and ANTI
// joins are converted to (NOT) EXISTS filters.
func EliminateSemiAndAntiJoins(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Select) {
		for _, join := range slices.Clone(e.List("joins")) {
			on := join.Get("on")
			kind := join.TextUpper("kind")
			if on != nil && (kind == "SEMI" || kind == "ANTI") {
				subquery := exp.NewSelect("1").From(join.This()).Where(on)
				exists := exp.New(exp.Exists, "this", subquery)
				if kind == "ANTI" {
					exists = exists.Not(exp.WithCopy(false))
				}
				join.Pop()
				e.Where(exists, exp.WithCopy(false))
			}
		}
	}
	return e
}

// EliminateFullOuterJoin mirrors transforms.eliminate_full_outer_join: a FULL OUTER JOIN is
// converted to a union of a LEFT and a RIGHT join.
func EliminateFullOuterJoin(e *exp.Expr) *exp.Expr {
	if !e.IsKind(exp.Select) {
		return e
	}
	type fj struct {
		index int
		join  *exp.Expr
	}
	var fulls []fj
	for i, join := range e.List("joins") {
		if join.TextUpper("side") == "FULL" {
			fulls = append(fulls, fj{i, join})
		}
	}
	if len(fulls) != 1 {
		return e
	}
	exprCopy := e.Copy()
	index, fullOuterJoin := fulls[0].index, fulls[0].join
	tables := [2]string{e.Get("from_").AliasOrName(), fullOuterJoin.AliasOrName()}
	joinConditions := fullOuterJoin.Get("on")
	if joinConditions == nil {
		var conds []any
		for _, col := range fullOuterJoin.List("using") {
			conds = append(conds, exp.NewColumn(col, tables[0]).Eq(exp.NewColumn(col, tables[1])))
		}
		joinConditions = exp.NewAnd(conds...)
	}
	fullOuterJoin.Set("side", "left")
	antiJoinClause := exp.NewSelect("1").From(e.Get("from_")).Where(joinConditions)
	exprCopy.List("joins")[index].Set("side", "right")
	exprCopy = exprCopy.Where(exp.New(exp.Exists, "this", antiJoinClause).Not())
	union := exp.NewUnion(e, exprCopy, exp.WithCopy(false), exp.WithDistinct(false))
	for _, arg := range []string{"with_", "order", "limit", "offset"} {
		if v := e.Arg(arg); exp.Truthy(v) {
			e.Set(arg, nil)
			exprCopy.Set(arg, nil)
			union.Set(arg, v)
		}
	}
	return union
}

// MoveCtesToTopLevel mirrors transforms.move_ctes_to_top_level: nested WITH clauses are
// moved to the top level (some dialects only allow CTEs at the top).
func MoveCtesToTopLevel(e *exp.Expr) *exp.Expr {
	topLevelWith := e.Get("with_")
	for _, innerWith := range e.FindAllList(exp.With) {
		if innerWith.Parent() == e {
			continue
		}
		if topLevelWith == nil {
			topLevelWith = innerWith.Pop()
			e.Set("with_", topLevelWith)
			continue
		}
		if innerWith.Recursive() {
			topLevelWith.Set("recursive", true)
		}
		parentCte := innerWith.FindAncestor(exp.CTE)
		innerWith.Pop()
		if parentCte != nil {
			exprs := topLevelWith.Expressions()
			i := slices.IndexFunc(exprs, func(x *exp.Expr) bool { return x.Equal(parentCte) })
			merged := slices.Insert(slices.Clone(exprs), i, innerWith.Expressions()...)
			topLevelWith.Set("expressions", merged)
		} else {
			topLevelWith.Set("expressions", concatExprs(topLevelWith.Expressions(), innerWith.Expressions()))
		}
	}
	return e
}

// EnsureBools mirrors transforms.ensure_bools: it converts numeric values used in conditions
// into explicit boolean expressions.
func EnsureBools(e *exp.Expr) *exp.Expr {
	ensureBool := func(node *exp.Expr) {
		if node.IsNumber() ||
			(!node.Is(exp.SubqueryPredicate) && node.IsTypeIn(exp.NewTypeSet(exp.DTypeUnknown), exp.DataTypeNumericTypes)) ||
			(node.IsKind(exp.Column) && node.Type() == nil) {
			node.Replace(node.Neq(0))
		}
	}
	for node := range e.Walk(true, nil) {
		CanonicalizeEnsureBools(node, ensureBool)
	}
	return e
}

// UnqualifyColumns mirrors transforms.unqualify_columns.
func UnqualifyColumns(e *exp.Expr) *exp.Expr {
	for _, column := range e.FindAllList(exp.Column) {
		// We only wanna pop off the table, db, catalog args
		parts := column.Parts()
		for _, part := range parts[:len(parts)-1] {
			part.Pop()
		}
	}
	return e
}

// UnqualifyPivotFields mirrors transforms.unqualify_pivot_fields.
func UnqualifyPivotFields(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Pivot) {
		var fields []*exp.Expr
		for _, f := range e.Fields() {
			fields = append(fields, UnqualifyColumns(f))
		}
		e.Set("fields", fields)
	}
	return e
}

// RemoveUniqueConstraints mirrors transforms.remove_unique_constraints.
func RemoveUniqueConstraints(e *exp.Expr) *exp.Expr {
	e.AssertIs(exp.Create)
	for _, constraint := range e.FindAllList(exp.UniqueColumnConstraint) {
		if parent := constraint.Parent(); parent.IsAny(exp.ColumnConstraint, exp.Constraint) {
			parent.Pop()
		} else {
			constraint.Pop()
		}
	}
	return e
}

// CtasWithTmpTablesToCreateTmpView mirrors transforms.ctas_with_tmp_tables_to_create_tmp_view.
func CtasWithTmpTablesToCreateTmpView(tmpStorageProvider Transform) Transform {
	if tmpStorageProvider == nil {
		tmpStorageProvider = func(e *exp.Expr) *exp.Expr { return e }
	}
	return func(e *exp.Expr) *exp.Expr {
		e.AssertIs(exp.Create)
		temporary := false
		if props := e.Get("properties"); props != nil {
			for _, p := range props.Expressions() {
				if p.IsKind(exp.TemporaryProperty) {
					temporary = true
				}
			}
		}
		// CTAS with temp tables map to CREATE TEMPORARY VIEW
		if e.TextUpper("kind") == "TABLE" && temporary {
			if e.Expression() != nil {
				return exp.New(exp.Create, "kind", "TEMPORARY VIEW", "this", e.This(), "expression", e.Expression())
			}
			return tmpStorageProvider(e)
		}
		return e
	}
}

// MoveSchemaColumnsToPartitionedBy mirrors transforms.move_schema_columns_to_partitioned_by.
func MoveSchemaColumnsToPartitionedBy(e *exp.Expr) *exp.Expr {
	e.AssertIs(exp.Create)
	schema := e.This()
	kind := e.TextUpper("kind")
	if schema.IsKind(exp.Schema) && (kind == "TABLE" || kind == "VIEW") {
		prop := e.Find(exp.PartitionedByProperty)
		if prop != nil && prop.This() != nil && !prop.This().IsKind(exp.Schema) {
			columns := map[string]bool{}
			for _, v := range prop.This().Expressions() {
				columns[strings.ToUpper(v.Name())] = true
			}
			var partitions, rest []*exp.Expr
			for _, col := range schema.Expressions() {
				if columns[strings.ToUpper(col.Name())] {
					partitions = append(partitions, col)
				}
			}
			for _, x := range schema.Expressions() {
				if !slices.ContainsFunc(partitions, x.Equal) {
					rest = append(rest, x)
				}
			}
			schema.Set("expressions", rest)
			prop.Replace(exp.New(exp.PartitionedByProperty, "this", exp.New(exp.Schema, "expressions", partitions)))
			e.Set("this", schema)
		}
	}
	return e
}

// MovePartitionedByToSchemaColumns mirrors transforms.move_partitioned_by_to_schema_columns.
func MovePartitionedByToSchemaColumns(e *exp.Expr) *exp.Expr {
	e.AssertIs(exp.Create)
	prop := e.Find(exp.PartitionedByProperty)
	if prop == nil || prop.This() == nil || !prop.This().IsKind(exp.Schema) {
		return e
	}
	for _, x := range prop.This().Expressions() {
		if !(x.IsKind(exp.ColumnDef) && x.Bool("kind")) {
			return e
		}
	}
	var ids []*exp.Expr
	for _, x := range prop.This().Expressions() {
		ids = append(ids, exp.ToIdentifier(x.This()))
	}
	propThis := exp.New(exp.Tuple, "expressions", ids)
	schema := e.This()
	for _, x := range prop.This().Expressions() {
		schema.Append("expressions", x)
	}
	prop.Set("this", propThis)
	return e
}

// StructKvToAlias mirrors transforms.struct_kv_to_alias: STRUCT(k := v) becomes STRUCT(v AS k).
func StructKvToAlias(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Struct) {
		var items []*exp.Expr
		for _, x := range e.Expressions() {
			if x.IsKind(exp.PropertyEQ) {
				items = append(items, exp.NewAlias(x.Expression(), x.This()))
			} else {
				items = append(items, x)
			}
		}
		e.Set("expressions", items)
	}
	return e
}

// EliminateJoinMarks mirrors transforms.eliminate_join_marks: it removes Oracle's (+) join
// marks by converting them to LEFT JOINs.
func EliminateJoinMarks(e *exp.Expr) *exp.Expr {
	scopes := TraverseScope(e)
	// we go in reverse to check the main query for left correlation
	for i := len(scopes) - 1; i >= 0; i-- {
		scope := scopes[i]
		query := scope.Expression
		where := query.Get("where")
		joins := query.List("joins")
		if where == nil {
			continue
		}
		hasMark := false
		for _, c := range where.FindAllList(exp.Column) {
			if c.Bool("join_mark") {
				hasMark = true
				break
			}
		}
		if !hasMark {
			continue
		}
		// knockout: we do not support left correlation (see point 2)
		if scope.IsCorrelatedSubquery() {
			panic(fmt.Errorf("Correlated queries are not supported"))
		}
		// make sure we have AND of ORs to have clear join terms
		where = Normalize(where.This(), false, 128)
		if !Normalized(where, false) {
			panic(fmt.Errorf("Cannot normalize JOIN predicates"))
		}
		joinsOns := NewOrderedMap[[]*exp.Expr]()
		conds := []*exp.Expr{where}
		if where.IsKind(exp.And) {
			conds = where.Flatten(true)
		}
		for _, cond := range conds {
			var joinCols []*exp.Expr
			for _, col := range cond.FindAllList(exp.Column) {
				if col.Bool("join_mark") {
					joinCols = append(joinCols, col)
				}
			}
			leftJoinTable := map[string]bool{}
			for _, col := range joinCols {
				leftJoinTable[col.TableName()] = true
			}
			if len(leftJoinTable) == 0 {
				continue
			}
			if len(leftJoinTable) > 1 {
				panic(fmt.Errorf("Cannot combine JOIN predicates from different tables"))
			}
			for _, col := range joinCols {
				col.Set("join_mark", false)
			}
			var table string
			for t := range leftJoinTable {
				table = t
			}
			list, _ := joinsOns.Get(table)
			joinsOns.Set(table, append(list, cond))
		}

		oldJoins := NewOrderedMap[*exp.Expr]()
		for _, join := range joins {
			oldJoins.Set(join.AliasOrName(), join)
		}
		newJoins := NewOrderedMap[*exp.Expr]()
		queryFrom := query.Get("from_")

		for table, predicates := range joinsOns.All() {
			src, ok := oldJoins.Get(table)
			if !ok {
				src = queryFrom
			}
			joinWhat := src.This().Copy()
			var preds []any
			for _, p := range predicates {
				preds = append(preds, p)
			}
			newJoins.Set(joinWhat.AliasOrName(), exp.New(exp.Join, "this", joinWhat, "on", exp.NewAnd(preds...), "kind", "LEFT"))
			for _, p := range predicates {
				for p.Parent().IsKind(exp.Paren) {
					p.Parent().Replace(p)
				}
				parent := p.Parent()
				p.Pop()
				if parent.Is(exp.Binary) {
					left := parent.Get("this")
					if left == nil {
						parent.Replace(parent.Right())
					} else {
						parent.Replace(left)
					}
				} else if parent.IsKind(exp.Where) {
					parent.Pop()
				}
			}
		}

		if newJoins.Has(queryFrom.AliasOrName()) {
			var onlyOld []string
			for _, k := range oldJoins.Keys() {
				if !newJoins.Has(k) {
					onlyOld = append(onlyOld, k)
				}
			}
			if len(onlyOld) < 1 {
				panic(fmt.Errorf("Cannot determine which table to use in the new FROM clause"))
			}
			j, _ := oldJoins.Get(onlyOld[0])
			query.Set("from_", exp.New(exp.From, "this", j.This()))
		}

		if newJoins.Len() > 0 {
			for n, j := range oldJoins.All() { // preserve any other joins
				if !newJoins.Has(n) && n != query.Get("from_").Name() {
					if !j.Bool("kind") {
						j.Set("kind", "CROSS")
					}
					newJoins.Set(n, j)
				}
			}
			query.Set("joins", newJoins.Values())
		}
	}
	return e
}

// AnyToExists mirrors transforms.any_to_exists: x = ANY(arr) becomes EXISTS(arr, x -> ...).
func AnyToExists(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Select) {
		for _, anyExpr := range e.FindAllList(exp.Any) {
			this := anyExpr.This()
			if this.Is(exp.Query) || anyExpr.Parent().IsAny(exp.Like, exp.ILike) {
				continue
			}
			binop := anyExpr.Parent()
			if binop.Is(exp.Binary) {
				lambdaArg := exp.ToIdentifier("x")
				anyExpr.Replace(lambdaArg)
				lambdaExpr := exp.New(exp.Lambda, "this", binop.Copy(), "expressions", []*exp.Expr{lambdaArg})
				binop.Replace(exp.New(exp.Exists, "this", this.Unnest(), "expression", lambdaExpr))
			}
		}
	}
	return e
}

// EliminateWindowClause mirrors transforms.eliminate_window_clause: named windows are inlined.
func EliminateWindowClause(e *exp.Expr) *exp.Expr {
	windowsV := e.Arg("windows")
	windows, isList := windowsV.([]*exp.Expr)
	if e.IsKind(exp.Select) && windowsV != nil && isList {
		e.Set("windows", nil)
		windowExpression := map[string]*exp.Expr{}
		inlineInherited := func(window *exp.Expr) {
			inherited, ok := windowExpression[strings.ToLower(window.Alias())]
			if !ok || inherited == nil {
				return
			}
			window.Set("alias", nil)
			for _, key := range []string{"partition_by", "order", "spec"} {
				switch arg := inherited.Arg(key).(type) {
				case *exp.Expr:
					window.Set(key, arg.Copy())
				case []*exp.Expr:
					cp := make([]*exp.Expr, len(arg))
					for i, x := range arg {
						cp[i] = x.Copy()
					}
					window.Set(key, cp)
				}
			}
		}
		for _, window := range windows {
			inlineInherited(window)
			windowExpression[strings.ToLower(window.Name())] = window
		}
		for window := range FindAllInScope(e, exp.Window) {
			inlineInherited(window)
		}
	}
	return e
}

// InheritStructFieldNames mirrors transforms.inherit_struct_field_names: subsequent structs
// of an array inherit the field names of the first one (BigQuery semantics).
func InheritStructFieldNames(e *exp.Expr) *exp.Expr {
	if !(e.IsKind(exp.Array) && e.Bool("struct_name_inheritance")) {
		return e
	}
	first := seqGet(e.Expressions(), 0)
	if !first.IsKind(exp.Struct) {
		return e
	}
	for _, fld := range first.Expressions() {
		if !fld.IsKind(exp.PropertyEQ) {
			return e
		}
	}
	var fieldNames []*exp.Expr
	for _, fld := range first.Expressions() {
		fieldNames = append(fieldNames, fld.This())
	}
	// Apply field names to subsequent structs that don't have them
	for _, st := range e.Expressions()[1:] {
		if !st.IsKind(exp.Struct) || len(st.Expressions()) != len(fieldNames) {
			continue
		}
		var newExpressions []*exp.Expr
		for i, x := range st.Expressions() {
			if !x.IsKind(exp.PropertyEQ) {
				// Create PropertyEQ: field_name := value, preserving the type from the inner expression
				propertyEQ := exp.New(exp.PropertyEQ, "this", fieldNames[i].Copy(), "expression", x)
				propertyEQ.SetType(x.Type())
				newExpressions = append(newExpressions, propertyEQ)
			} else {
				newExpressions = append(newExpressions, x)
			}
		}
		st.Set("expressions", newExpressions)
	}
	return e
}
