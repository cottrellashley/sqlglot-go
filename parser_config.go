package sqlglot

import (
	"maps"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
	"github.com/cottrellashley/sqlglot-go/trie"
)

// IsPredicatePrecedence mirrors parser.IsPredicatePrecedence.
type IsPredicatePrecedence int

const (
	IsPredicatePrecedenceHigh IsPredicatePrecedence = iota + 1
	IsPredicatePrecedenceComparison
	IsPredicatePrecedenceEquality
	IsPredicatePrecedenceLow
)

// StringSet is a set of strings.
type StringSet map[string]bool

func strSet(xs ...string) StringSet {
	s := make(StringSet, len(xs))
	for _, x := range xs {
		s[x] = true
	}
	return s
}

// With returns a copy of the set with extra members.
func (s StringSet) With(xs ...string) StringSet {
	n := maps.Clone(s)
	if n == nil {
		n = StringSet{}
	}
	for _, x := range xs {
		n[x] = true
	}
	return n
}

// Without returns a copy of the set without the given members.
func (s StringSet) Without(xs ...string) StringSet {
	n := maps.Clone(s)
	for _, x := range xs {
		delete(n, x)
	}
	return n
}

// Union returns a copy of the set merged with others.
func (s StringSet) Union(others ...StringSet) StringSet {
	n := maps.Clone(s)
	if n == nil {
		n = StringSet{}
	}
	for _, o := range others {
		for k := range o {
			n[k] = true
		}
	}
	return n
}

// OptionsType mirrors parser.OPTIONS_TYPE: keyword -> allowed continuations (each a
// sequence of words; an empty list means no continuation is required).
type OptionsType map[string][][]string

// Clone returns a copy.
func (o OptionsType) Clone() OptionsType { return maps.Clone(o) }

// Parser callback types (the values of the Python parser tables).
type (
	// ParseFunc is `lambda self: ...`.
	ParseFunc func(p *Parser) *exp.Expr
	// FuncBuilder builds a function expression from its arguments (FUNCTIONS values).
	FuncBuilder func(args []*exp.Expr, d *Dialect) *exp.Expr
	// ColumnOperator is `lambda self, this, path: ...` (COLUMN_OPERATORS values).
	ColumnOperator func(p *Parser, this, path *exp.Expr) *exp.Expr
	// LambdaParser is `lambda self, expressions: ...` (LAMBDAS values).
	LambdaParser func(p *Parser, expressions []*exp.Expr) *exp.Expr
	// TokenParser is `lambda self, token: ...` (PRIMARY_PARSERS values).
	TokenParser func(p *Parser, token *tokens.Token) *exp.Expr
	// PipeParser is `lambda self, query: ...` (PIPE_SYNTAX_TRANSFORM_PARSERS values).
	PipeParser func(p *Parser, query *exp.Expr) *exp.Expr
	// RangeParser is `lambda self, this: ...` (RANGE_PARSERS values).
	RangeParser func(p *Parser, this *exp.Expr) *exp.Expr
	// ModifierParser returns the (arg key, value) pair of a query modifier.
	ModifierParser func(p *Parser) (string, any)
	// AlterParser is an ALTER_PARSERS value (returns the parsed actions).
	AlterParser func(p *Parser) []*exp.Expr
	// TypeLiteralParser is `lambda self, this, data_type: ...` (TYPE_LITERAL_PARSERS values).
	TypeLiteralParser func(p *Parser, this, dataType *exp.Expr) *exp.Expr
)

// PropertyParser is a PROPERTY_PARSERS value. Kw holds the keyword arguments passed by the
// caller (e.g. default=True); Fn returns an expression, a []*exp.Expr or nil.
type PropertyParser struct {
	Fn            func(p *Parser, kw map[string]any) any
	AcceptsKwargs bool
}

// PP builds a PropertyParser that does not accept keyword arguments.
func PP(f func(p *Parser) any) PropertyParser {
	return PropertyParser{Fn: func(p *Parser, _ map[string]any) any { return f(p) }}
}

