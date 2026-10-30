// Package tokens implements the SQL tokenizer. It mirrors sqlglot/tokens.py and
// sqlglot/tokenizer_core.py. Dialects customise tokenization through a Config.
package tokens

import (
	"fmt"
	"maps"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/trie"
)

// TokenType is the kind of a token.
type TokenType uint16

// String returns the Python enum member name, e.g. "L_PAREN".
func (t TokenType) String() string {
	if int(t) < len(tokenTypeNames) {
		return tokenTypeNames[t]
	}
	return fmt.Sprintf("TokenType(%d)", t)
}

// Repr returns "TokenType.NAME".
func (t TokenType) Repr() string { return "TokenType." + t.String() }

// TokenTypeByName returns the token type with the given Python name.
func TokenTypeByName(name string) (TokenType, bool) {
	t, ok := tokenTypeByName[name]
	return t, ok
}

// TokenSet is a set of token types (bitset, copied by value).
type TokenSet [(int(numTokenTypes) + 63) / 64]uint64

// NewSet builds a TokenSet.
func NewSet(types ...TokenType) TokenSet {
	var s TokenSet
	for _, t := range types {
		s.Add(t)
	}
	return s
}

// Add inserts t.
func (s *TokenSet) Add(t TokenType) { s[t/64] |= 1 << (t % 64) }

// Remove deletes t.
func (s *TokenSet) Remove(t TokenType) { s[t/64] &^= 1 << (t % 64) }

// Has reports membership.
func (s *TokenSet) Has(t TokenType) bool { return s[t/64]&(1<<(t%64)) != 0 }

// Union returns s | others.
func (s TokenSet) Union(others ...TokenSet) TokenSet {
	for _, o := range others {
		for i := range s {
			s[i] |= o[i]
		}
	}
	return s
}

// Minus returns s - others.
func (s TokenSet) Minus(others ...TokenSet) TokenSet {
	for _, o := range others {
		for i := range s {
			s[i] &^= o[i]
		}
	}
	return s
}

// With returns s plus the given types.
func (s TokenSet) With(types ...TokenType) TokenSet {
	for _, t := range types {
		s.Add(t)
	}
	return s
}

// Without returns s minus the given types.
func (s TokenSet) Without(types ...TokenType) TokenSet {
	for _, t := range types {
		s.Remove(t)
	}
	return s
}

// Types lists the members in enum order.
func (s *TokenSet) Types() []TokenType {
	var out []TokenType
	for t := TokenType(0); t < numTokenTypes; t++ {
		if s.Has(t) {
			out = append(out, t)
		}
	}
	return out
}

// Token is a lexical token. Start and End are rune offsets into the SQL (End inclusive).
type Token struct {
	Type     TokenType
	Text     string
	Line     int
	Col      int
	Start    int
	End      int
	Comments []string
}

// NewToken creates a token with default positions.
func NewToken(t TokenType, text string) *Token {
	return &Token{Type: t, Text: text, Line: 1, Col: 1, Comments: []string{}}
}

// NumberToken returns a NUMBER token.
func NumberToken(n int) *Token { return NewToken(Number, fmt.Sprint(n)) }

// StringToken returns a STRING token.
func StringToken(s string) *Token { return NewToken(String, s) }

// IdentifierToken returns an IDENTIFIER token.
func IdentifierToken(s string) *Token { return NewToken(Identifier, s) }

// VarToken returns a VAR token.
func VarToken(s string) *Token { return NewToken(Var, s) }

// IsSentinel reports whether the token is the SENTINEL (Python's falsy token).
func (t *Token) IsSentinel() bool { return t == nil || t.Type == Sentinel }

func (t *Token) String() string {
	return fmt.Sprintf("<Token token_type: TokenType.%s, text: %s, line: %d, col: %d, start: %d, end: %d, comments: %s>",
		t.Type, t.Text, t.Line, t.Col, t.Start, t.End, pyList(t.Comments))
}

