package sqlglot

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func (p *Parser) parseTruncateTable() *exp.Expr {
	start := p.Prev
	if p.Match(tokens.LParen) {
		p.Retreat(p.Index - 2)
		return p.ParseFunction(FunctionOpts{})
	}
	isDatabase := p.Match(tokens.Database)
	p.Match(tokens.Table)
	exists := p.ParseExists(false)
	expressions := p.ParseCSV(func() *exp.Expr { return p.ParseTable(TableOpts{Schema: true, IsDbReference: isDatabase}) })
	var cluster *exp.Expr
	if p.Match(tokens.On) {
		cluster = p.ParseOnProperty()
	}
	var identity, option any
	if p.MatchTextSeq("RESTART", "IDENTITY") {
		identity = "RESTART"
	} else if p.MatchTextSeq("CONTINUE", "IDENTITY") {
		identity = "CONTINUE"
	}
	if p.MatchTextSeq("CASCADE") || p.MatchTextSeq("RESTRICT") {
		option = p.Prev.Text
	}
	partition := p.ParsePartition()
	if p.Curr.Ok() {
		return p.ParseAsCommand(start)
	}
	return p.Expr(exp.TruncateTable, "expressions", nonNilList(expressions), "is_database", isDatabase, "exists", exists,
		"cluster", cluster, "identity", identity, "option", option, "partition", partition)
}

func (p *Parser) parseIndexedColumn() *exp.Expr { return p.ParseOrdered(p.ParseOpclass) }

func (p *Parser) parseWithOperator() *exp.Expr {
	this := p.ParseIndexedColumn()
	if !p.Match(tokens.With) {
		return this
	}
	rt := p.C.ReservedTokens
	op := p.ParseVar(true, &rt, false)
	return p.Expr(exp.WithOperator, "this", this, "op", op)
}

func (p *Parser) parseWrappedOptions() []*exp.Expr {
	p.Match(tokens.Eq)
	p.Match(tokens.LParen)
	opts := []*exp.Expr{}
	for p.Curr.Ok() && !p.Match(tokens.RParen) {
		var option any
		if p.MatchTextSeq("FORMAT_NAME", "=") {
			option = nilIfNil(p.ParseFormatName())
		} else {
			option = p.ParseProperty()
		}
		if option == nil {
			p.RaiseError("Unable to parse option")
			break
		}
		opts = append(opts, ExprList(option)...)
	}
	return opts
}

func (p *Parser) parseCopyParameters() []*exp.Expr {
	options := []*exp.Expr{}
	for p.Curr.Ok() && !p.Check(tokens.RParen) {
		option := p.ParseVar(true, nil, false)
		if option == nil {
			break
		}
		prev := p.PrevTextUpper()
		p.Match(tokens.Eq)
		p.Match(tokens.Alias)
		param := p.Expr(exp.CopyParameter, "this", option)
		if p.C.CopyIntoVarlenOptions[prev] && p.Check(tokens.LParen) {
			param.Set("expressions", p.ParseWrappedOptions())
		} else if prev == "FILE_FORMAT" {
			param.Set("expression", p.ParseField(false, nil, false))
		} else if prev == "FORMAT" && p.Prev.Type == tokens.Alias && p.MatchTexts("AVRO", "JSON") {
			param.Set("this", exp.NewVar("FORMAT AS "+p.PrevTextUpper()))
			param.Set("expression", p.ParseField(false, nil, false))
		} else {
			e := p.ParseUnquotedField()
			if e == nil {
				e = p.ParseBracket(nil)
			}
			param.Set("expression", e)
		}
		options = append(options, param)
		if p.D.CopyParamsAreCsv {
			p.Match(tokens.Comma)
		}
	}
	return options
}

