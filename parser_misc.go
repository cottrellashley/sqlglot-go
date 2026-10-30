package sqlglot

import (
	"fmt"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
	"github.com/cottrellashley/sqlglot-go/trie"
)

func (p *Parser) parseAnalyzeStatistics() *exp.Expr {
	var this any
	kind := p.PrevTextUpper()
	var option any
	if p.MatchTextSeq("DELTA") {
		option = p.PrevTextUpper()
	}
	expressions := []*exp.Expr{}
	if !p.MatchTextSeq("STATISTICS") {
		p.RaiseError("Expecting token STATISTICS")
	}
	if p.MatchTextSeq("NOSCAN") {
		this = "NOSCAN"
	} else if p.Match(tokens.For) {
		if p.MatchTextSeq("ALL", "COLUMNS") {
			this = "FOR ALL COLUMNS"
		}
		if p.MatchTextSeq("COLUMNS") {
			this = "FOR COLUMNS"
			expressions = nonNilList(p.ParseCSV(p.ParseColumnReference))
		}
	} else if p.MatchTextSeq("SAMPLE") {
		sample := p.ParseNumber()
		var k any
		if p.Match(tokens.Percent) {
			k = p.PrevTextUpper()
		}
		expressions = []*exp.Expr{p.Expr(exp.AnalyzeSample, "sample", sample, "kind", k)}
	}
	return p.Expr(exp.AnalyzeStatistics, "kind", kind, "option", option, "this", this, "expressions", expressions)
}

func (p *Parser) parseAnalyzeValidate() *exp.Expr {
	var kind, this any
	var expression *exp.Expr
	if p.MatchTextSeq("REF", "UPDATE") {
		kind = "REF"
		this = "UPDATE"
		if p.MatchTextSeq("SET", "DANGLING", "TO", "NULL") {
			this = "UPDATE SET DANGLING TO NULL"
		}
	} else if p.MatchTextSeq("STRUCTURE") {
		kind = "STRUCTURE"
		if p.MatchTextSeq("CASCADE", "FAST") {
			this = "CASCADE FAST"
		} else if p.MatchTextSeq("CASCADE", "COMPLETE") && p.MatchTexts("ONLINE", "OFFLINE") {
			this = "CASCADE COMPLETE " + p.PrevTextUpper()
			expression = p.ParseInto()
		}
	}
	return p.Expr(exp.AnalyzeValidate, "kind", kind, "this", this, "expression", expression)
}

func (p *Parser) parseAnalyzeColumns() *exp.Expr {
	this := p.PrevTextUpper()
	if p.MatchTextSeq("COLUMNS") {
		return p.Expr(exp.AnalyzeColumns, "this", this+" "+p.PrevTextUpper())
	}
	return nil
}

func (p *Parser) parseAnalyzeDelete() *exp.Expr {
	var kind any
	if p.MatchTextSeq("SYSTEM") {
		kind = p.PrevTextUpper()
	}
	if p.MatchTextSeq("STATISTICS") {
		return p.Expr(exp.AnalyzeDelete, "kind", kind)
	}
	return nil
}

func (p *Parser) parseAnalyzeList() *exp.Expr {
	if p.MatchTextSeq("CHAINED", "ROWS") {
		return p.Expr(exp.AnalyzeListChainedRows, "expression", p.ParseInto())
	}
	return nil
}

func (p *Parser) parseAnalyzeHistogram() *exp.Expr {
	this := p.PrevTextUpper()
	var expression *exp.Expr
	expressions := []*exp.Expr{}
	var updateOptions any
	if p.MatchTextSeq("HISTOGRAM", "ON") {
		expressions = nonNilList(p.ParseCSV(p.ParseColumnReference))
		var withExpressions []string
		for p.Match(tokens.With) {
			if p.MatchTexts("SYNC", "ASYNC") {
				if p.CheckTextSeq("MODE") {
					withExpressions = append(withExpressions, p.PrevTextUpper()+" MODE")
					p.Advance()
				}
			} else {
				buckets := p.ParseNumber()
				if p.MatchTextSeq("BUCKETS") {
					s := "None"
					if buckets != nil {
						s = buckets.SQL()
					}
					withExpressions = append(withExpressions, s+" BUCKETS")
				}
			}
		}
		if len(withExpressions) > 0 {
			expression = p.Expr(exp.AnalyzeWith, "expressions", withExpressions)
		}
		if p.MatchTexts("MANUAL", "AUTO") && p.Check(tokens.Update) {
			updateOptions = p.PrevTextUpper()
			p.Advance()
		} else if p.MatchTextSeq("USING", "DATA") {
			expression = p.Expr(exp.UsingData, "this", p.ParseString())
		}
	}
	return p.Expr(exp.AnalyzeHistogram, "this", this, "expressions", expressions, "expression", expression, "update_options", updateOptions)
}

