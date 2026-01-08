#ifndef RTD_ENTRY_H
#define RTD_ENTRY_H

#include <windows.h>
#include <new>
#include "registry.h"
#include "factory.h"
#include "module.h"

// Helper Macro to define standard DLL Exports
// Usage: RTD_DEFINE_DLL_ENTRY(MyServerClass, CLSID_MyServer, L"My.ProgID", L"My Friendly Name")

#define RTD_DEFINE_DLL_ENTRY(ServerClass, Clsid, ProgID, FriendlyName) \
    HINSTANCE g_hModule = NULL; \
    \
    extern "C" { \
        BOOL APIENTRY DllMain(HMODULE hModule, DWORD ul_reason, LPVOID) { \
            if (ul_reason == DLL_PROCESS_ATTACH) g_hModule = hModule; \
            return TRUE; \
        } \
        \
        __declspec(dllexport) HRESULT __stdcall DllGetClassObject(REFCLSID rclsid, REFIID riid, LPVOID* ppv) { \
            try { \
                if (!ppv) return E_POINTER; \
                *ppv = nullptr; \
                if (IsEqualGUID(rclsid, Clsid)) { \
                    rtd::ClassFactory<ServerClass>* pFactory = new rtd::ClassFactory<ServerClass>(); \
                    HRESULT hr = pFactory->QueryInterface(riid, ppv); \
                    pFactory->Release(); \
                    return hr; \
                } \
                return CLASS_E_CLASSNOTAVAILABLE; \
            } catch (const std::bad_alloc&) { \
                return E_OUTOFMEMORY; \
            } catch (...) { \
                return E_FAIL; \
            } \
        } \
        \
        __declspec(dllexport) HRESULT __stdcall DllCanUnloadNow() { \
            return (rtd::GlobalModule::GetLockCount() == 0) ? S_OK : S_FALSE; \
        } \
        \
        __declspec(dllexport) HRESULT __stdcall DllRegisterServer() { \
            try { \
                return rtd::RegisterServer(g_hModule, Clsid, ProgID, FriendlyName); \
            } catch (const std::bad_alloc&) { \
                return E_OUTOFMEMORY; \
            } catch (...) { \
                return E_FAIL; \
            } \
        } \
        \
        __declspec(dllexport) HRESULT __stdcall DllUnregisterServer() { \
            try { \
                return rtd::UnregisterServer(Clsid, ProgID); \
            } catch (const std::bad_alloc&) { \
                return E_OUTOFMEMORY; \
            } catch (...) { \
                return E_FAIL; \
            } \
        } \
    }

#endif // RTD_ENTRY_H
