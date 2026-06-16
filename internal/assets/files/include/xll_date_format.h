#pragma once

// xll_date_format.h — process-global queue of pending date number-format
// requests plus the calc-context producer and calc-end consumer.
//
// Plan B / Task 3: when a value-materializing wrapper produces a result that
// contains Date cells, ScheduleDateFormatsForCaller (called ON A CALC THREAD)
// captures xlfCaller as the anchor and enqueues one PendingFormat per date cell
// into the single global PendingDateFormats queue. At calc-end — actually on the
// deferred runner the STA thread schedules via xlcOnTime, OUTSIDE the
// CalculationEnded event — DrainAndApplyDateFormats empties the queue and
// applies each format via COM Range.NumberFormat (NOT xlcSelect/xlcFormatNumber).
//
// WHY COM (NO SELECTION CHANGE): the C-API xlcSelect/xlcFormatNumber pair
// operates on Excel's ACTIVE selection — it must first SELECT the target range,
// which steals the user's active cell/selection and makes the screen flicker on
// every recalc that touches a fresh date cell. The COM route
// (Application -> Workbooks(book) -> Worksheets(sheet) -> Range(a1) ->
// NumberFormat = fmt) targets the cells directly and leaves the user's active
// cell and selection completely untouched. This matches the Excel-DNA / xlOil
// guidance that off-selection formatting must go through a COM Range. The
// Application IDispatch is acquired via the shared header-only
// xll::com::AcquireExcelApplication (include/com/excel_app.h), once per drain.
//
// ONCE-PER-CELL (perf, 2026-06-15): a recalc fires on EVERY keystroke, so the
// drain runs constantly while a user types. The old drain issued a SYNCHRONOUS
// xlfGetCell (type 7) COM round-trip PER pending date cell to read its current
// number format and conditionally skip already-date-formatted cells — O(N)
// UI-thread COM calls per keystroke, which made typing laggy on workbooks with
// many date cells. That conditional-skip read is GONE. Instead PendingDateFormats
// owns a process-global, mutex-guarded "formatted set" keyed by
// (idSheet,row,col). The FIRST time a date cell is seen we apply the auto-format
// UNCONDITIONALLY (overriding any pre-existing user format that one time), then
// mark the cell in the set; ALL subsequent recalcs do ZERO COM work for it. The
// once-per-cell rule matters even more now that the apply is a COM round-trip:
// the drain still issues at most one Range.NumberFormat per never-before-seen
// rectangle, never per keystroke for an already-formatted cell.
// The producer also consults the set so it never re-enqueues an already-formatted
// cell, keeping the pending vector tiny.
//
// INTENTIONAL BEHAVIOR CHANGE (user-confirmed): value-driven formatting wins —
// a date displays as a date. First touch replaces any pre-existing custom format
// (e.g. a hand-set "dd/mm/yyyy") with the auto-format exactly once.
//
// SET LIFETIME / TRADEOFF: the formatted set is NOT cleared at CalculationEnded;
// it persists for the loaded-DLL lifetime (memory bounded by the number of
// DISTINCT date cells touched in the session). The tradeoff: a cell's format is
// never upgraded later (e.g. a cell that first appears as a pure date and later
// gains a time-of-day fraction keeps yyyy-mm-dd, not yyyy-mm-dd hh:mm:ss). This
// is acceptable and matches prior practical behavior (the old guard also skipped
// any cell already carrying a date-like format).
//
// THREADING: producers run on Excel calc threads; the drain runs on the STA
// thread via the calc-end deferred runner (xlcOnTime), OUTSIDE the
// CalculationEnded event — a command context where both the C-API sheet-name
// lookup (xlSheetNm) and COM (Range.NumberFormat) are legal and safe. All access
// to the queue AND the formatted set goes through a single mutex, the same
// discipline as the RTD registries (see include/xll_rtd_once_grid.h).
//
// This module is NOT gated by XLL_RTD_ENABLED: sync (non-RTD) date formatting
// must work in every build, so its TU is compiled unconditionally via the
// file(GLOB SOURCES ... src/*.cpp) sweep in the generated CMakeLists.

