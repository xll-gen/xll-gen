# Design: Native Ribbon UI + Command (Macro) Support for xll-gen

- **Date:** 2026-06-11
- **Status:** Approved (brainstorming complete; pending implementation plan)
- **Repos affected:** `xll-gen` (primary), `types` (protocol.fbs regen), `sugar` (small follow-up only)
- **Implementation model:** Opus agents (per user request)

## 0. Goal & Decision Record

Add native Excel Ribbon buttons to xll-gen-generated XLL add-ins, where clicking a
button invokes a user-written Go handler ("macro"). Inside the handler, users may
use `sugar` to manipulate the running Excel instance.

Decisions made during brainstorming:

| Decision | Choice | Rejected alternative |
|---|---|---|
| Host repo | **xll-gen** (native ribbon embedded in XLL) | sugar runtime CommandBars: only legacy "Add-Ins"-tab buttons, requires building COM event-sink infra in sugar from zero, conflicts with sugar AGENTS.md §2.3 ("Excel→Go callbacks are xll-gen's job") |
| Ribbon XML authoring | **C: hybrid** — structured YAML (generated XML) *or* raw customUI XML file; mutually exclusive | A: YAML-only; B: raw-XML-only |
| Click dispatch | **Fire-and-forget** (onAction returns immediately; handler runs in a goroutine) | Synchronous wait: deadlocks Excel's STA when the handler calls back into Excel via sugar/COM |
| Handler ↔ sugar coupling | xll-gen does **not** depend on sugar; handlers are plain Go and may *optionally* use sugar | Generated code importing sugar |

## 1. Architecture: XLL as its own COM add-in helper

### 1.1 Mechanism (the Excel-DNA-verified path)

The XLL C API has **no** function for supplying ribbon XML. Excel only requests
ribbon XML through COM: `IRibbonExtensibility::GetCustomUI`, called on a loaded
**COM add-in**. Therefore the XLL DLL itself doubles as a COM add-in helper:

1. `xlAutoOpen` (after existing init) registers the DLL as a COM add-in under
   **HKCU** (no admin rights), keyed by a per-add-in CLSID/ProgID.
2. It then asks Excel to connect the COM add-in
   (`Application.COMAddIns(progId).Connect = True` via COM automation on the
   in-process `Application` object). To obtain that object from inside the XLL:
   `xlGetHwnd` → enumerate child windows for class `EXCEL7` →
   `AccessibleObjectFromWindow(hwnd, OBJID_NATIVEOM, IID_IDispatch, ...)` →
   `.Application`. This is the standard in-process acquisition path (same trick
   Excel-DNA uses); it requires `oleacc.lib`.
3. Excel instantiates the class via the DLL's existing `DllGetClassObject`
   export, sees `IRibbonExtensibility`, and calls `GetCustomUI(id)`.
4. `GetCustomUI` returns the ribbon XML embedded in the XLL at build time.
5. Ribbon button callbacks (`onAction`) arrive via `IDispatch::Invoke` on the
   same object.
6. `xlAutoClose` disconnects the COM add-in and removes the HKCU registration
   (best-effort; idempotent re-registration on next load).

### 1.2 Reuse of existing COM infrastructure

The RTD feature already gives the XLL a COM server skeleton. New work is one COM
class plus registry keys — not a COM stack.

