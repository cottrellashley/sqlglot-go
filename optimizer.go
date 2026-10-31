package sqlglot

// Port of sqlglot/optimizer/optimizer.py.

import (
	"github.com/cottrellashley/sqlglot-go/exp"
)

// OptimizeOptions mirrors the keyword arguments of optimizer.optimize. Python's optimize hands
// each rule the keyword arguments named in its signature; in Go every rule receives the
// options and reads the fields it accepts.
type OptimizeOptions struct {
	// Schema is a Schema or a mapping in one of the forms {table: {col: type}},
	// {db: {table: {col: type}}} or {catalog: {db: {table: {col: type}}}}.
	Schema any
	// DB and Catalog are the defaults set by USE DATABASE db / USE CATALOG c statements.
	DB      any
	Catalog any
	// Dialect is used to parse SQL string input and is passed to the rules that accept it.
	Dialect any
	// Rules is the sequence of rules to apply; nil means DefaultRules. Many rules require
	// qualified tables and columns, so don't drop RuleQualify unless you know what you're doing.
	Rules []Rule
	// SQL is the original SQL string, used for error highlighting.
	SQL string
	// NoCopy optimizes the input expression in place (copy=False). Has no effect on SQL input.
	NoCopy bool

	// Extra keyword arguments, passed to the rules that accept them. Optimize defaults
	// isolate_tables to true and quote_identifiers to false.
	NoIsolateTables           bool // isolate_tables=False
	QuoteIdentifiers          bool // quote_identifiers=True (qualify's own quoting step)
	NoExpandAliasRefs         bool
	NoExpandStars             bool
	NoQualifyColumns          bool
	AllowPartialQualification bool
	NoValidateQualifyColumns  bool
	NoIdentify                bool
	CanonicalizeTableAliases  bool
	OnQualify                 func(table *exp.Expr)
	LeaveTablesIsolated       bool
	MaxCopyFactor             *int
	MinCopyBudget             *int
	NoCopyBudget              bool
	Journal                   *Journal
	DNF                       bool
	MaxDistance               int // 0 means 128
	ExpressionMetadata        map[exp.Kind]*ExprMetadata
	CoercesTo                 map[exp.DType]exp.TypeSet
	NoOverwriteTypes          bool
	ConstantPropagation       bool
	CoalesceSimplification    bool
}

// Rule is an optimizer rule: it accepts an expression, reads the options it needs and returns
// the rewritten expression. Rules signal failures by panicking with an error (for example an
// *errs.OptimizeError), which Optimize returns.
type Rule func(e *exp.Expr, o *OptimizeOptions) *exp.Expr

