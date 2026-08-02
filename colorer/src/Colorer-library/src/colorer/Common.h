#ifndef COLORER_COMMON_H
#define COLORER_COMMON_H

#include "colorer/common/Features.h"

#ifdef COLORER_FEATURE_ICU
#include "colorer/strings/icu/strings.h"
#else
#include "colorer/strings/legacy/strings.h"
#endif

#if __cplusplus >= 201703L
# define UNICODE_LITERAL(name, value) inline const auto name = UnicodeString(value);
#else
# define UNICODE_LITERAL(name, value) static const auto name = UnicodeString(value);
#endif

/*
 Logging
*/
#include "colorer/common/Logger.h"

#if !defined(__EXCEPTIONS) && !defined(__cpp_exceptions) && !defined(_CPPUNWIND)
#include <cstdlib>
#include <exception>
#define throw ::abort(),
#define try if(true)
#define catch(...) for (std::exception e; false; )
#endif

#endif  // COLORER_COMMON_H
