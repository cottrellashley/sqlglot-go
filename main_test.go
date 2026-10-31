package sqlglot_test

import (
	"os"
	"testing"

	"github.com/cottrellashley/sqlglot-go/exp"
)

func TestMain(m *testing.M) {
	// like sqlglot's test suite (UNITTEST mode), reject undeclared argument keys
	exp.StrictArgKeys = true
	os.Exit(m.Run())
}
