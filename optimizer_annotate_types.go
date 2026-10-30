package sqlglot

// Port of sqlglot/optimizer/annotate_types.py.

import (
	"fmt"
	"slices"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// TypeValue is a type as used by the annotator: either an exp.DType or a DataType *exp.Expr
// (Python's `exp.DataType | exp.DType`). nil means None.
type TypeValue = any

// ExprMetadata mirrors an entry of sqlglot.typing.EXPRESSION_METADATA: either a fixed return
// type ("returns") or a custom annotator ("annotator").
type ExprMetadata struct {
	Returns   TypeValue
	Annotator func(a *TypeAnnotator, e *exp.Expr) *exp.Expr
}

// BinaryCoercionFunc computes the result type of a binary operation for a pair of types.
type BinaryCoercionFunc func(l, r *exp.Expr) TypeValue

// BigintExtractDateParts mirrors annotate_types.BIGINT_EXTRACT_DATE_PARTS.
var BigintExtractDateParts = map[string]bool{
	"EPOCH_SECOND": true, "EPOCH_MILLISECOND": true, "EPOCH_MICROSECOND": true, "EPOCH_NANOSECOND": true,
	"NANOSECOND": true,
}

// AnnotateOptions are the optional arguments of annotate_types.
type AnnotateOptions struct {
	Schema             any
	ExpressionMetadata map[exp.Kind]*ExprMetadata
	CoercesTo          map[exp.DType]exp.TypeSet
	Dialect            any
	NoOverwriteTypes   bool // overwrite_types=False
}

// AnnotateTypes mirrors annotate_types(expression, dialect=dialect): it infers the types of
// an expression tree in place and returns it.
func AnnotateTypes(e *exp.Expr, dialect any) *exp.Expr {
	return MustAnnotateTypesOpts(e, AnnotateOptions{Dialect: dialect})
}

// AnnotateTypesOpts mirrors annotate_types(expression, schema=None, expression_metadata=None,
// coerces_to=None, dialect=None, overwrite_types=True).
func AnnotateTypesOpts(e *exp.Expr, o AnnotateOptions) (out *exp.Expr, err error) {
	defer recoverOptimize(&err)
	schema, err := EnsureSchema(o.Schema, o.Dialect)
	if err != nil {
		return nil, err
	}
	return NewTypeAnnotator(schema, o.ExpressionMetadata, o.CoercesTo, nil, !o.NoOverwriteTypes).Annotate(e, true), nil
}

// MustAnnotateTypesOpts is AnnotateTypesOpts that panics on error.
func MustAnnotateTypesOpts(e *exp.Expr, o AnnotateOptions) *exp.Expr {
	out, err := AnnotateTypesOpts(e, o)
	if err != nil {
		panic(err)
	}
	return out
}

func recoverOptimize(err *error) {
	if r := recover(); r != nil {
		switch x := r.(type) {
		case *errs.OptimizeError:
			*err = x
		case *errs.SchemaError:
			*err = x
		case *errs.ParseError:
			*err = x
		case *errs.UnsupportedError:
			*err = x
		case parseBailout:
			*err = x.err
		case error:
			*err = x
		default:
			panic(r)
		}
	}
}

// typeThisOf returns the DType of a type value (DataType.this or the DType itself).
func typeThisOf(t TypeValue) (exp.DType, bool) {
	switch x := t.(type) {
	case exp.DType:
		return x, true
	case *exp.Expr:
		if x == nil {
			return "", false
		}
		d, ok := x.Arg("this").(exp.DType)
		return d, ok
	}
	return "", false
}

// typeValueExpr converts a type value to a DataType (DType.into_expr()).
func typeValueExpr(t TypeValue) *exp.Expr {
	switch x := t.(type) {
	case exp.DType:
		return x.IntoExpr()
	case *exp.Expr:
		return x
	}
	return nil
}

func isNilType(t TypeValue) bool {
	switch x := t.(type) {
	case nil:
		return true
	case *exp.Expr:
		return x == nil
	}
	return false
}

func coerceDateLiteral(l *exp.Expr, unit *exp.Expr) TypeValue {
	dateText := l.Name()
	isISODate := helper.IsISODate(dateText)
	if isISODate && isDateUnit(unit) {
		return exp.DTypeDate
	}
	// An ISO date is also an ISO datetime, but not vice versa
	if isISODate || helper.IsISODatetime(dateText) {
		return exp.DTypeDatetime
	}
	return exp.DTypeUnknown
}

func coerceDateType(l *exp.Expr, unit *exp.Expr) TypeValue {
	if l.IsType(exp.DTypeDate) && !isDateUnit(unit) {
		return exp.DTypeDatetime
	}
	if t := l.Type(); t != nil {
		return t
	}
	return exp.DTypeUnknown
}

func buildCoercesTo() map[exp.DType]exp.TypeSet {
	// Highest-to-lowest type precedence, as specified in Spark's docs (ANSI)
	precedences := [][]exp.DType{
		{exp.DTypeText, exp.DTypeNvarchar, exp.DTypeVarchar, exp.DTypeNchar, exp.DTypeChar},
		{exp.DTypeDecfloat, exp.DTypeDouble, exp.DTypeFloat, exp.DTypeBigdecimal, exp.DTypeDecimal, exp.DTypeBigint,
			exp.DTypeInt, exp.DTypeSmallint, exp.DTypeTinyint},
		{exp.DTypeTimestampltz, exp.DTypeTimestamptz, exp.DTypeTimestamp, exp.DTypeDatetime, exp.DTypeDate},
	}
	result := map[exp.DType]exp.TypeSet{}
	for _, precedence := range precedences {
		coercesTo := exp.NewTypeSet()
		for _, dataType := range precedence {
			result[dataType] = coercesTo.Union()
			coercesTo = coercesTo.With(dataType)
		}
	}
	return result
}

// TypeAnnotatorCoercesTo mirrors TypeAnnotator.COERCES_TO.
var TypeAnnotatorCoercesTo = buildCoercesTo()

type typePair [2]exp.DType

func swapAll(coercions map[typePair]BinaryCoercionFunc) map[typePair]BinaryCoercionFunc {
	out := map[typePair]BinaryCoercionFunc{}
	for k, f := range coercions {
		out[k] = f
	}
	for k, f := range coercions {
		f := f
		out[typePair{k[1], k[0]}] = func(l, r *exp.Expr) TypeValue { return f(r, l) }
	}
	return out
}

// TypeAnnotatorBinaryCoercions mirrors TypeAnnotator.BINARY_COERCIONS.
var TypeAnnotatorBinaryCoercions = func() map[typePair]BinaryCoercionFunc {
	out := map[typePair]BinaryCoercionFunc{}
	add := func(m map[typePair]BinaryCoercionFunc) {
		for k, v := range swapAll(m) {
			out[k] = v
		}
	}
	textInterval := map[typePair]BinaryCoercionFunc{}
	for _, t := range exp.DataTypeTextTypes.Slice() {
		textInterval[typePair{t, exp.DTypeInterval}] = func(l, r *exp.Expr) TypeValue {
			return coerceDateLiteral(l, r.Get("unit"))
		}
	}
	add(textInterval)
	textNumeric := map[typePair]BinaryCoercionFunc{}
	for _, text := range exp.DataTypeTextTypes.Slice() {
		for _, numeric := range exp.DataTypeNumericTypes.Slice() {
			// text + numeric will yield the numeric type to match most dialects' semantics
			textNumeric[typePair{text, numeric}] = func(l, r *exp.Expr) TypeValue {
				if exp.DataTypeNumericTypes.Has(l.Type().DataTypeThis()) {
					return l.Type()
				}
				return r.Type()
			}
		}
	}
	add(textNumeric)
	add(map[typePair]BinaryCoercionFunc{
		{exp.DTypeDate, exp.DTypeInterval}: func(l, r *exp.Expr) TypeValue { return coerceDateType(l, r.Get("unit")) },
	})
	return out
}()

type namedType struct {
	Name string
	Type TypeValue
}

type scopeSourceKey struct {
	scope *Scope
	name  string
}

type tableColumnAliases struct {
	table   *exp.Expr
	aliases map[string]string
}

// TypeAnnotator mirrors annotate_types.TypeAnnotator.
type TypeAnnotator struct {
	Schema             Schema
	Dialect            *Dialect
	ExpressionMetadata map[exp.Kind]*ExprMetadata
	CoercesTo          map[exp.DType]exp.TypeSet
	BinaryCoercions    map[typePair]BinaryCoercionFunc

	visited            map[*exp.Expr]bool
	nullExpressions    map[*exp.Expr]bool
	nullOrder          []*exp.Expr
	supportsNullType   bool
	setopColumnTypes   map[*exp.Expr][]namedType
	tableColumnAliases map[*exp.Expr]tableColumnAliases
	overwriteTypes     bool
	scopeSourceSelects map[scopeSourceKey]*OrderedMap[TypeValue]
}

// NewTypeAnnotator mirrors TypeAnnotator(schema, expression_metadata=None, coerces_to=None,
// binary_coercions=None, overwrite_types=True).
func NewTypeAnnotator(schema Schema, expressionMetadata map[exp.Kind]*ExprMetadata, coercesTo map[exp.DType]exp.TypeSet,
	binaryCoercions map[typePair]BinaryCoercionFunc, overwriteTypes bool) *TypeAnnotator {
	a := &TypeAnnotator{Schema: schema}
	d := schema.Dialect()
	if d == nil {
		d = BaseDialectClass().NewInstance()
	}
	a.Dialect = d
	a.ExpressionMetadata = expressionMetadata
	if len(a.ExpressionMetadata) == 0 {
		a.ExpressionMetadata = d.ExpressionMetadata
	}
	a.CoercesTo = coercesTo
	if len(a.CoercesTo) == 0 {
		a.CoercesTo = d.CoercesTo
	}
	if len(a.CoercesTo) == 0 {
		a.CoercesTo = TypeAnnotatorCoercesTo
	}
	a.BinaryCoercions = binaryCoercions
	if len(a.BinaryCoercions) == 0 {
		a.BinaryCoercions = TypeAnnotatorBinaryCoercions
	}
	a.visited = map[*exp.Expr]bool{}
	a.nullExpressions = map[*exp.Expr]bool{}
	a.supportsNullType = d.SupportsNullType
	a.setopColumnTypes = map[*exp.Expr][]namedType{}
	a.tableColumnAliases = map[*exp.Expr]tableColumnAliases{}
	a.overwriteTypes = overwriteTypes
	a.scopeSourceSelects = map[scopeSourceKey]*OrderedMap[TypeValue]{}
	return a
}

// Clear mirrors TypeAnnotator.clear.
func (a *TypeAnnotator) Clear() {
	a.visited = map[*exp.Expr]bool{}
	a.nullExpressions = map[*exp.Expr]bool{}
	a.nullOrder = nil
	a.setopColumnTypes = map[*exp.Expr][]namedType{}
	a.tableColumnAliases = map[*exp.Expr]tableColumnAliases{}
	a.scopeSourceSelects = map[scopeSourceKey]*OrderedMap[TypeValue]{}
}

// SetType mirrors TypeAnnotator._set_type.
func (a *TypeAnnotator) SetType(e *exp.Expr, targetType TypeValue) *exp.Expr {
	prevType := e.Type()
	var dt *exp.Expr
	if isNilType(targetType) {
		dt = exp.DTypeUnknown.IntoExpr()
	} else if x, ok := targetType.(*exp.Expr); ok && x.IsKind(exp.DataType) {
		dt = x
	} else if d, ok := targetType.(exp.DType); ok {
		if d == "" {
			dt = exp.DTypeUnknown.IntoExpr()
		} else {
			dt = d.IntoExpr()
		}
	} else {
		dt = typeValueExpr(targetType)
	}
	e.SetType(dt)
	a.visited[e] = true
	if !a.supportsNullType && e.Type().DataTypeThis() == exp.DTypeNull {
		if !a.nullExpressions[e] {
			a.nullOrder = append(a.nullOrder, e)
		}
		a.nullExpressions[e] = true
	} else if prevType != nil && prevType.DataTypeThis() == exp.DTypeNull {
		delete(a.nullExpressions, e)
	}
	return e
}

// Annotate mirrors TypeAnnotator.annotate(expression, annotate_scope=True).
func (a *TypeAnnotator) Annotate(e *exp.Expr, annotateScope bool) *exp.Expr {
	// This flag is used to avoid costly scope traversals when we only care about annotating
	// non-column expressions (partial type inference), e.g., when simplifying in the optimizer
	if annotateScope {
		for _, scope := range TraverseScope(e) {
			a.AnnotateScope(scope)
		}
	}
	// This takes care of non-traversable expressions
	a.AnnotateExpression(e, nil)

	// Replace NULL type with the default type of the targeted dialect, since the former is not an
	// actual type; it is mostly used to aid type coercion, e.g. in query set operations.
	for _, x := range slices.Clone(a.nullOrder) {
		if a.nullExpressions[x] {
			a.SetType(x, a.Dialect.DefaultNullType)
		}
	}
	return e
}

func (a *TypeAnnotator) getScopeSourceSelects(scope *Scope, sourceName string) *OrderedMap[TypeValue] {
	key := scopeSourceKey{scope, sourceName}
	if selects, ok := a.scopeSourceSelects[key]; ok {
		return selects
	}
	selects := NewOrderedMap[TypeValue]()
	source, _ := scope.Sources.Get(sourceName)

	var pivots []*exp.Expr
	for _, pivot := range scope.Pivots() {
		parent := pivot.Parent()
		if parent != nil {
			pp := parent.List("pivots")
			if len(pp) > 0 && pp[len(pp)-1] == pivot && pivot.AliasOrName() == sourceName {
				pivots = pp
				break
			}
		}
	}

	if len(pivots) > 0 {
		parent := pivots[len(pivots)-1].Parent()
		var parentSource Source
		if parent != nil {
			parentSource, _ = scope.Sources.Get(parent.AliasOrName())
		}
		if _, isScope := parentSource.(*Scope); !isScope && parent.Is(exp.Table) && parent.DB() == "" {
			// A chain aliased like the CTE it reads from shadows it in
			// `scope.sources`, so reach for the CTE's scope directly
			parentSource, _ = scope.CteSources.Get(parent.Name())
		}
		var srcTypes *OrderedMap[TypeValue]
		if ps, ok := parentSource.(*Scope); ok {
			srcTypes = a.getSourceScopeSelects(ps)
		} else if ms, ok := a.Schema.(*MappingSchema); ok && parent.Is(exp.Table) {
			srcTypes = NewOrderedMap[TypeValue]()
			if found := ms.find(parent, false, true); found != nil {
				for k, v := range found.All() {
					srcTypes.Set(k, v)
				}
			}
		} else {
			srcTypes = NewOrderedMap[TypeValue]()
		}
		for _, pivot := range pivots {
			if pivot.Unpivot() {
				srcTypes = a.getUnpivotColumnTypes(pivot, srcTypes)
			} else {
				srcTypes = a.getPivotColumnTypes(pivot, srcTypes)
			}
		}
		selects = srcTypes
	} else if ss, ok := source.(*Scope); ok {
		selects = a.getSourceScopeSelects(ss)
	}
	a.scopeSourceSelects[key] = selects
	return selects
}

func (a *TypeAnnotator) getSourceScopeSelects(source *Scope) *OrderedMap[TypeValue] {
	out := NewOrderedMap[TypeValue]()
	e := source.Expression
	if source.IsCTE() {
		// Recursive references can point to an unfinished UNION. Use its nearest resolved
		// anchor prefix until the recursive branches have been annotated.
		for e.Is(exp.SetOperation) {
			if _, ok := a.setopColumnTypes[e]; ok {
				break
			}
			e = e.This().Unnest()
		}
	}

	if e.Is(exp.UDTF) {
		var values []*exp.Expr
		switch {
		case e.IsKind(exp.Lateral):
			if e.This().IsKind(exp.Explode) {
				values = []*exp.Expr{e.This().This()}
			}
		case e.IsKind(exp.Unnest):
			values = []*exp.Expr{e}
		case !e.IsKind(exp.TableFromRows):
			if first := seqGet(e.Expressions(), 0); first != nil {
				values = first.Expressions()
			}
		}
		if len(values) == 0 {
			return out
		}
		aliasColumnNames := e.AliasColumnNames()
		var expType *exp.Expr
		if e.IsKind(exp.Unnest) {
			expType = e.Type()
		} else if e.IsKind(exp.Lateral) && e.This().IsKind(exp.Explode) {
			expType = e.This().Type()
		}
		if expType != nil && expType.IsType(exp.DTypeStruct) {
			for _, colDef := range expType.Expressions() {
				if colDef.IsKind(exp.ColumnDef) && colDef.Bool("kind") {
					out.Set(colDef.Name(), colDef.Get("kind"))
				}
			}
			return out
		}
		if e.IsKind(exp.Values) {
			// Like a set operation's, each VALUES column's type is coerced across all rows
			columnTypes := make([]TypeValue, len(values))
			for i := range columnTypes {
				columnTypes[i] = exp.DTypeNull
			}
			for _, row := range e.Expressions() {
				rowExprs := row.Expressions()
				for i := 0; i < len(rowExprs) && i < len(columnTypes); i++ {
					var vt TypeValue = rowExprs[i].Type()
					if isNilType(vt) {
						vt = exp.DTypeUnknown
					}
					columnTypes[i] = a.MaybeCoerce(vt, columnTypes[i])
				}
			}
			for i := 0; i < len(aliasColumnNames) && i < len(columnTypes); i++ {
				out.Set(aliasColumnNames[i], columnTypes[i])
			}
			return out
		}
		for i := 0; i < len(aliasColumnNames) && i < len(values); i++ {
			out.Set(aliasColumnNames[i], typeOrNil(values[i]))
		}
		return out
	}
	if e.Is(exp.SetOperation) {
		for _, nt := range a.setopColumnTypes[e] {
			out.Set(nt.Name, nt.Type)
		}
		return out
	}
	if e.Is(exp.Selectable) {
		for _, s := range e.Selects() {
			if t := s.Type(); t != nil {
				out.Set(s.AliasOrName(), t)
			}
		}
	}
	return out
}

// typeOrNil returns the node's type as a TypeValue (nil if unset).
func typeOrNil(e *exp.Expr) TypeValue {
	if t := e.Type(); t != nil {
		return t
	}
	return nil
}

// AnnotateScope mirrors TypeAnnotator.annotate_scope.
func (a *TypeAnnotator) AnnotateScope(scope *Scope) {
	// Iterate through all the expressions of the current scope in post-order, and annotate
	a.AnnotateExpression(scope.Expression, scope)
	a.fixupOrderByAliases(scope)

	if a.Dialect.QueryResultsAreStructs && scope.Expression.Is(exp.Query) {
		delete(scope.Expression.Meta(), "query_type")
		var columnTypes []namedType
		if scope.Expression.Is(exp.SetOperation) {
			columnTypes = a.setopColumnTypes[scope.Expression]
		} else {
			for _, sel := range scope.Expression.Selects() {
				columnTypes = append(columnTypes, namedType{sel.OutputName(), typeOrNil(sel)})
			}
		}
		var defs []*exp.Expr
		for _, ct := range columnTypes {
			var kind any
			if !isNilType(ct.Type) {
				kind = mustBuildType(ct.Type)
			}
			defs = append(defs, exp.New(exp.ColumnDef, "this", exp.ToIdentifier(ct.Name), "kind", kind))
		}
		structType := exp.New(exp.DataType, "this", exp.DTypeStruct, "expressions", defs, "nested", true)
		if len(defs) > 0 {
			ok := true
			for _, cd := range defs {
				if k := cd.Get("kind"); k == nil || k.IsType(exp.DTypeUnknown) {
					ok = false
					break
				}
			}
			if ok {
				// We don't use `_set_type` on purpose here. If we annotated the query directly, then
				// using it in other contexts (e.g., ARRAY(<query>)) could result in incorrect type
				// annotations, i.e., it shouldn't be interpreted as a STRUCT value.
				scope.Expression.Meta()["query_type"] = structType
			}
		}
	}
}

// mustBuildType mirrors exp.DataType.build(kind) for a type value.
func mustBuildType(t TypeValue) *exp.Expr {
	switch x := t.(type) {
	case *exp.Expr:
		return x.Copy()
	case exp.DType:
		return x.IntoExpr()
	}
	return exp.MustDataTypeBuild(t, nil, false)
}

// AnnotateExpression mirrors TypeAnnotator.annotate_expression(expression, scope=None): it
// annotates the expression's children first (post-order), then the expression itself.
func (a *TypeAnnotator) AnnotateExpression(e *exp.Expr, scope *Scope) {
	type item struct {
		e                *exp.Expr
		childrenAnnotated bool
	}
	stack := []item{{e, false}}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		expr := it.e
		if a.visited[expr] || (!a.overwriteTypes && expr.Type() != nil && !expr.IsType(exp.DTypeUnknown)) {
			continue // We've already inferred the expression's type
		}
		if !it.childrenAnnotated {
			stack = append(stack, item{expr, true})
			for _, child := range expr.IterExpressions() {
				stack = append(stack, item{child, false})
			}
			continue
		}

		if scope != nil && expr.Is(exp.TableColumn) {
			a.annotateTableColumn(expr, scope)
			continue
		}

		if scope != nil && expr.Is(exp.Column) && expr.TableName() != "" && !expr.IsStar() {
			a.annotateQualifiedColumn(expr, scope)
			continue
		}

		if expr.Is(exp.SetOperation) {
			a.annotateSetOperation(expr)
		}

		if spec := a.ExpressionMetadata[expr.Kind()]; spec != nil && spec.Annotator != nil {
			spec.Annotator(a, expr)
		} else if spec != nil && !isNilType(spec.Returns) {
			a.SetType(expr, spec.Returns)
		} else {
			a.SetType(expr, exp.DTypeUnknown)
		}
		a.restoreDotParts(expr)
	}
}

