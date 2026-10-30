package sqlglot

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

// FunctionOpts are the optional arguments of ParseFunction / ParseFunctionCall.
type FunctionOpts struct {
	Functions        map[string]FuncBuilder
	Anonymous        bool
	NoOptionalParens bool // optional_parens=False
	AnyToken         bool
}

func (p *Parser) parseAtom() *exp.Expr {
	if p.C.IdentifierTokens.Has(p.Curr.Type) {
		if column := p.ParseColumn(); column != nil {
			return column
		}
	}
	token := p.Curr
	tt := token.Type
	primaryParser, ok := p.C.PrimaryParsers[tt]
	if !ok {
		return nil
	}
	nextType := p.Next.Type
	if _, isOp := p.C.ColumnOperators[nextType]; isOp || p.C.ColumnPostfixTokens.Has(nextType) || (tt == tokens.String && nextType == tokens.String) {
		return nil
	}
	p.Advance()
	return primaryParser(p, token)
}

func (p *Parser) parseColumn() *exp.Expr {
	column := p.ParseColumnPartsFast()
	if column == nil {
		this := p.ParseColumnReference()
		if this == nil {
			this = p.ParseBracket(this)
		}
		if this != nil {
			column = p.ParseColumnOps(this)
		}
	}
	if column != nil {
		if p.D.SupportsColumnJoinMarks && p.Match(tokens.JoinMarker) {
			column.Set("join_mark", true)
		}
		if p.C.ColonIsVariantExtract {
			column = p.ParseColonAsVariantExtract(column)
		}
	}
	return column
}

func (p *Parser) parseColumnPartsFast() *exp.Expr {
	index := p.Index
	var parts []*exp.Expr
	var allComments []string
	for p.MatchSet(p.C.IdentifierTokens) {
		token := p.Prev
		comments := p.PrevComments
		if parts == nil {
			if _, ok := p.NoParenFunctionParser(strings.ToUpper(token.Text)); ok {
				p.Retreat(index)
				return nil
			}
		}
		hasDot := p.Match(tokens.Dot)
		currTT := p.Curr.Type
		if !hasDot {
			if _, ok := p.C.ColumnOperators[currTT]; ok || p.C.ColumnPostfixTokens.Has(currTT) {
				p.Retreat(index)
				return nil
			}
		} else if !p.C.IdentifierTokens.Has(currTT) {
			p.Retreat(index)
			return nil
		}
		if len(comments) > 0 {
			allComments = append(allComments, comments...)
			p.PrevComments = []string{}
		}
		parts = append(parts, p.ExpressionTok(exp.New(exp.Identifier, "this", token.Text, "quoted", token.Type == tokens.Identifier), token))
		if !hasDot {
			break
		}
	}
	if parts == nil {
		return nil
	}
	var column *exp.Expr
	switch n := len(parts); {
	case n == 1:
		column = exp.New(exp.Column, "this", parts[0])
	case n == 2:
		column = exp.New(exp.Column, "this", parts[1], "table", parts[0])
	case n == 3:
		column = exp.New(exp.Column, "this", parts[2], "table", parts[1], "db", parts[0])
	default:
		column = exp.New(exp.Column, "this", parts[3], "table", parts[2], "db", parts[1], "catalog", parts[0])
		for i := 4; i < n; i++ {
			column = exp.New(exp.Dot, "this", column, "expression", parts[i])
		}
	}
	if len(allComments) > 0 {
		column.AddComments(allComments, false)
	}
	return column
}

func (p *Parser) parseColumnReference() *exp.Expr {
	this := p.ParseField(false, nil, false)
	if this == nil && p.Check(tokens.Values) && p.C.ValuesFollowedByParen && (!p.Next.Ok() || p.Next.Type != tokens.LParen) {
		this = p.ParseIdVar(true, nil)
	}
	if this.Is(exp.Identifier) {
		this = p.ExpressionComments(exp.New(exp.Column, "this", this), this.PopComments())
	}
	return this
}

