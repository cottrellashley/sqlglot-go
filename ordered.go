package sqlglot

import "iter"

// OrderedMap is a string-keyed map that preserves insertion order, mirroring Python dicts.
type OrderedMap[V any] struct {
	keys []string
	m    map[string]V
}

// NewOrderedMap returns an empty ordered map.
func NewOrderedMap[V any]() *OrderedMap[V] { return &OrderedMap[V]{m: map[string]V{}} }

// Get returns the value for k.
func (o *OrderedMap[V]) Get(k string) (V, bool) {
	if o == nil {
		var zero V
		return zero, false
	}
	v, ok := o.m[k]
	return v, ok
}

// Has reports whether k is present.
func (o *OrderedMap[V]) Has(k string) bool {
	if o == nil {
		return false
	}
	_, ok := o.m[k]
	return ok
}

// Set inserts or updates k (updates keep the original position, like Python dicts).
func (o *OrderedMap[V]) Set(k string, v V) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

// Delete removes k and returns its value (dict.pop).
func (o *OrderedMap[V]) Delete(k string) (V, bool) {
	v, ok := o.m[k]
	if !ok {
		return v, false
	}
	delete(o.m, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
	return v, true
}

// Keys returns the keys in insertion order.
func (o *OrderedMap[V]) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

// Values returns the values in insertion order.
func (o *OrderedMap[V]) Values() []V {
	if o == nil {
		return nil
	}
	out := make([]V, len(o.keys))
	for i, k := range o.keys {
		out[i] = o.m[k]
	}
	return out
}

// Len returns the number of entries.
func (o *OrderedMap[V]) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Copy returns a shallow copy.
func (o *OrderedMap[V]) Copy() *OrderedMap[V] {
	n := NewOrderedMap[V]()
	if o != nil {
		n.Update(o)
	}
	return n
}

// Update sets all entries of other (dict.update).
func (o *OrderedMap[V]) Update(other *OrderedMap[V]) {
	if other == nil {
		return
	}
	for _, k := range other.keys {
		o.Set(k, other.m[k])
	}
}

// All iterates the entries in insertion order.
func (o *OrderedMap[V]) All() iter.Seq2[string, V] {
	return func(yield func(string, V) bool) {
		if o == nil {
			return
		}
		for _, k := range append([]string(nil), o.keys...) {
			v, ok := o.m[k]
			if !ok {
				continue
			}
			if !yield(k, v) {
				return
			}
		}
	}
}
