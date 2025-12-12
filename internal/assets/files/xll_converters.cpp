#include "xll_converters.h"
#include "PascalString.h"
#include "xll_utility.h"
#include "xll_ipc.h"
#include "xll_mem.h"
#include <sstream>
#include <cstring>

std::wstring GetSheetName(LPXLOPER12 pxRef) {
    if (!pxRef || (pxRef->xltype != xltypeRef && pxRef->xltype != xltypeSRef)) {
        return L"";
    }

    XLOPER12 xRes;
    int ret = Excel12(xlSheetNm, &xRes, 1, pxRef);
    if (ret != xlretSuccess) return L"";

    std::wstring result;
    if (xRes.xltype == xltypeStr && xRes.val.str) {
         size_t len = (size_t)xRes.val.str[0];
         if (len > 0) {
             result.assign(xRes.val.str + 1, len);
         }
    }
    Excel12(xlFree, 0, 1, &xRes);
    return result;
}

flatbuffers::Offset<ipc::types::Range> ConvertRange(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder) {
    if (!op) return 0;

    std::vector<ipc::types::Rect> refs;
    std::wstring sheetName = GetSheetName(op);

    if (op->xltype == xltypeRef) {
        if (op->val.mref.lpmref) {
            for (WORD i = 0; i < op->val.mref.lpmref->count; ++i) {
                const auto& r = op->val.mref.lpmref->reftbl[i];
                refs.emplace_back(r.rwFirst, r.rwLast, r.colFirst, r.colLast);
            }
        }
    } else if (op->xltype == xltypeSRef) {
        const auto& r = op->val.sref.ref;
        refs.emplace_back(r.rwFirst, r.rwLast, r.colFirst, r.colLast);
    }

    auto sheetNameOffset = builder.CreateString(WideToUtf8(sheetName));
    auto refsOffset = builder.CreateVectorOfStructs(refs);
    return ipc::types::CreateRange(builder, sheetNameOffset, refsOffset);
}

flatbuffers::Offset<ipc::types::Scalar> ConvertScalar(const XLOPER12& cell, flatbuffers::FlatBufferBuilder& builder) {
    if (cell.xltype == xltypeNum) {
        auto val = ipc::types::CreateNum(builder, cell.val.num);
        return ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Num, val.Union());
    } else if (cell.xltype == xltypeInt) {
        auto val = ipc::types::CreateInt(builder, cell.val.w);
        return ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Int, val.Union());
    } else if (cell.xltype == xltypeBool) {
        auto val = ipc::types::CreateBool(builder, cell.val.xbool);
        return ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Bool, val.Union());
    } else if (cell.xltype == xltypeStr) {
        auto s = ConvertExcelString(cell.val.str);
        auto sOff = builder.CreateString(s);
        auto val = ipc::types::CreateStr(builder, sOff);
        return ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Str, val.Union());
    } else if (cell.xltype == xltypeErr) {
        auto val = ipc::types::CreateErr(builder, (ipc::types::XlError)cell.val.err);
        return ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Err, val.Union());
    }
    auto val = ipc::types::CreateNil(builder);
    return ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Nil, val.Union());
}

flatbuffers::Offset<ipc::types::Any> ConvertMultiToAny(const XLOPER12& xMulti, flatbuffers::FlatBufferBuilder& builder) {
    int rows = xMulti.val.array.rows;
    int cols = xMulti.val.array.columns;
    int count = rows * cols;

    bool allNum = true;
    for(int i=0; i<count; ++i) {
        if (xMulti.val.array.lparray[i].xltype != xltypeNum) {
            allNum = false;
            break;
        }
    }

    if (allNum) {
        std::vector<double> data;
        data.reserve(count);
        for(int i=0; i<count; ++i) {
            data.push_back(xMulti.val.array.lparray[i].val.num);
        }
        auto dataOff = builder.CreateVector(data);
        auto arr = ipc::types::CreateNumGrid(builder, rows, cols, dataOff);
        return ipc::types::CreateAny(builder, ipc::types::AnyValue_NumGrid, arr.Union());
    } else {
        std::vector<flatbuffers::Offset<ipc::types::Scalar>> data;
        data.reserve(count);
        for(int i=0; i<count; ++i) {
             data.push_back(ConvertScalar(xMulti.val.array.lparray[i], builder));
        }
        auto dataOff = builder.CreateVector(data);
        auto arr = ipc::types::CreateGrid(builder, rows, cols, dataOff);
        return ipc::types::CreateAny(builder, ipc::types::AnyValue_Grid, arr.Union());
    }
}