func (p *Parser) parseCredentials() *exp.Expr {
	e := p.Expr(exp.Credentials)
	if p.MatchTextSeq("STORAGE_INTEGRATION", "=") {
		e.Set("storage", p.ParseField(false, nil, false))
	}
	if p.MatchTextSeq("CREDENTIALS") {
		if p.Match(tokens.Eq) {
			e.Set("credentials", p.ParseWrappedOptions())
		} else {
			e.Set("credentials", p.ParseField(false, nil, false))
		}
	}
	if p.MatchTextSeq("ENCRYPTION") {
		e.Set("encryption", p.ParseWrappedOptions())
	}
	if p.MatchTextSeq("IAM_ROLE") {
		if p.Match(tokens.Default) {
			e.Set("iam_role", exp.NewVar(p.Prev.Text))
		} else {
			e.Set("iam_role", p.ParseField(false, nil, false))
		}
	}
	if p.MatchTextSeq("REGION") {
		e.Set("region", p.ParseField(false, nil, false))
	}
	return e
}

func (p *Parser) parseFileLocation() *exp.Expr { return p.ParseField(false, nil, false) }

func (p *Parser) parseCopy() *exp.Expr {
	start := p.Prev
	p.Match(tokens.Into)
	var this *exp.Expr
	if p.Check(tokens.LParen) {
		this = p.ParseSelect(SelectOpts{Nested: true, NoSubqueryAlias: true})
	} else {
		this = p.ParseTable(TableOpts{Schema: true})
	}
	kind := p.Match(tokens.From) || !p.MatchTextSeq("TO")
	files := p.ParseCSV(p.ParseFileLocation)
	if p.Check(tokens.Eq) {
		p.AdvanceBy(-1)
		files = []*exp.Expr{}
	}
	credentials := p.ParseCredentials()
	p.MatchTextSeq("WITH")
	params := ParseWrapped(p, p.ParseCopyParameters, true)
	if p.Curr.Ok() {
		return p.ParseAsCommand(start)
	}
	return p.Expr(exp.Copy, "this", this, "kind", kind, "credentials", credentials, "files", nonNilList(files), "params", nonNilList(params))
}

func (p *Parser) parseNormalize() *exp.Expr {
	this := p.ParseBitwise()
	form := AndThen(p.Match(tokens.Comma), func() *exp.Expr { return p.ParseVar(false, nil, false) })
	return p.Expr(exp.Normalize, "this", this, "form", form)
}

func (p *Parser) parseCeilFloor(kind exp.Kind) *exp.Expr {
	args := p.ParseCSV(func() *exp.Expr { return p.ParseLambda(false) })
	this := seqGet(args, 0)
	decimals := seqGet(args, 1)
	var to *exp.Expr
	if p.MatchTextSeq("TO") {
		to = p.ParseVar(false, nil, false)
	}
	return exp.New(kind, "this", this, "decimals", decimals, "to", to)
}

func (p *Parser) parseStarOps() *exp.Expr {
	starToken := p.Prev
	if p.CheckTextSeq("COLUMNS", "(") {
		this := p.ParseFunction(FunctionOpts{})
		if this.Is(exp.Columns) {
			this.Set("unpack", true)
		}
		return this
	}
	index := p.Index
	var ilike *exp.Expr
	if p.Match(tokens.Ilike) {
		ilike = p.ParseString()
	}
	if ilike == nil {
		p.Retreat(index)
	}
	listOrNil := func(xs []*exp.Expr) any {
		if xs == nil {
			return nil
		}
		return xs
	}
	star := p.Expr(exp.Star, "ilike", ilike, "except_", listOrNil(p.ParseStarOp("EXCEPT", "EXCLUDE")),
		"replace", listOrNil(p.ParseStarOp("REPLACE")), "rename", listOrNil(p.ParseStarOp("RENAME")))
	updatePositionsTok(star, starToken)
	return star
}

func (p *Parser) parseGrantPrivilege() *exp.Expr {
	var parts []string
	for p.Curr.Ok() && !p.CheckSet(p.C.PrivilegeFollowTokens) {
		parts = append(parts, strings.ToUpper(p.Curr.Text))
		p.Advance()
	}
	if len(parts) == 0 {
		p.RaiseError("Expected privilege")
		return nil
	}
	this := exp.NewVar(strings.Join(parts, " "))
	var expressions any
	if p.Check(tokens.LParen) {
		expressions = p.ParseWrappedCSV(p.ParseColumn, false)
	}
	return p.Expr(exp.GrantPrivilege, "this", this, "expressions", expressions)
}