func (p *Parser) parseMerge() *exp.Expr {
	p.Match(tokens.Into)
	target := p.ParseTable(TableOpts{})
	if target != nil && p.Check(tokens.Alias) {
		target.Set("alias", p.ParseTableAlias(nil))
	}
	p.Match(tokens.Using)
	using := p.ParseTable(TableOpts{})
	on := AndThen(p.Match(tokens.On), p.ParseDisjunction)
	var usingCond any = false
	if p.Match(tokens.Using) {
		usingCond = p.ParseUsingIdentifiers()
	}
	return p.Expr(exp.Merge, "this", target, "using", using, "on", on, "using_cond", usingCond,
		"whens", p.ParseWhenMatched(), "returning", p.ParseReturning())
}

func (p *Parser) parseWhenMatched() *exp.Expr {
	whens := []*exp.Expr{}
	for p.Match(tokens.When) {
		matched := !p.Match(tokens.Not)
		p.MatchTextSeq("MATCHED")
		var source bool
		if p.MatchTextSeq("BY", "TARGET") {
			source = false
		} else {
			source = p.MatchTextSeq("BY", "SOURCE")
		}
		var condition *exp.Expr
		if p.Match(tokens.And) {
			condition = p.ParseDisjunction()
		}
		p.Match(tokens.Then)
		var then *exp.Expr
		if p.Match(tokens.Insert) {
			var this *exp.Expr
			if p.MatchTextSeq("ALL", "BY", "NAME") {
				this = exp.New(exp.Star)
			} else {
				this = p.ParseStar()
			}
			if this != nil {
				then = p.Expr(exp.Insert, "this", this)
			} else {
				var t *exp.Expr
				if p.MatchTextSeq("ROW") {
					t = exp.NewVar("ROW")
				} else {
					t = p.ParseValue(false)
				}
				expression := AndThen(p.MatchTextSeq("VALUES"), func() *exp.Expr { return p.ParseValue(true) })
				then = p.Expr(exp.Insert, "this", t, "expression", expression, "where", p.ParseWhere(false))
			}
		} else if p.Match(tokens.Update) {
			expressions := p.ParseStar()
			if expressions != nil {
				then = p.Expr(exp.Update, "expressions", expressions)
			} else if p.MatchTextSeq("ALL", "BY", "NAME") {
				then = p.Expr(exp.Update, "expressions", []*exp.Expr{exp.New(exp.Star)})
			} else {
				var exprs any = false
				if p.Match(tokens.Set) {
					exprs = p.ParseCSV(p.ParseUpdateAssignment)
				}
				then = p.Expr(exp.Update, "expressions", exprs, "where", p.ParseWhere(false))
			}
		} else if p.Match(tokens.Delete) {
			then = p.Expr(exp.Var, "this", p.Prev.Text)
		} else {
			then = p.ParseVarFromOptions(p.C.ConflictActions, true)
		}
		whens = append(whens, p.Expr(exp.When, "matched", matched, "source", source, "condition", condition, "then", then))
	}
	return p.Expr(exp.Whens, "expressions", whens)
}

func (p *Parser) parseShow() *exp.Expr {
	parser := p.FindParser(p.C.ShowParsers, p.C.ShowTrie)
	if parser != nil {
		return parser(p)
	}
	return p.ParseAsCommand(p.Prev)
}

