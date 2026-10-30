package exp

import (
	"fmt"
	"hash/maphash"
	"iter"
	"slices"
	"strings"
)

// Arg is one (key, value) argument of an expression. Values are one of: nil, *Expr, []*Expr,
// string, bool, int, DType, or another opaque value.
type Arg struct {
	Key   string
	Value any
}

// Expr is a node of the syntax tree. It mirrors sqlglot's Expression: a kind plus an ordered
// mapping of argument keys to values, with parent bookkeeping.
type Expr struct {
	kind   Kind
	args   []Arg
	parent *Expr
	argKey string
	index  int

	// Comments attached to the node (nil means "no comments", mirroring None).
	Comments []string

	typ    *Expr
	meta   map[string]any
	hash   uint64
	hashed bool
}

// New constructs an expression of the given kind from alternating key/value pairs, mirroring
// `exp.Kind(key=value, ...)`. Constructor side effects of the Python classes (unit
// normalisation for TimeUnit subclasses and DateTrunc) are applied.
func New(kind Kind, kv ...any) *Expr {
	if len(kv)%2 != 0 {
		panic(fmt.Sprintf("exp.New(%s): odd number of key/value arguments", kind))
	}
	if kind == DateTrunc {
		kv = dateTruncPreInit(kv)
	}
	e := &Expr{kind: kind, index: -1}
	if len(kv) > 0 {
		e.args = make([]Arg, 0, len(kv)/2)
	}
	primitive := kindTable[kind].primitive
	for i := 0; i < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			panic(fmt.Sprintf("exp.New(%s): argument key %v is not a string", kind, kv[i]))
		}
		v := normalizeValue(kv[i+1])
		if j := e.argPos(key); j >= 0 {
			e.args[j].Value = v
		} else {
			e.args = append(e.args, Arg{key, v})
		}
		if !primitive {
			e.setParent(key, v, -1)
		}
	}
	if kindTable[kind].ancestors.Has(TimeUnit) {
		timeUnitPostInit(e)
	}
	return e
}