func (p *Parser) buildJsonExtract(this *exp.Expr, pathParts []*exp.Expr) (*exp.Expr, []*exp.Expr) {
	if len(pathParts) > 1 {
		this = p.Expr(exp.JSONExtract, "this", this, "expression", exp.New(exp.JSONPath, "expressions", pathParts),
			"variant_extract", true, "requires_json", p.C.JsonExtractRequiresJsonExpression)
		pathParts = []*exp.Expr{exp.New(exp.JSONPathRoot)}
	}
	return this, pathParts
}

func (p *Parser) parseColonAsVariantExtract(this *exp.Expr) *exp.Expr {
	pathParts := []*exp.Expr{exp.New(exp.JSONPathRoot)}
	sel := tokens.NewSet(tokens.Select)
	for p.Match(tokens.Colon) {
		if !p.C.ColonChainIsSingleExtract {
			this, pathParts = p.BuildJsonExtract(this, pathParts)
		}
		key := p.ParseIdVar(true, &sel)
		if key != nil {
			quoted := key.Is(exp.Identifier) && key.Quoted()
			pathParts = append(pathParts, exp.New(exp.JSONPathKey, "this", key.Name(), "quoted", quoted))
		}
		for {
			if p.Match(tokens.Dot) {
				nextKey := p.ParseIdVar(true, &sel)
				if nextKey != nil {
					quoted := nextKey.Is(exp.Identifier) && nextKey.Quoted()
					pathParts = append(pathParts, exp.New(exp.JSONPathKey, "this", nextKey.Name(), "quoted", quoted))
				}
			} else if p.Match(tokens.LBracket) {
				bracketExpr := p.ParseBracketKeyValue(false)
				if !p.Match(tokens.RBracket) {
					p.RaiseError("Expected ]")
				}
				if bracketExpr != nil {
					switch {
					case bracketExpr.IsString():
						pathParts = append(pathParts, exp.New(exp.JSONPathKey, "this", bracketExpr.Name(), "quoted", true))
					case bracketExpr.IsStar():
						pathParts = append(pathParts, exp.New(exp.JSONPathSubscript, "this", exp.New(exp.JSONPathWildcard)))
					case bracketExpr.IsNumber():
						pathParts = append(pathParts, exp.New(exp.JSONPathSubscript, "this", bracketExpr.MustToPy()))
					default:
						this, pathParts = p.BuildJsonExtract(this, pathParts)
						this = p.Expr(exp.Bracket, "this", this, "expressions", []*exp.Expr{bracketExpr}, "json_access", true)
					}
				}
			} else if p.Match(tokens.Dcolon) {
				this, pathParts = p.BuildJsonExtract(this, pathParts)
				castType := p.ParseTypes(false, false, true, false)
				if castType != nil {
					this = p.Expr(exp.Cast, "this", this, "to", castType)
				} else {
					p.RaiseError("Expected type after '::'")
				}
			} else {
				break
			}
		}
	}
	this, _ = p.BuildJsonExtract(this, pathParts)
	return this
}

func (p *Parser) parseDcolon() *exp.Expr { return p.ParseTypes(false, false, true, false) }

