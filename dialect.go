package sqlglot

import (
	"fmt"
	"maps"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
	"github.com/cottrellashley/sqlglot-go/tokens"
	"github.com/cottrellashley/sqlglot-go/trie"
)

// NormalizationStrategy mirrors sqlglot's NormalizationStrategy.
type NormalizationStrategy string

const (
	Lowercase                NormalizationStrategy = "LOWERCASE"
	Uppercase                NormalizationStrategy = "UPPERCASE"
	CaseSensitive            NormalizationStrategy = "CASE_SENSITIVE"
	CaseInsensitive          NormalizationStrategy = "CASE_INSENSITIVE"
	CaseInsensitiveUppercase NormalizationStrategy = "CASE_INSENSITIVE_UPPERCASE"
)

// UsingColumnOrder mirrors sqlglot's UsingColumnOrder.
type UsingColumnOrder string

const (
	UsingList UsingColumnOrder = "USING_LIST"
	LeftTable UsingColumnOrder = "LEFT_TABLE"
	InPlace   UsingColumnOrder = "IN_PLACE"
)

// UnescapedSequences mirrors dialect.UNESCAPED_SEQUENCES.
var UnescapedSequences = map[string]string{
	"\\a":  "\a",
	"\\b":  "\b",
	"\\f":  "\f",
	"\\n":  "\n",
	"\\r":  "\r",
	"\\t":  "\t",
	"\\v":  "\v",
	"\\\\": "\\",
}

// StrictTimeFormats mirrors dialect.STRICT_TIME_FORMATS.
var StrictTimeFormats = [][2]string{
	{"%mstrict", "%m"},
	{"%dstrict", "%d"},
	{"%Hstrict", "%H"},
	{"%Istrict", "%I"},
	{"%Mstrict", "%M"},
	{"%Sstrict", "%S"},
}

func withStrictTimeInverse(inverse map[string]string) map[string]string {
	for _, sl := range StrictTimeFormats {
		strict, lax := sl[0], sl[1]
		if v, ok := inverse[strict]; ok {
			if _, ok := inverse[lax]; !ok {
				inverse[lax] = v
			}
		} else if _, ok := inverse[strict]; !ok {
			if v, ok := inverse[lax]; ok {
				inverse[strict] = v
			} else {
				inverse[strict] = lax
			}
		}
	}
	return inverse
}

// Bool returns a pointer to b (for optional boolean settings).
func Bool(b bool) *bool { return &b }

// DialectMethods holds the overridable Dialect methods (Python methods that dialects
// override, e.g. Snowflake.can_quote, DuckDB.to_json_path).
type DialectMethods struct {
	NormalizeIdentifier   func(d *Dialect, e *exp.Expr) *exp.Expr
	CaseSensitive         func(d *Dialect, text string) bool
	CanQuote              func(d *Dialect, identifier *exp.Expr, identify any) bool
	QuoteIdentifier       func(d *Dialect, e *exp.Expr, identify bool) *exp.Expr
	ToJSONPath            func(d *Dialect, path *exp.Expr) *exp.Expr
	GenerateValuesAliases func(d *Dialect, e *exp.Expr) []*exp.Expr
}

