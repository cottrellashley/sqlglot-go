package sqlglot

// Port of sqlglot/planner.py: a logical plan (a DAG of steps) built from an optimized query,
// executed by the executor package.

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// StepType is the type of a plan step.
type StepType int

// The plan step types.
const (
	ScanStep StepType = iota
	JoinStep
	AggregateStep
	SortStep
	SetOperationStep
)

// NoLimit is the Limit of steps without a LIMIT (Python's math.inf).
const NoLimit = math.MaxInt

// JoinSpec describes one join of a Join step.
type JoinSpec struct {
	Side      string
	JoinKey   []*exp.Expr
	SourceKey []*exp.Expr
	Condition *exp.Expr
}

// Step is one step of a Plan.
type Step struct {
	Type         StepType
	Name         string
	Dependencies []*Step
	Dependents   []*Step
	Projections  []*exp.Expr
	Limit        int
	Offset       int
	Condition    *exp.Expr

	// Scan: the source table expression (nil for a static scan).
	Source *exp.Expr
	// Join: the name of the source being joined to, and the joins by name.
	SourceName string
	Joins      *OrderedMap[*JoinSpec]
	// Aggregate: the aggregations, their operands (aliased), the group expressions by name,
	// and the name of the aggregated source.
	Aggregations    []*exp.Expr
	Operands        []*exp.Expr
	Group           *OrderedMap[*exp.Expr]
	AggregateSource string
	// Sort: the ORDER BY keys.
	Key []*exp.Expr
	// SetOperation: the operation kind (exp.Union, exp.Intersect or exp.Except), the names of
	// its operands and whether it is DISTINCT.
	Op          exp.Kind
	Left, Right string
	Distinct    bool

	id int
}

var stepIDs int

func newStep(t StepType) *Step {
	stepIDs++
	s := &Step{Type: t, Limit: NoLimit, id: stepIDs}
	if t == JoinStep {
		s.Joins = NewOrderedMap[*JoinSpec]()
	}
	if t == AggregateStep {
		s.Group = NewOrderedMap[*exp.Expr]()
	}
	return s
}

// AddDependency mirrors Step.add_dependency.
func (s *Step) AddDependency(dep *Step) {
	for _, d := range s.Dependencies {
		if d == dep {
			return
		}
	}
	s.Dependencies = append(s.Dependencies, dep)
	dep.Dependents = append(dep.Dependents, s)
}

// Plan mirrors planner.Plan: the step DAG of a query.
type Plan struct {
	Expression *exp.Expr
	Ctes       *exp.Expr
	Root       *Step
	dag        *OrderedMap[*Step]
}

// NewPlan builds the plan of an optimized query (tables and subqueries must be aliased).
func NewPlan(expression *exp.Expr) (plan *Plan, err error) {
	defer recoverOptimize(&err)
	p := &Plan{Expression: expression.Copy()}
	if with := p.Expression.Get("with_"); with != nil {
		p.Ctes = with.Copy()
	}
	p.Root = StepFromExpression(p.Expression, nil)
	return p, nil
}

// Steps returns every step of the plan (the keys of Plan.dag).
func (p *Plan) Steps() []*Step {
	var out []*Step
	seen := map[*Step]bool{}
	nodes := []*Step{p.Root}
	for len(nodes) > 0 {
		node := nodes[len(nodes)-1]
		nodes = nodes[:len(nodes)-1]
		if seen[node] {
			continue
		}
		seen[node] = true
		out = append(out, node)
		nodes = append(nodes, node.Dependencies...)
	}
	return out
}

// Leaves returns the steps without dependencies.
func (p *Plan) Leaves() []*Step {
	var out []*Step
	for _, s := range p.Steps() {
		if len(s.Dependencies) == 0 {
			out = append(out, s)
		}
	}
	return out
}

func (p *Plan) String() string { return "Plan\n----\n" + p.Root.String() }

