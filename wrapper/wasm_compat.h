#ifndef WASM_COMPAT_H
#define WASM_COMPAT_H

#ifdef __cplusplus
#if !defined(__EXCEPTIONS) && !defined(__cpp_exceptions) && !defined(_CPPUNWIND)
#include <cstdlib>
#include <exception>
#include <stdexcept>

#ifdef __clang__
#pragma clang diagnostic ignored "-Wunused-variable"
#pragma clang diagnostic ignored "-Wkeyword-macro"
#endif

// Include headers for exceptions we need to wrap
#include "colorer/strings/legacy/StringExceptions.h"

// Provide all-accepting constructors to avoid Most Vexing Parse errors when thrown
class DummyUnsupportedEncodingException : public UnsupportedEncodingException {
public:
    DummyUnsupportedEncodingException() : UnsupportedEncodingException(UnicodeString("")) {}
    template<typename... Args> DummyUnsupportedEncodingException(Args&&...) : UnsupportedEncodingException(UnicodeString("")) {}
};
#define UnsupportedEncodingException DummyUnsupportedEncodingException

class DummyStringIndexOutOfBoundsException : public StringIndexOutOfBoundsException {
public:
    DummyStringIndexOutOfBoundsException() : StringIndexOutOfBoundsException(UnicodeString("")) {}
    template<typename... Args> DummyStringIndexOutOfBoundsException(Args&&...) : StringIndexOutOfBoundsException(UnicodeString("")) {}
};
#define StringIndexOutOfBoundsException DummyStringIndexOutOfBoundsException

// Handles `throw Expr;`, `throw;`, avoids Most Vexing Parse and dangling-else warnings
#define throw for(int _dummy_throw=0; _dummy_throw<1; _dummy_throw++, ::abort())
#define try if(true)
#define catch(...) for (std::exception e; false; )

#endif
#endif

#endif // WASM_COMPAT_H
