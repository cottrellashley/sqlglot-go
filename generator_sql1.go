package sqlglot

// Generator methods translated from sqlglot/generator.py (uncache_sql .. indexparameters_sql).

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/tokens"
)

func (g *Generator) uncacheSQL(e *exp.Expr) string {
	table := g.SQLArg(e, "this")
	existsSQL := iff(e.Bool("exists"), " IF EXISTS", "")
	return "UNCACHE TABLE" + existsSQL + " " + table
}

func (g *Generator) cacheSQL(e *exp.Expr) string {
	lazy := iff(e.Bool("lazy"), " LAZY", "")
	table := g.SQLArg(e, "this")
	options := ""
	if opts := e.List("options"); len(opts) > 0 {
		options = " OPTIONS(" + g.SQL(opts[0]) + " = " + g.SQL(seqGet(opts, 1)) + ")"
	}
	sql := pfx(" AS"+g.Sep(), g.SQLArg(e, "expression"))
	sql = "CACHE" + lazy + " TABLE " + table + options + sql
	return g.PrependCtes(e, sql)
}

func (g *Generator) charactersetSQL(e *exp.Expr) string {
	def := iff(e.Bool("default"), "DEFAULT ", "")
	return def + "CHARACTER SET=" + g.SQLArg(e, "this")
}

func (g *Generator) columnParts(e *exp.Expr) string {
	if e.Bool("shadow") && g.D.ProjectionAliasesShadowSourceNames {
		// The qualifier would be captured by a colliding projection alias (see qualify_columns)
		return g.SQLArg(e, "this")
	}
	var parts []string
	for _, k := range []string{"catalog", "db", "table", "this"} {
		if v := e.Arg(k); exp.Truthy(v) {
			parts = append(parts, g.SQLAny(v))
		}
	}
	return strings.Join(parts, ".")
}

func (g *Generator) columnSQL(e *exp.Expr) string {
	joinMark := iff(e.Bool("join_mark"), " (+)", "")
	if joinMark != "" && !g.D.SupportsColumnJoinMarks {
		joinMark = ""
		g.Unsupported("Outer join syntax using the (+) operator is not supported.")
	}
	return g.ColumnParts(e) + joinMark
}

func (g *Generator) pseudocolumnSQL(e *exp.Expr) string { return g.ColumnSQL(e) }

func (g *Generator) columnpositionSQL(e *exp.Expr) string {
	this := pfx(" ", g.SQLArg(e, "this"))
	position := g.SQLArg(e, "position")
	return position + this
}

//gen:dispatch g.ColumndefSQL(e, " ")
func (g *Generator) columndefSQL(e *exp.Expr, sep string) string {
	column := g.SQLArg(e, "this")
	kind := g.SQLArg(e, "kind")
	constraints := g.Expressions(e, ExprsOpts{Key: "constraints", Sep: " ", Flat: true})
	exists := iff(e.Bool("exists"), "IF NOT EXISTS ", "")
	kind = pfx(sep, kind)
	constraints = pfx(" ", constraints)
	position := pfx(" ", g.SQLArg(e, "position"))
	if e.Find(exp.ComputedColumnConstraint) != nil && !g.C.ComputedColumnWithType {
		kind = ""
	}
	return exists + column + kind + constraints + position
}

func (g *Generator) columnconstraintSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	kindSQL := pyStrip(g.SQLArg(e, "kind"))
	if this != "" {
		return "CONSTRAINT " + this + " " + kindSQL
	}
	return kindSQL
}

func (g *Generator) computedcolumnconstraintSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	persisted := ""
	if e.Bool("not_null") {
		persisted = " PERSISTED NOT NULL"
	} else if e.Bool("persisted") {
		persisted = " PERSISTED"
	}
	return "AS " + this + persisted
}

func (g *Generator) autoincrementcolumnconstraintSQL(e *exp.Expr) string {
	return g.TokenSQL(tokens.AutoIncrement)
}

func (g *Generator) compresscolumnconstraintSQL(e *exp.Expr) string {
	var this string
	if _, ok := e.Arg("this").([]*exp.Expr); ok {
		this = g.Wrap(g.FlatKey(e, "this"))
	} else {
		this = g.SQLArg(e, "this")
	}
	return "COMPRESS " + this
}

