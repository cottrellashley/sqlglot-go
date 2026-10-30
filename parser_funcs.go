package sqlglot

import (
	"fmt"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func seqGet(xs []*exp.Expr, i int) *exp.Expr {
	if i < 0 {
		i += len(xs)
	}
	if i < 0 || i >= len(xs) {
		return nil
	}
	return xs[i]
}

func (p *Parser) parseIf() *exp.Expr {
	var this *exp.Expr
	if p.Match(tokens.LParen) {
		args := p.ParseCSV(func() *exp.Expr { return p.ParseAlias(p.ParseAssignment(), true) })
		this = p.ValidateExpression(exp.FromArgList(exp.If, args), args)
		p.MatchRParen(nil)
	} else {
		index := p.Index - 1
		if p.C.NoParenIfCommands && index == 0 {
			return p.ParseAsCommand(p.Prev)
		}
		condition := p.ParseDisjunction()
		if condition == nil || !p.Match(tokens.Then) {
			p.Retreat(index)
			return nil
		}
		t := p.ParseDisjunction()
		var f *exp.Expr
		if p.Match(tokens.Else) {
			f = p.ParseDisjunction()
		}
		p.Match(tokens.End)
		this = p.Expr(exp.If, "this", condition, "true", t, "false", f)
	}
	return this
}

func (p *Parser) parseNextValueFor() *exp.Expr {
	if !p.MatchTextSeq("VALUE", "FOR") {
		p.Retreat(p.Index - 1)
		return nil
	}
	this := p.ParseColumn()
	order := AndThen(p.Match(tokens.Over), func() *exp.Expr {
		return p.ParseWrappedExpr(func() *exp.Expr { return p.ParseOrder(nil, false) }, false)
	})
	return p.Expr(exp.NextValueFor, "this", this, "order", order)
}

func (p *Parser) parseExtract() *exp.Expr {
	this := p.ParseFunction(FunctionOpts{})
	if this == nil {
		this = p.ParseVarOrString(true)
	}
	if p.Match(tokens.From) {
		return p.Expr(exp.Extract, "this", this, "expression", p.ParseBitwise())
	}
	if !p.Match(tokens.Comma) {
		p.RaiseErrorAt("Expected FROM or comma after EXTRACT", p.Prev)
	}
	return p.Expr(exp.Extract, "this", this, "expression", p.ParseBitwise())
}

func (p *Parser) parseGapFill() *exp.Expr {
	p.Match(tokens.Table)
	this := p.ParseTable(TableOpts{})
	p.Match(tokens.Comma)
	args := append([]*exp.Expr{this}, p.ParseCSV(func() *exp.Expr { return p.ParseLambda(false) })...)
	return p.ValidateExpression(exp.FromArgList(exp.GapFill, args), args)
}

func (p *Parser) parseChar() *exp.Expr {
	exprs := p.ParseCSV(p.ParseAssignment)
	charset := AndThen(p.Match(tokens.Using), p.ParseCharsetName)
	return p.Expr(exp.Chr, "expressions", nonNilList(exprs), "charset", charset)
}

func (p *Parser) parseCharsetName() *exp.Expr {
	ts := tokens.NewSet(tokens.Binary, tokens.Identifier)
	return p.ParseVar(false, &ts, false)
}

func (p *Parser) parseWeightString() *exp.Expr {
	name := p.Tokens[p.Index-2].Text
	args := p.ParseCSV(p.ParseAssignment)
	if len(args) != 1 {
		return p.Expr(exp.Anonymous, "this", name, "expressions", nonNilList(args))
	}
	to := AndThen(p.Match(tokens.Alias), func() *exp.Expr { return p.ParseTypes(false, false, true, false) })
	return p.Expr(exp.WeightString, "this", args[0], "to", to)
}

func (p *Parser) parseCast(strict bool, safe any) *exp.Expr {
	this := p.ParseAssignment()
	if !p.Match(tokens.Alias) {
		if p.Match(tokens.Comma) {
			return p.Expr(exp.CastToStrType, "this", this, "to", p.ParseString())
		}
		p.RaiseError("Expected AS after CAST")
	}
	var fmtExpr *exp.Expr
	to := p.ParseTypes(false, false, true, true)
	var def *exp.Expr
	if p.Match(tokens.Default) {
		def = p.ParseBitwise()
		p.MatchTextSeq("ON", "CONVERSION", "ERROR")
	}
	if p.MatchSet(tokens.NewSet(tokens.Format, tokens.Comma)) {
		fmtString := p.ParseWrappedExpr(p.ParseString, true)
		fmtExpr = p.ParseAtTimeZone(fmtString)
		if to == nil {
			to = exp.DTypeUnknown.IntoExpr()
		}
		if dt := to.DataTypeThis(); dt != "" && exp.DataTypeTemporalTypes.Has(dt) {
			kind := exp.StrToTime
			if dt == exp.DTypeDate {
				kind = exp.StrToDate
			}
			mapping := p.D.FormatMapping
			tr := p.D.FormatTrie
			if len(mapping) == 0 {
				mapping = p.D.TimeMapping
				tr = p.D.TimeTrie
			}
			fs := ""
			if fmtString != nil {
				fs = fmtString.ThisStr()
			}
			this = p.Expr(kind, "this", this, "format", exp.LiteralString(FormatTime(fs, mapping, tr)), "safe", safe)
			if fmtExpr.Is(exp.AtTimeZone) && this.Is(exp.StrToTime) {
				this.Set("zone", fmtExpr.Get("zone"))
			}
			return this
		}
	} else if to == nil {
		p.RaiseError("Expected TYPE after CAST")
	} else if to.Is(exp.Identifier) {
		dt, err := exp.DataTypeBuild(to.Name(), p.D, true)
		if err != nil {
			panic(parseBailoutFrom(err))
		}
		to = dt
	} else {
		to = p.ParseCharCharset(to)
	}
	return p.BuildCast(strict, "this", this, "to", to, "format", fmtExpr, "safe", safe,
		"action", p.ParseVarFromOptions(p.C.CastActions, false), "default", def)
}

func (p *Parser) parseStringAgg() *exp.Expr {
	var args []*exp.Expr
	if p.Match(tokens.Distinct) {
		args = []*exp.Expr{p.Expr(exp.Distinct, "expressions", []*exp.Expr{p.ParseDisjunction()})}
		if p.Match(tokens.Comma) {
			args = append(args, p.ParseCSV(p.ParseDisjunction)...)
		}
	} else {
		p.Match(tokens.All)
		args = p.ParseCSV(p.ParseDisjunction)
	}
	var onOverflow *exp.Expr
	if p.MatchTextSeq("ON", "OVERFLOW") {
		if p.MatchTextSeq("ERROR") {
			onOverflow = exp.NewVar("ERROR")
		} else {
			p.MatchTextSeq("TRUNCATE")
			this := p.ParseString()
			withCount := p.MatchTextSeq("WITH", "COUNT") || !p.MatchTextSeq("WITHOUT", "COUNT")
			onOverflow = p.Expr(exp.OverflowTruncateBehavior, "this", this, "with_count", withCount)
		}
	}
	index := p.Index
	if !p.Match(tokens.RParen) && len(args) > 0 {
		args[0] = p.ParseLimit(p.ParseOrder(args[0], false), false, false)
		return p.Expr(exp.GroupConcat, "this", args[0], "separator", seqGet(args, 1))
	}
	if !p.MatchTextSeq("WITHIN", "GROUP") {
		p.Retreat(index)
		return p.ValidateExpression(exp.FromArgList(exp.GroupConcat, args), args)
	}
	p.MatchLParen(nil)
	return p.Expr(exp.GroupConcat, "this", p.ParseOrder(seqGet(args, 0), false), "separator", seqGet(args, 1), "on_overflow", onOverflow)
}

func (p *Parser) parseConvert(strict bool, safe any) *exp.Expr {
	this := p.ParseBitwise()
	var to *exp.Expr
	if p.Match(tokens.Using) {
		to = exp.DTypeCharacterSet.IntoExpr("kind", p.ParseCharsetName())
	} else if p.Match(tokens.Comma) {
		to = p.ParseCharCharset(p.ParseTypes(false, false, true, false))
	}
	return p.BuildCast(strict, "this", this, "to", to, "safe", safe)
}

func (p *Parser) parseCharCharset(to *exp.Expr) *exp.Expr {
	if to.Is(exp.DataType) && to.DataTypeThis() == exp.DTypeChar && (p.Match(tokens.CharacterSet) || p.MatchTextSeq("CHARACTER", "SET")) {
		to.Set("this", exp.DTypeCharacterSet)
		to.Set("kind", p.ParseVarOrString(false))
	}
	return to
}

func (p *Parser) parseXmlElement() *exp.Expr {
	var evalname any
	var this *exp.Expr
	if p.MatchTextSeq("EVALNAME") {
		evalname = true
		this = p.ParseBitwise()
	} else {
		p.MatchTextSeq("NAME")
		this = p.ParseIdVar(true, nil)
	}
	var expressions any = false
	if p.Match(tokens.Comma) {
		expressions = p.ParseCSV(p.ParseBitwise)
	}
	return p.Expr(exp.XMLElement, "this", this, "expressions", expressions, "evalname", evalname)
}

func (p *Parser) parseXmlTable() *exp.Expr {
	var namespaces, passing, columns any
	if p.MatchTextSeq("XMLNAMESPACES", "(") {
		namespaces = p.ParseXmlNamespace()
		p.MatchTextSeq(")", ",")
	}
	this := p.ParseString()
	if p.MatchTextSeq("PASSING") {
		p.MatchTextSeq("BY", "VALUE")
		passing = p.ParseCSV(p.ParseColumn)
	}
	byRef := p.MatchTextSeq("RETURNING", "SEQUENCE", "BY", "REF")
	if p.MatchTextSeq("COLUMNS") {
		columns = p.ParseCSV(p.ParseFieldDef)
	}
	return p.Expr(exp.XMLTable, "this", this, "namespaces", namespaces, "passing", passing, "columns", columns, "by_ref", byRef)
}

func (p *Parser) parseXmlNamespace() []*exp.Expr {
	var namespaces []*exp.Expr
	for {
		var uri *exp.Expr
		if p.Match(tokens.Default) {
			uri = p.ParseString()
		} else {
			uri = p.ParseAlias(p.ParseString(), false)
		}
		namespaces = append(namespaces, p.Expr(exp.XMLNamespace, "this", uri))
		if !p.Match(tokens.Comma) {
			break
		}
	}
	return namespaces
}

func (p *Parser) parseDecode() *exp.Expr {
	args := p.ParseCSV(p.ParseDisjunction)
	if len(args) < 3 {
		return p.Expr(exp.Decode, "this", seqGet(args, 0), "charset", seqGet(args, 1))
	}
	return p.Expr(exp.DecodeCase, "expressions", args)
}

func (p *Parser) parseJsonKeyValue() *exp.Expr {
	p.MatchTextSeq("KEY")
	key := p.ParseColumn()
	p.MatchSet(p.C.JsonKeyValueSeparatorTokens)
	p.MatchTextSeq("VALUE")
	value := p.ParseBitwise()
	if key == nil && value == nil {
		return nil
	}
	return p.Expr(exp.JSONKeyValue, "this", key, "expression", value)
}

func (p *Parser) parseFormatJson(this *exp.Expr) *exp.Expr {
	if this == nil || !p.MatchTextSeq("FORMAT", "JSON") {
		return this
	}
	return p.Expr(exp.FormatJson, "this", this)
}

func (p *Parser) parseOnCondition() *exp.Expr {
	var empty, errorH any
	vals := sortedStrings(p.C.OnConditionTokens)
	if p.D.OnConditionEmptyBeforeError {
		empty = p.ParseOnHandling("EMPTY", vals...)
		errorH = p.ParseOnHandling("ERROR", vals...)
	} else {
		errorH = p.ParseOnHandling("ERROR", vals...)
		empty = p.ParseOnHandling("EMPTY", vals...)
	}
	null := p.ParseOnHandling("NULL", vals...)
	if empty == nil && errorH == nil && null == nil {
		return nil
	}
	return p.Expr(exp.OnCondition, "empty", empty, "error", errorH, "null", null)
}

func sortedStrings(s StringSet) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// parseOnHandling returns a string ("X ON Y"), an expression (DEFAULT <expr> ON Y) or nil.
func (p *Parser) parseOnHandling(on string, values ...string) any {
	for _, value := range values {
		if p.MatchTextSeq(value, "ON", on) {
			return fmt.Sprintf("%s ON %s", value, on)
		}
	}
	index := p.Index
	if p.Match(tokens.Default) {
		def := p.ParseBitwise()
		if p.MatchTextSeq("ON", on) {
			return nilIfNil(def)
		}
		p.Retreat(index)
	}
	return nil
}

func (p *Parser) parseJsonObject(agg bool) *exp.Expr {
	star := p.ParseStar()
	var expressions []*exp.Expr
	if star != nil {
		expressions = []*exp.Expr{star}
	} else {
		expressions = p.ParseCSV(func() *exp.Expr { return p.ParseFormatJson(p.ParseJsonKeyValue()) })
	}
	nullHandling := p.ParseOnHandling("NULL", "NULL", "ABSENT")
	var uniqueKeys any
	if p.MatchTextSeq("WITH", "UNIQUE") {
		uniqueKeys = true
	} else if p.MatchTextSeq("WITHOUT", "UNIQUE") {
		uniqueKeys = false
	}
	p.MatchTextSeq("KEYS")
	returnType := AndThen(p.MatchTextSeq("RETURNING"), func() *exp.Expr { return p.ParseFormatJson(p.ParseType(true, false)) })
	encoding := AndThen(p.MatchTextSeq("ENCODING"), func() *exp.Expr { return p.ParseVar(false, nil, false) })
	kind := exp.JSONObject
	if agg {
		kind = exp.JSONObjectAgg
	}
	return p.Expr(kind, "expressions", nonNilList(expressions), "null_handling", nullHandling, "unique_keys", uniqueKeys,
		"return_type", returnType, "encoding", encoding)
}

func (p *Parser) parseJsonColumnDef() *exp.Expr {
	var this, kind *exp.Expr
	var ordinality, nested any
	if !p.MatchTextSeq("NESTED") {
		this = p.ParseIdVar(true, nil)
		ordinality = p.MatchPair(tokens.For, tokens.Ordinality)
		kind = p.ParseTypes(false, false, false, false)
	} else {
		nested = true
	}
	formatJson := p.MatchTextSeq("FORMAT", "JSON")
	path := AndThen(p.MatchTextSeq("PATH"), p.ParseString)
	var nestedSchema any
	if nested == true {
		nestedSchema = nilIfNil(p.ParseJsonSchema())
	} else {
		nestedSchema = nested
	}
	return p.Expr(exp.JSONColumnDef, "this", this, "kind", kind, "path", path, "nested_schema", nestedSchema,
		"ordinality", ordinality, "format_json", formatJson)
}

func (p *Parser) parseJsonSchema() *exp.Expr {
	p.MatchTextSeq("COLUMNS")
	return p.Expr(exp.JSONSchema, "expressions", nonNilList(p.ParseWrappedCSV(p.ParseJsonColumnDef, true)))
}

func (p *Parser) parseJsonTable() *exp.Expr {
	this := p.ParseFormatJson(p.ParseBitwise())
	path := AndThen(p.Match(tokens.Comma), p.ParseString)
	errorHandling := p.ParseOnHandling("ERROR", "ERROR", "NULL")
	emptyHandling := p.ParseOnHandling("EMPTY", "ERROR", "NULL")
	schema := p.ParseJsonSchema()
	return exp.New(exp.JSONTable, "this", this, "schema", schema, "path", path, "error_handling", errorHandling, "empty_handling", emptyHandling)
}

func (p *Parser) parseMatchAgainst() *exp.Expr {
	var expressions []*exp.Expr
	if p.MatchTextSeq("TABLE") {
		expressions = []*exp.Expr{}
		if table := p.ParseTable(TableOpts{}); table != nil {
			expressions = []*exp.Expr{table}
		}
	} else {
		expressions = p.ParseCSV(p.ParseColumn)
	}
	p.MatchTextSeq(")", "AGAINST", "(")
	this := p.ParseString()
	var modifier any
	if p.MatchTextSeq("IN", "NATURAL", "LANGUAGE", "MODE") {
		m := "IN NATURAL LANGUAGE MODE"
		if p.MatchTextSeq("WITH", "QUERY", "EXPANSION") {
			m += " WITH QUERY EXPANSION"
		}
		modifier = m
	} else if p.MatchTextSeq("IN", "BOOLEAN", "MODE") {
		modifier = "IN BOOLEAN MODE"
	} else if p.MatchTextSeq("WITH", "QUERY", "EXPANSION") {
		modifier = "WITH QUERY EXPANSION"
	}
	return p.Expr(exp.MatchAgainst, "this", this, "expressions", nonNilList(expressions), "modifier", modifier)
}

func (p *Parser) parseOpenJson() *exp.Expr {
	this := p.ParseBitwise()
	path := AndThen(p.Match(tokens.Comma), p.ParseString)
	colDef := func() *exp.Expr {
		this := p.ParseField(true, nil, false)
		kind := p.ParseTypes(false, false, true, false)
		path := p.ParseString()
		asJSON := p.MatchPair(tokens.Alias, tokens.Json)
		return p.Expr(exp.OpenJSONColumnDef, "this", this, "kind", kind, "path", path, "as_json", asJSON)
	}
	var expressions any
	if p.MatchPair(tokens.RParen, tokens.With) {
		p.MatchLParen(nil)
		expressions = p.ParseCSV(colDef)
	}
	return p.Expr(exp.OpenJSON, "this", this, "path", path, "expressions", expressions)
}

func (p *Parser) parsePosition(haystackFirst bool) *exp.Expr {
	args := p.ParseCSV(p.ParseBitwise)
	if p.Match(tokens.In) {
		return p.Expr(exp.StrPosition, "this", p.ParseBitwise(), "substr", seqGet(args, 0))
	}
	var haystack, needle *exp.Expr
	if haystackFirst {
		haystack, needle = seqGet(args, 0), seqGet(args, 1)
	} else {
		haystack, needle = seqGet(args, 1), seqGet(args, 0)
	}
	return p.Expr(exp.StrPosition, "this", haystack, "substr", needle, "position", seqGet(args, 2))
}

func (p *Parser) buildCast(strict bool, kv ...any) *exp.Expr {
	kind := exp.Cast
	if !strict {
		kind = exp.TryCast
		var trs any
		if p.D.TryCastRequiresString != nil {
			trs = *p.D.TryCastRequiresString
		}
		kv = append(kv, "requires_string", trs)
	}
	return p.Expr(kind, kv...)
}

func (p *Parser) parseJsonValue(jsonPath bool) *exp.Expr {
	this := p.ParseBitwise()
	p.Match(tokens.Comma)
	path := p.ParseBitwise()
	returning := AndThen(p.Match(tokens.Returning), func() *exp.Expr { return p.ParseTypes(false, false, true, false) })
	if jsonPath {
		path = p.D.ToJSONPath(path)
	}
	return p.Expr(exp.JSONValue, "this", this, "path", path, "returning", returning, "on_condition", p.ParseOnCondition())
}

func (p *Parser) parseGroupConcat() *exp.Expr {
	args := p.ParseCSV(func() *exp.Expr { return p.ParseLambda(false) })
	concatExprs := func(node *exp.Expr, exprs []*exp.Expr) *exp.Expr {
		if node.Is(exp.Distinct) && len(node.Expressions()) > 1 {
			c := []*exp.Expr{p.Expr(exp.Concat, "expressions", node.Expressions(), "safe", true, "coalesce", p.D.ConcatCoalesce)}
			node.Set("expressions", c)
			return node
		}
		if len(exprs) == 1 {
			return exprs[0]
		}
		return p.Expr(exp.Concat, "expressions", args, "safe", true, "coalesce", p.D.ConcatCoalesce)
	}
	var this *exp.Expr
	if len(args) > 0 {
		var order *exp.Expr
		if last := args[len(args)-1]; last.Is(exp.Order) {
			order = last
		}
		if order != nil {
			args[len(args)-1] = order.This()
			order.Set("this", concatExprs(order.This(), args))
		}
		if order != nil {
			this = order
		} else {
			this = concatExprs(args[0], args)
		}
	}
	var separator *exp.Expr
	if p.Match(tokens.Separator) {
		separator = p.ParseField(false, nil, false)
	}
	return p.Expr(exp.GroupConcat, "this", this, "separator", separator)
}

func (p *Parser) parseInitcap() *exp.Expr {
	e := exp.FromArgList(exp.Initcap, p.ParseFunctionArgs(false))
	if e.Arg("expression") == nil {
		e.Set("expression", exp.LiteralString(p.D.InitcapDefaultDelimiterChars))
	}
	return e
}

func (p *Parser) parseOperator(this *exp.Expr) *exp.Expr {
	if !p.Match(tokens.LParen) {
		p.Retreat(p.Index - 1)
		return nil
	}
	var op strings.Builder
	for p.Curr.Ok() && !p.Match(tokens.RParen) {
		op.WriteString(p.Curr.Text)
		p.Advance()
	}
	comments := p.PrevComments
	return p.ExpressionComments(exp.New(exp.Operator, "this", this, "operator", op.String(), "expression", p.ParseBitwise()), comments)
}
