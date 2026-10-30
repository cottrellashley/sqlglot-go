package sqlglot

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// SentinelLineBreak mirrors Generator.SENTINEL_LINE_BREAK.
const SentinelLineBreak = "__SQLGLOT__LB__"

// UnsupportedTemplate mirrors generator.UNSUPPORTED_TEMPLATE.
const UnsupportedTemplate = "Argument '%s' is not supported for expression '%s' when targeting %s."

var highByteRe = regexp.MustCompile(`[\x{80}-\x{ff}]`)

// EscapedUnicodeRe mirrors generator.ESCAPED_UNICODE_RE.
var EscapedUnicodeRe = regexp.MustCompile(`\\(\d+)`)

// Generator converts a syntax tree into SQL. It mirrors sqlglot.generator.Generator.
type Generator struct {
	D *Dialect
	C *GeneratorConfig

	Pretty              bool
	Identify            any
	Normalize           bool
	Pad                 int
	IndentSize          int
	NormalizeFunctions  any
	UnsupportedLevel    errs.ErrorLevel
	MaxUnsupported      int
	LeadingComma        bool
	MaxTextWidth        int
	Comments            bool
	UnsupportedMessages []string

	EscapedQuoteEnd               string
	EscapedByteQuoteEnd           string
	EscapedIdentifierEnd          string
	IdentifierEscapedSequences    map[string]string
	NextName                      func() string
	IdentifierStart               string
	IdentifierEnd                 string
	QuoteJSONPathKeyUsingBrackets bool

	// Extra is free-form per-generation storage for dialects.
	Extra map[string]any
}

func newGenerator(d *Dialect, opts exp.GenerateOptions) *Generator {
	g := &Generator{D: d, C: d.Generator}
	g.Pretty = opts.Pretty
	g.Identify = opts.Identify
	if g.Identify == nil {
		g.Identify = false
	}
	g.Normalize = opts.Normalize
	g.Pad = opts.Pad
	g.IndentSize = opts.Indent
	g.UnsupportedLevel = opts.UnsupportedLevel
	g.MaxUnsupported = opts.MaxUnsupported
	g.LeadingComma = opts.LeadingComma
	g.MaxTextWidth = opts.MaxTextWidth
	g.Comments = opts.Comments
	if opts.NormalizeFunctions == nil {
		g.NormalizeFunctions = d.NormalizeFunctions
	} else {
		g.NormalizeFunctions = opts.NormalizeFunctions
	}
	escapes := d.Tokenizer.StringEscapes
	g.EscapedQuoteEnd = escapes[0] + d.QuoteEnd
	if d.ByteEnd != "" {
		g.EscapedByteQuoteEnd = escapes[0] + d.ByteEnd
	}
	g.EscapedIdentifierEnd = d.IdentifierEnd + d.IdentifierEnd
	g.IdentifierEscapedSequences = d.IdentifierEscapedSequences
	g.NextName = helper.NameSequence("_t")
	g.IdentifierStart = d.IdentifierStart
	g.IdentifierEnd = d.IdentifierEnd
	g.QuoteJSONPathKeyUsingBrackets = true
	return g
}

// generatePanic wraps errors raised while generating.
type generatePanic struct{ err error }

// GenerateSafe generates SQL, converting raised errors (UnsupportedError, ...) into errors.
func (g *Generator) GenerateSafe(e *exp.Expr, copy bool) (sql string, err error) {
	defer func() {
		if r := recover(); r != nil {
			switch x := r.(type) {
			case generatePanic:
				err = x.err
			case *errs.UnsupportedError:
				err = x
			case *errs.ParseError:
				err = x
			case parseBailout:
				err = x.err
			default:
				panic(r)
			}
		}
	}()
	return g.Generate(e, copy), nil
}

// Raise aborts generation with an error.
func (g *Generator) Raise(err error) { panic(generatePanic{err}) }

// Raisef aborts generation with a formatted error (Python `raise ValueError(...)`).
func (g *Generator) Raisef(format string, args ...any) {
	panic(generatePanic{fmt.Errorf(format, args...)})
}

// Unsupported records (or raises, with IMMEDIATE) an unsupported-construct message.
func (g *Generator) Unsupported(message string) {
	if g.UnsupportedLevel == errs.Immediate {
		panic(generatePanic{&errs.UnsupportedError{Message: message}})
	}
	g.UnsupportedMessages = append(g.UnsupportedMessages, message)
}

