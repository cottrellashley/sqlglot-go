package pydec

import "testing"

func TestDecimal(t *testing.T) {
	cases := []struct{ a, op, b, want string }{
		{"1.5", "+", "2.25", "3.75"},
		{"1.0", "+", "2.0", "3.0"},
		{"0.06", "+", "0.01", "0.07"},
		{"1", "/", "3", "0.3333333333333333333333333333"},
		{"3", "/", "1.5", "2"},
		{"1.0", "/", "4", "0.25"},
		{"10", "/", "4", "2.5"},
		{"2.5", "*", "4", "10.0"},
		{"1e3", "+", "1", "1001"},
		{"1", "-", "1.0", "0.0"},
		{"100", "/", "0.1", "1.00E+3"},
		{"0.0000001", "*", "1", "1E-7"},
		{"1", "/", "7", "0.1428571428571428571428571429"},
	}
	for _, c := range cases {
		a, _ := Parse(c.a)
		b, _ := Parse(c.b)
		var r Dec
		switch c.op {
		case "+":
			r = a.Add(b)
		case "-":
			r = a.Sub(b)
		case "*":
			r = a.Mul(b)
		case "/":
			r = a.Quo(b)
		}
		if r.String() != c.want {
			t.Errorf("%s %s %s = %s, want %s", c.a, c.op, c.b, r.String(), c.want)
		}
	}
}
