#include "xll_deferred_commands.h"
#include "xll_commands.h"
#include "xll_date_format.h"
#include "xll_excel.h"          // xll::CallExcel
#include "xll_ipc.h"            // g_phost / g_host
#include "xll_lifecycle.h"      // xll::g_isUnloading
#include "xll_log.h"
#include "types/ScopedXLOPER12.h"
#include "types/protocol_generated.h"
#include <atomic>
#include <flatbuffers/flatbuffers.h>
#include <mutex>

namespace xll {

// Saved serial time of the most recent xlcOnTime schedule (#3, 2026-06-17).
// xlcOnTime cancellation requires passing the EXACT serial time that was used to
// schedule the macro, so we capture xlfNow()'s returned value here. Guarded by a
// mutex because Schedule (calc-end, STA) and Cancel (GracefulTeardownOnce, STA)
// both touch it — in practice the same thread, but the mutex keeps the
// discipline explicit and cheap. m_scheduledHasTime gates whether a cancel
// should be attempted at all (nothing armed -> nothing to cancel).
namespace {
    std::mutex g_onTimeMutex;
    double g_lastOnTimeSerial = 0.0;
    bool g_onTimeArmed = false;

    // Diagnostic dispatch counter (#3 empirical de-queue proof, 2026-07-24).
    // Incremented at the TOP of RunDeferredCalcEndCommands — i.e. every time Excel
    // ACTUALLY dispatches the OnTime runner macro, even if the runner then
    // self-aborts (g_isUnloading / g_phost==nullptr) and discards the queue. This
    // is the observable signal that answers "did the OnTime schedule survive a
    // CancelDeferredRunner?" without relying on a cell side effect. Pure atomic;
    // touches nothing Excel/g_phost-related, so it does not perturb the STA
    // self-abort invariant documented in RunDeferredCalcEndCommands.
    std::atomic<int> g_runnerDispatchCount{0};

