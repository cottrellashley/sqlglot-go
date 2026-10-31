package exp

import (
	"fmt"
	"strings"
	"sync"

	"github.com/cottrellashley/sqlglot-go/errs"
)

// DType is a data type enum value (sqlglot's exp.DType / exp.DataType.Type). The string is
// the enum value (e.g. "BIGINT", "USER-DEFINED").
type DType string

// String returns the enum value.
func (d DType) String() string { return string(d) }

// Name returns the Python enum member name (USER-DEFINED -> USERDEFINED).
func (d DType) Name() string {
	if d == DTypeUserdefined {
		return "USERDEFINED"
	}
	return string(d)
}

// IntoExpr mirrors DType.into_expr: DataType(this=self, **kwargs).
func (d DType) IntoExpr(kv ...any) *Expr {
	e := New(DataType, "this", d)
	return e.SetKwargs(kv...)
}

var dtypeByName = func() map[string]DType {
	m := map[string]DType{}
	for _, d := range AllDTypes {
		m[d.Name()] = d
		m[string(d)] = d
	}
	return m
}()

// DTypeByName looks a DType up by member name or value.
func DTypeByName(name string) (DType, bool) {
	d, ok := dtypeByName[name]
	return d, ok
}

// TypeSet is an immutable-by-convention set of data types.
type TypeSet map[DType]struct{}

// NewDTypeSet builds a set.
func NewTypeSet(types ...DType) TypeSet {
	s := make(TypeSet, len(types))
	for _, t := range types {
		s[t] = struct{}{}
	}
	return s
}

// Has reports membership.
func (s TypeSet) Has(t DType) bool { _, ok := s[t]; return ok }

// Union returns a new set with the members of both sets.
func (s TypeSet) Union(others ...TypeSet) TypeSet {
	out := make(TypeSet, len(s))
	for t := range s {
		out[t] = struct{}{}
	}
	for _, o := range others {
		for t := range o {
			out[t] = struct{}{}
		}
	}
	return out
}

// With returns a new set with extra members.
func (s TypeSet) With(types ...DType) TypeSet { return s.Union(NewTypeSet(types...)) }

// Without returns a new set without the given members.
func (s TypeSet) Without(types ...DType) TypeSet {
	out := s.Union()
	for _, t := range types {
		delete(out, t)
	}
	return out
}

// Slice returns the members (in AllDTypes order).
func (s TypeSet) Slice() []DType {
	var out []DType
	for _, d := range AllDTypes {
		if s.Has(d) {
			out = append(out, d)
		}
	}
	return out
}

// DataTypeBuild mirrors exp.DataType.build. dtype may be a string, a DType, a DataType
// expression, or (with udt) an Identifier/Dot. kv are extra args set on the result.
func DataTypeBuild(dtype any, dialect any, udt bool, kv ...any) (*Expr, error) {
	switch d := dtype.(type) {
	case string:
		return dataTypeFromStr(d, dialect, udt, kv...)
	case DType:
		e := New(DataType, "this", d)
		return e.SetKwargs(kv...), nil
	case *Expr:
		if d.IsAny(Identifier, Dot) && udt {
			args := append([]any{"this", DTypeUserdefined, "kind", d}, kv...)
			return New(DataType, args...), nil
		}
		if d.Is(DataType) {
			return d.Copy(), nil
		}
	}
	return nil, errs.Valuef("Invalid data type: %T. Expected str or DType", dtype)
}

// DataTypeBuildNoCopy is DataTypeBuild with copy=False for DataType inputs.
func DataTypeBuildNoCopy(dtype any, dialect any, udt bool) (*Expr, error) {
	if d, ok := dtype.(*Expr); ok && d != nil && d.Is(DataType) {
		return d, nil
	}
	return DataTypeBuild(dtype, dialect, udt)
}

// MustDataTypeBuild is DataTypeBuild that panics on error.
func MustDataTypeBuild(dtype any, dialect any, udt bool, kv ...any) *Expr {
	e, err := DataTypeBuild(dtype, dialect, udt, kv...)
	if err != nil {
		panic(err)
	}
	return e
}

