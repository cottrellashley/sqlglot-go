// Package executor runs SQL queries against in-memory tables: a port of sqlglot's Python
// executor (sqlglot/executor). Queries are optimized, planned (sqlglot.NewPlan) and executed
// step by step; expressions are compiled into Go closures with the semantics of the Python
// code that sqlglot's executor generates.
package executor

import (
	"fmt"
	"sort"
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// Options are the optional arguments of Execute.
type Options struct {
	// Schema is the database schema; when nil it is inferred from the first row of each table.
	Schema any
	// Dialect is the SQL dialect used to parse the query.
	Dialect any
	// Tables are the tables to query: a nested mapping (catalog -> db -> table, db -> table
	// or table) whose leaves are *Table values or rows: []Row (ordered rows), or
	// []map[string]any, whose columns are sorted by name since Go maps are unordered. The
	// table names, and the column names of rows, are normalized for the dialect.
	Tables map[string]any
	// Env adds functions callable from SQL (by upper case name).
	Env map[string]Func
}

// Execute runs a SQL statement (a string or expression) against the tables.
func Execute(sql any, o Options) (result *Table, err error) {
	defer recoverExecute(&err)
	tables, err := EnsureTables(o.Tables, o.Dialect)
	if err != nil {
		return nil, err
	}
	schema := o.Schema
	if schema == nil || isEmptyMapping(schema) {
		schema, err = inferSchema(tables, o.Dialect)
		if err != nil {
			return nil, err
		}
	}
	s, err := sqlglot.EnsureSchema(schema, o.Dialect)
	if err != nil {
		return nil, err
	}
	if args := tables.supportedTableArgs(); len(args) > 0 {
		schemaArgs, err := s.SupportedTableArgs()
		if err != nil {
			return nil, err
		}
		if strings.Join(args, ",") != strings.Join(schemaArgs, ",") {
			return nil, &errs.ExecuteError{Message: "Tables must support the same table args as schema"}
		}
	}
	expression, err := sqlglot.Optimize(sql, sqlglot.OptimizeOptions{Schema: s, Dialect: o.Dialect, LeaveTablesIsolated: true})
	if err != nil {
		return nil, err
	}
	plan, err := sqlglot.NewPlan(expression)
	if err != nil {
		return nil, err
	}
	return New(tables, o.Env).Execute(plan, nil)
}

func isEmptyMapping(schema any) bool {
	switch x := schema.(type) {
	case map[string]any:
		return len(x) == 0
	case *sqlglot.SchemaMapping:
		return x == nil || x.Len() == 0
	}
	return false
}

// inferSchema builds a schema from the first row of each table.
func inferSchema(tables *Tables, dialect any) (any, error) {
	schema := sqlglot.NewOrderedMap[any]()
	for _, entry := range tables.entries {
		if len(entry.table.Rows) == 0 {
			return nil, &errs.ExecuteError{Message: "list index out of range"}
		}
		level := schema
		for _, k := range entry.path[:len(entry.path)-1] {
			next, ok := level.Get(k)
			if !ok {
				next = sqlglot.NewOrderedMap[any]()
				level.Set(k, next)
			}
			level = next.(*sqlglot.SchemaMapping)
		}
		columns := sqlglot.NewOrderedMap[any]()
		for i, column := range entry.table.Columns {
			value := entry.table.Rows[0][i]
			typ := sqlglot.AnnotateTypes(exp.ConvertValue(toConvertible(value), true), dialect).Type()
			if typ != nil {
				columns.Set(column, typ)
			} else {
				columns.Set(column, pyTypeName(value))
			}
		}
		level.Set(entry.path[len(entry.path)-1], columns)
	}
	return schema, nil
}

// toConvertible converts an executor value for exp.ConvertValue.
func toConvertible(v any) any {
	switch x := v.(type) {
	case Date:
		return exp.DateValue{Year: x.Year(), Month: int(x.Month()), Day: x.Day()}
	case Time:
		return exp.TimeValue{Hour: x.Hour, Minute: x.Minute, Second: x.Second, Microsecond: x.Microsecond}
	}
	return v
}

func recoverExecute(err *error) {
	if r := recover(); r != nil {
		switch x := r.(type) {
		case *errs.ExecuteError:
			*err = x
		case errs.SqlglotError:
			*err = x
		case error:
			*err = &errs.ExecuteError{Message: x.Error()}
		default:
			panic(r)
		}
	}
}

// Executor mirrors PythonExecutor: it executes plans against tables.
type Executor struct {
	env            map[string]Func
	tables         *Tables
	subqueryPlans  map[string]*subqueryPlan
	planNamesBySQL map[string]string
	ctes           *exp.Expr
	outerScope     map[string]reader
}

type subqueryPlan struct {
	plan  *sqlglot.Plan
	cache map[string]*Table
}

// New creates an executor; env adds functions callable from SQL.
func New(tables *Tables, extra map[string]Func) *Executor {
	if tables == nil {
		tables = &Tables{}
	}
	e := &Executor{env: map[string]Func{}, tables: tables, subqueryPlans: map[string]*subqueryPlan{}, planNamesBySQL: map[string]string{}}
	for k, v := range ENV {
		e.env[k] = v
	}
	for k, v := range extra {
		e.env[strings.ToUpper(k)] = v
	}
	e.env["SUBQUERY_COMPARISON"] = e.subqueryComparison
	e.env["SUBQUERY_EXISTS"] = e.subqueryExists
	e.env["SUBQUERY_SCALAR"] = e.subqueryScalar
	return e
}

// Execute runs a plan; outerScope holds the readers of an enclosing query for subqueries.
func (e *Executor) Execute(plan *sqlglot.Plan, outerScope map[string]reader) (result *Table, err error) {
	defer recoverExecute(&err)
	ctes, scope := e.ctes, e.outerScope
	e.ctes, e.outerScope = plan.Ctes, outerScope
	defer func() { e.ctes, e.outerScope = ctes, scope }()
	return e.execute(plan), nil
}

func (e *Executor) execute(plan *sqlglot.Plan) *Table {
	finished := map[*sqlglot.Step]bool{}
	contexts := map[*sqlglot.Step]*Context{}
	queue := plan.Leaves()
	queued := map[*sqlglot.Step]bool{}
	for _, s := range queue {
		queued[s] = true
	}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		delete(queued, node)
		func() {
			defer func() {
				if r := recover(); r != nil {
					cause, _ := r.(error)
					panic(&errs.ExecuteError{Message: fmt.Sprintf("Step '%s' failed: %s", node.ID(), panicMessage(r)), Cause: cause})
				}
			}()
			var names []string
			tables := map[string]*Table{}
			for _, dep := range node.Dependencies {
				for _, name := range contexts[dep].names {
					if _, ok := tables[name]; !ok {
						names = append(names, name)
					}
					tables[name] = contexts[dep].tables[name]
				}
			}
			context := e.context(names, tables)
			var result *Context
			switch node.Type {
			case sqlglot.ScanStep:
				result = e.scan(node, context)
			case sqlglot.AggregateStep:
				result = e.aggregate(node, context)
			case sqlglot.JoinStep:
				result = e.join(node, context)
			case sqlglot.SortStep:
				result = e.sort(node, context)
			case sqlglot.SetOperationStep:
				result = e.setOperation(node, context)
			default:
				panic(&notImplementedError{"unknown step"})
			}
			contexts[node] = result
			if node.Offset > 0 {
				t := result.tables[node.Name]
				t.Rows = t.Rows[min(node.Offset, len(t.Rows)):]
			}
			finished[node] = true
			for _, dep := range node.Dependents {
				ready := true
				for _, d := range dep.Dependencies {
					if _, ok := contexts[d]; !ok {
						ready = false
					}
				}
				if ready && !queued[dep] && !finished[dep] {
					queue = append(queue, dep)
					queued[dep] = true
				}
			}
			for _, dep := range node.Dependencies {
				done := true
				for _, d := range dep.Dependents {
					if !finished[d] {
						done = false
					}
				}
				if done {
					delete(contexts, dep)
				}
			}
		}()
	}
	root := plan.Root
	return contexts[root].tables[root.Name]
}