    // Decode an Excel12 xlret status into a human-readable tag so the
    // CancelDeferredRunner log line is self-explanatory. In particular
    // xlretInvXlfn (2) means Excel REJECTED the call because the current context
    // does not permit command-class (xlc*) C-API functions — which is what
    // happens on the host-shutdown teardown path (see CancelDeferredRunner).
    const char* XlretName(int rc) {
        switch (rc) {
            case xlretSuccess:       return "xlretSuccess";
            case xlretAbort:         return "xlretAbort";
            case xlretInvXlfn:       return "xlretInvXlfn(invalid-context/fn)";
            case xlretInvCount:      return "xlretInvCount";
            case xlretInvXloper:     return "xlretInvXloper";
            case xlretStackOvfl:     return "xlretStackOvfl";
            case xlretFailed:        return "xlretFailed";
            case xlretUncalced:      return "xlretUncalced";
            case xlretNotThreadSafe: return "xlretNotThreadSafe";
            default:                 return "xlret(other)";
        }
    }
}

// Diagnostic accessor: total number of times Excel has dispatched the deferred
// runner macro this process generation. Used by the #3 de-queue proof harness
// (a valid-context Schedule+Cancel must leave this UNCHANGED; a Schedule-only
// control must increment it). See AGENTS.md §23.6 HIGH #2.
int DeferredRunnerDispatchCount() { return g_runnerDispatchCount.load(); }
void ResetDeferredRunnerDispatchCount() { g_runnerDispatchCount.store(0); }

DeferredCalcEndQueue& DeferredCalcEndQueue::Instance() {
    static DeferredCalcEndQueue inst;
    return inst;
}

void DeferredCalcEndQueue::Enqueue(std::vector<uint8_t>&& respBuf) {
    if (respBuf.empty()) return;
    std::lock_guard<std::mutex> lock(m_mutex);
    m_pending.push_back(std::move(respBuf));
}

std::vector<std::vector<uint8_t>> DeferredCalcEndQueue::Drain() {
    std::lock_guard<std::mutex> lock(m_mutex);
    std::vector<std::vector<uint8_t>> out;
    out.swap(m_pending);
    return out;
}

bool DeferredCalcEndQueue::HasPending() {
    std::lock_guard<std::mutex> lock(m_mutex);
    return !m_pending.empty();
}

// Schedule the runner macro to fire as soon as Excel is idle. xlcOnTime(now,
// "macro") queues the named macro onto Excel's macro queue; Excel dispatches it
// on the STA thread at the next idle point — crucially OUTSIDE the
// xleventCalculationEnded callback and after any in-flight recalc / RTD
// teardown has settled. We pass xlfNow() as the time so it runs immediately.
static void ScheduleDeferredRunner() {
    try {
        // Coalesce redundant schedules: only the 0->1 transition issues an
        // xlcOnTime. If the runner is already armed (one in-flight macro that has
        // not yet drained), skip — that runner will pick up everything queued so
        // far. The runner Disarm()s before it drains, so any calc-end that enqueues
        // during the drain wins the next TryArm() and gets a fresh schedule.
        if (!DeferredCalcEndQueue::Instance().TryArm()) return;
        // Time = now. xlcOnTime treats a time already past as "run ASAP".
        ScopedXLOPER12Result xNow;
        if (xll::CallExcel(xlfNow, xNow) != xlretSuccess) {
            // We armed but could not schedule; disarm so the next calc-end retries
            // instead of being silently suppressed forever.
            DeferredCalcEndQueue::Instance().Disarm();
            return;
        }
        // xlcOnTime(serial_time, macro_text). Tolerance/insert default.
        int schedRc = xll::CallExcel(xlcOnTime, nullptr, xNow.get(), DeferredRunnerMacroName());
        if (schedRc != xlretSuccess) {
            xll::LogInfo(std::string("ScheduleDeferredRunner: xlcOnTime schedule rc=") +
                         std::to_string(schedRc) + " (" + XlretName(schedRc) + ")");
            DeferredCalcEndQueue::Instance().Disarm();
            return;
        }
        // Capture the EXACT serial time we just scheduled with so a later
        // CancelDeferredRunner() can cancel THIS schedule (xlcOnTime cancel
        // matches on serial time). xlfNow returns xltypeNum. (#3, 2026-06-17)
        if ((xNow.get()->xltype & xltypeNum) != 0) {
            std::lock_guard<std::mutex> lock(g_onTimeMutex);
            g_lastOnTimeSerial = xNow.get()->val.num;
            g_onTimeArmed = true;
        }
    } catch (...) {
        // Never throw into the event. Disarm on the error path so a future
        // calc-end can re-arm rather than wedging the guard.
        DeferredCalcEndQueue::Instance().Disarm();
    }
}

// Generic xlcOnTime scheduler (AGENTS.md §3). See the header for the full
// context contract. Kept separate from ScheduleDeferredRunner because it must
// NOT touch the deferred-queue arm flag or the g_lastOnTimeSerial/g_onTimeArmed
// cancel-tracking state (the ribbon retry terminates by self-abort, never by a
// C-API cancel, so it has no serial to capture).
int ScheduleOnTimeMacro(const wchar_t* macroName, double delaySeconds) {
    try {
        // Unload self-gate (§20 discipline). This is a PUBLIC, general-purpose API
        // in the header, so it must not depend on every present and future caller
        // remembering to gate: issuing an Excel C-API command while the add-in is
        // tearing down is exactly the class of call §20 forbids, and a schedule
        // placed during teardown can only ever resolve to a leaked OnTime dispatch.
        // Both current callers already gate; this makes the guarantee STRUCTURAL
        // and local to the API rather than an unenforced caller contract.
        if (xll::g_isUnloading.load(std::memory_order_acquire)) return xlretFailed;
        if (!macroName) return xlretFailed;
        ScopedXLOPER12Result xNow;
        int nowRc = xll::CallExcel(xlfNow, xNow);
        if (nowRc != xlretSuccess || (xNow.get()->xltype & xltypeNum) == 0) {
            // Log the returned xltype ALONGSIDE rc. When xlfNow SUCCEEDS but hands
            // back a non-numeric operand, nowRc is 0 and a bare
            // "rc=0 (xlretSuccess)" reads as if the call worked — the xltype is the
            // only thing that distinguishes the two failure shapes. WARN, not INFO:
            // the macro was NOT scheduled and the caller's chain is now broken.
            xll::LogWarn(std::string("ScheduleOnTimeMacro: xlfNow rc=") +
                         std::to_string(nowRc) + " (" + XlretName(nowRc) + ")" +
                         " xltype=" + std::to_string(xNow.get()->xltype) +
                         " — macro NOT scheduled");
            return nowRc != xlretSuccess ? nowRc : xlretFailed;
        }
        // Excel serial time is in DAYS; convert the second-granularity delay.
        double when = xNow.get()->val.num +
                      (delaySeconds > 0.0 ? delaySeconds / 86400.0 : 0.0);
        ScopedXLOPER12 xWhen(when);
        int rc = xll::CallExcel(xlcOnTime, nullptr, xWhen.get(), macroName);
        if (rc != xlretSuccess) {
            // WARN, not INFO: a rejected arm means the caller's self-re-arming
            // chain (e.g. the ribbon-connect retry) silently DIES here. Callers
            // must inspect the returned rc; this line is the operator-visible half.
            xll::LogWarn(std::string("ScheduleOnTimeMacro: xlcOnTime rc=") +
                         std::to_string(rc) + " (" + XlretName(rc) + ")" +
                         " — macro NOT scheduled");
        }
        return rc;
    } catch (...) {
        return xlretFailed;
    }
}

void DeferCalcEndCommands(std::vector<uint8_t>&& respBuf) {
    try {
        const bool haveBuf = !respBuf.empty();
        if (haveBuf) {
            DeferredCalcEndQueue::Instance().Enqueue(std::move(respBuf));
        }
        // Schedule the runner if there are commands to execute OR date formats
        // pending. The date-format drain rides the same deferral (same in-event
        // cell-mutation reentrancy class), so even a buffer-less calc-end with
        // pending formats must wake the runner.
        if (haveBuf || PendingDateFormats::Instance().HasPending()) {
            ScheduleDeferredRunner();
        }
    } catch (...) { /* never throw into the event */ }
}

void RunDeferredCalcEndCommands() {
    // Diagnostic: record that Excel ACTUALLY dispatched the OnTime runner macro
    // (#3 de-queue proof). Logged/counted BEFORE the self-abort check so a leaked
    // or un-cancelled schedule that fires is always visible. A successful
    // CancelDeferredRunner must prevent this line from appearing after the cancel.
    int dispatchN = g_runnerDispatchCount.fetch_add(1) + 1;
    xll::LogInfo("RunDeferredCalcEndCommands: dispatched (count=" + std::to_string(dispatchN) + ")");

    // Disarm BEFORE draining (HIGH fix, 2026-06-16). Clearing the schedule guard
    // first means a calc-end that enqueues while we are draining/executing below
    // will win TryArm() and schedule a fresh runner — so concurrently-arriving
    // work is never dropped. (Enqueue + this runner are both on the STA thread, so
    // "concurrently" here means an event nested by Excel's own dispatch, not true
    // parallelism, but the ordering is what matters.)
    DeferredCalcEndQueue::Instance().Disarm();

    // This runner is now executing, so the previously-scheduled xlcOnTime has
    // fired and there is nothing left to cancel. Clear the armed flag so a later
    // CancelDeferredRunner() (on teardown) does not issue a cancel for a serial
    // that has already run. A fresh schedule (re-arm during/after the drain) sets
    // it again. (#3, 2026-06-17)
    {
        std::lock_guard<std::mutex> lock(g_onTimeMutex);
        g_onTimeArmed = false;
    }

    // Unload self-abort (§20.2): if the add-in is tearing down or the host is
    // gone, do NOT touch Excel — just drop any queued work. A leaked xlcOnTime
    // macro that fires post-unload lands here and no-ops safely.
    //
    // INVARIANT (why this single check, with no re-check before the COM calls
    // below, is sufficient): this runner and the teardown path (which deletes
    // g_phost / sets g_isUnloading) both run on Excel's STA dispatch thread.
    // Excel cannot dispatch this OnTime macro once the teardown macro has begun
    // on that same thread, so g_phost cannot transition non-null→freed between
    // this check and the ExecuteCommands/DrainAndApplyDateFormats COM calls. The
    // check is unlocked because there is no cross-thread race to guard, only the
    // post-unload leaked-schedule no-op.
    if (xll::g_isUnloading.load() || g_phost == nullptr) {
        DeferredCalcEndQueue::Instance().Drain(); // discard
        return;
    }
    try {
        auto buffers = DeferredCalcEndQueue::Instance().Drain();
        for (const auto& buf : buffers) {
            if (buf.empty()) continue;
            // Verify the OWNED copy before parsing. These bytes crossed a time
            // boundary (enqueued in the event, parsed here at idle); a malformed
            // buffer that slipped through would otherwise become a hard-to-attribute
            // deferred crash. Skip (and warn) on failure rather than fault.
            flatbuffers::Verifier verifier(buf.data(), buf.size());
            if (!verifier.VerifyBuffer<protocol::CalculationEndedResponse>(nullptr)) {
                xll::LogWarn("RunDeferredCalcEndCommands: skipping malformed deferred CalculationEndedResponse buffer");
                continue;
            }
            // Re-resolve the root from the OWNED copy; the command Vector points
            // into `buf`, which outlives this iteration.
            auto root = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(buf.data());
            if (!root) continue;
            auto commands = root->commands();
            if (commands) {
                ExecuteCommands(commands);
            }
        }
        // Date auto-format drain — deferred out of the event for the same
        // reentrancy reason as the commands above. Idempotent (once-per-cell).
        xll::DrainAndApplyDateFormats();
    } catch (...) { /* never throw on the STA macro path */ }
}

void CancelDeferredRunner() {
    // Cancel any pending xlcOnTime-scheduled deferred runner macro on the
    // CONFIRMED-teardown path (#3, 2026-06-17). Without this, a runner armed by a
    // late CalculationEnded (e.g. the RTD-streaming recalc that fires ~1/s) stays
    // queued on Excel's OnTime list after xlAutoClose; Excel can dispatch that
    // macro post-teardown, and the act of dispatching a queued OnTime macro is a
    // candidate for keeping Excel.exe alive windowless and/or re-materializing a
    // window (symptom S1). The runner itself self-aborts on g_isUnloading, but
    // cancelling the SCHEDULE removes the dispatch entirely.
    //
    // xlcOnTime cancellation form: xlcOnTime(serial_time, macro_text,
    // tolerance=missing, schedule=FALSE). The serial_time MUST equal the value
    // used to schedule, which we captured in ScheduleDeferredRunner.
    //
    // Host-reachability gate: only attempt the cancel if the host is still
    // reachable and we are not already too late. We are called from
    // GracefulTeardownOnce, which sets g_isUnloading=true BEFORE calling us, so we
    // do NOT gate on g_isUnloading here (that would make this a no-op). We DO gate
    // on whether a schedule is actually armed, and we wrap the C-API call in the
    // file's SEH/exception discipline.
    try {
        double serial;
        {
            std::lock_guard<std::mutex> lock(g_onTimeMutex);
            if (!g_onTimeArmed) return; // nothing scheduled -> nothing to cancel
            serial = g_lastOnTimeSerial;
            g_onTimeArmed = false;
        }

        // Rebuild the serial-time operand by value (xltypeNum) so it matches the
        // scheduled time exactly. ScopedXLOPER12(double) is the documented wrapper
        // for a numeric operand.
        ScopedXLOPER12 xWhen(serial);

        // tolerance = missing (omitted argument). Zero-init so the whole operand
        // is in a defined state before it crosses the C API (Excel ignores `val`
        // for xltypeMissing, but handing it an uninitialized union member is a smell).
        XLOPER12 xMissing{};
        xMissing.xltype = xltypeMissing;

        // schedule = FALSE -> cancel.
        ScopedXLOPER12 xSchedule(false);

        int rc = xll::CallExcel(xlcOnTime, nullptr, xWhen.get(), DeferredRunnerMacroName(), &xMissing, xSchedule.get());
        if (rc != xlretSuccess) {
            // Non-fatal, but IMPORTANT to read correctly: this is the Excel12
            // xlret STATUS of the call, NOT a boolean "cleared" result. rc=2 is
            // xlretInvXlfn — Excel REJECTED the command because the current
            // context does not permit command-class (xlc*) C-API calls. That is
            // the normal outcome on the host-shutdown teardown path (this runs
            // from OnBeginShutdown/OnDisconnection, a COM-event context, NOT an
            // Excel-dispatched macro/command context), so the schedule is NOT
            // de-queued here. It is harmless only because the runner self-aborts
            // on g_isUnloading/g_phost==nullptr and Excel un-registers this XLL's
            // macros on unload — those, not this cancel, are what neutralize a
            // leaked OnTime dispatch. (#3 empirical finding, 2026-07-24; see
            // AGENTS.md §23.6 HIGH #2.)
            xll::LogInfo(std::string("CancelDeferredRunner: xlcOnTime cancel rc=") +
                         std::to_string(rc) + " (" + XlretName(rc) +
                         ") — schedule NOT de-queued (see #3 note)");
        } else {
            xll::LogInfo("CancelDeferredRunner: pending deferred runner cancelled (rc=xlretSuccess)");
        }
    } catch (...) {
        // Never throw out of teardown.
    }
}

} // namespace xll