func pyList(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = "'" + strings.ReplaceAll(x, "'", "\\'") + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Delim is a (start, end) delimiter pair.
type Delim struct{ Start, End string }

// Q builds a symmetric quote.
func Q(s string) Delim { return Delim{s, s} }

// NumericEscape describes a numeric escape (base, min digits, max digits, max value).
type NumericEscape struct{ Base, MinDigits, MaxDigits, MaxValue int }

type formatString struct {
	end string
	tt  TokenType
}

// Config holds the per-dialect tokenizer settings (the Python Tokenizer class attributes),
// plus the dialect-level settings the tokenizer depends on.
type Config struct {
	SingleTokens                     map[rune]TokenType
	BitStrings                       []Delim
	ByteStrings                      []Delim
	HexStrings                       []Delim
	NationalRawStrings               []Delim
	RawStrings                       []Delim
	HeredocStrings                   []Delim
	UnicodeStrings                   []Delim
	Identifiers                      []Delim
	Quotes                           []Delim
	StringEscapes                    []string
	ByteStringEscapes                []string // nil: same as StringEscapes
	VarSingleTokens                  map[rune]bool
	IdentifierEscapes                []string
	HeredocTagIsIdentifier           bool
	HeredocStringAlternative         TokenType
	StringEscapesAllowedInRawStrings bool
	NumericEscapes                   map[string]NumericEscape
	DropUnknownEscapes               bool
	NumericEscapesAreBytes           bool
	LoneSurrogateReplacement         string
	NestedComments                   bool
	DashCommentRequiresBoundary      bool
	CommentsTerminateAtNewlineOnly   bool
	HintStart                        string
	TokensPrecedingHint              TokenSet
	Keywords                         map[string]TokenType
	Commands                         TokenSet
	CommandPrefixTokens              TokenSet
	NumericLiterals                  map[string]string
	NumbersCanHaveDecimals           bool
	// Comments lists comment delimiters; End == "" means a line comment.
	Comments []Delim
	// VarTokens is the JSONPathTokenizer.VAR_TOKENS set (only used by the JSON path parser).
	VarTokens TokenSet

	// dialect-level settings
	NumbersCanBeUnderscoreSeparated bool
	IdentifiersCanStartWithDigit    bool
	UnescapedSequences              map[string]string
	ByteStringsAreBytes             bool

	// computed by Finalize
	finalized         bool
	quotes            map[string]string
	identifiers       map[rune]string
	formatStrings     map[string]formatString
	stringEscapes     map[rune]bool
	byteStringEscapes map[rune]bool
	identifierEscapes map[rune]bool
	comments          map[string]*string
	keywordTrie       *trie.Node[rune]
	quoteRunes        map[rune]bool
}

// Clone returns a deep copy of the config (for dialect inheritance).
func (c *Config) Clone() *Config {
	n := *c
	n.SingleTokens = maps.Clone(c.SingleTokens)
	n.BitStrings = append([]Delim(nil), c.BitStrings...)
	n.ByteStrings = append([]Delim(nil), c.ByteStrings...)
	n.HexStrings = append([]Delim(nil), c.HexStrings...)
	n.NationalRawStrings = append([]Delim(nil), c.NationalRawStrings...)
	n.RawStrings = append([]Delim(nil), c.RawStrings...)
	n.HeredocStrings = append([]Delim(nil), c.HeredocStrings...)
	n.UnicodeStrings = append([]Delim(nil), c.UnicodeStrings...)
	n.Identifiers = append([]Delim(nil), c.Identifiers...)
	n.Quotes = append([]Delim(nil), c.Quotes...)
	n.StringEscapes = append([]string(nil), c.StringEscapes...)
	if c.ByteStringEscapes != nil {
		n.ByteStringEscapes = append([]string{}, c.ByteStringEscapes...)
	}
	n.VarSingleTokens = maps.Clone(c.VarSingleTokens)
	n.IdentifierEscapes = append([]string(nil), c.IdentifierEscapes...)
	n.NumericEscapes = maps.Clone(c.NumericEscapes)
	n.Keywords = maps.Clone(c.Keywords)
	n.NumericLiterals = maps.Clone(c.NumericLiterals)
	n.Comments = append([]Delim(nil), c.Comments...)
	n.UnescapedSequences = maps.Clone(c.UnescapedSequences)
	n.finalized = false
	return &n
}

func runeSet(xs []string) map[rune]bool {
	m := map[rune]bool{}
	for _, x := range xs {
		if r, size := utf8.DecodeRuneInString(x); size == len(x) {
			m[r] = true
		}
	}
	return m
}

// Finalize computes the derived lookup tables (mirrors _TokenizerBase.__init_subclass__).
// It must be called after all settings are in place and before tokenizing.
func (c *Config) Finalize() {
	c.quotes = map[string]string{}
	c.quoteRunes = map[rune]bool{}
	for _, q := range c.Quotes {
		c.quotes[q.Start] = q.End
		if r, size := utf8.DecodeRuneInString(q.Start); size == len(q.Start) {
			c.quoteRunes[r] = true
		}
	}
	c.identifiers = map[rune]string{}
	for _, q := range c.Identifiers {
		r, _ := utf8.DecodeRuneInString(q.Start)
		c.identifiers[r] = q.End
	}
	c.formatStrings = map[string]formatString{}
	for _, q := range c.Quotes {
		for _, p := range []string{"n", "N"} {
			c.formatStrings[p+q.Start] = formatString{q.End, NationalString}
		}
	}
	add := func(tt TokenType, qs []Delim) {
		for _, q := range qs {
			c.formatStrings[q.Start] = formatString{q.End, tt}
		}
	}
	add(BitString, c.BitStrings)
	add(ByteString, c.ByteStrings)
	add(HexString, c.HexStrings)
	add(NationalRawString, c.NationalRawStrings)
	add(RawString, c.RawStrings)
	add(HeredocString, c.HeredocStrings)
	add(UnicodeString, c.UnicodeStrings)

	c.stringEscapes = runeSet(c.StringEscapes)
	if c.ByteStringEscapes == nil {
		c.byteStringEscapes = runeSet(c.StringEscapes)
	} else {
		c.byteStringEscapes = runeSet(c.ByteStringEscapes)
	}
	c.identifierEscapes = runeSet(c.IdentifierEscapes)

	c.comments = map[string]*string{}
	for _, cm := range c.Comments {
		if cm.End == "" {
			c.comments[cm.Start] = nil
		} else {
			end := cm.End
			c.comments[cm.Start] = &end
		}
	}
	jinjaEnd := "#}"
	c.comments["{#"] = &jinjaEnd
	if _, ok := c.Keywords[c.HintStart]; ok {
		end := "*/"
		c.comments[c.HintStart] = &end
	}

	var keys []string
	for k := range c.Keywords {
		keys = append(keys, k)
	}
	for k := range c.comments {
		keys = append(keys, k)
	}
	for k := range c.quotes {
		keys = append(keys, k)
	}
	for k := range c.formatStrings {
		keys = append(keys, k)
	}
	c.keywordTrie = &trie.Node[rune]{}
	for _, k := range keys {
		hasSingle := strings.Contains(k, " ")
		if !hasSingle {
			for _, r := range k {
				if _, ok := c.SingleTokens[r]; ok {
					hasSingle = true
					break
				}
			}
		}
		if hasSingle {
			c.keywordTrie.Add([]rune(strings.ToUpper(k)))
		}
	}
	c.finalized = true
}

// KeywordTrie exposes the keyword trie (used by tests).
func (c *Config) KeywordTrie() *trie.Node[rune] { return c.keywordTrie }

// Quotes returns the computed quote map (start -> end).
func (c *Config) QuoteMap() map[string]string { return c.quotes }

// FormatStrings returns the computed format string map (start -> (end, token type)).
func (c *Config) FormatString(start string) (string, TokenType, bool) {
	f, ok := c.formatStrings[start]
	return f.end, f.tt, ok
}

// FormatStringStarts returns the starts of the format strings of a given token type.
func (c *Config) FormatStringStarts(tt TokenType) []string {
	var out []string
	for k, v := range c.formatStrings {
		if v.tt == tt {
			out = append(out, k)
		}
	}
	return out
}

// IdentifierDelims returns the computed identifier delimiters (start rune -> end).
func (c *Config) IdentifierDelims() map[rune]string { return c.identifiers }

// ---------------------------------------------------------------------------------------
// Tokenizer

// Tokenizer converts SQL text into tokens.
type Tokenizer struct {
	cfg *Config

	sql           []rune
	size          int
	tokens        []*Token
	start         int
	current       int
	line          int
	col           int
	comments      []string
	char          rune
	end           bool
	peek          rune
	prevTokenLine int
}

const noRune rune = -1

// New creates a tokenizer for the config (the config is finalized if needed).
func New(cfg *Config) *Tokenizer {
	if !cfg.finalized {
		cfg.Finalize()
	}
	return &Tokenizer{cfg: cfg}
}

func (t *Tokenizer) reset() {
	t.sql = nil
	t.size = 0
	t.tokens = nil
	t.start = 0
	t.current = 0
	t.line = 1
	t.col = 0
	t.comments = []string{}
	t.char = noRune
	t.end = false
	t.peek = noRune
	t.prevTokenLine = -1
}

// SQL returns the runes of the last tokenized input.
func (t *Tokenizer) SQL() []rune { return t.sql }

// Tokens returns the tokens produced so far (useful after an error).
func (t *Tokenizer) Tokens() []*Token { return t.tokens }

// Tokenize returns the tokens of sql.
func (t *Tokenizer) Tokenize(sql string) (toks []*Token, err error) {
	t.reset()
	t.sql = []rune(sql)
	t.size = len(t.sql)

	defer func() {
		if r := recover(); r != nil {
			start := max(t.current-50, 0)
			end := min(t.current+50, t.size-1)
			ctx := ""
			if start < end {
				ctx = string(t.sql[start:end])
			}
			if te, ok := r.(*errs.TokenError); ok && te.Message != "" && !strings.HasPrefix(te.Message, "Error tokenizing") {
				_ = te
			}
			err = &errs.TokenError{Message: fmt.Sprintf("Error tokenizing '%s'", ctx), Start: start, End: end, HasPos: true}
			toks = t.tokens
		}
	}()
	t.scan(false)
	return t.tokens, nil
}

func tokenErr(format string, args ...any) {
	panic(&errs.TokenError{Message: fmt.Sprintf(format, args...)})
}

func (t *Tokenizer) scan(checkSemicolon bool) {
	for t.size > 0 && !t.end {
		current := t.current
		for current < t.size {
			c := t.sql[current]
			if c == ' ' || c == '\t' {
				current++
			} else {
				break
			}
		}
		offset := 1
		if current > t.current {
			offset = current - t.current
		}
		t.start = current
		t.advance(offset, false)

		if !IsSpace(t.char) {
			if t.char >= '0' && t.char <= '9' {
				t.scanNumber()
			} else if end, ok := t.cfg.identifiers[t.char]; ok {
				t.scanIdentifier(end)
			} else {
				t.scanKeywords()
			}
		}
		if checkSemicolon && t.peek == ';' {
			break
		}
	}
	if len(t.tokens) > 0 && len(t.comments) > 0 {
		last := t.tokens[len(t.tokens)-1]
		last.Comments = append(last.Comments, t.comments...)
	}
}

func (t *Tokenizer) chars(size int) string {
	if size == 1 {
		if t.char == noRune {
			return ""
		}
		return string(t.char)
	}
	start := t.current - 1
	end := start + size
	if end <= t.size && start >= 0 {
		return string(t.sql[start:end])
	}
	return ""
}

func (t *Tokenizer) advance(i int, alnum bool) {
	c := t.char
	if c == '\n' || c == '\r' {
		if !(c == '\r' && t.peek == '\n') {
			t.col = i
			t.line++
		}
	} else {
		t.col += i
	}
	t.current += i
	t.end = t.current >= t.size
	idx := t.current - 1
	if idx < 0 {
		idx += t.size // Python negative indexing
	}
	if idx < 0 || idx >= t.size {
		panic(&errs.TokenError{Message: "string index out of range"})
	}
	t.char = t.sql[idx]
	if t.end {
		t.peek = noRune
	} else {
		t.peek = t.sql[t.current]
	}

	if alnum && isAlnum(t.char) {
		col, cur, end, peek := t.col, t.current, t.end, t.peek
		for isAlnum(peek) {
			col++
			cur++
			end = cur >= t.size
			if end {
				peek = noRune
			} else {
				peek = t.sql[cur]
			}
		}
		t.col, t.current, t.end, t.peek = col, cur, end, peek
		t.char = t.sql[cur-1]
	}
}

func (t *Tokenizer) text() string { return string(t.sql[t.start:t.current]) }

func (t *Tokenizer) add(tt TokenType, text *string) {
	t.prevTokenLine = t.line

	if len(t.comments) > 0 && tt == Semicolon && len(t.tokens) > 0 {
		last := t.tokens[len(t.tokens)-1]
		last.Comments = append(last.Comments, t.comments...)
		t.comments = []string{}
	}
	var txt string
	if text == nil {
		txt = string(t.sql[t.start:t.current])
	} else {
		txt = *text
	}
	t.tokens = append(t.tokens, &Token{
		Type: tt, Text: txt, Line: t.line, Col: t.col, Start: t.start, End: t.current - 1,
		Comments: t.comments,
	})
	t.comments = []string{}

	if t.cfg.Commands.Has(tt) && t.peek != ';' &&
		(len(t.tokens) == 1 || t.cfg.CommandPrefixTokens.Has(t.tokens[len(t.tokens)-2].Type)) {
		start := t.current
		n := len(t.tokens)
		t.scan(true)
		t.tokens = t.tokens[:n]
		end := t.current
		if end > t.size {
			end = t.size
		}
		s := ""
		if start < end {
			s = strings.TrimFunc(string(t.sql[start:end]), IsSpace)
		}
		if s != "" {
			t.add(String, &s)
		}
	}
}

func asciiUpper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 32
	}
	return r
}