func (a *TypeAnnotator) annotateQualifiedColumn(expr *exp.Expr, scope *Scope) {
	var source Source
	sourceScope := scope
	for sourceScope != nil && source == nil {
		source, _ = sourceScope.Sources.Get(expr.TableName())
		if source == nil {
			sourceScope = sourceScope.Parent
		}
	}

	switch src := source.(type) {
	case *exp.Expr:
		if !src.Is(exp.Table) {
			a.SetType(expr, exp.DTypeUnknown)
			break
		}
		if _, ok := a.tableColumnAliases[src]; !ok {
			aliases := map[string]string{}
			if aliasColumns := src.AliasColumnNames(); len(aliasColumns) > 0 {
				names, _ := a.Schema.ColumnNames(src, false, nil, nil)
				for i := 0; i < len(aliasColumns) && i < len(names); i++ {
					if _, ok := aliases[aliasColumns[i]]; !ok {
						aliases[aliasColumns[i]] = names[i]
					}
				}
			}
			a.tableColumnAliases[src] = tableColumnAliases{src, aliases}
		}
		columnExpr := expr
		if physical, ok := a.tableColumnAliases[src].aliases[expr.Name()]; ok {
			q := true
			columnExpr = exp.NewColumnFields(physical, nil, nil, nil, nil, &q, true)
		}
		tableColType, err := a.Schema.GetColumnType(src, columnExpr, nil, nil)
		if err != nil {
			panic(err)
		}
		var t TypeValue = tableColType
		if tableColType != nil && tableColType.IsType(exp.DTypeUnknown) && src.Bool("pivots") {
			ss := sourceScope
			if ss == nil {
				ss = scope
			}
			if v, ok := a.getScopeSourceSelects(ss, expr.TableName()).Get(expr.Name()); ok && !isNilType(v) {
				t = v
			} else {
				t = exp.DTypeUnknown
			}
		}
		a.SetType(expr, t)
	case *Scope:
		if sourceScope == nil {
			a.SetType(expr, exp.DTypeUnknown)
			break
		}
		if colType, ok := a.getScopeSourceSelects(sourceScope, expr.TableName()).Get(expr.Name()); ok && !isNilType(colType) {
			a.SetType(expr, colType)
		} else if src.Expression.IsKind(exp.Unnest) {
			a.SetType(expr, typeOrNil(src.Expression))
		} else {
			a.SetType(expr, exp.DTypeUnknown)
		}
	default:
		if len(scope.Pivots()) > 0 {
			if colType, ok := a.getScopeSourceSelects(scope, expr.TableName()).Get(expr.Name()); ok && !isNilType(colType) {
				a.SetType(expr, colType)
				break
			}
		}
		a.SetType(expr, exp.DTypeUnknown)
	}

	a.restoreDotParts(expr)
	if t := expr.Type(); t != nil {
		if b, ok := t.Arg("nullable").(bool); ok && !b {
			expr.Meta()["nonnull"] = true
		}
	}
}