func (p *Parser) parseColumnOps(this *exp.Expr) *exp.Expr {
	for p.C.Brackets.Has(p.Curr.Type) {
		this = p.ParseBracket(this)
	}
	for p.Curr.Ok() {
		opToken := p.Curr.Type
		op, ok := p.C.ColumnOperators[opToken]
		if !ok {
			break
		}
		p.Advance()
		var field *exp.Expr
		if p.C.CastColumnOperators.Has(opToken) {
			field = p.ParseDcolon()
			if field == nil {
				p.RaiseError("Expected type")
			}
		} else if op != nil && p.Curr.Ok() {
			field = p.ParseColumnReference()
			if field == nil {
				field = p.ParseBitwise()
			}
			if field.Is(exp.Column) && p.Check(tokens.Dot) {
				field = p.ParseColumnOps(field)
			}
		} else {
			dot := p.IsConnected() && p.Prev.Type == tokens.Dot
			field = p.ParseField(true, nil, true)
			if dot && field.IsAny(exp.Null, exp.Boolean) {
				field = p.ExpressionComments(exp.New(exp.Identifier, "this", p.Prev.Text), field.Comments)
			}
		}
		if field.IsAny(exp.Func, exp.Window) && this != nil {
			this = this.Transform(func(n *exp.Expr) *exp.Expr {
				if n.Is(exp.Column) {
					return n.ToDot(false)
				}
				return n
			}, true)
		}
		if op != nil {
			this = op(p, this, field)
		} else if this.Is(exp.Column) && this.Get("catalog") == nil {
			this = p.ExpressionComments(exp.New(exp.Column, "this", field, "table", this.This(), "db", this.Get("table"), "catalog", this.Get("db")), this.Comments)
		} else if field.Is(exp.Window) {
			windowFunc := p.Expr(exp.Dot, "this", this, "expression", field.This())
			field.Set("this", windowFunc)
			this = field
		} else {
			this = p.Expr(exp.Dot, "this", this, "expression", field)
		}
		if field != nil && len(field.Comments) > 0 {
			this.AddComments(field.PopComments(), false)
		}
		this = p.ParseBracket(this)
	}
	return this
}

func (p *Parser) parseParen() *exp.Expr {
	if !p.Match(tokens.LParen) {
		return nil
	}
	comments := p.PrevComments
	query := p.ParseSelect(SelectOpts{})
	var expressions []*exp.Expr
	if query != nil {
		expressions = []*exp.Expr{query}
	} else {
		expressions = p.ParseExpressions()
	}
	var this *exp.Expr
	if len(expressions) > 0 {
		this = expressions[0]
	}
	if this == nil && p.Check(tokens.RParen) {
		this = p.Expr(exp.Tuple)
	} else if len(expressions) > 1 || p.Prev.Type == tokens.Comma {
		this = p.Expr(exp.Tuple, "expressions", expressions)
	} else if this.IsAny(exp.Select, exp.SetOperation) {
		this = p.ParseSubquery(this, false)
	} else if this.IsAny(exp.Subquery, exp.Values) {
		this = p.ParseSubquery(p.ParseQueryModifiers(p.ParseSetOperations(this)), false)
	} else {
		this = p.Expr(exp.Paren, "this", this)
	}
	if this != nil {
		this.AddComments(comments, false)
	}
	p.MatchRParen(this)
	if this.Is(exp.Paren) && this.This().Is(exp.AggFunc) {
		return p.ParseWindow(this, false)
	}
	return this
}

func (p *Parser) parsePrimary() *exp.Expr {
	if MatchIn(p, p.C.PrimaryParsers) {
		tt := p.Prev.Type
		primary := p.C.PrimaryParsers[tt](p, p.Prev)
		if tt == tokens.String {
			expressions := []*exp.Expr{primary}
			for p.Check(tokens.String) {
				if p.IsConnected() && p.C.AdjacentStringsCannotBeConnected {
					p.RaiseError("Adjacent string literals need to be separated by whitespace or comments")
				}
				p.Advance()
				expressions = append(expressions, exp.LiteralString(p.Prev.Text))
			}
			if len(expressions) > 1 {
				return p.Expr(exp.Concat, "expressions", expressions, "coalesce", p.D.ConcatCoalesce)
			}
		}
		return primary
	}
	if p.MatchPair(tokens.Dot, tokens.Number) {
		return exp.LiteralNumber("0." + p.Prev.Text)
	}
	return p.ParseParen()
}

func (p *Parser) parseField(anyToken bool, toks *tokens.TokenSet, anonymousFunc bool) *exp.Expr {
	afterDot := p.C.SupportsDigitPrefixedFieldNames && p.Prev.Type == tokens.Dot
	var field *exp.Expr
	if anonymousFunc {
		field = p.ParseFunction(FunctionOpts{Anonymous: anonymousFunc, AnyToken: anyToken})
		if field == nil {
			field = p.ParsePrimary()
		}
	} else {
		field = p.ParsePrimary()
		if field == nil {
			field = p.ParseFunction(FunctionOpts{Anonymous: anonymousFunc, AnyToken: anyToken})
		}
	}
	if field == nil {
		field = p.ParseIdVar(anyToken, toks)
	}
	if afterDot && field.Is(exp.Literal) && field.IsNumber() {
		name := field.Name()
		if p.IsConnected() && p.ParseVar(true, nil, false) != nil {
			name += p.Prev.Text
		}
		field = exp.New(exp.Identifier, "this", name, "quoted", true).UpdatePositionsFrom(field)
	}
	return field
}

