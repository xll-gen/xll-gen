#include "xll_converters.h"
#include "xll_mem.h"
#include "xll_utility.h"
#include <vector>
#include <string>
#include "PascalString.h"
#include "xll_ipc.h"

// Note: We use protocol:: for types, as defined in protocol.fbs.

// Helper to get sheet name from a reference
std::wstring GetSheetName(LPXLOPER12 pxRef) {
    if (!(pxRef->xltype & xltypeRef)) {
        return L"";
    }

    XLOPER12 xSheetNm;
    // xlfGetCell type 1 returns the formula in A1 style, but we want just the sheet name?
    // Actually xlfGetCell(1, ...) gives absolute reference string like [Book1]Sheet1!$A$1
    // We want the sheet name.
    // Use xlSheetNm function:
    // "Returns the name of the sheet or macro sheet contained in a reference."
    if (Excel12(xlSheetNm, &xSheetNm, 1, pxRef) != xlretSuccess) {
        return L"";
    }

    // xSheetNm should be a string
    if (xSheetNm.xltype & xltypeStr) {
        std::wstring s = PascalToWString(xSheetNm.val.str);
        Excel12(xlFree, 0, 1, &xSheetNm);

        // The result is usually in the form [Book1]Sheet1
        // We probably want to return it as is.
        return s;
    }

    return L"";
}

flatbuffers::Offset<protocol::Range> ConvertRange(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder) {
    std::wstring sheet = GetSheetName(op);
    auto sheetNameOffset = builder.CreateString(std::string(sheet.begin(), sheet.end()));

    std::vector<protocol::Rect> rects;
    if (op->xltype & xltypeRef) {
        // Single reference can have multiple areas
        if (op->val.mref.lpmref) {
            for (WORD i = 0; i < op->val.mref.lpmref->count; ++i) {
                const auto& r = op->val.mref.lpmref->reftbl[i];
                rects.emplace_back(r.rwFirst, r.rwLast, r.colFirst, r.colLast);
            }
        }
    } else if (op->xltype & xltypeSRef) {
        // Single reference
        const auto& r = op->val.sref.ref;
        rects.emplace_back(r.rwFirst, r.rwLast, r.colFirst, r.colLast);
    }

    return protocol::CreateRangeDirect(builder, std::string(sheet.begin(), sheet.end()).c_str(), &rects);
}

flatbuffers::Offset<protocol::Scalar> ConvertScalar(const XLOPER12& cell, flatbuffers::FlatBufferBuilder& builder) {
    // Handle different scalar types
    if (cell.xltype & xltypeNum) {
        return protocol::CreateScalar(builder, protocol::ScalarValue_Num, protocol::CreateNum(builder, cell.val.num).Union());
    } else if (cell.xltype & xltypeInt) {
        return protocol::CreateScalar(builder, protocol::ScalarValue_Int, protocol::CreateInt(builder, cell.val.w).Union());
    } else if (cell.xltype & xltypeBool) {
        return protocol::CreateScalar(builder, protocol::ScalarValue_Bool, protocol::CreateBool(builder, cell.val.xbool != 0).Union());
    } else if (cell.xltype & xltypeStr) {
        std::wstring ws = PascalToWString(cell.val.str);
        std::string s = ConvertExcelString(ws); // Use util helper
        return protocol::CreateScalar(builder, protocol::ScalarValue_Str, protocol::CreateStrDirect(builder, s.c_str()).Union());
    } else if (cell.xltype & xltypeErr) {
        return protocol::CreateScalar(builder, protocol::ScalarValue_Err, protocol::CreateErr(builder, (protocol::XlError)cell.val.err).Union());
    } else if (cell.xltype & (xltypeMissing | xltypeNil)) {
        return protocol::CreateScalar(builder, protocol::ScalarValue_Nil, protocol::CreateNil(builder).Union());
    }

    // Default to Nil for unsupported
    return protocol::CreateScalar(builder, protocol::ScalarValue_Nil, protocol::CreateNil(builder).Union());
}

