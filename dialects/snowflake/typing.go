package snowflake

// Port of sqlglot/typing/snowflake.py.

import (
	"fmt"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
)

type annotator = sqlglot.TypeAnnotator

var datePartsSet = map[string]bool{"DAY": true, "WEEK": true, "MONTH": true, "QUARTER": true, "YEAR": true}

const (
	maxPrecision = 38
	maxScale     = 37
)

func typeOf(e *exp.Expr) sqlglot.TypeValue {
	if t := e.Type(); t != nil {
		return t
	}
	return nil
}

func annByArgs(args ...any) func(*annotator, *exp.Expr) *exp.Expr {
	return func(a *annotator, e *exp.Expr) *exp.Expr { return a.AnnotateByArgs(e, args, false, false) }
}

func setTypeFromStr(sql string) func(*annotator, *exp.Expr) *exp.Expr {
	return func(a *annotator, e *exp.Expr) *exp.Expr {
		return a.SetType(e, exp.MustDataTypeBuild(sql, "snowflake", false))
	}
}

func annotateReverse(a *annotator, e *exp.Expr) *exp.Expr {
	e = a.AnnotateByArgs(e, []any{"this"}, false, false)
	if e.IsType(exp.DTypeNull) {
		// Snowflake treats REVERSE(NULL) as a VARCHAR
		a.SetType(e, exp.DTypeVarchar)
	}
	return e
}

// annotateTimestampFromParts: TIMESTAMP_FROM_PARTS with time_zone -> TIMESTAMPTZ, without ->
// TIMESTAMP (defaults to TIMESTAMP_NTZ).
func annotateTimestampFromParts(a *annotator, e *exp.Expr) *exp.Expr {
	if e.Bool("zone") {
		a.SetType(e, exp.DTypeTimestamptz)
	} else {
		a.SetType(e, exp.DTypeTimestamp)
	}
	return e
}

func annotateDateOrTimeAdd(a *annotator, e *exp.Expr) *exp.Expr {
	if e.This().IsType(exp.DTypeDate) && !datePartsSet[e.TextUpper("unit")] {
		a.SetType(e, exp.DTypeTimestampntz)
	} else {
		a.AnnotateByArgs(e, []any{"this"}, false, false)
	}
	return e
}

// annotateDecodeCase infers DECODE(expr, val1, ret1, val2, ret2, ..., default) from its return
// values only (ret1, ret2, ..., default).
func annotateDecodeCase(a *annotator, e *exp.Expr) *exp.Expr {
	expressions := e.Expressions()
	var returnTypes []sqlglot.TypeValue
	for i := 2; i < len(expressions); i += 2 {
		returnTypes = append(returnTypes, typeOf(expressions[i]))
	}
	// If the total number of expressions is even, the last one is the default
	if len(expressions)%2 == 0 {
		returnTypes = append(returnTypes, typeOf(expressions[len(expressions)-1]))
	}
	var lastType sqlglot.TypeValue
	for _, retType := range returnTypes {
		base := lastType
		if base == nil {
			base = retType
		}
		lastType = a.MaybeCoerce(base, retType)
	}
	a.SetType(e, lastType)
	return e
}

func annotateArgMaxMin(a *annotator, e *exp.Expr) *exp.Expr {
	if e.Bool("count") {
		a.SetType(e, exp.DTypeArray)
	} else {
		a.SetType(e, typeOf(e.This()))
	}
	return e
}

// annotateWithinGroup types PERCENTILE_DISC/PERCENTILE_CONT ... WITHIN GROUP (ORDER BY x) as x.
func annotateWithinGroup(a *annotator, e *exp.Expr) *exp.Expr {
	if orderExpr := e.Expression(); e.This().IsAny(exp.PercentileDisc, exp.PercentileCont) &&
		orderExpr.IsKind(exp.Order) && len(orderExpr.Expressions()) == 1 && orderExpr.Expressions()[0].IsKind(exp.Ordered) {
		a.SetType(e, typeOf(orderExpr.Expressions()[0].This()))
	} else {
		a.SetType(e, typeOf(e.This()))
	}
	return e
}

func intParam(exprs []*exp.Expr, i int, def int) int {
	if i >= len(exprs) || exprs[i] == nil {
		return def
	}
	v, err := exprs[i].This().ToPy()
	if n, ok := v.(int); ok && err == nil {
		return n
	}
	return def
}