// DialectClass holds the class-level configuration of a dialect: its settings and the
// configurations of its tokenizer, parser and generator. It mirrors a Python Dialect
// subclass. Use NewDialectClass to derive a dialect from another one.
type DialectClass struct {
	// Name is the registry name ("" for the base dialect, "duckdb", ...).
	Name string
	// ClassName is the Python class name used in messages ("Dialect", "DuckDB", ...).
	ClassName string

	IndexOffset                              int
	WeekOffset                               int
	UnnestColumnOnly                         bool
	AliasPostTablesample                     bool
	TablesampleSizeIsPercent                 bool
	NormalizationStrategy                    NormalizationStrategy
	AsciiOnlyNormalization                   bool
	IdentifiersCanStartWithDigit             bool
	DpipeIsStringConcat                      bool
	StrictStringConcat                       bool
	SupportsUserDefinedTypes                 bool
	SupportsColumnJoinMarks                  bool
	CopyParamsAreCsv                         bool
	NormalizeFunctions                       any // "upper", "lower" or false
	OriginalNameMetaKey                      string
	LogBaseFirst                             *bool
	NullOrdering                             string
	TypedDivision                            bool
	SafeDivision                             bool
	ConcatCoalesce                           bool
	ConcatWsCoalesce                         bool
	HexLowercase                             bool
	DateFormat                               string
	DateintFormat                            string
	TimeFormat                               string
	TimeMapping                              map[string]string
	FormatMapping                            map[string]string
	UnescapedSequences                       map[string]string
	InverseVectorTypeAliases                 map[string]string
	Pseudocolumns                            map[string]bool
	SupportsPositionalColumnRefs             bool
	PreferCteAliasColumn                     bool
	ForceEarlyAliasRefExpansion              bool
	ExpandOnlyGroupAliasRef                  bool
	AnnotateAllScopes                        bool
	DisablesAliasRefExpansion                bool
	SupportsAliasRefsInJoinConditions        bool
	SupportsOrderByAll                       bool
	SupportsLimitAll                         bool
	ProjectionAliasesShadowSourceNames       bool
	TableAliasesShadowColumnNames            bool
	TablesReferenceableAsColumns             bool
	SupportsStructStarExpansion              bool
	StarIlikeBackslashEscape                 bool
	ExcludesPseudocolumnsFromStar            bool
	UsingColumnOrder                         UsingColumnOrder
	QueryResultsAreStructs                   bool
	RequiresParenthesizedStructAccess        bool
	SupportsNullType                         bool
	CoalesceComparisonNonStandard            bool
	HasDistinctArrayConstructors             bool
	SupportsFixedSizeArrays                  bool
	StrictJsonPathSyntax                     bool
	JsonPathSingleDotIsWildcard              bool
	OnConditionEmptyBeforeError              bool
	ArrayAggIncludesNulls                    *bool
	ArrayFuncsPropagatesNulls                bool
	PromoteToInferredDatetimeType            bool
	SupportsValuesDefault                    bool
	NumbersCanBeUnderscoreSeparated          bool
	HexStringIsIntegerType                   bool
	RegexpExtractDefaultGroup                int
	RegexpExtractPositionOverflowReturnsNull bool
	SetOpDistinctByDefault                   map[exp.Kind]*bool
	IntersectBindsTighterThanUnionAndExcept  bool
	CreatableKindMapping                     map[string]string
	AlterTableSupportsCascade                bool
	AlterTableAddRequiredForEachColumn       bool
	AlterTableDropRequiredForEachColumn      bool
	TryCastRequiresString                    *bool
	SafeToEliminateDoubleNegation            bool
	NormalizeNotNull                         bool
	InitcapSupportsCustomDelimiters          bool
	InitcapDefaultDelimiterChars             string
	ByteStringIsBytesType                    bool
	UuidIsStringType                         bool
	JsonExtractScalarScalarOnly              bool
	DefaultFunctionsColumnNames              map[exp.Kind][]string
	DefaultNullType                          exp.DType
	LeastGreatestIgnoresNulls                bool
	PrioritizeNonLiteralTypes                bool
	AliasPostVersion                         bool
	DatePartMapping                          map[string]string
	CoercesTo                                map[exp.DType]exp.TypeSet
	SupportedSettings                        map[string]bool

	// InverseTimeMappingOverrides is the dialect's own INVERSE_TIME_MAPPING (not inherited).
	InverseTimeMappingOverrides map[string]string
	// TimeMappingOrder / FormatMappingOrder record the insertion order of TimeMapping /
	// FormatMapping keys (Python dicts are ordered; the inverse mapping depends on it).
	TimeMappingOrder   []string
	FormatMappingOrder []string

	// ExpressionMetadata drives annotate_types (sqlglot.typing).
	ExpressionMetadata map[exp.Kind]*ExprMetadata

	Tokenizer         *tokens.Config
	JSONPathTokenizer *tokens.Config
	Parser            *ParserConfig
	Generator         *GeneratorConfig

	Methods DialectMethods

	// Extra is free-form storage for dialect-specific class data.
	Extra map[string]any

	// derived (computed by Finalize, mirroring the _Dialect metaclass)
	TimeTrie                           *trie.Node[rune]
	FormatTrie                         *trie.Node[rune]
	InverseTimeMapping                 map[string]string
	InverseTimeTrie                    *trie.Node[rune]
	InverseFormatMapping               map[string]string
	InverseFormatTrie                  *trie.Node[rune]
	InverseCreatableKindMapping        map[string]string
	EscapedSequences                   map[string]string
	IdentifierEscapedSequences         map[string]string
	QuoteStart, QuoteEnd               string
	IdentifierStart, IdentifierEnd     string
	BitStart, BitEnd                   string
	HexStart, HexEnd                   string
	ByteStart, ByteEnd                 string
	UnicodeStart, UnicodeEnd           string
	StringsSupportEscapedSequences     bool
	ByteStringsSupportEscapedSequences bool
	ValidIntervalUnits                 map[string]bool

	finalized bool
}

