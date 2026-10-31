package executor

// Port of sqlglot/executor/table.py and context.py.

import (
	"fmt"
	"sort"
	"strings"
)

// Table is a simple row-oriented table: column names and rows of values.
type Table struct {
	Columns []string
	Rows    [][]any
	// columnRange limits the columns visible through the table's reader ([start, stop)).
	columnRange *[2]int
	reader      *RowReader
}

// NewTable builds a table; columnRange may be nil (all columns visible).
func NewTable(columns []string, rows [][]any, columnRange *[2]int) *Table {
	t := &Table{Columns: append([]string(nil), columns...), Rows: rows, columnRange: columnRange}
	if t.Rows == nil {
		t.Rows = [][]any{}
	}
	t.reader = newRowReader(t.Columns, columnRange)
	return t
}

// AddColumns appends columns (the rows must be widened by the caller).
func (t *Table) AddColumns(columns ...string) {
	t.Columns = append(t.Columns, columns...)
	if t.columnRange != nil {
		t.columnRange = &[2]int{t.columnRange[0], t.columnRange[1] + len(columns)}
	}
	t.reader = newRowReader(t.Columns, t.columnRange)
}

// Append adds a row.
func (t *Table) Append(row []any) {
	if len(row) != len(t.Columns) {
		panic(fmt.Errorf("row has %d values for %d columns", len(row), len(t.Columns)))
	}
	t.Rows = append(t.Rows, row)
}

// Len is the number of rows.
func (t *Table) Len() int { return len(t.Rows) }

// ToPylist mirrors Table.to_pylist: the rows as column -> value maps.
func (t *Table) ToPylist() []map[string]any {
	out := make([]map[string]any, len(t.Rows))
	for i, row := range t.Rows {
		m := make(map[string]any, len(t.Columns))
		for j, c := range t.Columns {
			m[c] = row[j]
		}
		out[i] = m
	}
	return out
}

func (t *Table) at(i int) *RowReader {
	t.reader.row = t.Rows[i]
	return t.reader
}

// String mirrors Table.__repr__.
func (t *Table) String() string {
	var columns []string
	var indexes []int
	for i, c := range t.Columns {
		if t.columnRange == nil || (i >= t.columnRange[0] && i < t.columnRange[1]) {
			columns = append(columns, c)
			indexes = append(indexes, i)
		}
	}
	lines := []string{strings.Join(columns, " ")}
	for r, row := range t.Rows {
		if r > 10 {
			break
		}
		var cells []string
		for k, i := range indexes {
			w := len(columns[k])
			s := Str(row[i])
			if len(s) < w {
				s = strings.Repeat(" ", w-len(s)) + s
			}
			cells = append(cells, s[:w])
		}
		lines = append(lines, strings.Join(cells, " "))
	}
	return strings.Join(lines, "\n")
}

// reader resolves column references to values: a RowReader yields scalars, a RangeReader the
// values of a range of rows (for aggregations).
type reader interface {
	get(column string) any
}

// RowReader reads the columns of the current row.
type RowReader struct {
	columns map[string]int
	row     []any
}

func newRowReader(columns []string, columnRange *[2]int) *RowReader {
	m := map[string]int{}
	for i, c := range columns {
		if columnRange == nil || (i >= columnRange[0] && i < columnRange[1]) {
			m[c] = i
		}
	}
	return &RowReader{columns: m}
}

// Row is the current row.
func (r *RowReader) Row() []any { return r.row }

func (r *RowReader) get(column string) any {
	i, ok := r.columns[column]
	if !ok {
		panic(&keyError{column})
	}
	return r.row[i]
}

// RangeReader reads the values of a column over a range of rows.
type RangeReader struct {
	table       *Table
	start, stop int
}

func (r *RangeReader) get(column string) any {
	out := make([]any, 0, max(0, r.stop-r.start))
	for i := r.start; i < r.stop; i++ {
		out = append(out, r.table.at(i).get(column))
	}
	return rangeValues(out)
}

// rangeValues are the values of a column over a range of rows (a Python generator).
type rangeValues []any

type keyError struct{ key string }

func (e *keyError) Error() string { return fmt.Sprintf("KeyError: '%s'", e.key) }

// Context is the execution context of expressions: the tables in scope and how column
// references are read (one row, or a range of rows for aggregations).
type Context struct {
	names        []string
	tables       map[string]*Table
	table        *Table
	rowReaders   map[string]reader
	rangeReaders map[string]reader
	ranges       map[string]*RangeReader
	scope        map[string]reader
	env          *Executor
}

