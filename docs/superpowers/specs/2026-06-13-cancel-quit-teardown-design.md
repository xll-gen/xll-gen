# Cancel-Quit Teardown — design (2026-06-13)

Author: Claude Fable 5 (design only; not yet implemented).

## 1. The bug (confirmed)

Excel calls `xlAutoClose` **before** the "Save changes? / Cancel" dialog when the
user quits / closes the last dirty workbook (confirmed against Excel-DNA's
*"AutoClose and Excel shutdown"* docs:
*"press Alt+F4, Excel will call xlAutoClose, and then display a dialog … if the
user selects Cancel the session will continue. However, if the add-in has
responded to the earlier xlAutoClose, it might now be removed although the
session still continues."*).

In xll-gen, `xll::OnAutoClose()` (`internal/assets/files/src/xll_lifecycle.cpp`)
does **irreversible** teardown at that too-early point:

- latches `g_isUnloading = true` (one-way; never reset),
- `SetEvent(hShutdownEvent)`, `StopWorker`/`JoinWorker`/monitor join,
- `delete g_phost` (SHM host → `g_host` becomes a null deref),
- `CloseHandle(g_procInfo.hJob)` → Job has `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`
  (`xll_launch.cpp:202`) so **the Go server process is killed**.

After a **cancelled** quit the DLL stays loaded but the add-in is a **zombie**:
`g_phost == nullptr` → every UDF hits the null-guard (`xll_main.cpp.tmpl`,
the `if (g_phost == nullptr) return &g_xlErrValue;` block) and returns `#VALUE!`;
RTD/commands dead; server gone; `g_isUnloading` stuck true; and **no second
`xlAutoOpen`** ever runs, so nothing re-initializes until Excel restarts.

## 2. Key discriminators

| Event | cancelled quit | real quit | add-in disabled (session continues) | forced FreeLibrary (probe/crash) |
|---|---|---|---|---|
| `xlAutoClose` | ✅ fires (before save prompt) | ✅ fires | ✅ fires | ✗ (skipped) |
| `IDTExtensibility2::OnBeginShutdown` (COM add-in only) | ✗ | ✅ fires (after save resolved) | ✗ | ✗ |
| `OnDisconnection(RemoveMode)` (COM add-in only) | ✗ | `ext_dm_HostShutdown` | `ext_dm_UserClosed` | ✗ |
| `DLL_PROCESS_DETACH` | **✗ (DLL stays loaded)** | ✅ | ✅ (FreeLibrary) | ✅ |
| Excel **process exits** (⇒ Job auto-reaps server) | ✗ | ✅ | ✗ | ✅ |

**`xlAutoClose` is the only callback that fires on a cancelled quit** — so it
must NOT do anything destructive. **`DLL_PROCESS_DETACH` and `OnBeginShutdown`
never fire on a cancelled quit** — so they are the safe places for destructive
teardown.

## 3. Recommended architecture — "non-destructive xlAutoClose + reap on real exit"

Universal (works with or without a COM add-in), minimal, and needs **no
recovery/re-init logic** because on a cancelled quit *nothing was torn down*.

### 3.1 Make `xlAutoClose` non-destructive
`xll::OnAutoClose()` must NOT: kill the server, `CloseHandle(hJob)`,
`delete g_phost`, stop/join the worker, or latch `g_isUnloading`. Ideally it
does nothing irreversible (log + `return 1`). Any prep it does must be
reversible if the quit is cancelled.

Rationale: if the quit is cancelled, the host/worker/server are all still alive
and the registered UDFs keep working — exactly the desired behavior.

### 3.2 Reap the server on REAL process exit — for free
The Job's `KILL_ON_JOB_CLOSE` already kills the Go server when the Excel
**process** terminates (the job handle closes on process exit). So on a real
quit we need no explicit server kill at all — OS process teardown reaps it.

### 3.3 `DLL_PROCESS_DETACH` = the universal destructive trigger
DETACH fires on real quit's final unload AND on add-in-disable (session
continues), but NEVER on a cancelled quit. Keep it within the §20.2 "leak,
don't crash" loader-lock rules, but it MAY safely:
- set `g_isUnloading = true`,
- `SetEvent(hShutdownEvent)` (kernel call — safe under loader lock),
- `CloseHandle(g_procInfo.hJob)` to kill the server in the **add-in-disable**
  case where the process keeps running (kernel call — safe; otherwise the
  server would be orphaned for the rest of the session),
- detach (NOT join) the worker/monitor threads (current behavior).
- Do **NOT** `delete g_phost` here (C++/SHM destructor under loader lock is
  unsafe); leak it — the process is exiting in the real-quit case, and in the
  add-in-disable case a one-session leak is acceptable per §20.2.

### 3.4 `OnBeginShutdown` (COM add-in present) = OPTIONAL graceful pre-teardown
Where a ribbon/command COM add-in exists (`RibbonAddIn::OnBeginShutdown`,
currently a no-op at `ribbon_addin.cpp:269`), do the **graceful** §23.0 drains
(`WaitForRtdConnectDrain`, `WaitForCommandDrain`) and a clean
`SetEvent(hShutdownEvent)` server shutdown HERE — it runs on the STA thread
(COM/C++ safe, not under loader lock) and fires AFTER the cancel decision but
BEFORE DETACH. This restores the *clean* shutdown the current `OnAutoClose`
drains provide, without the cancelled-quit hazard. Also handle
`OnDisconnection(ext_dm_UserClosed)` as the add-in-disable graceful path.
This is an enhancement; correctness does not depend on it (DETACH + Job is the
floor).

