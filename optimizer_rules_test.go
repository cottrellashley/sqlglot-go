package sqlglot_test

import (
	"os"
	"path/filepath"
	"testing"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
)

func schemaFile(t *testing.T, name string) *sqlglot.SchemaMapping {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return jsonSchema(t, string(b))
}

// optimizeFixture mirrors calling optimizer.optimize (or a partial of it with rules) with the
// fixture's keyword arguments.
func optimizeFixture(rules []sqlglot.Rule) func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
	return func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
		opts := sqlglot.OptimizeOptions{Schema: o.Schema, Dialect: dialectOrNil(o.Dialect), Rules: rules}
		if o.LeaveTablesIsolated != nil {
			opts.LeaveTablesIsolated = *o.LeaveTablesIsolated
		}
		if o.ValidateQualifyColumns != nil {
			opts.NoValidateQualifyColumns = !*o.ValidateQualifyColumns
		}
		if o.CanonicalizeTableAliases != nil {
			opts.CanonicalizeTableAliases = *o.CanonicalizeTableAliases
		}
		return sqlglot.Optimize(e, opts)
	}
}

func TestOptimizerFixtures(t *testing.T) {
	M := sqlglot.M
	schema := M(
		"x", M("a", "INT", "b", "INT"),
		"y", M("b", "INT", "c", "INT"),
		"z", M("a", "INT", "c", "INT"),
		"u", M("f", "INT", "g", "INT", "h", "TEXT"),
	)
	checkFile(t, "optimizer", true, schema, optimizeFixture(nil))
}

func TestTPCHFixtures(t *testing.T) {
	checkFile(t, "tpc-h/tpc-h", true, schemaFile(t, "tpch_schema.json"), optimizeFixture(nil))
}

func TestTPCDSFixtures(t *testing.T) {
	checkFile(t, "tpc-ds/tpc-ds", true, schemaFile(t, "tpcds_schema.json"), optimizeFixture(nil))
}

func TestMergeSubqueriesFixtures(t *testing.T) {
	checkFile(t, "merge_subqueries", false, optimizerSchema(), optimizeFixture([]sqlglot.Rule{
		sqlglot.RuleQualifyTables, sqlglot.RuleQualifyColumns, sqlglot.RuleMergeSubqueries,
	}))
}

func TestCanonicalizeFixtures(t *testing.T) {
	checkFile(t, "canonicalize", false, optimizerSchema(), optimizeFixture([]sqlglot.Rule{
		sqlglot.RuleQualify, sqlglot.RuleQuoteIdentifiers, sqlglot.RuleAnnotateTypes, sqlglot.RuleCanonicalize,
	}))
}

func TestOptimizeJoinsFixtures(t *testing.T) {
	checkFile(t, "optimize_joins", false, nil, func(e *exp.Expr, _ fixtureOpts) (*exp.Expr, error) {
		return sqlglot.OptimizeJoins(e), nil
	})
}

func TestEliminateJoinsFixtures(t *testing.T) {
	checkFile(t, "eliminate_joins", true, nil, func(e *exp.Expr, _ fixtureOpts) (*exp.Expr, error) {
		return sqlglot.EliminateJoins(e), nil
	})
}

func TestEliminateCtesFixtures(t *testing.T) {
	checkFile(t, "eliminate_ctes", true, nil, func(e *exp.Expr, _ fixtureOpts) (*exp.Expr, error) {
		return sqlglot.EliminateCtes(e, nil), nil
	})
}

func TestEliminateSubqueriesFixtures(t *testing.T) {
	checkFile(t, "eliminate_subqueries", false, nil, func(e *exp.Expr, _ fixtureOpts) (*exp.Expr, error) {
		return sqlglot.EliminateSubqueries(e), nil
	})
}

func TestPushdownPredicatesFixtures(t *testing.T) {
	checkFile(t, "pushdown_predicates", false, nil, func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
		return sqlglot.PushdownPredicates(e, dialectOrNil(o.Dialect)), nil
	})
}

func TestUnnestSubqueriesFixtures(t *testing.T) {
	checkFile(t, "unnest_subqueries", false, nil, func(e *exp.Expr, _ fixtureOpts) (*exp.Expr, error) {
		return sqlglot.UnnestSubqueries(e), nil
	})
}

// TestPushdownProjectionsFixtures mirrors test_optimizer's pushdown_projections helper.
func TestPushdownProjectionsFixtures(t *testing.T) {
	checkFile(t, "pushdown_projections", false, optimizerSchema(), func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
		e = sqlglot.QualifyTables(e, sqlglot.QualifyTablesOptions{})
		e = sqlglot.NormalizeIdentifiers(e, dialectOrNil(o.Dialect))
		e = sqlglot.QualifyColumns(e, o.Schema, sqlglot.QualifyColumnsOptions{Dialect: dialectOrNil(o.Dialect)})
		return sqlglot.PushdownProjections(e, nil), nil
	})
}

// TestNormalizeFixtures mirrors test_optimizer's normalize helper.
func TestNormalizeFixtures(t *testing.T) {
	checkFile(t, "normalize", false, optimizerSchema(), func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
		e = sqlglot.Normalize(e, false, 128)
		e, err := sqlglot.AnnotateTypesOpts(e, sqlglot.AnnotateOptions{Schema: o.Schema})
		if err != nil {
			return nil, err
		}
		return sqlglot.Simplify(e, nil), nil
	})
}

func TestPushdownCteAliasColumnsFixtures(t *testing.T) {
	checkFile(t, "pushdown_cte_alias_columns", false, nil, func(e *exp.Expr, _ fixtureOpts) (*exp.Expr, error) {
		sqlglot.PushdownCteAliasColumns(sqlglot.BuildScope(e))
		return e, nil
	})
}

// TestCanonicalizeInternalNamesFixtures mirrors test_optimizer's qualify_then_canonicalize.
func TestCanonicalizeInternalNamesFixtures(t *testing.T) {
	M := sqlglot.M
	schema := optimizerSchema()
	schema.Set("jtbl", M("j", "JSON"))
	schema.Set("pvt", M("c", "TEXT", "v", "INT"))
	checkFile(t, "canonicalize_internal_names", false, schema, func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
		opts := sqlglot.QualifyOptions{Schema: o.Schema, Dialect: dialectOrNil(o.Dialect), Catalog: "c", DB: "db"}
		if o.ValidateQualifyColumns != nil {
			opts.NoValidateQualifyColumns = !*o.ValidateQualifyColumns
		}
		if o.CanonicalizeTableAliases != nil {
			opts.CanonicalizeTableAliases = *o.CanonicalizeTableAliases
		}
		e, err := sqlglot.Qualify(e, opts)
		if err != nil {
			return nil, err
		}
		return sqlglot.CanonicalizeInternalNames(e), nil
	})
}
