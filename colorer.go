package colorer

import (
	"context"
	_ "embed"
	"errors"
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
	Start int
	End   int
	Name  string
}

type Session struct {
	ctx context.Context
	r   wazero.Runtime
	mod api.Module
	ptr uint32 // Pointer to the ColorerSession struct in C++
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

	regions := make([]Region, count)
	getStart := s.mod.ExportedFunction("colorer_get_region_start")
	getEnd := s.mod.ExportedFunction("colorer_get_region_end")
	getName := s.mod.ExportedFunction("colorer_get_region_name")

	for i := 0; i < count; i++ {
		rStart, _ := getStart.Call(s.ctx, uint64(s.ptr), uint64(i))
		rEnd, _ := getEnd.Call(s.ctx, uint64(s.ptr), uint64(i))
		rNamePtr, _ := getName.Call(s.ctx, uint64(s.ptr), uint64(i))

		// Read the region classification name from WASM memory up to the null-terminator
		nameStr, err := readString(s.mod.Memory(), uint32(rNamePtr[0]))
		if err != nil {
			return nil, err
		}

		regions[i] = Region{
			Start: int(rStart[0]),
			End:   int(rEnd[0]),
			Name:  nameStr,
		}
	}

	return regions, nil
}

func (s *Session) Reset() {
	s.mod.ExportedFunction("colorer_reset_session").Call(s.ctx, uint64(s.ptr))
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
