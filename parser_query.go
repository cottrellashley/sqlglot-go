package sqlglot

import (
	"fmt"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func (p *Parser) parseDerivedTableValues(allowValueSynonym bool) *exp.Expr {
	isDerived := p.MatchPair(tokens.LParen, tokens.Values)
	if !isDerived && !(p.MatchTextSeq("VALUES") || p.MatchTextSeq("FORMAT", "VALUES") || (allowValueSynonym && p.MatchTextSeq("VALUE"))) {
		return nil
	}
	expressions := p.ParseCSV(func() *exp.Expr { return p.ParseValue(true) })
	alias := p.ParseTableAlias(nil)
	if isDerived {
		p.MatchRParen(nil)
	}
	if alias == nil {
		alias = p.ParseTableAlias(nil)
	}
	if expressions == nil {
		expressions = []*exp.Expr{}
	}
	return p.Expr(exp.Values, "expressions", expressions, "alias", alias)
}

func (p *Parser) parseTableSample(asModifier bool) *exp.Expr {
	if !p.Match(tokens.TableSample) && !(asModifier && p.MatchTextSeq("USING", "SAMPLE")) {
		return nil
	}
	var bucketNumerator, bucketDenominator, bucketField, percent, size, seed any
	rowSet := tokens.NewSet(tokens.Row)
	method := p.ParseVar(false, &rowSet, true)
	matchedLParen := p.Match(tokens.LParen)
	var expressions any
	var num *exp.Expr
	if p.C.TablesampleCsv {
		expressions = p.ParseCSV(p.ParsePrimary)
	} else {
		if p.Check(tokens.Number) {
			num = p.ParseFactor(false)
		} else {
			num = p.ParsePrimary()
			if num == nil {
				num = p.ParsePlaceholder()
			}
		}
	}
	if p.MatchTextSeq("BUCKET") {
		bucketNumerator = nilIfNil(p.ParseNumber())
		p.MatchTextSeq("OUT", "OF")
		bucketDenominator = nilIfNil(p.ParseNumber())
		p.Match(tokens.On)
		bucketField = nilIfNil(p.ParseField(false, nil, false))
	} else if p.MatchSet(tokens.NewSet(tokens.Percent, tokens.Mod)) {
		percent = nilIfNil(num)
	} else if p.Match(tokens.Rows) || !p.D.TablesampleSizeIsPercent {
		size = nilIfNil(num)
	} else {
		percent = nilIfNil(num)
	}
	if matchedLParen {
		p.MatchRParen(nil)
	}
	if p.Match(tokens.LParen) {
		method = p.ParseVar(false, nil, true)
		seed = AndThen(p.Match(tokens.Comma), p.ParseNumber)
		p.MatchRParen(nil)
	} else if p.MatchTexts("SEED", "REPEATABLE") {
		seed = nilIfNil(p.ParseWrappedExpr(p.ParseNumber, false))
	}
	if method == nil && p.C.DefaultSamplingMethod != "" {
		method = exp.NewVar(p.C.DefaultSamplingMethod)
	}
	return p.Expr(exp.TableSample, "expressions", expressions, "method", method, "bucket_numerator", bucketNumerator,
		"bucket_denominator", bucketDenominator, "bucket_field", bucketField, "percent", percent, "size", size, "seed", seed)
}

// pivotsArg is the value of a "pivots" argument: None (unset) when there are no pivots, like
// Python's `list(...) or None`.
func pivotsArg(pivots []*exp.Expr) any {
	if len(pivots) == 0 {
		return nil
	}
	return pivots
}

func (p *Parser) parsePivots() []*exp.Expr {
	if p.Curr.Type != tokens.Pivot && p.Curr.Type != tokens.Unpivot {
		return nil
	}
	var out []*exp.Expr
	for pv := p.ParsePivot(); pv != nil; pv = p.ParsePivot() {
		out = append(out, pv)
	}
	return out
}

// ParseJoins mirrors _parse_joins() (with default alias tokens).
func (p *Parser) ParseJoins() []*exp.Expr { return p.ParseJoinsWith(nil) }

func (p *Parser) parseJoinsWith(aliasTokens *tokens.TokenSet) []*exp.Expr {
	var out []*exp.Expr
	for j := p.ParseJoin(false, false, aliasTokens); j != nil; j = p.ParseJoin(false, false, aliasTokens) {
		out = append(out, j)
	}
	return out
}

func (p *Parser) parseUnpivotColumns() *exp.Expr {
	if !p.Match(tokens.Into) {
		return nil
	}
	this := AndThen(p.MatchTextSeq("NAME"), p.ParseColumn)
	var expressions any = false
	if p.MatchTextSeq("VALUE") {
		expressions = p.ParseCSV(p.ParseColumn)
	}
	return p.Expr(exp.UnpivotColumns, "this", this, "expressions", expressions)
}

func (p *Parser) parseSimplifiedPivot(isUnpivot bool) *exp.Expr {
	parseOn := func() *exp.Expr {
		this := p.ParseBitwise()
		if p.Match(tokens.In) {
			return p.ParseIn(this, false)
		}
		if p.Check(tokens.Alias) {
			return p.ParseAlias(this, false)
		}
		return this
	}
	this := p.ParseTable(TableOpts{})
	var expressions any = false
	if p.Match(tokens.On) {
		expressions = p.ParseCSV(parseOn)
	}
	into := p.ParseUnpivotColumns()
	var using any = false
	if p.Match(tokens.Using) {
		using = p.ParseCSV(func() *exp.Expr { return p.ParseAlias(p.ParseColumn(), false) })
	}
	group := p.ParseGroup(false)
	var unpivot any
	if isUnpivot {
		unpivot = true
	}
	return p.Expr(exp.Pivot, "this", this, "expressions", expressions, "using", using, "group", group, "unpivot", unpivot, "into", into)
}

func (p *Parser) parsePivotIn() *exp.Expr {
	aliased := func() *exp.Expr {
		this := p.ParseSelectOrExpression(false)
		p.Match(tokens.Alias)
		alias := p.ParseBitwise()
		if alias != nil {
			if alias.Is(exp.Column) && alias.DB() == "" {
				alias = alias.This()
			}
			return p.Expr(exp.PivotAlias, "this", this, "alias", alias)
		}
		return this
	}
	value := p.ParseColumn()
	if !p.Match(tokens.In) {
		p.RaiseError("Expecting IN")
	}
	if p.Match(tokens.LParen) {
		var exprs []*exp.Expr
		if p.Match(tokens.Any) {
			exprs = []*exp.Expr{exp.New(exp.PivotAny, "this", p.ParseOrder(nil, false))}
		} else {
			exprs = p.ParseCSV(aliased)
		}
		p.MatchRParen(nil)
		if exprs == nil {
			exprs = []*exp.Expr{}
		}
		return p.Expr(exp.In, "this", value, "expressions", exprs)
	}
	return p.Expr(exp.In, "this", value, "field", p.ParseIdVar(true, nil))
}

func (p *Parser) parsePivotAggregation() *exp.Expr {
	f := p.ParseFunction(FunctionOpts{})
	if f == nil {
		if p.Prev.Type == tokens.Comma {
			return nil
		}
		p.RaiseError("Expecting an aggregation function in PIVOT")
	}
	return p.ParseAlias(f, false)
}

func unpivotTarget(e *exp.Expr) *exp.Expr {
	if e.Is(exp.Column) && e.TableName() == "" {
		return e.This()
	}
	if e.Is(exp.Tuple) {
		var out []*exp.Expr
		for _, x := range e.Expressions() {
			out = append(out, unpivotTarget(x))
		}
		e.Set("expressions", out)
	}
	return e
}

func (p *Parser) parsePivot() *exp.Expr {
	index := p.Index
	var includeNulls any
	var unpivot bool
	if p.Match(tokens.Pivot) {
		unpivot = false
	} else if p.Match(tokens.Unpivot) {
		unpivot = true
		if p.MatchTextSeq("INCLUDE", "NULLS") {
			includeNulls = true
		} else if p.MatchTextSeq("EXCLUDE", "NULLS") {
			includeNulls = false
		}
	} else {
		return nil
	}
	if !p.Match(tokens.LParen) {
		p.Retreat(index)
		return nil
	}
	var expressions []*exp.Expr
	if unpivot {
		expressions = p.ParseCSV(p.ParseColumn)
	} else {
		expressions = p.ParseCSV(p.ParsePivotAggregation)
	}
	if len(expressions) == 0 {
		p.RaiseError("Failed to parse PIVOT's aggregation list")
	}
	if !p.Match(tokens.For) {
		p.RaiseError("Expecting FOR")
	}
	fields := []*exp.Expr{}
	for {
		field := p.TryParse(p.ParsePivotIn, false)
		if field == nil {
			break
		}
		fields = append(fields, field)
	}
	var defaultOnNull any = false
	if p.MatchTextSeq("DEFAULT", "ON", "NULL") {
		defaultOnNull = nilIfNil(p.ParseWrappedExpr(p.ParseBitwise, false))
	}
	group := p.ParseGroup(false)
	p.MatchRParen(nil)
	if expressions == nil {
		expressions = []*exp.Expr{}
	}
	pivot := p.Expr(exp.Pivot, "expressions", expressions, "fields", fields, "unpivot", unpivot,
		"include_nulls", includeNulls, "default_on_null", defaultOnNull, "group", group)
	if unpivot {
		var targets []*exp.Expr
		for _, e := range pivot.Expressions() {
			targets = append(targets, unpivotTarget(e))
		}
		pivot.Set("expressions", targets)
		for _, pf := range pivot.Fields() {
			if pf.Is(exp.In) {
				pf.Set("this", unpivotTarget(pf.This()))
			}
		}
		pivot.Set("value_columns_first", p.C.UnpivotValueColumnsFirst)
	}
	if !p.CheckSet(tokens.NewSet(tokens.Pivot, tokens.Unpivot)) {
		pivot.Set("alias", p.ParseTableAlias(nil))
	}
	if !unpivot {
		names := p.PivotColumnNames(expressions)
		columns := []*exp.Expr{}
		var allFields [][]string
		for _, pf := range pivot.Fields() {
			pfe := pf.Expressions()
			if len(pfe) > 0 && pfe[0].Is(exp.PivotAny) {
				continue
			}
			var row []string
			for _, fld := range pfe {
				if p.C.IdentifyPivotStrings && !fld.Is(exp.PivotAlias) {
					row = append(row, fld.SQL())
				} else {
					row = append(row, fld.AliasOrName())
				}
			}
			allFields = append(allFields, row)
		}
		if len(allFields) > 0 {
			if len(names) > 0 {
				allFields = append(allFields, names)
			}
			for _, parts := range cartesian(allFields) {
				if len(names) > 0 && p.C.PrefixedPivotColumns {
					last := parts[len(parts)-1]
					parts = append([]string{last}, parts[:len(parts)-1]...)
				}
				columns = append(columns, exp.ToIdentifier(strings.Join(parts, "_")))
			}
		}
		pivot.Set("columns", columns)
		pivot.Set("identify_pivot_strings", p.C.IdentifyPivotStrings)
		pivot.Set("prefixed_pivot_columns", p.C.PrefixedPivotColumns)
		pivot.Set("pivot_column_naming", p.C.PivotColumnNaming)
	}
	return pivot
}

// cartesian mirrors itertools.product over lists of strings.
func cartesian(lists [][]string) [][]string {
	result := [][]string{{}}
	for _, l := range lists {
		var next [][]string
		for _, prefix := range result {
			for _, x := range l {
				item := append(append([]string(nil), prefix...), x)
				next = append(next, item)
			}
		}
		result = next
	}
	return result
}

func (p *Parser) pivotColumnNames(aggregations []*exp.Expr) []string {
	var out []string
	for _, agg := range aggregations {
		if a := agg.Alias(); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func (p *Parser) parsePrewhere(skipWhereToken bool) *exp.Expr {
	if !skipWhereToken && !p.Match(tokens.Prewhere) {
		return nil
	}
	comments := p.PrevComments
	return p.ExpressionComments(exp.New(exp.PreWhere, "this", p.ParseDisjunction()), comments)
}

func (p *Parser) parseWhere(skipWhereToken bool) *exp.Expr {
	if !skipWhereToken && !p.Match(tokens.Where) {
		return nil
	}
	comments := p.PrevComments
	return p.ExpressionComments(exp.New(exp.Where, "this", p.ParseDisjunction()), comments)
}

func (p *Parser) parseGroup(skipGroupByToken bool) *exp.Expr {
	if !skipGroupByToken && !p.Match(tokens.GroupBy) {
		return nil
	}
	comments := p.PrevComments
	var kw KW
	appendTo := func(key string, xs ...*exp.Expr) {
		cur, _ := kw.Get(key).([]*exp.Expr)
		kw.Set(key, append(cur, xs...))
	}
	if p.Match(tokens.All) {
		kw.Set("all", true)
	} else if p.Match(tokens.Distinct) {
		kw.Set("all", false)
	}
	for {
		if CheckIn(p, p.C.QueryModifierParsers) || p.CheckSet(p.C.QueryModifierTokens) {
			break
		}
		appendTo("expressions", p.ParseCSV(func() *exp.Expr {
			if g := p.ParseGroupingSets(); g != nil {
				return g
			}
			if c := p.ParseCubeOrRollup(false); c != nil {
				return c
			}
			return p.ParseDisjunction()
		})...)
		beforeWithIndex := p.Index
		if p.Match(tokens.With) {
			if cr := p.ParseCubeOrRollup(true); cr != nil {
				key := "cube"
				if cr.Is(exp.Rollup) {
					key = "rollup"
				}
				appendTo(key, cr)
				goto checkIndex
			}
		}
		if gs := p.ParseGroupingSets(); gs != nil {
			appendTo("grouping_sets", gs)
			break
		} else if p.MatchTextSeq("TOTALS") {
			kw.Set("totals", true)
		}
	checkIndex:
		if beforeWithIndex <= p.Index && p.Index <= beforeWithIndex+1 {
			p.Retreat(beforeWithIndex)
			break
		}
	}
	return p.ExpressionComments(exp.New(exp.Group, kw.KV()...), comments)
}

func (p *Parser) parseCubeOrRollup(withPrefix bool) *exp.Expr {
	var kind exp.Kind
	if p.Match(tokens.Cube) {
		kind = exp.Cube
	} else if p.Match(tokens.Rollup) {
		kind = exp.Rollup
	} else {
		return nil
	}
	exprs := []*exp.Expr{}
	if !withPrefix {
		exprs = p.ParseWrappedCSV(p.ParseBitwise, false)
		if exprs == nil {
			exprs = []*exp.Expr{}
		}
	}
	return p.Expr(kind, "expressions", exprs)
}

func (p *Parser) parseGroupingSets() *exp.Expr {
	if p.Match(tokens.GroupingSets) {
		return p.Expr(exp.GroupingSets, "expressions", p.ParseWrappedCSV(p.ParseGroupingSet, false))
	}
	return nil
}

func (p *Parser) parseGroupingSet() *exp.Expr {
	if g := p.ParseGroupingSets(); g != nil {
		return g
	}
	if c := p.ParseCubeOrRollup(false); c != nil {
		return c
	}
	return p.ParseBitwise()
}

func (p *Parser) parseHaving(skipHavingToken bool) *exp.Expr {
	if !skipHavingToken && !p.Match(tokens.Having) {
		return nil
	}
	comments := p.PrevComments
	return p.ExpressionComments(exp.New(exp.Having, "this", p.ParseDisjunction()), comments)
}

func (p *Parser) parseQualify() *exp.Expr {
	if !p.Match(tokens.Qualify) {
		return nil
	}
	return p.Expr(exp.Qualify, "this", p.ParseDisjunction())
}

// NoParenFunctionParser looks up NO_PAREN_FUNCTION_PARSERS (including temporary entries).
func (p *Parser) NoParenFunctionParser(name string) (ParseFunc, bool) {
	if f, ok := p.noParenOverrides()[name]; ok {
		return f, true
	}
	f, ok := p.C.NoParenFunctionParsers[name]
	return f, ok
}

func (p *Parser) noParenOverrides() map[string]ParseFunc {
	if p.Extra == nil {
		return nil
	}
	m, _ := p.Extra["__no_paren_overrides"].(map[string]ParseFunc)
	return m
}

func (p *Parser) setNoParenOverride(name string, f ParseFunc) {
	if p.Extra == nil {
		p.Extra = map[string]any{}
	}
	m := p.noParenOverrides()
	if m == nil {
		m = map[string]ParseFunc{}
		p.Extra["__no_paren_overrides"] = m
	}
	if f == nil {
		delete(m, name)
	} else {
		m[name] = f
	}
}

func (p *Parser) parseConnectWithPrior() *exp.Expr {
	p.setNoParenOverride("PRIOR", func(p *Parser) *exp.Expr { return p.Expr(exp.Prior, "this", p.ParseBitwise()) })
	connect := p.ParseDisjunction()
	p.setNoParenOverride("PRIOR", nil)
	return connect
}

func (p *Parser) parseConnect(skipStartToken bool) *exp.Expr {
	var start *exp.Expr
	if skipStartToken {
	} else if p.MatchTextSeq("START", "WITH") {
		start = p.ParseDisjunction()
	} else {
		return nil
	}
	p.Match(tokens.ConnectBy)
	nocycle := p.MatchTextSeq("NOCYCLE")
	connect := p.ParseConnectWithPrior()
	if start == nil && p.MatchTextSeq("START", "WITH") {
		start = p.ParseDisjunction()
	}
	return p.Expr(exp.Connect, "start", start, "connect", connect, "nocycle", nocycle)
}

func (p *Parser) parseNameAsExpression() *exp.Expr {
	this := p.ParseIdVar(true, nil)
	if p.Match(tokens.Alias) {
		this = p.Expr(exp.Alias, "alias", this, "this", p.ParseDisjunction())
	}
	return this
}

func (p *Parser) parseInterpolate() []*exp.Expr {
	if p.MatchTextSeq("INTERPOLATE") {
		return p.ParseWrappedCSV(p.ParseNameAsExpression, false)
	}
	return nil
}

func (p *Parser) parseOrder(this *exp.Expr, skipOrderToken bool) *exp.Expr {
	var siblings any
	if !skipOrderToken && !p.Match(tokens.OrderBy) {
		if !p.Match(tokens.OrderSiblingsBy) {
			return this
		}
		siblings = true
	}
	comments := p.PrevComments
	exprs := p.ParseCSV(func() *exp.Expr { return p.ParseOrdered(nil) })
	if exprs == nil {
		exprs = []*exp.Expr{}
	}
	return p.ExpressionComments(exp.New(exp.Order, "this", this, "expressions", exprs, "siblings", siblings), comments)
}

func (p *Parser) parseSort(kind exp.Kind, token tokens.TokenType) *exp.Expr {
	if !p.Match(token) {
		return nil
	}
	exprs := p.ParseCSV(func() *exp.Expr { return p.ParseOrdered(nil) })
	if exprs == nil {
		exprs = []*exp.Expr{}
	}
	return p.Expr(kind, "expressions", exprs)
}

func (p *Parser) parseOrdered(parseMethod func() *exp.Expr) *exp.Expr {
	var this *exp.Expr
	if parseMethod != nil {
		this = parseMethod()
	} else {
		this = p.ParseDisjunction()
	}
	if this == nil {
		return nil
	}
	if strings.ToUpper(this.Name()) == "ALL" && p.D.SupportsOrderByAll {
		this = exp.NewVar("ALL")
	}
	asc := p.Match(tokens.Asc)
	var desc any
	if p.Match(tokens.Desc) {
		desc = true
	} else if asc {
		desc = false
	}
	isNullsFirst := p.MatchTextSeq("NULLS", "FIRST")
	isNullsLast := p.MatchTextSeq("NULLS", "LAST")
	nullsFirst := isNullsFirst
	explicitlyNullOrdered := isNullsFirst || isNullsLast
	d := exp.Truthy(desc)
	if !explicitlyNullOrdered &&
		((!d && p.D.NullOrdering == "nulls_are_small") || (d && p.D.NullOrdering != "nulls_are_small")) &&
		p.D.NullOrdering != "nulls_are_last" {
		nullsFirst = true
	}
	var withFill *exp.Expr
	if p.MatchTextSeq("WITH", "FILL") {
		from := AndThen(p.Match(tokens.From), p.ParseBitwise)
		to := AndThen(p.MatchTextSeq("TO"), p.ParseBitwise)
		step := AndThen(p.MatchTextSeq("STEP"), p.ParseBitwise)
		var interp any
		if i := p.ParseInterpolate(); i != nil {
			interp = i
		}
		withFill = p.Expr(exp.WithFill, "from_", from, "to", to, "step", step, "interpolate", interp)
	}
	return p.Expr(exp.Ordered, "this", this, "desc", desc, "nulls_first", nullsFirst, "with_fill", withFill)
}

func (p *Parser) parseLimitOptions() *exp.Expr {
	percent := p.MatchSet(tokens.NewSet(tokens.Percent, tokens.Mod))
	rows := p.MatchTexts("ROW", "ROWS")
	p.MatchTextSeq("ONLY")
	withTies := p.MatchTextSeq("WITH", "TIES")
	if !(percent || rows || withTies) {
		return nil
	}
	return p.Expr(exp.LimitOptions, "percent", percent, "rows", rows, "with_ties", withTies)
}

func (p *Parser) parseLimit(this *exp.Expr, top, skipLimitToken bool) *exp.Expr {
	tok := tokens.Limit
	if top {
		tok = tokens.Top
	}
	if skipLimitToken || p.Match(tok) {
		comments := p.PrevComments
		var expression *exp.Expr
		if top {
			limitParen := p.Match(tokens.LParen)
			if limitParen {
				expression = p.ParseTerm(true)
				if expression == nil {
					expression = p.ParseSelect(SelectOpts{})
				}
				p.MatchRParen(nil)
			} else {
				expression = p.ParseNumber()
			}
		} else {
			if p.D.SupportsLimitAll && p.Match(tokens.All) {
				expression = exp.NewVar("ALL")
			} else {
				expression = p.ParseTerm(false)
			}
		}
		limitOptions := p.ParseLimitOptions()
		var offset *exp.Expr
		if p.Match(tokens.Comma) {
			offset = expression
			expression = p.ParseTerm(true)
		}
		var limitBy any
		if lb := p.ParseLimitBy(); lb != nil {
			limitBy = lb
		}
		limitExp := p.ExpressionComments(exp.New(exp.Limit, "this", this, "expression", expression, "offset", offset,
			"limit_options", limitOptions, "expressions", limitBy), comments)
		if top {
			limitExp.Meta()["top"] = true
		}
		return limitExp
	}
	if p.Match(tokens.Fetch) {
		direction := "FIRST"
		if p.MatchSet(tokens.NewSet(tokens.First, tokens.Next)) {
			direction = p.PrevTextUpper()
		}
		var count *exp.Expr
		if !p.CheckTexts("ROW", "ROWS") {
			ft := p.C.FetchTokens
			count = p.ParseField(false, &ft, false)
		}
		return p.Expr(exp.Fetch, "direction", direction, "count", count, "limit_options", p.ParseLimitOptions())
	}
	return this
}

func (p *Parser) parseOffset(this *exp.Expr) *exp.Expr {
	if !p.Match(tokens.Offset) {
		return this
	}
	count := p.ParseTerm(true)
	p.MatchTexts("ROW", "ROWS")
	var limitBy any
	if lb := p.ParseLimitBy(); lb != nil {
		limitBy = lb
	}
	return p.Expr(exp.Offset, "this", this, "expression", count, "expressions", limitBy)
}

func (p *Parser) canParseLimitOrOffset() bool {
	if !p.CheckSet(p.C.AmbiguousAliasTokens) {
		return false
	}
	index := p.Index
	result := p.TryParse(func() *exp.Expr { return p.ParseLimit(nil, false, false) }, true) != nil ||
		p.TryParse(func() *exp.Expr { return p.ParseOffset(nil) }, true) != nil
	p.Retreat(index)
	if p.Next.Type == tokens.MatchCondition {
		result = false
	}
	return result
}

func (p *Parser) canParseNamedWindow() bool {
	if !p.Check(tokens.Window) {
		return false
	}
	at := func(i int) *tokens.Token {
		if p.Index+i < len(p.Tokens) {
			return p.Tokens[p.Index+i]
		}
		return nil
	}
	name := at(1)
	if name == nil || !p.C.IdVarTokens.Has(name.Type) {
		return false
	}
	aliasTok := at(2)
	if aliasTok == nil || aliasTok.Type != tokens.Alias {
		return false
	}
	body := at(3)
	return body != nil && body.Type == tokens.LParen
}

func (p *Parser) parseLimitBy() []*exp.Expr {
	if p.MatchTextSeq("BY") {
		return p.ParseCSV(p.ParseBitwise)
	}
	return nil
}

func (p *Parser) parseLocks() []*exp.Expr {
	locks := []*exp.Expr{}
	for {
		var update, key any
		if p.MatchTextSeq("FOR", "UPDATE") {
			update = true
		} else if p.MatchTextSeq("FOR", "SHARE") || p.MatchTextSeq("LOCK", "IN", "SHARE", "MODE") {
			update = false
		} else if p.MatchTextSeq("FOR", "KEY", "SHARE") {
			update, key = false, true
		} else if p.MatchTextSeq("FOR", "NO", "KEY", "UPDATE") {
			update, key = true, true
		} else {
			break
		}
		var expressions any
		if p.MatchTextSeq("OF") {
			expressions = p.ParseCSV(func() *exp.Expr { return p.ParseTable(TableOpts{Schema: true}) })
		}
		var wait any
		if p.MatchTextSeq("NOWAIT") {
			wait = true
		} else if p.MatchTextSeq("WAIT") {
			wait = nilIfNil(p.ParsePrimary())
		} else if p.MatchTextSeq("SKIP", "LOCKED") {
			wait = false
		}
		locks = append(locks, p.Expr(exp.Lock, "update", update, "expressions", expressions, "wait", wait, "key", key))
	}
	return locks
}

// ParseSetOperation mirrors parse_set_operation(this, consume_pipe, intersect_only).
func (p *Parser) parseSetOperation(this *exp.Expr, consumePipe, intersectOnly bool) *exp.Expr {
	start := p.Index
	_, sideToken, kindToken := p.ParseJoinParts()
	var side, kind any
	if sideToken != nil {
		side = sideToken.Text
	}
	if kindToken != nil {
		kind = kindToken.Text
	}
	if !p.MatchSet(p.C.SetOperations) || (intersectOnly && p.Prev.Type != tokens.Intersect) {
		p.Retreat(start)
		return nil
	}
	tt := p.Prev.Type
	var operation exp.Kind
	switch tt {
	case tokens.Union:
		operation = exp.Union
	case tokens.Except:
		operation = exp.Except
	default:
		operation = exp.Intersect
	}
	comments := p.Prev.Comments
	var distinct any
	if p.Match(tokens.Distinct) {
		distinct = true
	} else if p.Match(tokens.All) {
		distinct = false
	} else {
		d := p.D.SetOpDistinctByDefault[operation]
		if d == nil {
			p.RaiseError(fmt.Sprintf("Expected DISTINCT or ALL for %s", operation))
		} else {
			distinct = *d
		}
	}
	var byName any
	if p.MatchTextSeq("BY", "NAME") || p.MatchTextSeq("STRICT", "CORRESPONDING") {
		byName = true
	}
	if p.MatchTextSeq("CORRESPONDING") {
		byName = true
		if side == nil && kind == nil {
			kind = "INNER"
		}
	}
	var onColumnList any
	if byName != nil && p.MatchTexts("ON", "BY") {
		onColumnList = p.ParseWrappedCSV(p.ParseColumn, false)
	}
	expression := p.ParseSelect(SelectOpts{Nested: true, NoSetOperation: true, NoConsumePipe: !consumePipe})
	if this.Is(exp.Values) {
		this = p.ValuesToSelect(this)
	}
	if expression.Is(exp.Values) {
		expression = p.ValuesToSelect(expression)
	}
	for _, operand := range []*exp.Expr{this, expression} {
		if operand.Is(exp.Subquery) {
			if values := operand.Unnest(); values.Is(exp.Values) {
				wrapper := values.Parent()
				wrapper.Set("this", p.ValuesToSelect(values))
			}
		}
	}
	if this.Is(exp.Alias) && this.This().Is(exp.Subquery) {
		subquery := this.This()
		subquery.Set("alias", exp.New(exp.TableAlias, "this", this.Get("alias")))
		subquery.AddComments(this.PopComments(), false)
		this = subquery
	}
	return p.ExpressionComments(exp.New(operation, "this", this, "distinct", distinct, "by_name", byName,
		"expression", expression, "side", side, "kind", kind, "on", onColumnList), comments)
}

var setOpModifierOrder = []string{"order", "limit", "offset", "sort", "distribute", "cluster"}

func (p *Parser) parseSetOperations(this *exp.Expr) *exp.Expr {
	intersectBindsTighter := p.D.IntersectBindsTighterThanUnionAndExcept
	var lastSetop *exp.Expr
	for this != nil {
		setop := p.ParseSetOperation(this, false, false)
		if setop == nil {
			break
		}
		lastSetop = setop
		if setop.Is(exp.Intersect) && setop.This().IsAny(exp.Union, exp.Except) && !intersectBindsTighter {
			setop.Set("this", setop.This().Subquery(nil, false))
		} else if !setop.Is(exp.Intersect) && intersectBindsTighter {
			operand := setop.Expression()
			for {
				intersect := p.ParseSetOperation(operand, false, true)
				if intersect == nil {
					break
				}
				lastSetop = intersect
				operand = intersect
			}
			if operand != setop.Expression() {
				setop.Set("expression", operand.Subquery(nil, false))
			}
		}
		this = setop
	}
	if this.Is(exp.SetOperation) && p.C.ModifiersAttachedToSetOp {
		var expression *exp.Expr
		if lastSetop != nil {
			expression = lastSetop.Expression()
		}
		if expression != nil {
			for _, arg := range setOpModifierOrder {
				if !p.C.SetOpModifiers[arg] {
					continue
				}
				e, _ := expression.Arg(arg).(*exp.Expr)
				if e != nil && !(arg == "limit" && exp.Truthy(e.MetaGet("top"))) {
					expression.Set(arg, nil)
					this.Set(arg, e)
				}
			}
			for arg := range p.C.SetOpModifiers {
				if contains(setOpModifierOrder, arg) {
					continue
				}
				if e, _ := expression.Arg(arg).(*exp.Expr); e != nil {
					expression.Set(arg, nil)
					this.Set(arg, e)
				}
			}
		}
		if CheckIn(p, p.C.QueryModifierParsers) {
			this = p.ParseQueryModifiers(this)
		}
	}
	return this
}

func (p *Parser) parseExpression() *exp.Expr { return p.ParseAlias(p.ParseAssignment(), false) }

func (p *Parser) parseAssignment() *exp.Expr {
	this := p.ParseDisjunction()
	if this == nil {
		if _, ok := p.C.Assignment[p.Next.Type]; ok {
			text := ""
			if p.AdvanceAny(true) != nil {
				text = p.Prev.Text
			}
			this = exp.NewColumn(text)
		}
	}
	for MatchIn(p, p.C.Assignment) {
		if this.Is(exp.Column) && len(this.Parts()) == 1 {
			this = this.This()
		}
		comments := p.PrevComments
		kind := p.C.Assignment[p.Prev.Type]
		this = p.ExpressionComments(exp.New(kind, "this", this, "expression", p.ParseAssignment()), comments)
	}
	return this
}

func (p *Parser) parseDisjunction() *exp.Expr {
	this := p.ParseConjunction()
	for MatchIn(p, p.C.Disjunction) {
		comments := p.PrevComments
		kind := p.C.Disjunction[p.Prev.Type]
		this = p.ExpressionComments(exp.New(kind, "this", this, "expression", p.ParseConjunction()), comments)
	}
	return this
}

func (p *Parser) parseConjunction() *exp.Expr {
	this := p.ParseEquality(true)
	for MatchIn(p, p.C.Conjunction) {
		comments := p.PrevComments
		kind := p.C.Conjunction[p.Prev.Type]
		this = p.ExpressionComments(exp.New(kind, "this", this, "expression", p.ParseEquality(true)), comments)
	}
	return this
}

func (p *Parser) buildPredicate(kind exp.Kind, this, expression *exp.Expr, comments []string, negate bool) *exp.Expr {
	isPredicates := []exp.Kind{exp.Is, exp.NullSafeEQ, exp.NullSafeNEQ}
	isIn := func(k exp.Kind) bool { return k == exp.Is || k == exp.NullSafeEQ || k == exp.NullSafeNEQ }
	if isIn(kind) {
		inValues := func(m map[tokens.TokenType]exp.Kind, k exp.Kind) bool {
			for _, v := range m {
				if v == k {
					return true
				}
			}
			return false
		}
		if this.IsAny(exp.Connector, exp.Not, exp.NullSafeNEQ) ||
			(this != nil && inValues(p.C.Equality, this.Kind())) ||
			(this != nil && inValues(p.C.Comparison, this.Kind())) ||
			(kind != exp.Is && this.Is(exp.Is)) {
			this = exp.New(exp.Paren, "this", this)
		}
		if expression.IsAny(exp.Predicate, exp.Connector, exp.Not, exp.Escape) {
			expression = exp.New(exp.Paren, "this", expression)
		}
	} else {
		t := this
		if this.Is(exp.Not) {
			t = this.This()
		}
		if t.IsAny(isPredicates...) {
			this = exp.New(exp.Paren, "this", this)
		}
		e := expression
		if expression.Is(exp.Not) {
			e = expression.This()
		}
		if e.IsAny(isPredicates...) {
			expression = exp.New(exp.Paren, "this", expression)
		}
	}
	predicate := exp.New(kind, "this", this, "expression", expression)
	if negate {
		predicate.Set("negate", true)
	}
	return p.ExpressionComments(predicate, comments)
}

func (p *Parser) parseEquality(parseIs bool) *exp.Expr {
	this := p.ParseComparison(nil)
	for {
		if MatchIn(p, p.C.Equality) {
			comments := p.PrevComments
			kind := p.C.Equality[p.Prev.Type]
			this = p.BuildPredicate(kind, this, p.ParseComparison(nil), comments, false)
		} else if parseIs && p.IsPredicateAhead() {
			index := p.Index
			this = p.ParseComparison(p.ParseRange(this, true))
			if p.Index == index {
				break
			}
		} else {
			break
		}
	}
	return this
}

func (p *Parser) parseComparison(this *exp.Expr) *exp.Expr {
	if this == nil {
		this = p.ParseRange(nil, false)
	}
	for MatchIn(p, p.C.Comparison) {
		comments := p.PrevComments
		kind := p.C.Comparison[p.Prev.Type]
		this = p.BuildPredicate(kind, this, p.ParseRange(nil, false), comments, false)
	}
	return this
}