// NewMap constructs an expression from a map of arguments. Arguments are inserted in the
// kind's declared argument order (unknown keys last, sorted).
func NewMap(kind Kind, args map[string]any) *Expr {
	kv := make([]any, 0, len(args)*2)
	seen := map[string]bool{}
	for _, a := range kindTable[kind].args {
		if v, ok := args[a.Key]; ok {
			kv = append(kv, a.Key, v)
			seen[a.Key] = true
		}
	}
	var rest []string
	for k := range args {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	for _, k := range rest {
		kv = append(kv, k, args[k])
	}
	return New(kind, kv...)
}

func normalizeValue(v any) any {
	switch x := v.(type) {
	case *Expr:
		if x == nil {
			return nil
		}
	case []*Expr:
		if x == nil {
			return []*Expr{}
		}
	}
	return v
}

// Kind returns the node kind.
func (e *Expr) Kind() Kind { return e.kind }

// Key returns the kind's lowercase key (e.g. "select").
func (e *Expr) Key() string { return kindTable[e.kind].key }

// ClassName returns the Python class name of the node (e.g. "Select").
func (e *Expr) ClassName() string { return kindTable[e.kind].name }

// Is reports whether the node is an instance of kind (isinstance semantics).
func (e *Expr) Is(kind Kind) bool { return e != nil && kindTable[e.kind].ancestors.Has(kind) }

// IsAny reports whether the node is an instance of any of the kinds.
func (e *Expr) IsAny(kinds ...Kind) bool {
	if e == nil {
		return false
	}
	anc := &kindTable[e.kind].ancestors
	for _, k := range kinds {
		if anc.Has(k) {
			return true
		}
	}
	return false
}

// IsKind reports exact kind equality (Python `type(e) is K`).
func (e *Expr) IsKind(kind Kind) bool { return e != nil && e.kind == kind }

// Args returns the ordered arguments. The returned slice must not be modified.
func (e *Expr) Args() []Arg { return e.args }

func (e *Expr) argPos(key string) int {
	for i := range e.args {
		if e.args[i].Key == key {
			return i
		}
	}
	return -1
}

// Arg returns the raw value of an argument (nil if absent).
func (e *Expr) Arg(key string) any {
	if e == nil {
		return nil
	}
	for i := range e.args {
		if e.args[i].Key == key {
			return e.args[i].Value
		}
	}
	return nil
}

// HasArg reports whether the argument key is present (possibly with a nil value).
func (e *Expr) HasArg(key string) bool { return e != nil && e.argPos(key) >= 0 }

// Get returns the argument as an expression (nil if absent or not an expression).
func (e *Expr) Get(key string) *Expr {
	if x, ok := e.Arg(key).(*Expr); ok {
		return x
	}
	return nil
}

// List returns the argument as a list of expressions (nil if absent or not a list).
func (e *Expr) List(key string) []*Expr {
	if x, ok := e.Arg(key).([]*Expr); ok {
		return x
	}
	return nil
}

// Str returns the argument if it is a string, "" otherwise.
func (e *Expr) Str(key string) string {
	if x, ok := e.Arg(key).(string); ok {
		return x
	}
	return ""
}

// Bool reports the Python truthiness of an argument.
func (e *Expr) Bool(key string) bool { return Truthy(e.Arg(key)) }

// Int returns an int argument (0 if absent or not an int).
func (e *Expr) Int(key string) int {
	switch x := e.Arg(key).(type) {
	case int:
		return x
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

// Truthy mirrors Python truthiness for argument values.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case *Expr:
		return x != nil
	case []*Expr:
		return len(x) > 0
	case string:
		return x != ""
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case DType:
		return true
	case []string:
		return len(x) > 0
	case []any:
		return len(x) > 0
	}
	return true
}

// This returns the "this" argument as an expression.
func (e *Expr) This() *Expr { return e.Get("this") }

// Expression returns the "expression" argument.
func (e *Expr) Expression() *Expr { return e.Get("expression") }

// Expressions returns the "expressions" argument.
func (e *Expr) Expressions() []*Expr { return e.List("expressions") }

// Parent returns the parent node (nil for roots).
func (e *Expr) Parent() *Expr {
	if e == nil {
		return nil
	}
	return e.parent
}

// ArgKey returns the key under which this node is stored in its parent.
func (e *Expr) ArgKey() string { return e.argKey }

// Index returns the position of the node in its parent's list argument, or -1.
func (e *Expr) Index() int { return e.index }

// SetParentInfo sets the parent bookkeeping directly (used by low-level code).
func (e *Expr) SetParentInfo(parent *Expr, argKey string, index int) {
	e.parent, e.argKey, e.index = parent, argKey, index
}

func (e *Expr) invalidateHash() {
	for node := e; node != nil && node.hashed; node = node.parent {
		node.hashed = false
	}
}

// Set mirrors Expression.set(arg_key, value). A nil value removes the argument.
func (e *Expr) Set(key string, value any) { e.SetAt(key, value, -1, true) }

// SetAt mirrors Expression.set(arg_key, value, index, overwrite).
func (e *Expr) SetAt(key string, value any, index int, overwrite bool) {
	e.invalidateHash()
	value = normalizeValue(value)

	if index >= 0 {
		exprs := e.List(key)
		if index >= len(exprs) {
			return
		}
		if value == nil {
			exprs = slices.Delete(slices.Clone(exprs), index, index+1)
			for _, v := range exprs[index:] {
				v.index--
			}
			e.putArg(key, exprs)
			return
		}
		if list, ok := value.([]*Expr); ok {
			n := make([]*Expr, 0, len(exprs)-1+len(list))
			n = append(n, exprs[:index]...)
			n = append(n, list...)
			n = append(n, exprs[index+1:]...)
			exprs = n
		} else if v, ok := value.(*Expr); ok {
			if overwrite {
				// in place, like Python's list item assignment
				exprs[index] = v
			} else {
				exprs = slices.Insert(slices.Clone(exprs), index, v)
			}
		}
		value = exprs
	} else if value == nil {
		if j := e.argPos(key); j >= 0 {
			e.args = slices.Delete(e.args, j, j+1)
		}
		return
	}

	e.putArg(key, value)
	e.setParent(key, value, index)
}

func (e *Expr) putArg(key string, value any) {
	if j := e.argPos(key); j >= 0 {
		e.args[j].Value = value
	} else {
		e.args = append(e.args, Arg{key, value})
	}
}

// SetRaw stores a value without any parent bookkeeping (Python `e.args[k] = v`).
func (e *Expr) SetRaw(key string, value any) {
	e.invalidateHash()
	e.putArg(key, normalizeValue(value))
}

// SetKwargs sets several arguments (alternating key/value pairs) and returns the node.
func (e *Expr) SetKwargs(kv ...any) *Expr {
	for i := 0; i+1 < len(kv); i += 2 {
		e.Set(kv[i].(string), kv[i+1])
	}
	return e
}

func (e *Expr) setParent(key string, value any, index int) {
	switch v := value.(type) {
	case *Expr:
		v.parent = e
		v.argKey = key
		v.index = -1
		if index >= 0 {
			v.index = index
		}
	case []*Expr:
		for i, x := range v {
			if x != nil {
				x.parent = e
				x.argKey = key
				x.index = i
			}
		}
	case []any:
		for i, x := range v {
			if y, ok := x.(*Expr); ok && y != nil {
				y.parent = e
				y.argKey = key
				y.index = i
			}
		}
	}
}

// Append mirrors Expression.append(arg_key, value).
func (e *Expr) Append(key string, value any) {
	e.invalidateHash()
	value = normalizeValue(value)
	cur, ok := e.Arg(key).([]*Expr)
	if !ok {
		cur = []*Expr{}
		e.putArg(key, cur)
	}
	if v, ok := value.(*Expr); ok {
		v.parent = e
		v.argKey = key
		v.index = len(cur)
		e.putArg(key, append(cur, v))
	}
}

// Depth returns the depth of this node in the tree.
func (e *Expr) Depth() int {
	d := 0
	for p := e.parent; p != nil; p = p.parent {
		d++
	}
	return d
}

// IterExpressions yields the child expressions (exploding list args), in argument order.
func (e *Expr) IterExpressions() []*Expr {
	var out []*Expr
	for _, a := range e.args {
		switch v := a.Value.(type) {
		case *Expr:
			out = append(out, v)
		case []*Expr:
			for _, x := range v {
				if x != nil {
					out = append(out, x)
				}
			}
		case []any:
			for _, x := range v {
				if y, ok := x.(*Expr); ok && y != nil {
					out = append(out, y)
				}
			}
		}
	}
	return out
}

func (e *Expr) pushChildrenReversed(stack []*Expr) []*Expr {
	for i := len(e.args) - 1; i >= 0; i-- {
		switch v := e.args[i].Value.(type) {
		case *Expr:
			stack = append(stack, v)
		case []*Expr:
			for j := len(v) - 1; j >= 0; j-- {
				if v[j] != nil {
					stack = append(stack, v[j])
				}
			}
		case []any:
			for j := len(v) - 1; j >= 0; j-- {
				if y, ok := v[j].(*Expr); ok && y != nil {
					stack = append(stack, y)
				}
			}
		}
	}
	return stack
}

// DFS iterates the tree depth-first (pre-order). prune may be nil; when it returns true for
// a node, that node's children are skipped.
func (e *Expr) DFS(prune func(*Expr) bool) iter.Seq[*Expr] {
	return func(yield func(*Expr) bool) {
		stack := []*Expr{e}
		for len(stack) > 0 {
			node := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !yield(node) {
				return
			}
			if prune != nil && prune(node) {
				continue
			}
			stack = node.pushChildrenReversed(stack)
		}
	}
}

// BFS iterates the tree breadth-first.
func (e *Expr) BFS(prune func(*Expr) bool) iter.Seq[*Expr] {
	return func(yield func(*Expr) bool) {
		queue := []*Expr{e}
		for len(queue) > 0 {
			node := queue[0]
			queue = queue[1:]
			if !yield(node) {
				return
			}
			if prune != nil && prune(node) {
				continue
			}
			queue = append(queue, node.IterExpressions()...)
		}
	}
}

// Walk iterates the tree (BFS when bfs is true, DFS otherwise).
func (e *Expr) Walk(bfs bool, prune func(*Expr) bool) iter.Seq[*Expr] {
	if bfs {
		return e.BFS(prune)
	}
	return e.DFS(prune)
}

// FindAll iterates (BFS) the nodes that are instances of any of the kinds.
func (e *Expr) FindAll(kinds ...Kind) iter.Seq[*Expr] { return e.FindAllWalk(true, kinds...) }

// FindAllWalk is FindAll with an explicit traversal order.
func (e *Expr) FindAllWalk(bfs bool, kinds ...Kind) iter.Seq[*Expr] {
	return func(yield func(*Expr) bool) {
		for node := range e.Walk(bfs, nil) {
			if node.IsAny(kinds...) {
				if !yield(node) {
					return
				}
			}
		}
	}
}

// FindAllList collects FindAll into a slice.
func (e *Expr) FindAllList(kinds ...Kind) []*Expr { return slices.Collect(e.FindAll(kinds...)) }

// Find returns the first node (BFS) that is an instance of any of the kinds.
func (e *Expr) Find(kinds ...Kind) *Expr {
	for node := range e.FindAll(kinds...) {
		return node
	}
	return nil
}

// FindDFS is Find using a depth-first traversal.
func (e *Expr) FindDFS(kinds ...Kind) *Expr {
	for node := range e.FindAllWalk(false, kinds...) {
		return node
	}
	return nil
}

// FindAncestor returns the nearest ancestor that is an instance of any of the kinds.
func (e *Expr) FindAncestor(kinds ...Kind) *Expr {
	a := e.parent
	for a != nil && !a.IsAny(kinds...) {
		a = a.parent
	}
	return a
}

// ParentSelect returns the enclosing Select.
func (e *Expr) ParentSelect() *Expr { return e.FindAncestor(Select) }

// SameParent reports whether the parent has the same kind as the node.
func (e *Expr) SameParent() bool { return e.parent != nil && e.parent.kind == e.kind }

// Root returns the root of the tree.
func (e *Expr) Root() *Expr {
	r := e
	for r.parent != nil {
		r = r.parent
	}
	return r
}

// Unnest returns the first non-parenthesis child (Subquery.unnest strips subqueries).
func (e *Expr) Unnest() *Expr {
	if e == nil {
		return nil
	}
	x := e
	if implUnnest[e.kind] == Subquery {
		for x != nil && x.Is(Subquery) {
			x = x.This()
		}
		return x
	}
	for x != nil && x.kind == Paren {
		x = x.This()
	}
	return x
}

// Unalias returns the inner expression of an Alias.
func (e *Expr) Unalias() *Expr {
	if e.Is(Alias) {
		return e.This()
	}
	return e
}

// UnnestOperands returns the unnested child expressions.
func (e *Expr) UnnestOperands() []*Expr {
	children := e.IterExpressions()
	out := make([]*Expr, len(children))
	for i, c := range children {
		out[i] = c.Unnest()
	}
	return out
}

// Flatten yields child nodes whose parents are the same kind: A AND B AND C -> [A, B, C].
func (e *Expr) Flatten(unnest bool) []*Expr {
	var out []*Expr
	for node := range e.DFS(func(n *Expr) bool { return n.parent != nil && n.kind != e.kind }) {
		if node.kind != e.kind {
			if unnest && !kindTable[node.kind].isSubquery {
				out = append(out, node.Unnest())
			} else {
				out = append(out, node)
			}
		}
	}
	return out
}

// Copy returns a deep copy of the expression.
func (e *Expr) Copy() *Expr {
	if e == nil {
		return nil
	}
	root := &Expr{kind: e.kind, index: -1}
	type pair struct{ node, copy *Expr }
	stack := []pair{{e, root}}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node, cp := p.node, p.copy
		if node.Comments != nil {
			cp.Comments = slices.Clone(node.Comments)
		}
		if node.typ != nil {
			cp.typ = node.typ.Copy()
		}
		if node.meta != nil {
			cp.meta = make(map[string]any, len(node.meta))
			for k, v := range node.meta {
				cp.meta[k] = v
			}
		}
		cp.hash, cp.hashed = node.hash, node.hashed
		if len(node.args) > 0 {
			cp.args = make([]Arg, len(node.args))
		}
		for i, a := range node.args {
			switch v := a.Value.(type) {
			case *Expr:
				c := &Expr{kind: v.kind, index: -1, parent: cp, argKey: a.Key}
				stack = append(stack, pair{v, c})
				cp.args[i] = Arg{a.Key, c}
			case []*Expr:
				list := make([]*Expr, len(v))
				for j, x := range v {
					c := &Expr{kind: x.kind, parent: cp, argKey: a.Key, index: j}
					stack = append(stack, pair{x, c})
					list[j] = c
				}
				cp.args[i] = Arg{a.Key, list}
			case []any:
				list := make([]any, len(v))
				for j, x := range v {
					if y, ok := x.(*Expr); ok && y != nil {
						c := &Expr{kind: y.kind, parent: cp, argKey: a.Key, index: j}
						stack = append(stack, pair{y, c})
						list[j] = c
					} else {
						list[j] = x
					}
				}
				cp.args[i] = Arg{a.Key, list}
			default:
				cp.args[i] = Arg{a.Key, v}
			}
		}
	}
	return root
}

