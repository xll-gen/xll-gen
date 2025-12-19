#include "xll_ipc.h"
#include "types/converters.h"
#include "types/mem.h"
#include "types/utility.h"
#include <vector>
#include <mutex>
#include "protocol_generated.h"

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
            Excel12(xlAsyncReturn, 0, 2, &xAsyncHandle, pxResult);
            // Cleanup: AnyToXLOPER12 and NewExcelString use NewXLOPER12/ObjectPool
            // and set xlbitDLLFree. We should return it to the pool or free it.
            // Since we allocated it locally for this call, we should free it.
            if (pxResult->xltype & xlbitDLLFree) {
                xlAutoFree12(pxResult);
            }
        }
    }
}