func (t *Tokenizer) scanKeywords() {
	size := 0
	var word []rune
	hasWord := false
	chars := []rune{t.char}
	c := t.char
	prevSpace := false
	skip := false
	node := t.cfg.keywordTrie
	_, singleToken := t.cfg.SingleTokens[c]

	for len(chars) > 0 {
		if !skip {
			sub, ok := node.Children[asciiUpper(c)]
			if !ok {
				break
			}
			node = sub
			if node.End {
				word = append(word[:0], chars...)
				hasWord = true
			}
		}
		end := t.current + size
		size++
		if end < t.size {
			c = t.sql[end]
			if _, ok := t.cfg.SingleTokens[c]; ok {
				singleToken = true
			}
			isSpace := IsSpace(c)
			if !isSpace || !prevSpace {
				if isSpace {
					c = ' '
				}
				chars = append(chars, c)
				prevSpace = isSpace
				skip = false
			} else {
				skip = true
			}
		} else {
			c = noRune
			break
		}
	}

	if hasWord {
		w := string(word)
		if t.scanString(w) {
			return
		}
		if t.scanComment(w) {
			return
		}
		if prevSpace || singleToken || c == noRune {
			w = strings.ToUpper(w)
			if tt, ok := t.cfg.Keywords[w]; ok {
				t.advance(size-1, false)
				t.add(tt, &w)
				return
			}
		}
	}

	if tt, ok := t.cfg.SingleTokens[t.char]; ok {
		s := string(t.char)
		t.add(tt, &s)
		return
	}
	t.scanVar()
}

