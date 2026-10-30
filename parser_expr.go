package sqlglot

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

// TimeZoneRe mirrors parser.TIME_ZONE_RE.
var TimeZoneRe = regexp.MustCompile(`:.*?[a-zA-Z\+\-]`)

func (p *Parser) isPredicateAhead() bool {
	if p.C.IsPredicatePrecedence == IsPredicatePrecedenceHigh || !p.C.IsPredicateTokens.Has(p.Curr.Type) {
		return false
	}
	return p.Curr.Type != tokens.Not || p.Next.Type == tokens.Null
}

func (p *Parser) parseRange(this *exp.Expr, parseIs bool) *exp.Expr {
	if this == nil {
		this = p.ParseBitwise()
	}
	for {
		if !parseIs && p.IsPredicateAhead() {
			break
		}
		negate := p.Match(tokens.Not)
		if MatchIn(p, p.C.RangeParsers) {
			expression := p.C.RangeParsers[p.Prev.Type](p, this)
			if expression == nil {
				return this
			}
			this = expression
		} else if p.Match(tokens.Isnull) || (negate && p.Match(tokens.Null)) {
			this = p.BuildPredicate(exp.Is, this, exp.New(exp.Null), nil, false)
		} else if p.Match(tokens.Notnull) {
			if p.D.NormalizeNotNull {
				this = p.BuildPredicate(exp.Is, this, exp.New(exp.Null), nil, false)
				this = p.Expr(exp.Not, "this", this)
			} else {
				this = p.BuildPredicate(exp.Is, this, exp.New(exp.Null), nil, true)
			}
		} else {
			if negate {
				p.Retreat(p.Index - 1)
			}
			break
		}
		if negate {
			this = p.NegateRange(this)
		}
		if (negate || (this.Is(exp.Not) && this.This().Is(exp.Is))) &&
			(p.Curr.Type == tokens.Not || CheckIn(p, p.C.RangeParsers)) {
			this = p.Expr(exp.Paren, "this", this)
		}
	}
	return this
}

func (p *Parser) negateRange(this *exp.Expr) *exp.Expr {
	if this == nil {
		return this
	}
	expression := this
	if this.Is(exp.Escape) {
		expression = this.This()
	}
	if expression.IsAny(exp.Like, exp.ILike) {
		expression.Set("negate", true)
		return this
	}
	return p.Expr(exp.Not, "this", this)
}

func (p *Parser) parseIs(this *exp.Expr) *exp.Expr {
	index := p.Index - 1
	negate := p.Match(tokens.Not)
	if p.MatchTextSeq("DISTINCT", "FROM") {
		kind := exp.NullSafeNEQ
		if negate {
			kind = exp.NullSafeEQ
		}
		return p.BuildPredicate(kind, this, p.ParseIsRhs(true), nil, false)
	}
	var expression *exp.Expr
	if p.Match(tokens.Json) {
		var kind any = false
		if p.MatchTextsIn(p.C.IsJsonPredicateKind) {
			kind = p.PrevTextUpper()
		}
		var with any
		if p.MatchTextSeq("WITH") {
			with = true
		} else if p.MatchTextSeq("WITHOUT") {
			with = false
		}
		unique := p.Match(tokens.Unique)
		p.MatchTextSeq("KEYS")
		expression = p.Expr(exp.JSON, "this", kind, "with_", with, "unique", unique)
	} else {
		expression = p.ParseIsRhs(false)
		if expression == nil {
			p.Retreat(index)
			return nil
		}
	}
	if negate && expression.Is(exp.Null) && !p.D.NormalizeNotNull {
		this = p.BuildPredicate(exp.Is, this, expression, nil, true)
	} else {
		this = p.BuildPredicate(exp.Is, this, expression, nil, false)
		if negate {
			this = p.Expr(exp.Not, "this", this)
		}
	}
	return p.ParseColumnOps(this)
}

func (p *Parser) parseIsRhs(distinct bool) *exp.Expr {
	if distinct {
		switch p.C.IsPredicatePrecedence {
		case IsPredicatePrecedenceLow:
			return p.ParseEquality(false)
		case IsPredicatePrecedenceEquality:
			return p.ParseComparison(nil)
		case IsPredicatePrecedenceComparison:
			return p.ParseRange(nil, false)
		}
		return p.ParseBitwise()
	}
	if n := p.ParseNull(); n != nil {
		return n
	}
	return p.ParseBitwise()
}

