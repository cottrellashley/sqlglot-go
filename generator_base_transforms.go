package sqlglot

// The base Generator.TRANSFORMS table and AFTER_HAVING_MODIFIER_TRANSFORMS, translated from
// sqlglot/generator.py.

import "github.com/cottrellashley/sqlglot-go/exp"

// NamedGenFunc is an entry of an ordered generator table (AFTER_HAVING_MODIFIER_TRANSFORMS).
type NamedGenFunc struct {
	Name string
	Fn   GenFunc
}

// AfterHavingModifierTransforms mirrors generator.AFTER_HAVING_MODIFIER_TRANSFORMS (the module
// level table, without the base class' cluster/distribute/sort entries).
func AfterHavingModifierTransforms() []NamedGenFunc {
	return []NamedGenFunc{
		{"windows", func(g *Generator, e *exp.Expr) string {
			if e.Bool("windows") {
				return g.Seg("WINDOW ") + g.FlatKey(e, "windows")
			}
			return ""
		}},
		{"qualify", func(g *Generator, e *exp.Expr) string { return g.SQLArg(e, "qualify") }},
	}
}

func baseAfterHavingModifierTransforms() []NamedGenFunc {
	return append([]NamedGenFunc{
		{"cluster", func(g *Generator, e *exp.Expr) string { return g.SQLArg(e, "cluster") }},
		{"distribute", func(g *Generator, e *exp.Expr) string { return g.SQLArg(e, "distribute") }},
		{"sort", func(g *Generator, e *exp.Expr) string { return g.SQLArg(e, "sort") }},
	}, AfterHavingModifierTransforms()...)
}

