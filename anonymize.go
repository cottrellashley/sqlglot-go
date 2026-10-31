package sqlglot

// Port of sqlglot/anonymize.py: token-level anonymization of SQL.

import (
	"errors"
	"math/big"
	"sort"
	"strings"
	"unicode"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

const anonymizeAlphabet = "abcdefghijklmnopqrstuvwxyz"

// AnonymizedTypes are the token types whose text Anonymize replaces.
var AnonymizedTypes = tokens.NewSet(
	tokens.BitString, tokens.ByteString, tokens.HexString, tokens.HeredocString, tokens.Identifier,
	tokens.NationalRawString, tokens.NationalString, tokens.Number, tokens.RawString, tokens.String,
	tokens.UnicodeString, tokens.Var,
)

func isQuotedType(t tokens.TokenType) bool {
	return AnonymizedTypes.Has(t) && t != tokens.Number && t != tokens.Var
}

func isRewrittenType(t tokens.TokenType) bool { return t == tokens.Hint || t == tokens.Unknown }

// Anonymize replaces sensitive tokens (identifiers, strings, numbers) with fixed-width,
// length-preserving, consistent aliases, and blanks out comments and hint bodies.
//
// sqlOrTokens is a SQL string, tokenized with the dialect first, or its token list. Any
// un-tokenized remainder of a string (e.g. an unterminated literal) is appended as a blanked
// UNKNOWN token. The tokens are modified in place and returned.
func Anonymize(sqlOrTokens any, dialect any) ([]*tokens.Token, error) {
	d, err := GetDialect(dialect)
	if err != nil {
		return nil, err
	}
	var toks []*tokens.Token
	var sql []rune
	isSQL, errored := false, false
	switch x := sqlOrTokens.(type) {
	case string:
		isSQL = true
		sql = []rune(x)
		tokenizer := d.NewTokenizer()
		toks, err = tokenizer.Tokenize(x)
		if err != nil {
			var te *errs.TokenError
			if !errors.As(err, &te) {
				return nil, err
			}
			toks = tokenizer.Tokens()
			errored = true
		}
	case []*tokens.Token:
		toks = x
	default:
		return nil, errs.Valuef("anonymize expects a SQL string or a token list, got %T", sqlOrTokens)
	}

	cfg := d.Tokenizer
	comments := cfg.CommentDelims()
	hintStart := cfg.HintStart
	hintEnd := ""
	if end := comments[hintStart]; end != nil {
		hintEnd = *end
	}
	nested := cfg.NestedComments

	type seenKey struct {
		number bool
		text   string
	}
	seen := map[seenKey]string{}
	counter := 0
	for i, token := range toks {
		for j, c := range token.Comments {
			token.Comments[j] = blankText(c)
		}
		if token.Type == tokens.Hint {
			// A hint's text is the whole /*+ ... */ comment, so its body is blanked as well
			text := []rune(token.Text)
			blanked, stop := blankComment(text, 0, []rune(hintStart), []rune(hintEnd), nested)
			token.Text = blanked + blankText(string(text[stop:]))
			continue
		}
		if token.Type == tokens.Var && i+1 < len(toks) && toks[i+1].Type == tokens.LParen {
			// A function name can live in either registry, e.g. JSON_OBJECT is only in
			// FUNCTION_PARSERS
			name := strings.ToUpper(token.Text)
			if _, ok := d.Parser.Functions[name]; ok {
				continue
			}
			if _, ok := d.Parser.FunctionParsers[name]; ok {
				continue
			}
		}
		if !AnonymizedTypes.Has(token.Type) || token.Text == "" {
			continue
		}
		isNumber := token.Type == tokens.Number
		key := seenKey{isNumber, token.Text}
		if _, ok := seen[key]; !ok {
			if isNumber {
				seen[key] = numberAlias(counter, token.Text)
			} else {
				seen[key] = textAlias(counter, token.Text)
			}
			counter++
		}
		token.Text = seen[key]
	}

	if isSQL && errored {
		start := 0
		if len(toks) > 0 {
			start = toks[len(toks)-1].End + 1
		}
		length := len(sql)
		for start < length && pyIsSpace(sql[start]) {
			start++
		}
		if start < length {
			// The first two characters are kept so that the delimiter the tokenizer choked on
			// is still visible, e.g. 'u, /*, $$, `u
			head := string(sql[start:min(start+2, length)])
			text := head + strings.Repeat(".", max(0, length-start-2))
			line := 1 + strings.Count(string(sql[:start]), "\n")
			lastNewline := -1
			for k := start - 1; k >= 0; k-- {
				if sql[k] == '\n' {
					lastNewline = k
					break
				}
			}
			unknown := tokens.NewToken(tokens.Unknown, text)
			unknown.Start, unknown.End, unknown.Line, unknown.Col = start, length-1, line, start-lastNewline
			toks = append(toks, unknown)
		}
	}
	return toks, nil
}

// Render recreates the (anonymized) SQL string from the original sql and the token positions.
//
// Every token is rendered over its own source span, so the result is always as long as sql:
// a token that wasn't anonymized is emitted verbatim, and an anonymized one has its alias
// fitted between the quotes of its span. The gaps between tokens hold only whitespace and
// comments, so they're redacted rather than reconstructed: comment markers and whitespace
// survive, everything else is blanked.
func Render(sql string, toks []*tokens.Token, dialect any) (string, error) {
	d, err := GetDialect(dialect)
	if err != nil {
		return "", err
	}
	cfg := d.Tokenizer
	type delim struct{ start, end []rune }
	var comments []delim
	for start, end := range cfg.CommentDelims() {
		e := ""
		if end != nil {
			e = *end
		}
		comments = append(comments, delim{[]rune(start), []rune(e)})
	}
	sort.Slice(comments, func(i, j int) bool {
		if len(comments[i].start) != len(comments[j].start) {
			return len(comments[i].start) > len(comments[j].start)
		}
		return string(comments[i].start) < string(comments[j].start)
	})
	quoteMap := cfg.QuoteDelims()
	for k, v := range cfg.FormatStringDelims() {
		quoteMap[k] = v
	}
	for k, v := range cfg.IdentifierDelims() {
		quoteMap[string(k)] = v
	}
	var quotes []delim
	for k, v := range quoteMap {
		quotes = append(quotes, delim{[]rune(k), []rune(v)})
	}
	sort.Slice(quotes, func(i, j int) bool {
		a, b := quotes[i], quotes[j]
		if len(a.start) != len(b.start) {
			return len(a.start) > len(b.start)
		}
		if len(a.end) != len(b.end) {
			return len(a.end) > len(b.end)
		}
		return string(a.start) < string(b.start)
	})
	nested := cfg.NestedComments

	redact := func(s []rune) string {
		if len(s) == 0 || isSpaceRunes(s) {
			return string(s)
		}
		var b strings.Builder
		for i := 0; i < len(s); {
			matched := false
			for _, c := range comments {
				if hasPrefixRunes(s[i:], c.start) {
					text, next := blankComment(s, i, c.start, c.end, nested)
					b.WriteString(text)
					i = next
					matched = true
					break
				}
			}
			if !matched {
				if pyIsSpace(s[i]) {
					b.WriteRune(s[i])
				} else {
					b.WriteByte('.')
				}
				i++
			}
		}
		return b.String()
	}

	fit := func(span []rune, alias string, tt tokens.TokenType) string {
		start, end := 0, 0
		if isQuotedType(tt) {
			for _, q := range quotes {
				if len(span) >= len(q.start)+len(q.end) && hasPrefixRunes(span, q.start) && hasSuffixRunes(span, q.end) {
					start, end = len(q.start), len(q.end)
					if tt == tokens.HeredocString {
						// A heredoc's tag is part of its delimiter, e.g. $tag$body$tag$
						openEnd := indexRunes(span, q.end, start)
						closeStart := lastIndexRunes(span[:len(span)-end], q.start)
						if start <= openEnd && openEnd < closeStart {
							start, end = openEnd+len(q.end), len(span)-closeStart
						}
					}
					break
				}
			}
		}
		width := len(span) - start - end
		pad := "a"
		if tt == tokens.Number {
			pad = "0"
		}
		a := []rune(alias)
		if len(a) > width {
			a = a[:max(width, 0)]
		}
		return string(span[:start]) + strings.Repeat(pad, max(0, width-len(a))) + string(a) + string(span[len(span)-end:])
	}

	src := []rune(sql)
	var b strings.Builder
	prev := 0
	for _, token := range toks {
		b.WriteString(redact(src[min(prev, len(src)):min(max(token.Start, prev), len(src))]))
		next := min(token.End+1, len(src))
		span := src[min(token.Start, len(src)):next]
		prev = token.End + 1
		switch {
		case isRewrittenType(token.Type):
			// Blanked in place by Anonymize, or synthesized by it, so already span-shaped
			b.WriteString(token.Text)
		case AnonymizedTypes.Has(token.Type):
			b.WriteString(fit(span, token.Text, token.Type))
		default:
			b.WriteString(string(span))
		}
	}
	b.WriteString(redact(src[min(prev, len(src)):]))
	return b.String(), nil
}

func textAlias(counter int, text string) string {
	var digits []byte
	for counter > 0 {
		digits = append(digits, anonymizeAlphabet[counter%len(anonymizeAlphabet)])
		counter /= len(anonymizeAlphabet)
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	nonSpace := 0
	for _, r := range text {
		if !pyIsSpace(r) {
			nonSpace++
		}
	}
	letters := strings.Repeat("a", max(0, nonSpace-len(digits))) + string(digits)
	var b strings.Builder
	i := 0
	for _, r := range text {
		if pyIsSpace(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(letters[i])
			i++
		}
	}
	return b.String()
}

func pow10Big(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

func numberAlias(counter int, text string) string {
	if len([]rune(text)) > 4000 {
		var b strings.Builder
		digitsSeen := false
		for _, r := range text {
			if unicode.IsDigit(r) {
				if digitsSeen {
					b.WriteByte('0')
				} else {
					b.WriteByte('1')
				}
				digitsSeen = true
			} else {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	sep := ""
	if strings.Contains(text, "e") {
		sep = "e"
	} else if strings.Contains(text, "E") {
		sep = "E"
	}
	mantissa, exponent := text, ""
	if sep != "" {
		mantissa, exponent, _ = strings.Cut(text, sep)
	}
	sign := ""
	if strings.HasPrefix(exponent, "-") || strings.HasPrefix(exponent, "+") {
		sign, exponent = exponent[:1], exponent[1:]
	}
	integer, fraction, hasDot := strings.Cut(mantissa, ".")
	digits := len([]rune(integer)) + len([]rune(fraction))
	c := big.NewInt(int64(counter))
	span := new(big.Int).Mul(big.NewInt(9), pow10Big(digits-1))
	mantissaValue := new(big.Int).Add(pow10Big(digits-1), new(big.Int).Mod(c, span))
	result := mantissaValue.String()
	if hasDot {
		result = result[:len(integer)] + "." + result[len(integer):]
	}
	if sep != "" {
		// The exponent marker is kept even when there are no digits after it, e.g. 1e
		result += sep + sign
		if n := len([]rune(exponent)); n > 0 {
			expSpan := new(big.Int).Mul(big.NewInt(9), pow10Big(n-1))
			q := new(big.Int).Div(c, span)
			result += new(big.Int).Add(pow10Big(n-1), new(big.Int).Mod(q, expSpan)).String()
		}
	}
	return result
}

func blankText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if pyIsSpace(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('.')
		}
	}
	return b.String()
}

// blankComment blanks the body of the comment at i, returning its text and the index past it.
func blankComment(sql []rune, i int, start, end []rune, nested bool) (string, int) {
	body := i + len(start)
	if len(end) == 0 {
		stop := len(sql)
		for k := body; k < len(sql); k++ {
			if sql[k] == '\n' {
				stop = k
				break
			}
		}
		return string(start) + blankText(string(sql[min(body, stop):stop])), stop
	}
	depth := 1
	for j := body; j < len(sql); {
		switch {
		case nested && hasPrefixRunes(sql[j:], start):
			depth++
			j += len(start)
		case hasPrefixRunes(sql[j:], end):
			j += len(end)
			depth--
			if depth == 0 {
				return string(start) + blankText(string(sql[body:j-len(end)])) + string(end), j
			}
		default:
			j++
		}
	}
	return string(start) + blankText(string(sql[min(body, len(sql)):])), len(sql)
}

func isSpaceRunes(s []rune) bool {
	for _, r := range s {
		if !pyIsSpace(r) {
			return false
		}
	}
	return true
}

func hasPrefixRunes(s, prefix []rune) bool {
	if len(prefix) > len(s) {
		return false
	}
	for i, r := range prefix {
		if s[i] != r {
			return false
		}
	}
	return true
}

func hasSuffixRunes(s, suffix []rune) bool {
	return len(suffix) <= len(s) && hasPrefixRunes(s[len(s)-len(suffix):], suffix)
}

func indexRunes(s, sub []rune, from int) int {
	for i := from; i+len(sub) <= len(s); i++ {
		if hasPrefixRunes(s[i:], sub) {
			return i
		}
	}
	return -1
}

func lastIndexRunes(s, sub []rune) int {
	for i := len(s) - len(sub); i >= 0; i-- {
		if hasPrefixRunes(s[i:], sub) {
			return i
		}
	}
	return -1
}

// pyIsSpace mirrors str.isspace for one character.
func pyIsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }
