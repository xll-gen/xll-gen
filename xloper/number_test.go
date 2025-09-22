package xloper

import (
	"math"
	"testing"
	"unsafe"
)

func TestNumber(t *testing.T) {
	testCases := []struct {
		name  string
		value float64
	}{
		{"Zero", 0.0},
		{"Positive", 123.456},
		{"Negative", -987.654},
		{"Max", math.MaxFloat64},
		{"Smallest", math.SmallestNonzeroFloat64},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test NewNumber
			num := NewNumber(tc.value)
			if num.Type() != TypeNum {
				t.Errorf("NewNumber: expected type %d, got %d", TypeNum, num.Type())
			}
			if num.Float64() != tc.value {
				t.Errorf("NewNumber: expected value %f, got %f", tc.value, num.Float64())
			}

			// Test ViewNumber
			viewedNum, err := ViewNumber(unsafe.Pointer(num))
			if err != nil {
				t.Fatalf("ViewNumber: unexpected error: %v", err)
			}
			if viewedNum.Type() != TypeNum {
				t.Errorf("ViewNumber: expected type %d, got %d", TypeNum, viewedNum.Type())
			}
			if viewedNum.Float64() != tc.value {
				t.Errorf("ViewNumber: expected value %f, got %f", tc.value, viewedNum.Float64())
			}
		})
	}
}

func TestInteger(t *testing.T) {
	testCases := []struct {
		name  string
		value int32
	}{
		{"Zero", 0},
		{"Positive", 12345},
		{"Negative", -54321},
		{"Max", math.MaxInt32},
		{"Min", math.MinInt32},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test NewInteger
			intg := NewInt32(tc.value)
			if intg.Type() != TypeInt {
				t.Errorf("NewInteger: expected type %d, got %d", TypeInt, intg.Type())
			}
			if intg.Int32() != tc.value {
				t.Errorf("NewInteger: expected value %d, got %d", tc.value, intg.Int32())
			}

			// Test ViewInteger
			viewedInt, err := ViewInt32(unsafe.Pointer(intg))
			if err != nil {
				t.Fatalf("ViewInteger: unexpected error: %v", err)
			}
			if viewedInt.Type() != TypeInt {
				t.Errorf("ViewInteger: expected type %d, got %d", TypeInt, viewedInt.Type())
			}
			if viewedInt.Int32() != tc.value {
				t.Errorf("ViewInteger: expected value %d, got %d", tc.value, viewedInt.Int32())
			}
		})
	}
}

func TestBool(t *testing.T) {
	testCases := []struct {
		name  string
		value bool
	}{
		{"True", true},
		{"False", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test NewBool
			b := NewBool(tc.value)
			if b.Type() != TypeBool {
				t.Errorf("NewBool: expected type %d, got %d", TypeBool, b.Type())
			}
			if b.Bool() != tc.value {
				t.Errorf("NewBool: expected value %v, got %v", tc.value, b.Bool())
			}

			// Test ViewBool
			viewedBool, err := ViewBool(unsafe.Pointer(b))
			if err != nil {
				t.Fatalf("ViewBool: unexpected error: %v", err)
			}
			if viewedBool.Type().Base() != TypeBool {
				t.Errorf("ViewBool: expected type %d, got %d", TypeBool, viewedBool.Type())
			}
			if viewedBool.Bool() != tc.value {
				t.Errorf("ViewBool: expected value %v, got %v", tc.value, viewedBool.Bool())
			}
		})
	}
}
