package sqlglot

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

// PrevTextUpper returns upper(p.Prev.Text).
func (p *Parser) PrevTextUpper() string { return strings.ToUpper(p.Prev.Text) }

func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ExprList flattens a parse result (nil, *exp.Expr or []*exp.Expr) into a list.
func ExprList(v any) []*exp.Expr {
	switch x := v.(type) {
	case nil:
		return nil
	case *exp.Expr:
		if x == nil {
			return nil
		}
		return []*exp.Expr{x}
	case []*exp.Expr:
		return x
	case []any:
		var out []*exp.Expr
		for _, y := range x {
			out = append(out, ExprList(y)...)
		}
		return out
	}
	return nil
}

func kwToKV(kw map[string]any) []any {
	keys := make([]string, 0, len(kw))
	for k := range kw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kv := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		kv = append(kv, k, kw[k])
	}
	return kv
}

func (p *Parser) parseCommand() *exp.Expr {
	p.WarnUnsupported()
	comments := p.PrevComments
	return p.ExpressionComments(exp.New(exp.Command, "this", p.PrevTextUpper(), "expression", p.ParseString()), comments)
}

func (p *Parser) parseComment(allowExists bool) *exp.Expr {
	start := p.Prev
	var exists any
	if allowExists {
		exists = p.ParseExists(false)
	}
	p.Match(tokens.On)
	materialized := p.MatchTextSeq("MATERIALIZED")
	var kind *tokens.Token
	if p.MatchSet(p.C.Creatables) {
		kind = p.Prev
	}
	if kind == nil {
		return p.ParseAsCommand(start)
	}
	var this *exp.Expr
	switch kind.Type {
	case tokens.Function, tokens.Procedure:
		this = p.ParseUserDefinedFunction(kind.Type)
	case tokens.Table:
		ts := p.C.CommentTableAliasTokens
		this = p.ParseTable(TableOpts{AliasTokens: &ts})
	case tokens.Column:
		this = p.ParseColumn()
	default:
		this = p.ParseTableParts(true, false, false, false)
	}
	p.Match(tokens.Is)
	return p.Expr(exp.Comment, "this", this, "kind", kind.Text, "expression", p.ParseString(),
		"exists", exists, "materialized", materialized)
}

func (p *Parser) parseToTable() *exp.Expr {
	table := p.ParseTableParts(true, false, false, false)
	return p.Expr(exp.ToTableProperty, "this", table)
}

func (p *Parser) parseTtl() *exp.Expr {
	parseTtlAction := func() *exp.Expr {
		this := p.ParseBitwise()
		if p.MatchTextSeq("DELETE") {
			return p.Expr(exp.MergeTreeTTLAction, "this", this, "delete", true)
		}
		if p.MatchTextSeq("RECOMPRESS") {
			return p.Expr(exp.MergeTreeTTLAction, "this", this, "recompress", p.ParseBitwise())
		}
		if p.MatchTextSeq("TO", "DISK") {
			return p.Expr(exp.MergeTreeTTLAction, "this", this, "to_disk", p.ParseString())
		}
		if p.MatchTextSeq("TO", "VOLUME") {
			return p.Expr(exp.MergeTreeTTLAction, "this", this, "to_volume", p.ParseString())
		}
		return this
	}
	expressions := p.ParseCSV(parseTtlAction)
	where := p.ParseWhere(false)
	group := p.ParseGroup(false)
	var aggregates any
	if group != nil && p.Match(tokens.Set) {
		aggregates = p.ParseCSV(p.ParseSetItem)
	}
	return p.Expr(exp.MergeTreeTTL, "expressions", expressions, "where", where, "group", group, "aggregates", aggregates)
}

func (p *Parser) parseCondition() *exp.Expr {
	return p.ParseWrappedExpr(p.ParseExpression, true)
}

func (p *Parser) parseBlock() *exp.Expr {
	return p.Expr(exp.Block, "expressions", p.parseBatchStatements(func(p *Parser) *exp.Expr { return p.ParseStatement() }, true))
}

func (p *Parser) parseWhileblock() *exp.Expr {
	return p.Expr(exp.WhileBlock, "this", p.ParseCondition(), "body", p.ParseBlock())
}

func (p *Parser) parseStatement() *exp.Expr {
	if !p.Curr.Ok() {
		return nil
	}
	if MatchIn(p, p.C.StatementParsers) {
		comments := p.PrevComments
		stmt := p.C.StatementParsers[p.Prev.Type](p)
		if stmt != nil {
			stmt.AddComments(comments, true)
		}
		return stmt
	}
	if p.MatchSet(p.D.Tokenizer.Commands) {
		return p.ParseCommand()
	}
	if p.MatchTextSeq("WHILE") {
		return p.ParseWhileblock()
	}
	expression := p.ParseExpression()
	if expression != nil {
		expression = p.ParseSetOperations(expression)
	} else {
		expression = p.ParseSelect(SelectOpts{})
	}
	if expression.Is(exp.Subquery) && p.Check(tokens.PipeGt) {
		expression = p.ParsePipeSyntaxQuery(expression)
	}
	return p.ParseQueryModifiers(expression)
}