func (p *Parser) parseIn(this *exp.Expr, alias bool) *exp.Expr {
	unnest := p.ParseUnnest(false)
	if unnest != nil {
		this = p.Expr(exp.In, "this", this, "unnest", unnest)
	} else if p.MatchSet(tokens.NewSet(tokens.LParen, tokens.LBracket)) {
		matchedLParen := p.Prev.Type == tokens.LParen
		expressions := p.ParseCSV(func() *exp.Expr { return p.ParseSelectOrExpression(alias) })
		if len(expressions) == 1 && expressions[0].Is(exp.Query) {
			query := expressions[0]
			this = p.Expr(exp.In, "this", this, "query", p.ParseQueryModifiers(query).Subquery(nil, false))
		} else {
			if expressions == nil {
				expressions = []*exp.Expr{}
			}
			this = p.Expr(exp.In, "this", this, "expressions", expressions)
		}
		if matchedLParen {
			p.MatchRParen(this)
		} else if !p.MatchExpr(tokens.RBracket, this) {
			p.RaiseError("Expecting ]")
		}
	} else {
		this = p.Expr(exp.In, "this", this, "field", p.ParseColumn())
	}
	return this
}

func (p *Parser) parseBetween(this *exp.Expr) *exp.Expr {
	if this.IsAny(exp.Is, exp.NullSafeEQ, exp.NullSafeNEQ) {
		this = exp.New(exp.Paren, "this", this)
	}
	var symmetric any
	if p.MatchTextSeq("SYMMETRIC") {
		symmetric = true
	} else if p.MatchTextSeq("ASYMMETRIC") {
		symmetric = false
	}
	low := p.ParseBitwise()
	p.Match(tokens.And)
	high := p.ParseBitwise()
	return p.Expr(exp.Between, "this", this, "low", low, "high", high, "symmetric", symmetric)
}

func (p *Parser) parseEscape(this *exp.Expr) *exp.Expr {
	if !p.Match(tokens.Escape) {
		return this
	}
	e := p.ParseString()
	if e == nil {
		e = p.ParseNull()
	}
	return p.Expr(exp.Escape, "this", this, "expression", e)
}

func (p *Parser) parseIntervalSpan(this *exp.Expr, parseFunctionUnit bool) *exp.Expr {
	var unitsOmitted *bool
	fractionInString := false
	if this != nil && this.IsString() && p.C.SupportsOmittedIntervalSpanUnit && exp.IntervalDayTimeRe.MatchString(this.Name()) {
		index := p.Index
		firstUnit := p.ParseVar(true, nil, true)
		var secondUnit *exp.Expr
		if firstUnit != nil && p.MatchTextSeq("TO") {
			secondUnit = p.ParseVar(true, nil, true)
		}
		b := !(firstUnit != nil && secondUnit != nil)
		unitsOmitted = &b
		p.Retreat(index)
	}
	unitIndex := p.Index
	var unit *exp.Expr
	if unitsOmitted == nil || !*unitsOmitted {
		isUnit := p.Curr.Type == tokens.Var || p.D.ValidIntervalUnits[strings.ToUpper(p.Curr.Text)]
		if parseFunctionUnit && isUnit {
			unit = p.ParseFunction(FunctionOpts{})
		}
		if unit == nil && isUnit {
			unit = p.ParseVar(true, nil, true)
		}
	}
	if this != nil && this.IsNumber() {
		v, err := this.ToPy()
		if err != nil {
			p.RaiseError(fmt.Sprintf("Invalid numeric interval literal: '%s'", this.Name()))
		} else {
			this = exp.LiteralString(pyStr(v))
		}
	} else if this != nil && this.IsString() {
		parts := findAllIntervalParts(this.Name())
		if len(parts) > 0 && unit != nil {
			unit = nil
			p.Retreat(unitIndex)
		}
		if len(parts) == 1 {
			this = exp.LiteralString(parts[0][1])
			unit = p.Expr(exp.Var, "this", strings.ToUpper(parts[0][2]))
			fractionInString = strings.Contains(parts[0][1], ".")
		}
	}
	if p.C.IntervalSpans && p.MatchTextSeq("TO") {
		e := p.ParseFunction(FunctionOpts{})
		if e == nil {
			e = p.ParseVar(true, nil, true)
		}
		unit = p.Expr(exp.IntervalSpan, "this", unit, "expression", e)
	}
	return p.Expr(exp.Interval, "this", this, "unit", unit, "fraction_in_string", BoolOrNil(fractionInString))
}

