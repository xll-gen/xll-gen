#include "xll_events.h"
#include "xll_log.h"
#include "xll_cache.h"
#include "xll_commands.h"
#include "xll_ipc.h"
#include "shm/DirectHost.h"
#include "protocol_generated.h"
#include <vector>
#include <mutex>

namespace xll {
    void HandleCalculationEnded() {
        XLL_SAFE_BLOCK_BEGIN
            // Clear caches
            {
                std::lock_guard<std::mutex> lock(g_refCacheMutex);
                g_sentRefCache.clear();
            }
            CacheManager::Instance().ClearRefCache();

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
        XLL_SAFE_BLOCK_END_VOID
    }
}
