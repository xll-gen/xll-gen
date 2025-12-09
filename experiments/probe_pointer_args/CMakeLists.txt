cmake_minimum_required(VERSION 3.14)
project(ProbePointerArgs LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

# Add the sources for the library
add_library(ProbePointerArgs SHARED
    probe_pointer_args.cpp
    XLCALL.CPP
)

# Optional: Define output name if needed, otherwise it will be ProbePointerArgs.dll
# set_target_properties(ProbePointerArgs PROPERTIES OUTPUT_NAME "MyProbe")

# Add this to ensure that __declspec(dllexport) works correctly
set_target_properties(ProbePointerArgs PROPERTIES
    WINDOWS_EXPORT_ALL_SYMBOLS ON
    PREFIX ""
    SUFFIX ".xll"
)