var intervalPartRe = regexp.MustCompile(`\s*(-?[0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]+)\s*`)

// findAllIntervalParts mirrors INTERVAL_STRING_RE.findall (non-anchored).
func findAllIntervalParts(s string) [][]string { return intervalPartRe.FindAllStringSubmatch(s, -1) }

// pyStr mirrors Python's str() of a literal value.
func pyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case exp.Decimal:
		return x.Text
	case bool:
		if x {
			return "True"
		}
		return "False"
	case nil:
		return "None"
	case *exp.Expr:
		if x == nil {
			return "None"
		}
		return x.SQL()
	case int:
		return strconv.Itoa(x)
	}
	return fmt.Sprint(v)
}

func (p *Parser) parseInterval(requireInterval, parseFunctionUnit bool) *exp.Expr {
	index := p.Index
	if !p.Match(tokens.Interval) && requireInterval {
		return nil
	}
	var this *exp.Expr
	if p.Check(tokens.String) {
		this = p.ParsePrimary()
	} else {
		this = p.ParseTerm(true)
	}
	if this == nil || (this.Is(exp.Column) && this.TableName() == "" && !this.This().Quoted() && p.Curr.Ok() &&
		!p.D.ValidIntervalUnits[strings.ToUpper(p.Curr.Text)]) {
		p.Retreat(index)
		return nil
	}
	interval := p.ParseIntervalSpan(this, parseFunctionUnit)
	index = p.Index
	p.Match(tokens.Plus)
	if p.CheckSet(tokens.NewSet(tokens.String, tokens.Number)) {
		return p.Expr(exp.Add, "this", interval, "expression", p.ParseInterval(false, parseFunctionUnit))
	}
	p.Retreat(index)
	return interval
}

func (p *Parser) parseBitwise() *exp.Expr {
	this := p.ParseTerm(true)
	for {
		if MatchIn(p, p.C.Bitwise) {
			kind := p.C.Bitwise[p.Prev.Type]
			this = p.Expr(kind, "this", this, "expression", p.ParseTerm(true))
		} else if p.D.DpipeIsStringConcat && p.Match(tokens.Dpipe) {
			this = p.Expr(exp.DPipe, "this", this, "expression", p.ParseTerm(true), "safe", !p.D.StrictStringConcat)
		} else if p.Match(tokens.Dqmark) {
			var exprs []*exp.Expr
			if t := p.ParseTerm(true); t != nil {
				exprs = []*exp.Expr{t}
			} else {
				exprs = []*exp.Expr{}
			}
			this = p.Expr(exp.Coalesce, "this", this, "expressions", exprs)
		} else if p.MatchPair(tokens.Lt, tokens.Lt) {
			this = p.Expr(exp.BitwiseLeftShift, "this", this, "expression", p.ParseTerm(true))
		} else if p.MatchPair(tokens.Gt, tokens.Gt) {
			this = p.Expr(exp.BitwiseRightShift, "this", this, "expression", p.ParseTerm(true))
		} else if len(p.C.JsonOperators) > 0 && MatchIn(p, p.C.JsonOperators) {
			// Look the operator up before parsing the operand: Go doesn't order the map index
			// relative to the ParseTerm call.
			op := p.C.JsonOperators[p.Prev.Type]
			this = op(p, this, p.ParseTerm(true))
		} else {
			break
		}
	}
	return this
}

func (p *Parser) parseTerm(parseMod bool) *exp.Expr {
	this := p.ParseFactor(parseMod)
	for MatchIn(p, p.C.Term) {
		kind := p.C.Term[p.Prev.Type]
		comments := p.PrevComments
		expression := p.ParseFactor(parseMod)
		this = p.ExpressionComments(exp.New(kind, "this", this, "expression", expression), comments)
		if this.Is(exp.Collate) {
			p.NormalizeCollate(this)
		}
	}
	return this
}

func (p *Parser) normalizeCollate(collate *exp.Expr) {
	e := collate.Expression()
	if e.Is(exp.Column) && len(e.Parts()) == 1 {
		ident := e.This()
		if ident.Is(exp.Identifier) {
			if ident.Quoted() {
				collate.Set("expression", ident)
			} else {
				collate.Set("expression", exp.NewVar(ident.Name()))
			}
		}
	}
}

func isAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func (p *Parser) parseFactor(parseMod bool) *exp.Expr {
	this := p.ParseAtTimeZone(p.ParseFactorOperand())
	for CheckIn(p, p.C.Factor) {
		if !parseMod && p.Curr.Type == tokens.Mod {
			break
		}
		p.Advance()
		kind := p.C.Factor[p.Prev.Type]
		comments := p.PrevComments
		expression := p.ParseFactorOperand()
		if expression == nil && kind == exp.IntDiv && isAlpha(p.Prev.Text) {
			p.Retreat(p.Index - 1)
			return this
		}
		this = p.ExpressionComments(exp.New(kind, "this", this, "expression", expression), comments)
		if this.Is(exp.Div) {
			this.Set("typed", p.D.TypedDivision)
			this.Set("safe", p.D.SafeDivision)
		}
	}
	return this
}

func (p *Parser) parseFactorOperand() *exp.Expr {
	if len(p.C.Exponent) > 0 {
		return p.ParseExponent()
	}
	return p.ParseUnary()
}

func (p *Parser) parseExponent() *exp.Expr {
	this := p.ParseUnary()
	for MatchIn(p, p.C.Exponent) {
		comments := p.PrevComments
		kind := p.C.Exponent[p.Prev.Type]
		this = p.ExpressionComments(exp.New(kind, "this", this, "expression", p.ParseUnary()), comments)
	}
	return this
}

func (p *Parser) parseUnary() *exp.Expr {
	if MatchIn(p, p.C.UnaryParsers) {
		return p.C.UnaryParsers[p.Prev.Type](p)
	}
	return p.ParseType(true, false)
}

func (p *Parser) parseType(parseInterval, fallbackToIdentifier bool) *exp.Expr {
	if !fallbackToIdentifier {
		if atom := p.ParseAtom(); atom != nil {
			return atom
		}
	}
	if parseInterval {
		if interval := p.ParseInterval(true, true); interval != nil {
			return p.ParseColumnOps(interval)
		}
	}
	index := p.Index
	dataType := p.ParseTypes(true, false, false, false)
	if dataType.Is(exp.Cast) {
		return p.ParseColumnOps(dataType)
	}
	if dataType != nil {
		index2 := p.Index
		this := p.ParsePrimary()
		if this.Is(exp.Literal) {
			literal := this.Name()
			this = p.ParseColumnOps(this)
			if p.C.ZoneAwareTimestampConstructor && TimeZoneRe.MatchString(literal) {
				if dataType.IsType(exp.DTypeTimestamp) {
					dataType = exp.DTypeTimestamptz.IntoExpr()
				} else if dataType.IsType(exp.DTypeTime) {
					dataType = exp.DTypeTimetz.IntoExpr()
				}
			}
			if dt, ok := dataType.Arg("this").(exp.DType); ok {
				if parser, ok := p.C.TypeLiteralParsers[dt]; ok {
					return parser(p, this, dataType)
				}
			}
			return p.Expr(exp.Cast, "this", this, "to", dataType)
		}
		if len(dataType.Expressions()) > 0 && index2-index > 1 {
			p.Retreat(index2)
			return p.ParseColumnOps(dataType)
		}
		p.Retreat(index)
	}
	if fallbackToIdentifier {
		return p.ParseIdVar(true, nil)
	}
	return p.ParseColumn()
}

func (p *Parser) parseTypeSize() *exp.Expr {
	this := p.ParseType(true, false)
	if this == nil {
		return nil
	}
	if this.Is(exp.Column) && this.TableName() == "" {
		this = exp.NewVar(strings.ToUpper(this.Name()))
	}
	return p.Expr(exp.DataTypeParam, "this", this, "expression", p.ParseVar(true, nil, false))
}

func (p *Parser) parseUserDefinedType(identifier *exp.Expr) *exp.Expr {
	typeName := identifier.Name()
	for p.Match(tokens.Dot) {
		part := "False"
		if p.AdvanceAny(false) != nil {
			part = p.Prev.Text
		}
		typeName = typeName + "." + part
	}
	dt, err := exp.DataTypeBuild(typeName, p.D, true)
	if err != nil {
		panic(parseBailoutFrom(err))
	}
	return dt
}