func (a *TypeAnnotator) annotateTableColumn(e *exp.Expr, scope *Scope) {
	source, _ := scope.Sources.Get(e.Name())
	var columnTypes *OrderedMap[TypeValue]
	pivotMatch := false
	for _, pivot := range scope.Pivots() {
		if pivot.Alias() == e.Name() {
			pivotMatch = true
			break
		}
	}
	if pivotMatch {
		columnTypes = a.getScopeSourceSelects(scope, e.Name())
	} else if ss, ok := source.(*Scope); ok {
		if qt, ok := ss.Expression.MetaGet("query_type").(*exp.Expr); ok && qt != nil && qt.IsType(exp.DTypeStruct) {
			a.SetType(e, qt)
			return
		}
	} else if t, ok := source.(*exp.Expr); ok && t.Is(exp.Table) {
		if ms, ok := a.Schema.(*MappingSchema); ok {
			if found := ms.find(t, false, true); found != nil {
				columnTypes = NewOrderedMap[TypeValue]()
				for k, v := range found.All() {
					columnTypes.Set(k, v)
				}
			}
		}
	}

	if columnTypes.Len() > 0 {
		all := true
		for _, kind := range columnTypes.Values() {
			if isNilType(kind) {
				all = false
				break
			}
			if dt, err := exp.DataTypeBuildNoCopy(kind, nil, false); err != nil || dt.IsType(exp.DTypeUnknown) {
				all = false
				break
			}
		}
		if all {
			var defs []*exp.Expr
			for name, kind := range columnTypes.All() {
				defs = append(defs, exp.New(exp.ColumnDef, "this", exp.ToIdentifier(name), "kind", exp.MustDataTypeBuild(kind, nil, false)))
			}
			a.SetType(e, exp.New(exp.DataType, "this", exp.DTypeStruct, "expressions", defs, "nested", true))
			return
		}
	}
	a.SetType(e, exp.DTypeUnknown)
}