// Dialect is a dialect instance: the class configuration plus instance settings
// (normalization strategy, version and free-form settings).
type Dialect struct {
	*DialectClass
	NormalizationStrategy NormalizationStrategy
	Version               [3]int
	Settings              map[string]any
}

// Clone returns a deep copy of the class configuration (used for dialect inheritance).
func (c *DialectClass) Clone() *DialectClass {
	n := *c
	n.TimeMapping = maps.Clone(c.TimeMapping)
	n.FormatMapping = maps.Clone(c.FormatMapping)
	n.UnescapedSequences = maps.Clone(c.UnescapedSequences)
	n.InverseVectorTypeAliases = maps.Clone(c.InverseVectorTypeAliases)
	n.Pseudocolumns = maps.Clone(c.Pseudocolumns)
	n.SetOpDistinctByDefault = maps.Clone(c.SetOpDistinctByDefault)
	n.CreatableKindMapping = maps.Clone(c.CreatableKindMapping)
	n.DefaultFunctionsColumnNames = maps.Clone(c.DefaultFunctionsColumnNames)
	n.DatePartMapping = maps.Clone(c.DatePartMapping)
	n.CoercesTo = maps.Clone(c.CoercesTo)
	n.SupportedSettings = maps.Clone(c.SupportedSettings)
	n.ExpressionMetadata = maps.Clone(c.ExpressionMetadata)
	n.Extra = maps.Clone(c.Extra)
	n.InverseTimeMappingOverrides = nil
	n.TimeMappingOrder = append([]string(nil), c.TimeMappingOrder...)
	n.FormatMappingOrder = append([]string(nil), c.FormatMappingOrder...)
	n.Tokenizer = c.Tokenizer.Clone()
	n.JSONPathTokenizer = c.JSONPathTokenizer.Clone()
	n.Parser = c.Parser.Clone()
	n.Generator = c.Generator.Clone()
	n.finalized = false
	return &n
}

// NewDialectClass derives a new dialect class named name from parent (nil means the base
// dialect). The returned class must be finalized (Finalize) after customization; Register
// does that automatically.
func NewDialectClass(name string, parent *DialectClass) *DialectClass {
	if parent == nil {
		parent = BaseDialectClass()
	}
	c := parent.Clone()
	c.Name = name
	return c
}