func (p *Parser) parseFunction(o FunctionOpts) *exp.Expr {
	fnSyntax := false
	if p.Check(tokens.LBrace) && p.Next.Ok() && strings.ToUpper(p.Next.Text) == "FN" {
		p.AdvanceBy(2)
		fnSyntax = true
	}
	f := p.ParseFunctionCall(o)
	if fnSyntax {
		p.Match(tokens.RBrace)
	}
	return f
}

func (p *Parser) parseFunctionArgs(alias bool) []*exp.Expr {
	return p.ParseCSV(func() *exp.Expr { return p.ParseLambda(alias) })
}

func (p *Parser) parseConnectorFunction(connector func(args ...any) *exp.Expr) *exp.Expr {
	args := p.ParseFunctionArgs(false)
	if len(args) == 0 {
		p.RaiseError("Expected at least one argument")
	}
	in := make([]any, 0, len(args)+1)
	for _, a := range args {
		in = append(in, a)
	}
	in = append(in, exp.WithCopy(false))
	return exp.New(exp.Paren, "this", connector(in...))
}

func (p *Parser) parseFunctionCall(o FunctionOpts) *exp.Expr {
	if !p.Curr.Ok() {
		return nil
	}
	optionalParens := !o.NoOptionalParens
	comments := p.Curr.Comments
	prev := p.Prev
	token := p.Curr
	tt := p.Curr.Type
	var this any = p.Curr.Text
	upper := strings.ToUpper(p.Curr.Text)
	afterDot := prev.Type == tokens.Dot

	noParenParser, hasNoParen := p.NoParenFunctionParser(upper)
	if optionalParens && hasNoParen && !p.C.InvalidFuncNameTokens.Has(tt) && !afterDot {
		p.Advance()
		return p.ParseWindow(noParenParser(p), false)
	}
	if p.Next.Type != tokens.LParen {
		if optionalParens && !afterDot {
			if kind, ok := p.C.NoParenFunctions[tt]; ok {
				p.Advance()
				return p.Expression(exp.New(kind))
			}
		}
		return nil
	}
	if o.AnyToken {
		if p.C.ReservedTokens.Has(tt) {
			return nil
		}
	} else if !p.C.FuncTokens.Has(tt) {
		return nil
	}
	p.AdvanceBy(2)

	var result *exp.Expr
	parser, hasParser := p.C.FunctionParsers[upper]
	if hasParser && !o.Anonymous {
		result = parser(p)
	} else {
		if subqueryPredicate, ok := p.C.SubqueryPredicates[tt]; ok {
			var e *exp.Expr
			if p.C.SubqueryTokens.Has(p.Curr.Type) {
				e = p.ParseSelect(SelectOpts{})
				p.MatchRParen(nil)
			} else if prev.Ok() && (prev.Type == tokens.Like || prev.Type == tokens.Ilike) {
				p.AdvanceBy(-1)
				e = p.ParseBitwise()
			}
			if e != nil {
				return p.ExpressionComments(exp.New(subqueryPredicate, "this", e), comments)
			}
		}
		functions := o.Functions
		if functions == nil {
			functions = p.C.Functions
		}
		function, isKnown := functions[upper]
		knownFunction := isKnown && function != nil && !o.Anonymous
		alias := !knownFunction || p.C.FunctionsWithAliasedArgs[upper]
		args := p.ParseFunctionArgs(alias)
		var postFuncComments []string
		if p.Curr.Ok() {
			postFuncComments = p.Curr.Comments
		}
		if knownFunction && len(postFuncComments) > 0 {
			for _, c := range postFuncComments {
				if strings.HasPrefix(strings.TrimLeft(c, " \t\n\r\f\v"), exp.SQLGlotAnonymous) {
					knownFunction = false
					break
				}
			}
		}
		if alias && knownFunction {
			args = p.KvToPropEq(args, false)
		}
		if knownFunction {
			f := function(args, p.D)
			f = p.ValidateExpression(f, args)
			if nameKey := p.D.OriginalNameMetaKey; nameKey != "" {
				f.Meta()[nameKey] = this
			}
			result = f
		} else {
			if tt == tokens.Identifier {
				id := exp.New(exp.Identifier, "this", this, "quoted", true)
				updatePositionsTok(id, token)
				this = id
			}
			if args == nil {
				args = []*exp.Expr{}
			}
			result = p.Expr(exp.Anonymous, "this", this, "expressions", args)
		}
		if result != nil {
			updatePositionsTok(result, token)
		}
	}
	if result != nil {
		result.AddComments(comments, false)
	}
	if hasParser {
		p.MatchExpr(tokens.RParen, result)
	} else {
		p.MatchRParen(result)
	}
	return p.ParseWindow(result, false)
}