func panicMessage(r any) string {
	switch x := r.(type) {
	case *errs.ExecuteError:
		return x.Message
	case error:
		return x.Error()
	}
	return fmt.Sprint(r)
}

func (e *Executor) context(names []string, tables map[string]*Table) *Context {
	return newContext(e, names, tables, e.outerScope)
}

func (e *Executor) single(name string, t *Table) *Context {
	return e.context([]string{name}, map[string]*Table{name: t})
}

// generate mirrors PythonExecutor.generate: it compiles an expression (nil for no expression).
func (e *Executor) generate(expression *exp.Expr) code {
	if expression == nil {
		return nil
	}
	expression = e.replaceSubqueries(expression)
	cp := &compiler{e: e, lambda: map[string]int{}}
	return cp.compile(expression)
}

func (e *Executor) generateTuple(expressions []*exp.Expr) []code {
	out := make([]code, len(expressions))
	for i, x := range expressions {
		out[i] = e.generate(x)
	}
	return out
}

func isSubqueryNode(n *exp.Expr) bool {
	return n.IsAny(exp.Subquery, exp.Exists, exp.All, exp.Any)
}

func findSubquery(e *exp.Expr) *exp.Expr {
	for n := range e.BFS(nil) {
		if isSubqueryNode(n) {
			return n
		}
	}
	return nil
}