func (p *Parser) parseDrop(exists bool, kind string) *exp.Expr {
	start := p.Prev
	var temporary, materialized, iceberg bool
	if kind == "" {
		temporary = p.Match(tokens.Temporary)
		materialized = p.MatchTextSeq("MATERIALIZED")
		iceberg = p.MatchTextSeq("ICEBERG")
		if p.MatchSet(p.C.Creatables) {
			kind = p.PrevTextUpper()
		}
	}
	if kind == "" || (iceberg && kind != "TABLE") {
		return p.ParseAsCommand(start)
	}
	concurrently := kind == "INDEX" && p.C.IdVarTokens.Has(p.Next.Type) && p.MatchTextSeq("CONCURRENTLY")
	ifExists := exists || p.ParseExists(false)

	var tables []*exp.Expr
	switch kind {
	case "COLUMN":
		var name *exp.Expr
		if _, ok := p.C.NoParenFunctionParsers[strings.ToUpper(p.Curr.Text)]; !ok {
			name = p.ParseColumn()
		}
		if name == nil {
			name = p.ParseIdVar(true, nil)
		}
		if name.Is(exp.Identifier) {
			tables = []*exp.Expr{p.Expr(exp.Column, "this", name)}
		} else if name != nil {
			tables = []*exp.Expr{name}
		}
	case "TABLE", "VIEW":
		tables = p.ParseCSV(func() *exp.Expr { return p.ParseTableParts(true, false, false, false) })
	default:
		if t := p.ParseTableParts(true, kind == "SCHEMA", false, false); t != nil {
			tables = []*exp.Expr{t}
		}
	}

	var cluster *exp.Expr
	if p.Match(tokens.On) {
		if kind == "INDEX" || kind == "TRIGGER" {
			cluster = p.Expr(exp.OnProperty, "this", p.ParseTableParts(false, false, false, false))
		} else {
			cluster = p.ParseOnProperty()
		}
	}
	var expressions any
	if p.Check(tokens.LParen) {
		expressions = p.ParseWrappedCSV(func() *exp.Expr { return p.ParseTypes(false, false, true, false) }, false)
	}
	cascadeOrRestrict := ""
	if p.MatchTexts("CASCADE", "RESTRICT") {
		cascadeOrRestrict = p.PrevTextUpper()
	}
	k := kind
	if m, ok := p.D.CreatableKindMapping[kind]; ok && m != "" {
		k = m
	}
	if tables == nil {
		tables = []*exp.Expr{}
	}
	return p.Expr(exp.Drop,
		"exists", ifExists,
		"tables", tables,
		"expressions", expressions,
		"kind", k,
		"temporary", temporary,
		"materialized", materialized,
		"cascade", cascadeOrRestrict == "CASCADE",
		"restrict", cascadeOrRestrict == "RESTRICT",
		"constraints", p.MatchTextSeq("CONSTRAINTS"),
		"purge", p.MatchTextSeq("PURGE"),
		"cluster", cluster,
		"concurrently", concurrently,
		"sync", p.MatchTextSeq("SYNC"),
		"iceberg", iceberg,
		"force", p.MatchTextSeq("FORCE"),
	)
}

func (p *Parser) parseExists(not bool) bool {
	if not {
		return p.MatchTextSeq("IF", "NOT", "EXISTS")
	}
	return p.MatchTextSeq("IF", "EXISTS")
}

