#pragma once
// Manual definitions of the Office extensibility interfaces so we do not
// depend on MIDL-generated headers from the Office SDK.
#include <windows.h>
#include <ole2.h>

// {B65AD801-ABAF-11D0-BB8B-00A0C90F2744}
static const IID IID_IDTExtensibility2 =
    { 0xB65AD801, 0xABAF, 0x11D0, { 0xBB, 0x8B, 0x00, 0xA0, 0xC9, 0x0F, 0x27, 0x44 } };

// {000C0396-0000-0000-C000-000000000046}
static const IID IID_IRibbonExtensibility =
    { 0x000C0396, 0x0000, 0x0000, { 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46 } };

struct IDTExtensibility2 : public IDispatch {
    virtual HRESULT STDMETHODCALLTYPE OnConnection(IDispatch* Application, int ConnectMode, IDispatch* AddInInst, SAFEARRAY** custom) = 0;
    virtual HRESULT STDMETHODCALLTYPE OnDisconnection(int RemoveMode, SAFEARRAY** custom) = 0;
    virtual HRESULT STDMETHODCALLTYPE OnAddInsUpdate(SAFEARRAY** custom) = 0;
    virtual HRESULT STDMETHODCALLTYPE OnStartupComplete(SAFEARRAY** custom) = 0;
    virtual HRESULT STDMETHODCALLTYPE OnBeginShutdown(SAFEARRAY** custom) = 0;
};

struct IRibbonExtensibility : public IDispatch {
    virtual HRESULT STDMETHODCALLTYPE GetCustomUI(BSTR RibbonID, BSTR* RibbonXml) = 0;
};