## 4. State model change
Replace the one-way `g_isUnloading` latch semantics: it must be set ONLY on a
confirmed real teardown (OnBeginShutdown / OnDisconnection(HostShutdown|UserClosed)
/ DLL_PROCESS_DETACH), never in `xlAutoClose`. Add a single idempotency guard so
the graceful teardown body runs exactly once regardless of which of
{OnBeginShutdown, OnDisconnection, DETACH} fires first (e.g. an
`std::atomic<bool> g_teardownDone` CAS at the top of the shared teardown fn).

## 5. ⚠️ Make-or-break experiment to run BEFORE coding (cannot run here)
The design assumes that after `xlAutoClose` + Cancel, Excel keeps the XLL's
functions REGISTERED and will still route calls to them (i.e. "removed" in the
Excel-DNA quote means the add-in's *own* cleanup, not Excel unregistering the
functions). **Verify on real Excel:**

1. Build an add-in whose `xlAutoClose` does nothing destructive (stub it to just
   `return 1`).
2. Open a workbook, type `=Add(2,3)` → 5. Make the workbook dirty.
3. Alt+F4 → at the "Save?" prompt press **Cancel**.
4. Recalc `=Add(2,3)`.
   - **Still 5** → design valid (Excel kept the XLL live). Proceed with §3.
   - **`#VALUE!`/`#NAME?`** → Excel unregistered the XLL on xlAutoClose; §3 is
     insufficient and we must additionally **re-register on cancel detection**
     (re-run the `xlfRegister` loop) — triggered from the first `CalculationEnded`
     after a cancelled `xlAutoClose`. Escalate this back as a design change.

Run the symmetric experiment for the COM-add-in ribbon tab (does it survive a
cancelled quit if `xlAutoClose` no longer disconnects it?).

## 6. Per-file change list (assuming experiment §5 passes)
- `internal/assets/files/src/xll_lifecycle.cpp`
  - `OnAutoClose()`: strip destructive steps; keep log + `return 1`. Remove the
    `g_isUnloading=true` latch from here.
  - `DllMain` `DLL_PROCESS_DETACH`: add `CloseHandle(hJob)` (server kill) to the
    existing signal+detach block; keep "don't delete g_phost / don't join".
  - Factor the graceful teardown (drains + clean shutdown event + handle close)
    into a single `static void GracefulTeardownOnce()` guarded by an atomic CAS,
    callable from both OnBeginShutdown and (best-effort) DETACH.
- `internal/assets/files/src/ribbon_addin.cpp`
  - `OnBeginShutdown`: call `GracefulTeardownOnce()` (via a hook exported from
    lifecycle, to keep ribbon TU decoupled).
  - `OnDisconnection`: on `ext_dm_HostShutdown` and `ext_dm_UserClosed`, also
    call `GracefulTeardownOnce()`.
- `internal/templates/xll_main.cpp.tmpl`
  - Generated `xlAutoClose`: remove the eager ribbon disconnect / CoRevoke /
    unregister / drain sequence from the early path; move what is destructive
    into the graceful-teardown hook driven by the COM events above. Keep the
    `g_phost == nullptr` UDF guard (defensive) but it should no longer be the
    steady-state after a cancelled quit.
  - Ensure `xlAutoClose` for **non-ribbon** add-ins is also non-destructive
    (relies solely on §3.2/§3.3).

## 7. §20.2 / §23.0 reconciliation
- §20.2 ("leak, don't crash" at DETACH) is preserved and slightly extended
  (adds the loader-lock-safe `CloseHandle(hJob)` server kill for the
  add-in-disable case). No thread joins, no g_phost delete added.
- §23.0 drains move from `OnAutoClose` to `GracefulTeardownOnce()` (run from
  OnBeginShutdown on the STA thread — a *safer* context than today). The UAF
  window §23.0 guards (threads touching `g_phost` after delete) actually
  **shrinks**, because g_phost is no longer deleted at all (leaked on real exit;
  OS reclaims). Re-audit with `memory-safety-auditor` after implementation.

## 8. Open decisions for the user
1. **Scope now**: fix the ribbon/command path (covers the showcase) only, or also
   the pure-function (no-COM-add-in) path? The §3 design already covers both via
   DETACH+Job, so recommend doing both at once.
2. **If experiment §5 fails** (Excel unregisters the XLL on xlAutoClose): adopt
   the re-registration-on-cancel approach (heavier). Decide then.
3. Whether to keep a clean `SetEvent(hShutdownEvent)` graceful server stop at all,
   or accept the Job hard-kill on process exit as the only path (simpler).

## 9. Test plan
- Generator/unit: assert generated `xlAutoClose` no longer contains the server
  kill / `g_phost` delete / `g_isUnloading=true`; assert the COM-event teardown
  hook is emitted when ribbon/commands enabled.
- Manual Excel smoke (the scenario): dirty workbook → Alt+F4 → **Cancel** →
  confirm UDFs/RTD/ribbon still work; then a real quit → confirm the Go server
  process actually exits (no orphan) and no crash; then add-in disable via the
  Add-ins dialog → confirm server is reaped (not orphaned).
- `memory-safety-auditor` pass for the relocated drains / g_phost leak.
