package colorer

import (
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	// nameCache maps a region name's wasm pointer to the Go string already
	// read from it. The wrapper's own name_cache (colorer_wrapper.cpp) keeps
	// that pointer stable for the life of the session — schemas load once,
	// and colorer_reset_session does not touch it — so a name is read out of
	// linear memory at most once per session no matter how many lines carry
	// it.
	nameCache map[uint32]string
}

// NewSession instantiates Colorer and mounts the host configDirMount folder inside WASM
func NewSession(ctx context.Context, catalogPath string, configDirMount string) (*Session, error) {
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

	// Mount the host config directory containing XML schemas to the WASM root "/"
	// Redirect Stderr/Stdout to host console to intercept C++ error prints
	config := wazero.NewModuleConfig().
		WithFSConfig(wazero.NewFSConfig().WithDirMount(configDirMount, "/")).
		WithStdout(os.Stdout).
		WithStderr(os.Stderr)

	mod, err := r.InstantiateModule(ctx, compiled, config)
	if err != nil {
		r.Close(ctx)
		return nil, err
	}

	// Initialize the WASI Reactor runtime to deploy C++ global constructors
	initWasiFn := mod.ExportedFunction("_initialize")
	if initWasiFn != nil {
		if _, err := initWasiFn.Call(ctx); err != nil {
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
	res, err := allocFn.Call(ctx, pathLen)
	if err != nil {
		r.Close(ctx)
		return nil, err
	}
	pathPtr := uint32(res[0])
	defer mod.ExportedFunction("colorer_free").Call(ctx, uint64(pathPtr))

	mod.Memory().Write(pathPtr, append(pathBytes, 0))

	res, err = initFn.Call(ctx, uint64(pathPtr))
	if err != nil || res[0] == 0 {
		r.Close(ctx)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("colorer_init returned null pointer")
	}

	return &Session{
		ctx: ctx,
		r:   r,
		mod: mod,
		ptr: uint32(res[0]),
	}, nil
}

func (s *Session) Close() {
	if s.mod != nil {
		s.mod.ExportedFunction("colorer_destroy").Call(s.ctx, uint64(s.ptr))
	}
	s.r.Close(s.ctx)
}

func (s *Session) SetHRD(hrdClass, hrdName string) error {
	allocFn := s.mod.ExportedFunction("colorer_alloc")
	freeFn := s.mod.ExportedFunction("colorer_free")
	setHrdFn := s.mod.ExportedFunction("colorer_set_hrd")

	cBytes := append([]byte(hrdClass), 0)
	cRes, err := allocFn.Call(s.ctx, uint64(len(cBytes)))
	if err != nil {
		return err
	}
	cPtr := uint32(cRes[0])
	defer freeFn.Call(s.ctx, uint64(cPtr))
	s.mod.Memory().Write(cPtr, cBytes)

	nBytes := append([]byte(hrdName), 0)
	nRes, err := allocFn.Call(s.ctx, uint64(len(nBytes)))
	if err != nil {
		return err
	}
	nPtr := uint32(nRes[0])
	defer freeFn.Call(s.ctx, uint64(nPtr))
	s.mod.Memory().Write(nPtr, nBytes)

	ret, err := setHrdFn.Call(s.ctx, uint64(s.ptr), uint64(cPtr), uint64(nPtr))
	if err != nil || ret[0] == 0 {
		return errors.New("colorer_set_hrd failed")
	}
	return nil
}

func (s *Session) EnumHRDInstances(classID string) ([]HRDInstance, error) {
	allocFn := s.mod.ExportedFunction("colorer_alloc")
	freeFn := s.mod.ExportedFunction("colorer_free")
	enumFn := s.mod.ExportedFunction("colorer_enum_hrd_instances")
	getNameFn := s.mod.ExportedFunction("colorer_get_hrd_name")
	getDescFn := s.mod.ExportedFunction("colorer_get_hrd_description")

	cBytes := append([]byte(classID), 0)
	cRes, err := allocFn.Call(s.ctx, uint64(len(cBytes)))
	if err != nil {
		return nil, err
	}
	cPtr := uint32(cRes[0])
	defer freeFn.Call(s.ctx, uint64(cPtr))
	s.mod.Memory().Write(cPtr, cBytes)

	ret, err := enumFn.Call(s.ctx, uint64(s.ptr), uint64(cPtr))
	if err != nil {
		return nil, err
	}
	count := int(ret[0])
	var instances []HRDInstance
	for i := 0; i < count; i++ {
		namePtr, _ := getNameFn.Call(s.ctx, uint64(s.ptr), uint64(i))
		nameStr, _ := readString(s.mod.Memory(), uint32(namePtr[0]))
		descPtr, _ := getDescFn.Call(s.ctx, uint64(s.ptr), uint64(i))
		descStr, _ := readString(s.mod.Memory(), uint32(descPtr[0]))
		instances = append(instances, HRDInstance{Name: nameStr, Description: descStr})
	}
	return instances, nil
}

func (s *Session) GetRegionDefine(name string) (*RegionDefine, error) {
	allocFn := s.mod.ExportedFunction("colorer_alloc")
	freeFn := s.mod.ExportedFunction("colorer_free")
	getRdFn := s.mod.ExportedFunction("colorer_get_region_define")

	cBytes := append([]byte(name), 0)
	cRes, err := allocFn.Call(s.ctx, uint64(len(cBytes)))
	if err != nil {
		return nil, err
	}
	cPtr := uint32(cRes[0])
	defer freeFn.Call(s.ctx, uint64(cPtr))
	s.mod.Memory().Write(cPtr, cBytes)

	resPtrBlock, err := allocFn.Call(s.ctx, 20)
	if err != nil {
		return nil, err
	}
	pFore := uint32(resPtrBlock[0])
	pBack := pFore + 4
	pStyle := pFore + 8
	pIsForeSet := pFore + 12
	pIsBackSet := pFore + 16
	defer freeFn.Call(s.ctx, uint64(pFore))

	ret, err := getRdFn.Call(s.ctx, uint64(s.ptr), uint64(cPtr), uint64(pFore), uint64(pBack), uint64(pStyle), uint64(pIsForeSet), uint64(pIsBackSet))
	if err != nil || ret[0] == 0 {
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
	allocFn := s.mod.ExportedFunction("colorer_alloc")
	freeFn := s.mod.ExportedFunction("colorer_free")
	selectFn := s.mod.ExportedFunction("colorer_select_type")

	fnBytes := append([]byte(fileName), 0)
	res, err := allocFn.Call(s.ctx, uint64(len(fnBytes)))
	if err != nil {
		return false, err
	}
	fnPtr := uint32(res[0])
	defer freeFn.Call(s.ctx, uint64(fnPtr))
	s.mod.Memory().Write(fnPtr, fnBytes)

	flBytes := append([]byte(firstLine), 0)
	res, err = allocFn.Call(s.ctx, uint64(len(flBytes)))
	if err != nil {
		return false, err
	}
	flPtr := uint32(res[0])
	defer freeFn.Call(s.ctx, uint64(flPtr))
	s.mod.Memory().Write(flPtr, flBytes)

	ret, err := selectFn.Call(s.ctx, uint64(s.ptr), uint64(fnPtr), uint64(flPtr))
	if err != nil {
		return false, err
	}
	return ret[0] != 0, nil
}

// wasmRegionSize is sizeof(WasmRegion) in colorer_wrapper.cpp: eight 4-byte
// fields (int, unsigned int, and a pointer all being 4 bytes on wasm32), with
// no padding — a static_assert on the C++ side guards this.
const wasmRegionSize = 32

func (s *Session) ParseLine(line string) ([]Region, error) {
	allocFn := s.mod.ExportedFunction("colorer_alloc")
	freeFn := s.mod.ExportedFunction("colorer_free")
	parseFn := s.mod.ExportedFunction("colorer_parse_line")

	lineBytes := []byte(line)
	res, err := allocFn.Call(s.ctx, uint64(len(lineBytes)))
	if err != nil {
		return nil, err
	}
	linePtr := uint32(res[0])
	defer freeFn.Call(s.ctx, uint64(linePtr))
	s.mod.Memory().Write(linePtr, lineBytes)

	ret, err := parseFn.Call(s.ctx, uint64(s.ptr), uint64(linePtr), uint64(len(lineBytes)))
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

	regionsFn, err := s.exportedFn("colorer_get_regions")
	if err != nil {
		return nil, err
	}
	ptrRes, err := regionsFn.Call(s.ctx, uint64(s.ptr))
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

func (s *Session) Reset() {
	s.mod.ExportedFunction("colorer_reset_session").Call(s.ctx, uint64(s.ptr))
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
	_, err = fn.Call(s.ctx, uint64(s.ptr), uint64(uint32(line)))
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
	res, err := fn.Call(s.ctx, uint64(s.ptr))
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
