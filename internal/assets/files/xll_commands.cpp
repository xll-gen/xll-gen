#include "xll_ipc.h"
#include "xll_converters.h"
#include "xll_utility.h"
#include "xll_mem.h"
#include "PascalString.h"
#include <vector>
#include <string>

// Execute commands received from Go (e.g. Set, Format)

void ExecuteCommands(const flatbuffers::Vector<flatbuffers::Offset<protocol::CommandWrapper>>* commands) {
    if (!commands) return;

    for (const auto* wrapper : *commands) {
        if (!wrapper) continue;

        switch (wrapper->cmd_type()) {
            case protocol::Command_SetCommand: {
                const auto* cmd = wrapper->cmd_as_SetCommand();
                if (!cmd || !cmd->target()) continue;

                LPXLOPER12 pxRef = RangeToXLOPER12(cmd->target());
                LPXLOPER12 pxValue = AnyToXLOPER12(cmd->value());

                if (pxRef && pxValue) {
                     // xlSet
                     Excel12(xlSet, 0, 2, pxRef, pxValue);
                }

                if (pxRef) xlAutoFree12(pxRef);
                if (pxValue) xlAutoFree12(pxValue);
                break;
            }
            case protocol::Command_FormatCommand: {
                const auto* cmd = wrapper->cmd_as_FormatCommand();
                if (!cmd || !cmd->target() || !cmd->format()) continue;

                LPXLOPER12 pxRef = RangeToXLOPER12(cmd->target());

                if (pxRef) {
                    bool skip = false;
                    // Optimization: Skip if already formatted and is single cell
                    if (IsSingleCell(pxRef)) {
                         XLOPER12 xTypeId, xFmt;
                         xTypeId.xltype = xltypeInt;
                         xTypeId.val.w = 7; // xlfGetCell type 7: format
                         if (Excel12(xlfGetCell, &xFmt, 2, &xTypeId, pxRef) == xlretSuccess) {
                             if (xFmt.xltype == xltypeStr) {
                                 std::wstring currentFmt = PascalToWString(xFmt.val.str);
                                 std::wstring newFmt = ConvertToWString(cmd->format()->c_str());
                                 if (currentFmt == newFmt) {
                                     skip = true;
                                 }
                             }
                             Excel12(xlFree, 0, 1, &xFmt);
                         }
                    }

                    if (!skip) {
                        // Select range
                        Excel12(xlcSelect, 0, 1, pxRef);

                        // Apply format
                        std::wstring ws = ConvertToWString(cmd->format()->c_str());
                        LPXLOPER12 pxFmt = NewExcelString(ws); // Pass wstring directly

                        Excel12(xlcFormatNumber, 0, 1, pxFmt);
                        xlAutoFree12(pxFmt);
                    }

                    xlAutoFree12(pxRef);
                }
                break;
            }
            default:
                break;
        }
    }
}