func (t *Tokenizer) scanComment(commentStart string) bool {
	commentEnd, ok := t.cfg.comments[commentStart]
	if !ok {
		return false
	}
	if commentStart == "--" && t.cfg.DashCommentRequiresBoundary {
		bi := t.current + 1
		if bi < t.size {
			bc := t.sql[bi]
			if !(IsSpace(bc) || bc < 32 || bc == 127) {
				return false
			}
		}
	}

	commentStartLine := t.line
	startSize := utf8.RuneCountInString(commentStart)

	if commentEnd != nil {
		t.advance(startSize, false)
		count := 1
		endSize := utf8.RuneCountInString(*commentEnd)
		for !t.end {
			if t.chars(endSize) == *commentEnd {
				count--
				if count == 0 {
					break
				}
			}
			t.advance(1, true)
			if t.cfg.NestedComments && !t.end && t.chars(endSize) == commentStart {
				t.advance(startSize, false)
				count++
			}
		}
		txt := []rune(t.text())
		hi := len(txt) - endSize + 1
		if hi < startSize {
			hi = startSize
		}
		if hi > len(txt) {
			hi = len(txt)
		}
		t.comments = append(t.comments, string(txt[startSize:hi]))
		t.advance(endSize-1, false)
	} else {
		peek := t.peek
		terminatesAtCR := !t.cfg.CommentsTerminateAtNewlineOnly
		for !t.end && peek != '\n' && (!terminatesAtCR || peek != '\r') {
			t.advance(1, true)
			peek = t.peek
		}
		txt := []rune(t.text())
		if startSize <= len(txt) {
			t.comments = append(t.comments, string(txt[startSize:]))
		} else {
			t.comments = append(t.comments, "")
		}
	}

	if commentStart == t.cfg.HintStart && len(t.tokens) > 0 && t.cfg.TokensPrecedingHint.Has(t.tokens[len(t.tokens)-1].Type) {
		t.add(Hint, nil)
	}

	if commentStartLine == t.prevTokenLine {
		last := t.tokens[len(t.tokens)-1]
		last.Comments = append(last.Comments, t.comments...)
		t.comments = []string{}
		t.prevTokenLine = t.line
	}
	return true
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func (t *Tokenizer) scanNumber() {
	if t.char == '0' {
		peek := asciiUpper(t.peek)
		if peek == 'B' && len(t.cfg.BitStrings) > 0 {
			t.scanBits()
			return
		} else if peek == 'X' {
			if len(t.cfg.HexStrings) > 0 {
				t.scanHex()
			} else {
				t.add(Number, nil)
			}
			return
		}
	}

	decimal := false
	scientific := 0
	isUnderscoreSeparated := false
	numberText := ""
	numericLiteral := ""
	var numericType TokenType
	hasNumericType := false

	for {
		if isDigit(t.peek) {
			end := t.current + 1
			for end < t.size && isDigit(t.sql[end]) {
				end++
			}
			t.advance(end-t.current, false)
		} else if t.peek == '.' && !decimal {
			if (len(t.tokens) > 0 && t.tokens[len(t.tokens)-1].Type == Parameter) || !t.cfg.NumbersCanHaveDecimals {
				break
			}
			decimal = true
			t.advance(1, false)
		} else if (t.peek == '-' || t.peek == '+') && scientific == 1 {
			if t.current+1 < t.size && isDigit(t.sql[t.current+1]) {
				scientific++
				t.advance(1, false)
			} else {
				break
			}
		} else if asciiUpper(t.peek) == 'E' && scientific == 0 {
			scientific++
			t.advance(1, false)
		} else if t.peek == '_' && t.cfg.NumbersCanBeUnderscoreSeparated {
			isUnderscoreSeparated = true
			t.advance(1, false)
		} else if isIdentifierStart(t.peek) {
			numberText = t.text()
			for t.peek != noRune && !IsSpace(t.peek) {
				if _, ok := t.cfg.SingleTokens[t.peek]; ok {
					break
				}
				numericLiteral += string(t.peek)
				t.advance(1, false)
			}
			if lit, ok := t.cfg.NumericLiterals[strings.ToUpper(numericLiteral)]; ok {
				if tt, ok := t.cfg.Keywords[lit]; ok {
					numericType = tt
					hasNumericType = true
				}
			}
			if hasNumericType {
				break
			} else if t.cfg.IdentifiersCanStartWithDigit {
				t.add(Var, nil)
				return
			}
			t.advance(-utf8.RuneCountInString(numericLiteral), false)
			break
		} else {
			break
		}
	}

	if numberText == "" {
		numberText = string(t.sql[t.start:t.current])
	}
	if isUnderscoreSeparated {
		numberText = strings.ReplaceAll(numberText, "_", "")
	}
	t.add(Number, &numberText)
	if hasNumericType {
		dc := "::"
		t.add(Dcolon, &dc)
		t.add(numericType, &numericLiteral)
	}
}

func validInt(s string, base int) bool {
	// mirrors int(value, base) where value keeps its 0b/0x prefix
	if len(s) < 3 {
		return false
	}
	body := s[2:]
	body = strings.TrimPrefix(body, "_")
	if body == "" || strings.HasSuffix(body, "_") || strings.Contains(body, "__") {
		return false
	}
	for _, r := range body {
		if r == '_' {
			continue
		}
		v := digitValue(r)
		if v < 0 || v >= base {
			return false
		}
	}
	return true
}

func digitValue(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return int(r - '0')
	case r >= 'a' && r <= 'f':
		return int(r-'a') + 10
	case r >= 'A' && r <= 'F':
		return int(r-'A') + 10
	}
	return 16
}

