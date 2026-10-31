package sqlglot

// Port of sqlglot/schema.py.

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/trie"
)

// SchemaMapping is a nested, ordered schema mapping (Python's dict[str, object]). Values are
// nested *SchemaMapping values, type strings, *exp.Expr DataTypes or nil.
type SchemaMapping = OrderedMap[any]

// M builds a SchemaMapping from alternating key/value pairs, preserving their order, e.g.
// M("t", M("a", "INT", "b", "TEXT")).
func M(kv ...any) *SchemaMapping {
	m := NewOrderedMap[any]()
	for i := 0; i+1 < len(kv); i += 2 {
		m.Set(fmt.Sprint(kv[i]), toSchemaValue(kv[i+1]))
	}
	return m
}

// toSchemaValue converts Go maps (sorted by key) into SchemaMappings.
func toSchemaValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m := NewOrderedMap[any]()
		for _, k := range keys {
			m.Set(k, toSchemaValue(x[k]))
		}
		return m
	case map[string]string:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m := NewOrderedMap[any]()
		for _, k := range keys {
			m.Set(k, x[k])
		}
		return m
	case *OrderedMap[any]:
		return x
	}
	return v
}

// Schema mirrors schema.Schema, the abstract schema interface.
type Schema interface {
	Dialect() *Dialect
	AddTable(table any, columnMapping any, dialect any, normalize *bool, matchDepth bool) error
	ColumnNames(table any, onlyVisible bool, dialect any, normalize *bool) ([]string, error)
	GetColumnType(table any, column any, dialect any, normalize *bool) (*exp.Expr, error)
	HasColumn(table any, column any, dialect any, normalize *bool) (bool, error)
	GetUDFType(udf any, dialect any, normalize *bool) (*exp.Expr, error)
	SupportedTableArgs() ([]string, error)
	Empty() bool
}

type tableCacheKey struct {
	table       exp.ExprKey
	ensureTypes bool
}

// MappingSchema mirrors schema.MappingSchema: a schema defined by a nested mapping of
// tables to columns to types.
type MappingSchema struct {
	Mapping    *SchemaMapping
	UDFMapping *SchemaMapping
	Visible    *SchemaMapping
	Normalize  bool

	dialect            *Dialect
	mappingTrie        *trie.Node[string]
	udfTrie            *trie.Node[string]
	supportedTableArgs []string
	typeMappingCache   map[string]*exp.Expr
	findCache          map[tableCacheKey]any
	depth              int
}

// MappingSchemaOptions are the optional MappingSchema constructor arguments.
type MappingSchemaOptions struct {
	Visible     *SchemaMapping
	Dialect     any
	NoNormalize bool // normalize=False
	UDFMapping  *SchemaMapping
}

// NewMappingSchema mirrors MappingSchema(schema, visible=None, dialect=None, normalize=True,
// udf_mapping=None). schema may be nil, a *SchemaMapping or a Go map.
func NewMappingSchema(schema any, o MappingSchemaOptions) (s *MappingSchema, err error) {
	defer recoverSchema(&err)
	s = &MappingSchema{
		Visible:          o.Visible,
		Normalize:        !o.NoNormalize,
		typeMappingCache: map[string]*exp.Expr{},
		findCache:        map[tableCacheKey]any{},
	}
	if s.Visible == nil {
		s.Visible = NewOrderedMap[any]()
	}
	d, err := GetDialect(o.Dialect)
	if err != nil {
		return nil, err
	}
	s.dialect = d
	mapping, _ := toSchemaValue(schema).(*SchemaMapping)
	if mapping == nil {
		mapping = NewOrderedMap[any]()
	}
	udfMapping := o.UDFMapping
	if udfMapping == nil {
		udfMapping = NewOrderedMap[any]()
	}
	if s.Normalize {
		mapping = s.normalizeSchema(mapping)
		udfMapping = s.normalizeUDFs(udfMapping)
	}
	s.Mapping = mapping
	s.UDFMapping = udfMapping
	s.mappingTrie = &trie.Node[string]{}
	for _, t := range flattenSchema(s.Mapping, s.Depth()) {
		slices.Reverse(t)
		s.mappingTrie.Add(t)
	}
	s.udfTrie = &trie.Node[string]{}
	for _, t := range flattenSchema(s.UDFMapping, dictDepth(s.UDFMapping)) {
		slices.Reverse(t)
		s.udfTrie.Add(t)
	}
	return s, nil
}