func (g *Generator) generatedasidentitycolumnconstraintSQL(e *exp.Expr) string {
	this := ""
	if v := e.Arg("this"); v != nil {
		onNull := iff(e.Bool("on_null"), " ON NULL", "")
		this = iff(exp.Truthy(v), " ALWAYS", " BY DEFAULT"+onNull)
	}
	start := pfx("START WITH ", argStr(e.Arg("start")))
	increment := pfx(" INCREMENT BY ", argStr(e.Arg("increment")))
	minvalue := pfx(" MINVALUE ", argStr(e.Arg("minvalue")))
	maxvalue := pfx(" MAXVALUE ", argStr(e.Arg("maxvalue")))
	cycleSQL := ""
	if cycle := e.Arg("cycle"); cycle != nil {
		cycleSQL = iff(!exp.Truthy(cycle), " NO", "") + " CYCLE"
		if start == "" && increment == "" {
			cycleSQL = pyStrip(cycleSQL)
		}
	}
	sequenceOpts := ""
	if start != "" || increment != "" || cycleSQL != "" {
		sequenceOpts = " (" + pyStrip(start+increment+minvalue+maxvalue+cycleSQL) + ")"
	}
	expr := g.SQLArg(e, "expression")
	expr = iff(expr != "", "("+expr+")", "IDENTITY")
	return "GENERATED" + this + " AS " + expr + sequenceOpts
}

func (g *Generator) generatedasrowcolumnconstraintSQL(e *exp.Expr) string {
	start := iff(e.Bool("start"), "START", "END")
	hidden := iff(e.Bool("hidden"), " HIDDEN", "")
	return "GENERATED ALWAYS AS ROW " + start + hidden
}

func (g *Generator) periodforsystemtimeconstraintSQL(e *exp.Expr) string {
	return "PERIOD FOR SYSTEM_TIME (" + g.SQLArg(e, "this") + ", " + g.SQLArg(e, "expression") + ")"
}

func (g *Generator) notnullcolumnconstraintSQL(e *exp.Expr) string {
	return iff(e.Bool("allow_null"), "", "NOT ") + "NULL"
}

func (g *Generator) primarykeycolumnconstraintSQL(e *exp.Expr) string {
	if desc := e.Arg("desc"); desc != nil {
		return "PRIMARY KEY" + iff(exp.Truthy(desc), " DESC", " ASC")
	}
	options := pfx(" ", g.Expressions(e, ExprsOpts{Key: "options", Flat: true, Sep: " "}))
	include := g.SQLArg(e, "include")
	return "PRIMARY KEY" + include + options
}

func (g *Generator) uniquecolumnconstraintSQL(e *exp.Expr) string {
	this := pfx(" ", g.SQLArg(e, "this"))
	indexType := pfx(" USING ", argStr(e.Arg("index_type")))
	onConflict := pfx(" ", g.SQLArg(e, "on_conflict"))
	nullsSQL := iff(e.Bool("nulls"), " NULLS NOT DISTINCT", "")
	options := pfx(" ", g.Expressions(e, ExprsOpts{Key: "options", Flat: true, Sep: " "}))
	include := g.SQLArg(e, "include")
	return "UNIQUE" + nullsSQL + this + indexType + onConflict + include + options
}

func (g *Generator) inoutcolumnconstraintSQL(e *exp.Expr) string {
	input, output := e.Bool("input_"), e.Bool("output")
	// VARIADIC is mutually exclusive with IN/OUT/INOUT
	if e.Bool("variadic") {
		return "VARIADIC"
	}
	if input && output {
		return "IN" + g.C.InoutSeparator + "OUT"
	}
	if input {
		return "IN"
	}
	if output {
		return "OUT"
	}
	return ""
}

func (g *Generator) createableSQL(e *exp.Expr, locations PropertyLocations) string {
	return g.SQLArg(e, "this")
}

