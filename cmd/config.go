package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"examtopics-downloader/internal/config"
)

// runConfig implements `examtopicsdl config`. It loads the runtime
// config (config.json + env overrides + auto-generated hostId) and
// prints it as indented JSON, with an extra "loadedFrom" field that
// names the source file the values came from. Useful for debugging
// search-path resolution and confirming the active hostId.
func runConfig(args []string) int {
	return runConfigTo(os.Stdout, args)
}

// runConfigTo is the testable seam used by config_test.go; it writes
// JSON to the supplied io.Writer instead of os.Stdout.
func runConfigTo(w io.Writer, _ []string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: load failed: %v\n", err)
		return 1
	}
	// Anonymous wrapper exposes LoadedFrom as a real JSON key. The
	// embedded *Config keeps its `json:"-"` LoadedFrom hidden, and the
	// outer field with json:"loadedFrom" wins on serialization.
	out := struct {
		*config.Config
		LoadedFrom string `json:"loadedFrom,omitempty"`
	}{
		Config:     cfg,
		LoadedFrom: cfg.LoadedFrom,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "config: encode failed: %v\n", err)
		return 1
	}
	return 0
}
