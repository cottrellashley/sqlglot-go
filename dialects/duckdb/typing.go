package duckdb

// Port of sqlglot/typing/duckdb.py.

import (
	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
)

var datetimeWithoutTZ = []any{
	exp.DTypeDate,
	exp.DTypeTimestamp,
	exp.DTypeTimestampntz,
	exp.DTypeTimestampS,
	exp.DTypeTimestampMs,
	exp.DTypeTimestampNs,
}

func typeOf(e *exp.Expr) sqlglot.TypeValue {
	if t := e.Type(); t != nil {
		return t
	}
	return nil
}

func annotateAtTimeZone(a *sqlglot.TypeAnnotator, e *exp.Expr) *exp.Expr {
	this := e.This()
	switch {
	case this.IsType(datetimeWithoutTZ...):
		return a.SetType(e, exp.DTypeTimestamptz)
	case this.IsType(exp.DTypeTimestamptz):
		return a.SetType(e, exp.DTypeTimestamp)
	case this.IsType(exp.DTypeTimetz):
		return a.SetType(e, exp.DTypeTimetz)
	}
	return a.SetType(e, exp.DTypeUnknown)
}

func annotateDateTrunc(a *sqlglot.TypeAnnotator, e *exp.Expr) *exp.Expr {
	if e.This().IsType(exp.DTypeDate, exp.DTypeTimestampS, exp.DTypeTimestampMs, exp.DTypeTimestampNs) {
		return a.SetType(e, exp.DTypeTimestamp)
	}
	return a.SetType(e, typeOf(e.This()))
}

func annotateFloorCeil(a *sqlglot.TypeAnnotator, e *exp.Expr) *exp.Expr {
	if e.This().IsType(exp.DTypeDecimal) {
		return a.SetType(e, exp.DTypeUnknown)
	}
	return sqlglot.AnnotateIntAsDouble(a, e)
}

func annotateAddSub(a *sqlglot.TypeAnnotator, e *exp.Expr) *exp.Expr {
	a.AnnotateBinary(e)
	left, right := e.This(), e.Expression()
	if e.IsKind(exp.Add) && left.IsType(exp.DTypeInterval) {
		left, right = right, left
	}
	if right.IsType(exp.DTypeInterval) {
		if left.IsType(datetimeWithoutTZ...) {
			a.SetType(e, exp.DTypeTimestamp)
		} else if left.IsType(exp.DTypeTime, exp.DTypeTimetz, exp.DTypeTimestamptz) {
			a.SetType(e, typeOf(left))
		}
	}
	return e
}

func annotateByArgs(args ...any) func(*sqlglot.TypeAnnotator, *exp.Expr) *exp.Expr {
	return func(a *sqlglot.TypeAnnotator, e *exp.Expr) *exp.Expr {
		return a.AnnotateByArgs(e, args, false, false)
	}
}

func typing(m map[exp.Kind]*sqlglot.ExprMetadata) {
	returns := func(t exp.DType, kinds ...exp.Kind) {
		for _, k := range kinds {
			m[k] = &sqlglot.ExprMetadata{Returns: t}
		}
	}
	annotator := func(fn func(*sqlglot.TypeAnnotator, *exp.Expr) *exp.Expr, kinds ...exp.Kind) {
		for _, k := range kinds {
			m[k] = &sqlglot.ExprMetadata{Annotator: fn}
		}
	}
	returns(exp.DTypeBigint, exp.BitLength, exp.DateDiff, exp.Day, exp.DayOfMonth, exp.DayOfWeek,
		exp.DayOfWeekIso, exp.DayOfYear, exp.Extract, exp.Hour, exp.Length, exp.Minute, exp.Month,
		exp.Quarter, exp.Second, exp.Week, exp.Year)
	returns(exp.DTypeInt128, exp.CountIf, exp.Factorial)
	returns(exp.DTypeDouble, exp.Atan2, exp.JarowinklerSimilarity, exp.TimeToUnix)
	returns(exp.DTypeVarchar, exp.Format, exp.Reverse, exp.Decode)
	returns(exp.DTypeVarbinary, exp.Encode, exp.Unhex)
	annotator(annotateFloorCeil, exp.Ceil, exp.Floor)
	annotator(annotateByArgs("this"), exp.BitwiseAndAgg, exp.BitwiseOrAgg, exp.BitwiseXorAgg, exp.PercentileDisc, exp.Mode)
	annotator(annotateAddSub, exp.Add, exp.Sub)
	annotator(annotateAtTimeZone, exp.AtTimeZone)
	annotator(annotateByArgs("expression"), exp.DateBin)
	annotator(annotateDateTrunc, exp.DateTrunc, exp.TimestampTrunc)
	returns(exp.DTypeTimestamp, exp.Localtimestamp)
	returns(exp.DTypeInterval, exp.ToDays)
	returns(exp.DTypeTime, exp.TimeFromParts)
	returns(exp.DTypeTimestamp, exp.TimestampFromParts)
}