func (p *Parser) parseCreate() *exp.Expr {
	start := p.Prev
	replace := start.Type == tokens.Replace || p.MatchPair(tokens.Or, tokens.Replace) || p.MatchPair(tokens.Or, tokens.Alter)
	refresh := p.MatchPair(tokens.Or, tokens.Refresh)
	unique := p.Match(tokens.Unique)

	var clustered any
	if p.MatchTextSeq("CLUSTERED", "COLUMNSTORE") {
		clustered = true
	} else if p.MatchTextSeq("NONCLUSTERED", "COLUMNSTORE") || p.MatchTextSeq("COLUMNSTORE") {
		clustered = false
	}

	if p.CheckPair(tokens.Table, tokens.Function) {
		p.Advance()
	}

	var properties *exp.Expr
	var createToken *tokens.Token
	if p.MatchSet(p.C.Creatables) {
		createToken = p.Prev
	}
	if createToken == nil {
		properties = p.ParseProperties(false)
		if p.MatchSet(p.C.Creatables) {
			createToken = p.Prev
		}
		if properties == nil || createToken == nil {
			return p.ParseAsCommand(start)
		}
	}
	createTokenType := createToken.Type

	concurrently := createTokenType == tokens.Index && p.MatchTextSeq("CONCURRENTLY")
	exists := p.ParseExists(true)
	var this, expression *exp.Expr
	var indexes any
	var noSchemaBinding any
	var begin any
	var clone *exp.Expr

	extendProps := func(tempProps *exp.Expr) {
		if properties != nil && tempProps != nil {
			properties.Set("expressions", append(append([]*exp.Expr(nil), properties.Expressions()...), tempProps.Expressions()...))
		} else if tempProps != nil {
			properties = tempProps
		}
	}

	switch {
	case createTokenType == tokens.Function || createTokenType == tokens.Procedure:
		this = p.ParseUserDefinedFunction(createTokenType)
		extendProps(p.ParseProperties(false))
		if p.Match(tokens.Alias) {
			expression = p.ParseHeredoc()
		}
		isTable := false
		overloadMode := false
		if expression == nil && createTokenType == tokens.Function && this.Is(exp.UserDefinedFunction) && this.Bool("wrapped") {
			preTableIndex := p.Index
			isTable = p.Match(tokens.Table)
			expression = p.ParseExpression()
			overloadMode = expression != nil && p.Curr.Type == tokens.Comma && p.Next.Type == tokens.LParen
			if !overloadMode {
				p.Retreat(preTableIndex)
				isTable = false
				expression = nil
			}
		}
		extendProps(p.ParseFunctionProperties())
		if expression == nil {
			if p.Match(tokens.Command) {
				expression = p.ParseAsCommand(p.Prev)
			} else {
				begin = p.Match(tokens.Begin)
				return_ := p.MatchTextSeq("RETURN")
				if p.Check(tokens.String) {
					expression = p.ParseString()
					extendProps(p.ParseProperties(false))
				} else if createTokenType == tokens.Function {
					expression = p.ParseUserDefinedFunctionExpression()
				} else {
					expression = p.ParseBlock()
				}
				if return_ {
					expression = p.Expr(exp.Return, "this", expression)
				}
			}
		}
		if overloadMode && expression != nil {
			expression = p.ParseMacroOverloads(this, expression, isTable)
		}
	case createTokenType == tokens.Index:
		var index *exp.Expr
		anonymous := false
		if !p.Match(tokens.On) {
			index = p.ParseIdVar(true, nil)
		} else {
			anonymous = true
		}
		this = p.ParseIndex(index, anonymous)
	case (createTokenType == tokens.Constraint && p.Match(tokens.Trigger)) || createTokenType == tokens.Trigger:
		isConstraint := createTokenType == tokens.Constraint
		if isConstraint {
			createToken = p.Prev
		}
		triggerName := p.ParseIdVar(true, nil)
		if triggerName == nil {
			return p.ParseAsCommand(start)
		}
		timingVar := p.ParseVarFromOptions(p.C.TriggerTiming, false)
		var timing any
		if timingVar != nil {
			timing = timingVar.Arg("this")
		}
		if !exp.Truthy(timing) {
			return p.ParseAsCommand(start)
		}
		events := p.ParseTriggerEvents()
		if !p.Match(tokens.On) {
			p.RaiseError("Expected ON in trigger definition")
		}
		table := p.ParseTableParts(false, false, false, false)
		var referencedTable *exp.Expr
		if p.Match(tokens.From) {
			referencedTable = p.ParseTableParts(false, false, false, false)
		}
		deferrable, initially := p.ParseTriggerDeferrable()
		referencing := p.ParseTriggerReferencing()
		forEach := p.ParseTriggerForEach()
		var when any = false
		if p.MatchTextSeq("WHEN") {
			when = nilIfNil(p.ParseWrappedExpr(p.ParseDisjunction, true))
		}
		execute := p.ParseTriggerExecute()
		if execute == nil {
			return p.ParseAsCommand(start)
		}
		triggerProps := p.Expr(exp.TriggerProperties,
			"table", table,
			"timing", timing,
			"events", events,
			"execute", execute,
			"constraint", isConstraint,
			"referenced_table", referencedTable,
			"deferrable", strOrNil(deferrable),
			"initially", strOrNil(initially),
			"referencing", referencing,
			"for_each", strOrNil(forEach),
			"when", when,
		)
		this = triggerName
		extendProps(exp.New(exp.Properties, "expressions", []*exp.Expr{triggerProps}))
	case createTokenType == tokens.Type:
		this = p.ParseTableParts(true, false, false, false)
		if this == nil || !p.Match(tokens.Alias) {
			return p.ParseAsCommand(start)
		}
		if p.Match(tokens.Enum) {
			expression = exp.New(exp.DataType, "this", exp.DTypeEnum, "expressions", p.ParseWrappedCSV(p.ParseString, false))
		} else if p.Check(tokens.LParen) {
			expression = p.ParseSchema(nil)
		} else {
			return p.ParseAsCommand(start)
		}
	case p.C.DbCreatables.Has(createTokenType):
		tableParts := p.ParseTableParts(true, createTokenType == tokens.Schema, false, false)
		p.Match(tokens.Comma)
		extendProps(p.ParseProperties(true))
		this = p.ParseSchema(tableParts)
		extendProps(p.ParseProperties(false))
		hasAlias := p.Match(tokens.Alias)
		if !p.CheckSet(p.C.DdlSelectTokens) {
			extendProps(p.ParseProperties(false))
		}
		if createTokenType == tokens.Sequence {
			expression = p.ParseTypes(false, false, true, false)
			props := p.ParseProperties(false)
			if props != nil {
				sequenceProps := exp.New(exp.SequenceProperties)
				var options []*exp.Expr
				for _, prop := range append([]*exp.Expr(nil), props.Expressions()...) {
					if prop.Is(exp.SequenceProperties) {
						for _, a := range prop.Args() {
							if a.Key == "options" {
								options = append(options, ExprList(a.Value)...)
							} else {
								sequenceProps.Set(a.Key, a.Value)
							}
						}
						prop.Pop()
					}
				}
				if len(options) > 0 {
					sequenceProps.Set("options", options)
				}
				props.Append("expressions", sequenceProps)
				extendProps(props)
			}
		} else {
			expression = p.ParseDdlSelect()
			if expression == nil && hasAlias {
				expression = p.TryParse(func() *exp.Expr { return p.ParseTableParts(false, false, false, false) }, false)
			}
		}
		switch {
		case createTokenType == tokens.Table:
			extendProps(p.ParseProperties(false))
			idx := []*exp.Expr{}
			for {
				index := p.ParseIndex(nil, false)
				extendProps(p.ParseProperties(false))
				if index == nil {
					break
				}
				p.Match(tokens.Comma)
				idx = append(idx, index)
			}
			indexes = idx
		case createTokenType == tokens.View:
			if p.MatchTextSeq("WITH", "NO", "SCHEMA", "BINDING") {
				noSchemaBinding = true
			}
		case createTokenType == tokens.Sink || createTokenType == tokens.Source:
			extendProps(p.ParseProperties(false))
		}
		shallow := p.MatchTextSeq("SHALLOW")
		if p.MatchTextsIn(p.C.CloneKeywords) {
			copy := strings.ToLower(p.Prev.Text) == "copy"
			clone = p.Expr(exp.Clone, "this", p.ParseTable(TableOpts{Schema: true}), "shallow", shallow, "copy", copy)
		}
	}

	if p.Curr.Ok() && !p.CheckSet(tokens.NewSet(tokens.RParen, tokens.Comma)) {
		return p.ParseAsCommand(start)
	}
	createKindText := strings.ToUpper(createToken.Text)
	kind := createKindText
	if m := p.D.CreatableKindMapping[createKindText]; m != "" {
		kind = m
	}
	return p.Expr(exp.Create,
		"this", this,
		"kind", kind,
		"replace", replace,
		"refresh", refresh,
		"unique", unique,
		"expression", expression,
		"exists", exists,
		"properties", properties,
		"indexes", indexes,
		"no_schema_binding", noSchemaBinding,
		"begin", begin,
		"clone", clone,
		"concurrently", concurrently,
		"clustered", clustered,
	)
}

