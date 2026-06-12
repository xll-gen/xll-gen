#include "xll_ipc.h"
#include "xll_excel.h"
#include "xll_log.h"
#include "types/converters.h"
#include "types/mem.h"
#include "types/utility.h"
#include <vector>
#include <mutex>
#include "xll_async.h"
#include "xll_ipc.h"
#include "types/protocol_generated.h"


// Process async batch response (MSG_BATCH_ASYNC_RESPONSE = 128)

void ProcessAsyncBatchResponse(const protocol::BatchAsyncResponse* batch) {
    if (!batch || !batch->results()) return;

    for (const auto* result : *batch->results()) {
        if (!result) continue;

        auto* handleVec = result->handle();
        if (!handleVec || handleVec->size() != sizeof(XLOPER12)) continue;

        XLOPER12 xAsyncHandle;
        std::memcpy(&xAsyncHandle, handleVec->data(), sizeof(XLOPER12));

        LPXLOPER12 pxResult = NULL;

        if (result->error() && result->error()->size() > 0) { // Use size()
            std::wstring ws = ConvertToWString(result->error()->c_str());
            pxResult = NewExcelString(ws);
        } else {
            pxResult = AnyToXLOPER12(result->result());
        }

        if (pxResult) {
            // Ownership after xlAsyncReturn depends on whether the result is
            // DLL-owned (xlbitDLLFree set).
            //
            // HEAP-CORRUPTION FIX (real-Excel verification, 2026-06-12): the
            // earlier code assumed xlAsyncReturn deep-copies the ENTIRE result
            // synchronously and then freed it immediately via xlAutoFree12. That
            // assumption holds for scalars (the value is copied inline) but is
            // FALSE for xltypeMulti / arrays: Excel retains the lparray pointer
            // to populate the spill range AFTER the calc transaction, so a
            // synchronous delete[] of lparray is a use-after-free. It manifested
            // as STATUS_HEAP_CORRUPTION (0xc0000374, faulting module ntdll) and
            // killed Excel on every `=MyAsyncGrid()` recalc. The README
            // ("Dynamic arrays (spill)") documents async grid/numgrid spill as a
            // supported feature, so this is a correctness bug, not a limitation.
            //
            // Correct contract (identical to the SYNC return path, which works):
            // a result carrying xlbitDLLFree is owned by Excel-after-handoff. We
            // hand it to xlAsyncReturn and Excel invokes the registered
            // xlAutoFree12 callback when it is done with it (deferred — after the
            // array is consumed). We MUST NOT free it ourselves. Only a result
            // WITHOUT xlbitDLLFree (a borrowed pool node, e.g. an aliased
            // XLOPER12) is ours to return to the pool here.
            bool dllOwned = (pxResult->xltype & xlbitDLLFree) != 0;
            int rc = xll::CallExcel(xlAsyncReturn, nullptr, &xAsyncHandle, pxResult);
            if (rc != xlretSuccess) {
                // Excel did NOT take the result (stale async context,
                // xlretInvAsynchronousContext, etc.), so its deferred
                // xlAutoFree12 callback will never fire for it — free it
                // ourselves or it leaks. xlAutoFree12 handles both DLL-owned
                // payloads and plain pool nodes (it always releases the node).
                xll::LogWarn("xlAsyncReturn failed (rc=" + std::to_string(rc) + "); freeing result locally");
                xlAutoFree12(pxResult);
            } else if (!dllOwned) {
                // Borrowed pool node (no xlbitDLLFree): Excel copied the value
                // inline and will NOT call xlAutoFree12 for it; return it to
                // the pool here. NOTE: with the current converters this branch
                // is defensive-only — AnyToXLOPER12/NewExcelString set
                // xlbitDLLFree on EVERY result kind — but keep it: a future
                // borrowed/aliased result must not be handed to Excel's
                // deferred free. Do not "simplify" this into a synchronous
                // xlAutoFree12 — that is the exact heap-corruption bug above.
                ReleaseXLOPER12(pxResult);
            }
            // else: Excel owns it now and will call xlAutoFree12 when finished.
            // Note: do NOT read pxResult->xltype after xlAsyncReturn for the
            // dllOwned decision — capture it BEFORE the handoff (done above),
            // since Excel may have begun processing the handed-off XLOPER12.
        }
    }
}