func (p *Parser) parseGrantPrincipal() *exp.Expr {
	var kind any = false
	if p.MatchTexts("ROLE", "GROUP") {
		kind = p.PrevTextUpper()
	}
	principal := p.ParseIdVar(true, nil)
	if principal == nil {
		return nil
	}
	return p.Expr(exp.GrantPrincipal, "this", principal, "kind", kind)
}

func (p *Parser) parseGrantRevokeCommon() ([]*exp.Expr, any, *exp.Expr) {
	privileges := p.ParseCSV(p.ParseGrantPrivilege)
	p.Match(tokens.On)
	var kind any
	if p.MatchSet(p.C.Creatables) {
		kind = p.PrevTextUpper()
	}
	securable := p.TryParse(func() *exp.Expr { return p.ParseTableParts(false, false, false, false) }, false)
	return privileges, kind, securable
}

func (p *Parser) parseGrant() *exp.Expr {
	start := p.Prev
	privileges, kind, securable := p.ParseGrantRevokeCommon()
	if securable == nil || !p.MatchTextSeq("TO") {
		return p.ParseAsCommand(start)
	}
	principals := p.ParseCSV(p.ParseGrantPrincipal)
	grantOption := p.MatchTextSeq("WITH", "GRANT", "OPTION")
	if p.Curr.Ok() {
		return p.ParseAsCommand(start)
	}
	return p.Expr(exp.Grant, "privileges", nonNilList(privileges), "kind", kind, "securable", securable,
		"principals", nonNilList(principals), "grant_option", grantOption)
}

func (p *Parser) parseRevoke() *exp.Expr {
	start := p.Prev
	grantOption := p.MatchTextSeq("GRANT", "OPTION", "FOR")
	privileges, kind, securable := p.ParseGrantRevokeCommon()
	if securable == nil || !p.MatchTextSeq("FROM") {
		return p.ParseAsCommand(start)
	}
	principals := p.ParseCSV(p.ParseGrantPrincipal)
	var cascade any
	if p.MatchTexts("CASCADE", "RESTRICT") {
		cascade = p.PrevTextUpper()
	}
	if p.Curr.Ok() {
		return p.ParseAsCommand(start)
	}
	return p.Expr(exp.Revoke, "privileges", nonNilList(privileges), "kind", kind, "securable", securable,
		"principals", nonNilList(principals), "grant_option", grantOption, "cascade", cascade)
}

func (p *Parser) parseOverlay() *exp.Expr {
	arg := func(text string) *exp.Expr {
		if p.Match(tokens.Comma) || p.MatchTextSeq(text) {
			return p.ParseBitwise()
		}
		return nil
	}
	this := p.ParseBitwise()
	expression := arg("PLACING")
	from := arg("FROM")
	forE := arg("FOR")
	return p.Expr(exp.Overlay, "this", this, "expression", expression, "from_", from, "for_", forE)
}

func (p *Parser) parseFormatName() *exp.Expr {
	value := p.ParseString()
	if value == nil {
		value = p.ParseTableParts(false, false, false, false)
	}
	return p.Expr(exp.Property, "this", exp.NewVar("FORMAT_NAME"), "value", value)
}

func (p *Parser) parseDistinctArgFunction(kind exp.Kind, distinctIndex int) *exp.Expr {
	isDistinct := p.Match(tokens.Distinct)
	if !isDistinct {
		p.Match(tokens.All)
	}
	args := []*exp.Expr{p.ParseLambda(false)}
	if p.Match(tokens.Comma) {
		args = append(args, p.ParseFunctionArgs(false)...)
	}
	target := seqGet(args, distinctIndex)
	if isDistinct && target != nil {
		args[distinctIndex] = p.Expr(exp.Distinct, "expressions", []*exp.Expr{target})
	}
	return exp.FromArgList(kind, args)
}