// MaybeCopy returns a copy when copy is true.
func MaybeCopy(e *Expr, copy bool) *Expr {
	if copy && e != nil {
		return e.Copy()
	}
	return e
}

// AddComments mirrors Expression.add_comments.
func (e *Expr) AddComments(comments []string, prepend bool) {
	if e.Comments == nil {
		e.Comments = []string{}
	}
	if len(comments) == 0 {
		return
	}
	for _, comment := range comments {
		if parts := strings.Split(comment, SQLGlotMeta); len(parts) > 1 {
			for _, kv := range strings.Split(strings.Join(parts[1:], ""), ",") {
				pieces := strings.Split(kv, "=")
				k := strings.TrimSpace(pieces[0])
				if len(pieces) > 1 {
					e.Meta()[k] = toBool(strings.TrimSpace(pieces[1]))
				} else {
					e.Meta()[k] = true
				}
			}
		}
		if !prepend {
			e.Comments = append(e.Comments, comment)
		}
	}
	if prepend {
		e.Comments = append(slices.Clone(comments), e.Comments...)
	}
}

func toBool(s string) any {
	switch strings.ToLower(s) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	}
	return s
}

// PopComments removes and returns the node's comments.
func (e *Expr) PopComments() []string {
	c := e.Comments
	e.Comments = nil
	if c == nil {
		return []string{}
	}
	return c
}

