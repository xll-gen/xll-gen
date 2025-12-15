#include "xll_converters.h"
#include "xll_mem.h"
#include "xll_utility.h"
#include "PascalString.h"
#include "xll_ipc.h"
#include <vector>
#include <string>

// Note: We use protocol:: for types, as defined in protocol.fbs.

// Helper to get sheet name from a reference
std::wstring GetSheetName(LPXLOPER12 pxRef) {
    if (!(pxRef->xltype & xltypeRef)) {
        return L"";
    }

    XLOPER12 xSheetNm;
    if (Excel12(xlSheetNm, &xSheetNm, 1, pxRef) != xlretSuccess) {
        return L"";
    }

    if (xSheetNm.xltype & xltypeStr) {
        std::wstring s = PascalToWString(xSheetNm.val.str);
        Excel12(xlFree, 0, 1, &xSheetNm);
        return s;
    }

    return L"";
}

flatbuffers::Offset<protocol::Range> ConvertRange(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder) {
    std::wstring sheet = GetSheetName(op);

    std::vector<protocol::Rect> rects;
    if (op->xltype & xltypeRef) {
        if (op->val.mref.lpmref) {
            for (WORD i = 0; i < op->val.mref.lpmref->count; ++i) {
                const auto& r = op->val.mref.lpmref->reftbl[i];
                rects.emplace_back(r.rwFirst, r.rwLast, r.colFirst, r.colLast);
            }
        }
    } else if (op->xltype & xltypeSRef) {
        const auto& r = op->val.sref.ref;
        rects.emplace_back(r.rwFirst, r.rwLast, r.colFirst, r.colLast);
    }

    return protocol::CreateRangeDirect(builder, std::string(sheet.begin(), sheet.end()).c_str(), &rects);
}

flatbuffers::Offset<protocol::Scalar> ConvertScalar(const XLOPER12& cell, flatbuffers::FlatBufferBuilder& builder) {
    if (cell.xltype & xltypeNum) {
        return protocol::CreateScalar(builder, protocol::ScalarValue_Num, protocol::CreateNum(builder, cell.val.num).Union());
    } else if (cell.xltype & xltypeInt) {
        return protocol::CreateScalar(builder, protocol::ScalarValue_Int, protocol::CreateInt(builder, cell.val.w).Union());
    } else if (cell.xltype & xltypeBool) {
        return protocol::CreateScalar(builder, protocol::ScalarValue_Bool, protocol::CreateBool(builder, cell.val.xbool != 0).Union());
    } else if (cell.xltype & xltypeStr) {
        std::wstring ws = PascalToWString(cell.val.str);
        std::string s = ConvertExcelString(ws.c_str());
        return protocol::CreateScalar(builder, protocol::ScalarValue_Str, protocol::CreateStrDirect(builder, s.c_str()).Union());
    } else if (cell.xltype & xltypeErr) {
        return protocol::CreateScalar(builder, protocol::ScalarValue_Err, protocol::CreateErr(builder, (protocol::XlError)cell.val.err).Union());
    } else if (cell.xltype & (xltypeMissing | xltypeNil)) {
        return protocol::CreateScalar(builder, protocol::ScalarValue_Nil, protocol::CreateNil(builder).Union());
    }

    return protocol::CreateScalar(builder, protocol::ScalarValue_Nil, protocol::CreateNil(builder).Union());
}

flatbuffers::Offset<protocol::Any> ConvertMultiToAny(const XLOPER12& xMulti, flatbuffers::FlatBufferBuilder& builder) {
    int rows = xMulti.val.array.rows;
    int cols = xMulti.val.array.columns;

    std::vector<flatbuffers::Offset<protocol::Scalar>> data;
    data.reserve(rows * cols);

    for (int i = 0; i < rows * cols; ++i) {
        data.push_back(ConvertScalar(xMulti.val.array.lparray[i], builder));
    }

    auto grid = protocol::CreateGridDirect(builder, rows, cols, &data);
    return protocol::CreateAny(builder, protocol::AnyValue_Grid, grid.Union());
}