// IdentifierExpression mirrors _identifier_expression(token).
func (p *Parser) IdentifierExpression(token *tokens.Token) *exp.Expr {
	return p.IdentifierExpressionQuoted(token, false)
}

func (p *Parser) identifierExpressionQuoted(token *tokens.Token, quoted bool) *exp.Expr {
	if token == nil {
		token = p.Prev
	}
	var q any
	if quoted {
		q = true
	}
	return p.ExpressionTok(exp.New(exp.Identifier, "this", token.Text, "quoted", q), token)
}

func (p *Parser) buildPipeCte(query *exp.Expr, expressions []*exp.Expr, aliasCte *exp.Expr) *exp.Expr {
	var newCte any
	if aliasCte != nil {
		newCte = aliasCte
	} else {
		p.PipeCTECounter++
		newCte = fmt.Sprintf("__tmp%d", p.PipeCTECounter)
	}
	var ctes *exp.Expr
	if with := query.Get("with_"); with != nil {
		ctes = with.Pop()
	}
	args := make([]any, 0, len(expressions)+1)
	for _, e := range expressions {
		args = append(args, e)
	}
	args = append(args, exp.WithCopy(false))
	newSelect := exp.NewSelect(args...).From(newCte, exp.WithCopy(false))
	if ctes != nil {
		newSelect.Set("with_", ctes)
	}
	return newSelect.With(newCte, query, exp.WithCopy(false))
}

func (p *Parser) parsePipeSyntaxSelect(query *exp.Expr) *exp.Expr {
	sel := p.ParseSelect(SelectOpts{NoConsumePipe: true})
	if sel == nil {
		return query
	}
	args := []any{}
	for _, e := range sel.Expressions() {
		args = append(args, e)
	}
	args = append(args, exp.WithAppend(false))
	return p.BuildPipeCte(query.Select(args...), []*exp.Expr{exp.New(exp.Star)}, nil)
}

func pyNum(e *exp.Expr) *big.Rat {
	v, err := e.ToPy()
	if err != nil {
		return new(big.Rat)
	}
	switch x := v.(type) {
	case int:
		return new(big.Rat).SetInt64(int64(x))
	case *big.Int:
		return new(big.Rat).SetInt(x)
	case exp.Decimal:
		return x.Rat
	}
	return new(big.Rat)
}

func (p *Parser) parsePipeSyntaxLimit(query *exp.Expr) *exp.Expr {
	limit := p.ParseLimit(nil, false, false)
	offset := p.ParseOffset(nil)
	if limit.Is(exp.Limit) {
		currLimit := query.Get("limit")
		if currLimit == nil {
			currLimit = limit
		}
		currIsLimitAll := currLimit.Is(exp.Limit) && currLimit.IsLimitAll()
		var shouldReplace bool
		if limit.IsLimitAll() {
			shouldReplace = currIsLimitAll
		} else {
			shouldReplace = currIsLimitAll || pyNum(currLimit.Expression()).Cmp(pyNum(limit.Expression())) >= 0
		}
		if shouldReplace {
			query.Limit(limit, exp.WithCopy(false))
		}
	}
	if offset != nil {
		cur := new(big.Rat)
		if co := query.Get("offset"); co != nil {
			cur = pyNum(co.Expression())
		}
		sum := new(big.Rat).Add(cur, pyNum(offset.Expression()))
		var lit *exp.Expr
		if sum.IsInt() {
			lit = exp.LiteralNumber(sum.Num())
		} else {
			lit = exp.LiteralNumber(sum.FloatString(10))
		}
		query.Offset(lit, exp.WithCopy(false))
	}
	return query
}