// Meta returns the node's metadata map, creating it if needed.
func (e *Expr) Meta() map[string]any {
	if e.meta == nil {
		e.meta = map[string]any{}
	}
	return e.meta
}

// MetaGet returns a metadata value without creating the map.
func (e *Expr) MetaGet(key string) any {
	if e.meta == nil {
		return nil
	}
	return e.meta[key]
}

// HasMeta reports whether the node has any metadata.
func (e *Expr) HasMeta() bool { return len(e.meta) > 0 }

// UpdatePositionsFrom copies position metadata from another expression.
func (e *Expr) UpdatePositionsFrom(other *Expr) *Expr {
	if other != nil && other.meta != nil {
		for _, k := range PositionMetaKeys {
			if v, ok := other.meta[k]; ok {
				e.Meta()[k] = v
			}
		}
	}
	return e
}

// UpdatePositions sets explicit position metadata.
func (e *Expr) UpdatePositions(line, col, start, end int) *Expr {
	m := e.Meta()
	m["line"], m["col"], m["start"], m["end"] = line, col, start, end
	return e
}

// Type returns the data type annotation of the node (DataType nodes are their own type).
func (e *Expr) Type() *Expr {
	if e == nil {
		return nil
	}
	if kindTable[e.kind].isDataType {
		return e
	}
	if kindTable[e.kind].isCast {
		if e.typ != nil {
			return e.typ
		}
		return e.Get("to")
	}
	return e.typ
}