func (p *Parser) toPropEq(expression *exp.Expr, index int) *exp.Expr { return expression }

func (p *Parser) kvToPropEq(expressions []*exp.Expr, parseMap bool) []*exp.Expr {
	var transformed []*exp.Expr
	for index, e := range expressions {
		if p.C.KeyValueDefinitions.HasAny(e.Kind()) {
			if e.Is(exp.Alias) {
				e = p.Expr(exp.PropertyEQ, "this", e.Get("alias"), "expression", e.This())
			}
			if !e.Is(exp.PropertyEQ) {
				var this *exp.Expr
				if parseMap {
					this = e.This()
				} else {
					this = exp.ToIdentifier(e.This().Name())
				}
				e = p.Expr(exp.PropertyEQ, "this", this, "expression", e.Expression())
			}
			if !parseMap && e.This().Is(exp.Column) {
				e.This().Replace(e.This().This())
			}
		} else {
			e = p.ToPropEq(e, index)
		}
		transformed = append(transformed, e)
	}
	if transformed == nil && expressions != nil {
		transformed = []*exp.Expr{}
	}
	return transformed
}

func (p *Parser) parseFunctionProperties() *exp.Expr {
	var properties []*exp.Expr
	for {
		var keyword string
		var prop any
		if MatchTextsKey(p, p.C.PropertyParsers) {
			keyword = p.PrevTextUpper()
			prop = p.C.PropertyParsers[keyword].Fn(p, nil)
		} else if p.Match(tokens.Default) && MatchTextsKey(p, p.C.PropertyParsers) {
			keyword = p.PrevTextUpper()
			pp := p.C.PropertyParsers[keyword]
			if !pp.AcceptsKwargs {
				p.RaiseError(fmt.Sprintf("Cannot parse property '%s'", p.Prev.Text))
				break
			}
			prop = pp.Fn(p, map[string]any{"default": true})
		} else {
			break
		}
		if !exp.Truthy(prop) || isNilAny(prop) {
			p.RaiseError(fmt.Sprintf("Failed to parse property '%s'", keyword))
			break
		}
		properties = append(properties, ExprList(prop)...)
	}
	if len(properties) > 0 {
		return p.Expr(exp.Properties, "expressions", properties)
	}
	return nil
}

