"""Generates Go configuration code from sqlglot Parser/Generator/Tokenizer/Dialect classes.

Usage:
  python3 -I tools/port/gen_config.py <sqlglot-checkout> struct <module:Class> <GoStruct>
      Emit a Go struct definition with one field per class attribute.
  python3 -I tools/port/gen_config.py <sqlglot-checkout> data <module:Class> [<module:BaseClass>]
      Emit Go statements assigning the data attributes of Class (only those that differ from
      BaseClass when given). Callable tables are listed as TODO comments with their Python
      source, so that only the logic has to be ported by hand.

This is a porting aid used to port dialects from upstream sqlglot cheaply.
"""
import importlib
import inspect
import sys
import enum

SQLGLOT = sys.argv[1]
sys.path.insert(0, SQLGLOT)
import logging  # noqa: E402

logging.disable(logging.CRITICAL)

from sqlglot import exp  # noqa: E402
from sqlglot.tokens import TokenType  # noqa: E402

STR_SETS = {"JOIN_HINTS", "OPERATION_MODIFIERS", "QUOTED_TYPES_TO_PRESERVE", "RESERVED_CONSTRAINT_KINDS",
            "ADD_CONSTRAINT_KEYWORDS", "RESERVED_KEYWORDS", "PARAMETERIZABLE_TEXT_TYPES_STR",
            "SUPPORTED_SETTINGS", "PSEUDOCOLUMNS", "VALID_INTERVAL_UNITS", "INSERT_ALTERNATIVES"}


def camel(name: str) -> str:
    return "".join(p[:1].upper() + p[1:].lower() for p in name.split("_") if p)


def gs(s: str) -> str:
    out = '"'
    for ch in s:
        if ch == "\\":
            out += "\\\\"
        elif ch == '"':
            out += '\\"'
        elif ch == "\n":
            out += "\\n"
        elif ch == "\t":
            out += "\\t"
        elif ch == "\r":
            out += "\\r"
        elif ord(ch) < 32 or ord(ch) == 127:
            out += "\\x%02x" % ord(ch)
        else:
            out += ch
    return out + '"'


def kind_name(cls) -> str:
    n = cls.__name__
    return n[1:] + "Base" if n.startswith("_") else n


def tt(t) -> str:
    return "tokens." + camel(t.name)


def dt(d) -> str:
    return "exp.DType" + camel(d.name)


def classify(name, v):
    if isinstance(v, bool):
        return "bool"
    if isinstance(v, int):
        return "int"
    if isinstance(v, str):
        return "str"
    if v is None:
        return "none"
    if isinstance(v, enum.Enum):
        return "enum"
    if isinstance(v, (set, frozenset)):
        if name in STR_SETS or (v and all(isinstance(x, str) for x in v)):
            return "strset"
        if all(isinstance(x, TokenType) for x in v):
            return "tokenset"
        if all(isinstance(x, type) for x in v):
            return "kindset"
        if all(isinstance(x, exp.DType) for x in v):
            return "dtypeset"
        return "set?"
    if isinstance(v, (tuple, list)):
        if all(isinstance(x, type) for x in v):
            return "kindset"
        if all(isinstance(x, TokenType) for x in v):
            return "tokenset"
        if all(isinstance(x, str) for x in v):
            return "strlist"
        return "list?"
    if isinstance(v, dict):
        keys = list(v.keys())
        vals = list(v.values())
        if not v:
            return "dict-empty"
        if all(isinstance(x, TokenType) for x in keys):
            if all(isinstance(x, TokenType) for x in vals):
                return "tt->tt"
            if all(isinstance(x, type) for x in vals):
                return "tt->kind"
            if all(callable(x) or x is None for x in vals):
                return "tt->fn"
            if all(isinstance(x, str) for x in vals):
                return "tt->str"
        if all(isinstance(x, str) for x in keys):
            if all(isinstance(x, type) for x in vals):
                return "str->kind"
            if all(isinstance(x, tuple) for x in vals):
                return "options"
            if all(isinstance(x, str) for x in vals):
                return "str->str"
            if all(callable(x) for x in vals):
                return "str->fn"
            if all(isinstance(x, TokenType) for x in vals):
                return "str->tt"
        if all(isinstance(x, type) for x in keys):
            if all(isinstance(x, enum.Enum) for x in vals):
                return "kind->enum"
            if all(callable(x) for x in vals):
                return "kind->fn"
            if all(isinstance(x, bool) or x is None for x in vals):
                return "kind->bool"
            if all(isinstance(x, str) for x in vals):
                return "kind->str"
            if all(isinstance(x, tuple) for x in vals):
                return "kind->strs"
        if all(isinstance(x, exp.DType) for x in keys):
            if all(isinstance(x, str) for x in vals):
                return "dtype->str"
            if all(callable(x) for x in vals):
                return "dtype->fn"
            if all(isinstance(x, exp.DType) for x in vals):
                return "dtype->dtype"
            if all(isinstance(x, (set, frozenset)) for x in vals):
                return "dtype->dtypeset"
        if all(isinstance(x, tuple) for x in keys):
            return "tuple->str"
        if all(isinstance(x, str) for x in keys) and all(isinstance(x, dict) for x in vals):
            return "trie"
        return "dict?"
    if callable(v):
        return "fn"
    return "?" + type(v).__name__


