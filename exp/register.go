package exp

import (
	"fmt"
	"strings"
	"sync"
)

// KindSpec describes an expression kind defined outside this package: the Go equivalent of
// subclassing an expression class in Python, e.g. `class MyFunc(exp.Func)`.
type KindSpec struct {
	// Name is the class name, e.g. "SpecialUdf".
	Name string
	// Key is the lowercase key (Expression.key); it defaults to the lowercased Name.
	Key string
	// Args are the argument specs (arg_types), in declaration order.
	Args []ArgSpec
	// Bases are the parent kinds, e.g. Func or AggFunc. The new kind is an instance of them
	// and of their ancestors, and inherits their method implementations in order.
	Bases []Kind
	// SQLNames are the function names (Func.sql_names); they default to the snake-cased Name.
	SQLNames []string
	// VarLen marks functions with variable-length arguments (is_var_len_args), which are
	// collected in VarLenKey ("expressions" by default).
	VarLen    bool
	VarLenKey string
}

var registerMu sync.Mutex

// RegisterKind adds a new expression kind and returns it.
//
// Kinds must be registered before they are used, typically from an init function: registration
// is not synchronized with concurrent parsing or generation. Registering a function kind does
// not make parsers recognize it; add it to a dialect's parser functions, e.g.
// c.Parser.Functions["MY_FUNC"] = sqlglot.FromArgList(kind). Function kinds without a
// generator entry are generated as NAME(args...), like Python's function_fallback_sql.
func RegisterKind(spec KindSpec) Kind {
	registerMu.Lock()
	defer registerMu.Unlock()
	if spec.Name == "" {
		panic("exp.RegisterKind: empty kind name")
	}
	if _, ok := kindByName[spec.Name]; ok {
		panic(fmt.Sprintf("exp.RegisterKind: kind %q already exists", spec.Name))
	}
	if nextKind >= kindCap {
		panic(fmt.Sprintf("exp.RegisterKind: more than %d registered kinds", MaxRegisteredKinds))
	}
	key := spec.Key
	if key == "" {
		key = strings.ToLower(spec.Name)
	}
	if _, ok := kindByKey[key]; ok {
		panic(fmt.Sprintf("exp.RegisterKind: kind key %q already exists", key))
	}

	k := nextKind
	info := kindInfo{
		name:      spec.Name,
		key:       key,
		args:      append([]ArgSpec(nil), spec.Args...),
		sqlNames:  append([]string(nil), spec.SQLNames...),
		varLen:    spec.VarLen,
		varLenKey: spec.VarLenKey,
	}
	seen := map[Kind]bool{}
	for _, b := range spec.Bases {
		if b == InvalidKind || b >= nextKind {
			panic(fmt.Sprintf("exp.RegisterKind: invalid base kind %d", b))
		}
		for _, a := range append([]Kind{b}, kindTable[b].bases...) {
			if !seen[a] {
				seen[a] = true
				info.bases = append(info.bases, a)
			}
		}
	}
	for _, b := range info.bases {
		bi := &kindTable[b]
		info.primitive = info.primitive || bi.primitive
		info.hashRawArgs = info.hashRawArgs || bi.hashRawArgs
		info.isSubquery = info.isSubquery || bi.isSubquery
		info.isCast = info.isCast || bi.isCast
		info.isDataType = info.isDataType || bi.isDataType
	}
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
	kindTable[k] = info
	for _, table := range implTables {
		for _, b := range info.bases {
			if table[b] != InvalidKind {
				table[k] = table[b]
				break
			}
		}
	}
	kindByKey[key] = k
	kindByName[spec.Name] = k
	nextKind++
	return k
}