func (p *Parser) parsePipeSyntaxAggregateFields() *exp.Expr {
	this := p.ParseDisjunction()
	if p.CheckTextSeq("GROUP", "AND") {
		return this
	}
	this = p.ParseAlias(this, false)
	if p.CheckSet(tokens.NewSet(tokens.Asc, tokens.Desc)) {
		return p.ParseOrdered(func() *exp.Expr { return this })
	}
	return this
}

func (p *Parser) parsePipeSyntaxAggregateGroupOrderBy(query *exp.Expr, groupByExists bool) *exp.Expr {
	expr := p.ParseCSV(p.ParsePipeSyntaxAggregateFields)
	var aggregatesOrGroups, orders []*exp.Expr
	for _, element := range expr {
		var this *exp.Expr
		if element.Is(exp.Ordered) {
			this = element.This()
			if this.Is(exp.Alias) {
				element.Set("this", this.Get("alias"))
			}
			orders = append(orders, element)
		} else {
			this = element
		}
		aggregatesOrGroups = append(aggregatesOrGroups, this)
	}
	toAny := func(xs []*exp.Expr, extra ...any) []any {
		out := make([]any, 0, len(xs)+len(extra))
		for _, x := range xs {
			out = append(out, x)
		}
		return append(out, extra...)
	}
	if groupByExists {
		sel := toAny(append(append([]*exp.Expr(nil), aggregatesOrGroups...), query.Expressions()...), exp.WithAppend(false), exp.WithCopy(false))
		var groups []any
		for _, proj := range aggregatesOrGroups {
			if a := proj.Get("alias"); a != nil {
				groups = append(groups, a)
			} else {
				groups = append(groups, proj)
			}
		}
		query.Select(sel...).GroupBy(append(groups, exp.WithCopy(false))...)
	} else {
		query.Select(toAny(aggregatesOrGroups, exp.WithAppend(false), exp.WithCopy(false))...)
	}
	if len(orders) > 0 {
		return query.OrderBy(toAny(orders, exp.WithAppend(false), exp.WithCopy(false))...)
	}
	return query
}

func (p *Parser) parsePipeSyntaxAggregate(query *exp.Expr) *exp.Expr {
	p.MatchTextSeq("AGGREGATE")
	query = p.ParsePipeSyntaxAggregateGroupOrderBy(query, false)
	if p.Match(tokens.GroupBy) || (p.MatchTextSeq("GROUP", "AND") && p.Match(tokens.OrderBy)) {
		query = p.ParsePipeSyntaxAggregateGroupOrderBy(query, true)
	}
	return p.BuildPipeCte(query, []*exp.Expr{exp.New(exp.Star)}, nil)
}

func (p *Parser) parsePipeSyntaxSetOperator(query *exp.Expr) *exp.Expr {
	firstSetop := p.ParseSetOperation(query, false, false)
	if firstSetop == nil {
		return nil
	}
	unwrap := func() *exp.Expr {
		e := p.ParseParen()
		if e == nil {
			return nil
		}
		return e.AssertIs(exp.Subquery).Unnest()
	}
	firstSetop.This().Pop()
	setops := []*exp.Expr{firstSetop.Expression().Pop().AssertIs(exp.Subquery).Unnest()}
	setops = append(setops, p.ParseCSV(unwrap)...)
	query = p.BuildPipeCte(query, []*exp.Expr{exp.New(exp.Star)}, nil)
	var ctes *exp.Expr
	if with := query.Get("with_"); with != nil {
		ctes = with.Pop()
	}
	distinct := firstSetop.Arg("distinct")
	extra := []any{}
	for _, a := range firstSetop.Args() {
		if a.Key != "distinct" {
			extra = append(extra, a.Key, a.Value)
		}
	}
	result := query
	for _, s := range setops {
		kv := append([]any{"this", result, "expression", s, "distinct", distinct}, extra...)
		result = exp.New(firstSetop.Kind(), kv...)
	}
	result.Set("with_", ctes)
	return p.BuildPipeCte(result, []*exp.Expr{exp.New(exp.Star)}, nil)
}