func (p *Parser) parseSequenceProperties() *exp.Expr {
	seq := exp.New(exp.SequenceProperties)
	var options []*exp.Expr
	index := p.Index
	for p.Curr.Ok() {
		p.Match(tokens.Comma)
		if p.MatchTextSeq("INCREMENT") {
			p.MatchTextSeq("BY")
			p.MatchTextSeq("=")
			seq.Set("increment", p.ParseTerm(true))
		} else if p.MatchTextSeq("MINVALUE") {
			seq.Set("minvalue", p.ParseTerm(true))
		} else if p.MatchTextSeq("MAXVALUE") {
			seq.Set("maxvalue", p.ParseTerm(true))
		} else if p.MatchTextSeq("START") {
			p.MatchTextSeq("WITH")
			p.MatchTextSeq("=")
			seq.Set("start", p.ParseTerm(true))
		} else if p.MatchTextSeq("CACHE") {
			if n := p.ParseNumber(); n != nil {
				seq.Set("cache", n)
			} else {
				seq.Set("cache", true)
			}
		} else if p.MatchTextSeq("OWNED", "BY") {
			if p.MatchTextSeq("NONE") {
				seq.Set("owned", nil)
			} else {
				seq.Set("owned", p.ParseColumn())
			}
		} else {
			opt := p.ParseVarFromOptions(p.C.CreateSequence, false)
			if opt != nil {
				options = append(options, opt)
			} else {
				break
			}
		}
	}
	if len(options) > 0 {
		seq.Set("options", options)
	} else {
		seq.Set("options", nil)
	}
	if p.Index == index {
		return nil
	}
	return seq
}

func (p *Parser) parseTriggerEvents() []*exp.Expr {
	var events []*exp.Expr
	for {
		eventType := ""
		if p.MatchSet(p.C.TriggerEvents) {
			eventType = p.PrevTextUpper()
		}
		if eventType == "" {
			p.RaiseError("Expected trigger event (INSERT, UPDATE, DELETE, TRUNCATE)")
		}
		var columns any
		if eventType == "UPDATE" && p.MatchTextSeq("OF") {
			columns = p.ParseCSV(p.ParseColumn)
		}
		var et any = eventType
		if eventType == "" {
			et = false
		}
		events = append(events, p.Expr(exp.TriggerEvent, "this", et, "columns", columns))
		if !p.Match(tokens.Or) {
			break
		}
	}
	return events
}

func (p *Parser) parseTriggerDeferrable() (string, string) {
	deferrableVar := p.ParseVarFromOptions(p.C.TriggerDeferrable, false)
	deferrable := ""
	if deferrableVar != nil {
		deferrable = deferrableVar.ThisStr()
	}
	initially := ""
	if deferrable != "" && p.MatchTextSeq("INITIALLY") {
		if p.MatchTexts("IMMEDIATE", "DEFERRED") {
			initially = p.PrevTextUpper()
		}
	}
	return deferrable, initially
}

