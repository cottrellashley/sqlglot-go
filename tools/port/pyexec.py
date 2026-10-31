"""Executes the SQL lines of stdin with sqlglot's Python executor against a small table, printing
the rows as JSON: the reference side of differential testing against internal/cmd/goexec.

Usage: python3 -I pyexec.py <sqlglot checkout> < queries.sql
"""

import sys, json
sys.path.insert(0, sys.argv[1])
import logging; logging.disable(logging.CRITICAL)
from sqlglot.executor import execute
tables = {"t": [{"a": 1, "b": "x"}, {"a": 2, "b": "y"}, {"a": 2, "b": "z"}, {"a": None, "b": "w"}]}
for line in sys.stdin:
    sql = line.rstrip("\n")
    if not sql:
        continue
    try:
        r = execute(sql, tables=tables)
        print(json.dumps([list(row) for row in r.rows], default=str))
    except Exception as e:
        print("ERROR " + type(e).__name__)
