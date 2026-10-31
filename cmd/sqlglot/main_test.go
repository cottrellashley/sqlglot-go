package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		stdin string
		want  string
	}{
		{[]string{"--read", "snowflake", "--write", "duckdb", "--no-pretty", "SELECT IFF(a > 1, 'x', 'y') FROM t"},
			"", `SELECT CASE WHEN "a" > 1 THEN 'x' ELSE 'y' END FROM "t"` + "\n"},
		{[]string{"--no-pretty", "--identify", "false", "-"}, "select 1 as x; select 2", "SELECT 1 AS x\nSELECT 2\n"},
		{[]string{"--parse", "x"}, "", "Column(\n  this=Identifier(this=x, quoted=False))\n"},
	} {
		var out bytes.Buffer
		if err := run(tc.args, strings.NewReader(tc.stdin), &out); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if out.String() != tc.want {
			t.Errorf("%v:\n got %q\nwant %q", tc.args, out.String(), tc.want)
		}
	}
	if err := run([]string{"--error-level", "BOGUS", "x"}, nil, &bytes.Buffer{}); err == nil {
		t.Error("expected an error for an unknown error level")
	}
}
