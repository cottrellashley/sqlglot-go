package sqlglot

import (
	"fmt"
	"strings"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

// sentinel mirrors SENTINEL_NONE: a falsy token used when there is no current/next/prev.
var sentinel = &tokens.Token{Type: tokens.Sentinel, Text: "SENTINEL", Line: 1, Col: 1, Comments: []string{}}

// Parser consumes tokens and produces a syntax tree. It mirrors sqlglot.parser.Parser; the
// overridable methods live in C.Methods (see parser_methods_gen.go).
type Parser struct {
	D *Dialect
	C *ParserConfig

	ErrorLevel          errs.ErrorLevel
	ErrorMessageContext int
	MaxErrors           int
	MaxNodes            int

	SQL    []rune
	Errors []*errs.ParseError

	Tokens       []*tokens.Token
	TokensSize   int
	Index        int
	Curr         *tokens.Token
	Next         *tokens.Token
	Prev         *tokens.Token
	PrevComments []string

	PipeCTECounter int
	chunks         [][]*tokens.Token
	chunkIndex     int
	nodeCount      int

	// Extra is free-form per-parse storage for dialects.
	Extra map[string]any
}

func newParser(d *Dialect, opts ParserOptions) *Parser {
	p := &Parser{D: d, C: d.Parser}
	p.ErrorLevel = opts.ErrorLevel
	if p.ErrorLevel == errs.Unset {
		p.ErrorLevel = errs.Immediate
	}
	p.ErrorMessageContext = opts.ErrorMessageContext
	if p.ErrorMessageContext == 0 {
		p.ErrorMessageContext = 100
	}
	p.MaxErrors = opts.MaxErrors
	if p.MaxErrors == 0 {
		p.MaxErrors = 3
	}
	p.MaxNodes = opts.MaxNodes
	if p.MaxNodes == 0 {
		p.MaxNodes = -1
	}
	p.Reset()
	return p
}

// Reset clears the parser state.
func (p *Parser) Reset() {
	p.SQL = nil
	p.Errors = nil
	p.Tokens = nil
	p.TokensSize = 0
	p.Index = 0
	p.Curr = sentinel
	p.Next = sentinel
	p.Prev = sentinel
	p.PrevComments = []string{}
	p.PipeCTECounter = 0
	p.chunks = nil
	p.chunkIndex = 0
	p.nodeCount = 0
}

// Advance moves to the next token.
func (p *Parser) Advance() { p.AdvanceBy(1) }

// AdvanceBy moves times tokens forward (negative values move backwards).
func (p *Parser) AdvanceBy(times int) {
	index := p.Index + times
	p.Index = index
	if index >= 0 && index < p.TokensSize {
		p.Curr = p.Tokens[index]
	} else {
		p.Curr = sentinel
	}
	if index+1 >= 0 && index+1 < p.TokensSize {
		p.Next = p.Tokens[index+1]
	} else {
		p.Next = sentinel
	}
	if index > 0 && index-1 < p.TokensSize {
		prev := p.Tokens[index-1]
		p.Prev = prev
		p.PrevComments = prev.Comments
	} else {
		p.Prev = sentinel
		p.PrevComments = []string{}
	}
}

func (p *Parser) advanceChunk() {
	p.Index = -1
	p.Tokens = p.chunks[p.chunkIndex]
	p.TokensSize = len(p.Tokens)
	p.chunkIndex++
	p.Advance()
}

// Retreat moves back (or forward) to index.
func (p *Parser) Retreat(index int) {
	if index != p.Index {
		p.AdvanceBy(index - p.Index)
	}
}

// AddComments attaches the pending comments to e.
func (p *Parser) AddComments(e *exp.Expr) {
	if e != nil && len(p.PrevComments) > 0 {
		e.AddComments(p.PrevComments, false)
		p.PrevComments = []string{}
	}
}

// Match consumes the current token if it has type tt.
func (p *Parser) Match(tt tokens.TokenType) bool {
	if p.Curr.Type == tt {
		p.Advance()
		return true
	}
	return false
}

// MatchExpr is Match(tt, expression=e): pending comments are attached to e.
func (p *Parser) MatchExpr(tt tokens.TokenType, e *exp.Expr) bool {
	if p.Curr.Type == tt {
		p.Advance()
		p.AddComments(e)
		return true
	}
	return false
}

// Check reports whether the current token has type tt without consuming it
// (Python _match(tt, advance=False)).
func (p *Parser) Check(tt tokens.TokenType) bool { return p.Curr.Type == tt }

// MatchSet consumes the current token if its type is in set.
func (p *Parser) MatchSet(set tokens.TokenSet) bool {
	if set.Has(p.Curr.Type) {
		p.Advance()
		return true
	}
	return false
}

// CheckSet reports whether the current token type is in set.
func (p *Parser) CheckSet(set tokens.TokenSet) bool { return set.Has(p.Curr.Type) }

// MatchIn consumes the current token if its type is a key of m.
func MatchIn[V any](p *Parser, m map[tokens.TokenType]V) bool {
	if _, ok := m[p.Curr.Type]; ok {
		p.Advance()
		return true
	}
	return false
}

// CheckIn reports whether the current token type is a key of m.
func CheckIn[V any](p *Parser, m map[tokens.TokenType]V) bool {
	_, ok := m[p.Curr.Type]
	return ok
}

// MatchPair consumes two tokens if they have types a and b.
func (p *Parser) MatchPair(a, b tokens.TokenType) bool {
	if p.Curr.Type == a && p.Next.Type == b {
		p.AdvanceBy(2)
		return true
	}
	return false
}

// CheckPair reports whether the next two tokens have types a and b.
func (p *Parser) CheckPair(a, b tokens.TokenType) bool {
	return p.Curr.Type == a && p.Next.Type == b
}

func (p *Parser) currTextMatchable() bool { return !p.C.TextMatchExcludedTokens.Has(p.Curr.Type) }

// MatchTexts consumes the current token if its upper-cased text is one of texts.
func (p *Parser) MatchTexts(texts ...string) bool {
	if p.CheckTexts(texts...) {
		p.Advance()
		return true
	}
	return false
}

// CheckTexts is MatchTexts without advancing.
func (p *Parser) CheckTexts(texts ...string) bool {
	if !p.currTextMatchable() {
		return false
	}
	up := strings.ToUpper(p.Curr.Text)
	for _, t := range texts {
		if up == t {
			return true
		}
	}
	return false
}

// MatchTextsIn consumes the current token if its upper-cased text is in set.
func (p *Parser) MatchTextsIn(set StringSet) bool {
	if p.CheckTextsIn(set) {
		p.Advance()
		return true
	}
	return false
}

// CheckTextsIn is MatchTextsIn without advancing.
func (p *Parser) CheckTextsIn(set StringSet) bool {
	return p.currTextMatchable() && set[strings.ToUpper(p.Curr.Text)]
}

// MatchTextsKey consumes the current token if its upper-cased text is a key of m.
func MatchTextsKey[V any](p *Parser, m map[string]V) bool {
	if CheckTextsKey(p, m) {
		p.Advance()
		return true
	}
	return false
}

// CheckTextsKey is MatchTextsKey without advancing.
func CheckTextsKey[V any](p *Parser, m map[string]V) bool {
	if !p.currTextMatchable() {
		return false
	}
	_, ok := m[strings.ToUpper(p.Curr.Text)]
	return ok
}

// MatchTextSeq consumes a sequence of tokens whose upper-cased texts are texts.
func (p *Parser) MatchTextSeq(texts ...string) bool {
	index := p.Index
	for _, text := range texts {
		if p.currTextMatchable() && strings.ToUpper(p.Curr.Text) == text {
			p.Advance()
		} else {
			p.Retreat(index)
			return false
		}
	}
	return true
}

// CheckTextSeq is MatchTextSeq without advancing.
func (p *Parser) CheckTextSeq(texts ...string) bool {
	index := p.Index
	ok := p.MatchTextSeq(texts...)
	p.Retreat(index)
	return ok
}

// IsConnected reports whether the previous and current tokens are adjacent.
func (p *Parser) IsConnected() bool {
	return p.Prev.Ok() && p.Curr.Ok() && p.Prev.End+1 == p.Curr.Start
}

// FindSQL returns the SQL text spanning start..end tokens.
func (p *Parser) FindSQL(start, end *tokens.Token) string {
	a, b := start.Start, end.End+1
	if a < 0 {
		a = 0
	}
	if b > len(p.SQL) {
		b = len(p.SQL)
	}
	if a >= b {
		return ""
	}
	return string(p.SQL[a:b])
}

// parseBailout is used to unwind the parser on IMMEDIATE errors.
type parseBailout struct{ err *errs.ParseError }

// RaiseError records (or raises, with ErrorLevel IMMEDIATE) a parse error at the current
// token.
func (p *Parser) RaiseError(message string) { p.RaiseErrorAt(message, nil) }

// RaiseErrorAt records a parse error at the given token.
func (p *Parser) RaiseErrorAt(message string, token *tokens.Token) {
	if !token.Ok() {
		switch {
		case p.Curr.Ok():
			token = p.Curr
		case p.Prev.Ok():
			token = p.Prev
		default:
			token = tokens.StringToken("")
		}
	}
	formatted, startCtx, highlight, endCtx := errs.HighlightSQL(p.SQL, [][2]int{{token.Start, token.End}}, p.ErrorMessageContext)
	formattedMessage := fmt.Sprintf("%s. Line %d, Col: %d.\n  %s", message, token.Line, token.Col, formatted)
	err := errs.NewParseError(formattedMessage, errs.ErrorDetail{
		Description:  message,
		Line:         token.Line,
		Col:          token.Col,
		StartContext: startCtx,
		Highlight:    highlight,
		EndContext:   endCtx,
	})
	if p.ErrorLevel == errs.Immediate {
		panic(parseBailout{err})
	}
	p.Errors = append(p.Errors, err)
}

// ValidateExpression checks mandatory arguments of e (and the node limit).
func (p *Parser) ValidateExpression(e *exp.Expr, args []*exp.Expr) *exp.Expr {
	if p.MaxNodes > -1 {
		p.nodeCount++
		if p.nodeCount > p.MaxNodes {
			p.RaiseError(fmt.Sprintf("Maximum number of AST nodes (%d) exceeded", p.MaxNodes))
		}
	}
	if p.ErrorLevel != errs.Ignore {
		for _, msg := range e.ErrorMessages(args) {
			p.RaiseError(msg)
		}
	}
	return e
}

// TryParse runs fn with IMMEDIATE error level; on a parse error (or a nil result) it
// retreats and returns nil. With retreat it always restores the index.
func (p *Parser) TryParse(fn func() *exp.Expr, retreat bool) (result *exp.Expr) {
	index := p.Index
	level := p.ErrorLevel
	p.ErrorLevel = errs.Immediate
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(parseBailout); !ok {
				panic(r)
			}
			result = nil
		}
		if result == nil || retreat {
			p.Retreat(index)
		}
		p.ErrorLevel = level
	}()
	result = fn()
	return result
}

