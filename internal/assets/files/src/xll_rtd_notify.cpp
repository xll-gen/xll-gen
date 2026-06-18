#ifdef XLL_RTD_ENABLED

#include "xll_rtd_notify.h"
#include "xll_rtd.h"        // g_rtdServer + RtdServer type (NotifyUpdate)
#include "xll_lifecycle.h"  // xll::g_isUnloading, g_hModule
#include "xll_log.h"
#include <windows.h>
#include <atomic>

// STA-routed RTD UpdateNotify dispatch. See xll_rtd_notify.h for the full
// rationale (cross-apartment COM call fix, IMPROVEMENT_BACKLOG §0).

namespace {

// Window class name + the coalesced notify message. WM_APP+1 keeps us clear of
// system messages and of any WM_APP message the ribbon/host might use.
constexpr wchar_t kRtdNotifyWindowClass[] = L"XllGenRtdNotifyWindow";
constexpr UINT WM_XLLGEN_RTD_NOTIFY = WM_APP + 1;

// The hidden HWND_MESSAGE window, created on the STA at xlAutoOpen. Written on
// the STA (Create/Destroy) and read on the worker thread (SignalRtdUpdate), so
// it is std::atomic to avoid a data race on the handle (UB under the C++ memory
// model even though pointer-sized loads are atomic on x86/x64). PostMessage
// tolerates a stale/destroyed HWND (it fails, which we treat as a no-op).
std::atomic<HWND> g_rtdNotifyWindow{nullptr};

// Coalescing flag. Set by SignalRtdUpdate on the 0->1 transition (which is the
// only caller that PostMessages); cleared FIRST in the WndProc so an update that
// arrives DURING NotifyUpdate() re-posts and is not lost.
std::atomic<bool> g_rtdNotifyPending{false};

LRESULT CALLBACK RtdNotifyWndProc(HWND hWnd, UINT msg, WPARAM wParam, LPARAM lParam) {
    if (msg == WM_XLLGEN_RTD_NOTIFY) {
        // Win32 dispatches this across the C ABI from Excel's message pump; an
        // escaping C++/SEH fault would unwind through DispatchMessage = UB. Wrap
        // the body in XLL_SAFE_BLOCK like every other entry point (AGENTS.md §20).
        XLL_SAFE_BLOCK_BEGIN
        // Clear the coalescing flag FIRST. Any RTD update that arrives after this
        // point (during the NotifyUpdate call below) will observe pending==false
        // and re-PostMessage, so its UpdateNotify is not lost. (UpdateTopic has
        // already stored the value under m_topicMutex before SignalRtdUpdate ran,
        // so RefreshData would pick it up regardless, but re-posting guarantees a
        // pump cycle for it.)
        g_rtdNotifyPending.store(false, std::memory_order_release);

        // Lifecycle guard: bail if teardown has begun or the server is gone. This
        // no-ops an already-queued message during/after teardown while this code
        // is still mapped (§20.2). WndProc runs on the STA — the correct apartment
        // for the cross-COM UpdateNotify call.
        if (xll::g_isUnloading.load(std::memory_order_acquire) || !g_rtdServer) {
            return 0;
        }
        g_rtdServer->NotifyUpdate();
        return 0;
        XLL_SAFE_BLOCK_END(0)
    }
    return DefWindowProcW(hWnd, msg, wParam, lParam);
}

} // anonymous namespace

namespace xll {

void CreateRtdNotifyWindow() {
    // MUST be on the STA (xlAutoOpen). Idempotent.
    if (g_rtdNotifyWindow.load(std::memory_order_acquire)) return;

    WNDCLASSEXW wc{};
    wc.cbSize = sizeof(wc);
    wc.lpfnWndProc = RtdNotifyWndProc;
    wc.hInstance = g_hModule;
    wc.lpszClassName = kRtdNotifyWindowClass;

    // RegisterClassExW returns 0 on failure; ERROR_CLASS_ALREADY_EXISTS is benign
    // (a prior probe-load that leaked the class — the class registration is keyed
    // by (atom, hInstance) and survives until UnregisterClass / process exit).
    ATOM atom = RegisterClassExW(&wc);
    if (atom == 0) {
        DWORD err = GetLastError();
        if (err != ERROR_CLASS_ALREADY_EXISTS) {
            LogWarn("RTD notify: RegisterClassExW failed (" + std::to_string(err) +
                    "); falling back to direct NotifyUpdate from the worker thread");
            return;
        }
    }

    HWND hwnd = CreateWindowExW(
        0, kRtdNotifyWindowClass, L"", 0,
        0, 0, 0, 0,
        HWND_MESSAGE, // message-only window: no UI, only receives messages
        nullptr, g_hModule, nullptr);

    if (!hwnd) {
        LogWarn("RTD notify: CreateWindowExW(HWND_MESSAGE) failed (" +
                std::to_string(GetLastError()) +
                "); falling back to direct NotifyUpdate from the worker thread");
        // Leave the class registered; DestroyRtdNotifyWindow will UnregisterClass.
        return;
    }

    // Reset the coalescing flag for a fresh window (defensive; also reset in
    // DLL_PROCESS_ATTACH-equivalent fresh-load — see header lifecycle note).
    // Publish the HWND only after the flag is reset (release) so a worker that
    // observes the handle also sees the cleared flag.
    g_rtdNotifyPending.store(false, std::memory_order_release);
    g_rtdNotifyWindow.store(hwnd, std::memory_order_release);
    LogDebug("RTD notify: hidden HWND_MESSAGE notify window created on the STA");
}

void SignalRtdUpdate() {
    // Callable from ANY thread (the worker). Non-blocking.
    if (g_isUnloading.load(std::memory_order_acquire)) return;
    HWND h = g_rtdNotifyWindow.load(std::memory_order_acquire);
    if (!h) return;

    // Coalesce: only the 0->1 transition actually posts. If PostMessage fails
    // (e.g. the window was destroyed between the read and the post), roll the flag
    // back so the next update can re-arm. PostMessage never blocks on the STA.
    if (!g_rtdNotifyPending.exchange(true, std::memory_order_acq_rel)) {
        if (!PostMessageW(h, WM_XLLGEN_RTD_NOTIFY, 0, 0)) {
            g_rtdNotifyPending.store(false, std::memory_order_release);
        }
    }
}

void DestroyRtdNotifyWindow() {
    // MUST be on the STA (the creating thread), AFTER JoinWorker. Idempotent.
    HWND h = g_rtdNotifyWindow.exchange(nullptr, std::memory_order_acq_rel);
    if (h) {
        DestroyWindow(h);
    }
    // UnregisterClass is keyed by (class name, hInstance) and is safe even if the
    // window was never created (it just fails benignly). Done after DestroyWindow
    // so no live window holds the class.
    UnregisterClassW(kRtdNotifyWindowClass, g_hModule);
    g_rtdNotifyPending.store(false, std::memory_order_release);
}

} // namespace xll

#endif // XLL_RTD_ENABLED