func (p *Parser) parseTriggerReferencingClause(keyword string) *exp.Expr {
	if !p.MatchTextSeq(keyword) {
		return nil
	}
	if !p.MatchTextSeq("TABLE") {
		p.RaiseError(fmt.Sprintf("Expected TABLE after %s in REFERENCING clause", keyword))
	}
	p.MatchTextSeq("AS")
	return p.ParseIdVar(true, nil)
}

func (p *Parser) parseTriggerReferencing() *exp.Expr {
	if !p.MatchTextSeq("REFERENCING") {
		return nil
	}
	var oldAlias, newAlias *exp.Expr
	for {
		if alias := p.ParseTriggerReferencingClause("OLD"); alias != nil {
			if oldAlias != nil {
				p.RaiseError("Duplicate OLD clause in REFERENCING")
			}
			oldAlias = alias
		} else if alias := p.ParseTriggerReferencingClause("NEW"); alias != nil {
			if newAlias != nil {
				p.RaiseError("Duplicate NEW clause in REFERENCING")
			}
			newAlias = alias
		} else {
			break
		}
	}
	if oldAlias == nil && newAlias == nil {
		p.RaiseError("REFERENCING clause requires at least OLD TABLE or NEW TABLE")
	}
	return p.Expr(exp.TriggerReferencing, "old", oldAlias, "new", newAlias)
}

func (p *Parser) parseTriggerForEach() string {
	if !p.MatchTextSeq("FOR", "EACH") {
		return ""
	}
	if p.MatchTexts("ROW", "STATEMENT") {
		return p.PrevTextUpper()
	}
	return ""
}

func (p *Parser) parseTriggerExecute() *exp.Expr {
	if !p.Match(tokens.Execute) {
		return nil
	}
	if !p.MatchSet(tokens.NewSet(tokens.Function, tokens.Procedure)) {
		p.RaiseError("Expected FUNCTION or PROCEDURE after EXECUTE")
	}
	funcCall := p.ParseColumn()
	return p.Expr(exp.TriggerExecute, "this", funcCall)
}

func (p *Parser) parsePropertyBefore() any {
	p.Match(tokens.Comma)
	kw := map[string]any{}
	set := func(k string, v any) {
		if exp.Truthy(v) {
			kw[k] = v
		}
	}
	set("no", p.MatchTextSeq("NO"))
	set("dual", p.MatchTextSeq("DUAL"))
	set("before", p.MatchTextSeq("BEFORE"))
	def := p.MatchTextSeq("DEFAULT")
	set("default", def)
	var local any = false
	if p.MatchTextSeq("LOCAL") {
		local = "LOCAL"
	} else if p.MatchTextSeq("NOT", "LOCAL") {
		local = "NOT LOCAL"
	}
	set("local", local)
	set("after", p.MatchTextSeq("AFTER"))
	set("minimum", p.MatchTexts("MIN", "MINIMUM"))
	set("maximum", p.MatchTexts("MAX", "MAXIMUM"))

	if MatchTextsKey(p, p.C.PropertyParsers) {
		parser := p.C.PropertyParsers[p.PrevTextUpper()]
		if len(kw) > 0 && !parser.AcceptsKwargs {
			p.RaiseError(fmt.Sprintf("Cannot parse property '%s'", p.Prev.Text))
			return nil
		}
		return parser.Fn(p, kw)
	}
	if p.MatchPair(tokens.Char, tokens.Set) {
		return nilIfNil(p.ParseCharacterSet(def))
	}
	return nil
}

func (p *Parser) parseWrappedProperties() []any {
	return ParseWrapped(p, func() []any { return CSVAny(p, p.ParseProperty, tokens.Comma) }, false)
}

// CSVAny is _parse_csv for parse methods returning arbitrary results (nil results skipped).
func CSVAny(p *Parser, parseMethod func() any, sep tokens.TokenType) []any {
	var items []any
	r := parseMethod()
	if r != nil {
		items = append(items, r)
	}
	for p.Match(sep) {
		if e, ok := r.(*exp.Expr); ok {
			p.AddComments(e)
		}
		r = parseMethod()
		if r != nil {
			items = append(items, r)
		}
	}
	return items
}

func (p *Parser) parseProperty() any {
	if MatchTextsKey(p, p.C.PropertyParsers) {
		return p.C.PropertyParsers[p.PrevTextUpper()].Fn(p, nil)
	}
	if p.MatchPair(tokens.Char, tokens.Set) {
		return nilIfNil(p.ParseCharacterSet(false))
	}
	if p.Match(tokens.Default) {
		if MatchTextsKey(p, p.C.PropertyParsers) {
			parser := p.C.PropertyParsers[p.PrevTextUpper()]
			if !parser.AcceptsKwargs {
				p.RaiseError(fmt.Sprintf("Cannot parse property '%s'", p.Prev.Text))
				return nil
			}
			return parser.Fn(p, map[string]any{"default": true})
		}
		if p.MatchPair(tokens.Char, tokens.Set) {
			return nilIfNil(p.ParseCharacterSet(true))
		}
	}
	if p.MatchTextSeq("COMPOUND", "SORTKEY") {
		return nilIfNil(p.ParseSortkey(true))
	}
	if p.MatchTextSeq("PARAMETER", "STYLE", "PANDAS") {
		return p.Expr(exp.ParameterStyleProperty, "this", "PANDAS")
	}
	if p.MatchTextSeq("NOT", "DETERMINISTIC") {
		return p.Expr(exp.StabilityProperty, "this", exp.LiteralString("VOLATILE"))
	}
	index := p.Index
	if seqProps := p.ParseSequenceProperties(); seqProps != nil {
		return seqProps
	}
	p.Retreat(index)
	return nilIfNil(p.ParseKeyValueProperty(nil))
}

