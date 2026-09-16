package colorer

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed build_wasm/colorer.wasm
var colorerWasm []byte

var (
	cacheOnce        sync.Once
	compilationCache wazero.CompilationCache
)

// sharedCompilationCache returns a process-wide cache of compiled WASM code.
// Compiling the embedded module is by far the most expensive part of creating
// a session, so the machine code is stored on disk and reused both by later
// sessions and by later runs of the program.
func sharedCompilationCache() wazero.CompilationCache {
	cacheOnce.Do(func() {
		if dir, err := os.UserCacheDir(); err == nil {
			if cache, cErr := wazero.NewCompilationCacheWithDir(filepath.Join(dir, "colorer4go")); cErr == nil {
				compilationCache = cache
				return
			}
		}
		compilationCache = wazero.NewCompilationCache()
	})
	return compilationCache
}

type Region struct {
	Start     int
	End       int
	Name      string
	Fore      uint32
	Back      uint32
	Style     uint32
	IsForeSet bool
	IsBackSet bool
}

// Pair is one paired token of a line: a region under def:PairStart (Opens) or
// def:PairEnd. Colorer makes these regions special, so ParseLine does not
// return them — FarColorer draws a pair only when the cursor is on it. Start
// and End are offsets like Region's; the colours are the pair's own assign.
//
// Matching is left to the caller, as BaseEditor::searchPair does it: from the
// pair under the cursor walk the following pairs (or, for a pair end, the
// preceding ones) across lines, counting +1 for each start and -1 for each end
// from 1 (or -1), and the pair where the count reaches zero is the match.
type Pair struct {
	Start     int
	End       int
	Opens     bool
	Fore      uint32
	Back      uint32
	Style     uint32
	IsForeSet bool
	IsBackSet bool
}

type RegionDefine struct {
	Fore      uint32
	Back      uint32
	Style     uint32
	IsForeSet bool
	IsBackSet bool
}

type HRDInstance struct {
	Name        string
	Description string
}

type Session struct {
	ctx context.Context
	r   wazero.Runtime
	mod api.Module
	ptr uint32 // Pointer to the ColorerSession struct in C++

	// Cached once per session instead of re-resolved by name on every
	// ParseLine call. ExportedFunction is a lookup by string key; parsing a
	// file line by line turns a per-call lookup into millions of repeated
	// ones for names that never change for the life of the session. The
	// other exported functions (SetHrd, SelectType, and so on) are called
	// rarely enough — once per file open or theme switch — that caching
	// them would add fields without a difference anyone would feel.
	lineBufferFn api.Function
	parseLineFn  api.Function
	getRegionsFn api.Function

	// nameCache maps a region name's wasm pointer to the Go string already
	// read from it. The wrapper's own name_cache (colorer_wrapper.cpp) keeps
	// that pointer stable for the life of the session — schemas load once,
	// and colorer_reset_session does not touch it — so a name is read out of
	// linear memory at most once per session no matter how many lines carry
	// it.
	nameCache map[uint32]string

	host  *hostState
	fatal *FatalError
}

// Level is the severity of a Diagnostic. The values are Colorer's own
// Logger::LogLevel, so the level given to WithDiagnostics is applied inside
// Colorer, before a filtered message is even formatted.
type Level int

const (
	LevelError Level = 1 + iota
	LevelWarn
	LevelInfo
	LevelDebug
	LevelTrace
)

func (l Level) String() string {
	switch l {
	case LevelError:
		return "error"
	case LevelWarn:
		return "warning"
	case LevelInfo:
		return "info"
	case LevelDebug:
		return "debug"
	case LevelTrace:
		return "trace"
	}
	return "level(" + strconv.Itoa(int(l)) + ")"
}

// Diagnostic is one message Colorer reported through its Logger interface —
// a malformed XML file, a regular expression in a scheme that does not
// compile, a region referenced but never defined — or one line the module
// wrote to its stdout or stderr.
//
// Most of what Colorer reports this way is not fatal: it skips the broken part
// and keeps going, and highlighting is merely incomplete. Without a handler
// those messages are discarded.
type Diagnostic struct {
	Level Level
	// File and Line locate the Colorer source line that reported the message,
	// relative to the library tree ("colorer/parsers/HrcLibraryImpl.cpp").
	// For a line of module output File is "stdout" or "stderr" and Line is 0.
	File     string
	Line     int
	Function string
	Message  string
}

func (d Diagnostic) String() string {
	if d.Line == 0 {
		return fmt.Sprintf("[%s] %s: %s", d.Level, d.File, d.Message)
	}
	return fmt.Sprintf("[%s] %s:%d %s(): %s", d.Level, d.File, d.Line, d.Function, d.Message)
}