func (p *Parser) parsePipeSyntaxJoin(query *exp.Expr) *exp.Expr {
	join := p.ParseJoin(false, false, nil)
	if join == nil {
		return nil
	}
	if query.Is(exp.Select) {
		return query.Join(join, JoinOptsNone, exp.WithCopy(false))
	}
	return query
}

// JoinOptsNone is the zero exp.JoinOptions.
var JoinOptsNone = exp.JoinOptions{}

func (p *Parser) parsePipeSyntaxPivot(query *exp.Expr) *exp.Expr {
	pivots := p.ParsePivots()
	if len(pivots) == 0 {
		return query
	}
	if from := query.Get("from_"); from != nil {
		from.This().Set("pivots", pivots)
	}
	return p.BuildPipeCte(query, []*exp.Expr{exp.New(exp.Star)}, nil)
}

func (p *Parser) parsePipeSyntaxExtend(query *exp.Expr) *exp.Expr {
	p.MatchTextSeq("EXTEND")
	args := []any{exp.New(exp.Star)}
	for _, e := range p.ParseExpressions() {
		args = append(args, e)
	}
	args = append(args, exp.WithAppend(false), exp.WithCopy(false))
	query.Select(args...)
	return p.BuildPipeCte(query, []*exp.Expr{exp.New(exp.Star)}, nil)
}

func (p *Parser) parsePipeSyntaxTablesample(query *exp.Expr) *exp.Expr {
	sample := p.ParseTableSample(false)
	if with := query.Get("with_"); with != nil {
		exprs := with.Expressions()
		exprs[len(exprs)-1].This().Set("sample", sample)
	} else {
		query.Set("sample", sample)
	}
	return query
}

func (p *Parser) parsePipeSyntaxQuery(query *exp.Expr) *exp.Expr {
	if query.Is(exp.Subquery) {
		query = exp.NewSelect(exp.New(exp.Star)).From(query, exp.WithCopy(false))
	}
	if query.Get("from_") == nil {
		query = exp.NewSelect(exp.New(exp.Star)).From(query.Subquery(nil, false), exp.WithCopy(false))
	}
	for p.Match(tokens.PipeGt) {
		startIndex := p.Index
		startText := strings.ToUpper(p.Curr.Text)
		parser, ok := p.C.PipeSyntaxTransformParsers[startText]
		if !ok {
			parsed := p.ParsePipeSyntaxSetOperator(query)
			if parsed == nil {
				parsed = p.ParsePipeSyntaxJoin(query)
			}
			if parsed == nil {
				p.Retreat(startIndex)
				p.RaiseError(fmt.Sprintf("Unsupported pipe syntax operator: '%s'.", startText))
				break
			}
			query = parsed
		} else {
			query = parser(p, query)
		}
	}
	return query
}

func (p *Parser) parseDeclareitem() *exp.Expr {
	p.MatchTexts("VAR", "VARIABLE")
	vars := p.ParseCSV(func() *exp.Expr { return p.ParseIdVar(true, nil) })
	if len(vars) == 0 {
		return nil
	}
	p.Match(tokens.Alias)
	var kind *exp.Expr
	if p.Match(tokens.Table) {
		kind = p.ParseSchema(nil)
	} else {
		kind = p.ParseTypes(false, false, true, false)
	}
	def := AndThen(p.Match(tokens.Default) || p.Match(tokens.Eq), p.ParseBitwise)
	return p.Expr(exp.DeclareItem, "this", vars, "kind", kind, "default", def)
}

func (p *Parser) parseDeclare() *exp.Expr {
	start := p.Prev
	replace := p.MatchTextSeq("OR", "REPLACE")
	var expressions []*exp.Expr
	res := p.TryParse(func() *exp.Expr {
		expressions = p.ParseCSV(p.ParseDeclareitem)
		if len(expressions) == 0 {
			return nil
		}
		return expressions[0]
	}, false)
	if res == nil || p.Curr.Ok() {
		return p.ParseAsCommand(start)
	}
	return p.Expr(exp.Declare, "expressions", expressions, "replace", replace)
}