func (p *Parser) parseKeyValueProperty(parseValue func() *exp.Expr) *exp.Expr {
	index := p.Index
	key := p.ParseColumn()
	if !p.Match(tokens.Eq) {
		p.Retreat(index)
		return nil
	}
	if key.Is(exp.Column) {
		if len(key.Parts()) > 1 {
			key = key.ToDot(true)
		} else {
			key = exp.NewVar(key.Name())
		}
	}
	var value *exp.Expr
	if parseValue != nil {
		value = parseValue()
	} else {
		value = p.ParseBitwise()
		if value == nil {
			value = p.ParseVar(true, nil, false)
		}
	}
	if value.Is(exp.Column) {
		value = exp.NewVar(value.Name())
	}
	return p.Expr(exp.Property, "this", key, "value", value)
}

func (p *Parser) parseStored() *exp.Expr {
	if p.MatchTextSeq("BY") {
		return p.Expr(exp.StorageHandlerProperty, "this", p.ParseVarOrString(false))
	}
	p.Match(tokens.Alias)
	var inputFormat, outputFormat *exp.Expr
	if p.MatchTextSeq("INPUTFORMAT") {
		inputFormat = p.ParseString()
	}
	if p.MatchTextSeq("OUTPUTFORMAT") {
		outputFormat = p.ParseString()
	}
	var this *exp.Expr
	if inputFormat != nil || outputFormat != nil {
		this = p.Expr(exp.InputOutputFormat, "input_format", inputFormat, "output_format", outputFormat)
	} else {
		this = p.ParseVarOrString(false)
		if this == nil {
			this = p.ParseNumber()
		}
		if this == nil {
			this = p.ParseIdVar(true, nil)
		}
	}
	return p.Expr(exp.FileFormatProperty, "this", this, "hive_format", true)
}

func (p *Parser) parseUnquotedField() *exp.Expr {
	field := p.ParseField(false, nil, false)
	if field.Is(exp.Identifier) && !field.Quoted() {
		field = exp.NewVar(field)
	}
	return field
}

func (p *Parser) parsePropertyAssignment(kind exp.Kind, kv ...any) *exp.Expr {
	p.Match(tokens.Eq)
	p.Match(tokens.Alias)
	args := append([]any{"this", p.ParseUnquotedField()}, kv...)
	return p.Expr(kind, args...)
}

func (p *Parser) parseProperties(before bool) *exp.Expr {
	var properties []*exp.Expr
	for {
		var prop any
		if before {
			prop = p.ParsePropertyBefore()
		} else {
			prop = p.ParseProperty()
		}
		list := ExprList(prop)
		if !exp.Truthy(prop) || prop == nil {
			break
		}
		if l, ok := prop.([]*exp.Expr); ok && len(l) == 0 {
			break
		}
		properties = append(properties, list...)
		var last *exp.Expr
		if len(properties) > 0 {
			last = properties[len(properties)-1]
		}
		p.MatchExpr(tokens.Comma, last)
	}
	if len(properties) > 0 {
		return p.Expr(exp.Properties, "expressions", properties)
	}
	return nil
}

func (p *Parser) parseFallback(no bool) *exp.Expr {
	return p.Expr(exp.FallbackProperty, "no", no, "protection", p.MatchTextSeq("PROTECTION"))
}

func (p *Parser) parseSqlSecurity() *exp.Expr {
	var this any = false
	if p.MatchTextsIn(p.C.SecurityPropertyKeywords) {
		this = p.PrevTextUpper()
	}
	return p.Expr(exp.SqlSecurityProperty, "this", this)
}

func (p *Parser) parseSettingsProperty() *exp.Expr {
	return p.Expr(exp.SettingsProperty, "expressions", p.ParseCSV(p.ParseAssignment))
}

func (p *Parser) parseCalledOnNullInputProperty() *exp.Expr {
	if !p.MatchTextSeq("ON", "NULL", "INPUT") {
		p.Retreat(p.Index - 1)
		return nil
	}
	return p.Expr(exp.CalledOnNullInputProperty)
}

func (p *Parser) parseVolatileProperty() *exp.Expr {
	var pre *tokens.Token
	if p.Index >= 2 {
		pre = p.Tokens[p.Index-2]
	}
	if pre != nil && p.C.PreVolatileTokens.Has(pre.Type) {
		return exp.New(exp.VolatileProperty)
	}
	return p.Expr(exp.StabilityProperty, "this", exp.LiteralString("VOLATILE"))
}

