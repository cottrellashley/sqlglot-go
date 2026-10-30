package exp

import "strings"

// UnabbreviatedUnitName mirrors TimeUnit.UNABBREVIATED_UNIT_NAME.
var UnabbreviatedUnitName = map[string]string{
	"D":  "DAY",
	"H":  "HOUR",
	"M":  "MINUTE",
	"MS": "MILLISECOND",
	"NS": "NANOSECOND",
	"Q":  "QUARTER",
	"S":  "SECOND",
	"US": "MICROSECOND",
	"W":  "WEEK",
	"Y":  "YEAR",
}

// timeUnitPostInit mirrors TimeUnit.__init__.
func timeUnitPostInit(e *Expr) {
	unit := e.Get("unit")
	if unit == nil {
		return
	}
	if (unit.kind == Column || unit.kind == Literal || unit.kind == Var) &&
		!(unit.Is(Column) && len(unit.Parts()) != 1) {
		name := unit.Name()
		if full, ok := UnabbreviatedUnitName[name]; ok {
			name = full
		}
		v := New(Var, "this", strings.ToUpper(name))
		e.putArg("unit", v)
		e.setParent("unit", v, -1)
	} else if unit.kind == Week {
		unit.Set("this", New(Var, "this", strings.ToUpper(unit.This().Name())))
	}
}

// dateTruncPreInit mirrors DateTrunc.__init__: it pops "unabbreviate" and normalises "unit".
func dateTruncPreInit(kv []any) []any {
	unabbreviate := true
	out := make([]any, 0, len(kv))
	for i := 0; i < len(kv); i += 2 {
		if kv[i] == "unabbreviate" {
			if b, ok := kv[i+1].(bool); ok {
				unabbreviate = b
			} else {
				unabbreviate = Truthy(kv[i+1])
			}
			continue
		}
		out = append(out, kv[i], kv[i+1])
	}
	for i := 0; i < len(out); i += 2 {
		if out[i] != "unit" {
			continue
		}
		unit, ok := out[i+1].(*Expr)
		if !ok || unit == nil {
			continue
		}
		if unit.IsAny(Column, Literal, Var) && !(unit.Is(Column) && len(unit.Parts()) != 1) {
			name := strings.ToUpper(unit.Name())
			if full, ok := UnabbreviatedUnitName[name]; ok && unabbreviate {
				name = full
			}
			out[i+1] = LiteralString(name)
		}
	}
	return out
}

// UnixToTime scale constants (UnixToTime.SECONDS, ...).
var (
	UnixToTimeSeconds     = func() *Expr { return LiteralNumber(0) }
	UnixToTimeDecis       = func() *Expr { return LiteralNumber(1) }
	UnixToTimeCentis      = func() *Expr { return LiteralNumber(2) }
	UnixToTimeMillis      = func() *Expr { return LiteralNumber(3) }
	UnixToTimeDecimillis  = func() *Expr { return LiteralNumber(4) }
	UnixToTimeCentimillis = func() *Expr { return LiteralNumber(5) }
	UnixToTimeMicros      = func() *Expr { return LiteralNumber(6) }
	UnixToTimeDecimicros  = func() *Expr { return LiteralNumber(7) }
	UnixToTimeCentimicros = func() *Expr { return LiteralNumber(8) }
	UnixToTimeNanos       = func() *Expr { return LiteralNumber(9) }
)
