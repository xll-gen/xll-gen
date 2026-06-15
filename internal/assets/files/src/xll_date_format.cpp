#include "xll_date_format.h"
#include "xll_excel.h"          // xll::CallExcel
#include "types/utility.h"      // IsDateLikeFormat, PascalToWString
#include "types/ScopedXLOPER12.h"

namespace xll {

PendingDateFormats& PendingDateFormats::Instance() {
    static PendingDateFormats inst;
    return inst;
}

void PendingDateFormats::Enqueue(const std::vector<PendingFormat>& items) {
    if (items.empty()) return;
    std::lock_guard<std::mutex> lock(m_mutex);
    m_pending.insert(m_pending.end(), items.begin(), items.end());
}

std::vector<PendingFormat> PendingDateFormats::Drain() {
    std::lock_guard<std::mutex> lock(m_mutex);
    std::vector<PendingFormat> out;
    out.swap(m_pending);
    return out;
}

void ScheduleDateFormatsForCaller(const protocol::Any* result) {
    try {
        std::vector<DateCell> cells;
        CollectDateCells(result, cells);
        if (cells.empty()) return;

        ScopedXLOPER12 xCaller;
        if (xll::CallExcel(xlfCaller, xCaller) != xlretSuccess) return;

        IDSHEET idSheet = 0;
        XLREF12 anchor{};
        const DWORD t = xCaller.get()->xltype & ~(xlbitDLLFree | xlbitXLFree);
        if (t & xltypeSRef) {
            anchor = xCaller.get()->val.sref.ref;
        } else if ((t & xltypeRef) && xCaller.get()->val.mref.lpmref &&
                   xCaller.get()->val.mref.lpmref->count > 0) {
            idSheet = xCaller.get()->val.mref.idSheet;
            anchor = xCaller.get()->val.mref.lpmref->reftbl[0];
        } else {
            return;
        }

        std::vector<PendingFormat> items;
        items.reserve(cells.size());
        for (const auto& c : cells) {
            PendingFormat pf;
            pf.idSheet = idSheet;
            pf.ref.rwFirst = pf.ref.rwLast = anchor.rwFirst + c.rowOff;
            pf.ref.colFirst = pf.ref.colLast = anchor.colFirst + c.colOff;
            pf.format = c.format;
            items.push_back(std::move(pf));
        }
        PendingDateFormats::Instance().Enqueue(items);
    } catch (...) { /* never throw into the wrapper */ }
}

// Builds a single-cell xltypeRef XLOPER12 pointing at pf's cell. `mref` must
// outlive `out` (it backs out.val.mref.lpmref) — both are stack locals in the
// drain loop below.
static void MakeCellRef(const PendingFormat& pf, XLOPER12& out, XLMREF12& mref) {
    mref.count = 1;
    mref.reftbl[0] = pf.ref;
    out.xltype = xltypeRef;
    out.val.mref.idSheet = pf.idSheet;
    out.val.mref.lpmref = &mref;
}

void DrainAndApplyDateFormats() {
    auto items = PendingDateFormats::Instance().Drain();
    for (const auto& pf : items) {
        try {
            XLOPER12 ref; XLMREF12 mref;
            MakeCellRef(pf, ref, mref);

            // Skip when the cell already carries a date/time number format —
            // mirrors the FormatCommand "already formatted" guard in
            // src/xll_commands.cpp (xlfGetCell type 7 -> current format string).
            XLOPER12 xType; xType.xltype = xltypeInt; xType.val.w = 7;
            ScopedXLOPER12Result xFmt;
            if (xll::CallExcel(xlfGetCell, xFmt, &xType, &ref) == xlretSuccess &&
                xFmt->xltype == xltypeStr) {
                std::wstring cur = PascalToWString(xFmt->val.str);
                if (IsDateLikeFormat(cur)) continue;
            }

            xll::CallExcel(xlcSelect, nullptr, &ref);
            xll::CallExcel(xlcFormatNumber, nullptr, pf.format);
        } catch (...) { /* skip this cell; never break calc-end */ }
    }
}

} // namespace xll
