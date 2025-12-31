#include "xll_commands.h"
#include "xll_ipc.h"
#include "xll_excel.h"
#include "types/converters.h"
#include "types/utility.h"
#include "types/mem.h"
#include <vector>
#include <string>

// Execute commands received from Go (e.g. Set, Format)

void ExecuteCommands(const flatbuffers::Vector<flatbuffers::Offset<protocol::CommandWrapper>>* commands) {
    if (!commands) return;

    for (const auto* wrapper : *commands) {
        if (!wrapper) continue;

        switch (wrapper->cmd_type()) {
            case protocol::Command::SetCommand: {
                const auto* cmd = wrapper->cmd_as_SetCommand();
                if (!cmd || !cmd->target()) continue;

                LPXLOPER12 pxRef = RangeToXLOPER12(cmd->target());
                LPXLOPER12 pxValue = AnyToXLOPER12(cmd->value());

                if (pxRef && pxValue) {
                     // xlSet
                     xll::CallExcel(xlSet, nullptr, pxRef, pxValue);
                }

                if (pxRef) {
                    if (pxRef->xltype & xlbitDLLFree) xlAutoFree12(pxRef);
                    else ReleaseXLOPER12(pxRef);
                }
                if (pxValue) {
                    if (pxValue->xltype & xlbitDLLFree) xlAutoFree12(pxValue);
                    else ReleaseXLOPER12(pxValue);
                }
                break;
            }
            case protocol::Command::FormatCommand: {
                const auto* cmd = wrapper->cmd_as_FormatCommand();
                if (!cmd || !cmd->target() || !cmd->format()) continue;

                LPXLOPER12 pxRef = RangeToXLOPER12(cmd->target());

                if (pxRef) {
                    bool skip = false;
                    // Optimization: Skip if already formatted and is single cell
                    if (IsSingleCell(pxRef)) {
                         XLOPER12 xTypeId;
                         ScopedXLOPER12Result xFmt;
                         xTypeId.xltype = xltypeInt;
                         xTypeId.val.w = 7; // xlfGetCell type 7: format
                         if (xll::CallExcel(xlfGetCell, xFmt, &xTypeId, pxRef) == xlretSuccess) {
                             if (xFmt->xltype == xltypeStr) {
                                 std::wstring currentFmt = PascalToWString(xFmt->val.str);
                                 std::wstring newFmt = ConvertToWString(cmd->format()->c_str());
                                 if (currentFmt == newFmt) {
                                     skip = true;
                                 }
                             }
                         }
                    }

                    if (!skip) {
                        // Select range
                        xll::CallExcel(xlcSelect, nullptr, pxRef);

                        // Apply format
                        std::wstring ws = ConvertToWString(cmd->format()->c_str());

                        // Use generic CallExcel which handles strings via ScopedXLOPER12
                        // Note: NewExcelString allocates XLOPER12 with xlbitDLLFree,
                        // while CallExcel's ScopedXLOPER12 handles temporary strings for inputs.
                        // Here we use CallExcel(..., ws) which is cleaner.
                        xll::CallExcel(xlcFormatNumber, nullptr, ws);
                    }

                    if (pxRef->xltype & xlbitDLLFree) xlAutoFree12(pxRef);
                    else ReleaseXLOPER12(pxRef);
                }
                break;
            }
            default:
                break;
        }
    }
}