| Need | Status | Location |
|---|---|---|
| `DllGetClassObject` export | exists — add a CLSID branch | `internal/assets/files/include/rtd/entry.h` (macro to be extended or superseded by a multi-class dispatch) |
| `IClassFactory` | exists — `rtd::ClassFactory<T>` template, reuse as-is | `internal/assets/files/include/rtd/factory.h` |
| Registry helpers | exists — extend for `Software\Microsoft\Office\Excel\Addins\<ProgID>` keys | `internal/assets/files/include/rtd/registry.h` |
| IUnknown/IDispatch patterns | exists (RTD server) | `internal/assets/files/include/rtd/server.h` |
| Click → Go IPC | exists — reuse SHM request path | `internal/assets/files/src/xll_ipc.cpp` |
| **`RibbonAddIn` COM class** | **new** — implements `IDTExtensibility2` + `IRibbonExtensibility` + `IDispatch` | new header `internal/assets/files/include/rtd/ribbon.h` (or sibling `com/` dir — implementer's choice) |

`IDTExtensibility2` methods (`OnConnection`, `OnDisconnection`, etc.) are
required for a loadable COM add-in; implementations are mostly empty.
`IDispatch::Invoke` resolves `onAction` callback names to ribbon-dispatch calls
(one generic dispatch — callback names arrive as the Invoke member name via
`GetIDsOfNames`; map name → command, send IPC, return immediately).

### 1.3 End-to-end flow

```
xlAutoOpen
  → register HKCU COM-addin keys → connect via COMAddIns → Excel loads RibbonAddIn
  → GetCustomUI() returns embedded XML → ribbon tab/buttons appear
button click
  → IDispatch::Invoke("RunReport", controlId)
  → CommandInvokeRequest over SHM (non-blocking) → return immediately (STA freed)
Go server
  → route by command_name → run handler in goroutine (panic-recovered)
  → handler optionally attaches to Excel with sugar and manipulates it
```

### 1.4 Risk: locked-down HKCU (graceful degradation is REQUIRED)

In group-policy-restricted environments (e.g. central-bank desktops), HKCU
writes under Office add-in keys may be blocked. This is a known Excel-DNA
failure mode ("The Ribbon/COM Add-in helper ... could not be registered").

**Degradation contract:** if COM-addin registration or connection fails:
- Log a clear warning (existing `xll_log` path).
- Worksheet functions, RTD, async — everything existing — must work unchanged.
- Registered `commands:` remain invocable via keyboard shortcut and by typing
  the command name into the Alt+F8 dialog (XLL commands are runnable there but
  not listed), because command registration (`xlfRegister`, macroType=2) does
  **not** depend on the ribbon/COM path.
- No error dialogs at startup; failure is silent except logs.

## 2. `xll.yaml` schema

Two new top-level sections. `commands` registers macros; `ribbon` (optional)
references them. Separation mirrors Excel's model: a command is independently
invocable (ribbon, shortcut, Alt+F8); a button merely points at one.

```yaml
commands:                         # XLL commands == Go handlers (macroType=2)
  - name: RunReport               # exported command name (runnable by typing it in Alt+F8; XLL commands are not LISTED there)
    description: "월간 리포트 생성"
    handler: RunReport            # Go method name; defaults to name
    shortcut: "R"          # bound as Ctrl+Shift+R (single letter)

ribbon:                           # optional; two MUTUALLY EXCLUSIVE modes
  # -- mode 1: structured (xll-gen generates customUI XML) --
  tab: "내 도구"
  groups:
    - label: "리포트"
      buttons:
        - label: "월간 리포트"
          command: RunReport      # must match a commands[].name
          size: large             # large | normal (default normal)
          image: "report"         # optional imageMso name
  # -- mode 2: raw XML escape hatch --
  # xml: "ribbon.xml"             # path relative to xll.yaml
```

Validation rules (in `internal/config/config.go` `Validate()`):
- `ribbon.xml` and `ribbon.tab/groups` are mutually exclusive; error if both.
- Every `buttons[].command` must name an existing `commands[].name`.
- In raw-XML mode, parse the XML and check every `onAction="X"` has a matching
  `commands[].name == X`; unknown names are a **build error** (fail fast, not a
  runtime no-op).
- `commands[].name` must not collide with `functions[].name` (single xlfRegister
  namespace).
- `commands[].name` must match `[A-Za-z0-9_]+` with no leading digit (valid C/Go
  identifier; used to derive the exported `Cmd_*` proc and the handler method).
- `commands` without `ribbon` is valid (shortcut/Alt+F8 only). `ribbon` without
  `commands` is an error.

Structured-mode XML generation produces a single `<customUI>` (Office 2010+
namespace `.../2009/07/customui`... — implementer must verify the namespace
version against what the targeted Excel builds accept; 2006/01 is the safe
baseline) with one `<tab>`, N `<group>`, buttons with
`onAction="<command name>"`. Generated XML is embedded in the C++ build the same
way other generated assets are (string literal in a generated header is fine —
ribbon XML is small; no resource-compiler step needed).

## 3. IPC protocol

### 3.1 protocol.fbs additions

```flatbuffers
table CommandInvokeRequest {
  command_name: string;     // routes to the Go handler
  control_id:   string;     // clicked ribbon control id (handler context)
}

table CommandInvokeResponse {  // delivery ack, NOT handler completion
  ok:    bool;
  error: string;            // unknown command_name / dispatch failure
}
```

### 3.2 Message ID

`internal/assets/files/include/xll_ipc.h` (and the Go mirror in
`pkg/server/types.go` — keep in sync per shm-protocol discipline):

```cpp
#define MSG_COMMAND_INVOKE 137   // fills the 137-139 gap after MSG_RTD_HEARTBEAT 136
```

### 3.3 Threading contract (the load-bearing rule)

`onAction` arrives on Excel's main STA thread. The C++ side sends
`CommandInvokeRequest` and **returns immediately**. It must NOT wait for handler
completion: the handler may call back into Excel via sugar/COM, which marshals
to the same STA thread — a synchronous wait deadlocks Excel.

Consequences:
- The response is only a routing ack (logged on failure; nothing surfaced to UI).
- Handler errors cannot reach the cell/UI through this channel; see §4.
- Go side runs each command handler in its own goroutine with panic recovery,
  exactly like `HandleRtdConnect` / `HandleCalculationCanceled` in
  `pkg/server/handlers.go`.

### 3.4 Shortcut registration

`shortcut:` uses the same key-binding mechanism the existing `functions[].shortcut`
field uses at registration time (xlfRegister's shortcut argument applies to
commands; macroType=2 supports it natively). No extra protocol needed.

## 4. Go handler API (user-facing)

### 4.1 Generated interface additions (`interface.go.tmpl`)

One method per `commands:` entry, following the existing event-handler shape
(ctx first, `error` return):

```go
type XllService interface {
    // ... existing functions/events ...
    RunReport(ctx context.Context, cmd CommandContext) error
}
```

### 4.2 CommandContext

```go
type CommandContext struct {
    CommandName string // invoked command name
    ControlID   string // ribbon control id ("" when invoked via shortcut/Alt+F8)
    ExcelPID    uint32 // parent Excel PID, for multi-instance sugar attach
}
```

Defined in generated code (or a small runtime package under `pkg/`), NOT in
sugar — xll-gen stays sugar-free.

### 4.3 Example user handler

```go
func (s *Service) RunReport(ctx context.Context, cmd CommandContext) error {
    app, err := excel.GetApplication() // sugar attach to running Excel
    if err != nil {
        return err // logged server-side; cannot reach UI (fire-and-forget)
    }
    defer app.Context().Release()

    return app.Books().Active().Sheets().Active().
        Range("A1").SetValue("리포트 생성 완료").Err()
}
```

### 4.4 Error surfacing (v1 scope)

Fire-and-forget means errors don't propagate to Excel automatically. v1: server
logs handler errors/panics (existing log path). User-visible feedback is the
handler's own job (sugar StatusBar/MsgBox/cell write). Richer feedback (e.g. a
generated toast/statusbar helper) is out of scope for v1.

