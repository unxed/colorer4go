#ifndef COLORER_WRAPPER_H
#define COLORER_WRAPPER_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

__attribute__((used)) void* colorer_alloc(size_t size);
__attribute__((used)) void colorer_free(void* ptr);
__attribute__((used)) char* colorer_line_buffer(void* handle, int min_size);
__attribute__((used)) void* colorer_init(const char* catalog_path);
__attribute__((used)) void colorer_destroy(void* handle);
__attribute__((used)) void colorer_reset_session(void* handle);
__attribute__((used)) int colorer_set_hrd(void* handle, const char* hrd_class, const char* hrd_name);
__attribute__((used)) int colorer_enum_hrd_instances(void* handle, const char* class_id);
__attribute__((used)) const char* colorer_get_hrd_name(void* handle, int index);
__attribute__((used)) const char* colorer_get_hrd_description(void* handle, int index);
__attribute__((used)) int colorer_get_region_define(void* handle, const char* name, unsigned int* fore, unsigned int* back, unsigned int* style, int* isForeSet, int* isBackSet);
__attribute__((used)) int colorer_select_type(void* handle, const char* file_name, const char* first_line);
__attribute__((used)) int colorer_parse_line(void* handle, const char* line_utf8, int line_len);
__attribute__((used)) const void* colorer_get_regions(void* handle);
__attribute__((used)) void colorer_forget_before(void* handle, int lno);
__attribute__((used)) int colorer_first_line(void* handle);
__attribute__((used)) int colorer_next_line(void* handle);
__attribute__((used)) int colorer_get_region_start(void* handle, int index);
__attribute__((used)) int colorer_get_region_end(void* handle, int index);
__attribute__((used)) const char* colorer_get_region_name(void* handle, int index);
__attribute__((used)) unsigned int colorer_get_region_fore(void* handle, int index);
__attribute__((used)) unsigned int colorer_get_region_back(void* handle, int index);
__attribute__((used)) unsigned int colorer_get_region_style(void* handle, int index);
__attribute__((used)) int colorer_get_region_is_fore_set(void* handle, int index);
__attribute__((used)) int colorer_get_region_is_back_set(void* handle, int index);

#ifdef __cplusplus
}
#endif

#endif // COLORER_WRAPPER_H