// Sep mirrors Generator.sep(sep=" ").
func (g *Generator) Sep() string { return g.SepWith(" ") }

// SepWith mirrors Generator.sep(sep).
func (g *Generator) SepWith(sep string) string {
	if g.Pretty {
		return strings.TrimSpace(sep) + "\n"
	}
	return sep
}

// Seg mirrors Generator.seg(sql, sep=" ").
func (g *Generator) Seg(sql string) string { return g.SepWith(" ") + sql }

// SegWith mirrors Generator.seg(sql, sep).
func (g *Generator) SegWith(sql, sep string) string { return g.SepWith(sep) + sql }

// SanitizeComment mirrors Generator.sanitize_comment.
func (g *Generator) SanitizeComment(comment string) string {
	if comment == "" {
		return comment
	}
	r := []rune(comment)
	if !unicode.IsSpace(r[0]) {
		comment = " " + comment
	}
	r = []rune(comment)
	if !unicode.IsSpace(r[len(r)-1]) {
		comment = comment + " "
	}
	comment = strings.ReplaceAll(comment, "*/", "* /")
	comment = strings.ReplaceAll(comment, "/*", "/ *")
	return comment
}

// MaybeComment mirrors Generator.maybe_comment(sql, expression).
func (g *Generator) MaybeComment(sql string, e *exp.Expr) string {
	return g.MaybeCommentFull(sql, e, nil, false, false)
}

// MaybeCommentFull mirrors maybe_comment(sql, expression, comments, separated). hasComments
// distinguishes comments=None (use the expression's) from an explicit list.
func (g *Generator) MaybeCommentFull(sql string, e *exp.Expr, comments []string, hasComments bool, separated bool) string {
	var cs []string
	if g.Comments {
		if hasComments {
			cs = comments
		} else if e != nil {
			cs = e.Comments
		}
	}
	if len(cs) == 0 || (e != nil && g.C.ExcludeComments.HasAny(e.Kind())) {
		return sql
	}
	var list []string
	for _, c := range cs {
		if c != "" {
			list = append(list, "/*"+g.ReplaceLineBreaks(g.SanitizeComment(c))+"*/")
		}
	}
	if len(list) == 0 {
		return sql
	}
	if separated || (e != nil && g.C.WithSeparatedComments.HasAny(e.Kind())) {
		commentsSQL := strings.Join(list, g.Sep())
		if sql == "" || startsWithSpace(sql) {
			return g.Sep() + commentsSQL + sql
		}
		return commentsSQL + g.Sep() + sql
	}
	return sql + " " + strings.Join(list, " ")
}

// MaybeCommentWith is maybe_comment(sql, comments=...).
func (g *Generator) MaybeCommentWith(sql string, comments []string) string {
	return g.MaybeCommentFull(sql, nil, comments, true, false)
}

func startsWithSpace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}

// ReplaceLineBreaks mirrors _replace_line_breaks.
func (g *Generator) ReplaceLineBreaks(s string) string {
	if g.Pretty {
		return strings.ReplaceAll(s, "\n", SentinelLineBreak)
	}
	return s
}

// Wrap mirrors Generator.wrap(expression).
func (g *Generator) Wrap(e any) string {
	var thisSQL string
	switch x := e.(type) {
	case string:
		thisSQL = x
	case *exp.Expr:
		if x.IsAny(exp.Select, exp.SetOperation) {
			thisSQL = g.SQL(x)
		} else {
			thisSQL = g.SQLArg(x, "this")
		}
	}
	if thisSQL == "" {
		return "()"
	}
	thisSQL = g.IndentFull(thisSQL, 1, 0, true, false, false)
	return "(" + g.SepWith("") + thisSQL + g.SegWith(")", "")
}

// NoIdentify runs fn with identify disabled.
func (g *Generator) NoIdentify(fn func() string) string {
	orig := g.Identify
	g.Identify = false
	r := fn()
	g.Identify = orig
	return r
}

// NormalizeFunc mirrors Generator.normalize_func.
func (g *Generator) NormalizeFunc(name string) string {
	switch v := g.NormalizeFunctions.(type) {
	case string:
		if v == "upper" {
			return strings.ToUpper(name)
		}
		if v == "lower" {
			return strings.ToLower(name)
		}
	case bool:
		if v {
			return strings.ToUpper(name)
		}
	}
	return name
}