func (p *Parser) parseTypes(checkFunc, schema, allowIdentifiers, withCollation bool) *exp.Expr {
	index := p.Index
	var this *exp.Expr
	var typeToken tokens.TokenType
	hasTypeToken := false

	if p.MatchSet(p.C.TypeTokens) {
		typeToken = p.Prev.Type
		hasTypeToken = true
	} else {
		var identifier *exp.Expr
		if allowIdentifiers {
			vs := tokens.NewSet(tokens.Var)
			identifier = p.ParseIdVar(false, &vs)
		}
		if identifier.Is(exp.Identifier) {
			if identifier.Quoted() && p.C.QuotedTypesToPreserve[identifier.Name()] {
				this = exp.MustDataTypeBuild(identifier, nil, true)
			} else {
				toks, err := p.D.Tokenize(identifier.Name())
				if err != nil {
					toks = nil
				}
				if len(toks) > 0 && p.C.TypeTokens.Has(toks[0].Type) {
					typeToken = toks[0].Type
					hasTypeToken = true
					if len(toks) > 1 {
						dt, err := exp.DataTypeBuild(identifier.Name(), p.D, false)
						if err != nil {
							panic(parseBailoutFrom(err))
						}
						return dt
					}
				} else if p.D.SupportsUserDefinedTypes {
					this = p.ParseUserDefinedType(identifier)
				} else {
					p.Retreat(p.Index - 1)
					return nil
				}
			}
		} else {
			return nil
		}
	}

	if hasTypeToken && typeToken == tokens.PseudoType {
		return p.Expr(exp.PseudoType, "this", p.PrevTextUpper())
	}
	if hasTypeToken && typeToken == tokens.ObjectIdentifier {
		return p.Expr(exp.ObjectIdentifier, "this", p.PrevTextUpper())
	}
	if hasTypeToken && typeToken == tokens.Map && p.Match(tokens.LBracket) {
		keyType := p.ParseTypes(checkFunc, schema, allowIdentifiers, false)
		if !p.Match(tokens.Farrow) {
			p.Retreat(index)
			return nil
		}
		valueType := p.ParseTypes(checkFunc, schema, allowIdentifiers, false)
		if !p.Match(tokens.RBracket) {
			p.Retreat(index)
			return nil
		}
		return exp.New(exp.DataType, "this", exp.DTypeMap, "expressions", []*exp.Expr{keyType, valueType}, "nested", true)
	}

	nested := hasTypeToken && p.C.NestedTypeTokens.Has(typeToken)
	isStruct := hasTypeToken && p.C.StructTypeTokens.Has(typeToken)
	isAggregate := hasTypeToken && p.C.AggregateTypeTokens.Has(typeToken)
	var expressions []*exp.Expr
	hasExpressions := false
	maybeFunc := false

	if p.Match(tokens.LParen) {
		hasExpressions = true
		switch {
		case isStruct:
			expressions = p.ParseCSV(func() *exp.Expr { return p.ParseStructTypes(true) })
		case nested:
			expressions = p.ParseCSV(func() *exp.Expr { return p.ParseTypes(checkFunc, schema, allowIdentifiers, false) })
			if typeToken == tokens.Nullable && len(expressions) == 1 {
				this = expressions[0]
				this.Set("nullable", true)
				p.MatchRParen(nil)
				return this
			}
		case p.C.EnumTypeTokens.Has(typeToken):
			expressions = p.ParseCSV(func() *exp.Expr { return p.ParseEquality(true) })
		case typeToken == tokens.Ref:
			target := p.TryParse(func() *exp.Expr { return p.ParseTableParts(true, false, false, false) }, false)
			if target == nil {
				p.Retreat(index)
				return nil
			}
			expressions = []*exp.Expr{target}
		case typeToken == tokens.Json:
			expressions = p.ParseCSV(p.ParseJsonTypeArg)
		case isAggregate:
			funcOrIdent := p.ParseFunction(FunctionOpts{Anonymous: true})
			if funcOrIdent == nil {
				vs := tokens.NewSet(tokens.Var, tokens.Any)
				funcOrIdent = p.ParseIdVar(false, &vs)
			}
			if funcOrIdent == nil {
				return nil
			}
			expressions = []*exp.Expr{funcOrIdent}
			if p.Match(tokens.Comma) {
				expressions = append(expressions, p.ParseCSV(func() *exp.Expr {
					return p.ParseTypes(checkFunc, schema, allowIdentifiers, false)
				})...)
			}
		default:
			expressions = p.ParseCSV(p.ParseTypeSize)
			if typeToken == tokens.Vector && len(expressions) == 2 {
				expressions = p.ParseVectorExpressions(expressions)
			}
		}
		if !p.Match(tokens.RParen) {
			p.Retreat(index)
			return nil
		}
		maybeFunc = true
	}

	var values []*exp.Expr
	hasValues := false
	if nested && p.Match(tokens.Lt) {
		hasExpressions = true
		if isStruct {
			expressions = p.ParseCSV(func() *exp.Expr { return p.ParseStructTypes(true) })
		} else {
			expressions = p.ParseCSV(func() *exp.Expr { return p.ParseTypes(checkFunc, schema, allowIdentifiers, true) })
		}
		if !p.Match(tokens.Gt) {
			p.RaiseError("Expecting >")
		}
		if p.MatchSet(tokens.NewSet(tokens.LBracket, tokens.LParen)) {
			values = p.ParseCSV(p.ParseDisjunction)
			hasValues = true
			if len(values) == 0 && isStruct {
				values = nil
				hasValues = false
				p.Retreat(p.Index - 1)
			} else {
				p.MatchSet(tokens.NewSet(tokens.RBracket, tokens.RParen))
			}
		}
	}

	if hasTypeToken && p.C.Timestamps.Has(typeToken) {
		if p.MatchTextSeq("WITH", "TIME", "ZONE") {
			maybeFunc = false
			tz := exp.DTypeTimestamptz
			if p.C.Times.Has(typeToken) {
				tz = exp.DTypeTimetz
			}
			this = exp.New(exp.DataType, "this", tz, "expressions", optList(expressions, hasExpressions))
		} else if p.MatchTextSeq("WITH", "LOCAL", "TIME", "ZONE") {
			maybeFunc = false
			this = exp.New(exp.DataType, "this", exp.DTypeTimestampltz, "expressions", optList(expressions, hasExpressions))
		} else if p.MatchTextSeq("WITHOUT", "TIME", "ZONE") {
			maybeFunc = false
		}
	} else if hasTypeToken && typeToken == tokens.Interval {
		if p.D.ValidIntervalUnits[strings.ToUpper(p.Curr.Text)] {
			unit := p.ParseVar(false, nil, true)
			if p.MatchTextSeq("TO") {
				unit = exp.New(exp.IntervalSpan, "this", unit, "expression", p.ParseVar(false, nil, true))
			}
			this = p.Expr(exp.DataType, "this", p.Expr(exp.Interval, "unit", unit))
		} else {
			this = p.Expr(exp.DataType, "this", exp.DTypeInterval)
		}
	} else if hasTypeToken && typeToken == tokens.Void {
		this = exp.New(exp.DataType, "this", exp.DTypeNull)
	}

	if maybeFunc && checkFunc {
		index2 := p.Index
		peek := p.ParseString()
		if peek == nil {
			p.Retreat(index)
			return nil
		}
		p.Retreat(index2)
	}

	if this == nil {
		if p.MatchTextSeq("UNSIGNED") {
			unsigned, ok := p.C.SignedToUnsignedTypeToken[typeToken]
			if !ok {
				p.RaiseError(fmt.Sprintf("Cannot convert %s to unsigned.", typeToken))
			} else {
				typeToken = unsigned
			}
		}
		if typeToken == tokens.Nullable && len(expressions) == 0 {
			p.Retreat(index)
			return nil
		}
		dt, ok := exp.DTypeByName(typeToken.String())
		if !ok {
			p.RaiseError(fmt.Sprintf("Unknown data type %s", typeToken))
			dt = exp.DTypeUnknown
		}
		this = exp.New(exp.DataType, "this", dt, "expressions", optList(expressions, hasExpressions), "nested", nested)
		if hasValues {
			kind := exp.Array
			if isStruct {
				kind = exp.Struct
			}
			if values == nil {
				values = []*exp.Expr{}
			}
			this = exp.NewCast(exp.New(kind, "expressions", values), this, exp.WithCopy(false))
		}
	} else if len(expressions) > 0 {
		this.Set("expressions", expressions)
	}

	for p.Match(tokens.List) {
		this = exp.New(exp.DataType, "this", exp.DTypeList, "expressions", []*exp.Expr{this}, "nested", true)
	}

	index = p.Index
	matchedArray := p.Match(tokens.Array)
	for p.Curr.Ok() {
		datatypeToken := p.Prev.Type
		matchedLBracket := p.Match(tokens.LBracket)
		if (!matchedLBracket && !matchedArray) || (datatypeToken == tokens.Array && p.Match(tokens.RBracket)) {
			break
		}
		matchedArray = false
		vals := p.ParseCSV(p.ParseDisjunction)
		if len(vals) > 0 && !schema && (!p.D.SupportsFixedSizeArrays || datatypeToken == tokens.Array || !p.Check(tokens.RBracket)) {
			p.Retreat(index)
			break
		}
		var v any
		if len(vals) > 0 {
			v = vals
		}
		this = exp.New(exp.DataType, "this", exp.DTypeArray, "expressions", []*exp.Expr{this}, "values", v, "nested", true)
		p.Match(tokens.RBracket)
	}

	if len(p.C.TypeConverters) > 0 {
		if dt, ok := this.Arg("this").(exp.DType); ok {
			if converter, ok := p.C.TypeConverters[dt]; ok {
				this = converter(this)
			}
		}
	}
	if withCollation && this.Is(exp.DataType) && p.Match(tokens.Collate) {
		c := p.ParseIdentifier()
		if c == nil {
			c = p.ParseColumn()
		}
		this.Set("collate", c)
	}
	return this
}