// PPE builds a PropertyParser returning a single expression.
func PPE(f func(p *Parser) *exp.Expr) PropertyParser {
	return PropertyParser{Fn: func(p *Parser, _ map[string]any) any { return nilIfNil(f(p)) }}
}

// PPK builds a PropertyParser that accepts keyword arguments.
func PPK(f func(p *Parser, kw map[string]any) any) PropertyParser {
	return PropertyParser{Fn: f, AcceptsKwargs: true}
}

func nilIfNil(e *exp.Expr) any {
	if e == nil {
		return nil
	}
	return e
}

// ParserConfig holds the class-level configuration of a parser: the Python Parser class
// attributes (tables, token sets and flags) and the overridable methods.
type ParserConfig struct {
	AddConstraintKeywords             StringSet                                  // ADD_CONSTRAINT_KEYWORDS
	AddConstraintTokens               tokens.TokenSet                            // ADD_CONSTRAINT_TOKENS
	AddJoinOnTrue                     bool                                       // ADD_JOIN_ON_TRUE
	AdjacentStringsCannotBeConnected  bool                                       // ADJACENT_STRINGS_CANNOT_BE_CONNECTED
	AggregateTypeTokens               tokens.TokenSet                            // AGGREGATE_TYPE_TOKENS
	AliasTokens                       tokens.TokenSet                            // ALIAS_TOKENS
	Alterables                        tokens.TokenSet                            // ALTERABLES
	AlterAlterParsers                 map[string]ParseFunc                       // ALTER_ALTER_PARSERS
	AlterColumnTypeRequiresKeyword    bool                                       // ALTER_COLUMN_TYPE_REQUIRES_KEYWORD
	AlterDropRequiresColumn           bool                                       // ALTER_DROP_REQUIRES_COLUMN
	AlterParsers                      map[string]AlterParser                     // ALTER_PARSERS
	AlterRenameRequiresColumn         bool                                       // ALTER_RENAME_REQUIRES_COLUMN
	AlterTableMixedActions            bool                                       // ALTER_TABLE_MIXED_ACTIONS
	AlterTablePartitions              bool                                       // ALTER_TABLE_PARTITIONS
	AlterTableRequiresAction          bool                                       // ALTER_TABLE_REQUIRES_ACTION
	AmbiguousAliasTokens              tokens.TokenSet                            // AMBIGUOUS_ALIAS_TOKENS
	AnalyzeExpressionParsers          map[string]ParseFunc                       // ANALYZE_EXPRESSION_PARSERS
	AnalyzeStyles                     StringSet                                  // ANALYZE_STYLES
	ArrayConstructors                 map[string]exp.Kind                        // ARRAY_CONSTRUCTORS
	Assignment                        map[tokens.TokenType]exp.Kind              // ASSIGNMENT
	Bitwise                           map[tokens.TokenType]exp.Kind              // BITWISE
	Brackets                          tokens.TokenSet                            // BRACKETS
	CastActions                       OptionsType                                // CAST_ACTIONS
	CastColumnOperators               tokens.TokenSet                            // CAST_COLUMN_OPERATORS
	CloneKeywords                     StringSet                                  // CLONE_KEYWORDS
	ColonChainIsSingleExtract         bool                                       // COLON_CHAIN_IS_SINGLE_EXTRACT
	ColonIsVariantExtract             bool                                       // COLON_IS_VARIANT_EXTRACT
	ColonPlaceholderTokens            tokens.TokenSet                            // COLON_PLACEHOLDER_TOKENS
	ColumnOperators                   map[tokens.TokenType]ColumnOperator        // COLUMN_OPERATORS
	ColumnPostfixTokens               tokens.TokenSet                            // COLUMN_POSTFIX_TOKENS
	CommentTableAliasTokens           tokens.TokenSet                            // COMMENT_TABLE_ALIAS_TOKENS
	Comparison                        map[tokens.TokenType]exp.Kind              // COMPARISON
	ConflictActions                   OptionsType                                // CONFLICT_ACTIONS
	Conjunction                       map[tokens.TokenType]exp.Kind              // CONJUNCTION
	ConstraintParsers                 map[string]ParseFunc                       // CONSTRAINT_PARSERS
	CopyIntoVarlenOptions             StringSet                                  // COPY_INTO_VARLEN_OPTIONS
	Creatables                        tokens.TokenSet                            // CREATABLES
	CreateSequence                    OptionsType                                // CREATE_SEQUENCE
	DbCreatables                      tokens.TokenSet                            // DB_CREATABLES
	DdlSelectTokens                   tokens.TokenSet                            // DDL_SELECT_TOKENS
	DefaultSamplingMethod             string                                     // DEFAULT_SAMPLING_METHOD
	DescribeStyles                    StringSet                                  // DESCRIBE_STYLES
	Disjunction                       map[tokens.TokenType]exp.Kind              // DISJUNCTION
	DistinctTokens                    tokens.TokenSet                            // DISTINCT_TOKENS
	EnumTypeTokens                    tokens.TokenSet                            // ENUM_TYPE_TOKENS
	Equality                          map[tokens.TokenType]exp.Kind              // EQUALITY
	ExecuteAsOptions                  OptionsType                                // EXECUTE_AS_OPTIONS
	Exponent                          map[tokens.TokenType]exp.Kind              // EXPONENT
	ExpressionParsers                 map[exp.Kind]ParseFunc                     // EXPRESSION_PARSERS
	Factor                            map[tokens.TokenType]exp.Kind              // FACTOR
	FetchTokens                       tokens.TokenSet                            // FETCH_TOKENS
	Functions                         map[string]FuncBuilder                     // FUNCTIONS
	FunctionsWithAliasedArgs          StringSet                                  // FUNCTIONS_WITH_ALIASED_ARGS
	FunctionParsers                   map[string]ParseFunc                       // FUNCTION_PARSERS
	FuncTokens                        tokens.TokenSet                            // FUNC_TOKENS
	HistoricalDataKind                StringSet                                  // HISTORICAL_DATA_KIND
	HistoricalDataPrefix              StringSet                                  // HISTORICAL_DATA_PREFIX
	IdentifierTokens                  tokens.TokenSet                            // IDENTIFIER_TOKENS
	IdentifyPivotStrings              bool                                       // IDENTIFY_PIVOT_STRINGS
	IdVarTokens                       tokens.TokenSet                            // ID_VAR_TOKENS
	InsertAlternatives                StringSet                                  // INSERT_ALTERNATIVES
	IntervalSpans                     bool                                       // INTERVAL_SPANS
	InvalidFuncNameTokens             tokens.TokenSet                            // INVALID_FUNC_NAME_TOKENS
	IsolatedLoadingOptions            OptionsType                                // ISOLATED_LOADING_OPTIONS
	IsJsonPredicateKind               StringSet                                  // IS_JSON_PREDICATE_KIND
	IsPredicatePrecedence             IsPredicatePrecedence                      // IS_PREDICATE_PRECEDENCE
	IsPredicateTokens                 tokens.TokenSet                            // IS_PREDICATE_TOKENS
	JoinsHaveEqualPrecedence          bool                                       // JOINS_HAVE_EQUAL_PRECEDENCE
	JoinHints                         StringSet                                  // JOIN_HINTS
	JoinKinds                         tokens.TokenSet                            // JOIN_KINDS
	JoinMethods                       tokens.TokenSet                            // JOIN_METHODS
	JoinSides                         tokens.TokenSet                            // JOIN_SIDES
	JsonArrowsRequireJsonType         bool                                       // JSON_ARROWS_REQUIRE_JSON_TYPE
	JsonExtractRequiresJsonExpression bool                                       // JSON_EXTRACT_REQUIRES_JSON_EXPRESSION
	JsonKeyValueSeparatorTokens       tokens.TokenSet                            // JSON_KEY_VALUE_SEPARATOR_TOKENS
	JsonOperators                     map[tokens.TokenType]ColumnOperator        // JSON_OPERATORS
	KeyConstraintOptions              OptionsType                                // KEY_CONSTRAINT_OPTIONS
	KeyValueDefinitions               exp.KindSet                                // KEY_VALUE_DEFINITIONS
	Lambdas                           map[tokens.TokenType]LambdaParser          // LAMBDAS
	LambdaArgTerminators              tokens.TokenSet                            // LAMBDA_ARG_TERMINATORS
	LogDefaultsToLn                   bool                                       // LOG_DEFAULTS_TO_LN
	MapKeysAreArbitraryExpressions    bool                                       // MAP_KEYS_ARE_ARBITRARY_EXPRESSIONS
	Modifiables                       exp.KindSet                                // MODIFIABLES
	ModifiersAttachedToSetOp          bool                                       // MODIFIERS_ATTACHED_TO_SET_OP
	NestedTypeTokens                  tokens.TokenSet                            // NESTED_TYPE_TOKENS
	NoParenFunctions                  map[tokens.TokenType]exp.Kind              // NO_PAREN_FUNCTIONS
	NoParenFunctionParsers            map[string]ParseFunc                       // NO_PAREN_FUNCTION_PARSERS
	NoParenIfCommands                 bool                                       // NO_PAREN_IF_COMMANDS
	NumericParsers                    map[tokens.TokenType]TokenParser           // NUMERIC_PARSERS
	OdbcDatetimeLiterals              map[string]exp.Kind                        // ODBC_DATETIME_LITERALS
	OnConditionTokens                 StringSet                                  // ON_CONDITION_TOKENS
	OpclassFollowKeywords             StringSet                                  // OPCLASS_FOLLOW_KEYWORDS
	OperationModifiers                StringSet                                  // OPERATION_MODIFIERS
	OptionalAliasTokenCte             bool                                       // OPTIONAL_ALIAS_TOKEN_CTE
	OptypeFollowTokens                tokens.TokenSet                            // OPTYPE_FOLLOW_TOKENS
	PartitionKeywords                 StringSet                                  // PARTITION_KEYWORDS
	PipeSyntaxTransformParsers        map[string]PipeParser                      // PIPE_SYNTAX_TRANSFORM_PARSERS
	PivotColumnNaming                 string                                     // PIVOT_COLUMN_NAMING
	PlaceholderParsers                map[tokens.TokenType]ParseFunc             // PLACEHOLDER_PARSERS
	PrefixedPivotColumns              bool                                       // PREFIXED_PIVOT_COLUMNS
	PreVolatileTokens                 tokens.TokenSet                            // PRE_VOLATILE_TOKENS
	PrimaryParsers                    map[tokens.TokenType]TokenParser           // PRIMARY_PARSERS
	PrivilegeFollowTokens             tokens.TokenSet                            // PRIVILEGE_FOLLOW_TOKENS
	ProcedureOptions                  OptionsType                                // PROCEDURE_OPTIONS
	PropertyParsers                   map[string]PropertyParser                  // PROPERTY_PARSERS
	QueryModifierParsers              map[tokens.TokenType]ModifierParser        // QUERY_MODIFIER_PARSERS
	QueryModifierTokens               tokens.TokenSet                            // QUERY_MODIFIER_TOKENS
	QuotedTypesToPreserve             StringSet                                  // QUOTED_TYPES_TO_PRESERVE
	RangeParsers                      map[tokens.TokenType]RangeParser           // RANGE_PARSERS
	RecursiveCteSearchKind            StringSet                                  // RECURSIVE_CTE_SEARCH_KIND
	ReservedConstraintKinds           StringSet                                  // RESERVED_CONSTRAINT_KINDS
	ReservedTokens                    tokens.TokenSet                            // RESERVED_TOKENS
	SchemaBindingOptions              OptionsType                                // SCHEMA_BINDING_OPTIONS
	SchemaUnnamedConstraints          StringSet                                  // SCHEMA_UNNAMED_CONSTRAINTS
	SecurityPropertyKeywords          StringSet                                  // SECURITY_PROPERTY_KEYWORDS
	SelectStartTokens                 tokens.TokenSet                            // SELECT_START_TOKENS
	SetAssignmentDelimiters           StringSet                                  // SET_ASSIGNMENT_DELIMITERS
	SetOperations                     tokens.TokenSet                            // SET_OPERATIONS
	SetOpModifiers                    StringSet                                  // SET_OP_MODIFIERS
	SetParsers                        map[string]ParseFunc                       // SET_PARSERS
	SetRequiresAssignmentDelimiter    bool                                       // SET_REQUIRES_ASSIGNMENT_DELIMITER
	ShowParsers                       map[string]ParseFunc                       // SHOW_PARSERS
	SignedToUnsignedTypeToken         map[tokens.TokenType]tokens.TokenType      // SIGNED_TO_UNSIGNED_TYPE_TOKEN
	StatementParsers                  map[tokens.TokenType]ParseFunc             // STATEMENT_PARSERS
	StrictCast                        bool                                       // STRICT_CAST
	StringAliases                     bool                                       // STRING_ALIASES
	StringParsers                     map[tokens.TokenType]TokenParser           // STRING_PARSERS
	StructTypeTokens                  tokens.TokenSet                            // STRUCT_TYPE_TOKENS
	SubqueryPredicates                map[tokens.TokenType]exp.Kind              // SUBQUERY_PREDICATES
	SubqueryTokens                    tokens.TokenSet                            // SUBQUERY_TOKENS
	SupportsDigitPrefixedFieldNames   bool                                       // SUPPORTS_DIGIT_PREFIXED_FIELD_NAMES
	SupportsDirectedJoin              bool                                       // SUPPORTS_DIRECTED_JOIN
	SupportsImplicitUnnest            bool                                       // SUPPORTS_IMPLICIT_UNNEST
	SupportsNthValueFromModifier      bool                                       // SUPPORTS_NTH_VALUE_FROM_MODIFIER
	SupportsOmittedIntervalSpanUnit   bool                                       // SUPPORTS_OMITTED_INTERVAL_SPAN_UNIT
	SupportsPartitionSelection        bool                                       // SUPPORTS_PARTITION_SELECTION
	TablesampleCsv                    bool                                       // TABLESAMPLE_CSV
	TableAliasTokens                  tokens.TokenSet                            // TABLE_ALIAS_TOKENS
	TableIndexHintTokens              tokens.TokenSet                            // TABLE_INDEX_HINT_TOKENS
	TablePostfixTokens                tokens.TokenSet                            // TABLE_POSTFIX_TOKENS
	TableTerminators                  tokens.TokenSet                            // TABLE_TERMINATORS
	Term                              map[tokens.TokenType]exp.Kind              // TERM
	TextMatchExcludedTokens           tokens.TokenSet                            // TEXT_MATCH_EXCLUDED_TOKENS
	Times                             tokens.TokenSet                            // TIMES
	Timestamps                        tokens.TokenSet                            // TIMESTAMPS
	TransactionCharacteristics        OptionsType                                // TRANSACTION_CHARACTERISTICS
	TransactionKind                   StringSet                                  // TRANSACTION_KIND
	TriggerDeferrable                 OptionsType                                // TRIGGER_DEFERRABLE
	TriggerEvents                     tokens.TokenSet                            // TRIGGER_EVENTS
	TriggerTiming                     OptionsType                                // TRIGGER_TIMING
	TrimPatternFirst                  bool                                       // TRIM_PATTERN_FIRST
	TrimTypes                         StringSet                                  // TRIM_TYPES
	TypedLambdaArgs                   bool                                       // TYPED_LAMBDA_ARGS
	TypeConverters                    map[exp.DType]func(dt *exp.Expr) *exp.Expr // TYPE_CONVERTERS
	TypeLiteralParsers                map[exp.DType]TypeLiteralParser            // TYPE_LITERAL_PARSERS
	TypeTokens                        tokens.TokenSet                            // TYPE_TOKENS
	UnaryParsers                      map[tokens.TokenType]ParseFunc             // UNARY_PARSERS
	UnnestOffsetAliasTokens           tokens.TokenSet                            // UNNEST_OFFSET_ALIAS_TOKENS
	UnpivotValueColumnsFirst          bool                                       // UNPIVOT_VALUE_COLUMNS_FIRST
	UpdateAliasTokens                 tokens.TokenSet                            // UPDATE_ALIAS_TOKENS
	Usables                           OptionsType                                // USABLES
	ValuesFollowedByParen             bool                                       // VALUES_FOLLOWED_BY_PAREN
	VersionPhrases                    map[string]string                          // VERSION_PHRASES
	ViewAttributes                    StringSet                                  // VIEW_ATTRIBUTES
	WindowAliasTokens                 tokens.TokenSet                            // WINDOW_ALIAS_TOKENS
	WindowBeforeParenTokens           tokens.TokenSet                            // WINDOW_BEFORE_PAREN_TOKENS
	WindowExcludeOptions              OptionsType                                // WINDOW_EXCLUDE_OPTIONS
	WindowSides                       StringSet                                  // WINDOW_SIDES
	WrappedTransformColumnConstraint  bool                                       // WRAPPED_TRANSFORM_COLUMN_CONSTRAINT
	ZoneAwareTimestampConstructor     bool                                       // ZONE_AWARE_TIMESTAMP_CONSTRUCTOR

	// Methods holds the overridable parser methods (the Python _parse_* methods).
	Methods ParserMethods

	// Extra is free-form storage for dialect-specific parser data.
	Extra map[string]any

	// computed
	ShowTrie *trie.Node[string]
	SetTrie  *trie.Node[string]
}

