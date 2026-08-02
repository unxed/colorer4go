#ifndef COLORER_WRAPPER_H
#define COLORER_WRAPPER_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

// Экспортируемые функции (C API)
__attribute__((used)) void* colorer_factory_create();
__attribute__((used)) void colorer_factory_destroy(void* pf);

#ifdef __cplusplus
}
#endif

#endif // COLORER_WRAPPER_H