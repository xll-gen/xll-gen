#include "include/xll_commands.h"
#include "include/xll_converters.h"
#include "include/xll_utility.h"
#include <string>

bool IsSingleCell(LPXLOPER12 xRef) {
    if (!xRef) return false;
    int type = xRef->xltype & ~(xlbitXLFree | xlbitDLLFree);
    if (type == xltypeSRef) {
         const auto& r = xRef->val.sref.ref;
         return (r.rwFirst == r.rwLast && r.colFirst == r.colLast);
    } else if (type == xltypeRef) {
         const auto* m = xRef->val.mref.lpmref;
         if (m && m->count == 1) {
             const auto& r = m->reftbl[0];
             return (r.rwFirst == r.rwLast && r.colFirst == r.colLast);
         }
    }
    return false;
}

void ExecuteCommands(const ipc::CalculationEndedResponse* resp) {
    if (!resp || !resp->commands()) return;
    auto wrappers = resp->commands();
    for (auto it = wrappers->begin(); it != wrappers->end(); ++it) {
        if (it->cmd_type() == ipc::Command_SetCommand) {
            auto cmd = static_cast<const ipc::SetCommand*>(it->cmd());
            LPXLOPER12 xRef = RangeToXLOPER12(cmd->target());
            LPXLOPER12 xVal = AnyToXLOPER12(cmd->value());
            if (xRef && xVal) {
                Excel12(xlSet, 0, 2, xRef, xVal);
            }
            if (xRef) xlAutoFree12(xRef);
            if (xVal) xlAutoFree12(xVal);
        } else if (it->cmd_type() == ipc::Command_FormatCommand) {
            auto cmd = static_cast<const ipc::FormatCommand*>(it->cmd());
            LPXLOPER12 xRef = RangeToXLOPER12(cmd->target());
            if (xRef) {
                bool doFormat = true;
                std::string fmt = cmd->format()->str();
                std::wstring wfmt = StringToWString(fmt);

                if (IsSingleCell(xRef)) {
                    XLOPER12 xFmt;
                    if (Excel12(xlfGetCell, &xFmt, 2, TempInt12(7), xRef) == xlretSuccess) {
                        if (xFmt.xltype == xltypeStr && xFmt.val.str) {
                            size_t len = (size_t)xFmt.val.str[0];
                            if (len == wfmt.length()) {
                                if (wcsncmp(xFmt.val.str + 1, wfmt.c_str(), len) == 0) {
                                    doFormat = false;
                                }
                            }
                        }
                        Excel12(xlFree, 0, 1, &xFmt);
                    }
                }

                if (doFormat) {
                    XLOPER12 xOldSel;
                    if (Excel12(xlfSelection, &xOldSel, 0) == xlretSuccess) {
                        Excel12(xlcSelect, 0, 1, xRef);
                        Excel12(xlcFormatNumber, 0, 1, TempStr12(wfmt.c_str()));
                        Excel12(xlcSelect, 0, 1, &xOldSel);
                        Excel12(xlFree, 0, 1, &xOldSel);
                    }
                }
                xlAutoFree12(xRef);
            }
        }
    }
}
