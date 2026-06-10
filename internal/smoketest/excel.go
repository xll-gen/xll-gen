//go:build windows

package smoketest

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
)

// pidFromHwnd asks user32 for the process that owns a top-level window.
// Returns 0 if Hwnd is invalid; Excel always hands back a valid HWND from
// `Application.Hwnd`, so a zero here means we failed to capture it.
func pidFromHwnd(hwnd uintptr) uint32 {
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	_, _, _ = procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// excelApp wraps the COM `Excel.Application` IDispatch with the handful of
// operations the smoke harness needs: RegisterXLL, a single-cell formula
// round-trip, and quit. All calls MUST happen on the same OS thread that
// initialized COM — callers are expected to runtime.LockOSThread() and to
// invoke openExcel/Close from inside that locked goroutine.
type excelApp struct {
	disp     *ole.IDispatch
	pid      uint32    // captured before Quit so we can force-kill if it hangs
	teardown sync.Once // guards CoUninitialize + UnlockOSThread (paired with the
	// CoInitializeEx + LockOSThread done in openExcel). Without this, a second
	// Close() would CoUninitialize an already-uninitialized apartment and
	// UnlockOSThread an already-unlocked thread — both undefined / unbalanced.
}

func openExcel() (*excelApp, error) {
	runtime.LockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		// S_FALSE just means already initialized for this thread; treat as success.
		oleErr, ok := err.(*ole.OleError)
		if !ok || oleErr.Code() != 0x00000001 /* S_FALSE */ {
			runtime.UnlockOSThread()
			return nil, fmt.Errorf("CoInitializeEx: %w", err)
		}
	}

	unk, err := oleutil.CreateObject("Excel.Application")
	if err != nil {
		ole.CoUninitialize()
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("CreateObject(Excel.Application): %w", err)
	}
	disp, err := unk.QueryInterface(ole.IID_IDispatch)
	unk.Release()
	if err != nil {
		ole.CoUninitialize()
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("QueryInterface(IDispatch): %w", err)
	}

	// Make Excel quiet for the test.
	_, _ = oleutil.PutProperty(disp, "Visible", false)
	_, _ = oleutil.PutProperty(disp, "DisplayAlerts", false)
	_, _ = oleutil.PutProperty(disp, "ScreenUpdating", false)

	app := &excelApp{disp: disp}
	if hwndV, err := oleutil.GetProperty(disp, "Hwnd"); err == nil {
		app.pid = pidFromHwnd(uintptr(hwndV.Val))
	}
	return app, nil
}

// Close best-effort closes any open workbooks without saving, quits Excel,
// and tears down COM. It also force-kills the Excel process if Quit fails to
// land within 5s, and kills any leftover xll_smoke.exe server (the generated
// XLL spawned it; xlAutoClose normally reaps it, but we belt-and-suspenders
// to guarantee a clean machine state for the next run).
//
// Safe to call multiple times.
func (a *excelApp) Close() {
	if a == nil {
		return
	}
	pid := a.pid

	// Tear down COM artifacts first so Quit isn't fighting live references.
	if a.disp != nil {
		// Mark every workbook as saved so Quit doesn't prompt.
		if books, err := oleutil.GetProperty(a.disp, "Workbooks"); err == nil {
			booksDisp := books.ToIDispatch()
			if booksDisp != nil {
				if cnt, err := oleutil.GetProperty(booksDisp, "Count"); err == nil {
					n := int(cnt.Val)
					for i := 1; i <= n; i++ {
						wb, err := oleutil.GetProperty(booksDisp, "Item", i)
						if err != nil {
							continue
						}
						wbDisp := wb.ToIDispatch()
						if wbDisp != nil {
							_, _ = oleutil.PutProperty(wbDisp, "Saved", true)
							wbDisp.Release()
						}
					}
				}
				booksDisp.Release()
			}
		}
		_, _ = oleutil.CallMethod(a.disp, "Quit")
		a.disp.Release()
		a.disp = nil
	}

	// CoUninitialize + UnlockOSThread exactly once, no matter how many times
	// Close is called (the doc above promises "safe to call multiple times").
	a.teardown.Do(func() {
		ole.CoUninitialize()
		runtime.UnlockOSThread()
	})

	// Belt-and-suspenders: force-kill anything still around. We never spawned
	// Excel manually — the COM CreateObject above did — so only PIDs we
	// captured get the axe. The server image name is unique to this harness.
	if pid != 0 && processAlive(pid, 5*time.Second) {
		_ = exec.Command("taskkill", "/F", "/PID", strconv.FormatUint(uint64(pid), 10)).Run()
	}
	_ = exec.Command("taskkill", "/F", "/IM", "xll_smoke.exe").Run()
}

