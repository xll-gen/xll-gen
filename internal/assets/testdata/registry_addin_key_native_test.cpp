// Offline unit test for rtd::RegisterOfficeAddinKey's LoadBehavior policy.
//
// WHAT THIS EXISTS FOR (backlog "2026-08-03 MED 사이클 파생", first line).
// RegisterOfficeAddinKey runs on every xlAutoOpen and used to write
// LoadBehavior=0 UNCONDITIONALLY. While the graceful teardown still deleted
// HKCU\...\Excel\Addins\<progId> that was harmless — the key was recreated from
// scratch each session, so there was never a previous value to destroy. v0.8.50
// stopped deleting it (the key IS the COM Add-ins dialog's row source, so
// deleting it on an add-in DISABLE removed the row the user needs to tick back
// on), and from that release onwards the unconditional write became a real
// defect: Office records a user's re-tick as LoadBehavior=3, and the next load
// silently reset it to 0.
//
// So the invariant under test is a two-case one, and only a test that can tell
// the cases apart is worth anything:
//   * value ABSENT  (fresh install)      -> we write 0, because we connect
//                                           programmatically from xlAutoOpen and
//                                           must not autoload at Excel startup.
//   * value PRESENT (any prior session)  -> we PRESERVE it, whatever it is. That
//                                           byte belongs to Office and the user.
//
// WHY A REAL-REGISTRY TEST. RegCreateKeyExW/RegSetValueExW are real Win32 calls
// with no seam to fake, and the whole defect lives in the ORDER of two of them —
// a source-level grep can pin the shape but cannot show that the value survives.
// The blast radius is contained with RegOverridePredefKey, which remaps
// HKEY_CURRENT_USER for THIS PROCESS ONLY onto a scratch key; the real
// HKCU\Software\Microsoft\Office\Excel\Addins is never opened, let alone written.
// Two independent belts, because a test that silently writes to the live Office
// hive would be far worse than no test:
//   1. the override's return code is checked and the harness ABORTS (exit 2, no
//      registry writes attempted) if it did not take effect;
//   2. the ProgID is a scratch name carrying this process's PID, and the cleanup
//      deletes it from the REAL hive too after the override is lifted — a no-op
//      when belt 1 held, a repair when it did not.
//
// Build/run: driven by internal/assets/registry_addin_key_cpp_test.go
// (TestRegisterOfficeAddinKeyLoadBehaviorPolicy). Exit code 0 and "0 failures"
// on stdout mean pass.

#include "rtd/registry.h"

#include <cstdio>
#include <string>
#include <windows.h>

