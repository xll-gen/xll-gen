cmake_minimum_required(VERSION 3.14)
project(go_embed_test LANGUAGES C CXX)

if(WIN32)
    enable_language(RC)
endif()

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

include(FetchContent)

# 1. Fetch Zstd
FetchContent_Declare(
  zstd
  GIT_REPOSITORY https://github.com/facebook/zstd.git
  GIT_TAG v1.5.7
)
set(ZSTD_BUILD_PROGRAMS OFF CACHE BOOL "" FORCE)
set(ZSTD_BUILD_TESTS OFF CACHE BOOL "" FORCE)
set(ZSTD_BUILD_SHARED OFF CACHE BOOL "" FORCE)
set(ZSTD_BUILD_STATIC ON CACHE BOOL "" FORCE)

# Manually populate and add subdirectory to ensure targets are defined correctly
FetchContent_GetProperties(zstd)
if(NOT zstd_POPULATED)
  FetchContent_Populate(zstd)
  add_subdirectory(${zstd_SOURCE_DIR}/build/cmake ${zstd_BINARY_DIR})
endif()

# 2. Build Compressor Tool (C++)
add_executable(compressor "${CMAKE_CURRENT_SOURCE_DIR}/compressor.cpp")
target_link_libraries(compressor PRIVATE libzstd_static)
target_include_directories(compressor PRIVATE ${zstd_SOURCE_DIR}/lib)

# 3. Build Go Binary
set(GO_SOURCE "${CMAKE_CURRENT_SOURCE_DIR}/main.go")
set(GO_EXE "${CMAKE_CURRENT_BINARY_DIR}/guest")
if(WIN32)
    set(GO_EXE "${GO_EXE}.exe")
endif()

add_custom_command(
    OUTPUT ${GO_EXE}
    COMMAND go build -o ${GO_EXE} ${GO_SOURCE}
    DEPENDS ${GO_SOURCE}
    COMMENT "Building Go binary..."
)

# 4. Compress Go Binary
set(ZSTD_BINARY "${CMAKE_CURRENT_BINARY_DIR}/guest.zst")

add_custom_command(
    OUTPUT ${ZSTD_BINARY}
    COMMAND compressor ${GO_EXE} ${ZSTD_BINARY}
    DEPENDS compressor ${GO_EXE}
    COMMENT "Compressing Go binary..."
)

set(HOST_SOURCES "${CMAKE_CURRENT_SOURCE_DIR}/main.cpp")

if(WIN32)
    # --- Windows Specific: Generate RC File ---
    set(RC_FILE "${CMAKE_CURRENT_BINARY_DIR}/resource.rc")

    file(WRITE ${RC_FILE} "101 RCDATA \"${ZSTD_BINARY}\"")
    set_source_files_properties(${RC_FILE} PROPERTIES OBJECT_DEPENDS ${ZSTD_BINARY})

    list(APPEND HOST_SOURCES ${RC_FILE})
else()
    # --- Linux Specific: Generate C Header ---
    set(HEADER_FILE "${CMAKE_CURRENT_BINARY_DIR}/embedded_data.h")

    add_custom_command(
        OUTPUT ${HEADER_FILE}
        COMMAND go run "${CMAKE_CURRENT_SOURCE_DIR}/bin2c.go" ${ZSTD_BINARY} ${HEADER_FILE}
        DEPENDS ${ZSTD_BINARY} "${CMAKE_CURRENT_SOURCE_DIR}/bin2c.go"
        COMMENT "Generating C header from compressed binary..."
    )
endif()

# 5. Build Host
add_executable(host_exe ${HOST_SOURCES})

if(NOT WIN32)
    add_custom_target(generate_header DEPENDS ${HEADER_FILE})
    add_dependencies(host_exe generate_header)
    target_include_directories(host_exe PRIVATE ${CMAKE_CURRENT_BINARY_DIR})
endif()

target_link_libraries(host_exe PRIVATE libzstd_static)
target_include_directories(host_exe PRIVATE ${zstd_SOURCE_DIR}/lib)

if(NOT WIN32)
    target_link_libraries(host_exe PRIVATE pthread)
endif()

set_target_properties(host_exe PROPERTIES OUTPUT_NAME "host")
