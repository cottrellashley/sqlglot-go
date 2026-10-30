package sqlglot

import (
	"fmt"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func (p *Parser) parseTransaction() *exp.Expr {
	var this any
	if p.MatchTextsIn(p.C.TransactionKind) {
		this = p.Prev.Text
	}
	p.MatchTexts("TRANSACTION", "WORK")
	modes := []string{}
	for {
		var mode []string
		for p.Match(tokens.Var) || p.Match(tokens.Not) {
			mode = append(mode, p.Prev.Text)
		}
		if len(mode) > 0 {
			modes = append(modes, strings.Join(mode, " "))
		}
		if !p.Match(tokens.Comma) {
			break
		}
	}
	return p.Expr(exp.Transaction, "this", this, "modes", modes)
}

func (p *Parser) parseCommitOrRollback() *exp.Expr {
	var chain any
	var savepoint *exp.Expr
	isRollback := p.Prev.Type == tokens.Rollback
	p.MatchTexts("TRANSACTION", "WORK")
	if p.MatchTextSeq("TO") {
		p.MatchTextSeq("SAVEPOINT")
		savepoint = p.ParseIdVar(true, nil)
	}
	if p.Match(tokens.And) {
		chain = !p.MatchTextSeq("NO")
		p.MatchTextSeq("CHAIN")
	}
	if isRollback {
		return p.Expr(exp.Rollback, "savepoint", savepoint)
	}
	return p.Expr(exp.Commit, "chain", chain)
}

func (p *Parser) parseRefresh() *exp.Expr {
	start := p.Prev
	kind := ""
	switch {
	case p.MatchTextSeq("EXTERNAL", "TABLE"):
		kind = "EXTERNAL TABLE"
	case p.Match(tokens.Table):
		kind = "TABLE"
	case p.MatchTextSeq("MATERIALIZED", "VIEW"):
		kind = "MATERIALIZED VIEW"
	}
	this := p.ParseString()
	if kind == "" && !this.Is(exp.Literal) {
		return p.ParseAsCommand(start)
	}
	if this == nil {
		this = p.ParseTable(TableOpts{})
	}
	return p.Expr(exp.Refresh, "this", this, "kind", kind)
}

func (p *Parser) parseColumnDefWithExists() *exp.Expr {
	start := p.Index
	p.Match(tokens.Column)
	existsColumn := p.ParseExists(true)
	expression := p.ParseFieldDef()
	if !expression.Is(exp.ColumnDef) {
		p.Retreat(start)
		return nil
	}
	expression.Set("exists", existsColumn)
	return expression
}

func (p *Parser) parseAddColumn() *exp.Expr {
	if p.PrevTextUpper() != "ADD" {
		return nil
	}
	return p.ParseColumnDefWithExists()
}

func (p *Parser) parseDropColumn() *exp.Expr {
	if !p.Match(tokens.Drop) {
		return nil
	}
	kind := ""
	if !p.C.AlterDropRequiresColumn {
		index := p.Index
		p.ParseExists(false)
		if p.Curr.Type != tokens.Column && p.Curr.Type != tokens.Constraint && p.MatchSet(p.C.IdVarTokens) &&
			(!p.Curr.Ok() || p.Check(tokens.Comma) || p.CheckTexts("CASCADE", "RESTRICT")) {
			kind = "COLUMN"
		}
		p.Retreat(index)
	}
	return p.ParseDrop(false, kind)
}

func (p *Parser) parseAlterDropAction() *exp.Expr {
	drop := p.ParseDropColumn()
	if drop.Is(exp.Drop) && drop.TextUpper("kind") == "COLUMN" && !p.D.AlterTableDropRequiredForEachColumn && p.Match(tokens.Comma) {
		drop.Set("tables", append(append([]*exp.Expr(nil), drop.List("tables")...), p.ParseCSV(p.ParseColumn)...))
	}
	return drop
}

func (p *Parser) parseDropPartition(exists any) *exp.Expr {
	return p.Expr(exp.DropPartition, "expressions", nonNilList(p.ParseCSV(p.ParsePartition)), "exists", exists)
}

func (p *Parser) parseAlterTableAdd() []*exp.Expr {
	addAlteration := func() *exp.Expr {
		p.MatchTextSeq("ADD")
		if p.CheckSet(p.C.AddConstraintTokens) || p.CheckTextsIn(p.C.AddConstraintKeywords) {
			return p.Expr(exp.AddConstraint, "expressions", nonNilList(p.ParseCSV(p.ParseConstraint)))
		}
		columnDef := p.ParseAddColumn()
		if columnDef.Is(exp.ColumnDef) {
			return columnDef
		}
		exists := p.ParseExists(true)
		if p.CheckPair(tokens.Partition, tokens.LParen) {
			this := p.ParseField(true, nil, false)
			var location any = false
			if p.CheckTextSeq("LOCATION") {
				location = p.ParseProperty()
			}
			return p.Expr(exp.AddPartition, "exists", exists, "this", this, "location", location)
		}
		return nil
	}
	if !p.CheckSet(p.C.AddConstraintTokens) && (!p.D.AlterTableAddRequiredForEachColumn || p.MatchTextSeq("COLUMNS")) {
		schema := p.ParseSchema(nil)
		if schema != nil {
			return []*exp.Expr{schema}
		}
		return p.ParseCSV(p.ParseColumnDefWithExists)
	}
	return p.ParseCSV(addAlteration)
}

func (p *Parser) parseAlterTableAlter() *exp.Expr {
	if MatchTextsKey(p, p.C.AlterAlterParsers) {
		return p.C.AlterAlterParsers[p.PrevTextUpper()](p)
	}
	p.Match(tokens.Column)
	exists := BoolOrNil(p.ParseExists(false))
	column := p.ParseField(true, nil, false)
	if p.MatchPair(tokens.Drop, tokens.Default) {
		return p.Expr(exp.AlterColumn, "this", column, "drop", true, "exists", exists)
	}
	if p.MatchPair(tokens.Set, tokens.Default) {
		return p.Expr(exp.AlterColumn, "this", column, "default", p.ParseDisjunction(), "exists", exists)
	}
	if p.Match(tokens.Comment) {
		return p.Expr(exp.AlterColumn, "this", column, "comment", p.ParseString(), "exists", exists)
	}
	if p.MatchTextSeq("DROP", "NOT", "NULL") {
		return p.Expr(exp.AlterColumn, "this", column, "drop", true, "allow_null", true, "exists", exists)
	}
	if p.MatchTextSeq("SET", "NOT", "NULL") {
		return p.Expr(exp.AlterColumn, "this", column, "allow_null", false, "exists", exists)
	}
	if p.MatchTextSeq("SET", "VISIBLE") {
		return p.Expr(exp.AlterColumn, "this", column, "visible", "VISIBLE", "exists", exists)
	}
	if p.MatchTextSeq("SET", "INVISIBLE") {
		return p.Expr(exp.AlterColumn, "this", column, "visible", "INVISIBLE", "exists", exists)
	}
	p.MatchTextSeq("SET", "DATA")
	var dtype *exp.Expr
	if p.MatchTextSeq("TYPE") || !p.C.AlterColumnTypeRequiresKeyword {
		dtype = p.ParseTypes(false, false, true, false)
	}
	if dtype == nil {
		return nil
	}
	collate := AndThen(p.Match(tokens.Collate), func() *exp.Expr { return p.ParseTerm(true) })
	using := AndThen(p.Match(tokens.Using), p.ParseDisjunction)
	return p.Expr(exp.AlterColumn, "this", column, "dtype", dtype, "collate", collate, "using", using, "exists", exists)
}

func (p *Parser) parseAlterDiststyle() *exp.Expr {
	if p.MatchTexts("ALL", "EVEN", "AUTO") {
		return p.Expr(exp.AlterDistStyle, "this", exp.NewVar(p.PrevTextUpper()))
	}
	p.MatchTextSeq("KEY", "DISTKEY")
	return p.Expr(exp.AlterDistStyle, "this", p.ParseColumn())
}

func (p *Parser) parseAlterSortkey(compound any) *exp.Expr {
	if exp.Truthy(compound) {
		p.MatchTextSeq("SORTKEY")
	}
	if p.Check(tokens.LParen) {
		return p.Expr(exp.AlterSortKey, "expressions", p.ParseWrappedIdVars(false), "compound", compound)
	}
	p.MatchTexts("AUTO", "NONE")
	return p.Expr(exp.AlterSortKey, "this", exp.NewVar(p.PrevTextUpper()), "compound", compound)
}

func (p *Parser) parseAlterTableDrop() []*exp.Expr {
	index := p.Index - 1
	partitionExists := p.ParseExists(false)
	if p.Check(tokens.Partition) {
		return p.ParseCSV(func() *exp.Expr { return p.ParseDropPartition(partitionExists) })
	}
	p.Retreat(index)
	return p.ParseCSV(p.ParseAlterDropAction)
}

func (p *Parser) parseAlterTableRename() *exp.Expr {
	if p.Match(tokens.Column) || (!p.C.AlterRenameRequiresColumn && !p.CheckTextSeq("TO")) {
		exists := p.ParseExists(false)
		oldColumn := p.ParseColumn()
		to := p.MatchTextSeq("TO")
		newColumn := p.ParseColumn()
		if oldColumn == nil || !to || newColumn == nil {
			return nil
		}
		return p.Expr(exp.RenameColumn, "this", oldColumn, "to", newColumn, "exists", exists)
	}
	p.MatchTextSeq("TO")
	return p.Expr(exp.AlterRename, "this", p.ParseTable(TableOpts{Schema: true}))
}

func (p *Parser) parseAlterTableSet() *exp.Expr {
	alterSet := p.Expr(exp.AlterSet)
	switch {
	case p.Check(tokens.LParen) || p.MatchTextSeq("TABLE", "PROPERTIES"):
		alterSet.Set("expressions", p.ParseWrappedCSV(p.ParseAssignment, false))
	case p.CheckTextSeq("FILESTREAM_ON"):
		alterSet.Set("expressions", []*exp.Expr{p.ParseAssignment()})
	case p.MatchTexts("LOGGED", "UNLOGGED"):
		alterSet.Set("option", exp.NewVar(p.PrevTextUpper()))
	case p.MatchTextSeq("WITHOUT") && p.MatchTexts("CLUSTER", "OIDS"):
		alterSet.Set("option", exp.NewVar("WITHOUT "+p.PrevTextUpper()))
	case p.MatchTextSeq("LOCATION"):
		alterSet.Set("location", p.ParseField(false, nil, false))
	case p.MatchTextSeq("ACCESS", "METHOD"):
		alterSet.Set("access_method", p.ParseField(false, nil, false))
	case p.MatchTextSeq("TABLESPACE"):
		alterSet.Set("tablespace", p.ParseField(false, nil, false))
	case p.MatchTextSeq("FILE", "FORMAT") || p.MatchTextSeq("FILEFORMAT"):
		alterSet.Set("file_format", []*exp.Expr{p.ParseField(false, nil, false)})
	case p.MatchTextSeq("STAGE_FILE_FORMAT"):
		alterSet.Set("file_format", p.ParseWrappedOptions())
	case p.MatchTextSeq("STAGE_COPY_OPTIONS"):
		alterSet.Set("copy_options", p.ParseWrappedOptions())
	case p.MatchTextSeq("TAG") || p.MatchTextSeq("TAGS"):
		alterSet.Set("tag", p.ParseCSV(p.ParseAssignment))
	default:
		if p.MatchTextSeq("SERDE") {
			alterSet.Set("serde", p.ParseField(false, nil, false))
		}
		properties := p.ParseWrappedExpr(func() *exp.Expr { return p.ParseProperties(false) }, true)
		alterSet.Set("expressions", []*exp.Expr{properties})
	}
	return alterSet
}

func (p *Parser) parseAlterSession() *exp.Expr {
	if p.Match(tokens.Set) {
		expressions := p.ParseCSV(func() *exp.Expr { return p.ParseSetItemAssignment("") })
		return p.Expr(exp.AlterSession, "expressions", nonNilList(expressions), "unset", false)
	}
	p.MatchTextSeq("UNSET")
	expressions := p.ParseCSV(func() *exp.Expr { return p.Expr(exp.SetItem, "this", p.ParseIdVar(true, nil)) })
	return p.Expr(exp.AlterSession, "expressions", nonNilList(expressions), "unset", true)
}

func (p *Parser) parseAlter() *exp.Expr {
	start := p.Prev
	iceberg := p.MatchTextSeq("ICEBERG")
	var alterToken *tokens.Token
	if p.MatchSet(p.C.Alterables) {
		alterToken = p.Prev
	}
	if alterToken == nil {
		return p.ParseAsCommand(start)
	}
	if iceberg && alterToken.Type != tokens.Table {
		return p.ParseAsCommand(start)
	}
	exists := p.ParseExists(false)
	only := p.MatchTextSeq("ONLY")
	var this, cluster *exp.Expr
	var check bool
	var parser AlterParser
	if alterToken.Type == tokens.Session {
		parser = p.C.AlterParsers[strings.ToUpper(alterToken.Text)]
	} else {
		this = p.ParseTable(TableOpts{Schema: true, ParsePartition: p.C.AlterTablePartitions})
		check = p.MatchTextSeq("WITH", "CHECK")
		if p.Match(tokens.On) {
			cluster = p.ParseOnProperty()
		}
		if MatchTextsKey(p, p.C.AlterParsers) {
			parser = p.C.AlterParsers[p.PrevTextUpper()]
		}
	}
	options := []*exp.Expr{}
	if parser == nil && alterToken.Type == tokens.Table && !p.C.AlterTableRequiresAction {
		if properties := p.TryParse(func() *exp.Expr { return p.ParseProperties(false) }, false); properties != nil {
			options = properties.Expressions()
		}
	}
	var actions []*exp.Expr
	if parser != nil {
		actions = parser(p)
	}
	if len(actions) > 0 || len(options) > 0 {
		for p.C.AlterTableMixedActions && (p.Match(tokens.Comma) || p.Prev.Type == tokens.Comma) {
			if !CheckTextsKey(p, p.C.PropertyParsers) && MatchTextsKey(p, p.C.AlterParsers) {
				parser := p.C.AlterParsers[p.PrevTextUpper()]
				var parsed []*exp.Expr
				p.TryParse(func() *exp.Expr {
					parsed = parser(p)
					if len(parsed) == 0 {
						return nil
					}
					return parsed[0]
				}, false)
				hasCommand := false
				for _, a := range parsed {
					if a.Is(exp.Command) {
						hasCommand = true
					}
				}
				if len(parsed) == 0 || hasCommand {
					return p.ParseAsCommand(start)
				}
				actions = append(actions, parsed...)
			} else if properties := p.TryParse(func() *exp.Expr { return p.ParseProperties(false) }, false); properties != nil {
				options = append(options, properties.Expressions()...)
			} else {
				break
			}
		}
		notValid := p.MatchTextSeq("NOT", "VALID")
		if properties := p.TryParse(func() *exp.Expr { return p.ParseProperties(false) }, false); properties != nil {
			options = append(options, properties.Expressions()...)
		}
		cascade := p.D.AlterTableSupportsCascade && p.MatchTextSeq("CASCADE")
		if !p.Curr.Ok() {
			if actions == nil {
				actions = []*exp.Expr{}
			}
			return p.Expr(exp.Alter, "this", this, "kind", strings.ToUpper(alterToken.Text), "exists", exists,
				"actions", actions, "only", only, "options", options, "cluster", cluster, "not_valid", notValid,
				"check", check, "cascade", cascade, "iceberg", iceberg)
		}
	}
	return p.ParseAsCommand(start)
}

func (p *Parser) parseAnalyze() *exp.Expr {
	start := p.Prev
	if !p.Curr.Ok() {
		return p.Expr(exp.Analyze)
	}
	options := []string{}
	for p.MatchTextsIn(p.C.AnalyzeStyles) {
		if p.PrevTextUpper() == "BUFFER_USAGE_LIMIT" {
			n := p.ParseNumber()
			s := "None"
			if n != nil {
				s = n.SQL()
			}
			options = append(options, "BUFFER_USAGE_LIMIT "+s)
		} else {
			options = append(options, p.PrevTextUpper())
		}
	}
	var tables []*exp.Expr
	var innerExpression *exp.Expr
	var kind any
	if p.Curr.Ok() {
		kind = strings.ToUpper(p.Curr.Text)
	}
	switch {
	case p.Match(tokens.Table):
		tables = p.ParseCSV(func() *exp.Expr { return p.ParseTableParts(false, false, false, false) })
	case p.Match(tokens.Index):
		if t := p.ParseTableParts(false, false, false, false); t != nil {
			tables = []*exp.Expr{t}
		}
	case p.MatchTextSeq("TABLES"):
		if p.MatchSet(tokens.NewSet(tokens.From, tokens.In)) {
			kind = fmt.Sprintf("%v %s", kind, p.PrevTextUpper())
			if t := p.ParseTable(TableOpts{Schema: true, IsDbReference: true}); t != nil {
				tables = []*exp.Expr{t}
			}
		}
	case p.MatchTextSeq("DATABASE"):
		if t := p.ParseTable(TableOpts{Schema: true, IsDbReference: true}); t != nil {
			tables = []*exp.Expr{t}
		}
	case p.MatchTextSeq("CLUSTER"):
		if t := p.ParseTable(TableOpts{}); t != nil {
			tables = []*exp.Expr{t}
		}
	case MatchTextsKey(p, p.C.AnalyzeExpressionParsers):
		kind = nil
		innerExpression = p.C.AnalyzeExpressionParsers[p.PrevTextUpper()](p)
	default:
		kind = nil
		tables = p.ParseCSV(func() *exp.Expr { return p.ParseTableParts(false, false, false, false) })
	}
	partition := p.TryParse(p.ParsePartition, false)
	if partition == nil && p.MatchTextsIn(p.C.PartitionKeywords) {
		return p.ParseAsCommand(start)
	}
	var mode any
	if p.MatchTextSeq("WITH", "SYNC", "MODE") || p.MatchTextSeq("WITH", "ASYNC", "MODE") {
		mode = fmt.Sprintf("WITH %s MODE", strings.ToUpper(p.Tokens[p.Index-2].Text))
	}
	if MatchTextsKey(p, p.C.AnalyzeExpressionParsers) {
		innerExpression = p.C.AnalyzeExpressionParsers[p.PrevTextUpper()](p)
	}
	properties := p.ParseProperties(false)
	return p.Expr(exp.Analyze, "kind", kind, "tables", nonNilList(tables), "mode", mode, "partition", partition,
		"properties", properties, "expression", innerExpression, "options", options)
}
