package sqlglot

// Port of sqlglot/jsonpath.py: the JSON path tokenizer, parser and generator transforms.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

// JSONPathTokenizerConfig returns the configuration of jsonpath.JSONPathTokenizer.
func JSONPathTokenizerConfig() *tokens.Config {
	c := tokens.BaseConfig().Clone()
	c.SingleTokens = map[rune]tokens.TokenType{
		'(':  tokens.LParen,
		')':  tokens.RParen,
		'[':  tokens.LBracket,
		']':  tokens.RBracket,
		':':  tokens.Colon,
		',':  tokens.Comma,
		'-':  tokens.Dash,
		'.':  tokens.Dot,
		'?':  tokens.Placeholder,
		'@':  tokens.Parameter,
		'\'': tokens.Quote,
		'"':  tokens.Quote,
		'$':  tokens.Dollar,
		'*':  tokens.Star,
	}
	c.Keywords = map[string]tokens.TokenType{"..": tokens.Dot}
	c.IdentifierEscapes = []string{`\`}
	c.StringEscapes = []string{`\`}
	c.NumbersCanHaveDecimals = false
	c.VarTokens = tokens.NewSet(tokens.Var)
	return c
}

// isPyFalse reports whether v is the Python False singleton (as returned by _parse_literal).
func isPyFalse(v any) bool {
	b, ok := v.(bool)
	return ok && !b
}

// ParseJSONPath mirrors jsonpath.parse(path, dialect): it parses a JSON path string into an
// exp.JSONPath. Paths are canonicalized to always start with a JSONPathRoot.
func ParseJSONPath(path string, d *Dialect) (result *exp.Expr, err error) {
	if d == nil {
		d = BaseDialectClass().NewInstance()
	}
	tokenizer := d.NewJSONPathTokenizer()
	toks, terr := tokenizer.Tokenize(path)
	if terr != nil {
		return nil, terr
	}
	runes := []rune(path)
	size := len(toks)
	varTokens := d.JSONPathTokenizer.VarTokens
	i := 0

	type jsonPathError struct{ err error }
	defer func() {
		if r := recover(); r != nil {
			if x, ok := r.(jsonPathError); ok {
				result, err = nil, x.err
				return
			}
			panic(r)
		}
	}()
	fail := func(msg string) {
		panic(jsonPathError{&errs.ParseError{Message: fmt.Sprintf("%s at index %d: %s", msg, i, path)}})
	}

	curr := func() (tokens.TokenType, bool) {
		if i < size {
			return toks[i].Type, true
		}
		return 0, false
	}
	prev := func() *tokens.Token { return toks[i-1] }
	advance := func() *tokens.Token {
		i++
		return prev()
	}
	match := func(tt tokens.TokenType, raiseUnmatched bool) *tokens.Token {
		if c, ok := curr(); ok && c == tt {
			return advance()
		}
		if raiseUnmatched {
			fail("Expected " + tt.Repr())
		}
		return nil
	}
	matchSet := func(types tokens.TokenSet) *tokens.Token {
		if c, ok := curr(); ok && types.Has(c) {
			return advance()
		}
		return nil
	}

	var parseBracket func() *exp.Expr

	parseLiteral := func() any {
		token := match(tokens.String, false)
		if token == nil {
			token = match(tokens.Identifier, false)
		}
		if token != nil {
			return token.Text
		}
		if match(tokens.Star, false) != nil {
			return exp.New(exp.JSONPathWildcard)
		}
		if match(tokens.Placeholder, false) != nil || match(tokens.LParen, false) != nil {
			script := prev().Text == "("
			start := i
			for {
				if match(tokens.LBracket, false) != nil {
					parseBracket() // nested call which we can throw away
				}
				if c, ok := curr(); !ok || c == tokens.RBracket {
					break
				}
				advance()
			}
			kind := iff(script, exp.JSONPathScript, exp.JSONPathFilter)
			var end int
			if i < size {
				end = toks[i].End
			} else {
				end = toks[size-1].End
			}
			from := toks[start].Start
			if from > len(runes) {
				from = len(runes)
			}
			if end > len(runes) {
				end = len(runes)
			}
			text := ""
			if from < end {
				text = string(runes[from:end])
			}
			return exp.New(kind, "this", text)
		}
		number := ""
		if match(tokens.Dash, false) != nil {
			number = "-"
		}
		if token := match(tokens.Number, false); token != nil {
			number += token.Text
		}
		if number != "" {
			n, err := strconv.Atoi(number)
			if err != nil {
				panic(jsonPathError{fmt.Errorf("invalid literal for int() with base 10: '%s'", number)})
			}
			return n
		}
		return false
	}

	parseSlice := func() any {
		start := parseLiteral()
		var end, step any
		if match(tokens.Colon, false) != nil {
			end = parseLiteral()
		}
		if match(tokens.Colon, false) != nil {
			step = parseLiteral()
		}
		if end == nil && step == nil {
			return start
		}
		return exp.New(exp.JSONPathSlice, "start", start, "end", end, "step", step)
	}

	parseBracket = func() *exp.Expr {
		literal := parseSlice()
		var node *exp.Expr
		if !isPyFalse(literal) {
			indexes := []any{literal}
			for match(tokens.Comma, false) != nil {
				literal = parseSlice()
				if !isPyFalse(literal) {
					indexes = append(indexes, literal)
				}
			}
			if len(indexes) == 1 {
				switch x := indexes[0].(type) {
				case string:
					node = exp.New(exp.JSONPathKey, "this", x)
				case *exp.Expr:
					if x.IsAny(exp.JSONPathScript, exp.JSONPathFilter) {
						node = exp.New(exp.JSONPathSelector, "this", x)
					} else {
						node = exp.New(exp.JSONPathSubscript, "this", x)
					}
				default:
					node = exp.New(exp.JSONPathSubscript, "this", x)
				}
			} else {
				node = exp.New(exp.JSONPathUnion, "expressions", indexes)
			}
		} else {
			fail("Cannot have empty segment")
		}
		match(tokens.RBracket, true)
		return node
	}

	parseVarText := func() string {
		// Consumes var tokens to build a JSON path key; the previous token has already been
		// consumed, so the key starts right after the token before it.
		prevIndex := i - 2
		for matchSet(varTokens) != nil {
		}
		start := 0
		if prevIndex >= 0 {
			start = toks[prevIndex].End + 1
		}
		if start > len(runes) {
			start = len(runes)
		}
		if i >= len(toks) {
			// This key is the last token for the path, so it's text is the remaining path
			return string(runes[start:])
		}
		end := toks[i].Start
		if end < start {
			return ""
		}
		return string(runes[start:end])
	}

	// We canonicalize the JSON path AST so that it always starts with a
	// "root" element, so paths like "field" will be generated as "$.field"
	match(tokens.Dollar, false)
	expressions := []*exp.Expr{exp.New(exp.JSONPathRoot)}

	for {
		if _, ok := curr(); !ok {
			break
		}
		if match(tokens.Dot, false) != nil || match(tokens.Colon, false) != nil {
			recursive := prev().Text == ".."
			var value any
			if matchSet(varTokens) != nil {
				value = parseVarText()
			} else if match(tokens.Identifier, false) != nil {
				value = prev().Text
			} else if match(tokens.Star, false) != nil {
				value = exp.New(exp.JSONPathWildcard)
			}
			if recursive {
				expressions = append(expressions, exp.New(exp.JSONPathRecursive, "this", value))
			} else if exp.Truthy(value) {
				expressions = append(expressions, exp.New(exp.JSONPathKey, "this", value))
			} else if !d.JsonPathSingleDotIsWildcard {
				fail("Expected key name or * after DOT")
			}
		} else if match(tokens.LBracket, false) != nil {
			expressions = append(expressions, parseBracket())
		} else if matchSet(varTokens) != nil {
			expressions = append(expressions, exp.New(exp.JSONPathKey, "this", parseVarText()))
		} else if match(tokens.Identifier, false) != nil {
			expressions = append(expressions, exp.New(exp.JSONPathKey, "this", prev().Text))
		} else if match(tokens.Star, false) != nil {
			expressions = append(expressions, exp.New(exp.JSONPathWildcard))
		} else {
			fail("Unexpected " + toks[i].Type.Repr())
		}
	}
	return exp.New(exp.JSONPath, "expressions", expressions), nil
}

// jsonPathPartTransforms mirrors jsonpath.JSON_PATH_PART_TRANSFORMS.
func jsonPathPartTransforms() map[exp.Kind]GenFunc {
	return map[exp.Kind]GenFunc{
		exp.JSONPathFilter:    func(g *Generator, e *exp.Expr) string { return "?" + pyStr(e.Arg("this")) },
		exp.JSONPathKey:       func(g *Generator, e *exp.Expr) string { return g.JsonPathKeySQL(e) },
		exp.JSONPathRecursive: func(g *Generator, e *exp.Expr) string { return ".." + argStr(e.Arg("this")) },
		exp.JSONPathRoot:      func(g *Generator, e *exp.Expr) string { return "$" },
		exp.JSONPathScript:    func(g *Generator, e *exp.Expr) string { return "(" + pyStr(e.Arg("this")) },
		exp.JSONPathSelector: func(g *Generator, e *exp.Expr) string {
			return "[" + g.JsonPathPart(e.Arg("this")) + "]"
		},
		exp.JSONPathSlice: func(g *Generator, e *exp.Expr) string {
			var parts []string
			for _, k := range []string{"start", "end", "step"} {
				p := e.Arg(k)
				if p == nil {
					continue
				}
				if isPyFalse(p) {
					parts = append(parts, "")
				} else {
					parts = append(parts, g.JsonPathPart(p))
				}
			}
			return strings.Join(parts, ":")
		},
		exp.JSONPathSubscript: func(g *Generator, e *exp.Expr) string { return g.JsonPathSubscriptSQL(e) },
		exp.JSONPathUnion: func(g *Generator, e *exp.Expr) string {
			var parts []string
			switch v := e.Arg("expressions").(type) {
			case []any:
				for _, p := range v {
					parts = append(parts, g.JsonPathPart(p))
				}
			case []*exp.Expr:
				for _, p := range v {
					parts = append(parts, g.JsonPathPart(p))
				}
			}
			return "[" + strings.Join(parts, ",") + "]"
		},
		exp.JSONPathWildcard: func(g *Generator, e *exp.Expr) string { return "*" },
	}
}

// AllJSONPathParts mirrors jsonpath.ALL_JSON_PATH_PARTS.
var AllJSONPathParts = func() []exp.Kind {
	var out []exp.Kind
	for k := range jsonPathPartTransforms() {
		out = append(out, k)
	}
	return out
}()
