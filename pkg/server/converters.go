package server

import (
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
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

func CreateScalarAny(b *flatbuffers.Builder, val ScalarValue) flatbuffers.UOffsetT {
	var uOff flatbuffers.UOffsetT
	switch val.Type {
	case protocol.AnyValueInt:
		protocol.IntStart(b)
		protocol.IntAddVal(b, val.Int)
		uOff = protocol.IntEnd(b)
	case protocol.AnyValueNum:
		protocol.NumStart(b)
		protocol.NumAddVal(b, val.Num)
		uOff = protocol.NumEnd(b)
	case protocol.AnyValueBool:
		protocol.BoolStart(b)
		protocol.BoolAddVal(b, val.Bool)
		uOff = protocol.BoolEnd(b)
	case protocol.AnyValueStr:
		sOff := b.CreateString(val.Str)
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		uOff = protocol.StrEnd(b)
	case protocol.AnyValueErr:
		protocol.ErrStart(b)
		protocol.ErrAddVal(b, protocol.XlError(val.Err))
		uOff = protocol.ErrEnd(b)
	}

	protocol.AnyStart(b)
	protocol.AnyAddValType(b, val.Type)
	protocol.AnyAddVal(b, uOff)
	return protocol.AnyEnd(b)
}