// Option configures NewSession.
type Option func(*sessionOptions)

type sessionOptions struct {
	level   Level
	handler func(Diagnostic)

	userHRD string
	userHRC string
}

// WithDiagnostics delivers every message Colorer reports at level or more
// severe to handler, starting with the ones produced while the catalog loads.
// The module's stdout and stderr are delivered too, line by line, as LevelInfo
// and LevelError; without a handler they go to the process's own stdout and
// stderr, which is where they always went.
//
// handler runs synchronously on the goroutine that is calling into the
// session, from inside that call. It must not call back into the session.
func WithDiagnostics(level Level, handler func(Diagnostic)) Option {
	return func(o *sessionOptions) {
		o.level = level
		o.handler = handler
	}
}

// WithUserHRD loads the user's own colour styles on top of the catalog, the
// way FarColorer loads its "user file of color styles". path is a host path to
// either an XML file in the catalog's <hrd-sets> format, or a folder of .hrd
// files, each of which names itself in its root element:
//
//	<hrd xmlns="http://colorer.sf.net/2003/hrd" class="rgb" name="mine" description="My style">
//
// The styles are then listed by EnumHRDInstances and accepted by SetHRD like
// the catalog's own.
//
// Colorer resolves a <location link> in an <hrd-sets> file against
// catalog.xml, not against the file that contains it, and the module sees
// only what is mounted into it: the configuration directory and the folder of
// path. A link must therefore be relative to catalog.xml and stay inside the
// configuration directory; a folder of .hrd files has no such limit.
//
// Names Colorer opens must be ASCII: the library is built with its legacy
// strings, which read a file name as CP1251, and a name that does not survive
// that — a Cyrillic "И", any CJK character — does not open, or aborts the
// call. The folder itself, which is mounted, may be named anything. A file,
// or a folder holding a .hrd file, with a name that is not ASCII is reported
// and skipped rather than handed to Colorer.
//
// A path that does not exist is reported as a LevelWarn diagnostic and
// skipped, so a mistyped setting does not stop highlighting. A file Colorer
// cannot read fails NewSession with a *FatalError, wrapped in an error that
// names the host path.
func WithUserHRD(path string) Option {
	return func(o *sessionOptions) {
		o.userHRD = path
	}
}

// WithUserHRC loads the user's own schemes on top of the catalog, the way
// FarColorer loads its "user file of schemes". path is a host path to either
// an .hrc file or a folder, from which every .hrc file except *.ent.hrc is
// loaded. Links inside those files are resolved against the file itself, so
// they work as long as they stay inside the folder that was named.
//
// User schemes are loaded after user colour styles, in FarColorer's order.
// File names and error handling are those of WithUserHRD; a link inside a
// scheme is not checked in advance.
func WithUserHRC(path string) Option {
	return func(o *sessionOptions) {
		o.userHRC = path
	}
}

// Where the user's own files are mounted inside the module. The configuration
// directory is the root, so these names only have to be ones no catalog uses.
const (
	userHRDGuestDir = "/.colorer4go/user-hrd"
	userHRCGuestDir = "/.colorer4go/user-hrc"
)

// userLoad is one user path to hand to Colorer once the catalog is loaded.
type userLoad struct {
	op        string // the wrapper function that loads it
	what      string // for error messages
	hostPath  string
	guestPath string
	// loads tells which files of a folder Colorer opens, as
	// ParserFactory::Impl::loadHrdPath and loadHrcPath select them.
	loads func(name string) bool
}

func loadsHRD(name string) bool { return strings.HasSuffix(name, ".hrd") }

func loadsHRC(name string) bool {
	return strings.HasSuffix(name, ".hrc") && !strings.HasSuffix(name, ".ent.hrc")
}

// userMount works out how a host path is reached from inside the module. The
// module cannot mount a single file, so a file is reached through a read-only
// mount of the folder that holds it, and a folder through a mount of itself.
// It refuses a path whose files Colorer could not open by name; see
// WithUserHRD.
func userMount(hostPath, guestDir string, loads func(string) bool) (hostDir, guestPath string, err error) {
	info, err := os.Stat(hostPath)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		name := filepath.Base(hostPath)
		if !isASCII(name) {
			return "", "", fmt.Errorf("file name %q is not ASCII, which Colorer cannot open", name)
		}
		return filepath.Dir(hostPath), guestDir + "/" + name, nil
	}
	entries, err := os.ReadDir(hostPath)
	if err != nil {
		return "", "", err
	}
	for _, e := range entries {
		if loads(e.Name()) && !isASCII(e.Name()) {
			return "", "", fmt.Errorf("%q holds %q, a file name that is not ASCII, which Colorer cannot open", hostPath, e.Name())
		}
	}
	return hostPath, guestDir, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// FatalError reports a call into Colorer that did not return. The module was