func newContext(e *Executor, names []string, tables map[string]*Table, outer map[string]reader) *Context {
	c := &Context{names: names, tables: tables, env: e,
		rowReaders: map[string]reader{}, rangeReaders: map[string]reader{}, ranges: map[string]*RangeReader{}}
	for k, v := range outer {
		c.rowReaders[k] = v
		c.rangeReaders[k] = v
	}
	for _, name := range names {
		t := tables[name]
		c.rowReaders[name] = t.reader
		rr := &RangeReader{table: t}
		c.ranges[name] = rr
		c.rangeReaders[name] = rr
	}
	c.scope = c.rowReaders
	return c
}

// Table returns the context's table (all its tables must have the same columns and rows).
func (c *Context) Table() *Table {
	if c.table == nil {
		c.table = c.tables[c.names[0]]
		for _, name := range c.names {
			other := c.tables[name]
			if strings.Join(other.Columns, "\x00") != strings.Join(c.table.Columns, "\x00") {
				panic(fmt.Errorf("Columns are different."))
			}
			if len(other.Rows) != len(c.table.Rows) {
				panic(fmt.Errorf("Rows are different."))
			}
		}
	}
	return c.table
}

// Columns are the columns of the context's table.
func (c *Context) Columns() []string {
	if len(c.names) == 0 {
		return nil
	}
	return c.Table().Columns
}

func (c *Context) addColumns(columns ...string) {
	for _, name := range c.names {
		c.tables[name].AddColumns(columns...)
		c.rowReaders[name] = c.tables[name].reader
	}
}

// rows iterates the rows, positioning every table's reader on each one.
func (c *Context) rows(f func(r *RowReader)) {
	c.scope = c.rowReaders
	n := len(c.Table().Rows)
	for i := 0; i < n; i++ {
		var r *RowReader
		for _, name := range c.names {
			r = c.tables[name].at(i)
		}
		f(r)
	}
}

// tableRows iterates the rows of one table, positioning only its reader.
func (c *Context) tableRows(name string, f func(r *RowReader) bool) {
	c.scope = c.rowReaders
	t := c.tables[name]
	for i := 0; i < len(t.Rows); i++ {
		if !f(t.at(i)) {
			return
		}
	}
}

func (c *Context) eval(code code) any { return code(c) }

func (c *Context) evalTuple(codes []code) []any {
	out := make([]any, len(codes))
	for i, code := range codes {
		out[i] = code(c)
	}
	return out
}

func (c *Context) setRow(row []any) {
	for _, name := range c.names {
		c.tables[name].reader.row = row
	}
	c.scope = c.rowReaders
}

func (c *Context) setIndex(i int) {
	for _, name := range c.names {
		c.tables[name].at(i)
	}
	c.scope = c.rowReaders
}

func (c *Context) setRange(start, end int) {
	for _, name := range c.names {
		c.ranges[name].start, c.ranges[name].stop = start, end
	}
	c.scope = c.rangeReaders
}

// sort mirrors Context.sort: a stable sort of the rows by the key, NULLs last.
func (c *Context) sort(key []code) {
	rows := c.Table().Rows
	keys := make([][]any, len(rows))
	for i, row := range rows {
		c.setRow(row)
		keys[i] = c.evalTuple(key)
	}
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return compareSortKeys(keys[idx[i]], keys[idx[j]]) < 0 })
	sorted := make([][]any, len(rows))
	for i, k := range idx {
		sorted[i] = rows[k]
	}
	copy(rows, sorted)
}

// compareSortKeys compares the key tuples ((value is None, value) for each value).
func compareSortKeys(a, b []any) int {
	for i := range a {
		an, bn := a[i] == nil, b[i] == nil
		if an != bn {
			if an {
				return 1
			}
			return -1
		}
		if an {
			continue
		}
		if !keyEqual(a[i], b[i]) {
			return keyCompare(a[i], b[i])
		}
	}
	return 0
}

// orderedKey is the result of ORDERED(this, desc, nulls_first): a (rank, value) pair where
// NULLs rank first or last and desc reverses the value comparisons.
type orderedKey struct {
	rank    int
	null    bool
	value   any
	reverse bool
}

func keyEqual(a, b any) bool {
	x, ok1 := a.(orderedKey)
	y, ok2 := b.(orderedKey)
	if ok1 && ok2 {
		return x.rank == y.rank && x.null == y.null && (x.null || Equal(x.value, y.value))
	}
	return Equal(a, b)
}

func keyCompare(a, b any) int {
	x, ok1 := a.(orderedKey)
	y, ok2 := b.(orderedKey)
	if ok1 && ok2 {
		if x.rank != y.rank {
			return cmpInt64(int64(x.rank), int64(y.rank))
		}
		if x.null || y.null {
			// both NULL placeholders (0) compare equal; a NULL never shares a rank with a value
			return 0
		}
		if x.reverse {
			return Compare(y.value, x.value)
		}
		return Compare(x.value, y.value)
	}
	return Compare(a, b)
}