func isNilAny(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

func (p *Parser) parseUserDefinedFunctionExpression() *exp.Expr { return p.ParseStatement() }

func (p *Parser) parseFunctionParameter() *exp.Expr {
	return p.ParseColumnDef(p.ParseIdVar(true, nil), false)
}

func (p *Parser) parseUserDefinedFunction(kind tokens.TokenType) *exp.Expr {
	this := p.ParseTableParts(true, false, false, false)
	if !p.Match(tokens.LParen) {
		return this
	}
	expressions := p.ParseCSV(p.ParseFunctionParameter)
	p.MatchRParen(nil)
	if expressions == nil {
		expressions = []*exp.Expr{}
	}
	return p.Expr(exp.UserDefinedFunction, "this", this, "expressions", expressions, "wrapped", true)
}

func (p *Parser) parseMacroOverloads(this, firstBody *exp.Expr, firstIsTable bool) *exp.Expr {
	var firstExprs any
	if len(this.Expressions()) > 0 {
		firstExprs = this.Expressions()
	}
	overloads := []*exp.Expr{p.Expr(exp.MacroOverload, "this", firstBody, "expressions", firstExprs, "is_table", firstIsTable)}
	this.Set("expressions", nil)
	this.Set("wrapped", false)
	for p.Match(tokens.Comma) {
		if !p.Match(tokens.LParen) {
			break
		}
		params := p.ParseCSV(p.ParseFunctionParameter)
		p.MatchRParen(nil)
		if !p.Match(tokens.Alias) {
			break
		}
		isTable := p.Match(tokens.Table)
		body := p.ParseExpression()
		if params == nil {
			params = []*exp.Expr{}
		}
		overloads = append(overloads, p.Expression(exp.New(exp.MacroOverload, "this", body, "expressions", params, "is_table", isTable)))
	}
	return p.Expr(exp.MacroOverloads, "expressions", overloads)
}

func (p *Parser) parseIntroducer(token *tokens.Token) *exp.Expr {
	literal := p.ParsePrimary()
	if literal != nil {
		return p.ExpressionTok(exp.New(exp.Introducer, "this", token.Text, "expression", literal), token)
	}
	return p.IdentifierExpression(token)
}

func (p *Parser) parseSessionParameter() *exp.Expr {
	var kind any
	this := p.ParseIdVar(true, nil)
	if this == nil {
		this = p.ParsePrimary()
	}
	if this != nil && p.Match(tokens.Dot) {
		kind = this.Name()
		this = p.ParseVar(false, nil, false)
		if this == nil {
			this = p.ParsePrimary()
		}
	}
	return p.Expr(exp.SessionParameter, "this", this, "kind", kind)
}

func (p *Parser) parseLambdaArg() *exp.Expr { return p.ParseIdVar(true, nil) }

func (p *Parser) parseLambda(alias bool) *exp.Expr {
	nextTT := p.Next.Type
	if p.C.LambdaArgTerminators.Has(nextTT) {
		if atom := p.ParseAtom(); atom != nil {
			return atom
		}
	}
	index := p.Index
	if p.Match(tokens.LParen) {
		expressions := p.ParseCSV(p.ParseLambdaArg)
		if !p.Match(tokens.RParen) {
			p.Retreat(index)
		} else if MatchIn(p, p.C.Lambdas) {
			return p.C.Lambdas[p.Prev.Type](p, expressions)
		} else {
			p.Retreat(index)
		}
	} else if _, ok := p.C.Lambdas[nextTT]; p.C.TypedLambdaArgs || ok {
		expressions := []*exp.Expr{p.ParseLambdaArg()}
		if MatchIn(p, p.C.Lambdas) {
			return p.C.Lambdas[p.Prev.Type](p, expressions)
		}
		p.Retreat(index)
	}
	var this *exp.Expr
	if p.Match(tokens.Distinct) {
		this = p.Expr(exp.Distinct, "expressions", p.ParseCSV(p.ParseDisjunction))
	} else {
		p.Match(tokens.All)
		this = p.ParseSelectOrExpression(alias)
	}
	return p.ParseLimit(p.ParseRespectOrIgnoreNulls(p.ParseOrder(p.ParseHavingMax(p.ParseRespectOrIgnoreNulls(this)), false)), false, false)
}

func (p *Parser) parseSchema(this *exp.Expr) *exp.Expr {
	index := p.Index
	if !p.Match(tokens.LParen) {
		return this
	}
	if p.MatchSet(p.C.SelectStartTokens) {
		p.Retreat(index)
		return this
	}
	args := p.ParseCSV(func() *exp.Expr {
		if c := p.ParseConstraint(); c != nil {
			return c
		}
		return p.ParseFieldDef()
	})
	p.MatchRParen(nil)
	if args == nil {
		args = []*exp.Expr{}
	}
	return p.Expr(exp.Schema, "this", this, "expressions", args)
}

func (p *Parser) parseFieldDef() *exp.Expr {
	return p.ParseColumnDef(p.ParseField(true, nil, false), true)
}

func (p *Parser) parseColumnDef(this *exp.Expr, computedColumn bool) *exp.Expr {
	if this.Is(exp.Column) {
		this = this.This()
	}
	if !computedColumn {
		p.Match(tokens.Alias)
	}
	kind := p.ParseTypes(false, true, true, false)
	if p.MatchTextSeq("FOR", "ORDINALITY") {
		return p.Expr(exp.ColumnDef, "this", this, "ordinality", true)
	}
	constraints := []*exp.Expr{}
	if (kind == nil && p.Match(tokens.Alias)) || p.MatchTexts("ALIAS", "MATERIALIZED") {
		persisted := p.PrevTextUpper() == "MATERIALIZED"
		expression := p.ParseDisjunction()
		if !persisted {
			if p.MatchTextSeq("PERSISTED") {
				persisted = true
			} else if p.MatchTexts("STORED", "VIRTUAL") {
				persisted = p.PrevTextUpper() == "STORED"
			}
		}
		var dataType *exp.Expr
		if p.MatchTextSeq("AUTO") {
			dataType = exp.New(exp.Var, "this", "AUTO")
		} else {
			dataType = p.ParseTypes(false, false, true, false)
		}
		ck := exp.New(exp.ComputedColumnConstraint, "this", expression, "persisted", persisted, "data_type", dataType,
			"not_null", p.MatchPair(tokens.Not, tokens.Null))
		constraints = append(constraints, p.Expr(exp.ColumnConstraint, "kind", ck))
	} else if kind == nil && p.CheckSet(tokens.NewSet(tokens.In, tokens.Out)) {
		inOut := p.Expr(exp.InOutColumnConstraint, "input_", p.Match(tokens.In), "output", p.Match(tokens.Out))
		constraints = append(constraints, inOut)
		kind = p.ParseTypes(false, false, true, false)
	} else if kind != nil && p.Check(tokens.Alias) && (!p.C.WrappedTransformColumnConstraint || p.Next.Type == tokens.LParen) {
		p.Advance()
		this2 := p.ParseDisjunction()
		var persisted any = false
		if p.MatchTexts("STORED", "VIRTUAL") {
			persisted = p.PrevTextUpper() == "STORED"
		}
		constraints = append(constraints, p.Expr(exp.ColumnConstraint, "kind",
			exp.New(exp.ComputedColumnConstraint, "this", this2, "persisted", persisted)))
	}
	for {
		constraint := p.ParseColumnConstraint()
		if constraint == nil {
			break
		}
		constraints = append(constraints, constraint)
	}
	if kind == nil && len(constraints) == 0 {
		return this
	}
	var position *exp.Expr
	if p.MatchTexts("FIRST", "AFTER") {
		pos := p.Prev.Text
		position = p.Expr(exp.ColumnPosition, "this", p.ParseColumn(), "position", pos)
	}
	return p.Expr(exp.ColumnDef, "this", this, "kind", kind, "constraints", constraints, "position", position)
}

func (p *Parser) parseAutoIncrement() *exp.Expr {
	var start, increment *exp.Expr
	var order any
	if p.Check(tokens.LParen) {
		args := p.ParseWrappedCSV(p.ParseBitwise, false)
		if len(args) > 0 {
			start = args[0]
		}
		if len(args) > 1 {
			increment = args[1]
		}
	}
	for {
		if p.MatchTextSeq("START") {
			start = p.ParseBitwise()
		} else if p.MatchTextSeq("INCREMENT") {
			increment = p.ParseBitwise()
		} else if p.MatchTextSeq("ORDER") {
			order = true
		} else if p.MatchTextSeq("NOORDER") {
			order = false
		} else {
			break
		}
	}
	if start != nil || increment != nil || order != nil {
		return exp.New(exp.GeneratedAsIdentityColumnConstraint, "start", start, "increment", increment, "this", false, "order", order)
	}
	return exp.New(exp.AutoIncrementColumnConstraint)
}