// processAlive polls `tasklist` every 250ms up to timeout. Returns true the
// moment we still see the PID at the end of the window (i.e. Quit didn't
// take, kill it). `tasklist` writes "INFO: No tasks ..." to stdout when the
// PID is gone, exit 0 either way — that's the signal we key on.
func processAlive(pid uint32, timeout time.Duration) bool {
	pidStr := strconv.FormatUint(uint64(pid), 10)
	deadline := time.Now().Add(timeout)
	for {
		out, err := exec.Command("tasklist", "/FI", "PID eq "+pidStr, "/NH").Output()
		if err == nil {
			if len(out) < 4 || string(out[:4]) == "INFO" {
				return false // process is gone
			}
		}
		if !time.Now().Before(deadline) {
			return true // still alive after the grace window
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// RegisterXLL invokes `Application.RegisterXLL(Filename)`. Returns the
// boolean Excel reports (true = registered successfully).
func (a *excelApp) RegisterXLL(absPath string) (bool, error) {
	v, err := oleutil.CallMethod(a.disp, "RegisterXLL", absPath)
	if err != nil {
		return false, fmt.Errorf("RegisterXLL(%q): %w", absPath, err)
	}
	// Excel returns VARIANT_BOOL (int16: -1 true, 0 false).
	switch x := v.Value().(type) {
	case bool:
		return x, nil
	case int16:
		return x != 0, nil
	case int32:
		return x != 0, nil
	}
	return false, fmt.Errorf("RegisterXLL: unexpected return %T (%v)", v.Value(), v.Value())
}

// AddWorkbook opens a fresh blank workbook and returns its IDispatch.
// Caller owns the returned dispatch (call Release()).
func (a *excelApp) AddWorkbook() (*ole.IDispatch, error) {
	books, err := oleutil.GetProperty(a.disp, "Workbooks")
	if err != nil {
		return nil, fmt.Errorf("GetProperty(Workbooks): %w", err)
	}
	booksDisp := books.ToIDispatch()
	defer booksDisp.Release()

	wb, err := oleutil.CallMethod(booksDisp, "Add")
	if err != nil {
		return nil, fmt.Errorf("Workbooks.Add: %w", err)
	}
	return wb.ToIDispatch(), nil
}

// SetRtdThrottle drops Application.RTD.ThrottleInterval to ms milliseconds
// so the smoke harness doesn't have to wait Excel's 2s default. Best-effort:
// older Excel SKUs may reject negative or sub-100ms values; we ignore.
func (a *excelApp) SetRtdThrottle(ms int32) {
	rtd, err := oleutil.GetProperty(a.disp, "RTD")
	if err != nil {
		return
	}
	rtdDisp := rtd.ToIDispatch()
	if rtdDisp == nil {
		return
	}
	defer rtdDisp.Release()
	_, _ = oleutil.PutProperty(rtdDisp, "ThrottleInterval", ms)
}

// SetCellFormula writes `formula` into `cellAddr` (e.g. "A1") of `sheet`.
func SetCellFormula(sheet *ole.IDispatch, cellAddr, formula string) error {
	rng, err := oleutil.GetProperty(sheet, "Range", cellAddr)
	if err != nil {
		return fmt.Errorf("Range(%s): %w", cellAddr, err)
	}
	rngDisp := rng.ToIDispatch()
	defer rngDisp.Release()
	if _, err := oleutil.PutProperty(rngDisp, "Formula", formula); err != nil {
		return fmt.Errorf("PutProperty(Formula): %w", err)
	}
	return nil
}

// GetCellValue reads `cellAddr`'s current displayed value.
func GetCellValue(sheet *ole.IDispatch, cellAddr string) (any, error) {
	rng, err := oleutil.GetProperty(sheet, "Range", cellAddr)
	if err != nil {
		return nil, fmt.Errorf("Range(%s): %w", cellAddr, err)
	}
	rngDisp := rng.ToIDispatch()
	defer rngDisp.Release()
	val, err := oleutil.GetProperty(rngDisp, "Value")
	if err != nil {
		return nil, fmt.Errorf("GetProperty(Value): %w", err)
	}
	return val.Value(), nil
}

// CalculateFull forces Excel to recompute the entire workbook synchronously.
func (a *excelApp) CalculateFull() error {
	_, err := oleutil.CallMethod(a.disp, "CalculateFull")
	if err != nil {
		return fmt.Errorf("CalculateFull: %w", err)
	}
	return nil
}

// PollUntilNumeric repeatedly reads `cellAddr` until its value is a numeric
// non-error, or `timeout` elapses. Returns the numeric value as int32 plus
// the raw final value for diagnostics. Async cells start as "#GETTING_DATA"
// (string) and RTD cells start as "#N/A" or an int16 error code; both
// resolve to a number once the round trip completes.
func (a *excelApp) PollUntilNumeric(sheet *ole.IDispatch, cellAddr string, timeout time.Duration) (int32, any, error) {
	deadline := time.Now().Add(timeout)
	var last any
	for {
		v, err := GetCellValue(sheet, cellAddr)
		if err != nil {
			return 0, last, err
		}
		last = v
		if n, ok := numericInt32(v); ok {
			return n, v, nil
		}
		if !time.Now().Before(deadline) {
			return 0, last, fmt.Errorf("cell %s did not resolve to a number within %s (last=%v %T)", cellAddr, timeout, last, last)
		}
		// The GetCellValue COM round-trip above implicitly services the STA
		// message queue, so RTD UpdateNotify callbacks get dispatched without a
		// separate pump. A short sleep is fine; Excel's RTD throttle is the
		// rate-limiter.
		time.Sleep(150 * time.Millisecond)
	}
}

func numericInt32(v any) (int32, bool) {
	switch x := v.(type) {
	case int32:
		return x, true
	case int16:
		// Excel error codes arrive as int16 with high bit set (e.g. -2146826246
		// for #N/A as 32-bit). VT_ERROR errors come back as int32 < 0; pure
		// int16 here is a real number.
		return int32(x), true
	case int64:
		return int32(x), true
	case int:
		return int32(x), true
	case float64:
		// Reject NaN/Inf — those would suggest #N/A or similar.
		if x != x || x > 2147483647 || x < -2147483648 {
			return 0, false
		}
		return int32(x), true
	case float32:
		return int32(x), true
	case string:
		// "#GETTING_DATA" sometimes leaks as a string. Definitely not ready.
		return 0, false
	case nil:
		return 0, false
	}
	return 0, false
}
