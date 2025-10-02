package xloper

import (
	"bytes"
	"testing"
	"unsafe"
)

func TestAsyncHandle(t *testing.T) {
	// Create some dummy data and a pointer to it.
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	dataPtr := unsafe.Pointer(&data[0])

	// Manually construct an AsyncHandle struct in memory.
	// This simulates what we might receive from Excel.
	var handleMem Any
	h := (*AsyncHandle)(unsafe.Pointer(&handleMem))
	h.typ = TypeBigData
	h.handle = dataPtr
	h.cbData = int32(len(data))

	// Test ViewAsyncHandle
	viewedHandle, err := ViewAsyncHandle(unsafe.Pointer(&handleMem))
	if err != nil {
		t.Fatalf("ViewAsyncHandle failed: %v", err)
	}

	if viewedHandle.Type() != TypeBigData {
		t.Errorf("Expected type %v, got %v", TypeBigData, viewedHandle.Type())
	}

	// Test Bytes() which uses unsafe.Slice
	byteSlice := viewedHandle.Bytes()
	if !bytes.Equal(byteSlice, data) {
		t.Errorf("Bytes() returned %v, want %v", byteSlice, data)
	}

	// Test Value()
	val := viewedHandle.Value().([]byte)
	if !bytes.Equal(val, data) {
		t.Errorf("Value() returned %v, want %v", val, data)
	}
}
