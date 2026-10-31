#!/bin/sh
# Regenerates the Go test files converted from sqlglot's Python test suite.
# Usage: tools/port/convert_all.sh <path to the sqlglot python checkout>
set -e
REF=$1
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
conv() { # <python test file relative to $REF> <go output> <package>
	(cd /tmp && python3 -I "$ROOT/tools/port/convert_tests.py" "$REF/$1" "$1" "$3") > "$2.tmp"
	gofmt "$2.tmp" > "$2" && rm "$2.tmp"
}
for f in "$REF"/tests/dialects/test_*.py; do
	name=$(basename "$f" .py)
	conv "tests/dialects/$name.py" "$ROOT/tests/dialects/${name#test_}_test.go" dialects_test
done
# Core suites (the ones for modules that exist in Go).
for name in test_build test_errors test_expressions test_generator test_helper test_jsonpath test_parser \
	test_schema test_serde test_time test_tokens test_transforms test_transpile test_typing; do
	conv "tests/$name.py" "$ROOT/tests/core/${name#test_}_test.go" core_test
done
