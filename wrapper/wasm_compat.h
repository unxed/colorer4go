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

// Defined in colorer_wrapper.cpp. Reports the throw site to the host through
// the colorer4go.fatal import, then aborts. Exceptions are compiled out, so a
// throw can only end the call; what the host can still be told is where it
// happened.
extern "C" [[noreturn]] void colorer4go_throw_abort(const char* file, int line, const char* func) noexcept;

// Handles `throw Expr;`, `throw;`, avoids Most Vexing Parse and dangling-else warnings.
// The thrown expression itself is evaluated and discarded: capturing it would
// need an operand, and the bare `throw;` in Colorer's catch blocks has none.
#define throw for(int _dummy_throw=0; _dummy_throw<1; _dummy_throw++, ::colorer4go_throw_abort(__FILE__, __LINE__, __func__))
#define try if(true)
#define catch(...) for (std::exception e; false; )

#endif
#endif

#endif // WASM_COMPAT_H