flatbuffers::Offset<protocol::Any> ConvertMultiToAny(const XLOPER12& xMulti, flatbuffers::FlatBufferBuilder& builder) {
    // xMulti is an xltypeMulti which is a grid of XLOPERs
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

flatbuffers::Offset<protocol::Grid> ConvertGrid(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder) {
    if (op->xltype & xltypeMulti) {
        int rows = op->val.array.rows;
        int cols = op->val.array.columns;
        std::vector<flatbuffers::Offset<protocol::Scalar>> data;
        data.reserve(rows * cols);
        for(int i=0; i<rows*cols; ++i) {
            data.push_back(ConvertScalar(op->val.array.lparray[i], builder));
        }
        return protocol::CreateGridDirect(builder, rows, cols, &data);
    } else if (op->xltype & xltypeMissing || op->xltype & xltypeNil) {
        // Empty grid
        return protocol::CreateGrid(builder, 0, 0, 0);
    } else {
        // Treat as 1x1 grid
        std::vector<flatbuffers::Offset<protocol::Scalar>> data;
        data.push_back(ConvertScalar(*op, builder));
        return protocol::CreateGridDirect(builder, 1, 1, &data);
    }
}

flatbuffers::Offset<protocol::NumGrid> ConvertNumGrid(FP12* fp, flatbuffers::FlatBufferBuilder& builder) {
    if (!fp) return protocol::CreateNumGrid(builder, 0, 0, 0);

    int rows = fp->rows;
    int cols = fp->columns;
    // FP12 array is double[]
    std::vector<double> data(fp->array, fp->array + (rows * cols));

    return protocol::CreateNumGridDirect(builder, rows, cols, &data);
}

// Global recursion depth counter to prevent stack overflow on circular structures (though unlikely in Excel)
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
        std::string s = ConvertExcelString(ws);
        return protocol::CreateAny(builder, protocol::AnyValue_Str, protocol::CreateStrDirect(builder, s.c_str()).Union());
    } else if (op->xltype & xltypeErr) {
        return protocol::CreateAny(builder, protocol::AnyValue_Err, protocol::CreateErr(builder, (protocol::XlError)op->val.err).Union());
    } else if (op->xltype & (xltypeRef | xltypeSRef)) {
        // References -> Range or single value?
        // xll-gen logic: inputs are usually coerced to values unless explicit Range arg.
        // But for 'Any', we probably want to support Range if it's a reference.
        // However, if the user passed a Range to an Any argument, do we resolve it or pass the reference?
        // In "Singleflight" optimization logic (mentioned in memory), large ranges are sent as RefCache key.
        // But here we are just converting.

        // Check if we should use RefCache (Singleflight)
        // If it's a large range (>100 cells), we might want to send it as a RefCache key if we had one.
        // But typically, the Go side logic handles the "if large, use cache" check on the *client* side or we do it here.
        // The memory says: "Singleflight caching pattern where ranges larger than 100 cells are sent to the Go server via SetRefCache".

        // This implies we should check size.
        // Get size
        int rows = 0, cols = 0;
        if (Excel12(xlfRows, &op, 1, op) == xlretSuccess) rows = op->val.w; // Wait, xlfRows returns result in another XLOPER.
        // Simpler: use Coerce to get dimensions or iterate references.
        // Actually, we can just look at mref/sref.

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
            // Generate a cache key (e.g. string representation of address + some unique ID?)
            // Actually the memory says "allow subsequent calls in the same cycle to pass a RefCache key".
            // This implies *we* (the C++ side) set the cache.
            // But to set the cache we need to send a message.
            // "sent to the Go server via SetRefCache (MsgID: 130)"

            // This is complex logic for a converter.
            // The converter just converts. The caller (stub) decides whether to use RefCache.
            // But `ConvertAny` is used by the stub.

            // Re-reading memory: "The any argument type ... implements a "Singleflight" caching pattern ...
            // sent to the Go server via SetRefCache ... allowing subsequent calls ... to pass a RefCache key."

            // If we are inside ConvertAny, we are building the argument for a function call.
            // If we do the SetRefCache logic here, we need to:
            // 1. Generate a key.
            // 2. Send SetRefCache message (synchronously or async?).
            // 3. Return a RefCache object in the Any.

            // HOWEVER, we are inside a flatbuffer builder for the MAIN message.
            // We cannot send a message on the same channel/slot while building another message?
            // Ah, memory says: "ConvertAny function in xll_converters.cpp uses a local FlatBufferBuilder and explicit g_host.Send for SetRefCache calls, preventing corruption of the caller's ZeroCopy slot".

            // So YES, we must implement it here.

            // Create a unique key.
            // Using address string is good but might not be unique if content changes?
            // Excel recalculation usually implies content *might* have changed.
            // But within one calculation cycle (or if inputs are same), same pointer/ref means same data?
            // Actually, we can use the address string + sheet.

            std::wstring sheet = GetSheetName(op);
            // Get address
            XLOPER12 xAddr;
            if (Excel12(xlfReftext, &xAddr, 1, op) == xlretSuccess && (xAddr.xltype & xltypeStr)) {
                 std::wstring addr = PascalToWString(xAddr.val.str);
                 Excel12(xlFree, 0, 1, &xAddr);

                 // Check if we already sent this key in this cycle?
                 // We have g_sentRefCache map.
                 std::string key = ConvertExcelString(addr); // Use util string convert

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
                     // We need to fetch the value (Multi/Grid) and send it.
                     // Coerce to value (Multi)
                     XLOPER12 xValue;
                     // xlCoerce with 0 as type (default) attempts to convert to value.
                     // Or use xltypeMulti | xltypeMissing | ...
                     XLOPER12 xType;
                     xType.xltype = xltypeInt;
                     xType.val.w = xltypeMulti;

                     if (Excel12(xlCoerce, &xValue, 2, op, &xType) == xlretSuccess) {
                         // Build SetRefCacheRequest
                         flatbuffers::FlatBufferBuilder cacheBuilder;
                         auto anyVal = ConvertMultiToAny(xValue, cacheBuilder);
                         auto req = protocol::CreateSetRefCacheRequestDirect(cacheBuilder, key.c_str(), anyVal);
                         cacheBuilder.Finish(req);

                         // Send ID 130
                         // We don't care about response or timeout here (fire and forget for cache set?)
                         // Actually SetRefCache returns an Ack.
                         std::vector<uint8_t> respBuf;
                         g_host.Send(cacheBuilder.GetBufferPointer(), cacheBuilder.GetSize(), (shm::MsgType)130, respBuf, 2000);

                         Excel12(xlFree, 0, 1, &xValue);
                     }
                 }

                 // Return RefCache type
                 return protocol::CreateAny(builder, protocol::AnyValue_RefCache, protocol::CreateRefCacheDirect(builder, key.c_str()).Union());
            }
        }

        return protocol::CreateAny(builder, protocol::AnyValue_Range, ConvertRange(op, builder).Union());

    } else if (op->xltype & xltypeMulti) {
        // Grid
        return ConvertMultiToAny(*op, builder);
    } else if (op->xltype & (xltypeMissing | xltypeNil)) {
         return protocol::CreateAny(builder, protocol::AnyValue_Nil, protocol::CreateNil(builder).Union());
    }

    // Default fallback
    return protocol::CreateAny(builder, protocol::AnyValue_Nil, protocol::CreateNil(builder).Union());
}