func optList(xs []*exp.Expr, has bool) any {
	if !has {
		return nil
	}
	if xs == nil {
		return []*exp.Expr{}
	}
	return xs
}

func (p *Parser) parseJsonTypeArg() *exp.Expr {
	if p.MatchTextSeq("SKIP") {
		regexp := p.Match(tokens.Rlike)
		arg := p.ParseColumn()
		if arg.Is(exp.Column) {
			arg = arg.ToDot(true)
		}
		return p.Expr(exp.SkipJSONColumn, "regexp", regexp, "expression", arg)
	}
	paramOrCol := p.ParseColumn()
	if !paramOrCol.Is(exp.Column) {
		return nil
	}
	if len(paramOrCol.Parts()) == 1 && p.Match(tokens.Eq) {
		param := paramOrCol.Name()
		value := p.ParsePrimary()
		return p.Expr(exp.EQ, "this", exp.NewVar(param), "expression", value)
	}
	col := paramOrCol.ToDot(true)
	kind := p.ParseTypes(false, false, false, false)
	return p.Expr(exp.ColumnDef, "this", col, "kind", kind)
}

func (p *Parser) parseVectorExpressions(expressions []*exp.Expr) []*exp.Expr {
	dt, err := exp.DataTypeBuild(expressions[0].Name(), p.D, false)
	if err != nil {
		panic(parseBailoutFrom(err))
	}
	return append([]*exp.Expr{dt}, expressions[1:]...)
}

