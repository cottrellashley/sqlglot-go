package sqlglot

import (
	"fmt"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

// SelectOpts are the optional arguments of ParseSelect (zero values are the defaults).
type SelectOpts struct {
	Nested          bool
	Table           bool
	NoSubqueryAlias bool // parse_subquery_alias=False
	NoSetOperation  bool // parse_set_operation=False
	NoConsumePipe   bool // consume_pipe=False
}

func (p *Parser) parseRowFormat(matchRow bool) *exp.Expr {
	if matchRow && !p.MatchPair(tokens.Row, tokens.Format) {
		return nil
	}
	if p.MatchTextSeq("SERDE") {
		this := p.ParseString()
		serdeProperties := p.ParseSerdeProperties(false)
		return p.Expr(exp.RowFormatSerdeProperty, "this", this, "serde_properties", serdeProperties)
	}
	p.MatchTextSeq("DELIMITED")
	var kv []any
	if p.MatchTextSeq("FIELDS", "TERMINATED", "BY") {
		kv = append(kv, "fields", p.ParseString())
		if p.MatchTextSeq("ESCAPED", "BY") {
			kv = append(kv, "escaped", p.ParseString())
		}
	}
	if p.MatchTextSeq("COLLECTION", "ITEMS", "TERMINATED", "BY") {
		kv = append(kv, "collection_items", p.ParseString())
	}
	if p.MatchTextSeq("MAP", "KEYS", "TERMINATED", "BY") {
		kv = append(kv, "map_keys", p.ParseString())
	}
	if p.MatchTextSeq("LINES", "TERMINATED", "BY") {
		kv = append(kv, "lines", p.ParseString())
	}
	if p.MatchTextSeq("NULL", "DEFINED", "AS") {
		kv = append(kv, "null", p.ParseString())
	}
	return p.Expr(exp.RowFormatDelimitedProperty, kv...)
}

func (p *Parser) parseLoad() *exp.Expr {
	if p.MatchTextSeq("DATA") {
		local := p.MatchTextSeq("LOCAL")
		p.MatchTextSeq("INPATH")
		inpath := p.ParseString()
		overwrite := p.Match(tokens.Overwrite)
		var temp any
		if p.Match(tokens.Into) {
			temp = p.Match(tokens.Temporary)
			p.Match(tokens.Table)
		}
		this := p.ParseTable(TableOpts{Schema: true})
		files := AndThen(p.MatchTextSeq("FROM", "FILES"), func() *exp.Expr {
			var exprs []*exp.Expr
			for _, x := range p.ParseWrappedProperties() {
				exprs = append(exprs, ExprList(x)...)
			}
			return exp.New(exp.Properties, "expressions", exprs)
		})
		partition := p.ParsePartition()
		inputFormat := AndThen(p.MatchTextSeq("INPUTFORMAT"), p.ParseString)
		serde := AndThen(p.MatchTextSeq("SERDE"), p.ParseString)
		return p.Expr(exp.LoadData, "this", this, "local", local, "overwrite", overwrite, "temp", temp,
			"inpath", inpath, "files", files, "partition", partition, "input_format", inputFormat, "serde", serde)
	}
	return p.ParseAsCommand(p.Prev)
}

func (p *Parser) parseDelete() *exp.Expr {
	hint := p.ParseHint()
	var tables any
	if !p.Check(tokens.From) {
		if t := p.ParseCSV(func() *exp.Expr { return p.ParseTable(TableOpts{}) }); len(t) > 0 {
			tables = t
		}
	}
	returning := p.ParseReturning()
	this := AndThen(p.Match(tokens.From), func() *exp.Expr { return p.ParseTable(TableOpts{Joins: true}) })
	var using any = false
	if p.Match(tokens.Using) {
		using = p.ParseCSV(func() *exp.Expr { return p.ParseTable(TableOpts{Joins: true}) })
	}
	cluster := AndThen(p.Match(tokens.On), p.ParseOnProperty)
	where := p.ParseWhere(false)
	if returning == nil {
		returning = p.ParseReturning()
	}
	return p.Expr(exp.Delete, "hint", hint, "tables", tables, "this", this, "using", using, "cluster", cluster,
		"where", where, "returning", returning, "order", p.ParseOrder(nil, false), "limit", p.ParseLimit(nil, false, false))
}

func (p *Parser) parseUpdateAssignment() *exp.Expr {
	this := p.ParseComparison(nil)
	if p.Match(tokens.Eq) {
		comments := p.PrevComments
		this = p.ExpressionComments(exp.New(exp.EQ, "this", this, "expression", p.ParseDisjunction()), comments)
	}
	return this
}

func (p *Parser) parseUpdate() *exp.Expr {
	hint := p.ParseHint()
	ut := p.C.UpdateAliasTokens
	kv := []any{"hint", hint, "this", p.ParseTable(TableOpts{Joins: true, AliasTokens: &ut})}
	for p.Curr.Ok() {
		if p.Match(tokens.Set) {
			kv = append(kv, "expressions", p.ParseCSV(p.ParseUpdateAssignment))
		} else if p.Check(tokens.Returning) {
			kv = append(kv, "returning", p.ParseReturning())
		} else if p.Check(tokens.From) {
			from := p.ParseFrom(true, false, false)
			var table *exp.Expr
			if from != nil {
				table = from.This()
			}
			if table.Is(exp.Subquery) && p.Check(tokens.Join) {
				joins := p.ParseJoins()
				if len(joins) > 0 {
					table.Set("joins", joins)
				} else {
					table.Set("joins", nil)
				}
			}
			kv = append(kv, "from_", from)
		} else if p.Check(tokens.Where) {
			kv = append(kv, "where", p.ParseWhere(false))
		} else if p.Check(tokens.OrderBy) {
			kv = append(kv, "order", p.ParseOrder(nil, false))
		} else if p.Check(tokens.Limit) {
			kv = append(kv, "limit", p.ParseLimit(nil, false, false))
		} else {
			break
		}
	}
	return p.Expr(exp.Update, kv...)
}

func (p *Parser) parseUse() *exp.Expr {
	kind := p.ParseVarFromOptions(p.C.Usables, false)
	return p.Expr(exp.Use, "kind", kind, "this", p.ParseTable(TableOpts{}))
}

func (p *Parser) parseUncache() *exp.Expr {
	if !p.Match(tokens.Table) {
		p.RaiseError("Expecting TABLE after UNCACHE")
	}
	exists := p.ParseExists(false)
	return p.Expr(exp.Uncache, "exists", exists, "this", p.ParseTable(TableOpts{Schema: true}))
}

func (p *Parser) parseCache() *exp.Expr {
	lazy := p.MatchTextSeq("LAZY")
	p.Match(tokens.Table)
	table := p.ParseTable(TableOpts{Schema: true})
	options := []*exp.Expr{}
	if p.MatchTextSeq("OPTIONS") {
		p.MatchLParen(nil)
		k := p.ParseString()
		p.Match(tokens.Eq)
		v := p.ParseString()
		options = []*exp.Expr{k, v}
		var clean []*exp.Expr
		for _, o := range options {
			if o != nil {
				clean = append(clean, o)
			}
		}
		options = clean
		p.MatchRParen(nil)
	}
	p.Match(tokens.Alias)
	return p.Expr(exp.Cache, "this", table, "lazy", lazy, "options", options, "expression", p.ParseSelect(SelectOpts{Nested: true}))
}

func (p *Parser) parsePartition() *exp.Expr {
	if !p.MatchTextsIn(p.C.PartitionKeywords) {
		return nil
	}
	sub := p.PrevTextUpper() == "SUBPARTITION"
	return p.Expr(exp.Partition, "subpartition", sub, "expressions", p.ParseWrappedCSV(p.ParseDisjunction, false))
}

func (p *Parser) parseValue(values bool) *exp.Expr {
	valueExpression := func() *exp.Expr {
		if p.D.SupportsValuesDefault && p.Match(tokens.Default) {
			return exp.NewVar(p.PrevTextUpper())
		}
		return p.ParseExpression()
	}
	if p.Match(tokens.LParen) {
		expressions := p.ParseCSV(valueExpression)
		p.MatchRParen(nil)
		if expressions == nil {
			expressions = []*exp.Expr{}
		}
		return p.Expr(exp.Tuple, "expressions", expressions)
	}
	expression := p.ParseExpression()
	if expression != nil {
		return p.Expr(exp.Tuple, "expressions", []*exp.Expr{expression})
	}
	return nil
}

func (p *Parser) parseProjections() ([]*exp.Expr, []*exp.Expr) {
	return p.ParseExpressions(), nil
}

func (p *Parser) parseWrappedSelect(table bool) *exp.Expr {
	var this *exp.Expr
	if p.MatchSet(tokens.NewSet(tokens.Pivot, tokens.Unpivot)) {
		this = p.ParseSimplifiedPivot(p.Prev.Type == tokens.Unpivot)
	} else {
		if table && !p.Check(tokens.From) {
			this = p.ParseTable(TableOpts{ConsumePipe: true})
		} else {
			this = p.ParseSelect(SelectOpts{Nested: true, NoSetOperation: true})
		}
		if table && this.Is(exp.Values) && this.Alias() != "" {
			alias := this.Get("alias").Pop()
			this = exp.New(exp.Table, "this", this, "alias", alias)
		}
		this = p.ParseQueryModifiers(p.ParseSetOperations(this))
	}
	return this
}

func (p *Parser) parseSelect(o SelectOpts) *exp.Expr {
	query := p.ParseSelectQuery(o)
	if !o.NoConsumePipe && p.Check(tokens.PipeGt) {
		if query.Is(exp.Query) {
			query = p.ParsePipeSyntaxQuery(query)
			if query != nil && o.Table {
				query = query.Subquery(nil, false)
			}
		}
	}
	return query
}

func (p *Parser) parseSelectQuery(o SelectOpts) *exp.Expr {
	cte := p.ParseWith(false)
	if cte != nil {
		this := p.ParseStatement()
		if this == nil {
			p.RaiseError("Failed to parse any statement following CTE")
			return cte
		}
		for this.Is(exp.Subquery) && this.IsWrapper() {
			this = this.This()
		}
		if this.Kind().HasArg("with_") {
			if innerCTE := this.Get("with_"); innerCTE != nil {
				cte.Set("expressions", append(append([]*exp.Expr(nil), cte.Expressions()...), innerCTE.Expressions()...))
				if innerCTE.Bool("recursive") {
					cte.Set("recursive", true)
				}
			}
			this.Set("with_", cte)
		} else {
			p.RaiseError(fmt.Sprintf("%s does not support CTE", this.Key()))
			this = cte
		}
		return this
	}

	var from *exp.Expr
	if p.Check(tokens.From) {
		from = p.ParseFrom(true, false, true)
	}
	var laterals []*exp.Expr
	if from != nil {
		for l := p.ParseLateral(); l != nil; l = p.ParseLateral() {
			laterals = append(laterals, l)
		}
	}

	if from != nil && p.Check(tokens.Insert) {
		source := from.This()
		if len(laterals) > 0 {
			source.Set("laterals", laterals)
		} else {
			source.Set("laterals", nil)
		}
		var inserts []*exp.Expr
		for p.Check(tokens.Insert) {
			inserts = append(inserts, p.ParseStatement())
		}
		return p.ExpressionComments(exp.New(exp.MultitableInserts, "expressions", inserts, "source", source), from.Comments)
	}

	var this *exp.Expr
	switch {
	case p.Match(tokens.Select):
		comments := p.PrevComments
		hint := p.ParseHint()
		var all any
		matchedDistinct := false
		if p.Next.Ok() && p.Next.Type != tokens.Dot {
			all = p.Match(tokens.All)
			matchedDistinct = p.MatchSet(p.C.DistinctTokens)
		}
		var kind any
		if p.Match(tokens.Alias) && p.MatchTexts("STRUCT", "VALUE") {
			kind = p.PrevTextUpper()
		}
		var distinct *exp.Expr
		parseDistinct := func() *exp.Expr {
			var on *exp.Expr
			if p.Match(tokens.On) {
				on = p.ParseValue(false)
			}
			return p.Expr(exp.Distinct, "on", on)
		}
		if matchedDistinct {
			distinct = parseDistinct()
		}
		var operationModifiers []*exp.Expr
		for p.Curr.Ok() && p.MatchTextsIn(p.C.OperationModifiers) {
			operationModifiers = append(operationModifiers, exp.NewVar(p.PrevTextUpper()))
		}
		limit := p.ParseLimit(nil, true, false)
		if limit != nil && !matchedDistinct && !exp.Truthy(all) {
			matchedDistinct = p.MatchSet(p.C.DistinctTokens)
			if matchedDistinct {
				distinct = parseDistinct()
			} else {
				all = p.Match(tokens.All)
			}
		}
		if exp.Truthy(all) && distinct != nil {
			p.RaiseError("Cannot specify both ALL and DISTINCT after SELECT")
		}
		projections, exclude := p.ParseProjections()
		var opMods any
		if len(operationModifiers) > 0 {
			opMods = operationModifiers
		}
		var excl any
		if exclude != nil {
			excl = exclude
		}
		if projections == nil {
			projections = []*exp.Expr{}
		}
		this = p.Expr(exp.Select, "kind", kind, "hint", hint, "distinct", distinct, "expressions", projections,
			"limit", limit, "exclude", excl, "operation_modifiers", opMods)
		this.Comments = comments
		if into := p.ParseInto(); into != nil {
			this.Set("into", into)
		}
		if from == nil {
			from = p.ParseFrom(false, false, false)
		}
		if from != nil {
			this.Set("from_", from)
		}
		this = p.ParseQueryModifiers(this)
	case (o.Table || o.Nested) && p.Match(tokens.LParen):
		comments := p.PrevComments
		this = p.ParseWrappedSelect(o.Table)
		if this != nil {
			this.AddComments(comments, true)
		}
		p.MatchRParen(nil)
		return p.ParseSubquery(this, !o.NoSubqueryAlias)
	case p.Check(tokens.Values):
		this = p.ParseDerivedTableValues(false)
	case from != nil:
		this = exp.NewSelect(exp.New(exp.Star)).From(from.This(), exp.WithCopy(false))
		this = p.ParseQueryModifiers(this)
	case p.Match(tokens.Summarize):
		table := p.Match(tokens.Table)
		this = p.ParseSelect(SelectOpts{})
		if this == nil {
			this = p.ParseString()
		}
		if this == nil {
			this = p.ParseTable(TableOpts{})
		}
		return p.Expr(exp.Summarize, "this", this, "table", table)
	case p.Match(tokens.Describe):
		this = p.ParseDescribe()
	}

	if len(laterals) > 0 && this.Is(exp.Select) {
		this.Set("laterals", append(append([]*exp.Expr(nil), laterals...), this.List("laterals")...))
	}
	if !o.NoSetOperation {
		return p.ParseSetOperations(this)
	}
	return this
}

func (p *Parser) parseRecursiveWithSearch() *exp.Expr {
	p.MatchTextSeq("SEARCH")
	kind := ""
	if p.MatchTextsIn(p.C.RecursiveCteSearchKind) {
		kind = p.PrevTextUpper()
	}
	if kind == "" {
		return nil
	}
	p.MatchTextSeq("FIRST", "BY")
	this := p.ParseIdVar(true, nil)
	expression := AndThen(p.MatchTextSeq("SET"), func() *exp.Expr { return p.ParseIdVar(true, nil) })
	to := AndThen(p.MatchTextSeq("TO"), p.ParseBitwise)
	def := AndThen(p.Match(tokens.Default), p.ParseBitwise)
	using := AndThen(p.MatchTextSeq("USING"), func() *exp.Expr { return p.ParseIdVar(true, nil) })
	return p.Expr(exp.RecursiveWithSearch, "kind", kind, "this", this, "expression", expression, "to", to, "default", def, "using", using)
}

func (p *Parser) parseWith(skipWithToken bool) *exp.Expr {
	if !skipWithToken && !p.Match(tokens.With) {
		return nil
	}
	comments := p.PrevComments
	recursive := p.Match(tokens.Recursive)
	var lastComments []string
	expressions := []*exp.Expr{}
	var udfs []*exp.Expr
	for {
		cte := p.ParseCte()
		if cte != nil {
			if cte.Is(exp.FunctionSpecification) {
				udfs = append(udfs, cte)
			} else {
				expressions = append(expressions, cte)
			}
			if len(lastComments) > 0 {
				cte.AddComments(lastComments, false)
			}
		}
		if !p.Match(tokens.Comma) && !p.Match(tokens.With) {
			break
		}
		p.Match(tokens.With)
		recursive = p.Match(tokens.Recursive) || recursive
		lastComments = p.PrevComments
	}
	search := p.ParseRecursiveWithSearch()
	var cycle *exp.Expr
	if search != nil && search.Str("kind") != "CYCLE" && p.CheckTextSeq("CYCLE") {
		cycle = p.ParseRecursiveWithSearch()
	}
	var udfsArg any
	if len(udfs) > 0 {
		udfsArg = udfs
	}
	return p.ExpressionComments(exp.New(exp.With, "expressions", expressions, "recursive", BoolOrNil(recursive),
		"search", search, "cycle", cycle, "udfs", udfsArg), comments)
}

func (p *Parser) parseCte() *exp.Expr {
	index := p.Index
	idv := p.C.IdVarTokens
	alias := p.ParseTableAlias(&idv)
	if alias == nil || alias.This() == nil {
		p.RaiseError("Expected CTE to have alias")
	}
	var keyExpressions any
	if p.MatchTextSeq("USING", "KEY") {
		keyExpressions = p.ParseWrappedIdVars(false)
	}
	if !p.Match(tokens.Alias) && !p.C.OptionalAliasTokenCte {
		p.Retreat(index)
		return nil
	}
	comments := p.PrevComments
	var materialized any
	if p.MatchTextSeq("NOT", "MATERIALIZED") {
		materialized = false
	} else if p.MatchTextSeq("MATERIALIZED") {
		materialized = true
	}
	cte := p.ExpressionComments(exp.New(exp.CTE,
		"this", p.ParseWrappedExpr(p.ParseStatement, false),
		"alias", alias,
		"materialized", materialized,
		"key_expressions", keyExpressions,
	), comments)
	if values := cte.This(); values.Is(exp.Values) {
		cte.Set("this", p.ValuesToSelect(values))
	}
	return cte
}

func (p *Parser) valuesToSelect(values *exp.Expr) *exp.Expr {
	sel := exp.NewSelect(exp.New(exp.Star))
	for _, key := range exp.QueryModifiers {
		if modifier := values.Arg(key); exp.Truthy(modifier) {
			values.Set(key, nil)
			sel.Set(key, modifier)
		}
	}
	if values.Alias() != "" {
		return sel.From(values)
	}
	return sel.From(exp.NewTableAliasOf(values, "_values", nil))
}
