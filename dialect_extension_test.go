package sqlglot_test

// Go equivalents of the test_dialect.py cases that subclass dialect classes inline
// (test_custom_dialect, test_patch_dialect_parser and the custom settings part of
// test_get_or_raise). They show the intended way to add or extend a dialect: derive a
// DialectClass from an existing one, override its tables, and register it.

import (
	"reflect"
	"testing"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/dialects/snowflake"
	"github.com/cottrellashley/sqlglot-go/exp"
)

func TestCustomDialect(t *testing.T) {
	newDialect := func() *sqlglot.DialectClass {
		c := sqlglot.NewDialectClass("mydialect", nil)
		c.Parser = snowflake.New().Parser.Clone()
		c.Parser.Functions["DOUBLE_IT"] = func(args []*exp.Expr, _ *sqlglot.Dialect) *exp.Expr {
			arg := exp.NewNull()
			if len(args) > 0 {
				arg = args[0]
			}
			return exp.New(exp.Mul, "this", exp.LiteralNumber(2), "expression", arg)
		}
		return c
	}

	// A dialect class can be used directly...
	result, err := sqlglot.ParseOne("SELECT DOUBLE_IT(5)", newDialect())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Expressions()[0].IsKind(exp.Mul) {
		t.Fatalf("expected a Mul, got %s", result.Expressions()[0].Repr())
	}
	if got := result.SQL(); got != "SELECT 2 * 5" {
		t.Fatalf("got %q", got)
	}

	// ...or registered under a name, like the built-in dialects.
	sqlglot.Register("mydialect", newDialect)
	out, err := sqlglot.Transpile("SELECT DOUBLE_IT(5)", "mydialect", "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	if out[0] != "SELECT 2 * 5" {
		t.Fatalf("got %q", out[0])
	}
}

func TestPatchDialectParser(t *testing.T) {
	c := snowflake.New()
	c.Parser.Functions["MY_CUSTOM_FUNC"] = sqlglot.FromArgList(exp.Length)

	result, err := sqlglot.ParseOne("SELECT 1", c)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsKind(exp.Select) {
		t.Fatalf("expected a Select, got %s", result.Repr())
	}
	result, err = sqlglot.ParseOne("SELECT MY_CUSTOM_FUNC(a)", c)
	if err != nil {
		t.Fatal(err)
	}
	if result.Find(exp.Length) == nil {
		t.Fatalf("expected a Length in %s", result.Repr())
	}

	// The registered Snowflake dialect is unaffected.
	result, err = sqlglot.ParseOne("SELECT MY_CUSTOM_FUNC(a)", "snowflake")
	if err != nil {
		t.Fatal(err)
	}
	if result.Find(exp.Length) != nil {
		t.Fatal("patching a copy of the class changed the registered dialect")
	}
}

func TestCustomDialectSettings(t *testing.T) {
	sqlglot.Register("mysettingsdialect", func() *sqlglot.DialectClass {
		c := sqlglot.NewDialectClass("mysettingsdialect", nil)
		c.SupportedSettings = map[string]bool{"s1": true, "s2": true, "s3": true, "s4": true, "s5": true}
		return c
	})
	d, err := sqlglot.GetDialect("mysettingsdialect, s1=TruE, s2=1, s3=FaLse, s4=0, s5=nonbool")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"s1": true, "s2": true, "s3": false, "s4": false, "s5": "nonbool"}
	if !reflect.DeepEqual(d.Settings, want) {
		t.Fatalf("got %v", d.Settings)
	}
	if _, err := sqlglot.GetDialect("mysettingsdialect, unknown=1"); err == nil {
		t.Fatal("expected an error for an unsupported setting")
	}
}
