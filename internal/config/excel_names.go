package config

import "strings"

// This file carries the two name-collision blocklists used by Validate for
// function/command names. Background (2026-06-12, found via the showcase):
//
//   - A name matching an Excel 4.0 (XLM) macro keyword (e.g. ECHO, BEEP) is
//     rejected by Excel OUTRIGHT in worksheet formulas — typing =Echo(...)
//     fails to enter, and setting it via COM raises 0x800A03EC. Registration
//     via xlfRegister may even "succeed", which makes the failure mode
//     maximally confusing.
//   - A name matching a BUILT-IN worksheet function (e.g. ISEVEN) is silently
//     shadowed: =IsEven(10) resolves to the built-in and the XLL handler is
//     never called. No error anywhere.
//
// Both are config-time errors: there is no way to make such a registration
// work, and the failure is otherwise invisible until a user stares at a cell.

// xlmReservedNamesList holds Excel 4.0 (XLM) macro keywords that Excel
// rejects in worksheet formulas. Every entry was empirically verified on
// real Excel (2026-06-12, hidden-instance probe: setting `=<NAME>(1)` via
// COM raises 0x800A03EC, whereas unknown names simply evaluate to #NAME?).
// Names with dots (FILE.CLOSE, GET.CELL, ...) cannot appear here because
// they are not valid identifiers for generated C++ anyway. The list is not
// guaranteed exhaustive — extend it with verified entries only.
var xlmReservedNamesList = []string{
	"ABSREF", "ALERT", "ARGUMENT", "BEEP", "BREAK", "CALLER", "CLOSE",
	"COPY", "CUT", "DEREF", "DIRECTORY", "DUPLICATE", "ECHO", "EXEC",
	"FCLOSE", "FOPEN", "FPOS", "FREAD", "FREADLN", "FSIZE", "FWRITE",
	"FWRITELN", "GOTO", "HALT", "HELP", "HIDE", "HLINE", "HPAGE",
	"HSCROLL", "INPUT", "LEGEND", "MESSAGE", "MOVE", "NEW", "NOTE",
	"OPEN", "PASTE", "PRECISION", "PRINT", "QUIT", "REFTEXT", "REGISTER",
	"RELREF", "RESTART", "RESULT", "RETURN", "RUN", "SAVE", "SELECT",
	"SELECTION", "SIZE", "SORT", "SPLIT", "STEP", "TABLE", "TEXTREF",
	"UNDO", "UNHIDE", "UNREGISTER", "VLINE", "VOLATILE", "VPAGE",
	"VSCROLL", "WAIT", "WHILE",
}

