package exp

import "testing"

// TestSerdeRegisteredKind is the Go equivalent of test_serde.py's test_custom_expression,
// which subclasses exp.Expression at module level.
func TestSerdeRegisteredKind(t *testing.T) {
	custom := RegisterKind(KindSpec{Name: "CustomExpressionSerdeTest", Args: []ArgSpec{{Key: "this"}}})
	for _, before := range []*Expr{New(custom), New(custom, "this", NewColumn("a"))} {
		data, err := DumpJSON(before)
		if err != nil {
			t.Fatal(err)
		}
		after, err := LoadJSON(data)
		if err != nil {
			t.Fatal(err)
		}
		if !before.Equal(after) || after.Kind() != custom {
			t.Fatalf("round trip changed %s into %s", before.Repr(), after.Repr())
		}
	}
}

func TestSerdeMixedValues(t *testing.T) {
	before := New(JSONPathUnion, "expressions", []any{New(JSONPathWildcard), "y", 1})
	before.Meta()["x"] = 1
	before.Comments = []string{"c"}
	data, err := DumpJSON(before)
	if err != nil {
		t.Fatal(err)
	}
	after, err := LoadJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if before.Repr() != after.Repr() || after.MetaGet("x") != 1 || len(after.Comments) != 1 {
		t.Fatalf("round trip changed %s into %s (meta %v)", before.Repr(), after.Repr(), after.Meta())
	}
}
