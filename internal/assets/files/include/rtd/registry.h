#ifndef RTD_REGISTRY_H
#define RTD_REGISTRY_H

#include <windows.h>
#include <string>
#include <vector>

namespace rtd {

    /**
     * @brief Helper to delete registry keys in HKCU (Current User).
     */
    inline long DeleteKeyUser(const wchar_t* szKey) {
        if (!szKey || !*szKey) return E_INVALIDARG;

        std::wstring szFullKey = L"Software\\Classes\\";
        szFullKey += szKey;

        // RegDeleteTreeW is available since Vista, which is safe to assume.
        // It recursively deletes the key and all subkeys.
        return RegDeleteTreeW(HKEY_CURRENT_USER, szFullKey.c_str());
    }

    /**
     * @brief Helper to set registry keys in HKCU (Current User).
     * This avoids the need for Administrator privileges.
     */
    inline long SetKeyAndValueUser(const wchar_t* szKey, const wchar_t* szSubkey, const wchar_t* szValue) {
        if (!szKey || !*szKey) return E_INVALIDARG;

        HKEY hKey;
        std::wstring szFullKey = L"Software\\Classes\\";
        szFullKey += szKey;
        if (szSubkey) {
            szFullKey += L"\\";
            szFullKey += szSubkey;
        }

        if (RegCreateKeyExW(HKEY_CURRENT_USER, szFullKey.c_str(), 0, nullptr, REG_OPTION_NON_VOLATILE, KEY_SET_VALUE, nullptr, &hKey, nullptr) != ERROR_SUCCESS)
            return E_FAIL;

        if (szValue) {
            RegSetValueExW(hKey, nullptr, 0, REG_SZ, (const BYTE*)szValue, (wcslen(szValue) + 1) * sizeof(wchar_t));
        }
        RegCloseKey(hKey);
        return S_OK;
    }

    /**
     * @brief Helper to register the COM server.
     */
    inline HRESULT RegisterServer(HMODULE hModule, const GUID& clsid, const wchar_t* progID, const wchar_t* friendlyName) {
        std::vector<wchar_t> szModule(MAX_PATH);
        DWORD len = GetModuleFileNameW(hModule, szModule.data(), static_cast<DWORD>(szModule.size()));

        while (len == szModule.size() && szModule.size() < 32768) {
            szModule.resize(szModule.size() * 2);
            len = GetModuleFileNameW(hModule, szModule.data(), static_cast<DWORD>(szModule.size()));
        }

        if (len == 0 || len == szModule.size()) return E_FAIL;

        LPOLESTR pszCLSID;
        StringFromCLSID(clsid, &pszCLSID);
        std::wstring clsidStr(pszCLSID);
        CoTaskMemFree(pszCLSID);

        std::wstring szCLSIDKey = L"CLSID\\" + clsidStr;

        // 1. ProgID -> CLSID
        if (FAILED(SetKeyAndValueUser(progID, nullptr, friendlyName))) return E_FAIL;
        if (FAILED(SetKeyAndValueUser(progID, L"CLSID", clsidStr.c_str()))) return E_FAIL;

        // 2. CLSID -> DLL Path
        if (FAILED(SetKeyAndValueUser(szCLSIDKey.c_str(), nullptr, friendlyName))) return E_FAIL;
        if (FAILED(SetKeyAndValueUser(szCLSIDKey.c_str(), L"ProgID", progID))) return E_FAIL;
        if (FAILED(SetKeyAndValueUser(szCLSIDKey.c_str(), L"InprocServer32", szModule.data()))) return E_FAIL;

        // ThreadingModel = Apartment is crucial for Excel RTD
        std::wstring szInprocKey = szCLSIDKey + L"\\InprocServer32";

        // Re-open InprocServer32 key to set ThreadingModel
        HKEY hKey;
        std::wstring szFullKey = L"Software\\Classes\\";
        szFullKey += szInprocKey;

        if (RegOpenKeyExW(HKEY_CURRENT_USER, szFullKey.c_str(), 0, KEY_SET_VALUE, &hKey) == ERROR_SUCCESS) {
            const wchar_t* threading = L"Both";
            RegSetValueExW(hKey, L"ThreadingModel", 0, REG_SZ, (const BYTE*)threading, (wcslen(threading) + 1) * sizeof(wchar_t));
            RegCloseKey(hKey);
        }

        return S_OK;
    }

    /**
     * @brief Unregister the server (Clean up).
     */
    inline HRESULT UnregisterServer(const GUID& clsid, const wchar_t* progID) {
        LPOLESTR pszCLSID;
        StringFromCLSID(clsid, &pszCLSID);
        std::wstring clsidStr(pszCLSID);
        CoTaskMemFree(pszCLSID);

        std::wstring szCLSIDKey = L"CLSID\\" + clsidStr;

        // 1. Delete ProgID
        DeleteKeyUser(progID);

        // 2. Delete CLSID
        DeleteKeyUser(szCLSIDKey.c_str());

        return S_OK;
    }

} // namespace rtd

#endif // RTD_REGISTRY_H
