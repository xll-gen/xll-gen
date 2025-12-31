#include "xll_ipc.h"
#include "xll_excel.h"
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
            xll::CallExcel(xlAsyncReturn, nullptr, &xAsyncHandle, pxResult);
            // Cleanup: AnyToXLOPER12 and NewExcelString use NewXLOPER12/ObjectPool.
            // We must ensure the node is returned to the pool.
            // If xlbitDLLFree is set, xlAutoFree12 frees content AND node.
            // If not set (scalar), we must manually return the node.
            if (pxResult->xltype & xlbitDLLFree) {
                xlAutoFree12(pxResult);
            } else {
                ReleaseXLOPER12(pxResult);
            }
        }
    }
}
