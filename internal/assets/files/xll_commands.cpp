#include "xll_ipc.h"
#include "xll_converters.h"
#include "xll_utility.h"
#include <vector>
#include <string>

// Execute commands received from Go (e.g. Set, Format)
// These are synchronous commands sent in response to CalculationEnded or similar.
// Or we might queue them.

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
                // xlpsFormat (type 1) of xlcFormatNumber?
                // xlcFormatNumber(format_string, reference) - NO, xlcFormatNumber takes format string and applies to SELECTION.
                // We should use xlSet to set cell properties? No.
                // We must select the range then format?
                // Or use xlfSetFormat? (Does not exist).
                // Usually one selects and calls xlcFormatNumber.

                // Optimization: Skip if already formatted? (See memory)
                // "ExecuteCommands function includes an optimization to skip xlcFormatNumber execution if the target range is a single cell and its existing format matches".

                if (pxRef) {
                    bool skip = false;
                    // Check if single cell
                    if (IsSingleCell(pxRef)) { // xll_utility.h
                         // Check existing format
                         // xlfGetCell(7, ref) returns format string
                         XLOPER12 xTypeId, xFmt;
                         xTypeId.xltype = xltypeInt;
                         xTypeId.val.w = 7;
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
                        XLOPER12 xFmtStr;
                        xFmtStr.xltype = xltypeStr;
                        // Use temporary buffer for format string
                        std::wstring ws = ConvertToWString(cmd->format()->c_str());
                        xFmtStr.val.str = WStringToNewPascalString(ws.c_str()); // Need to free?
                        // WStringToNewPascalString allocates with new char[].
                        // We should free it.
                        // Actually, we can use a helper that returns an object, or manually manage.
                        // Or just use NewExcelString but we don't need the OP.

                        // Let's use `NewExcelString` and free it.
                        LPXLOPER12 pxFmt = NewExcelString(ws.c_str());
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