// Indent mirrors Generator.indent(sql).
func (g *Generator) Indent(sql string) string { return g.IndentFull(sql, 0, 0, false, false, false) }

// IndentFull mirrors Generator.indent(sql, level, pad, skip_first, skip_last). When hasPad is
// false the generator's pad is used.
func (g *Generator) IndentFull(sql string, level, pad int, hasPad, skipFirst, skipLast bool) string {
	if !g.Pretty || sql == "" {
		return sql
	}
	if !hasPad {
		pad = g.Pad
	}
	lines := strings.Split(sql, "\n")
	prefix := strings.Repeat(" ", level*g.IndentSize+pad)
	for i, line := range lines {
		if (skipFirst && i == 0) || (skipLast && i == len(lines)-1) {
			continue
		}
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// IndentSkip is indent(sql, skip_first=..., skip_last=...).
func (g *Generator) IndentSkip(sql string, skipFirst, skipLast bool) string {
	return g.IndentFull(sql, 0, 0, false, skipFirst, skipLast)
}

// SQL generates SQL for an expression (Python self.sql(expression)).
func (g *Generator) SQL(e *exp.Expr) string { return g.sqlFull(e, true) }

// SQLNoComment is self.sql(expression, comment=False).
func (g *Generator) SQLNoComment(e *exp.Expr) string { return g.sqlFull(e, false) }

// SQLAny is self.sql(value) for a string or expression value.
func (g *Generator) SQLAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case *exp.Expr:
		return g.SQL(x)
	}
	return ""
}

// SQLArg is self.sql(expression, key).
func (g *Generator) SQLArg(e *exp.Expr, key string) string {
	if e == nil {
		return ""
	}
	switch v := e.Arg(key).(type) {
	case *exp.Expr:
		return g.SQL(v)
	case string:
		return v
	}
	return ""
}

// Dispatch returns the handler used for a kind (TRANSFORMS, then <kind>_sql methods).
func (g *Generator) Dispatch(k exp.Kind) GenFunc { return g.C.dispatch[k] }

func (g *Generator) sqlFull(e *exp.Expr, comment bool) string {
	if e == nil {
		return ""
	}
	var sql string
	if handler := g.C.dispatch[e.Kind()]; handler != nil {
		sql = handler(g, e)
	} else if e.Is(exp.Func) {
		sql = g.FunctionFallbackSQL(e)
	} else if e.Is(exp.Property) {
		sql = g.PropertySQL(e)
	} else {
		panic(generatePanic{fmt.Errorf("Unsupported expression type %s", e.ClassName())})
	}
	if g.Comments && comment {
		return g.MaybeComment(sql, e)
	}
	return sql
}

func (g *Generator) callKindMethod(k exp.Kind, e *exp.Expr) string {
	if m := g.C.SQLMethods[k]; m != nil {
		return m(g, e)
	}
	panic(generatePanic{fmt.Errorf("no SQL method for %s", k)})
}

// ExprsOpts are the keyword arguments of Generator.expressions.
type ExprsOpts struct {
	Key       string
	Sqls      []any // used when the expression is nil
	Flat      bool
	NoIndent  bool // indent=False
	SkipFirst bool
	SkipLast  bool
	Sep       string // "" means ", "; use EmptySep for an empty separator
	Prefix    string
	Dynamic   bool
	NewLine   bool
}

// EmptySep selects an empty separator in ExprsOpts.
const EmptySep = "\x00"