// stopped in the middle of whatever it was doing, so its state is unknown: the
// session refuses every later call, returning this same error, and the only
// thing left to do with it is Close.
//
// Colorer is compiled without C++ exceptions. Where it throws one — a catalog
// that does not exist, an HRD scheme name it does not know — it cannot catch
// it, and the call aborts; Reason then says where the exception was thrown.
// Colorer's Logger usually explains the circumstances just before that, which
// is what WithDiagnostics is for.
type FatalError struct {
	// Op is the exported wrapper function that failed, e.g. "colorer_set_hrd".
	Op string
	// Reason is what the module reported before it stopped: the throw site of
	// the exception, or else the last line it wrote to stderr during the call.
	// Empty when it stopped without saying anything.
	Reason string
	// Err is the error the WebAssembly runtime returned for the call.
	Err error
}

func (e *FatalError) Error() string {
	var b strings.Builder
	b.WriteString("colorer: ")
	b.WriteString(e.Op)
	b.WriteString(" failed")
	if e.Reason != "" {
		b.WriteString(": ")
		b.WriteString(e.Reason)
	}
	if e.Err != nil {
		// The runtime appends a multi-line wasm stack trace; Unwrap keeps it.
		msg := e.Err.Error()
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		b.WriteString(" (")
		b.WriteString(msg)
		b.WriteString(")")
	}
	return b.String()
}

func (e *FatalError) Unwrap() error { return e.Err }

// hostState is what the functions of the "colorer4go" host module share with
// the session. It exists before the session does, because the catalog is
// loaded — and can already fail — inside NewSession.
type hostState struct {
	opts sessionOptions

	// Collected during one call into the module, reset before the next.
	fatalReason string
	lastStderr  string
}

func (h *hostState) deliver(d Diagnostic) {
	if h.opts.handler != nil && d.Level <= h.opts.level {
		h.opts.handler(d)
	}
}

func (h *hostState) beginCall() {
	h.fatalReason = ""
	h.lastStderr = ""
}

func (h *hostState) reason() string {
	if h.fatalReason != "" {
		return h.fatalReason
	}
	if h.lastStderr != "" {
		return "stderr: " + h.lastStderr
	}
	return ""
}

// callFn runs one exported function and turns a failed call into a
// FatalError. Every call into the module goes through here or through
// Session.call, so none of them can fail without saying why.
func callFn(ctx context.Context, h *hostState, fn api.Function, op string, params ...uint64) ([]uint64, error) {
	if fn == nil {
		return nil, fmt.Errorf("%s is not exported by the embedded colorer.wasm; rebuild it with ./build_wasm.sh", op)
	}
	h.beginCall()
	res, err := fn.Call(ctx, params...)
	if err != nil {
		return nil, &FatalError{Op: op, Reason: h.reason(), Err: err}
	}
	return res, nil
}

// call is callFn for a live session: it refuses to run once a call has
// failed, and remembers the first failure.
func (s *Session) call(fn api.Function, op string, params ...uint64) ([]uint64, error) {
	if s.fatal != nil {
		return nil, s.fatal
	}
	res, err := callFn(s.ctx, s.host, fn, op, params...)
	var fe *FatalError
	if errors.As(err, &fe) {
		s.fatal = fe
	}
	return res, err
}

// Err returns the FatalError that made the session unusable, or nil while it
// is still usable.
func (s *Session) Err() error {
	if s.fatal == nil {
		return nil
	}
	return s.fatal
}

// streamWriter splits one of the module's output streams into lines for the
// diagnostics handler, and remembers the last stderr line so that a call
// which then aborts can say what was printed. Without a handler the bytes
// still go where they always went.
type streamWriter struct {
	host  *hostState
	name  string
	level Level
	out   io.Writer
	buf   []byte
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if w.host.opts.handler == nil && w.out != nil {
		if _, err := w.out.Write(p); err != nil {
			return 0, err
		}
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(string(bytes.TrimRight(w.buf[:i], "\r")))
		w.buf = w.buf[i+1:]
	}
	if w.name == "stderr" && len(w.buf) > 0 {
		// An abort message need not end in a newline; keep what there is.
		w.host.lastStderr = string(w.buf)
	}
	return len(p), nil
}

func (w *streamWriter) emit(line string) {
	if line == "" {
		return
	}
	if w.name == "stderr" {
		w.host.lastStderr = line
	}
	w.host.deliver(Diagnostic{Level: w.level, File: w.name, Message: line})
}