### 4.5 sugar follow-up (separate, small, NOT in this implementation)

`excel.GetApplication()` currently attaches to *an* active instance. Multi-
instance targeting by PID (`excel.GetApplicationByPID(pid uint32)`) is a small
sugar-side backlog item. v1 of ribbon support works as-is for the
single-instance (overwhelmingly common) case. Record in sugar's backlog; do not
implement here (scope-locality rule).

## 5. Testing

- **config:** table tests for the §2 validation rules (mutual exclusion,
  dangling `command:` refs, onAction cross-check in raw-XML mode, name
  collisions).
- **XML generation:** golden-file test — structured yaml in, expected customUI
  XML out.
- **Go dispatch:** unit test `HandleCommandInvoke` routing (known name → handler
  called with correct CommandContext; unknown name → ok=false response; panicking
  handler → recovered + logged, server alive).
- **C++ side:** registration-test binary (`regtest_main.cpp.tmpl` pattern) gains
  a case asserting commands register with macroType=2 and functions stay
  macroType=1.
- **E2E (manual + scripted where the harness allows):** build example add-in
  with one ribbon button; verify tab appears, click writes to a cell via sugar
  handler; verify HKCU-blocked simulation (point registration at an unwritable
  key) degrades gracefully — functions still work, warning logged.
  Excel-spawning tests follow the two-tier cleanup rule (graceful exit then
  force-kill).

## 6. Out of scope (v1)

- Dynamic ribbon (invalidate/refresh, `getEnabled`/`getLabel` callbacks).
- Ribbon controls beyond buttons (dropdowns, galleries, menus) in structured
  mode — raw-XML mode covers them only if their callbacks are all `onAction`-shaped;
  other callback signatures (getLabel etc.) are rejected by validation in v1.
- Handler → Excel result channel (beyond what sugar does in-handler).
- sugar `GetApplicationByPID` (separate sugar backlog item).
- Custom button images (file-based); `imageMso` names only.

## 7. Co-change checklist (release discipline)

- `protocol.fbs` change → regen FlatBuffers in `types` repo → version pin bump
  in consumers (cross-repo-coordinator check before tagging).
- `xll_ipc.h` MSG id ↔ `pkg/server/types.go` mirror (shm-protocol-guardian).
- New C++ assets reviewed against XLL SDK rules (xll-cpp-reviewer) — especially
  DllMain/unload behavior of the new COM class and `xlAutoClose` disconnect
  ordering.
- AGENTS.md (xll-gen): document the new commands/ribbon co-change cluster.
- sugar AGENTS.md backlog: add `GetApplicationByPID` row.