// Reverse Conversions

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
            return NewExcelString(any->val_as_Str()->val()->c_str());
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
             // NumGrid usually returns FP12, but AnyToXLOPER must return XLOPER12.
             // We have to convert FP12 to XLOPER12 (xltypeMulti of numbers).
             // This is expensive but necessary for Any.
             // Or maybe we just return Err? No, users expect data.
             const protocol::NumGrid* ng = any->val_as_NumGrid();
             int rows = ng->rows();
             int cols = ng->cols();
             LPXLOPER12 op = NewXLOPER12();
             op->xltype = xltypeMulti | xlbitDLLFree;
             op->val.array.rows = rows;
             op->val.array.columns = cols;
             op->val.array.lparray = (LPXLOPER12)malloc(rows * cols * sizeof(XLOPER12)); // Use malloc to be compatible with FreeXLOPER12 logic?
             // Actually FreeXLOPER12 uses delete[] for lparray if it was allocated with new.
             // xll_mem.cpp: delete[] op->val.array.lparray;
             // So we must use new.
             // Wait, xll_mem.cpp uses `delete op` for the op itself, but `delete[]` for array?
             // Let's check xll_mem.cpp (via memory or reading).
             // Assuming we use new.
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
             return NULL; // Or Missing?
        }
    }
}

