package sqlglot

import (
	"fmt"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func (p *Parser) parseCheckConstraint() *exp.Expr {
	if !p.Check(tokens.LParen) {
		return nil
	}
	this := p.ParseWrappedExpr(p.ParseAssignment, false)
	return p.Expr(exp.CheckColumnConstraint, "this", this, "enforced", p.MatchTextSeq("ENFORCED"))
}

func (p *Parser) parseAutoProperty() *exp.Expr {
	if !p.MatchTextSeq("REFRESH") {
		p.Retreat(p.Index - 1)
		return nil
	}
	return p.Expr(exp.AutoRefreshProperty, "this", p.ParseVar(false, nil, true))
}

func (p *Parser) parseCompress() *exp.Expr {
	if p.Check(tokens.LParen) {
		return p.Expr(exp.CompressColumnConstraint, "this", p.ParseWrappedCSV(p.ParseBitwise, false))
	}
	return p.Expr(exp.CompressColumnConstraint, "this", p.ParseBitwise())
}

func (p *Parser) parseGeneratedAsIdentity() *exp.Expr {
	var this *exp.Expr
	if p.MatchTextSeq("BY", "DEFAULT") {
		onNull := p.MatchPair(tokens.On, tokens.Null)
		this = p.Expr(exp.GeneratedAsIdentityColumnConstraint, "this", false, "on_null", onNull)
	} else {
		p.MatchTextSeq("ALWAYS")
		this = p.Expr(exp.GeneratedAsIdentityColumnConstraint, "this", true)
	}
	p.Match(tokens.Alias)
	if p.MatchTextSeq("ROW") {
		start := p.MatchTextSeq("START")
		if !start {
			p.Match(tokens.End)
		}
		hidden := p.MatchTextSeq("HIDDEN")
		return p.Expr(exp.GeneratedAsRowColumnConstraint, "start", start, "hidden", hidden)
	}
	identity := p.MatchTextSeq("IDENTITY")
	if p.Match(tokens.LParen) {
		if p.MatchTextSeq("START", "WITH") {
			this.Set("start", p.ParseBitwise())
		}
		if p.MatchTextSeq("INCREMENT", "BY") {
			this.Set("increment", p.ParseBitwise())
		}
		if p.MatchTextSeq("MINVALUE") {
			this.Set("minvalue", p.ParseBitwise())
		}
		if p.MatchTextSeq("MAXVALUE") {
			this.Set("maxvalue", p.ParseBitwise())
		}
		if p.MatchTextSeq("CYCLE") {
			this.Set("cycle", true)
		} else if p.MatchTextSeq("NO", "CYCLE") {
			this.Set("cycle", false)
		}
		if !identity {
			this.Set("expression", p.ParseDisjunction())
		} else if this.Get("start") == nil && p.Check(tokens.Number) {
			args := p.ParseCSV(p.ParseBitwise)
			var start, inc *exp.Expr
			if len(args) > 0 {
				start = args[0]
			}
			if len(args) > 1 {
				inc = args[1]
			}
			this.Set("start", start)
			this.Set("increment", inc)
		}
		p.MatchRParen(nil)
	}
	return this
}

func (p *Parser) parseInline() *exp.Expr {
	p.MatchTextSeq("LENGTH")
	return p.Expr(exp.InlineLengthColumnConstraint, "this", p.ParseBitwise())
}

func (p *Parser) parseNotConstraint() *exp.Expr {
	if p.MatchTextSeq("NULL") {
		return p.Expr(exp.NotNullColumnConstraint)
	}
	if p.MatchTextSeq("CASESPECIFIC") {
		return p.Expr(exp.CaseSpecificColumnConstraint, "not_", true)
	}
	if p.MatchTextSeq("FOR", "REPLICATION") {
		return p.Expr(exp.NotForReplicationColumnConstraint)
	}
	p.Retreat(p.Index - 1)
	return nil
}

func (p *Parser) parseColumnConstraint() *exp.Expr {
	var this *exp.Expr
	if p.Match(tokens.Constraint) && !p.CheckTextsIn(p.C.ReservedConstraintKinds) {
		this = p.ParseIdVar(true, nil)
	}
	procedureOptionFollows := false
	if p.Check(tokens.With) && p.Next.Ok() {
		_, procedureOptionFollows = p.C.ProcedureOptions[strings.ToUpper(p.Next.Text)]
	}
	index := p.Index
	if !procedureOptionFollows && MatchTextsKey(p, p.C.ConstraintParsers) {
		constraint := p.C.ConstraintParsers[p.PrevTextUpper()](p)
		if constraint == nil {
			p.Retreat(index)
			return nil
		}
		return p.Expr(exp.ColumnConstraint, "this", this, "kind", constraint)
	}
	if p.Match(tokens.CharacterSet) || p.MatchPair(tokens.Char, tokens.Set) {
		return p.Expr(exp.ColumnConstraint, "this", this, "kind",
			p.Expr(exp.CharacterSetColumnConstraint, "this", p.ParseVarOrString(false)))
	}
	return this
}

func (p *Parser) parseConstraint() *exp.Expr {
	if !p.Match(tokens.Constraint) || p.CheckTextsIn(p.C.ReservedConstraintKinds) {
		return p.ParseUnnamedConstraint(p.C.SchemaUnnamedConstraints)
	}
	return p.Expr(exp.Constraint, "this", p.ParseIdVar(true, nil), "expressions", p.ParseUnnamedConstraints())
}

func (p *Parser) parseUnnamedConstraints() []*exp.Expr {
	constraints := []*exp.Expr{}
	for {
		constraint := p.ParseUnnamedConstraint(nil)
		if constraint == nil {
			constraint = p.ParseFunction(FunctionOpts{})
		}
		if constraint == nil {
			break
		}
		constraints = append(constraints, constraint)
	}
	return constraints
}

func (p *Parser) parseUnnamedConstraint(constraints StringSet) *exp.Expr {
	index := p.Index
	if p.Check(tokens.Identifier) {
		return nil
	}
	if constraints != nil {
		if !p.MatchTextsIn(constraints) {
			return nil
		}
	} else if !MatchTextsKey(p, p.C.ConstraintParsers) {
		return nil
	}
	key := p.PrevTextUpper()
	parser, ok := p.C.ConstraintParsers[key]
	if !ok {
		p.RaiseError(fmt.Sprintf("No parser found for schema constraint %s.", key))
		return nil
	}
	result := parser(p)
	if result == nil {
		p.Retreat(index)
	}
	return result
}

func (p *Parser) parseUniqueKey() *exp.Expr {
	if p.Curr.Ok() && p.Curr.Type != tokens.Identifier {
		if _, ok := p.C.ConstraintParsers[strings.ToUpper(p.Curr.Text)]; ok {
			return nil
		}
	}
	return p.ParseIdVar(false, nil)
}

func (p *Parser) parseUnique() *exp.Expr {
	p.MatchTexts("KEY", "INDEX")
	nulls := p.MatchTextSeq("NULLS", "NOT", "DISTINCT")
	this := p.ParseSchema(p.ParseUniqueKey())
	var indexType any = false
	if p.Match(tokens.Using) {
		if p.AdvanceAny(false) != nil {
			indexType = p.Prev.Text
		} else {
			indexType = nil
		}
	}
	return p.Expr(exp.UniqueColumnConstraint, "nulls", nulls, "this", this, "index_type", indexType,
		"on_conflict", p.ParseOnConflict(), "options", p.ParseKeyConstraintOptions())
}

func (p *Parser) parseKeyConstraintOptions() []string {
	options := []string{}
	for {
		if !p.Curr.Ok() {
			break
		}
		if p.Match(tokens.On) {
			var action any
			on := "False"
			if p.AdvanceAny(false) != nil {
				on = p.Prev.Text
			}
			switch {
			case p.MatchTextSeq("NO", "ACTION"):
				action = "NO ACTION"
			case p.MatchTextSeq("CASCADE"):
				action = "CASCADE"
			case p.MatchTextSeq("RESTRICT"):
				action = "RESTRICT"
			case p.MatchPair(tokens.Set, tokens.Null):
				action = "SET NULL"
			case p.MatchPair(tokens.Set, tokens.Default):
				action = "SET DEFAULT"
			default:
				p.RaiseError("Invalid key constraint")
			}
			options = append(options, fmt.Sprintf("ON %s %s", on, pyStr(action)))
		} else {
			v := p.ParseVarFromOptions(p.C.KeyConstraintOptions, false)
			if v == nil {
				break
			}
			options = append(options, v.Name())
		}
	}
	return options
}

func (p *Parser) parseReferences(match bool) *exp.Expr {
	if match && !p.Match(tokens.References) {
		return nil
	}
	this := p.ParseTable(TableOpts{Schema: true})
	options := p.ParseKeyConstraintOptions()
	return p.Expr(exp.Reference, "this", this, "expressions", nil, "options", options)
}

func (p *Parser) parseForeignKey() *exp.Expr {
	var expressions any
	if !p.Check(tokens.References) {
		expressions = p.ParseWrappedIdVars(false)
	}
	reference := p.ParseReferences(true)
	var onOptions KW
	for p.Match(tokens.On) {
		if !p.MatchSet(tokens.NewSet(tokens.Delete, tokens.Update)) {
			p.RaiseError("Expected DELETE or UPDATE")
		}
		kind := strings.ToLower(p.Prev.Text)
		var action string
		if p.MatchTextSeq("NO", "ACTION") {
			action = "NO ACTION"
		} else if p.Match(tokens.Set) {
			p.MatchSet(tokens.NewSet(tokens.Null, tokens.Default))
			action = "SET " + p.PrevTextUpper()
		} else {
			p.Advance()
			action = p.PrevTextUpper()
		}
		onOptions.Set(kind, action)
	}
	kv := []any{"expressions", expressions, "reference", reference, "options", p.ParseKeyConstraintOptions()}
	kv = append(kv, onOptions.KV()...)
	return p.Expr(exp.ForeignKey, kv...)
}

func (p *Parser) parsePrimaryKeyPart() *exp.Expr { return p.ParseField(false, nil, false) }

func (p *Parser) parsePeriodForSystemTime() *exp.Expr {
	if !p.MatchTextSeq("FOR", "SYSTEM_TIME") {
		p.Retreat(p.Index - 1)
		return nil
	}
	idVars := p.ParseWrappedIdVars(false)
	var a, b *exp.Expr
	if len(idVars) > 0 {
		a = idVars[0]
	}
	if len(idVars) > 1 {
		b = idVars[1]
	}
	return p.Expr(exp.PeriodForSystemTimeConstraint, "this", a, "expression", b)
}

func (p *Parser) parsePrimaryKey(wrappedOptional, inProps, namedPrimaryKey bool) *exp.Expr {
	var desc any
	if p.MatchSet(tokens.NewSet(tokens.Asc, tokens.Desc)) {
		desc = p.Prev.Type == tokens.Desc
	}
	var this *exp.Expr
	if namedPrimaryKey && p.Next.Ok() && p.Next.Type == tokens.LParen {
		if _, ok := p.C.ConstraintParsers[strings.ToUpper(p.Curr.Text)]; !ok {
			this = p.ParseIdVar(true, nil)
		}
	}
	if !inProps && !p.Check(tokens.LParen) {
		return p.Expr(exp.PrimaryKeyColumnConstraint, "desc", desc, "options", p.ParseKeyConstraintOptions())
	}
	expressions := p.ParseWrappedCSV(p.ParsePrimaryKeyPart, wrappedOptional)
	if expressions == nil {
		expressions = []*exp.Expr{}
	}
	return p.Expr(exp.PrimaryKey, "this", this, "expressions", expressions, "include", p.ParseIndexParams(),
		"options", p.ParseKeyConstraintOptions())
}

func (p *Parser) parseBracketKeyValue(isMap bool) *exp.Expr {
	return p.ParseSlice(p.ParseAlias(p.ParseDisjunction(), true))
}

func (p *Parser) parseOdbcDatetimeLiteral() *exp.Expr {
	p.Match(tokens.Var)
	kind := p.C.OdbcDatetimeLiterals[strings.ToLower(p.Prev.Text)]
	expression := p.Expr(kind, "this", p.ParseString())
	if !p.Match(tokens.RBrace) {
		p.RaiseError("Expected }")
	}
	return expression
}

// BuildArrayConstructor mirrors parser.build_array_constructor.
func BuildArrayConstructor(kind exp.Kind, args []*exp.Expr, bracketKind tokens.TokenType, d *Dialect) *exp.Expr {
	if args == nil {
		args = []*exp.Expr{}
	}
	arr := exp.New(kind, "expressions", args)
	if kind == exp.Array {
		arr.Set("value_constructor", len(args) == 1 && args[0].Is(exp.Query))
		if d.HasDistinctArrayConstructors {
			arr.Set("bracket_notation", bracketKind == tokens.LBracket)
		}
	}
	return arr
}

func (p *Parser) parseBracket(this *exp.Expr) *exp.Expr {
	if !p.MatchSet(p.C.Brackets) {
		return this
	}
	parseMap := false
	if p.C.MapKeysAreArbitraryExpressions {
		if p.Index-2 >= 0 && p.Index-2 < len(p.Tokens) {
			parseMap = strings.ToUpper(p.Tokens[p.Index-2].Text) == "MAP"
		}
	}
	bracketKind := p.Prev.Type
	if bracketKind == tokens.LBrace && p.Curr.Ok() && p.Curr.Type == tokens.Var {
		if _, ok := p.C.OdbcDatetimeLiterals[strings.ToLower(p.Curr.Text)]; ok {
			return p.ParseOdbcDatetimeLiteral()
		}
	}
	expressions := p.ParseCSV(func() *exp.Expr { return p.ParseBracketKeyValue(bracketKind == tokens.LBrace) })
	if bracketKind == tokens.LBracket && !p.Match(tokens.RBracket) {
		p.RaiseError("Expected ]")
	} else if bracketKind == tokens.LBrace && !p.Match(tokens.RBrace) {
		p.RaiseError("Expected }")
	}
	if bracketKind == tokens.LBrace {
		this = p.Expr(exp.Struct, "expressions", p.KvToPropEq(nonNilList(expressions), parseMap))
	} else if this == nil {
		this = BuildArrayConstructor(exp.Array, expressions, bracketKind, p.D)
	} else {
		if ct, ok := p.C.ArrayConstructors[strings.ToUpper(this.Name())]; ok {
			return BuildArrayConstructor(ct, expressions, bracketKind, p.D)
		}
		expressions = ApplyIndexOffset(this, expressions, -p.D.IndexOffset, p.D)
		this = p.ExpressionComments(exp.New(exp.Bracket, "this", this, "expressions", nonNilList(expressions)), this.PopComments())
	}
	p.AddComments(this)
	return p.ParseBracket(this)
}

func nonNilList(xs []*exp.Expr) []*exp.Expr {
	if xs == nil {
		return []*exp.Expr{}
	}
	return xs
}

func (p *Parser) parseSlice(this *exp.Expr) *exp.Expr {
	if !p.Match(tokens.Colon) {
		return this
	}
	var end *exp.Expr
	if p.CheckPair(tokens.Dash, tokens.Colon) {
		p.Advance()
	} else {
		end = p.ParseAssignment()
	}
	var step *exp.Expr
	if p.Match(tokens.Colon) {
		step = p.ParseUnary()
	}
	return p.Expr(exp.Slice, "this", this, "expression", end, "step", step)
}

func (p *Parser) parseCase() *exp.Expr {
	if p.Check(tokens.Dot) {
		p.Retreat(p.Index - 1)
		return nil
	}
	ifs := []*exp.Expr{}
	var def *exp.Expr
	comments := p.PrevComments
	expression := p.ParseDisjunction()
	for p.Match(tokens.When) {
		this := p.ParseDisjunction()
		p.Match(tokens.Then)
		then := p.ParseDisjunction()
		ifs = append(ifs, p.Expr(exp.If, "this", this, "true", then))
	}
	if p.Match(tokens.Else) {
		def = p.ParseDisjunction()
	}
	if !p.Match(tokens.End) {
		if def.Is(exp.Interval) && strings.ToUpper(def.This().SQL()) == "END" {
			def = exp.NewColumn("interval")
		} else {
			p.RaiseErrorAt("Expected END after CASE", p.Prev)
		}
	}
	return p.ExpressionComments(exp.New(exp.Case, "this", expression, "ifs", ifs, "default", def), comments)
}
