// Package trie implements the prefix tries used by the tokenizer, parser, time formatter and
// schema. It mirrors sqlglot/trie.py, generalised over the key element type.
package trie

// Result is the outcome of a trie lookup.
type Result int

const (
	Failed Result = iota
	Prefix
	Exists
)

// Node is a trie node. Children are keyed by key elements; End marks a complete key.
type Node[K comparable] struct {
	Children map[K]*Node[K]
	End      bool
}

// New builds a trie from the given keys (each key is a sequence of elements).
func New[K comparable](keys ...[]K) *Node[K] {
	t := &Node[K]{}
	for _, k := range keys {
		t.Add(k)
	}
	return t
}

// NewFromStrings builds a rune trie from strings.
func NewFromStrings(keys ...string) *Node[rune] {
	t := &Node[rune]{}
	for _, k := range keys {
		t.Add([]rune(k))
	}
	return t
}

// Add inserts a key.
func (t *Node[K]) Add(key []K) {
	current := t
	for _, c := range key {
		if current.Children == nil {
			current.Children = map[K]*Node[K]{}
		}
		next, ok := current.Children[c]
		if !ok {
			next = &Node[K]{}
			current.Children[c] = next
		}
		current = next
	}
	current.End = true
}

// Lookup mirrors in_trie: it walks the key and reports whether it failed, is a prefix of a
// key, or is an existing key, together with the node reached.
func (t *Node[K]) Lookup(key []K) (Result, *Node[K]) {
	if len(key) == 0 {
		return Failed, t
	}
	current := t
	for _, c := range key {
		next, ok := current.Children[c]
		if !ok {
			return Failed, current
		}
		current = next
	}
	if current.End {
		return Exists, current
	}
	return Prefix, current
}

// Step looks up a single element from this node.
func (t *Node[K]) Step(c K) (Result, *Node[K]) {
	next, ok := t.Children[c]
	if !ok {
		return Failed, t
	}
	if next.End {
		return Exists, next
	}
	return Prefix, next
}

// Empty reports whether the trie has no keys.
func (t *Node[K]) Empty() bool { return t == nil || (len(t.Children) == 0 && !t.End) }