// Expressions mirrors Generator.expressions(expression, ...).
func (g *Generator) Expressions(e *exp.Expr, o ExprsOpts) string {
	var items []any
	if e != nil {
		key := o.Key
		if key == "" {
			key = "expressions"
		}
		switch v := e.Arg(key).(type) {
		case []*exp.Expr:
			for _, x := range v {
				items = append(items, x)
			}
		case []string:
			for _, x := range v {
				items = append(items, x)
			}
		case *exp.Expr:
			// Python iterates the expression, which yields its "expressions" (if it has them)
			if !v.Kind().HasArg("expressions") {
				g.Raisef("TypeError: '%s' object is not iterable", v.ClassName())
			}
			for _, x := range v.Expressions() {
				items = append(items, x)
			}
		}
	} else {
		items = o.Sqls
	}
	if len(items) == 0 {
		return ""
	}
	sep := o.Sep
	if sep == "" {
		sep = ", "
	} else if sep == EmptySep {
		sep = ""
	}
	if o.Flat {
		var parts []string
		for _, it := range items {
			if s := g.SQLAny(it); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, sep)
	}
	num := len(items)
	var result []string
	for i, it := range items {
		var sql string
		var comments string
		switch x := it.(type) {
		case *exp.Expr:
			sql = g.SQLNoComment(x)
			if sql == "" {
				continue
			}
			comments = g.MaybeComment("", x)
		case string:
			sql = x
			if sql == "" {
				continue
			}
		default:
			continue
		}
		if g.Pretty {
			if g.LeadingComma {
				s := ""
				if i > 0 {
					s = sep
				}
				result = append(result, s+o.Prefix+sql+comments)
			} else {
				tail := ""
				if i+1 < num {
					if comments != "" {
						tail = strings.TrimRightFunc(sep, unicode.IsSpace)
					} else {
						tail = sep
					}
				}
				result = append(result, o.Prefix+sql+tail+comments)
			}
		} else {
			tail := ""
			if i+1 < num {
				tail = sep
			}
			result = append(result, o.Prefix+sql+comments+tail)
		}
	}
	var resultSQL string
	if g.Pretty && (!o.Dynamic || g.TooWide(result)) {
		if o.NewLine {
			result = append([]string{""}, append(result, "")...)
		}
		trimmed := make([]string, len(result))
		for i, s := range result {
			trimmed[i] = strings.TrimRightFunc(s, unicode.IsSpace)
		}
		resultSQL = strings.Join(trimmed, "\n")
	} else {
		resultSQL = strings.Join(result, "")
	}
	if !o.NoIndent {
		return g.IndentSkip(resultSQL, o.SkipFirst, o.SkipLast)
	}
	return resultSQL
}

// ExprsList is Expressions over an explicit list of items (sqls=...).
func (g *Generator) ExprsList(items []*exp.Expr, o ExprsOpts) string {
	o.Sqls = nil
	for _, x := range items {
		o.Sqls = append(o.Sqls, x)
	}
	return g.Expressions(nil, o)
}

// Flat is Expressions(e, flat=True) for the "expressions" arg.
func (g *Generator) Flat(e *exp.Expr) string { return g.Expressions(e, ExprsOpts{Flat: true}) }

// TooWide mirrors Generator.too_wide.
func (g *Generator) TooWide(args []string) bool {
	n := 0
	for _, a := range args {
		n += utf8.RuneCountInString(a)
	}
	return n > g.MaxTextWidth
}

// FormatArgs mirrors Generator.format_args(*args) (bools and nils are skipped).
func (g *Generator) FormatArgs(args ...any) string { return g.FormatArgsSep(", ", args...) }

// FormatArgsSep mirrors format_args(*args, sep=sep).
func (g *Generator) FormatArgsSep(sep string, args ...any) string {
	var sqls []string
	for _, a := range args {
		switch x := a.(type) {
		case nil, bool:
			continue
		case *exp.Expr:
			if x == nil {
				continue
			}
			sqls = append(sqls, g.SQL(x))
		case string:
			sqls = append(sqls, x)
		case []*exp.Expr:
			for _, y := range x {
				sqls = append(sqls, g.SQL(y))
			}
		default:
			sqls = append(sqls, fmt.Sprint(x))
		}
	}
	if g.Pretty && g.TooWide(sqls) {
		return g.IndentSkip("\n"+strings.Join(sqls, strings.TrimSpace(sep)+"\n")+"\n", true, true)
	}
	return strings.Join(sqls, sep)
}

// Func mirrors Generator.func(name, *args): NAME(arg, ...). Arguments may be expressions,
// strings, []*exp.Expr (exploded) or nil.
func (g *Generator) Func(name string, args ...any) string {
	return g.FuncFull(name, args, "(", ")", false)
}

// FuncNoNormalize is func(name, *args, normalize=False).
func (g *Generator) FuncNoNormalize(name string, args ...any) string {
	return g.FuncFull(name, args, "(", ")", true)
}

// FuncFull mirrors func(name, *args, prefix=..., suffix=..., normalize=not noNormalize).
func (g *Generator) FuncFull(name string, args []any, prefix, suffix string, noNormalize bool) string {
	if !noNormalize {
		name = g.NormalizeFunc(name)
	}
	return name + prefix + g.FormatArgs(args...) + suffix
}