func (t *Tokenizer) scanBits() {
	t.advance(1, false)
	value := t.extractValue()
	if validInt(value, 2) {
		s := value[2:]
		t.add(BitString, &s)
	} else {
		t.add(Identifier, nil)
	}
}

func (t *Tokenizer) scanHex() {
	t.advance(1, false)
	value := t.extractValue()
	if validInt(value, 16) {
		s := value[2:]
		t.add(HexString, &s)
	} else {
		t.add(Identifier, nil)
	}
}

func (t *Tokenizer) extractValue() string {
	for {
		c := t.peek
		if c != noRune && !IsSpace(c) {
			if _, ok := t.cfg.SingleTokens[c]; !ok {
				t.advance(1, true)
				continue
			}
		}
		break
	}
	return t.text()
}

func (t *Tokenizer) scanString(start string) bool {
	base := 0
	tt := String
	var end string

	if e, ok := t.cfg.quotes[start]; ok {
		end = e
	} else if fs, ok := t.cfg.formatStrings[start]; ok {
		end, tt = fs.end, fs.tt
		switch tt {
		case HexString:
			base = 16
		case BitString:
			base = 2
		case HeredocString:
			t.advance(1, false)
			tag := ""
			if string(t.char) != end {
				tag = t.extractString(end, nil, true, !t.cfg.HeredocTagIsIdentifier, false)
			}
			if tag != "" && t.cfg.HeredocTagIsIdentifier && (t.end || isAllDigits(tag) || strings.IndexFunc(tag, IsSpace) >= 0) {
				if !t.end {
					t.advance(-1, false)
				}
				t.advance(-utf8.RuneCountInString(tag), false)
				t.add(t.cfg.HeredocStringAlternative, nil)
				return true
			}
			end = start + tag + end
		}
	} else {
		return false
	}

	t.advance(utf8.RuneCountInString(start), false)
	escapes := t.cfg.stringEscapes
	if tt == ByteString {
		escapes = t.cfg.byteStringEscapes
	}
	raw := tt == RawString || tt == NationalRawString || tt == HeredocString
	text := t.extractString(end, escapes, raw, true, tt == ByteString && t.cfg.ByteStringsAreBytes)

	if base != 0 && text != "" {
		if !validDigits(text, base) {
			panic(&errs.TokenError{Message: fmt.Sprintf("Numeric string contains invalid characters from %d:%d", t.line, t.start)})
		}
	}
	t.add(tt, &text)
	return true
}

