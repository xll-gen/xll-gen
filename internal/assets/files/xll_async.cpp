#include "xll_ipc.h"
#include "xll_converters.h"
#include "xll_mem.h"
#include "xll_utility.h"
#include <vector>
#include <mutex>
#include "protocol_generated.h"

// Process async batch response (MSG_BATCH_ASYNC_RESPONSE = 128)

void ProcessAsyncBatchResponse(const protocol::BatchAsyncResponse* batch) {
    if (!batch || !batch->results()) return;

    for (const auto* result : *batch->results()) {
        if (!result) continue;

        uint64_t handle = result->handle();
        LPXLOPER12 pxAsyncHandle = (LPXLOPER12)handle;

        LPXLOPER12 pxResult = NULL;

        if (result->error() && result->error()->size() > 0) { // Use size()
            std::wstring ws = ConvertToWString(result->error()->c_str());
            pxResult = NewExcelString(ws);
        } else {
            pxResult = AnyToXLOPER12(result->result());
        }

        if (pxResult) {
            Excel12(xlAsyncReturn, 0, 2, pxAsyncHandle, pxResult);
            // Cleanup: AnyToXLOPER12 and NewExcelString use NewXLOPER12/ObjectPool
            // and set xlbitDLLFree. We should return it to the pool or free it.
            // Since we allocated it locally for this call, we should free it.
            xlAutoFree12(pxResult);
        }
    }
}