func (e *Executor) replaceSubqueries(expression *exp.Expr) *exp.Expr {
	if findSubquery(expression) == nil {
		return expression
	}
	expression = expression.Copy()
	for {
		subquery := findSubquery(expression)
		if subquery == nil {
			return expression
		}
		target, replacement := e.compileSubquery(subquery)
		if target == expression {
			expression = replacement
		} else {
			target.Replace(replacement)
		}
	}
}

func subqueryCall(name string, args ...*exp.Expr) *exp.Expr {
	return exp.New(exp.Anonymous, "this", name, "expressions", args)
}

func (e *Executor) compileSubquery(subquery *exp.Expr) (*exp.Expr, *exp.Expr) {
	query := subquery.This().Unnest()
	scope := sqlglot.BuildScope(query)
	var outerColumns []*exp.Expr
	if scope != nil {
		for _, c := range scope.ExternalColumns() {
			outerColumns = append(outerColumns, c.Copy())
		}
	}
	plan := e.registerSubquery(query)
	parent := subquery.Parent()
	scopeVar := exp.New(exp.Var, "this", "scope")

	if subquery.IsKind(exp.Exists) {
		return subquery, subqueryCall("SUBQUERY_EXISTS", append([]*exp.Expr{plan, scopeVar}, outerColumns...)...)
	}
	if n := len(query.Selects()); n != 1 {
		panic(&errs.ExecuteError{Message: fmt.Sprintf("Subquery used as an expression returned %d columns", n)})
	}
	if subquery.IsAny(exp.All, exp.Any) {
		return e.compileQuantified(parent, strings.ToUpper(subquery.Key()), plan, outerColumns, "")
	}
	if parent.IsKind(exp.In) && subquery == parent.Get("query") {
		return e.compileQuantified(parent, "ANY", plan, outerColumns, "EQ")
	}
	return subquery, subqueryCall("SUBQUERY_SCALAR", append([]*exp.Expr{plan, scopeVar}, outerColumns...)...)
}

func (e *Executor) compileQuantified(comparison *exp.Expr, quantifier string, plan *exp.Expr, outerColumns []*exp.Expr, op string) (*exp.Expr, *exp.Expr) {
	if !comparison.IsAny(exp.Binary, exp.In) {
		panic(&errs.ExecuteError{Message: fmt.Sprintf("Unsupported %s subquery: expected a comparison", quantifier)})
	}
	if op == "" {
		op = strings.ToUpper(comparison.Key())
	}
	args := []*exp.Expr{comparison.This(), plan, exp.New(exp.Var, "this", "scope"), exp.LiteralString(op), exp.LiteralString(quantifier)}
	return comparison, subqueryCall("SUBQUERY_COMPARISON", append(args, outerColumns...)...)
}