func dataTypeFromStr(dtype string, dialect any, udt bool, kv ...any) (*Expr, error) {
	if strings.ToUpper(dtype) == "UNKNOWN" {
		args := append([]any{"this", DTypeUnknown}, kv...)
		return New(DataType, args...), nil
	}
	e, err := ParseOne(dtype, ParseOptions{Dialect: dialect, Into: []Kind{DataType}, ErrorLevel: errs.Ignore})
	if err != nil {
		if _, ok := err.(*errs.ParseError); ok && udt {
			args := append([]any{"this", DTypeUserdefined, "kind", dtype}, kv...)
			return New(DataType, args...), nil
		}
		return nil, err
	}
	if e == nil {
		if udt {
			args := append([]any{"this", DTypeUserdefined, "kind", dtype}, kv...)
			return New(DataType, args...), nil
		}
		return nil, &errs.ParseError{Message: fmt.Sprintf("Failed to parse data type %q", dtype)}
	}
	return e.SetKwargs(kv...), nil
}

var isTypeCache sync.Map // string -> *Expr

func buildForIsType(dtype any) *Expr {
	switch d := dtype.(type) {
	case DType:
		return New(DataType, "this", d)
	case *Expr:
		if d.Is(DataType) {
			return d
		}
		if d.IsAny(Identifier, Dot) {
			return New(DataType, "this", DTypeUserdefined, "kind", d)
		}
	case string:
		if v, ok := isTypeCache.Load(d); ok {
			return v.(*Expr)
		}
		e, err := DataTypeBuild(d, nil, true)
		if err != nil {
			panic(err)
		}
		isTypeCache.Store(d, e)
		return e
	}
	panic(errs.Valuef("Invalid data type: %T. Expected str or DType", dtype))
}

// IsType mirrors Expression.is_type / Cast.is_type / DataType.is_type. Each dtype may be a
// DType, a string or a DataType expression.
func (e *Expr) IsType(dtypes ...any) bool { return e.isType(false, dtypes) }

// IsTypeCheckNullable is DataType.is_type(..., check_nullable=True).
func (e *Expr) IsTypeCheckNullable(dtypes ...any) bool { return e.isType(true, dtypes) }

func (e *Expr) isType(checkNullable bool, dtypes []any) bool {
	if e == nil {
		return false
	}
	switch implIsType[e.kind] {
	case Cast:
		return e.To().isType(checkNullable, dtypes)
	case DataType:
		return dataTypeIsType(e, checkNullable, dtypes)
	}
	if e.typ == nil {
		return false
	}
	return dataTypeIsType(e.typ, checkNullable, dtypes)
}

// IsTypeIn reports whether the node's type is in the set (e.g. DataTypeTextTypes).
func (e *Expr) IsTypeIn(sets ...TypeSet) bool {
	var dts []any
	for _, s := range sets {
		for t := range s {
			dts = append(dts, t)
		}
	}
	return e.IsType(dts...)
}

func dataTypeIsType(self *Expr, checkNullable bool, dtypes []any) bool {
	selfNullable := self.Bool("nullable")
	selfThis := self.Arg("this")
	for _, dtype := range dtypes {
		// fast path for plain DTypes
		if d, ok := dtype.(DType); ok && !checkNullable {
			if selfThis == DTypeUserdefined || d == DTypeUserdefined {
				if self.Equal(New(DataType, "this", d)) {
					return true
				}
				continue
			}
			if selfThis == d {
				return true
			}
			continue
		}
		other := buildForIsType(dtype)
		otherNullable := other.Bool("nullable")
		var matches bool
		if len(other.Expressions()) > 0 || (checkNullable && (selfNullable || otherNullable)) ||
			selfThis == DTypeUserdefined || other.Arg("this") == DTypeUserdefined {
			matches = self.Equal(other)
		} else {
			matches = selfThis == other.Arg("this")
		}
		if matches {
			return true
		}
	}
	return false
}

// DataTypeThis returns the DType of a DataType node ("" when "this" is not a DType).
func (e *Expr) DataTypeThis() DType {
	if d, ok := e.Arg("this").(DType); ok {
		return d
	}
	return ""
}
