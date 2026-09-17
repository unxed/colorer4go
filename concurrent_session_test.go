package colorer

import (
	"context"
	"sync"
	"testing"
)

// TestNewSession_Concurrent creates and closes sessions from several
// goroutines at once. Every session has a wazero runtime of its own, and all of
// them compile through the one process-wide compilation cache; f4 crashed in
// CI with two sessions being set up at the same time, one of them panicking in
// wazevo's moduleEngine.NewFunction ("index out of range [14] with length 0")
// under Module.ExportedFunction("_initialize") while the other was inside a
// WASM call. This test only has to not panic: a session that fails to load
// the empty configuration directory is closed by NewSession and is fine here.
func TestNewSession_Concurrent(t *testing.T) {
	const workers, rounds = 8, 5
	dirs := make([]string, workers)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				s, err := NewSession(context.Background(), "/base/catalog.xml", dir)
				if err == nil {
					s.Close()
				}
			}
		}(dirs[w])
	}
	wg.Wait()
}