// readGuestString copies a pointer-and-length string out of linear memory.
func readGuestString(mod api.Module, ptr, length uint32) string {
	if length == 0 {
		return ""
	}
	b, ok := mod.Memory().Read(ptr, length)
	if !ok {
		return ""
	}
	return string(b)
}

// instantiateHostModule provides the "colorer4go" imports the wrapper uses to
// report diagnostics (colorer_wrapper.cpp, HostLogger and
// colorer4go_throw_abort).
func instantiateHostModule(ctx context.Context, r wazero.Runtime, h *hostState) error {
	i32 := api.ValueTypeI32
	_, err := r.NewHostModuleBuilder("colorer4go").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			h.deliver(Diagnostic{
				Level:    Level(int32(stack[0])),
				File:     readGuestString(mod, uint32(stack[1]), uint32(stack[2])),
				Line:     int(int32(stack[3])),
				Function: readGuestString(mod, uint32(stack[4]), uint32(stack[5])),
				Message:  readGuestString(mod, uint32(stack[6]), uint32(stack[7])),
			})
		}), []api.ValueType{i32, i32, i32, i32, i32, i32, i32, i32}, nil).
		Export("log").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			h.fatalReason = readGuestString(mod, uint32(stack[0]), uint32(stack[1]))
		}), []api.ValueType{i32, i32}, nil).
		Export("fatal").
		Instantiate(ctx)
	return err
}

// NewSession instantiates Colorer and mounts the host configDirMount folder inside WASM.
//
// A catalog Colorer cannot load makes it return a *FatalError; see
// WithDiagnostics for how to learn more about why.
func NewSession(ctx context.Context, catalogPath string, configDirMount string, opts ...Option) (*Session, error) {
	host := &hostState{}
	for _, opt := range opts {
		opt(&host.opts)
	}

	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCompilationCache(sharedCompilationCache()))

	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	// Compile the module to inspect imports
	compiled, err := r.CompileModule(ctx, colorerWasm)
	if err != nil {
		r.Close(ctx)
		return nil, err
	}

	// Always instantiate the "env" module in case there are references to it
	envBuilder := r.NewHostModuleBuilder("env")
	for _, f := range compiled.ImportedFunctions() {
		if f.ModuleName() == "env" {
			envBuilder.NewFunctionBuilder().
				WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
					// Empty stub
				}), f.ParamTypes(), f.ResultTypes()).
				Export(f.Name())
		}
	}
	if _, err = envBuilder.Instantiate(ctx); err != nil {
		r.Close(ctx)
		return nil, err
	}
	if err = instantiateHostModule(ctx, r, host); err != nil {
		r.Close(ctx)
		return nil, err
	}

	// Mount the host config directory containing XML schemas to the WASM root
	// "/", and the user's own styles and schemes, read-only, beside it. The
	// module's stdout and stderr go to the diagnostics handler when there is
	// one, and to the process's own streams otherwise.
	fsConfig := wazero.NewFSConfig().WithDirMount(configDirMount, "/")
	var userLoads []userLoad
	for _, u := range []userLoad{
		{op: "colorer_load_user_hrd", what: "user colour styles", hostPath: host.opts.userHRD, guestPath: userHRDGuestDir, loads: loadsHRD},
		{op: "colorer_load_user_hrc", what: "user schemes", hostPath: host.opts.userHRC, guestPath: userHRCGuestDir, loads: loadsHRC},
	} {
		if u.hostPath == "" {
			continue
		}
		hostDir, guestPath, mErr := userMount(u.hostPath, u.guestPath, u.loads)
		if mErr != nil {
			host.deliver(Diagnostic{Level: LevelWarn, File: "colorer4go", Message: fmt.Sprintf("%s not loaded: %v", u.what, mErr)})
			continue
		}
		fsConfig = fsConfig.WithReadOnlyDirMount(hostDir, u.guestPath)
		host.deliver(Diagnostic{Level: LevelInfo, File: "colorer4go", Message: fmt.Sprintf("%s %q are seen by Colorer as %q", u.what, u.hostPath, guestPath)})
		u.guestPath = guestPath
		userLoads = append(userLoads, u)
	}
	config := wazero.NewModuleConfig().
		WithFSConfig(fsConfig).
		WithStdout(&streamWriter{host: host, name: "stdout", level: LevelInfo, out: os.Stdout}).
		WithStderr(&streamWriter{host: host, name: "stderr", level: LevelError, out: os.Stderr})

	mod, err := r.InstantiateModule(ctx, compiled, config)
	if err != nil {
		r.Close(ctx)
		return nil, err
	}

	// Initialize the WASI Reactor runtime to deploy C++ global constructors
	initWasiFn := mod.ExportedFunction("_initialize")
	if initWasiFn != nil {
		if _, err := callFn(ctx, host, initWasiFn, "_initialize"); err != nil {
			r.Close(ctx)
			return nil, err
		}
	}

	if host.opts.handler != nil {
		if _, err := callFn(ctx, host, mod.ExportedFunction("colorer_set_log_level"), "colorer_set_log_level", uint64(host.opts.level)); err != nil {
			r.Close(ctx)
			return nil, err
		}
	}

	allocFn := mod.ExportedFunction("colorer_alloc")
	initFn := mod.ExportedFunction("colorer_init")
	if allocFn == nil || initFn == nil {
		r.Close(ctx)
		return nil, errors.New("required functions (colorer_alloc or colorer_init) are not exported from WASM")
	}

	// Copy the catalog path string to WASM memory
	pathBytes := []byte(catalogPath)
	pathLen := uint64(len(pathBytes) + 1)
	res, err := callFn(ctx, host, allocFn, "colorer_alloc", pathLen)
	if err != nil {
		r.Close(ctx)
		return nil, err
	}
	pathPtr := uint32(res[0])
	mod.Memory().Write(pathPtr, append(pathBytes, 0))

	res, err = callFn(ctx, host, initFn, "colorer_init", uint64(pathPtr))
	if err != nil {
		// The module is not usable after a failed call, not even to free.
		r.Close(ctx)
		return nil, err
	}
	_, _ = callFn(ctx, host, mod.ExportedFunction("colorer_free"), "colorer_free", uint64(pathPtr))
	if res[0] == 0 {
		r.Close(ctx)
		return nil, errors.New("colorer_init returned null pointer")
	}
	for _, u := range userLoads {
		if err := loadUserPath(ctx, host, mod, uint32(res[0]), u); err != nil {
			r.Close(ctx)
			return nil, err
		}
	}

	s := &Session{
		ctx:  ctx,
		r:    r,
		mod:  mod,
		ptr:  uint32(res[0]),
		host: host,
	}
	s.lineBufferFn = mod.ExportedFunction("colorer_line_buffer")
	s.parseLineFn = mod.ExportedFunction("colorer_parse_line")
	s.getRegionsFn = mod.ExportedFunction("colorer_get_regions")
	return s, nil
}

