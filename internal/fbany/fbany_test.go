package fbany

import (
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

// TestMapGo locks down the canonical Go-value→Any union mapping shared by
// the RTD update path and the generated sync/async `return: "any"` paths.
func TestMapGo(t *testing.T) {
	ts := time.Date(2026, 6, 11, 9, 30, 0, 0, time.UTC)
	cases := []struct {
		name        string
		in          any
		wantTag     protocol.AnyValue
		wantPayload any
	}{
		{"nil", nil, protocol.AnyValueNil, nil},
		{"string", "hello", protocol.AnyValueStr, "hello"},
		{"int", int(1 << 40), protocol.AnyValueNum, float64(1 << 40)},
		{"int8", int8(-7), protocol.AnyValueInt, int32(-7)},
		{"int16", int16(-300), protocol.AnyValueInt, int32(-300)},
		{"int32", int32(-42), protocol.AnyValueInt, int32(-42)},
		{"int64", int64(1<<53 - 1), protocol.AnyValueNum, float64(1<<53 - 1)},
		{"uint8", uint8(65), protocol.AnyValueInt, int32(65)},
		{"uint16", uint16(60000), protocol.AnyValueInt, int32(60000)},
		{"uint_small", uint(42), protocol.AnyValueInt, int32(42)},
		{"uint32_max", uint32(1<<32 - 1), protocol.AnyValueNum, float64(1<<32 - 1)},
		{"uint64_small", uint64(42), protocol.AnyValueInt, int32(42)},
		{"uint64_wide", uint64(1 << 40), protocol.AnyValueNum, float64(1 << 40)},
		{"float64", 3.14159, protocol.AnyValueNum, 3.14159},
		{"float32", float32(2.5), protocol.AnyValueNum, float64(2.5)},
		{"bool", true, protocol.AnyValueBool, true},
		{"time", ts, protocol.AnyValueDate, ts},
		{"default_fmt", struct{ X int }{X: 7}, protocol.AnyValueStr, "{7}"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tag, payload := MapGo(tc.in)
			if tag != tc.wantTag {
				t.Fatalf("tag = %v, want %v", tag, tc.wantTag)
			}
			if payload != tc.wantPayload {
				t.Fatalf("payload = %#v, want %#v", payload, tc.wantPayload)
			}
		})
	}
}

func TestMapGo_TimeIsDate(t *testing.T) {
	tag, payload := MapGo(time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC))
	if tag != protocol.AnyValueDate {
		t.Fatalf("tag = %v, want Date", tag)
	}
	if _, ok := payload.(time.Time); !ok {
		t.Fatalf("payload = %T, want time.Time", payload)
	}
}

func TestBuildGo_DateSerial(t *testing.T) {
	b := flatbuffers.NewBuilder(0)
	off := BuildGo(b, time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC))
	b.Finish(off)
	a := protocol.GetRootAsAny(b.FinishedBytes(), 0)
	if a.ValType() != protocol.AnyValueDate {
		t.Fatalf("val_type = %v, want Date", a.ValType())
	}
	// The generated Go protocol API exposes union members via Val(*Table) +
	// Init, not a ValAs<Type>() helper, so read the Date that way.
	var tbl flatbuffers.Table
	if !a.Val(&tbl) {
		t.Fatal("union member missing")
	}
	var d protocol.Date
	d.Init(tbl.Bytes, tbl.Pos)
	if d.Serial() != 46188 {
		t.Fatalf("serial = %v, want 46188", d.Serial())
	}
}

func TestBuildGrid_TimeCellIsDate(t *testing.T) {
	b := flatbuffers.NewBuilder(0)
	off, err := BuildGrid(b, [][]any{{time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), 1.5}})
	if err != nil {
		t.Fatal(err)
	}
	b.Finish(off)
	g := protocol.GetRootAsGrid(b.FinishedBytes(), 0)
	var c0 protocol.Scalar
	g.Data(&c0, 0)
	if c0.ValType() != protocol.ScalarValueDate {
		t.Fatalf("cell0 val_type = %v, want Date", c0.ValType())
	}
}

// readAny finishes the builder on the given Any offset and re-reads it.
func readAny(b *flatbuffers.Builder, off flatbuffers.UOffsetT) *protocol.Any {
	b.Finish(off)
	return protocol.GetRootAsAny(b.FinishedBytes(), 0)
}

// TestBuildGo round-trips representative values through BuildGo and the
// protocol.Any reader, proving MapGo's payloads satisfy Build's expected
// dynamic types (a mismatch panics inside Build).
func TestBuildGo(t *testing.T) {
	t.Run("nil_is_Nil_union", func(t *testing.T) {
		b := flatbuffers.NewBuilder(64)
		anyVal := readAny(b, BuildGo(b, nil))
		if got := anyVal.ValType(); got != protocol.AnyValueNil {
			t.Fatalf("ValType = %v, want Nil", got)
		}
	})

	t.Run("int_survives_as_double", func(t *testing.T) {
		b := flatbuffers.NewBuilder(64)
		anyVal := readAny(b, BuildGo(b, int(1<<40)))
		if got := anyVal.ValType(); got != protocol.AnyValueNum {
			t.Fatalf("ValType = %v, want Num", got)
		}
		var tbl flatbuffers.Table
		if !anyVal.Val(&tbl) {
			t.Fatal("union member missing")
		}
		var num protocol.Num
		num.Init(tbl.Bytes, tbl.Pos)
		if num.Val() != float64(1<<40) {
			t.Fatalf("Num.Val = %v, want %v", num.Val(), float64(1<<40))
		}
	})

	t.Run("string", func(t *testing.T) {
		b := flatbuffers.NewBuilder(64)
		anyVal := readAny(b, BuildGo(b, "hi"))
		if got := anyVal.ValType(); got != protocol.AnyValueStr {
			t.Fatalf("ValType = %v, want Str", got)
		}
		var tbl flatbuffers.Table
		if !anyVal.Val(&tbl) {
			t.Fatal("union member missing")
		}
		var s protocol.Str
		s.Init(tbl.Bytes, tbl.Pos)
		if string(s.Val()) != "hi" {
			t.Fatalf("Str.Val = %q, want %q", s.Val(), "hi")
		}
	})
}
