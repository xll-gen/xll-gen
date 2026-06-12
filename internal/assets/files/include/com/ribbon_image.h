#pragma once
#include <windows.h>
#include <ocidl.h> // IPictureDisp
#include <vector>

namespace xll { namespace ribbon {

    // One embedded ribbon image. The byte arrays live in the generated
    // ribbon_images.h (process lifetime), so plain pointers are safe.
    struct RibbonImage {
        const wchar_t* name;
        const unsigned char* data;
        size_t size;
    };

    // Set once from xlAutoOpen before the COM add-in connects (same
    // set-before-connect contract as SetRibbonXml); read-only afterwards.
    void SetRibbonImages(std::vector<RibbonImage> images);

    // Decodes PNG/JPG/BMP/GIF/ICO bytes via GDI+ into a 32bpp premultiplied-
    // ARGB IPictureDisp (PNG alpha preserved). Returns nullptr on failure.
    // Lazily starts GDI+ on first use — call only from a normal thread
    // (Excel's STA / a test main), never from DllMain. Caller owns the
    // returned reference.
    IPictureDisp* LoadPictureFromBytes(const unsigned char* data, size_t size);

    // Looks up an embedded image by its customUI name (image="...") and
    // decodes it. nullptr if the name is unknown or decoding fails.
    IPictureDisp* CreateRibbonPicture(const wchar_t* name);

    // GdiplusShutdown iff a decode started it. Generated xlAutoClose calls
    // this after the ribbon add-in disconnects (no further loadImage can
    // arrive; xlAutoClose runs on the same STA thread as Invoke). Already-
    // created pictures stay valid: they own plain GDI HBITMAPs, independent
    // of GDI+ once created.
    void ShutdownRibbonImageEngine();

}} // namespace xll::ribbon