func (a *TypeAnnotator) restoreDotParts(expr *exp.Expr) {
	// Dot access into semi-structured values is a case sensitive data lookup, i.e. the
	// engine doesn't resolve the keys as identifiers, so we undo their normalization.
	dotParts, _ := expr.MetaGet("dot_parts").([]string)
	if len(dotParts) == 0 || !expr.IsType(exp.DTypeJson, exp.DTypeMap, exp.DTypeVariant) {
		if len(dotParts) > 0 {
			// A struct field can itself contain semi-structured data. Resolve each
			// struct access before deciding whether the remaining keys are identifiers.
			if expr.IsType(exp.DTypeStruct) && expr.Parent().IsKind(exp.Dot) {
				expr.Parent().Meta()["dot_parts"] = dotParts[1:]
			}
			if expr.Type() == nil || expr.IsType(exp.DTypeUnknown) {
				return
			}
			delete(expr.Meta(), "dot_parts")
		}
		return
	}
	parent := expr.Parent()
	for _, part := range dotParts {
		if !parent.IsKind(exp.Dot) {
			break
		}
		identifier := parent.Expression()
		if identifier.IsKind(exp.Identifier) {
			// Rename in place to preserve the identifier's meta, e.g. token positions
			identifier.Set("this", part)
			identifier.Set("quoted", true)
		} else {
			identifier.Replace(exp.ToIdentifierQuoted(part, true))
		}
		parent = parent.Parent()
	}
	delete(expr.Meta(), "dot_parts")
}

