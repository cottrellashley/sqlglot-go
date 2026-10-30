package sqlglot_test

import (
	"strings"
	"testing"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/internal/testutil"
)

func TestIdentityFixtures(t *testing.T) {
	failures := 0
	for _, sql := range testutil.LoadSQLFixtures("identity.sql") {
		out, err := sqlglot.Transpile(sql, nil, nil)
		want := strings.TrimSpace(sql)
		if err != nil {
			failures++
			t.Errorf("%s\n  error: %v", want, err)
			continue
		}
		if out[0] != want {
			failures++
			t.Errorf("\n got: %s\nwant: %s", out[0], want)
		}
	}
	if failures > 0 {
		t.Logf("%d failures", failures)
	}
}

func TestPrettyFixtures(t *testing.T) {
	for _, p := range testutil.LoadSQLFixturePairs("pretty.sql") {
		out, err := sqlglot.Transpile(p.SQL, nil, nil, exp.Pretty(true))
		if err != nil {
			t.Errorf("%s\n  error: %v", p.SQL, err)
			continue
		}
		if out[0] != p.Expected {
			t.Errorf("\n got: %s\nwant: %s", out[0], p.Expected)
		}
	}
}
