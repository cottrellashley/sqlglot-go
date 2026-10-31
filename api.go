package sqlglot

// The top-level API (sqlglot/__init__.py) and the backend that connects package exp to the
// parser, generator and dialects.

import (
	"fmt"
	"strings"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func init() { exp.SetBackend(backend{}) }

type backend struct{}

func (backend) ParseOne(sql string, opts exp.ParseOptions) (*exp.Expr, error) {
	return ParseOneOpts(sql, opts.Dialect, ParserOptions{
		ErrorLevel:          opts.ErrorLevel,
		ErrorMessageContext: opts.ErrorMessageContext,
		MaxErrors:           opts.MaxErrors,
	}, opts.Into...)
}

func (backend) Generate(e *exp.Expr, opts exp.GenerateOptions) (string, error) {
	d, err := GetDialect(opts.Dialect)
	if err != nil {
		return "", err
	}
	return d.Generate(e, opts)
}

func (backend) BuildFunc(name string, args []*exp.Expr, kwargs []exp.Arg, dialect any) (*exp.Expr, error) {
	d, err := GetDialect(dialect)
	if err != nil {
		return nil, err
	}
	return BuildFunc(name, args, kwargs, d)
}

func (backend) TypeMapping(dialect any) map[exp.DType]string {
	d, err := GetDialect(dialect)
	if err != nil {
		return nil
	}
	return d.Generator.TypeMapping
}

func (backend) Hook(name string, args ...any) (any, error) {
	return nil, fmt.Errorf("unknown backend hook %q", name)
}

// BuildFunc mirrors the body of exp.func: it builds a function expression by name using the
// dialect's FUNCTIONS table, falling back to exp.Anonymous.
func BuildFunc(name string, converted []*exp.Expr, kwargs []exp.Arg, d *Dialect) (fn *exp.Expr, err error) {
	defer recoverParse(&err)
	if constructor, ok := d.Parser.Functions[strings.ToUpper(name)]; ok {
		if len(converted) > 0 {
			fn = constructor(converted, d)
		} else if kind, ok := exp.FunctionByName(strings.ToUpper(name)); ok {
			// Python uses the class of a from_arg_list constructor; builders don't expose their
			// class here, so FUNCTION_BY_NAME is used for both cases.
			kv := make([]any, 0, len(kwargs)*2)
			for _, a := range kwargs {
				kv = append(kv, a.Key, a.Value)
			}
			fn = exp.New(kind, kv...)
		} else {
			return nil, errs.Valuef("Unable to convert '%s' into a Func. Either manually construct "+
				"the Func expression of interest or parse the function call.", name)
		}
	} else {
		kv := []any{"this", name}
		if len(kwargs) > 0 {
			for _, a := range kwargs {
				kv = append(kv, a.Key, a.Value)
			}
		} else {
			kv = append(kv, "expressions", converted)
		}
		fn = exp.New(exp.Anonymous, kv...)
	}
	if fn == nil {
		return nil, errs.Valuef("Unable to convert '%s' into a Func.", name)
	}
	if msgs := fn.ErrorMessages(converted); len(msgs) > 0 {
		return nil, errs.Valuef("%s", msgs[0])
	}
	return fn, nil
}

// Tokenize mirrors sqlglot.tokenize(sql, read=None, dialect=None).
func Tokenize(sql string, dialect any) ([]*tokens.Token, error) {
	d, err := GetDialect(dialect)
	if err != nil {
		return nil, err
	}
	return d.Tokenize(sql)
}

// Parse mirrors sqlglot.parse(sql, read=None, dialect=None, **opts): it parses a string into
// a list of syntax trees, one per statement.
func Parse(sql string, dialect any) ([]*exp.Expr, error) {
	return ParseOpts(sql, dialect, ParserOptions{ErrorLevel: errs.Unset})
}

// ParseOpts is Parse with explicit parser options.
func ParseOpts(sql string, dialect any, opts ParserOptions) ([]*exp.Expr, error) {
	d, err := GetDialect(dialect)
	if err != nil {
		return nil, err
	}
	return d.Parse(sql, opts)
}

// ParseOne mirrors sqlglot.parse_one(sql, read=None, dialect=None, into=None): it parses a
// string into a single syntax tree (the first statement).
func ParseOne(sql string, dialect any, into ...exp.Kind) (*exp.Expr, error) {
	return ParseOneOpts(sql, dialect, ParserOptions{ErrorLevel: errs.Unset}, into...)
}

// MustParseOne is ParseOne that panics on error.
func MustParseOne(sql string, dialect any, into ...exp.Kind) *exp.Expr {
	e, err := ParseOne(sql, dialect, into...)
	if err != nil {
		panic(err)
	}
	return e
}

// ParseOneOpts is ParseOne with explicit parser options.
func ParseOneOpts(sql string, dialect any, opts ParserOptions, into ...exp.Kind) (*exp.Expr, error) {
	d, err := GetDialect(dialect)
	if err != nil {
		return nil, err
	}
	var result []*exp.Expr
	if len(into) > 0 {
		result, err = d.ParseInto(into, sql, opts)
	} else {
		result, err = d.Parse(sql, opts)
	}
	if err != nil {
		return nil, err
	}
	for _, e := range result {
		if e == nil {
			break
		}
		return e, nil
	}
	return nil, &errs.ParseError{Message: fmt.Sprintf("No expression was parsed from '%s'", sql)}
}

// ParseOneInto parses sql into the given expression kind (parse_one(sql, into=kind)).
func ParseOneInto(sql string, kind exp.Kind, d *Dialect) (*exp.Expr, error) {
	return ParseOneOpts(sql, d, ParserOptions{ErrorLevel: errs.Unset}, kind)
}

// Transpile mirrors sqlglot.transpile(sql, read=None, write=None, identity=True, error_level=None,
// **opts): it parses sql with the read dialect and generates each statement in the write
// dialect. Generator options are exp.GenOption values (exp.Pretty(true), ...).
func Transpile(sql string, read, write any, opts ...exp.GenOption) ([]string, error) {
	return TranspileOpts(sql, read, write, ParserOptions{ErrorLevel: errs.Unset}, opts...)
}

// TranspileOpts is Transpile with explicit parser options.
func TranspileOpts(sql string, read, write any, popts ParserOptions, opts ...exp.GenOption) ([]string, error) {
	if write == nil {
		write = read
	}
	rd, err := GetDialect(read)
	if err != nil {
		return nil, err
	}
	wd, err := GetDialect(write)
	if err != nil {
		return nil, err
	}
	stmts, err := rd.Parse(sql, popts)
	if err != nil {
		return nil, err
	}
	gopts := exp.DefaultGenerateOptions()
	for _, o := range opts {
		o(&gopts)
	}
	gopts.NoCopy = true
	out := make([]string, 0, len(stmts))
	for _, s := range stmts {
		if s == nil {
			out = append(out, "")
			continue
		}
		sql, err := wd.Generate(s, gopts)
		if err != nil {
			return nil, err
		}
		out = append(out, sql)
	}
	return out, nil
}
