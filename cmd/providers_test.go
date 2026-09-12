package main

import (
	"bytes"
	"strings"
	"testing"

	"examtopics-downloader/internal/constants"
)

func TestRunProvidersTo_OnePerLine(t *testing.T) {
	var buf bytes.Buffer
	exit := runProvidersTo(&buf, nil)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	got := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(got) != len(constants.KnownProviders) {
		t.Fatalf("printed %d lines, want %d", len(got), len(constants.KnownProviders))
	}
	for i, p := range constants.KnownProviders {
		if got[i] != p {
			t.Errorf("line %d = %q, want %q", i, got[i], p)
		}
	}
}