// Helper function to convert a value XLOPER (Multi, Scalar, Missing/Nil) to a Grid offset.
static flatbuffers::Offset<protocol::Grid> ConvertValueToGrid(const XLOPER12& op, flatbuffers::FlatBufferBuilder& builder) {
    if (op.xltype & xltypeMulti) {
        int rows = op.val.array.rows;
        int cols = op.val.array.columns;
        std::vector<flatbuffers::Offset<protocol::Scalar>> data;
        data.reserve(rows * cols);
        for(int i=0; i<rows*cols; ++i) {
            data.push_back(ConvertScalar(op.val.array.lparray[i], builder));
        }
        return protocol::CreateGridDirect(builder, rows, cols, &data);
    } else if (op.xltype & (xltypeMissing | xltypeNil)) {
        return protocol::CreateGrid(builder, 0, 0, 0);
    } else {
        std::vector<flatbuffers::Offset<protocol::Scalar>> data;
        data.push_back(ConvertScalar(op, builder));
        return protocol::CreateGridDirect(builder, 1, 1, &data);
    }
}

flatbuffers::Offset<protocol::Grid> ConvertGrid(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder) {
    if (op->xltype & (xltypeRef | xltypeSRef)) {
        XLOPER12 xRes;
        XLOPER12 xDestType;
        xDestType.xltype = xltypeMissing; // Equivalent to omitting, coerces to Multi (block) or Value (single)

        if (Excel12(xlCoerce, &xRes, 2, op, &xDestType) == xlretSuccess) {
            auto result = ConvertValueToGrid(xRes, builder);
            Excel12(xlFree, 0, 1, &xRes);
            return result;
        } else {
            // Coercion failed, return empty grid
            return protocol::CreateGrid(builder, 0, 0, 0);
        }
    }
    return ConvertValueToGrid(*op, builder);
}

flatbuffers::Offset<protocol::NumGrid> ConvertNumGrid(FP12* fp, flatbuffers::FlatBufferBuilder& builder) {
    if (!fp) return protocol::CreateNumGrid(builder, 0, 0, 0);

    int rows = fp->rows;
    int cols = fp->columns;
    std::vector<double> data(fp->array, fp->array + (rows * cols));

    return protocol::CreateNumGridDirect(builder, rows, cols, &data);
}

static thread_local int g_conversionDepth = 0;

