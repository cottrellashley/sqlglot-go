package executor

// CPython set iteration order. sqlglot's Python executor builds some results with
// list(set(...)) (ARRAY_UNIQUE_AGG, UNION DISTINCT), so their order is the order of CPython's
// set table. It is deterministic for numbers, None and tuples of them (string hashes are
// randomized per process), and this file emulates it for those values; other values keep
// their first-seen order.

import (
	"math"
	"math/big"
)

const (
	pyHashBits    = 61
	pyHashModulus = uint64(1)<<pyHashBits - 1
	pyHashInf     = 314159
	pyNoneHash    = 0xFCA86420
	pyLinearProbe = 9
	pyPerturb     = 5
	pySetMinSize  = 8
	xxPrime1      = 11400714785074694791
	xxPrime2      = 14029467366897019727
	xxPrime5      = 2870177450012600261
)

// pyHash returns CPython's hash of a value, if it is deterministic.
func pyHash(v any) (uint64, bool) {
	switch x := v.(type) {
	case nil:
		return pyNoneHash, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case int:
		return pyIntHash(big.NewInt(int64(x))), true
	case *big.Int:
		return pyIntHash(x), true
	case float64:
		return pyFloatHash(x), true
	case Tuple:
		acc := uint64(xxPrime5)
		for _, it := range x {
			lane, ok := pyHash(it)
			if !ok {
				return 0, false
			}
			acc += lane * xxPrime2
			acc = acc<<31 | acc>>33
			acc *= xxPrime1
		}
		acc += uint64(len(x)) ^ (xxPrime5 ^ 3527539)
		if acc == math.MaxUint64 {
			return 1546275796, true
		}
		return acc, true
	}
	return 0, false
}

func pyIntHash(n *big.Int) uint64 {
	m := new(big.Int).Mod(new(big.Int).Abs(n), new(big.Int).SetUint64(pyHashModulus)).Uint64()
	h := int64(m)
	if n.Sign() < 0 {
		h = -h
	}
	if h == -1 {
		h = -2
	}
	return uint64(h)
}

func pyFloatHash(v float64) uint64 {
	if math.IsInf(v, 1) {
		return pyHashInf
	}
	if math.IsInf(v, -1) {
		negInf := int64(-pyHashInf)
		return uint64(negInf)
	}
	if math.IsNaN(v) {
		return 0
	}
	m, e := math.Frexp(v)
	sign := int64(1)
	if m < 0 {
		sign, m = -1, -m
	}
	var x uint64
	for m != 0 {
		x = ((x << 28) & pyHashModulus) | x>>(pyHashBits-28)
		m *= 268435456.0
		e -= 28
		y := uint64(m)
		m -= float64(y)
		x += y
		if x >= pyHashModulus {
			x -= pyHashModulus
		}
	}
	if e >= 0 {
		e %= pyHashBits
	} else {
		e = pyHashBits - 1 - ((-1 - e) % pyHashBits)
	}
	x = ((x << uint(e)) & pyHashModulus) | x>>(pyHashBits-uint(e))
	h := int64(x) * sign
	if h == -1 {
		h = -2
	}
	return uint64(h)
}

type pySetEntry struct {
	key  any
	hash uint64
	used bool
}

// pySet emulates CPython's set table (no deletions, so no dummy entries).
type pySet struct {
	table []pySetEntry
	fill  int
}

func newPySet() *pySet { return &pySet{table: make([]pySetEntry, pySetMinSize)} }

func (s *pySet) mask() uint64 { return uint64(len(s.table) - 1) }

func (s *pySet) add(key any, hash uint64) {
	mask := s.mask()
	i := hash & mask
	perturb := hash
	for {
		probes := 0
		if i+pyLinearProbe <= mask {
			probes = pyLinearProbe
		}
		for j := i; ; j++ {
			e := &s.table[j]
			if !e.used {
				e.key, e.hash, e.used = key, hash, true
				s.fill++
				if uint64(s.fill)*5 >= mask*3 {
					s.resize(s.fill * 4)
				}
				return
			}
			if e.hash == hash && Equal(e.key, key) {
				return
			}
			if probes == 0 {
				break
			}
			probes--
		}
		perturb >>= pyPerturb
		i = (i*5 + 1 + perturb) & mask
	}
}

func (s *pySet) resize(minused int) {
	size := pySetMinSize
	for size <= minused {
		size <<= 1
	}
	old := s.table
	s.table = make([]pySetEntry, size)
	for _, e := range old {
		if e.used {
			s.insertClean(e.key, e.hash)
		}
	}
}

func (s *pySet) insertClean(key any, hash uint64) {
	mask := s.mask()
	i := hash & mask
	perturb := hash
	for {
		if !s.table[i].used {
			s.table[i] = pySetEntry{key, hash, true}
			return
		}
		if i+pyLinearProbe <= mask {
			for j := uint64(1); j <= pyLinearProbe; j++ {
				if !s.table[i+j].used {
					s.table[i+j] = pySetEntry{key, hash, true}
					return
				}
			}
		}
		perturb >>= pyPerturb
		i = (i*5 + 1 + perturb) & mask
	}
}

// merge mirrors set_merge(s, other).
func (s *pySet) merge(other *pySet) {
	if other.fill == 0 {
		return
	}
	if uint64(s.fill+other.fill)*5 >= s.mask()*3 {
		s.resize((s.fill + other.fill) * 2)
	}
	if s.fill == 0 && len(s.table) == len(other.table) {
		copy(s.table, other.table)
		s.fill = other.fill
		return
	}
	if s.fill == 0 {
		for _, e := range other.table {
			if e.used {
				s.insertClean(e.key, e.hash)
			}
		}
		s.fill = other.fill
		return
	}
	for _, e := range other.table {
		if e.used {
			s.add(e.key, e.hash)
		}
	}
}

func (s *pySet) items() []any {
	var out []any
	for _, e := range s.table {
		if e.used {
			out = append(out, e.key)
		}
	}
	return out
}

func pySetOf(items []any) (*pySet, bool) {
	s := newPySet()
	for _, it := range items {
		h, ok := pyHash(it)
		if !ok {
			return nil, false
		}
		s.add(it, h)
	}
	return s, true
}

// pySetList mirrors list(set(items)).
func pySetList(items []any) []any {
	if s, ok := pySetOf(items); ok {
		return s.items()
	}
	return newValueSet(items).items
}

// pySetUnion mirrors list(set(a).union(set(b))).
func pySetUnion(a, b []any) []any {
	sa, ok1 := pySetOf(a)
	sb, ok2 := pySetOf(b)
	if !ok1 || !ok2 {
		return newValueSet(append(append([]any{}, a...), b...)).items
	}
	result := newPySet()
	result.merge(sa)
	result.merge(sb)
	return result.items()
}
