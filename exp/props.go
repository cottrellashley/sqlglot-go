package exp

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Text mirrors Expression.text(key): the string value of a string arg, or the "this" of an
// Identifier/Literal/Var, or the name of a Star/Null.
func (e *Expr) Text(key string) string {
	if e == nil {
		return ""
	}
	switch v := e.Arg(key).(type) {
	case string:
		return v
	case *Expr:
		if v.IsAny(Identifier, Literal, Var) {
			if s, ok := v.Arg("this").(string); ok {
				return s
			}
			return fmt.Sprint(v.Arg("this"))
		}
		if v.IsAny(Star, Null) {
			return v.Name()
		}
	}
	return ""
}

// TextUpper returns upper(Text(key)).
func (e *Expr) TextUpper(key string) string { return strings.ToUpper(e.Text(key)) }

// Name mirrors the polymorphic Expression.name property.
func (e *Expr) Name() string {
	if e == nil {
		return ""
	}
	switch implName[e.kind] {
	case Star:
		return "*"
	case Null:
		return "NULL"
	case Placeholder:
		if s := e.Text("this"); s != "" {
			return s
		}
		return "?"
	case Dot:
		return e.Expression().Name()
	case Anonymous:
		if s, ok := e.Arg("this").(string); ok {
			return s
		}
		return e.This().Name()
	case DynamicIdentifier:
		if t := e.This(); t != nil {
			return t.Name()
		}
		return ""
	case Table:
		this := e.This()
		if this == nil || (this.Is(Func) && !this.Is(DynamicIdentifier)) {
			return ""
		}
		return this.Name()
	case Ordered, From, Execute, Cast, DataTypeParam:
		return e.This().Name()
	}
	return e.Text("this")
}

// ThisStr returns the "this" argument when it is a string (e.g. Identifier, Literal, Var).
func (e *Expr) ThisStr() string { return e.Str("this") }

// Alias mirrors Expression.alias: the alias name or "".
func (e *Expr) Alias() string {
	if e == nil {
		return ""
	}
	if a, ok := e.Arg("alias").(*Expr); ok {
		return a.Name()
	}
	return e.Text("alias")
}

// AliasOrName mirrors Expression.alias_or_name.
func (e *Expr) AliasOrName() string {
	if e == nil {
		return ""
	}
	switch implAliasOrName[e.kind] {
	case From, Join:
		return e.This().AliasOrName()
	}
	if a := e.Alias(); a != "" {
		return a
	}
	return e.Name()
}

// AliasColumnNames mirrors Expression.alias_column_names.
func (e *Expr) AliasColumnNames() []string {
	ta := e.Get("alias")
	if ta == nil {
		return nil
	}
	var out []string
	for _, c := range ta.List("columns") {
		out = append(out, c.Name())
	}
	return out
}

// OutputName mirrors Expression.output_name: the output column name of a projection.
func (e *Expr) OutputName() string {
	if e == nil {
		return ""
	}
	switch implOutputName[e.kind] {
	case Column, Literal, Identifier, Star, Dot, Cast, TableColumn:
		return e.Name()
	case Alias:
		return e.Alias()
	case Subquery:
		return e.Alias()
	case Paren, Agg:
		return e.This().Name()
	case Bracket:
		if exprs := e.Expressions(); len(exprs) == 1 {
			return exprs[0].OutputName()
		}
		return ""
	case JSONExtract:
		if len(e.Expressions()) == 0 {
			return e.Expression().OutputName()
		}
		return ""
	case JSONExtractScalar:
		return e.Expression().OutputName()
	case JSONPath:
		exprs := e.Expressions()
		if len(exprs) == 0 {
			return ""
		}
		if s, ok := exprs[len(exprs)-1].Arg("this").(string); ok {
			return s
		}
		return ""
	}
	return ""
}

// IsString reports whether the node is a string Literal.
func (e *Expr) IsString() bool {
	return e != nil && e.Is(Literal) && e.Bool("is_string")
}

