package sqlglot_test

import (
	"testing"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
)

func qualifyColumnsFixture(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
	opts := sqlglot.QualifyOptions{Schema: o.Schema, Dialect: dialectOrNil(o.Dialect), NoIdentify: true}
	if o.ValidateQualifyColumns != nil && !*o.ValidateQualifyColumns {
		opts.NoValidateQualifyColumns = true
	}
	if o.CanonicalizeTableAliases != nil {
		opts.CanonicalizeTableAliases = *o.CanonicalizeTableAliases
	}
	return sqlglot.Qualify(e, opts)
}

func dialectOrNil(d string) any {
	if d == "" {
		return nil
	}
	return d
}

func TestQualifyTablesFixtures(t *testing.T) {
	checkFile(t, "qualify_tables", false, nil, func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
		opts := sqlglot.QualifyTablesOptions{DB: "db", Catalog: "c", Dialect: dialectOrNil(o.Dialect)}
		if o.CanonicalizeTableAliases != nil {
			opts.CanonicalizeTableAliases = *o.CanonicalizeTableAliases
		}
		return sqlglot.QualifyTables(e, opts), nil
	})
}

func TestQualifyColumnsFixtures(t *testing.T) {
	checkFile(t, "qualify_columns", false, optimizerSchema(), qualifyColumnsFixture)
}

func TestQualifyColumnsDDLFixtures(t *testing.T) {
	checkFile(t, "qualify_columns_ddl", false, optimizerSchema(), qualifyColumnsFixture)
}

func TestIsolateTableSelectsFixtures(t *testing.T) {
	checkFile(t, "isolate_table_selects", false, optimizerSchema(), func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
		s, err := sqlglot.EnsureSchema(o.Schema, dialectOrNil(o.Dialect))
		if err != nil {
			return nil, err
		}
		return sqlglot.IsolateTableSelects(e, s, nil), nil
	})
}

func TestNormalizeIdentifiersFixtures(t *testing.T) {
	checkFile(t, "normalize_identifiers", false, nil, func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
		return sqlglot.NormalizeIdentifiers(e, dialectOrNil(o.Dialect)), nil
	})
}

func TestQuoteIdentifiersFixtures(t *testing.T) {
	checkFile(t, "quote_identifiers", false, nil, func(e *exp.Expr, o fixtureOpts) (*exp.Expr, error) {
		return sqlglot.QuoteIdentifiers(e, dialectOrNil(o.Dialect), true), nil
	})
}
