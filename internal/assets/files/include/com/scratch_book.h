#pragma once
// scratch_book.h — helpers for the ribbon "temp-workbook bounce".
//
// The bounce exists because the in-process Application object is reachable only
// through the XLDESK -> EXCEL7 child window, which does not exist when the XLL
// loads with no workbook open. `ribbon.bounce: full` creates a scratch workbook
// so one does, then closes it again. These are the two pieces of that dance that
// are pure logic: naming the active workbook, and suppressing/restoring the
// Application event flags around the close.
//
// They were inline in xll_main.cpp.tmpl and had no template variables in them.
// Living here they are compiled once, and — more usefully — the SEH restore
// contract below is stated in one place instead of being re-emitted per project.
//
// GATING: compiled whenever the ribbon is enabled. The bounce mode that actually
// USES them is `full`; other modes simply never call them. That is cheaper than
// threading a bounce-mode macro through CMake for two functions.
//
// THREADING: STA only (xlAutoOpen and the bounce path both qualify).

#include <windows.h>
#include <oaidl.h>
#include <string>

#include "types/xlcall.h"

namespace xll {
namespace ribbon {

// Returns the name of the currently ACTIVE workbook via the XLM macro function
// GET.DOCUMENT(88) (xlfGetDocument, selector 88 = "name of the active document/
// workbook", e.g. L"Book1"), or an empty string if it cannot be determined.
// Selector 88 verified in types/include/types/xlcall.h (#define xlfGetDocument
// 188) against the XLM GET.DOCUMENT reference (type_num 88 -> active workbook
// name). The result is an Excel-allocated Pascal string, so it is held in a
// ScopedXLOPER12Result (xlFree on destruction) and decoded with PascalToWString.
std::wstring GetActiveWorkbookName();

// Idempotent: replays the captured originals for whatever is still armed, LOGS
// (not swallows) a failed restore put, releases the AddRef'd app, and clears the
// record. Restores ONLY what was actually flipped, with the ORIGINAL captured
// values — never a blind =true, which would clobber a user/automation host that
// had them off on purpose.
//
// Call this again after the SEH block that wraps the bounce: see the class below
// for why the destructor alone is not enough.
void RestorePendingEventSuppression();

// Suppresses Application.EnableEvents / .DisplayAlerts for the scratch-book
// close and restores the ORIGINAL captured values afterwards.
//
// WHY: document-classification / DLP add-ins hook WorkbookBeforeClose with a
// modal prompt; firing that mid-xlAutoOpen — before Excel's UI (and the add-in
// itself) has finished initializing — can crash or hang Excel. EnableEvents=false
// stops the Application event sink entirely (covers third-party COM add-in
// sinks); DisplayAlerts=false additionally suppresses Excel-native prompts.
// `ribbon.bounce: keep-open` removes the close altogether; this guard hardens the
// default (full) mode.
//
// RESTORE PATHS. The RAII destructor covers normal exit and C++ unwind, but an
// ASYNC SEH FAULT inside xlcFileClose — the very scenario this guard defends
// against — unwinds via XLL_SAFE_BLOCK's __except WITHOUT running intervening C++
// destructors on /EHsc. Leaving EnableEvents=false would silence every add-in's
// Workbook events for the rest of the session, so the suppression state does NOT
// live in this object: it lives in a file-static record (AddRef'd app + captured
// originals + armed flags), and the restore is the idempotent free function
// above. The destructor calls it (normal path) and the caller calls it again
// right after the SAFE_BLOCK (the replay for the destructor-skipped SEH path, a
// no-op when the destructor already ran). This also covers partial construction:
// the record arms property-by-property, so a fault between the two puts still
// leaves the first one replayable.
//
// Best-effort by design: if a property cannot be read (unexpected VT) or the put
// fails, the corresponding armed flag stays false, nothing is restored for it,
// and the close proceeds exactly as it did before this guard existed.
struct ScratchCloseEventSuppressor {
    explicit ScratchCloseEventSuppressor(IDispatch* app);
    ~ScratchCloseEventSuppressor();
    ScratchCloseEventSuppressor(const ScratchCloseEventSuppressor&) = delete;
    ScratchCloseEventSuppressor& operator=(const ScratchCloseEventSuppressor&) = delete;
};

} // namespace ribbon
} // namespace xll
