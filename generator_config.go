package sqlglot

import (
	"maps"
	"regexp"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

// GenFunc generates SQL for an expression (a TRANSFORMS value or a <kind>_sql method).
type GenFunc func(g *Generator, e *exp.Expr) string

// TypeParamSetting mirrors TYPE_PARAM_SETTINGS values: (defaults, limits).
type TypeParamSetting struct {
	Defaults []int
	Limits   []*int
}

// GeneratorConfig holds the class-level configuration of a generator: the Python Generator
// class attributes, the TRANSFORMS table, the per-kind SQL methods and the overridable
// helper methods.
type GeneratorConfig struct {
	AfterHavingModifierTransforms            []NamedGenFunc                      // AFTER_HAVING_MODIFIER_TRANSFORMS (ordered)
	AlterPartitionOptions                    exp.KindSet                         // ALTER_PARTITION_OPTIONS
	AlterSetType                             string                              // ALTER_SET_TYPE
	AlterSetWrapped                          bool                                // ALTER_SET_WRAPPED
	AlterTableAddColumnsWrapped              bool                                // ALTER_TABLE_ADD_COLUMNS_WRAPPED
	AlterTableIncludeColumnKeyword           bool                                // ALTER_TABLE_INCLUDE_COLUMN_KEYWORD
	ArrayConcatIsVarLen                      bool                                // ARRAY_CONCAT_IS_VAR_LEN
	ArraySizeDimRequired                     *bool                               // ARRAY_SIZE_DIM_REQUIRED
	ArraySizeName                            string                              // ARRAY_SIZE_NAME
	AutoRefreshBareIntervals                 bool                                // AUTO_REFRESH_BARE_INTERVALS
	ByteStringFunction                       string                              // BYTE_STRING_FUNCTION
	CanImplementArrayAny                     bool                                // CAN_IMPLEMENT_ARRAY_ANY
	CastToIntRounds                          bool                                // CAST_TO_INT_ROUNDS
	CollateIsFunc                            bool                                // COLLATE_IS_FUNC
	ComputedColumnWithType                   bool                                // COMPUTED_COLUMN_WITH_TYPE
	CopyHasIntoKeyword                       bool                                // COPY_HAS_INTO_KEYWORD
	CopyParamsAreWrapped                     bool                                // COPY_PARAMS_ARE_WRAPPED
	CopyParamsEqRequired                     bool                                // COPY_PARAMS_EQ_REQUIRED
	CreateFunctionReturnAs                   bool                                // CREATE_FUNCTION_RETURN_AS
	CteRecursiveKeywordRequired              bool                                // CTE_RECURSIVE_KEYWORD_REQUIRED
	DataTypeSpecifiersAllowed                bool                                // DATA_TYPE_SPECIFIERS_ALLOWED
	DeclareDefaultAssignment                 string                              // DECLARE_DEFAULT_ASSIGNMENT
	DirectedJoins                            bool                                // DIRECTED_JOINS
	DuplicateKeyUpdateWithSet                bool                                // DUPLICATE_KEY_UPDATE_WITH_SET
	EnsureBools                              bool                                // ENSURE_BOOLS
	ExceptIntersectSupportAllClause          bool                                // EXCEPT_INTERSECT_SUPPORT_ALL_CLAUSE
	ExcludeComments                          exp.KindSet                         // EXCLUDE_COMMENTS
	ExpressionsWithoutNestedCtes             exp.KindSet                         // EXPRESSIONS_WITHOUT_NESTED_CTES
	ExpressionPrecedesPropertiesCreatables   StringSet                           // EXPRESSION_PRECEDES_PROPERTIES_CREATABLES
	ExtractAllowsQuotes                      bool                                // EXTRACT_ALLOWS_QUOTES
	GroupingsSep                             string                              // GROUPINGS_SEP
	HexFunc                                  string                              // HEX_FUNC
	HistoricalDataPostAlias                  bool                                // HISTORICAL_DATA_POST_ALIAS
	IgnoreNullsBeforeOrder                   bool                                // IGNORE_NULLS_BEFORE_ORDER
	IgnoreNullsInFunc                        bool                                // IGNORE_NULLS_IN_FUNC
	IndexOn                                  string                              // INDEX_ON
	InoutSeparator                           string                              // INOUT_SEPARATOR
	InsertOverwrite                          string                              // INSERT_OVERWRITE
	IntervalAllowsPluralForm                 bool                                // INTERVAL_ALLOWS_PLURAL_FORM
	IsBoolAllowed                            bool                                // IS_BOOL_ALLOWED
	JoinHints                                bool                                // JOIN_HINTS
	JsonKeyValuePairSep                      string                              // JSON_KEY_VALUE_PAIR_SEP
	JsonPathBracketedKeySupported            bool                                // JSON_PATH_BRACKETED_KEY_SUPPORTED
	JsonPathKeyQuotedForcesBrackets          bool                                // JSON_PATH_KEY_QUOTED_FORCES_BRACKETS
	JsonPathSingleQuoteEscape                bool                                // JSON_PATH_SINGLE_QUOTE_ESCAPE
	JsonTypeRequiredForExtraction            bool                                // JSON_TYPE_REQUIRED_FOR_EXTRACTION
	LastDaySupportsDatePart                  bool                                // LAST_DAY_SUPPORTS_DATE_PART
	LikePropertyInsideSchema                 bool                                // LIKE_PROPERTY_INSIDE_SCHEMA
	LimitFetch                               string                              // LIMIT_FETCH
	LimitIsTop                               bool                                // LIMIT_IS_TOP
	LimitOnlyLiterals                        bool                                // LIMIT_ONLY_LITERALS
	LockingReadsSupported                    bool                                // LOCKING_READS_SUPPORTED
	MatchedBySource                          bool                                // MATCHED_BY_SOURCE
	MatchAgainstTablePrefix                  *string                             // MATCH_AGAINST_TABLE_PREFIX
	MergeAllByName                           bool                                // MERGE_ALL_BY_NAME
	ModOperator                              string                              // MOD_OPERATOR
	ModParenParentTypes                      exp.KindSet                         // MOD_PAREN_PARENT_TYPES
	MultiArgDistinct                         bool                                // MULTI_ARG_DISTINCT
	NamedPlaceholderToken                    string                              // NAMED_PLACEHOLDER_TOKEN
	NormalizeExtractDateParts                bool                                // NORMALIZE_EXTRACT_DATE_PARTS
	NullOrderingSupported                    *bool                               // NULL_ORDERING_SUPPORTED
	Nvl2Supported                            bool                                // NVL2_SUPPORTED
	PadFillPatternIsRequired                 bool                                // PAD_FILL_PATTERN_IS_REQUIRED
	ParameterizableTextTypes                 exp.TypeSet                         // PARAMETERIZABLE_TEXT_TYPES
	ParameterToken                           string                              // PARAMETER_TOKEN
	ParseJsonName                            *string                             // PARSE_JSON_NAME
	PivotAliasWithAs                         bool                                // PIVOT_ALIAS_WITH_AS
	PropertiesLocation                       map[exp.Kind]exp.PropertiesLocation // PROPERTIES_LOCATION
	QueryHints                               bool                                // QUERY_HINTS
	QueryHintSep                             string                              // QUERY_HINT_SEP
	QuoteJsonPath                            bool                                // QUOTE_JSON_PATH
	RenameTableWithDb                        bool                                // RENAME_TABLE_WITH_DB
	ReservedKeywords                         StringSet                           // RESERVED_KEYWORDS
	RespectIgnoreNullsUnsupportedExpressions exp.KindSet                         // RESPECT_IGNORE_NULLS_UNSUPPORTED_EXPRESSIONS
	ReturningEnd                             bool                                // RETURNING_END
	SafeIntDivision                          bool                                // SAFE_INT_DIVISION
	SafeJsonPathKeyRe                        *regexp.Regexp                      // SAFE_JSON_PATH_KEY_RE
	SelectKinds                              []string                            // SELECT_KINDS
	SemiAntiJoinWithSide                     bool                                // SEMI_ANTI_JOIN_WITH_SIDE
	SentinelLineBreak                        string                              // SENTINEL_LINE_BREAK
	SetAssignmentRequiresVariableKeyword     bool                                // SET_ASSIGNMENT_REQUIRES_VARIABLE_KEYWORD
	SetOpLimits                              bool                                // SET_OP_LIMITS
	SetOpModifiers                           bool                                // SET_OP_MODIFIERS
	SetOpParenthesizedOperands               bool                                // SET_OP_PARENTHESIZED_OPERANDS
	SingleStringInterval                     bool                                // SINGLE_STRING_INTERVAL
	StarExcept                               string                              // STAR_EXCEPT
	StarExcludeRequiresDerivedTable          bool                                // STAR_EXCLUDE_REQUIRES_DERIVED_TABLE
	StructDelimiter                          []string                            // STRUCT_DELIMITER
	SupportedJsonPathParts                   exp.KindSet                         // SUPPORTED_JSON_PATH_PARTS
	SupportsAlterColumnIfExists              bool                                // SUPPORTS_ALTER_COLUMN_IF_EXISTS
	SupportsAlterColumnNullability           bool                                // SUPPORTS_ALTER_COLUMN_NULLABILITY
	SupportsBetweenFlags                     bool                                // SUPPORTS_BETWEEN_FLAGS
	SupportsChangeColumn                     bool                                // SUPPORTS_CHANGE_COLUMN
	SupportsConvertTimezone                  bool                                // SUPPORTS_CONVERT_TIMEZONE
	SupportsCreateTableLike                  bool                                // SUPPORTS_CREATE_TABLE_LIKE
	SupportsDecodeCase                       bool                                // SUPPORTS_DECODE_CASE
	SupportsDropAlterIcebergProperty         bool                                // SUPPORTS_DROP_ALTER_ICEBERG_PROPERTY
	SupportsExplodingProjections             bool                                // SUPPORTS_EXPLODING_PROJECTIONS
	SupportsGroupingSetsAsSuffix             bool                                // SUPPORTS_GROUPING_SETS_AS_SUFFIX
	SupportsLikeQuantifiers                  bool                                // SUPPORTS_LIKE_QUANTIFIERS
	SupportsMedian                           bool                                // SUPPORTS_MEDIAN
	SupportsMergeWhere                       bool                                // SUPPORTS_MERGE_WHERE
	SupportsModifyColumn                     bool                                // SUPPORTS_MODIFY_COLUMN
	SupportsNamedCteColumns                  bool                                // SUPPORTS_NAMED_CTE_COLUMNS
	SupportsNearestJoin                      bool                                // SUPPORTS_NEAREST_JOIN
	SupportsSelectInto                       bool                                // SUPPORTS_SELECT_INTO
	SupportsSingleArgConcat                  bool                                // SUPPORTS_SINGLE_ARG_CONCAT
	SupportsTableAliasColumns                bool                                // SUPPORTS_TABLE_ALIAS_COLUMNS
	SupportsTableCopy                        bool                                // SUPPORTS_TABLE_COPY
	SupportsToNumber                         bool                                // SUPPORTS_TO_NUMBER
	SupportsUescape                          bool                                // SUPPORTS_UESCAPE
	SupportsUnixSeconds                      bool                                // SUPPORTS_UNIX_SECONDS
	SupportsUnloggedTables                   bool                                // SUPPORTS_UNLOGGED_TABLES
	SupportsWindowExclude                    bool                                // SUPPORTS_WINDOW_EXCLUDE
	TablesampleKeywords                      string                              // TABLESAMPLE_KEYWORDS
	TablesampleRequiresParens                bool                                // TABLESAMPLE_REQUIRES_PARENS
	TablesampleSeedKeyword                   string                              // TABLESAMPLE_SEED_KEYWORD
	TablesampleSizeIsRows                    bool                                // TABLESAMPLE_SIZE_IS_ROWS
	TablesampleWithMethod                    bool                                // TABLESAMPLE_WITH_METHOD
	TableHints                               bool                                // TABLE_HINTS
	TimePartSingulars                        map[string]string                   // TIME_PART_SINGULARS
	TokenMapping                             map[tokens.TokenType]string         // TOKEN_MAPPING
	Transforms                               map[exp.Kind]GenFunc                // TRANSFORMS
	TrySupported                             bool                                // TRY_SUPPORTED
	TypeMapping                              map[exp.DType]string                // TYPE_MAPPING
	TypeParamSettings                        map[exp.DType]TypeParamSetting      // TYPE_PARAM_SETTINGS
	TzToWithTimeZone                         bool                                // TZ_TO_WITH_TIME_ZONE
	UnicodeSubstitute                        func(m []string) string             // UNICODE_SUBSTITUTE
	UnnestWithOrdinality                     bool                                // UNNEST_WITH_ORDINALITY
	UnpivotAliasesAreIdentifiers             bool                                // UNPIVOT_ALIASES_ARE_IDENTIFIERS
	UnsupportedTypes                         exp.TypeSet                         // UNSUPPORTED_TYPES
	UnwrappedIntervalValues                  exp.KindSet                         // UNWRAPPED_INTERVAL_VALUES
	UpdateStatementSupportsFrom              bool                                // UPDATE_STATEMENT_SUPPORTS_FROM
	ValuesAsTable                            bool                                // VALUES_AS_TABLE
	WindowFuncsWithNullOrdering              exp.KindSet                         // WINDOW_FUNCS_WITH_NULL_ORDERING
	WithPropertiesPrefix                     string                              // WITH_PROPERTIES_PREFIX
	WithSeparatedComments                    exp.KindSet                         // WITH_SEPARATED_COMMENTS
	WrapDerivedValues                        bool                                // WRAP_DERIVED_VALUES

	// SQLMethods holds the <kind>_sql methods (Python auto-discovered methods), keyed by kind.
	// Dialects add or replace entries here to override `def <kind>_sql(self, expression)`.
	SQLMethods map[exp.Kind]GenFunc
	// Methods holds the overridable generator helper methods and the <kind>_sql methods that
	// take extra arguments.
	Methods GeneratorMethods
	// Extra is free-form storage for dialect-specific generator data.
	Extra map[string]any

	dispatch map[exp.Kind]GenFunc
}

var _ = regexp.MustCompile
var _ = tokens.Comma

// Clone returns a deep copy of the configuration (used for dialect inheritance).
func (c *GeneratorConfig) Clone() *GeneratorConfig {
	n := *c
	n.AfterHavingModifierTransforms = append([]NamedGenFunc(nil), c.AfterHavingModifierTransforms...)
	n.ExpressionPrecedesPropertiesCreatables = maps.Clone(c.ExpressionPrecedesPropertiesCreatables)
	n.ParameterizableTextTypes = c.ParameterizableTextTypes.Union()
	n.PropertiesLocation = maps.Clone(c.PropertiesLocation)
	n.ReservedKeywords = maps.Clone(c.ReservedKeywords)
	n.SelectKinds = append([]string(nil), c.SelectKinds...)
	n.StructDelimiter = append([]string(nil), c.StructDelimiter...)
	n.TimePartSingulars = maps.Clone(c.TimePartSingulars)
	n.TokenMapping = maps.Clone(c.TokenMapping)
	n.Transforms = maps.Clone(c.Transforms)
	n.TypeMapping = maps.Clone(c.TypeMapping)
	n.TypeParamSettings = maps.Clone(c.TypeParamSettings)
	n.UnsupportedTypes = c.UnsupportedTypes.Union()
	n.SQLMethods = maps.Clone(c.SQLMethods)
	n.Extra = maps.Clone(c.Extra)
	n.dispatch = nil
	return &n
}