func (a *TypeAnnotator) fixupOrderByAliases(scope *Scope) {
	query := scope.Expression
	if !query.Is(exp.Query) {
		return
	}
	order := query.Get("order")
	if order == nil {
		return
	}
	// Build alias -> type map from fully-annotated projections (last match wins,
	// consistent with how _expand_alias_refs handles duplicate aliases).
	aliasTypes := map[string]*exp.Expr{}
	for _, sel := range query.Selects() {
		if sel.IsKind(exp.Alias) && sel.This().Type() != nil && !sel.This().IsType(exp.DTypeUnknown) {
			aliasTypes[sel.Alias()] = sel.This().Type()
		}
	}
	if len(aliasTypes) == 0 {
		return
	}
	for _, ordered := range order.Expressions() {
		var aliasCols []*exp.Expr
		for _, c := range ordered.FindAllList(exp.Column) {
			if c.TableName() == "" {
				if _, ok := aliasTypes[c.Name()]; ok {
					aliasCols = append(aliasCols, c)
				}
			}
		}
		for _, col := range aliasCols {
			a.SetType(col, aliasTypes[col.Name()])
		}
		if len(aliasCols) > 0 {
			for node := range ordered.Walk(true, func(n *exp.Expr) bool { return n.IsKind(exp.Subquery) }) {
				if !node.IsAny(exp.Column, exp.Literal) {
					delete(a.visited, node)
				}
			}
			a.AnnotateExpression(ordered, scope)
		}
	}
}

// MaybeCoerce mirrors TypeAnnotator._maybe_coerce: the result type of combining two types.
func (a *TypeAnnotator) MaybeCoerce(type1, type2 TypeValue) TypeValue {
	var t1, t2 exp.DType
	if x, ok := type1.(*exp.Expr); ok {
		d, isD := x.Arg("this").(exp.DType)
		if len(x.Expressions()) > 0 || !isD {
			return type1
		}
		t1 = d
	} else {
		t1, _ = type1.(exp.DType)
	}
	if x, ok := type2.(*exp.Expr); ok {
		d, isD := x.Arg("this").(exp.DType)
		if len(x.Expressions()) > 0 || !isD {
			return type2
		}
		t2 = d
	} else {
		t2, _ = type2.(exp.DType)
	}
	// We propagate the UNKNOWN type upwards if found
	if t1 == exp.DTypeUnknown || t2 == exp.DTypeUnknown {
		return exp.DTypeUnknown
	}
	if t1 == exp.DTypeNull {
		return t2
	}
	if t2 == exp.DTypeNull {
		return t1
	}
	if a.CoercesTo[t1].Has(t2) {
		return t2
	}
	return t1
}

func (a *TypeAnnotator) annotateSetOperation(e *exp.Expr) {
	// The expression annotation pass visits children first, including when a scope is
	// annotated independently. Preserve duplicate names until positional matching is done.
	var operands [][]namedType
	for _, operand := range []*exp.Expr{e.This().Unnest(), e.Expression().Unnest()} {
		if operand.Is(exp.SetOperation) {
			cols := a.setopColumnTypes[operand]
			delete(a.setopColumnTypes, operand)
			operands = append(operands, cols)
		} else {
			var cols []namedType
			for _, s := range operand.Selects() {
				var t TypeValue = typeOrNil(s)
				if isNilType(t) {
					t = exp.DTypeUnknown
				}
				cols = append(cols, namedType{s.AliasOrName(), t})
			}
			operands = append(operands, cols)
		}
	}
	left, right := operands[0], operands[1]
	var columns []namedType
	if len(left) > 0 && len(right) > 0 {
		if e.Bool("by_name") {
			leftTypes, rightTypes := NewOrderedMap[TypeValue](), NewOrderedMap[TypeValue]()
			for _, nt := range left {
				leftTypes.Set(nt.Name, nt.Type)
			}
			for _, nt := range right {
				rightTypes.Set(nt.Name, nt.Type)
			}
			var names []string
			if on := e.List("on"); len(on) > 0 {
				for _, c := range on {
					names = append(names, c.Name())
				}
			} else if e.TextUpper("kind") == "INNER" {
				for _, n := range leftTypes.Keys() {
					if rightTypes.Has(n) {
						names = append(names, n)
					}
				}
			} else if e.TextUpper("side") == "LEFT" {
				names = leftTypes.Keys()
			} else {
				merged := leftTypes.Copy()
				merged.Update(rightTypes)
				names = merged.Keys()
			}
			// Missing columns are NULL-filled, preserving the other operand's type.
			for _, name := range names {
				var lt, rt TypeValue = exp.DTypeNull, exp.DTypeNull
				if v, ok := leftTypes.Get(name); ok {
					lt = v
				}
				if v, ok := rightTypes.Get(name); ok {
					rt = v
				}
				columns = append(columns, namedType{name, a.MaybeCoerce(lt, rt)})
			}
		} else if len(left) == len(right) {
			for i := range left {
				columns = append(columns, namedType{left[i].Name, a.MaybeCoerce(left[i].Type, right[i].Type)})
			}
		}
	}
	a.setopColumnTypes[e] = columns
}

