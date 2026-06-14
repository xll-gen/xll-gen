#pragma once

// xll_rtd_placeholder.h — first-paint placeholder support for plain mode:"rtd"
// functions.
//
// Plain rtd streams live values: the cell's value is whatever the RTD topic
// currently holds, and the wrapper returns xlfRtd's result verbatim (it CANNOT
// substitute a placeholder without masking a value the stream genuinely pushed).
// The one knob we own is ConnectData's initial value — the value Excel shows
// from the moment the topic connects until the first stream push lands. That
// initial value used to be the literal text "Connecting…"; it now defaults to
// #GETTING_DATA, configurable per project (rtd.loading_placeholder) and per
// function (loading_placeholder), exactly like rtd-once. See AGENTS.md §19.3.
//
// This registry maps a plain-rtd function name (topic string 0) to the resolved
// placeholder so the shared, non-generated ConnectData (xll_rtd.cpp) can look it
// up. It is the plain-rtd analogue of RtdOnceRegistry's function-name set; it is
// populated once at xlAutoOpen, before any topic can connect.
//
// THREADING: Set runs on the main STA thread at xlAutoOpen; MakeInitial runs on
// the detached-free portion of ConnectData (the STA thread). All access goes
// through a single mutex.

#ifdef XLL_RTD_ENABLED

#include <windows.h>
#include <oaidl.h>
#include <string>
#include <vector>
#include <map>
#include <mutex>

namespace xll {

// How a plain-rtd topic's ConnectData initial value renders.
enum class RtdPlaceholderKind { GettingData, NA, Text };

// RtdPlaceholderRegistry maps plain-rtd function names to their first-paint
// placeholder. Single global instance.
class RtdPlaceholderRegistry {
public:
    static RtdPlaceholderRegistry& Instance() {
        static RtdPlaceholderRegistry inst;
        return inst;
    }

    // The resolved placeholder for one function: a kind plus (for Text) the
    // verbatim string to display.
    struct Entry {
        RtdPlaceholderKind kind = RtdPlaceholderKind::GettingData;
        std::wstring text;
    };

    // Populated once at xlAutoOpen with every plain-rtd function's resolved
    // placeholder (per-function loading_placeholder over the global default,
    // computed at generation time). Cheap copy; called before any topic can
    // connect.
    void Set(const std::vector<std::pair<std::wstring, Entry>>& entries) {
        std::lock_guard<std::mutex> lock(m_mutex);
        m_map.clear();
        for (const auto& e : entries) m_map[e.first] = e.second;
    }

    // Builds the ConnectData initial VARIANT for funcName. A function that is
    // not registered (e.g. code generated before this feature, or a topic whose
    // name does not match any rtd function) falls back to #GETTING_DATA — the
    // project-wide default. The COM cell-error scodes mirror rtd/server.h /
    // xll_rtd_once.h: #GETTING_DATA is 2043 (xlerrGettingData + 2000), #N/A is
    // 2042 (xlerrNA + 2000). For Text the BSTR is freshly allocated and Excel
    // takes ownership of pvarOut (it frees the BSTR), matching the legacy
    // "Connecting…" path.
    VARIANT MakeInitial(const std::wstring& funcName) const {
        std::lock_guard<std::mutex> lock(m_mutex);
        VARIANT v;
        VariantInit(&v);
        RtdPlaceholderKind kind = RtdPlaceholderKind::GettingData;
        const std::wstring* text = nullptr;
        auto it = m_map.find(funcName);
        if (it != m_map.end()) {
            kind = it->second.kind;
            text = &it->second.text;
        }
        switch (kind) {
        case RtdPlaceholderKind::NA:
            v.vt = VT_ERROR;
            v.scode = 2042; // xlerrNA (42) + 2000 COM cell-error mapping
            break;
        case RtdPlaceholderKind::Text:
            v.vt = VT_BSTR;
            v.bstrVal = SysAllocString(text ? text->c_str() : L"");
            break;
        case RtdPlaceholderKind::GettingData:
        default:
            v.vt = VT_ERROR;
            v.scode = 2043; // xlerrGettingData (43) + 2000
            break;
        }
        return v;
    }

private:
    RtdPlaceholderRegistry() = default;
    // Trivial dtor ON PURPOSE (§20.2 "leak, don't crash"): a Meyers singleton
    // holding only std::wstring/std::map (no oleaut32 resources — the BSTRs in
    // MakeInitial are handed to Excel, never retained here), so static-teardown
    // order is irrelevant and the maps are simply abandoned at process exit.
    ~RtdPlaceholderRegistry() = default;
    RtdPlaceholderRegistry(const RtdPlaceholderRegistry&) = delete;
    RtdPlaceholderRegistry& operator=(const RtdPlaceholderRegistry&) = delete;

    mutable std::mutex m_mutex;
    std::map<std::wstring, Entry> m_map;
};

} // namespace xll

#endif // XLL_RTD_ENABLED
