#include "xll_events.h"
#include "xll_log.h"
#include "xll_cache.h"
#include "xll_commands.h"
#include "xll_ipc.h"
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

        std::vector<uint8_t> respBuf;
        auto res = g_host.Send(nullptr, 0, (shm::MsgType)MSG_CALCULATION_ENDED, respBuf, 2000);
        if (!res.HasError() && res.Value() > 0) {
            // Process returned commands (e.g. SetCommand)
            auto root = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(respBuf.data());
            auto commands = root->commands();
            if (commands) {
                ExecuteCommands(commands);
            }
        }
    }
}
