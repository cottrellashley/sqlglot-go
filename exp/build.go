package exp

import (
	"encoding/hex"
	"github.com/cottrellashley/sqlglot-go/internal/pydec"

	"github.com/cottrellashley/sqlglot-go/errs"

	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SafeIdentifierRe mirrors SAFE_IDENTIFIER_RE.
var SafeIdentifierRe = regexp.MustCompile(`^[_a-zA-Z][\w]*$`)

// LiteralString mirrors Literal.string(value).
func LiteralString(v any) *Expr {
	return New(Literal, "this", fmt.Sprint(v), "is_string", true)
}

// LiteralNumber mirrors Literal.number(value): negative numbers become Neg(Literal).
func LiteralNumber(v any) *Expr {
	lit := New(Literal, "this", numberString(v), "is_string", false)
	if p, err := lit.ToPy(); err == nil {
		switch x := p.(type) {
		case int:
			if x < 0 {
				lit.Set("this", strconv.Itoa(-x))
				return New(Neg, "this", lit)
			}
		case *big.Int:
			if x.Sign() < 0 {
				lit.Set("this", new(big.Int).Neg(x).String())
				return New(Neg, "this", lit)
			}
		case Decimal:
			if x.Rat.Sign() < 0 {
				// Python stores str(-to_py), i.e. the Decimal formatting of the absolute value
				text := strings.TrimPrefix(x.Text, "-")
				if d, ok := pydec.Parse(x.Text); ok {
					text = d.Negate().String()
				}
				lit.Set("this", text)
				return New(Neg, "this", lit)
			}
		}
	}
	return lit
}

func numberString(v any) string {
	switch x := v.(type) {
	case int:
		return strconv.Itoa(x)
	case int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(x)
	case float64:
		return PyFloatRepr(x)
	case float32:
		return PyFloatRepr(float64(x))
	case *big.Int:
		return x.String()
	case Decimal:
		return x.Text
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// PyFloatRepr formats a float like Python's repr(float).
func PyFloatRepr(f float64) string {
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	if math.IsNaN(f) {
		return "nan"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // d.ddddde±XX
	mant, expStr, _ := strings.Cut(s, "e")
	exp10, _ := strconv.Atoi(expStr)
	neg := strings.HasPrefix(mant, "-")
	mant = strings.TrimPrefix(mant, "-")
	digits := strings.Replace(mant, ".", "", 1)
	var out string
	if exp10 >= -4 && exp10 < 16 {
		// fixed notation
		if exp10 >= 0 {
			if len(digits) <= exp10+1 {
				out = digits + strings.Repeat("0", exp10+1-len(digits)) + ".0"
			} else {
				out = digits[:exp10+1] + "." + digits[exp10+1:]
			}
		} else {
			out = "0." + strings.Repeat("0", -exp10-1) + digits
		}
	}
	if out == "" {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		sign := "+"
		if exp10 < 0 {
			sign = "-"
			exp10 = -exp10
		}
		out = fmt.Sprintf("%se%s%02d", m, sign, exp10)
	}
	if neg {
		out = "-" + out
	}
	return out
}

// NewTrue mirrors exp.true().
func NewTrue() *Expr { return New(Boolean, "this", true) }

// NewFalse mirrors exp.false().
func NewFalse() *Expr { return New(Boolean, "this", false) }

// NewNull mirrors exp.null().
func NewNull() *Expr { return New(Null) }

// NewBoolean returns a Boolean literal.
func NewBoolean(b bool) *Expr { return New(Boolean, "this", b) }

// NewVar mirrors exp.var(name).
func NewVar(name any) *Expr {
	switch x := name.(type) {
	case *Expr:
		return New(Var, "this", x.Name())
	case string:
		if x == "" {
			panic("Cannot convert empty name into var.")
		}
		return New(Var, "this", x)
	case nil:
		panic("Cannot convert empty name into var.")
	}
	return New(Var, "this", fmt.Sprint(name))
}

// DateValue is a calendar date for ConvertValue (Python datetime.date).
type DateValue struct{ Year, Month, Day int }

// TimeValue is a time of day for ConvertValue (Python datetime.time).
type TimeValue struct{ Hour, Minute, Second, Microsecond int }

// TupleValue converts to a Tuple expression (Python tuple).
type TupleValue []any

// KV is an ordered key/value pair used for ordered maps and structs in ConvertValue.
type KV struct {
	Key   any
	Value any
}

// OrderedMap converts to a Map expression preserving key order (Python dict).
type OrderedMap []KV

// StructValue converts to a Struct expression (Python namedtuple / object __dict__).
type StructValue []KV

// ConvertValue mirrors exp.convert: it converts a Go value into an expression.
func ConvertValue(value any, copy bool) *Expr {
	switch v := value.(type) {
	case *Expr:
		if v == nil {
			return NewNull()
		}
		return MaybeCopy(v, copy)
	case string:
		return LiteralString(v)
	case bool:
		return NewBoolean(v)
	case nil:
		return NewNull()
	case float64:
		if math.IsNaN(v) {
			return NewNull()
		}
		return LiteralNumber(v)
	case float32:
		if math.IsNaN(float64(v)) {
			return NewNull()
		}
		return LiteralNumber(v)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, *big.Int, Decimal:
		return LiteralNumber(v)
	case []byte:
		return New(HexString, "this", hex.EncodeToString(v))
	case time.Time:
		lit := LiteralString(pyIsoformat(v, true))
		return New(TimeStrToTime, "this", lit, "zone", LiteralString(v.Location().String()))
	case NaiveDatetime:
		return New(TimeStrToTime, "this", LiteralString(pyIsoformat(v.Time, false)), "zone", nil)
	case DateValue:
		return New(DateStrToDate, "this", LiteralString(fmt.Sprintf("%04d-%02d-%02d", v.Year, v.Month, v.Day)))
	case TimeValue:
		s := fmt.Sprintf("%02d:%02d:%02d", v.Hour, v.Minute, v.Second)
		if v.Microsecond != 0 {
			s += fmt.Sprintf(".%06d", v.Microsecond)
		}
		return New(TsOrDsToTime, "this", LiteralString(s))
	case TupleValue:
		exprs := make([]*Expr, len(v))
		for i, x := range v {
			exprs[i] = ConvertValue(x, copy)
		}
		return New(Tuple, "expressions", exprs)
	case StructValue:
		exprs := make([]*Expr, len(v))
		for i, kv := range v {
			exprs[i] = New(PropertyEQ, "this", ToIdentifier(kv.Key), "expression", ConvertValue(kv.Value, copy))
		}
		return New(Struct, "expressions", exprs)
	case OrderedMap:
		keys := make([]*Expr, len(v))
		vals := make([]*Expr, len(v))
		for i, kv := range v {
			keys[i] = ConvertValue(kv.Key, copy)
			vals[i] = ConvertValue(kv.Value, copy)
		}
		return New(Map, "keys", New(Array, "expressions", keys), "values", New(Array, "expressions", vals))
	case []any:
		exprs := make([]*Expr, len(v))
		for i, x := range v {
			exprs[i] = ConvertValue(x, copy)
		}
		return New(Array, "expressions", exprs)
	case []*Expr:
		exprs := make([]*Expr, len(v))
		for i, x := range v {
			exprs[i] = ConvertValue(x, copy)
		}
		return New(Array, "expressions", exprs)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		om := make(OrderedMap, len(keys))
		for i, k := range keys {
			om[i] = KV{k, v[k]}
		}
		return ConvertValue(om, copy)
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		exprs := make([]*Expr, rv.Len())
		for i := range exprs {
			exprs[i] = ConvertValue(rv.Index(i).Interface(), copy)
		}
		return New(Array, "expressions", exprs)
	case reflect.Struct:
		var sv StructValue
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).IsExported() {
				sv = append(sv, KV{t.Field(i).Name, rv.Field(i).Interface()})
			}
		}
		return ConvertValue(sv, copy)
	case reflect.Pointer:
		if rv.IsNil() {
			return NewNull()
		}
		return ConvertValue(rv.Elem().Interface(), copy)
	}
	panic(fmt.Sprintf("Cannot convert %v", value))
}

// NaiveDatetime is a datetime without time zone for ConvertValue (time.Time values are
// treated as time zone aware).
type NaiveDatetime struct{ time.Time }

func pyIsoformat(t time.Time, aware bool) string {
	s := t.Format("2006-01-02 15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	if aware {
		_, off := t.Zone()
		sign := "+"
		if off < 0 {
			sign = "-"
			off = -off
		}
		s += fmt.Sprintf("%s%02d:%02d", sign, off/3600, (off%3600)/60)
	}
	return s
}

// ToIdentifier mirrors exp.to_identifier(name) with automatic quoting.
func ToIdentifier(name any) *Expr { return ToIdentifierOpts(name, nil, true) }

// ToIdentifierQuoted mirrors exp.to_identifier(name, quoted=quoted).
func ToIdentifierQuoted(name any, quoted bool) *Expr { return ToIdentifierOpts(name, &quoted, true) }

// ToIdentifierOpts mirrors exp.to_identifier(name, quoted, copy).
func ToIdentifierOpts(name any, quoted *bool, copy bool) *Expr {
	switch x := name.(type) {
	case nil:
		return nil
	case *Expr:
		if x == nil {
			return nil
		}
		if x.Is(Identifier) {
			return MaybeCopy(x, copy)
		}
	case string:
		q := !SafeIdentifierRe.MatchString(x)
		if quoted != nil {
			q = *quoted
		}
		return New(Identifier, "this", x, "quoted", q)
	}
	panic(fmt.Sprintf("Name needs to be a string or an Identifier, got: %T", name))
}

// DotBuild mirrors Dot.build: Dot(Dot(a, b), c) from [a, b, c].
func DotBuild(exprs []*Expr) *Expr {
	if len(exprs) < 2 {
		panic("Dot requires >= 2 expressions.")
	}
	x := exprs[0]
	for _, y := range exprs[1:] {
		x = New(Dot, "this", x, "expression", y)
	}
	return x
}

// NewColumn mirrors exp.column(col, table, db, catalog). parts are table, db, catalog
// (strings, Identifiers or nil).
func NewColumn(col any, parts ...any) *Expr {
	var table, db, catalog any
	if len(parts) > 0 {
		table = parts[0]
	}
	if len(parts) > 1 {
		db = parts[1]
	}
	if len(parts) > 2 {
		catalog = parts[2]
	}
	return NewColumnFields(col, table, db, catalog, nil, nil, true)
}

// NewColumnFields mirrors exp.column with fields/quoted/copy.
func NewColumnFields(col, table, db, catalog any, fields []any, quoted *bool, copy bool) *Expr {
	var c *Expr
	if x, ok := col.(*Expr); ok && x != nil && !x.Is(Identifier) {
		c = MaybeCopy(x, copy)
	} else if ok && x != nil {
		c = MaybeCopy(x, copy)
	} else {
		c = ToIdentifierOpts(col, quoted, true)
	}
	this := New(Column,
		"this", c,
		"table", ToIdentifierOpts(table, quoted, copy),
		"db", ToIdentifierOpts(db, quoted, copy),
		"catalog", ToIdentifierOpts(catalog, quoted, copy),
	)
	if len(fields) > 0 {
		exprs := []*Expr{this}
		for _, f := range fields {
			exprs = append(exprs, ToIdentifierOpts(f, quoted, copy))
		}
		return DotBuild(exprs)
	}
	return this
}

// NewTable mirrors exp.table_(table, db, catalog).
func NewTable(table any, parts ...any) *Expr {
	var db, catalog any
	if len(parts) > 0 {
		db = parts[0]
	}
	if len(parts) > 1 {
		catalog = parts[1]
	}
	return NewTableOpts(table, db, catalog, nil, nil)
}

// NewTableOpts mirrors exp.table_(table, db, catalog, quoted, alias).
func NewTableOpts(table, db, catalog any, quoted *bool, alias any) *Expr {
	ident := func(v any) *Expr {
		if !truthyName(v) {
			return nil
		}
		return ToIdentifierOpts(v, quoted, true)
	}
	var ta *Expr
	if truthyName(alias) {
		ta = New(TableAlias, "this", ToIdentifier(alias))
	}
	return New(Table, "this", ident(table), "db", ident(db), "catalog", ident(catalog), "alias", ta)
}

func truthyName(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case *Expr:
		return x != nil
	}
	return true
}

// NewAlias mirrors exp.alias_(expression, alias, ...). Supported options: WithDialect,
// WithCopy, WithQuoted.
func NewAlias(expression any, alias any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	return aliasImpl(expression, alias, nil, o)
}

// NewTableAliasOf mirrors exp.alias_(expression, alias, table=True|columns).
func NewTableAliasOf(expression any, alias any, columns []any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	if columns == nil {
		columns = []any{}
	}
	return aliasImpl(expression, alias, columns, o)
}

func toAnys[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func aliasImpl(expression any, alias any, tableCols []any, o buildOpts) *Expr {
	e := o.parse(expression, "")
	var id *Expr
	if alias != nil {
		id = ToIdentifierOpts(alias, o.quoted, true)
	}
	if tableCols != nil {
		ta := New(TableAlias, "this", id)
		e.Set("alias", ta)
		for _, c := range tableCols {
			ta.Append("columns", ToIdentifierOpts(c, o.quoted, true))
		}
		return e
	}
	if e.kind.HasArg("alias") && e.kind != Window {
		e.Set("alias", id)
		return e
	}
	return New(Alias, "this", e, "alias", id)
}

// wrap mirrors _wrap(expression, kind).
func wrap(e *Expr, kind Kind) *Expr {
	if e.Is(kind) {
		return New(Paren, "this", e)
	}
	return e
}

// Wrap mirrors exp._wrap: parenthesize e if it is an instance of kind.
func Wrap(e *Expr, kind Kind) *Expr {
	if e == nil {
		return nil
	}
	return wrap(e, kind)
}

// NewCondition mirrors exp.condition(expression, dialect, copy).
func NewCondition(expression any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	return o.parse(expression, "", Condition)
}

func combine(exprs []any, op Kind, o buildOpts) *Expr {
	var conds []*Expr
	for _, x := range exprs {
		if x == nil {
			continue
		}
		if e, ok := x.(*Expr); ok && e == nil {
			continue
		}
		conds = append(conds, o.parse(x, "", Condition))
	}
	if len(conds) == 0 {
		return nil
	}
	this := conds[0]
	rest := conds[1:]
	if len(rest) > 0 && o.wrap {
		this = wrap(this, Connector)
	}
	for _, x := range rest {
		if o.wrap {
			x = wrap(x, Connector)
		}
		this = New(op, "this", this, "expression", x)
	}
	return this
}

// NewAnd mirrors exp.and_(*expressions). Arguments may be expressions, strings and Options.
func NewAnd(args ...any) *Expr {
	pos, o := splitArgs(args)
	return combine(pos, And, o)
}

// NewOr mirrors exp.or_(*expressions).
func NewOr(args ...any) *Expr {
	pos, o := splitArgs(args)
	return combine(pos, Or, o)
}

// NewXor mirrors exp.xor(*expressions).
func NewXor(args ...any) *Expr {
	pos, o := splitArgs(args)
	return combine(pos, Xor, o)
}

// NewNot mirrors exp.not_(expression).
func NewNot(expression any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	this := o.parse(expression, "", Condition)
	return New(Not, "this", wrap(this, Connector))
}

// NewParen mirrors exp.paren(expression, copy).
func NewParen(expression any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	return New(Paren, "this", o.parse(expression, ""))
}

// NewCase mirrors exp.case(expression).
func NewCase(expression any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	var this *Expr
	if expression != nil {
		this = o.parse(expression, "")
	}
	return New(Case, "this", this, "ifs", []*Expr{})
}

// When mirrors Case.when(condition, then).
func (e *Expr) When(condition, then any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	inst := MaybeCopy(e, o.copy)
	inst.Append("ifs", New(If, "this", o.parse(condition, ""), "true", o.parse(then, "")))
	return inst
}

// Else mirrors Case.else_(condition).
func (e *Expr) Else(condition any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	inst := MaybeCopy(e, o.copy)
	inst.Set("default", o.parse(condition, ""))
	return inst
}

// NewArray mirrors exp.array(*expressions).
func NewArray(args ...any) *Expr {
	pos, o := splitArgs(args)
	exprs := make([]*Expr, 0, len(pos))
	for _, x := range pos {
		exprs = append(exprs, o.parse(x, ""))
	}
	return New(Array, "expressions", exprs)
}

// NewTuple mirrors exp.tuple_(*expressions).
func NewTuple(args ...any) *Expr {
	pos, o := splitArgs(args)
	exprs := make([]*Expr, 0, len(pos))
	for _, x := range pos {
		exprs = append(exprs, o.parse(x, ""))
	}
	return New(Tuple, "expressions", exprs)
}

// NewFunc mirrors exp.func(name, *args, dialect=...). Arguments may be expressions or SQL
// strings; options (WithDialect, WithCopy, WithArg for kwargs) may be mixed in.
func NewFunc(name string, args ...any) *Expr {
	pos, o := splitArgs(args)
	var converted []*Expr
	for _, a := range pos {
		converted = append(converted, MustMaybeParse(a, ParseOptions{Dialect: o.dialect, ErrorLevel: errs.Unset}, "", o.copy))
	}
	var kwargs []Arg
	if len(o.extra) > 0 {
		if len(converted) > 0 {
			panic("Can't use both args and kwargs to instantiate a function.")
		}
		keys := make([]string, 0, len(o.extra))
		for k := range o.extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			kwargs = append(kwargs, Arg{k, MustMaybeParse(o.extra[k], ParseOptions{Dialect: o.dialect, ErrorLevel: errs.Unset}, "", o.copy)})
		}
	}
	f, err := mustBackend().BuildFunc(name, converted, kwargs, o.dialect)
	if err != nil {
		panic(err)
	}
	return f
}

// NewCast mirrors exp.cast(expression, to). It does not re-cast when the expression is
// already a cast to an equivalent type.
func NewCast(expression any, to any, opts ...Option) *Expr {
	_, o := splitArgs(toAnys(opts))
	expr := o.parse(expression, "")
	var dataType *Expr
	if d, ok := to.(*Expr); ok && d != nil && d.Is(DataType) && !o.copy {
		dataType = d
	} else {
		dataType = MustDataTypeBuild(to, o.dialect, false)
	}
	if expr.Is(Cast) {
		tm := mustBackend().TypeMapping(o.dialect)
		existing, ok1 := expr.To().Arg("this").(DType)
		newT, ok2 := dataType.Arg("this").(DType)
		equivalent := false
		if ok1 && ok2 {
			a, ok := tm[existing]
			if !ok {
				a = string(existing)
			}
			b, ok := tm[newT]
			if !ok {
				b = string(newT)
			}
			equivalent = a == b
		}
		if expr.IsType(dataType) || equivalent {
			return expr
		}
	}
	c := New(Cast, "this", expr, "to", dataType)
	c.SetType(dataType)
	return c
}

// ParseIdentifier mirrors exp.parse_identifier(name, dialect).
func ParseIdentifier(name any, dialect any) *Expr {
	if s, ok := name.(string); ok && SafeIdentifierRe.MatchString(s) {
		return New(Identifier, "this", s, "quoted", false)
	}
	if e, ok := name.(*Expr); ok {
		return e
	}
	e, err := MaybeParse(name, ParseOptions{Dialect: dialect, Into: []Kind{Identifier}, ErrorLevel: errs.Unset}, "", false)
	if err != nil {
		return ToIdentifier(name)
	}
	return e
}

// IntervalStringRe mirrors INTERVAL_STRING_RE.
var IntervalStringRe = regexp.MustCompile(`\s*(-?[0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]+)\s*`)

// IntervalDayTimeRe mirrors INTERVAL_DAY_TIME_RE (anchored at the start like re.match).
var IntervalDayTimeRe = regexp.MustCompile(`^\s*-?\s*\d+(?:\.\d+)?\s+(?:-?(?:\d+:)?\d+:\d+(?:\.\d+)?|-?(?:\d+:){1,2}|:)\s*`)

// ToInterval mirrors exp.to_interval.
func ToInterval(interval any) *Expr {
	s := ""
	switch x := interval.(type) {
	case *Expr:
		if x.Is(Literal) {
			if !x.IsString() {
				panic("Invalid interval string.")
			}
			s = x.Str("this")
		} else {
			return x
		}
	case string:
		s = x
	}
	e := Parse("INTERVAL "+s, nil)
	if !e.Is(Interval) {
		panic("expected Interval")
	}
	return e
}

// ToTable mirrors exp.to_table(sql_path, dialect, **kwargs). kv are extra args.
func ToTable(sqlPath any, dialect any, kv ...any) *Expr {
	if t, ok := sqlPath.(*Expr); ok && t != nil && t.Is(Table) {
		return t.Copy()
	}
	s := fmt.Sprint(sqlPath)
	table, err := MaybeParse(sqlPath, ParseOptions{Dialect: dialect, Into: []Kind{Table}, ErrorLevel: errs.Unset}, "", false)
	if err != nil {
		parts := splitNumWords(s, ".", 3)
		if parts[2] == nil {
			panic(err)
		}
		table = NewTable(*parts[2], strPtrAny(parts[1]), strPtrAny(parts[0]))
	}
	return table.SetKwargs(kv...)
}

// ToTableNoCopy is ToTable without copying Table inputs.
func ToTableNoCopy(sqlPath any, dialect any) *Expr {
	if t, ok := sqlPath.(*Expr); ok && t != nil && t.Is(Table) {
		return t
	}
	return ToTable(sqlPath, dialect)
}

func strPtrAny(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func splitNumWords(value, sep string, minNumWords int) []*string {
	words := strings.Split(value, sep)
	out := make([]*string, 0, max(minNumWords, len(words)))
	for i := 0; i < minNumWords-len(words); i++ {
		out = append(out, nil)
	}
	for i := range words {
		w := words[i]
		out = append(out, &w)
	}
	return out
}

// ToColumn mirrors exp.to_column(sql_path, quoted, dialect, **kwargs).
func ToColumn(sqlPath any, quoted *bool, dialect any, kv ...any) *Expr {
	if c, ok := sqlPath.(*Expr); ok && c != nil && c.Is(Column) {
		return c.Copy()
	}
	col, err := MaybeParse(sqlPath, ParseOptions{Dialect: dialect, Into: []Kind{Column}, ErrorLevel: errs.Unset}, "", false)
	if err != nil {
		parts := strings.Split(fmt.Sprint(sqlPath), ".")
		reverse(parts)
		args := make([]any, 4)
		for i := range args {
			if i < len(parts) {
				args[i] = parts[i]
			}
		}
		c := NewColumnFields(args[0], args[1], args[2], args[3], nil, quoted, true)
		return c.SetKwargs(kv...)
	}
	col.SetKwargs(kv...)
	if quoted != nil && *quoted {
		for i := range col.FindAll(Identifier) {
			i.Set("quoted", true)
		}
	}
	return col
}

// NewValues mirrors exp.values(values, alias, columns).
func NewValues(values []any, alias string, columns []string) *Expr {
	if len(columns) > 0 && alias == "" {
		panic("Alias is required when providing columns")
	}
	exprs := make([]*Expr, len(values))
	for i, v := range values {
		if t, ok := v.([]any); ok {
			v = TupleValue(t)
		}
		exprs[i] = ConvertValue(v, false)
	}
	var ta *Expr
	if len(columns) > 0 {
		cols := make([]*Expr, len(columns))
		for i, c := range columns {
			cols[i] = ToIdentifier(c)
		}
		ta = New(TableAlias, "this", ToIdentifier(alias), "columns", cols)
	} else if alias != "" {
		ta = New(TableAlias, "this", ToIdentifier(alias))
	}
	return New(Values, "expressions", exprs, "alias", ta)
}

// NewRenameTable mirrors exp.rename_table.
func NewRenameTable(oldName, newName any, dialect any) *Expr {
	return New(Alter, "this", ToTable(oldName, dialect), "kind", "TABLE",
		"actions", []*Expr{New(AlterRename, "this", ToTable(newName, dialect))})
}

// NewRenameColumn mirrors exp.rename_column.
func NewRenameColumn(tableName, oldColumn, newColumn any, exists any, dialect any) *Expr {
	return New(Alter, "this", ToTable(tableName, dialect), "kind", "TABLE",
		"actions", []*Expr{New(RenameColumn, "this", ToColumn(oldColumn, nil, dialect), "to", ToColumn(newColumn, nil, dialect), "exists", exists)})
}

// ReplaceChildren mirrors exp.replace_children: fun returns zero or more replacements.
func ReplaceChildren(e *Expr, fun func(*Expr) []*Expr) {
	for _, a := range append([]Arg(nil), e.args...) {
		switch v := a.Value.(type) {
		case []*Expr:
			var out []*Expr
			for _, cn := range v {
				out = append(out, fun(cn)...)
			}
			e.Set(a.Key, out)
		case *Expr:
			out := fun(v)
			if len(out) > 0 {
				e.Set(a.Key, out[0])
			} else {
				e.Set(a.Key, nil)
			}
		}
	}
}

// ReplaceTree mirrors exp.replace_tree.
func ReplaceTree(e *Expr, fun func(*Expr) *Expr, prune func(*Expr) bool) *Expr {
	var stack []*Expr
	for n := range e.DFS(prune) {
		stack = append(stack, n)
	}
	var newNode *Expr
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		newNode = fun(node)
		if newNode != node {
			node.Replace(newNode)
			if newNode != nil {
				stack = append(stack, newNode)
			}
		}
	}
	return newNode
}

// ColumnTableNames mirrors exp.column_table_names.
func ColumnTableNames(e *Expr, exclude string) map[string]bool {
	out := map[string]bool{}
	for c := range e.FindAll(Column) {
		if t := c.TableName(); t != "" && t != exclude {
			out[t] = true
		}
	}
	return out
}

// TableName mirrors exp.table_name(table, dialect, identify).
func TableName(table any, dialect any, identify bool) string {
	expr := MustMaybeParse(table, ParseOptions{Dialect: dialect, Into: []Kind{Table}, ErrorLevel: errs.Unset}, "", false)
	var parts []string
	for _, part := range expr.Parts() {
		if identify || !SafeIdentifierRe.MatchString(part.Name()) {
			parts = append(parts, part.SQL(dialect, Identify(true), NoCopy(), Comments(false)))
		} else {
			parts = append(parts, part.Name())
		}
	}
	return strings.Join(parts, ".")
}

var operatorKinds = []Kind{AtTimeZone, Between, Binary, BitwiseNot, In, Neg, Not}

func isOperand(p *Expr) bool {
	parent := p.parent
	if parent.IsAny(Bracket, In) {
		return p.argKey == "this"
	}
	return parent.IsAny(operatorKinds...)
}

func takesQuery(p *Expr) bool {
	parent := p.parent
	if parent.Is(In) {
		return p.argKey == "expressions" && len(parent.Expressions()) == 1
	}
	if parent.Is(Func) {
		return len(parent.IterExpressions()) == 1
	}
	if parent.Is(Table) {
		return parent.parent.IsAny(Create, Subquery)
	}
	return parent == nil || parent.IsAny(CTE, Paren, SetOperation)
}

func replacePlaceholder(p *Expr, e *Expr) *Expr {
	if e.Is(Query) && !e.Is(Subquery) && !takesQuery(p) {
		return e.Subquery(nil, false)
	}
	if isOperand(p) && e.IsAny(operatorKinds...) && !e.Is(Dot) {
		return New(Paren, "this", e)
	}
	return e
}

// isNoneValue reports whether v is Python None (nil or a typed nil expression).
func isNoneValue(v any) bool {
	if v == nil {
		return true
	}
	e, ok := v.(*Expr)
	return ok && e == nil
}

// ReplacePlaceholders mirrors exp.replace_placeholders(expression, *args, **kwargs).
func ReplacePlaceholders(e *Expr, args []any, kwargs map[string]any) *Expr {
	i := 0
	return e.Transform(func(node *Expr) *Expr {
		if node.Is(Placeholder) {
			if node.Arg("this") != nil {
				if v, ok := kwargs[node.Name()]; ok && !isNoneValue(v) {
					return replacePlaceholder(node, ConvertValue(v, false))
				}
			} else if i < len(args) {
				v := args[i]
				i++
				return replacePlaceholder(node, ConvertValue(v, false))
			}
		}
		return node
	}, true)
}

// FromArgList mirrors Func.from_arg_list: positional arguments are assigned to the kind's
// argument keys in order (the variable-length key collects the remaining arguments).
func FromArgList(kind Kind, args []*Expr) *Expr {
	info := &kindTable[kind]
	var kv []any
	if info.varLen {
		varIdx := info.argIndex[info.varLenKey]
		for i, a := range args {
			if i >= varIdx {
				break
			}
			kv = append(kv, info.args[i].Key, a)
		}
		rest := []*Expr{}
		if len(args) > varIdx {
			rest = append(rest, args[varIdx:]...)
		}
		kv = append(kv, info.varLenKey, rest)
	} else {
		for i, a := range args {
			if i >= len(info.args) {
				break
			}
			kv = append(kv, info.args[i].Key, a)
		}
	}
	return New(kind, kv...)
}