flatbuffers::Offset<protocol::Any> ConvertAny(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder) {
    if (op->xltype & xltypeNum) {
        return protocol::CreateAny(builder, protocol::AnyValue_Num, protocol::CreateNum(builder, op->val.num).Union());
    } else if (op->xltype & xltypeInt) {
        return protocol::CreateAny(builder, protocol::AnyValue_Int, protocol::CreateInt(builder, op->val.w).Union());
    } else if (op->xltype & xltypeBool) {
        return protocol::CreateAny(builder, protocol::AnyValue_Bool, protocol::CreateBool(builder, op->val.xbool != 0).Union());
    } else if (op->xltype & xltypeStr) {
        std::wstring ws = PascalToWString(op->val.str);
        std::string s = ConvertExcelString(ws.c_str());
        return protocol::CreateAny(builder, protocol::AnyValue_Str, protocol::CreateStrDirect(builder, s.c_str()).Union());
    } else if (op->xltype & xltypeErr) {
        return protocol::CreateAny(builder, protocol::AnyValue_Err, protocol::CreateErr(builder, (protocol::XlError)op->val.err).Union());
    } else if (op->xltype & (xltypeRef | xltypeSRef)) {
        size_t cellCount = 0;

        if (op->xltype & xltypeSRef) {
            int h = op->val.sref.ref.rwLast - op->val.sref.ref.rwFirst + 1;
            int w = op->val.sref.ref.colLast - op->val.sref.ref.colFirst + 1;
            cellCount = h * w;
        } else if (op->xltype & xltypeRef) {
            for(WORD i=0; i<op->val.mref.lpmref->count; ++i) {
                const auto& r = op->val.mref.lpmref->reftbl[i];
                cellCount += (r.rwLast - r.rwFirst + 1) * (r.colLast - r.colFirst + 1);
            }
        }

        if (cellCount > 100) {
            std::wstring sheet = GetSheetName(op);
            XLOPER12 xAddr;

            if (Excel12(xlfReftext, &xAddr, 1, op) == xlretSuccess && (xAddr.xltype & xltypeStr)) {
                 std::wstring addr = PascalToWString(xAddr.val.str);
                 Excel12(xlFree, 0, 1, &xAddr);

                 std::string key = ConvertExcelString(addr.c_str());

                 bool alreadySent = false;
                 {
                     std::lock_guard<std::mutex> lock(g_refCacheMutex);
                     if (g_sentRefCache.find(key) != g_sentRefCache.end()) {
                         alreadySent = true;
                     } else {
                         g_sentRefCache[key] = true;
                     }
                 }

                 if (!alreadySent) {
                     XLOPER12 xValue;
                     XLOPER12 xType;
                     xType.xltype = xltypeInt;
                     xType.val.w = xltypeMulti;

                     if (Excel12(xlCoerce, &xValue, 2, op, &xType) == xlretSuccess) {
                         flatbuffers::FlatBufferBuilder cacheBuilder;
                         auto anyVal = ConvertMultiToAny(xValue, cacheBuilder);
                         auto req = protocol::CreateSetRefCacheRequestDirect(cacheBuilder, key.c_str(), anyVal);
                         cacheBuilder.Finish(req);

                         std::vector<uint8_t> respBuf;
                         g_host.Send(cacheBuilder.GetBufferPointer(), cacheBuilder.GetSize(), (shm::MsgType)130, respBuf, 2000);

                         Excel12(xlFree, 0, 1, &xValue);
                     }
                 }

                 return protocol::CreateAny(builder, protocol::AnyValue_RefCache, protocol::CreateRefCacheDirect(builder, key.c_str()).Union());
            }
        }

        return protocol::CreateAny(builder, protocol::AnyValue_Range, ConvertRange(op, builder).Union());

    } else if (op->xltype & xltypeMulti) {
        return ConvertMultiToAny(*op, builder);
    } else if (op->xltype & (xltypeMissing | xltypeNil)) {
         return protocol::CreateAny(builder, protocol::AnyValue_Nil, protocol::CreateNil(builder).Union());
    }

    return protocol::CreateAny(builder, protocol::AnyValue_Nil, protocol::CreateNil(builder).Union());
}

LPXLOPER12 AnyToXLOPER12(const protocol::Any* any) {
    if (!any) return NULL;

    switch (any->val_type()) {
        case protocol::AnyValue_Num: {
            LPXLOPER12 op = NewXLOPER12();
            op->xltype = xltypeNum | xlbitDLLFree;
            op->val.num = any->val_as_Num()->val();
            return op;
        }
        case protocol::AnyValue_Int: {
            LPXLOPER12 op = NewXLOPER12();
            op->xltype = xltypeInt | xlbitDLLFree;
            op->val.w = any->val_as_Int()->val();
            return op;
        }
        case protocol::AnyValue_Bool: {
            LPXLOPER12 op = NewXLOPER12();
            op->xltype = xltypeBool | xlbitDLLFree;
            op->val.xbool = any->val_as_Bool()->val();
            return op;
        }
        case protocol::AnyValue_Str: {
            std::wstring ws = StringToWString(any->val_as_Str()->val()->str());
            return NewExcelString(ws);
        }
        case protocol::AnyValue_Err: {
             LPXLOPER12 op = NewXLOPER12();
             op->xltype = xltypeErr | xlbitDLLFree;
             op->val.err = (int)any->val_as_Err()->val();
             return op;
        }
        case protocol::AnyValue_Grid: {
             return GridToXLOPER12(any->val_as_Grid());
        }
        case protocol::AnyValue_NumGrid: {
             const protocol::NumGrid* ng = any->val_as_NumGrid();
             int rows = ng->rows();
             int cols = ng->cols();
             LPXLOPER12 op = NewXLOPER12();
             op->xltype = xltypeMulti | xlbitDLLFree;
             op->val.array.rows = rows;
             op->val.array.columns = cols;
             op->val.array.lparray = new XLOPER12[rows * cols];

             auto data = ng->data();
             for(int i=0; i<rows*cols; ++i) {
                 op->val.array.lparray[i].xltype = xltypeNum;
                 op->val.array.lparray[i].val.num = data->Get(i);
             }
             return op;
        }
        case protocol::AnyValue_Range: {
            return RangeToXLOPER12(any->val_as_Range());
        }
        case protocol::AnyValue_Nil:
        default: {
             return NULL;
        }
    }
}