// StepFromExpression mirrors Step.from_expression: it builds the step DAG of an expression.
// ctes maps CTE names to their steps.
func StepFromExpression(expression *exp.Expr, ctes map[string]*Step) *Step {
	expression = expression.Unnest()
	if with := expression.Get("with_"); with != nil {
		// CTEs break the mold of scope and introduce themselves to all in the context.
		next := map[string]*Step{}
		for k, v := range ctes {
			next[k] = v
		}
		ctes = next
		for _, cte := range with.Expressions() {
			step := StepFromExpression(cte.This(), ctes)
			step.Name = cte.Alias()
			ctes[step.Name] = step
		}
	}

	var step *Step
	from := expression.Get("from_")
	switch {
	case expression.IsKind(exp.Select) && from != nil:
		step = scanFromExpression(from.This(), ctes)
	case expression.Is(exp.SetOperation):
		step = setOperationFromExpression(expression, ctes)
	default:
		step = newStep(ScanStep)
	}

	if joins := expression.List("joins"); expression.Arg("joins") != nil {
		join := joinFromJoins(joins, ctes)
		join.Name = step.Name
		join.SourceName = step.Name
		join.AddDependency(step)
		step = join
	}

	// final selects in this chain of steps representing a select
	var projections []*exp.Expr
	// intermediate computations of agg funcs, e.g. x + 1 in SUM(x + 1)
	type operand struct {
		expr *exp.Expr
		name string
	}
	var operands []operand
	var aggregations []*exp.Expr
	nextOperandName := helper.NameSequence("_a_")

	stepTable := func() any {
		if step.Name == "" {
			return nil
		}
		return step.Name
	}
	quotedColumn := func(name string, table any) *exp.Expr {
		return exp.NewColumnFields(name, table, nil, nil, nil, Bool(true), true)
	}

	extractAggOperands := func(expression *exp.Expr) bool {
		var aggFuncs []*exp.Expr
		for f := range FindAllInScope(expression, exp.AggFunc) {
			aggFuncs = append(aggFuncs, f)
		}
		if len(aggFuncs) > 0 {
			found := false
			for _, a := range aggregations {
				if a.Equal(expression) {
					found = true
				}
			}
			if !found {
				aggregations = append(aggregations, expression)
			}
		}
		for _, agg := range aggFuncs {
			for _, op := range agg.UnnestOperands() {
				targets := []*exp.Expr{op}
				if op.IsKind(exp.Distinct) {
					targets = op.Expressions()
				}
				for _, target := range targets {
					if target.IsKind(exp.Column) {
						continue
					}
					name := ""
					for _, o := range operands {
						if o.expr.Equal(target) {
							name = o.name
						}
					}
					if name == "" {
						name = nextOperandName()
						operands = append(operands, operand{target.Copy(), name})
					}
					target.Replace(quotedColumn(name, nil))
				}
			}
		}
		return len(aggFuncs) > 0
	}
	setOpsAndAggs := func(s *Step) {
		s.Operands = nil
		for _, o := range operands {
			s.Operands = append(s.Operands, exp.NewAlias(o.expr, o.name))
		}
		s.Aggregations = append([]*exp.Expr(nil), aggregations...)
	}

	for _, e := range expression.Expressions() {
		if FindInScope(e, exp.AggFunc) != nil {
			projections = append(projections, quotedColumn(e.AliasOrName(), stepTable()))
			extractAggOperands(e)
		} else {
			projections = append(projections, e)
		}
	}

	if where := expression.Get("where"); where != nil {
		step.Condition = where.This()
	}

	group := expression.Get("group")
	var aggregate *Step
	if group != nil || len(aggregations) > 0 {
		aggregate = newStep(AggregateStep)
		aggregate.AggregateSource = step.Name
		aggregate.Name = step.Name

		if having := expression.Get("having"); having != nil {
			if extractAggOperands(exp.NewAlias(having.This(), "_h", exp.WithQuoted(true))) {
				aggregate.Condition = quotedColumn("_h", stepTable())
			} else {
				aggregate.Condition = having.This()
			}
		}
		setOpsAndAggs(aggregate)

		// give aggregates names and replace projections with references to them
		if group != nil {
			for i, e := range group.Expressions() {
				aggregate.Group.Set("_g"+strconv.Itoa(i), e)
			}
		}
		type entry struct {
			expr *exp.Expr
			name string
		}
		var intermediateExprs []entry
		intermediateNames := map[string]string{}
		for k, v := range aggregate.Group.All() {
			intermediateExprs = append(intermediateExprs, entry{v, k})
			if v.IsKind(exp.Column) {
				intermediateNames[v.Name()] = k
			}
		}
		lookup := func(node *exp.Expr) string {
			name := ""
			for _, e := range intermediateExprs {
				if e.expr.Equal(node) {
					name = e.name // later entries win, like dict assignment
				}
			}
			return name
		}
		for _, projection := range projections {
			for node := range projection.Walk(true, nil) {
				if name := lookup(node); name != "" {
					node.Replace(exp.NewColumn(name, stepTable()))
				}
			}
		}
		if aggregate.Condition != nil {
			for node := range aggregate.Condition.Walk(true, nil) {
				name := lookup(node)
				if name == "" {
					name = intermediateNames[node.Name()]
				}
				if name != "" {
					node.Replace(exp.NewColumn(name, stepTable()))
				}
			}
		}
		aggregate.AddDependency(step)
		step = aggregate
	}

	// Plan DISTINCT before ORDER BY, since Aggregate sorts by its own group key
	var distinct *Step
	if expression.IsKind(exp.Select) && expression.Bool("distinct") {
		distinct = newStep(AggregateStep)
		distinct.AggregateSource = step.Name
		distinct.Name = step.Name
		source := projections
		if len(source) == 0 {
			source = expression.Expressions()
		}
		for _, e := range source {
			distinct.Group.Set(e.AliasOrName(), e.Unalias())
		}
		projections = nil
		for _, name := range distinct.Group.Keys() {
			projections = append(projections, quotedColumn(name, stepTable()))
		}
		distinct.AddDependency(step)
		step = distinct
	}

	if order := expression.Get("order"); order != nil {
		if aggregate != nil {
			for i, ordered := range order.Expressions() {
				name := fmt.Sprintf("_o_%d", i)
				if extractAggOperands(exp.NewAlias(ordered.This(), name, exp.WithQuoted(true))) {
					ordered.This().Replace(quotedColumn(name, nilIfEmpty(aggregate.Name)))
				}
			}
			setOpsAndAggs(aggregate)
		}
		if distinct != nil {
			for i, ordered := range order.Expressions() {
				key := ordered.This()
				groupName := ""
				for n, e := range distinct.Group.All() {
					if e.Equal(key) {
						groupName = n
						break
					}
				}
				if groupName != "" {
					key.Replace(quotedColumn(groupName, stepTable()))
					continue
				}
				// a bare column is a reference to an output name
				if key.IsKind(exp.Column) && key.TableName() == "" && distinct.Group.Has(key.Name()) {
					continue
				}
				key = key.Copy()
				var refs []*exp.Expr
				for n := range key.Walk(true, nil) {
					if n.IsKind(exp.Column) && n.TableName() == "" && distinct.Group.Has(n.Name()) {
						refs = append(refs, n)
					}
				}
				for _, n := range refs {
					g, _ := distinct.Group.Get(n.Name())
					replacement := g.Copy()
					if n == key {
						key = replacement
					} else {
						n.Replace(replacement)
					}
				}
				// the key has no single value once DISTINCT collapses rows; follow duckdb and
				// sqlite and take an arbitrary one
				if !key.IsKind(exp.Column) {
					alias := fmt.Sprintf("_a_%d", i)
					distinct.Operands = append(distinct.Operands, exp.NewAlias(key, alias))
					key = quotedColumn(alias, nil)
				}
				distinct.Aggregations = append(distinct.Aggregations,
					exp.NewAlias(exp.New(exp.First, "this", key), fmt.Sprintf("_o_%d", i), exp.WithQuoted(true)))
				ordered.This().Replace(quotedColumn(fmt.Sprintf("_o_%d", i), stepTable()))
			}
		}
		sort := newStep(SortStep)
		sort.Name = step.Name
		sort.Key = order.Expressions()
		sort.AddDependency(step)
		step = sort
	}

	step.Projections = projections

	if limit := expression.Get("limit"); limit != nil && !limit.IsLimitAll() {
		n, err := strconv.Atoi(limit.Text("expression"))
		if err != nil {
			panic(errs.Valuef("invalid literal for int() with base 10: '%s'", limit.Text("expression")))
		}
		step.Limit = n
	}
	if offset := expression.Get("offset"); offset != nil {
		n, err := strconv.Atoi(offset.Text("expression"))
		if err != nil {
			panic(errs.Valuef("invalid literal for int() with base 10: '%s'", offset.Text("expression")))
		}
		step.Offset = n
	}
	return step
}