// annotateMedian: FLOAT/DOUBLE -> DOUBLE, NUMBER(p, s) -> NUMBER(min(p+3, 38), min(s+3, 37)).
func annotateMedian(a *annotator, e *exp.Expr) *exp.Expr {
	e = a.AnnotateByArgs(e, []any{"this"}, false, false)
	inputType := e.This().Type()
	if inputType.IsType(exp.DTypeDouble) {
		a.SetType(e, exp.DTypeDouble)
	} else {
		var exprs []*exp.Expr
		if inputType != nil {
			exprs = inputType.Expressions()
		}
		precision := intParam(exprs, 0, maxPrecision)
		scale := intParam(exprs, 1, 0)
		newType := exp.MustDataTypeBuild(fmt.Sprintf("NUMBER(%d, %d)", min(precision+3, maxPrecision), min(scale+3, maxScale)), "snowflake", false)
		a.SetType(e, newType)
	}
	return e
}

// annotateVariance: DECFLOAT -> DECFLOAT(38), FLOAT/DOUBLE -> DOUBLE, INT/NUMBER(p, 0) ->
// NUMBER(38, 6), NUMBER(p, s) -> NUMBER(38, max(12, s)).
func annotateVariance(a *annotator, e *exp.Expr) *exp.Expr {
	e = a.AnnotateByArgs(e, []any{"this"}, false, false)
	inputType := e.This().Type()
	switch {
	case inputType.IsType(exp.DTypeDecfloat):
		a.SetType(e, exp.MustDataTypeBuild("DECFLOAT", "snowflake", false))
	case inputType.IsType(exp.DTypeFloat, exp.DTypeDouble):
		a.SetType(e, exp.DTypeDouble)
	default:
		var exprs []*exp.Expr
		if inputType != nil {
			exprs = inputType.Expressions()
		}
		scale := intParam(exprs, 1, 0)
		newScale := max(12, scale)
		if scale == 0 {
			newScale = 6
		}
		a.SetType(e, exp.MustDataTypeBuild(fmt.Sprintf("NUMBER(%d, %d)", maxPrecision, newScale), "snowflake", false))
	}
	return e
}

// annotateKurtosis: DECFLOAT -> DECFLOAT, DOUBLE/FLOAT -> DOUBLE, others -> NUMBER(38, 12).
func annotateKurtosis(a *annotator, e *exp.Expr) *exp.Expr {
	e = a.AnnotateByArgs(e, []any{"this"}, false, false)
	inputType := e.This().Type()
	switch {
	case inputType.IsType(exp.DTypeDecfloat):
		a.SetType(e, exp.MustDataTypeBuild("DECFLOAT", "snowflake", false))
	case inputType.IsType(exp.DTypeFloat, exp.DTypeDouble):
		a.SetType(e, exp.DTypeDouble)
	default:
		a.SetType(e, exp.MustDataTypeBuild(fmt.Sprintf("NUMBER(%d, 12)", maxPrecision), "snowflake", false))
	}
	return e
}

// annotateMathWithFloatDecfloat: math functions that preserve DECFLOAT and return DOUBLE
// otherwise.
func annotateMathWithFloatDecfloat(a *annotator, e *exp.Expr) *exp.Expr {
	e = a.AnnotateByArgs(e, []any{"this"}, false, false)
	if e.This().IsType(exp.DTypeDecfloat) {
		a.SetType(e, typeOf(e.This()))
	} else {
		a.SetType(e, exp.DTypeDouble)
	}
	return e
}

func annotateStrToTime(a *annotator, e *exp.Expr) *exp.Expr {
	// target_type is stored as a DataType instance
	targetType := exp.DTypeTimestamp
	if t := e.Get("target_type"); t.IsKind(exp.DataType) {
		targetType = t.DataTypeThis()
	}
	a.SetType(e, targetType)
	return e
}

func annotateFloorCeil(a *annotator, e *exp.Expr) *exp.Expr {
	if e.This().IsTypeIn(exp.DataTypeTextTypes) {
		// A scale argument changes Snowflake's implicit string coercion from FLOAT to NUMBER.
		if e.Get("decimals") == nil {
			return a.SetType(e, exp.DTypeDouble)
		}
		return a.SetType(e, exp.DTypeUnknown)
	}
	if e.This().IsType(exp.DTypeVariant) {
		return a.SetType(e, exp.DTypeDouble)
	}
	return sqlglot.AnnotateByNumericArg(a, e)
}

