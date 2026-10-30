package sqlglot

import (
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func init() {
	Register("", newBaseDialectClass)
}

// newBaseDialectClass builds sqlglot's base Dialect class (the "sqlglot" dialect).
func newBaseDialectClass() *DialectClass {
	c := &DialectClass{Name: "", ClassName: "Dialect"}
	c.AliasPostTablesample = false
	c.AliasPostVersion = true
	c.AlterTableAddRequiredForEachColumn = true
	c.AlterTableDropRequiredForEachColumn = true
	c.AlterTableSupportsCascade = false
	c.AnnotateAllScopes = false
	c.ArrayAggIncludesNulls = Bool(true)
	c.ArrayFuncsPropagatesNulls = false
	c.AsciiOnlyNormalization = false
	c.ByteStringIsBytesType = false
	c.CoalesceComparisonNonStandard = false
	c.ConcatCoalesce = false
	c.ConcatWsCoalesce = false
	c.CopyParamsAreCsv = true
	c.DateintFormat = "'%Y%m%d'"
	c.DateFormat = "'%Y-%m-%d'"
	c.DatePartMapping = map[string]string{"Y": "YEAR", "YY": "YEAR", "YYY": "YEAR", "YYYY": "YEAR", "YR": "YEAR", "YEARS": "YEAR", "YRS": "YEAR", "MM": "MONTH", "MON": "MONTH", "MONS": "MONTH", "MONTHS": "MONTH", "D": "DAY", "DD": "DAY", "DAYS": "DAY", "DAYOFMONTH": "DAY", "DAY OF WEEK": "DAYOFWEEK", "WEEKDAY": "DAYOFWEEK", "DOW": "DAYOFWEEK", "DW": "DAYOFWEEK", "WEEKDAY_ISO": "DAYOFWEEKISO", "DOW_ISO": "DAYOFWEEKISO", "DW_ISO": "DAYOFWEEKISO", "DAYOFWEEK_ISO": "DAYOFWEEKISO", "DAY OF YEAR": "DAYOFYEAR", "DOY": "DAYOFYEAR", "DY": "DAYOFYEAR", "W": "WEEK", "WK": "WEEK", "WEEKOFYEAR": "WEEK", "WOY": "WEEK", "WY": "WEEK", "WEEK_ISO": "WEEKISO", "WEEKOFYEARISO": "WEEKISO", "WEEKOFYEAR_ISO": "WEEKISO", "Q": "QUARTER", "QTR": "QUARTER", "QTRS": "QUARTER", "QUARTERS": "QUARTER", "H": "HOUR", "HH": "HOUR", "HR": "HOUR", "HOURS": "HOUR", "HRS": "HOUR", "M": "MINUTE", "MI": "MINUTE", "MIN": "MINUTE", "MINUTES": "MINUTE", "MINS": "MINUTE", "S": "SECOND", "SEC": "SECOND", "SECONDS": "SECOND", "SECS": "SECOND", "MS": "MILLISECOND", "MSEC": "MILLISECOND", "MSECS": "MILLISECOND", "MSECOND": "MILLISECOND", "MSECONDS": "MILLISECOND", "MILLISEC": "MILLISECOND", "MILLISECS": "MILLISECOND", "MILLISECON": "MILLISECOND", "MILLISECONDS": "MILLISECOND", "US": "MICROSECOND", "USEC": "MICROSECOND", "USECS": "MICROSECOND", "MICROSEC": "MICROSECOND", "MICROSECS": "MICROSECOND", "USECOND": "MICROSECOND", "USECONDS": "MICROSECOND", "MICROSECONDS": "MICROSECOND", "NS": "NANOSECOND", "NSEC": "NANOSECOND", "NANOSEC": "NANOSECOND", "NSECOND": "NANOSECOND", "NSECONDS": "NANOSECOND", "NANOSECS": "NANOSECOND", "EPOCH_SECOND": "EPOCH", "EPOCH_SECONDS": "EPOCH", "EPOCH_MILLISECONDS": "EPOCH_MILLISECOND", "EPOCH_MICROSECONDS": "EPOCH_MICROSECOND", "EPOCH_NANOSECONDS": "EPOCH_NANOSECOND", "TZH": "TIMEZONE_HOUR", "TZM": "TIMEZONE_MINUTE", "DEC": "DECADE", "DECS": "DECADE", "DECADES": "DECADE", "MIL": "MILLENNIUM", "MILS": "MILLENNIUM", "MILLENIA": "MILLENNIUM", "C": "CENTURY", "CENT": "CENTURY", "CENTS": "CENTURY", "CENTURIES": "CENTURY"}
	c.DefaultNullType = exp.DTypeUnknown
	c.DisablesAliasRefExpansion = false
	c.DpipeIsStringConcat = true
	c.ExcludesPseudocolumnsFromStar = false
	c.ExpandOnlyGroupAliasRef = false
	c.ForceEarlyAliasRefExpansion = false
	c.HasDistinctArrayConstructors = false
	c.HexLowercase = false
	c.HexStringIsIntegerType = false
	c.IdentifiersCanStartWithDigit = false
	c.IndexOffset = 0
	c.InitcapDefaultDelimiterChars = " \t\n\r\x0c\x0b!\"#$%&'()*+,\\-./:;<=>?@\\[\\]^_`{|}~"
	c.InitcapSupportsCustomDelimiters = true
	c.IntersectBindsTighterThanUnionAndExcept = true
	c.JsonExtractScalarScalarOnly = false
	c.JsonPathSingleDotIsWildcard = false
	c.LeastGreatestIgnoresNulls = true
	c.LogBaseFirst = Bool(true)
	c.NormalizationStrategy = "LOWERCASE"
	c.NormalizeFunctions = "upper"
	c.NormalizeNotNull = true
	c.NullOrdering = "nulls_are_small"
	c.NumbersCanBeUnderscoreSeparated = false
	c.OnConditionEmptyBeforeError = true
	c.OriginalNameMetaKey = ""
	c.PreferCteAliasColumn = false
	c.PrioritizeNonLiteralTypes = false
	c.ProjectionAliasesShadowSourceNames = false
	c.PromoteToInferredDatetimeType = false
	c.Pseudocolumns = map[string]bool{}
	c.QueryResultsAreStructs = false
	c.RegexpExtractDefaultGroup = 0
	c.RegexpExtractPositionOverflowReturnsNull = true
	c.RequiresParenthesizedStructAccess = false
	c.SafeDivision = false
	c.SafeToEliminateDoubleNegation = true
	c.SetOpDistinctByDefault = map[exp.Kind]*bool{exp.Except: Bool(true), exp.Intersect: Bool(true), exp.Union: Bool(true)}
	c.StarIlikeBackslashEscape = false
	c.StrictJsonPathSyntax = true
	c.StrictStringConcat = false
	c.SupportedSettings = map[string]bool{"normalization_strategy": true, "version": true}
	c.SupportsAliasRefsInJoinConditions = false
	c.SupportsFixedSizeArrays = false
	c.SupportsLimitAll = false
	c.SupportsNullType = false
	c.SupportsOrderByAll = false
	c.SupportsPositionalColumnRefs = false
	c.SupportsStructStarExpansion = false
	c.SupportsUserDefinedTypes = true
	c.SupportsValuesDefault = true
	c.TablesampleSizeIsPercent = false
	c.TablesReferenceableAsColumns = false
	c.TableAliasesShadowColumnNames = false
	c.TimeFormat = "'%Y-%m-%d %H:%M:%S'"
	c.TryCastRequiresString = nil
	c.TypedDivision = false
	c.UnnestColumnOnly = false
	c.UsingColumnOrder = "USING_LIST"
	c.UuidIsStringType = false
	c.WeekOffset = 0
	c.TimeMapping = map[string]string{}
	c.FormatMapping = map[string]string{}
	c.UnescapedSequences = map[string]string{}
	c.InverseVectorTypeAliases = map[string]string{}
	c.CreatableKindMapping = map[string]string{}
	c.DefaultFunctionsColumnNames = map[exp.Kind][]string{}
	c.CoercesTo = map[exp.DType]exp.TypeSet{}
	c.ExpressionMetadata = BaseExpressionMetadata()
	c.Extra = map[string]any{}
	c.ValidIntervalUnits = map[string]bool{}

	c.Tokenizer = tokens.BaseConfig()
	c.JSONPathTokenizer = JSONPathTokenizerConfig()
	c.Parser = NewParserConfig()
	baseParserOverrides(c.Parser)
	c.Generator = NewGeneratorConfig()

	c.Methods = DialectMethods{
		NormalizeIdentifier:   baseNormalizeIdentifier,
		CaseSensitive:         baseCaseSensitive,
		CanQuote:              baseCanQuote,
		QuoteIdentifier:       baseQuoteIdentifier,
		ToJSONPath:            baseToJSONPath,
		GenerateValuesAliases: baseGenerateValuesAliases,
	}
	return c
}

// baseParserOverrides applies sqlglot/parsers/base.py (BaseParser) on top of parser.Parser.
func baseParserOverrides(c *ParserConfig) {
	c.NoParenFunctions[tokens.Localtime] = exp.Localtime
	c.NoParenFunctions[tokens.Localtimestamp] = exp.Localtimestamp
	c.NoParenFunctions[tokens.CurrentCatalog] = exp.CurrentCatalog
	c.NoParenFunctions[tokens.SessionUser] = exp.SessionUser
	c.IdVarTokens = c.IdVarTokens.Without(tokens.StraightJoin)
	c.TableAliasTokens = c.TableAliasTokens.Without(tokens.StraightJoin)
}
