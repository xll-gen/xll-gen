cmake_minimum_required(VERSION 3.14)
project(mock_host LANGUAGES CXX)
set(CMAKE_CXX_STANDARD 17)

include(FetchContent)

# Optimize FlatBuffers build
set(FLATBUFFERS_BUILD_TESTS OFF CACHE BOOL "" FORCE)
set(FLATBUFFERS_INSTALL OFF CACHE BOOL "" FORCE)
set(FLATBUFFERS_BUILD_FLATC OFF CACHE BOOL "" FORCE)
set(FLATBUFFERS_BUILD_FLATHASH OFF CACHE BOOL "" FORCE)
set(FLATBUFFERS_BUILD_GRPCTEST OFF CACHE BOOL "" FORCE)
set(FLATBUFFERS_BUILD_SHAREDLIB OFF CACHE BOOL "" FORCE)

FetchContent_Declare(
  flatbuffers
  GIT_REPOSITORY https://github.com/google/flatbuffers.git
  GIT_TAG v25.9.23
  GIT_SHALLOW TRUE
)
FetchContent_MakeAvailable(flatbuffers)

# Optimize SHM build
set(SHM_BUILD_TESTS OFF CACHE BOOL "" FORCE)
set(SHM_BUILD_BENCHMARKS OFF CACHE BOOL "" FORCE)

FetchContent_Declare(
  shm
  GIT_REPOSITORY https://github.com/xll-gen/shm.git
  GIT_TAG v0.5.4
  GIT_SHALLOW TRUE
)
FetchContent_MakeAvailable(shm)

if(NOT TARGET shm)
  add_library(shm INTERFACE)
  target_include_directories(shm INTERFACE ${shm_SOURCE_DIR}/include)
endif()

add_executable(mock_host main.cpp)
target_include_directories(mock_host PRIVATE ../generated/cpp)
target_link_libraries(mock_host PRIVATE shm flatbuffers)