func (g *Generator) createSQL(e *exp.Expr) string {
	kind := g.SQLArg(e, "kind")
	if m := g.D.InverseCreatableKindMapping[kind]; m != "" {
		kind = m
	}
	properties := e.Get("properties")
	if kind == "TRIGGER" && properties != nil {
		if pe := properties.Expressions(); len(pe) > 0 && pe[0].IsKind(exp.TriggerProperties) && pe[0].Bool("constraint") {
			kind = "CONSTRAINT " + kind
		}
	}
	locs := PropertyLocations{}
	if properties != nil {
		locs = g.LocateProperties(properties)
	}
	this := g.CreateableSQL(e, locs)

	propertiesSQL := ""
	if len(locs[exp.PostSchema]) > 0 || len(locs[exp.PostWith]) > 0 {
		propsAST := newPropertiesWithParent(concatExprs(locs[exp.PostSchema], locs[exp.PostWith]), e)
		propertiesSQL = g.SQL(propsAST)
		if len(locs[exp.PostSchema]) > 0 {
			propertiesSQL = g.Sep() + propertiesSQL
		} else if !g.Pretty {
			// Standalone POST_WITH properties need a leading whitespace in non-pretty mode
			propertiesSQL = " " + propertiesSQL
		}
	}

	begin := iff(e.Bool("begin"), " BEGIN", "")
	expressionSQL := g.SQLArg(e, "expression")
	if expressionSQL != "" {
		expressionSQL = begin + g.Sep() + expressionSQL
		ee := e.Expression()
		if !ee.IsKind(exp.MacroOverloads) && (g.C.CreateFunctionReturnAs || !ee.IsKind(exp.Return)) {
			postaliasPropsSQL := ""
			if len(locs[exp.PostAlias]) > 0 {
				postaliasPropsSQL = g.Properties(exp.New(exp.Properties, "expressions", locs[exp.PostAlias]), "", ", ", "", false)
			}
			expressionSQL = " AS" + pfx(" ", postaliasPropsSQL) + expressionSQL
		}
	}

	postindexPropsSQL := ""
	if len(locs[exp.PostIndex]) > 0 {
		postindexPropsSQL = g.Properties(exp.New(exp.Properties, "expressions", locs[exp.PostIndex]), " ", ", ", "", false)
	}
	indexes := pfx(" ", g.Expressions(e, ExprsOpts{Key: "indexes", NoIndent: true, Sep: " "}))
	indexSQL := indexes + postindexPropsSQL

	replace := iff(e.Bool("replace"), " OR REPLACE", "")
	refresh := iff(e.Bool("refresh"), " OR REFRESH", "")
	unique := iff(e.Bool("unique"), " UNIQUE", "")

	clusteredSQL := ""
	if clustered := e.Arg("clustered"); clustered != nil {
		clusteredSQL = iff(exp.Truthy(clustered), " CLUSTERED COLUMNSTORE", " NONCLUSTERED COLUMNSTORE")
	}

	postcreatePropsSQL := ""
	if len(locs[exp.PostCreate]) > 0 {
		postcreatePropsSQL = g.Properties(exp.New(exp.Properties, "expressions", locs[exp.PostCreate]), " ", " ", "", false)
	}
	modifiers := clusteredSQL + replace + refresh + unique + postcreatePropsSQL

	postexpressionPropsSQL := ""
	if len(locs[exp.PostExpression]) > 0 {
		postexpressionPropsSQL = g.Properties(exp.New(exp.Properties, "expressions", locs[exp.PostExpression]), " ", " ", "", false)
	}

	concurrently := iff(e.Bool("concurrently"), " CONCURRENTLY", "")
	existsSQL := iff(e.Bool("exists"), " IF NOT EXISTS", "")
	noSchemaBinding := iff(e.Bool("no_schema_binding"), " WITH NO SCHEMA BINDING", "")
	clone := pfx(" ", g.SQLArg(e, "clone"))

	var propertiesExpression string
	if g.C.ExpressionPrecedesPropertiesCreatables[kind] {
		propertiesExpression = expressionSQL + propertiesSQL
	} else {
		propertiesExpression = propertiesSQL + expressionSQL
	}
	expressionSQL = "CREATE" + modifiers + " " + kind + concurrently + existsSQL + " " + this + propertiesExpression +
		postexpressionPropsSQL + indexSQL + noSchemaBinding + clone
	return g.PrependCtes(e, expressionSQL)
}

func (g *Generator) sequencepropertiesSQL(e *exp.Expr) string {
	start := pfx("START WITH ", g.SQLArg(e, "start"))
	increment := pfx(" INCREMENT BY ", g.SQLArg(e, "increment"))
	minvalue := pfx(" MINVALUE ", g.SQLArg(e, "minvalue"))
	maxvalue := pfx(" MAXVALUE ", g.SQLArg(e, "maxvalue"))
	owned := pfx(" OWNED BY ", g.SQLArg(e, "owned"))
	cacheStr := ""
	switch c := e.Arg("cache").(type) {
	case nil:
	case bool:
		if c {
			cacheStr = " CACHE"
		} else {
			cacheStr = " CACHE False"
		}
	default:
		cacheStr = " CACHE " + pyStr(c)
	}
	options := pfx(" ", g.Expressions(e, ExprsOpts{Key: "options", Flat: true, Sep: " "}))
	return pyLStrip(start + increment + minvalue + maxvalue + cacheStr + options + owned)
}

func (g *Generator) triggerpropertiesSQL(e *exp.Expr) string {
	timing := e.Str("timing")
	var evs []string
	for _, ev := range e.List("events") {
		evs = append(evs, g.SQL(ev))
	}
	events := strings.Join(evs, " OR ")
	timingEvents := ""
	if timing != "" || events != "" {
		timingEvents = pyStrip(timing + " " + events)
	}
	parts := []string{timingEvents, "ON", g.SQLArg(e, "table")}
	if rt := e.Get("referenced_table"); rt != nil {
		parts = append(parts, "FROM", g.SQL(rt))
	}
	if d := e.Arg("deferrable"); exp.Truthy(d) {
		parts = append(parts, pyStr(d))
	}
	if i := e.Arg("initially"); exp.Truthy(i) {
		parts = append(parts, "INITIALLY "+pyStr(i))
	}
	if r := e.Get("referencing"); r != nil {
		parts = append(parts, g.SQL(r))
	}
	if f := e.Arg("for_each"); exp.Truthy(f) {
		parts = append(parts, "FOR EACH "+pyStr(f))
	}
	if w := e.Get("when"); w != nil {
		parts = append(parts, "WHEN ("+g.SQL(w)+")")
	}
	parts = append(parts, g.SQLArg(e, "execute"))
	return strings.Join(parts, g.Sep())
}

