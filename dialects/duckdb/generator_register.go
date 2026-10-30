package duckdb

import (
	sqlglot "github.com/cottrellashley/sqlglot-go"
	"github.com/cottrellashley/sqlglot-go/exp"
)

// methods registers the DuckDBGenerator methods. Python's super() calls resolve to the
// entries captured from the parent (base) generator before they are replaced.
func methods(c *sqlglot.GeneratorConfig) {
	m := c.SQLMethods
	sup := func(k exp.Kind) sqlglot.GenFunc { return m[k] }

	superWindow, superFilter := sup(exp.Window), sup(exp.Filter)

	m[exp.DataType] = datatypeSQL(sup(exp.DataType))
	m[exp.TimeSlice] = timesliceSQL
	m[exp.UnicodeString] = unicodestringSQL
	m[exp.ByteString] = bytestringSQL(sup(exp.ByteString))
	m[exp.BitmapBucketNumber] = bitmapbucketnumberSQL
	m[exp.BitmapBitPosition] = bitmapbitpositionSQL
	m[exp.BitmapConstructAgg] = bitmapconstructaggSQL
	m[exp.GetIgnoreCase] = unsupportedFallback("DuckDB does not support the GET_IGNORE_CASE() function")
	m[exp.Compress] = unsupportedFallback("DuckDB does not support the COMPRESS() function")
	m[exp.Encrypt] = unsupportedFallback("ENCRYPT is not supported in DuckDB")
	m[exp.Decrypt] = decryptSQL
	m[exp.DecryptRaw] = decryptrawSQL
	m[exp.EncryptRaw] = unsupportedFallback("ENCRYPT_RAW is not supported in DuckDB")
	m[exp.ParseUrl] = unsupportedFallback("PARSE_URL is not supported in DuckDB")
	m[exp.ParseIp] = unsupportedFallback("PARSE_IP is not supported in DuckDB")
	m[exp.DecompressString] = unsupportedFallback("DECOMPRESS_STRING is not supported in DuckDB")
	m[exp.DecompressBinary] = unsupportedFallback("DECOMPRESS_BINARY is not supported in DuckDB")
	m[exp.JarowinklerSimilarity] = jarowinklersimilaritySQL
	m[exp.Randstr] = randstrSQL
	m[exp.Reduce] = reduceSQL
	m[exp.Zipf] = zipfSQL
	m[exp.ToBinary] = tobinarySQL
	m[exp.ToNumber] = tonumberSQL(sup(exp.ToNumber))
	m[exp.Generator] = generatorSQL
	m[exp.Greatest] = greatestLeastSQL
	m[exp.Least] = greatestLeastSQL
	c.Methods.LambdaSQL = lambdaSQL(c.Methods.LambdaSQL)
	m[exp.Show] = showSQL
	m[exp.Soundex] = soundexSQL
	m[exp.Placeholder] = placeholderSQL(sup(exp.Placeholder))
	m[exp.Install] = installSQL
	m[exp.ApproxTopK] = approxtopkSQL
	m[exp.StrPosition] = strpositionSQL
	m[exp.Substring] = substringSQL
	m[exp.StrToTime] = strtotimeSQL
	m[exp.StrToDate] = strtodateSQL
	m[exp.ParseDatetime] = parsedatetimeSQL
	m[exp.ParseTime] = parsetimeSQL
	m[exp.TsOrDsToTime] = tsordstotimeSQL
	m[exp.CurrentDate] = currentdateSQL
	m[exp.CheckJson] = checkjsonSQL
	m[exp.ParseJSON] = parsejsonSQL
	m[exp.Unicode] = unicodeSQL
	m[exp.StripNullValue] = stripnullvalueSQL
	m[exp.Trunc] = truncSQL
	m[exp.Normal] = normalSQL
	m[exp.Uniform] = uniformSQL
	m[exp.TimeFromParts] = timefrompartsSQL
	m[exp.Extract] = extractSQL(sup(exp.Extract))
	m[exp.TimestampFromParts] = timestampfrompartsSQL
	m[exp.TimestampLtzFromParts] = timestampltzfrompartsSQL
	m[exp.TimestampTzFromParts] = timestamptzfrompartsSQL
	c.Methods.AfterLimitModifiers = afterLimitModifiers
	c.Methods.TablesampleSQL = tablesampleSQL(c.Methods.TablesampleSQL)
	m[exp.In] = inSQL(sup(exp.In))
	m[exp.Interval] = intervalSQL(sup(exp.Interval))
	c.Methods.JoinSQL = joinSQL(c.Methods.JoinSQL)
	m[exp.CountIf] = countifSQL
	m[exp.Bracket] = bracketSQL(sup(exp.Bracket))
	m[exp.WithinGroup] = withingroupSQL
	m[exp.Length] = lengthSQL
	m[exp.BitLength] = bitlengthSQL
	c.Methods.ChrSQL = chrSQL
	m[exp.Collation] = unsupportedFallback("COLLATION function is not supported by DuckDB")
	m[exp.Collate] = collateSQL(sup(exp.Collate))
	m[exp.RegexpCount] = regexpcountSQL
	m[exp.RegexpReplace] = regexpreplaceSQL
	m[exp.RegexpLike] = regexplikeSQL
	m[exp.Levenshtein] = levenshteinSQL
	m[exp.Pad] = padSQL(sup(exp.Pad))
	m[exp.Minhash] = minhashSQL
	m[exp.MinhashCombine] = minhashcombineSQL
	m[exp.ApproximateSimilarity] = approximatesimilaritySQL
	m[exp.ArrayUniqueAgg] = arrayuniqueaggSQL
	m[exp.ArrayConcatAgg] = arrayconcataggSQL
	m[exp.ArrayUnionAgg] = unsupportedFallback("ARRAY_UNION_AGG is not supported in DuckDB")
	m[exp.ArrayDistinct] = arraydistinctSQL
	m[exp.ArrayIntersect] = arrayintersectSQL
	m[exp.ArrayExcept] = arrayexceptSQL
	m[exp.ArraySlice] = arraysliceSQL
	m[exp.ArraysZip] = arrayszipSQL
	m[exp.Lower] = lowerSQL
	m[exp.Upper] = upperSQL
	m[exp.Reverse] = reverseSQL
	m[exp.Left] = func(g *gen, e *exp.Expr) string { return leftRightSQL(g, e, "LEFT") }
	m[exp.Right] = func(g *gen, e *exp.Expr) string { return leftRightSQL(g, e, "RIGHT") }
	m[exp.RtrimmedLength] = rtrimmedlengthSQL
	m[exp.Stuff] = stuffSQL
	m[exp.Rand] = randSQL
	m[exp.ByteLength] = bytelengthSQL
	m[exp.Base64Encode] = base64encodeSQL
	m[exp.Hex] = hexSQL
	m[exp.Replace] = replaceSQL
	m[exp.BitwiseXor] = bitwisexorSQL
	m[exp.ObjectInsert] = objectinsertSQL
	m[exp.MapCat] = mapcatSQL
	m[exp.MapContainsKey] = mapcontainskeySQL
	m[exp.MapDelete] = mapdeleteSQL
	m[exp.MapPick] = mappickSQL
	m[exp.MapSize] = mapsizeSQL
	m[exp.MapInsert] = mapinsertSQL
	m[exp.StartsWith] = startswithSQL
	m[exp.Space] = spaceSQL
	m[exp.TableFromRows] = tablefromrowsSQL(sup(exp.TableFromRows))
	m[exp.Unnest] = unnestSQL(sup(exp.Unnest))
	m[exp.ArrayAgg] = arrayaggSQL(sup(exp.ArrayAgg))
	m[exp.IgnoreNulls] = ignorenullsSQL(sup(exp.IgnoreNulls))
	m[exp.Split] = splitSQL
	m[exp.SplitPart] = splitpartSQL
	m[exp.RespectNulls] = respectnullsSQL(sup(exp.RespectNulls))
	m[exp.ArrayToString] = arraytostringSQL
	m[exp.ConcatWs] = concatwsSQL(sup(exp.ConcatWs))
	m[exp.RegexpExtract] = regexpExtractSQL
	m[exp.RegexpExtractAll] = regexpExtractSQL
	m[exp.RegexpInstr] = regexpinstrSQL
	m[exp.NumberToStr] = numbertostrSQL
	m[exp.AutoIncrementColumnConstraint] = autoincrementcolumnconstraintSQL
	m[exp.Aliases] = aliasesSQL(sup(exp.Aliases))
	m[exp.Posexplode] = posexplodeSQL
	m[exp.AddMonths] = addmonthsSQL
	m[exp.Format] = formatSQL
	superHexstring := c.Methods.HexstringSQL
	c.Methods.HexstringSQL = func(g *gen, e *exp.Expr, binaryFunctionRepr string) string {
		// UNHEX('FF') correctly produces blob \xFF in DuckDB
		return superHexstring(g, e, "UNHEX")
	}
	m[exp.DateTrunc] = datetruncSQL
	m[exp.DatetimeTrunc] = datetimetruncSQL
	m[exp.TimestampTrunc] = timestamptruncSQL
	m[exp.Trim] = trimSQL(sup(exp.Trim))
	m[exp.Round] = roundSQL
	m[exp.TryCast] = trycastSQL(sup(exp.TryCast))
	m[exp.Strtok] = strtokSQL
	m[exp.StrtokToArray] = strtoktoarraySQL
	m[exp.ApproxQuantile] = approxquantileSQL
	m[exp.ApproxQuantiles] = approxquantilesSQL
	m[exp.JSONExtractScalar] = jsonextractscalarSQL
	m[exp.BitwiseNot] = bitwisenotSQL
	m[exp.Window] = windowSQL(superWindow, superFilter)
	m[exp.Filter] = filterSQL(superWindow, superFilter)
	m[exp.Uuid] = uuidSQL(sup(exp.Uuid))

	c.Transforms[exp.Corr] = func(g *gen, e *exp.Expr) string { return corrSQLWith(g, e, superWindow, superFilter) }
}
