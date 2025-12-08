#ifndef XLL_CONVERTERS_H
#define XLL_CONVERTERS_H

#include "xll_utility.h"
#include "xll_ipc.h"
#include "schema_generated.h"
#include <mutex>

// Forward declaration of globals needed by cache logic
// We can pass them as arguments or extern them.
// g_host is needed for SetRefCache.
extern shm::DirectHost g_host;
extern std::map<std::string, bool> g_sentRefCache;
extern std::mutex g_refCacheMutex;

flatbuffers::Offset<ipc::types::Range> ConvertRange(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<ipc::types::Scalar> ConvertScalar(const XLOPER12& cell, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<ipc::types::Any> ConvertMultiToAny(const XLOPER12& xMulti, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<ipc::types::NumGrid> ConvertNumGrid(FP12* fp, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<ipc::types::Grid> ConvertGrid(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<ipc::types::Any> ConvertAny(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder);

#endif // XLL_CONVERTERS_H
