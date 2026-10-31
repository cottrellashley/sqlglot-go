package sqlglot_test

import (
	"testing"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// customSchema is a minimal Schema implementation: every table has one INT column "a".
type customSchema struct{}

func (customSchema) Dialect() *sqlglot.Dialect { return nil }
func (customSchema) AddTable(table any, columnMapping any, dialect any, normalize *bool, matchDepth bool) error {
	return nil
}
func (customSchema) ColumnNames(table any, onlyVisible bool, dialect any, normalize *bool) ([]string, error) {
	return []string{"a"}, nil
}
func (customSchema) GetColumnType(table any, column any, dialect any, normalize *bool) (*exp.Expr, error) {
	return exp.DataTypeBuild("int", nil, false)
}
func (customSchema) HasColumn(table any, column any, dialect any, normalize *bool) (bool, error) {
	return true, nil
}
func (customSchema) GetUDFType(udf any, dialect any, normalize *bool) (*exp.Expr, error) {
	return exp.DTypeUnknown.IntoExpr(), nil
}
func (customSchema) SupportedTableArgs() ([]string, error) { return []string{"this"}, nil }
func (customSchema) Empty() bool                            { return false }

// TestCustomSchema is the Go equivalent of test_schema.py's test_custom_schema, which
// subclasses Schema inline.
func TestCustomSchema(t *testing.T) {
	var schema sqlglot.Schema = customSchema{}
	e, err := sqlglot.ParseOne("SELECT * FROM t", nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err = sqlglot.Qualify(e, sqlglot.QualifyOptions{Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	e, err = sqlglot.AnnotateTypesOpts(e, sqlglot.AnnotateOptions{Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.SQL(); got != `SELECT "t"."a" AS "a" FROM "t" AS "t"` {
		t.Fatalf("got %s", got)
	}
	if got := e.Selects()[0].Type().SQL(); got != "INT" {
		t.Fatalf("got type %s", got)
	}
}