namespace {

int g_failures = 0;
int g_checks = 0;

void Check(bool ok, const std::string& what) {
    ++g_checks;
    if (!ok) {
        ++g_failures;
        std::printf("FAIL: %s\n", what.c_str());
    }
}

void CheckEqDword(DWORD got, DWORD want, const std::string& what) {
    ++g_checks;
    if (got != want) {
        ++g_failures;
        std::printf("FAIL: %s\n      got  %lu\n      want %lu\n", what.c_str(),
                    static_cast<unsigned long>(got), static_cast<unsigned long>(want));
    }
}

void CheckEqStr(const std::wstring& got, const std::wstring& want, const std::string& what) {
    ++g_checks;
    if (got != want) {
        ++g_failures;
        std::printf("FAIL: %s (wide string mismatch, len got=%u want=%u)\n", what.c_str(),
                    static_cast<unsigned>(got.size()), static_cast<unsigned>(want.size()));
    }
}

// ---------------------------------------------------------------------------
// Scratch-hive plumbing
// ---------------------------------------------------------------------------

std::wstring g_progId;    // scratch ProgID, unique per process
std::wstring g_addinsKey; // Software\Microsoft\Office\Excel\Addins\<g_progId>
std::wstring g_scratch;   // real-HKCU path of the sandbox root
HKEY g_hScratch = nullptr;

// The Addins key as RegisterOfficeAddinKey itself builds it. Under the override
// this resolves inside the sandbox; after the override is lifted it resolves in
// the real hive (which is what the belt-2 cleanup wants).
std::wstring AddinsKeyPath() {
    return L"Software\\Microsoft\\Office\\Excel\\Addins\\" + g_progId;
}

bool OpenAddinsKey(HKEY* out, REGSAM sam) {
    return RegOpenKeyExW(HKEY_CURRENT_USER, g_addinsKey.c_str(), 0, sam, out) == ERROR_SUCCESS;
}

// Deletes the add-in key so the next case starts from a known state.
void ResetAddinsKey() { RegDeleteTreeW(HKEY_CURRENT_USER, g_addinsKey.c_str()); }

// Writes LoadBehavior directly, standing in for what Office does when the user
// ticks the box in the COM Add-ins dialog.
bool SeedLoadBehavior(DWORD value) {
    HKEY hKey = nullptr;
    if (RegCreateKeyExW(HKEY_CURRENT_USER, g_addinsKey.c_str(), 0, nullptr, REG_OPTION_NON_VOLATILE,
                        KEY_SET_VALUE, nullptr, &hKey, nullptr) != ERROR_SUCCESS) {
        return false;
    }
    const long rc = RegSetValueExW(hKey, L"LoadBehavior", 0, REG_DWORD,
                                   reinterpret_cast<const BYTE*>(&value), sizeof(value));
    RegCloseKey(hKey);
    return rc == ERROR_SUCCESS;
}

bool SeedLoadBehaviorAsString(const std::wstring& value) {
    HKEY hKey = nullptr;
    if (RegCreateKeyExW(HKEY_CURRENT_USER, g_addinsKey.c_str(), 0, nullptr, REG_OPTION_NON_VOLATILE,
                        KEY_SET_VALUE, nullptr, &hKey, nullptr) != ERROR_SUCCESS) {
        return false;
    }
    const long rc = RegSetValueExW(
        hKey, L"LoadBehavior", 0, REG_SZ, reinterpret_cast<const BYTE*>(value.c_str()),
        static_cast<DWORD>((value.size() + 1) * sizeof(wchar_t)));
    RegCloseKey(hKey);
    return rc == ERROR_SUCCESS;
}

// Reads LoadBehavior as a DWORD. Returns false when the value is absent or is
// not a REG_DWORD.
bool ReadLoadBehavior(DWORD* out) {
    HKEY hKey = nullptr;
    if (!OpenAddinsKey(&hKey, KEY_QUERY_VALUE)) return false;
    DWORD type = 0, data = 0, cb = sizeof(data);
    const long rc = RegQueryValueExW(hKey, L"LoadBehavior", nullptr, &type,
                                     reinterpret_cast<LPBYTE>(&data), &cb);
    RegCloseKey(hKey);
    if (rc != ERROR_SUCCESS || type != REG_DWORD) return false;
    *out = data;
    return true;
}

// Returns the raw type of LoadBehavior, or REG_NONE when it does not exist.
DWORD LoadBehaviorType() {
    HKEY hKey = nullptr;
    if (!OpenAddinsKey(&hKey, KEY_QUERY_VALUE)) return REG_NONE;
    DWORD type = 0, cb = 0;
    const long rc = RegQueryValueExW(hKey, L"LoadBehavior", nullptr, &type, nullptr, &cb);
    RegCloseKey(hKey);
    return rc == ERROR_SUCCESS ? type : REG_NONE;
}

bool ReadString(const wchar_t* name, std::wstring* out) {
    HKEY hKey = nullptr;
    if (!OpenAddinsKey(&hKey, KEY_QUERY_VALUE)) return false;
    wchar_t buf[512];
    DWORD type = 0, cb = sizeof(buf);
    const long rc =
        RegQueryValueExW(hKey, name, nullptr, &type, reinterpret_cast<LPBYTE>(buf), &cb);
    RegCloseKey(hKey);
    if (rc != ERROR_SUCCESS || type != REG_SZ) return false;
    const size_t chars = cb / sizeof(wchar_t);
    out->assign(buf, chars > 0 && buf[chars - 1] == L'\0' ? chars - 1 : chars);
    return true;
}

// ---------------------------------------------------------------------------
// Cases
// ---------------------------------------------------------------------------

// FRESH INSTALL: no prior value -> we write 0. This half is the pre-existing,
// still-correct behavior (AGENTS.md §18.11.5: the surviving key must autoload
// nothing at Excel startup; it is inert until something connects it), and it is
// asserted here so a fix for the preserve case cannot quietly drop it.
void TestFreshInstallWritesZero() {
    ResetAddinsKey();

    const HRESULT hr = rtd::RegisterOfficeAddinKey(g_progId.c_str(), L"Friendly One", L"Desc One");
    Check(hr == S_OK, "FreshInstall: RegisterOfficeAddinKey must return S_OK");

    DWORD lb = 0xDEADBEEF;
    Check(ReadLoadBehavior(&lb), "FreshInstall: LoadBehavior must exist as a REG_DWORD");
    CheckEqDword(lb, 0, "FreshInstall: LoadBehavior must be 0 (we connect programmatically)");

    std::wstring s;
    Check(ReadString(L"FriendlyName", &s), "FreshInstall: FriendlyName must exist");
    CheckEqStr(s, L"Friendly One", "FreshInstall: FriendlyName");
    Check(ReadString(L"Description", &s), "FreshInstall: Description must exist");
    CheckEqStr(s, L"Desc One", "FreshInstall: Description");
}

// THE REGRESSION. Office records a user's tick in the COM Add-ins dialog as
// LoadBehavior=3. The next xlAutoOpen must leave it alone.
void TestUserTickSurvivesReload() {
    ResetAddinsKey();
    Check(SeedLoadBehavior(3), "UserTick: seeding LoadBehavior=3 must succeed");

    const HRESULT hr = rtd::RegisterOfficeAddinKey(g_progId.c_str(), L"Friendly Two", L"Desc Two");
    Check(hr == S_OK, "UserTick: RegisterOfficeAddinKey must return S_OK");

    DWORD lb = 0xDEADBEEF;
    Check(ReadLoadBehavior(&lb), "UserTick: LoadBehavior must still exist as a REG_DWORD");
    CheckEqDword(lb, 3,
                 "UserTick: an EXISTING LoadBehavior must be PRESERVED — writing 0 here silently "
                 "unticks the box the user just ticked in the COM Add-ins dialog");

    // FriendlyName/Description stay ours: they describe the add-in, not its load
    // policy, so refreshing them on every load is correct.
    std::wstring s;
    Check(ReadString(L"FriendlyName", &s), "UserTick: FriendlyName must exist");
    CheckEqStr(s, L"Friendly Two", "UserTick: FriendlyName must be refreshed (ours to own)");
    Check(ReadString(L"Description", &s), "UserTick: Description must exist");
    CheckEqStr(s, L"Desc Two", "UserTick: Description must be refreshed (ours to own)");
}

// Every other load-policy value Office can leave behind is equally not ours.
// 8|1 = load-on-demand + connected, 16 = "connect first time, then on demand".
void TestOtherLoadBehaviorsPreserved() {
    const DWORD values[] = {1, 2, 8, 9, 16};
    for (size_t i = 0; i < sizeof(values) / sizeof(values[0]); ++i) {
        ResetAddinsKey();
        Check(SeedLoadBehavior(values[i]), "OtherValues: seeding must succeed");
        rtd::RegisterOfficeAddinKey(g_progId.c_str(), L"F", L"D");
        DWORD lb = 0xDEADBEEF;
        Check(ReadLoadBehavior(&lb), "OtherValues: LoadBehavior must still exist");
        CheckEqDword(lb, values[i], "OtherValues: existing LoadBehavior must be preserved");
    }
}

// A 0 that is already there is left alone too — i.e. the second session of a
// normal install is a no-op on this value, not a rewrite. Cheap to assert, and
// it pins that the preserve branch is decided on EXISTENCE, not on the value.
void TestExistingZeroIsNotRewritten() {
    ResetAddinsKey();
    Check(SeedLoadBehavior(0), "ExistingZero: seeding LoadBehavior=0 must succeed");
    rtd::RegisterOfficeAddinKey(g_progId.c_str(), L"F", L"D");
    DWORD lb = 0xDEADBEEF;
    Check(ReadLoadBehavior(&lb), "ExistingZero: LoadBehavior must still exist");
    CheckEqDword(lb, 0, "ExistingZero: LoadBehavior must still be 0");
}

// EXISTENCE, NOT READABILITY. A LoadBehavior that is not a REG_DWORD is not
// something we put there, so it is not ours to correct either — and an
// implementation that probes with a typed DWORD read would treat it as absent
// and clobber it. Pins the null-buffer existence probe.
void TestNonDwordValuePreserved() {
    ResetAddinsKey();
    Check(SeedLoadBehaviorAsString(L"3"), "NonDword: seeding a REG_SZ LoadBehavior must succeed");
    rtd::RegisterOfficeAddinKey(g_progId.c_str(), L"F", L"D");
    CheckEqDword(LoadBehaviorType(), REG_SZ,
                 "NonDword: a non-REG_DWORD LoadBehavior must be left exactly as found — the "
                 "existence probe must not depend on the value's type");
    std::wstring s;
    Check(ReadString(L"LoadBehavior", &s), "NonDword: the REG_SZ value must still be readable");
    CheckEqStr(s, L"3", "NonDword: the REG_SZ payload must be unchanged");
}

// The guard clause, kept honest while we are here.
void TestRejectsEmptyProgId() {
    Check(rtd::RegisterOfficeAddinKey(nullptr, L"F", L"D") == E_INVALIDARG,
          "EmptyProgId: nullptr must be rejected");
    Check(rtd::RegisterOfficeAddinKey(L"", L"F", L"D") == E_INVALIDARG,
          "EmptyProgId: empty string must be rejected");
}

} // namespace