// SetType sets the data type annotation. dtype may be a DataType *Expr, a DType, a string
// (parsed with DataTypeBuild) or nil.
func (e *Expr) SetType(dtype any) {
	switch d := dtype.(type) {
	case nil:
		e.typ = nil
	case *Expr:
		if d == nil {
			e.typ = nil
		} else if d.kind == DataType {
			e.typ = d
		} else {
			e.typ = MustDataTypeBuild(d, nil, false)
		}
	default:
		e.typ = MustDataTypeBuild(d, nil, false)
	}
}

// RawType returns the stored annotation without the DataType/Cast special cases.
func (e *Expr) RawType() *Expr { return e.typ }

// IsLeaf reports whether no argument holds an expression or a non-empty list.
func (e *Expr) IsLeaf() bool {
	for _, a := range e.args {
		switch v := a.Value.(type) {
		case *Expr:
			return false
		case []*Expr:
			if len(v) > 0 {
				return false
			}
		}
	}
	return true
}

// Transform visits all nodes (excluding newly created ones) and applies fun, mirroring
// Expression.transform. If fun returns nil, the node is removed.
func (e *Expr) Transform(fun func(*Expr) *Expr, copy bool) *Expr {
	start := e
	if copy {
		start = e.Copy()
	}
	var root *Expr
	stack := []*Expr{start}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		parent, argKey, index := node.parent, node.argKey, node.index
		newNode := fun(node)
		if root == nil {
			root = newNode
		} else if parent != nil && argKey != "" && newNode != node {
			parent.SetAt(argKey, newNode, index, true)
		}
		if newNode != node {
			continue
		}
		stack = node.pushChildrenReversed(stack)
	}
	if root == nil {
		panic("transform: root is nil")
	}
	return root
}

