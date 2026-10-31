// Package pydec implements the subset of Python's decimal.Decimal arithmetic used by the
// simplifier: parsing, +, -, *, / and comparisons in the default context (28 significant
// digits, ROUND_HALF_EVEN), and str() formatting.
package pydec

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Prec is the default context precision.
const Prec = 28

// Dec is a finite decimal number: (-1)^neg * coef * 10^exp.
type Dec struct {
	Neg  bool
	Coef *big.Int // >= 0
	Exp  int
}

var ten = big.NewInt(10)

func pow10(n int) *big.Int { return new(big.Int).Exp(ten, big.NewInt(int64(n)), nil) }

// Parse parses a numeric literal (Python Decimal(str)).
func Parse(s string) (Dec, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "_", ""))
	d := Dec{Coef: new(big.Int)}
	if s == "" {
		return d, false
	}
	if s[0] == '+' || s[0] == '-' {
		d.Neg = s[0] == '-'
		s = s[1:]
	}
	mant, expPart := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant, expPart = s[:i], s[i+1:]
	}
	exp := 0
	if expPart != "" {
		e, err := strconv.Atoi(expPart)
		if err != nil {
			return d, false
		}
		exp = e
	}
	intPart, fracPart := mant, ""
	if i := strings.IndexByte(mant, '.'); i >= 0 {
		intPart, fracPart = mant[:i], mant[i+1:]
	}
	digits := intPart + fracPart
	if digits == "" {
		return d, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return d, false
		}
	}
	d.Coef.SetString(digits, 10)
	d.Exp = exp - len(fracPart)
	return d, true
}

// FromInt converts an integer exactly.
func FromInt(i *big.Int) Dec {
	return Dec{Neg: i.Sign() < 0, Coef: new(big.Int).Abs(i), Exp: 0}
}

func numDigits(c *big.Int) int {
	if c.Sign() == 0 {
		return 1
	}
	return len(c.String())
}

// round rounds to Prec significant digits (ROUND_HALF_EVEN). sticky indicates that the exact
// value is slightly larger in magnitude than coef (used by division).
func (d Dec) round() Dec {
	n := numDigits(d.Coef)
	if n <= Prec {
		return d
	}
	drop := n - Prec
	div := pow10(drop)
	q, r := new(big.Int).QuoRem(d.Coef, div, new(big.Int))
	half := new(big.Int).Quo(div, big.NewInt(2))
	cmp := r.Cmp(half)
	if cmp > 0 || (cmp == 0 && q.Bit(0) == 1) {
		q.Add(q, big.NewInt(1))
	}
	exp := d.Exp + drop
	if numDigits(q) > Prec {
		q.Quo(q, ten)
		exp++
	}
	return Dec{Neg: d.Neg, Coef: q, Exp: exp}
}

func (d Dec) signed() *big.Int {
	c := new(big.Int).Set(d.Coef)
	if d.Neg {
		c.Neg(c)
	}
	return c
}

// Add returns d + o.
func (d Dec) Add(o Dec) Dec {
	exp := min(d.Exp, o.Exp)
	a := new(big.Int).Mul(d.signed(), pow10(d.Exp-exp))
	b := new(big.Int).Mul(o.signed(), pow10(o.Exp-exp))
	s := new(big.Int).Add(a, b)
	neg := s.Sign() < 0
	if s.Sign() == 0 {
		neg = d.Neg && o.Neg
	}
	return Dec{Neg: neg, Coef: s.Abs(s), Exp: exp}.round()
}

// Sub returns d - o.
func (d Dec) Sub(o Dec) Dec {
	o.Neg = !o.Neg
	return d.Add(o)
}

// Mul returns d * o.
func (d Dec) Mul(o Dec) Dec {
	return Dec{Neg: d.Neg != o.Neg, Coef: new(big.Int).Mul(d.Coef, o.Coef), Exp: d.Exp + o.Exp}.round()
}

// Quo returns d / o (o must be non-zero).
func (d Dec) Quo(o Dec) Dec {
	neg := d.Neg != o.Neg
	if d.Coef.Sign() == 0 {
		return Dec{Neg: neg, Coef: new(big.Int), Exp: d.Exp - o.Exp}
	}
	shift := numDigits(o.Coef) - numDigits(d.Coef) + Prec + 1
	exp := d.Exp - o.Exp - shift
	var coef, rem *big.Int
	if shift >= 0 {
		coef, rem = new(big.Int).QuoRem(new(big.Int).Mul(d.Coef, pow10(shift)), o.Coef, new(big.Int))
	} else {
		coef, rem = new(big.Int).QuoRem(d.Coef, new(big.Int).Mul(o.Coef, pow10(-shift)), new(big.Int))
	}
	if rem.Sign() != 0 {
		// result is not exact; adjust to ensure correct rounding
		if new(big.Int).Mod(coef, big.NewInt(5)).Sign() == 0 {
			coef.Add(coef, big.NewInt(1))
		}
	} else {
		// result is exact; get as close to ideal exponent as possible
		ideal := d.Exp - o.Exp
		for exp < ideal && new(big.Int).Mod(coef, ten).Sign() == 0 {
			coef.Quo(coef, ten)
			exp++
		}
	}
	return Dec{Neg: neg, Coef: coef, Exp: exp}.round()
}

// Negate is unary minus (-d), rounded to the context precision like Python.
func (d Dec) Negate() Dec {
	n := Dec{Neg: !d.Neg, Coef: new(big.Int).Set(d.Coef), Exp: d.Exp}
	if n.Coef.Sign() == 0 {
		n.Neg = false
	}
	return n.round()
}

// IsZero reports whether d is zero.
func (d Dec) IsZero() bool { return d.Coef.Sign() == 0 }

// Rat returns the exact rational value.
func (d Dec) Rat() *big.Rat {
	r := new(big.Rat).SetInt(d.signed())
	if d.Exp >= 0 {
		return r.Mul(r, new(big.Rat).SetInt(pow10(d.Exp)))
	}
	return r.Quo(r, new(big.Rat).SetInt(pow10(-d.Exp)))
}

// Cmp compares numerically.
func (d Dec) Cmp(o Dec) int { return d.Rat().Cmp(o.Rat()) }

// String mirrors str(Decimal).
func (d Dec) String() string {
	digits := d.Coef.String()
	sign := ""
	if d.Neg {
		sign = "-"
	}
	leftdigits := d.Exp + len(digits)
	var dotplace int
	if d.Exp <= 0 && leftdigits > -6 {
		dotplace = leftdigits
	} else {
		dotplace = 1
	}
	var intpart, fracpart string
	switch {
	case dotplace <= 0:
		intpart = "0"
		fracpart = "." + strings.Repeat("0", -dotplace) + digits
	case dotplace >= len(digits):
		intpart = digits + strings.Repeat("0", dotplace-len(digits))
	default:
		intpart = digits[:dotplace]
		fracpart = "." + digits[dotplace:]
	}
	exp := ""
	if leftdigits != dotplace {
		exp = fmt.Sprintf("E%+d", leftdigits-dotplace)
	}
	return sign + intpart + fracpart + exp
}