LPXLOPER12 RangeToXLOPER12(const protocol::Range* range) {
    if (!range) return NULL;

    LPXLOPER12 op = NewXLOPER12();
    op->xltype = xltypeRef | xlbitDLLFree;
    op->val.mref.lpmref = (LPXLMREF12) new char[sizeof(XLMREF12) + sizeof(XLREF12) * range->refs()->size()];
    op->val.mref.idSheet = 0;

    op->val.mref.lpmref->count = (WORD)range->refs()->size();
    for(size_t i=0; i<range->refs()->size(); ++i) {
        const auto* r = range->refs()->Get(i);
        op->val.mref.lpmref->reftbl[i].rwFirst = r->row_first();
        op->val.mref.lpmref->reftbl[i].rwLast = r->row_last();
        op->val.mref.lpmref->reftbl[i].colFirst = r->col_first();
        op->val.mref.lpmref->reftbl[i].colLast = r->col_last();
    }

    return op;
}

LPXLOPER12 GridToXLOPER12(const protocol::Grid* grid) {
    if (!grid) return NULL;

    int rows = grid->rows();
    int cols = grid->cols();

    LPXLOPER12 op = NewXLOPER12();
    op->xltype = xltypeMulti | xlbitDLLFree;
    op->val.array.rows = rows;
    op->val.array.columns = cols;
    op->val.array.lparray = new XLOPER12[rows * cols];

    for (int i = 0; i < rows * cols; ++i) {
        auto scalar = grid->data()->Get(i);
        auto& cell = op->val.array.lparray[i];
        cell.xltype = xltypeNil; // Default

        switch(scalar->val_type()) {
            case protocol::ScalarValue_Num:
                cell.xltype = xltypeNum;
                cell.val.num = scalar->val_as_Num()->val();
                break;
            case protocol::ScalarValue_Int:
                cell.xltype = xltypeInt;
                cell.val.w = scalar->val_as_Int()->val();
                break;
            case protocol::ScalarValue_Bool:
                cell.xltype = xltypeBool;
                cell.val.xbool = scalar->val_as_Bool()->val();
                break;
            case protocol::ScalarValue_Str: {
                cell.xltype = xltypeStr;
                std::wstring ws = StringToWString(scalar->val_as_Str()->val()->str());
                size_t len = ws.length();
                if (len > 32767) len = 32767;
                cell.val.str = new XCHAR[len + 1];
                cell.val.str[0] = (XCHAR)len;
                wmemcpy(cell.val.str + 1, ws.c_str(), len);
                break;
            }
            case protocol::ScalarValue_Err:
                cell.xltype = xltypeErr;
                cell.val.err = (int)scalar->val_as_Err()->val();
                break;
            default:
                break;
        }
    }

    return op;
}

FP12* NumGridToFP12(const protocol::NumGrid* grid) {
    if (!grid) return NULL;
    int rows = grid->rows();
    int cols = grid->cols();

    size_t bytes = sizeof(FP12) + (rows * cols - 1) * sizeof(double);
    FP12* fp = (FP12*)malloc(bytes);
    fp->rows = rows;
    fp->columns = cols;

    const auto* data = grid->data();
    for(int i=0; i<rows*cols; ++i) {
        fp->array[i] = data->Get(i);
    }

    return fp;
}
