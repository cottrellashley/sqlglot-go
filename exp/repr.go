package exp

import (
	"fmt"
	"strconv"
	"strings"
)

// Repr mirrors repr(expression): an indented tree representation.
func (e *Expr) Repr() string { return toS(e, false, 0, false) }

// ToS mirrors Expression.to_s(): Repr including empty args.
func (e *Expr) ToS() string { return toS(e, true, 0, false) }

func toS(node any, verbose bool, level int, reprStr bool) string {
	indent := "\n" + strings.Repeat("  ", level+1)
	delim := "," + indent

	switch n := node.(type) {
	case *Expr:
		if n == nil {
			return "None"
		}
		type kv struct {
			k string
			v any
		}
		var args []kv
		for _, a := range n.args {
			if a.Value == nil && !verbose {
				continue
			}
			if l, ok := a.Value.([]*Expr); ok && len(l) == 0 && !verbose {
				continue
			}
			args = append(args, kv{a.Key, a.Value})
		}
		if (n.Type() != nil || verbose) && !kindTable[n.kind].isDataType {
			args = append(args, kv{"_type", n.Type()})
		}
		if len(n.Comments) > 0 || verbose {
			args = append(args, kv{"_comments", n.Comments})
		}
		if verbose {
			args = append(args, kv{"_id", fmt.Sprintf("%p", n)})
		}
		if n.IsLeaf() {
			indent = ""
			delim = ", "
		}
		rs := n.IsString() || (n.Is(Identifier) && n.Quoted())
		items := make([]string, len(args))
		for i, a := range args {
			items[i] = a.k + "=" + toS(a.v, verbose, level+1, rs)
		}
		return n.ClassName() + "(" + indent + strings.Join(items, delim) + ")"
	case []*Expr:
		items := make([]string, len(n))
		for i, x := range n {
			items[i] = toS(x, verbose, level+1, false)
		}
		s := strings.Join(items, delim)
		if s != "" {
			s = indent + s
		}
		return "[" + s + "]"
	case []string:
		items := make([]string, len(n))
		for i, x := range n {
			items[i] = toS(x, verbose, level+1, false)
		}
		s := strings.Join(items, delim)
		if s != "" {
			s = indent + s
		}
		return "[" + s + "]"
	case nil:
		return "None"
	case bool:
		if n {
			return "True"
		}
		return "False"
	case DType:
		return "DType." + n.Name()
	case string:
		s := n
		if reprStr {
			s = pyRepr(s)
		}
		return strings.Join(strings.Split(dedent(strings.Trim(s, "\n")), "\n"), indent)
	}
	s := fmt.Sprint(node)
	return strings.Join(strings.Split(dedent(strings.Trim(s, "\n")), "\n"), indent)
}

// pyRepr approximates Python's repr() of a str.
func pyRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(fmt.Sprintf(`\x%02x`, r))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// dedent mirrors textwrap.dedent.
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	prefix := ""
	first := true
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		ws := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if first {
			prefix = ws
			first = false
		} else {
			for !strings.HasPrefix(ws, prefix) {
				prefix = prefix[:len(prefix)-1]
			}
		}
	}
	if prefix == "" {
		return s
	}
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[i] = strings.TrimLeft(l, " \t")
		} else {
			lines[i] = strings.TrimPrefix(l, prefix)
		}
	}
	return strings.Join(lines, "\n")
}

var _ = strconv.Itoa

// PyRepr mirrors Python's repr() of a string.
func PyRepr(s string) string { return pyRepr(s) }
