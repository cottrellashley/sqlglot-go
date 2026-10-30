package sqlglot

// Port of sqlglot/time.py.

import (
	"regexp"
	"strings"

	"github.com/cottrellashley/sqlglot-go/trie"
)

// FormatTime mirrors time.format_time: it converts a time format string using the given
// mapping (e.g. "%Y" -> "yyyy"), greedily matching the longest keys with the trie. An empty
// input returns "" (Python None).
func FormatTime(s string, mapping map[string]string, t *trie.Node[rune]) string {
	if s == "" {
		return ""
	}
	if t == nil || t.Empty() {
		keys := make([]string, 0, len(mapping))
		for k := range mapping {
			keys = append(keys, k)
		}
		t = trie.NewFromStrings(keys...)
	}
	runes := []rune(s)
	start, end, size := 0, 1, len(runes)
	current := t
	var chunks []string
	sym := ""
	hasSym := false

	for end <= size {
		chars := string(runes[start:end])
		result, next := current.Step(runes[end-1])
		current = next
		if result == trie.Failed {
			if hasSym {
				end--
				chars = sym
				sym, hasSym = "", false
			} else {
				chars = string(runes[start])
				end = start + 1
			}
			start += len([]rune(chars))
			chunks = append(chunks, chars)
			current = t
		} else if result == trie.Exists {
			sym, hasSym = chars, true
		}
		end++
		if result != trie.Failed && end > size {
			chunks = append(chunks, chars)
		}
	}

	var b strings.Builder
	for _, c := range chunks {
		if m, ok := mapping[c]; ok {
			b.WriteString(m)
		} else {
			b.WriteString(c)
		}
	}
	return b.String()
}

var isoDatetimeRe = regexp.MustCompile(
	`^\d{4}-?\d{2}-?\d{2}(?:[T ]\d{2}(?::?\d{2}(?::?\d{2}(?:[.,](\d+))?)?)?)?` +
		`(?:Z|[+-]\d{2}(?::?\d{2}(?::?\d{2}(?:\.\d+)?)?)?)?$`)

// SubsecondPrecision mirrors time.subsecond_precision: it returns 0, 3 or 6 depending on the
// number of significant sub-second digits of an ISO timestamp literal (0 if it can't be
// parsed).
func SubsecondPrecision(timestampLiteral string) int {
	m := isoDatetimeRe.FindStringSubmatch(timestampLiteral)
	if m == nil {
		return 0
	}
	frac := m[1]
	if len(frac) > 6 {
		frac = frac[:6]
	}
	frac = frac + strings.Repeat("0", 6-len(frac))
	digits := len(strings.TrimRight(strings.TrimLeft(frac, ""), "0"))
	if strings.Trim(frac, "0") == "" {
		digits = 0
	}
	switch {
	case digits > 3:
		return 6
	case digits > 0:
		return 3
	}
	return 0
}
