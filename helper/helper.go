// Package helper contains small utilities mirroring sqlglot/helper.py.
package helper

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// CamelToSnakeCase converts "CamelCase" to "CAMEL_CASE" (sqlglot's camel_to_snake_case).
func CamelToSnakeCase(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// SeqGet returns seq[index] (supporting negative indices) or the zero value.
func SeqGet[T any](seq []T, index int) T {
	var zero T
	if index < 0 {
		index += len(seq)
	}
	if index < 0 || index >= len(seq) {
		return zero
	}
	return seq[index]
}

// CSV joins the non-empty args with sep (sqlglot's csv).
func CSV(sep string, args ...string) string {
	var parts []string
	for _, a := range args {
		if a != "" {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, sep)
}

// FindNewName mirrors find_new_name.
func FindNewName(taken func(string) bool, base string) string {
	if !taken(base) {
		return base
	}
	i := 2
	n := fmt.Sprintf("%s_%d", base, i)
	for taken(n) {
		i++
		n = fmt.Sprintf("%s_%d", base, i)
	}
	return n
}

// FindNewNameIn is FindNewName over a set.
func FindNewNameIn(taken map[string]bool, base string) string {
	return FindNewName(func(s string) bool { return taken[s] }, base)
}

// NameSequence returns a generator of names prefix0, prefix1, ...
func NameSequence(prefix string) func() string {
	i := 0
	return func() string {
		s := prefix + strconv.Itoa(i)
		i++
		return s
	}
}

// SplitNumWords mirrors split_num_words. Missing words are returned as nil pointers.
func SplitNumWords(value, sep string, minNumWords int, fillFromStart bool) []*string {
	words := strings.Split(value, sep)
	out := make([]*string, 0, max(minNumWords, len(words)))
	pad := minNumWords - len(words)
	if fillFromStart {
		for i := 0; i < pad; i++ {
			out = append(out, nil)
		}
	}
	for i := range words {
		w := words[i]
		out = append(out, &w)
	}
	if !fillFromStart {
		for i := 0; i < pad; i++ {
			out = append(out, nil)
		}
	}
	return out
}

// ToBool mirrors to_bool for strings: "true"/"1" -> true, "false"/"0" -> false, otherwise
// the original string is returned.
func ToBool(value string) any {
	switch strings.ToLower(value) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	}
	return value
}

var intRe = regexp.MustCompile(`^\s*[+-]?(\d+(_\d+)*)\s*$`)

// IsInt mirrors is_int (Python int() semantics for strings).
func IsInt(text string) bool { return intRe.MatchString(text) }

// IsFloat mirrors is_float (Python float() semantics for strings).
func IsFloat(text string) bool {
	s := strings.TrimSpace(strings.ReplaceAll(text, "_", ""))
	if s == "" {
		return false
	}
	switch strings.ToLower(strings.TrimLeft(s, "+-")) {
	case "inf", "infinity", "nan":
		return true
	}
	_, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return true
		}
		return false
	}
	return true
}

// ToInt mirrors to_int: converts an integral decimal string to an int, or returns false.
func ToInt(value string) (*big.Int, bool) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok || !r.IsInt() {
		return nil, false
	}
	return r.Num(), true
}

var isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$|^\d{8}$|^\d{4}-W\d{2}(-\d)?$`)

// IsISODate mirrors is_iso_date (datetime.date.fromisoformat).
func IsISODate(text string) bool {
	if !isoDateRe.MatchString(text) {
		return false
	}
	if len(text) == 10 && text[4] == '-' {
		_, err := time.Parse("2006-01-02", text)
		return err == nil
	}
	if len(text) == 8 {
		_, err := time.Parse("20060102", text)
		return err == nil
	}
	return true
}

// IsISODatetime mirrors is_iso_datetime (datetime.datetime.fromisoformat, Python 3.11+).
func IsISODatetime(text string) bool {
	if IsISODate(text) {
		return true
	}
	if len(text) < 11 {
		return false
	}
	datePart, rest := text[:10], text[11:]
	if !IsISODate(datePart) {
		return false
	}
	sep := text[10]
	if sep >= 0x80 {
		return false
	}
	// time part: HH[:MM[:SS[.fff[fff]]]][+HH:MM|Z]
	return isoTimeRe.MatchString(rest)
}

var isoTimeRe = regexp.MustCompile(`^\d{2}(:?\d{2}(:?\d{2}([.,]\d{1,9})?)?)?(Z|[+-]\d{2}(:?\d{2}(:?\d{2}([.,]\d{1,6})?)?)?)?$`)

// TSort mirrors tsort: a topological sort of a DAG (node -> dependencies).
func TSort(dag map[string]map[string]bool) ([]string, error) {
	d := map[string]map[string]bool{}
	for k, deps := range dag {
		cp := map[string]bool{}
		for dep := range deps {
			cp[dep] = true
		}
		d[k] = cp
	}
	for _, deps := range dag {
		for dep := range deps {
			if _, ok := d[dep]; !ok {
				d[dep] = map[string]bool{}
			}
		}
	}
	var result []string
	for len(d) > 0 {
		var current []string
		for node, deps := range d {
			if len(deps) == 0 {
				current = append(current, node)
			}
		}
		if len(current) == 0 {
			return nil, fmt.Errorf("Cycle error")
		}
		for _, n := range current {
			delete(d, n)
		}
		for _, deps := range d {
			for _, n := range current {
				delete(deps, n)
			}
		}
		sort.Strings(current)
		result = append(result, current...)
	}
	return result, nil
}

// DateUnits mirrors DATE_UNITS.
var DateUnits = map[string]bool{"day": true, "week": true, "month": true, "quarter": true, "year": true, "year_month": true}

// SuggestClosestMatch returns a " Did you mean X?" suffix for the closest match, if any.
func SuggestClosestMatch(word string, possibilities []string) string {
	best, bestScore := "", 0.0
	for _, p := range possibilities {
		if s := similarity(word, p); s > bestScore {
			best, bestScore = p, s
		}
	}
	if bestScore >= 0.6 {
		return fmt.Sprintf(" Did you mean %s?", best)
	}
	return ""
}

// similarity approximates difflib.SequenceMatcher.ratio.
func similarity(a, b string) float64 {
	if len(a)+len(b) == 0 {
		return 1
	}
	m := matches([]rune(a), []rune(b))
	return 2 * float64(m) / float64(len([]rune(a))+len([]rune(b)))
}

func matches(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// longest common substring, recursively (Ratcliff/Obershelp)
	bestI, bestJ, bestK := 0, 0, 0
	for i := range a {
		for j := range b {
			k := 0
			for i+k < len(a) && j+k < len(b) && a[i+k] == b[j+k] {
				k++
			}
			if k > bestK {
				bestI, bestJ, bestK = i, j, k
			}
		}
	}
	if bestK == 0 {
		return 0
	}
	return bestK + matches(a[:bestI], b[:bestJ]) + matches(a[bestI+bestK:], b[bestJ+bestK:])
}
