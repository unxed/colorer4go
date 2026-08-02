cmake_minimum_required(VERSION 3.14)
project(ColorerWasm C CXX)

# --- 1. Настройка зависимости: LibXml2 ---
# Скачиваем и собираем libxml2 как статическую библиотеку для WASM
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

# Colorer ожидает цель LibXml2::LibXml2
if(NOT TARGET LibXml2::LibXml2)
    add_library(LibXml2::LibXml2 ALIAS LibXml2)
endif()

# --- 2. Настройка Colorer ---
# Отключаем ненужные для WASM зависимости и фичи
set(COLORER_USE_ICU_STRINGS OFF CACHE BOOL "Use legacy strings instead of ICU" FORCE)
set(COLORER_USE_ZIPINPUTSOURCE OFF CACHE BOOL "Disable Zip support" FORCE)
set(COLORER_BUILD_HARD_WARNINGS OFF CACHE BOOL "Disable hard warnings" FORCE)

# Утилиты Colorer ожидают эту переменную
set(LIBCOLORER_TREE "${CMAKE_CURRENT_SOURCE_DIR}/colorer/src/Colorer-library/src")
include(${CMAKE_CURRENT_SOURCE_DIR}/colorer/src/Colorer-library/cmake/utils.cmake)
colorer_extract_version()

# Подключаем библиотеку Colorer (исходники не трогаем)
add_subdirectory(${LIBCOLORER_TREE} colorer_lib_build)

# --- 3. Сборка нашей C-обертки в WASM ---
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

# Указываем флаги для WASM (экспортируем нужные функции)
set_target_properties(colorer_wasm PROPERTIES
    OUTPUT_NAME "colorer"
    SUFFIX ".wasm"
    LINK_FLAGS "-Wl,--export-dynamic -Wl,--no-entry -Wl,--allow-undefined"
)