// Finalize computes the derived settings, mirroring the _Dialect metaclass.
func (c *DialectClass) Finalize() {
	if c.finalized {
		return
	}
	c.TimeTrie = trie.NewFromStrings(sortedKeys(c.TimeMapping)...)
	if len(c.FormatMapping) > 0 {
		c.FormatTrie = trie.NewFromStrings(sortedKeys(c.FormatMapping)...)
	} else {
		c.FormatTrie = c.TimeTrie
	}
	inv := map[string]string{}
	for _, k := range orderedKeys(c.TimeMapping, c.TimeMappingOrder) {
		inv[c.TimeMapping[k]] = k
	}
	// Python dict comprehension: later keys win; iterate in insertion order is not available
	// for Go maps, so dialects that rely on a specific winner set InverseTimeMappingOverrides.
	for k, v := range c.InverseTimeMappingOverrides {
		inv[k] = v
	}
	c.InverseTimeMapping = withStrictTimeInverse(inv)
	c.InverseTimeTrie = trie.NewFromStrings(sortedKeys(c.InverseTimeMapping)...)
	invFmt := map[string]string{}
	for _, k := range orderedKeys(c.FormatMapping, c.FormatMappingOrder) {
		invFmt[c.FormatMapping[k]] = k
	}
	c.InverseFormatMapping = withStrictTimeInverse(invFmt)
	c.InverseFormatTrie = trie.NewFromStrings(sortedKeys(c.InverseFormatMapping)...)

	c.InverseCreatableKindMapping = map[string]string{}
	for k, v := range c.CreatableKindMapping {
		c.InverseCreatableKindMapping[v] = k
	}

	// Remove transforms that correspond to unsupported JSONPathPart expressions
	if g := c.Generator; g != nil {
		for _, part := range AllJSONPathParts {
			if !g.SupportedJsonPathParts.Has(part) {
				delete(g.Transforms, part)
			}
		}
	}

	tok := c.Tokenizer
	tok.NumbersCanBeUnderscoreSeparated = c.NumbersCanBeUnderscoreSeparated
	tok.IdentifiersCanStartWithDigit = c.IdentifiersCanStartWithDigit
	tok.ByteStringsAreBytes = c.ByteStringIsBytesType

	c.QuoteStart, c.QuoteEnd = tok.Quotes[0].Start, tok.Quotes[0].End
	c.IdentifierStart, c.IdentifierEnd = tok.Identifiers[0].Start, tok.Identifiers[0].End

	firstFormat := func(qs []tokens.Delim) (string, string) {
		if len(qs) == 0 {
			return "", ""
		}
		return qs[0].Start, qs[0].End
	}
	c.BitStart, c.BitEnd = firstFormat(tok.BitStrings)
	c.HexStart, c.HexEnd = firstFormat(tok.HexStrings)
	c.ByteStart, c.ByteEnd = firstFormat(tok.ByteStrings)
	c.UnicodeStart, c.UnicodeEnd = firstFormat(tok.UnicodeStrings)

	c.StringsSupportEscapedSequences = contains(tok.StringEscapes, "\\")
	byteEscapes := tok.ByteStringEscapes
	if byteEscapes == nil {
		byteEscapes = tok.StringEscapes
	}
	c.ByteStringsSupportEscapedSequences = contains(byteEscapes, "\\")

	if c.StringsSupportEscapedSequences || c.ByteStringsSupportEscapedSequences {
		merged := maps.Clone(UnescapedSequences)
		for k, v := range c.UnescapedSequences {
			merged[k] = v
		}
		c.UnescapedSequences = merged
	}
	tok.UnescapedSequences = c.UnescapedSequences

	c.EscapedSequences = map[string]string{}
	for k, v := range c.UnescapedSequences {
		if v == "\\" || (!isPrintable(v) && k[1:] != v) {
			c.EscapedSequences[v] = k
		}
	}
	c.IdentifierEscapedSequences = map[string]string{}
	for k, v := range c.UnescapedSequences {
		if contains(tok.IdentifierEscapes, k[:1]) && contains(tok.IdentifierEscapes, v) {
			c.IdentifierEscapedSequences[v] = k
		}
	}
	format := ""
	if _, ok := tok.NumericEscapes["x"]; ok {
		format = "x"
	} else if _, ok := tok.NumericEscapes["0"]; ok {
		format = "o"
	} else if _, ok := tok.NumericEscapes["u"]; ok {
		format = "u"
	}
	if format != "" {
		for code := 0; code < 128; code++ {
			if (code < 32 || code == 127) && code != 9 && code != 10 && code != 13 {
				ch := string(rune(code))
				if _, ok := c.EscapedSequences[ch]; ok {
					continue
				}
				switch format {
				case "x":
					c.EscapedSequences[ch] = fmt.Sprintf("\\x%02x", code)
				case "o":
					c.EscapedSequences[ch] = fmt.Sprintf("\\%03o", code)
				case "u":
					c.EscapedSequences[ch] = fmt.Sprintf("\\u%04x", code)
				}
			}
		}
	}

	_, c.SupportsColumnJoinMarks = tok.Keywords["(+)"]

	if c.Name != "" && c.Name != "bigquery" && c.Name != "snowflake" {
		c.InitcapSupportsCustomDelimiters = false
	}

	units := maps.Clone(c.ValidIntervalUnits)
	if units == nil {
		units = map[string]bool{}
	}
	for k, v := range c.DatePartMapping {
		units[k] = true
		units[v] = true
	}
	c.ValidIntervalUnits = units

	tok.Finalize()
	if jt := c.JSONPathTokenizer; jt != nil {
		// The JSON path tokenizer is instantiated with the dialect, like the main tokenizer
		jt.NumbersCanBeUnderscoreSeparated = c.NumbersCanBeUnderscoreSeparated
		jt.IdentifiersCanStartWithDigit = c.IdentifiersCanStartWithDigit
		jt.ByteStringsAreBytes = c.ByteStringIsBytesType
		jt.UnescapedSequences = c.UnescapedSequences
		jt.Finalize()
	}
	c.Parser.finalize(c)
	c.Generator.finalize(c)
	c.finalized = true
}

