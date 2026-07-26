#include "xll_events.h"
#include "xll_log.h"
#include "xll_cache.h"
#include "xll_commands.h"
#include "xll_deferred_commands.h"
#include "xll_ipc.h"
#include "xll_date_format.h"
#include "shm/DirectHost.h"
#include "types/protocol_generated.h"
#include <vector>
#include <mutex>
#ifdef XLL_RTD_ENABLED
#include "xll_rtd_once.h"
#include "xll_rtd_once_grid.h"
#endif

namespace xll {
    void HandleCalculationEnded() {
        // Clear caches
        {
            std::lock_guard<std::mutex> lock(g_refCacheMutex);
            g_sentRefCache.clear();
        }
        // Re-read Excel's iterative-calculation state. This must run HERE, in
        // the event callback, because GET.DOCUMENT is macro-sheet only and the
        // calc-end callback is a valid command context; it is a no-op for
        // projects that never route a reference argument through the RefCache.
        //
        // On the ORDER relative to ClearRefCache: the two are currently
        // INDEPENDENT — ClearRefCache() only empties refCache_ and does not
        // touch refPathUsed_ or iterativeCalc_, so swapping them would not
        // change behavior today. The refresh is placed before the clear because
        // it is the decision that closes the cycle just observed (it consumes
        // the "a reference argument used the RefCache this cycle" flag), and
        // the clear is the cycle boundary itself. Keep the order as
        // documentation of that intent, not as a load-bearing dependency.
        //
        // Why the gate exists: iterative (circular-reference) calculation runs
        // the SAME cells up to MaxIterations times inside ONE calculation cycle
        // — with different values each pass — while this event fires exactly
        // ONCE for that whole cycle (verified against real Excel; AGENTS.md
        // §19.4). Without the gate the pass-1 (sheet, rect) -> value-digest
        // entry survives into passes 2..N and a cache-enabled function freezes
        // at its first-pass result.
        CacheManager::Instance().RefreshIterativeCalcMode();
        {
            // Log only the TRANSITION (STA-only, so a plain static is fine).
            // The logging lives here rather than in xll_cache.cpp on purpose:
            // that file must stay free of cross-TU calls so the offline g++
            // gate (internal/assets/testdata/cache_native_test.cpp) can link it
            // against nothing but a stub Excel12v.
            static bool s_lastIterativeMode = false;
            const bool iterativeMode = CacheManager::Instance().IterativeCalcMode();
            if (iterativeMode != s_lastIterativeMode) {
                s_lastIterativeMode = iterativeMode;
                xll::LogInfo(iterativeMode
                    ? "RefCache: iterative calculation detected (GET.DOCUMENT(15)); "
                      "per-cycle reference-digest memoization disabled so each "
                      "iteration re-reads the range"
                    : "RefCache: iterative calculation off; per-cycle "
                      "reference-digest memoization re-enabled");
            }
        }
        CacheManager::Instance().ClearRefCache();

#ifdef XLL_RTD_ENABLED
        // rtd-once: drop completed one-shot results for non-memoize functions
        // so the next user-initiated recalc recomputes (F9 semantics). Same
        // per-calc-cycle lifecycle as the RefCache clear above. No-op when no
        // rtd-once results are pending. memoize:true results survive.
        xll::RtdOnceRegistry::Instance().ClearNonMemoized();
        // Same per-calc-cycle clear for the grid-once registry (byte-buffer
        // twin): once-mode grid payloads with no live topic are dropped;
        // memoize / unexpired-memoize_ttl payloads survive. See AGENTS.md §19.3.
        xll::RtdOnceGridRegistry::Instance().ClearNonMemoized();
#endif

        // Keep the synchronous MSG_CALCULATION_ENDED round-trip HERE, inside the
        // event: the IPC blocking is NOT the reentrancy hazard (proven by
        // bisection — see xll_deferred_commands.h). This is also what invokes the
        // user's Go calc-end handler and produces any returned SetCommand /
        // FormatCommand. Date-format requests were enqueued on the calc thread
        // (ScheduleDateFormatsForCaller) before we got here.
        std::vector<uint8_t> respBuf;
        bool haveCommands = false;
        auto res = g_host.Send(nullptr, 0, (shm::MsgType)MSG_CALCULATION_ENDED, respBuf, 2000);
        if (!res.HasError() && res.Value() > 0) {
            auto root = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(respBuf.data());
            auto commands = root->commands();
            haveCommands = commands && commands->size() > 0;
        }

        // CELL MUTATION MUST NOT HAPPEN INSIDE THIS EVENT CALLBACK.
        // ExecuteCommands (xlSet) and DrainAndApplyDateFormats
        // (xlcSelect/xlcFormatNumber) re-enter Excel's calc/RTD machinery and
        // crash Excel (0xc0000005) when they fire during an rtd-once
        // materialize/disconnect window. Defer BOTH out of the event: copy the
        // response buffer into the process-global queue and schedule the runner
        // macro via xlcOnTime so the writes run on the STA thread at an idle
        // point, NOT mid-recalc/mid-RTD-teardown. Command ordering is preserved
        // (FIFO queue; in-order command vector). See xll_deferred_commands.h and
        // AGENTS.md §19.3 / §23.
        if (haveCommands) {
            xll::DeferCalcEndCommands(std::move(respBuf));
        } else {
            // No commands, but date formats may be pending — wake the runner so
            // DrainAndApplyDateFormats still runs (deferred). Empty buffer => the
            // queue ignores it; the scheduler checks PendingDateFormats.
            xll::DeferCalcEndCommands(std::vector<uint8_t>{});
        }
    }
}