flatbuffers::Offset<ipc::types::NumGrid> ConvertNumGrid(FP12* fp, flatbuffers::FlatBufferBuilder& builder) {
    if (!fp) return 0;
    int rows = fp->rows;
    int cols = fp->columns;
    int count = rows * cols;
    std::vector<double> data(count);
    for(int i=0; i<count; ++i) data[i] = fp->array[i];
    auto dataOff = builder.CreateVector(data);
    return ipc::types::CreateNumGrid(builder, rows, cols, dataOff);
}

flatbuffers::Offset<ipc::types::Grid> ConvertGrid(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder) {
    if (!op) return 0;
    XLOPER12 xMulti;
    int ret = Excel12(xlCoerce, &xMulti, 2, op, TempInt12(xltypeMulti));
    if (ret != xlretSuccess) return 0;

    int rows = xMulti.val.array.rows;
    int cols = xMulti.val.array.columns;
    int count = rows * cols;
    std::vector<flatbuffers::Offset<ipc::types::Scalar>> data;
    data.reserve(count);
    for(int i=0; i<count; ++i) {
         data.push_back(ConvertScalar(xMulti.val.array.lparray[i], builder));
    }
    Excel12(xlFree, 0, 1, &xMulti);
    auto dataOff = builder.CreateVector(data);
    return ipc::types::CreateGrid(builder, rows, cols, dataOff);
}

flatbuffers::Offset<ipc::types::Any> ConvertAny(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder) {
    if (!op) {
        auto nilVal = ipc::types::CreateNil(builder);
        return ipc::types::CreateAny(builder, ipc::types::AnyValue_Nil, nilVal.Union());
    }

    if (op->xltype == xltypeNum) {
        auto val = ipc::types::CreateNum(builder, op->val.num);
        return ipc::types::CreateAny(builder, ipc::types::AnyValue_Num, val.Union());
    } else if (op->xltype == xltypeInt) {
        auto val = ipc::types::CreateInt(builder, op->val.w);
        return ipc::types::CreateAny(builder, ipc::types::AnyValue_Int, val.Union());
    } else if (op->xltype == xltypeBool) {
        auto val = ipc::types::CreateBool(builder, op->val.xbool);
        return ipc::types::CreateAny(builder, ipc::types::AnyValue_Bool, val.Union());
    } else if (op->xltype == xltypeStr) {
        auto s = ConvertExcelString(op->val.str);
        auto sOff = builder.CreateString(s);
        auto val = ipc::types::CreateStr(builder, sOff);
        return ipc::types::CreateAny(builder, ipc::types::AnyValue_Str, val.Union());
    } else if (op->xltype == xltypeErr) {
        auto val = ipc::types::CreateErr(builder, (ipc::types::XlError)op->val.err);
        return ipc::types::CreateAny(builder, ipc::types::AnyValue_Err, val.Union());
    } else if (op->xltype == xltypeMissing || op->xltype == xltypeNil) {
        auto val = ipc::types::CreateNil(builder);
        return ipc::types::CreateAny(builder, ipc::types::AnyValue_Nil, val.Union());
    } else if (op->xltype == xltypeRef || op->xltype == xltypeSRef) {
        long rows = 0;
        long cols = 0;
        std::wstring sheetName = GetSheetName(op);
        std::stringstream ss;
        ss << WideToUtf8(sheetName) << "!";

        if (op->xltype == xltypeRef) {
             if (op->val.mref.lpmref) {
                 for (WORD i = 0; i < op->val.mref.lpmref->count; ++i) {
                     const auto& r = op->val.mref.lpmref->reftbl[i];
                     rows += (r.rwLast - r.rwFirst + 1);
                     if (i==0) cols = (r.colLast - r.colFirst + 1);
                     ss << r.rwFirst << ":" << r.rwLast << ":" << r.colFirst << ":" << r.colLast << ";";
                 }
             }
        } else {
             const auto& r = op->val.sref.ref;
             rows = r.rwLast - r.rwFirst + 1;
             cols = r.colLast - r.colFirst + 1;
             ss << r.rwFirst << ":" << r.rwLast << ":" << r.colFirst << ":" << r.colLast;
        }

        long totalCells = rows * cols;
        std::string key = ss.str();

        bool useCache = (totalCells > 100);

        if (useCache) {
             std::lock_guard<std::mutex> lock(g_refCacheMutex);
             bool cached = g_sentRefCache[key];
             if (!cached) {
                  XLOPER12 xMulti;
                  int ret = Excel12(xlCoerce, &xMulti, 2, op, TempInt12(xltypeMulti));
                  if (ret == xlretSuccess) {
                      flatbuffers::FlatBufferBuilder reqB(1024);

                      auto anyOff = ConvertMultiToAny(xMulti, reqB);
                      auto keyOff = reqB.CreateString(key);
                      auto cacheReq = ipc::CreateSetRefCacheRequest(reqB, keyOff, anyOff);
                      reqB.Finish(cacheReq);

                      Excel12(xlFree, 0, 1, &xMulti);

                      std::vector<uint8_t> respBuf;
                      uint32_t timeoutMs = 2000;

                      auto res = g_host.Send(reqB.GetBufferPointer(), reqB.GetSize(), (shm::MsgType)MSG_SETREFCACHE, respBuf, 2000);
                      if (res && res.Value() > 0) {
                          g_sentRefCache[key] = true;
                          cached = true;
                      }
                  }
             }

             if (cached) {
                 auto keyOff = builder.CreateString(key);
                 auto val = ipc::types::CreateRefCache(builder, keyOff);
                 return ipc::types::CreateAny(builder, ipc::types::AnyValue_RefCache, val.Union());
             }
        }

        XLOPER12 xMulti;
        int ret = Excel12(xlCoerce, &xMulti, 2, op, TempInt12(xltypeMulti));
        if (ret == xlretSuccess) {
             auto anyOff = ConvertMultiToAny(xMulti, builder);
             Excel12(xlFree, 0, 1, &xMulti);
             return anyOff;
        }
    }

    auto nilVal = ipc::types::CreateNil(builder);
    return ipc::types::CreateAny(builder, ipc::types::AnyValue_Nil, nilVal.Union());
}

