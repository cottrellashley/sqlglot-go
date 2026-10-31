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
	"github.com/cottrellashley/sqlglot-go/tokens"
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

// TestRegisterKind is the Go equivalent of test_generator.py's test_fallback_function_sql and
// test_fallback_function_var_args_sql, which subclass exp.Func inline.
func TestRegisterKind(t *testing.T) {
	specialUdf := exp.RegisterKind(exp.KindSpec{
		Name:  "SpecialUdfTest",
		Args:  []exp.ArgSpec{{Key: "a", Required: true}, {Key: "b"}},
		Bases: []exp.Kind{exp.Func},
	})
	specialUdfVarArgs := exp.RegisterKind(exp.KindSpec{
		Name:     "SpecialUdfVarArgsTest",
		Args:     []exp.ArgSpec{{Key: "a", Required: true}, {Key: "expressions"}},
		Bases:    []exp.Kind{exp.Func},
		SQLNames: []string{"SPECIAL_UDF"},
		VarLen:   true,
	})
	if !specialUdf.IsA(exp.Func) || !specialUdf.IsA(exp.Condition) || specialUdf.String() != "SpecialUdfTest" {
		t.Fatalf("bad kind %v", specialUdf)
	}

	for _, tc := range []struct {
		kind exp.Kind
		name string
		sql  string
		want string
	}{
		{specialUdf, "SPECIAL_UDF_TEST", "SELECT SPECIAL_UDF_TEST(a) FROM x", "SELECT SPECIAL_UDF_TEST(a) FROM x"},
		{specialUdfVarArgs, "SPECIAL_UDF", "SELECT SPECIAL_UDF(a, b, c, d + 1) FROM x", "SELECT SPECIAL_UDF(a, b, c, d + 1) FROM x"},
	} {
		c := snowflake.New()
		c.Parser.Functions[tc.name] = sqlglot.FromArgList(tc.kind)
		e, err := sqlglot.ParseOne(tc.sql, c)
		if err != nil {
			t.Fatal(err)
		}
		if e.Find(tc.kind) == nil {
			t.Fatalf("%s: no %v node in %s", tc.sql, tc.kind, e.Repr())
		}
		if got := e.SQL(); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
		if got, err := e.Generate("duckdb"); err != nil || got != tc.want {
			t.Fatalf("duckdb: got %q (%v), want %q", got, err, tc.want)
		}
	}

	if got := exp.New(specialUdfVarArgs, "a", exp.NewColumn("a"), "expressions", []*exp.Expr{exp.NewColumn("b")}).SQL(); got != "SPECIAL_UDF(a, b)" {
		t.Fatalf("got %q", got)
	}
}

// TestReadmeDialect checks the minimal dialect of the README.
func TestReadmeDialect(t *testing.T) {
	sqlglot.Register("readmedialect", func() *sqlglot.DialectClass {
		c := sqlglot.NewDialectClass("readmedialect", sqlglot.BaseDialectClass())
		c.ClassName = "ReadmeDialect"
		c.NormalizationStrategy = sqlglot.Uppercase
		c.Tokenizer.Identifiers = []tokens.Delim{tokens.Q("`")}
		c.Tokenizer.Keywords["MINUS"] = tokens.Except
		c.Parser.Functions["STRLEN"] = sqlglot.FromArgList(exp.Length)
		c.Generator.Transforms[exp.Length] = sqlglot.RenameFunc("STRLEN")
		c.Generator.TypeMapping[exp.DTypeText] = "STRING"
		return c
	})
	for _, tc := range []struct{ sql, read, write, want string }{
		{"SELECT LENGTH(`a b`), CAST(x AS TEXT) FROM t", "readmedialect", "duckdb", `SELECT LENGTH("a b"), CAST(x AS TEXT) FROM t`},
		{"SELECT STRLEN(a) FROM t MINUS SELECT 1", "readmedialect", "duckdb", "SELECT LENGTH(a) FROM t EXCEPT SELECT 1"},
		{`SELECT LENGTH("a b"), CAST(x AS TEXT) FROM t`, "duckdb", "readmedialect", "SELECT STRLEN(`a b`), CAST(x AS STRING) FROM t"},
	} {
		out, err := sqlglot.Transpile(tc.sql, tc.read, tc.write)
		if err != nil {
			t.Fatal(err)
		}
		if out[0] != tc.want {
			t.Errorf("%s: got %s, want %s", tc.sql, out[0], tc.want)
		}
	}
}
