// Standalone GDI+ ribbon image decoder. Deliberately free of SHM / logging /
// lifecycle dependencies so the regtest mock host can compile this TU
// directly. gdiplus is the only extra link dependency this TU introduces. The
// whole body is guarded: ribbon-less projects glob src/*.cpp but must not
// acquire a gdiplus link requirement.
#ifdef XLL_RIBBON_ENABLED

// The generated CMake defines NOGDI globally (shm/Logger.h vs wingdi ERROR
// macro). This TU needs real GDI (CreateDIBSection, gdiplus) and includes no
// shm headers, so locally undo it before any windows header is seen.
#ifdef NOGDI
#undef NOGDI
#endif

#include "com/ribbon_image.h"

// MinGW's gdiplus.h needs min/max visible inside namespace Gdiplus (windows.h
// may have NOMINMAX in effect anywhere in the ecosystem).
#include <algorithm>
namespace Gdiplus { using std::min; using std::max; }
#include <gdiplus.h>

#include <objbase.h> // CreateStreamOnHGlobal (ole32)
#include <olectl.h>  // OleCreatePictureIndirect, PICTDESC
#include <cstring>   // memcpy
#include <mutex>

namespace {
    std::vector<xll::ribbon::RibbonImage> g_images;
    std::mutex g_gdiplusMutex;
    ULONG_PTR g_gdiplusToken = 0;
    bool g_gdiplusStarted = false;

    // Mutex-guarded (not once_flag) so the engine can restart after a
    // ShutdownRibbonImageEngine on the reload-without-unmap path.
    bool EnsureGdiplus() {
        std::lock_guard<std::mutex> lock(g_gdiplusMutex);
        if (g_gdiplusStarted) return true;
        Gdiplus::GdiplusStartupInput input;
        g_gdiplusStarted =
            (Gdiplus::GdiplusStartup(&g_gdiplusToken, &input, nullptr) == Gdiplus::Ok);
        return g_gdiplusStarted;
    }

    // Copies the decoded bitmap into a top-down 32bpp premultiplied-ARGB DIB
    // section; the ribbon renders per-pixel alpha only from this layout.
    HBITMAP ToPARGBBitmap(Gdiplus::Bitmap& bmp) {
        const UINT w = bmp.GetWidth(), h = bmp.GetHeight();
        if (w == 0 || h == 0) return nullptr;

        BITMAPINFO bi = {};
        bi.bmiHeader.biSize = sizeof(bi.bmiHeader);
        bi.bmiHeader.biWidth = static_cast<LONG>(w);
        bi.bmiHeader.biHeight = -static_cast<LONG>(h); // top-down, matches LockBits
        bi.bmiHeader.biPlanes = 1;
        bi.bmiHeader.biBitCount = 32;
        bi.bmiHeader.biCompression = BI_RGB;

        void* bits = nullptr;
        HBITMAP hbmp = CreateDIBSection(nullptr, &bi, DIB_RGB_COLORS, &bits, nullptr, 0);
        if (!hbmp || !bits) {
            if (hbmp) DeleteObject(hbmp);
            return nullptr;
        }

        Gdiplus::Rect rect(0, 0, static_cast<INT>(w), static_cast<INT>(h));
        Gdiplus::BitmapData data = {};
        data.Width = w;
        data.Height = h;
        data.Stride = static_cast<INT>(w * 4);
        data.PixelFormat = PixelFormat32bppPARGB;
        data.Scan0 = bits;
        if (bmp.LockBits(&rect,
                         Gdiplus::ImageLockModeRead | Gdiplus::ImageLockModeUserInputBuf,
                         PixelFormat32bppPARGB, &data) != Gdiplus::Ok) {
            DeleteObject(hbmp);
            return nullptr;
        }
        bmp.UnlockBits(&data);
        return hbmp;
    }
}

namespace xll { namespace ribbon {

    // Set once during xlAutoOpen (set-before-connect contract, see
    // com/ribbon_addin.h); read-only afterwards — no lock needed.
    void SetRibbonImages(std::vector<RibbonImage> images) { g_images = std::move(images); }

    IPictureDisp* LoadPictureFromBytes(const unsigned char* data, size_t size) {
        if (!data || size == 0 || !EnsureGdiplus()) return nullptr;

        HGLOBAL hmem = GlobalAlloc(GMEM_MOVEABLE, size);
        if (!hmem) return nullptr;
        if (void* dst = GlobalLock(hmem)) {
            memcpy(dst, data, size);
            GlobalUnlock(hmem);
        } else {
            GlobalFree(hmem);
            return nullptr;
        }
        IStream* stream = nullptr;
        // fDeleteOnRelease=TRUE: the stream owns hmem from here on.
        if (FAILED(CreateStreamOnHGlobal(hmem, TRUE, &stream)) || !stream) {
            GlobalFree(hmem); // ownership only transfers on success
            return nullptr;
        }

        IPictureDisp* picture = nullptr;
        {
            // Scoped: the Gdiplus::Bitmap must be destroyed while GDI+ is up.
            Gdiplus::Bitmap bmp(stream);
            if (bmp.GetLastStatus() == Gdiplus::Ok) {
                if (HBITMAP hbmp = ToPARGBBitmap(bmp)) {
                    PICTDESC pd = {};
                    pd.cbSizeofstruct = sizeof(pd);
                    pd.picType = PICTYPE_BITMAP;
                    pd.bmp.hbitmap = hbmp;
                    if (FAILED(OleCreatePictureIndirect(
                            &pd, IID_IPictureDisp, TRUE,
                            reinterpret_cast<void**>(&picture)))) {
                        DeleteObject(hbmp); // fOwn=TRUE transfers only on success
                        picture = nullptr;
                    }
                }
            }
        }
        stream->Release();
        return picture;
    }

    IPictureDisp* CreateRibbonPicture(const wchar_t* name) {
        if (!name) return nullptr;
        for (const auto& img : g_images) {
            if (_wcsicmp(img.name, name) == 0) {
                return LoadPictureFromBytes(img.data, img.size);
            }
        }
        return nullptr;
    }

    // Precondition: xlAutoClose runs on the same STA thread that services
    // loadImage Invokes, so no decode is in flight when this is called. The
    // lock guards only against a lazy EnsureGdiplus restart on the rare
    // reload-without-unmap path.
    void ShutdownRibbonImageEngine() {
        std::lock_guard<std::mutex> lock(g_gdiplusMutex);
        if (g_gdiplusStarted) {
            Gdiplus::GdiplusShutdown(g_gdiplusToken);
            g_gdiplusStarted = false;
            g_gdiplusToken = 0;
        }
    }

}} // namespace xll::ribbon
#endif // XLL_RIBBON_ENABLED
