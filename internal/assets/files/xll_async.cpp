#include "xll_ipc.h"
#include "xll_converters.h"
#include "xll_mem.h"
#include <vector>
#include <mutex>
#include "protocol_generated.h"

// Process async batch response (MSG_BATCH_ASYNC_RESPONSE = 128)
// This function constructs XLOPER12s for the results and puts them into the global cache or similar mechanism?
// Actually, for async functions, Excel calls the function, we return an async handle (xlAsyncReturn).
// Then when result is ready, we call xlAsyncReturn with the handle and the value.

// The Go server sends a batch of results.
// We iterate them and call xlAsyncReturn.

void ProcessAsyncBatchResponse(const protocol::BatchAsyncResponse* batch) {
    if (!batch || !batch->results()) return;

    for (const auto* result : *batch->results()) {
        if (!result) continue;

        uint64_t handle = result->handle();
        LPXLOPER12 pxAsyncHandle = (LPXLOPER12)handle; // This is the handle Excel gave us (Xoper)

        // Convert result to XLOPER12
        LPXLOPER12 pxResult = NULL;

        if (result->error() && result->error()->Length() > 0) {
            // Error case
            // Can be string or error code?
            // Usually we return #VALUE! or the error string.
            // Let's return a string for now or map common errors.
            // protocol.fbs has XlError in Any. But here we have error string field.
            // If error is set, we return it as string or #FAIL?
            pxResult = NewXLOPER12();
            pxResult->xltype = xltypeStr | xlbitDLLFree;
            std::wstring ws = ConvertToWString(result->error()->c_str());
            // pxResult->val.str ... use helper
            Excel12(xlFree, 0, 1, pxResult); // Reset
            pxResult = NewExcelString(ws.c_str());
        } else {
            // Success case
            pxResult = AnyToXLOPER12(result->result());
        }

        // Call xlAsyncReturn
        // signature: void xlAsyncReturn(LPXLOPER12 pxAsyncHandle, LPXLOPER12 pxValue);
        // We need to use Excel12 with xlAsyncReturn (id is not in xlcall.h by default usually? It is in standard xlcall.h)
        // Wait, xlAsyncReturn is a function exposed by Excel, but called via Excel12?
        // No, it's a C API function `xlAsyncReturn`.
        // Checks xlcall.h ...
        // In newer Excel 2010+ SDK, xlAsyncReturn is defined.

        // However, we are in a background thread (worker).
        // xlAsyncReturn is thread-safe?
        // "This function is thread-safe and can be called on any thread." - Microsoft Docs.

        // So we can call it directly.
        // We cast the handle back to LPXLOPER12.

        // NOTE: We must ensure pxResult is valid.

        if (pxResult) {
            // We need to ensure we don't leak pxResult.
            // xlAsyncReturn copies the data?
            // "Excel copies the XLOPER12 structure."
            // So we should free pxResult after the call.

            // Wait, we need to pass the handle we received.
            // In xll_main.cpp, we passed `pxAsyncHandle` (which is type X) to Go as an integer (pointer).
            // Now we cast it back.

            int ret = Excel12(xlAsyncReturn, 0, 2, pxAsyncHandle, pxResult);

            // Clean up pxResult
            // If it has xlbitDLLFree, we should call xlAutoFree12?
            // Or just manually free since we allocated it?
            // xlAsyncReturn copies it. So we own it.

            // We used AnyToXLOPER12 which allocates using NewXLOPER12 (ObjectPool) and sets xlbitDLLFree.
            // So we should use ReleaseXLOPER12 to free the container but delete content?
            // Actually, we can just call xlAutoFree12(pxResult).
            // But we are in the XLL, so we can call our own xlAutoFree12 logic or just helper.

            // If we call xlAutoFree12, it puts the OP back to pool.
            // Correct.

            // But wait, xll_mem.cpp's xlAutoFree12 expects the pointer to be valid.

            // Let's use ReleaseXLOPER12 if we want to be explicit, or just call xlAutoFree12.
            // xll_mem.h: void xlAutoFree12(LPXLOPER12 pxFree);

            xlAutoFree12(pxResult);
        }
    }
}
