"""Converts a sqlglot Python unittest module into a Go test file running on internal/pyrt.

Usage: python3 -I convert_tests.py <python test file> <relative source path> <go package> > out_test.go

Every test class becomes a pyrt.Class and every method a Go function. Statements are
translated one by one; constructs the converter does not support become calls to NotPorted,
which fail the enclosing statement at run time (so unported code is never silently skipped).
"""

import ast
import json
import re
import sys

GO_RESERVED = {
    # keywords
    "break", "case", "chan", "const", "continue", "default", "defer", "else", "fallthrough",
    "for", "func", "go", "goto", "if", "import", "interface", "map", "package", "range",
    "return", "select", "struct", "switch", "type", "var",
    # predeclared identifiers
    "any", "append", "bool", "byte", "cap", "clear", "close", "comparable", "complex",
    "complex64", "complex128", "copy", "delete", "error", "false", "float32", "float64",
    "imag", "int", "int8", "int16", "int32", "int64", "iota", "len", "make", "max", "min",
    "new", "nil", "panic", "print", "println", "real", "recover", "rune", "string", "true",
    "uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
    # names used by the generated code
    "self", "t", "testing", "a", "it",
}

BINOPS = {
    ast.Add: "Add", ast.Sub: "Sub", ast.Mult: "Mul", ast.Div: "Div", ast.FloorDiv: "FloorDiv",
    ast.Mod: "Mod", ast.Pow: "Pow",
}
BITOPS = {ast.BitAnd: "&", ast.BitOr: "|", ast.BitXor: "^", ast.LShift: "<<", ast.RShift: ">>"}
CMPOPS = {
    ast.Eq: "Eq", ast.NotEq: "Ne", ast.Lt: "Lt", ast.LtE: "Le", ast.Gt: "Gt", ast.GtE: "Ge",
    ast.Is: "Is", ast.IsNot: "IsNot", ast.In: "In", ast.NotIn: "NotIn",
}
# Test methods that exercise Python-only mechanics. They become documented skips; the reason
# names the Go-native test covering the same behavior where there is one.
PYTHON_ONLY = {
    ("tests/dialects/test_dialect.py", "TestDialect.test_lazy_load"):
        "python-only: checks that importing sqlglot loads dialect modules lazily (Go packages are linked statically)",
    ("tests/dialects/test_dialect.py", "TestDialect.test_get_or_raise"):
        "python-only: defines a Dialect subclass inline; see TestCustomDialectSettings in dialect_extension_test.go",
    ("tests/dialects/test_dialect.py", "TestDialect.test_patch_dialect_parser"):
        "python-only: patches Snowflake.parser_class with a Python subclass; see TestPatchDialectParser in dialect_extension_test.go",
    ("tests/dialects/test_dialect.py", "TestDialect.test_custom_dialect"):
        "python-only: subclasses Dialect and SnowflakeParser inline; see TestCustomDialect in dialect_extension_test.go",
    ("tests/test_expressions.py", "TestExprs.test_eq_on_same_instance_short_circuits"):
        "python-only: inspects the private hash cache of an expression",
    ("tests/test_expressions.py", "TestExprs.test_convert"):
        "python-only: converts namedtuples, plain objects and pytz datetimes; see TestConvertValue in exp/convert_test.go",
    ("tests/test_expressions.py", "TestExprs.test_convert_python39"):
        "python-only: converts zoneinfo datetimes; see TestConvertValue in exp/convert_test.go",
    ("tests/test_optimizer.py", "TestOptimizer.test_unnest_type_trace_is_memoized"):
        "python-only: counts calls of a private Resolver method with patch.object",
    ("tests/test_optimizer.py", "TestOptimizer.test_annotate_types_caches_positional_aliases"):
        "python-only: counts calls of MappingSchema.column_names with patch.object",
    ("tests/test_optimizer.py", "TestOptimizer.test_resolver_column_context"):
        "python-only: monkeypatches Resolver.get_table with patch.object",
    ("tests/test_optimizer.py", "TestOptimizer.test_qualify_skips_unneeded_annotation"):
        "python-only: patches the TypeAnnotator class to inspect its private state",
    ("tests/test_optimizer.py", "TestOptimizer.test_annotate_types_caches_schema_lookups"):
        "python-only: inspects the private lookup cache of MappingSchema",
    ("tests/test_executor.py", "TestExecutor.test_py_dialect"):
        "python-only: checks the Python source generated for expressions (the Go executor compiles them to closures)",
    ("tests/test_executor.py", "TestExecutor.test_optimized_tpch"):
        "needs DuckDB: compares the optimized TPC-H queries' results with DuckDB's",
    ("tests/test_executor.py", "TestExecutor.test_execute_tpch"):
        "needs DuckDB: compares the TPC-H query results with DuckDB's",
    ("tests/test_executor.py", "TestExecutor.test_execute_tpcds"):
        "needs DuckDB: compares the TPC-DS query results with DuckDB's",
    ("tests/test_executor.py", "TestExecutor.test_sql_boolean_logic_with_numpy_scalars"):
        "python-only: evaluates numpy scalars",
    ("tests/test_executor.py", "TestExecutor.test_subquery_memoization"):
        "python-only: inspects PythonExecutor's private subquery cache; see TestSubqueryMemoization in executor/internal_test.go",
    ("tests/test_executor.py", "TestExecutor.test_subquery_execution_does_not_mutate_the_plan"):
        "python-only: drives PythonExecutor directly; see TestSubqueryExecutionDoesNotMutateThePlan in executor/internal_test.go",
    ("tests/test_executor.py", "TestExecutor.test_sql_boolean_logic_preserves_short_circuiting"):
        "python-only: drives PythonExecutor directly; see TestBooleanLogicShortCircuits in executor/internal_test.go",
    ("tests/test_executor.py", "TestExecutor.test_agg_order"):
        "python-only: inspects a Plan built by Python; see TestAggregationOrder in executor/internal_test.go",
    ("tests/test_serde.py", "TestSerde.test_custom_expression"):
        "python-only: subclasses exp.Expression at module level; see TestSerdeRegisteredKind in exp/serde_test.go",
    ("tests/test_schema.py", "TestSchema.test_custom_schema"):
        "python-only: subclasses Schema inline; see TestCustomSchema in schema_test.go",
    ("tests/test_typing.py", "TestTyping.test_no_redundant_expression_metadata"):
        "python-only: lints the Python sources of sqlglot.typing (module files and bytecode)",
    ("tests/test_generator.py", "TestGenerator.test_fallback_function_sql"):
        "python-only: subclasses exp.Func inline; see TestRegisterKind in dialect_extension_test.go",
    ("tests/test_generator.py", "TestGenerator.test_fallback_function_var_args_sql"):
        "python-only: subclasses exp.Func inline; see TestRegisterKind in dialect_extension_test.go",
}