// loadUserPath hands one mounted user path to Colorer.
func loadUserPath(ctx context.Context, host *hostState, mod api.Module, handle uint32, u userLoad) error {
	b := append([]byte(u.guestPath), 0)
	res, err := callFn(ctx, host, mod.ExportedFunction("colorer_alloc"), "colorer_alloc", uint64(len(b)))
	if err != nil {
		return err
	}
	ptr := uint32(res[0])
	mod.Memory().Write(ptr, b)
	res, err = callFn(ctx, host, mod.ExportedFunction(u.op), u.op, uint64(handle), uint64(ptr))
	if err != nil {
		// A failed call leaves nothing worth freeing; the runtime is closed next.
		return fmt.Errorf("loading %s %q (seen by Colorer as %q): %w", u.what, u.hostPath, u.guestPath, err)
	}
	_, _ = callFn(ctx, host, mod.ExportedFunction("colorer_free"), "colorer_free", uint64(ptr))
	if res[0] == 0 {
		return fmt.Errorf("%s refused the session handle", u.op)
	}
	return nil
}

func (s *Session) Close() {
	// After a failed call the module's heap is in whatever state the abort
	// left it; closing the runtime releases it all without running into it.
	if s.mod != nil && s.fatal == nil {
		s.call(s.mod.ExportedFunction("colorer_destroy"), "colorer_destroy", uint64(s.ptr))
	}
	s.r.Close(s.ctx)
}

// writeCString copies str into memory the module allocates, NUL-terminated,
// and returns a function that frees it again.
func (s *Session) writeCString(str string) (uint32, func(), error) {
	b := append([]byte(str), 0)
	res, err := s.call(s.mod.ExportedFunction("colorer_alloc"), "colorer_alloc", uint64(len(b)))
	if err != nil {
		return 0, func() {}, err
	}
	ptr := uint32(res[0])
	s.mod.Memory().Write(ptr, b)
	return ptr, func() {
		s.call(s.mod.ExportedFunction("colorer_free"), "colorer_free", uint64(ptr))
	}, nil
}

func (s *Session) SetHRD(hrdClass, hrdName string) error {
	cPtr, freeC, err := s.writeCString(hrdClass)
	if err != nil {
		return err
	}
	defer freeC()
	nPtr, freeN, err := s.writeCString(hrdName)
	if err != nil {
		return err
	}
	defer freeN()

	ret, err := s.call(s.mod.ExportedFunction("colorer_set_hrd"), "colorer_set_hrd", uint64(s.ptr), uint64(cPtr), uint64(nPtr))
	if err != nil {
		return err
	}
	if ret[0] == 0 {
		return errors.New("colorer_set_hrd failed")
	}
	return nil
}