// The rules of optimizer.RULES, plus the qualify sub-steps commonly used on their own.
var (
	RuleQualify Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr {
		return MustQualify(e, QualifyOptions{
			Dialect: o.Dialect, DB: o.DB, Catalog: o.Catalog, Schema: o.Schema,
			NoExpandAliasRefs: o.NoExpandAliasRefs, NoExpandStars: o.NoExpandStars,
			IsolateTables: !o.NoIsolateTables, NoQualifyColumns: o.NoQualifyColumns,
			AllowPartialQualification: o.AllowPartialQualification,
			NoValidateQualifyColumns:  o.NoValidateQualifyColumns,
			NoQuoteIdentifiers:        !o.QuoteIdentifiers, NoIdentify: o.NoIdentify,
			CanonicalizeTableAliases: o.CanonicalizeTableAliases, OnQualify: o.OnQualify, SQL: o.SQL,
		})
	}
	RuleQualifyTables Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr {
		return QualifyTables(e, QualifyTablesOptions{
			DB: o.DB, Catalog: o.Catalog, OnQualify: o.OnQualify, Dialect: o.Dialect,
			CanonicalizeTableAliases: o.CanonicalizeTableAliases,
		})
	}
	RuleQualifyColumns Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr {
		return QualifyColumns(e, o.Schema, QualifyColumnsOptions{
			NoExpandAliasRefs: o.NoExpandAliasRefs, NoExpandStars: o.NoExpandStars,
			AllowPartialQualification: o.AllowPartialQualification, Dialect: o.Dialect,
		})
	}
	RulePushdownProjections Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr {
		return PushdownProjections(e, o.Journal)
	}
	RuleNormalize Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr {
		maxDistance := o.MaxDistance
		if maxDistance == 0 {
			maxDistance = 128
		}
		return Normalize(e, o.DNF, maxDistance)
	}
	RuleUnnestSubqueries   Rule = func(e *exp.Expr, _ *OptimizeOptions) *exp.Expr { return UnnestSubqueries(e) }
	RulePushdownPredicates Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr {
		return PushdownPredicates(e, o.Dialect)
	}
	RuleOptimizeJoins       Rule = func(e *exp.Expr, _ *OptimizeOptions) *exp.Expr { return OptimizeJoins(e) }
	RuleEliminateSubqueries Rule = func(e *exp.Expr, _ *OptimizeOptions) *exp.Expr { return EliminateSubqueries(e) }
	RuleMergeSubqueries     Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr {
		return MergeSubqueries(e, MergeSubqueriesOptions{
			LeaveTablesIsolated: o.LeaveTablesIsolated, MaxCopyFactor: o.MaxCopyFactor,
			MinCopyBudget: o.MinCopyBudget, NoCopyBudget: o.NoCopyBudget,
		})
	}
	RuleEliminateJoins   Rule = func(e *exp.Expr, _ *OptimizeOptions) *exp.Expr { return EliminateJoins(e) }
	RuleEliminateCtes    Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr { return EliminateCtes(e, o.Journal) }
	RuleQuoteIdentifiers Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr {
		return QuoteIdentifiers(e, o.Dialect, !o.NoIdentify)
	}
	RuleAnnotateTypes Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr {
		return MustAnnotateTypesOpts(e, AnnotateOptions{
			Schema: o.Schema, ExpressionMetadata: o.ExpressionMetadata, CoercesTo: o.CoercesTo,
			Dialect: o.Dialect, NoOverwriteTypes: o.NoOverwriteTypes,
		})
	}
	RuleCanonicalize Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr { return Canonicalize(e, o.Dialect) }
	RuleSimplify     Rule = func(e *exp.Expr, o *OptimizeOptions) *exp.Expr {
		return SimplifyOpts(e, o.ConstantPropagation, o.CoalesceSimplification, o.Dialect)
	}
)

// DefaultRules mirrors optimizer.RULES. Rule order matters.
func DefaultRules() []Rule {
	return []Rule{
		RuleQualify,
		RulePushdownProjections,
		RuleNormalize,
		RuleUnnestSubqueries,
		RulePushdownPredicates,
		RuleOptimizeJoins,
		RuleEliminateSubqueries,
		RuleMergeSubqueries,
		RuleEliminateJoins,
		RuleEliminateCtes,
		RuleQuoteIdentifiers,
		RuleAnnotateTypes,
		RuleCanonicalize,
		RuleSimplify,
	}
}

// Optimize mirrors optimizer.optimize: it rewrites a sqlglot AST (or SQL string) into an
// optimized form. The result may be a new root even if NoCopy is set.
func Optimize(expression any, o OptimizeOptions) (optimized *exp.Expr, err error) {
	defer recoverOptimize(&err)
	schema, err := EnsureSchema(o.Schema, o.Dialect)
	if err != nil {
		return nil, err
	}
	o.Schema = schema
	rules := o.Rules
	if rules == nil {
		rules = DefaultRules()
	}
	optimized, err = exp.MaybeParse(expression, exp.ParseOptions{Dialect: o.Dialect}, "", !o.NoCopy)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		optimized = rule(optimized, &o)
	}
	return optimized, nil
}

// MustOptimize is Optimize that panics on error.
func MustOptimize(expression any, o OptimizeOptions) *exp.Expr {
	out, err := Optimize(expression, o)
	if err != nil {
		panic(err)
	}
	return out
}