// IsNumber reports whether the node is a numeric literal (or a negated one).
func (e *Expr) IsNumber() bool {
	if e == nil {
		return false
	}
	return (e.Is(Literal) && !e.Bool("is_string")) || (e.Is(Neg) && e.This().IsNumber())
}

// IsInt reports whether the node is an integer literal.
func (e *Expr) IsInt() bool {
	if !e.IsNumber() {
		return false
	}
	v, err := e.ToPy()
	if err != nil {
		return false
	}
	switch v.(type) {
	case int, *big.Int:
		return true
	}
	return false
}

// IsStar mirrors Expression.is_star.
func (e *Expr) IsStar() bool {
	if e == nil {
		return false
	}
	switch implIsStar[e.kind] {
	case Dot:
		return e.Expression().IsStar()
	case Select:
		for _, x := range e.Expressions() {
			if x.IsStar() {
				return true
			}
		}
		return false
	case SetOperation, Subquery:
		return isStarQuery(e)
	}
	return e.Is(Star) || (e.Is(Column) && e.This().Is(Star))
}

func isStarQuery(e *Expr) bool {
	stack := []*Expr{e}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if node == nil {
			continue
		}
		if node.Is(SetOperation) {
			stack = append(stack, node.This(), node.Expression())
		} else if node.Is(Subquery) {
			stack = append(stack, node.This())
		} else if node.IsStar() {
			return true
		}
	}
	return false
}

// Decimal is the Go counterpart of Python's decimal.Decimal returned by Literal.to_py for
// non-integer numbers. Text keeps the literal text.
type Decimal struct {
	Text string
	Rat  *big.Rat
}

func (d Decimal) String() string { return d.Text }

// Float returns the float64 value.
func (d Decimal) Float() float64 { f, _ := d.Rat.Float64(); return f }

// ToPy mirrors Expression.to_py: the Go equivalent of the literal value. Integers are returned
// as int (or *big.Int when they overflow), other numbers as Decimal, strings as string,
// booleans as bool and NULL as nil.
func (e *Expr) ToPy() (any, error) {
	switch implToPy[e.kind] {
	case Literal:
		this := e.Str("this")
		if e.IsNumber() {
			if v, err := pyInt(this); err == nil {
				return v, nil
			}
			if r, ok := pyDecimal(this); ok {
				return Decimal{Text: this, Rat: r}, nil
			}
			return nil, fmt.Errorf("Invalid numeric literal: '%s'", this)
		}
		return this, nil
	case Neg:
		if e.IsNumber() {
			v, err := e.This().ToPy()
			if err != nil {
				return nil, err
			}
			switch x := v.(type) {
			case int:
				return -x, nil
			case *big.Int:
				return new(big.Int).Neg(x), nil
			case Decimal:
				return Decimal{Text: "-" + x.Text, Rat: new(big.Rat).Neg(x.Rat)}, nil
			}
		}
	case Null:
		return nil, nil
	case Boolean:
		b, _ := e.Arg("this").(bool)
		return b, nil
	}
	return nil, fmt.Errorf("%s cannot be converted to a Python object.", e.SQL())
}

// MustToPy is ToPy that panics on error.
func (e *Expr) MustToPy() any {
	v, err := e.ToPy()
	if err != nil {
		panic(err)
	}
	return v
}

func pyInt(s string) (any, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil, fmt.Errorf("invalid")
	}
	clean := t
	if strings.Contains(clean, "_") {
		// Python allows single underscores between digits
		if strings.HasPrefix(clean, "_") || strings.HasSuffix(clean, "_") || strings.Contains(clean, "__") {
			return nil, fmt.Errorf("invalid")
		}
		clean = strings.ReplaceAll(clean, "_", "")
	}
	for i, r := range clean {
		if (r == '+' || r == '-') && i == 0 {
			continue
		}
		if r < '0' || r > '9' {
			return nil, fmt.Errorf("invalid")
		}
	}
	if v, err := strconv.ParseInt(clean, 10, 64); err == nil {
		return int(v), nil
	}
	b, ok := new(big.Int).SetString(clean, 10)
	if !ok {
		return nil, fmt.Errorf("invalid")
	}
	return b, nil
}

