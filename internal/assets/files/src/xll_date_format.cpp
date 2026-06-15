#include "xll_date_format.h"
#include "xll_excel.h"          // xll::CallExcel
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

bool PendingDateFormats::AlreadyFormatted(IDSHEET idSheet, int row, int col) {
    std::lock_guard<std::mutex> lock(m_mutex);
    return m_formatted.count(std::make_tuple(idSheet, row, col)) != 0;
}

void PendingDateFormats::MarkFormatted(IDSHEET idSheet, int row, int col) {
    std::lock_guard<std::mutex> lock(m_mutex);
    m_formatted.insert(std::make_tuple(idSheet, row, col));
}

// Shared back half of the producer: given date cells already collected on the
// calc thread, capture xlfCaller as the anchor and enqueue one PendingFormat
// per cell. No-op when `cells` is empty; NEVER throws.
static void EnqueueDateFormatsForCaller(const std::vector<DateCell>& cells) {
    try {
        if (cells.empty()) return;

        ScopedXLOPER12 xCaller;
        if (xll::CallExcel(xlfCaller, xCaller) != xlretSuccess) return;

        IDSHEET idSheet = 0;
        XLREF12 anchor{};
        const DWORD t = xCaller.get()->xltype & ~(xlbitDLLFree | xlbitXLFree);
        if (t & xltypeSRef) {
            anchor = xCaller.get()->val.sref.ref;
            // SRef is relative to the calling cell's sheet and carries no
            // idSheet. Resolve the concrete IDSHEET now (on the calc thread,
            // where it is legal) so the CalculationEnded drain targets the
            // right sheet even if the active sheet changed by then. No-arg
            // xlSheetId returns an xltypeRef for the active sheet; mirrors
            // LookupSheetName in types/src/converters.cpp.
            ScopedXLOPER12Result xSheetId;
            if (xll::CallExcel(xlSheetId, xSheetId) == xlretSuccess &&
                ((xSheetId.get()->xltype & ~(xlbitDLLFree | xlbitXLFree)) & xltypeRef)) {
                idSheet = xSheetId.get()->val.mref.idSheet;
            }
            // If resolution fails, idSheet stays 0: the item is still enqueued
            // with key (0,row,col), but xlcSelect on an idSheet==0 xltypeRef is
            // not a valid sheet handle, so the drain's select fails -> the cell
            // is NOT marked -> it self-heals on the next recalc rather than
            // formatting the wrong sheet. (xlSheetId failing on a live calc
            // thread is near-impossible, so this path is effectively dead.)
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
            const int row = anchor.rwFirst + c.rowOff;
            const int col = anchor.colFirst + c.colOff;
            // Once-per-cell: never re-enqueue a cell the drain has already
            // formatted, so the pending vector stays tiny across recalcs.
            if (PendingDateFormats::Instance().AlreadyFormatted(idSheet, row, col))
                continue;
            PendingFormat pf;
            pf.idSheet = idSheet;
            pf.ref.rwFirst = pf.ref.rwLast = row;
            pf.ref.colFirst = pf.ref.colLast = col;
            pf.format = c.format;
            items.push_back(std::move(pf));
        }
        PendingDateFormats::Instance().Enqueue(items);
    } catch (...) { /* never throw into the wrapper */ }
}

void ScheduleDateFormatsForCaller(const protocol::Any* result) {
    try {
        std::vector<DateCell> cells;
        CollectDateCells(result, cells);
        EnqueueDateFormatsForCaller(cells);
    } catch (...) { /* never throw into the wrapper */ }
}

void ScheduleDateFormatsForCaller(const protocol::Grid* grid) {
    try {
        // Sync grid wrappers hand us a bare Grid (no Any wrapper). Reuse the
        // Grid overload of CollectDateCells so date-format derivation stays
        // centralized in types/converters.cpp.
        std::vector<DateCell> cells;
        CollectDateCells(grid, cells);
        EnqueueDateFormatsForCaller(cells);
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
    auto& inst = PendingDateFormats::Instance();
    auto items = inst.Drain();
    for (const auto& pf : items) {
        const int row = pf.ref.rwFirst;
        const int col = pf.ref.colFirst;
        // Once-per-cell: a cell already in the formatted set costs ZERO COM
        // work — this is the hot path on every keystroke recalc. (The producer
        // already filters, but a cell could be enqueued twice within one cycle,
        // or marked by a prior drain, so re-check here too.)
        if (inst.AlreadyFormatted(pf.idSheet, row, col)) continue;
        try {
            XLOPER12 ref; XLMREF12 mref;
            MakeCellRef(pf, ref, mref);

            // First touch: apply the auto-format UNCONDITIONALLY (no xlfGetCell
            // read, no IsDateLikeFormat check). Value-driven formatting wins:
            // this overrides any pre-existing user format exactly once. On
            // success mark the cell so all later recalcs skip it; on failure do
            // NOT mark, so the cell is retried next cycle.
            if (xll::CallExcel(xlcSelect, nullptr, &ref) == xlretSuccess &&
                xll::CallExcel(xlcFormatNumber, nullptr, pf.format) == xlretSuccess) {
                inst.MarkFormatted(pf.idSheet, row, col);
            }
        } catch (...) { /* skip this cell; never break calc-end */ }
    }
}

} // namespace xll
