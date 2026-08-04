cmake_minimum_required(VERSION 3.14)
project(ColorerWasm C CXX)

# Globally disable C++ exceptions and preserve RTTI for dynamic_cast support in the core
add_compile_options(-fno-exceptions -frtti)

# --- 1. Configure Dependency: LibXml2 ---
# Download and build libxml2 as a static library for WASM
include(FetchContent)
FetchContent_Declare(
    libxml2
    URL https://download.gnome.org/sources/libxml2/2.11/libxml2-2.11.4.tar.xz
)

set(LIBXML2_WITH_PYTHON OFF CACHE BOOL "Disable Python" FORCE)
set(LIBXML2_WITH_ZLIB OFF CACHE BOOL "Disable Zlib" FORCE)
set(LIBXML2_WITH_LZMA OFF CACHE BOOL "Disable LZMA" FORCE)
set(LIBXML2_WITH_ICONV OFF CACHE BOOL "Disable Iconv" FORCE)
set(LIBXML2_WITH_HTTP OFF CACHE BOOL "Disable HTTP client" FORCE)
set(LIBXML2_WITH_FTP OFF CACHE BOOL "Disable FTP client" FORCE)
set(LIBXML2_WITH_TESTS OFF CACHE BOOL "Disable tests" FORCE)
set(LIBXML2_WITH_PROGRAMS OFF CACHE BOOL "Disable programs" FORCE)
set(BUILD_SHARED_LIBS OFF CACHE BOOL "Build static" FORCE)

FetchContent_MakeAvailable(libxml2)

# Colorer expects the LibXml2::LibXml2 target
if(NOT TARGET LibXml2::LibXml2)
    add_library(LibXml2::LibXml2 ALIAS LibXml2)
endif()

# --- 2. Configure Colorer ---
# Disable unused dependencies and features for WASM
set(COLORER_USE_ICU_STRINGS OFF CACHE BOOL "Use legacy strings instead of ICU" FORCE)
set(COLORER_USE_ZIPINPUTSOURCE OFF CACHE BOOL "Disable Zip support" FORCE)
set(COLORER_BUILD_HARD_WARNINGS OFF CACHE BOOL "Disable hard warnings" FORCE)

# Colorer utilities expect this variable
set(LIBCOLORER_TREE "${CMAKE_CURRENT_SOURCE_DIR}/colorer/src/Colorer-library/src")
include(${CMAKE_CURRENT_SOURCE_DIR}/colorer/src/Colorer-library/cmake/utils.cmake)
colorer_extract_version()

# Include the Colorer library (original sources are kept untouched)
add_subdirectory(${LIBCOLORER_TREE} colorer_lib_build)

# --- 3. Build our C-wrapper in WASM ---
# Build as an executable since WASM Reactor on WASI compiles as a static executable
add_executable(colorer_wasm
    wrapper/colorer_wrapper.cpp
)

target_include_directories(colorer_wasm PRIVATE
    wrapper
    ${LIBCOLORER_TREE}
)

target_link_libraries(colorer_wasm PRIVATE
    colorer::colorer
)

# Specify WASM flags (build in Reactor model and export required functions)
set_target_properties(colorer_wasm PROPERTIES
    OUTPUT_NAME "colorer"
    SUFFIX ".wasm"
    LINK_FLAGS "-mexec-model=reactor -Wl,--export-dynamic -Wl,--export=colorer_alloc -Wl,--export=colorer_free -Wl,--export=colorer_init -Wl,--export=colorer_destroy -Wl,--export=colorer_reset_session -Wl,--export=colorer_set_hrd -Wl,--export=colorer_enum_hrd_instances -Wl,--export=colorer_get_hrd_name -Wl,--export=colorer_get_hrd_description -Wl,--export=colorer_get_region_define -Wl,--export=colorer_select_type -Wl,--export=colorer_parse_line -Wl,--export=colorer_get_region_start -Wl,--export=colorer_get_region_end -Wl,--export=colorer_get_region_name -Wl,--export=colorer_get_region_fore -Wl,--export=colorer_get_region_back -Wl,--export=colorer_get_region_style -Wl,--export=colorer_get_region_is_fore_set -Wl,--export=colorer_get_region_is_back_set"
)