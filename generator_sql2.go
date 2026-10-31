package sqlglot

// Generator methods translated from sqlglot/generator.py (index_sql .. historicaldata_sql).

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cottrellashley/sqlglot-go/exp"
)

func (g *Generator) indexSQL(e *exp.Expr) string {
	unique := iff(e.Bool("unique"), "UNIQUE ", "")
	primary := iff(e.Bool("primary"), "PRIMARY ", "")
	amp := iff(e.Bool("amp"), "AMP ", "")
	name := sfx(g.SQLArg(e, "this"), " ")
	table := pfx(g.C.IndexOn+" ", g.SQLArg(e, "table"))
	index := iff(table == "", "INDEX ", "")
	params := g.SQLArg(e, "params")
	return unique + primary + amp + index + name + table + params
}

func (g *Generator) dynamicidentifierSQL(e *exp.Expr) string {
	this := e.This()
	if this != nil && this.IsString() {
		resolved := exp.MustMaybeParse(this.Name(), exp.ParseOptions{}, "", true).SQL(g.D)
		if e.HasArg("expressions") {
			// `IDENTIFIER(...)` invoked as a function, e.g. `IDENTIFIER('my_func')(1, 2)`
			// We can't safely emit the call to other dialects since name/arg semantics may differ
			g.Unsupported("Transpiling dynamically-invoked IDENTIFIER() functions is unsupported")
		}
		return resolved
	}
	g.Unsupported("IDENTIFIER() with non-literal arguments is not supported")
	return g.Func("IDENTIFIER", this)
}