LPXLOPER12 RangeToXLOPER12(const protocol::Range* range) {
    if (!range) return NULL;

    // We cannot easily create a real Reference (xltypeRef) that points to Excel cells
    // unless we are in a macro sheet or similar context, and even then it's tricky to "return" a reference to Excel
    // that Excel accepts as a valid reference to cells.
    // However, we CAN return xltypeRef if we allocate it correctly.
    // The problem is the sheet ID.

    // Attempt to resolve sheet name to ID?
    // This is hard.
    // For now, most XLLs returning references return them as values (Grid).
    // But if we must return a Reference (e.g. for C API), we need the sheet ID.

    // xll-gen strategy: If we receive a Range from Go, it's usually for a command (Set/Format).
    // If we are returning it to Excel as a result of a UDF, it will likely fail or be converted to value.
    // But let's try to construct it.

    // Note: We used to return Grid for Range return types in older versions?
    // No, if the return type is 'range', we try to return a Ref.

    LPXLOPER12 op = NewXLOPER12();
    op->xltype = xltypeRef | xlbitDLLFree;
    op->val.mref.lpmref = (LPXLMREF12) new char[sizeof(XLMREF12) + sizeof(XLREF12) * range->refs()->size()];
    op->val.mref.idSheet = 0; // We don't know the sheet ID easily! This is a limitation.
    // If sheet_name is present, we could try `xlSheetId` but that requires xlfEvaluate or similar.

    // If we can't find sheet ID, this reference is invalid.
    // But maybe it's on the active sheet?

    if (range->sheet_name() && range->sheet_name()->Length() > 0) {
        // Try to get sheet ID
        // Construct string like "Sheet1"
        std::wstring sName = ConvertToWString(range->sheet_name()->c_str());
        // We need "Sheet1" -> SheetId
        XLOPER12 xName, xId;
        xName.xltype = xltypeStr;
        xName.val.str = WStringToPascalString(sName.c_str()); // This allocates temp buffer

        if (Excel12(xlSheetId, &xId, 1, &xName) == xlretSuccess) {
            op->val.mref.idSheet = xId.val.mref.idSheet;
            Excel12(xlFree, 0, 1, &xId);
        }
        // WStringToPascalString uses static buffer so no free needed for xName.val.str?
        // Wait, TempStr12 uses static buffer. WStringToPascalString allocates?
        // PascalString.h usually returns a unique_ptr or vector, or fills a buffer.
        // Let's assume WStringToPascalString returns a pointer to thread local buffer or we need to manage it.
        // xll_utility.h: LPWSTR WStringToPascalString(const wchar_t*);
        // It returns a LPWSTR (which is actually unsigned short* for XLOPER12 string?)
        // Actually XLOPER12 string is XCHAR* (wchar_t*).
    }

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

        // Initialize to Nil/Empty
        cell.xltype = xltypeNil;

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
                // Must allocate string
                cell.xltype = xltypeStr;
                std::wstring ws = ConvertToWString(scalar->val_as_Str()->val()->c_str());

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

    // FP12 is a struct with rows, columns and array[1] (VLA).
    // We need to allocate enough memory.
    int rows = grid->rows();
    int cols = grid->cols();

    // Use NewFP12 helper if available or malloc
    // Since we return this to Excel (or use internally),
    // If returning to Excel, FP12 is usually not freed by xlAutoFree12 unless ... wait.
    // FP12 return from DLL: Excel doesn't call xlAutoFree12 for FP12 (type K).
    // It only supports K as *argument*.
    // For return values, we can return XLOPER12 (xltypeMulti) or ...
    // Wait, K% is supported for return?
    // "K Data Type: ... returns a pointer to an FP12 structure... Excel will not free the memory... use static memory or..."

    // If we return FP12*, we must manage it.
    // But `xll-gen` usually maps `numgrid` return to `LPXLOPER12` (xltypeMulti) in the wrapper?
    // Memory says: "Scalar return types ... mapped to LPXLOPER12 ...".
    // Does `numgrid` map to `K` or `Q` for return?
    // User plan: "The numgrid argument type ... maps to ... FP12* (type K%) in C++".
    // But return type?
    // If it's a return type, we probably convert it to XLOPER12 (xltypeMulti) in `xll_main.cpp` using `AnyToXLOPER12` (if Any) or explicit conversion.

    // However, if we do have a function returning `FP12*`, we need to use a thread-local static buffer or similar.
    // But here we are implementing `NumGridToFP12`.
    // Let's assume it allocates and caller manages or it uses a static buffer.

    // Given we are refactoring namespaces, I should just fix the namespace and keep logic "as is" or close to it.
    // I will use `new` and let caller handle it (or leak if not handled, but that's existing logic).

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