func pyDecimal(s string) (*big.Rat, bool) {
	t := strings.TrimSpace(strings.ReplaceAll(s, "_", ""))
	if t == "" {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(t)
	return r, ok
}

// Parts mirrors Column.parts / Table.parts / Dot.parts.
func (e *Expr) Parts() []*Expr {
	switch implParts[e.kind] {
	case Column:
		var out []*Expr
		for _, p := range []string{"catalog", "db", "table", "this"} {
			if v := e.Get(p); v != nil {
				out = append(out, v)
			}
		}
		return out
	case Table:
		var out []*Expr
		for _, p := range []string{"catalog", "db", "this"} {
			part := e.Get(p)
			if part == nil {
				continue
			}
			if part.Is(Dot) {
				out = append(out, part.Flatten(true)...)
			} else {
				out = append(out, part)
			}
		}
		return out
	case Dot:
		flat := e.Flatten(true)
		if len(flat) == 0 {
			return nil
		}
		this := flat[0]
		parts := append([]*Expr(nil), flat[1:]...)
		reverse(parts)
		for _, arg := range ColumnParts {
			if part := this.Get(arg); part != nil {
				parts = append(parts, part)
			}
		}
		reverse(parts)
		return parts
	}
	return nil
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// TableName returns Column.table / Table... the "table" text of a Column.
func (e *Expr) TableName() string { return e.Text("table") }

// DB returns the "db" text (Column.db / Table.db).
func (e *Expr) DB() string { return e.Text("db") }

// Catalog returns the "catalog" text (Column.catalog / Table.catalog).
func (e *Expr) Catalog() string { return e.Text("catalog") }

// Quoted mirrors Identifier.quoted.
func (e *Expr) Quoted() bool { return e.Bool("quoted") }

// Left mirrors Binary.left / SetOperation.left.
func (e *Expr) Left() *Expr { return e.This() }

// Right mirrors Binary.right / SetOperation.right.
func (e *Expr) Right() *Expr { return e.Expression() }

// Unit returns the "unit" argument (TimeUnit.unit / DateTrunc.unit).
func (e *Expr) Unit() *Expr { return e.Get("unit") }

// To returns Cast.to.
func (e *Expr) To() *Expr { return e.Get("to") }

// Ctes mirrors Query.ctes / DDL.ctes.
func (e *Expr) Ctes() []*Expr {
	if w := e.Get("with_"); w != nil {
		return w.Expressions()
	}
	return nil
}

// Selects mirrors Selectable.selects for every kind.
func (e *Expr) Selects() []*Expr {
	if e == nil {
		return nil
	}
	switch implSelects[e.kind] {
	case Select:
		return e.Expressions()
	case Table:
		return nil
	case DerivedTable:
		if this := e.This(); this.Is(Query) {
			return this.Selects()
		}
		return nil
	case UDTF:
		return udtfSelects(e)
	case Lateral:
		cols := udtfSelects(e)
		if this := e.This(); this.Is(Unnest) {
			if off := this.Get("offset"); off.Is(Identifier) {
				cols = append(append([]*Expr(nil), cols...), off)
			}
		}
		return cols
	case Unnest:
		cols := udtfSelects(e)
		switch off := e.Arg("offset").(type) {
		case bool:
			if off {
				cols = append(append([]*Expr(nil), cols...), ToIdentifier("offset"))
			}
		case *Expr:
			cols = append(append([]*Expr(nil), cols...), off)
		}
		return cols
	case SetOperation:
		x := e
		for x.Is(SetOperation) {
			x = x.This().Unnest()
		}
		return x.Selects()
	case DDL:
		if x := e.Expression(); x.Is(Query) {
			return x.Selects()
		}
		return nil
	}
	return nil
}

func udtfSelects(e *Expr) []*Expr {
	if a := e.Get("alias"); a != nil {
		return a.List("columns")
	}
	return nil
}

// NamedSelects mirrors Selectable.named_selects.
func (e *Expr) NamedSelects() []string {
	if e == nil {
		return nil
	}
	switch implNamedSelects[e.kind] {
	case Select:
		var out []string
		for _, x := range e.Expressions() {
			if x.AliasOrName() != "" {
				out = append(out, x.OutputName())
			} else if x.Is(Aliases) {
				for _, a := range x.Expressions() {
					out = append(out, a.Name())
				}
			}
		}
		return out
	case Table:
		return nil
	case SetOperation:
		x := e
		for x.Is(SetOperation) {
			if x.Bool("by_name") {
				left := x.This().Unnest().NamedSelects()
				right := x.Expression().Unnest().NamedSelects()
				seen := map[string]bool{}
				var out []string
				for _, n := range append(append([]string(nil), left...), right...) {
					if !seen[n] {
						seen[n] = true
						out = append(out, n)
					}
				}
				return out
			}
			x = x.This().Unnest()
		}
		return x.NamedSelects()
	case DDL:
		if x := e.Expression(); x.Is(Query) {
			return x.NamedSelects()
		}
		return nil
	}
	var out []string
	for _, s := range e.Selects() {
		out = append(out, s.OutputName())
	}
	return out
}

// IsWrapper mirrors Subquery.is_wrapper: all args except "this" are None.
func (e *Expr) IsWrapper() bool {
	for _, a := range e.args {
		if a.Key != "this" && a.Value != nil {
			return false
		}
	}
	return true
}

// Unwrap mirrors Subquery.unwrap.
func (e *Expr) Unwrap() *Expr {
	x := e
	for x.SameParent() && x.IsWrapper() {
		x = x.parent
	}
	return x
}

// IsLimitAll mirrors Limit.is_limit_all.
func (e *Expr) IsLimitAll() bool {
	le := e.Get("expression")
	return le.Is(Var) && strings.ToUpper(le.Name()) == "ALL"
}

// IsSemiOrAntiJoin mirrors Join.is_semi_or_anti_join.
func (e *Expr) IsSemiOrAntiJoin() bool {
	k := e.TextUpper("kind")
	return k == "SEMI" || k == "ANTI"
}

// Keys mirrors Map.keys / VarMap.keys.
func (e *Expr) Keys() []*Expr {
	if k := e.Get("keys"); k != nil {
		return k.Expressions()
	}
	return nil
}

// Values mirrors Map.values / VarMap.values.
func (e *Expr) Values() []*Expr {
	if v := e.Get("values"); v != nil {
		return v.Expressions()
	}
	return nil
}

// Columns mirrors TableAlias.columns.
func (e *Expr) Columns() []*Expr { return e.List("columns") }

// Constraints mirrors ColumnDef.constraints.
func (e *Expr) Constraints() []*Expr { return e.List("constraints") }

// Recursive mirrors With.recursive.
func (e *Expr) Recursive() bool { return e.Bool("recursive") }

// Actions mirrors Alter.actions.
func (e *Expr) Actions() []*Expr { return e.List("actions") }

// Unpivot mirrors Pivot.unpivot.
func (e *Expr) Unpivot() bool { return e.Bool("unpivot") }

// Fields mirrors Pivot.fields.
func (e *Expr) Fields() []*Expr { return e.List("fields") }

// ReturnType mirrors TsOrDsAdd.return_type.
func (e *Expr) ReturnType() *Expr {
	if rt := e.Get("return_type"); rt != nil {
		return MustDataTypeBuild(rt, nil, false)
	}
	return MustDataTypeBuild(DTypeDate, nil, false)
}

// Interval mirrors IntervalOp.interval().
func (e *Expr) Interval() *Expr {
	var this, unit *Expr
	if x := e.Expression(); x != nil {
		this = x.Copy()
	}
	if u := e.Unit(); u != nil {
		unit = u.Copy()
	}
	return New(Interval, "this", this, "unit", unit)
}

// IsNegative reports whether a numeric literal is negative (Neg wrapper).
func (e *Expr) IsNegative() bool { return e.Is(Neg) }

// ToDot mirrors Column.to_dot.
func (e *Expr) ToDot(includeDots bool) *Expr {
	parts := e.Parts()
	parent := e.parent
	if includeDots {
		for parent.Is(Dot) {
			parts = append(parts, parent.Expression())
			parent = parent.parent
		}
	}
	if len(parts) > 1 {
		cp := make([]*Expr, len(parts))
		for i, p := range parts {
			cp[i] = p.Copy()
		}
		return DotBuild(cp)
	}
	return parts[0]
}

// ToColumn mirrors Table.to_column.
func (e *Expr) ToColumnExpr(copy bool) *Expr {
	parts := e.Parts()
	last := parts[len(parts)-1]
	var col *Expr
	if last.Is(Identifier) {
		head := parts
		if len(head) > 4 {
			head = parts[:4]
		}
		rev := append([]*Expr(nil), head...)
		reverse(rev)
		var fields []any
		if len(parts) > 4 {
			for _, p := range parts[4:] {
				fields = append(fields, p)
			}
		}
		args := make([]any, 4)
		for i := range args {
			if i < len(rev) {
				args[i] = rev[i]
			}
		}
		col = NewColumnFields(args[0], args[1], args[2], args[3], fields, nil, copy)
	} else {
		col = last
	}
	if alias := e.Get("alias"); alias != nil {
		col = NewAlias(col, alias.This(), WithCopy(copy))
	}
	return col
}

// PivotOutputColumns mirrors Pivot.output_columns: the ordered post-rename output column names
// (dict keys) and the mapping from each of them to its pre-rename source-side name.
func (e *Expr) PivotOutputColumns(prePivotColumns []string) ([]string, map[string]string) {
	excluded := map[string]bool{}
	var outputs []string
	if e.Unpivot() {
		var nameColumns []*Expr
		for _, field := range e.Fields() {
			if !field.Is(In) {
				continue
			}
			if field.This().Is(Identifier) {
				nameColumns = append(nameColumns, field.This())
			}
			for _, x := range field.Expressions() {
				for c := range x.FindAll(Column) {
					excluded[c.OutputName()] = true
				}
			}
		}
		var valueColumns []*Expr
		for _, x := range e.Expressions() {
			idents := []*Expr{x}
			if x.Is(Tuple) {
				idents = x.Expressions()
			}
			for _, ident := range idents {
				if ident.Is(Identifier) {
					valueColumns = append(valueColumns, ident)
				}
			}
		}
		var ordered []*Expr
		if e.Bool("value_columns_first") {
			ordered = append(append(ordered, valueColumns...), nameColumns...)
		} else {
			ordered = append(append(ordered, nameColumns...), valueColumns...)
		}
		for _, i := range ordered {
			outputs = append(outputs, i.Name())
		}
	} else {
		for c := range e.FindAll(Column) {
			excluded[c.OutputName()] = true
		}
		for _, c := range e.List("columns") {
			outputs = append(outputs, c.OutputName())
		}
		if len(outputs) == 0 {
			for _, c := range e.Expressions() {
				outputs = append(outputs, c.AliasOrName())
			}
		}
	}
	if len(excluded) == 0 || len(outputs) == 0 {
		return nil, map[string]string{}
	}
	var preRename []string
	for _, c := range prePivotColumns {
		if !excluded[c] {
			preRename = append(preRename, c)
		}
	}
	preRename = append(preRename, outputs...)
	postRename := preRename
	if alias := e.Get("alias"); alias != nil {
		if renames := alias.List("columns"); len(renames) > 0 {
			var names []string
			for _, r := range renames {
				names = append(names, r.Name())
			}
			if len(names) < len(preRename) {
				postRename = append(names, preRename[len(names):]...)
			} else {
				postRename = names
			}
		}
	}
	out := map[string]string{}
	var names []string
	for i := 0; i < len(postRename) && i < len(preRename); i++ {
		if _, ok := out[postRename[i]]; !ok {
			names = append(names, postRename[i])
		}
		out[postRename[i]] = preRename[i]
	}
	return names, out
}
