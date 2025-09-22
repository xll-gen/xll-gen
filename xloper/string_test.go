package xloper

import (
	"testing"
	"unsafe"
)

func TestNewString(t *testing.T) {
	testCases := []struct {
		name  string
		input string
	}{
		{
			name:  "Simple ASCII string",
			input: "hello",
		},
		{
			name:  "String with spaces",
			input: "hello world",
		},
		{
			name:  "Empty string",
			input: "",
		},
		{
			name:  "String with special characters",
			input: "!@#$%^&*()",
		},
		{
			name:  "Korean string",
			input: "안녕하세요",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewString(tc.input)

			if s == nil {
				t.Fatalf("NewString returned nil")
			}

			expectedType := TypeStr
			if s.Type().Base() != expectedType {
				t.Errorf("Expected type %d, but got %d", expectedType, s.Type())
			}

			// The first element of the buffer is the length.
			expectedLen := uint16(len(encodeWTF16(tc.input, nil)))
			if (*s.stringBuf)[0] != expectedLen {
				t.Errorf("Expected string length %d, but got %d", expectedLen, (*s.stringBuf)[0])
			}

			if s.String() != tc.input {
				t.Errorf("Expected string value '%s', but got '%s'", tc.input, s.String())
			}

			stringBufAddr := uintptr(unsafe.Pointer(&(*s.stringBuf)[0]))
			if uintptr(unsafe.Pointer(s.ptr)) != stringBufAddr {
				t.Errorf("Pointer in struct does not point to stringBuf. Got %x, expected %x", s.ptr, stringBufAddr)
			}
		})
	}
}
