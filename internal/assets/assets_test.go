package assets

import (
	"strings"
	"testing"
)

// TestCallExcelKeeperPassThrough guards the xll_excel.h helper that lets a live
// ScopedXLOPER12 lvalue (e.g. the xlfCaller result reused in a follow-up Excel
// call) be passed to xll::CallExcel without taking its address.
//
// Regression context: the caller-aware (caller:true) codegen path passes a
// ScopedXLOPER12 by value into CallExcel. ScopedXLOPER12 is move-only, so the
// generic make_keeper(T&&) -> ScopedXLOPER12(...) wrapper hits the deleted copy
// constructor and the generated xll_main.cpp fails to compile. The dedicated
// make_keeper(ScopedXLOPER12&) overload (which extracts .get()) is what makes
// that path build. Removing it silently breaks every caller-aware add-in.
func TestCallExcelKeeperPassThrough(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["include/xll_excel.h"]
	if !ok {
		t.Fatalf("embedded include/xll_excel.h not found in assets")
	}
	if !strings.Contains(src, "make_keeper(ScopedXLOPER12&") {
		t.Errorf("xll_excel.h missing make_keeper(ScopedXLOPER12&) pass-through; " +
			"a live ScopedXLOPER12 passed to CallExcel would hit the deleted copy ctor")
	}
}
