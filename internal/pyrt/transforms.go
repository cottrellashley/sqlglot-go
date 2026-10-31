package pyrt

// Bindings for sqlglot.transforms.

import (
	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
)

func transformFn(name string, f sqlglot.Transform) *Fn {
	return Func(name, func(a Args) Value {
		return Guard(func() Value {
			e, ok := norm(a.Pos[0]).(*exp.Expr)
			if !ok {
				Raise(TypeError, "%s expects an expression, got %s", name, TypeName(a.Pos[0]))
			}
			return f(e)
		})
	})
}

func init() {
	for name, f := range map[string]sqlglot.Transform{
		"unnest_generate_date_array_using_recursive_cte": sqlglot.UnnestGenerateDateArrayUsingRecursiveCte,
		"unnest_generate_series":                         sqlglot.UnnestGenerateSeries,
		"eliminate_distinct_on":                          sqlglot.EliminateDistinctOn,
		"eliminate_qualify":                              sqlglot.EliminateQualify,
		"remove_precision_parameterized_types":           sqlglot.RemovePrecisionParameterizedTypes,
		"unqualify_unnest":                               sqlglot.UnqualifyUnnest,
		"add_within_group_for_percentiles":               sqlglot.AddWithinGroupForPercentiles,
		"remove_within_group_for_percentiles":            sqlglot.RemoveWithinGroupForPercentiles,
		"add_recursive_cte_column_names":                 sqlglot.AddRecursiveCteColumnNames,
		"epoch_cast_to_ts":                               sqlglot.EpochCastToTs,
		"eliminate_semi_and_anti_joins":                  sqlglot.EliminateSemiAndAntiJoins,
		"eliminate_full_outer_join":                      sqlglot.EliminateFullOuterJoin,
		"move_ctes_to_top_level":                         sqlglot.MoveCtesToTopLevel,
		"ensure_bools":                                   sqlglot.EnsureBools,
		"unqualify_columns":                              sqlglot.UnqualifyColumns,
		"unqualify_pivot_fields":                         sqlglot.UnqualifyPivotFields,
		"remove_unique_constraints":                      sqlglot.RemoveUniqueConstraints,
		"move_schema_columns_to_partitioned_by":          sqlglot.MoveSchemaColumnsToPartitionedBy,
		"move_partitioned_by_to_schema_columns":          sqlglot.MovePartitionedByToSchemaColumns,
		"struct_kv_to_alias":                             sqlglot.StructKvToAlias,
		"eliminate_join_marks":                           sqlglot.EliminateJoinMarks,
		"any_to_exists":                                  sqlglot.AnyToExists,
		"eliminate_window_clause":                        sqlglot.EliminateWindowClause,
		"inherit_struct_field_names":                     sqlglot.InheritStructFieldNames,
	} {
		sqlglotModule.Attrs[name] = transformFn(name, f)
	}
	sqlglotModule.Attrs["unnest_to_explode"] = Func("unnest_to_explode", func(a Args) Value {
		return Guard(func() Value {
			return sqlglot.UnnestToExplode(truthArg(a, 1, "unnest_using_arrays_zip", true))(norm(a.Pos[0]).(*exp.Expr))
		})
	})
	sqlglotModule.Attrs["explode_projection_to_unnest"] = Func("explode_projection_to_unnest", func(a Args) Value {
		indexOffset := 0
		if v := a.Get(0, "index_offset", nil); v != nil {
			indexOffset = mustInt(v)
		}
		return transformFn("explode_projection_to_unnest", sqlglot.ExplodeProjectionToUnnest(indexOffset, false))
	})
}
