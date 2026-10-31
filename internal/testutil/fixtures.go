// Package testutil holds the test helpers shared by the sqlglot test suites. It mirrors
// tests/helpers.py from the Python project.
package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FixturesDir returns the absolute path of testdata/fixtures.
func FixturesDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "fixtures")
}

func filterComments(s string) string {
	var lines []string
	for _, line := range splitLines(s) {
		if line != "" && !strings.HasPrefix(line, "--") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// splitLines mirrors str.splitlines().
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func read(filename string) string {
	b, err := os.ReadFile(filepath.Join(FixturesDir(), filename))
	if err != nil {
		panic(err)
	}
	return string(b)
}

// LoadSQLFixtures mirrors load_sql_fixtures: one statement per line, where lines starting
// with "#" are metadata attached to the following statement.
func LoadSQLFixtures(filename string) []string {
	var out, lines []string
	for _, line := range splitLines(filterComments(read(filename))) {
		lines = append(lines, line)
		if !strings.HasPrefix(line, "#") {
			out = append(out, strings.Join(lines, "\n"))
			lines = nil
		}
	}
	return out
}

// FixturePair is one (meta, sql, expected) entry of a fixture pairs file.
type FixturePair struct {
	Meta     map[string]string
	SQL      string
	Expected string
}

// ExtractMeta mirrors _extract_meta: it splits the leading "# key: value" lines off sql.
func ExtractMeta(sql string) (string, map[string]string) {
	meta := map[string]string{}
	lines := strings.Split(sql, "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(lines[i], "#") {
		key, val, _ := strings.Cut(lines[i], ":")
		meta[strings.TrimSpace(strings.TrimLeft(key, "#"))] = strings.TrimSpace(val)
		i++
	}
	return strings.Join(lines[i:], "\n"), meta
}

// LoadSQLFixturePairs mirrors load_sql_fixture_pairs: statements separated by ";" in
// (input, expected) pairs, with optional "# key: value" metadata lines.
func LoadSQLFixturePairs(filename string) []FixturePair {
	statements := strings.Split(filterComments(read(filename)), ";")
	var out []FixturePair
	for i := 0; i < len(statements); i += 2 {
		if i+1 < len(statements) {
			sql, meta := ExtractMeta(strings.TrimSpace(statements[i]))
			out = append(out, FixturePair{Meta: meta, SQL: sql, Expected: strings.TrimSpace(statements[i+1])})
		}
	}
	return out
}

// StringToBool mirrors string_to_bool.
func StringToBool(s string) bool {
	s = strings.ToLower(s)
	return s == "true" || s == "1"
}