// Binary mirrors Generator.binary(expression, op).
func (g *Generator) Binary(e *exp.Expr, op string) string {
	var sqls []string
	type item struct {
		node *exp.Expr
		s    string
		isS  bool
	}
	stack := []item{{node: e}}
	kind := e.Kind()
	isIs := kind == exp.Is && (op == "IS" || op == "IS NOT")
	negate := e.Arg("negate")
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if it.isS {
			sqls = append(sqls, it.s)
			continue
		}
		node := it.node
		if node != nil && node.Kind() == kind &&
			(node == e || (isIs && !node.Expression().IsKind(exp.Boolean)) || pyScalarEq(node.Arg("negate"), negate)) {
			if opFunc := node.Arg("operator"); exp.Truthy(opFunc) {
				op = "OPERATOR(" + g.SQLAny(opFunc) + ")"
			} else if isIs {
				op = iff(node.Bool("negate"), "IS NOT", "IS")
			}
			stack = append(stack, item{node: node.Get("expression")})
			stack = append(stack, item{s: " " + g.MaybeCommentFull(op, nil, node.Comments, true, false) + " ", isS: true})
			stack = append(stack, item{node: node.Get("this")})
		} else {
			sqls = append(sqls, g.SQL(node))
		}
	}
	return strings.Join(sqls, "")
}

// pyScalarEq mirrors Python == for scalar argument values (None, bools, strings, ints).
func pyScalarEq(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case *exp.Expr:
		y, ok := b.(*exp.Expr)
		return ok && x.Equal(y)
	case bool:
		switch y := b.(type) {
		case bool:
			return x == y
		case int:
			return iff(x, 1, 0) == y
		}
		return false
	case int:
		switch y := b.(type) {
		case int:
			return x == y
		case bool:
			return iff(y, 1, 0) == x
		}
		return false
	case string:
		y, ok := b.(string)
		return ok && x == y
	}
	return a == b
}

// OpExpressions mirrors Generator.op_expressions(op, expression, flat).
func (g *Generator) OpExpressions(op string, e *exp.Expr, flat bool) string {
	flat = flat || e.Parent().Is(exp.Properties)
	exprsSQL := g.Expressions(e, ExprsOpts{Flat: flat})
	if flat {
		return op + " " + exprsSQL
	}
	s := ""
	if exprsSQL != "" {
		s = g.Sep()
	}
	return g.Seg(op) + s + exprsSQL
}

// NakedProperty mirrors Generator.naked_property.
func (g *Generator) NakedProperty(e *exp.Expr) string {
	name, ok := exp.PropertyToName[e.Kind()]
	if !ok {
		g.Unsupported("Unsupported property " + e.ClassName())
		name = "None"
	}
	return name + " " + g.SQLArg(e, "this")
}

// UnsupportedArgs mirrors the @unsupported_args decorator: it reports the given args (or
// ArgDiag(arg, diagnostic) pairs) as unsupported before calling fn.
func UnsupportedArgs(fn GenFunc, args ...string) GenFunc {
	return func(g *Generator, e *exp.Expr) string {
		g.CheckUnsupportedArgs(e, args...)
		return fn(g, e)
	}
}

// CheckUnsupportedArgs is the body of the @unsupported_args decorator, for use at the top of a
// generator method.
func (g *Generator) CheckUnsupportedArgs(e *exp.Expr, args ...string) {
	for _, a := range args {
		name, diag, _ := strings.Cut(a, "\x00")
		if exp.Truthy(e.Arg(name)) {
			if diag == "" {
				diag = fmt.Sprintf(UnsupportedTemplate, name, e.ClassName(), g.D.ClassName)
			}
			g.Unsupported(diag)
		}
	}
}

// ArgDiag builds an UnsupportedArgs entry with a custom diagnostic.
func ArgDiag(arg, diagnostic string) string { return arg + "\x00" + diagnostic }

// NewGeneratorConfig returns a fresh copy of the generator.Generator class configuration.
func NewGeneratorConfig() *GeneratorConfig { return rawGeneratorConfig.Clone() }

var rawGeneratorConfig = newBaseGeneratorConfig()

func (c *GeneratorConfig) finalize(d *DialectClass) {
	c.dispatch = map[exp.Kind]GenFunc{}
	for k, v := range c.SQLMethods {
		c.dispatch[k] = v
	}
	for k, v := range c.Transforms {
		c.dispatch[k] = v
	}
}