func scanFromExpression(expression *exp.Expr, ctes map[string]*Step) *Step {
	alias := expression.AliasOrName()
	if expression.IsKind(exp.Subquery) {
		step := StepFromExpression(expression.This(), ctes)
		step.Name = alias
		return step
	}
	step := newStep(ScanStep)
	step.Name = alias
	step.Source = expression
	if cte, ok := ctes[expression.Name()]; ok {
		step.AddDependency(cte)
	}
	return step
}

func joinFromJoins(joins []*exp.Expr, ctes map[string]*Step) *Step {
	step := newStep(JoinStep)
	for _, join := range joins {
		sourceKey, joinKey, condition := JoinCondition(join)
		step.Joins.Set(join.AliasOrName(), &JoinSpec{
			Side: join.TextUpper("side"), JoinKey: joinKey, SourceKey: sourceKey, Condition: condition,
		})
		step.AddDependency(scanFromExpression(join.This(), ctes))
	}
	return step
}

func setOperationFromExpression(expression *exp.Expr, ctes map[string]*Step) *Step {
	left := StepFromExpression(expression.This(), ctes)
	// SELECT 1 UNION SELECT 2  <-- these subqueries don't have names
	if left.Name == "" {
		left.Name = "left"
	}
	right := StepFromExpression(expression.Expression(), ctes)
	if right.Name == "" {
		right.Name = "right"
	}
	step := newStep(SetOperationStep)
	step.Op = expression.Kind()
	step.Left, step.Right = left.Name, right.Name
	step.Distinct = expression.Bool("distinct")
	step.AddDependency(left)
	step.AddDependency(right)
	return step
}

