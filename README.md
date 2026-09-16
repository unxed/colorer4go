# Colorer Go Library (WASM Edition)

This project is a high-performance pure Go port of the **Colorer** syntax highlighting engine. It compiles the C++ core engine to WebAssembly (WASI) and runs it via the lightweight, CGO-free, JIT-enabled **wazero** virtual machine.

---

## Go API Reference

The package `github.com/unxed/colorer4go` exports a clean, idiomatic Go API under the package namespace `colorer`. You do not need to deal with WebAssembly memory or C++ pointers; everything is handled internally.

### Types

#### `type Region struct`
Represents a matched syntax highlighting token/region.
```go
type Region struct {
	Start int    // Start offset of the token, in UTF-16 code units
	End   int    // End offset of the token, in UTF-16 code units
	Name  string // The classification name (e.g. "def:Comment", "def:SymbolStrong")
}
```

#### `type Session struct`
A stateful syntax highlighting session. Since Colorer maintains state (caches) across parsed lines to handle multi-line blocks correctly, a `Session` is stateful.

> **Concurrency Note:** `Session` is not thread-safe. If you need to parse text in multiple goroutines, instantiate a separate `Session` for each goroutine.

---

### Functions & Methods

#### `func NewSession`
Instantiates a new Colorer WASM environment and loads the catalog rules.
```go
func NewSession(ctx context.Context, catalogPath string, configDirMount string, opts ...Option) (*Session, error)
```
* `catalogPath`: Path to the `catalog.xml` file inside the WASM virtual filesystem (e.g., `"/base/catalog.xml"`).
* `configDirMount`: The path on the host machine containing the Colorer configurations (e.g., `"colorer/configs"`). It will be mounted as the root `/` inside the WASM sandbox.
* `opts`: optional; see [Diagnostics](#diagnostics).

#### `func WithUserHRD` and `func WithUserHRC`
The user's own colour styles and schemes, loaded on top of the catalog — what
FarColorer calls the user file of color styles and the user file of schemes.
```go
func WithUserHRD(path string) Option
func WithUserHRC(path string) Option
```
Both take a host path. `WithUserHRD` accepts an XML file in the catalog's
`<hrd-sets>` format, or a folder of `.hrd` files that name themselves in their
root element (`<hrd class="rgb" name="mine" description="My style">`).
`WithUserHRC` accepts an `.hrc` file, or a folder from which every `.hrc` file
except `*.ent.hrc` is loaded. They are loaded after the catalog, styles first,
in FarColorer's order; the styles are then listed by `EnumHRDInstances` and
accepted by `SetHRD` like the catalog's own.

The module sees only what is mounted into it, so each path's folder is mounted
read-only beside the configuration directory. Two consequences:

* A `<location link>` in an `<hrd-sets>` file is resolved by Colorer against
  `catalog.xml`, not against the file holding it; it has to stay inside the
  configuration directory. A folder of `.hrd` files has no such limit. Links
  inside `.hrc` files are resolved against the file itself and work as long as
  they stay inside the named folder.
* File names Colorer opens must be ASCII: its legacy strings read a name as
  CP1251, and a name that does not survive that does not open or aborts the
  call. Such a path is refused with a diagnostic instead. The folder's own name
  does not matter.

A path that does not exist is reported at `LevelWarn` and skipped. A file
Colorer cannot parse fails `NewSession` with an error that names the host path
and wraps the `*FatalError`.

#### `func (*Session) SelectType`
Selects the syntax highlighting scheme (HRC type) based on the file name and/or the first line of the file.
```go
func (s *Session) SelectType(fileName, firstLine string) (bool, error)
```
* `fileName`: The name of the file (e.g., `"test.json"`), used for extension matching.
* `firstLine`: The first line of the file, used for shebang or header matching.
* Returns `true` if a suitable scheme was found and loaded successfully.

#### `func (*Session) ParseLine`
Parses a single line of text and returns a list of highlighting regions.
```go
func (s *Session) ParseLine(line string) ([]Region, error)
```
* `line`: A single line of UTF-8 encoded text.
* Returns a slice of `Region` tokens.

#### `func (*Session) ForgetBefore`
Releases the stored text of every session line below `line`.
```go
func (s *Session) ForgetBefore(line int) error
```
The session numbers its lines from zero at the last `Reset`, and `ParseLine` appends at `NextLine`. Without this call every line ever parsed stays in WASM memory as UTF-16 until the session is reset, so walking a large file drags the whole file into the heap.

Only lines the parser will not ask for again may be dropped. It reads forward only, so everything below the next line to be parsed is safe; keeping a margin of a few lines below the region of interest is safer still. Dropping a line and then parsing it again traps the module: that surfaces as an error from `ParseLine` and leaves the session unusable.

The call is clamped and idempotent — lines already dropped and lines not yet parsed are ignored.

#### `func (*Session) FirstLine` and `func (*Session) NextLine`
The two bounds of the window of lines the session still holds.
```go
func (s *Session) FirstLine() (int, error)
func (s *Session) NextLine() (int, error)
```
`FirstLine` is the oldest line still stored — zero until `ForgetBefore` is called. `NextLine` is the number the next line passed to `ParseLine` will get, i.e. the count of lines fed since the last `Reset`.

#### `func (*Session) Reset`
Resets the session's internal line-state cache and clears stored lines. Call this when you want to start parsing a completely new file within the same session.
```go
func (s *Session) Reset()
```

#### `func (*Session) Close`
Closes the session, deallocates C++ memory, and shuts down the wazero runtime. Always use `defer session.Close()` after creation.
```go
func (s *Session) Close()
```

---

### Diagnostics

Colorer reports what it finds wrong with the configuration through its own
`Logger` interface: XML that does not parse (with file and line), a regular
expression in a scheme that does not compile, a region that is referenced but
never defined. Most of these are not fatal — Colorer skips the broken part and
highlighting is merely incomplete — which is exactly why they are easy to miss.
Without a handler they are discarded.

```go
func WithDiagnostics(level Level, handler func(Diagnostic)) Option
```

Delivers every message at `level` or more severe (`LevelError`, `LevelWarn`,
`LevelInfo`, `LevelDebug`, `LevelTrace` — Colorer's own levels, filtered inside
Colorer), starting with the ones produced while the catalog loads. The module's
stdout and stderr are delivered too, line by line. The handler runs on the
goroutine calling into the session, from inside that call, and must not call
back into the session. The stock catalog produces nothing at `LevelWarn`.

```go
type Diagnostic struct {
	Level    Level
	File     string // "colorer/parsers/HrcLibraryImpl.cpp", or "stdout"/"stderr"
	Line     int
	Function string
	Message  string
}
```

#### `type FatalError`

Colorer is compiled without C++ exceptions (the WASI build cannot unwind), so
wherever it throws one — a catalog that does not exist, an HRC file that is not
well-formed, an HRD scheme name it does not know — the call aborts. The call
then returns a `*FatalError`:

```go
type FatalError struct {
	Op     string // the wrapper function that failed, e.g. "colorer_set_hrd"
	Reason string // "C++ exception thrown at colorer/parsers/ParserFactoryImpl.cpp:271 in getHrdNode()"
	Err    error  // the runtime's error, with the wasm stack trace
}
```

The exception's own message cannot be recovered, only its throw site; the
diagnostics delivered just before it usually say the rest (for a broken HRC
file, libxml2's error with the file name and line).

The module's state after an aborted call is unknown, so the session refuses
every later call and returns the same error. `Session.Err()` reports it; the
only thing left to do with such a session is `Close`.

```go
session, err := colorer.NewSession(ctx, "/base/catalog.xml", dir,
	colorer.WithDiagnostics(colorer.LevelWarn, func(d colorer.Diagnostic) {
		log.Printf("colorer: %s", d)
	}))
```

---

### Complete Go Usage Example

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/unxed/colorer4go"
)

func main() {
	ctx := context.Background()

	// Instantiate the session (mounts host config directory to WASM root "/")
	session, err := colorer.NewSession(ctx, "/base/catalog.xml", "colorer/configs")
	if err != nil {
		log.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	// Detect and load the JSON highlighting scheme
	success, err := session.SelectType("test.json", "{")
	if err != nil || !success {
		log.Fatalf("Failed to select JSON scheme")
	}

	// Parse a line
	line := `{"key": "value"}`
	regions, err := session.ParseLine(line)
	if err != nil {
		log.Fatalf("Failed to parse: %v", err)
	}

	// Print matched tokens
	fmt.Printf("Parsed %d regions:\n", len(regions))
	for _, r := range regions {
		fmt.Printf("  [%d..%d]: %s\n", r.Start, r.End, r.Name)
	}
}
```

---

## Memory Model of a Session

A session accumulates two things as it parses, and they are released differently.

**The lines themselves** are held by `WasmLineSource` in the wrapper as a deque of UTF-16 strings, indexed by absolute line number through a `base` offset. `ForgetBefore` pops the front of that deque, which frees each line's buffer immediately. This is the part that scales with file size — a 40 MB source file is about 80 MB of WASM heap if nothing is ever dropped.

**The parse cache** inside libcolorer's `TextParser` holds one node per multi-line block that is still open, plus a copy of the line that opened it. It is bounded by the nesting structure of the file rather than by its length, and there is no way to trim it short of `Reset`, which drops the parse position with it. Making it trimmable, or snapshottable, would be a change to libcolorer itself and not to this wrapper.

So: `ForgetBefore` is what makes a long forward walk cost bounded memory; `Reset` remains the only way to release the parse cache, at the price of the context it holds.

---

## Build Requirements (For WASM Recompilation)

If you want to modify the C++ wrapper and rebuild `colorer.wasm` yourself:
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