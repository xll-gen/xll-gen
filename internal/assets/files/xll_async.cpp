#include "include/xll_async.h"
#include "include/xll_converters.h"
#include "schema_generated.h"
#include <cstring>

int32_t ProcessAsyncBatchResponse(const uint8_t* req, std::vector<XLOPER12>& handles, std::vector<XLOPER12>& values) {
    handles.clear();
    values.clear();

    auto batch = flatbuffers::GetRoot<ipc::BatchAsyncResponse>(req);
    if (batch->results()) {
        for (auto res : *batch->results()) {
            XLOPER12 h;
            h.xltype = xltypeBigData | xlbitXLFree;
            LPXLOPER12 originalHandle = (LPXLOPER12)res->handle();
            if (originalHandle) {
                h = *originalHandle; // Copy the struct content

                XLOPER12 val;
                std::memset(&val, 0, sizeof(XLOPER12));

                if (res->error() && res->error()->size() > 0) {
                    val.xltype = xltypeErr; val.val.err = xlerrValue;
                } else {
                    // result() is now ipc::types::Any* (Table)
                    auto* anyPtr = res->result();
                    LPXLOPER12 pVal = AnyToXLOPER12(anyPtr);
                    if (pVal) {
                        val = *pVal;
                        // If pVal was allocated by NewXLOPER12 (xlbitDLLFree), we must eventually free it.
                        // But here we copy the struct into 'val' (in our vector).
                        // If pVal has xlbitDLLFree, 'val' inherits it.
                        // We need to delete pVal itself (the container) but keep the content.
                        delete pVal;
                    } else {
                        val.xltype = xltypeErr; val.val.err = xlerrValue;
                    }
                }
                handles.push_back(h);
                values.push_back(val);
            }
        }
    }

    if (!handles.empty()) {
        if (handles.size() == 1) {
            Excel12(xlAsyncReturn, 0, 2, &handles[0], &values[0]);
        } else {
            int N = (int)handles.size();

            XLOPER12 arrH, arrV;
            arrH.xltype = xltypeMulti; arrH.val.array.rows = 1; arrH.val.array.columns = N; arrH.val.array.lparray = handles.data();
            arrV.xltype = xltypeMulti; arrV.val.array.rows = 1; arrV.val.array.columns = N; arrV.val.array.lparray = values.data();

            Excel12(xlAsyncReturn, 0, 2, &arrH, &arrV);
        }

        // Cleanup allocated strings and arrays
        for(auto& v : values) {
            if (v.xltype & xltypeStr) {
                if (v.val.str) {
                    delete[] v.val.str;
                    v.val.str = nullptr;
                }
            } else if (v.xltype & xltypeMulti) {
                if (v.val.array.lparray) {
                    int count = v.val.array.rows * v.val.array.columns;
                    for(int i=0; i<count; ++i) {
                        if ((v.val.array.lparray[i].xltype & xltypeStr) && v.val.array.lparray[i].val.str) {
                            delete[] v.val.array.lparray[i].val.str;
                        }
                    }
                    delete[] v.val.array.lparray;
                    v.val.array.lparray = nullptr;
                }
            }
        }
    }
    return 1;
}