func (g *Generator) triggerreferencingSQL(e *exp.Expr) string {
	var parts []string
	if o := e.Get("old"); o != nil {
		parts = append(parts, "OLD TABLE AS "+g.SQL(o))
	}
	if n := e.Get("new"); n != nil {
		parts = append(parts, "NEW TABLE AS "+g.SQL(n))
	}
	return "REFERENCING " + strings.Join(parts, " ")
}

func (g *Generator) triggereventSQL(e *exp.Expr) string {
	if len(e.List("columns")) > 0 {
		return pyStr(e.Arg("this")) + " OF " + g.FlatKey(e, "columns")
	}
	return g.SQLArg(e, "this")
}

func (g *Generator) cloneSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	shallow := iff(e.Bool("shallow"), "SHALLOW ", "")
	keyword := iff(e.Bool("copy") && g.C.SupportsTableCopy, "COPY", "CLONE")
	return shallow + keyword + " " + this
}

func (g *Generator) describeSQL(e *exp.Expr) string {
	style := pfx(" ", argStr(e.Arg("style")))
	partition := pfx(" ", g.SQLArg(e, "partition"))
	format := pfx(" ", g.SQLArg(e, "format"))
	asJSON := iff(e.Bool("as_json"), " AS JSON", "")
	return "DESCRIBE" + style + format + " " + g.SQLArg(e, "this") + partition + asJSON
}

func (g *Generator) heredocSQL(e *exp.Expr) string {
	tag := g.SQLArg(e, "tag")
	return "$" + tag + "$" + g.SQLArg(e, "this") + "$" + tag + "$"
}

func (g *Generator) prependCtes(e *exp.Expr, sql string) string {
	if with := g.SQLArg(e, "with_"); with != "" {
		sql = with + g.Sep() + sql
	}
	return sql
}

func (g *Generator) withSQL(e *exp.Expr) string {
	udfs := pfx("WITH ", g.FlatKey(e, "udfs"))
	sql := g.Flat(e)
	recursive := iff(g.C.CteRecursiveKeywordRequired && e.Bool("recursive"), "RECURSIVE ", "")
	search := pfx(" ", g.SQLArg(e, "search"))
	cycle := pfx(" ", g.SQLArg(e, "cycle"))
	if sql != "" {
		sql = "WITH " + recursive + sql + search + cycle
	}
	if udfs != "" && sql != "" {
		return udfs + " " + sql
	}
	return udfs + sql
}

func (g *Generator) cteSQL(e *exp.Expr) string {
	if alias := e.Get("alias"); alias != nil {
		alias.AddComments(e.PopComments(), false)
	}
	aliasSQL := g.SQLArg(e, "alias")
	materialized := ""
	if m, ok := e.Arg("materialized").(bool); ok {
		materialized = iff(m, "MATERIALIZED ", "NOT MATERIALIZED ")
	} else if v := e.Arg("materialized"); exp.Truthy(v) {
		materialized = "MATERIALIZED "
	}
	keyExpressions := around(" USING KEY (", g.FlatKey(e, "key_expressions"), ")")
	return aliasSQL + keyExpressions + " AS " + materialized + g.Wrap(e)
}

func (g *Generator) tablealiasSQL(e *exp.Expr) string {
	alias := g.SQLArg(e, "this")
	columns := around("(", g.FlatKey(e, "columns"), ")")
	if columns != "" && !g.C.SupportsTableAliasColumns && !(g.C.SupportsNamedCteColumns && e.Parent().IsKind(exp.CTE)) {
		columns = ""
		g.Unsupported("Named columns are not supported in table alias.")
	}
	if alias == "" && !g.D.UnnestColumnOnly {
		alias = g.NextName()
	}
	return alias + columns
}

func pyIntBase(s string, base int) (*big.Int, bool) {
	s = strings.ReplaceAll(pyStrip(s), "_", "")
	return new(big.Int).SetString(s, base)
}

func (g *Generator) bitstringSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	if g.D.BitStart != "" {
		return g.D.BitStart + this + g.D.BitEnd
	}
	n, ok := pyIntBase(this, 2)
	if !ok {
		g.Raisef("invalid literal for int() with base 2: '%s'", this)
	}
	return n.String()
}