func (e *Executor) registerSubquery(query *exp.Expr) *exp.Expr {
	if e.ctes != nil && query.Get("with_") == nil {
		query.Set("with_", e.ctes)
	}
	sql := query.SQL()
	name, ok := e.planNamesBySQL[sql]
	if !ok {
		name = fmt.Sprintf("_sq_%d", len(e.subqueryPlans))
		e.planNamesBySQL[sql] = name
		plan, err := sqlglot.NewPlan(query)
		if err != nil {
			panic(err)
		}
		e.subqueryPlans[name] = &subqueryPlan{plan: plan, cache: map[string]*Table{}}
	}
	return exp.LiteralString(name)
}

func (e *Executor) subqueryTable(planName any, scope any, args []any) *Table {
	sp := e.subqueryPlans[Str(planName)]
	key, hashable := "", true
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(*typeError); !ok {
					panic(r)
				}
				hashable = false // an unhashable correlated value can't be memoized
			}
		}()
		key = RowKey(args)
	}()
	if hashable {
		if t, ok := sp.cache[key]; ok {
			return t
		}
	}
	outer, _ := scope.(scopeValue)
	t, err := e.Execute(sp.plan, outer)
	if err != nil {
		panic(err)
	}
	if hashable {
		sp.cache[key] = t
	}
	return t
}

func (e *Executor) subqueryExists(args ...any) any {
	return len(e.subqueryTable(args[0], args[1], args[2:]).Rows) > 0
}

func (e *Executor) subqueryScalar(args ...any) any {
	rows := e.subqueryTable(args[0], args[1], args[2:]).Rows
	if len(rows) > 1 {
		panic(&errs.ExecuteError{Message: "More than one row returned by a subquery used as an expression"})
	}
	if len(rows) == 0 {
		return nil
	}
	return rows[0][0]
}

func (e *Executor) subqueryComparison(args ...any) any {
	value, planName, scope, op, quantifier := args[0], args[1], args[2], Str(args[3]), Str(args[4])
	compare := e.env[op]
	isAny := quantifier == "ANY"
	sawNull := false
	for _, row := range e.subqueryTable(planName, scope, args[5:]).Rows {
		result := compare(value, row[0])
		if result == nil {
			sawNull = true
		} else if Truthy(result) == isAny {
			return isAny
		}
	}
	if sawNull {
		return nil
	}
	return !isAny
}

func columnNames(expressions []*exp.Expr) []string {
	out := make([]string, len(expressions))
	for i, x := range expressions {
		out[i] = x.AliasOrName()
	}
	return out
}

func (e *Executor) scan(step *sqlglot.Step, context *Context) *Context {
	source := ""
	hasSource := step.Source != nil
	if hasSource {
		source = step.Source.Name()
		if source == "" {
			source = step.Source.Alias()
		}
	}
	var tableIter func(f func(*RowReader) bool)
	switch {
	case !hasSource:
		context = e.context(nil, map[string]*Table{})
		static := &RowReader{columns: map[string]int{}, row: []any{}}
		tableIter = func(f func(*RowReader) bool) { f(static) }
	case context.has(source):
		if len(step.Projections) == 0 && step.Condition == nil {
			return e.single(step.Name, context.tables[source])
		}
		tableIter = func(f func(*RowReader) bool) { context.tableRows(source, f) }
	default:
		var t *Table
		context, t = e.scanTable(step)
		tableIter = func(f func(*RowReader) bool) {
			context.scope = context.rowReaders
			for i := range t.Rows {
				if !f(t.at(i)) {
					return
				}
			}
		}
	}
	return e.single(step.Name, e.projectAndFilter(context, step, tableIter))
}

func (c *Context) has(name string) bool {
	_, ok := c.tables[name]
	return ok
}

func (e *Executor) scanTable(step *sqlglot.Step) (*Context, *Table) {
	t := e.tables.find(step.Source)
	if t == nil {
		panic(&errs.ExecuteError{Message: fmt.Sprintf("Table '%s' not found", step.Source.SQL())})
	}
	return e.single(step.Source.AliasOrName(), t), t
}