#include <windows.h>
#include <vector>
#include <string>
#include <mutex>
#include <set>
#include <tuple>
#include "types/converters.h" // protocol::Any, DateCell

namespace xll {

struct PendingFormat {
    IDSHEET idSheet;      // 0 for SRef (current sheet); sheet id for external Ref
    XLREF12 ref;          // single-cell rect
    std::wstring format;
};

// Process-global queue of pending date-format requests plus the persistent
// "formatted set" (see file header: once-per-cell). Single Meyers singleton;
// all access is mutex-guarded.
class PendingDateFormats {
public:
    static PendingDateFormats& Instance();
    void Enqueue(const std::vector<PendingFormat>& items);
    std::vector<PendingFormat> Drain();

    // Whether any format requests are queued. Used by the calc-end deferred
    // runner scheduler so it can wake the runner when only date formats (and no
    // SetCommand/FormatCommand) are pending. Mutex-guarded like the rest.
    bool HasPending();

    // Once-per-cell guard. A cell is identified by (idSheet,row,col). Once
    // MarkFormatted is called for a cell, AlreadyFormatted returns true for the
    // rest of the loaded-DLL lifetime, so neither the producer nor the drain
    // ever issues a COM call for it again. Both lock m_mutex — the same mutex
    // that guards m_pending — because the set and the queue are mutated from the
    // same producer (calc thread) / drain (STA thread) sites and one lock keeps
    // the discipline simple and correct (no second-lock ordering to reason
    // about).
    bool AlreadyFormatted(IDSHEET idSheet, int row, int col);
    void MarkFormatted(IDSHEET idSheet, int row, int col);
private:
    PendingDateFormats() = default;
    ~PendingDateFormats() = default;
    PendingDateFormats(const PendingDateFormats&) = delete;
    PendingDateFormats& operator=(const PendingDateFormats&) = delete;

    std::mutex m_mutex;
    std::vector<PendingFormat> m_pending;
    std::set<std::tuple<IDSHEET, int, int>> m_formatted;
};

// Calc-context helper: if `result` contains any Date cells, capture xlfCaller
// (the anchor) and enqueue a format request for each date cell NOT already in
// the formatted set. No-op when there are no dates. NEVER throws.
void ScheduleDateFormatsForCaller(const protocol::Any* result);

// Grid overload: sync grid-return wrappers hold a bare `protocol::Grid*`
// (their ipc Response.result is typed `protocol.Grid`, not `protocol.Any`),
// so they cannot reach the Any entry point above. Same contract: no-op when
// the grid carries no dates; NEVER throws.
void ScheduleDateFormatsForCaller(const protocol::Grid* grid);

// Calc-end helper: drain the queue; drop cells already in the formatted set,
// GROUP the rest by (idSheet, format) — never merging different sheets or
// formats — then greedy-mesh each group's (row,col) cells into rectangular
// blocks (see include/xll_greedy_mesh.h). The in-process Application IDispatch
// is acquired ONCE per drain (xll::com::AcquireExcelApplication); each idSheet
// is resolved to a worksheet name via the C-API xlSheetNm and then to a COM
// Worksheet object (cached per drain). Each rectangle is formatted with ONE COM
// assignment — Worksheet.Range(a1).NumberFormat = fmt — which does NOT touch the
// user's selection (no xlcSelect/xlcFormatNumber, no xlfGetCell read). On
// success every cell in the rectangle is marked formatted; on ANY failure
// (no Application yet, unresolvable sheet, COM error) none are, so the rect
// retries next cycle (self-heal). The common YDH case — one contiguous date
// column, identical format — collapses to a single Range.NumberFormat op. All
// COM objects/VARIANTs/BSTRs are released on every path. NEVER throws into
// calc-end.
void DrainAndApplyDateFormats();

} // namespace xll