//gen:dispatch g.HexstringSQL(e, "")
func (g *Generator) hexstringSQL(e *exp.Expr, binaryFunctionRepr string) string {
	this := g.SQLArg(e, "this")
	isIntegerType := e.Bool("is_integer")
	if (isIntegerType && !g.D.HexStringIsIntegerType) || (g.D.HexStart == "" && binaryFunctionRepr == "") {
		// Integer representation will be returned if:
		// - The read dialect treats the hex value as integer literal but not the write
		// - The transpilation is not supported (write dialect hasn't set HEX_START or the param flag)
		n, ok := pyIntBase(this, 16)
		if !ok {
			g.Raisef("invalid literal for int() with base 16: '%s'", this)
		}
		return n.String()
	}
	if !isIntegerType {
		// Read dialect treats the hex value as BINARY/BLOB
		if binaryFunctionRepr != "" {
			// The write dialect supports the transpilation to its equivalent BINARY/BLOB
			return g.Func(binaryFunctionRepr, exp.LiteralString(this))
		}
		if g.D.HexStringIsIntegerType {
			// The write dialect does not support the transpilation, it'll treat the hex value as INTEGER
			g.Unsupported("Unsupported transpilation from BINARY/BLOB hex string")
		}
	}
	return g.D.HexStart + this + g.D.HexEnd
}

func latin1Hex(g *Generator, s string) string {
	var b strings.Builder
	for _, r := range s {
		if r > 0xff {
			g.Raisef("'latin-1' codec can't encode character '%c'", r)
		}
		fmt.Fprintf(&b, "%02x", r)
	}
	return b.String()
}

