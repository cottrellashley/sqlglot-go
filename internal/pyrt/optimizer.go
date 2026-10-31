package pyrt

import (
	"fmt"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// optimizerNS holds the names of the sqlglot.optimizer package and its submodules. Imported
// names resolve by their last component, so a submodule and the function it is named after
// (sqlglot.optimizer.qualify and sqlglot.optimizer.qualify.qualify) are one ModFn value.
var optimizerNS = map[string]Value{}

// ModFn is an optimizer function that is also the module it lives in: calling it calls the
// function, and its attributes are the names of the optimizer package.
type ModFn struct {
	Fn *Fn
	// Rule is the Go rule used when the function is passed to optimize(rules=[...]).
	Rule sqlglot.Rule
}

// PyCall implements calling.
func (m *ModFn) PyCall(a Args) Value {
	if sk := firstSkipped(a); sk != nil {
		skipEvents++
		return sk
	}
	return m.Fn.F(a)
}

// PyAttr implements Attrer.
func (m *ModFn) PyAttr(name string) (Value, bool) {
	switch name {
	case "__name__":
		return m.Fn.Name, true
	}
	v, ok := optimizerNS[name]
	return v, ok
}

var optimizerModule = &Module{Name: "sqlglot.optimizer", Get: func(name string) (Value, bool) {
	v, ok := optimizerNS[name]
	return v, ok
}}

// defOpt registers an optimizer function in the optimizer namespace and the sqlglot module.
func defOpt(name string, rule sqlglot.Rule, f func(a Args) Value) *ModFn {
	m := &ModFn{Fn: Func(name, func(a Args) Value { return Guard(func() Value { return f(a) }) }), Rule: rule}
	optimizerNS[name] = m
	sqlglotModule.Attrs[name] = m
	return m
}

func exprArg(a Args, i int, name string, dialect any) *exp.Expr {
	v := norm(a.Get(i, name, nil))
	if e, ok := v.(*exp.Expr); ok {
		return e
	}
	e, err := exp.MaybeParse(toGo(v), exp.ParseOptions{Dialect: dialect}, "", false)
	check(err)
	return e
}

func truthArg(a Args, i int, name string, def bool) bool { return Truth(a.Get(i, name, def)) }

func onQualifyArg(a Args, i int) func(*exp.Expr) {
	f := a.Get(i, "on_qualify", nil)
	if f == nil {
		return nil
	}
	return func(t *exp.Expr) { Call(f, t) }
}

// journalArg converts a Python journal (a list of recorded entries) for the Go API; sync
// writes the Go journal back into the list.
func journalArg(a Args, i int) (j *sqlglot.Journal, sync func()) {
	l, ok := norm(a.Get(i, "journal", nil)).(*List)
	if !ok {
		return nil, func() {}
	}
	j = &sqlglot.Journal{}
	for _, v := range l.V {
		*j = append(*j, v.(*JournalEntryV).E)
	}
	return j, func() {
		l.V = l.V[:0]
		for _, e := range *j {
			l.V = append(l.V, &JournalEntryV{e})
		}
	}
}

// JournalEntryV is one recorded argument mutation of an optimizer journal.
type JournalEntryV struct{ E sqlglot.JournalEntry }

func qualifyOptions(a Args) sqlglot.QualifyOptions {
	d := dialectArg(a.Get(1, "dialect", nil))
	o := sqlglot.QualifyOptions{
		Dialect: d,
		DB:      toGo(a.Get(2, "db", nil)),
		Catalog: toGo(a.Get(3, "catalog", nil)),
	}
	if s := a.Get(4, "schema", nil); s != nil {
		o.Schema = toSchema(s)
	}
	o.NoExpandAliasRefs = !truthArg(a, 5, "expand_alias_refs", true)
	o.NoExpandStars = !truthArg(a, 6, "expand_stars", true)
	o.IsolateTables = truthArg(a, 7, "isolate_tables", false)
	o.NoQualifyColumns = !truthArg(a, 8, "qualify_columns", true)
	o.AllowPartialQualification = truthArg(a, 9, "allow_partial_qualification", false)
	o.NoValidateQualifyColumns = !truthArg(a, 10, "validate_qualify_columns", true)
	o.NoQuoteIdentifiers = !truthArg(a, 11, "quote_identifiers", true)
	o.NoIdentify = !truthArg(a, 12, "identify", true)
	o.CanonicalizeTableAliases = truthArg(a, 13, "canonicalize_table_aliases", false)
	o.OnQualify = onQualifyArg(a, 14)
	if s := a.Get(15, "sql", nil); s != nil {
		o.SQL = Str(s)
	}
	return o
}

func init() {
	sqlglotModule.Attrs["optimizer"] = optimizerModule
	for _, sub := range []string{"scope", "journal", "resolver", "helper"} {
		optimizerNS[sub] = optimizerModule
	}
	optimizerNS["logger"] = &LoggerV{"sqlglot"}

	optimize := defOpt("optimize", nil, func(a Args) Value {
		o := sqlglot.OptimizeOptions{
			DB:      toGo(a.Get(2, "db", nil)),
			Catalog: toGo(a.Get(3, "catalog", nil)),
			Dialect: dialectArg(a.Get(4, "dialect", nil)),
			NoCopy:  !truthArg(a, 7, "copy", true),
		}
		if s := a.Get(1, "schema", nil); s != nil {
			o.Schema = toSchema(s)
		}
		if rules := a.Get(5, "rules", nil); rules != nil {
			o.Rules = []sqlglot.Rule{}
			for _, r := range Iter(rules) {
				m, ok := norm(r).(*ModFn)
				if !ok || m.Rule == nil {
					NotPorted("optimize(rules=[%s])", Repr(r))
				}
				o.Rules = append(o.Rules, m.Rule)
			}
		}
		if s := a.Get(6, "sql", nil); s != nil {
			o.SQL = Str(s)
		}
		for _, kw := range a.KW {
			switch kw.K {
			case "schema", "db", "catalog", "dialect", "rules", "sql", "copy":
			case "identify":
				o.NoIdentify = !Truth(kw.V)
			case "leave_tables_isolated":
				o.LeaveTablesIsolated = Truth(kw.V)
			case "on_qualify":
				f := kw.V
				o.OnQualify = func(t *exp.Expr) { Call(f, t) }
			case "validate_qualify_columns":
				o.NoValidateQualifyColumns = !Truth(kw.V)
			case "canonicalize_table_aliases":
				o.CanonicalizeTableAliases = Truth(kw.V)
			case "expand_alias_refs":
				o.NoExpandAliasRefs = !Truth(kw.V)
			case "expand_stars":
				o.NoExpandStars = !Truth(kw.V)
			case "isolate_tables":
				o.NoIsolateTables = !Truth(kw.V)
			case "quote_identifiers":
				o.QuoteIdentifiers = Truth(kw.V)
			case "allow_partial_qualification":
				o.AllowPartialQualification = Truth(kw.V)
			default:
				NotPorted("optimize(%s=...)", kw.K)
			}
		}
		out, err := sqlglot.Optimize(toGo(a.Pos[0]), o)
		check(err)
		return out
	})
	optimizerNS["optimizer"] = optimize

	defOpt("qualify", sqlglot.RuleQualify, func(a Args) Value {
		o := qualifyOptions(a)
		out, err := sqlglot.Qualify(exprArg(a, 0, "expression", o.Dialect), o)
		check(err)
		return out
	})
	defOpt("qualify_tables", sqlglot.RuleQualifyTables, func(a Args) Value {
		d := dialectArg(a.Get(4, "dialect", nil))
		return sqlglot.QualifyTables(exprArg(a, 0, "expression", d), sqlglot.QualifyTablesOptions{
			DB: toGo(a.Get(1, "db", nil)), Catalog: toGo(a.Get(2, "catalog", nil)),
			OnQualify: onQualifyArg(a, 3), Dialect: d,
			CanonicalizeTableAliases: truthArg(a, 5, "canonicalize_table_aliases", false),
		})
	})
	defOpt("qualify_columns", sqlglot.RuleQualifyColumns, func(a Args) Value {
		d := dialectArg(a.Get(5, "dialect", nil))
		return sqlglot.QualifyColumns(exprArg(a, 0, "expression", d), toSchema(a.Get(1, "schema", nil)), sqlglot.QualifyColumnsOptions{
			NoExpandAliasRefs:         !truthArg(a, 2, "expand_alias_refs", true),
			NoExpandStars:             !truthArg(a, 3, "expand_stars", true),
			AllowPartialQualification: truthArg(a, 4, "allow_partial_qualification", false),
			Dialect:                   d,
		})
	})
	defOpt("validate_qualify_columns", nil, func(a Args) Value {
		sql := ""
		if s := a.Get(1, "sql", nil); s != nil {
			sql = Str(s)
		}
		return sqlglot.ValidateQualifyColumns(exprArg(a, 0, "expression", nil), sql)
	})
	defOpt("quote_identifiers", sqlglot.RuleQuoteIdentifiers, func(a Args) Value {
		d := dialectArg(a.Get(1, "dialect", nil))
		return sqlglot.QuoteIdentifiers(exprArg(a, 0, "expression", d), d, truthArg(a, 2, "identify", true))
	})
	defOpt("pushdown_cte_alias_columns", nil, func(a Args) Value {
		sqlglot.PushdownCteAliasColumns(scopeArg(a.Pos[0]))
		return nil
	})
	defOpt("isolate_table_selects", nil, func(a Args) Value {
		d := dialectArg(a.Get(2, "dialect", nil))
		var schema sqlglot.Schema
		if s := a.Get(1, "schema", nil); s != nil {
			var err error
			schema, err = sqlglot.EnsureSchema(toSchema(s), d)
			check(err)
		}
		return sqlglot.IsolateTableSelects(exprArg(a, 0, "expression", d), schema, d)
	})
	defOpt("normalize_identifiers", nil, func(a Args) Value {
		return sqlglot.NormalizeIdentifiersOpts(toGo(a.Pos[0]), dialectArg(a.Get(1, "dialect", nil)),
			truthArg(a, 2, "store_original_column_identifiers", false))
	})
	defOpt("normalize", sqlglot.RuleNormalize, func(a Args) Value {
		maxDistance := 128
		if v := a.Get(2, "max_distance", nil); v != nil {
			maxDistance = mustInt(v)
		}
		return sqlglot.Normalize(exprArg(a, 0, "expression", nil), truthArg(a, 1, "dnf", false), maxDistance)
	})
	defOpt("normalization_distance", nil, func(a Args) Value {
		maxDistance := -1
		if v := a.Get(2, "max_", nil); v != nil {
			maxDistance = mustInt(v)
		}
		return sqlglot.NormalizationDistance(exprArg(a, 0, "expression", nil), truthArg(a, 1, "dnf", false), maxDistance)
	})
	defOpt("simplify", sqlglot.RuleSimplify, func(a Args) Value {
		d := dialectArg(a.Get(3, "dialect", nil))
		return sqlglot.SimplifyOpts(exprArg(a, 0, "expression", d), truthArg(a, 1, "constant_propagation", false),
			truthArg(a, 2, "coalesce_simplification", false), d)
	})
	defOpt("gen", nil, func(a Args) Value {
		return sqlglot.Gen(exprArg(a, 0, "expression", nil), truthArg(a, 1, "comments", false))
	})
	defOpt("canonicalize", sqlglot.RuleCanonicalize, func(a Args) Value {
		d := dialectArg(a.Get(1, "dialect", nil))
		return sqlglot.Canonicalize(exprArg(a, 0, "expression", d), d)
	})
	defOpt("annotate_types", sqlglot.RuleAnnotateTypes, func(a Args) Value {
		d := dialectArg(a.Get(4, "dialect", nil))
		o := sqlglot.AnnotateOptions{Dialect: d}
		if s := a.Get(1, "schema", nil); s != nil {
			o.Schema = toSchema(s)
		}
		if a.Get(2, "expression_metadata", nil) != nil {
			NotPorted("annotate_types(expression_metadata=...)")
		}
		if a.Get(3, "coerces_to", nil) != nil {
			NotPorted("annotate_types(coerces_to=...)")
		}
		o.NoOverwriteTypes = !truthArg(a, 5, "overwrite_types", true)
		out, err := sqlglot.AnnotateTypesOpts(exprArg(a, 0, "expression", d), o)
		check(err)
		return out
	})
	defOpt("unnest_subqueries", sqlglot.RuleUnnestSubqueries, func(a Args) Value {
		return sqlglot.UnnestSubqueries(exprArg(a, 0, "expression", nil))
	})
	defOpt("pushdown_predicates", sqlglot.RulePushdownPredicates, func(a Args) Value {
		d := dialectArg(a.Get(1, "dialect", nil))
		return sqlglot.PushdownPredicates(exprArg(a, 0, "expression", d), d)
	})
	defOpt("pushdown_projections", sqlglot.RulePushdownProjections, func(a Args) Value {
		j, sync := journalArg(a, 1)
		defer sync()
		return sqlglot.PushdownProjections(exprArg(a, 0, "expression", nil), j)
	})
	defOpt("optimize_joins", sqlglot.RuleOptimizeJoins, func(a Args) Value {
		return sqlglot.OptimizeJoins(exprArg(a, 0, "expression", nil))
	})
	defOpt("reorder_joins", nil, func(a Args) Value {
		return sqlglot.ReorderJoins(exprArg(a, 0, "expression", nil))
	})
	defOpt("normalize_joins", nil, func(a Args) Value {
		return sqlglot.NormalizeJoins(exprArg(a, 0, "expression", nil))
	})
	defOpt("eliminate_subqueries", sqlglot.RuleEliminateSubqueries, func(a Args) Value {
		return sqlglot.EliminateSubqueries(exprArg(a, 0, "expression", nil))
	})
	defOpt("merge_subqueries", sqlglot.RuleMergeSubqueries, func(a Args) Value {
		o := sqlglot.MergeSubqueriesOptions{LeaveTablesIsolated: truthArg(a, 1, "leave_tables_isolated", false)}
		if a.Has(2, "max_copy_factor") {
			if v := a.Get(2, "max_copy_factor", nil); v == nil {
				o.NoCopyBudget = true
			} else {
				o.MaxCopyFactor = mustInt(v)
			}
		}
		if v := a.Get(3, "min_copy_budget", nil); v != nil {
			o.MinCopyBudget = mustInt(v)
		}
		return sqlglot.MergeSubqueries(exprArg(a, 0, "expression", nil), o)
	})
	defOpt("eliminate_joins", sqlglot.RuleEliminateJoins, func(a Args) Value {
		return sqlglot.EliminateJoins(exprArg(a, 0, "expression", nil))
	})
	defOpt("eliminate_ctes", sqlglot.RuleEliminateCtes, func(a Args) Value {
		j, sync := journalArg(a, 1)
		defer sync()
		return sqlglot.EliminateCtes(exprArg(a, 0, "expression", nil), j)
	})
	defOpt("canonicalize_internal_names", nil, func(a Args) Value {
		return sqlglot.CanonicalizeInternalNames(exprArg(a, 0, "expression", nil))
	})
	defOpt("record", nil, func(a Args) Value {
		l := norm(a.Pos[0]).(*List)
		var j sqlglot.Journal
		j.Record(norm(a.Pos[1]).(*exp.Expr), Str(a.Pos[2]))
		l.V = append(l.V, &JournalEntryV{j[0]})
		return nil
	})
	defOpt("revert", nil, func(a Args) Value {
		j, sync := journalArg(Args{Pos: []Value{nil, a.Pos[0]}}, 1)
		start := 0
		if v := a.Get(1, "start", nil); v != nil {
			start = mustInt(v)
		}
		j.Revert(start)
		sync()
		return nil
	})

	// sqlglot.optimizer.scope
	defOpt("build_scope", nil, func(a Args) Value {
		return scopeValue(sqlglot.BuildScope(exprArg(a, 0, "expression", nil)))
	})
	defOpt("traverse_scope", nil, func(a Args) Value {
		return scopeList(sqlglot.TraverseScope(exprArg(a, 0, "expression", nil)))
	})
	defOpt("walk_in_scope", nil, func(a Args) Value {
		var out []Value
		for n := range sqlglot.WalkInScope(exprArg(a, 0, "expression", nil), nil) {
			out = append(out, n)
		}
		return &Iterator{items: out}
	})
	defOpt("find_all_in_scope", nil, func(a Args) Value {
		var out []Value
		for n := range sqlglot.FindAllInScope(exprArg(a, 0, "expression", nil), kinds(a.Pos[1:])...) {
			out = append(out, n)
		}
		return &Iterator{items: out}
	})
	defOpt("find_in_scope", nil, func(a Args) Value {
		if n := sqlglot.FindInScope(exprArg(a, 0, "expression", nil), kinds(a.Pos[1:])...); n != nil {
			return n
		}
		return nil
	})
}

var scopeValues = map[*sqlglot.Scope]*ScopeV{}

// ScopeV wraps an optimizer Scope. Each Scope has a single wrapper, so identity checks work.
type ScopeV struct{ S *sqlglot.Scope }

func scopeValue(s *sqlglot.Scope) Value {
	if s == nil {
		return nil
	}
	if v, ok := scopeValues[s]; ok {
		return v
	}
	v := &ScopeV{s}
	scopeValues[s] = v
	return v
}

func scopeList(scopes []*sqlglot.Scope) *List {
	out := make([]Value, len(scopes))
	for i, s := range scopes {
		out[i] = scopeValue(s)
	}
	return &List{out}
}

func scopeArg(v Value) *sqlglot.Scope {
	s, ok := norm(v).(*ScopeV)
	if !ok {
		Raise(TypeError, "expected a Scope, got %s", TypeName(v))
	}
	return s.S
}

func sourceValue(src sqlglot.Source) Value {
	switch x := src.(type) {
	case *sqlglot.Scope:
		return scopeValue(x)
	case *exp.Expr:
		return x
	}
	return fromGo(src)
}

func sourcesDict(m *sqlglot.OrderedMap[sqlglot.Source]) *Dict {
	d := NewDict()
	for k, v := range m.All() {
		d.Set(k, sourceValue(v))
	}
	return d
}

// PyAttr implements Attrer.
func (s *ScopeV) PyAttr(name string) (Value, bool) {
	sc := s.S
	switch name {
	case "expression":
		return sc.Expression, true
	case "parent":
		return scopeValue(sc.Parent), true
	case "sources":
		return sourcesDict(sc.Sources), true
	case "lateral_sources":
		return sourcesDict(sc.LateralSources), true
	case "cte_sources":
		return sourcesDict(sc.CteSources), true
	case "selected_sources":
		d := NewDict()
		for k, v := range sc.SelectedSources().All() {
			d.Set(k, Tuple{v.Node, sourceValue(v.Source)})
		}
		return d, true
	case "columns":
		return exprList(sc.Columns()), true
	case "external_columns":
		return exprList(sc.ExternalColumns()), true
	case "local_columns":
		return exprList(sc.LocalColumns()), true
	case "unqualified_columns":
		return exprList(sc.UnqualifiedColumns()), true
	case "table_columns":
		return exprList(sc.TableColumns()), true
	case "tables":
		return exprList(sc.Tables()), true
	case "ctes":
		return exprList(sc.Ctes()), true
	case "derived_tables":
		return exprList(sc.DerivedTables()), true
	case "udtfs":
		return exprList(sc.Udtfs()), true
	case "subqueries":
		return exprList(sc.Subqueries()), true
	case "stars":
		return exprList(sc.Stars()), true
	case "pivots":
		return exprList(sc.Pivots()), true
	case "join_hints":
		return exprList(sc.JoinHints()), true
	case "outer_columns":
		return norm(append([]string{}, sc.OuterColumns...)), true
	case "can_be_correlated":
		return sc.CanBeCorrelated, true
	case "subquery_scopes":
		return scopeList(sc.SubqueryScopes), true
	case "derived_table_scopes":
		return scopeList(sc.DerivedTableScopes), true
	case "table_scopes":
		return scopeList(sc.TableScopes), true
	case "cte_scopes":
		return scopeList(sc.CteScopes), true
	case "union_scopes", "set_operation_scopes":
		return scopeList(sc.SetOperationScopes), true
	case "udtf_scopes":
		return scopeList(sc.UdtfScopes), true
	case "is_subquery":
		return sc.IsSubquery(), true
	case "is_derived_table":
		return sc.IsDerivedTable(), true
	case "is_union", "is_set_operation":
		return sc.IsSetOperation(), true
	case "is_cte":
		return sc.IsCTE(), true
	case "is_root":
		return sc.IsRoot(), true
	case "is_udtf":
		return sc.IsUDTF(), true
	case "is_correlated_subquery":
		return sc.IsCorrelatedSubquery(), true
	case "traverse":
		return method(name, func(a Args) Value { return &Iterator{items: scopeList(sc.Traverse()).V} }), true
	case "find":
		return method(name, func(a Args) Value {
			if n := sc.Find(kinds(a.Pos)...); n != nil {
				return n
			}
			return nil
		}), true
	case "find_all":
		return method(name, func(a Args) Value {
			var out []Value
			for n := range sc.FindAll(kinds(a.Pos)...) {
				out = append(out, n)
			}
			return &Iterator{items: out}
		}), true
	case "walk":
		return method(name, func(a Args) Value {
			var out []Value
			for n := range sc.Walk(nil) {
				out = append(out, n)
			}
			return &Iterator{items: out}
		}), true
	case "replace":
		return method(name, func(a Args) Value {
			sc.Replace(norm(a.Pos[0]).(*exp.Expr), norm(a.Pos[1]).(*exp.Expr))
			return nil
		}), true
	case "rename_source":
		return method(name, func(a Args) Value { sc.RenameSource(Str(a.Pos[0]), Str(a.Pos[1])); return nil }), true
	case "remove_source":
		return method(name, func(a Args) Value { sc.RemoveSource(Str(a.Pos[0])); return nil }), true
	case "clear_cache":
		return method(name, func(a Args) Value { sc.ClearCache(); return nil }), true
	case "source_columns":
		return method(name, func(a Args) Value { return exprList(sc.SourceColumns(Str(a.Pos[0]))) }), true
	case "ref_count":
		return method(name, func(a Args) Value {
			d := NewDict()
			for k, v := range sc.RefCount() {
				d.Set(sourceValue(k), v)
			}
			return d
		}), true
	}
	return nil, false
}

// PyHash makes scopes usable as dict keys (by identity).
func (s *ScopeV) PyHash() string { return fmt.Sprintf("scope:%p", s) }
