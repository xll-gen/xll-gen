package generator

// TypeInfo holds the code generation properties for a given type.
type TypeInfo struct {
	SchemaType string
	GoType     string
	// RetGoType is the Go type handlers RETURN for this xll.yaml type when it
	// differs from GoType (the argument-position type). FlatBuffers read views
	// like *protocol.Any make sense as arguments but cannot be constructed by
	// a handler, so e.g. "any" is received as *protocol.Any but returned as a
	// plain Go any that the generated code serializes (see fbany.MapGo).
	// Empty means "same as GoType".
	RetGoType  string
	CppType    string
	ArgCppType string
	XllType    string
	ArgXllType string
}

// typeRegistry serves as the central source of truth for type properties.
var typeRegistry = map[string]TypeInfo{
	"int": {
		SchemaType:      "int",
		GoType:          "int32",
		CppType:         "LPXLOPER12",
		ArgCppType:      "int32_t",
		XllType:         "Q",
		ArgXllType:      "J",	},
	"float": {
		SchemaType:      "double",
		GoType:          "float64",
		CppType:         "LPXLOPER12",
		ArgCppType:      "double",
		XllType:         "Q",
		ArgXllType:      "B",	},
	"string": {
		SchemaType:      "string",
		GoType:          "string",
		CppType:         "LPXLOPER12",
		ArgCppType:      "LPXLOPER12",
		XllType:         "Q",
		ArgXllType:      "Q",	},
	"bool": {
		SchemaType:      "bool",
		GoType:          "bool",
		CppType:         "LPXLOPER12",
		ArgCppType:      "short",
		XllType:         "Q",
		ArgXllType:      "A",	},
	// XllType (return position) is always "Q": wrappers return value XLOPER12s
	// (xltypeMulti/xltypeStr/...), never live range references. "U" is only
	// meaningful in argument position (pass-by-reference); as a return code it
	// breaks the registration — Excel accepts the xlfRegister call but the
	// worksheet name resolves to #NAME?. See AGENTS.md §19.2.
	"range": {
		SchemaType:      "protocol.Range",
		GoType:          "*protocol.Range",
		CppType:         "LPXLOPER12",
		ArgCppType:      "LPXLOPER12",
		XllType:         "Q",
		ArgXllType:      "U",	},
	// grid: ARGUMENT is the *protocol.Grid read view; RETURN is a plain Go
	// [][]any the handler builds (serialized via pkg/server.BuildGridFromGo).
	// Return code "Q" (LPXLOPER12 → xltypeMulti) spills in dynamic-array Excel.
	"grid": {
		SchemaType:      "protocol.Grid",
		GoType:          "*protocol.Grid",
		RetGoType:       "[][]any",
		CppType:         "LPXLOPER12",
		ArgCppType:      "LPXLOPER12",
		XllType:         "Q",
		ArgXllType:      "U",	},
	// numgrid: ARGUMENT is *protocol.NumGrid; RETURN is [][]float64
	// (serialized via pkg/server.BuildNumGridFromGo). Return code "K%" (FP12)
	// also spills in dynamic-array Excel.
	"numgrid": {
		SchemaType:      "protocol.NumGrid",
		GoType:          "*protocol.NumGrid",
		RetGoType:       "[][]float64",
		CppType:         "FP12*",
		ArgCppType:      "FP12*",
		XllType:         "K%",
		ArgXllType:      "K%",	},
	// date: rides the EXISTING double request path — a date ARGUMENT is sent as
	// a double (Excel serial) and decoded back to a time.Time in the generated
	// server via server.SerialToTime(...). RetGoType is time.Time for forward
	// compatibility, but only the ARGUMENT position is wired today (config
	// validation rejects a date RETURN — the response path does not yet encode
	// time.Time back to a serial double).
	"date": {
		SchemaType: "double",
		GoType:     "time.Time",
		RetGoType:  "time.Time",
		CppType:    "LPXLOPER12",
		ArgCppType: "double",
		XllType:    "Q",
		ArgXllType: "B",
	},
	"any": {
		SchemaType:      "protocol.Any",
		GoType:          "*protocol.Any",
		RetGoType:       "any",
		CppType:         "LPXLOPER12",
		ArgCppType:      "LPXLOPER12",
		XllType:         "Q",
		ArgXllType:      "U",	},
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

// LookupRetGoType returns the Go type a handler returns for the given
// xll.yaml type. Falls back to LookupGoType when no return-specific type is
// registered.
func LookupRetGoType(t string) string {
	if info, ok := typeRegistry[t]; ok && info.RetGoType != "" {
		return info.RetGoType
	}
	return LookupGoType(t)
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