func baseTransforms() map[exp.Kind]GenFunc {
	t := map[exp.Kind]GenFunc{
		exp.Adjacent: func(g *Generator, e *exp.Expr) string { return g.Binary(e, "-|-") },
		exp.AllowedValuesProperty: func(g *Generator, e *exp.Expr) string {
			return "ALLOWED_VALUES " + g.Expressions(e, ExprsOpts{Flat: true})
		},
		exp.AnalyzeColumns:               func(g *Generator, e *exp.Expr) string { return g.SQLArg(e, "this") },
		exp.AnalyzeWith:                  func(g *Generator, e *exp.Expr) string { return g.Expressions(e, ExprsOpts{Sep: " ", Prefix: "WITH "}) },
		exp.ArrayContainedBy:             func(g *Generator, e *exp.Expr) string { return g.Binary(e, "<@") },
		exp.ArrayContainsAll:             func(g *Generator, e *exp.Expr) string { return g.Binary(e, "@>") },
		exp.ArrayOverlaps:                func(g *Generator, e *exp.Expr) string { return g.Binary(e, "&&") },
		exp.AssumeColumnConstraint:       func(g *Generator, e *exp.Expr) string { return "ASSUME (" + g.SQLArg(e, "this") + ")" },
		exp.AutoRefreshProperty:          func(g *Generator, e *exp.Expr) string { return "AUTO REFRESH " + g.SQLArg(e, "this") },
		exp.BackupProperty:               func(g *Generator, e *exp.Expr) string { return "BACKUP " + g.SQLArg(e, "this") },
		exp.BinaryColumnConstraint:       func(g *Generator, e *exp.Expr) string { return "BINARY" },
		exp.CaseSpecificColumnConstraint: func(g *Generator, e *exp.Expr) string { return iff(e.Bool("not_"), "NOT ", "") + "CASESPECIFIC" },
		exp.CalledOnNullInputProperty:    func(g *Generator, e *exp.Expr) string { return "CALLED ON NULL INPUT" },
		exp.Ceil:                         func(g *Generator, e *exp.Expr) string { return g.CeilFloor(e) },
		exp.CharacterSetColumnConstraint: func(g *Generator, e *exp.Expr) string { return "CHARACTER SET " + g.SQLArg(e, "this") },
		exp.CharacterSetProperty: func(g *Generator, e *exp.Expr) string {
			return iff(e.Bool("default"), "DEFAULT ", "") + "CHARACTER SET=" + g.SQLArg(e, "this")
		},
		exp.ClusteredColumnConstraint: func(g *Generator, e *exp.Expr) string {
			return "CLUSTERED (" + g.Expressions(e, ExprsOpts{Key: "this", NoIndent: true}) + ")"
		},
		exp.CollateColumnConstraint: func(g *Generator, e *exp.Expr) string { return "COLLATE " + g.SQLArg(e, "this") },
		exp.CommentColumnConstraint: func(g *Generator, e *exp.Expr) string { return "COMMENT " + g.SQLArg(e, "this") },
		exp.ConnectByRoot:           func(g *Generator, e *exp.Expr) string { return "CONNECT_BY_ROOT " + g.SQLArg(e, "this") },
		exp.ConvertToCharset: func(g *Generator, e *exp.Expr) string {
			return g.Func("CONVERT", e.This(), e.Arg("dest"), e.Arg("source"))
		},
		exp.CopyGrantsProperty: func(g *Generator, e *exp.Expr) string { return "COPY GRANTS" },
		exp.CopyTagsProperty:   func(g *Generator, e *exp.Expr) string { return "COPY TAGS" },
		exp.CredentialsProperty: func(g *Generator, e *exp.Expr) string {
			return "CREDENTIALS=(" + g.Expressions(e, ExprsOpts{Key: "expressions", Sep: " "}) + ")"
		},
		exp.CurrentCatalog:             func(g *Generator, e *exp.Expr) string { return "CURRENT_CATALOG" },
		exp.SessionUser:                func(g *Generator, e *exp.Expr) string { return "SESSION_USER" },
		exp.DateFormatColumnConstraint: func(g *Generator, e *exp.Expr) string { return "FORMAT " + g.SQLArg(e, "this") },
		exp.DefaultColumnConstraint:    func(g *Generator, e *exp.Expr) string { return "DEFAULT " + g.SQLArg(e, "this") },
		exp.ApiProperty:                func(g *Generator, e *exp.Expr) string { return "API" },
		exp.ApplicationProperty:        func(g *Generator, e *exp.Expr) string { return "APPLICATION" },
		exp.CatalogProperty:            func(g *Generator, e *exp.Expr) string { return "CATALOG" },
		exp.ComputeProperty:            func(g *Generator, e *exp.Expr) string { return "COMPUTE" },
		exp.DatabaseProperty:           func(g *Generator, e *exp.Expr) string { return "DATABASE" },
		exp.DynamicProperty:            func(g *Generator, e *exp.Expr) string { return "DYNAMIC" },
		exp.EmptyProperty:              func(g *Generator, e *exp.Expr) string { return "EMPTY" },
		exp.EncodeColumnConstraint:     func(g *Generator, e *exp.Expr) string { return "ENCODE " + g.SQLArg(e, "this") },
		exp.EndStatement:               func(g *Generator, e *exp.Expr) string { return "END" },
		exp.EnviromentProperty: func(g *Generator, e *exp.Expr) string {
			return "ENVIRONMENT (" + g.Expressions(e, ExprsOpts{Flat: true}) + ")"
		},
		exp.HandlerProperty:        func(g *Generator, e *exp.Expr) string { return "HANDLER " + g.SQLArg(e, "this") },
		exp.ParameterStyleProperty: func(g *Generator, e *exp.Expr) string { return "PARAMETER STYLE " + g.SQLArg(e, "this") },
		exp.EphemeralColumnConstraint: func(g *Generator, e *exp.Expr) string {
			return "EPHEMERAL" + iff(e.Bool("this"), " "+g.SQLArg(e, "this"), "")
		},
		exp.ExcludeColumnConstraint: func(g *Generator, e *exp.Expr) string { return "EXCLUDE " + pyLStrip(g.SQLArg(e, "this")) },
		exp.ExecuteAsProperty:       func(g *Generator, e *exp.Expr) string { return g.NakedProperty(e) },
		exp.Except:                  func(g *Generator, e *exp.Expr) string { return g.SetOperations(e) },
		exp.ExternalProperty:        func(g *Generator, e *exp.Expr) string { return "EXTERNAL" },
		exp.Floor:                   func(g *Generator, e *exp.Expr) string { return g.CeilFloor(e) },
		exp.Get:                     func(g *Generator, e *exp.Expr) string { return g.GetPutSQL(e) },
		exp.GlobalProperty:          func(g *Generator, e *exp.Expr) string { return "GLOBAL" },
		exp.HeapProperty:            func(g *Generator, e *exp.Expr) string { return "HEAP" },
		exp.HybridProperty:          func(g *Generator, e *exp.Expr) string { return "HYBRID" },
		exp.IcebergProperty:         func(g *Generator, e *exp.Expr) string { return "ICEBERG" },
		exp.InheritsProperty: func(g *Generator, e *exp.Expr) string {
			return "INHERITS (" + g.Expressions(e, ExprsOpts{Flat: true}) + ")"
		},
		exp.InlineLengthColumnConstraint: func(g *Generator, e *exp.Expr) string { return "INLINE LENGTH " + g.SQLArg(e, "this") },
		exp.InputModelProperty:           func(g *Generator, e *exp.Expr) string { return "INPUT" + g.SQLArg(e, "this") },
		exp.Intersect:                    func(g *Generator, e *exp.Expr) string { return g.SetOperations(e) },
		exp.IntervalSpan: func(g *Generator, e *exp.Expr) string {
			return g.SQLArg(e, "this") + " TO " + g.SQLArg(e, "expression")
		},
		exp.Int64:                   func(g *Generator, e *exp.Expr) string { return g.SQL(exp.NewCast(e.This(), exp.DTypeBigint)) },
		exp.JSONBContainsAnyTopKeys: func(g *Generator, e *exp.Expr) string { return g.Binary(e, "?|") },
		exp.JSONBContainsAllTopKeys: func(g *Generator, e *exp.Expr) string { return g.Binary(e, "?&") },
		exp.JSONBContainsTopKey:     func(g *Generator, e *exp.Expr) string { return g.Binary(e, "?") },
		exp.JSONBDeleteAtPath:       func(g *Generator, e *exp.Expr) string { return g.Binary(e, "#-") },
		exp.JSONBPathExists:         func(g *Generator, e *exp.Expr) string { return g.Binary(e, "@?") },
		exp.JSONObject:              func(g *Generator, e *exp.Expr) string { return g.JsonObjectSQL(e, "") },
		exp.JSONObjectAgg:           func(g *Generator, e *exp.Expr) string { return g.JsonObjectSQL(e, "") },
		exp.LanguageProperty:        func(g *Generator, e *exp.Expr) string { return g.NakedProperty(e) },
		exp.LocationProperty:        func(g *Generator, e *exp.Expr) string { return g.NakedProperty(e) },
		exp.LogProperty:             func(g *Generator, e *exp.Expr) string { return iff(e.Bool("no"), "NO ", "") + "LOG" },
		exp.MaskingProperty:         func(g *Generator, e *exp.Expr) string { return "MASKING" },
		exp.MaterializedProperty:    func(g *Generator, e *exp.Expr) string { return "MATERIALIZED" },
		exp.NetFunc:                 func(g *Generator, e *exp.Expr) string { return "NET." + g.SQLArg(e, "this") },
		exp.NetworkProperty:         func(g *Generator, e *exp.Expr) string { return "NETWORK" },
		exp.NonClusteredColumnConstraint: func(g *Generator, e *exp.Expr) string {
			return "NONCLUSTERED (" + g.Expressions(e, ExprsOpts{Key: "this", NoIndent: true}) + ")"
		},
		exp.NoPrimaryIndexProperty:            func(g *Generator, e *exp.Expr) string { return "NO PRIMARY INDEX" },
		exp.NotForReplicationColumnConstraint: func(g *Generator, e *exp.Expr) string { return "NOT FOR REPLICATION" },
		exp.OnCommitProperty: func(g *Generator, e *exp.Expr) string {
			return "ON COMMIT " + iff(e.Bool("delete"), "DELETE", "PRESERVE") + " ROWS"
		},
		exp.OnProperty:                        func(g *Generator, e *exp.Expr) string { return "ON " + g.SQLArg(e, "this") },
		exp.OnUpdateColumnConstraint:          func(g *Generator, e *exp.Expr) string { return "ON UPDATE " + g.SQLArg(e, "this") },
		exp.Operator:                          func(g *Generator, e *exp.Expr) string { return g.Binary(e, "") },
		exp.OutputModelProperty:               func(g *Generator, e *exp.Expr) string { return "OUTPUT" + g.SQLArg(e, "this") },
		exp.ExtendsLeft:                       func(g *Generator, e *exp.Expr) string { return g.Binary(e, "&<") },
		exp.ExtendsRight:                      func(g *Generator, e *exp.Expr) string { return g.Binary(e, "&>") },
		exp.PathColumnConstraint:              func(g *Generator, e *exp.Expr) string { return "PATH " + g.SQLArg(e, "this") },
		exp.PartitionedByBucket:               func(g *Generator, e *exp.Expr) string { return g.Func("BUCKET", e.This(), e.Expression()) },
		exp.PartitionByTruncate:               func(g *Generator, e *exp.Expr) string { return g.Func("TRUNCATE", e.This(), e.Expression()) },
		exp.PivotAny:                          func(g *Generator, e *exp.Expr) string { return "ANY" + g.SQLArg(e, "this") },
		exp.PositionalColumn:                  func(g *Generator, e *exp.Expr) string { return "#" + g.SQLArg(e, "this") },
		exp.ProjectionPolicyColumnConstraint:  func(g *Generator, e *exp.Expr) string { return "PROJECTION POLICY " + g.SQLArg(e, "this") },
		exp.InvisibleColumnConstraint:         func(g *Generator, e *exp.Expr) string { return "INVISIBLE" },
		exp.ZeroFillColumnConstraint:          func(g *Generator, e *exp.Expr) string { return "ZEROFILL" },
		exp.Put:                               func(g *Generator, e *exp.Expr) string { return g.GetPutSQL(e) },
		exp.RemoteWithConnectionModelProperty: func(g *Generator, e *exp.Expr) string { return "REMOTE WITH CONNECTION " + g.SQLArg(e, "this") },
		exp.ReturnsProperty: func(g *Generator, e *exp.Expr) string {
			if e.Bool("null") {
				return "RETURNS NULL ON NULL INPUT"
			}
			return g.NakedProperty(e)
		},
		exp.RowAccessProperty:           func(g *Generator, e *exp.Expr) string { return "ROW ACCESS" },
		exp.SafeFunc:                    func(g *Generator, e *exp.Expr) string { return "SAFE." + g.SQLArg(e, "this") },
		exp.SampleProperty:              func(g *Generator, e *exp.Expr) string { return "SAMPLE BY " + g.SQLArg(e, "this") },
		exp.SecureProperty:              func(g *Generator, e *exp.Expr) string { return "SECURE" },
		exp.SecurityIntegrationProperty: func(g *Generator, e *exp.Expr) string { return "SECURITY" },
		exp.SetConfigProperty:           func(g *Generator, e *exp.Expr) string { return g.SQLArg(e, "this") },
		exp.SetProperty:                 func(g *Generator, e *exp.Expr) string { return iff(e.Bool("multi"), "MULTI", "") + "SET" },
		exp.SettingsProperty:            func(g *Generator, e *exp.Expr) string { return "SETTINGS" + g.Seg("") + g.Expressions(e, ExprsOpts{}) },
		exp.SharingProperty:             func(g *Generator, e *exp.Expr) string { return "SHARING=" + g.SQLArg(e, "this") },
		exp.SqlReadWriteProperty:        func(g *Generator, e *exp.Expr) string { return e.Name() },
		exp.SqlSecurityProperty:         func(g *Generator, e *exp.Expr) string { return "SQL SECURITY " + g.SQLArg(e, "this") },
		exp.StabilityProperty:           func(g *Generator, e *exp.Expr) string { return e.Name() },
		exp.Stream:                      func(g *Generator, e *exp.Expr) string { return "STREAM " + g.SQLArg(e, "this") },
		exp.StreamingTableProperty:      func(g *Generator, e *exp.Expr) string { return "STREAMING" },
		exp.StrictProperty:              func(g *Generator, e *exp.Expr) string { return "STRICT" },
		exp.SwapTable:                   func(g *Generator, e *exp.Expr) string { return "SWAP WITH " + g.SQLArg(e, "this") },
		exp.TableColumn:                 func(g *Generator, e *exp.Expr) string { return g.SQL(e.This()) },
		exp.Tags:                        func(g *Generator, e *exp.Expr) string { return "TAG (" + g.Expressions(e, ExprsOpts{Flat: true}) + ")" },
		exp.TemporaryProperty:           func(g *Generator, e *exp.Expr) string { return "TEMPORARY" },
		exp.TitleColumnConstraint:       func(g *Generator, e *exp.Expr) string { return "TITLE " + g.SQLArg(e, "this") },
		exp.ToMap:                       func(g *Generator, e *exp.Expr) string { return "MAP " + g.SQLArg(e, "this") },
		exp.ToTableProperty:             func(g *Generator, e *exp.Expr) string { return "TO " + g.SQL(e.This()) },
		exp.TransformModelProperty:      func(g *Generator, e *exp.Expr) string { return g.Func("TRANSFORM", e.Expressions()) },
		exp.TransientProperty:           func(g *Generator, e *exp.Expr) string { return "TRANSIENT" },
		exp.VirtualProperty:             func(g *Generator, e *exp.Expr) string { return "VIRTUAL" },
		exp.TriggerExecute:              func(g *Generator, e *exp.Expr) string { return "EXECUTE FUNCTION " + g.SQLArg(e, "this") },
		exp.Union:                       func(g *Generator, e *exp.Expr) string { return g.SetOperations(e) },
		exp.UnloggedProperty:            func(g *Generator, e *exp.Expr) string { return "UNLOGGED" },
		exp.UsingTemplateProperty:       func(g *Generator, e *exp.Expr) string { return "USING TEMPLATE " + g.SQLArg(e, "this") },
		exp.UsingData:                   func(g *Generator, e *exp.Expr) string { return "USING DATA " + g.SQLArg(e, "this") },
		exp.UppercaseColumnConstraint:   func(g *Generator, e *exp.Expr) string { return "UPPERCASE" },
		exp.UtcDate: func(g *Generator, e *exp.Expr) string {
			return g.SQL(exp.New(exp.CurrentDate, "this", exp.LiteralString("UTC")))
		},
		exp.UtcTime: func(g *Generator, e *exp.Expr) string {
			return g.SQL(exp.New(exp.CurrentTime, "this", exp.LiteralString("UTC")))
		},
		exp.UtcTimestamp: func(g *Generator, e *exp.Expr) string {
			return g.SQL(exp.New(exp.CurrentTimestamp, "this", exp.LiteralString("UTC")))
		},
		exp.Variadic:                  func(g *Generator, e *exp.Expr) string { return "VARIADIC " + g.SQLArg(e, "this") },
		exp.VarMap:                    func(g *Generator, e *exp.Expr) string { return g.Func("MAP", e.Arg("keys"), e.Arg("values")) },
		exp.ViewAttributeProperty:     func(g *Generator, e *exp.Expr) string { return "WITH " + g.SQLArg(e, "this") },
		exp.VolatileProperty:          func(g *Generator, e *exp.Expr) string { return "VOLATILE" },
		exp.WithJournalTableProperty:  func(g *Generator, e *exp.Expr) string { return "WITH JOURNAL TABLE=" + g.SQLArg(e, "this") },
		exp.WithProcedureOptions:      func(g *Generator, e *exp.Expr) string { return "WITH " + g.Expressions(e, ExprsOpts{Flat: true}) },
		exp.WithSchemaBindingProperty: func(g *Generator, e *exp.Expr) string { return "WITH SCHEMA " + g.SQLArg(e, "this") },
		exp.WithOperator:              func(g *Generator, e *exp.Expr) string { return g.SQLArg(e, "this") + " WITH " + g.SQLArg(e, "op") },
		exp.ForceProperty:             func(g *Generator, e *exp.Expr) string { return "FORCE" },
	}
	for k, v := range jsonPathPartTransforms() {
		t[k] = v
	}
	return t
}

func newBaseGeneratorConfig() *GeneratorConfig {
	c := &GeneratorConfig{}
	baseGeneratorData(c)
	c.Transforms = baseTransforms()
	c.AfterHavingModifierTransforms = baseAfterHavingModifierTransforms()
	c.SQLMethods = baseKindSQLMethods()
	c.Methods = baseGeneratorMethods()
	c.Extra = map[string]any{}
	return c
}