func (s *Session) EnumHRDInstances(classID string) ([]HRDInstance, error) {
	getNameFn := s.mod.ExportedFunction("colorer_get_hrd_name")
	getDescFn := s.mod.ExportedFunction("colorer_get_hrd_description")

	cPtr, freeC, err := s.writeCString(classID)
	if err != nil {
		return nil, err
	}
	defer freeC()

	ret, err := s.call(s.mod.ExportedFunction("colorer_enum_hrd_instances"), "colorer_enum_hrd_instances", uint64(s.ptr), uint64(cPtr))
	if err != nil {
		return nil, err
	}
	count := int(ret[0])
	var instances []HRDInstance
	for i := 0; i < count; i++ {
		namePtr, err := s.call(getNameFn, "colorer_get_hrd_name", uint64(s.ptr), uint64(i))
		if err != nil {
			return nil, err
		}
		nameStr, _ := readString(s.mod.Memory(), uint32(namePtr[0]))
		descPtr, err := s.call(getDescFn, "colorer_get_hrd_description", uint64(s.ptr), uint64(i))
		if err != nil {
			return nil, err
		}
		descStr, _ := readString(s.mod.Memory(), uint32(descPtr[0]))
		instances = append(instances, HRDInstance{Name: nameStr, Description: descStr})
	}
	return instances, nil
}

func (s *Session) GetRegionDefine(name string) (*RegionDefine, error) {
	cPtr, freeC, err := s.writeCString(name)
	if err != nil {
		return nil, err
	}
	defer freeC()

	resPtrBlock, err := s.call(s.mod.ExportedFunction("colorer_alloc"), "colorer_alloc", 20)
	if err != nil {
		return nil, err
	}
	pFore := uint32(resPtrBlock[0])
	pBack := pFore + 4
	pStyle := pFore + 8
	pIsForeSet := pFore + 12
	pIsBackSet := pFore + 16
	defer s.call(s.mod.ExportedFunction("colorer_free"), "colorer_free", uint64(pFore))

	ret, err := s.call(s.mod.ExportedFunction("colorer_get_region_define"), "colorer_get_region_define", uint64(s.ptr), uint64(cPtr), uint64(pFore), uint64(pBack), uint64(pStyle), uint64(pIsForeSet), uint64(pIsBackSet))
	if err != nil {
		return nil, err
	}
	if ret[0] == 0 {
		return nil, errors.New("region not found")
	}

	fore, _ := s.mod.Memory().ReadUint32Le(pFore)
	back, _ := s.mod.Memory().ReadUint32Le(pBack)
	style, _ := s.mod.Memory().ReadUint32Le(pStyle)
	isForeSet, _ := s.mod.Memory().ReadUint32Le(pIsForeSet)
	isBackSet, _ := s.mod.Memory().ReadUint32Le(pIsBackSet)

	return &RegionDefine{
		Fore:      fore,
		Back:      back,
		Style:     style,
		IsForeSet: isForeSet != 0,
		IsBackSet: isBackSet != 0,
	}, nil
}

func (s *Session) SelectType(fileName, firstLine string) (bool, error) {
	fnPtr, freeFn, err := s.writeCString(fileName)
	if err != nil {
		return false, err
	}
	defer freeFn()
	flPtr, freeFl, err := s.writeCString(firstLine)
	if err != nil {
		return false, err
	}
	defer freeFl()

	ret, err := s.call(s.mod.ExportedFunction("colorer_select_type"), "colorer_select_type", uint64(s.ptr), uint64(fnPtr), uint64(flPtr))
	if err != nil {
		return false, err
	}
	return ret[0] != 0, nil
}

// FileType is one file type the catalog or the user's schemes declare.
type FileType struct {
	Name        string // the HRC type name, e.g. "json"
	Group       string // the group menus list it under, e.g. "rare"
	Description string // the human name, e.g. "JSON"
}

