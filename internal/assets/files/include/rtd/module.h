#ifndef RTD_MODULE_H
#define RTD_MODULE_H

#include <windows.h>

namespace rtd {

    /**
     * @brief Holds global state for the DLL (Reference Counts).
     * Implemented as a template to ensure a single instance across translation units
     * in a header-only library.
     */
    template <typename T>
    struct ModuleState {
        // Counts active components (Servers + Factories) and explicit Locks
        static long LockCount;

        static void Lock() {
            InterlockedIncrement(&LockCount);
        }

        static void Unlock() {
            InterlockedDecrement(&LockCount);
        }

        static long GetLockCount() {
            return LockCount;
        }
    };

    // Definition of static member
    template <typename T>
    long ModuleState<T>::LockCount = 0;

    // Type alias for easy usage
    typedef ModuleState<void> GlobalModule;

} // namespace rtd

#endif // RTD_MODULE_H