// Replace swaps this node with another node in its parent (nil removes it).
func (e *Expr) Replace(n *Expr) *Expr {
	parent := e.parent
	if parent == nil || parent == n {
		return n
	}
	if e.argKey != "" {
		if n == nil {
			parent.SetAt(e.argKey, nil, e.index, true)
		} else {
			parent.SetAt(e.argKey, n, e.index, true)
		}
	}
	if n != e {
		e.parent, e.argKey, e.index = nil, "", -1
	}
	return n
}

// ReplaceWithList replaces this node with a list of nodes (splicing into a list argument).
// If the node is not inside a list, its parent is replaced instead (sqlglot semantics).
func (e *Expr) ReplaceWithList(nodes []*Expr) {
	parent := e.parent
	if parent == nil {
		return
	}
	if e.argKey != "" {
		if value, ok := parent.Arg(e.argKey).(*Expr); ok {
			if value.parent != nil {
				value.parent.ReplaceWithList(nodes)
			}
		} else {
			parent.SetAt(e.argKey, nodes, e.index, true)
		}
	}
	e.parent, e.argKey, e.index = nil, "", -1
}

// Pop removes this node from its tree and returns it.
func (e *Expr) Pop() *Expr {
	e.Replace(nil)
	return e
}

// AssertIs panics if the node is not an instance of kind and returns the node otherwise.
func (e *Expr) AssertIs(kind Kind) *Expr {
	if !e.Is(kind) {
		panic(fmt.Sprintf("%s is not %s.", e.SQL(), kind))
	}
	return e
}

// ErrorMessages mirrors Expression.error_messages: it validates mandatory arguments.
func (e *Expr) ErrorMessages(args []*Expr) []string {
	var errors []string
	info := &kindTable[e.kind]
	for _, k := range info.required {
		v := e.Arg(k)
		if v == nil {
			errors = append(errors, fmt.Sprintf("Required keyword: '%s' missing for %s", k, classRepr(e.kind)))
			continue
		}
		if l, ok := v.([]*Expr); ok && len(l) == 0 {
			errors = append(errors, fmt.Sprintf("Required keyword: '%s' missing for %s", k, classRepr(e.kind)))
		}
	}
	if len(args) > 0 && e.Is(Func) && len(args) > len(info.args) && !info.varLen {
		errors = append(errors, fmt.Sprintf(
			"The number of provided arguments (%d) is greater than the maximum number of supported arguments (%d)",
			len(args), len(info.args)))
	}
	return errors
}

func classRepr(k Kind) string {
	return fmt.Sprintf("<class 'sqlglot.expressions.%s'>", kindTable[k].name)
}

// ---------------------------------------------------------------------------------------
// hashing & equality

var hashSeed = maphash.MakeSeed()

func hashString(s string) uint64 { return maphash.String(hashSeed, s) }

func mix(h uint64, parts ...uint64) uint64 {
	for _, p := range parts {
		h ^= p + 0x9e3779b97f4a7c15 + (h << 6) + (h >> 2)
		h *= 0xbf58476d1ce4e5b9
		h ^= h >> 31
	}
	return h
}

