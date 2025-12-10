#ifndef XLL_EMBED_H
#define XLL_EMBED_H

#include <string>

namespace embed {

// ExtractAndStartExe extracts the embedded executable (if needed) and starts it.
// Returns the full path to the executable to be used for connection.
// If extraction fails, it returns an empty string.
std::string ExtractAndStartExe(const std::string& tempDirPattern);

} // namespace embed

#endif // XLL_EMBED_H
