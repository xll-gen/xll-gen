#pragma once
#include "xlcall.h"
#include "schema_generated.h"
#include <flatbuffers/flatbuffers.h>

flatbuffers::Offset<ipc::types::Range> ConvertRange(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<ipc::types::Scalar> ConvertScalar(const XLOPER12& cell, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<ipc::types::Grid> ConvertGrid(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<ipc::types::NumGrid> ConvertNumGrid(FP12* fp, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<ipc::types::Any> ConvertAny(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder);

// Helper for internal use (also exported if needed)
std::wstring GetSheetName(LPXLOPER12 pxRef);
flatbuffers::Offset<ipc::types::Any> ConvertMultiToAny(const XLOPER12& xMulti, flatbuffers::FlatBufferBuilder& builder);
