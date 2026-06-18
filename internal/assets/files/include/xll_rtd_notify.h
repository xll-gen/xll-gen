#pragma once

#ifdef XLL_RTD_ENABLED

// xll_rtd_notify.h — STA-routed RTD UpdateNotify dispatch.
//
// WHY THIS EXISTS (cross-apartment COM call, IMPROVEMENT_BACKLOG §0 [MED])
// -----------------------------------------------------------------------
// RtdServer::NotifyUpdate() calls Excel's IRTDUpdateEvent::UpdateNotify(). That
// callback (m_callback) is obtained on Excel's main STA thread in
// RtdServerBase::ServerStart — Excel always calls IRtdServer methods on its STA.
// Before this change, NotifyUpdate() was invoked from the background worker
// thread (xll_worker.cpp WorkerLoop -> ProcessGuestCalls -> ProcessRtdUpdate),
// which is a plain std::thread, NOT a COM-initialized STA thread. So the worker
// made a RAW CROSS-APARTMENT COM call on an interface pointer that was never
// marshalled (the RTD class object's ThreadingModel is "Both"). That is a latent
// COM correctness defect, and the worker could ALSO head-of-line block while the
// STA was busy servicing a recalc.
//
// THE FIX (hidden message-only window + coalesced PostMessage)
// -----------------------------------------------------------
// We route the UpdateNotify onto the STA. At xlAutoOpen (on the STA) we create a
// hidden HWND_MESSAGE window. The worker, instead of calling NotifyUpdate()
// directly, PostMessages a coalesced WM_XLLGEN_RTD_NOTIFY to that window.
// Excel's STA message pump dispatches our WndProc on the STA, where calling
// NotifyUpdate() is finally on the correct apartment. PostMessage is
// non-blocking even when the STA is busy, so the worker never head-of-line
// blocks. Coalescing (a single pending flag) collapses an update burst into one
// UpdateNotify per pump cycle — Excel's RefreshData then reads ALL dirty topics
// at once (UpdateTopic already stores them under m_topicMutex in
// xll_rtd.cpp::ProcessRtdUpdate before SignalRtdUpdate is called), so no update
// is lost by coalescing.
//
// THREADING / LIFECYCLE (§20 / §20.2)
// -----------------------------------
//   - CreateRtdNotifyWindow()  MUST run on the STA (xlAutoOpen). Idempotent.
//   - SignalRtdUpdate()        callable from ANY thread (the worker). Non-blocking.
//   - DestroyRtdNotifyWindow() MUST run on the STA (the creating thread). It is
//                              called from RunDestructiveTeardown (Phase 2), which
//                              runs on the STA on BOTH the host-shutdown path (via
//                              RtdServer::ServerTerminate) AND the add-in-disable
//                              path (GracefulTeardownOnce non-host branch), AFTER
//                              JoinWorker — so no more PostMessages can arrive.
//   - On a FORCED DLL_PROCESS_DETACH with no graceful teardown (probe /
//     pathological FreeLibrary) the window LEAKS. That is the SAME accepted §20.2
//     residual as the intentionally-leaked hProcess / hShutdownEvent: we do NOT
//     DestroyWindow under the loader lock (a cross-thread DestroyWindow fails
//     anyway, and loader-lock UI calls are unsafe). The worker is stopped
//     (g_isUnloading is set in DETACH) so no new posts are made, and the WndProc's
//     g_isUnloading guard no-ops any already-queued message while the code is
//     still mapped.
//
// Header-only declarations; the impl lives in src/xll_rtd_notify.cpp (compiled by
// the generated CMake src/*.cpp glob, like xll_deferred_commands.cpp). The whole
// asset is XLL_RTD_ENABLED-gated.

namespace xll {

// Create the hidden HWND_MESSAGE notify window. MUST be called on the STA
// (xlAutoOpen), BEFORE the worker is started so the window exists before any RTD
// update can arrive. Idempotent (no-op if already created). Logs on failure,
// never throws.
void CreateRtdNotifyWindow();

// Coalesced, non-blocking signal that an RTD update is ready for Excel. Callable
// from ANY thread (the worker). Posts WM_XLLGEN_RTD_NOTIFY to the notify window;
// collapses a burst into a single pending post. No-op if unloading or the window
// does not exist. Never blocks, never throws.
void SignalRtdUpdate();

// Destroy the notify window. MUST be called on the STA (the creating thread),
// AFTER the worker has been joined (no more PostMessages). Idempotent +
// null-checked: safe to call when the window was never created. Never throws.
void DestroyRtdNotifyWindow();

} // namespace xll

#endif // XLL_RTD_ENABLED
