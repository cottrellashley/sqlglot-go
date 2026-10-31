"""Transpiles the SQL lines of stdin with Python sqlglot, printing a JSON list (or ERROR) per
line: the reference side of differential testing against internal/cmd/gosql.

Usage: python3 -I pysql.py <sqlglot checkout> [read dialect] [write dialect] < queries.sql
"""

import json
import sys

sys.path.insert(0, sys.argv[1])

import sqlglot  # noqa: E402

read = sys.argv[2] if len(sys.argv) > 2 else ""
write = sys.argv[3] if len(sys.argv) > 3 else read
for line in sys.stdin:
    line = line.rstrip("\n")
    if not line:
        continue
    try:
        print(json.dumps(sqlglot.transpile(line, read=read or None, write=write or None)))
    except Exception as e:
        print("ERROR", type(e).__name__, e)