func hashScalar(v any, lower bool) uint64 {
	switch x := v.(type) {
	case *Expr:
		return x.Hash()
	case string:
		if lower {
			x = strings.ToLower(x)
		}
		return mix(1, hashString(x))
	case bool:
		if x {
			return mix(2, 1)
		}
		return mix(2, 0)
	case int:
		return mix(3, uint64(x))
	case DType:
		return mix(4, hashString(string(x)))
	case []*Expr:
		h := uint64(5)
		for _, y := range x {
			h = mix(h, y.Hash())
		}
		return h
	case []any:
		h := uint64(7)
		for _, y := range x {
			h = mix(h, hashScalar(y, lower))
		}
		return h
	}
	return mix(6, hashString(fmt.Sprintf("%#v", v)))
}

// Hash returns a structural hash of the tree (cached, invalidated on mutation). Two nodes
// are Equal iff they have the same kind and the same hash, mirroring sqlglot.
func (e *Expr) Hash() uint64 {
	if e.hashed {
		return e.hash
	}
	// collect nodes needing hashes, then hash in post-order
	var nodes []*Expr
	stack := []*Expr{e}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		nodes = append(nodes, node)
		for _, a := range node.args {
			switch v := a.Value.(type) {
			case *Expr:
				if !v.hashed {
					stack = append(stack, v)
				}
			case []*Expr:
				for _, x := range v {
					if x != nil && !x.hashed {
						stack = append(stack, x)
					}
				}
			case []any:
				for _, x := range v {
					if y, ok := x.(*Expr); ok && y != nil && !y.hashed {
						stack = append(stack, y)
					}
				}
			}
		}
	}
	for i := len(nodes) - 1; i >= 0; i-- {
		node := nodes[i]
		h := mix(0, hashString(kindTable[node.kind].key))
		keys := make([]int, len(node.args))
		for j := range keys {
			keys[j] = j
		}
		slices.SortFunc(keys, func(a, b int) int { return strings.Compare(node.args[a].Key, node.args[b].Key) })
		if kindTable[node.kind].hashRawArgs {
			for _, j := range keys {
				a := node.args[j]
				if Truthy(a.Value) {
					h = mix(h, hashString(a.Key), hashScalar(a.Value, false))
				}
			}
		} else {
			for _, j := range keys {
				a := node.args[j]
				switch v := a.Value.(type) {
				case []*Expr:
					for _, x := range v {
						h = mix(h, hashString(a.Key), x.Hash())
					}
				case []any:
					for _, x := range v {
						h = mix(h, hashString(a.Key), hashScalar(x, true))
					}
				case nil:
				case bool:
					if v {
						h = mix(h, hashString(a.Key), hashScalar(v, true))
					}
				default:
					h = mix(h, hashString(a.Key), hashScalar(v, true))
				}
			}
		}
		node.hash, node.hashed = h, true
	}
	return e.hash
}

// Equal mirrors Expression.__eq__: same kind and same structural hash.
func (e *Expr) Equal(o *Expr) bool {
	if e == o {
		return true
	}
	if e == nil || o == nil {
		return false
	}
	return e.kind == o.kind && e.Hash() == o.Hash()
}

// ExprKey is a comparable key usable in Go maps to emulate Python dicts/sets keyed by
// expressions (structural equality).
type ExprKey struct {
	Kind Kind
	Hash uint64
}

// MapKey returns the structural map key of the node.
func (e *Expr) MapKey() ExprKey { return ExprKey{e.kind, e.Hash()} }

// String returns the SQL of the expression in the default dialect.
func (e *Expr) String() string { return e.SQL() }

// SetParent sets only the parent pointer (Python `node.parent = parent`).
func (e *Expr) SetParent(p *Expr) { e.parent = p }

// FixParents drops None-valued arguments and resets the parent pointers of the children
// without invalidating cached hashes (the values are unchanged).
func (e *Expr) FixParents() {
	kept := e.args[:0]
	for _, a := range e.args {
		if a.Value == nil {
			continue
		}
		kept = append(kept, a)
	}
	e.args = kept
	for _, a := range e.args {
		e.setParent(a.Key, a.Value, -1)
	}
}
