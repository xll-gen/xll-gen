package generator

// TypeInfo holds the code generation properties for a given type.
type TypeInfo struct {
	SchemaType      string
	GoType          string
	CppType         string
	ArgCppType      string
	XllType         string
	ArgXllType      string
	DefaultErrorVal string
}

// typeRegistry serves as the central source of truth for type properties.
var typeRegistry = map[string]TypeInfo{
	"int": {
		SchemaType:      "int",
		GoType:          "int32",
		CppType:         "LPXLOPER12",
		ArgCppType:      "int32_t",
		XllType:         "Q",
		ArgXllType:      "J",
		DefaultErrorVal: "&g_xlErrValue",
	},
	"float": {
		SchemaType:      "double",
		GoType:          "float64",
		CppType:         "LPXLOPER12",
		ArgCppType:      "double",
		XllType:         "Q",
		ArgXllType:      "B",
		DefaultErrorVal: "&g_xlErrValue",
	},
	"string": {
		SchemaType:      "string",
		GoType:          "string",
		CppType:         "LPXLOPER12",
		ArgCppType:      "LPXLOPER12",
		XllType:         "Q",
		ArgXllType:      "Q",
		DefaultErrorVal: "&g_xlErrValue",
	},
	"bool": {
		SchemaType:      "bool",
		GoType:          "bool",
		CppType:         "LPXLOPER12",
		ArgCppType:      "short",
		XllType:         "Q",
		ArgXllType:      "A",
		DefaultErrorVal: "&g_xlErrValue",
	},
	"range": {
		SchemaType:      "protocol.Range",
		GoType:          "*protocol.Range",
		CppType:         "LPXLOPER12",
		ArgCppType:      "LPXLOPER12",
		XllType:         "U",
		ArgXllType:      "U",
		DefaultErrorVal: "&g_xlErrValue",
	},
	"grid": {
		SchemaType:      "protocol.Grid",
		GoType:          "*protocol.Grid",
		CppType:         "LPXLOPER12",
		ArgCppType:      "LPXLOPER12",
		XllType:         "U",
		ArgXllType:      "U",
		DefaultErrorVal: "&g_xlErrValue",
	},
	"numgrid": {
		SchemaType:      "protocol.NumGrid",
		GoType:          "*protocol.NumGrid",
		CppType:         "FP12*",
		ArgCppType:      "FP12*",
		XllType:         "K%",
		ArgXllType:      "K%",
		DefaultErrorVal: "0",
	},
	"any": {
		SchemaType:      "protocol.Any",
		GoType:          "*protocol.Any",
		CppType:         "LPXLOPER12",
		ArgCppType:      "LPXLOPER12",
		XllType:         "U",
		ArgXllType:      "U",
		DefaultErrorVal: "&g_xlErrValue",
	},
}

// LookupSchemaType returns the FlatBuffers schema type for the given xll.yaml type.
func LookupSchemaType(t string) string {
	if info, ok := typeRegistry[t]; ok && info.SchemaType != "" {
		return info.SchemaType
	}
	return t
}

// LookupGoType returns the Go type for the given xll.yaml type.
func LookupGoType(t string) string {
	if info, ok := typeRegistry[t]; ok && info.GoType != "" {
		return info.GoType
	}
	return t
}

// LookupCppType returns the C++ type for the given xll.yaml type (used for returns).
func LookupCppType(t string) string {
	if info, ok := typeRegistry[t]; ok && info.CppType != "" {
		return info.CppType
	}
	return t
}

// LookupArgCppType returns the C++ type for the given xll.yaml type (used for arguments).
func LookupArgCppType(t string) string {
	if info, ok := typeRegistry[t]; ok && info.ArgCppType != "" {
		return info.ArgCppType
	}
	return t
}

// LookupXllType returns the XLL registration code for the given xll.yaml type (used for returns).
func LookupXllType(t string) string {
	if info, ok := typeRegistry[t]; ok && info.XllType != "" {
		return info.XllType
	}
	return t
}

// LookupArgXllType returns the XLL registration code for the given xll.yaml type (used for arguments).
func LookupArgXllType(t string) string {
	if info, ok := typeRegistry[t]; ok && info.ArgXllType != "" {
		return info.ArgXllType
	}
	return t
}

// DefaultErrorVal returns the default C++ error value for the given xll.yaml type.
func DefaultErrorVal(t string) string {
	if info, ok := typeRegistry[t]; ok && info.DefaultErrorVal != "" {
		return info.DefaultErrorVal
	}
	// Fallback logic from original gen_cpp.go: return "0" if not matched.
	return "0"
}