func (p *Parser) parseSetItemAssignment(kind string) *exp.Expr {
	index := p.Index
	if (kind == "GLOBAL" || kind == "SESSION") && p.MatchTextSeq("TRANSACTION") {
		return p.ParseSetTransaction(kind == "GLOBAL")
	}
	left := p.ParsePrimary()
	if left == nil {
		left = p.ParseColumn()
	}
	assignmentDelimiter := p.MatchTextsIn(p.C.SetAssignmentDelimiters)
	if left == nil || (p.C.SetRequiresAssignmentDelimiter && !assignmentDelimiter) {
		p.Retreat(index)
		return nil
	}
	right := p.ParseStatement()
	if right == nil {
		right = p.ParseIdVar(true, nil)
	}
	if right.IsAny(exp.Column, exp.Identifier) {
		right = exp.NewVar(right.Name())
	}
	this := p.Expr(exp.EQ, "this", left, "expression", right)
	return p.Expr(exp.SetItem, "this", this, "kind", strOrNil(kind))
}

func (p *Parser) parseSetTransaction(global bool) *exp.Expr {
	p.MatchTextSeq("TRANSACTION")
	characteristics := p.ParseCSV(func() *exp.Expr { return p.ParseVarFromOptions(p.C.TransactionCharacteristics, true) })
	return p.Expr(exp.SetItem, "expressions", nonNilList(characteristics), "kind", "TRANSACTION", "global_", global)
}

func (p *Parser) parseSetItem() *exp.Expr {
	parser := p.FindParser(p.C.SetParsers, p.C.SetTrie)
	if parser != nil {
		return parser(p)
	}
	return p.ParseSetItemAssignment("")
}

func (p *Parser) parseSet(unset, tag bool) *exp.Expr {
	index := p.Index
	set := p.Expr(exp.Set, "expressions", nonNilList(p.ParseCSV(p.ParseSetItem)), "unset", unset, "tag", tag)
	if p.Curr.Ok() {
		p.Retreat(index)
		return p.ParseAsCommand(p.Prev)
	}
	return set
}

func (p *Parser) parseVarFromOptions(options OptionsType, raiseUnmatched bool) *exp.Expr {
	start := p.Curr
	if !start.Ok() {
		return nil
	}
	option := strings.ToUpper(start.Text)
	var continuations [][]string
	hasKey := false
	if !p.C.TextMatchExcludedTokens.Has(start.Type) {
		continuations, hasKey = options[option]
	}
	index := p.Index
	p.Advance()
	matched := false
	for _, keywords := range continuations {
		if p.MatchTextSeq(keywords...) {
			option = option + " " + strings.Join(keywords, " ")
			matched = true
			break
		}
	}
	if !matched {
		if len(continuations) > 0 || !hasKey {
			if raiseUnmatched {
				p.RaiseError(fmt.Sprintf("Unknown option %s", option))
			}
			p.Retreat(index)
			return nil
		}
	}
	return exp.NewVar(option)
}

func (p *Parser) parseAsCommand(start *tokens.Token) *exp.Expr {
	for p.Curr.Ok() {
		p.Advance()
	}
	text := []rune(p.FindSQL(start, p.Prev))
	size := len([]rune(start.Text))
	if size > len(text) {
		size = len(text)
	}
	p.WarnUnsupported()
	return exp.New(exp.Command, "this", string(text[:size]), "expression", string(text[size:]))
}

func (p *Parser) parseDictProperty(this string) *exp.Expr {
	settings := []*exp.Expr{}
	p.MatchLParen(nil)
	kind := p.ParseIdVar(true, nil)
	if p.Match(tokens.LParen) {
		for {
			key := p.ParseIdVar(true, nil)
			value := p.ParseFunction(FunctionOpts{})
			if value == nil {
				value = p.ParsePrimaryOrVar()
			}
			if key == nil && value == nil {
				break
			}
			settings = append(settings, p.Expr(exp.DictSubProperty, "this", key, "value", value))
		}
		p.Match(tokens.RParen)
	}
	p.MatchRParen(nil)
	var k any
	if kind != nil {
		k = kind.Arg("this")
	}
	return p.Expr(exp.DictProperty, "this", this, "kind", k, "settings", settings)
}

func (p *Parser) parseDictRange(this string) *exp.Expr {
	p.MatchLParen(nil)
	hasMin := p.MatchTextSeq("MIN")
	var min, max *exp.Expr
	varOrPrimary := func() *exp.Expr {
		if v := p.ParseVar(false, nil, false); v != nil {
			return v
		}
		return p.ParsePrimary()
	}
	if hasMin {
		min = varOrPrimary()
		p.MatchTextSeq("MAX")
		max = varOrPrimary()
	} else {
		max = varOrPrimary()
		min = exp.LiteralNumber(0)
	}
	p.MatchRParen(nil)
	return p.Expr(exp.DictRange, "this", this, "min", min, "max", max)
}