# self.<method>(...) calls with a dedicated Go method on *pyrt.Case
CASE_METHODS = {
    "validate_identity": "ValidateIdentity",
    "validate_all": "ValidateAll",
    "validate_transpile": "ValidateTranspile",
    "parse_one": "ParseOne",
    "assertEqual": "AssertEqual",
}
# builtins with a direct pyrt function
DIRECT_BUILTINS = {
    "len": "Len", "str": "Str", "repr": "Repr", "isinstance": "IsInstance", "bool": "Bool",
    "int": "Int", "float": "Float", "hash": "Hash", "hasattr": "HasAttr",
}


class NotPortedErr(Exception):
    pass


def go_str(s):
    if any(0xD800 <= ord(c) <= 0xDFFF for c in s):
        raise NotPortedErr("string with lone surrogates")
    printable = all(c.isprintable() or c == "\n" for c in s)
    if ("\\" in s or '"' in s) and "`" not in s and "\r" not in s and printable and "\n" not in s:
        return "`" + s + "`"
    out = json.dumps(s, ensure_ascii=False)
    # JSON leaves some characters raw that gofmt dislikes; escape them explicitly
    return "".join(c if c.isprintable() or c in "\n" else "\\u%04x" % ord(c) for c in out)


def camel(name):
    return "".join(p[:1].upper() + p[1:] for p in name.split("_") if p)


class FuncCtx:
    """Per-function translation state."""

    def __init__(self, conv, kind, params=()):
        self.conv = conv
        self.kind = kind  # "method", "function" or "module"
        self.locals = {}
        for p in params:
            self.declare(p)
        self.tmp = 0
        self.raw = False

    def declare(self, name):
        if name not in self.locals:
            go = name if name not in GO_RESERVED and not name[:1].isupper() else name + "_"
            if name.strip("_") == "":
                go = "underscore" + str(len(name))
            if go in self.locals.values():
                go += "_"
            self.locals[name] = go
        return self.locals[name]

    def fresh(self, prefix="tmp"):
        self.tmp += 1
        return f"{prefix}{self.tmp}"


def assigned_names(nodes):
    """Names bound by assignments in a function body (Python function scope)."""
    names = []

    class V(ast.NodeVisitor):
        def visit_FunctionDef(self, node):
            names.append(node.name)

        def visit_Lambda(self, node):
            pass

        def visit_ListComp(self, node):
            pass

        visit_SetComp = visit_DictComp = visit_GeneratorExp = visit_ListComp

        def visit_ClassDef(self, node):
            pass

        def visit_Name(self, node):
            if isinstance(node.ctx, ast.Store):
                names.append(node.id)

        def visit_ExceptHandler(self, node):
            if node.name:
                names.append(node.name)
            self.generic_visit(node)

        def visit_NamedExpr(self, node):
            names.append(node.target.id)
            self.generic_visit(node)

        def visit_Import(self, node):
            pass

        visit_ImportFrom = visit_Import

    for n in nodes:
        V().visit(n)
    out = []
    for n in names:
        if n not in out:
            out.append(n)
    return out


