package sqlglot

import (
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func (p *Parser) parseJoinHint(funcName string) *exp.Expr {
	args := p.ParseCSV(func() *exp.Expr { return p.ParseTable(TableOpts{}) })
	return exp.New(exp.JoinHint, "this", strings.ToUpper(funcName), "expressions", nonNilList(args))
}

func (p *Parser) parseSubstring() *exp.Expr {
	args := p.ParseCSV(p.ParseBitwise)
	var start, length *exp.Expr
	for p.Curr.Ok() {
		if p.Match(tokens.From) {
			start = p.ParseBitwise()
		} else if p.Match(tokens.For) {
			if start == nil {
				start = exp.LiteralNumber(1)
			}
			length = p.ParseBitwise()
		} else {
			break
		}
	}
	if start != nil {
		args = append(args, start)
	}
	if length != nil {
		args = append(args, length)
	}
	return p.ValidateExpression(exp.FromArgList(exp.Substring, args), args)
}

func (p *Parser) parseTrim() *exp.Expr {
	var position any
	var collation, expression *exp.Expr
	if p.MatchTextsIn(p.C.TrimTypes) {
		position = p.PrevTextUpper()
	}
	this := p.ParseBitwise()
	if p.MatchSet(tokens.NewSet(tokens.From, tokens.Comma)) {
		invertOrder := p.Prev.Type == tokens.From || p.C.TrimPatternFirst
		expression = p.ParseBitwise()
		if invertOrder {
			this, expression = expression, this
		}
	}
	if p.Match(tokens.Collate) {
		collation = p.ParseBitwise()
	}
	return p.Expr(exp.Trim, "this", this, "position", position, "expression", expression, "collation", collation)
}

func (p *Parser) parseWindowClause() []*exp.Expr {
	if p.Match(tokens.Window) {
		return p.ParseCSV(p.ParseNamedWindow)
	}
	return nil
}

func (p *Parser) parseNamedWindow() *exp.Expr { return p.ParseWindow(p.ParseIdVar(true, nil), true) }

func (p *Parser) parseRespectOrIgnoreNulls(this *exp.Expr) *exp.Expr {
	if p.Curr.Type == tokens.Var {
		if p.MatchTextSeq("IGNORE", "NULLS") {
			return p.Expr(exp.IgnoreNulls, "this", this)
		}
		if p.MatchTextSeq("RESPECT", "NULLS") {
			return p.Expr(exp.RespectNulls, "this", this)
		}
	}
	return this
}

func (p *Parser) parseHavingMax(this *exp.Expr) *exp.Expr {
	if p.Match(tokens.Having) {
		p.MatchTexts("MAX", "MIN")
		max := p.PrevTextUpper() != "MIN"
		return p.Expr(exp.HavingMax, "this", this, "expression", p.ParseColumn(), "max", max)
	}
	return this
}

func (p *Parser) parsePer(this *exp.Expr) *exp.Expr {
	if p.Next.Type == tokens.LParen && p.MatchTextSeq("PER") {
		return p.Expr(exp.Per, "this", this, "expressions", p.ParseWrappedCSV(p.ParseDisjunction, false))
	}
	return this
}

func (p *Parser) parseWindow(this *exp.Expr, alias bool) *exp.Expr {
	fn := this
	var comments []string
	if fn != nil {
		comments = fn.Comments
	}
	if p.C.SupportsNthValueFromModifier && this.Is(exp.NthValue) {
		if p.MatchTextSeq("FROM", "FIRST") {
			this.Set("from_first", true)
		} else if p.MatchTextSeq("FROM", "LAST") {
			this.Set("from_first", false)
		}
	}
	if p.MatchTextSeq("WITHIN", "GROUP") {
		order := p.ParseWrappedExpr(func() *exp.Expr { return p.ParseOrder(nil, false) }, false)
		this = p.Expr(exp.WithinGroup, "this", this, "expression", order)
	}
	if p.MatchPair(tokens.Filter, tokens.LParen) {
		p.Match(tokens.Where)
		this = p.Expr(exp.Filter, "this", this, "expression", p.ParseWhere(true))
		p.MatchRParen(nil)
	}
	if this.Is(exp.AggFunc) {
		ignoreRespect := FindInScope(this, exp.IgnoreNulls, exp.RespectNulls)
		if ignoreRespect != nil && ignoreRespect != this {
			ignoreRespect.Replace(ignoreRespect.This())
			this = p.Expr(ignoreRespect.Kind(), "this", this)
		}
	}
	this = p.ParseRespectOrIgnoreNulls(this)

	var over any
	if alias {
		p.Match(tokens.Alias)
	} else if !p.MatchSet(p.C.WindowBeforeParenTokens) {
		return p.ParsePer(this)
	} else {
		over = p.PrevTextUpper()
	}
	if len(comments) > 0 && fn != nil {
		fn.PopComments()
	}
	if !p.Match(tokens.LParen) {
		return p.ParsePer(p.ExpressionComments(exp.New(exp.Window, "this", this, "alias", p.ParseIdVar(false, nil), "over", over), comments))
	}
	wat := p.C.WindowAliasTokens
	windowAlias := p.ParseIdVar(false, &wat)
	var first any
	if p.Match(tokens.First) {
		first = true
	}
	if p.MatchTextSeq("LAST") {
		first = false
	}
	partition, order := p.ParsePartitionAndOrder()
	kind := ""
	if p.MatchSet(tokens.NewSet(tokens.Rows, tokens.Range)) || p.MatchTextSeq("GROUPS") {
		kind = p.Prev.Text
	}
	var spec *exp.Expr
	if kind != "" {
		p.Match(tokens.Between)
		start := p.ParseWindowSpec()
		var end WindowSpecPart
		if p.Match(tokens.And) {
			end = p.ParseWindowSpec()
		}
		var exclude *exp.Expr
		if p.MatchTextSeq("EXCLUDE") {
			exclude = p.ParseVarFromOptions(p.C.WindowExcludeOptions, true)
		}
		spec = p.Expr(exp.WindowSpec, "kind", kind, "start", start.Value, "start_side", start.Side,
			"end", end.Value, "end_side", end.Side, "exclude", exclude)
	}
	p.MatchRParen(nil)
	window := p.ExpressionComments(exp.New(exp.Window, "this", this, "partition_by", partition, "order", order,
		"spec", spec, "alias", windowAlias, "over", over, "first", first), comments)
	if p.CheckSet(p.C.WindowBeforeParenTokens) {
		return p.ParseWindow(window, alias)
	}
	return p.ParsePer(window)
}

func (p *Parser) parsePartitionAndOrder() ([]*exp.Expr, *exp.Expr) {
	return p.ParsePartitionBy(), p.ParseOrder(nil, false)
}

// WindowSpecPart is the result of ParseWindowSpec: value (string, expression or nil) and side.
type WindowSpecPart struct {
	Value any
	Side  any
}

func (p *Parser) parseWindowSpec() WindowSpecPart {
	p.Match(tokens.Between)
	var value any
	if p.MatchTextSeq("UNBOUNDED") {
		value = "UNBOUNDED"
	} else if p.MatchTextSeq("CURRENT", "ROW") {
		value = "CURRENT ROW"
	} else {
		value = nilIfNil(p.ParseBitwise())
	}
	var side any
	if p.MatchTextsIn(p.C.WindowSides) {
		side = p.Prev.Text
	}
	return WindowSpecPart{Value: value, Side: side}
}

func (p *Parser) parseAlias(this *exp.Expr, explicit bool) *exp.Expr {
	if p.CanParseLimitOrOffset() {
		return this
	}
	if p.CanParseNamedWindow() {
		return this
	}
	anyToken := p.Match(tokens.Alias)
	comments := append([]string(nil), p.PrevComments...)
	if explicit && !anyToken {
		return this
	}
	if p.Match(tokens.LParen) {
		aliases := p.ExpressionComments(exp.New(exp.Aliases, "this", this, "expressions",
			nonNilList(p.ParseCSV(func() *exp.Expr { return p.ParseIdVar(anyToken, nil) }))), comments)
		p.MatchRParen(aliases)
		return aliases
	}
	at := p.C.AliasTokens
	alias := p.ParseIdVar(anyToken, &at)
	if alias == nil && p.C.StringAliases {
		alias = p.ParseStringAsIdentifier()
	}
	if alias != nil {
		comments = append(comments, alias.PopComments()...)
		this = p.ExpressionComments(exp.New(exp.Alias, "this", this, "alias", alias), comments)
		column := this.This()
		if len(this.Comments) == 0 && column != nil && len(column.Comments) > 0 {
			this.Comments = column.PopComments()
		}
	}
	return this
}

func (p *Parser) parseIdVar(anyToken bool, toks *tokens.TokenSet) *exp.Expr {
	expression := p.ParseIdentifier()
	if expression == nil {
		set := p.C.IdVarTokens
		if toks != nil {
			set = *toks
		}
		if (anyToken && p.AdvanceAny(false) != nil) || p.MatchSet(set) {
			quoted := p.Prev.Type == tokens.String
			expression = p.IdentifierExpressionQuoted(nil, quoted)
		}
	}
	return expression
}

func (p *Parser) parseString() *exp.Expr {
	if MatchIn(p, p.C.StringParsers) {
		return p.C.StringParsers[p.Prev.Type](p, p.Prev)
	}
	return p.ParsePlaceholder()
}

func (p *Parser) parseStringAsIdentifier() *exp.Expr {
	if !p.Match(tokens.String) {
		return nil
	}
	output := exp.ToIdentifierQuoted(p.Prev.Text, true)
	updatePositionsTok(output, p.Prev)
	return output
}

func (p *Parser) parseNumber() *exp.Expr {
	if MatchIn(p, p.C.NumericParsers) {
		return p.C.NumericParsers[p.Prev.Type](p, p.Prev)
	}
	return p.ParsePlaceholder()
}

func (p *Parser) parseIdentifier() *exp.Expr {
	if p.Match(tokens.Identifier) {
		return p.IdentifierExpressionQuoted(nil, true)
	}
	return p.ParsePlaceholder()
}

func (p *Parser) parseVar(anyToken bool, toks *tokens.TokenSet, upper bool) *exp.Expr {
	if (anyToken && p.AdvanceAny(false) != nil) || p.Match(tokens.Var) || (toks != nil && p.MatchSet(*toks)) {
		text := p.Prev.Text
		if upper {
			text = strings.ToUpper(text)
		}
		return p.Expr(exp.Var, "this", text)
	}
	return p.ParsePlaceholder()
}

func (p *Parser) parseVarOrString(upper bool) *exp.Expr {
	if s := p.ParseString(); s != nil {
		return s
	}
	return p.ParseVar(true, nil, upper)
}

func (p *Parser) parsePrimaryOrVar() *exp.Expr {
	if pr := p.ParsePrimary(); pr != nil {
		return pr
	}
	return p.ParseVar(true, nil, false)
}

func (p *Parser) parseNull() *exp.Expr {
	if p.MatchSet(tokens.NewSet(tokens.Null, tokens.Unknown)) {
		return p.C.PrimaryParsers[tokens.Null](p, p.Prev)
	}
	return p.ParsePlaceholder()
}

func (p *Parser) parseBoolean() *exp.Expr {
	if p.Match(tokens.True) {
		return p.C.PrimaryParsers[tokens.True](p, p.Prev)
	}
	if p.Match(tokens.False) {
		return p.C.PrimaryParsers[tokens.False](p, p.Prev)
	}
	return p.ParsePlaceholder()
}

func (p *Parser) parseStar() *exp.Expr {
	if p.Match(tokens.Star) {
		return p.C.PrimaryParsers[tokens.Star](p, p.Prev)
	}
	return p.ParsePlaceholder()
}

func (p *Parser) parseParameter() *exp.Expr {
	this := p.ParseIdentifier()
	if this == nil {
		this = p.ParsePrimaryOrVar()
	}
	return p.Expr(exp.Parameter, "this", this)
}

func (p *Parser) parsePlaceholder() *exp.Expr {
	if MatchIn(p, p.C.PlaceholderParsers) {
		placeholder := p.C.PlaceholderParsers[p.Prev.Type](p)
		if placeholder != nil {
			return placeholder
		}
		p.AdvanceBy(-1)
	}
	return nil
}

func (p *Parser) parseStarOp(keywords ...string) []*exp.Expr {
	if !p.MatchTexts(keywords...) {
		return nil
	}
	if p.Check(tokens.LParen) {
		return p.ParseWrappedCSV(p.ParseExpression, false)
	}
	expression := p.ParseAlias(p.ParseDisjunction(), true)
	if expression != nil {
		return []*exp.Expr{expression}
	}
	return nil
}

func (p *Parser) parseWrappedIdVars(optional bool) []*exp.Expr {
	return p.ParseWrappedCSV(func() *exp.Expr { return p.ParseIdVar(true, nil) }, optional)
}

func (p *Parser) parseExpressions() []*exp.Expr { return p.ParseCSV(p.ParseExpression) }

func (p *Parser) parseSelectOrExpression(alias bool) *exp.Expr {
	var inner *exp.Expr
	if alias {
		inner = p.ParseAlias(p.ParseAssignment(), true)
	} else {
		inner = p.ParseAssignment()
	}
	if e := p.ParseSetOperations(inner); e != nil {
		return e
	}
	return p.ParseSelect(SelectOpts{})
}

func (p *Parser) parseDdlSelect() *exp.Expr {
	return p.ParseQueryModifiers(p.ParseSetOperations(p.ParseSelect(SelectOpts{Nested: true, NoSubqueryAlias: true})))
}
