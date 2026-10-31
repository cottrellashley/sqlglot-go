package pyrt

// Bindings for sqlglot.lineage.

import (
	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

var lineageNodes = map[*sqlglot.LineageNode]*LineageNodeV{}

// LineageNodeV wraps a lineage node. Each node has a single wrapper, so identity checks and
// payload updates work.
type LineageNodeV struct {
	N       *sqlglot.LineageNode
	payload *Dict
}

func lineageNodeValue(n *sqlglot.LineageNode) *LineageNodeV {
	if v, ok := lineageNodes[n]; ok {
		return v
	}
	v := &LineageNodeV{N: n}
	lineageNodes[n] = v
	return v
}

func (v *LineageNodeV) payloadDict() *Dict {
	if v.payload == nil {
		v.payload = NewDict()
		for _, k := range sortedKeys(v.N.Payload) {
			v.payload.Set(k, fromGo(v.N.Payload[k]))
		}
		n := v.N
		v.payload.onSet = func(k, val Value) { n.Payload[Str(k)] = val }
		v.payload.onDelete = func(k Value) { delete(n.Payload, Str(k)) }
	}
	return v.payload
}

// PyAttr implements Attrer.
func (v *LineageNodeV) PyAttr(name string) (Value, bool) {
	n := v.N
	switch name {
	case "name":
		return n.Name, true
	case "expression":
		return n.Expression, true
	case "source":
		return n.Source, true
	case "source_name":
		return n.SourceName, true
	case "reference_node_name":
		return n.ReferenceNodeName, true
	case "downstream":
		out := make([]Value, len(n.Downstream))
		for i, d := range n.Downstream {
			out[i] = lineageNodeValue(d)
		}
		return &List{out}, true
	case "payload":
		return v.payloadDict(), true
	case "walk":
		return Func(name, func(Args) Value {
			var out []Value
			for d := range n.Walk() {
				out = append(out, lineageNodeValue(d))
			}
			return &Iterator{items: out}
		}), true
	case "to_html":
		return Func(name, func(a Args) Value {
			return Guard(func() Value {
				o := sqlglot.GraphHTMLOptions{NoImports: !truthArg(a, -1, "imports", true)}
				if opts, ok := norm(a.Get(-1, "options", nil)).(*Dict); ok {
					o.Options = map[string]any{}
					for i, k := range opts.keys {
						o.Options[Str(k)] = toGo(opts.vals[i])
					}
				}
				return &GraphHTMLV{n.ToHTML(dialectArg(a.Get(0, "dialect", nil)), o)}
			})
		}), true
	}
	return nil, false
}

// GraphHTMLV wraps a lineage graph HTML renderer.
type GraphHTMLV struct{ G *sqlglot.GraphHTML }

// String implements str().
func (g *GraphHTMLV) String() string { return g.G.String() }

// PyAttr implements Attrer.
func (g *GraphHTMLV) PyAttr(name string) (Value, bool) {
	switch name {
	case "_repr_html_":
		return Func(name, func(Args) Value { return g.G.String() }), true
	case "edges":
		var out []Value
		for _, e := range g.G.Edges {
			out = append(out, fromGo(e))
		}
		return &List{out}, true
	case "nodes":
		d := NewDict()
		for _, n := range g.G.Nodes {
			d.Set(n["id"], fromGo(n))
		}
		return d, true
	}
	return nil, false
}

func init() {
	sqlglotModule.Attrs["lineage"] = Func("lineage", func(a Args) Value {
		return Guard(func() Value {
			o := sqlglot.LineageOptions{
				Dialect:       dialectArg(a.Get(4, "dialect", nil)),
				NoTrimSelects: !truthArg(a, 6, "trim_selects", true),
				NoCopy:        !truthArg(a, 7, "copy", true),
			}
			if s := a.Get(3, "sources", nil); s != nil {
				o.Sources = map[string]any{}
				d := norm(s).(*Dict)
				for i, k := range d.keys {
					o.Sources[Str(k)] = toGo(d.vals[i])
				}
			}
			if s := a.Get(5, "scope", nil); s != nil {
				o.Scope = scopeArg(s)
			}
			if f := a.Get(8, "on_node", nil); f != nil {
				o.OnNode = func(n *sqlglot.LineageNode) { Call(f, lineageNodeValue(n)) }
			}
			var qo *sqlglot.QualifyOptions
			for _, kw := range a.KW {
				switch kw.K {
				case "sql", "column", "schema", "sources", "dialect", "scope", "trim_selects", "copy", "on_node":
					continue
				}
				if qo == nil {
					qo = &sqlglot.QualifyOptions{NoValidateQualifyColumns: true, NoIdentify: true}
				}
				switch kw.K {
				case "validate_qualify_columns":
					qo.NoValidateQualifyColumns = !Truth(kw.V)
				case "identify":
					qo.NoIdentify = !Truth(kw.V)
				case "expand_stars":
					qo.NoExpandStars = !Truth(kw.V)
				case "expand_alias_refs":
					qo.NoExpandAliasRefs = !Truth(kw.V)
				case "db":
					qo.DB = toGo(kw.V)
				case "catalog":
					qo.Catalog = toGo(kw.V)
				default:
					NotPorted("lineage(%s=...)", kw.K)
				}
			}
			o.Qualify = qo
			var schema any
			if s := a.Get(2, "schema", nil); s != nil {
				schema = toSchema(s)
			}
			sql := toGo(a.Get(1, "sql", nil))
			column := a.Get(0, "column", nil)
			if column == nil {
				result, err := sqlglot.LineageAll(sql, schema, o)
				check(err)
				d := NewDict()
				for k, n := range result.All() {
					d.Set(k, lineageNodeValue(n))
				}
				return d
			}
			col := toGo(column)
			if c, ok := col.(*exp.Expr); ok {
				col = c
			}
			n, err := sqlglot.Lineage(col, sql, schema, o)
			check(err)
			return lineageNodeValue(n)
		})
	})
}

func tokensArg(v Value) []*tokens.Token {
	var out []*tokens.Token
	for _, t := range Iter(v) {
		tv, ok := norm(t).(*TokenV)
		if !ok {
			Raise(TypeError, "expected tokens, got %s", TypeName(t))
		}
		out = append(out, tv.T)
	}
	return out
}

func init() {
	sqlglotModule.Attrs["anonymize"] = Func("anonymize", func(a Args) Value {
		return Guard(func() Value {
			var input any
			if s, ok := norm(a.Pos[0]).(string); ok {
				input = s
			} else {
				input = tokensArg(a.Pos[0])
			}
			toks, err := sqlglot.Anonymize(input, dialectArg(a.Get(1, "dialect", nil)))
			check(err)
			if l, ok := norm(a.Pos[0]).(*List); ok {
				// the Python function mutates and returns the given list
				l.V = tokenList(toks).V
				return l
			}
			return tokenList(toks)
		})
	})
	sqlglotModule.Attrs["render"] = Func("render", func(a Args) Value {
		return Guard(func() Value {
			out, err := sqlglot.Render(Str(a.Pos[0]), tokensArg(a.Pos[1]), dialectArg(a.Get(2, "dialect", nil)))
			check(err)
			return out
		})
	})
}