func (p *Parser) parseComprehension(this *exp.Expr) *exp.Expr {
	index := p.Index
	expression := p.ParseIdVar(true, nil)
	var position *exp.Expr
	if p.Match(tokens.Comma) {
		position = p.ParseIdVar(true, nil)
	}
	if !p.Match(tokens.In) {
		p.Retreat(index - 1)
		return nil
	}
	iterator := p.ParseColumn()
	var condition *exp.Expr
	if p.MatchTextSeq("IF") {
		condition = p.ParseDisjunction()
	}
	return p.Expr(exp.Comprehension, "this", this, "expression", expression, "position", position, "iterator", iterator, "condition", condition)
}

func (p *Parser) parseHeredoc() *exp.Expr {
	if p.Match(tokens.HeredocString) {
		return p.Expr(exp.Heredoc, "this", p.Prev.Text)
	}
	if !p.MatchTextSeq("$") {
		return nil
	}
	tags := []string{"$"}
	var tagText any
	if p.IsConnected() {
		p.Advance()
		tags = append(tags, p.PrevTextUpper())
	} else {
		p.RaiseError("No closing $ found")
	}
	if tags[len(tags)-1] != "$" {
		if p.IsConnected() && p.MatchTextSeq("$") {
			tagText = tags[len(tags)-1]
			tags = append(tags, "$")
		} else {
			p.RaiseError("No closing $ found")
		}
	}
	heredocStart := p.Curr
	for p.Curr.Ok() {
		if p.CheckTextSeq(tags...) {
			this := p.FindSQL(heredocStart, p.Prev)
			p.AdvanceBy(len(tags))
			return p.Expr(exp.Heredoc, "this", this, "tag", tagText)
		}
		p.Advance()
	}
	p.RaiseError(fmt.Sprintf("No closing %s found", strings.Join(tags, "")))
	return nil
}

// FindParser mirrors _find_parser: it matches the longest multi-word key in trie.
func (p *Parser) FindParser(parsers map[string]ParseFunc, tr *trie.Node[string]) ParseFunc {
	if !p.Curr.Ok() || tr == nil {
		return nil
	}
	index := p.Index
	var this []string
	node := tr
	for {
		curr := strings.ToUpper(p.Curr.Text)
		key := strings.Split(curr, " ")
		this = append(this, curr)
		p.Advance()
		var result trie.Result
		result, node = node.Lookup(key)
		if result == trie.Failed {
			break
		}
		if result == trie.Exists {
			return parsers[strings.Join(this, " ")]
		}
	}
	p.Retreat(index)
	return nil
}

func (p *Parser) replaceLambda(node *exp.Expr, expressions []*exp.Expr) *exp.Expr {
	if node == nil {
		return node
	}
	lambdaTypes := map[string]any{}
	for _, e := range expressions {
		if to := e.Get("to"); to != nil {
			lambdaTypes[e.Name()] = to
		} else {
			lambdaTypes[e.Name()] = false
		}
	}
	for column := range node.FindAll(exp.Column) {
		parts := column.Parts()
		if len(parts) == 0 {
			continue
		}
		typ, ok := lambdaTypes[parts[0].Name()]
		if !ok {
			continue
		}
		var dotOrID *exp.Expr
		if column.TableName() != "" {
			dotOrID = column.ToDot(true)
		} else {
			dotOrID = column.This()
		}
		if dt, ok := typ.(*exp.Expr); ok && dt.Is(exp.DataType) {
			dotOrID = p.Expr(exp.Cast, "this", dotOrID, "to", dt.Copy())
		}
		parent := column.Parent()
		broke := false
		for parent.Is(exp.Dot) {
			if !parent.Parent().Is(exp.Dot) {
				parent.Replace(dotOrID)
				broke = true
				break
			}
			parent = parent.Parent()
		}
		if !broke {
			if column == node {
				node = dotOrID
			} else {
				column.Replace(dotOrID)
			}
		}
	}
	return node
}
