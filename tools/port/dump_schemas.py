"""Dumps TPCH_SCHEMA and TPCDS_SCHEMA from tests/helpers.py to testdata/*_schema.json.

Usage: python3 -I dump_schemas.py <sqlglot checkout>/tests/helpers.py <go module>/testdata
"""

import ast, json, sys
src = open(sys.argv[1]).read()
tree = ast.parse(src)
out = {}
for node in tree.body:
    if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
        name = node.targets[0].id
        if name in ("TPCH_SCHEMA", "TPCDS_SCHEMA"):
            out[name] = ast.literal_eval(node.value)
for name, value in out.items():
    with open(f"{sys.argv[2]}/{name.lower()}.json", "w") as f:
        json.dump(value, f, indent=1)
    print(name, len(value))
