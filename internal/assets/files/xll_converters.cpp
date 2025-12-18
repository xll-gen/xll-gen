#include "xll_converters.h"
#include "xll_mem.h"
#include "xll_utility.h"
#include "PascalString.h"
#include "xll_ipc.h"
#include <mutex>
#include <map>
#include <algorithm> // for std::copy

// FlatBuffers Converters

flatbuffers::Offset<protocol::Grid> GridToFlatBuffer(flatbuffers::FlatBufferBuilder& builder, LPXLOPER12 op) {
    if (op->xltype == xltypeMulti) {
        int rows = op->val.array.rows;
        int cols = op->val.array.columns;
        int count = rows * cols;

        std::vector<flatbuffers::Offset<protocol::Scalar>> elements;
        elements.reserve(count);

        for (int i = 0; i < count; ++i) {
            LPXLOPER12 cell = &op->val.array.lparray[i];
            if (cell->xltype == xltypeNum) {
                elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Num, protocol::CreateNum(builder, cell->val.num).Union()));
            } else if (cell->xltype == xltypeInt) {
                elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Int, protocol::CreateInt(builder, cell->val.w).Union()));
            } else if (cell->xltype == xltypeBool) {
                elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Bool, protocol::CreateBool(builder, cell->val.xbool).Union()));
            } else if (cell->xltype == xltypeStr) {
                 elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Str, protocol::CreateStr(builder, builder.CreateString(ConvertExcelString(cell->val.str))).Union()));
            } else if (cell->xltype == xltypeErr) {
                 elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Err, protocol::CreateErr(builder, (protocol::XlError)cell->val.err).Union()));
            } else {
                 elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Nil, protocol::CreateNil(builder).Union()));
            }
        }

        auto vec = builder.CreateVector(elements);
        return protocol::CreateGrid(builder, (uint32_t)rows, (uint32_t)cols, vec);
    }

    // Handle scalar as 1x1 Grid
    std::vector<flatbuffers::Offset<protocol::Scalar>> elements;
    if (op->xltype == xltypeNum) {
        elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Num, protocol::CreateNum(builder, op->val.num).Union()));
    } else if (op->xltype == xltypeInt) {
        elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Int, protocol::CreateInt(builder, op->val.w).Union()));
    } else if (op->xltype == xltypeBool) {
        elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Bool, protocol::CreateBool(builder, op->val.xbool).Union()));
    } else if (op->xltype == xltypeStr) {
            elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Str, protocol::CreateStr(builder, builder.CreateString(ConvertExcelString(op->val.str))).Union()));
    } else if (op->xltype == xltypeErr) {
            elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Err, protocol::CreateErr(builder, (protocol::XlError)op->val.err).Union()));
    } else {
            elements.push_back(protocol::CreateScalar(builder, protocol::ScalarValue::Nil, protocol::CreateNil(builder).Union()));
    }

    auto vec = builder.CreateVector(elements);
    return protocol::CreateGrid(builder, 1, 1, vec);
}

flatbuffers::Offset<protocol::NumGrid> NumGridToFlatBuffer(flatbuffers::FlatBufferBuilder& builder, LPXLOPER12 op) {
    // XLOPER12 input not supported for NumGrid, only FP12.
    return protocol::CreateNumGrid(builder, 0, 0, 0);
}

flatbuffers::Offset<protocol::NumGrid> NumGridToFlatBuffer(flatbuffers::FlatBufferBuilder& builder, FP12* fp) {
    if (!fp) return protocol::CreateNumGrid(builder, 0, 0, 0);
    int rows = fp->rows;
    int cols = fp->columns;
    int count = rows * cols;

    // FP12 array is double[]
    auto vec = builder.CreateVector(fp->array, count);
    return protocol::CreateNumGrid(builder, (uint32_t)rows, (uint32_t)cols, vec);
}

flatbuffers::Offset<protocol::Range> RangeToFlatBuffer(flatbuffers::FlatBufferBuilder& builder, LPXLOPER12 op, const std::string& format = "") {
    // Requires xltypeRef or SRRef
    auto fmtOff = builder.CreateString(format);

    if (op->xltype & xltypeRef) {
         std::vector<protocol::Rect> rects;
         int count = op->val.mref.lpmref->count;
         for(int i=0; i<count; ++i) {
             auto& r = op->val.mref.lpmref->reftbl[i];
             rects.emplace_back(r.rwFirst, r.rwLast, r.colFirst, r.colLast);
         }
         auto vec = builder.CreateVectorOfStructs(rects);

         return protocol::CreateRange(builder, 0, vec, fmtOff);
    }
    // SRRef
    if (op->xltype & xltypeSRef) {
         std::vector<protocol::Rect> rects;
         auto& r = op->val.sref.ref;
         rects.emplace_back(r.rwFirst, r.rwLast, r.colFirst, r.colLast);
         auto vec = builder.CreateVectorOfStructs(rects);
         return protocol::CreateRange(builder, 0, vec, fmtOff);
    }

    return protocol::CreateRange(builder, 0, 0, fmtOff);
}

// Helper for converting Multi to Any
flatbuffers::Offset<protocol::Any> ConvertMultiToAny(XLOPER12& op, flatbuffers::FlatBufferBuilder& builder) {
    // Check if it's homogenous numbers -> NumGrid
    // Else -> Grid
    bool allNums = true;
    int count = op.val.array.rows * op.val.array.columns;
    for(int i=0; i<count; ++i) {
        if (op.val.array.lparray[i].xltype != xltypeNum) {
            allNums = false;
            break;
        }
    }

    if (allNums) {
         // Create NumGrid
         std::vector<double> nums;
         nums.reserve(count);
         for(int i=0; i<count; ++i) nums.push_back(op.val.array.lparray[i].val.num);
         auto vec = builder.CreateVector(nums);
         auto ng = protocol::CreateNumGrid(builder, op.val.array.rows, op.val.array.columns, vec);
         return protocol::CreateAny(builder, protocol::AnyValue::NumGrid, ng.Union());
    } else {
         auto g = GridToFlatBuffer(builder, &op);
         return protocol::CreateAny(builder, protocol::AnyValue::Grid, g.Union());
    }
}