// TypeName mirrors Step.type_name.
func (s *Step) TypeName() string {
	switch s.Type {
	case ScanStep:
		return "Scan"
	case JoinStep:
		return "Join"
	case AggregateStep:
		return "Aggregate"
	case SortStep:
		return "Sort"
	}
	return s.Op.Name()
}

// ID mirrors Step.id.
func (s *Step) ID() string {
	name := ""
	if s.Name != "" {
		name = " " + s.Name
	}
	return fmt.Sprintf("%s:%s (%d)", s.TypeName(), name, s.id)
}

func (s *Step) String() string { return s.toS(0) }

func (s *Step) toS(level int) string {
	indent := strings.Repeat("  ", level)
	nested := indent + "    "
	context := s.contextLines(nested + "  ")
	lines := []string{indent + "- " + s.ID()}
	if len(context) > 0 {
		lines = append(lines, nested+"Context:")
		lines = append(lines, context...)
	}
	lines = append(lines, nested+"Projections:")
	for _, e := range s.Projections {
		lines = append(lines, nested+"  - "+e.SQL())
	}
	if s.Condition != nil {
		lines = append(lines, nested+"Condition: "+s.Condition.SQL())
	}
	if s.Limit != NoLimit {
		lines = append(lines, fmt.Sprintf("%sLimit: %d", nested, s.Limit))
	}
	if s.Offset != 0 {
		lines = append(lines, fmt.Sprintf("%sOffset: %d", nested, s.Offset))
	}
	if len(s.Dependencies) > 0 {
		lines = append(lines, nested+"Dependencies:")
		for _, d := range s.Dependencies {
			lines = append(lines, "  "+d.toS(level+1))
		}
	}
	return strings.Join(lines, "\n")
}

func (s *Step) contextLines(indent string) []string {
	var lines []string
	switch s.Type {
	case ScanStep:
		source := "-static-"
		if s.Source != nil {
			source = s.Source.SQL()
		}
		lines = append(lines, indent+"Source: "+source)
	case JoinStep:
		source := s.SourceName
		if source == "" {
			source = s.Name
		}
		lines = append(lines, indent+"Source: "+source)
		for name, join := range s.Joins.All() {
			side := join.Side
			if side == "" {
				side = "INNER"
			}
			lines = append(lines, indent+name+": "+side)
			var keys []string
			for _, k := range join.JoinKey {
				keys = append(keys, k.SQL())
			}
			if len(keys) > 0 {
				lines = append(lines, indent+"Key: "+strings.Join(keys, ", "))
			}
			if join.Condition != nil {
				lines = append(lines, indent+"On: "+join.Condition.SQL())
			}
		}
	case AggregateStep:
		lines = append(lines, indent+"Aggregations:")
		for _, e := range s.Aggregations {
			lines = append(lines, indent+"  - "+e.SQL())
		}
		if s.Group.Len() > 0 {
			lines = append(lines, indent+"Group:")
			for _, e := range s.Group.Values() {
				lines = append(lines, indent+"  - "+e.SQL())
			}
		}
		if s.Condition != nil {
			lines = append(lines, indent+"Having:", indent+"  - "+s.Condition.SQL())
		}
		if len(s.Operands) > 0 {
			lines = append(lines, indent+"Operands:")
			for _, e := range s.Operands {
				lines = append(lines, indent+"  - "+e.SQL())
			}
		}
	case SortStep:
		lines = append(lines, indent+"Key:")
		for _, e := range s.Key {
			lines = append(lines, indent+"  - "+e.SQL())
		}
	case SetOperationStep:
		if s.Distinct {
			lines = append(lines, indent+"Distinct: True")
		}
	}
	return lines
}
