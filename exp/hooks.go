package exp

import (
	"fmt"

	"github.com/cottrellashley/sqlglot-go/errs"
)

// Constants mirroring sqlglot.expressions.core.
const (
	SQLGlotMeta      = "sqlglot.meta"
	SQLGlotAnonymous = "sqlglot.anonymous"
)

var (
	// NonnullConstants and Constants mirror NONNULL_CONSTANTS and CONSTANTS.
	NonnullConstants = []Kind{Literal, Boolean}
	Constants        = []Kind{Literal, Boolean, Null}

	TableParts       = []string{"this", "db", "catalog"}
	ColumnParts      = []string{"this", "table", "db", "catalog"}
	PositionMetaKeys = []string{"line", "col", "start", "end"}
)

// ParseOptions are the options forwarded to the parser by builders (sqlglot's **opts).
type ParseOptions struct {
	Dialect             any
	Into                []Kind
	ErrorLevel          errs.ErrorLevel // errs.Unset means the parser default (RAISE)
	ErrorMessageContext int
	MaxErrors           int
}

// GenerateOptions mirror sqlglot.generator.Generator's constructor arguments.
type GenerateOptions struct {
	Dialect            any
	Pretty             bool
	Identify           any // bool or "safe"
	Normalize          bool
	Pad                int
	Indent             int
	NormalizeFunctions any // nil (dialect default), bool, "upper" or "lower"
	UnsupportedLevel   errs.ErrorLevel
	MaxUnsupported     int
	LeadingComma       bool
	MaxTextWidth       int
	Comments           bool
	NoCopy             bool
}

// DefaultPretty mirrors sqlglot.pretty: the default of the Pretty generation option.
var DefaultPretty bool

// DefaultGenerateOptions returns the generator defaults.
func DefaultGenerateOptions() GenerateOptions {
	return GenerateOptions{
		Pretty:           DefaultPretty,
		Identify:         false,
		Pad:              2,
		Indent:           2,
		UnsupportedLevel: errs.Warn,
		MaxUnsupported:   3,
		MaxTextWidth:     80,
		Comments:         true,
	}
}

// Backend is the bridge from the AST package to the parser/generator/dialect machinery. It is
// installed by the root sqlglot package at init time.
type Backend interface {
	ParseOne(sql string, opts ParseOptions) (*Expr, error)
	Generate(e *Expr, opts GenerateOptions) (string, error)
	// BuildFunc mirrors exp.func(name, *args, dialect=...).
	BuildFunc(name string, args []*Expr, kwargs []Arg, dialect any) (*Expr, error)
	// TypeMapping returns the generator TYPE_MAPPING of a dialect.
	TypeMapping(dialect any) map[DType]string
	// Hook lets the backend provide optional services (annotate_types, simplify, scope...).
	Hook(name string, args ...any) (any, error)
}

var backend Backend

// SetBackend installs the parser/generator backend (called by package sqlglot).
func SetBackend(b Backend) { backend = b }

func mustBackend() Backend {
	if backend == nil {
		panic("sqlglot backend not installed: import github.com/cottrellashley/sqlglot-go")
	}
	return backend
}

// ParseOne parses a SQL string through the installed backend.
func ParseOne(sql string, opts ParseOptions) (*Expr, error) {
	return mustBackend().ParseOne(sql, opts)
}

// BackendHook calls a named backend service.
func BackendHook(name string, args ...any) (any, error) { return mustBackend().Hook(name, args...) }

// ---------------------------------------------------------------------------------------
// builder options

// Option configures a builder call (dialect, copy, append, ...). Options may be mixed with
// expression arguments in the variadic parameters of builders.
type Option func(*buildOpts)

type buildOpts struct {
	dialect    any
	copy       bool
	append     bool
	wrap       bool
	distinct   bool
	quoted     *bool
	into       []Kind
	prefix     string
	errorLevel errs.ErrorLevel
	extra      map[string]any
}

func defaultBuildOpts() buildOpts {
	return buildOpts{copy: true, append: true, wrap: true, distinct: true, errorLevel: errs.Unset}
}

func (o *buildOpts) parseOptions(into ...Kind) ParseOptions {
	return ParseOptions{Dialect: o.dialect, Into: into, ErrorLevel: o.errorLevel}
}

// WithDialect sets the dialect used to parse string arguments.
func WithDialect(d any) Option { return func(o *buildOpts) { o.dialect = d } }

// WithCopy sets whether expression arguments are copied (default true).
func WithCopy(b bool) Option { return func(o *buildOpts) { o.copy = b } }

// WithAppend sets whether list builders append to existing values (default true).
func WithAppend(b bool) Option { return func(o *buildOpts) { o.append = b } }

// WithWrap sets whether connector operands are wrapped in parentheses (default true).
func WithWrap(b bool) Option { return func(o *buildOpts) { o.wrap = b } }

// WithDistinct sets the distinct flag of set operations (default true).
func WithDistinct(b bool) Option { return func(o *buildOpts) { o.distinct = b } }

// WithQuoted forces identifier quoting.
func WithQuoted(b bool) Option { return func(o *buildOpts) { o.quoted = &b } }

// WithErrorLevel sets the parser error level used for string arguments.
func WithErrorLevel(l errs.ErrorLevel) Option { return func(o *buildOpts) { o.errorLevel = l } }

// WithArg passes a named keyword argument to builders that accept extra kwargs.
func WithArg(key string, value any) Option {
	return func(o *buildOpts) {
		if o.extra == nil {
			o.extra = map[string]any{}
		}
		o.extra[key] = value
	}
}