FP12* NumGridToFP12(const ipc::types::NumGrid* g) {
    if (!g) return nullptr;
    int rows = g->rows();
    int cols = g->cols();
    FP12* fp = NewFP12(rows, cols);
    if (!fp) return nullptr;
    auto data = g->data();
    int count = rows * cols;
    if (data->size() == count) {
        std::memcpy(fp->array, data->data(), count * sizeof(double));
    }
    return fp;
}

LPXLOPER12 GridToXLOPER12(const ipc::types::Grid* g) {
    if (!g) return NewXLOPER12();
    int rows = g->rows();
    int cols = g->cols();
    int count = rows * cols;
    auto data = g->data();
    if (data->size() != count) return NewXLOPER12();

    LPXLOPER12 x = NewXLOPER12();
    x->xltype = xltypeMulti | xlbitDLLFree;
    x->val.array.rows = rows;
    x->val.array.columns = cols;
    x->val.array.lparray = new XLOPER12[count];
    std::memset(x->val.array.lparray, 0, count * sizeof(XLOPER12));

    for(int i=0; i<count; ++i) {
        auto scalar = data->Get(i);
        auto& cell = x->val.array.lparray[i];

        switch (scalar->val_type()) {
            case ipc::types::ScalarValue_Num:
                cell.xltype = xltypeNum;
                cell.val.num = scalar->val_as_Num()->val();
                break;
            case ipc::types::ScalarValue_Int:
                cell.xltype = xltypeInt;
                cell.val.w = scalar->val_as_Int()->val();
                break;
            case ipc::types::ScalarValue_Bool:
                cell.xltype = xltypeBool;
                cell.val.xbool = scalar->val_as_Bool()->val() ? 1 : 0;
                break;
            case ipc::types::ScalarValue_Str: {
                std::string s = scalar->val_as_Str()->val()->str();
                std::wstring ws = StringToWString(s);
                auto pascalStr = WStringToPascalString(ws);
                cell.val.str = new wchar_t[pascalStr.size()];
                std::memcpy(cell.val.str, pascalStr.data(), pascalStr.size() * sizeof(wchar_t));
                cell.xltype = xltypeStr | xlbitDLLFree;
                break;
            }
            case ipc::types::ScalarValue_Err:
                cell.xltype = xltypeErr;
                cell.val.err = (int)scalar->val_as_Err()->val();
                break;
            default:
                cell.xltype = xltypeNil;
        }
    }
    return x;
}

