#pragma once
#include <windows.h>
#include "xlcall.h"
#include "protocol_generated.h" // Needed for protocol:: types
#include "schema_generated.h"
#include <flatbuffers/flatbuffers.h>

// Excel -> Flatbuffers
flatbuffers::Offset<protocol::Range> ConvertRange(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder, const std::string& format = "");
flatbuffers::Offset<protocol::Scalar> ConvertScalar(const XLOPER12& cell, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<protocol::Grid> ConvertGrid(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<protocol::NumGrid> ConvertNumGrid(FP12* fp, flatbuffers::FlatBufferBuilder& builder);
flatbuffers::Offset<protocol::Any> ConvertAny(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder);

// Flatbuffers -> Excel
LPXLOPER12 AnyToXLOPER12(const protocol::Any* any);
LPXLOPER12 RangeToXLOPER12(const protocol::Range* range);
LPXLOPER12 GridToXLOPER12(const protocol::Grid* grid);
FP12* NumGridToFP12(const protocol::NumGrid* grid);

// Helper for internal use (also exported if needed)
std::wstring GetSheetName(LPXLOPER12 pxRef);
flatbuffers::Offset<protocol::Any> ConvertMultiToAny(const XLOPER12& xMulti, flatbuffers::FlatBufferBuilder& builder);
