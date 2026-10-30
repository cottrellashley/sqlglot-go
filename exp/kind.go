package exp

import (
	"strings"
)

// Kind identifies the type of an expression node. There is one Kind per sqlglot expression
// class (exp.Select, exp.Column, ...). Trait classes (Func, Binary, Condition, ...) are kinds
// too, which makes Is(kind) behave like Python's isinstance.
type Kind uint16

// ArgSpec describes one argument of an expression kind (sqlglot's arg_types entry).
type ArgSpec struct {
	Key      string
	Required bool
}

type kindInfo struct {
	name        string
	key         string
	args        []ArgSpec
	bases       []Kind
	isFunc      bool
	sqlNames    []string
	varLen      bool
	varLenKey   string
	primitive   bool
	hashRawArgs bool
	isSubquery  bool
	isCast      bool
	isDataType  bool

	// computed
	ancestors KindSet
	argIndex  map[string]int
	required  []string
}

// NumKinds is the number of expression kinds (including InvalidKind).
const NumKinds = int(numKinds)

var (
	kindByKey  = map[string]Kind{}
	kindByName = map[string]Kind{}
	// FunctionByName mirrors exp.FUNCTION_BY_NAME.
	functionByName = map[string]Kind{}
)

func init() {
	for k := Kind(1); k < numKinds; k++ {
		info := &kindTable[k]
		info.ancestors.Add(k)
		for _, b := range info.bases {
			info.ancestors.Add(b)
		}
		info.argIndex = make(map[string]int, len(info.args))
		for i, a := range info.args {
			info.argIndex[a.Key] = i
			if a.Required {
				info.required = append(info.required, a.Key)
			}
		}
		if info.varLenKey == "" {
			info.varLenKey = "expressions"
		}
		kindByKey[info.key] = k
		kindByName[info.name] = k
	}
	for _, f := range allFunctions {
		for _, name := range kindTable[f].sqlNames {
			functionByName[name] = f
		}
	}
}

// String returns the Python class name of the kind (e.g. "Select").
func (k Kind) String() string {
	if k == InvalidKind || int(k) >= NumKinds {
		return "Invalid"
	}
	return kindTable[k].name
}

// Name returns the Python class name of the kind.
func (k Kind) Name() string { return k.String() }

// Key returns the lowercase key of the kind (sqlglot's Expression.key), e.g. "select".
func (k Kind) Key() string { return kindTable[k].key }

// Args returns the argument specs of the kind in declaration order.
func (k Kind) Args() []ArgSpec { return kindTable[k].args }

// HasArg reports whether key is one of the kind's declared arguments.
func (k Kind) HasArg(key string) bool {
	_, ok := kindTable[k].argIndex[key]
	return ok
}

// RequiredArgs returns the keys of the mandatory arguments.
func (k Kind) RequiredArgs() []string { return kindTable[k].required }

// IsA reports whether k is base or a subclass of base (isinstance semantics).
func (k Kind) IsA(base Kind) bool { return kindTable[k].ancestors.Has(base) }

// IsAny reports whether k is a subclass of any of the given kinds.
func (k Kind) IsAny(bases ...Kind) bool {
	anc := &kindTable[k].ancestors
	for _, b := range bases {
		if anc.Has(b) {
			return true
		}
	}
	return false
}

// IsFunction reports whether the kind is a concrete function registered in ALL_FUNCTIONS.
func (k Kind) IsFunction() bool { return kindTable[k].isFunc }

// SQLNames mirrors Func.sql_names().
func (k Kind) SQLNames() []string {
	if names := kindTable[k].sqlNames; len(names) > 0 {
		return names
	}
	return []string{camelToSnake(kindTable[k].name)}
}

// SQLName mirrors Func.sql_name().
func (k Kind) SQLName() string { return k.SQLNames()[0] }

// IsVarLenArgs mirrors Func.is_var_len_args.
func (k Kind) IsVarLenArgs() bool { return kindTable[k].varLen }

// VarLenArgKey mirrors Func.var_len_arg_key.
func (k Kind) VarLenArgKey() string { return kindTable[k].varLenKey }

// IsPrimitive mirrors Expression.is_primitive.
func (k Kind) IsPrimitive() bool { return kindTable[k].primitive }

// Ancestors returns the set of kinds k is an instance of (including itself).
func (k Kind) Ancestors() KindSet { return kindTable[k].ancestors }

// KindByKey looks a kind up by its lowercase key (exp.EXPR_CLASSES).
func KindByKey(key string) (Kind, bool) {
	k, ok := kindByKey[key]
	return k, ok
}

// KindByName looks a kind up by its class name (e.g. "Select").
func KindByName(name string) (Kind, bool) {
	k, ok := kindByName[name]
	return k, ok
}

// FunctionByName mirrors exp.FUNCTION_BY_NAME.
func FunctionByName(name string) (Kind, bool) {
	k, ok := functionByName[name]
	return k, ok
}

// AllFunctions mirrors exp.ALL_FUNCTIONS.
func AllFunctions() []Kind { return allFunctions }

// AllKinds returns every kind.
func AllKinds() []Kind {
	out := make([]Kind, 0, NumKinds-1)
	for k := Kind(1); k < numKinds; k++ {
		out = append(out, k)
	}
	return out
}

func camelToSnake(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// KindSet is a set of kinds (a bitset; the zero value is empty and it is copied by value).
type KindSet [(int(numKinds) + 63) / 64]uint64

// NewKindSet builds a KindSet from kinds.
func NewKindSet(kinds ...Kind) KindSet {
	var s KindSet
	for _, k := range kinds {
		s.Add(k)
	}
	return s
}

// Add inserts a kind.
func (s *KindSet) Add(k Kind) { s[k/64] |= 1 << (k % 64) }

// Remove deletes a kind.
func (s *KindSet) Remove(k Kind) { s[k/64] &^= 1 << (k % 64) }

// Has reports exact membership (Python `type(e) in SET`).
func (s *KindSet) Has(k Kind) bool { return s[k/64]&(1<<(k%64)) != 0 }

// HasAny reports whether k is a subclass of any member (Python isinstance(e, tuple(SET))).
func (s *KindSet) HasAny(k Kind) bool {
	anc := &kindTable[k].ancestors
	for i := range s {
		if s[i]&anc[i] != 0 {
			return true
		}
	}
	return false
}

// Union returns the union of s and other.
func (s KindSet) Union(other KindSet) KindSet {
	for i := range s {
		s[i] |= other[i]
	}
	return s
}

// With returns s plus kinds.
func (s KindSet) With(kinds ...Kind) KindSet {
	for _, k := range kinds {
		s.Add(k)
	}
	return s
}

// Without returns s minus kinds.
func (s KindSet) Without(kinds ...Kind) KindSet {
	for _, k := range kinds {
		s.Remove(k)
	}
	return s
}

// Kinds lists the members.
func (s *KindSet) Kinds() []Kind {
	var out []Kind
	for k := Kind(1); k < numKinds; k++ {
		if s.Has(k) {
			out = append(out, k)
		}
	}
	return out
}

// Len returns the number of members.
func (s *KindSet) Len() int {
	n := 0
	for k := Kind(1); k < numKinds; k++ {
		if s.Has(k) {
			n++
		}
	}
	return n
}

// AnyExpression is a sentinel kind meaning "any expression" (sqlglot's Expr base class). It
// is only meaningful in `into` lists; it is not the kind of any node.
const AnyExpression Kind = 0xFFFF
