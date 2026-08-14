package colorer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Pre-check configs existence on the host to prevent unexpected WASM traps
func verifyConfigsOnHost(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}

	configDir := filepath.Join(wd, "colorer", "configs")
	catalogPath := filepath.Join(configDir, "base", "catalog.xml")
	if _, err := os.Stat(catalogPath); err != nil {
		t.Fatalf("Critical Error: Colorer configurations not found at %s. Please make sure that colorer/configs is populated before running tests.", catalogPath)
	}
	return configDir
}

// Test 1: Basic successful parsing of a JSON line
func TestColorerWasm_SuccessJSON(t *testing.T) {
	verifyConfigsOnHost(t)
	ctx := context.Background()

	session, err := NewSession(ctx, "/base/catalog.xml", "colorer/configs")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	success, err := session.SelectType("test.json", "{")
	if err != nil || !success {
		t.Fatalf("Failed to select type: %v, success: %v", err, success)
	}

	regions, err := session.ParseLine(`{"key": "value"}`)
	if err != nil {
		t.Fatalf("Failed to parse line: %v", err)
	}

	t.Logf("Parsed %d regions:", len(regions))
	for _, r := range regions {
		t.Logf("  [%d..%d]: %s", r.Start, r.End, r.Name)
	}

	if len(regions) != 15 {
		t.Errorf("Expected exactly 15 regions for JSON line, but got %d", len(regions))
	}
}

// Test 2: Plain text parsing where no highlight tokens are matched
func TestColorerWasm_PlainText(t *testing.T) {
	verifyConfigsOnHost(t)
	ctx := context.Background()

	session, err := NewSession(ctx, "/base/catalog.xml", "colorer/configs")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	success, err := session.SelectType("test.txt", "Hello")
	if err != nil || !success {
		t.Fatalf("Failed to select type: %v", err)
	}

	regions, err := session.ParseLine("")
	if err != nil {
		t.Fatalf("Failed to parse line: %v", err)
	}

	if len(regions) != 0 {
		t.Errorf("Expected 0 regions for empty line, but got %d", len(regions))
	}
}

// Test 3: Cache stability and multiple lines sequential parsing
func TestColorerWasm_MultipleLinesAndReset(t *testing.T) {
	verifyConfigsOnHost(t)
	ctx := context.Background()

	session, err := NewSession(ctx, "/base/catalog.xml", "colorer/configs")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	success, err := session.SelectType("test.json", "{")
	if err != nil || !success {
		t.Fatalf("Failed to select type: %v", err)
	}

	lines := []string{
		`{`,
		`  "key": "value",`,
		`  "number": 123`,
		`}`,
	}

	for i, line := range lines {
		regions, err := session.ParseLine(line)
		if err != nil {
			t.Fatalf("Failed to parse line %d: %v", i, err)
		}
		t.Logf("Line %d parsed successfully. Found %d regions.", i, len(regions))
	}

	session.Reset()
	success, err = session.SelectType("another.json", `{"a": 1}`)
	if err != nil || !success {
		t.Fatalf("Failed to select type after Reset: %v", err)
	}
}

// jsonBlockLines builds a JSON document whose braces stay open across most of
// it, so the parser really does carry state from line to line.
func jsonBlockLines(n int) []string {
	lines := []string{"{", `  "items": [`}
	for i := len(lines); i < n-2; i++ {
		lines = append(lines, fmt.Sprintf(`    {"key%d": "value%d"},`, i, i))
	}
	return append(lines, "  ]", "}")
}

func sameRegions(a, b []Region) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func newJSONSession(t *testing.T) *Session {
	t.Helper()
	verifyConfigsOnHost(t)
	session, err := NewSession(context.Background(), "/base/catalog.xml", "colorer/configs")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	success, err := session.SelectType("test.json", "{")
	if err != nil || !success {
		session.Close()
		t.Fatalf("Failed to select type: %v, success: %v", err, success)
	}
	return session
}

