package pyrt

// Bindings for sqlglot.diff.

import (
	"fmt"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// EditV is a diff edit (Insert, Remove, Move, Update or Keep), compared structurally like the
// Python dataclasses.
type EditV struct {
	Kind string
	// Expression for Insert/Remove; Source and Target for the others.
	A, B *exp.Expr
}

func editValue(e sqlglot.Edit) *EditV {
	switch x := e.(type) {
	case sqlglot.Insert:
		return &EditV{"Insert", x.Expression, nil}
	case sqlglot.Remove:
		return &EditV{"Remove", x.Expression, nil}
	case sqlglot.Move:
		return &EditV{"Move", x.Source, x.Target}
	case sqlglot.Update:
		return &EditV{"Update", x.Source, x.Target}
	case sqlglot.Keep:
		return &EditV{"Keep", x.Source, x.Target}
	}
	panic(fmt.Sprintf("unknown edit %T", e))
}

func (e *EditV) single() bool { return e.Kind == "Insert" || e.Kind == "Remove" }

// PyAttr implements Attrer.
func (e *EditV) PyAttr(name string) (Value, bool) {
	switch name {
	case "expression":
		if e.single() {
			return e.A, true
		}
	case "source":
		if !e.single() {
			return e.A, true
		}
	case "target":
		if !e.single() {
			return e.B, true
		}
	}
	return nil, false
}

// PyEq implements ==.
func (e *EditV) PyEq(o Value) (bool, bool) {
	x, ok := o.(*EditV)
	if !ok {
		return false, true
	}
	if x.Kind != e.Kind || !e.A.Equal(x.A) {
		return false, true
	}
	return e.single() || e.B.Equal(x.B), true
}

// PyHash makes edits hashable.
func (e *EditV) PyHash() string {
	h := fmt.Sprintf("edit:%s:%d", e.Kind, e.A.Hash())
	if !e.single() {
		h += fmt.Sprintf(":%d", e.B.Hash())
	}
	return h
}

// String implements repr().
func (e *EditV) String() string {
	if e.single() {
		return fmt.Sprintf("%s(expression=%s)", e.Kind, e.A.Repr())
	}
	return fmt.Sprintf("%s(source=%s, target=%s)", e.Kind, e.A.Repr(), e.B.Repr())
}

func editClass(kind string) *BuiltinType {
	return &BuiltinType{
		Name:  kind,
		Check: func(v Value) bool { x, ok := norm(v).(*EditV); return ok && x.Kind == kind },
		New: func(a Args) Value {
			if kind == "Insert" || kind == "Remove" {
				e, _ := norm(a.Get(0, "expression", nil)).(*exp.Expr)
				return &EditV{kind, e, nil}
			}
			s, _ := norm(a.Get(0, "source", nil)).(*exp.Expr)
			t, _ := norm(a.Get(1, "target", nil)).(*exp.Expr)
			return &EditV{kind, s, t}
		},
	}
}

func init() {
	for _, k := range []string{"Insert", "Remove", "Move", "Update", "Keep"} {
		editClasses[k] = editClass(k)
		globals["sqlglot.diff."+k] = editClasses[k]
	}
	diff := Func("diff", func(a Args) Value {
		return Guard(func() Value {
			o := sqlglot.DiffOptions{DeltaOnly: truthArg(a, 3, "delta_only", false), Dialect: dialectArg(a.Get(-1, "dialect", nil))}
			if f := a.Get(-1, "f", nil); f != nil {
				o.F, _ = toFloat(f)
			}
			if t := a.Get(-1, "t", nil); t != nil {
				o.T, _ = toFloat(t)
			}
			if m := a.Get(2, "matchings", nil); m != nil {
				for _, pair := range Iter(m) {
					s, _ := norm(Item(pair, 0)).(*exp.Expr)
					t, _ := norm(Item(pair, 1)).(*exp.Expr)
					o.Matchings = append(o.Matchings, [2]*exp.Expr{s, t})
				}
			}
			source, _ := norm(a.Pos[0]).(*exp.Expr)
			target, _ := norm(a.Pos[1]).(*exp.Expr)
			edits, err := sqlglot.Diff(source, target, o)
			check(err)
			out := make([]Value, len(edits))
			for i, e := range edits {
				out[i] = editValue(e)
			}
			return &List{out}
		})
	})
	globals["sqlglot.diff.diff"] = diff
	sqlglotModule.Attrs["diff"] = diff
}

var editClasses = map[string]*BuiltinType{}

// PyType implements type(edit).
func (e *EditV) PyType() Value { return editClasses[e.Kind] }
