#pragma once

// xll_date_format.h — process-global queue of pending date number-format
// requests plus the calc-context producer and calc-end consumer.
//
// Plan B / Task 3: when a value-materializing wrapper produces a result that
// contains Date cells, ScheduleDateFormatsForCaller (called ON A CALC THREAD)
// captures xlfCaller as the anchor and enqueues one PendingFormat per date cell
// into the single global PendingDateFormats queue. At CalculationEnded (on the
// main STA thread) DrainAndApplyDateFormats empties the queue and, for each
// cell that is NOT already date/time-formatted, applies the format via
// xlcSelect + xlcFormatNumber — mirroring the conditional FormatCommand idiom in
// src/xll_commands.cpp.
//
// THREADING: producers run on Excel calc threads; the drain runs on the STA
// thread at calc-end. All access to the queue goes through a single mutex, the
// same discipline as the RTD registries (see include/xll_rtd_once_grid.h).
//
// This module is NOT gated by XLL_RTD_ENABLED: sync (non-RTD) date formatting
// must work in every build, so its TU is compiled unconditionally via the
// file(GLOB SOURCES ... src/*.cpp) sweep in the generated CMakeLists.

#include <windows.h>
#include <vector>
#include <string>
#include <mutex>
#include "types/converters.h" // protocol::Any, DateCell

namespace xll {

struct PendingFormat {
    IDSHEET idSheet;      // 0 for SRef (current sheet); sheet id for external Ref
    XLREF12 ref;          // single-cell rect
    std::wstring format;
};

// Process-global queue of pending date-format requests. Single Meyers
// singleton; all access is mutex-guarded.
class PendingDateFormats {
public:
    static PendingDateFormats& Instance();
    void Enqueue(const std::vector<PendingFormat>& items);
    std::vector<PendingFormat> Drain();
private:
    PendingDateFormats() = default;
    ~PendingDateFormats() = default;
    PendingDateFormats(const PendingDateFormats&) = delete;
    PendingDateFormats& operator=(const PendingDateFormats&) = delete;

    std::mutex m_mutex;
    std::vector<PendingFormat> m_pending;
};

// Calc-context helper: if `result` contains any Date cells, capture xlfCaller
// (the anchor) and enqueue conditional format requests for each date cell.
// No-op when there are no dates. NEVER throws.
void ScheduleDateFormatsForCaller(const protocol::Any* result);

// Grid overload: sync grid-return wrappers hold a bare `protocol::Grid*`
// (their ipc Response.result is typed `protocol.Grid`, not `protocol.Any`),
// so they cannot reach the Any entry point above. Same contract: no-op when
// the grid carries no dates; NEVER throws.
void ScheduleDateFormatsForCaller(const protocol::Grid* grid);

// Calc-end helper: drain the queue; for each cell apply the format via
// xlcFormatNumber UNLESS the cell is already date/time-formatted.
void DrainAndApplyDateFormats();

} // namespace xll