func validDigits(s string, base int) bool {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "+-")
	if base == 16 {
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			s = s[2:]
		}
	} else if base == 2 {
		if strings.HasPrefix(s, "0b") || strings.HasPrefix(s, "0B") {
			s = s[2:]
		}
	}
	s = strings.TrimPrefix(s, "_")
	if s == "" || strings.HasSuffix(s, "_") || strings.Contains(s, "__") {
		return false
	}
	for _, r := range s {
		if r == '_' {
			continue
		}
		if v := digitValue(r); v < 0 || v >= base {
			return false
		}
	}
	return true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func (t *Tokenizer) scanIdentifier(identifierEnd string) {
	t.advance(1, false)
	escapes := map[rune]bool{}
	for r := range t.cfg.identifierEscapes {
		escapes[r] = true
	}
	if r, size := utf8.DecodeRuneInString(identifierEnd); size == len(identifierEnd) {
		escapes[r] = true
	}
	text := t.extractString(identifierEnd, escapes, false, true, false)
	t.add(Identifier, &text)
}

func (t *Tokenizer) scanVar() {
	for {
		peek := t.peek
		if peek == noRune || IsSpace(peek) {
			break
		}
		if !t.cfg.VarSingleTokens[peek] {
			if _, ok := t.cfg.SingleTokens[peek]; ok {
				break
			}
		}
		t.advance(1, true)
	}
	if len(t.tokens) > 0 && t.tokens[len(t.tokens)-1].Type == Parameter {
		t.add(Var, nil)
		return
	}
	if tt, ok := t.cfg.Keywords[strings.ToUpper(string(t.sql[t.start:t.current]))]; ok {
		t.add(tt, nil)
	} else {
		t.add(Var, nil)
	}
}

