# Emits the EXPRESSION_METADATA of a typing module as (class, kind, value) records.
import sys, inspect, json
sys.path.insert(0, sys.argv[1])
import importlib
mod = importlib.import_module(sys.argv[2])
attr = sys.argv[3] if len(sys.argv) > 3 else "EXPRESSION_METADATA"
meta = getattr(mod, attr)
from sqlglot import exp
rows = []
for cls, spec in meta.items():
    if "annotator" in spec:
        fn = spec["annotator"]
        try:
            src = inspect.getsource(fn).strip()
        except Exception:
            src = repr(fn)
        name = getattr(fn, "__name__", "")
        if name != "<lambda>":
            src = "FN:" + name
        rows.append({"cls": cls.__name__, "kind": "annotator", "src": src, "line": fn.__code__.co_firstlineno if hasattr(fn, "__code__") else 0})
    elif "returns" in spec:
        r = spec["returns"]
        if isinstance(r, exp.DType):
            rows.append({"cls": cls.__name__, "kind": "returns", "dtype": r.name})
        else:
            rows.append({"cls": cls.__name__, "kind": "returns_expr", "sql": r.sql()})
    else:
        rows.append({"cls": cls.__name__, "kind": "empty"})
print(json.dumps(rows))
