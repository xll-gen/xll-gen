package xloper

import (
	"reflect"
	"testing"
)

func TestMulti(t *testing.T) {
	data := [][]any{
		{"hello", 42, 3.14, true, nil},
		{"world", int32(-7), -2.71, false, XlErrorCode(0x07)},
	}

	multi, err := NewMulti(data)
	if err != nil {
		t.Fatalf("NewMulti failed: %v", err)
	}

	val := multi.Value().([][]any)
	if len(val) != len(data) {
		t.Fatalf("expected %d rows, got %d", len(data), len(val))
	}
	for r := range data {
		if len(val[r]) != len(data[r]) {
			t.Fatalf("expected %d cols in row %d, got %d", len(data[r]), r, len(val[r]))
		}
		for c := range data[r] {
			want := data[r][c]
			got := val[r][c]
			switch v := want.(type) {
			case float64:
				if gotf, ok := got.(float64); !ok || gotf != v {
					t.Errorf("cell [%d][%d]: expected float64 %v, got %T %v", r, c, v, got, got)
				}
			case int, int32:
				if goti, ok := got.(int32); !ok || int32(reflect.ValueOf(v).Int()) != goti {
					t.Errorf("cell [%d][%d]: expected int32 %v, got %T %v", r, c, v, got, got)
				}
			case string:
				if gots, ok := got.(string); !ok || gots != v {
					t.Errorf("cell [%d][%d]: expected string %q, got %T %v", r, c, v, got, got)
				}
			case bool:
				if gotb, ok := got.(bool); !ok || gotb != v {
					t.Errorf("cell [%d][%d]: expected bool %v, got %T %v", r, c, v, got, got)
				}
			case nil:
				if got != nil {
					t.Errorf("cell [%d][%d]: expected nil, got %T %v", r, c, got, got)
				}
			case XlErrorCode:
				if goterr, ok := got.(XlErrorCode); !ok || goterr != v {
					t.Errorf("cell [%d][%d]: expected XlErrorCode %v, got %T %v", r, c, v, got, got)
				}
			default:
				t.Errorf("cell [%d][%d]: unexpected type %T", r, c, v)
			}
		}
	}
}
