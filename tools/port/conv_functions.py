"""Translates a parser FUNCTIONS-style dict literal (name -> builder) into Go map entries.

Usage: python3 -I conv_functions.py <python file> <first line> <last line>

The common shapes are handled mechanically:
    exp.X.from_arg_list                     -> sqlglot.FromArgList(exp.X)
    lambda args[, dialect]: exp.X(k=..., ...) -> func(args, d) *exp.Expr { return exp.New(exp.X, "k", ...) }
    _helper / _helper(arg, ...)              -> helper / helper(arg, ...)
Anything else is emitted as a `// TODO` comment for manual porting.
"""

import ast
import json
import sys


def camel(name):
    parts = name.lstrip("_").split("_")
    return parts[0] + "".join(p[:1].upper() + p[1:] for p in parts[1:])


def dtype(name):
    return "exp.DType" + "".join(p[:1].upper() + p[1:].lower() for p in name.split("_"))


class Unsupported(Exception):
    pass


HELPERS = {
    "build_formatted_time": "sqlglot.BuildFormattedTime",
    "binary_from_function": "sqlglot.BinaryFromFunction",
    "build_like": "sqlglot.BuildLikeFor",
    "build_timetostr_or_tochar": "sqlglot.BuildTimetostrOrTochar",
    "build_replace_with_optional_replacement": "sqlglot.BuildReplaceWithOptionalReplacement",
    "build_default_decimal_type": "sqlglot.BuildDefaultDecimalType",
}


def expr(n):
    if isinstance(n, ast.Constant):
        if n.value is None:
            return "nil"
        if n.value is True:
            return "true"
        if n.value is False:
            return "false"
        if isinstance(n.value, str):
            return json.dumps(n.value)
        return repr(n.value)
    if isinstance(n, ast.Name):
        if n.id == "args":
            return "args"
        return camel(n.id)
    if isinstance(n, ast.Call):
        f = n.func
        src = ast.unparse(f)
        if src == "seq_get":
            return f"seqGet({expr(n.args[0])}, {expr(n.args[1])})"
        if src == "exp.Literal.number":
            return f"exp.LiteralNumber({expr(n.args[0])})"
        if src == "exp.Literal.string":
            return f"exp.LiteralString({expr(n.args[0])})"
        if src in ("exp.null", "exp.Null"):
            return "exp.NewNull()"
        if src == "exp.true":
            return "exp.NewTrue()"
        if src == "exp.false":
            return "exp.NewFalse()"
        if src == "exp.var":
            return f"exp.NewVar({expr(n.args[0])})"
        if src == "map_date_part":
            return f"sqlglot.MapDatePart({expr(n.args[0])}, nil)"
        if src == "dialect.to_json_path":
            return f"d.ToJSONPath({expr(n.args[0])})"
        if src.endswith(".from_arg_list") and src.startswith("exp."):
            return f"exp.FromArgList(exp.{src.split('.')[1]}, {expr(n.args[0])})"
        if src.startswith("exp.") and src.count(".") == 1:
            if n.args:
                raise Unsupported("positional constructor")
            kv = []
            for kw in n.keywords:
                kv.append(f'"{kw.arg}", {expr(kw.value)}')
            return f"exp.New(exp.{src[4:]}{', ' if kv else ''}{', '.join(kv)})"
        if src in HELPERS:
            return f"{HELPERS[src]}({', '.join(expr(a) for a in n.args)})"
        if isinstance(f, ast.Name) and f.id.startswith("_"):
            # keyword arguments are passed positionally after the positional ones
            args = [expr(a) for a in n.args] + [f"/* {kw.arg} */ {expr(kw.value)}" for kw in n.keywords]
            return f"{camel(f.id)}({', '.join(args)})"
        raise Unsupported(src)
    if isinstance(n, ast.Attribute):
        src = ast.unparse(n)
        if src.startswith("exp.DType."):
            return dtype(n.attr)
        if src.startswith("exp.") and src.count(".") == 1:
            return src
        raise Unsupported(src)
    if isinstance(n, ast.Subscript) and ast.unparse(n.value) == "args" and isinstance(n.slice, ast.Slice):
        lo = expr(n.slice.lower) if n.slice.lower else "0"
        return f"args[min({lo}, len(args)):]"
    if isinstance(n, ast.BoolOp) and isinstance(n.op, ast.Or):
        return f"orExpr({', '.join(expr(v) for v in n.values)})"
    raise Unsupported(type(n).__name__)


def builder(v):
    src = ast.unparse(v)
    if src.endswith(".from_arg_list") and src.startswith("exp."):
        return f"sqlglot.FromArgList(exp.{src.split('.')[1]})"
    if isinstance(v, ast.Lambda):
        return f"func(args []*exp.Expr, d *sqlglot.Dialect) *exp.Expr {{ return {expr(v.body)} }}"
    if isinstance(v, ast.Name):
        if v.id in HELPERS:
            return HELPERS[v.id]
        return camel(v.id)
    if isinstance(v, ast.Call):
        return expr(v)
    raise Unsupported(src)


def main():
    path, first, last = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
    lines = open(path).read().splitlines()[first - 1 : last]
    text = "\n".join(lines)
    i = text.index("{")
    j = text.rindex("}")
    d = ast.parse(text[i : j + 1], mode="eval").body
    for k, v in zip(d.keys, d.values):
        if k is None:
            # **dict.fromkeys((names...), builder)
            if isinstance(v, ast.Call) and ast.unparse(v.func) == "dict.fromkeys":
                names = ast.literal_eval(v.args[0])
                try:
                    b = builder(v.args[1])
                    for name in names:
                        print(f"\t\t{json.dumps(name)}: {b},")
                except Unsupported as e:
                    for name in names:
                        print(f"\t\t// TODO {name!r}: {' '.join(ast.unparse(v.args[1]).split())[:300]}  [{e}]")
            else:
                print(f"\t\t// TODO **{ast.unparse(v)[:200]}")
            continue
        key = ast.unparse(k)
        try:
            print(f"\t\t{json.dumps(ast.literal_eval(k))}: {builder(v)},")
        except (Unsupported, ValueError) as e:
            print(f"\t\t// TODO {key}: {' '.join(ast.unparse(v).split())[:300]}  [{e}]")


if __name__ == "__main__":
    main()