// builtinFunctionNamesList holds current built-in worksheet function names
// (Excel 2016 through Microsoft 365, including dynamic-array and lambda-era
// functions). A built-in silently shadows an XLL registration of the same
// name, so these are rejected for `functions` entries. Dotted names are
// included for completeness even though identifier validation would catch
// them anyway. Curated from the official function reference; extend freely —
// a missing entry only weakens the guard, while every present entry is a
// genuine collision on some supported Excel version.
var builtinFunctionNamesList = []string{
	// Math & trigonometry
	"ABS", "ACOS", "ACOSH", "ACOT", "ACOTH", "AGGREGATE", "ARABIC", "ASIN",
	"ASINH", "ATAN", "ATAN2", "ATANH", "BASE", "CEILING", "CEILING.MATH",
	"CEILING.PRECISE", "COMBIN", "COMBINA", "COS", "COSH", "COT", "COTH",
	"CSC", "CSCH", "DECIMAL", "DEGREES", "EVEN", "EXP", "FACT", "FACTDOUBLE",
	"FLOOR", "FLOOR.MATH", "FLOOR.PRECISE", "GCD", "INT", "ISO.CEILING",
	"LCM", "LN", "LOG", "LOG10", "MDETERM", "MINVERSE", "MMULT", "MOD",
	"MROUND", "MULTINOMIAL", "MUNIT", "ODD", "PI", "POWER", "PRODUCT",
	"QUOTIENT", "RADIANS", "RAND", "RANDARRAY", "RANDBETWEEN", "ROMAN",
	"ROUND", "ROUNDDOWN", "ROUNDUP", "SEC", "SECH", "SEQUENCE", "SERIESSUM",
	"SIGN", "SIN", "SINH", "SQRT", "SQRTPI", "SUBTOTAL", "SUM", "SUMIF",
	"SUMIFS", "SUMPRODUCT", "SUMSQ", "SUMX2MY2", "SUMX2PY2", "SUMXMY2",
	"TAN", "TANH", "TRUNC",
	// Statistical
	"AVEDEV", "AVERAGE", "AVERAGEA", "AVERAGEIF", "AVERAGEIFS", "BETA.DIST",
	"BETA.INV", "BETADIST", "BETAINV", "BINOM.DIST", "BINOM.DIST.RANGE",
	"BINOM.INV", "BINOMDIST", "CHIDIST", "CHIINV", "CHISQ.DIST",
	"CHISQ.DIST.RT", "CHISQ.INV", "CHISQ.INV.RT", "CHISQ.TEST", "CHITEST",
	"CONFIDENCE", "CONFIDENCE.NORM", "CONFIDENCE.T", "CORREL", "COUNT",
	"COUNTA", "COUNTBLANK", "COUNTIF", "COUNTIFS", "COVAR", "COVARIANCE.P",
	"COVARIANCE.S", "CRITBINOM", "DEVSQ", "EXPON.DIST", "EXPONDIST",
	"F.DIST", "F.DIST.RT", "F.INV", "F.INV.RT", "F.TEST", "FDIST", "FINV",
	"FISHER", "FISHERINV", "FORECAST", "FORECAST.ETS",
	"FORECAST.ETS.CONFINT", "FORECAST.ETS.SEASONALITY", "FORECAST.ETS.STAT",
	"FORECAST.LINEAR", "FREQUENCY", "FTEST", "GAMMA", "GAMMA.DIST",
	"GAMMA.INV", "GAMMADIST", "GAMMAINV", "GAMMALN", "GAMMALN.PRECISE",
	"GAUSS", "GEOMEAN", "GROWTH", "HARMEAN", "HYPGEOM.DIST", "HYPGEOMDIST",
	"INTERCEPT", "KURT", "LARGE", "LINEST", "LOGEST", "LOGINV",
	"LOGNORM.DIST", "LOGNORM.INV", "LOGNORMDIST", "MAX", "MAXA", "MAXIFS",
	"MEDIAN", "MIN", "MINA", "MINIFS", "MODE", "MODE.MULT", "MODE.SNGL",
	"NEGBINOM.DIST", "NEGBINOMDIST", "NORM.DIST", "NORM.INV", "NORM.S.DIST",
	"NORM.S.INV", "NORMDIST", "NORMINV", "NORMSDIST", "NORMSINV", "PEARSON",
	"PERCENTILE", "PERCENTILE.EXC", "PERCENTILE.INC", "PERCENTRANK",
	"PERCENTRANK.EXC", "PERCENTRANK.INC", "PERMUT", "PERMUTATIONA", "PHI",
	"POISSON", "POISSON.DIST", "PROB", "QUARTILE", "QUARTILE.EXC",
	"QUARTILE.INC", "RANK", "RANK.AVG", "RANK.EQ", "RSQ", "SKEW", "SKEW.P",
	"SLOPE", "SMALL", "STANDARDIZE", "STDEV", "STDEV.P", "STDEV.S",
	"STDEVA", "STDEVP", "STDEVPA", "STEYX", "T.DIST", "T.DIST.2T",
	"T.DIST.RT", "T.INV", "T.INV.2T", "T.TEST", "TDIST", "TINV", "TREND",
	"TRIMMEAN", "TTEST", "VAR", "VAR.P", "VAR.S", "VARA", "VARP", "VARPA",
	"WEIBULL", "WEIBULL.DIST", "Z.TEST", "ZTEST",
	// Text
	"ARRAYTOTEXT", "ASC", "BAHTTEXT", "CHAR", "CLEAN", "CODE", "CONCAT",
	"CONCATENATE", "DBCS", "DOLLAR", "EXACT", "FIND", "FINDB", "FIXED",
	"JIS", "LEFT", "LEFTB", "LEN", "LENB", "LOWER", "MID", "MIDB",
	"NUMBERVALUE", "PHONETIC", "PROPER", "REPLACE", "REPLACEB", "REPT",
	"RIGHT", "RIGHTB", "SEARCH", "SEARCHB", "SUBSTITUTE", "T", "TEXT",
	"TEXTAFTER", "TEXTBEFORE", "TEXTJOIN", "TEXTSPLIT", "TRIM", "UNICHAR",
	"UNICODE", "UPPER", "VALUE", "VALUETOTEXT",
	// Logical & lambda-era
	"AND", "BYCOL", "BYROW", "FALSE", "IF", "IFERROR", "IFNA", "IFS",
	"ISOMITTED", "LAMBDA", "LET", "MAKEARRAY", "MAP", "NOT", "OR",
	"REDUCE", "SCAN", "SWITCH", "TRUE", "XOR",
	// Lookup & reference
	"ADDRESS", "AREAS", "CHOOSE", "CHOOSECOLS", "CHOOSEROWS", "COLUMN",
	"COLUMNS", "DROP", "EXPAND", "FILTER", "FORMULATEXT", "GETPIVOTDATA",
	"GROUPBY", "HLOOKUP", "HSTACK", "HYPERLINK", "IMAGE", "INDEX",
	"INDIRECT", "LOOKUP", "MATCH", "OFFSET", "PIVOTBY", "ROW", "ROWS",
	"RTD", "SORTBY", "TAKE", "TOCOL", "TOROW", "TRANSPOSE", "UNIQUE",
	"VLOOKUP", "VSTACK", "WRAPCOLS", "WRAPROWS", "XLOOKUP", "XMATCH",
	// Date & time
	"DATE", "DATEDIF", "DATEVALUE", "DAY", "DAYS", "DAYS360", "EDATE",
	"EOMONTH", "HOUR", "ISOWEEKNUM", "MINUTE", "MONTH", "NETWORKDAYS",
	"NETWORKDAYS.INTL", "NOW", "SECOND", "TIME", "TIMEVALUE", "TODAY",
	"WEEKDAY", "WEEKNUM", "WORKDAY", "WORKDAY.INTL", "YEAR", "YEARFRAC",
	// Information
	"CELL", "ERROR.TYPE", "INFO", "ISBLANK", "ISERR", "ISERROR", "ISEVEN",
	"ISFORMULA", "ISLOGICAL", "ISNA", "ISNONTEXT", "ISNUMBER", "ISODD",
	"ISREF", "ISTEXT", "N", "NA", "SHEET", "SHEETS", "TYPE",
	// Database
	"DAVERAGE", "DCOUNT", "DCOUNTA", "DGET", "DMAX", "DMIN", "DPRODUCT",
	"DSTDEV", "DSTDEVP", "DSUM", "DVAR", "DVARP",
	// Financial
	"ACCRINT", "ACCRINTM", "AMORDEGRC", "AMORLINC", "COUPDAYBS", "COUPDAYS",
	"COUPDAYSNC", "COUPNCD", "COUPNUM", "COUPPCD", "CUMIPMT", "CUMPRINC",
	"DB", "DDB", "DISC", "DOLLARDE", "DOLLARFR", "DURATION", "EFFECT",
	"FV", "FVSCHEDULE", "INTRATE", "IPMT", "IRR", "ISPMT", "MDURATION",
	"MIRR", "NOMINAL", "NPER", "NPV", "ODDFPRICE", "ODDFYIELD", "ODDLPRICE",
	"ODDLYIELD", "PDURATION", "PMT", "PPMT", "PRICE", "PRICEDISC",
	"PRICEMAT", "PV", "RATE", "RECEIVED", "RRI", "SLN", "STOCKHISTORY",
	"SYD", "TBILLEQ", "TBILLPRICE", "TBILLYIELD", "VDB", "XIRR", "XNPV",
	"YIELD", "YIELDDISC", "YIELDMAT",
	// Engineering
	"BESSELI", "BESSELJ", "BESSELK", "BESSELY", "BIN2DEC", "BIN2HEX",
	"BIN2OCT", "BITAND", "BITLSHIFT", "BITOR", "BITRSHIFT", "BITXOR",
	"COMPLEX", "CONVERT", "DEC2BIN", "DEC2HEX", "DEC2OCT", "DELTA", "ERF",
	"ERF.PRECISE", "ERFC", "ERFC.PRECISE", "GESTEP", "HEX2BIN", "HEX2DEC",
	"HEX2OCT", "IMABS", "IMAGINARY", "IMARGUMENT", "IMCONJUGATE", "IMCOS",
	"IMCOSH", "IMCOT", "IMCSC", "IMCSCH", "IMDIV", "IMEXP", "IMLN",
	"IMLOG10", "IMLOG2", "IMPOWER", "IMPRODUCT", "IMREAL", "IMSEC",
	"IMSECH", "IMSIN", "IMSINH", "IMSQRT", "IMSUB", "IMSUM", "IMTAN",
	"OCT2BIN", "OCT2DEC", "OCT2HEX",
	// Web & cube
	"ENCODEURL", "FILTERXML", "WEBSERVICE", "CUBEKPIMEMBER", "CUBEMEMBER",
	"CUBEMEMBERPROPERTY", "CUBERANKEDMEMBER", "CUBESET", "CUBESETCOUNT",
	"CUBEVALUE",
}

var (
	xlmReservedNames     = toUpperSet(xlmReservedNamesList)
	builtinFunctionNames = toUpperSet(builtinFunctionNamesList)
)

func toUpperSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[strings.ToUpper(n)] = true
	}
	return m
}

// checkExcelNameCollision returns a non-empty diagnostic when the given
// function/command name collides with an XLM macro keyword or a built-in
// worksheet function. Comparison is case-insensitive (Excel names are).
func checkExcelNameCollision(name string) string {
	upper := strings.ToUpper(name)
	if xlmReservedNames[upper] {
		return "collides with the Excel 4.0 (XLM) macro keyword '" + upper +
			"' — Excel rejects worksheet formulas using this name outright " +
			"(manual entry fails; COM raises 0x800A03EC). Choose a different name"
	}
	if builtinFunctionNames[upper] {
		return "collides with the built-in Excel worksheet function '" + upper +
			"' — the built-in silently shadows the XLL registration and the " +
			"handler would never be called. Choose a different name"
	}
	return ""
}
