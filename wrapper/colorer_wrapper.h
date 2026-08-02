#ifndef COLORER_WRAPPER_H
#define COLORER_WRAPPER_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

// Exported C API functions
__attribute__((used)) void* colorer_alloc(size_t size);
__attribute__((used)) void colorer_free(void* ptr);
__attribute__((used)) void* colorer_init(const char* catalog_path);
__attribute__((used)) void colorer_destroy(void* handle);
__attribute__((used)) void colorer_reset_session(void* handle);
__attribute__((used)) int colorer_select_type(void* handle, const char* file_name, const char* first_line);
__attribute__((used)) int colorer_parse_line(void* handle, const char* line_utf8, int line_len);
__attribute__((used)) int colorer_get_region_start(void* handle, int index);
__attribute__((used)) int colorer_get_region_end(void* handle, int index);
__attribute__((used)) const char* colorer_get_region_name(void* handle, int index);

#ifdef __cplusplus
}
#endif

#endif // COLORER_WRAPPER_H