// Clone returns a deep copy of the configuration (used for dialect inheritance).
func (c *ParserConfig) Clone() *ParserConfig {
	n := *c
	n.AddConstraintKeywords = maps.Clone(c.AddConstraintKeywords)
	n.AlterAlterParsers = maps.Clone(c.AlterAlterParsers)
	n.AlterParsers = maps.Clone(c.AlterParsers)
	n.AnalyzeExpressionParsers = maps.Clone(c.AnalyzeExpressionParsers)
	n.AnalyzeStyles = maps.Clone(c.AnalyzeStyles)
	n.ArrayConstructors = maps.Clone(c.ArrayConstructors)
	n.Assignment = maps.Clone(c.Assignment)
	n.Bitwise = maps.Clone(c.Bitwise)
	n.CastActions = maps.Clone(c.CastActions)
	n.CloneKeywords = maps.Clone(c.CloneKeywords)
	n.ColumnOperators = maps.Clone(c.ColumnOperators)
	n.Comparison = maps.Clone(c.Comparison)
	n.ConflictActions = maps.Clone(c.ConflictActions)
	n.Conjunction = maps.Clone(c.Conjunction)
	n.ConstraintParsers = maps.Clone(c.ConstraintParsers)
	n.CopyIntoVarlenOptions = maps.Clone(c.CopyIntoVarlenOptions)
	n.CreateSequence = maps.Clone(c.CreateSequence)
	n.DescribeStyles = maps.Clone(c.DescribeStyles)
	n.Disjunction = maps.Clone(c.Disjunction)
	n.Equality = maps.Clone(c.Equality)
	n.ExecuteAsOptions = maps.Clone(c.ExecuteAsOptions)
	n.Exponent = maps.Clone(c.Exponent)
	n.ExpressionParsers = maps.Clone(c.ExpressionParsers)
	n.Factor = maps.Clone(c.Factor)
	n.Functions = maps.Clone(c.Functions)
	n.FunctionsWithAliasedArgs = maps.Clone(c.FunctionsWithAliasedArgs)
	n.FunctionParsers = maps.Clone(c.FunctionParsers)
	n.HistoricalDataKind = maps.Clone(c.HistoricalDataKind)
	n.HistoricalDataPrefix = maps.Clone(c.HistoricalDataPrefix)
	n.InsertAlternatives = maps.Clone(c.InsertAlternatives)
	n.IsolatedLoadingOptions = maps.Clone(c.IsolatedLoadingOptions)
	n.IsJsonPredicateKind = maps.Clone(c.IsJsonPredicateKind)
	n.JoinHints = maps.Clone(c.JoinHints)
	n.JsonOperators = maps.Clone(c.JsonOperators)
	n.KeyConstraintOptions = maps.Clone(c.KeyConstraintOptions)
	n.Lambdas = maps.Clone(c.Lambdas)
	n.NoParenFunctions = maps.Clone(c.NoParenFunctions)
	n.NoParenFunctionParsers = maps.Clone(c.NoParenFunctionParsers)
	n.NumericParsers = maps.Clone(c.NumericParsers)
	n.OdbcDatetimeLiterals = maps.Clone(c.OdbcDatetimeLiterals)
	n.OnConditionTokens = maps.Clone(c.OnConditionTokens)
	n.OpclassFollowKeywords = maps.Clone(c.OpclassFollowKeywords)
	n.OperationModifiers = maps.Clone(c.OperationModifiers)
	n.PartitionKeywords = maps.Clone(c.PartitionKeywords)
	n.PipeSyntaxTransformParsers = maps.Clone(c.PipeSyntaxTransformParsers)
	n.PlaceholderParsers = maps.Clone(c.PlaceholderParsers)
	n.PrimaryParsers = maps.Clone(c.PrimaryParsers)
	n.ProcedureOptions = maps.Clone(c.ProcedureOptions)
	n.PropertyParsers = maps.Clone(c.PropertyParsers)
	n.QueryModifierParsers = maps.Clone(c.QueryModifierParsers)
	n.QuotedTypesToPreserve = maps.Clone(c.QuotedTypesToPreserve)
	n.RangeParsers = maps.Clone(c.RangeParsers)
	n.RecursiveCteSearchKind = maps.Clone(c.RecursiveCteSearchKind)
	n.ReservedConstraintKinds = maps.Clone(c.ReservedConstraintKinds)
	n.SchemaBindingOptions = maps.Clone(c.SchemaBindingOptions)
	n.SchemaUnnamedConstraints = maps.Clone(c.SchemaUnnamedConstraints)
	n.SecurityPropertyKeywords = maps.Clone(c.SecurityPropertyKeywords)
	n.SetAssignmentDelimiters = maps.Clone(c.SetAssignmentDelimiters)
	n.SetOpModifiers = maps.Clone(c.SetOpModifiers)
	n.SetParsers = maps.Clone(c.SetParsers)
	n.ShowParsers = maps.Clone(c.ShowParsers)
	n.SignedToUnsignedTypeToken = maps.Clone(c.SignedToUnsignedTypeToken)
	n.StatementParsers = maps.Clone(c.StatementParsers)
	n.StringParsers = maps.Clone(c.StringParsers)
	n.SubqueryPredicates = maps.Clone(c.SubqueryPredicates)
	n.Term = maps.Clone(c.Term)
	n.TransactionCharacteristics = maps.Clone(c.TransactionCharacteristics)
	n.TransactionKind = maps.Clone(c.TransactionKind)
	n.TriggerDeferrable = maps.Clone(c.TriggerDeferrable)
	n.TriggerTiming = maps.Clone(c.TriggerTiming)
	n.TrimTypes = maps.Clone(c.TrimTypes)
	n.TypeConverters = maps.Clone(c.TypeConverters)
	n.TypeLiteralParsers = maps.Clone(c.TypeLiteralParsers)
	n.UnaryParsers = maps.Clone(c.UnaryParsers)
	n.Usables = maps.Clone(c.Usables)
	n.VersionPhrases = maps.Clone(c.VersionPhrases)
	n.ViewAttributes = maps.Clone(c.ViewAttributes)
	n.WindowExcludeOptions = maps.Clone(c.WindowExcludeOptions)
	n.WindowSides = maps.Clone(c.WindowSides)
	n.Extra = maps.Clone(c.Extra)
	return &n
}
