package exp

// Port of sqlglot/serde.py: a flat, JSON-serializable representation of expression trees.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

const (
	serdeIndex    = "i"
	serdeArgKey   = "k"
	serdeIsArray  = "a"
	serdeClass    = "c"
	serdeType     = "t"
	serdeComments = "o"
	serdeMeta     = "m"
	serdeValue    = "v"
	serdeDataType = "DataType.Type"
)

// Dump mirrors Expr.dump: it serializes the expression into a JSON-serializable list of
// payloads, one per node or argument value, in depth-first order.
func (e *Expr) Dump() []map[string]any { return dump(e) }

type serdeItem struct {
	node    any
	index   int
	argKey  string
	isArray bool
}

func dump(expression *Expr) []map[string]any {
	i := 0
	var payloads []map[string]any
	stack := []serdeItem{{expression, -1, "", false}}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		payload := map[string]any{}
		if it.index >= 0 {
			payload[serdeIndex] = it.index
			payload[serdeArgKey] = it.argKey
		}
		if it.isArray {
			payload[serdeIsArray] = true
		}
		payloads = append(payloads, payload)

		switch node := it.node.(type) {
		case *Expr:
			payload[serdeClass] = node.ClassName()
			if t := node.Type(); t != nil && t != node {
				payload[serdeType] = dump(t)
			}
			if len(node.Comments) > 0 {
				payload[serdeComments] = append([]string(nil), node.Comments...)
			}
			if node.meta != nil {
				meta := make(map[string]any, len(node.meta))
				for k, v := range node.meta {
					meta[k] = v
				}
				payload[serdeMeta] = meta
			}
			for j := len(node.args) - 1; j >= 0; j-- {
				a := node.args[j]
				switch vs := a.Value.(type) {
				case nil:
				case []*Expr:
					for k := len(vs) - 1; k >= 0; k-- {
						stack = append(stack, serdeItem{vs[k], i, a.Key, true})
					}
				case []any:
					for k := len(vs) - 1; k >= 0; k-- {
						stack = append(stack, serdeItem{vs[k], i, a.Key, true})
					}
				default:
					stack = append(stack, serdeItem{vs, i, a.Key, false})
				}
			}
		case DType:
			payload[serdeClass] = serdeDataType
			payload[serdeValue] = string(node)
		default:
			payload[serdeValue] = node
		}
		i++
	}
	return payloads
}

// Load mirrors Expr.load: it rebuilds an expression from the payloads returned by Dump. Numbers
// may be ints, float64s or json.Numbers (see LoadJSON).
func Load(payloads []map[string]any) (*Expr, error) {
	root, err := load(payloads)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, nil
	}
	e, ok := root.(*Expr)
	if !ok {
		return nil, fmt.Errorf("serde: the root payload is a %T, not an expression", root)
	}
	return e, nil
}

func load(payloads []map[string]any) (any, error) {
	if len(payloads) == 0 {
		return nil, nil
	}
	root, err := loadNode(payloads[0])
	if err != nil {
		return nil, err
	}
	nodes := []any{root}
	for _, payload := range payloads[1:] {
		var node any
		if _, ok := payload[serdeClass]; ok {
			if node, err = loadNode(payload); err != nil {
				return nil, err
			}
		} else {
			node = serdeNumber(payload[serdeValue])
		}
		nodes = append(nodes, node)

		index, ok := serdeInt(payload[serdeIndex])
		if !ok || index < 0 || index >= len(nodes)-1 {
			return nil, fmt.Errorf("serde: invalid parent index %v", payload[serdeIndex])
		}
		parent, ok := nodes[index].(*Expr)
		if !ok {
			return nil, fmt.Errorf("serde: payload %d is not an expression", index)
		}
		argKey, _ := payload[serdeArgKey].(string)
		if isArray, _ := payload[serdeIsArray].(bool); isArray {
			appendArg(parent, argKey, node)
		} else {
			parent.Set(argKey, node)
		}
	}
	return root, nil
}

// appendArg appends a list item, like Expression.append; lists holding values that are not
// expressions are stored as []any.
func appendArg(parent *Expr, key string, node any) {
	if n, ok := node.(*Expr); ok {
		if cur, isAny := parent.Arg(key).([]any); isAny {
			parent.Set(key, append(cur, n))
			return
		}
		parent.Append(key, n)
		return
	}
	if str, ok := node.(string); ok {
		switch x := parent.Arg(key).(type) {
		case nil:
			parent.Set(key, []string{str})
			return
		case []string:
			parent.Set(key, append(x, str))
			return
		}
	}
	var cur []any
	switch x := parent.Arg(key).(type) {
	case []*Expr:
		for _, y := range x {
			cur = append(cur, y)
		}
	case []string:
		for _, y := range x {
			cur = append(cur, y)
		}
	case []any:
		cur = x
	}
	parent.Set(key, append(cur, node))
}

func loadNode(payload map[string]any) (any, error) {
	className, _ := payload[serdeClass].(string)
	if className == serdeDataType {
		v, _ := payload[serdeValue].(string)
		return DType(v), nil
	}
	if i := strings.LastIndexByte(className, '.'); i >= 0 {
		className = className[i+1:]
	}
	kind, ok := KindByName(className)
	if !ok {
		return nil, fmt.Errorf("serde: unknown expression class %q", className)
	}
	e := New(kind)
	if t, ok := payload[serdeType]; ok {
		typ, err := load(serdePayloads(t))
		if err != nil {
			return nil, err
		}
		if dt, ok := typ.(*Expr); ok {
			e.typ = dt
		}
	}
	if c, ok := payload[serdeComments]; ok {
		switch cs := c.(type) {
		case []string:
			e.Comments = append([]string(nil), cs...)
		case []any:
			e.Comments = []string{}
			for _, x := range cs {
				e.Comments = append(e.Comments, fmt.Sprint(x))
			}
		}
	}
	if m, ok := payload[serdeMeta].(map[string]any); ok {
		e.meta = make(map[string]any, len(m))
		for k, v := range m {
			e.meta[k] = serdeNumber(v)
		}
	}
	return e, nil
}

func serdePayloads(v any) []map[string]any {
	switch x := v.(type) {
	case []map[string]any:
		return x
	case []any:
		out := make([]map[string]any, 0, len(x))
		for _, p := range x {
			if m, ok := p.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func serdeInt(v any) (int, bool) {
	switch x := serdeNumber(v).(type) {
	case int:
		return x, true
	case float64:
		if x == float64(int(x)) {
			return int(x), true
		}
	}
	return 0, false
}

// serdeNumber converts json.Number values into ints, *big.Ints or float64s.
func serdeNumber(v any) any {
	n, ok := v.(json.Number)
	if !ok {
		return v
	}
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		if b, ok := new(big.Int).SetString(s, 10); ok {
			return b
		}
	}
	f, _ := n.Float64()
	return f
}

// DumpJSON serializes an expression to JSON (json.dumps(expression.dump())).
func DumpJSON(e *Expr) ([]byte, error) { return json.Marshal(e.Dump()) }

// LoadJSON rebuilds an expression from the output of DumpJSON. Integers are preserved exactly.
func LoadJSON(data []byte) (*Expr, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var payloads []map[string]any
	if err := dec.Decode(&payloads); err != nil {
		return nil, err
	}
	return Load(payloads)
}
