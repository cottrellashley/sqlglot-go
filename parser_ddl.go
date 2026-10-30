package sqlglot

import (
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func (p *Parser) parseLocking() *exp.Expr {
	var kind any
	switch {
	case p.Match(tokens.Table):
		kind = "TABLE"
	case p.Match(tokens.View):
		kind = "VIEW"
	case p.Match(tokens.Row):
		kind = "ROW"
	case p.MatchTextSeq("DATABASE"):
		kind = "DATABASE"
	}
	var this *exp.Expr
	if kind == "DATABASE" || kind == "TABLE" || kind == "VIEW" {
		this = p.ParseTableParts(false, false, false, false)
	}
	var forOrIn any
	if p.Match(tokens.For) {
		forOrIn = "FOR"
	} else if p.Match(tokens.In) {
		forOrIn = "IN"
	}
	var lockType any
	switch {
	case p.MatchTextSeq("ACCESS"):
		lockType = "ACCESS"
	case p.MatchTexts("EXCL", "EXCLUSIVE"):
		lockType = "EXCLUSIVE"
	case p.MatchTextSeq("SHARE"):
		lockType = "SHARE"
	case p.MatchTextSeq("READ"):
		lockType = "READ"
	case p.MatchTextSeq("WRITE"):
		lockType = "WRITE"
	case p.MatchTextSeq("CHECKSUM"):
		lockType = "CHECKSUM"
	}
	override := p.MatchTextSeq("OVERRIDE")
	return p.Expr(exp.LockingProperty, "this", this, "kind", kind, "for_or_in", forOrIn, "lock_type", lockType, "override", override)
}

func (p *Parser) parsePartitionBy() []*exp.Expr {
	if !p.Match(tokens.PartitionBy) {
		return []*exp.Expr{}
	}
	if p.C.IdVarTokens.Has(p.Next.Type) && p.MatchTextSeq("EXCLUDING") {
		return []*exp.Expr{p.Expr(exp.PartitionByExcluding, "expressions", p.ParseCSV(p.ParseDisjunction))}
	}
	return p.ParseCSV(p.ParseDisjunction)
}

func (p *Parser) parsePartitionBoundSpec() *exp.Expr {
	boundExpr := func() *exp.Expr {
		if p.MatchTextSeq("MINVALUE") {
			return exp.NewVar("MINVALUE")
		}
		if p.MatchTextSeq("MAXVALUE") {
			return exp.NewVar("MAXVALUE")
		}
		return p.ParseBitwise()
	}
	var this, expression, fromExpressions, toExpressions any
	if p.Match(tokens.In) {
		this = p.ParseWrappedCSV(p.ParseBitwise, false)
	} else if p.Match(tokens.From) {
		fromExpressions = p.ParseWrappedCSV(boundExpr, false)
		p.MatchTextSeq("TO")
		toExpressions = p.ParseWrappedCSV(boundExpr, false)
	} else if p.MatchTextSeq("WITH", "(", "MODULUS") {
		this = nilIfNil(p.ParseNumber())
		p.MatchTextSeq(",", "REMAINDER")
		expression = nilIfNil(p.ParseNumber())
		p.MatchRParen(nil)
	} else {
		p.RaiseError("Failed to parse partition bound spec.")
	}
	return p.Expr(exp.PartitionBoundSpec, "this", this, "expression", expression, "from_expressions", fromExpressions, "to_expressions", toExpressions)
}

func (p *Parser) parsePartitionedOf() *exp.Expr {
	if !p.MatchTextSeq("OF") {
		p.Retreat(p.Index - 1)
		return nil
	}
	this := p.ParseTable(TableOpts{Schema: true})
	var expression *exp.Expr
	if p.Match(tokens.Default) {
		expression = exp.NewVar("DEFAULT")
	} else if p.MatchTextSeq("FOR", "VALUES") {
		expression = p.ParsePartitionBoundSpec()
	} else {
		p.RaiseError("Expecting either DEFAULT or FOR VALUES clause.")
	}
	return p.Expr(exp.PartitionedOfProperty, "this", this, "expression", expression)
}

func (p *Parser) parsePartitionedBy() *exp.Expr {
	p.Match(tokens.Eq)
	this := p.ParseSchema(nil)
	if this == nil {
		this = p.ParseBracket(p.ParseField(false, nil, false))
	}
	return p.Expr(exp.PartitionedByProperty, "this", this)
}

func (p *Parser) parseWithdata(no bool) *exp.Expr {
	var statistics any
	if p.MatchTextSeq("AND", "STATISTICS") {
		statistics = true
	} else if p.MatchTextSeq("AND", "NO", "STATISTICS") {
		statistics = false
	}
	return p.Expr(exp.WithDataProperty, "no", no, "statistics", statistics)
}

func (p *Parser) parseContainsProperty() *exp.Expr {
	if p.MatchTextSeq("SQL") {
		return p.Expr(exp.SqlReadWriteProperty, "this", "CONTAINS SQL")
	}
	return nil
}

func (p *Parser) parseModifiesProperty() *exp.Expr {
	if p.MatchTextSeq("SQL", "DATA") {
		return p.Expr(exp.SqlReadWriteProperty, "this", "MODIFIES SQL DATA")
	}
	return nil
}

func (p *Parser) parseNoProperty() *exp.Expr {
	if p.MatchTextSeq("PRIMARY", "INDEX") {
		return exp.New(exp.NoPrimaryIndexProperty)
	}
	if p.MatchTextSeq("SQL") {
		return p.Expr(exp.SqlReadWriteProperty, "this", "NO SQL")
	}
	return nil
}

func (p *Parser) parseOnProperty() *exp.Expr {
	if p.MatchTextSeq("COMMIT", "PRESERVE", "ROWS") {
		return exp.New(exp.OnCommitProperty)
	}
	if p.MatchTextSeq("COMMIT", "DELETE", "ROWS") {
		return exp.New(exp.OnCommitProperty, "delete", true)
	}
	return p.Expr(exp.OnProperty, "this", p.ParseSchema(p.ParseIdVar(true, nil)))
}

func (p *Parser) parseReadsProperty() *exp.Expr {
	if p.MatchTextSeq("SQL", "DATA") {
		return p.Expr(exp.SqlReadWriteProperty, "this", "READS SQL DATA")
	}
	return nil
}

func (p *Parser) parseDistkey() *exp.Expr {
	return p.Expr(exp.DistKeyProperty, "this", p.ParseWrappedExpr(func() *exp.Expr { return p.ParseIdVar(true, nil) }, false))
}

func (p *Parser) parseCreateLike() *exp.Expr {
	table := p.ParseTable(TableOpts{Schema: true})
	var options []*exp.Expr
	for p.MatchTexts("INCLUDING", "EXCLUDING") {
		this := p.PrevTextUpper()
		idVar := p.ParseIdVar(true, nil)
		if idVar == nil {
			return nil
		}
		options = append(options, p.Expr(exp.Property, "this", this, "value", exp.NewVar(strings.ToUpper(idVar.ThisStr()))))
	}
	if options == nil {
		options = []*exp.Expr{}
	}
	return p.Expr(exp.LikeProperty, "this", table, "expressions", options)
}

func (p *Parser) parseSortkey(compound bool) *exp.Expr {
	return p.Expr(exp.SortKeyProperty, "this", p.ParseWrappedIdVars(false), "compound", compound)
}

func (p *Parser) parseCharacterSet(def bool) *exp.Expr {
	p.Match(tokens.Eq)
	return p.Expr(exp.CharacterSetProperty, "this", p.ParseVarOrString(false), "default", def)
}

func (p *Parser) parseRemoteWithConnection() *exp.Expr {
	p.MatchTextSeq("WITH", "CONNECTION")
	return p.Expr(exp.RemoteWithConnectionModelProperty, "this", p.ParseTableParts(false, false, false, false))
}

func (p *Parser) parseReturns() *exp.Expr {
	var value *exp.Expr
	var null any
	isTable := p.Match(tokens.Table)
	if isTable {
		if p.Match(tokens.Lt) {
			value = p.Expr(exp.Schema, "this", "TABLE", "expressions", p.ParseCSV(func() *exp.Expr { return p.ParseStructTypes(false) }))
			if !p.Match(tokens.Gt) {
				p.RaiseError("Expecting >")
			}
		} else {
			value = p.ParseSchema(exp.NewVar("TABLE"))
		}
	} else if p.MatchTextSeq("NULL", "ON", "NULL", "INPUT") {
		null = true
	} else {
		value = p.ParseTypes(false, false, true, false)
	}
	return p.Expr(exp.ReturnsProperty, "this", value, "is_table", isTable, "null", null)
}

func (p *Parser) parseDescribe() *exp.Expr {
	var kind any
	if p.MatchSet(p.C.Creatables) {
		kind = p.Prev.Text
	}
	var style any
	if p.MatchTextsIn(p.C.DescribeStyles) {
		style = p.PrevTextUpper()
	}
	if p.Match(tokens.Dot) {
		style = nil
		p.Retreat(p.Index - 2)
	}
	var format any
	if p.Check(tokens.Format) {
		format = p.ParseProperty()
	}
	var this *exp.Expr
	if CheckIn(p, p.C.StatementParsers) {
		this = p.ParseStatement()
	} else {
		this = p.ParseTable(TableOpts{Schema: true})
	}
	properties := p.ParseProperties(false)
	var expressions any
	if properties != nil {
		expressions = properties.Expressions()
	}
	partition := p.ParsePartition()
	return p.Expr(exp.Describe, "this", this, "style", style, "kind", kind, "expressions", expressions,
		"partition", partition, "format", format, "as_json", p.MatchTextSeq("AS", "JSON"))
}

func (p *Parser) parseMultitableInserts(comments []string, kind string) *exp.Expr {
	if kind == "" {
		kind = p.PrevTextUpper()
	}
	var expressions []*exp.Expr
	parseConditionalInsert := func() *exp.Expr {
		var expression *exp.Expr
		if p.Match(tokens.When) {
			expression = p.ParseDisjunction()
			p.Match(tokens.Then)
		}
		elseFlag := p.Match(tokens.Else)
		if !p.Match(tokens.Into) {
			return nil
		}
		insert := p.Expr(exp.Insert, "this", p.ParseTable(TableOpts{Schema: true}), "expression", p.ParseDerivedTableValues(false))
		return p.Expr(exp.ConditionalInsert, "this", insert, "expression", expression, "else_", elseFlag)
	}
	for e := parseConditionalInsert(); e != nil; e = parseConditionalInsert() {
		expressions = append(expressions, e)
	}
	if expressions == nil {
		expressions = []*exp.Expr{}
	}
	return p.ExpressionComments(exp.New(exp.MultitableInserts, "kind", kind, "expressions", expressions, "source", p.ParseTable(TableOpts{})), comments)
}

func (p *Parser) parseInsert() *exp.Expr {
	comments := []string{}
	hint := p.ParseHint()
	overwrite := p.Match(tokens.Overwrite)
	ignore := p.Match(tokens.Ignore)
	local := p.MatchTextSeq("LOCAL")
	var alternative any
	var isFunction any
	var this *exp.Expr

	if p.MatchTextSeq("DIRECTORY") {
		this = p.Expr(exp.Directory, "this", p.ParseVarOrString(false), "local", local, "row_format", p.ParseRowFormat(true))
	} else {
		if p.MatchSet(tokens.NewSet(tokens.First, tokens.All)) {
			comments = append(comments, p.PrevComments...)
			return p.ParseMultitableInserts(comments, "")
		}
		if p.Check(tokens.When) {
			return p.ParseMultitableInserts(comments, "ALL")
		}
		if p.Match(tokens.Or) {
			alternative = false
			if p.MatchTextsIn(p.C.InsertAlternatives) {
				alternative = p.Prev.Text
			}
		}
		p.Match(tokens.Into)
		comments = append(comments, p.PrevComments...)
		p.Match(tokens.Table)
		isF := p.Match(tokens.Function)
		isFunction = isF
		if isF {
			this = p.ParseFunction(FunctionOpts{})
		} else {
			this = p.ParseInsertTable()
		}
	}

	var setValues *exp.Expr
	if p.Match(tokens.Set) {
		var columns, values []*exp.Expr
		parseSetAssignment := func() *exp.Expr {
			target := p.ParseColumn()
			if target.Is(exp.Column) && p.Match(tokens.Eq) {
				var value *exp.Expr
				if p.D.SupportsValuesDefault && p.Match(tokens.Default) {
					value = exp.NewVar(p.PrevTextUpper())
				} else {
					value = p.ParseDisjunction()
				}
				if value != nil {
					columns = append(columns, target.This())
					values = append(values, value)
					return value
				}
			}
			p.RaiseError("Expected column assignment in INSERT ... SET")
			return nil
		}
		p.ParseCSV(parseSetAssignment)
		if columns == nil {
			columns = []*exp.Expr{}
		}
		if values == nil {
			values = []*exp.Expr{}
		}
		this = p.Expr(exp.Schema, "this", this, "expressions", columns)
		setValues = p.Expr(exp.Values, "expressions", []*exp.Expr{exp.New(exp.Tuple, "expressions", values)}, "alias", p.ParseTableAlias(nil))
	}

	returning := p.ParseReturning()
	var stored any = false
	if p.MatchTextSeq("STORED") {
		stored = nilIfNil(p.ParseStored())
	}
	byName := p.MatchTextSeq("BY", "NAME")
	exists := p.ParseExists(false)
	var replaceWhere, replaceUsing any
	if p.Match(tokens.Replace) {
		if p.Match(tokens.Where) {
			replaceWhere = nilIfNil(p.ParseDisjunction())
		} else if p.Match(tokens.Using) {
			replaceUsing = p.ParseUsingIdentifiers()
		}
	}
	partition := AndThen(p.Match(tokens.PartitionBy), p.ParsePartitionedBy)
	settings := AndThen(p.MatchTextSeq("SETTINGS"), p.ParseSettingsProperty)
	def := p.MatchTextSeq("DEFAULT", "VALUES")
	expression := setValues
	if expression == nil {
		expression = p.ParseDerivedTableValues(true)
	}
	if expression == nil {
		expression = p.ParseDdlSelect()
	}
	conflict := p.ParseOnConflict()
	if returning == nil {
		returning = p.ParseReturning()
	}
	source := AndThen(p.Match(tokens.Table), func() *exp.Expr { return p.ParseTable(TableOpts{}) })
	return p.ExpressionComments(exp.New(exp.Insert,
		"hint", hint,
		"is_function", isFunction,
		"this", this,
		"stored", stored,
		"by_name", byName,
		"exists", exists,
		"where", replaceWhere,
		"using", replaceUsing,
		"partition", partition,
		"settings", settings,
		"default", def,
		"expression", expression,
		"conflict", conflict,
		"returning", returning,
		"overwrite", overwrite,
		"alternative", alternative,
		"ignore", ignore,
		"source", source,
	), comments)
}

func (p *Parser) parseInsertTable() *exp.Expr {
	this := p.ParseTable(TableOpts{Schema: true, ParsePartition: true})
	if this.Is(exp.Table) && p.Match(tokens.Alias) {
		alias := p.ParseIdVar(true, nil)
		if alias != nil {
			this.Set("alias", p.ExpressionComments(exp.New(exp.TableAlias, "this", alias), alias.PopComments()))
		}
		return p.ParseSchema(this)
	}
	return this
}

func (p *Parser) parseKill() *exp.Expr {
	var kind *exp.Expr
	if p.MatchTexts("CONNECTION", "QUERY") {
		kind = exp.NewVar(p.Prev.Text)
	}
	return p.Expr(exp.Kill, "this", p.ParsePrimary(), "kind", kind)
}

func (p *Parser) parseOnConflict() *exp.Expr {
	conflict := p.MatchTextSeq("ON", "CONFLICT")
	duplicate := p.MatchTextSeq("ON", "DUPLICATE", "KEY")
	if !conflict && !duplicate {
		return nil
	}
	var conflictKeys any
	var constraint *exp.Expr
	if conflict {
		if p.MatchTextSeq("ON", "CONSTRAINT") {
			constraint = p.ParseIdVar(true, nil)
		} else if p.Match(tokens.LParen) {
			conflictKeys = p.ParseCSV(p.ParseIndexedColumn)
			p.MatchRParen(nil)
		}
	}
	indexPredicate := p.ParseWhere(false)
	action := p.ParseVarFromOptions(p.C.ConflictActions, true)
	var expressions any
	if p.Prev.Type == tokens.Update {
		p.Match(tokens.Set)
		expressions = p.ParseCSV(p.ParseUpdateAssignment)
	}
	return p.Expr(exp.OnConflict,
		"duplicate", duplicate,
		"expressions", expressions,
		"action", action,
		"conflict_keys", conflictKeys,
		"index_predicate", indexPredicate,
		"constraint", constraint,
		"where", p.ParseWhere(false),
	)
}

func (p *Parser) parseReturning() *exp.Expr {
	if !p.Match(tokens.Returning) {
		return nil
	}
	exprs := p.ParseCSV(p.ParseExpression)
	into := AndThen(p.Match(tokens.Into), func() *exp.Expr { return p.ParseTablePart(false) })
	return p.Expr(exp.Returning, "expressions", exprs, "into", into)
}

func (p *Parser) parseRow() *exp.Expr {
	if !p.Match(tokens.Format) {
		return nil
	}
	return p.ParseRowFormat(false)
}

func (p *Parser) parseSerdeProperties(with bool) *exp.Expr {
	index := p.Index
	with = with || p.MatchTextSeq("WITH")
	if !p.Match(tokens.SerdeProperties) {
		p.Retreat(index)
		return nil
	}
	var exprs []*exp.Expr
	for _, x := range p.ParseWrappedProperties() {
		exprs = append(exprs, ExprList(x)...)
	}
	if exprs == nil {
		exprs = []*exp.Expr{}
	}
	return p.Expr(exp.SerdeProperties, "expressions", exprs, "with_", with)
}