func (e *Executor) projectAndFilter(context *Context, step *sqlglot.Step, tableIter func(f func(*RowReader) bool)) *Table {
	var sink *Table
	if len(step.Projections) > 0 {
		sink = NewTable(columnNames(step.Projections), nil, nil)
	} else {
		sink = NewTable(context.Columns(), nil, nil)
	}
	condition := e.generate(step.Condition)
	projections := e.generateTuple(step.Projections)
	limit := step.Limit
	tableIter(func(r *RowReader) bool {
		if limit != sqlglot.NoLimit && len(sink.Rows) >= step.Offset+limit {
			return false
		}
		if condition != nil && !Truthy(context.eval(condition)) {
			return true
		}
		if len(projections) > 0 {
			sink.Append(context.evalTuple(projections))
		} else {
			sink.Append(r.row)
		}
		return true
	})
	return sink
}

type columnRange = [2]int

func (e *Executor) join(step *sqlglot.Step, context *Context) *Context {
	source := step.SourceName
	sourceTable := context.tables[source]
	sourceContext := e.single(source, sourceTable)
	rangeNames := []string{source}
	ranges := map[string]columnRange{source: {0, len(sourceTable.Columns)}}

	for name, join := range step.Joins.All() {
		t := context.tables[name]
		start := 0
		for _, r := range ranges {
			start = max(start, r[1])
		}
		ranges[name] = columnRange{start, start + len(t.Columns)}
		rangeNames = append(rangeNames, name)
		joinContext := e.single(name, t)
		condition := e.generate(join.Condition)
		var conditionContext *Context
		if condition != nil {
			columns := append(append([]string{}, sourceContext.Columns()...), joinContext.Columns()...)
			tables := map[string]*Table{}
			for _, n := range rangeNames {
				r := ranges[n]
				tables[n] = NewTable(columns, nil, &r)
			}
			conditionContext = e.context(rangeNames, tables)
		}
		var joined *Table
		if len(join.SourceKey) > 0 {
			joined = e.hashJoin(join, sourceContext, joinContext, condition, conditionContext)
		} else {
			joined = e.nestedLoopJoin(join, sourceContext, joinContext, condition, conditionContext)
		}
		tables := map[string]*Table{}
		for _, n := range rangeNames {
			r := ranges[n]
			tables[n] = NewTable(joined.Columns, joined.Rows, &r)
		}
		sourceContext = e.context(append([]string{}, rangeNames...), tables)
	}

	if step.Condition == nil && len(step.Projections) == 0 {
		return sourceContext
	}
	sink := e.projectAndFilter(sourceContext, step, func(f func(*RowReader) bool) {
		stop := false
		sourceContext.rows(func(r *RowReader) {
			if !stop && !f(r) {
				stop = true
			}
		})
	})
	if len(step.Projections) > 0 {
		return e.single(step.Name, sink)
	}
	tables := map[string]*Table{}
	for _, n := range sourceContext.names {
		t := sourceContext.tables[n]
		tables[n] = NewTable(t.Columns, sink.Rows, t.columnRange)
	}
	return e.context(sourceContext.names, tables)
}

func joinMatches(row []any, condition code, conditionContext *Context) bool {
	if condition == nil {
		return true
	}
	conditionContext.setRow(row)
	v := conditionContext.eval(condition)
	b, ok := v.(bool)
	return ok && b
}

func concatRows(a, b []any) []any {
	return append(append(make([]any, 0, len(a)+len(b)), a...), b...)
}

func (e *Executor) nestedLoopJoin(join *sqlglot.JoinSpec, sourceContext, joinContext *Context, condition code, conditionContext *Context) *Table {
	t := NewTable(append(append([]string{}, sourceContext.Columns()...), joinContext.Columns()...), nil, nil)
	sourceRows, joinRows := sourceContext.Table().Rows, joinContext.Table().Rows
	matchedSource, matchedJoin := map[int]bool{}, map[int]bool{}
	for si, sr := range sourceRows {
		for ji, jr := range joinRows {
			row := concatRows(sr, jr)
			if joinMatches(row, condition, conditionContext) {
				t.Append(row)
				matchedSource[si], matchedJoin[ji] = true, true
			}
		}
	}
	appendUnmatchedJoinRows(t, join, sourceRows, joinRows, matchedSource, matchedJoin)
	return t
}

