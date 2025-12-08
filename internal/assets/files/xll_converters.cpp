#include "xll_converters.h"
#include <vector>
#include <sstream>

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
                      int ok;
                      if (reqB.GetSize() > 950 * 1024) {
                          // Uses SendChunked from xll_ipc.h
                          ok = SendChunked(g_host, reqB.GetBufferPointer(), reqB.GetSize(), respBuf);
                      } else {
                          // MSG_SETREFCACHE (129)
                          ok = g_host.Send(reqB.GetBufferPointer(), reqB.GetSize(), MSG_SETREFCACHE, respBuf);
                      }

                      if (ok == MSG_ACK && !respBuf.empty()) {
                          auto ack = ipc::GetAck(respBuf.data());
                          if (ack && ack->ok()) {
                              g_sentRefCache[key] = true;
                              cached = true;
                          }
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