def opt_value(x):
    if isinstance(x, str):
        return "{" + gs(x) + "}"
    return "{" + ", ".join(gs(s) for s in x) + "}"


def go_value(name, v, k):
    if k == "bool":
        return "true" if v else "false"
    if k == "int":
        return str(v)
    if k == "str":
        return gs(v)
    if k == "none":
        return '""'
    if k == "enum":
        return f"{type(v).__name__}{camel(v.name)}"
    if k == "strset":
        return "strSet(" + ", ".join(gs(x) for x in sorted(v)) + ")"
    if k == "strlist":
        return "[]string{" + ", ".join(gs(x) for x in v) + "}"
    if k == "tokenset":
        return "tokens.NewSet(" + ", ".join(tt(x) for x in sorted(v, key=lambda t: t.value)) + ")"
    if k == "kindset":
        return "exp.NewKindSet(" + ", ".join("exp." + kind_name(x) for x in sorted(v, key=lambda c: c.__name__)) + ")"
    if k == "dtypeset":
        return "exp.NewTypeSet(" + ", ".join(dt(x) for x in sorted(v, key=lambda d: d.name)) + ")"
    if k == "tt->tt":
        return "map[tokens.TokenType]tokens.TokenType{" + ", ".join(f"{tt(a)}: {tt(b)}" for a, b in v.items()) + "}"
    if k == "tt->kind":
        return "map[tokens.TokenType]exp.Kind{" + ", ".join(f"{tt(a)}: exp.{kind_name(b)}" for a, b in v.items()) + "}"
    if k == "tt->str":
        return "map[tokens.TokenType]string{" + ", ".join(f"{tt(a)}: {gs(b)}" for a, b in v.items()) + "}"
    if k == "str->kind":
        return "map[string]exp.Kind{" + ", ".join(f"{gs(a)}: exp.{kind_name(b)}" for a, b in v.items()) + "}"
    if k == "str->str":
        return "map[string]string{" + ", ".join(f"{gs(a)}: {gs(b)}" for a, b in v.items()) + "}"
    if k == "str->tt":
        return "map[string]tokens.TokenType{" + ", ".join(f"{gs(a)}: {tt(b)}" for a, b in v.items()) + "}"
    if k == "options":
        return "OptionsType{" + ", ".join(f"{gs(a)}: {{" + ", ".join(opt_value(x) for x in b) + "}" for a, b in v.items()) + "}"
    if k == "kind->enum":
        return "map[exp.Kind]exp.PropertiesLocation{" + ", ".join(f"exp.{kind_name(a)}: exp.{camel(b.name)}" for a, b in v.items()) + "}"
    if k == "kind->bool":
        return "map[exp.Kind]*bool{" + ", ".join(f"exp.{kind_name(a)}: {'nil' if b is None else ('Bool(true)' if b else 'Bool(false)')}" for a, b in v.items()) + "}"
    if k == "kind->str":
        return "map[exp.Kind]string{" + ", ".join(f"exp.{kind_name(a)}: {gs(b)}" for a, b in v.items()) + "}"
    if k == "kind->strs":
        return "map[exp.Kind][]string{" + ", ".join(f"exp.{kind_name(a)}: {{" + ", ".join(gs(s) for s in b) + "}" for a, b in v.items()) + "}"
    if k == "dtype->str":
        return "map[exp.DType]string{" + ", ".join(f"{dt(a)}: {gs(b)}" for a, b in v.items()) + "}"
    if k == "dtype->dtype":
        return "map[exp.DType]exp.DType{" + ", ".join(f"{dt(a)}: {dt(b)}" for a, b in v.items()) + "}"
    if k == "dtype->dtypeset":
        return "map[exp.DType]exp.TypeSet{" + ", ".join(f"{dt(a)}: " + go_value("", b, "dtypeset") for a, b in v.items()) + "}"
    if k == "tuple->str":
        return "map[string]string{" + ", ".join(f"{gs(' '.join(a))}: {gs(b)}" for a, b in v.items()) + "}"
    return None


