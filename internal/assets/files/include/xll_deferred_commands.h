#pragma once

// xll_deferred_commands.h — calc-end deferred command runner.
//
// WHY THIS EXISTS (reentrancy crash fix, 2026-06-15)
// --------------------------------------------------
// The exported CalculationEnded() macro (xleventCalculationEnded) runs
// HandleCalculationEnded(), which does a synchronous MSG_CALCULATION_ENDED
// round-trip and historically called ExecuteCommands(commands) — i.e. xlSet /
// xlcSelect / xlcFormatNumber — SYNCHRONOUSLY, still INSIDE the
// xleventCalculationEnded callback. When that cell write fires during an
// rtd-once scalar/grid materialize recalc cycle (the rtd-once cache-hit wrapper
// withholds xlfRtd while Excel is concurrently DISCONNECTing the rtd-once
// topics), the write RE-ENTERS Excel's calc/RTD machinery at a fragile point
// and Excel faults with 0xc0000005 in EXCEL.EXE (~7-8s after a bulk build).
//
// Controlled single-variable bisection against a 100%-real-Excel repro proved
// the trigger is the in-event cell-mutating call (ExecuteCommands -> xlSet),
// NOT the IPC round-trip and NOT the cache clear. See AGENTS.md §19.3 / §23.
//
// THE FIX
// -------
// Do NOT mutate cells inside the event callback. HandleCalculationEnded keeps
// the synchronous round-trip (IPC blocking is safe), but instead of executing
// the returned commands inline it COPIES the response buffer into this
// process-global queue and schedules a registered runner macro via xlcOnTime
// (time = now). Excel runs that macro OUTSIDE the event, on the STA thread, at
// an idle point where it is not mid-recalc / mid-RTD-teardown. The runner
// drains the queue and performs the xlSet / format calls there. Command
// ORDERING is preserved: the queue is FIFO and each buffer's command vector is
// executed in order.
//
// DrainAndApplyDateFormats() rides the SAME deferral: applying a number format
// via xlcSelect/xlcFormatNumber is the same class of in-event cell mutation, so
// it is moved out of the event for correctness. It is idempotent (once-per-cell
// marking) and cheap, so deferring it is low-risk.
//
// THREADING / LIFECYCLE
// ---------------------
// Enqueue runs on the STA thread inside HandleCalculationEnded; the runner runs
// on the STA thread when Excel dispatches the xlcOnTime macro — so there is no
// true concurrency, but the queue is mutex-guarded anyway (cheap, and keeps the
// same discipline as the RTD/date registries). The queue is a Meyers singleton
// with a trivial destructor: on a forced unload (§20.2 "leak, don't crash") we
// never run Excel C-API from a static destructor, and a pending xlcOnTime macro
// is harmless because Excel un-registers this XLL's macros when the DLL unloads
// (and the runner re-checks g_isUnloading / g_phost before touching anything).
//
// Header-only discipline: the small impl lives in src/xll_deferred_commands.cpp
// (compiled by the generated CMake src/*.cpp glob, like xll_date_format.cpp).

#include <vector>
#include <cstdint>
#include <mutex>
#include <atomic>

namespace xll {

// Process-global FIFO of CalculationEndedResponse FlatBuffer byte copies that
// still need their commands executed. Each entry is a full, self-contained copy
// of the MSG_CALCULATION_ENDED response buffer (the flatbuffers Vector points
// INTO these bytes, so the copy must own them).
class DeferredCalcEndQueue {
public:
    static DeferredCalcEndQueue& Instance();

    // Append one response buffer copy. Empty buffers are ignored.
    void Enqueue(std::vector<uint8_t>&& respBuf);

    // Take everything queued so far, FIFO order preserved.
    std::vector<std::vector<uint8_t>> Drain();

    // Whether any work is pending (so the scheduler can skip the xlcOnTime call
    // when there is nothing to run).
    bool HasPending();

    // xlcOnTime-coalescing guard (HIGH fix, 2026-06-16).
    // ------------------------------------------------------------------
    // Every calc-end used to issue a fresh xlcOnTime(now, runner) unconditionally.
    // Under RTD streaming recalc (~1/s) or keystroke recalc, calc-end N+1 schedules
    // again before Excel dispatches the runner from calc-end N, so Excel's OnTime
    // queue accumulates redundant invocations of the SAME macro. They are
    // self-limiting (a later runner finds an empty queue -> no-op) but wasteful on
    // the exact hot path that originally crashed, and a redundant run can replay a
    // selection-moving xlcSelect after the user regained control.
    //
    // TryArm() does an atomic test-and-set: it returns true (caller should call
    // xlcOnTime) only on the 0->1 transition; subsequent calls return false until
    // the runner clears the flag. Disarm() is called at the TOP of the runner,
    // BEFORE Drain(), so a calc-end that enqueues DURING the drain re-arms and gets
    // a fresh schedule — no work is lost. This collapses N pending schedules into
    // at most one in-flight + one armed.
    //
    // All accesses are on the STA thread (enqueue in the event, runner on the
    // dispatched macro), so there is no true concurrency; the atomic keeps the
    // discipline cheap and explicit and is safe even if that assumption ever bends.
    bool TryArm()  { bool expected = false; return m_runnerScheduled.compare_exchange_strong(expected, true); }
    void Disarm()  { m_runnerScheduled.store(false); }

private:
    DeferredCalcEndQueue() = default;
    ~DeferredCalcEndQueue() = default;
    DeferredCalcEndQueue(const DeferredCalcEndQueue&) = delete;
    DeferredCalcEndQueue& operator=(const DeferredCalcEndQueue&) = delete;

    std::mutex m_mutex;
    std::vector<std::vector<uint8_t>> m_pending;
    std::atomic<bool> m_runnerScheduled{false};
};

// The registered runner macro name (procedure + function text). xlcOnTime
// schedules this by name; xlAutoOpen registers it as a macro (macroType=2).
// Kept as a single source of truth so the template's xlfRegister call and the
// xlcOnTime scheduler cannot drift apart.
inline const wchar_t* DeferredRunnerMacroName() {
    return L"__xllgen_RunDeferredCalcEnd";
}

// Called from HandleCalculationEnded (STA thread, inside the event): copies the
// MSG_CALCULATION_ENDED response into the queue (if it carries commands) and
// schedules the runner macro via xlcOnTime so the cell writes happen OUTSIDE
// the event callback. Also schedules the runner when date formats are pending,
// so DrainAndApplyDateFormats runs deferred too. NEVER throws into the event.
void DeferCalcEndCommands(std::vector<uint8_t>&& respBuf);

// The runner body, invoked by the exported runner macro on the STA thread when
// Excel dispatches the xlcOnTime call. Drains the queue, runs ExecuteCommands
// for each buffer (FIFO, original command order), then DrainAndApplyDateFormats.
// Self-aborts if the add-in is unloading (g_isUnloading) or the host is gone
// (g_phost == nullptr). NEVER throws.
void RunDeferredCalcEndCommands();

} // namespace xll