func (t *Tokenizer) readNumericEscape(start, base, minDigits, maxDigits, maxValue int) (int, int) {
	value := 0
	end := start
	limit := min(start+maxDigits, t.size)
	for end < limit {
		d := digitValue(t.sql[end])
		if d >= base || value*base+d > maxValue {
			break
		}
		value = value*base + d
		end++
	}
	if end-start >= minDigits {
		return value, end
	}
	return -1, end
}

func (t *Tokenizer) sqlSlice(a, b int) string {
	if a < 0 {
		a = 0
	}
	if b > t.size {
		b = t.size
	}
	if a >= b {
		return ""
	}
	return string(t.sql[a:b])
}

func isOctal(s string) bool { return len(s) == 1 && s[0] >= '0' && s[0] <= '7' }

func (t *Tokenizer) readByteEscape(pos int) (int, int) {
	peek := t.sqlSlice(pos+1, pos+2)
	octal := isOctal(peek)
	key := peek
	if octal {
		key = "0"
	}
	spec, ok := t.cfg.NumericEscapes[key]
	if !ok || (spec.Base != 8 && spec.MaxValue > 0xFF) {
		return -1, pos
	}
	start := pos + 2
	if octal {
		start = pos + 1
	}
	value, end := t.readNumericEscape(start, spec.Base, spec.MinDigits, spec.MaxDigits, spec.MaxValue)
	if value >= 0 {
		value &= 0xFF
	}
	return value, end
}

func (t *Tokenizer) scanNumericEscape() string {
	start := t.current
	peek := ""
	if t.peek != noRune {
		peek = string(t.peek)
	}
	octal := isOctal(peek)
	key := peek
	if octal {
		key = "0"
	}
	spec, ok := t.cfg.NumericEscapes[key]
	if !ok {
		return ""
	}
	digitStart := start + 1
	if octal {
		digitStart = start
	}
	value, end := t.readNumericEscape(digitStart, spec.Base, spec.MinDigits, spec.MaxDigits, spec.MaxValue)
	isByte := t.cfg.NumericEscapesAreBytes && (spec.Base == 8 || spec.MaxValue <= 0xFF)
	if isByte && value >= 0 {
		value &= 0xFF
	}
	if value >= 0xD800 && value <= 0xDFFF {
		low, lowEnd := -1, end
		if value <= 0xDBFF && t.sqlSlice(end, end+2) == "\\"+peek {
			low, lowEnd = t.readNumericEscape(end+2, spec.Base, spec.MinDigits, spec.MaxDigits, spec.MaxValue)
		}
		if low >= 0xDC00 && low <= 0xDFFF {
			value = 0x10000 + ((value - 0xD800) << 10) + (low - 0xDC00)
			end = lowEnd
		} else if t.cfg.LoneSurrogateReplacement != "" && end < t.size {
			t.advance(end-start+1, false)
			return t.cfg.LoneSurrogateReplacement
		} else {
			value = -1
		}
	}
	if isByte && value >= 0xC2 && value <= 0xF4 {
		run := []byte{byte(value)}
		runEnd := end
		byteCount := 4
		if value < 0xE0 {
			byteCount = 2
		} else if value < 0xF0 {
			byteCount = 3
		}
		for len(run) < byteCount && t.sqlSlice(runEnd, runEnd+1) == "\\" {
			b, be := t.readByteEscape(runEnd)
			if !(b >= 0x80 && b <= 0xBF) {
				break
			}
			run = append(run, byte(b))
			runEnd = be
		}
		decoded := ""
		if utf8.Valid(run) {
			decoded = string(run)
		}
		if decoded != "" && runEnd < t.size {
			t.advance(runEnd-start+1, false)
			return decoded
		}
	}
	if value >= 0 && end < t.size {
		t.advance(end-start+1, false)
		return string(rune(value))
	}
	return ""
}

