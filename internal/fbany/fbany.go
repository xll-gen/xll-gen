// Package fbany is the single canonical builder for protocol.Any FlatBuffers
// unions. It exists as a leaf package (depending only on flatbuffers and the
// external types/go/protocol module) because the scalar→Any building logic
// was previously triplicated across pkg/server/converters.go,
// pkg/server/async_batcher.go and pkg/rtd/manager.go, and pkg/rtd cannot
// import pkg/server (pkg/server/handlers.go imports pkg/rtd — an import
// cycle). Long-term this builder is a candidate for migration to the
// xll-gen/types repo (see IMPROVEMENT_BACKLOG.md R1); keeping it internal
// makes that migration non-breaking.
package fbany

import (
	"fmt"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

// Build constructs the union member table selected by tag from val and wraps
// it in a protocol.Any table, returning the Any offset.
//
// Expected dynamic types per tag (a mismatch panics, matching the historical
// inline type assertions in the async batcher):
//
//	AnyValueInt  → int32
//	AnyValueNum  → float64
//	AnyValueBool → bool
//	AnyValueStr  → string
//	AnyValueErr  → int16
//	AnyValueNil  → val ignored
//
// Any other tag (including AnyValueNONE) produces a well-formed Any whose
// val_type is forced to AnyValueNONE and whose union member is empty (offset
// 0). The legacy inline switches left uOff at zero for unhandled tags but kept
// the original (non-scalar) tag; that produced an Any advertising a union kind
// with no backing table — a corrupt union the C++ reader would dereference.
// Forcing NONE here is the deliberate "no value" outcome (see
// IMPROVEMENT_BACKLOG.md §2). For the six handled tags the output is unchanged.
func Build(b *flatbuffers.Builder, tag protocol.AnyValue, val any) flatbuffers.UOffsetT {
	var uOff flatbuffers.UOffsetT
	switch tag {
	case protocol.AnyValueInt:
		protocol.IntStart(b)
		protocol.IntAddVal(b, val.(int32))
		uOff = protocol.IntEnd(b)
	case protocol.AnyValueNum:
		protocol.NumStart(b)
		protocol.NumAddVal(b, val.(float64))
		uOff = protocol.NumEnd(b)
	case protocol.AnyValueBool:
		protocol.BoolStart(b)
		protocol.BoolAddVal(b, val.(bool))
		uOff = protocol.BoolEnd(b)
	case protocol.AnyValueStr:
		sOff := b.CreateString(val.(string))
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		uOff = protocol.StrEnd(b)
	case protocol.AnyValueErr:
		protocol.ErrStart(b)
		protocol.ErrAddVal(b, protocol.XlError(val.(int16)))
		uOff = protocol.ErrEnd(b)
	case protocol.AnyValueNil:
		protocol.NilStart(b)
		uOff = protocol.NilEnd(b)
	default:
		// Unknown/unhandled tag (including AnyValueNONE). We deliberately do
		// NOT attempt to build a union member for an unrecognized tag — doing
		// so would require an arbitrary type assertion on val and could panic
		// or, worse, serialize a union whose val_type does not match the
		// table it points at (a corrupt union the C++ side would misread).
		// Instead emit val_type=NONE with an empty (offset 0) member. This is
		// a well-formed Any the reader treats as "no value", not corruption.
		// See IMPROVEMENT_BACKLOG.md §2 / §3 (default branch on the ValType
		// switch). uOff stays 0; tag is forced to NONE below.
		tag = protocol.AnyValueNONE
	}

	protocol.AnyStart(b)
	protocol.AnyAddValType(b, tag)
	protocol.AnyAddVal(b, uOff)
	return protocol.AnyEnd(b)
}

// MapGo maps an arbitrary Go value onto a protocol.Any union tag plus the
// payload Build expects for that tag. This is the canonical Go-value→Any
// mapping shared by the RTD update path and the generated sync/async
// `return: "any"` paths, so a value renders identically in a cell no matter
// which route delivered it:
//
//	nil          → AnyValueNil  (empty cell)
//	string       → AnyValueStr
//	int32        → AnyValueInt
//	int, int64   → AnyValueNum  (Go ints can exceed 32 bits; double keeps 53)
//	float64/32   → AnyValueNum
//	bool         → AnyValueBool
//	time.Time    → AnyValueStr  (RFC3339)
//	anything else → AnyValueStr via fmt.Sprintf("%v", v)
func MapGo(value any) (protocol.AnyValue, any) {
	switch v := value.(type) {
	case nil:
		return protocol.AnyValueNil, nil
	case string:
		return protocol.AnyValueStr, v
	case int:
		// Go int can be 64-bit, so send as double to prevent truncation
		return protocol.AnyValueNum, float64(v)
	case int32:
		return protocol.AnyValueInt, v
	case int64:
		// Protocol only supports 32-bit int, so we send as double to preserve value (up to 53 bits)
		return protocol.AnyValueNum, float64(v)
	case float64:
		return protocol.AnyValueNum, v
	case float32:
		return protocol.AnyValueNum, float64(v)
	case bool:
		return protocol.AnyValueBool, v
	case time.Time:
		return protocol.AnyValueStr, v.Format(time.RFC3339)
	default:
		return protocol.AnyValueStr, fmt.Sprintf("%v", v)
	}
}

// BuildGo maps value through MapGo and serializes it with Build, returning
// the protocol.Any offset. Convenience for callers that don't need the tag.
func BuildGo(b *flatbuffers.Builder, value any) flatbuffers.UOffsetT {
	tag, payload := MapGo(value)
	return Build(b, tag, payload)
}

// BuildEmpty wraps an empty union member (offset 0) in a protocol.Any table
// carrying the given tag. This preserves the legacy behavior of
// server.CreateScalarAny for tags outside the five scalar kinds — notably
// AnyValueNil, for which Build would emit a real Nil table instead.
func BuildEmpty(b *flatbuffers.Builder, tag protocol.AnyValue) flatbuffers.UOffsetT {
	protocol.AnyStart(b)
	protocol.AnyAddValType(b, tag)
	protocol.AnyAddVal(b, 0)
	return protocol.AnyEnd(b)
}