// FileTypes lists the file types the session knows, in the library's order:
// the catalog's own, then the user's (WithUserHRC).
func (s *Session) FileTypes() ([]FileType, error) {
	enumFn, err := s.exportedFn("colorer_enum_file_types")
	if err != nil {
		return nil, err
	}
	getters := make([]api.Function, 3)
	for i, name := range []string{"colorer_get_file_type_name", "colorer_get_file_type_group", "colorer_get_file_type_description"} {
		if getters[i], err = s.exportedFn(name); err != nil {
			return nil, err
		}
	}
	ret, err := s.call(enumFn, "colorer_enum_file_types", uint64(s.ptr))
	if err != nil {
		return nil, err
	}
	count := int(int32(ret[0]))
	types := make([]FileType, 0, count)
	for i := 0; i < count; i++ {
		var fields [3]string
		for f, fn := range getters {
			res, err := s.call(fn, "colorer_get_file_type_string", uint64(s.ptr), uint64(i))
			if err != nil {
				return nil, err
			}
			if res[0] != 0 {
				if fields[f], err = readString(s.mod.Memory(), uint32(res[0])); err != nil {
					return nil, err
				}
			}
		}
		types = append(types, FileType{Name: fields[0], Group: fields[1], Description: fields[2]})
	}
	return types, nil
}

// LoadFileType loads the scheme of the named file type, as SelectType does
// for the type it picks, without selecting it. That is where a scheme Colorer
// cannot parse shows up, so loading every type finds such a scheme before a
// file of its type is opened.
//
// It reports whether the type has a scheme afterwards. A name no type has is an
// error; a scheme Colorer cannot parse is a *FatalError, as on selection.
func (s *Session) LoadFileType(name string) (bool, error) {
	fn, err := s.exportedFn("colorer_load_file_type")
	if err != nil {
		return false, err
	}
	ptr, free, err := s.writeCString(name)
	if err != nil {
		return false, err
	}
	defer free()
	ret, err := s.call(fn, "colorer_load_file_type", uint64(s.ptr), uint64(ptr))
	if err != nil {
		return false, err
	}
	switch int32(ret[0]) {
	case -1:
		return false, fmt.Errorf("colorer: no file type named %q", name)
	case 0:
		return false, nil
	}
	return true, nil
}

// wasmRegionSize is sizeof(WasmRegion) in colorer_wrapper.cpp: eight 4-byte
// fields (int, unsigned int, and a pointer all being 4 bytes on wasm32), with
// no padding — a static_assert on the C++ side guards this.
const wasmRegionSize = 32

// ParseLinePairs is ParseLine that also returns the line's paired tokens.
func (s *Session) ParseLinePairs(line string) ([]Region, []Pair, error) {
	regions, err := s.ParseLine(line)
	if err != nil {
		return nil, nil, err
	}
	countFn, err := s.exportedFn("colorer_pair_count")
	if err != nil {
		return nil, nil, err
	}
	ret, err := s.call(countFn, "colorer_pair_count", uint64(s.ptr))
	if err != nil {
		return nil, nil, err
	}
	count := int(int32(ret[0]))
	if count <= 0 {
		return regions, nil, nil
	}
	pairsFn, err := s.exportedFn("colorer_get_pairs")
	if err != nil {
		return nil, nil, err
	}
	ptrRes, err := s.call(pairsFn, "colorer_get_pairs", uint64(s.ptr))
	if err != nil {
		return nil, nil, err
	}
	buf, ok := s.mod.Memory().Read(uint32(ptrRes[0]), uint32(count*wasmRegionSize))
	if !ok {
		return nil, nil, errors.New("failed to read pair array from wasm memory")
	}
	pairs := make([]Pair, count)
	for i := range pairs {
		rec := buf[i*wasmRegionSize:]
		pairs[i] = Pair{
			Start:     int(int32(binary.LittleEndian.Uint32(rec[0:4]))),
			End:       int(int32(binary.LittleEndian.Uint32(rec[4:8]))),
			Opens:     binary.LittleEndian.Uint32(rec[8:12]) != 0,
			Fore:      binary.LittleEndian.Uint32(rec[12:16]),
			Back:      binary.LittleEndian.Uint32(rec[16:20]),
			Style:     binary.LittleEndian.Uint32(rec[20:24]),
			IsForeSet: binary.LittleEndian.Uint32(rec[24:28]) != 0,
			IsBackSet: binary.LittleEndian.Uint32(rec[28:32]) != 0,
		}
	}
	return regions, pairs, nil
}

