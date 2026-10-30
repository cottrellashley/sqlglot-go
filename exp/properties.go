package exp

import "strings"

// PropertiesLocation mirrors exp.Properties.Location.
type PropertiesLocation int

const (
	PostCreate PropertiesLocation = iota
	PostName
	PostSchema
	PostWith
	PostAlias
	PostExpression
	PostIndex
	Unsupported
)

func (l PropertiesLocation) String() string {
	return [...]string{"POST_CREATE", "POST_NAME", "POST_SCHEMA", "POST_WITH", "POST_ALIAS", "POST_EXPRESSION", "POST_INDEX", "UNSUPPORTED"}[l]
}

// NameToProperty mirrors Properties.NAME_TO_PROPERTY.
var NameToProperty = map[string]Kind{
	"ALGORITHM":      AlgorithmProperty,
	"AUTO_INCREMENT": AutoIncrementProperty,
	"CHARACTER SET":  CharacterSetProperty,
	"CLUSTERED_BY":   ClusteredByProperty,
	"COLLATE":        CollateProperty,
	"COMMENT":        SchemaCommentProperty,
	"CREDENTIALS":    CredentialsProperty,
	"DEFINER":        DefinerProperty,
	"DISTKEY":        DistKeyProperty,
	"DISTRIBUTED_BY": DistributedByProperty,
	"DISTSTYLE":      DistStyleProperty,
	"ENGINE":         EngineProperty,
	"EXECUTE AS":     ExecuteAsProperty,
	"FORMAT":         FileFormatProperty,
	"LANGUAGE":       LanguageProperty,
	"LOCATION":       LocationProperty,
	"LOCK":           LockProperty,
	"PARTITIONED_BY": PartitionedByProperty,
	"RETURNS":        ReturnsProperty,
	"ROW_FORMAT":     RowFormatProperty,
	"SORTKEY":        SortKeyProperty,
	"ENCODE":         EncodeProperty,
	"INCLUDE":        IncludeProperty,
}

// PropertyToName mirrors Properties.PROPERTY_TO_NAME.
var PropertyToName = func() map[Kind]string {
	m := map[Kind]string{}
	for k, v := range NameToProperty {
		m[v] = k
	}
	return m
}()

// PropertiesFromDict mirrors Properties.from_dict.
func PropertiesFromDict(props OrderedMap) *Expr {
	var exprs []*Expr
	for _, kv := range props {
		key, _ := kv.Key.(string)
		if k, ok := NameToProperty[strings.ToUpper(key)]; ok {
			exprs = append(exprs, New(k, "this", ConvertValue(kv.Value, false)))
		} else {
			exprs = append(exprs, New(Property, "this", LiteralString(key), "value", ConvertValue(kv.Value, false)))
		}
	}
	return New(Properties, "expressions", exprs)
}
