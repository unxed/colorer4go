package colorer

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestProbeFirstSessions gathers information for a crash seen in f4's CI and
// is skipped unless COLORER4GO_PROBE=1; .github/workflows/probe.yml runs it.
//
// The crash is "index out of range [14] with length 0" in wazevo's
// moduleEngine.NewFunction (module_engine.go:204, p.entryPreamblesPtrs[typIndex])
// under Module.ExportedFunction("_initialize") in NewSession. In wazero v1.12.0
// a module found in the file cache is published to the engine's in-memory
// table (addCompiledModuleToMemory) before its entry preambles are compiled,
// and the engine is shared by every runtime that uses the same
// CompilationCache. A session that takes the module from that table in the
// meantime and instantiates it reads an empty entryPreamblesPtrs. With a sleep
// added to wazero between the two steps this test produced exactly that panic
// and stack; what it is meant to find out here is whether it happens without
// one, on real runners.
//
// Only the first sessions of a process can meet the module half-published:
// colorer4go never closes the CompiledModule, so after the first CompileModule
// the module stays in the in-memory table for the life of the process. The
// test therefore starts all its sessions at the beginning of the process, one
// every COLORER4GO_PROBE_STEP, with COLORER4GO_PROBE_BURN spinning goroutines
// standing in for a loaded machine, and prints one PROBE-RESULT line.
func TestProbeFirstSessions(t *testing.T) {
	if os.Getenv("COLORER4GO_PROBE") != "1" {
		t.Skip("set COLORER4GO_PROBE=1 to run")
	}
	workers := probeEnvInt("COLORER4GO_PROBE_WORKERS", 16)
	burn := probeEnvInt("COLORER4GO_PROBE_BURN", 0)
	step, err := time.ParseDuration(os.Getenv("COLORER4GO_PROBE_STEP"))
	if err != nil {
		step = 2 * time.Millisecond
	}

	stop := make(chan struct{})
	defer close(stop)
	for b := 0; b < burn; b++ {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
			}
		}()
	}

	var (
		mu     sync.Mutex
		panics int
		first  string
		wg     sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					panics++
					if first == "" {
						first = fmt.Sprintf("%v\n%s", r, debug.Stack())
					}
					mu.Unlock()
				}
			}()
			time.Sleep(time.Duration(i) * step)
			s, err := NewSession(context.Background(), "/base/catalog.xml", t.TempDir())
			if err == nil {
				s.Close()
			}
		}(w)
	}
	wg.Wait()
	fmt.Printf("PROBE-RESULT workers=%d step=%v burn=%d panics=%d\n", workers, step, burn, panics)
	if first != "" {
		fmt.Printf("PROBE-FIRST-PANIC %s\n", first)
	}
}

func probeEnvInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}
