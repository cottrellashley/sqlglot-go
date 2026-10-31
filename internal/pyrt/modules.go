package pyrt

// Bindings for the sqlglot.helper, sqlglot.time and sqlglot.errors modules, and the decimal
// stand-in used by the converted tests.

import (
	"sort"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// DecimalV is a decimal.Decimal, kept as its string form.
type DecimalV struct{ S string }

// String implements fmt.Stringer (str(Decimal)).
func (d *DecimalV) String() string { return d.S }

// PyHash makes decimals hashable.
func (d *DecimalV) PyHash() string { return "d:" + d.S }

func init() {
	globals["Decimal"] = &BuiltinType{
		Name:  "Decimal",
		Check: func(v Value) bool { _, ok := norm(v).(*DecimalV); return ok },
		New:   func(a Args) Value { return &DecimalV{Str(a.Get(0, "value", "0"))} },
	}

	// sqlglot.helper
	sqlglotModule.Attrs["tsort"] = Func("tsort", func(a Args) Value {
		d, ok := norm(a.Pos[0]).(*Dict)
		if !ok {
			Raise(TypeError, "tsort expects a dict")
		}
		dag := map[string]map[string]bool{}
		for i, k := range d.keys {
			deps := map[string]bool{}
			for _, dep := range Iter(d.vals[i]) {
				deps[Str(dep)] = true
			}
			dag[Str(k)] = deps
		}
		out, err := helper.TSort(dag)
		if err != nil {
			Raise(ValueError, "%s", err.Error())
		}
		return norm(out)
	})
	sqlglotModule.Attrs["name_sequence"] = Func("name_sequence", func(a Args) Value {
		next := helper.NameSequence(Str(a.Pos[0]))
		return Func("name_sequence", func(Args) Value { return next() })
	})
	sqlglotModule.Attrs["to_int"] = Func("to_int", func(a Args) Value {
		maxDigits := 4300
		if v := a.Get(1, "max_digits", nil); v != nil {
			maxDigits = mustInt(v)
		}
		n, ok := helper.ToIntDigits(Str(a.Pos[0]), maxDigits)
		if !ok {
			return nil
		}
		return norm(n)
	})
	sqlglotModule.Attrs["merge_ranges"] = Func("merge_ranges", func(a Args) Value {
		ranges := append([]Value(nil), Iter(a.Pos[0])...)
		if len(ranges) == 0 {
			return &List{}
		}
		sort.SliceStable(ranges, func(i, j int) bool { return Truth(Lt(ranges[i], ranges[j])) })
		merged := []Value{ranges[0]}
		for _, r := range ranges[1:] {
			start, end := Item(r, 0), Item(r, 1)
			lastStart, lastEnd := Item(merged[len(merged)-1], 0), Item(merged[len(merged)-1], 1)
			if Truth(Le(start, lastEnd)) {
				if Truth(Lt(lastEnd, end)) {
					lastEnd = end
				}
				merged[len(merged)-1] = Tuple{lastStart, lastEnd}
			} else {
				merged = append(merged, Tuple{start, end})
			}
		}
		return &List{merged}
	})

	// sqlglot.time
	sqlglotModule.Attrs["format_time"] = Func("format_time", func(a Args) Value {
		s := a.Get(0, "string", nil)
		if s == nil || Str(s) == "" {
			return nil
		}
		mapping := map[string]string{}
		if d, ok := norm(a.Get(1, "mapping", nil)).(*Dict); ok {
			for i, k := range d.keys {
				mapping[Str(k)] = Str(d.vals[i])
			}
		}
		if a.Get(2, "trie", nil) != nil {
			NotPorted("format_time(trie=...)")
		}
		return sqlglot.FormatTime(Str(s), mapping, nil)
	})
	sqlglotModule.Attrs["subsecond_precision"] = Func("subsecond_precision", func(a Args) Value {
		return sqlglot.SubsecondPrecision(Str(a.Pos[0]))
	})

	// sqlglot.errors
	sqlglotModule.Attrs["ANSI_UNDERLINE"] = errs.ANSIUnderline
	sqlglotModule.Attrs["ANSI_RESET"] = errs.ANSIReset
	sqlglotModule.Attrs["ERROR_MESSAGE_CONTEXT_DEFAULT"] = errs.ErrorMessageContextDefault
	sqlglotModule.Attrs["highlight_sql"] = Func("highlight_sql", func(a Args) Value {
		var positions [][2]int
		for _, p := range Iter(a.Get(1, "positions", nil)) {
			positions = append(positions, [2]int{mustInt(Item(p, 0)), mustInt(Item(p, 1))})
		}
		if len(positions) == 0 {
			Raise(ValueError, "positions must contain at least one (start, end) tuple")
		}
		contextLength := errs.ErrorMessageContextDefault
		if v := a.Get(2, "context_length", nil); v != nil {
			contextLength = mustInt(v)
		}
		formatted, start, highlight, end := errs.HighlightSQL([]rune(Str(a.Pos[0])), positions, contextLength)
		return Tuple{formatted, start, highlight, end}
	})
}