// MustMappingSchema is NewMappingSchema that panics on error.
func MustMappingSchema(schema any, o MappingSchemaOptions) *MappingSchema {
	s, err := NewMappingSchema(schema, o)
	if err != nil {
		panic(err)
	}
	return s
}

func recoverSchema(err *error) {
	if r := recover(); r != nil {
		switch x := r.(type) {
		case *errs.SchemaError:
			*err = x
		case *errs.ParseError:
			*err = x
		case error:
			*err = x
		default:
			panic(r)
		}
	}
}

func schemaErrorf(format string, args ...any) {
	panic(&errs.SchemaError{Message: fmt.Sprintf(format, args...)})
}

// Dialect returns the schema's dialect.
func (s *MappingSchema) Dialect() *Dialect { return s.dialect }

// Empty mirrors AbstractMappingSchema.empty.
func (s *MappingSchema) Empty() bool { return s.Mapping.Len() == 0 }

// Depth mirrors MappingSchema.depth: the nesting depth of tables (the columns level excluded).
func (s *MappingSchema) Depth() int {
	if !s.Empty() && s.depth == 0 {
		// The columns themselves are a mapping, but we don't want to include those
		s.depth = dictDepth(s.Mapping) - 1
	}
	return s.depth
}

// SupportedTableArgs mirrors AbstractMappingSchema.supported_table_args.
func (s *MappingSchema) SupportedTableArgs() (args []string, err error) {
	defer recoverSchema(&err)
	return s.supportedTableArgsOrPanic(), nil
}

func (s *MappingSchema) supportedTableArgsOrPanic() []string {
	if len(s.supportedTableArgs) == 0 && s.Mapping.Len() > 0 {
		depth := s.Depth()
		switch {
		case depth == 0:
			s.supportedTableArgs = []string{}
		case depth >= 1 && depth <= 3:
			s.supportedTableArgs = exp.TableParts[:depth]
		default:
			schemaErrorf("Invalid mapping shape. Depth: %d", depth)
		}
	}
	return s.supportedTableArgs
}