// Test 4: trimming the window must not change what the parser produces. The
// parser only ever reads forward, so a line below the one being parsed can be
// dropped; the regions of every line must come out identical to a session that
// dropped nothing.
func TestColorerWasm_ForgetBeforeMatchesUntrimmedParse(t *testing.T) {
	lines := jsonBlockLines(300)

	reference := newJSONSession(t)
	defer reference.Close()
	want := make([][]Region, len(lines))
	for i, line := range lines {
		regions, err := reference.ParseLine(line)
		if err != nil {
			t.Fatalf("reference: failed to parse line %d: %v", i, err)
		}
		want[i] = regions
	}

	// keep says how many lines behind the one just parsed stay in the window.
	// Zero is the extreme the invariant allows: nothing but the current line.
	for _, keep := range []int{0, 8} {
		t.Run(fmt.Sprintf("keep%d", keep), func(t *testing.T) {
			session := newJSONSession(t)
			defer session.Close()

			for i, line := range lines {
				regions, err := session.ParseLine(line)
				if err != nil {
					t.Fatalf("failed to parse line %d: %v", i, err)
				}
				if !sameRegions(regions, want[i]) {
					t.Fatalf("line %d: got %d regions %v, want %d regions %v",
						i, len(regions), regions, len(want[i]), want[i])
				}
				if err := session.ForgetBefore(i - keep); err != nil {
					t.Fatalf("ForgetBefore(%d) failed: %v", i-keep, err)
				}
			}

			first, err := session.FirstLine()
			if err != nil {
				t.Fatalf("FirstLine failed: %v", err)
			}
			if want := len(lines) - 1 - keep; first != want {
				t.Errorf("FirstLine() = %d, want %d", first, want)
			}
		})
	}
}

// Test 5: the window bookkeeping itself — where the two bounds stand, and that
// out-of-range and repeated calls are harmless.
func TestColorerWasm_ForgetBeforeWindowBookkeeping(t *testing.T) {
	session := newJSONSession(t)
	defer session.Close()

	assertWindow := func(step string, wantFirst, wantNext int) {
		t.Helper()
		first, err := session.FirstLine()
		if err != nil {
			t.Fatalf("%s: FirstLine failed: %v", step, err)
		}
		next, err := session.NextLine()
		if err != nil {
			t.Fatalf("%s: NextLine failed: %v", step, err)
		}
		if first != wantFirst || next != wantNext {
			t.Errorf("%s: window is [%d, %d), want [%d, %d)", step, first, next, wantFirst, wantNext)
		}
	}

	assertWindow("fresh session", 0, 0)

	lines := jsonBlockLines(10)
	for i, line := range lines {
		if _, err := session.ParseLine(line); err != nil {
			t.Fatalf("failed to parse line %d: %v", i, err)
		}
	}
	assertWindow("after 10 lines", 0, 10)

	if err := session.ForgetBefore(4); err != nil {
		t.Fatalf("ForgetBefore(4) failed: %v", err)
	}
	assertWindow("after ForgetBefore(4)", 4, 10)

	// Below the current first line, and negative: both no-ops.
	if err := session.ForgetBefore(4); err != nil {
		t.Fatalf("repeated ForgetBefore(4) failed: %v", err)
	}
	if err := session.ForgetBefore(1); err != nil {
		t.Fatalf("ForgetBefore(1) failed: %v", err)
	}
	if err := session.ForgetBefore(-5); err != nil {
		t.Fatalf("ForgetBefore(-5) failed: %v", err)
	}
	assertWindow("after no-op calls", 4, 10)

	// Past the end: clamped to the next line, never beyond it, so the line
	// numbering the parse cache uses stays intact.
	if err := session.ForgetBefore(9999); err != nil {
		t.Fatalf("ForgetBefore(9999) failed: %v", err)
	}
	assertWindow("after ForgetBefore(9999)", 10, 10)

	// An empty window still accepts the next line.
	if _, err := session.ParseLine(`  "tail": 1`); err != nil {
		t.Fatalf("failed to parse after emptying the window: %v", err)
	}
	assertWindow("after parsing into an empty window", 10, 11)

	session.Reset()
	assertWindow("after Reset", 0, 0)
}