// orderedEscapeKeys returns the keys of an escape mapping in the order Python's dict would
// apply them (the backslash sequence comes first, as in UNESCAPED_SEQUENCES).
func orderedEscapeKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if a == `\` {
			return -1
		}
		if b == `\` {
			return 1
		}
		return strings.Compare(a, b)
	})
	return keys
}

func (g *Generator) identifierSQL(e *exp.Expr) string {
	text := e.Name()
	lower := strings.ToLower(text)
	quoted := e.Quoted()
	if g.Normalize && !quoted {
		text = lower
	}
	if len(g.IdentifierEscapedSequences) > 0 {
		for _, char := range orderedEscapeKeys(g.IdentifierEscapedSequences) {
			text = strings.ReplaceAll(text, char, g.IdentifierEscapedSequences[char])
		}
	}
	text = strings.ReplaceAll(text, g.IdentifierEnd, g.EscapedIdentifierEnd)
	first, _ := utf8.DecodeRuneInString(text)
	if quoted || g.D.CanQuote(e, g.Identify) || g.C.ReservedKeywords[lower] ||
		(!g.D.IdentifiersCanStartWithDigit && text != "" && unicode.IsDigit(first)) {
		text = g.IdentifierStart + g.ReplaceLineBreaks(text) + g.IdentifierEnd
	}
	return text
}

func (g *Generator) hexSQL(e *exp.Expr) string {
	text := g.Func(g.C.HexFunc, g.SQLArg(e, "this"))
	if g.D.HexLowercase {
		text = g.Func("LOWER", text)
	}
	return text
}

func (g *Generator) lowerhexSQL(e *exp.Expr) string {
	text := g.Func(g.C.HexFunc, g.SQLArg(e, "this"))
	if !g.D.HexLowercase {
		text = g.Func("LOWER", text)
	}
	return text
}

func (g *Generator) inputoutputformatSQL(e *exp.Expr) string {
	inputFormat := pfx("INPUTFORMAT ", g.SQLArg(e, "input_format"))
	outputFormat := pfx("OUTPUTFORMAT ", g.SQLArg(e, "output_format"))
	return inputFormat + g.Sep() + outputFormat
}

//gen:dispatch g.NationalSQL(e, "N")
func (g *Generator) nationalSQL(e *exp.Expr, prefix string) string {
	return prefix + g.SQL(exp.LiteralString(e.Name()))
}

func (g *Generator) partitionSQL(e *exp.Expr) string {
	kw := iff(e.Bool("subpartition"), "SUBPARTITION", "PARTITION")
	return kw + "(" + g.Flat(e) + ")"
}

// propertyLocation is PROPERTIES_LOCATION[p.__class__] (a KeyError for unknown properties).
func (g *Generator) propertyLocation(p *exp.Expr) exp.PropertiesLocation {
	loc, ok := g.C.PropertiesLocation[p.Kind()]
	if !ok {
		g.Raisef("KeyError: %s", p.Kind().ClassRepr())
	}
	return loc
}

func (g *Generator) propertiesSQL(e *exp.Expr) string {
	var rootProperties, withProperties []*exp.Expr
	for _, p := range e.Expressions() {
		switch g.PropertyLocation(p) {
		case exp.PostWith:
			withProperties = append(withProperties, p)
		case exp.PostSchema:
			rootProperties = append(rootProperties, p)
		}
	}
	rootPropsAST := newPropertiesWithParent(rootProperties, e.Parent())
	withPropsAST := newPropertiesWithParent(withProperties, e.Parent())
	rootProps := g.RootProperties(rootPropsAST)
	withProps := g.WithProperties(withPropsAST)
	if rootProps != "" && withProps != "" && !g.Pretty {
		withProps = " " + withProps
	}
	return rootProps + withProps
}

func (g *Generator) rootProperties(properties *exp.Expr) string {
	if len(properties.Expressions()) > 0 {
		return g.Expressions(properties, ExprsOpts{NoIndent: true, Sep: " "})
	}
	return ""
}

// properties mirrors Generator.properties(properties, prefix="", sep=", ", suffix="", wrapped=True).
func (g *Generator) properties(properties *exp.Expr, prefix, sep, suffix string, wrapped bool) string {
	if len(properties.Expressions()) > 0 {
		expressions := g.Expressions(properties, ExprsOpts{Sep: sep, NoIndent: true})
		if expressions != "" {
			if wrapped {
				expressions = g.Wrap(expressions)
			}
			return prefix + iff(pyStrip(prefix) != "", " ", "") + expressions + suffix
		}
	}
	return ""
}

func (g *Generator) withProperties(properties *exp.Expr) string {
	return g.Properties(properties, g.SegWith(g.C.WithPropertiesPrefix, ""), ", ", "", true)
}

func (g *Generator) locateProperties(properties *exp.Expr) PropertyLocations {
	locs := PropertyLocations{}
	for _, p := range properties.Expressions() {
		loc := g.PropertyLocation(p)
		if loc != exp.Unsupported {
			locs[loc] = append(locs[loc], p)
		} else {
			g.Unsupported("Unsupported property " + p.Key())
		}
	}
	return locs
}

func (g *Generator) propertyName(e *exp.Expr, stringKey bool) string {
	if e.This().IsKind(exp.Dot) {
		return g.SQLArg(e, "this")
	}
	if stringKey {
		return g.SQL(exp.LiteralString(e.Name()))
	}
	return e.Name()
}

func (g *Generator) propertySQL(e *exp.Expr) string {
	if e.IsKind(exp.Property) {
		return g.PropertyName(e, false) + "=" + g.SQLArg(e, "value")
	}
	name, ok := exp.PropertyToName[e.Kind()]
	if !ok || name == "" {
		g.Unsupported("Unsupported property " + e.Key())
		return ""
	}
	return name + "=" + g.SQLArg(e, "this")
}

func (g *Generator) uuidpropertySQL(e *exp.Expr) string {
	return "UUID " + g.SQLArg(e, "this")
}

func (g *Generator) likepropertySQL(e *exp.Expr) string {
	if g.C.SupportsCreateTableLike {
		var opts []string
		for _, x := range e.Expressions() {
			opts = append(opts, x.Name()+" "+g.SQLArg(x, "value"))
		}
		options := pfx(" ", strings.Join(opts, " "))
		like := "LIKE " + g.SQLArg(e, "this") + options
		if g.C.LikePropertyInsideSchema && !e.Parent().IsKind(exp.Schema) {
			like = "(" + like + ")"
		}
		return like
	}
	if len(e.Expressions()) > 0 {
		g.Unsupported("Transpilation of LIKE property options is unsupported")
	}
	sel := exp.NewSelect("*").From(e.This()).Limit(0)
	return "AS " + g.SQL(sel)
}

func (g *Generator) fallbackpropertySQL(e *exp.Expr) string {
	no := iff(e.Bool("no"), "NO ", "")
	protection := iff(e.Bool("protection"), " PROTECTION", "")
	return no + "FALLBACK" + protection
}

func (g *Generator) journalpropertySQL(e *exp.Expr) string {
	no := iff(e.Bool("no"), "NO ", "")
	local := sfx(argStr(e.Arg("local")), " ")
	dual := iff(e.Bool("dual"), "DUAL ", "")
	before := iff(e.Bool("before"), "BEFORE ", "")
	after := iff(e.Bool("after"), "AFTER ", "")
	return no + local + dual + before + after + "JOURNAL"
}

func (g *Generator) freespacepropertySQL(e *exp.Expr) string {
	freespace := g.SQLArg(e, "this")
	percent := iff(e.Bool("percent"), " PERCENT", "")
	return "FREESPACE=" + freespace + percent
}

func (g *Generator) checksumpropertySQL(e *exp.Expr) string {
	property := "OFF"
	if e.Bool("default") {
		property = "DEFAULT"
	} else if e.Bool("on") {
		property = "ON"
	}
	return "CHECKSUM=" + property
}

func (g *Generator) mergeblockratiopropertySQL(e *exp.Expr) string {
	if e.Bool("no") {
		return "NO MERGEBLOCKRATIO"
	}
	if e.Bool("default") {
		return "DEFAULT MERGEBLOCKRATIO"
	}
	percent := iff(e.Bool("percent"), " PERCENT", "")
	return "MERGEBLOCKRATIO=" + g.SQLArg(e, "this") + percent
}

func (g *Generator) modulepropertySQL(e *exp.Expr) string {
	expressions := around("(", g.Flat(e), ")")
	return "USING " + g.SQLArg(e, "this") + expressions
}

func (g *Generator) datablocksizepropertySQL(e *exp.Expr) string {
	def, minimum, maximum := e.Bool("default"), e.Bool("minimum"), e.Bool("maximum")
	if def || minimum || maximum {
		prop := "MAXIMUM"
		if def {
			prop = "DEFAULT"
		} else if minimum {
			prop = "MINIMUM"
		}
		return prop + " DATABLOCKSIZE"
	}
	units := pfx(" ", argStr(e.Arg("units")))
	return "DATABLOCKSIZE=" + g.SQLArg(e, "size") + units
}

func (g *Generator) blockcompressionpropertySQL(e *exp.Expr) string {
	var prop string
	if autotemp := e.Get("autotemp"); autotemp != nil {
		prop = "AUTOTEMP(" + g.Expressions(autotemp, ExprsOpts{}) + ")"
	} else if e.Bool("always") {
		prop = "ALWAYS"
	} else if e.Bool("default") {
		prop = "DEFAULT"
	} else if e.Bool("manual") {
		prop = "MANUAL"
	} else if e.Bool("never") {
		prop = "NEVER"
	}
	return "BLOCKCOMPRESSION=" + prop
}

func (g *Generator) isolatedloadingpropertySQL(e *exp.Expr) string {
	no := iff(e.Bool("no"), " NO", "")
	concurrent := iff(e.Bool("concurrent"), " CONCURRENT", "")
	target := pfx(" ", g.SQLArg(e, "target"))
	return "WITH" + no + concurrent + " ISOLATED LOADING" + target
}

func (g *Generator) partitionboundspecSQL(e *exp.Expr) string {
	if _, ok := e.Arg("this").([]*exp.Expr); ok {
		return "IN (" + g.FlatKey(e, "this") + ")"
	}
	if e.Bool("this") {
		modulus := g.SQLArg(e, "this")
		remainder := g.SQLArg(e, "expression")
		return "WITH (MODULUS " + modulus + ", REMAINDER " + remainder + ")"
	}
	fromExpressions := g.FlatKey(e, "from_expressions")
	toExpressions := g.FlatKey(e, "to_expressions")
	return "FROM (" + fromExpressions + ") TO (" + toExpressions + ")"
}

func (g *Generator) partitionedofpropertySQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	forValuesOrDefault := " DEFAULT"
	if fv := e.Expression(); fv.IsKind(exp.PartitionBoundSpec) {
		forValuesOrDefault = " FOR VALUES " + g.SQL(fv)
	}
	return "PARTITION OF " + this + forValuesOrDefault
}

func (g *Generator) lockingpropertySQL(e *exp.Expr) string {
	kind := pyStr(e.Arg("kind"))
	this := iff(e.Bool("this"), " "+g.SQLArg(e, "this"), "")
	forOrIn := pfx(" ", argStr(e.Arg("for_or_in")))
	lockType := pyStr(e.Arg("lock_type"))
	override := iff(e.Bool("override"), " OVERRIDE", "")
	return "LOCKING " + kind + this + forOrIn + " " + lockType + override
}

func (g *Generator) lockingstatementSQL(e *exp.Expr) string {
	g.Unsupported("LOCKING request modifier is not supported")
	return g.SQLArg(e, "expression")
}

func (g *Generator) withdatapropertySQL(e *exp.Expr) string {
	dataSQL := "WITH " + iff(e.Bool("no"), "NO ", "") + "DATA"
	statisticsSQL := ""
	if statistics := e.Arg("statistics"); statistics != nil {
		statisticsSQL = " AND " + iff(!exp.Truthy(statistics), "NO ", "") + "STATISTICS"
	}
	return dataSQL + statisticsSQL
}

func (g *Generator) withsystemversioningpropertySQL(e *exp.Expr) string {
	this := pfx("HISTORY_TABLE=", g.SQLArg(e, "this"))
	var dataConsistency, retentionPeriod any
	if s := g.SQLArg(e, "data_consistency"); s != "" {
		dataConsistency = "DATA_CONSISTENCY_CHECK=" + s
	}
	if s := g.SQLArg(e, "retention_period"); s != "" {
		retentionPeriod = "HISTORY_RETENTION_PERIOD=" + s
	}
	var onSQL string
	if this != "" {
		onSQL = g.Func("ON", this, dataConsistency, retentionPeriod)
	} else {
		onSQL = iff(e.Bool("on"), "ON", "OFF")
	}
	sql := "SYSTEM_VERSIONING=" + onSQL
	if e.Bool("with_") {
		return "WITH(" + sql + ")"
	}
	return sql
}

func (g *Generator) insertSQL(e *exp.Expr) string {
	hint := g.SQLArg(e, "hint")
	overwrite := e.Bool("overwrite")
	var this string
	if e.This().IsKind(exp.Directory) {
		this = iff(overwrite, " OVERWRITE", " INTO")
	} else {
		this = iff(overwrite, g.C.InsertOverwrite, " INTO")
	}
	stored := pfx(" ", g.SQLArg(e, "stored"))
	alternative := pfx(" OR ", argStr(e.Arg("alternative")))
	ignore := iff(e.Bool("ignore"), " IGNORE", "")
	if e.Bool("is_function") {
		this += " FUNCTION"
	}
	this = this + " " + g.SQLArg(e, "this")

	exists := iff(e.Bool("exists"), " IF EXISTS", "")
	where := pfx(g.Sep()+"REPLACE WHERE ", g.SQLArg(e, "where"))
	using := around(g.Sep()+"REPLACE USING (", g.FlatKey(e, "using"), ")")
	expressionSQL := g.Sep() + g.SQLArg(e, "expression")
	onConflict := pfx(" ", g.SQLArg(e, "conflict"))
	byName := iff(e.Bool("by_name"), " BY NAME", "")
	defaultValues := iff(e.Bool("default"), "DEFAULT VALUES", "")
	returning := g.SQLArg(e, "returning")

	if g.C.ReturningEnd {
		expressionSQL = expressionSQL + onConflict + defaultValues + returning
	} else {
		expressionSQL = returning + expressionSQL + onConflict
	}

	partitionBy := pfx(" ", g.SQLArg(e, "partition"))
	settings := pfx(" ", g.SQLArg(e, "settings"))
	source := pfx("TABLE ", g.SQLArg(e, "source"))

	sql := "INSERT" + hint + alternative + ignore + this + stored + byName + exists + partitionBy + settings + where +
		using + expressionSQL + source
	return g.PrependCtes(e, sql)
}

func (g *Generator) introducerSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " " + g.SQLArg(e, "expression")
}

func (g *Generator) killSQL(e *exp.Expr) string {
	kind := pfx(" ", g.SQLArg(e, "kind"))
	this := pfx(" ", g.SQLArg(e, "this"))
	return "KILL" + kind + this
}

func (g *Generator) pseudotypeSQL(e *exp.Expr) string { return e.Name() }

func (g *Generator) objectidentifierSQL(e *exp.Expr) string { return e.Name() }

func (g *Generator) onconflictSQL(e *exp.Expr) string {
	conflict := iff(e.Bool("duplicate"), "ON DUPLICATE KEY", "ON CONFLICT")
	constraint := pfx(" ON CONSTRAINT ", g.SQLArg(e, "constraint"))
	conflictKeys := around("(", g.FlatKey(e, "conflict_keys"), ")")
	indexPredicate := g.SQLArg(e, "index_predicate")
	conflictKeys = conflictKeys + indexPredicate + " "
	action := g.SQLArg(e, "action")
	expressions := g.Flat(e)
	if expressions != "" {
		setKeyword := iff(g.C.DuplicateKeyUpdateWithSet, "SET ", "")
		expressions = " " + setKeyword + expressions
	}
	where := g.SQLArg(e, "where")
	return conflict + constraint + conflictKeys + action + expressions + where
}

func (g *Generator) returningSQL(e *exp.Expr) string {
	return g.Seg("RETURNING") + " " + g.Flat(e)
}

func (g *Generator) rowformatdelimitedpropertySQL(e *exp.Expr) string {
	fields := pfx(" FIELDS TERMINATED BY ", g.SQLArg(e, "fields"))
	escaped := pfx(" ESCAPED BY ", g.SQLArg(e, "escaped"))
	items := pfx(" COLLECTION ITEMS TERMINATED BY ", g.SQLArg(e, "collection_items"))
	keys := pfx(" MAP KEYS TERMINATED BY ", g.SQLArg(e, "map_keys"))
	lines := pfx(" LINES TERMINATED BY ", g.SQLArg(e, "lines"))
	null := pfx(" NULL DEFINED AS ", g.SQLArg(e, "null"))
	return "ROW FORMAT DELIMITED" + fields + escaped + items + keys + lines + null
}

func (g *Generator) withtablehintSQL(e *exp.Expr) string {
	return "WITH (" + g.Flat(e) + ")"
}

func (g *Generator) indextablehintSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this") + " INDEX"
	target := pfx(" FOR ", g.SQLArg(e, "target"))
	return this + target + " (" + g.Flat(e) + ")"
}

func (g *Generator) historicaldataSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	kind := g.SQLArg(e, "kind")
	expr := g.SQLArg(e, "expression")
	return this + " (" + kind + " => " + expr + ")"
}
