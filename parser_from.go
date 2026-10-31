package sqlglot

import (
	"fmt"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func (p *Parser) parseTableAlias(aliasTokens *tokens.TokenSet) *exp.Expr {
	if p.CanParseLimitOrOffset() {
		return nil
	}
	if strings.ToUpper(p.Curr.Text) == "START" && strings.ToUpper(p.Next.Text) == "WITH" {
		return nil
	}
	anyToken := p.Match(tokens.Alias)
	toks := p.C.TableAliasTokens
	if aliasTokens != nil {
		toks = *aliasTokens
	}
	alias := p.ParseIdVar(anyToken, &toks)
	if alias == nil {
		alias = p.ParseStringAsIdentifier()
	}
	index := p.Index
	var columns []*exp.Expr
	if p.Match(tokens.LParen) {
		columns = p.ParseCSV(p.ParseFunctionParameter)
		if len(columns) > 0 {
			p.MatchRParen(nil)
		} else {
			p.Retreat(index)
		}
	}
	if alias == nil && len(columns) == 0 {
		return nil
	}
	var cols any
	if columns != nil {
		cols = columns
	}
	tableAlias := p.Expr(exp.TableAlias, "this", alias, "columns", cols)
	if alias.Is(exp.Identifier) {
		tableAlias.AddComments(alias.PopComments(), false)
	}
	return tableAlias
}

func (p *Parser) parseSubquery(this *exp.Expr, parseAlias bool) *exp.Expr {
	if this == nil {
		return nil
	}
	pivots := p.ParsePivots()
	var alias *exp.Expr
	if parseAlias {
		alias = p.ParseTableAlias(nil)
	}
	return p.Expr(exp.Subquery, "this", this, "pivots", pivotsArg(pivots), "alias", alias, "sample", p.ParseTableSample(false))
}

func (p *Parser) implicitUnnestsToExplicit(this *exp.Expr) *exp.Expr {
	refs := map[string]bool{NormalizeIdentifiers(this.Get("from_").This().Copy(), p.D).AliasOrName(): true}
	for _, join := range this.List("joins") {
		table := join.This()
		normalized := table.Copy()
		normalized.Meta()["maybe_column"] = true
		normalized = NormalizeIdentifiers(normalized, p.D)
		if table.Is(exp.Table) && join.Get("on") == nil {
			parts := normalized.Parts()
			if len(parts) > 1 && refs[parts[0].Name()] {
				tableAsColumn := table.ToColumnExpr(true)
				unnest := exp.New(exp.Unnest, "expressions", []*exp.Expr{tableAsColumn})
				if ta := table.Get("alias"); ta.Is(exp.TableAlias) {
					tableAsColumn.Replace(tableAsColumn.This())
					exp.NewTableAliasOf(unnest, nil, []any{ta.This()}, exp.WithCopy(false))
				}
				table.Replace(unnest)
			}
		}
		refs[normalized.AliasOrName()] = true
	}
	return this
}

func (p *Parser) parseQueryModifiers(this *exp.Expr) *exp.Expr {
	if this != nil && p.C.Modifiables.HasAny(this.Kind()) {
		for _, join := range p.ParseJoins() {
			this.Append("joins", join)
		}
		for lateral := p.ParseLateral(); lateral != nil; lateral = p.ParseLateral() {
			this.Append("laterals", lateral)
		}
		for {
			if CheckIn(p, p.C.QueryModifierParsers) {
				modifierToken := p.Curr
				if modifierToken.Type == tokens.Limit || modifierToken.Type == tokens.Fetch {
					if limit := this.Get("limit"); limit != nil && exp.Truthy(limit.MetaGet("top")) {
						break
					}
				}
				parser := p.C.QueryModifierParsers[modifierToken.Type]
				key, expression := parser(p)
				if exp.Truthy(expression) {
					if exp.Truthy(this.Arg(key)) {
						p.RaiseErrorAt(fmt.Sprintf("Found multiple '%s' clauses", strings.ToUpper(modifierToken.Text)), modifierToken)
					}
					this.Set(key, expression)
					if key == "limit" {
						lim := expression.(*exp.Expr)
						offset := lim.Get("offset")
						lim.Set("offset", nil)
						if offset != nil {
							if exp.Truthy(this.Arg("offset")) {
								p.RaiseErrorAt("Found multiple 'OFFSET' clauses", modifierToken)
							}
							off := exp.New(exp.Offset, "expression", offset)
							this.Set("offset", off)
							limitBy := lim.Expressions()
							lim.Set("expressions", nil)
							off.Set("expressions", limitBy)
						}
					}
					continue
				}
			}
			if strings.ToUpper(p.Curr.Text) == "START" {
				modifierToken := p.Curr
				connect := p.ParseConnect(false)
				if connect != nil {
					if exp.Truthy(this.Arg("connect")) {
						p.RaiseErrorAt("Found multiple 'START WITH' clauses", modifierToken)
					}
					this.Set("connect", connect)
					continue
				}
			}
			break
		}
	}
	if p.C.SupportsImplicitUnnest && this != nil && this.Get("from_") != nil {
		this = p.ImplicitUnnestsToExplicit(this)
	}
	return this
}

func (p *Parser) parseHintFallbackToString() *exp.Expr {
	start := p.Curr
	for p.Curr.Ok() {
		p.Advance()
	}
	end := p.Tokens[p.Index-1]
	return exp.New(exp.Hint, "expressions", []any{p.FindSQL(start, end)})
}

func (p *Parser) parseHintFunctionCall() *exp.Expr { return p.ParseFunctionCall(FunctionOpts{}) }

func (p *Parser) parseHintBody() *exp.Expr {
	startIndex := p.Index
	shouldFallback := false
	var hints []*exp.Expr
	err := p.Catch(func() {
		for {
			hint := p.ParseCSV(func() *exp.Expr {
				if h := p.ParseHintFunctionCall(); h != nil {
					return h
				}
				return p.ParseVar(false, nil, true)
			})
			if len(hint) == 0 {
				break
			}
			hints = append(hints, hint...)
		}
	})
	if err != nil {
		shouldFallback = true
	}
	if shouldFallback || p.Curr.Ok() {
		p.Retreat(startIndex)
		return p.ParseHintFallbackToString()
	}
	if hints == nil {
		hints = []*exp.Expr{}
	}
	return p.Expr(exp.Hint, "expressions", hints)
}

func (p *Parser) parseHint() *exp.Expr {
	if p.Match(tokens.Hint) && len(p.PrevComments) > 0 {
		e, err := ParseOneInto(p.PrevComments[0], exp.Hint, p.D)
		if err != nil {
			panic(parseBailoutFrom(err))
		}
		return e
	}
	return nil
}

func (p *Parser) parseInto() *exp.Expr {
	if !p.Match(tokens.Into) {
		return nil
	}
	temp := p.Match(tokens.Temporary)
	unlogged := p.MatchTextSeq("UNLOGGED")
	p.Match(tokens.Table)
	return p.Expr(exp.Into, "this", p.ParseTable(TableOpts{Schema: true}), "temporary", temp, "unlogged", unlogged)
}

func (p *Parser) parseFrom(joins, skipFromToken, consumePipe bool) *exp.Expr {
	if !skipFromToken && !p.Match(tokens.From) {
		return nil
	}
	comments := p.PrevComments
	return p.ExpressionComments(exp.New(exp.From, "this", p.ParseTable(TableOpts{Joins: joins, ConsumePipe: consumePipe})), comments)
}

func (p *Parser) parseMatchRecognizeMeasure() *exp.Expr {
	var frame any = false
	if p.MatchTexts("FINAL", "RUNNING") {
		frame = p.PrevTextUpper()
	}
	return p.Expr(exp.MatchRecognizeMeasure, "window_frame", frame, "this", p.ParseExpression())
}

func (p *Parser) parseMatchRecognize() *exp.Expr {
	if !p.Match(tokens.MatchRecognize) {
		return nil
	}
	p.MatchLParen(nil)
	partition := p.ParsePartitionBy()
	order := p.ParseOrder(nil, false)
	var measures any
	if p.MatchTextSeq("MEASURES") {
		measures = p.ParseCSV(p.ParseMatchRecognizeMeasure)
	}
	var rows *exp.Expr
	if p.MatchTextSeq("ONE", "ROW", "PER", "MATCH") {
		rows = exp.NewVar("ONE ROW PER MATCH")
	} else if p.MatchTextSeq("ALL", "ROWS", "PER", "MATCH") {
		text := "ALL ROWS PER MATCH"
		if p.MatchTextSeq("SHOW", "EMPTY", "MATCHES") {
			text += " SHOW EMPTY MATCHES"
		} else if p.MatchTextSeq("OMIT", "EMPTY", "MATCHES") {
			text += " OMIT EMPTY MATCHES"
		} else if p.MatchTextSeq("WITH", "UNMATCHED", "ROWS") {
			text += " WITH UNMATCHED ROWS"
		}
		rows = exp.NewVar(text)
	}
	var after *exp.Expr
	if p.MatchTextSeq("AFTER", "MATCH", "SKIP") {
		text := "AFTER MATCH SKIP"
		if p.MatchTextSeq("PAST", "LAST", "ROW") {
			text += " PAST LAST ROW"
		} else if p.MatchTextSeq("TO", "NEXT", "ROW") {
			text += " TO NEXT ROW"
		} else if p.MatchTextSeq("TO", "FIRST") || p.MatchTextSeq("TO", "LAST") {
			direction := p.PrevTextUpper()
			patternVar := p.AdvanceAny(false)
			if patternVar == nil {
				p.RaiseError(fmt.Sprintf("Expecting pattern variable after AFTER MATCH SKIP TO %s", direction))
			}
			text += fmt.Sprintf(" TO %s %s", direction, TokText(patternVar))
		}
		after = exp.NewVar(text)
	}
	var pattern *exp.Expr
	if p.MatchTextSeq("PATTERN") {
		p.MatchLParen(nil)
		if !p.Curr.Ok() {
			p.RaiseErrorAt("Expecting )", p.Curr)
		}
		paren := 1
		start := p.Curr
		end := p.Prev
		for p.Curr.Ok() && paren > 0 {
			if p.Curr.Type == tokens.LParen {
				paren++
			}
			if p.Curr.Type == tokens.RParen {
				paren--
			}
			end = p.Prev
			p.Advance()
		}
		if paren > 0 {
			p.RaiseErrorAt("Expecting )", p.Curr)
		}
		pattern = exp.NewVar(p.FindSQL(start, end))
	}
	var define any
	if p.MatchTextSeq("DEFINE") {
		define = p.ParseCSV(p.ParseNameAsExpression)
	}
	p.MatchRParen(nil)
	return p.Expr(exp.MatchRecognize, "partition_by", partition, "order", order, "measures", measures,
		"rows", rows, "after", after, "pattern", pattern, "define", define, "alias", p.ParseTableAlias(nil))
}

func (p *Parser) parseLateral() *exp.Expr {
	var crossApply any
	if p.MatchPair(tokens.Cross, tokens.Apply) {
		crossApply = true
	} else if p.MatchPair(tokens.Outer, tokens.Apply) {
		crossApply = false
	}
	var this *exp.Expr
	var view, outer any
	if crossApply != nil {
		this = p.ParseSelect(SelectOpts{Table: true})
	} else if p.Match(tokens.Lateral) {
		this = p.ParseSelect(SelectOpts{Table: true})
		view = p.Match(tokens.View)
		outer = p.Match(tokens.Outer)
	} else {
		return nil
	}
	if this == nil {
		this = p.ParseUnnest(true)
		if this == nil {
			this = p.ParseFunction(FunctionOpts{})
		}
		if this == nil {
			this = p.ParseIdVar(false, nil)
		}
		for p.Match(tokens.Dot) {
			e := p.ParseFunction(FunctionOpts{})
			if e == nil {
				e = p.ParseIdVar(false, nil)
			}
			this = exp.New(exp.Dot, "this", this, "expression", e)
		}
	}
	var ordinality any
	var tableAlias *exp.Expr
	if exp.Truthy(view) {
		table := p.ParseIdVar(false, nil)
		columns := []*exp.Expr{}
		if p.Match(tokens.Alias) {
			columns = p.ParseCSV(func() *exp.Expr { return p.ParseIdVar(true, nil) })
			if columns == nil {
				columns = []*exp.Expr{}
			}
		}
		tableAlias = p.Expr(exp.TableAlias, "this", table, "columns", columns)
	} else if this.IsAny(exp.Subquery, exp.Unnest) && this.Alias() != "" {
		tableAlias = this.Get("alias").Pop()
	} else {
		ordinality = p.MatchPair(tokens.With, tokens.Ordinality)
		tableAlias = p.ParseTableAlias(nil)
	}
	return p.Expr(exp.Lateral, "this", this, "view", view, "outer", outer, "alias", tableAlias,
		"cross_apply", crossApply, "ordinality", ordinality)
}

func (p *Parser) parseStream() *exp.Expr {
	index := p.Index
	if p.Match(tokens.Stream) {
		if this := p.TryParse(func() *exp.Expr { return p.ParseTable(TableOpts{}) }, false); this != nil {
			return p.Expr(exp.Stream, "this", this)
		}
		p.Retreat(index)
	}
	return nil
}

func (p *Parser) parseJoinParts() (*tokens.Token, *tokens.Token, *tokens.Token) {
	var method, side, kind *tokens.Token
	if p.MatchSet(p.C.JoinMethods) {
		method = p.Prev
	}
	if p.MatchSet(p.C.JoinSides) {
		side = p.Prev
	}
	if p.MatchSet(p.C.JoinKinds) {
		kind = p.Prev
	}
	return method, side, kind
}

func (p *Parser) parseUsingIdentifiers() []*exp.Expr {
	asIdentifier := func() *exp.Expr {
		this := p.ParseColumn()
		if this.Is(exp.Column) {
			return this.This()
		}
		return this
	}
	return p.ParseWrappedCSV(asIdentifier, true)
}

func (p *Parser) parseJoin(skipJoinToken, parseBracket bool, aliasTokens *tokens.TokenSet) *exp.Expr {
	if p.Match(tokens.Comma) {
		table := p.TryParse(func() *exp.Expr { return p.ParseTable(TableOpts{AliasTokens: aliasTokens}) }, false)
		var crossJoin *exp.Expr
		if table != nil {
			crossJoin = p.Expr(exp.Join, "this", table)
		}
		if crossJoin != nil && p.C.JoinsHaveEqualPrecedence {
			crossJoin.Set("kind", "CROSS")
		}
		return crossJoin
	}
	index := p.Index
	method, side, kind := p.ParseJoinParts()
	directed := p.C.SupportsDirectedJoin && p.MatchTextSeq("DIRECTED")
	var hint any
	if p.MatchTextsIn(p.C.JoinHints) {
		hint = p.Prev.Text
	}
	join := p.Match(tokens.Join) || (kind != nil && kind.Type == tokens.StraightJoin)
	joinComments := p.PrevComments
	if !skipJoinToken && !join {
		p.Retreat(index)
		kind, method, side = nil, nil, nil
	}
	outerApply := p.CheckPair(tokens.Outer, tokens.Apply)
	crossApply := p.CheckPair(tokens.Cross, tokens.Apply)
	if !skipJoinToken && !join && !outerApply && !crossApply {
		return nil
	}
	var kw KW
	kw.Set("this", p.ParseTable(TableOpts{ParseBracket: parseBracket, AliasTokens: aliasTokens}))
	if kind != nil && kind.Type == tokens.Array && p.Match(tokens.Comma) {
		kw.Set("expressions", p.ParseCSV(func() *exp.Expr {
			return p.ParseTable(TableOpts{ParseBracket: parseBracket, AliasTokens: aliasTokens})
		}))
	}
	if method != nil {
		kw.Set("method", strings.ToUpper(method.Text))
	}
	if side != nil {
		kw.Set("side", strings.ToUpper(side.Text))
	}
	if kind != nil {
		kw.Set("kind", strings.ToUpper(kind.Text))
	}
	if hint != nil {
		kw.Set("hint", hint)
	}
	if p.Match(tokens.MatchCondition) {
		kw.Set("match_condition", p.ParseWrappedExpr(func() *exp.Expr { return p.ParseComparison(nil) }, false))
	}
	this, _ := kw.Get("this").(*exp.Expr)
	if p.Match(tokens.On) {
		kw.Set("on", nilIfNil(p.ParseDisjunction()))
	} else if p.Match(tokens.Using) {
		kw.Set("using", p.ParseUsingIdentifiers())
	} else if method == nil && !(outerApply || crossApply) && !this.Is(exp.Unnest) &&
		!(kind != nil && (kind.Type == tokens.Cross || kind.Type == tokens.Array)) {
		index := p.Index
		joins := p.ParseJoinsWith(aliasTokens)
		if len(joins) > 0 && p.Match(tokens.On) {
			kw.Set("on", nilIfNil(p.ParseDisjunction()))
		} else if len(joins) > 0 && p.Match(tokens.Using) {
			kw.Set("using", p.ParseUsingIdentifiers())
		} else {
			joins = nil
			p.Retreat(index)
		}
		if len(joins) > 0 {
			this.Set("joins", joins)
		} else {
			this.Set("joins", nil)
		}
	}
	kw.Set("pivots", pivotsArg(p.ParsePivots()))

	var comments []string
	comments = append(comments, joinComments...)
	for _, t := range []*tokens.Token{method, side, kind} {
		if t != nil {
			comments = append(comments, t.Comments...)
		}
	}
	if p.C.AddJoinOnTrue && !exp.Truthy(kw.Get("on")) && !exp.Truthy(kw.Get("using")) && !exp.Truthy(kw.Get("method")) {
		k := kw.Get("kind")
		if k == nil || k == "INNER" || k == "OUTER" {
			kw.Set("on", exp.NewTrue())
		}
	}
	if directed {
		kw.Set("directed", directed)
	}
	return p.ExpressionComments(exp.New(exp.Join, kw.KV()...), comments)
}

func (p *Parser) parseOpclass() *exp.Expr {
	this := p.ParseDisjunction()
	if p.CheckTextsIn(p.C.OpclassFollowKeywords) {
		return this
	}
	if !p.CheckSet(p.C.OptypeFollowTokens) {
		return p.Expr(exp.Opclass, "this", this, "expression", p.ParseTableParts(false, false, false, false))
	}
	return this
}
