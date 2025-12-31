#pragma once

#ifdef XLL_RTD_ENABLED

#include <windows.h>
#include <string>
#include <vector>
#include <map>
#include <mutex>
#include "types/protocol_generated.h"
#include "rtd/server.h"

// Helper function to create CLSID from string
GUID StringToGuid(const std::wstring& str);

// RTD Update Queue
struct RtdValue {
    long topicId;
    std::wstring value;
    bool dirty;
};

// Global RTD State
extern std::mutex g_rtdMutex;
extern std::map<long, RtdValue> g_rtdValues;
extern rtd::IRTDUpdateEvent* g_rtdCallback;

void ProcessRtdUpdate(const protocol::RtdUpdate* update);

// RTD Server Implementation
class RtdServer : public rtd::RtdServerBase {
public:
    HRESULT __stdcall ConnectData(long TopicID, SAFEARRAY** Strings, VARIANT_BOOL* GetNewValues, VARIANT* pvarOut) override;
    HRESULT __stdcall DisconnectData(long TopicID) override;
    HRESULT __stdcall RefreshData(long* TopicCount, SAFEARRAY** parrayOut) override;
    HRESULT __stdcall ServerTerminate() override;

private:
    // Helper to send messages to Go
    // We might need to access g_host.
};

#endif // XLL_RTD_ENABLED