func (a *TypeAnnotator) getUnpivotColumnTypes(pivot *exp.Expr, srcTypes *OrderedMap[TypeValue]) *OrderedMap[TypeValue] {
	newTypes := NewOrderedMap[TypeValue]()
	for _, field := range pivot.Fields() {
		fieldCol := field.This()
		first := seqGet(field.Expressions(), 0)
		var inSrc *exp.Expr
		if aliasNode := first.Get("alias"); first.IsKind(exp.PivotAlias) && aliasNode != nil {
			newTypes.Set(fieldCol.Name(), typeOrNil(aliasNode))
			inSrc = first.This()
		} else {
			newTypes.Set(fieldCol.Name(), exp.DTypeVarchar.IntoExpr())
			inSrc = first
		}
		inCols := []*exp.Expr{inSrc}
		if inSrc.IsKind(exp.Tuple) {
			inCols = inSrc.Expressions()
		}
		valExpr := seqGet(pivot.Expressions(), 0)
		valCols := []*exp.Expr{valExpr}
		if valExpr.IsKind(exp.Tuple) {
			valCols = valExpr.Expressions()
		}
		for i := 0; i < len(valCols) && i < len(inCols); i++ {
			// A chained operator's IN-list may name columns an earlier operator
			// produced, which carry no annotation of their own
			var inType TypeValue = typeOrNil(inCols[i])
			if isNilType(inType) || inCols[i].Type().IsType(exp.DTypeUnknown) {
				if v, ok := srcTypes.Get(inCols[i].OutputName()); ok && !isNilType(v) {
					inType = v
				}
			}
			newTypes.Set(valCols[i].OutputName(), inType)
		}
	}
	out := NewOrderedMap[TypeValue]()
	names, _ := pivot.PivotOutputColumns(srcTypes.Keys())
	for _, name := range names {
		t, _ := newTypes.Get(name)
		if isNilType(t) {
			t, _ = srcTypes.Get(name)
		}
		if !isNilType(t) {
			out.Set(name, t)
		}
	}
	return out
}

func (a *TypeAnnotator) getPivotColumnTypes(pivot *exp.Expr, srcTypes *OrderedMap[TypeValue]) *OrderedMap[TypeValue] {
	firstField := seqGet(pivot.Fields(), 0)
	if !firstField.IsKind(exp.In) {
		panic(&errs.OptimizeError{Message: fmt.Sprintf("Expected In expression for pivot field, got %s", firstField.ClassName())})
	}
	if cols, ok := pivot.Arg("columns").([]*exp.Expr); ok && len(cols) == 0 {
		excluded := map[string]bool{}
		for _, c := range pivot.FindAllList(exp.Column) {
			excluded[c.Name()] = true
		}
		aliases := pivot.AliasColumnNames()
		out := NewOrderedMap[TypeValue]()
		i := 0
		for _, name := range srcTypes.Keys() {
			if excluded[name] {
				continue
			}
			key := name
			if i < len(aliases) && aliases[i] != "" {
				key = aliases[i]
			}
			v, _ := srcTypes.Get(name)
			out.Set(key, v)
			i++
		}
		return out
	}

	pivotConstants := firstField.Expressions()
	// The first agg_cols_offset entries are source columns that pass through the PIVOT unchanged;
	// the rest are the aggregated columns, one per combination of IN value and aggregate function.
	outputNames, outputToSrc := pivot.PivotOutputColumns(srcTypes.Keys())

	// Output types may be requested before the PIVOT itself has been annotated.
	var aggTypes []*exp.Expr
	for _, agg := range pivot.Expressions() {
		if agg.Type() == nil || agg.IsType(exp.DTypeUnknown) {
			agg = agg.Copy()
			for _, column := range agg.FindAllList(exp.Column) {
				v, _ := srcTypes.Get(column.Name())
				a.SetType(column, v)
			}
			a.AnnotateExpression(agg, nil)
		}
		if agg.IsKind(exp.Alias) {
			aggTypes = append(aggTypes, agg.This().Type())
		} else {
			aggTypes = append(aggTypes, agg.Type())
		}
	}
	aggColsOffset := len(outputNames) - len(pivotConstants)*len(aggTypes)
	if aggColsOffset < 0 {
		panic(&errs.OptimizeError{Message: fmt.Sprintf("Negative pivot column offset: %d", aggColsOffset)})
	}
	newTypes := NewOrderedMap[TypeValue]()
	for _, name := range outputNames[:aggColsOffset] {
		if t, ok := srcTypes.Get(outputToSrc[name]); ok && !isNilType(t) {
			newTypes.Set(name, t)
		}
	}
	var repeated []*exp.Expr
	for range pivotConstants {
		repeated = append(repeated, aggTypes...)
	}
	rest := outputNames[aggColsOffset:]
	for i := 0; i < len(rest) && i < len(repeated); i++ {
		if repeated[i] != nil {
			newTypes.Set(rest[i], repeated[i])
		}
	}
	return newTypes
}

// AnnotateBinary mirrors TypeAnnotator._annotate_binary.
func (a *TypeAnnotator) AnnotateBinary(e *exp.Expr) *exp.Expr {
	left, right := e.Left(), e.Right()
	if left == nil || right == nil {
		Logger.Warn("Failed to annotate badly formed binary expression: " + e.SQL(a.Dialect))
		a.SetType(e, nil)
		return e
	}
	leftType, rightType := exp.DTypeUnknown, exp.DTypeUnknown
	if t := left.Type(); t != nil {
		leftType = t.DataTypeThis()
	}
	if t := right.Type(); t != nil {
		rightType = t.DataTypeThis()
	}
	if e.IsAny(exp.Connector, exp.Predicate) {
		a.SetType(e, exp.DTypeBoolean)
	} else if f, ok := a.BinaryCoercions[typePair{leftType, rightType}]; ok {
		a.SetType(e, f(left, right))
	} else {
		a.AnnotateByArgs(e, []any{left, right}, false, false)
	}
	if e.IsKind(exp.Is) || (left.MetaGet("nonnull") == true && right.MetaGet("nonnull") == true) {
		e.Meta()["nonnull"] = true
	}
	return e
}

// AnnotateUnary mirrors TypeAnnotator._annotate_unary.
func (a *TypeAnnotator) AnnotateUnary(e *exp.Expr) *exp.Expr {
	if e.IsKind(exp.Not) {
		a.SetType(e, exp.DTypeBoolean)
	} else {
		a.SetType(e, typeOrNil(e.This()))
	}
	if e.This().MetaGet("nonnull") == true {
		e.Meta()["nonnull"] = true
	}
	return e
}