func schemaTableParts(table *exp.Expr) []string {
	parts := table.Parts()
	out := make([]string, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- {
		out = append(out, parts[i].Name())
	}
	return out
}

func (s *MappingSchema) udfParts(udf *exp.Expr) []string {
	// a.b.c(...) is represented as Dot(Dot(a, b), Anonymous(c, ...))
	var parts []string
	if parent := udf.Parent(); parent.IsKind(exp.Dot) {
		for _, p := range parent.Flatten(true) {
			parts = append(parts, p.Name())
		}
	} else {
		parts = []string{udf.Name()}
	}
	slices.Reverse(parts)
	if d := dictDepth(s.UDFMapping); d < len(parts) {
		parts = parts[:d]
	}
	return parts
}

func (s *MappingSchema) findInTrie(parts []string, t *trie.Node[string], raiseOnMissing bool) []string {
	value, node := t.Lookup(parts)
	if value == trie.Failed {
		return nil
	}
	if value == trie.Prefix {
		possibilities := flattenTrie(node)
		if len(possibilities) == 1 {
			parts = append(append([]string{}, parts...), possibilities[0]...)
		} else {
			if raiseOnMissing {
				var msgs []string
				for _, p := range possibilities {
					msgs = append(msgs, strings.Join(p, "."))
				}
				schemaErrorf("Ambiguous mapping for %s: %s.", strings.Join(parts, "."), strings.Join(msgs, ", "))
			}
			return nil
		}
	}
	return parts
}

// flattenTrie mirrors flatten_schema(trie) for a trie node: the key paths below it.
func flattenTrie(t *trie.Node[string]) [][]string {
	var out [][]string
	var walk func(n *trie.Node[string], prefix []string)
	walk = func(n *trie.Node[string], prefix []string) {
		if n.End && len(prefix) > 0 {
			out = append(out, append([]string{}, prefix...))
		}
		keys := make([]string, 0, len(n.Children))
		for k := range n.Children {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walk(n.Children[k], append(prefix, k))
		}
	}
	walk(t, nil)
	return out
}

// Find mirrors MappingSchema.find(table, raise_on_missing=True, ensure_data_types=False): the
// column mapping of a table (nil if not found).
func (s *MappingSchema) Find(table *exp.Expr, raiseOnMissing, ensureDataTypes bool) (result *SchemaMapping, err error) {
	defer recoverSchema(&err)
	return s.find(table, raiseOnMissing, ensureDataTypes), nil
}

func (s *MappingSchema) find(table *exp.Expr, raiseOnMissing, ensureDataTypes bool) *SchemaMapping {
	key := tableCacheKey{table.MapKey(), ensureDataTypes}
	if cached, ok := s.findCache[key]; ok && cached != nil {
		m, _ := cached.(*SchemaMapping)
		return m
	}
	supported := s.supportedTableArgsOrPanic()
	parts := schemaTableParts(table)
	if len(parts) > len(supported) {
		parts = parts[:len(supported)]
	}
	resolved := s.findInTrie(parts, s.mappingTrie, raiseOnMissing)
	if resolved == nil {
		return nil
	}
	value := s.nestedGet(resolved, s.Mapping, raiseOnMissing)
	m, ok := value.(*SchemaMapping)
	if !ok {
		if value != nil {
			// a non-dict value (e.g. a column type at the wrong depth)
			s.findCache[key] = nil
		}
		return nil
	}
	if ensureDataTypes {
		converted := NewOrderedMap[any]()
		for col, dtype := range m.All() {
			if str, ok := dtype.(string); ok {
				converted.Set(col, s.toDataType(str, nil))
			} else {
				converted.Set(col, dtype)
			}
		}
		m = converted
	}
	s.findCache[key] = m
	return m
}

func (s *MappingSchema) nestedGet(parts []string, d *SchemaMapping, raiseOnMissing bool) any {
	supported := s.supportedTableArgsOrPanic()
	var path [][2]string
	for i := 0; i < len(supported) && i < len(parts); i++ {
		path = append(path, [2]string{supported[i], parts[len(parts)-1-i]})
	}
	return nestedGet(d, path, raiseOnMissing)
}

// Copy mirrors MappingSchema.copy.
func (s *MappingSchema) Copy() *MappingSchema {
	n, err := NewMappingSchema(s.Mapping.Copy(), MappingSchemaOptions{
		Visible:     s.Visible.Copy(),
		Dialect:     s.dialect,
		NoNormalize: !s.Normalize,
		UDFMapping:  s.UDFMapping.Copy(),
	})
	if err != nil {
		panic(err)
	}
	return n
}

// AddTable mirrors MappingSchema.add_table(table, column_mapping=None, dialect=None,
// normalize=None, match_depth=True).
func (s *MappingSchema) AddTable(table any, columnMapping any, dialect any, normalize *bool, matchDepth bool) (err error) {
	defer recoverSchema(&err)
	normalizedTable := s.normalizeTable(table, dialect, normalize)
	if matchDepth && !s.Empty() && len(normalizedTable.Parts()) != s.Depth() {
		schemaErrorf("Table %s must match the schema's nesting level: %d.", normalizedTable.SQL(s.dialect), s.Depth())
	}
	normalizedColumns := NewOrderedMap[any]()
	for k, v := range ensureColumnMapping(columnMapping).All() {
		normalizedColumns.Set(s.normalizeName(k, dialect, false, normalize), v)
	}
	if schema := s.find(normalizedTable, false, false); schema != nil && normalizedColumns.Len() == 0 {
		return nil
	}
	parts := schemaTableParts(normalizedTable)
	reversed := slices.Clone(parts)
	slices.Reverse(reversed)
	nestedSet(s.Mapping, reversed, normalizedColumns)
	s.mappingTrie.Add(parts)
	delete(s.findCache, tableCacheKey{normalizedTable.MapKey(), true})
	delete(s.findCache, tableCacheKey{normalizedTable.MapKey(), false})
	return nil
}

// ColumnNames mirrors MappingSchema.column_names.
func (s *MappingSchema) ColumnNames(table any, onlyVisible bool, dialect any, normalize *bool) (names []string, err error) {
	defer recoverSchema(&err)
	normalizedTable := s.normalizeTable(table, dialect, normalize)
	schema := s.find(normalizedTable, true, false)
	if schema == nil {
		return []string{}, nil
	}
	if !onlyVisible || s.Visible.Len() == 0 {
		return schema.Keys(), nil
	}
	visible := s.nestedGet(schemaTableParts(normalizedTable), s.Visible, true)
	var vis []string
	switch v := visible.(type) {
	case []string:
		vis = v
	case []any:
		for _, x := range v {
			vis = append(vis, fmt.Sprint(x))
		}
	}
	for _, col := range schema.Keys() {
		if slices.Contains(vis, col) {
			names = append(names, col)
		}
	}
	return names, nil
}

// GetColumnType mirrors MappingSchema.get_column_type.
func (s *MappingSchema) GetColumnType(table any, column any, dialect any, normalize *bool) (t *exp.Expr, err error) {
	defer recoverSchema(&err)
	normalizedTable := s.normalizeTable(table, dialect, normalize)
	var name any = column
	if c, ok := column.(*exp.Expr); ok {
		name = c.This()
	}
	normalizedColumnName := s.normalizeName(name, dialect, false, normalize)
	if tableSchema := s.find(normalizedTable, false, false); tableSchema.Len() > 0 {
		switch ct := mustGet(tableSchema, normalizedColumnName).(type) {
		case *exp.Expr:
			if ct.IsKind(exp.DataType) {
				return ct, nil
			}
		case string:
			return s.toDataType(ct, dialect), nil
		}
	}
	return exp.DTypeUnknown.IntoExpr(), nil
}

func mustGet(m *SchemaMapping, k string) any {
	v, _ := m.Get(k)
	return v
}

// GetUDFType mirrors MappingSchema.get_udf_type.
func (s *MappingSchema) GetUDFType(udf any, dialect any, normalize *bool) (t *exp.Expr, err error) {
	defer recoverSchema(&err)
	parts := s.normalizeUDF(udf, dialect, normalize)
	resolved := s.findInTrie(parts, s.udfTrie, false)
	if resolved == nil {
		return exp.DTypeUnknown.IntoExpr(), nil
	}
	var path [][2]string
	for i := range resolved {
		path = append(path, [2]string{resolved[i], resolved[len(resolved)-1-i]})
	}
	switch ut := nestedGet(s.UDFMapping, path, false).(type) {
	case *exp.Expr:
		return ut, nil
	case string:
		return s.toDataType(ut, dialect), nil
	}
	return exp.DTypeUnknown.IntoExpr(), nil
}

// HasColumn mirrors MappingSchema.has_column.
func (s *MappingSchema) HasColumn(table any, column any, dialect any, normalize *bool) (ok bool, err error) {
	defer recoverSchema(&err)
	normalizedTable := s.normalizeTable(table, dialect, normalize)
	var name any = column
	if c, ok := column.(*exp.Expr); ok {
		name = c.This()
	}
	normalizedColumnName := s.normalizeName(name, dialect, false, normalize)
	tableSchema := s.find(normalizedTable, false, false)
	return tableSchema.Has(normalizedColumnName), nil
}

func (s *MappingSchema) normalizeSchema(schema *SchemaMapping) *SchemaMapping {
	normalized := NewOrderedMap[any]()
	flattened := flattenSchema(schema, dictDepth(schema)-1)
	errorMsg := "Table %s must match the schema's nesting level: %d."
	for _, keys := range flattened {
		var path [][2]string
		for _, k := range keys {
			path = append(path, [2]string{k, k})
		}
		columns, ok := nestedGet(schema, path, true).(*SchemaMapping)
		if !ok {
			schemaErrorf(errorMsg, strings.Join(keys[:len(keys)-1], "."), len(flattened[0]))
		}
		if columns.Len() == 0 {
			schemaErrorf("Table %s must have at least one column", strings.Join(keys[:len(keys)-1], "."))
		}
		if _, ok := columns.Values()[0].(*SchemaMapping); ok {
			schemaErrorf(errorMsg, strings.Join(append(append([]string{}, keys...), flattenSchema(columns, dictDepth(columns)-1)[0]...), "."),
				len(flattened[0]))
		}
		var normalizedKeys []string
		for _, k := range keys {
			normalizedKeys = append(normalizedKeys, s.normalizeName(k, nil, true, nil))
		}
		for columnName, columnType := range columns.All() {
			nestedSet(normalized, append(append([]string{}, normalizedKeys...), s.normalizeName(columnName, nil, false, nil)), columnType)
		}
	}
	return normalized
}

func (s *MappingSchema) normalizeUDFs(udfs *SchemaMapping) *SchemaMapping {
	normalized := NewOrderedMap[any]()
	for _, keys := range flattenSchema(udfs, dictDepth(udfs)) {
		var path [][2]string
		for _, k := range keys {
			path = append(path, [2]string{k, k})
		}
		udfType := nestedGet(udfs, path, true)
		var normalizedKeys []string
		for _, k := range keys {
			normalizedKeys = append(normalizedKeys, s.normalizeName(k, nil, true, nil))
		}
		nestedSet(normalized, normalizedKeys, udfType)
	}
	return normalized
}

func (s *MappingSchema) resolveDialect(dialect any) *Dialect {
	if dialect == nil {
		return s.dialect
	}
	if d, ok := dialect.(*Dialect); ok && d == nil {
		return s.dialect
	}
	if str, ok := dialect.(string); ok && str == "" {
		return s.dialect
	}
	return MustDialect(dialect)
}

func (s *MappingSchema) normalizeUDF(udf any, dialect any, normalize *bool) []string {
	d := s.resolveDialect(dialect)
	norm := s.Normalize
	if normalize != nil {
		norm = *normalize
	}
	var u *exp.Expr
	switch x := udf.(type) {
	case string:
		parsed := exp.MustMaybeParse(x, exp.ParseOptions{Dialect: d}, "", true)
		if parsed.IsKind(exp.Anonymous) {
			u = parsed
		} else if parsed.IsKind(exp.Dot) && parsed.Expression().IsKind(exp.Anonymous) {
			u = parsed.Expression()
		} else {
			schemaErrorf("Unable to parse UDF from: '%s'", x)
		}
	case *exp.Expr:
		u = x
	}
	parts := s.udfParts(u)
	if norm {
		for i, p := range parts {
			parts[i] = s.normalizeName(p, d, true, nil)
		}
	}
	return parts
}

func (s *MappingSchema) normalizeTable(table any, dialect any, normalize *bool) *exp.Expr {
	d := s.resolveDialect(dialect)
	norm := s.Normalize
	if normalize != nil {
		norm = *normalize
	}
	normalizedTable := exp.MustMaybeParse(table, exp.ParseOptions{Dialect: d, Into: []exp.Kind{exp.Table}}, "", norm)
	if norm {
		for _, part := range normalizedTable.Parts() {
			if part.IsKind(exp.Identifier) {
				part.Replace(NormalizeName(part, d, true, true))
			}
		}
	}
	return normalizedTable
}

func (s *MappingSchema) normalizeName(name any, dialect any, isTable bool, normalize *bool) string {
	norm := s.Normalize
	if normalize != nil {
		norm = *normalize
	}
	d := s.resolveDialect(dialect)
	return NormalizeName(name, d, isTable, norm).Name()
}

func (s *MappingSchema) toDataType(schemaType string, dialect any) *exp.Expr {
	if t, ok := s.typeMappingCache[schemaType]; ok {
		return t
	}
	d := s.dialect
	if dialect != nil {
		d = s.resolveDialect(dialect)
	}
	udt := d.SupportsUserDefinedTypes
	e, err := exp.DataTypeBuild(schemaType, d, udt)
	if err != nil {
		inDialect := ""
		if d != nil {
			inDialect = " in dialect " + d.ClassName
		}
		schemaErrorf("Failed to build type '%s'%s.", schemaType, inDialect)
	}
	e.Transform(d.NormalizeIdentifier, false)
	s.typeMappingCache[schemaType] = e
	return e
}

// NormalizeName mirrors schema.normalize_name(identifier, dialect=None, is_table=False,
// normalize=True).
func NormalizeName(identifier any, dialect any, isTable bool, normalize bool) *exp.Expr {
	d := MustDialect(dialect)
	var id *exp.Expr
	switch x := identifier.(type) {
	case string:
		id = exp.ParseIdentifier(x, d)
	case *exp.Expr:
		id = x
		if normalize {
			id = x.Copy()
		}
	}
	if !normalize {
		return id
	}
	// this is used for normalize_identifier, bigquery has special rules pertaining tables
	id.Meta()["is_table"] = isTable
	return d.NormalizeIdentifier(id)
}

// EnsureSchema mirrors schema.ensure_schema: it returns schema if it already is a Schema,
// otherwise a MappingSchema built from it.
func EnsureSchema(schema any, dialect any) (Schema, error) {
	if s, ok := schema.(Schema); ok && s != nil {
		if ms, ok := s.(*MappingSchema); ok && ms == nil {
			return NewMappingSchema(nil, MappingSchemaOptions{Dialect: dialect})
		}
		return s, nil
	}
	return NewMappingSchema(schema, MappingSchemaOptions{Dialect: dialect})
}

// ensureColumnMapping mirrors schema.ensure_column_mapping.
func ensureColumnMapping(mapping any) *SchemaMapping {
	switch x := mapping.(type) {
	case nil:
		return NewOrderedMap[any]()
	case *SchemaMapping:
		return x
	case map[string]any, map[string]string:
		return toSchemaValue(x).(*SchemaMapping)
	case string:
		m := NewOrderedMap[any]()
		for _, s := range strings.Split(x, ",") {
			s = strings.TrimSpace(s)
			name, typ, _ := strings.Cut(s, ":")
			m.Set(strings.TrimSpace(name), strings.TrimSpace(typ))
		}
		return m
	case []string:
		m := NewOrderedMap[any]()
		for _, s := range x {
			m.Set(strings.TrimSpace(s), nil)
		}
		return m
	}
	panic(fmt.Errorf("Invalid mapping provided: %T", mapping))
}

// dictDepth mirrors helper.dict_depth.
func dictDepth(v any) int {
	m, ok := v.(*SchemaMapping)
	if !ok {
		return 0
	}
	if m.Len() == 0 {
		return 1
	}
	return 1 + dictDepth(m.Values()[0])
}

// flattenSchema mirrors schema.flatten_schema(schema, depth, keys).
func flattenSchema(schema *SchemaMapping, depth int) [][]string {
	return flattenSchemaKeys(schema, depth, nil)
}

func flattenSchemaKeys(schema *SchemaMapping, depth int, keys []string) [][]string {
	var tables [][]string
	for k, v := range schema.All() {
		sub, isDict := v.(*SchemaMapping)
		if depth == 1 || !isDict {
			tables = append(tables, append(append([]string{}, keys...), k))
		} else if depth >= 2 {
			tables = append(tables, flattenSchemaKeys(sub, depth-1, append(append([]string{}, keys...), k))...)
		}
	}
	return tables
}

// nestedGet mirrors schema.nested_get(d, *path, raise_on_missing=True).
func nestedGet(d *SchemaMapping, path [][2]string, raiseOnMissing bool) any {
	var result any = d
	for _, p := range path {
		name, key := p[0], p[1]
		m, ok := result.(*SchemaMapping)
		if !ok {
			result = nil
		} else {
			result, _ = m.Get(key)
		}
		if result == nil {
			if raiseOnMissing {
				if name == "this" {
					name = "table"
				}
				panic(fmt.Errorf("Unknown %s: %s", name, key))
			}
			return nil
		}
	}
	return result
}

// nestedSet mirrors schema.nested_set.
func nestedSet(d *SchemaMapping, keys []string, value any) *SchemaMapping {
	if len(keys) == 0 {
		return d
	}
	if len(keys) == 1 {
		d.Set(keys[0], value)
		return d
	}
	subd := d
	for _, key := range keys[:len(keys)-1] {
		v, ok := subd.Get(key)
		if !ok {
			n := NewOrderedMap[any]()
			subd.Set(key, n)
			subd = n
		} else {
			subd = v.(*SchemaMapping)
		}
	}
	subd.Set(keys[len(keys)-1], value)
	return d
}