// Catch runs fn and returns the parse error it raised, if any (Python try/except ParseError).
func (p *Parser) Catch(fn func()) (err *errs.ParseError) {
	defer func() {
		if r := recover(); r != nil {
			if b, ok := r.(parseBailout); ok {
				err = b.err
				return
			}
			panic(r)
		}
	}()
	fn()
	return nil
}

// Raise raises an existing parse error (Python `raise e`).
func (p *Parser) Raise(err *errs.ParseError) { panic(parseBailout{err}) }

// Parse parses a token list into statements.
func (p *Parser) Parse(rawTokens []*tokens.Token, sql string) (out []*exp.Expr, err error) {
	defer recoverParse(&err)
	return p.parse(func(p *Parser) *exp.Expr { return p.ParseStatement() }, rawTokens, sql), nil
}

func recoverParse(err *error) {
	if r := recover(); r != nil {
		switch x := r.(type) {
		case parseBailout:
			*err = x.err
		case *errs.ParseError:
			*err = x
		case *errs.TokenError:
			*err = x
		case *errs.UnsupportedError:
			*err = x
		default:
			panic(r)
		}
	}
}

// ParseIntoTypes parses tokens into one of the given expression kinds (Python parse_into).
func (p *Parser) ParseIntoTypes(kinds []exp.Kind, rawTokens []*tokens.Token, sql string) (out []*exp.Expr, err error) {
	var errors []*errs.ParseError
	for _, kind := range kinds {
		parser, ok := p.C.ExpressionParsers[kind]
		if !ok {
			return nil, fmt.Errorf("No parser registered for %s", intoName(kind))
		}
		var res []*exp.Expr
		e := func() (err error) {
			defer recoverParse(&err)
			res = p.parse(func(p *Parser) *exp.Expr { return parser(p) }, rawTokens, sql)
			return nil
		}()
		if e == nil {
			return res, nil
		}
		pe, ok := e.(*errs.ParseError)
		if !ok {
			return nil, e
		}
		if len(pe.Errors) > 0 {
			pe.Errors[0].IntoExpression = intoName(kind)
		}
		errors = append(errors, pe)
	}
	what := sql
	if what == "" {
		what = fmt.Sprint(rawTokens)
	}
	return nil, &errs.ParseError{
		Message: fmt.Sprintf("Failed to parse '%s' into %s", what, intoNames(kinds)),
		Errors:  errs.MergeErrors(errors),
	}
}