// AnnotateLiteral mirrors TypeAnnotator._annotate_literal.
func (a *TypeAnnotator) AnnotateLiteral(e *exp.Expr) *exp.Expr {
	if e.IsString() {
		a.SetType(e, exp.DTypeVarchar)
	} else if e.IsInt() {
		a.SetType(e, exp.DTypeInt)
	} else {
		a.SetType(e, exp.DTypeDouble)
	}
	e.Meta()["nonnull"] = true
	return e
}

// AnnotateByArgs mirrors TypeAnnotator._annotate_by_args(expression, *args, promote=False,
// array=False). args are argument names (strings) or expressions.
func (a *TypeAnnotator) AnnotateByArgs(e *exp.Expr, args []any, promote, array bool) *exp.Expr {
	var literalType, nonLiteralType TypeValue
	var nestedType *exp.Expr

	for _, arg := range args {
		var expressions []*exp.Expr
		switch x := arg.(type) {
		case string:
			switch v := e.Arg(x).(type) {
			case *exp.Expr:
				expressions = []*exp.Expr{v}
			case []*exp.Expr:
				expressions = v
			}
		case *exp.Expr:
			if x != nil {
				expressions = []*exp.Expr{x}
			}
		case []*exp.Expr:
			expressions = x
		}
		for _, expr := range expressions {
			exprType := expr.Type()
			if exprType == nil || exprType.IsType(exp.DTypeUnknown) {
				a.SetType(e, exp.DTypeUnknown)
				return e
			}
			if nestedType != nil {
				// Only resolve ARRAY<NULL>; preserve existing coercion for other nested types.
				if nestedType.IsType(exp.DTypeArray) && len(nestedType.Expressions()) > 0 &&
					nestedType.Expressions()[0].IsType(exp.DTypeNull) && exprType.IsType(exp.DTypeArray) &&
					len(exprType.Expressions()) > 0 {
					nestedType = exprType
				}
				continue
			}
			// Prefer the first nested data type, resolving ARRAY<NULL> from later arrays.
			if exprType.Bool("nested") {
				nestedType = exprType
			} else if expr.IsKind(exp.Literal) {
				base := literalType
				if isNilType(base) {
					base = exprType
				}
				literalType = a.MaybeCoerce(base, exprType)
			} else {
				base := nonLiteralType
				if isNilType(base) {
					base = exprType
				}
				nonLiteralType = a.MaybeCoerce(base, exprType)
			}
		}
	}

	var resultType TypeValue
	if nestedType != nil {
		resultType = nestedType
	} else if !isNilType(literalType) && !isNilType(nonLiteralType) {
		if a.Dialect.PrioritizeNonLiteralTypes {
			lt, _ := typeThisOf(literalType)
			nt, _ := typeThisOf(nonLiteralType)
			if (exp.DataTypeIntegerTypes.Has(lt) && exp.DataTypeIntegerTypes.Has(nt)) ||
				(exp.DataTypeRealTypes.Has(lt) && exp.DataTypeRealTypes.Has(nt)) {
				resultType = nonLiteralType
			}
		}
		if isNilType(resultType) {
			resultType = a.MaybeCoerce(nonLiteralType, literalType)
		}
	} else {
		switch {
		case !isNilType(literalType):
			resultType = literalType
		case !isNilType(nonLiteralType):
			resultType = nonLiteralType
		case array:
			resultType = exp.DTypeNull
		default:
			resultType = exp.DTypeUnknown
		}
	}

	a.SetType(e, resultType)

	if promote {
		thisType, _ := typeThisOf(resultType)
		if exp.DataTypeIntegerTypes.Has(thisType) {
			a.SetType(e, exp.DTypeBigint)
		} else if exp.DataTypeFloatTypes.Has(thisType) {
			a.SetType(e, exp.DTypeDouble)
		}
	}
	if array && !e.IsType(exp.DTypeUnknown) {
		a.SetType(e, exp.New(exp.DataType, "this", exp.DTypeArray, "expressions", []*exp.Expr{e.Type()}, "nested", true))
	}
	return e
}

// AnnotateTimeunit mirrors TypeAnnotator._annotate_timeunit.
func (a *TypeAnnotator) AnnotateTimeunit(e *exp.Expr) *exp.Expr {
	var datatype TypeValue
	t := e.This().Type().DataTypeThis()
	if exp.DataTypeTextTypes.Has(t) {
		datatype = coerceDateLiteral(e.This(), e.Unit())
	} else if exp.DataTypeTemporalTypes.Has(t) {
		datatype = coerceDateType(e.This(), e.Unit())
	} else {
		datatype = exp.DTypeUnknown
	}
	a.SetType(e, datatype)
	return e
}

// AnnotateBracket mirrors TypeAnnotator._annotate_bracket.
func (a *TypeAnnotator) AnnotateBracket(e *exp.Expr) *exp.Expr {
	bracketArg := e.Expressions()[0]
	this := e.This()
	if bracketArg.IsKind(exp.Slice) {
		a.SetType(e, typeOrNil(this))
	} else if this.Type().IsType(exp.DTypeArray) {
		a.SetType(e, seqGet(this.Type().Expressions(), 0))
	} else if keys := this.Keys(); this.IsAny(exp.Map, exp.VarMap) && slices.ContainsFunc(keys, bracketArg.Equal) {
		index := slices.IndexFunc(keys, bracketArg.Equal)
		value := seqGet(this.Values(), index)
		if value != nil {
			a.SetType(e, typeOrNil(value))
		} else {
			a.SetType(e, nil)
		}
	} else {
		a.SetType(e, exp.DTypeUnknown)
	}
	return e
}

// AnnotateDiv mirrors TypeAnnotator._annotate_div.
func (a *TypeAnnotator) AnnotateDiv(e *exp.Expr) *exp.Expr {
	left, right := e.Left(), e.Right()
	leftType, rightType := exp.DTypeUnknown, exp.DTypeUnknown
	if t := left.Type(); t != nil {
		leftType = t.DataTypeThis()
	}
	if t := right.Type(); t != nil {
		rightType = t.DataTypeThis()
	}
	if e.Bool("typed") && exp.DataTypeIntegerTypes.Has(leftType) && exp.DataTypeIntegerTypes.Has(rightType) {
		a.SetType(e, exp.DTypeBigint)
	} else {
		a.SetType(e, a.MaybeCoerce(leftType, rightType))
		if t := e.Type(); t != nil && !exp.DataTypeRealTypes.Has(t.DataTypeThis()) {
			a.SetType(e, a.MaybeCoerce(t, exp.DTypeDouble))
		}
	}
	return e
}

