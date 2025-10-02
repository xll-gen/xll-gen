package xloper

import (
	"reflect"
	"testing"
	"unsafe"
)

func TestXLREF_BoundsCheck(t *testing.T) {
	testCases := []struct {
		name    string
		ref     XLREF
		wantErr error
	}{
		{"Valid", XLREF{0, 10, 0, 5}, nil},
		{"Valid Max", XLREF{0, 1048575, 0, 16383}, nil},
		{"Invalid Negative Row", XLREF{-1, 10, 0, 5}, ErrInvalid},
		{"Invalid Negative Col", XLREF{0, 10, -1, 5}, ErrInvalid},
		{"Invalid Out of Bounds Row", XLREF{0, 1048576, 0, 5}, ErrOutOfBounds},
		{"Invalid Out of Bounds Col", XLREF{0, 10, 0, 16384}, ErrOutOfBounds},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.BoundsCheck()
			if err != tc.wantErr {
				t.Errorf("Expected error %v, but got %v", tc.wantErr, err)
			}
		})
	}
}

func TestSref(t *testing.T) {
	ref := XLREF{RowFirst: 1, RowLast: 2, ColFirst: 3, ColLast: 4}

	t.Run("NewSref", func(t *testing.T) {
		s := NewSref(ref)
		if s == nil {
			t.Fatal("NewSref returned nil for valid input")
		}
		if s.Type() != TypeSRef {
			t.Errorf("Expected type %v, got %v", TypeSRef, s.Type())
		}
		if s.ref != ref {
			t.Errorf("Expected Ref() to return %v, got %v", ref, s.ref)
		}
		if !reflect.DeepEqual(s.Value(), s) {
			t.Errorf("Expected value %v, got %v", s, s.Value())
		}
	})

	t.Run("NewSref Invalid", func(t *testing.T) {
		s := NewSref(XLREF{-1, 2, 3, 4})
		if s != nil {
			t.Error("NewSref should return nil for invalid input")
		}
	})

	t.Run("SetSref", func(t *testing.T) {
		var a Any
		err := SetSref(&a, ref)
		if err != nil {
			t.Fatalf("SetSref failed: %v", err)
		}

		s := (*Sref)(unsafe.Pointer(&a))
		if s.Type() != TypeSRef {
			t.Errorf("Expected type %v, got %v", TypeSRef, s.Type())
		}
		if s.ref != ref {
			t.Errorf("Expected ref %v, got %v", ref, s.ref)
		}
	})

	t.Run("ViewSref", func(t *testing.T) {
		s := NewSref(ref)
		viewed, err := ViewSref(unsafe.Pointer(s))
		if err != nil {
			t.Fatalf("ViewSref failed: %v", err)
		}
		if viewed.ref != ref {
			t.Errorf("Expected ref %v, got %v", ref, viewed.ref)
		}
	})
}

func TestMref(t *testing.T) {
	var idSheet uintptr = 12345
	refs := []XLREF{
		{RowFirst: 1, RowLast: 2, ColFirst: 3, ColLast: 4},
		{RowFirst: 10, RowLast: 12, ColFirst: 13, ColLast: 14},
	}

	t.Run("NewMref", func(t *testing.T) {
		m := NewMref(idSheet, refs)
		if m == nil {
			t.Fatal("NewMref returned nil for valid input")
		}
		if m.Type() != TypeRef {
			t.Errorf("Expected type %v, got %v", TypeRef, m.Type())
		}
		if m.IdSheet() != idSheet {
			t.Errorf("Expected idSheet %v, got %v", idSheet, m.IdSheet())
		}
		if !reflect.DeepEqual(m.mrefBuf.Refs(), refs) {
			t.Errorf("Expected refs %v, got %v", refs, m.mrefBuf.Refs())
		}
		val := m.Value()
		valMref, ok := val.(*Mref)
		if !ok || !reflect.DeepEqual(valMref, m) {
			t.Errorf("Expected value %v, got %v", m, val)
		}
	})

	t.Run("NewMref Invalid", func(t *testing.T) {
		// Test with empty refs
		m := NewMref(idSheet, []XLREF{})
		if m != nil {
			t.Error("NewMref should return nil for empty refs")
		}
	})

	t.Run("SetMref", func(t *testing.T) {
		var a Any
		err := SetMref(&a, idSheet, refs)
		if err != nil {
			t.Fatalf("SetMref failed: %v", err)
		}

		m := (*Mref)(unsafe.Pointer(&a))
		if m.Type() != TypeRef {
			t.Errorf("Expected type %v, got %v", TypeRef, m.Type())
		}
		if m.IdSheet() != idSheet {
			t.Errorf("Expected idSheet %v, got %v", idSheet, m.IdSheet())
		}
		if !reflect.DeepEqual(m.mrefBuf.Refs(), refs) {
			t.Errorf("Expected refs %v, got %v", refs, m.mrefBuf.Refs())
		}
	})

	t.Run("ViewMref", func(t *testing.T) {
		m := NewMref(idSheet, refs)
		viewed, err := ViewMref(unsafe.Pointer(m))
		if err != nil {
			t.Fatalf("ViewMref failed: %v", err)
		}
		if !reflect.DeepEqual(viewed.mrefBuf.Refs(), refs) {
			t.Errorf("Expected refs %v, got %v", refs, viewed.mrefBuf.Refs())
		}
	})
}