class Converter:
    def __init__(self, src, relpath, pkg):
        self.src = src
        self.lines = src.splitlines()
        self.relpath = relpath
        self.pkg = pkg
        self.tree = ast.parse(src)
        self.prefix = re.sub(r"\W", "_", relpath.rsplit("/", 1)[-1].rsplit(".", 1)[0])
        self.imports = {}  # local name -> qualified python name
        self.module_names = {}  # module-level defined names -> go var
        self.classes = {}
        self.not_ported = 0
        self.stmts = 0
        self.out = []

    # ------------------------------------------------------------------ helpers
    def segment(self, node):
        try:
            s = ast.get_source_segment(self.src, node) or ast.unparse(node)
        except Exception:
            s = ast.unparse(node)
        s = " ".join(s.split())
        return s[:200]

    def np(self, node, why=""):
        self.not_ported += 1
        msg = self.segment(node)
        if why:
            msg = f"{why}: {msg}"
        return f"NP({go_str('%s')}, {go_str(msg)})"

    # ------------------------------------------------------------------ names
    def name_ref(self, name, ctx):
        if ctx is not None and name in ctx.locals:
            return ctx.locals[name]
        if name == "self":
            return "self"
        if name in self.module_names:
            return self.module_names[name]
        if name in ("True", "False", "None"):
            return {"True": "true", "False": "false", "None": "nil"}[name]
        return f"G({go_str(self.imports.get(name, name))})"

    # ------------------------------------------------------------------ expressions
    def expr(self, node, ctx):
        m = getattr(self, "e_" + type(node).__name__, None)
        if m is None:
            raise NotPortedErr(type(node).__name__)
        return m(node, ctx)

    def e_Constant(self, node, ctx):
        v = node.value
        if v is None:
            return "nil"
        if v is True:
            return "true"
        if v is False:
            return "false"
        if isinstance(v, str):
            return go_str(v)
        if isinstance(v, int):
            if abs(v) >= 2**63:
                return f"Int({go_str(str(v))})"
            return str(v)
        if isinstance(v, float):
            r = repr(v)
            if r in ("inf", "-inf", "nan"):
                return f"Float({go_str(r)})"
            if "." not in r and "e" not in r:
                r += ".0"
            return r
        if isinstance(v, bytes):
            return f"Bytes({go_str(v.decode('latin-1'))})"
        raise NotPortedErr(f"constant {type(v).__name__}")

    def e_Name(self, node, ctx):
        return self.name_ref(node.id, ctx)

    def e_JoinedStr(self, node, ctx):
        parts = []
        for v in node.values:
            if isinstance(v, ast.Constant):
                parts.append(go_str(v.value))
            elif isinstance(v, ast.FormattedValue):
                conv = {-1: "0", 115: "'s'", 114: "'r'", 97: "'r'"}[v.conversion]
                spec = '""'
                if v.format_spec is not None:
                    if not all(isinstance(x, ast.Constant) for x in v.format_spec.values):
                        raise NotPortedErr("dynamic format spec")
                    spec = go_str("".join(x.value for x in v.format_spec.values))
                inner = self.expr(v.value, ctx)
                if conv == "0" and spec == '""':
                    parts.append(f"FV{{V: {inner}}}")
                else:
                    parts.append(f"FV{{V: {inner}, Conv: {conv}, Spec: {spec}}}")
        return "F(" + ", ".join(parts) + ")"

    def e_Attribute(self, node, ctx):
        if isinstance(node.value, ast.Name) and node.value.id == "self" and node.attr == "dialect":
            return "self.Dialect()"
        return f"A({self.expr(node.value, ctx)}, {go_str(node.attr)})"

    def call_args(self, node, ctx):
        args = []
        for a in node.args:
            if isinstance(a, ast.Starred):
                args.append(f"Star({self.expr(a.value, ctx)})")
            else:
                args.append(self.expr(a, ctx))
        for kw in node.keywords:
            if kw.arg is None:
                args.append(f"StarStar({self.expr(kw.value, ctx)})")
            else:
                args.append(f"Kw({go_str(kw.arg)}, {self.expr(kw.value, ctx)})")
        return args

    def e_Call(self, node, ctx):
        f = node.func
        args = self.call_args(node, ctx)
        joined = ", ".join(args)
        if isinstance(f, ast.Attribute):
            if isinstance(f.value, ast.Name) and f.value.id == "self" and f.attr in CASE_METHODS and not (ctx and ctx.raw):
                return f"self.{CASE_METHODS[f.attr]}({joined})"
            obj = self.expr(f.value, ctx)
            return f"M({obj}, {go_str(f.attr)}{', ' if args else ''}{joined})"
        if isinstance(f, ast.Name):
            local = ctx is not None and f.id in ctx.locals
            if not local and f.id not in self.module_names and f.id in DIRECT_BUILTINS:
                simple = not node.keywords and not any(isinstance(a, ast.Starred) for a in node.args)
                if simple and len(node.args) == (2 if f.id in ("isinstance", "hasattr") else 1):
                    if f.id == "hasattr":
                        return f"HasAttr({args[0]}, Str({args[1]}))"
                    return f"{DIRECT_BUILTINS[f.id]}({joined})"
        return f"Call({self.expr(f, ctx)}{', ' if args else ''}{joined})"

    def e_Subscript(self, node, ctx):
        obj = self.expr(node.value, ctx)
        s = node.slice
        if isinstance(s, ast.Slice):
            lo = self.expr(s.lower, ctx) if s.lower else "nil"
            hi = self.expr(s.upper, ctx) if s.upper else "nil"
            st = self.expr(s.step, ctx) if s.step else "nil"
            return f"Slice({obj}, {lo}, {hi}, {st})"
        return f"Item({obj}, {self.expr(s, ctx)})"

    def e_BinOp(self, node, ctx):
        l, r = self.expr(node.left, ctx), self.expr(node.right, ctx)
        op = type(node.op)
        if op in BINOPS:
            return f"{BINOPS[op]}({l}, {r})"
        if op in BITOPS:
            return f"BitOp({l}, {r}, {go_str(BITOPS[op])})"
        raise NotPortedErr(f"operator {op.__name__}")

    def e_UnaryOp(self, node, ctx):
        v = self.expr(node.operand, ctx)
        if isinstance(node.op, ast.Not):
            return f"Not({v})"
        if isinstance(node.op, ast.USub):
            if isinstance(node.operand, ast.Constant) and isinstance(node.operand.value, (int, float)):
                return "-" + v
            return f"Neg({v})"
        if isinstance(node.op, ast.UAdd):
            return v
        if isinstance(node.op, ast.Invert):
            return f"Invert({v})"
        raise NotPortedErr("unary operator")

    def e_BoolOp(self, node, ctx):
        fn = "AndV" if isinstance(node.op, ast.And) else "OrV"
        vals = [self.expr(v, ctx) for v in node.values]
        out = vals[-1]
        for v in reversed(vals[:-1]):
            out = f"{fn}({v}, func() Value {{ return {out} }})"
        return out

    def e_Compare(self, node, ctx):
        parts = []
        left = node.left
        for op, right in zip(node.ops, node.comparators):
            parts.append(f"{CMPOPS[type(op)]}({self.expr(left, ctx)}, {self.expr(right, ctx)})")
            left = right
        out = parts[-1]
        for p in reversed(parts[:-1]):
            out = f"AndV({p}, func() Value {{ return {out} }})"
        return out

    def e_IfExp(self, node, ctx):
        return (f"IfV({self.expr(node.test, ctx)}, func() Value {{ return {self.expr(node.body, ctx)} }}, "
                f"func() Value {{ return {self.expr(node.orelse, ctx)} }})")

    def seq_items(self, elts, ctx):
        out = []
        for e in elts:
            if isinstance(e, ast.Starred):
                out.append(f"Star({self.expr(e.value, ctx)})")
            else:
                out.append(self.expr(e, ctx))
        return ", ".join(out)

    def e_List(self, node, ctx):
        return f"NewList({self.seq_items(node.elts, ctx)})"

    def e_Tuple(self, node, ctx):
        return f"NewTuple({self.seq_items(node.elts, ctx)})"

    def e_Set(self, node, ctx):
        return f"NewSet({self.seq_items(node.elts, ctx)})"

    def e_Dict(self, node, ctx):
        items = []
        for k, v in zip(node.keys, node.values):
            if k is None:
                items.append(f"StarStar({self.expr(v, ctx)})")
            else:
                items.append(f"{self.expr(k, ctx)}, {self.expr(v, ctx)}")
        return f"NewDict({', '.join(items)})"

    def comprehension(self, node, ctx, kind):
        # Comprehension variables live in their own scope: declare them inside the closure.
        inner = []
        names = []
        for gen in node.generators:
            for n in ast.walk(gen.target):
                if isinstance(n, ast.Name):
                    names.append(n.id)
        saved = dict(ctx.locals)
        decl = []
        for n in names:
            go = ctx.declare(n) if n not in saved else saved[n]
            if n in saved:
                go = n + "_c"
                ctx.locals[n] = go
            decl.append(go)
        if kind == "dict":
            body = f"out.Set({self.expr(node.key, ctx)}, {self.expr(node.value, ctx)})"
            init, ret = "out := NewDict()", "return out"
        elif kind == "set":
            body = f"out.Add({self.expr(node.elt, ctx)})"
            init, ret = "out := NewSet()", "return out"
        else:
            body = f"out = append(out, {self.expr(node.elt, ctx)})"
            init, ret = "var out []Value", "return &List{V: out}"
        code = body
        for gen in reversed(node.generators):
            conds = [f"Truth({self.expr(c, ctx)})" for c in gen.ifs]
            if conds:
                code = f"if {' && '.join(conds)} {{ {code} }}"
            target = self.assign_target(gen.target, "item_", ctx)
            code = f"for _, item_ := range Iter({self.expr(gen.iter, ctx)}) {{ {target}; {code} }}"
        ctx.locals = saved
        decls = f"var {', '.join(decl)} Value; _ = []Value{{{', '.join(decl)}}}; " if decl else ""
        return f"func() Value {{ {decls}{init}; {code}; {ret} }}()"

    def e_ListComp(self, node, ctx):
        return self.comprehension(node, ctx, "list")

    def e_GeneratorExp(self, node, ctx):
        return self.comprehension(node, ctx, "list")

    def e_SetComp(self, node, ctx):
        return self.comprehension(node, ctx, "set")

    def e_DictComp(self, node, ctx):
        return self.comprehension(node, ctx, "dict")

    def e_Lambda(self, node, ctx):
        a = node.args
        if a.vararg or a.kwarg or a.kwonlyargs:
            raise NotPortedErr("lambda with *args/**kwargs")
        saved = dict(ctx.locals)
        binds = []
        defaults = [None] * (len(a.args) - len(a.defaults)) + list(a.defaults)
        for i, (p, d) in enumerate(zip(a.args, defaults)):
            go = p.arg + "_l" if p.arg in saved else ctx.declare(p.arg)
            ctx.locals[p.arg] = go
            dflt = self.expr(d, ctx) if d is not None else "nil"
            binds.append(f"var {go} Value = {dflt}; if len(args) > {i} {{ {go} = args[{i}] }}; _ = {go}")
        body = self.expr(node.body, ctx)
        ctx.locals = saved
        return f"Lambda(func(args ...Value) Value {{ {'; '.join(binds)}{'; ' if binds else ''}return {body} }})"

    def e_NamedExpr(self, node, ctx):
        name = ctx.declare(node.target.id)
        return f"func() Value {{ {name} = {self.expr(node.value, ctx)}; return {name} }}()"

    def e_Starred(self, node, ctx):
        return f"Star({self.expr(node.value, ctx)})"

    # ------------------------------------------------------------------ statements
    def assign_target(self, target, value, ctx):
        if isinstance(target, ast.Name):
            return f"{ctx.declare(target.id)} = {value}"
        if isinstance(target, (ast.Tuple, ast.List)):
            stars = [i for i, e in enumerate(target.elts) if isinstance(e, ast.Starred)]
            if len(stars) > 1:
                raise NotPortedErr("starred assignment")
            names = []
            nested = []
            for e in target.elts:
                if isinstance(e, ast.Starred):
                    e = e.value
                if isinstance(e, ast.Name):
                    names.append("&" + ctx.declare(e.id))
                elif isinstance(e, (ast.Tuple, ast.List)):
                    tmp = ctx.declare(ctx.fresh("nested"))
                    names.append("&" + tmp)
                    nested.append(self.assign_target(e, tmp, ctx))
                else:
                    raise NotPortedErr("nested unpacking")
            if stars:
                code = f"UnpackStar({value}, {stars[0]}, {', '.join(names)})"
            else:
                code = f"Unpack({value}, {', '.join(names)})"
            return "; ".join([code] + nested)
        if isinstance(target, ast.Attribute):
            return f"SetA({self.expr(target.value, ctx)}, {go_str(target.attr)}, {value})"
        if isinstance(target, ast.Subscript):
            if isinstance(target.slice, ast.Slice):
                raise NotPortedErr("slice assignment")
            return f"SetItem({self.expr(target.value, ctx)}, {self.expr(target.slice, ctx)}, {value})"
        raise NotPortedErr("assignment target")

    def wrap(self, code, node, ctx, raw):
        """Wraps a simple statement so that failures are reported per statement."""
        self.stmts += 1
        if raw or ctx.kind != "method":
            return [code]
        return [f"self.Do({node.lineno}, func() {{ {code} }})"]

    def is_literal(self, node):
        if isinstance(node, ast.Constant):
            return True
        if isinstance(node, (ast.Dict,)):
            return all(k is not None and self.is_literal(k) for k in node.keys) and all(self.is_literal(v) for v in node.values)
        if isinstance(node, (ast.List, ast.Tuple)):
            return all(self.is_literal(e) for e in node.elts)
        if isinstance(node, ast.UnaryOp) and isinstance(node.op, ast.USub):
            return self.is_literal(node.operand)
        if isinstance(node, ast.Name) and node.id in ("UnsupportedError", "True", "False", "None"):
            return True
        return False

    def stmt(self, node, ctx, raw=False):
        prev = ctx.raw
        ctx.raw = raw or ctx.kind != "method"
        try:
            m = getattr(self, "s_" + type(node).__name__, None)
            if m is None:
                raise NotPortedErr(type(node).__name__)
            return m(node, ctx, raw)
        except NotPortedErr as e:
            return self.wrap(self.np(node, str(e)), node, ctx, raw)
        finally:
            ctx.raw = prev

    def body(self, nodes, ctx, raw=False):
        out = []
        for n in nodes:
            out.extend(self.stmt(n, ctx, raw))
        return out

    def s_Expr(self, node, ctx, raw):
        v = node.value
        if isinstance(v, ast.Constant) and isinstance(v.value, str):
            return []  # docstring
        if (
            not raw
            and ctx.kind == "method"
            and isinstance(v, ast.Call)
            and isinstance(v.func, ast.Attribute)
            and isinstance(v.func.value, ast.Name)
            and v.func.value.id == "self"
            and v.func.attr in CASE_METHODS
            and all(self.is_literal(a) for a in v.args)
            and all(kw.arg and self.is_literal(kw.value) for kw in v.keywords)
        ):
            self.stmts += 1
            return [f"self.L({node.lineno}).{self.expr(v, ctx)[len('self.'):]}"]
        code = self.expr(v, ctx)
        if code.startswith(("Call(", "M(")):
            code = f"Stmt({code})"
        elif not isinstance(v, ast.Call) and not code.startswith("NotPorted"):
            code = "_ = " + code
        return self.wrap(code, node, ctx, raw)

    def s_Assign(self, node, ctx, raw):
        value = self.expr(node.value, ctx)
        if len(node.targets) == 1:
            code = self.assign_target(node.targets[0], value, ctx)
        else:
            tmp = ctx.declare(ctx.fresh("multi"))
            parts = [f"{tmp} = {value}"] + [self.assign_target(t, tmp, ctx) for t in node.targets]
            code = "; ".join(parts)
        names = []
        for t in node.targets:
            for n in ast.walk(t):
                if isinstance(n, ast.Name) and isinstance(n.ctx, ast.Store) and n.id in ctx.locals:
                    names.append("&" + ctx.locals[n.id])
        if not raw and ctx.kind == "method" and names:
            self.stmts += 1
            return [f"self.DoAssign({node.lineno}, func() {{ {code} }}, {', '.join(dict.fromkeys(names))})"]
        return self.wrap(code, node, ctx, raw)

    def s_AnnAssign(self, node, ctx, raw):
        if node.value is None:
            return []
        return self.wrap(self.assign_target(node.target, self.expr(node.value, ctx), ctx), node, ctx, raw)

    def s_AugAssign(self, node, ctx, raw):
        op = type(node.op)
        cur = self.expr(node.target, ctx)
        rhs = self.expr(node.value, ctx)
        if op in BINOPS:
            val = f"{BINOPS[op]}({cur}, {rhs})"
        elif op in BITOPS:
            val = f"BitOp({cur}, {rhs}, {go_str(BITOPS[op])})"
        else:
            raise NotPortedErr("augmented operator")
        return self.wrap(self.assign_target(node.target, val, ctx), node, ctx, raw)

    def s_For(self, node, ctx, raw):
        if node.orelse:
            raise NotPortedErr("for-else")
        it = self.expr(node.iter, ctx)
        target = self.assign_target(node.target, "item_", ctx)
        body = self.body(node.body, ctx, raw)
        return [f"for _, item_ := range Iter({it}) {{", f"{target}"] + body + ["}"]

    def s_While(self, node, ctx, raw):
        if node.orelse:
            raise NotPortedErr("while-else")
        return [f"for Truth({self.expr(node.test, ctx)}) {{"] + self.body(node.body, ctx, raw) + ["}"]

    def s_If(self, node, ctx, raw):
        out = [f"if Truth({self.expr(node.test, ctx)}) {{"] + self.body(node.body, ctx, raw)
        orelse = node.orelse
        while len(orelse) == 1 and isinstance(orelse[0], ast.If):
            n = orelse[0]
            out += [f"}} else if Truth({self.expr(n.test, ctx)}) {{"] + self.body(n.body, ctx, raw)
            orelse = n.orelse
        if orelse:
            out += ["} else {"] + self.body(orelse, ctx, raw)
        return out + ["}"]

    def s_Pass(self, node, ctx, raw):
        return []

    def s_Break(self, node, ctx, raw):
        return ["break"]

    def s_Continue(self, node, ctx, raw):
        return ["continue"]

    def s_Return(self, node, ctx, raw):
        if ctx.kind == "module":
            raise NotPortedErr("return at module level")
        val = self.expr(node.value, ctx) if node.value is not None else "nil"
        return [f"return {val}"]

    def s_Raise(self, node, ctx, raw):
        if node.exc is None:
            raise NotPortedErr("bare raise")
        return self.wrap(f"Raise_({self.expr(node.exc, ctx)})", node, ctx, True)

    def s_Assert(self, node, ctx, raw):
        msg = f", {self.expr(node.msg, ctx)}" if node.msg is not None else ""
        return self.wrap(f"Assert({self.expr(node.test, ctx)}{msg})", node, ctx, raw)

    def s_Delete(self, node, ctx, raw):
        parts = []
        for t in node.targets:
            if isinstance(t, ast.Subscript) and not isinstance(t.slice, ast.Slice):
                parts.append(f"DelItem({self.expr(t.value, ctx)}, {self.expr(t.slice, ctx)})")
            elif isinstance(t, ast.Name):
                parts.append(f"{ctx.declare(t.id)} = nil")
            else:
                raise NotPortedErr("del target")
        return self.wrap("; ".join(parts), node, ctx, raw)

    def s_Import(self, node, ctx, raw):
        for alias in node.names:
            self.imports[alias.asname or alias.name.split(".")[0]] = alias.name
        return []

    def s_ImportFrom(self, node, ctx, raw):
        for alias in node.names:
            self.imports[alias.asname or alias.name] = f"{node.module}.{alias.name}"
        return []

    def s_Global(self, node, ctx, raw):
        return []

    s_Nonlocal = s_Global

    def s_FunctionDef(self, node, ctx, raw):
        name = ctx.declare(node.name)
        fn = self.function(node, ctx)
        return self.wrap(f"{name} = {fn}", node, ctx, raw)

    def s_With(self, node, ctx, raw):
        return self.with_items(node.items, node.body, node, ctx, raw)

    def with_items(self, items, body_nodes, node, ctx, raw):
        item = items[0]
        rest = items[1:]
        cm = item.context_expr
        as_ = ""
        if item.optional_vars is not None:
            if not isinstance(item.optional_vars, ast.Name):
                raise NotPortedErr("with target")
            as_ = ctx.declare(item.optional_vars.id) + " = "

        def inner(raw_inner):
            if rest:
                return self.with_items(rest, body_nodes, node, ctx, raw_inner)
            return self.body(body_nodes, ctx, raw_inner)

        if isinstance(cm, ast.Call) and isinstance(cm.func, ast.Attribute) and isinstance(cm.func.value, ast.Name) and cm.func.value.id == "self":
            meth = cm.func.attr
            args = self.call_args(cm, ctx)
            if meth == "subTest":
                body = inner(raw)
                tail = (", " + ", ".join(args)) if args else ""
                self.stmts += 1
                return ["self.SubTest(func() {"] + body + [f"}}{tail})"]
            if meth in ("assertRaises", "assertRaisesRegex", "assertLogs", "assertNoLogs"):
                if meth.startswith("assertRaises") and not args:
                    raise NotPortedErr("assertRaises without exception")
                body = inner(True)
                go = meth[:1].upper() + meth[1:]
                bind = [f"{as_}cm_"] if as_ else ["_ = cm_"]
                tail = (", " + ", ".join(args)) if args else ""
                call = f"self.{go}(func(cm_ Value) {{\n" + "\n".join(bind + body) + f"\n}}{tail})"
                return self.wrap(call, node, ctx, raw)
        patch = self.logger_patch(cm)
        if patch is not None:
            body = inner(raw)
            bind = [f"{as_}m_"] if as_ else ["_ = m_"]
            call = f"self.PatchLogMethod(func(m_ Value) {{\n" + "\n".join(bind + body) + f"\n}}, {go_str(patch)})"
            return self.wrap(call, node, ctx, raw) if raw else [call]
        body = inner(raw)
        bind = [f"{as_}cm_"] if as_ else ["_ = cm_"]
        call = f"With({self.expr(cm, ctx)}, func(cm_ Value) {{\n" + "\n".join(bind + body) + "\n})"
        return self.wrap(call, node, ctx, raw) if raw else [call]

    def logger_patch(self, cm):
        """Returns the logger level patched by mock.patch(...logger.<level>) / patch.object(logger, level)."""
        if not isinstance(cm, ast.Call):
            return None
        f = ast.unparse(cm.func)
        if f in ("mock.patch", "patch", "unittest.mock.patch") and cm.args and isinstance(cm.args[0], ast.Constant):
            target = cm.args[0].value
            if isinstance(target, str) and ".logger." in target:
                return target.rsplit(".", 1)[1]
        if f in ("mock.patch.object", "patch.object") and len(cm.args) >= 2 and isinstance(cm.args[1], ast.Constant):
            if isinstance(cm.args[0], ast.Name) and "logger" in cm.args[0].id:
                return cm.args[1].value
        return None

    def s_Try(self, node, ctx, raw):
        if node.orelse:
            raise NotPortedErr("try-else")
        body = self.body(node.body, ctx, True)
        err = ctx.fresh("err")
        out = [f"{err} := Try(func() {{"] + body + ["})"]
        if node.handlers:
            out.append(f"if {err} != nil {{")
            first = True
            for h in node.handlers:
                cond = "true" if h.type is None else f"Matches({err}, {self.expr(h.type, ctx)})"
                out.append(("if " if first else "} else if ") + cond + " {")
                first = False
                if h.name:
                    out.append(f"{ctx.declare(h.name)} = {err}")
                out += self.body(h.body, ctx, raw)
            out += ["} else {", f"panic({err})", "}", "}"]
        else:
            out.append(f"_ = {err}")
        if node.finalbody:
            out += self.body(node.finalbody, ctx, raw)
            if not node.handlers:
                out += [f"if {err} != nil {{ panic({err}) }}"]
        return self.wrap("\n".join(out), node, ctx, raw)

    # ------------------------------------------------------------------ functions
    def params(self, args, ctx):
        if args.kwonlyargs or args.posonlyargs:
            pass
        binds = []
        plist = args.posonlyargs + args.args
        defaults = [None] * (len(plist) - len(args.defaults)) + list(args.defaults)
        start = 1 if plist and plist[0].arg in ("self", "cls") and ctx.kind in ("method", "helper") else 0
        if start and plist[0].arg == "cls":
            # classmethods run on the test case object, which holds the class attributes
            ctx.locals["cls"] = "self"
        for i, (p, d) in enumerate(zip(plist[start:], defaults[start:])):
            go = ctx.declare(p.arg)
            dflt = self.expr(d, ctx) if d is not None else "nil"
            binds.append(f"{go} = a.Get({i}, {go_str(p.arg)}, {dflt})")
        for p, d in zip(args.kwonlyargs, args.kw_defaults):
            go = ctx.declare(p.arg)
            dflt = self.expr(d, ctx) if d is not None else "nil"
            binds.append(f"{go} = a.Get(-1, {go_str(p.arg)}, {dflt})")
        if args.vararg:
            go = ctx.declare(args.vararg.arg)
            binds.append(f"{go} = Tuple(a.Pos[min(len(a.Pos), {len(plist) - start}):])")
        if args.kwarg:
            go = ctx.declare(args.kwarg.arg)
            named = [p.arg for p in plist[start:]] + [p.arg for p in args.kwonlyargs]
            binds.append(f"{go} = KwDict(a{''.join(', ' + go_str(n) for n in named)})")
        return binds

    def function(self, node, outer):
        """A nested def as a Go closure."""
        ctx = FuncCtx(self, outer.kind)
        ctx.locals = dict(outer.locals)
        saved = set(outer.locals)
        for n in assigned_names(node.body):
            if n not in saved:
                ctx.declare(n)
        binds = self.params(node.args, ctx)
        body = self.body(node.body, ctx, True)
        new = [v for k, v in ctx.locals.items() if k not in saved]
        decl = f"var {', '.join(new)} Value; _ = []Value{{{', '.join(new)}}}" if new else ""
        lines = ["Func(" + go_str(node.name) + ", func(a Args) Value {"]
        if decl:
            lines.append(decl)
        lines += binds + body + self.tail(body) + ["})"]
        return "\n".join(lines)

    def method(self, node, cls_name):
        # Test methods report failures per statement; helper methods (validate, check_file, ...)
        # let exceptions propagate to the calling statement, like Python.
        is_test = node.name.startswith("test") or node.name in ("setUp", "setUpClass")
        ctx = FuncCtx(self, "method" if is_test else "helper")
        for n in assigned_names(node.body):
            ctx.declare(n)
        binds = self.params(node.args, ctx)
        out = [f"func {self.method_func(cls_name, node.name)}(self *Case, a Args) Value {{"]
        reason = PYTHON_ONLY.get((self.relpath, f"{cls_name}.{node.name}"))
        if reason:
            return out + [f"M(self, \"skipTest\", {go_str(reason)})", "return nil", "}"]
        skip = self.decorators(node, ctx)
        body = self.body(node.body, ctx)
        # declared after converting the body, which may introduce temporaries
        names = [n for n in ctx.locals.values() if n != "self"]
        if names:
            out.append(f"var {', '.join(names)} Value")
            out.append(f"_ = []Value{{{', '.join(names)}}}")
        out += binds + skip
        out += body + self.tail(body) + ["}"]
        return out

    def decorators(self, node, ctx):
        out = []
        for d in node.decorator_list:
            src = ast.unparse(d)
            if src in ("classmethod", "staticmethod"):
                continue
            if src in ("unittest.skip", "skip") or src.startswith(("unittest.skip(", "skip(")):
                reason = d.args[0] if isinstance(d, ast.Call) and d.args else ast.Constant("skipped")
                out.append(f"M(self, \"skipTest\", {self.expr(reason, ctx)})")
            elif src.startswith(("unittest.skipIf(", "skipIf(", "unittest.skipUnless(", "skipUnless(")):
                cond = self.expr(d.args[0], ctx)
                if "Unless" in src:
                    cond = f"Not({cond})"
                reason = self.expr(d.args[1], ctx) if len(d.args) > 1 else '"skipped"'
                out.append(f"if Truth({cond}) {{ M(self, \"skipTest\", {reason}) }}")
            elif (src.startswith(("mock.patch(", "patch(", "unittest.mock.patch(")) and isinstance(d, ast.Call)
                  and d.args and isinstance(d.args[0], ast.Constant) and str(d.args[0].value).endswith(".logger")):
                params = [p.arg for p in node.args.args if p.arg != "self"]
                if not params:
                    out.append(f"M(self, \"skipTest\", {go_str('decorator not ported: ' + src)})")
                    continue
                go = ctx.declare(params[-1])
                out.append(f"{go}_restore := func() {{}}")
                out.append(f"{go}, {go}_restore = self.PatchLogger()")
                out.append(f"defer {go}_restore()")
            else:
                self.not_ported += 1
                out.append(f"M(self, \"skipTest\", {go_str('decorator not ported: ' + src)})")
        return out

    @staticmethod
    def tail(body):
        return [] if body and body[-1].startswith("return ") else ["return nil"]

    def method_func(self, cls_name, meth):
        return f"{self.prefix}_{cls_name}_{meth}"

    def module_function(self, node):
        ctx = FuncCtx(self, "function")
        for n in assigned_names(node.body):
            ctx.declare(n)
        binds = self.params(node.args, ctx)
        body = self.body(node.body, ctx, True)
        names = list(ctx.locals.values())
        gov = self.module_names[node.name]
        out = [f"var {gov} Value = Func({go_str(node.name)}, func(a Args) Value {{"]
        if names:
            out += [f"var {', '.join(names)} Value", f"_ = []Value{{{', '.join(names)}}}"]
        out += binds + body + self.tail(body) + ["})"]
        return out

    # ------------------------------------------------------------------ module
    def convert(self):
        body = self.tree.body
        mod_ctx = FuncCtx(self, "module")
        decls = []
        for node in body:
            if isinstance(node, (ast.Import, ast.ImportFrom)):
                self.stmt(node, mod_ctx)
            elif isinstance(node, ast.FunctionDef):
                self.module_names[node.name] = f"{self.prefix}_{node.name}"
            elif isinstance(node, ast.ClassDef):
                self.classes[node.name] = node
            elif isinstance(node, (ast.Assign, ast.AnnAssign)):
                targets = node.targets if isinstance(node, ast.Assign) else [node.target]
                for t in targets:
                    if isinstance(t, ast.Name):
                        self.module_names[t.id] = f"{self.prefix}_{t.id}"
        # module-level values
        for node in body:
            if isinstance(node, ast.FunctionDef):
                decls += self.module_function(node)
            elif isinstance(node, (ast.Assign, ast.AnnAssign)) and getattr(node, "value", None) is not None:
                targets = node.targets if isinstance(node, ast.Assign) else [node.target]
                try:
                    value = self.expr(node.value, mod_ctx)
                except NotPortedErr as e:
                    value = self.np(node, str(e))
                for t in targets:
                    if isinstance(t, ast.Name):
                        decls.append(f"var {self.module_names[t.id]} Value = Lazy(func() Value {{ return {value} }})")
        out = []
        tests = []
        for name, cls in self.classes.items():
            if name == "Validator":
                continue
            out += self.convert_class(cls)
            if any(isinstance(n, ast.FunctionDef) and n.name.startswith("test") for n in cls.body) or self.base_of(cls):
                tests.append(name)
        header = [
            f"// Code generated by tools/port/convert_tests.py from {self.relpath}; DO NOT EDIT.",
            "",
            f"package {self.pkg}",
            "",
            "import (",
            '\t"testing"',
            "",
            '\t. "github.com/cottrellashley/sqlglot-go/internal/pyrt"',
            ")",
            "",
            "var _ = testing.Short",
            "",
        ]
        runners = []
        for name in tests:
            go_name = name if name.startswith("Test") else "Test" + name
            runners += [f"func {go_name}(t *testing.T) {{ RunClass(t, {self.class_var(name)}) }}", ""]
        return header + decls + [""] + runners + out

    def class_var(self, name):
        return f"{self.prefix}_{name}"

    def base_of(self, cls):
        for b in cls.bases:
            if isinstance(b, ast.Name) and b.id in self.classes and b.id != "Validator":
                return b.id
        return None

    def convert_class(self, cls):
        class_names = []
        attrs = []
        methods = []
        funcs = []
        tests = []
        for node in cls.body:
            if isinstance(node, ast.FunctionDef):
                methods.append(f"{go_str(node.name)}: {self.method_func(cls.name, node.name)},")
                funcs += self.method(node, cls.name) + [""]
                if node.name.startswith("test"):
                    tests.append(go_str(node.name))
            elif isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
                try:
                    value = self.expr(node.value, None)
                except NotPortedErr as e:
                    value = self.np(node, str(e))
                attrs.append(f"{go_str(node.targets[0].id)}: {value},")
                # later statements of the class body can refer to the attribute by name
                if node.targets[0].id not in self.module_names:
                    self.module_names[node.targets[0].id] = value
                    class_names.append(node.targets[0].id)
            elif isinstance(node, ast.Expr) and isinstance(node.value, ast.Constant):
                pass
            else:
                self.not_ported += 1
        for name in class_names:
            del self.module_names[name]
        base = self.base_of(cls)
        out = [f"var {self.class_var(cls.name)} = &Class{{",
               f"Name: {go_str(cls.name)},",
               f"File: {go_str(self.relpath)},"]
        if base:
            out.append(f"Base: {self.class_var(base)},")
        out += ["Attrs: map[string]Value{"] + attrs + ["},", "Methods: map[string]Method{"] + methods + ["},"]
        out += ["Tests: []string{" + ", ".join(tests) + "},", "}", ""]
        return out + funcs


def main():
    path, relpath, pkg = sys.argv[1], sys.argv[2], sys.argv[3]
    conv = Converter(open(path).read(), relpath, pkg)
    lines = conv.convert()
    print("\n".join(lines))
    print(f"// statements: {conv.stmts}, not ported: {conv.not_ported}", file=sys.stderr)


if __name__ == "__main__":
    main()