// AnnotateDot mirrors TypeAnnotator._annotate_dot.
func (a *TypeAnnotator) AnnotateDot(e *exp.Expr) *exp.Expr {
	a.SetType(e, nil)
	// Propagate type from qualified UDF calls (e.g., db.my_udf(...))
	if e.Expression().IsKind(exp.Anonymous) {
		a.SetType(e, typeOrNil(e.Expression()))
		return e
	}
	if thisType := e.This().Type(); thisType != nil && thisType.IsType(exp.DTypeStruct) {
		for _, x := range thisType.Expressions() {
			if x.Name() == e.Expression().Name() {
				a.SetType(e, x.Get("kind"))
				break
			}
		}
	}
	return e
}

// AnnotateExplode mirrors TypeAnnotator._annotate_explode.
func (a *TypeAnnotator) AnnotateExplode(e *exp.Expr) *exp.Expr {
	if inputType := e.This().Type(); inputType != nil && inputType.IsType(exp.DTypeArray) {
		a.SetType(e, seqGet(inputType.Expressions(), 0))
	} else {
		a.SetType(e, nil)
	}
	return e
}

// AnnotateUnnest mirrors TypeAnnotator._annotate_unnest.
func (a *TypeAnnotator) AnnotateUnnest(e *exp.Expr) *exp.Expr {
	child := seqGet(e.Expressions(), 0)
	var exprType TypeValue
	if child != nil && child.IsType(exp.DTypeArray) {
		exprType = seqGet(child.Type().Expressions(), 0)
	}
	a.SetType(e, exprType)
	return e
}

// AnnotateSubquery mirrors TypeAnnotator._annotate_subquery.
func (a *TypeAnnotator) AnnotateSubquery(e *exp.Expr) *exp.Expr {
	// For scalar subqueries (subqueries with a single projection), infer the type
	// from that single projection.
	query := e.Unnest()
	if query.Is(exp.Query) {
		if query.Str("kind") == "STRUCT" {
			qt, _ := query.MetaGet("query_type").(*exp.Expr)
			a.SetType(e, qt)
			return e
		}
		if selects := query.Selects(); len(selects) == 1 {
			a.SetType(e, typeOrNil(selects[0]))
			return e
		}
	}
	a.SetType(e, exp.DTypeUnknown)
	return e
}

func (a *TypeAnnotator) annotateStructValue(e *exp.Expr) (TypeValue, bool) {
	// Case: STRUCT(key AS value)
	var this *exp.Expr
	kind := e.Type()
	if alias := e.Get("alias"); alias != nil {
		this = alias.Copy()
	} else if e.Expression() != nil {
		// Case: STRUCT(key = value) or STRUCT(key := value)
		this = e.This().Copy()
		kind = e.Expression().Type()
	} else if e.IsKind(exp.Column) {
		// Case: STRUCT(c)
		this = e.This().Copy()
	}
	if kind != nil && kind.IsType(exp.DTypeUnknown) {
		return nil, false
	}
	if this != nil {
		return exp.New(exp.ColumnDef, "this", this, "kind", kind), true
	}
	if kind == nil {
		return nil, false
	}
	return kind, true
}

// AnnotateStruct mirrors TypeAnnotator._annotate_struct.
func (a *TypeAnnotator) AnnotateStruct(e *exp.Expr) *exp.Expr {
	var expressions []*exp.Expr
	for _, expr := range e.Expressions() {
		fieldType, ok := a.annotateStructValue(expr)
		if !ok {
			a.SetType(e, nil)
			return e
		}
		expressions = append(expressions, typeValueExpr(fieldType))
	}
	a.SetType(e, exp.New(exp.DataType, "this", exp.DTypeStruct, "expressions", expressions, "nested", true))
	return e
}

// AnnotateMap mirrors TypeAnnotator._annotate_map.
func (a *TypeAnnotator) AnnotateMap(e *exp.Expr) *exp.Expr {
	keys, values := e.Get("keys"), e.Get("values")
	mapType := exp.New(exp.DataType, "this", exp.DTypeMap)
	if keys.IsKind(exp.Array) && values.IsKind(exp.Array) {
		keyType := seqGet(keys.Type().Expressions(), 0)
		valueType := seqGet(values.Type().Expressions(), 0)
		if keyType == nil || keyType.IsType(exp.DTypeUnknown) || valueType == nil || valueType.IsType(exp.DTypeUnknown) {
			a.SetType(e, exp.DTypeUnknown)
			return e
		}
		mapType.Set("expressions", []*exp.Expr{keyType, valueType})
		mapType.Set("nested", true)
	}
	a.SetType(e, mapType)
	return e
}

// AnnotateToMap mirrors TypeAnnotator._annotate_to_map.
func (a *TypeAnnotator) AnnotateToMap(e *exp.Expr) *exp.Expr {
	mapType := exp.New(exp.DataType, "this", exp.DTypeMap)
	arg := e.This()
	if arg.IsType(exp.DTypeStruct) {
		for _, coldef := range arg.Type().Expressions() {
			kind := coldef.Get("kind")
			if kind.DataTypeThis() != exp.DTypeUnknown || len(kind.Expressions()) > 0 {
				mapType.Set("expressions", []*exp.Expr{exp.DTypeVarchar.IntoExpr(), kind})
				mapType.Set("nested", true)
				break
			}
		}
	}
	a.SetType(e, mapType)
	return e
}

// AnnotateExtract mirrors TypeAnnotator._annotate_extract.
func (a *TypeAnnotator) AnnotateExtract(e *exp.Expr) *exp.Expr {
	part := e.Name()
	switch {
	case part == "TIME":
		a.SetType(e, exp.DTypeTime)
	case part == "DATE":
		a.SetType(e, exp.DTypeDate)
	case BigintExtractDateParts[part]:
		a.SetType(e, exp.DTypeBigint)
	default:
		a.SetType(e, exp.DTypeInt)
	}
	return e
}

// AnnotateWithinGroup mirrors TypeAnnotator._annotate_within_group.
func (a *TypeAnnotator) AnnotateWithinGroup(e *exp.Expr) *exp.Expr {
	if e.This().IsKind(exp.PercentileDisc) {
		var sortType TypeValue = exp.DTypeUnknown
		if order := e.Get("expression"); order != nil && len(order.Expressions()) > 0 {
			sortType = typeOrNil(order.Expressions()[0].This())
		}
		a.SetType(e, sortType)
		return e
	}
	return a.AnnotateByArgs(e, []any{"this"}, false, false)
}

// AnnotateByArrayElement mirrors TypeAnnotator._annotate_by_array_element.
func (a *TypeAnnotator) AnnotateByArrayElement(e *exp.Expr) *exp.Expr {
	arrayArg := e.This()
	if arrayArg.Type().IsType(exp.DTypeArray) {
		var elementType TypeValue = seqGet(arrayArg.Type().Expressions(), 0)
		if isNilType(elementType) {
			elementType = exp.DTypeUnknown
		}
		a.SetType(e, elementType)
	} else {
		a.SetType(e, exp.DTypeUnknown)
	}
	return e
}
