# Colorer Go Library (WASM Edition)

This project is a high-performance pure Go port of the **Colorer** syntax highlighting engine. It compiles the C++ core engine to WebAssembly (WASI) and runs it via the lightweight, CGO-free, JIT-enabled **wazero** virtual machine.

## Build Requirements

If you want to modify the C++ wrapper and rebuild `colorer.wasm`:
1. **WASI SDK** (v23+): https://github.com/WebAssembly/wasi-sdk
2. **Binaryen** (v115+ for `wasm-opt` optimization): https://github.com/WebAssembly/binaryen

To compile, run the build script:
```bash
BINARYEN_PATH=/opt/binaryen WASI_SDK_PATH=/opt/wasi-sdk ./build_wasm.sh
```

---

## Architecture and Technical Decisions

Porting a complex C++ engine (originally designed with heavy object-oriented exception handling) to the constraints of WebAssembly required several non-trivial architectural adjustments.

### 1. WASI Reactor Model instead of Command Model
By default, `wasi-sdk` compiles targets as standard executables (Command), which require a `main()` entrypoint and execute via `_start` upon instantiation.

* **What did NOT work:** Compiling the wrapper as a Command and suppressing the entrypoint (`-Wl,--no-entry`) compiled successfully but crashed instantly on instantiation with `wasm error: unreachable`. This was because the WASI loader still invoked `_start`, which trapped with `unreachable` due to the missing `main` function.
* **The Solution:** The target compilation model was changed to a shared library (Reactor) using the linker flag `-mexec-model=reactor`. In this model, `wasi-sdk` generates an `_initialize` function instead of `_start`.
Inside our Go driver (`colorer.go`), we automatically call `_initialize` immediately after instantiating the module. This invokes the C++ static constructors (static initializers), safely setting up the C++ standard library globals (such as `std::cerr` streams and internal RTTI tables) without crashes.

### 2. Complete Removal of C++ Exceptions (`-fno-exceptions`)
Colorer relies on exception throwing (`throw Exception(...)`) for runtime errors (e.g., "schema file not found").

* **What did NOT work:**
  * *Using native WASM exceptions (`-fwasm-exceptions`):* The binary compiled, but the moment any error occurred, the `libc++abi` unwinder crashed inside `__dynamic_cast` or `__cxa_end_catch` with `wasm error: out of bounds memory access` (or `invalid table access`). This was caused by static linking conflicts of libc++abi within a WASI Reactor module where function pointer tables got misaligned.
  * *Redefining `throw` via header macros:* redifining `throw(...)` failed on `throw Exception(...)` because of spaces. Redefining empty `-Dthrow` caused syntax errors due to compiler-level `throw` restrictions and Most Vexing Parse (`Type(variable);` became a declaration without a default constructor).
* **The Solution:** Exceptions and RTTI are completely disabled at the compiler level (`-fno-exceptions -frtti`).
To bypass the compiler syntax errors for the `throw` keyword, a **C++ comma operator macro** is defined in `Common.h` when exceptions are disabled:
```cpp
#define throw ::abort(),
```
This converts any statement like `throw Exception("error");` into `::abort(), Exception("error");` which is 100% syntactically valid C++ (evaluates as a void comma expression) and safely terminates the WASM sandbox via `abort()`. The only empty rethrow statements (`throw;` in `HrcLibraryImpl.cpp` and `TextLinesStore.cpp`) were manually patched to a safe no-op `((void)0);` since catch blocks are dead code under exceptions-disabled mode anyway.

### 3. Adapting `ghc::filesystem` for WASI
Colorer uses the header-only `ghc::filesystem` library for file path operations. By default, it lacked support for the `__wasi__` target, resulting in `#error "Operating system currently not supported!"`.

* **The Solution:** Added `|| defined(__wasi__)` in `filesystem.hpp` to map the WASI target to `GHC_OS_WEB`, which fully restored POSIX filesystem compilation.

---

## Safety and Robustness

**Does using `abort()` affect the stability of the Go application?**
* **The Go process is safe:** Wazero isolates WASM execution. If a fatal C++ abort occurs (such as a bad XML schema structure), Wazero safely traps the execution and returns a standard Go `error` (`wasm error: unreachable`). The host Go process remains stable.
* **Pre-flight checks:** To prevent WASM traps on invalid paths during production, the Go wrapper should perform pre-flight checks (verifying file existence on the host filesystem) before passing paths to `NewSession`.