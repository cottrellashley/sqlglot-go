package sqlglot

import (
	"fmt"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

// TableOpts are the optional arguments of ParseTable (zero values are the defaults).
type TableOpts struct {
	Schema         bool
	Joins          bool
	AliasTokens    *tokens.TokenSet
	ParseBracket   bool
	IsDbReference  bool
	ParsePartition bool
	ConsumePipe    bool
}

func (p *Parser) parseIndexParams() *exp.Expr {
	var using any
	if !p.CheckTextSeq("USING", "INDEX", "TABLESPACE") && p.Match(tokens.Using) {
		using = nilIfNil(p.ParseVar(true, nil, false))
	}
	var columns any
	if p.Check(tokens.LParen) {
		columns = p.ParseWrappedCSV(p.ParseWithOperator, false)
	}
	var include any
	if p.MatchTextSeq("INCLUDE") {
		include = p.ParseWrappedIdVars(false)
	}
	partitionBy := p.ParsePartitionBy()
	var withStorage any = false
	if p.Match(tokens.With) {
		var props []*exp.Expr
		for _, x := range p.ParseWrappedProperties() {
			props = append(props, ExprList(x)...)
		}
		if props == nil {
			props = []*exp.Expr{}
		}
		withStorage = props
	}
	var tablespace *exp.Expr
	if p.MatchTextSeq("USING", "INDEX", "TABLESPACE") || p.MatchTextSeq("TABLESPACE") {
		tablespace = p.ParseIdVar(true, nil)
	}
	where := p.ParseWhere(false)
	var on *exp.Expr
	if p.Match(tokens.On) {
		on = p.ParseField(false, nil, false)
	}
	return p.Expr(exp.IndexParameters, "using", using, "columns", columns, "include", include,
		"partition_by", partitionBy, "where", where, "with_storage", withStorage, "tablespace", tablespace, "on", on)
}

func (p *Parser) parseIndex(index *exp.Expr, anonymous bool) *exp.Expr {
	var unique, primary, amp any
	var table *exp.Expr
	if index != nil || anonymous {
		p.Match(tokens.On)
		p.Match(tokens.Table)
		table = p.ParseTableParts(true, false, false, false)
	} else {
		unique = p.Match(tokens.Unique)
		primary = p.MatchTextSeq("PRIMARY")
		amp = p.MatchTextSeq("AMP")
		if !p.Match(tokens.Index) {
			return nil
		}
		index = p.ParseIdVar(true, nil)
	}
	params := p.ParseIndexParams()
	return p.Expr(exp.Index, "this", index, "table", table, "unique", unique, "primary", primary, "amp", amp, "params", params)
}

func (p *Parser) parseTableHints() []*exp.Expr {
	var hints []*exp.Expr
	if p.MatchPair(tokens.With, tokens.LParen) {
		hints = append(hints, p.Expr(exp.WithTableHint, "expressions", p.ParseCSV(func() *exp.Expr {
			if f := p.ParseFunction(FunctionOpts{}); f != nil {
				return f
			}
			return p.ParseVar(true, nil, false)
		})))
		p.MatchRParen(nil)
	} else {
		for p.MatchSet(p.C.TableIndexHintTokens) {
			hint := exp.New(exp.IndexTableHint, "this", p.PrevTextUpper())
			p.MatchSet(tokens.NewSet(tokens.Index, tokens.Key))
			if p.Match(tokens.For) {
				if p.AdvanceAny(false) != nil {
					hint.Set("target", p.PrevTextUpper())
				} else {
					hint.Set("target", nil)
				}
			}
			hint.Set("expressions", p.ParseWrappedIdVars(false))
			hints = append(hints, hint)
		}
	}
	if len(hints) == 0 {
		return nil
	}
	return hints
}

func (p *Parser) parseTablePart(schema bool) *exp.Expr {
	if !schema {
		if f := p.ParseFunction(FunctionOpts{NoOptionalParens: true}); f != nil {
			return f
		}
	}
	if e := p.ParseIdVar(false, nil); e != nil {
		return e
	}
	if e := p.ParseStringAsIdentifier(); e != nil {
		return e
	}
	return p.ParsePlaceholder()
}

func (p *Parser) parseTablePartsFast() *exp.Expr {
	index := p.Index
	var parts []*exp.Expr
	var allComments []string
	for p.MatchSet(p.C.IdentifierTokens) {
		token := p.Prev
		comments := p.PrevComments
		hasDot := p.Match(tokens.Dot)
		currTT := p.Curr.Type
		if !hasDot {
			if p.C.TablePostfixTokens.Has(currTT) {
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
	var table *exp.Expr
	switch n := len(parts); {
	case n == 1:
		table = exp.New(exp.Table, "this", parts[0])
	case n == 2:
		table = exp.New(exp.Table, "this", parts[1], "db", parts[0])
	default:
		this := parts[2]
		for i := 3; i < n; i++ {
			this = exp.New(exp.Dot, "this", this, "expression", parts[i])
		}
		table = exp.New(exp.Table, "this", this, "db", parts[1], "catalog", parts[0])
	}
	if len(allComments) > 0 {
		table.AddComments(allComments, false)
	}
	return table
}

func (p *Parser) parseTableParts(schema, isDbReference, wildcard, fast bool) *exp.Expr {
	if fast {
		return p.ParseTablePartsFast()
	}
	var catalog, db, table any
	table = nilIfNil(p.ParseTablePart(schema))
	for p.Match(tokens.Dot) {
		if exp.Truthy(catalog) {
			t, _ := table.(*exp.Expr)
			table = p.Expr(exp.Dot, "this", t, "expression", p.ParseTablePart(schema))
		} else {
			catalog = db
			db = table
			if t := p.ParseTablePart(schema); t != nil {
				table = t
			} else {
				table = ""
			}
		}
	}
	tExpr, _ := table.(*exp.Expr)
	if wildcard && p.IsConnected() && (tExpr.Is(exp.Identifier) || !exp.Truthy(table)) && p.Match(tokens.Star) {
		if tExpr.Is(exp.Identifier) {
			tExpr.SetRaw("this", tExpr.ThisStr()+"*")
		} else {
			table = exp.New(exp.Identifier, "this", "*")
		}
	}
	if isDbReference {
		catalog = db
		db = table
		table = nil
	}
	if !exp.Truthy(table) && !isDbReference {
		p.RaiseError(fmt.Sprintf("Expected table name but got %s", p.Curr))
	}
	if !exp.Truthy(db) && isDbReference {
		p.RaiseError(fmt.Sprintf("Expected database name but got %s", p.Curr))
	}
	t := p.Expr(exp.Table, "this", table, "db", db, "catalog", catalog)
	var comments []string
	for _, part := range t.Parts() {
		if pc := part.PopComments(); len(pc) > 0 {
			comments = append(comments, pc...)
		}
	}
	if len(comments) > 0 {
		t.AddComments(comments, false)
	}
	if changes := p.ParseChanges(); changes != nil {
		t.Set("changes", changes)
	}
	if atBefore := p.ParseHistoricalData(); atBefore != nil {
		t.Set("when", atBefore)
	}
	if pivots := p.ParsePivots(); len(pivots) > 0 {
		t.Set("pivots", pivots)
	}
	return t
}

func (p *Parser) parseTable(o TableOpts) *exp.Expr {
	aliasTokens := func() *tokens.TokenSet {
		if o.AliasTokens != nil {
			return o.AliasTokens
		}
		t := p.C.TableAliasTokens
		return &t
	}
	if !o.Schema && !o.IsDbReference && !o.ConsumePipe && !o.Joins {
		index := p.Index
		table := p.ParseTableParts(false, false, false, true)
		if table != nil {
			currTT, nextTT := p.Curr.Type, p.Next.Type
			fastTerminators := p.C.TableTerminators
			if fastTerminators.Has(currTT) && nextTT != tokens.MatchCondition {
				return table
			}
			postfix := p.C.TablePostfixTokens
			if !postfix.Has(currTT) && !postfix.Has(nextTT) {
				if alias := p.ParseTableAlias(aliasTokens()); alias != nil {
					table.Set("alias", alias)
				}
				if fastTerminators.Has(p.Curr.Type) {
					return table
				}
			}
			p.Retreat(index)
		}
	}
	if stream := p.ParseStream(); stream != nil {
		return stream
	}
	if lateral := p.ParseLateral(); lateral != nil {
		return lateral
	}
	if unnest := p.ParseUnnest(true); unnest != nil {
		return unnest
	}
	if values := p.ParseDerivedTableValues(false); values != nil {
		return values
	}
	if subquery := p.ParseSelect(SelectOpts{Table: true, NoConsumePipe: !o.ConsumePipe}); subquery != nil {
		if !exp.Truthy(subquery.Arg("pivots")) {
			subquery.Set("pivots", p.ParsePivots())
		}
		if o.Joins {
			for _, join := range p.ParseJoins() {
				subquery.Append("joins", join)
			}
		}
		return subquery
	}
	var bracket *exp.Expr
	if o.ParseBracket {
		if b := p.ParseBracket(nil); b != nil {
			bracket = p.Expr(exp.Table, "this", b)
		}
	}
	var rowsFrom *exp.Expr
	if p.MatchTextSeq("ROWS", "FROM") {
		if t := p.ParseWrappedCSV(func() *exp.Expr { return p.ParseTable(TableOpts{}) }, false); len(t) > 0 {
			rowsFrom = p.Expr(exp.Table, "rows_from", t)
		}
	}
	only := p.Match(tokens.Only)
	this := bracket
	if this == nil {
		this = rowsFrom
	}
	if this == nil {
		this = p.ParseBracket(p.ParseTableParts(o.Schema, o.IsDbReference, false, false))
	}
	if only {
		this.Set("only", only)
	}
	p.Match(tokens.Star)
	parsePartition := o.ParsePartition || p.C.SupportsPartitionSelection
	if parsePartition && p.Check(tokens.Partition) {
		this.Set("partition", p.ParsePartition())
	}
	if o.Schema {
		return p.ParseSchema(this)
	}
	if p.D.AliasPostVersion {
		this.Set("version", p.ParseVersion())
	}
	if p.D.AliasPostTablesample {
		this.Set("sample", p.ParseTableSample(false))
		if exp.Truthy(this.Arg("sample")) && p.D.AliasPostVersion && !exp.Truthy(this.Arg("version")) {
			this.Set("version", p.ParseVersion())
		}
	}
	if alias := p.ParseTableAlias(aliasTokens()); alias != nil {
		this.Set("alias", alias)
		if this.Is(exp.Table) && !exp.Truthy(this.Arg("when")) {
			this.Set("when", p.ParseHistoricalData())
		}
	}
	if p.Match(tokens.IndexedBy) {
		this.Set("indexed", p.ParseTableParts(false, false, false, false))
	} else if p.MatchTextSeq("NOT", "INDEXED") {
		this.Set("indexed", false)
	}
	if this.Is(exp.Table) && p.MatchTextSeq("AT") {
		return p.Expr(exp.AtIndex, "this", this.ToColumnExpr(false), "expression", p.ParseIdVar(true, nil))
	}
	if hints := p.ParseTableHints(); hints != nil {
		this.Set("hints", hints)
	} else {
		this.Set("hints", nil)
	}
	if !exp.Truthy(this.Arg("pivots")) {
		this.Set("pivots", p.ParsePivots())
	}
	if !p.D.AliasPostTablesample {
		this.Set("sample", p.ParseTableSample(false))
	}
	if !p.D.AliasPostVersion {
		this.Set("version", p.ParseVersion())
	}
	if o.Joins {
		for _, join := range p.ParseJoinsWith(o.AliasTokens) {
			this.Append("joins", join)
		}
	}
	if p.MatchPair(tokens.With, tokens.Ordinality) {
		this.Set("ordinality", true)
		this.Set("alias", p.ParseTableAlias(nil))
	}
	if this.Is(exp.Table) && this.This().Is(exp.TableFromRows) {
		tableFromRows := this.This()
		for _, a := range exp.TableFromRows.Args() {
			if a.Key != "this" {
				tableFromRows.Set(a.Key, this.Arg(a.Key))
			}
		}
		this = tableFromRows
	}
	return this
}

// versionPhrases mirrors VERSION_PHRASES iteration order.
var versionPhraseOrder = []string{"FOR SYSTEM_TIME", "FOR SYSTEM TIME", "FOR TIMESTAMP", "FOR VERSION", "TIMESTAMP AS OF", "VERSION AS OF"}

func (p *Parser) parseVersion() *exp.Expr {
	var this string
	found := false
	for _, phrase := range p.versionPhrases() {
		if p.MatchTextSeq(splitWords(phrase)...) {
			this = p.C.VersionPhrases[phrase]
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	var kind string
	var expression *exp.Expr
	if p.MatchSet(tokens.NewSet(tokens.From, tokens.Between)) {
		kind = p.PrevTextUpper()
		start := p.ParseBitwise()
		p.MatchTexts("TO", "AND")
		end := p.ParseBitwise()
		expression = p.Expr(exp.Tuple, "expressions", []*exp.Expr{start, end})
	} else if p.MatchTextSeq("CONTAINED", "IN") {
		kind = "CONTAINED IN"
		expression = p.Expr(exp.Tuple, "expressions", p.ParseWrappedCSV(p.ParseBitwise, false))
	} else if p.Match(tokens.All) {
		kind = "ALL"
	} else {
		p.MatchTextSeq("AS", "OF")
		kind = "AS OF"
		expression = p.ParseBitwise()
	}
	return p.Expr(exp.Version, "this", this, "expression", expression, "kind", kind)
}

func (p *Parser) versionPhrases() []string {
	if order, ok := p.C.Extra["VERSION_PHRASES_ORDER"].([]string); ok {
		return order
	}
	var out []string
	for _, ph := range versionPhraseOrder {
		if _, ok := p.C.VersionPhrases[ph]; ok {
			out = append(out, ph)
		}
	}
	for ph := range p.C.VersionPhrases {
		if !contains(out, ph) {
			out = append(out, ph)
		}
	}
	return out
}

func splitWords(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func (p *Parser) parseHistoricalData() *exp.Expr {
	index := p.Index
	var historical *exp.Expr
	if p.MatchTextsIn(p.C.HistoricalDataPrefix) {
		this := p.PrevTextUpper()
		var kind any = false
		if p.Match(tokens.LParen) && p.MatchTextsIn(p.C.HistoricalDataKind) {
			kind = p.PrevTextUpper()
		}
		var expression *exp.Expr
		if p.Match(tokens.Farrow) {
			expression = p.ParseBitwise()
		}
		if expression != nil {
			p.MatchRParen(nil)
			historical = p.Expr(exp.HistoricalData, "this", this, "kind", kind, "expression", expression)
		} else {
			p.Retreat(index)
		}
	}
	return historical
}

func (p *Parser) parseChanges() *exp.Expr {
	if !p.MatchTextSeq("CHANGES", "(", "INFORMATION", "=>") {
		return nil
	}
	information := p.ParseVar(true, nil, false)
	p.MatchRParen(nil)
	return p.Expr(exp.Changes, "information", information, "at_before", p.ParseHistoricalData(), "end", p.ParseHistoricalData())
}

func (p *Parser) parseUnnest(withAlias bool) *exp.Expr {
	if !p.CheckPair(tokens.Unnest, tokens.LParen) {
		return nil
	}
	p.Advance()
	expressions := p.ParseWrappedCSV(func() *exp.Expr { return p.ParseEquality(true) }, false)
	var offset any = p.MatchPair(tokens.With, tokens.Ordinality)
	var alias *exp.Expr
	if withAlias {
		alias = p.ParseTableAlias(nil)
	}
	if alias != nil {
		if p.D.UnnestColumnOnly {
			if exp.Truthy(alias.Arg("columns")) {
				p.RaiseError("Unexpected extra column alias in unnest.")
			}
			alias.Set("columns", []*exp.Expr{alias.This()})
			alias.Set("this", nil)
		}
		columns := alias.List("columns")
		if exp.Truthy(offset) && len(expressions) < len(columns) {
			last := columns[len(columns)-1]
			alias.Set("columns", columns[:len(columns)-1])
			offset = last
		}
	}
	if !exp.Truthy(offset) && p.MatchPair(tokens.With, tokens.Offset) {
		p.Match(tokens.Alias)
		ts := p.C.UnnestOffsetAliasTokens
		off := p.ParseIdVar(false, &ts)
		if off == nil {
			off = exp.ToIdentifier("offset")
		}
		offset = off
	}
	if expressions == nil {
		expressions = []*exp.Expr{}
	}
	return p.Expr(exp.Unnest, "expressions", expressions, "alias", alias, "offset", offset)
}
