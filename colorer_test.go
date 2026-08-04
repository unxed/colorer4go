package colorer

import (
	"context"
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