func (g *Generator) bytestringSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	isBytes := e.Bool("is_bytes")
	if isBytes && g.C.ByteStringFunction != "" {
		// Hex digits can be quoted directly without escaping.
		return g.Func(g.C.ByteStringFunction, "'"+latin1Hex(g, this)+"'")
	}
	if g.D.ByteStart != "" {
		escaped := g.EscapeStr(this, len(g.D.Tokenizer.NumericEscapes) > 0 || g.D.Tokenizer.DropUnknownEscapes,
			g.D.ByteEnd, g.EscapedByteQuoteEnd, true, isBytes)
		delimited := g.D.ByteStart + escaped + g.D.ByteEnd
		if isBytes && !g.D.ByteStringIsBytesType {
			return g.SQL(exp.NewCast(delimited, exp.DTypeBinary, exp.WithDialect(g.D)))
		}
		if !isBytes && g.D.ByteStringIsBytesType {
			return g.SQL(exp.NewCast(delimited, exp.DTypeVarchar, exp.WithDialect(g.D)))
		}
		return delimited
	}
	if contains(g.D.Tokenizer.StringEscapes, `\`) {
		return g.SQL(exp.LiteralString(this))
	}
	g.Unsupported("Byte strings are not supported for " + g.D.ClassName)
	return ""
}

// reSubFunc mirrors re.sub with a callable (or a group-expanding template) replacement.
func reSubFunc(re *regexp.Regexp, s string, repl func(groups []string) string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		return repl(re.FindStringSubmatch(m))
	})
}

func (g *Generator) unicodestringSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	escape := e.Get("escape")
	unicodeStart := g.D.UnicodeStart
	var escapeSubstitute func([]string) string
	var leftQuote, rightQuote string
	if unicodeStart != "" {
		escapeSubstitute = func(m []string) string { return `\` + m[1] }
		leftQuote, rightQuote = unicodeStart, g.D.UnicodeEnd
	} else {
		escapeSubstitute = func(m []string) string { return `\u` + m[1] }
		leftQuote, rightQuote = g.D.QuoteStart, g.D.QuoteEnd
	}
	var escapePattern *regexp.Regexp
	escapeSQL := ""
	if escape != nil {
		re, err := regexp.Compile(escape.Name() + `(\d+)`)
		if err != nil {
			g.Raise(err)
		}
		escapePattern = re
		if g.C.SupportsUescape {
			escapeSQL = " UESCAPE " + g.SQL(escape)
		}
	} else {
		escapePattern = EscapedUnicodeRe
	}
	if unicodeStart == "" || (escape != nil && !g.C.SupportsUescape) {
		sub := escapeSubstitute
		if g.C.UnicodeSubstitute != nil {
			sub = g.C.UnicodeSubstitute
		}
		this = reSubFunc(escapePattern, this, sub)
	}
	if unicodeStart != "" {
		// A Unicode literal only escapes its delimiter by doubling it; the escape character
		// introduces a code point, so the dialect's ordinary string escapes don't apply here
		this = strings.ReplaceAll(g.ReplaceLineBreaks(this), rightQuote, rightQuote+rightQuote)
	} else {
		this = g.EscapeStr(this, false, "", "", false, false)
	}
	return leftQuote + this + rightQuote + escapeSQL
}

func (g *Generator) rawstringSQL(e *exp.Expr) string {
	s := e.Str("this")
	if contains(g.D.Tokenizer.StringEscapes, `\`) {
		s = strings.ReplaceAll(s, `\`, `\\`)
	}
	s = g.EscapeStr(s, false, "", "", false, false)
	return g.D.QuoteStart + s + g.D.QuoteEnd
}

func (g *Generator) datatypeparamSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	specifier := g.SQLArg(e, "expression")
	specifier = iff(specifier != "" && g.C.DataTypeSpecifiersAllowed, " "+specifier, "")
	return this + specifier
}

func (g *Generator) datatypeParamBoundLimiter(e *exp.Expr, typeValue exp.DType, defaults []int, bounds []*int) *exp.Expr {
	params := e.Expressions()
	if len(params) == 0 {
		if len(defaults) > 0 {
			var ps []*exp.Expr
			for _, d := range defaults {
				ps = append(ps, exp.New(exp.DataTypeParam, "this", exp.LiteralNumber(d)))
			}
			e.Set("expressions", ps)
		}
		return e
	}
	if len(bounds) == 0 {
		return e
	}
	for i, param := range params {
		var bound *int
		if i < len(bounds) {
			bound = bounds[i]
		}
		if bound == nil {
			continue
		}
		paramValue := param
		if param.IsKind(exp.DataTypeParam) {
			paramValue = param.This()
		}
		if paramValue.IsKind(exp.Literal) && paramValue.IsNumber() {
			if v := pyNum(paramValue); v != nil && v.Cmp(new(big.Rat).SetInt64(int64(*bound))) > 0 {
				g.Unsupported(fmt.Sprintf("%s parameter %s exceeds %s's maximum of %d; capping",
					typeValue, paramValue.Name(), g.D.ClassName, *bound))
				params[i] = exp.New(exp.DataTypeParam, "this", exp.LiteralNumber(*bound))
			}
		}
	}
	return e
}

func (g *Generator) datatypeSQL(e *exp.Expr) string {
	nested := ""
	values := ""
	exprNested := e.Bool("nested")
	typeValue, isDType := e.Arg("this").(exp.DType)

	if !exprNested && isDType {
		if settings, ok := g.C.TypeParamSettings[typeValue]; ok {
			e = g.DatatypeParamBoundLimiter(e, typeValue, settings.Defaults, settings.Limits)
		}
	}

	var interior string
	if exprNested && g.Pretty {
		interior = g.Expressions(e, ExprsOpts{Dynamic: true, NewLine: true, SkipFirst: true, SkipLast: true})
	} else {
		interior = g.Flat(e)
	}

	if isDType && g.C.UnsupportedTypes.Has(typeValue) {
		g.Unsupported(fmt.Sprintf("Data type %s is not supported when targeting %s", typeValue, g.D.ClassName))
	}

	var typeSQL string
	if typeValue == exp.DTypeUserdefined && e.Arg("kind") != nil && exp.Truthy(e.Arg("kind")) {
		typeSQL = g.SQLArg(e, "kind")
	} else if typeValue == exp.DTypeCharacterSet {
		char := iff(interior != "", "CHAR("+interior+")", "CHAR")
		return char + " CHARACTER SET " + g.SQLArg(e, "kind")
	} else if isDType {
		if m, ok := g.C.TypeMapping[typeValue]; ok {
			typeSQL = m
		} else {
			typeSQL = string(typeValue)
		}
	} else {
		typeSQL = pyStr(e.Arg("this"))
	}

	if interior != "" {
		if exprNested {
			nested = g.C.StructDelimiter[0] + interior + g.C.StructDelimiter[1]
			if e.Arg("values") != nil {
				l, r := "(", ")"
				if typeValue == exp.DTypeArray {
					l, r = "[", "]"
				}
				values = l + g.FlatKey(e, "values") + r
			}
		} else if typeValue == exp.DTypeInterval {
			nested = " " + interior
		} else {
			nested = "(" + interior + ")"
		}
	}

	typeSQL = typeSQL + nested + values
	if g.C.TzToWithTimeZone && (typeValue == exp.DTypeTimetz || typeValue == exp.DTypeTimestamptz) {
		typeSQL += " WITH TIME ZONE"
	}
	if collate := g.SQLArg(e, "collate"); collate != "" {
		typeSQL += " COLLATE " + collate
	}
	return typeSQL
}

func (g *Generator) directorySQL(e *exp.Expr) string {
	local := iff(e.Bool("local"), "LOCAL ", "")
	rowFormat := pfx(" ", g.SQLArg(e, "row_format"))
	return local + "DIRECTORY " + g.SQLArg(e, "this") + rowFormat
}

func (g *Generator) deleteSQL(e *exp.Expr) string {
	hint := g.SQLArg(e, "hint")
	this := pfx(" FROM ", g.SQLArg(e, "this"))
	using := pfx(" USING ", g.ExprsKey(e, "using"))
	cluster := pfx(" ", g.SQLArg(e, "cluster"))
	where := g.SQLArg(e, "where")
	returning := g.SQLArg(e, "returning")
	order := g.SQLArg(e, "order")
	limit := g.SQLArg(e, "limit")
	tables := pfx(" ", g.ExprsKey(e, "tables"))
	var expressionSQL string
	if g.C.ReturningEnd {
		expressionSQL = this + using + cluster + where + returning + order + limit
	} else {
		expressionSQL = returning + this + using + cluster + where + order + limit
	}
	return g.PrependCtes(e, "DELETE"+hint+tables+expressionSQL)
}

func (g *Generator) dropSQL(e *exp.Expr) string {
	tables := g.FlatKey(e, "tables")
	expressions := around(" (", g.Flat(e), ")")
	kind := pyStr(e.Arg("kind"))
	if m := g.D.InverseCreatableKindMapping[kind]; m != "" {
		kind = m
	}
	iceberg := iff(e.Bool("iceberg") && g.C.SupportsDropAlterIcebergProperty, " ICEBERG", "")
	existsSQL := iff(e.Bool("exists"), " IF EXISTS ", " ")
	concurrentlySQL := iff(e.Bool("concurrently"), " CONCURRENTLY", "")
	onCluster := pfx(" ", g.SQLArg(e, "cluster"))
	temporary := iff(e.Bool("temporary"), " TEMPORARY", "")
	materialized := iff(e.Bool("materialized"), " MATERIALIZED", "")
	cascade := iff(e.Bool("cascade"), " CASCADE", "")
	restrict := iff(e.Bool("restrict"), " RESTRICT", "")
	constraints := iff(e.Bool("constraints"), " CONSTRAINTS", "")
	purge := iff(e.Bool("purge"), " PURGE", "")
	sync := iff(e.Bool("sync"), " SYNC", "")
	force := iff(e.Bool("force"), " FORCE", "")
	return "DROP" + temporary + materialized + iceberg + " " + kind + concurrentlySQL + existsSQL + tables + onCluster +
		expressions + cascade + restrict + constraints + purge + sync + force
}

// triBool converts a None/True/False argument into -1/1/0.
func triBool(v any) int {
	switch x := v.(type) {
	case nil:
		return -1
	case bool:
		return iff(x, 1, 0)
	}
	return iff(exp.Truthy(v), 1, 0)
}

func triFromPtr(b *bool) int {
	if b == nil {
		return -1
	}
	return iff(*b, 1, 0)
}

func (g *Generator) setOperation(e *exp.Expr) string {
	opType := e.Kind()
	opName := strings.ToUpper(e.Key())
	distinct := triBool(e.Arg("distinct"))
	if distinct == 0 && (opType == exp.Except || opType == exp.Intersect) && !g.C.ExceptIntersectSupportAllClause {
		g.Unsupported(opName + " ALL is not supported")
	}
	defaultDistinct := triFromPtr(g.D.SetOpDistinctByDefault[opType])
	if distinct == -1 {
		distinct = defaultDistinct
		if distinct == -1 {
			g.Unsupported(opName + " requires DISTINCT or ALL to be specified")
		}
	}
	distinctOrAll := ""
	if distinct != defaultDistinct {
		distinctOrAll = iff(distinct == 1, " DISTINCT", " ALL")
	}
	sideKind := sfx(joinNonEmpty(" ", e.TextUpper("side"), e.TextUpper("kind")), " ")
	byName := iff(e.Bool("by_name"), " BY NAME", "")
	on := around(" ON (", g.FlatKey(e, "on"), ")")
	return sideKind + opName + distinctOrAll + byName + on
}

func hasQueryModifiers(e *exp.Expr) bool {
	for _, k := range exp.QueryModifiers {
		if exp.Truthy(e.Arg(k)) {
			return true
		}
	}
	return false
}

func (g *Generator) setOperations(e *exp.Expr) string {
	if !g.C.SetOpModifiers {
		limit, order, offset := e.Arg("limit"), e.Arg("order"), e.Arg("offset")
		if exp.Truthy(limit) || exp.Truthy(order) || exp.Truthy(offset) {
			sub := exp.NewSubqueryFrom(e, "_l_0", exp.WithCopy(false))
			sel := g.MoveCtesToTopLevel(sub.Select("*", exp.WithCopy(false)))
			for _, arg := range []string{"limit", "order", "offset"} {
				if v := e.Get(arg); v != nil {
					sel.Set(arg, v.Pop())
				}
			}
			return g.SQL(sel)
		}
	}

	type item struct {
		node   *exp.Expr
		s      string
		isStr  bool
		depth  int
		isInt  bool
		isNone bool
	}
	var sqls []string
	stack := []item{{node: e}}
	depth := 0
	openParens := ""

	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if it.isInt {
			// Closes a plain grouping opened below, restoring its depth
			depth = it.depth
			sqls[len(sqls)-1] += g.SepWith("") + g.IndentFull(")", depth, 0, true, false, false)
		} else if it.isStr {
			sqls = append(sqls, openParens+g.IndentFull(it.s, depth, 0, true, false, false))
			openParens = ""
		} else if node := it.node; node.Is(exp.SetOperation) {
			stack = append(stack, item{node: node.Expression()})
			stack = append(stack, item{s: g.MaybeCommentFull(g.SetOperation(node), nil, node.Comments, true, true), isStr: true})
			left := node.This()
			if !g.C.SetOpParenthesizedOperands && !g.D.IntersectBindsTighterThanUnionAndExcept {
				// Left grouping is implicit, so skip the `SELECT * FROM (...)` wrapper:
				// it renames duplicate columns and deepens recursion. Inner CTEs and
				// modifiers still need the wrapper.
				var wrappers []*exp.Expr
				inner := left
				for inner.IsKind(exp.Subquery) && inner.IsWrapper() {
					wrappers = append(wrappers, inner)
					inner = inner.This()
				}
				if inner.Is(exp.SetOperation) && !exp.Truthy(inner.Arg("with_")) && !hasQueryModifiers(inner) {
					for _, w := range wrappers {
						inner.AddComments(w.PopComments(), false)
					}
					left = inner
				}
			} else if g.C.SetOpParenthesizedOperands && left.IsKind(exp.Subquery) && left.IsWrapper() &&
				len(left.Comments) == 0 && left.This().Is(exp.SetOperation) &&
				!exp.Truthy(left.This().Arg("with_")) && !hasQueryModifiers(left.This()) {
				// Write a plain grouping's parentheses here rather than recursing through
				// subquery_sql: left-to-right chains nest one grouping per INTERSECT, and
				// long ones would exhaust the stack. The depth pushed here marks the close.
				openParens += g.IndentFull("(", depth, 0, true, false, false) + g.SepWith("")
				stack = append(stack, item{depth: depth, isInt: true})
				depth++
				left = left.This()
			}
			stack = append(stack, item{node: left})
		} else {
			if !g.C.SetOpLimits && node.IsKind(exp.Select) && exp.Truthy(node.Arg("limit")) {
				node = node.Subquery(nil, false)
				if !g.C.SetOpParenthesizedOperands {
					node = exp.NewSelect("*").From(node, exp.WithCopy(false))
				}
			} else if !g.C.SetOpParenthesizedOperands && node.IsKind(exp.Subquery) && node.Unnest().Is(exp.SetOperation) {
				node = exp.NewSelect("*").From(node, exp.WithCopy(false))
			}
			sqls = append(sqls, openParens+g.IndentFull(g.SQL(node), depth, 0, true, false, false))
			openParens = ""
		}
	}

	this := strings.Join(sqls, g.Sep())
	this = g.QueryModifiers(e, this)
	return g.PrependCtes(e, this)
}

func (g *Generator) fetchSQL(e *exp.Expr) string {
	direction := pfx(" ", argStr(e.Arg("direction")))
	count := pfx(" ", g.SQLArg(e, "count"))
	limitOptions := g.SQLArg(e, "limit_options")
	if limitOptions == "" {
		limitOptions = " ROWS ONLY"
	}
	return g.Seg("FETCH") + direction + count + limitOptions
}

func (g *Generator) limitoptionsSQL(e *exp.Expr) string {
	percent := iff(e.Bool("percent"), " PERCENT", "")
	rows := iff(e.Bool("rows"), " ROWS", "")
	withTies := iff(e.Bool("with_ties"), " WITH TIES", "")
	if withTies == "" && rows != "" {
		withTies = " ONLY"
	}
	return percent + rows + withTies
}

func (g *Generator) filterSQL(e *exp.Expr) string {
	this := g.SQLArg(e, "this")
	where := pyStrip(g.SQLArg(e, "expression"))
	return this + " FILTER(" + where + ")"
}

func (g *Generator) perSQL(e *exp.Expr) string {
	return g.SQLArg(e, "this") + " PER (" + g.Flat(e) + ")"
}

func (g *Generator) hintSQL(e *exp.Expr) string {
	if !g.C.QueryHints {
		g.Unsupported("Hints are not supported")
		return ""
	}
	return " /*+ " + pyStrip(g.Expressions(e, ExprsOpts{Sep: g.C.QueryHintSep})) + " */"
}

func (g *Generator) indexparametersSQL(e *exp.Expr) string {
	using := pfx(" USING ", g.SQLArg(e, "using"))
	columns := around("(", g.FlatKey(e, "columns"), ")")
	partitionBy := pfx(" PARTITION BY ", g.FlatKey(e, "partition_by"))
	where := g.SQLArg(e, "where")
	include := around(" INCLUDE (", g.FlatKey(e, "include"), ")")
	withStorage := around(" WITH (", g.FlatKey(e, "with_storage"), ")")
	tablespace := g.SQLArg(e, "tablespace")
	if tablespace != "" {
		prefix := iff(e.Parent().IsKind(exp.Index), "", " USING INDEX")
		tablespace = prefix + " TABLESPACE " + tablespace
	}
	on := pfx(" ON ", g.SQLArg(e, "on"))
	return using + columns + include + withStorage + tablespace + partitionBy + where + on
}
