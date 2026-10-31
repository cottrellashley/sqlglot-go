package exp_test

import (
	"math"
	"testing"
	"time"
	_ "time/tzdata"

	_ "github.com/cottrellashley/sqlglot-go" // installs the generator backend
	. "github.com/cottrellashley/sqlglot-go/exp"
)

// TestConvertValue is the Go equivalent of test_expressions.py's test_convert and
// test_convert_python39, which convert Python-specific values (namedtuples, plain objects,
// pytz/zoneinfo datetimes).
func TestConvertValue(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value any
		want  string
	}{
		{1, "1"},
		{"1", "'1'"},
		{nil, "NULL"},
		{true, "TRUE"},
		{TupleValue{1, "2", nil}, "(1, '2', NULL)"},
		{[]any{1, "2", nil}, "ARRAY(1, '2', NULL)"},
		{OrderedMap{{"x", nil}}, "MAP(ARRAY('x'), ARRAY(NULL))"},
		{NaiveDatetime{time.Date(2022, 10, 1, 1, 1, 1, 1000, time.UTC)}, "TIME_STR_TO_TIME('2022-10-01 01:01:01.000001')"},
		{time.Date(2022, 10, 1, 1, 1, 1, 0, time.UTC), "TIME_STR_TO_TIME('2022-10-01 01:01:01+00:00', 'UTC')"},
		{time.Date(2022, 10, 1, 1, 1, 1, 0, la), "TIME_STR_TO_TIME('2022-10-01 01:01:01-07:00', 'America/Los_Angeles')"},
		{DateValue{2022, 10, 1}, "DATE_STR_TO_DATE('2022-10-01')"},
		{math.NaN(), "NULL"},
		{[]byte{0, 0, 0, 0, 0, 0, 0x07, 0xd3}, "2003"},
		{StructValue{{"x", 1}, {"y", 2}}, "STRUCT(1 AS x, 2 AS y)"},
		{TimeValue{14, 30, 45, 0}, "CAST('14:30:45' AS TIME)"},
		{TimeValue{9, 15, 30, 123456}, "CAST('09:15:30.123456' AS TIME)"},
	} {
		if got := ConvertValue(tc.value, false).SQL(); got != tc.want {
			t.Errorf("ConvertValue(%#v) = %s, want %s", tc.value, got, tc.want)
		}
	}
}
