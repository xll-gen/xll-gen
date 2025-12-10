#ifndef XLL_EMBED_H
#define XLL_EMBED_H

#include <string>

namespace embed {

// ExtractAndStartExe extracts the embedded executable (if needed) and starts it.
// It uses a checksum-based caching strategy:
// 1. Computes hash of embedded resource.
// 2. Checks for %TEMP%/<ProjectName>/embedded_server_<HASH>.exe
// 3. Extracts if missing (handling concurrency).
// Returns the full path to the executable to be used for connection.
std::string ExtractAndStartExe(const std::string& tempDirPattern, const std::string& projectName);

} // namespace embed

#endif // XLL_EMBED_H