func (e *Executor) hashJoin(join *sqlglot.JoinSpec, sourceContext, joinContext *Context, condition code, conditionContext *Context) *Table {
	sourceKey, joinKey := e.generateTuple(join.SourceKey), e.generateTuple(join.JoinKey)
	type entry struct {
		index int
		row   []any
	}
	var order []string
	groups := map[string]*[2][]entry{}
	add := func(ctx *Context, key []code, side int) {
		i := 0
		ctx.rows(func(r *RowReader) {
			values := ctx.evalTuple(key)
			ok := true
			for _, v := range values {
				if v == nil {
					ok = false
				}
			}
			if ok {
				k := RowKey(values)
				g, exists := groups[k]
				if !exists {
					g = &[2][]entry{}
					groups[k] = g
					order = append(order, k)
				}
				g[side] = append(g[side], entry{i, r.row})
			}
			i++
		})
	}
	add(sourceContext, sourceKey, 0)
	add(joinContext, joinKey, 1)

	t := NewTable(append(append([]string{}, sourceContext.Columns()...), joinContext.Columns()...), nil, nil)
	matchedSource, matchedJoin := map[int]bool{}, map[int]bool{}
	for _, k := range order {
		g := groups[k]
		for _, s := range g[0] {
			for _, j := range g[1] {
				row := concatRows(s.row, j.row)
				if joinMatches(row, condition, conditionContext) {
					t.Append(row)
					matchedSource[s.index], matchedJoin[j.index] = true, true
				}
			}
		}
	}
	appendUnmatchedJoinRows(t, join, sourceContext.Table().Rows, joinContext.Table().Rows, matchedSource, matchedJoin)
	return t
}

func appendUnmatchedJoinRows(t *Table, join *sqlglot.JoinSpec, sourceRows, joinRows [][]any, matchedSource, matchedJoin map[int]bool) {
	side := join.Side
	if side == "LEFT" || side == "FULL" {
		n := 0
		if len(sourceRows) > 0 {
			n = len(t.Columns) - len(sourceRows[0])
		}
		nulls := make([]any, n)
		for i, row := range sourceRows {
			if !matchedSource[i] {
				t.Append(concatRows(row, nulls))
			}
		}
	}
	if side == "RIGHT" || side == "FULL" {
		width := len(t.Columns)
		if len(joinRows) > 0 {
			width -= len(joinRows[0])
		}
		nulls := make([]any, width)
		for i, row := range joinRows {
			if !matchedJoin[i] {
				t.Append(concatRows(nulls, row))
			}
		}
	}
}

func (e *Executor) aggregate(step *sqlglot.Step, context *Context) *Context {
	groupBy := e.generateTuple(step.Group.Values())
	aggregations := e.generateTuple(step.Aggregations)
	operands := e.generateTuple(step.Operands)

	if len(operands) > 0 {
		operandTable := NewTable(columnNames(step.Operands), nil, nil)
		context.rows(func(*RowReader) {
			operandTable.Append(context.evalTuple(operands))
		})
		rows := context.Table().Rows
		for i := range rows {
			rows[i] = concatRows(rows[i], operandTable.Rows[i])
		}
		width := len(context.Columns())
		context.addColumns(operandTable.Columns...)
		r := columnRange{width, width + len(operandTable.Columns)}
		operandView := NewTable(context.Columns(), rows, &r)
		names := append([]string{""}, context.names...)
		tables := map[string]*Table{"": operandView}
		for _, n := range context.names {
			tables[n] = context.tables[n]
		}
		context = e.context(dedupe(names), tables)
	}

	context.sort(groupBy)

	var group []any
	start, end := 0, 1
	length := len(context.Table().Rows)
	table := NewTable(append(append([]string{}, step.Group.Keys()...), columnNames(step.Aggregations)...), nil, nil)
	addRow := func() { table.Append(concatRows(group, context.evalTuple(aggregations))) }

	if length > 0 {
		for i := 0; i < length; i++ {
			context.setIndex(i)
			key := context.evalTuple(groupBy)
			if group == nil {
				group = key
			}
			end++
			if !rowsEqual(key, group) {
				context.setRange(start, end-2)
				addRow()
				group = key
				start = end - 2
			}
			if step.Condition == nil && step.Limit != sqlglot.NoLimit && len(table.Rows) >= step.Offset+step.Limit {
				break
			}
			if i == length-1 {
				context.setRange(start, end-1)
				addRow()
			}
		}
	} else if step.Limit > 0 && len(groupBy) == 0 {
		context.setRange(0, 0)
		table.Append(context.evalTuple(aggregations))
	}

	names := []string{step.Name}
	tables := map[string]*Table{step.Name: table}
	for _, n := range context.names {
		if _, ok := tables[n]; !ok {
			names = append(names, n)
		}
		tables[n] = table
	}
	context = e.context(names, tables)
	if len(step.Projections) > 0 || step.Condition != nil {
		return e.single(step.Name, e.projectAndFilter(context, step, func(f func(*RowReader) bool) { context.tableRows(step.Name, f) }))
	}
	return context
}