func (p *Parser) parseStructTypes(typeRequired bool) *exp.Expr {
	index := p.Index
	var this *exp.Expr
	if p.Curr.Ok() && p.Next.Ok() && p.C.TypeTokens.Has(p.Curr.Type) && p.C.TypeTokens.Has(p.Next.Type) {
		this = p.ParseIdVar(true, nil)
	} else {
		this = p.ParseType(false, true)
		if this == nil {
			this = p.ParseIdVar(true, nil)
		}
	}
	p.Match(tokens.Colon)
	if typeRequired && !this.Is(exp.DataType) && !p.CheckSet(p.C.TypeTokens) {
		p.Retreat(index)
		return p.ParseTypes(false, false, true, false)
	}
	return p.ParseColumnDef(this, true)
}

func (p *Parser) parseAtTimeZone(this *exp.Expr) *exp.Expr {
	if !p.MatchTextSeq("AT", "TIME", "ZONE") {
		return this
	}
	return p.ParseAtTimeZone(p.Expr(exp.AtTimeZone, "this", this, "zone", p.ParseUnary()))
}

// parseBailoutFrom converts an error returned by a nested parse into a parser panic value.
func parseBailoutFrom(err error) any {
	if pe, ok := err.(*errs.ParseError); ok {
		return parseBailout{pe}
	}
	return err
}
