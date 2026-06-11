# Ribbon/Command E2E checklist

Manual verification on a real Excel machine. Run before any release that
touches ribbon/command code paths.

1. Build an example add-in with a ribbon (`commands` + `ribbon` in `xll.yaml`); open the `.xll` in Excel.
2. The custom tab appears with the declared buttons (Korean labels render correctly — they are embedded as XML numeric character references).
3. Click a button → the Go handler runs (e.g. writes to A1 via sugar). During a slow handler (add a 5s sleep), Excel stays responsive — proves the fire-and-forget STA contract.
4. Ctrl+Shift+`<letter>` invokes the handler without using the ribbon.
5. Alt+F8 → type the command name → Run → handler runs (XLL commands are runnable but not listed).
6. Graceful degradation: make the HKCU add-in keys unwritable (ACL the key read-only, or run with a registry-restricted test account) → warning in the native log, NO error dialog, worksheet functions and the Ctrl+Shift shortcut still work.
7. Quit Excel → clean exit (no crash — the explicit COMAddIns disconnect in `xlAutoClose` releases Excel's reference while the DLL is mapped); no orphan Go server process; no leaked SHM; HKCU ribbon keys removed.

Excel-spawning automated tests MUST follow the two-tier cleanup rule:
graceful exit first, then force-kill on timeout — never just defer.
