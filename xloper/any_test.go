package xloper

import (
	"reflect"
	"testing"
	"unsafe"
)

func TestAny_SetAndValue(t *testing.T) {
	testCases := []struct {
		name  string
		value any
		want  any
	}{
		{"String", "hello", "hello"},
		{"Int32", int32(42), int32(42)},
		{"Int that fits in Int32", int(123), int32(123)},
		{"Int that becomes Number", int(3000000000), float64(3000000000)},
		{"Int64", int64(123456789012345), float64(123456789012345)},
		{"Float64", 3.14, 3.14},
		{"Float32", float32(1.618), float64(float32(1.618))},
		{"Bool true", true, true},
		{"Bool false", false, false},
		{"Nil", nil, nil},
		{"Error", XlErrNA, XlErrNA},
		{"SRef", XLREF{1, 2, 3, 4}, NewSref(XLREF{1, 2, 3, 4})},
		{"Default to #VALUE!", struct{}{}, XlErrValue},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var a Any
			a.Set(tc.value)

			got := a.Value()

			// Special case for SRef comparison
			if sref, ok := tc.want.(*Sref); ok {
				gotSref, ok2 := got.(*Sref)
				if !ok2 || gotSref.ref != sref.ref {
					t.Errorf("Any.Set(%v); Value() = %v, want %v", tc.value, got, tc.want)
				}
				return
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Any.Set(%v); Value() = %v (%T), want %v (%T)", tc.value, got, got, tc.want, tc.want)
			}
		})
	}
}

func TestView(t *testing.T) {
	testCases := []struct {
		name   string
		xloper XLOPER
		want   any
	}{
		{"String", NewString("test"), "test"},
		{"Number", NewNumber(1.23), 1.23},
		{"Int32", NewInt32(123), int32(123)},
		{"Bool", NewBool(true), true},
		{"Error", newError(XlErrDiv0), XlErrDiv0},
		{"Nil", Nil, nil},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Get a pointer to the XLOPER struct
			ptr := unsafe.Pointer(reflect.ValueOf(tc.xloper).Pointer())

			// Use View to interpret the pointer
			viewed := View(ptr)
			if viewed == nil {
				t.Fatal("View returned nil")
			}

			// Check the value
			got := viewed.Value()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("View().Value() = %v (%T), want %v (%T)", got, got, tc.want, tc.want)
			}
		})
	}

	t.Run("Multi", func(t *testing.T) {
		multi, err := NewMulti([][]any{{"A"}})
		if err != nil {
			t.Fatalf("NewMulti failed: %v", err)
		}
		ptr := unsafe.Pointer(multi)
		viewed := View(ptr)
		if _, ok := viewed.(*Multi); !ok {
			t.Errorf("View() did not return *Multi, got %T", viewed)
		}
	})

	t.Run("Nil Pointer", func(t *testing.T) {
		viewed := View(nil)
		if viewed != Nil {
			t.Errorf("View(nil) should return the Nil singleton, got %T", viewed)
		}
	})
}