int main() {
    wchar_t pidBuf[32];
    const DWORD pid = GetCurrentProcessId();
    std::swprintf(pidBuf, 32, L"%lu", static_cast<unsigned long>(pid));

    g_progId = std::wstring(L"XllGenScratch.RegistryHarness.") + pidBuf;
    g_addinsKey = AddinsKeyPath();
    g_scratch = std::wstring(L"Software\\xll-gen-test\\registry_addin_") + pidBuf;

    // --- Sandbox: remap HKEY_CURRENT_USER for this process only. ---
    if (RegCreateKeyExW(HKEY_CURRENT_USER, g_scratch.c_str(), 0, nullptr, REG_OPTION_NON_VOLATILE,
                        KEY_ALL_ACCESS, nullptr, &g_hScratch, nullptr) != ERROR_SUCCESS) {
        std::printf("ABORT: could not create the scratch key HKCU\\%ls\n", g_scratch.c_str());
        return 2;
    }
    const long ovr = RegOverridePredefKey(HKEY_CURRENT_USER, g_hScratch);
    if (ovr != ERROR_SUCCESS) {
        // Belt 1. Nothing has been written to the Addins tree at this point and
        // nothing will be: bail out rather than touch the live Office hive.
        std::printf("ABORT: RegOverridePredefKey(HKCU) failed with %ld; refusing to run against "
                    "the real hive\n",
                    ovr);
        RegCloseKey(g_hScratch);
        RegDeleteTreeW(HKEY_CURRENT_USER, g_scratch.c_str());
        return 2;
    }

    TestFreshInstallWritesZero();
    TestUserTickSurvivesReload();
    TestOtherLoadBehaviorsPreserved();
    TestExistingZeroIsNotRewritten();
    TestNonDwordValuePreserved();
    TestRejectsEmptyProgId();

    // --- Teardown: lift the override FIRST, then clean the real hive. ---
    RegOverridePredefKey(HKEY_CURRENT_USER, nullptr);
    RegCloseKey(g_hScratch);
    g_hScratch = nullptr;
    RegDeleteTreeW(HKEY_CURRENT_USER, g_scratch.c_str());
    RegDeleteKeyW(HKEY_CURRENT_USER, L"Software\\xll-gen-test"); // fails if other runs are live
    // Belt 2: a no-op when the override held; a repair when it did not.
    RegDeleteTreeW(HKEY_CURRENT_USER, g_addinsKey.c_str());

    std::printf("registry_addin_key_native_test: %d checks, %d failures\n", g_checks, g_failures);
    return g_failures == 0 ? 0 : 1;
}