flatbuffers::Offset<protocol::Range> ConvertRange(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder, const std::string& format) {
    return RangeToFlatBuffer(builder, op, format);
}

flatbuffers::Offset<protocol::Any> AnyToFlatBuffer(flatbuffers::FlatBufferBuilder& builder, LPXLOPER12 op) {
    if (op->xltype == xltypeNum) {
        return protocol::CreateAny(builder, protocol::AnyValue::Num, protocol::CreateNum(builder, op->val.num).Union());
    } else if (op->xltype == xltypeInt) {
        return protocol::CreateAny(builder, protocol::AnyValue::Int, protocol::CreateInt(builder, op->val.w).Union());
    } else if (op->xltype == xltypeBool) {
        return protocol::CreateAny(builder, protocol::AnyValue::Bool, protocol::CreateBool(builder, op->val.xbool).Union());
    } else if (op->xltype == xltypeStr) {
        return protocol::CreateAny(builder, protocol::AnyValue::Str, protocol::CreateStr(builder, builder.CreateString(ConvertExcelString(op->val.str))).Union());
    } else if (op->xltype == xltypeErr) {
        return protocol::CreateAny(builder, protocol::AnyValue::Err, protocol::CreateErr(builder, (protocol::XlError)op->val.err).Union());
    } else if (op->xltype & (xltypeRef | xltypeSRef)) {
        // Optimize: If > 100 cells, send RefCache key
        int cellCount = 1;
        if (op->xltype & xltypeRef) {
            int count = op->val.mref.lpmref->count;
            cellCount = 0;
            for(int i=0; i<count; ++i) {
                auto& r = op->val.mref.lpmref->reftbl[i];
                cellCount += (r.rwLast - r.rwFirst + 1) * (r.colLast - r.colFirst + 1);
            }
        } else {
            auto& r = op->val.sref.ref;
            cellCount = (r.rwLast - r.rwFirst + 1) * (r.colLast - r.colFirst + 1);
        }

        if (cellCount > 100) {
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

                 return protocol::CreateAny(builder, protocol::AnyValue::RefCache, protocol::CreateRefCacheDirect(builder, key.c_str()).Union());
            }
        }

        return protocol::CreateAny(builder, protocol::AnyValue::Range, ConvertRange(op, builder).Union());

    } else if (op->xltype & xltypeMulti) {
        return ConvertMultiToAny(*op, builder);
    } else if (op->xltype & (xltypeMissing | xltypeNil)) {
         return protocol::CreateAny(builder, protocol::AnyValue::Nil, protocol::CreateNil(builder).Union());
    }

    return protocol::CreateAny(builder, protocol::AnyValue::Nil, protocol::CreateNil(builder).Union());
}

LPXLOPER12 AnyToXLOPER12(const protocol::Any* any) {
    if (!any) return NULL;

    switch (any->val_type()) {
        case protocol::AnyValue::Num: {
            LPXLOPER12 op = NewXLOPER12();
            op->xltype = xltypeNum | xlbitDLLFree;
            op->val.num = any->val_as_Num()->val();
            return op;
        }
        case protocol::AnyValue::Int: {
            LPXLOPER12 op = NewXLOPER12();
            op->xltype = xltypeInt | xlbitDLLFree;
            op->val.w = any->val_as_Int()->val();
            return op;
        }
        case protocol::AnyValue::Bool: {
            LPXLOPER12 op = NewXLOPER12();
            op->xltype = xltypeBool | xlbitDLLFree;
            op->val.xbool = any->val_as_Bool()->val();
            return op;
        }
        case protocol::AnyValue::Str: {
            std::wstring ws = StringToWString(any->val_as_Str()->val()->str());
            return NewExcelString(ws);
        }
        case protocol::AnyValue::Err: {
             LPXLOPER12 op = NewXLOPER12();
             op->xltype = xltypeErr | xlbitDLLFree;
             op->val.err = (int)any->val_as_Err()->val();
             return op;
        }
        case protocol::AnyValue::Grid: {
             return GridToXLOPER12(any->val_as_Grid());
        }
        case protocol::AnyValue::NumGrid: {
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
        case protocol::AnyValue::Range: {
            return RangeToXLOPER12(any->val_as_Range());
        }
        case protocol::AnyValue::Nil:
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
            case protocol::ScalarValue::Num:
                cell.xltype = xltypeNum;
                cell.val.num = scalar->val_as_Num()->val();
                break;
            case protocol::ScalarValue::Int:
                cell.xltype = xltypeInt;
                cell.val.w = scalar->val_as_Int()->val();
                break;
            case protocol::ScalarValue::Bool:
                cell.xltype = xltypeBool;
                cell.val.xbool = scalar->val_as_Bool()->val();
                break;
            case protocol::ScalarValue::Str: {
                cell.xltype = xltypeStr;
                std::wstring ws = StringToWString(scalar->val_as_Str()->val()->str());
                auto vec = WStringToPascalString(ws);
                cell.val.str = new XCHAR[vec.size()];
                std::copy(vec.begin(), vec.end(), cell.val.str);
                break;
            }
            case protocol::ScalarValue::Err:
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

    FP12* fp = NewFP12(rows, cols);

    const auto* data = grid->data();
    for(int i=0; i<rows*cols; ++i) {
        fp->array[i] = data->Get(i);
    }

    return fp;
}