// splitArgs separates options from positional arguments.
func splitArgs(args []any) ([]any, buildOpts) {
	opts := defaultBuildOpts()
	var pos []any
	for _, a := range args {
		switch x := a.(type) {
		case Option:
			x(&opts)
		case []Option:
			for _, o := range x {
				o(&opts)
			}
		default:
			pos = append(pos, a)
		}
	}
	return pos, opts
}

// ExpOrStr is anything accepted where sqlglot accepts `ExpOrStr`: *Expr, string or int.
type ExpOrStr = any

// MaybeParse mirrors exp.maybe_parse.
func MaybeParse(v any, opts ParseOptions, prefix string, copy bool) (*Expr, error) {
	switch x := v.(type) {
	case *Expr:
		if x == nil {
			return nil, &errs.ParseError{Message: "SQL cannot be None"}
		}
		if copy {
			return x.Copy(), nil
		}
		return x, nil
	case nil:
		return nil, &errs.ParseError{Message: "SQL cannot be None"}
	}
	sql := fmt.Sprint(v)
	if prefix != "" {
		sql = prefix + " " + sql
	}
	return ParseOne(sql, opts)
}

// MustMaybeParse is MaybeParse that panics on error.
func MustMaybeParse(v any, opts ParseOptions, prefix string, copy bool) *Expr {
	e, err := MaybeParse(v, opts, prefix, copy)
	if err != nil {
		panic(err)
	}
	return e
}

// Parse is a convenience wrapper around MaybeParse for builder code: it parses strings in the
// given dialect and panics on error.
func Parse(v any, dialect any, into ...Kind) *Expr {
	return MustMaybeParse(v, ParseOptions{Dialect: dialect, Into: into, ErrorLevel: errs.Unset}, "", false)
}

func (o *buildOpts) parse(v any, prefix string, into ...Kind) *Expr {
	return MustMaybeParse(v, o.parseOptions(into...), prefix, o.copy)
}

// ---------------------------------------------------------------------------------------
// SQL generation

// GenOption configures SQL generation.
type GenOption func(*GenerateOptions)

// Pretty enables pretty printing.
func Pretty(b bool) GenOption { return func(o *GenerateOptions) { o.Pretty = b } }

// Identify sets identifier quoting (bool or "safe").
func Identify(v any) GenOption { return func(o *GenerateOptions) { o.Identify = v } }

// NormalizeIdentifiers enables identifier normalization.
func NormalizeIdentifiers(b bool) GenOption { return func(o *GenerateOptions) { o.Normalize = b } }

// UnsupportedLevel sets the unsupported error level.
func UnsupportedLevel(l errs.ErrorLevel) GenOption {
	return func(o *GenerateOptions) { o.UnsupportedLevel = l }
}

// Comments enables/disables comments in the output.
func Comments(b bool) GenOption { return func(o *GenerateOptions) { o.Comments = b } }

// NoCopy disables copying the expression before generation.
func NoCopy() GenOption { return func(o *GenerateOptions) { o.NoCopy = true } }

// NormalizeFunctions sets function name normalization (bool, "upper", "lower").
func NormalizeFunctions(v any) GenOption {
	return func(o *GenerateOptions) { o.NormalizeFunctions = v }
}

// LeadingComma enables leading commas in pretty mode.
func LeadingComma(b bool) GenOption { return func(o *GenerateOptions) { o.LeadingComma = b } }

// MaxTextWidth sets the pretty printer max segment width.
func MaxTextWidth(n int) GenOption { return func(o *GenerateOptions) { o.MaxTextWidth = n } }

// PadWidth sets the pretty printer pad.
func PadWidth(n int) GenOption { return func(o *GenerateOptions) { o.Pad = n } }

// IndentWidth sets the pretty printer indent.
func IndentWidth(n int) GenOption { return func(o *GenerateOptions) { o.Indent = n } }

// MaxUnsupported sets how many unsupported messages are reported.
func MaxUnsupported(n int) GenOption { return func(o *GenerateOptions) { o.MaxUnsupported = n } }

// GenOpts builds GenerateOptions from a dialect followed by GenOptions (or a GenerateOptions).
func GenOpts(args ...any) GenerateOptions {
	opts := DefaultGenerateOptions()
	for _, a := range args {
		switch x := a.(type) {
		case GenOption:
			x(&opts)
		case []GenOption:
			for _, o := range x {
				o(&opts)
			}
		case GenerateOptions:
			opts = x
		case *GenerateOptions:
			opts = *x
		default:
			opts.Dialect = x
		}
	}
	return opts
}

// SQL generates SQL for the expression. args may contain a dialect (name or dialect value)
// and GenOptions, e.g. e.SQL("duckdb", exp.Pretty(true)). Generation errors panic; use
// Generate to handle them.
func (e *Expr) SQL(args ...any) string {
	sql, err := e.Generate(args...)
	if err != nil {
		panic(err)
	}
	return sql
}

// Generate is SQL returning errors instead of panicking.
func (e *Expr) Generate(args ...any) (string, error) {
	return mustBackend().Generate(e, GenOpts(args...))
}

// QueryModifiers mirrors exp.QUERY_MODIFIERS (ordered).
var QueryModifiers = []string{
	"match", "laterals", "joins", "connect", "pivots", "prewhere", "where", "group", "having",
	"qualify", "windows", "distribute", "sort", "cluster", "order", "limit", "offset", "locks",
	"sample", "settings", "format", "options", "for_",
}

// TimestampParts mirrors exp.TIMESTAMP_PARTS.
var TimestampParts = []string{"year", "month", "day", "hour", "min", "sec", "nano"}