func latin1(s string) string {
	b := []byte(s)
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func (t *Tokenizer) find(r []rune, from, to int) int {
	if to > t.size {
		to = t.size
	}
	if len(r) == 1 {
		for i := from; i < to; i++ {
			if t.sql[i] == r[0] {
				return i
			}
		}
		return -1
	}
	for i := from; i+len(r) <= to; i++ {
		match := true
		for j := range r {
			if t.sql[i+j] != r[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func (t *Tokenizer) extractString(delimiter string, escapes map[rune]bool, rawString, raiseUnmatched, bytesLiteral bool) string {
	var text strings.Builder
	delim := []rune(delimiter)
	delimSize := len(delim)
	if escapes == nil {
		escapes = t.cfg.stringEscapes
	}
	unescaped := t.cfg.UnescapedSequences
	numericEscapes := t.cfg.NumericEscapes
	sql := t.sql

	if delimSize == 1 {
		pos := t.current - 1
		end := t.find(delim, pos, t.size)
		if end != -1 &&
			(end+1 >= t.size || sql[end+1] != delim[0] || !escapes[delim[0]]) &&
			(!(len(unescaped) > 0 || escapes['\\']) || t.find([]rune{'\\'}, pos, end) == -1) {
			newlines := 0
			lastNL := -1
			for i := pos; i < end; i++ {
				if sql[i] == '\n' {
					newlines++
					lastNL = i
				}
			}
			if newlines > 0 {
				t.line += newlines
				t.col = end - lastNL
			} else {
				t.col += end - pos
			}
			t.current = end + 1
			t.end = t.current >= t.size
			t.char = sql[end]
			if t.end {
				t.peek = noRune
			} else {
				t.peek = sql[t.current]
			}
			s := string(sql[pos:end])
			if bytesLiteral && !isASCII(s) {
				s = latin1(s)
			}
			return s
		}
	}

	for {
		backslashEscape := !rawString && t.char == '\\' && escapes['\\']

		if backslashEscape && len(numericEscapes) > 0 {
			if decoded := t.scanNumericEscape(); decoded != "" {
				text.WriteString(decoded)
				continue
			}
		}

		if !rawString && len(unescaped) > 0 && escapes[t.char] && t.current+1 < t.size {
			key := string(t.char)
			if t.peek != noRune {
				key += string(t.peek)
			}
			if seq, ok := unescaped[key]; ok {
				if t.peek == '\n' || t.peek == '\r' {
					t.advance(1, false)
					t.advance(1, false)
				} else {
					t.advance(2, false)
				}
				text.WriteString(seq)
				continue
			}
		}

		if backslashEscape && t.cfg.DropUnknownEscapes && t.current+1 < t.size {
			peek := t.peek
			t.advance(1, false)
			t.advance(1, false)
			if bytesLiteral {
				text.WriteString(latin1(string(peek)))
			} else {
				text.WriteRune(peek)
			}
			continue
		}

		escapedDelimiter := (delimSize == 1 && t.peek == delim[0]) ||
			(delimSize > 1 && t.peek == delim[0] && t.cfg.quoteRunes[t.peek])

		if (t.cfg.StringEscapesAllowedInRawStrings || !rawString) &&
			escapes[t.char] &&
			(escapedDelimiter || escapes[t.peek]) &&
			(!t.cfg.quoteRunes[t.char] || t.char == t.peek) {
			if escapedDelimiter {
				if !rawString {
					text.WriteRune(t.peek)
				} else {
					text.WriteRune(t.char)
					text.WriteRune(t.peek)
				}
			} else {
				text.WriteRune(t.char)
				text.WriteRune(t.peek)
			}
			if t.current+1 < t.size {
				t.advance(2, false)
			} else {
				panic(&errs.TokenError{Message: fmt.Sprintf("Missing %s from %d:%d", delimiter, t.line, t.current)})
			}
		} else {
			if t.chars(delimSize) == delimiter {
				if delimSize > 1 {
					t.advance(delimSize-1, false)
				}
				break
			}
			if t.end {
				if !raiseUnmatched {
					if t.char != noRune {
						text.WriteRune(t.char)
					}
					return text.String()
				}
				panic(&errs.TokenError{Message: fmt.Sprintf("Missing %s from %d:%d", delimiter, t.line, t.start)})
			}
			current := t.current - 1
			t.advance(1, true)
			chunk := string(sql[current : t.current-1])
			if bytesLiteral && !isASCII(chunk) {
				chunk = latin1(chunk)
			}
			text.WriteString(chunk)
		}
	}
	return text.String()
}

// IsSpace mirrors Python's str.isspace for a single character.
func IsSpace(r rune) bool {
	if r == noRune {
		return false
	}
	if r >= 0x1c && r <= 0x1f {
		return true
	}
	return unicode.IsSpace(r)
}

func isAlnum(r rune) bool {
	if r == noRune {
		return false
	}
	if r < 0x80 {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

func isIdentifierStart(r rune) bool {
	if r == noRune {
		return false
	}
	return r == '_' || unicode.IsLetter(r) || unicode.Is(unicode.Nl, r)
}

// Ok reports whether the token is a real token (Python truthiness: not the SENTINEL).
func (t *Token) Ok() bool { return t != nil && t.Type != Sentinel }