func annotateDoubleIfFloatElseInt(a *annotator, e *exp.Expr) *exp.Expr {
	if e.This().IsTypeIn(exp.DataTypeFloatTypes) {
		return a.SetType(e, exp.DTypeDouble)
	}
	return a.SetType(e, exp.DTypeInt)
}

func annotateConvertTimezone(a *annotator, e *exp.Expr) *exp.Expr {
	if e.Bool("source_tz") {
		return a.SetType(e, exp.DTypeTimestampntz)
	}
	return a.SetType(e, exp.DTypeTimestamptz)
}

func typing(m map[exp.Kind]*sqlglot.ExprMetadata) {
	m[exp.Collate] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.Pow] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Ntile] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.Rank] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.DenseRank] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.RowNumber] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.Atan] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Cbrt] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Tan] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Log] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.VariancePop] = &sqlglot.ExprMetadata{Annotator: annotateVariance}
	m[exp.Ln] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Asin] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Sqrt] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Sin] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Cot] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Rand] = &sqlglot.ExprMetadata{Returns: exp.DTypeBigint}
	m[exp.Round] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.Cos] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Radians] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Degrees] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Exp] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Tanh] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Acos] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Kurtosis] = &sqlglot.ExprMetadata{Annotator: annotateKurtosis}
	m[exp.Variance] = &sqlglot.ExprMetadata{Annotator: annotateVariance}
	m[exp.Sign] = &sqlglot.ExprMetadata{Annotator: annotateDoubleIfFloatElseInt}
	m[exp.Quarter] = &sqlglot.ExprMetadata{Returns: exp.DTypeTinyint}
	m[exp.DayOfYear] = &sqlglot.ExprMetadata{Returns: exp.DTypeTinyint}
	m[exp.DayOfWeek] = &sqlglot.ExprMetadata{Returns: exp.DTypeTinyint}
	m[exp.DayOfMonth] = &sqlglot.ExprMetadata{Returns: exp.DTypeTinyint}
	m[exp.TimeAdd] = &sqlglot.ExprMetadata{Annotator: annotateDateOrTimeAdd}
	m[exp.CurrentTimestamp] = &sqlglot.ExprMetadata{Returns: exp.DTypeTimestampltz}
	m[exp.StrToTime] = &sqlglot.ExprMetadata{Annotator: annotateStrToTime}
	m[exp.Substring] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.ConcatWs] = &sqlglot.ExprMetadata{Annotator: annByArgs("expressions")}
	m[exp.Ceil] = &sqlglot.ExprMetadata{Annotator: annotateFloorCeil}
	m[exp.Floor] = &sqlglot.ExprMetadata{Annotator: annotateFloorCeil}
	m[exp.ArrayConcat] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.DateTrunc] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.DateAdd] = &sqlglot.ExprMetadata{Annotator: annotateDateOrTimeAdd}
	m[exp.Array] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.ArrayAgg] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.HexString] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.WithinGroup] = &sqlglot.ExprMetadata{Annotator: annotateWithinGroup}
	m[exp.TimeSlice] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.Stuff] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.AddMonths] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.Pad] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.Right] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.TimestampTrunc] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.Mode] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.Left] = &sqlglot.ExprMetadata{Annotator: annByArgs("this")}
	m[exp.ApproxTopK] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.ApproxTopKEstimate] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.ArrayAppend] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.ArrayCompact] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.ArrayConstructCompact] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.ArrayPrepend] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.ArrayRemove] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.ArraysZip] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.ArrayUniqueAgg] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.ArrayUnionAgg] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.MapKeys] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.RegexpExtractAll] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.Split] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.StringToArray] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.StrtokToArray] = &sqlglot.ExprMetadata{Returns: exp.DTypeArray}
	m[exp.BitmapCount] = &sqlglot.ExprMetadata{Returns: exp.DTypeBigint}
	m[exp.BitmapBucketNumber] = &sqlglot.ExprMetadata{Returns: exp.DTypeBigint}
	m[exp.MD5NumberUpper64] = &sqlglot.ExprMetadata{Returns: exp.DTypeBigint}
	m[exp.Seq8] = &sqlglot.ExprMetadata{Returns: exp.DTypeBigint}
	m[exp.Zipf] = &sqlglot.ExprMetadata{Returns: exp.DTypeBigint}
	m[exp.GroupingId] = &sqlglot.ExprMetadata{Returns: exp.DTypeBigint}
	m[exp.BitmapBitPosition] = &sqlglot.ExprMetadata{Returns: exp.DTypeBigint}
	m[exp.MD5NumberLower64] = &sqlglot.ExprMetadata{Returns: exp.DTypeBigint}
	m[exp.Factorial] = &sqlglot.ExprMetadata{Returns: exp.DTypeBigint}
	m[exp.Compress] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.DecryptRaw] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.EncryptRaw] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.MD5Digest] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.SHA1Digest] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.SHA2Digest] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.Unhex] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.Base64DecodeBinary] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.TryBase64DecodeBinary] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.TryHexDecodeBinary] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.BitmapConstructAgg] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.DecompressBinary] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.BitmapOrAgg] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.Decrypt] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.Encrypt] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.ToBinary] = &sqlglot.ExprMetadata{Returns: exp.DTypeBinary}
	m[exp.Booland] = &sqlglot.ExprMetadata{Returns: exp.DTypeBoolean}
	m[exp.Boolor] = &sqlglot.ExprMetadata{Returns: exp.DTypeBoolean}
	m[exp.EqualNull] = &sqlglot.ExprMetadata{Returns: exp.DTypeBoolean}
	m[exp.ToBoolean] = &sqlglot.ExprMetadata{Returns: exp.DTypeBoolean}
	m[exp.Search] = &sqlglot.ExprMetadata{Returns: exp.DTypeBoolean}
	m[exp.BoolxorAgg] = &sqlglot.ExprMetadata{Returns: exp.DTypeBoolean}
	m[exp.Boolnot] = &sqlglot.ExprMetadata{Returns: exp.DTypeBoolean}
	m[exp.IsNullValue] = &sqlglot.ExprMetadata{Returns: exp.DTypeBoolean}
	m[exp.SearchIp] = &sqlglot.ExprMetadata{Returns: exp.DTypeBoolean}
	m[exp.MapContainsKey] = &sqlglot.ExprMetadata{Returns: exp.DTypeBoolean}
	m[exp.NextDay] = &sqlglot.ExprMetadata{Returns: exp.DTypeDate}
	m[exp.PreviousDay] = &sqlglot.ExprMetadata{Returns: exp.DTypeDate}
	m[exp.BitwiseAndAgg] = &sqlglot.ExprMetadata{Annotator: setTypeFromStr("NUMBER")}
	m[exp.BitwiseOrAgg] = &sqlglot.ExprMetadata{Annotator: setTypeFromStr("NUMBER")}
	m[exp.BitwiseXorAgg] = &sqlglot.ExprMetadata{Annotator: setTypeFromStr("NUMBER")}
	m[exp.RegexpCount] = &sqlglot.ExprMetadata{Annotator: setTypeFromStr("NUMBER")}
	m[exp.RegexpInstr] = &sqlglot.ExprMetadata{Annotator: setTypeFromStr("NUMBER")}
	m[exp.ToNumber] = &sqlglot.ExprMetadata{Annotator: setTypeFromStr("NUMBER")}
	m[exp.EuclideanDistance] = &sqlglot.ExprMetadata{Returns: exp.DTypeDouble}
	m[exp.CosineDistance] = &sqlglot.ExprMetadata{Returns: exp.DTypeDouble}
	m[exp.ApproximateSimilarity] = &sqlglot.ExprMetadata{Returns: exp.DTypeDouble}
	m[exp.ManhattanDistance] = &sqlglot.ExprMetadata{Returns: exp.DTypeDouble}
	m[exp.MonthsBetween] = &sqlglot.ExprMetadata{Returns: exp.DTypeDouble}
	m[exp.Normal] = &sqlglot.ExprMetadata{Returns: exp.DTypeDouble}
	m[exp.ApproxPercentileEstimate] = &sqlglot.ExprMetadata{Returns: exp.DTypeDouble}
	m[exp.DotProduct] = &sqlglot.ExprMetadata{Returns: exp.DTypeDouble}
	m[exp.ToDecfloat] = &sqlglot.ExprMetadata{Returns: exp.DTypeDecfloat}
	m[exp.TryToDecfloat] = &sqlglot.ExprMetadata{Returns: exp.DTypeDecfloat}
	m[exp.RegrValy] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.RegrSxy] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.RegrValx] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.RegrSxx] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.RegrSyy] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.RegrSlope] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.RegrR2] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.RegrIntercept] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.RegrCount] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Atan2] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.RegrAvgy] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.RegrAvgx] = &sqlglot.ExprMetadata{Annotator: annotateMathWithFloatDecfloat}
	m[exp.Minute] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.Second] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.Seq1] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.Seq4] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.ByteLength] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.WidthBucket] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.JarowinklerSimilarity] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.RtrimmedLength] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.Seq2] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.Grouping] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.MapSize] = &sqlglot.ExprMetadata{Returns: exp.DTypeInt}
	m[exp.ParseIp] = &sqlglot.ExprMetadata{Returns: exp.DTypeObject}
	m[exp.ObjectAgg] = &sqlglot.ExprMetadata{Returns: exp.DTypeObject}
	m[exp.ApproxPercentileCombine] = &sqlglot.ExprMetadata{Returns: exp.DTypeObject}
	m[exp.ApproxTopKCombine] = &sqlglot.ExprMetadata{Returns: exp.DTypeObject}
	m[exp.XMLGet] = &sqlglot.ExprMetadata{Returns: exp.DTypeObject}
	m[exp.ParseUrl] = &sqlglot.ExprMetadata{Returns: exp.DTypeObject}
	m[exp.ApproxPercentileAccumulate] = &sqlglot.ExprMetadata{Returns: exp.DTypeObject}
	m[exp.ApproxTopKAccumulate] = &sqlglot.ExprMetadata{Returns: exp.DTypeObject}
	m[exp.MapPick] = &sqlglot.ExprMetadata{Returns: exp.DTypeMap}
	m[exp.MapInsert] = &sqlglot.ExprMetadata{Returns: exp.DTypeMap}
	m[exp.MapDelete] = &sqlglot.ExprMetadata{Returns: exp.DTypeMap}
	m[exp.MapCat] = &sqlglot.ExprMetadata{Returns: exp.DTypeMap}
	m[exp.ToFile] = &sqlglot.ExprMetadata{Returns: exp.DTypeFile}
	m[exp.TsOrDsToTime] = &sqlglot.ExprMetadata{Returns: exp.DTypeTime}
	m[exp.TimeFromParts] = &sqlglot.ExprMetadata{Returns: exp.DTypeTime}
	m[exp.Localtimestamp] = &sqlglot.ExprMetadata{Returns: exp.DTypeTimestampltz}
	m[exp.TryHexDecodeString] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentIpAddress] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.Base64Encode] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentSecondaryRoles] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.Replace] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.SplitPart] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentSchemas] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentDatabase] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.Base64DecodeString] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentWarehouse] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.Randstr] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.Soundex] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CheckJson] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.TryBase64DecodeString] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.Uuid] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.AIClassify] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CheckXml] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentClient] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentRoleType] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.RegexpExtract] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.Collation] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentAvailableRoles] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.RegexpReplace] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentAccountName] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentTransaction] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentRegion] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.AIAgg] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.DecompressString] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.Strtok] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentAccount] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentStatement] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentOrganizationUser] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.HexDecodeString] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentOrganizationName] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.SoundexP123] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.AISummarizeAgg] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.CurrentSession] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.Hex] = &sqlglot.ExprMetadata{Returns: exp.DTypeVarchar}
	m[exp.MinhashCombine] = &sqlglot.ExprMetadata{Returns: exp.DTypeVariant}
	m[exp.Minhash] = &sqlglot.ExprMetadata{Returns: exp.DTypeVariant}
	m[exp.ArgMax] = &sqlglot.ExprMetadata{Annotator: annotateArgMaxMin}
	m[exp.ArgMin] = &sqlglot.ExprMetadata{Annotator: annotateArgMaxMin}
	m[exp.ConvertTimezone] = &sqlglot.ExprMetadata{Annotator: annotateConvertTimezone}
	m[exp.DecodeCase] = &sqlglot.ExprMetadata{Annotator: annotateDecodeCase}
	m[exp.HashAgg] = &sqlglot.ExprMetadata{Annotator: setTypeFromStr("NUMBER(19, 0)")}
	m[exp.Median] = &sqlglot.ExprMetadata{Annotator: annotateMedian}
	m[exp.Reverse] = &sqlglot.ExprMetadata{Annotator: annotateReverse}
	m[exp.TimestampFromParts] = &sqlglot.ExprMetadata{Annotator: annotateTimestampFromParts}
}