LPXLOPER12 AnyToXLOPER12(const ipc::types::Any* any) {
    if (!any) return NewXLOPER12(); // Nil

    switch (any->val_type()) {
    case ipc::types::AnyValue_Num: {
        LPXLOPER12 x = NewXLOPER12();
        x->xltype = xltypeNum | xlbitDLLFree;
        x->val.num = any->val_as_Num()->val();
        return x;
    }
    case ipc::types::AnyValue_Int: {
        LPXLOPER12 x = NewXLOPER12();
        x->xltype = xltypeInt | xlbitDLLFree;
        x->val.w = any->val_as_Int()->val();
        return x;
    }
    case ipc::types::AnyValue_Bool: {
        LPXLOPER12 x = NewXLOPER12();
        x->xltype = xltypeBool | xlbitDLLFree;
        x->val.xbool = any->val_as_Bool()->val() ? 1 : 0;
        return x;
    }
    case ipc::types::AnyValue_Str: {
        std::string s = any->val_as_Str()->val()->str();
        return NewExcelString(StringToWString(s));
    }
    case ipc::types::AnyValue_Err: {
        LPXLOPER12 x = NewXLOPER12();
        x->xltype = xltypeErr | xlbitDLLFree;
        x->val.err = (int)any->val_as_Err()->val();
        return x;
    }
    case ipc::types::AnyValue_Range: {
        return RangeToXLOPER12(any->val_as_Range());
    }
    case ipc::types::AnyValue_NumGrid: {
        auto g = any->val_as_NumGrid();
        int rows = g->rows();
        int cols = g->cols();
        int count = rows * cols;
        auto data = g->data();
        if (data->size() != count) return NewXLOPER12(); // Error

        LPXLOPER12 x = NewXLOPER12();
        x->xltype = xltypeMulti | xlbitDLLFree;
        x->val.array.rows = rows;
        x->val.array.columns = cols;
        x->val.array.lparray = new XLOPER12[count];
        std::memset(x->val.array.lparray, 0, count * sizeof(XLOPER12));

        for(int i=0; i<count; ++i) {
            x->val.array.lparray[i].xltype = xltypeNum;
            x->val.array.lparray[i].val.num = data->Get(i);
        }
        return x;
    }
    case ipc::types::AnyValue_Grid: {
        return GridToXLOPER12(any->val_as_Grid());
    }
    default:
        return NewXLOPER12();
    }
}

LPXLOPER12 RangeToXLOPER12(const ipc::types::Range* range) {
    if (!range) return nullptr;

    auto refs = range->refs();
    if (!refs || refs->size() == 0) return nullptr;

    std::wstring sheetName = StringToWString(range->sheet_name()->str());
    DWORD idSheet = 0;
    bool hasSheet = false;

    if (!sheetName.empty()) {
        XLOPER12 xName;
        xName.xltype = xltypeStr;
        // Need pascal string, use TempStr12 logic or create one on stack
        // TempStr12 uses a ring buffer, safe for short term use in Excel12 call.
        // But we are in converters.
        // Let's use xll_utility.h TempStr12.

        XLOPER12 xId;
        int ret = Excel12(xlSheetId, &xId, 1, TempStr12(sheetName.c_str()));
        if (ret == xlretSuccess && (xId.xltype == xltypeRef)) {
            idSheet = xId.val.mref.idSheet;
            hasSheet = true;
            Excel12(xlFree, 0, 1, &xId);
        }
    }

    LPXLOPER12 x = NewXLOPER12();
    // If we have a sheet ID or multiple refs, use xltypeRef
    // If no sheet ID and single ref, can use xltypeSRef?
    // Safer to use xltypeRef if we can, but xltypeRef requires idSheet.
    // If we don't have idSheet, we should use SRef for current sheet.

    if (!hasSheet) {
        if (refs->size() == 1) {
            x->xltype = xltypeSRef | xlbitDLLFree;
            const auto& r = refs->Get(0);
            x->val.sref.count = 1;
            x->val.sref.ref.rwFirst = r->row_first();
            x->val.sref.ref.rwLast = r->row_last();
            x->val.sref.ref.colFirst = r->col_first();
            x->val.sref.ref.colLast = r->col_last();
            return x;
        } else {
             // Multiple refs without sheet ID?
             // Use current sheet ID.
             XLOPER12 xId;
             int ret = Excel12(xlSheetId, &xId, 0); // Active sheet?
             if (ret == xlretSuccess && (xId.xltype == xltypeRef)) {
                  idSheet = xId.val.mref.idSheet;
                  hasSheet = true;
                  Excel12(xlFree, 0, 1, &xId);
             }
        }
    }

    // Construct xltypeRef
    x->xltype = xltypeRef | xlbitDLLFree;
    x->val.mref.lpmref = (LPXLMREF12) new char[sizeof(XLMREF12) + sizeof(XLREF12) * refs->size()];
    x->val.mref.lpmref->count = (WORD)refs->size();
    x->val.mref.idSheet = idSheet;

    for(size_t i=0; i<refs->size(); ++i) {
        const auto& r = refs->Get((flatbuffers::uoffset_t)i);
        x->val.mref.lpmref->reftbl[i].rwFirst = r->row_first();
        x->val.mref.lpmref->reftbl[i].rwLast = r->row_last();
        x->val.mref.lpmref->reftbl[i].colFirst = r->col_first();
        x->val.mref.lpmref->reftbl[i].colLast = r->col_last();
    }

    return x;
}