func isPrintable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) && r != ' ' {
			return false
		}
	}
	return true
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// orderedKeys returns the keys of m in the given order (keys missing from order are appended
// sorted).
func orderedKeys(m map[string]string, order []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range order {
		if _, ok := m[k]; ok && !seen[k] {
			out = append(out, k)
			seen[k] = true
		}
	}
	for _, k := range sortedKeys(m) {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out
}

// SetTimeMapping sets TIME_MAPPING from ordered pairs.
func (c *DialectClass) SetTimeMapping(pairs [][2]string) {
	c.TimeMapping = map[string]string{}
	c.TimeMappingOrder = nil
	for _, kv := range pairs {
		if _, ok := c.TimeMapping[kv[0]]; !ok {
			c.TimeMappingOrder = append(c.TimeMappingOrder, kv[0])
		}
		c.TimeMapping[kv[0]] = kv[1]
	}
}

// SetFormatMapping sets FORMAT_MAPPING from ordered pairs.
func (c *DialectClass) SetFormatMapping(pairs [][2]string) {
	c.FormatMapping = map[string]string{}
	c.FormatMappingOrder = nil
	for _, kv := range pairs {
		if _, ok := c.FormatMapping[kv[0]]; !ok {
			c.FormatMappingOrder = append(c.FormatMappingOrder, kv[0])
		}
		c.FormatMapping[kv[0]] = kv[1]
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------------------
// registry

type dialectEntry struct {
	once  sync.Once
	build func() *DialectClass
	cls   *DialectClass
}

var (
	registryMu sync.RWMutex
	registry   = map[string]*dialectEntry{}
)

// Register registers a dialect under name. build is called lazily (once) the first time the
// dialect is requested; it must return a customized class derived with NewDialectClass.
func Register(name string, build func() *DialectClass) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = &dialectEntry{build: build}
}

// DialectNames returns the names of the registered dialects.
func DialectNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var out []string
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// HasDialect reports whether a dialect is registered.
func HasDialect(name string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	_, ok := registry[name]
	return ok
}

// GetDialectClass returns the class of a registered dialect.
func GetDialectClass(name string) (*DialectClass, error) {
	registryMu.RLock()
	entry, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("Unknown dialect '%s'.%s", name, helper.SuggestClosestMatch(name, DialectNames()))
	}
	entry.once.Do(func() {
		cls := entry.build()
		if cls.Name == "" && name != "" {
			cls.Name = name
		}
		cls.Finalize()
		entry.cls = cls
	})
	return entry.cls, nil
}

// MustDialectClass is GetDialectClass that panics.
func MustDialectClass(name string) *DialectClass {
	c, err := GetDialectClass(name)
	if err != nil {
		panic(err)
	}
	return c
}

// BaseDialectClass returns the base ("sqlglot") dialect class.
func BaseDialectClass() *DialectClass { return MustDialectClass("") }

// NewInstance creates a dialect instance with default settings.
func (c *DialectClass) NewInstance() *Dialect {
	d, err := c.NewInstanceWith(nil)
	if err != nil {
		panic(err)
	}
	return d
}

// NewInstanceWith creates a dialect instance with the given settings.
func (c *DialectClass) NewInstanceWith(settings map[string]any) (*Dialect, error) {
	d := &Dialect{DialectClass: c, Version: [3]int{math.MaxInt, 0, 0}, Settings: map[string]any{}}
	d.NormalizationStrategy = c.NormalizationStrategy
	for k, v := range settings {
		switch k {
		case "version":
			parts := strings.Split(fmt.Sprint(v), ".")
			for len(parts) < 3 {
				parts = append(parts, "0")
			}
			for i := 0; i < 3; i++ {
				n, err := strconv.Atoi(parts[i])
				if err != nil {
					return nil, fmt.Errorf("invalid version %v", v)
				}
				d.Version[i] = n
			}
		case "normalization_strategy":
			d.NormalizationStrategy = NormalizationStrategy(strings.ToUpper(fmt.Sprint(v)))
		default:
			d.Settings[k] = v
			if !c.SupportedSettings[k] {
				return nil, fmt.Errorf("Unknown setting '%s'.%s", k, helper.SuggestClosestMatch(k, sortedKeys(c.SupportedSettings)))
			}
		}
	}
	return d, nil
}

// GetDialect mirrors Dialect.get_or_raise. d may be nil/"" (base dialect), a dialect name
// optionally followed by settings ("duckdb, normalization_strategy = lowercase"), a
// *Dialect or a *DialectClass.
func GetDialect(d any) (*Dialect, error) {
	switch x := d.(type) {
	case nil:
		return BaseDialectClass().NewInstance(), nil
	case *Dialect:
		if x == nil {
			return BaseDialectClass().NewInstance(), nil
		}
		return x, nil
	case *DialectClass:
		x.Finalize()
		return x.NewInstance(), nil
	case string:
		if x == "" {
			return BaseDialectClass().NewInstance(), nil
		}
		parts := strings.Split(x, ",")
		name := strings.TrimSpace(parts[0])
		settings := map[string]any{}
		for _, kv := range parts[1:] {
			pair := strings.Split(kv, "=")
			key := strings.TrimSpace(pair[0])
			var value any
			switch len(pair) {
			case 1:
				value = true
			case 2:
				value = helper.ToBool(strings.TrimSpace(pair[1]))
			default:
				return nil, fmt.Errorf("Invalid dialect format: '%s'. Please use the correct format: 'dialect [, k1 = v2 [, ...]]'.", x)
			}
			settings[key] = value
		}
		cls, err := GetDialectClass(name)
		if err != nil {
			return nil, err
		}
		return cls.NewInstanceWith(settings)
	}
	return nil, fmt.Errorf("Invalid dialect type for '%v': '%T'.", d, d)
}

// MustDialect is GetDialect that panics on error.
func MustDialect(d any) *Dialect {
	dl, err := GetDialect(d)
	if err != nil {
		panic(err)
	}
	return dl
}

// Is reports whether the dialect is the named dialect (Python `dialect == "duckdb"`).
func (d *Dialect) Is(name string) bool { return d != nil && d.Name == name }

// IsClass reports whether the dialect instance's class is cls.
func (d *Dialect) IsClass(cls *DialectClass) bool { return d != nil && d.DialectClass == cls }

// FormatTime mirrors the Dialect.format_time classmethod: it converts a time format in this
// dialect to its equivalent Python strftime format. String inputs are quoted SQL strings.
func (c *DialectClass) FormatTime(expression any) *exp.Expr {
	format := func(s string) *exp.Expr {
		out := FormatTime(s, c.TimeMapping, c.TimeTrie)
		if s == "" {
			out = "None" // Literal.string(None)
		}
		return exp.LiteralString(out)
	}
	switch x := expression.(type) {
	case string:
		r := []rune(x)
		if len(r) >= 2 {
			return format(string(r[1 : len(r)-1]))
		}
		return format("")
	case *exp.Expr:
		if x != nil && x.IsString() {
			return format(x.ThisStr())
		}
		if x == nil {
			return nil
		}
		return x
	}
	return nil
}

// NormalizeIdentifier mirrors Dialect.normalize_identifier.
func (d *Dialect) NormalizeIdentifier(e *exp.Expr) *exp.Expr {
	return d.Methods.NormalizeIdentifier(d, e)
}

func baseNormalizeIdentifier(d *Dialect, e *exp.Expr) *exp.Expr {
	if e.Is(exp.Identifier) && d.NormalizationStrategy != CaseSensitive &&
		(!e.Quoted() || d.NormalizationStrategy == CaseInsensitive || d.NormalizationStrategy == CaseInsensitiveUppercase) {
		var normalized string
		if d.NormalizationStrategy == Uppercase || d.NormalizationStrategy == CaseInsensitiveUppercase {
			if d.AsciiOnlyNormalization {
				normalized = asciiUpper(e.ThisStr())
			} else {
				normalized = pyUpper(e.ThisStr())
			}
		} else {
			if d.AsciiOnlyNormalization {
				normalized = asciiLower(e.ThisStr())
			} else {
				normalized = pyLower(e.ThisStr())
			}
		}
		e.Set("this", normalized)
	}
	return e
}

func asciiUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// pyUpper / pyLower mirror Python's str.upper/str.lower (full Unicode case mapping).
func pyUpper(s string) string { return strings.ToUpper(s) }
func pyLower(s string) string { return strings.ToLower(s) }

// CaseSensitive mirrors Dialect.case_sensitive.
func (d *Dialect) CaseSensitive(text string) bool { return d.Methods.CaseSensitive(d, text) }

func baseCaseSensitive(d *Dialect, text string) bool {
	if d.NormalizationStrategy == CaseInsensitive {
		return false
	}
	for _, r := range text {
		if d.NormalizationStrategy == Uppercase {
			if unicode.IsLower(r) {
				return true
			}
		} else if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// CanQuote mirrors Dialect.can_quote. identify is a bool or "safe"/"unsafe".
func (d *Dialect) CanQuote(identifier *exp.Expr, identify any) bool {
	return d.Methods.CanQuote(d, identifier, identify)
}

func baseCanQuote(d *Dialect, identifier *exp.Expr, identify any) bool {
	if identifier.Quoted() {
		return true
	}
	switch v := identify.(type) {
	case bool:
		if !v {
			return false
		}
	case nil:
		return false
	case string:
		if v == "" {
			return false
		}
	}
	if identifier.Parent().Is(exp.Func) {
		return false
	}
	if b, ok := identify.(bool); ok && b {
		return true
	}
	this := identifier.ThisStr()
	isSafe := !d.CaseSensitive(this) && exp.SafeIdentifierRe.MatchString(this)
	switch identify {
	case "safe":
		return isSafe
	case "unsafe":
		return !isSafe
	}
	panic(fmt.Sprintf("Unexpected argument for identify: '%v'", identify))
}

// QuoteIdentifier mirrors Dialect.quote_identifier.
func (d *Dialect) QuoteIdentifier(e *exp.Expr, identify bool) *exp.Expr {
	return d.Methods.QuoteIdentifier(d, e, identify)
}

func baseQuoteIdentifier(d *Dialect, e *exp.Expr, identify bool) *exp.Expr {
	if e.Is(exp.Identifier) {
		var id any = "unsafe"
		if identify {
			id = true
		}
		e.Set("quoted", d.CanQuote(e, id))
	}
	return e
}

// ToJSONPath mirrors Dialect.to_json_path.
func (d *Dialect) ToJSONPath(path *exp.Expr) *exp.Expr { return d.Methods.ToJSONPath(d, path) }

func baseToJSONPath(d *Dialect, path *exp.Expr) *exp.Expr {
	if path.Is(exp.Literal) {
		pathText := path.Name()
		if path.IsNumber() {
			pathText = "[" + pathText + "]"
		}
		parsed, err := ParseJSONPath(pathText, d)
		if err == nil {
			return parsed
		}
		trimmed := strings.TrimLeftFunc(pathText, unicode.IsSpace)
		if d.StrictJsonPathSyntax && !strings.HasPrefix(trimmed, "lax") && !strings.HasPrefix(trimmed, "strict") {
			Logger.Warn(fmt.Sprintf("Invalid JSON path syntax. %s", err.Error()))
		}
	}
	return path
}

// GenerateValuesAliases mirrors Dialect.generate_values_aliases.
func (d *Dialect) GenerateValuesAliases(e *exp.Expr) []*exp.Expr {
	return d.Methods.GenerateValuesAliases(d, e)
}

func baseGenerateValuesAliases(d *Dialect, e *exp.Expr) []*exp.Expr {
	var out []*exp.Expr
	for i := range e.Expressions()[0].Expressions() {
		out = append(out, exp.ToIdentifier(fmt.Sprintf("_col_%d", i)))
	}
	return out
}

// Tokenizer returns a new tokenizer for the dialect.
func (d *Dialect) NewTokenizer() *tokens.Tokenizer { return tokens.New(d.Tokenizer) }

// Tokenize tokenizes sql.
func (d *Dialect) Tokenize(sql string) ([]*tokens.Token, error) {
	return d.NewTokenizer().Tokenize(sql)
}

// ParserOptions are the parser constructor arguments.
type ParserOptions struct {
	ErrorLevel          errs.ErrorLevel // errs.Unset means IMMEDIATE
	ErrorMessageContext int             // 0 means 100
	MaxErrors           int             // 0 means 3
	MaxNodes            int             // 0 means unlimited
}

// NewParser creates a parser for the dialect.
func (d *Dialect) NewParser(opts ParserOptions) *Parser { return newParser(d, opts) }

// Parse parses sql into a list of statements.
func (d *Dialect) Parse(sql string, opts ParserOptions) ([]*exp.Expr, error) {
	toks, err := d.Tokenize(sql)
	if err != nil {
		return nil, err
	}
	return d.NewParser(opts).Parse(toks, sql)
}

// ParseInto parses sql into the given expression kinds.
func (d *Dialect) ParseInto(into []exp.Kind, sql string, opts ParserOptions) ([]*exp.Expr, error) {
	toks, err := d.Tokenize(sql)
	if err != nil {
		return nil, err
	}
	return d.NewParser(opts).ParseIntoTypes(into, toks, sql)
}

// NewGenerator creates a generator for the dialect.
func (d *Dialect) NewGenerator(opts exp.GenerateOptions) *Generator { return newGenerator(d, opts) }

// Generate generates SQL for an expression.
func (d *Dialect) Generate(e *exp.Expr, opts exp.GenerateOptions) (string, error) {
	return d.NewGenerator(opts).GenerateSafe(e, !opts.NoCopy)
}

// Transpile parses sql and generates it back in this dialect.
func (d *Dialect) Transpile(sql string, opts exp.GenerateOptions) ([]string, error) {
	stmts, err := d.Parse(sql, ParserOptions{ErrorLevel: errs.Unset})
	if err != nil {
		return nil, err
	}
	out := make([]string, len(stmts))
	for i, s := range stmts {
		if s == nil {
			continue
		}
		o := opts
		o.NoCopy = true
		g, err := d.Generate(s, o)
		if err != nil {
			return nil, err
		}
		out[i] = g
	}
	return out, nil
}

// JSONPathTokenizerFor returns a JSONPath tokenizer for the dialect.
func (d *Dialect) NewJSONPathTokenizer() *tokens.Tokenizer { return tokens.New(d.JSONPathTokenizer) }