func (s *Session) ParseLine(line string) ([]Region, error) {
	if s.lineBufferFn == nil {
		return nil, errors.New("colorer_line_buffer is not exported by the embedded colorer.wasm; rebuild it with ./build_wasm.sh")
	}

	lineBytes := []byte(line)
	res, err := s.call(s.lineBufferFn, "colorer_line_buffer", uint64(s.ptr), uint64(len(lineBytes)))
	if err != nil {
		return nil, err
	}
	linePtr := uint32(res[0])
	if linePtr == 0 && len(lineBytes) > 0 {
		return nil, errors.New("colorer_line_buffer returned a null pointer")
	}
	if len(lineBytes) > 0 {
		s.mod.Memory().Write(linePtr, lineBytes)
	}

	ret, err := s.call(s.parseLineFn, "colorer_parse_line", uint64(s.ptr), uint64(linePtr), uint64(len(lineBytes)))
	if err != nil {
		return nil, err
	}

	count := int(ret[0])
	if count < 0 {
		return nil, errors.New("colorer_parse_line failed internally")
	}
	if count == 0 {
		return nil, nil
	}

	if s.getRegionsFn == nil {
		return nil, errors.New("colorer_get_regions is not exported by the embedded colorer.wasm; rebuild it with ./build_wasm.sh")
	}
	ptrRes, err := s.call(s.getRegionsFn, "colorer_get_regions", uint64(s.ptr))
	if err != nil {
		return nil, err
	}
	buf, ok := s.mod.Memory().Read(uint32(ptrRes[0]), uint32(count*wasmRegionSize))
	if !ok {
		return nil, errors.New("failed to read region array from wasm memory")
	}

	if s.nameCache == nil {
		s.nameCache = make(map[uint32]string)
	}

	regions := make([]Region, count)
	for i := 0; i < count; i++ {
		rec := buf[i*wasmRegionSize:]
		namePtr := binary.LittleEndian.Uint32(rec[8:12])
		name, cached := s.nameCache[namePtr]
		if !cached {
			name, err = readString(s.mod.Memory(), namePtr)
			if err != nil {
				return nil, err
			}
			s.nameCache[namePtr] = name
		}
		regions[i] = Region{
			Start:     int(int32(binary.LittleEndian.Uint32(rec[0:4]))),
			End:       int(int32(binary.LittleEndian.Uint32(rec[4:8]))),
			Name:      name,
			Fore:      binary.LittleEndian.Uint32(rec[12:16]),
			Back:      binary.LittleEndian.Uint32(rec[16:20]),
			Style:     binary.LittleEndian.Uint32(rec[20:24]),
			IsForeSet: binary.LittleEndian.Uint32(rec[24:28]) != 0,
			IsBackSet: binary.LittleEndian.Uint32(rec[28:32]) != 0,
		}
	}

	return regions, nil
}

// Reset is a no-op on a session that has failed; see Err.
func (s *Session) Reset() {
	s.call(s.mod.ExportedFunction("colorer_reset_session"), "colorer_reset_session", uint64(s.ptr))
}

// exportedFn looks a function up in the module and says what to do when the
// embedded colorer.wasm is older than this Go code.
func (s *Session) exportedFn(name string) (api.Function, error) {
	fn := s.mod.ExportedFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("%s is not exported by the embedded colorer.wasm; rebuild it with ./build_wasm.sh", name)
	}
	return fn, nil
}

// ForgetBefore releases the stored text of every session line below line.
//
// The session numbers lines from zero at the last Reset; ParseLine appends at
// NextLine. Without this call every line ever parsed stays in wasm memory as
// UTF-16 until the session is reset, so scrolling through a large file drags
// the whole file into the heap.
//
// Only lines the parser will not ask for again may be dropped. It reads
// forward only, so anything below the next line to be parsed is safe; a
// caller that keeps a margin below the viewport is safer still. Dropping a
// line and then parsing it again traps the module, which surfaces as an error
// from ParseLine and leaves the session unusable.
//
// The call is clamped and idempotent: lines already dropped and lines not yet
// parsed are ignored.
func (s *Session) ForgetBefore(line int) error {
	fn, err := s.exportedFn("colorer_forget_before")
	if err != nil {
		return err
	}
	if line < 0 {
		line = 0
	}
	_, err = s.call(fn, "colorer_forget_before", uint64(s.ptr), uint64(uint32(line)))
	return err
}

// FirstLine is the number of the oldest line the session still holds. It is
// zero until ForgetBefore is called, and returns to zero on Reset.
func (s *Session) FirstLine() (int, error) {
	return s.lineBound("colorer_first_line")
}

// NextLine is the number the next line passed to ParseLine will get, i.e. the
// count of lines fed since the last Reset.
func (s *Session) NextLine() (int, error) {
	return s.lineBound("colorer_next_line")
}

func (s *Session) lineBound(name string) (int, error) {
	fn, err := s.exportedFn(name)
	if err != nil {
		return 0, err
	}
	res, err := s.call(fn, name, uint64(s.ptr))
	if err != nil {
		return 0, err
	}
	return int(int32(res[0])), nil
}

func readString(mem api.Memory, offset uint32) (string, error) {
	var buf []byte
	for {
		b, ok := mem.ReadByte(offset)
		if !ok {
			return "", errors.New("out of memory bounds")
		}
		if b == 0 {
			break
		}
		buf = append(buf, b)
		offset++
	}
	return string(buf), nil
}