func dedupe(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func rowsEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func (e *Executor) sort(step *sqlglot.Step, context *Context) *Context {
	projections := e.generateTuple(step.Projections)
	projectionColumns := columnNames(step.Projections)
	allColumns := append(append([]string{}, context.Columns()...), projectionColumns...)
	sink := NewTable(allColumns, nil, nil)
	context.rows(func(r *RowReader) {
		sink.Append(concatRows(r.row, context.evalTuple(projections)))
	})
	names := append([]string{""}, context.names...)
	tables := map[string]*Table{"": sink}
	for _, n := range context.names {
		tables[n] = sink
	}
	sortCtx := e.context(dedupe(names), tables)
	sortCtx.sort(e.generateTuple(step.Key))
	rows := sortCtx.Table().Rows
	if step.Limit != sqlglot.NoLimit {
		rows = rows[:min(len(rows), step.Offset+step.Limit)]
	}
	if len(projectionColumns) > 0 {
		width := len(context.Columns())
		out := make([][]any, len(rows))
		for i, row := range rows {
			out[i] = row[width:len(allColumns)]
		}
		rows = out
	}
	columns := projectionColumns
	if len(columns) == 0 {
		columns = context.Columns()
	}
	return e.single(step.Name, NewTable(columns, rows, nil))
}

func (e *Executor) setOperation(step *sqlglot.Step, context *Context) *Context {
	left, right := context.tables[step.Left], context.tables[step.Right]
	sink := NewTable(left.Columns, nil, nil)
	counts := func(rows [][]any) map[string]int {
		m := map[string]int{}
		for _, r := range rows {
			m[RowKey(r)]++
		}
		return m
	}
	switch {
	case step.Op == exp.Intersect:
		rightCounts := counts(right.Rows)
		seen := map[string]bool{}
		for _, row := range left.Rows {
			k := RowKey(row)
			if rightCounts[k] > 0 && (!step.Distinct || !seen[k]) {
				sink.Append(row)
				seen[k] = true
				if !step.Distinct {
					rightCounts[k]--
				}
			}
		}
	case step.Op == exp.Except:
		rightCounts := counts(right.Rows)
		seen := map[string]bool{}
		for _, row := range left.Rows {
			k := RowKey(row)
			if rightCounts[k] > 0 && !step.Distinct {
				rightCounts[k]--
			} else if rightCounts[k] == 0 && (!step.Distinct || !seen[k]) {
				sink.Append(row)
				seen[k] = true
			}
		}
	case step.Op == exp.Union && step.Distinct:
		// list(set(left.rows).union(set(right.rows))): CPython's set order
		toTuples := func(rows [][]any) []any {
			out := make([]any, len(rows))
			for i, r := range rows {
				out[i] = Tuple(r)
			}
			return out
		}
		for _, row := range pySetUnion(toTuples(left.Rows), toTuples(right.Rows)) {
			sink.Rows = append(sink.Rows, []any(row.(Tuple)))
		}
	default:
		sink.Rows = append(append([][]any{}, left.Rows...), right.Rows...)
	}
	if step.Limit != sqlglot.NoLimit {
		sink.Rows = sink.Rows[:min(len(sink.Rows), step.Offset+step.Limit)]
	}
	return e.single(step.Name, sink)
}

// Tables mirrors executor.table.Tables: tables by (catalog, db,) name.
type Tables struct {
	entries []tableEntry
	depth   int
}

type tableEntry struct {
	path  []string // mapping order: [catalog, db,] table
	table *Table
}

// EnsureTables mirrors ensure_tables: it normalizes the table and column names of a nested
// mapping of tables.
func EnsureTables(tables map[string]any, dialect any) (t *Tables, err error) {
	defer recoverExecute(&err)
	d, err := sqlglot.GetDialect(dialect)
	if err != nil {
		return nil, err
	}
	t = &Tables{}
	var walk func(m map[string]any, path []string)
	walk = func(m map[string]any, path []string) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			name := d.NormalizeIdentifier(exp.ToIdentifier(k)).Name()
			switch v := m[k].(type) {
			case map[string]any:
				walk(v, append(append([]string{}, path...), name))
			case *Table:
				t.add(append(append([]string{}, path...), name), v)
			case []map[string]any:
				t.add(append(append([]string{}, path...), name), tableFromRows(v, d))
			case []Row:
				t.add(append(append([]string{}, path...), name), tableFromOrderedRows(v, d))
			default:
				panic(&errs.ExecuteError{Message: fmt.Sprintf("unsupported table value %T", v)})
			}
		}
	}
	walk(tables, nil)
	return t, nil
}

