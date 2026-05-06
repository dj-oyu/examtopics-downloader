package main

import (
	"fmt"
	"io"
	"os"

	"examtopics-downloader/internal/constants"
)

// runProviders implements `examtopicsdl providers`. It prints each
// known provider slug on its own line; downstream tooling can grep or
// pipe the output as needed.
func runProviders(args []string) int {
	return runProvidersTo(os.Stdout, args)
}

// runProvidersTo is the testable seam used by providers_test.go; it
// writes to the supplied io.Writer instead of os.Stdout so tests can
// capture the output without intercepting the global stream.
func runProvidersTo(w io.Writer, _ []string) int {
	for _, p := range constants.KnownProviders {
		if _, err := fmt.Fprintln(w, p); err != nil {
			fmt.Fprintf(os.Stderr, "providers: write failed: %v\n", err)
			return 1
		}
	}
	return 0
}