func intoName(k exp.Kind) string {
	return k.ClassRepr()
}

func intoNames(kinds []exp.Kind) string {
	if len(kinds) == 1 {
		return intoName(kinds[0])
	}
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = intoName(k)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// CheckErrors logs or raises the collected errors according to the error level.
func (p *Parser) CheckErrors() {
	if p.ErrorLevel == errs.Warn {
		for _, e := range p.Errors {
			Logger.Error(e.Error())
		}
	} else if p.ErrorLevel == errs.Raise && len(p.Errors) > 0 {
		msgs := make([]string, len(p.Errors))
		for i, e := range p.Errors {
			msgs[i] = e.Error()
		}
		panic(parseBailout{&errs.ParseError{
			Message: errs.ConcatMessages(msgs, p.MaxErrors),
			Errors:  errs.MergeErrors(p.Errors),
		}})
	}
}

// Expression finalizes a new node: attaches pending comments and validates it.
func (p *Parser) Expression(e *exp.Expr) *exp.Expr {
	p.AddComments(e)
	if !e.Kind().IsPrimitive() {
		e = p.ValidateExpression(e, nil)
	}
	return e
}

// ExpressionTok is Expression with position information taken from token.
func (p *Parser) ExpressionTok(e *exp.Expr, token *tokens.Token) *exp.Expr {
	if token != nil {
		updatePositionsTok(e, token)
	}
	return p.Expression(e)
}

// ExpressionComments is Expression with explicit comments.
func (p *Parser) ExpressionComments(e *exp.Expr, comments []string) *exp.Expr {
	if len(comments) > 0 {
		e.AddComments(comments, false)
	} else {
		p.AddComments(e)
	}
	if !e.Kind().IsPrimitive() {
		e = p.ValidateExpression(e, nil)
	}
	return e
}

// Expr builds and finalizes a node of kind from alternating key/value pairs:
// p.Expr(exp.Not, "this", x) mirrors self.expression(exp.Not(this=x)).
func (p *Parser) Expr(kind exp.Kind, kv ...any) *exp.Expr {
	return p.Expression(exp.New(kind, kv...))
}

func updatePositionsTok(e *exp.Expr, t *tokens.Token) {
	e.UpdatePositions(t.Line, t.Col, t.Start, t.End)
}

func (p *Parser) parseBatchStatements(parseMethod func(p *Parser) *exp.Expr, sepFirstStatement bool) []*exp.Expr {
	var expressions []*exp.Expr
	if sepFirstStatement {
		p.Match(tokens.Begin)
		expressions = append(expressions, parseMethod(p))
	}
	n := len(p.chunks)
	for p.chunkIndex < n {
		p.advanceChunk()
		if p.Check(tokens.Else) {
			return expressions
		}
		if len(expressions) > 0 && !p.Next.Ok() && p.Match(tokens.End) {
			expressions = append(expressions, exp.New(exp.EndStatement))
			continue
		}
		expressions = append(expressions, parseMethod(p))
		if p.Index < p.TokensSize {
			p.RaiseError("Invalid expression / Unexpected token")
		}
		p.CheckErrors()
	}
	return expressions
}

func (p *Parser) parse(parseMethod func(p *Parser) *exp.Expr, rawTokens []*tokens.Token, sql string) []*exp.Expr {
	p.Reset()
	p.SQL = []rune(sql)
	total := len(rawTokens)
	chunks := [][]*tokens.Token{{}}
	for i, token := range rawTokens {
		if token.Type == tokens.Semicolon {
			if len(token.Comments) > 0 {
				chunks = append(chunks, []*tokens.Token{token})
			}
			if i < total-1 {
				chunks = append(chunks, []*tokens.Token{})
			}
		} else {
			chunks[len(chunks)-1] = append(chunks[len(chunks)-1], token)
		}
	}
	p.chunks = chunks
	return p.parseBatchStatements(parseMethod, false)
}

// ParseBatchStatements exposes _parse_batch_statements for dialects (procedural blocks).
func (p *Parser) ParseBatchStatements(parseMethod func(p *Parser) *exp.Expr, sepFirstStatement bool) []*exp.Expr {
	return p.parseBatchStatements(parseMethod, sepFirstStatement)
}

// ChunkIndex / Chunks expose the statement chunking state.
func (p *Parser) Chunks() [][]*tokens.Token { return p.chunks }

// WarnUnsupported logs that the current statement falls back to a Command.
func (p *Parser) WarnUnsupported() {
	if p.TokensSize <= 1 {
		return
	}
	sql := []rune(p.FindSQL(p.Tokens[0], p.Tokens[len(p.Tokens)-1]))
	if len(sql) > p.ErrorMessageContext {
		sql = sql[:p.ErrorMessageContext]
	}
	Logger.Warn(fmt.Sprintf("'%s' contains unsupported syntax. Falling back to parsing as a 'Command'.", string(sql)))
}

// ParseCSV parses a comma separated list (mirrors _parse_csv).
func (p *Parser) ParseCSV(parseMethod func() *exp.Expr) []*exp.Expr {
	return p.ParseCSVSep(parseMethod, tokens.Comma)
}

// ParseCSVSep is ParseCSV with a custom separator.
func (p *Parser) ParseCSVSep(parseMethod func() *exp.Expr, sep tokens.TokenType) []*exp.Expr {
	var items []*exp.Expr
	parseResult := parseMethod()
	if parseResult != nil {
		items = append(items, parseResult)
	}
	for p.Match(sep) {
		p.AddComments(parseResult)
		parseResult = parseMethod()
		if parseResult != nil {
			items = append(items, parseResult)
		}
	}
	return items
}

// ParseWrapped parses parseMethod surrounded by parentheses (mirrors _parse_wrapped).
func ParseWrapped[T any](p *Parser, parseMethod func() T, optional bool) T {
	wrapped := p.Match(tokens.LParen)
	if !wrapped && !optional {
		p.RaiseError("Expecting (")
	}
	result := parseMethod()
	if wrapped {
		p.MatchRParen(nil)
	}
	return result
}

// ParseWrappedExpr is ParseWrapped for expression-returning methods.
func (p *Parser) ParseWrappedExpr(parseMethod func() *exp.Expr, optional bool) *exp.Expr {
	return ParseWrapped(p, parseMethod, optional)
}

// ParseWrappedCSV parses a parenthesized comma separated list.
func (p *Parser) ParseWrappedCSV(parseMethod func() *exp.Expr, optional bool) []*exp.Expr {
	return ParseWrapped(p, func() []*exp.Expr { return p.ParseCSV(parseMethod) }, optional)
}

// ParseWrappedCSVSep is ParseWrappedCSV with a custom separator.
func (p *Parser) ParseWrappedCSVSep(parseMethod func() *exp.Expr, sep tokens.TokenType, optional bool) []*exp.Expr {
	return ParseWrapped(p, func() []*exp.Expr { return p.ParseCSVSep(parseMethod, sep) }, optional)
}

// MatchLParen expects a '('.
func (p *Parser) MatchLParen(e *exp.Expr) {
	if !p.MatchExpr(tokens.LParen, e) {
		p.RaiseError("Expecting (")
	}
}

// MatchRParen expects a ')'.
func (p *Parser) MatchRParen(e *exp.Expr) {
	if !p.MatchExpr(tokens.RParen, e) {
		p.RaiseError("Expecting )")
	}
}

// AdvanceAny consumes the current token unless it is reserved (mirrors _advance_any).
func (p *Parser) AdvanceAny(ignoreReserved bool) *tokens.Token {
	if p.Curr.Ok() && (ignoreReserved || !p.C.ReservedTokens.Has(p.Curr.Type)) {
		p.Advance()
		return p.Prev
	}
	return nil
}

// AndThen mirrors Python's `cond and value()`: false when cond is false, otherwise value().
func AndThen(cond bool, value func() *exp.Expr) any {
	if !cond {
		return false
	}
	if v := value(); v != nil {
		return v
	}
	return nil
}

// BoolOrNil mirrors `x or None` for booleans: true or nil.
func BoolOrNil(b bool) any {
	if b {
		return true
	}
	return nil
}

// TokText returns the token text or "" for nil/sentinel tokens.
func TokText(t *tokens.Token) string {
	if !t.Ok() {
		return ""
	}
	return t.Text
}

// KW is an insertion-ordered keyword-argument builder (Python **kwargs dicts).
type KW struct {
	keys []string
	vals map[string]any
}

// Set sets key (keeping the position of existing keys).
func (k *KW) Set(key string, v any) {
	if k.vals == nil {
		k.vals = map[string]any{}
	}
	if _, ok := k.vals[key]; !ok {
		k.keys = append(k.keys, key)
	}
	k.vals[key] = v
}

// Get returns the value of key.
func (k *KW) Get(key string) any { return k.vals[key] }

// Has reports whether key is set.
func (k *KW) Has(key string) bool { _, ok := k.vals[key]; return ok }

// KV returns the alternating key/value list.
func (k *KW) KV() []any {
	out := make([]any, 0, len(k.keys)*2)
	for _, key := range k.keys {
		out = append(out, key, k.vals[key])
	}
	return out
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
