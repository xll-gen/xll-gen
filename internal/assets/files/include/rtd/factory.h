#ifndef RTD_FACTORY_H
#define RTD_FACTORY_H

#include <windows.h>
#include <ole2.h>
#include "module.h"

namespace rtd {

    template <typename ServerClass>
    class ClassFactory : public IClassFactory {
        long m_refCount;
    public:
        ClassFactory() : m_refCount(1) {
            GlobalModule::Lock();
        }

        virtual ~ClassFactory() {
            GlobalModule::Unlock();
        }

        HRESULT __stdcall QueryInterface(REFIID riid, void** ppv) override {
            if (!ppv) return E_POINTER;
            *ppv = nullptr;

            if (IsEqualGUID(riid, IID_IUnknown) || IsEqualGUID(riid, IID_IClassFactory)) {
                *ppv = static_cast<IClassFactory*>(this);
                AddRef();
                return S_OK;
            }
            return E_NOINTERFACE;
        }

        ULONG __stdcall AddRef() override {
            return InterlockedIncrement(&m_refCount);
        }

        ULONG __stdcall Release() override {
            ULONG res = InterlockedDecrement(&m_refCount);
            if (res == 0) delete this; return res;
        }

        HRESULT __stdcall CreateInstance(IUnknown* pUnkOuter, REFIID riid, void** ppv) override {
            try {
                if (pUnkOuter) return CLASS_E_NOAGGREGATION;
                if (!ppv) return E_POINTER;
                *ppv = nullptr;

                ServerClass* p = new (std::nothrow) ServerClass();
                if (!p) return E_OUTOFMEMORY;

                // p starts with RefCount=1
                HRESULT hr = p->QueryInterface(riid, ppv);
                // If QI succeeds, RefCount=2, *ppv set.
                // If QI fails, RefCount=1, *ppv=nullptr.
                p->Release();
                // If QI succeeded, RefCount=1 (held by client). Correct.
                // If QI failed, RefCount=0 (deleted). Correct.
                return hr;
            } catch (const std::bad_alloc&) {
                return E_OUTOFMEMORY;
            } catch (...) {
                return E_FAIL;
            }
        }

        HRESULT __stdcall LockServer(BOOL fLock) override {
            if (fLock) {
                GlobalModule::Lock();
            } else {
                GlobalModule::Unlock();
            }
            return S_OK;
        }
    };

} // namespace rtd

#endif // RTD_FACTORY_H
