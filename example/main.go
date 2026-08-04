package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/unxed/colorer4go"
)

func main() {
	ctx := context.Background()

	// Automatically locate the path to original configs in the repository
	configDirOnHost := "colorer/configs"
	if _, err := os.Stat(configDirOnHost); err != nil {
		// If the example is executed from inside the example/ directory
		configDirOnHost = "../colorer/configs"
	}

	absConfigDir, err := filepath.Abs(configDirOnHost)
	if err != nil {
		fmt.Printf("Error getting absolute path: %v\n", err)
		os.Exit(1)
	}

	catalogPath := filepath.Join(absConfigDir, "base", "catalog.xml")
	if _, err := os.Stat(catalogPath); err != nil {
		fmt.Printf("Error: Original Colorer schemas not found at %s.\nPlease make sure the repository is fully cloned.\n", catalogPath)
		os.Exit(1)
	}

	fmt.Printf("Initializing Colorer with original schemas from: %s\n", absConfigDir)

	// Create a session using original repository schemas
	session, err := colorer.NewSession(ctx, "/base/catalog.xml", absConfigDir)
	if err != nil {
		fmt.Printf("Failed to create session: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	fmt.Println("Selecting JSON file type...")
	success, err := session.SelectType("test.json", "{")
	if err != nil || !success {
		fmt.Printf("Failed to select file type: %v, success: %v\n", err, success)
		os.Exit(1)
	}

	line := `{"key": "value"}`
	fmt.Printf("Parsing line: %s\n", line)
	regions, err := session.ParseLine(line)
	if err != nil {
		fmt.Printf("Parsing error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nSuccess! Found %d highlight regions:\n", len(regions))
	for _, r := range regions {
		fmt.Printf("  [%d..%d]: %s\n", r.Start, r.End, r.Name)
	}
}