def fn_source(f):
    if getattr(f, "__name__", "") == "from_arg_list" and hasattr(f, "__self__"):
        return f"FromArgList(exp.{kind_name(f.__self__)})"
    try:
        src = inspect.getsource(f).strip()
    except Exception:
        src = repr(f)
    if len(src) > 400:
        src = src[:400] + " ..."
    return src


def go_key(key):
    if isinstance(key, TokenType):
        return tt(key)
    if isinstance(key, type):
        return "exp." + kind_name(key)
    if isinstance(key, exp.DType):
        return dt(key)
    if isinstance(key, str):
        return gs(key)
    return repr(key)


def emit_fn(target, field, key, f):
    src = fn_source(f)
    if src.startswith("FromArgList("):
        print(f"\t{target}.{field}[{go_key(key)}] = {src}")
    else:
        print(f"\t// TODO {target}.{field}[{go_key(key)}]: " + src.replace("\n", "\n\t//   "))


def load(spec):
    mod, cls = spec.split(":")
    return getattr(importlib.import_module(mod), cls)


def attrs(cls):
    names = set()
    for k in dir(cls):
        if k.isupper() and not k.startswith("__"):
            names.add(k)
    return sorted(names)


def emit_data(cls, base, target="c"):
    for name in attrs(cls):
        v = getattr(cls, name)
        if base is not None and hasattr(base, name):
            bv = getattr(base, name)
            if v is bv or (not callable(v) and not isinstance(v, dict) and v == bv):
                continue
            if isinstance(v, dict) and isinstance(bv, dict) and v == bv:
                continue
        k = classify(name, v)
        field = camel(name)
        if k.endswith("->fn") or k == "fn":
            keys = list(v.keys()) if isinstance(v, dict) else []
            if base is not None and hasattr(base, name) and isinstance(getattr(base, name), dict):
                bv = getattr(base, name)
                for key in keys:
                    if key in bv and (bv[key] is v[key] or bv[key] == v[key]):
                        continue
                    emit_fn(target, field, key, v[key])
                for key in bv:
                    if key not in v:
                        print(f"\tdelete({target}.{field}, {key!r})")
            else:
                for key in keys:
                    emit_fn(target, field, key, v[key])
            continue
        gv = go_value(name, v, k)
        if gv is None:
            print(f"\t// TODO {field} ({k}): {v!r}"[:600])
        else:
            print(f"\t{target}.{field} = {gv}")


if __name__ == "__main__":
    mode = sys.argv[2]
    cls = load(sys.argv[3])
    if mode == "data":
        base = load(sys.argv[4]) if len(sys.argv) > 4 else None
        emit_data(cls, base, sys.argv[5] if len(sys.argv) > 5 else "c")
    elif mode == "classify":
        for name in attrs(cls):
            print(name, camel(name), classify(name, getattr(cls, name)))
