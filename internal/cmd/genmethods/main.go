// Command genmethods generates the overridable-method tables of the parser and generator.
//
// Every unexported method of *Parser declared in parser_*.go files (except the core files)
// becomes a field of ParserMethods plus an exported wrapper that dispatches through it, so
// dialects can override any parser method exactly like Python subclasses do.
//
// For *Generator, methods named <kind>SQL(e *exp.Expr) string are registered in the
// per-kind SQL method table (the Python <key>_sql auto-discovery), every other unexported
// method in generator_*.go files becomes a field of GeneratorMethods.
//
// Run with `go generate ./...` from the module root.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
)

type method struct {
	name     string // unexported impl name
	params   []*ast.Field
	results  *ast.FieldList
	recv     string
	isKind   bool
	kind     string
	dispatch string // //gen:dispatch expression for kind methods with extra parameters
}

// kindForMethod returns the kind whose <key>SQL method this is ("" if none). The prefix must be
// exactly a (lowercase) expression key, mirroring Python's `<key>_sql` auto-discovery.
func kindForMethod(name string) string {
	if !strings.HasSuffix(name, "SQL") {
		return ""
	}
	key := strings.TrimSuffix(name, "SQL")
	if key == "" || key != strings.ToLower(key) {
		return ""
	}
	if k, ok := exp.KindByKey(key); ok {
		return k.String()
	}
	return ""
}

func dispatchDirective(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	for _, c := range doc.List {
		if rest, ok := strings.CutPrefix(c.Text, "//gen:dispatch "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func typeString(fset *token.FileSet, e ast.Expr) string {
	var b bytes.Buffer
	printer.Fprint(&b, fset, e)
	return b.String()
}

func exported(name string) string { return strings.ToUpper(name[:1]) + name[1:] }

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	fset := token.NewFileSet()
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	skip := map[string]bool{
		"parser_core.go": true, "parser_config.go": true, "parser_base_data.go": true,
		"parser_methods_gen.go": true, "generator_core.go": true, "generator_config.go": true,
		"generator_methods_gen.go": true, "generator_base_data.go": true,
	}
	var parserMethods, generatorMethods []method
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_test.go") || skip[base] {
			continue
		}
		isParser := strings.HasPrefix(base, "parser_")
		isGen := strings.HasPrefix(base, "generator_")
		if !isParser && !isGen {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
				continue
			}
			star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			id, ok := star.X.(*ast.Ident)
			if !ok {
				continue
			}
			name := fd.Name.Name
			if ast.IsExported(name) || strings.HasPrefix(name, "_") {
				continue
			}
			m := method{name: name, params: fd.Type.Params.List, results: fd.Type.Results, recv: id.Name}
			if id.Name == "Parser" && isParser {
				parserMethods = append(parserMethods, m)
			} else if id.Name == "Generator" && isGen {
				kind := kindForMethod(name)
				simple := len(m.params) == 1 && len(m.params[0].Names) == 1 &&
					typeString(fset, m.params[0].Type) == "*exp.Expr" && m.results != nil &&
					len(m.results.List) == 1 && typeString(fset, m.results.List[0].Type) == "string"
				if kind != "" && simple {
					m.isKind = true
					m.kind = kind
				} else if kind != "" {
					m.kind = kind
					m.dispatch = dispatchDirective(fd.Doc)
					if m.dispatch == "" {
						fmt.Fprintf(os.Stderr, "%s: kind method with extra parameters needs a //gen:dispatch directive\n", name)
						os.Exit(1)
					}
				}
				generatorMethods = append(generatorMethods, m)
			}
		}
	}
	sort.Slice(parserMethods, func(i, j int) bool { return parserMethods[i].name < parserMethods[j].name })
	sort.Slice(generatorMethods, func(i, j int) bool { return generatorMethods[i].name < generatorMethods[j].name })

	write(filepath.Join(dir, "parser_methods_gen.go"), emit(fset, "Parser", "p", "ParserMethods", parserMethods, false))
	write(filepath.Join(dir, "generator_methods_gen.go"), emit(fset, "Generator", "g", "GeneratorMethods", generatorMethods, true))
}