func (p *Parser) parseRetentionPeriod() *exp.Expr {
	number := p.ParseNumber()
	numberStr := ""
	if number != nil {
		numberStr = number.SQL() + " "
	}
	unit := p.ParseVar(true, nil, false)
	unitStr := "None"
	if unit != nil {
		unitStr = unit.SQL()
	}
	return exp.NewVar(numberStr + unitStr)
}

func (p *Parser) parseSystemVersioningProperty(with bool) *exp.Expr {
	p.Match(tokens.Eq)
	prop := p.Expr(exp.WithSystemVersioningProperty, "on", true, "with_", with)
	if p.MatchTextSeq("OFF") {
		prop.Set("on", false)
		return prop
	}
	p.Match(tokens.On)
	if p.Match(tokens.LParen) {
		for p.Curr.Ok() && !p.Match(tokens.RParen) {
			if p.MatchTextSeq("HISTORY_TABLE", "=") {
				prop.Set("this", p.ParseTableParts(false, false, false, false))
			} else if p.MatchTextSeq("DATA_CONSISTENCY_CHECK", "=") {
				if p.AdvanceAny(false) != nil {
					prop.Set("data_consistency", p.PrevTextUpper())
				} else {
					prop.Set("data_consistency", nil)
				}
			} else if p.MatchTextSeq("HISTORY_RETENTION_PERIOD", "=") {
				prop.Set("retention_period", p.ParseRetentionPeriod())
			}
			p.Match(tokens.Comma)
		}
	}
	return prop
}

func (p *Parser) parseDataDeletionProperty() *exp.Expr {
	p.Match(tokens.Eq)
	on := p.MatchTextSeq("ON") || !p.MatchTextSeq("OFF")
	prop := p.Expr(exp.DataDeletionProperty, "on", on)
	if p.Match(tokens.LParen) {
		for p.Curr.Ok() && !p.Match(tokens.RParen) {
			if p.MatchTextSeq("FILTER_COLUMN", "=") {
				prop.Set("filter_column", p.ParseColumn())
			} else if p.MatchTextSeq("RETENTION_PERIOD", "=") {
				prop.Set("retention_period", p.ParseRetentionPeriod())
			}
			p.Match(tokens.Comma)
		}
	}
	return prop
}

func (p *Parser) parseDistributedProperty() *exp.Expr {
	kind := "HASH"
	var expressions any
	if p.MatchTextSeq("BY", "HASH") {
		expressions = p.ParseWrappedCSV(func() *exp.Expr { return p.ParseIdVar(true, nil) }, false)
	} else if p.MatchTextSeq("BY", "RANDOM") {
		kind = "RANDOM"
	}
	var buckets *exp.Expr
	if p.MatchTextSeq("BUCKETS") && !p.MatchTextSeq("AUTO") {
		buckets = p.ParseNumber()
	}
	return p.Expr(exp.DistributedByProperty, "expressions", expressions, "kind", kind, "buckets", buckets, "order", p.ParseOrder(nil, false))
}

func (p *Parser) parseCompositeKeyProperty(kind exp.Kind) *exp.Expr {
	p.MatchTextSeq("KEY")
	expressions := p.ParseWrappedIdVars(false)
	return p.Expr(kind, "expressions", expressions)
}

func (p *Parser) parseWithProperty() any {
	if p.MatchTextSeq("(", "SYSTEM_VERSIONING") {
		prop := p.ParseSystemVersioningProperty(true)
		p.MatchRParen(nil)
		return prop
	}
	if p.Check(tokens.LParen) {
		result := []*exp.Expr{}
		for _, i := range p.ParseWrappedProperties() {
			result = append(result, ExprList(i)...)
		}
		return result
	}
	if p.MatchTextSeq("JOURNAL") {
		return p.ParseWithjournaltable()
	}
	if p.MatchTextsIn(p.C.ViewAttributes) {
		return p.Expr(exp.ViewAttributeProperty, "this", p.PrevTextUpper())
	}
	if p.MatchTextSeq("DATA") {
		return p.ParseWithdata(false)
	} else if p.MatchTextSeq("NO", "DATA") {
		return p.ParseWithdata(true)
	}
	if p.Check(tokens.SerdeProperties) {
		return p.ParseSerdeProperties(true)
	}
	if p.Match(tokens.Schema) {
		return p.Expr(exp.WithSchemaBindingProperty, "this", p.ParseVarFromOptions(p.C.SchemaBindingOptions, true))
	}
	if CheckTextsKey(p, p.C.ProcedureOptions) {
		return p.Expr(exp.WithProcedureOptions, "expressions", p.ParseCSV(p.ParseProcedureOption))
	}
	if !p.Next.Ok() {
		return nil
	}
	return nilIfNil(p.ParseWithisolatedloading())
}

func (p *Parser) parseProcedureOption() *exp.Expr {
	if p.MatchTextSeq("EXECUTE", "AS") {
		this := p.ParseVarFromOptions(p.C.ExecuteAsOptions, false)
		if this == nil {
			this = p.ParseString()
		}
		return p.Expr(exp.ExecuteAsProperty, "this", this)
	}
	return p.ParseVarFromOptions(p.C.ProcedureOptions, true)
}

