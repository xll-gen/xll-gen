package server

import (
	"strconv"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/internal/fbany"
	"github.com/xll-gen/xll-gen/pkg/log"
)

func ToScalar(v *protocol.Any) (ScalarValue, bool) {
	if v == nil {
		return ScalarValue{}, false
	}
	var tbl flatbuffers.Table
	if !v.Val(&tbl) {
		return ScalarValue{}, false
	}

	switch v.ValType() {
	case protocol.AnyValueInt:
		var t protocol.Int
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueInt, Int: t.Val()}, true
	case protocol.AnyValueNum:
		var t protocol.Num
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueNum, Num: t.Val()}, true
	case protocol.AnyValueBool:
		var t protocol.Bool
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueBool, Bool: t.Val()}, true
	case protocol.AnyValueStr:
		var t protocol.Str
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueStr, Str: string(t.Val())}, true
	case protocol.AnyValueErr:
		var t protocol.Err
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueErr, Err: int16(t.Val())}, true
	}
	return ScalarValue{}, false
}

// ParseInt parses an RTD topic argument as an int32. On a malformed value it
// returns 0 and logs a warning rather than silently swallowing the error (the
// old fmt.Sscanf path discarded the error, so "abc" became 0 with no trace).
// Signature is preserved (callers in the RTD path expect a bare int32).
func ParseInt(s string) int32 {
	v, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		log.Warn("RTD arg: invalid integer, defaulting to 0", "value", s, "error", err)
		return 0
	}
	return int32(v)
}

// ParseFloat parses an RTD topic argument as a float64. On a malformed value
// it returns 0 and logs a warning (see ParseInt). Signature is preserved.
func ParseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		log.Warn("RTD arg: invalid float, defaulting to 0", "value", s, "error", err)
		return 0
	}
	return v
}

func ParseBool(s string) bool {
	return s == "TRUE" || s == "1" || s == "true"
}

// MapAnyValue maps an arbitrary Go value onto a protocol.Any union tag plus
// the payload the Any builder expects for that tag (nil → Nil, string → Str,
// int32 → Int, other ints/floats → Num, bool → Bool, time.Time → RFC3339 Str,
// everything else → fmt-formatted Str). Used by generated code for
// `return: "any"` async results, where the tag is needed at queue time.
func MapAnyValue(v any) (AnyValue, any) {
	return fbany.MapGo(v)
}

// BuildAnyFromGo serializes an arbitrary Go value into a protocol.Any table
// using the MapAnyValue mapping, returning the Any offset. Used by generated
// code for `return: "any"` sync responses; the same mapping backs RTD
// updates, so a value renders identically in a cell from either path.
func BuildAnyFromGo(b *flatbuffers.Builder, v any) flatbuffers.UOffsetT {
	return fbany.BuildGo(b, v)
}

// CreateScalarAny serializes a ScalarValue into a protocol.Any table via the
// canonical internal/fbany builder, returning the Any offset.
func CreateScalarAny(b *flatbuffers.Builder, val ScalarValue) flatbuffers.UOffsetT {
	switch val.Type {
	case protocol.AnyValueInt:
		return fbany.Build(b, val.Type, val.Int)
	case protocol.AnyValueNum:
		return fbany.Build(b, val.Type, val.Num)
	case protocol.AnyValueBool:
		return fbany.Build(b, val.Type, val.Bool)
	case protocol.AnyValueStr:
		return fbany.Build(b, val.Type, val.Str)
	case protocol.AnyValueErr:
		return fbany.Build(b, val.Type, val.Err)
	}
	// Tags outside the five scalar kinds (incl. the AnyValueNONE zero value)
	// historically produced an Any with an empty union member. ToScalar never
	// constructs such a ScalarValue; preserved for byte-identity.
	return fbany.BuildEmpty(b, val.Type)
}