func write(path string, src []byte) {
	out, err := format.Source(src)
	if err != nil {
		fmt.Fprintln(os.Stderr, string(src))
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func params(fset *token.FileSet, fields []*ast.Field) (decl, call string) {
	var ds, cs []string
	i := 0
	for _, f := range fields {
		t := typeString(fset, f.Type)
		names := f.Names
		if len(names) == 0 {
			names = []*ast.Ident{ast.NewIdent(fmt.Sprintf("a%d", i))}
		}
		for _, n := range names {
			nm := n.Name
			if nm == "_" {
				nm = fmt.Sprintf("a%d", i)
			}
			ds = append(ds, nm+" "+t)
			if strings.HasPrefix(t, "...") {
				cs = append(cs, nm+"...")
			} else {
				cs = append(cs, nm)
			}
			i++
		}
	}
	return strings.Join(ds, ", "), strings.Join(cs, ", ")
}

func results(fset *token.FileSet, r *ast.FieldList) string {
	if r == nil || len(r.List) == 0 {
		return ""
	}
	var parts []string
	named := false
	for _, f := range r.List {
		t := typeString(fset, f.Type)
		if len(f.Names) > 0 {
			named = true
			for range f.Names {
				parts = append(parts, t)
			}
		} else {
			parts = append(parts, t)
		}
	}
	_ = named
	if len(parts) == 1 {
		return " " + parts[0]
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func emit(fset *token.FileSet, recv, r, table string, methods []method, gen bool) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by internal/cmd/genmethods; DO NOT EDIT.\n\npackage sqlglot\n\n")
	fmt.Fprintf(&b, "import (\n\t\"github.com/cottrellashley/sqlglot-go/exp\"\n\t\"github.com/cottrellashley/sqlglot-go/tokens\"\n\t\"github.com/cottrellashley/sqlglot-go/trie\"\n)\n\nvar _ = tokens.Comma\nvar _ exp.Kind\nvar _ *trie.Node[rune]\n\n")
	fmt.Fprintf(&b, "// %s holds the overridable %s methods. Dialects override an entry to change\n// the behaviour of the corresponding exported method (the Python method override).\ntype %s struct {\n", table, strings.ToLower(recv), table)
	for _, m := range methods {
		if m.isKind {
			continue
		}
		d, _ := params(fset, m.params)
		sep := ""
		if d != "" {
			sep = ", "
		}
		fmt.Fprintf(&b, "\t%s func(%s *%s%s%s)%s\n", exported(m.name), r, recv, sep, d, results(fset, m.results))
	}
	fmt.Fprintf(&b, "}\n\n")
	for _, m := range methods {
		d, c := params(fset, m.params)
		res := results(fset, m.results)
		ret := "return "
		if res == "" {
			ret = ""
		}
		if m.isKind {
			fmt.Fprintf(&b, "// %s generates SQL for exp.%s (dispatching through the per-kind SQL methods).\n", exported(m.name), m.kind)
			fmt.Fprintf(&b, "func (%s *%s) %s(%s)%s { %s%s.callKindMethod(exp.%s, %s) }\n\n", r, recv, exported(m.name), d, res, ret, r, m.kind, c)
			continue
		}
		sep := ""
		if c != "" {
			sep = ", "
		}
		fmt.Fprintf(&b, "func (%s *%s) %s(%s)%s { %s%s.C.Methods.%s(%s%s%s) }\n\n", r, recv, exported(m.name), d, res, ret, r, exported(m.name), r, sep, c)
	}
	fmt.Fprintf(&b, "func base%s() %s {\n\treturn %s{\n", table, table, table)
	for _, m := range methods {
		if m.isKind {
			continue
		}
		fmt.Fprintf(&b, "\t\t%s: (*%s).%s,\n", exported(m.name), recv, m.name)
	}
	fmt.Fprintf(&b, "\t}\n}\n")
	if gen {
		fmt.Fprintf(&b, "\nfunc baseKindSQLMethods() map[exp.Kind]GenFunc {\n\treturn map[exp.Kind]GenFunc{\n")
		for _, m := range methods {
			if m.isKind {
				fmt.Fprintf(&b, "\t\texp.%s: (*Generator).%s,\n", m.kind, m.name)
			} else if m.dispatch != "" {
				fmt.Fprintf(&b, "\t\texp.%s: func(g *Generator, e *exp.Expr) string { return %s },\n", m.kind, m.dispatch)
			}
		}
		fmt.Fprintf(&b, "\t}\n}\n")
	}
	return b.Bytes()
}