func tableFromRows(rows []map[string]any, d *sqlglot.Dialect) *Table {
	if len(rows) == 0 {
		return NewTable(nil, nil, nil)
	}
	var columns []string
	for c := range rows[0] {
		columns = append(columns, c)
	}
	sort.Strings(columns)
	normalized := make([]string, len(columns))
	for i, c := range columns {
		normalized[i] = d.NormalizeIdentifier(exp.ToIdentifier(c)).Name()
	}
	out := make([][]any, len(rows))
	for i, row := range rows {
		r := make([]any, len(columns))
		for j, c := range columns {
			r[j] = row[c]
		}
		out[i] = r
	}
	return NewTable(normalized, out, nil)
}

// Row is a table row whose columns are ordered (the first row gives the column order).
type Row = *sqlglot.OrderedMap[any]

func tableFromOrderedRows(rows []Row, d *sqlglot.Dialect) *Table {
	if len(rows) == 0 {
		return NewTable(nil, nil, nil)
	}
	columns := rows[0].Keys()
	normalized := make([]string, len(columns))
	for i, c := range columns {
		normalized[i] = d.NormalizeIdentifier(exp.ToIdentifier(c)).Name()
	}
	out := make([][]any, len(rows))
	for i, row := range rows {
		r := make([]any, len(columns))
		for j, c := range columns {
			r[j], _ = row.Get(c)
		}
		out[i] = r
	}
	return NewTable(normalized, out, nil)
}

func (t *Tables) add(path []string, table *Table) {
	if t.depth == 0 {
		t.depth = len(path)
	}
	t.entries = append(t.entries, tableEntry{path, table})
}

// Add registers a table under its path (catalog, db, table names).
func (t *Tables) Add(table *Table, path ...string) { t.add(path, table) }

func (t *Tables) supportedTableArgs() []string {
	if t.depth == 0 {
		return nil
	}
	return exp.TableParts[:min(t.depth, len(exp.TableParts))]
}

// Find returns the table a table expression refers to (nil if there is none), mirroring
// AbstractMappingSchema.find.
func (t *Tables) Find(table *exp.Expr) *Table { return t.find(table) }

// find mirrors AbstractMappingSchema.find for a table expression.
func (t *Tables) find(table *exp.Expr) *Table {
	var parts []string // reversed: table, db, catalog
	for _, p := range []string{"this", "db", "catalog"} {
		if v := table.Get(p); v != nil {
			parts = append(parts, v.Name())
		}
	}
	if len(parts) > t.depth {
		parts = parts[:t.depth]
	}
	var matches []tableEntry
	for _, e := range t.entries {
		ok := true
		for i, p := range parts {
			if e.path[len(e.path)-1-i] != p {
				ok = false
				break
			}
		}
		if ok {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return nil
	case 1:
		return matches[0].table
	}
	var names []string
	for _, m := range matches {
		names = append(names, strings.Join(m.path[:len(m.path)-len(parts)], "."))
	}
	panic(&errs.SchemaError{Message: fmt.Sprintf("Ambiguous mapping for %s: %s.", table.SQL(), strings.Join(names, ", "))})
}
