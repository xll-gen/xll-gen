#ifndef RTD_DEFS_H
#define RTD_DEFS_H

#include <windows.h>
#include <ole2.h>
#include <olectl.h>
#include <string>

namespace rtd {

    // IRTDUpdateEvent Interface
    struct IRTDUpdateEvent : public IDispatch {
        virtual HRESULT __stdcall UpdateNotify() = 0;
        virtual HRESULT __stdcall get_HeartbeatInterval(long* value) = 0;
        virtual HRESULT __stdcall put_HeartbeatInterval(long value) = 0;
        virtual HRESULT __stdcall Disconnect() = 0;
    };

    // IRtdServer Interface
    struct IRtdServer : public IDispatch {
        virtual HRESULT __stdcall ServerStart(IRTDUpdateEvent* Callback, long* pfRes) = 0;
        virtual HRESULT __stdcall ConnectData(long TopicID, SAFEARRAY** Strings, VARIANT_BOOL* GetNewValues, VARIANT* pvarOut) = 0;
        virtual HRESULT __stdcall RefreshData(long* TopicCount, SAFEARRAY** parrayOut) = 0;
        virtual HRESULT __stdcall DisconnectData(long TopicID) = 0;
        virtual HRESULT __stdcall Heartbeat(long* pfRes) = 0;
        virtual HRESULT __stdcall ServerTerminate() = 0;
    };

    // Standard IRtdServer IID: {EC0E6191-DB51-11D3-8F3E-00C04F3651B8}
    static const GUID IID_IRtdServer =
        { 0xEC0E6191, 0xDB51, 0x11D3, { 0x8F, 0x3E, 0x00, 0xC0, 0x4F, 0x36, 0x51, 0xB8 } };

    // Standard IRTDUpdateEvent IID: {A43788C1-D91B-11D3-8F39-00C04F3651B8}
    static const GUID IID_IRTDUpdateEvent =
        { 0xA43788C1, 0xD91B, 0x11D3, { 0x8F, 0x39, 0x00, 0xC0, 0x4F, 0x36, 0x51, 0xB8 } };

} // namespace rtd

#endif // RTD_DEFS_H