func (p *Parser) parseDefiner() *exp.Expr {
	p.Match(tokens.Eq)
	user := p.ParseIdVar(true, nil)
	p.Match(tokens.Parameter)
	host := ""
	if h := p.ParseIdVar(true, nil); h != nil {
		host = h.SQL()
	} else if p.Match(tokens.Mod) {
		host = p.Prev.Text
	}
	if user == nil || host == "" {
		return nil
	}
	return exp.New(exp.DefinerProperty, "this", fmt.Sprintf("%s@%s", user.SQL(), host))
}

func (p *Parser) parseWithjournaltable() *exp.Expr {
	p.Match(tokens.Table)
	p.Match(tokens.Eq)
	return p.Expr(exp.WithJournalTableProperty, "this", p.ParseTableParts(false, false, false, false))
}

func (p *Parser) parseLog(no bool) *exp.Expr { return p.Expr(exp.LogProperty, "no", no) }

func (p *Parser) parseJournal(kv ...any) *exp.Expr { return p.Expr(exp.JournalProperty, kv...) }

func (p *Parser) parseChecksum() *exp.Expr {
	p.Match(tokens.Eq)
	var on any
	if p.Match(tokens.On) {
		on = true
	} else if p.MatchTextSeq("OFF") {
		on = false
	}
	return p.Expr(exp.ChecksumProperty, "on", on, "default", p.Match(tokens.Default))
}

func (p *Parser) parseCluster() *exp.Expr {
	p.Match(tokens.ClusterBy)
	return p.Expr(exp.Cluster, "expressions", p.ParseCSV(p.ParseColumn))
}

func (p *Parser) parseClusterProperty() *exp.Expr {
	return p.Expr(exp.ClusterProperty, "expressions", p.ParseWrappedCSV(p.ParseColumn, false))
}

func (p *Parser) parseClusteredBy() *exp.Expr {
	p.MatchTextSeq("BY")
	p.MatchLParen(nil)
	expressions := p.ParseCSV(p.ParseColumn)
	p.MatchRParen(nil)
	var sortedBy any
	if p.MatchTextSeq("SORTED", "BY") {
		p.MatchLParen(nil)
		sortedBy = p.ParseCSV(func() *exp.Expr { return p.ParseOrdered(nil) })
		p.MatchRParen(nil)
	}
	p.Match(tokens.Into)
	buckets := p.ParseNumber()
	p.MatchTextSeq("BUCKETS")
	return p.Expr(exp.ClusteredByProperty, "expressions", expressions, "sorted_by", sortedBy, "buckets", buckets)
}

func (p *Parser) parseCopyProperty() *exp.Expr {
	if !p.MatchTextSeq("GRANTS") {
		p.Retreat(p.Index - 1)
		return nil
	}
	return p.Expr(exp.CopyGrantsProperty)
}

func (p *Parser) parseFreespace() *exp.Expr {
	p.Match(tokens.Eq)
	return p.Expr(exp.FreespaceProperty, "this", p.ParseNumber(), "percent", p.Match(tokens.Percent))
}

func (p *Parser) parseMergeblockratio(no, def bool) *exp.Expr {
	if p.Match(tokens.Eq) {
		return p.Expr(exp.MergeBlockRatioProperty, "this", p.ParseNumber(), "percent", p.Match(tokens.Percent))
	}
	return p.Expr(exp.MergeBlockRatioProperty, "no", no, "default", def)
}

func (p *Parser) parseDatablocksize(def, minimum, maximum any) *exp.Expr {
	p.Match(tokens.Eq)
	size := p.ParseNumber()
	var units any
	if p.MatchTexts("BYTES", "KBYTES", "KILOBYTES") {
		units = p.Prev.Text
	}
	return p.Expr(exp.DataBlocksizeProperty, "size", size, "units", units, "default", def, "minimum", minimum, "maximum", maximum)
}

func (p *Parser) parseBlockcompression() *exp.Expr {
	p.Match(tokens.Eq)
	always := p.MatchTextSeq("ALWAYS")
	manual := p.MatchTextSeq("MANUAL")
	never := p.MatchTextSeq("NEVER")
	def := p.MatchTextSeq("DEFAULT")
	var autotemp *exp.Expr
	if p.MatchTextSeq("AUTOTEMP") {
		autotemp = p.ParseSchema(nil)
	}
	return p.Expr(exp.BlockCompressionProperty, "always", always, "manual", manual, "never", never, "default", def, "autotemp", autotemp)
}

func (p *Parser) parseWithisolatedloading() *exp.Expr {
	index := p.Index
	no := p.MatchTextSeq("NO")
	concurrent := p.MatchTextSeq("CONCURRENT")
	if !p.MatchTextSeq("ISOLATED", "LOADING") {
		p.Retreat(index)
		return nil
	}
	target := p.ParseVarFromOptions(p.C.IsolatedLoadingOptions, false)
	return p.Expr(exp.IsolatedLoadingProperty, "no", no, "concurrent", concurrent, "target", target)
}
