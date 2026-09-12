// Command skillsync mirrors the canonical-source markdown files at
// the repo root into the per-client layouts each consumer expects:
//
//	skills/exam-translator.md  →  the bulk-translation orchestrator
//	  prompt and the single-question retranslate prompt context.
//	  Targets:
//	    - internal/translate/assets/exam-translator.md (committed,
//	      embedded into the Go binary via //go:embed so a fresh
//	      `go build ./...` works without first running `go generate`).
//	    - .claude/skills/exam-translator/SKILL.md (dev-local, gitignored;
//	      discovered by Claude Code, the default translate client).
//
//	agents/exam-translator-worker.md  →  the EXECUTION-MODE worker
//	  definition the orchestrator delegates each literal `bulk-next`
//	  invocation to. Client-neutral, materialized into the Claude Code
//	  subagent layout:
//	    - .claude/agents/exam-translator-worker.md
//
//	The .gemini/ destinations were dropped with gemini-cli support:
//	the skill and the worker prompt are client-neutral markdown, and
//	`examtopicsdl translate -client <name> -dry-run` materializes the
//	skill into whichever client layout is configured.
//
// Wiring:
//
//	//go:generate go run ../../tools/skillsync
//
// lives in internal/translate/translate.go. CI runs `go generate ./...`
// followed by `git diff --exit-code` to refuse PRs that edited a copy
// directly without updating the master at the repo root.
//
// The script self-locates the repo root by walking up from cwd until
// it finds go.mod, so the same invocation works whether `go generate`
// fires from internal/translate (the usual path) or anywhere else.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func findRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err == nil {
			return cwd, nil
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			return "", fmt.Errorf("could not locate go.mod from cwd")
		}
		cwd = parent
	}
}

// syncJob describes one source-of-truth file and the locations it
// should be mirrored to. The list at the bottom of main() is the
// single place to add a new orchestrator skill / worker agent: the
// script handles repeat sources (e.g., a skill targeting two
// layouts) by listing its destinations together.
type syncJob struct {
	src     string
	targets []string
}

func main() {
	root, err := findRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "skillsync: %v\n", err)
		os.Exit(1)
	}
	jobs := []syncJob{
		{
			src: filepath.Join(root, "skills", "exam-translator.md"),
			targets: []string{
				filepath.Join(root, "internal", "translate", "assets", "exam-translator.md"),
				filepath.Join(root, ".claude", "skills", "exam-translator", "SKILL.md"),
			},
		},
		{
			src: filepath.Join(root, "agents", "exam-translator-worker.md"),
			targets: []string{
				filepath.Join(root, ".claude", "agents", "exam-translator-worker.md"),
			},
		},
	}
	for _, j := range jobs {
		data, err := os.ReadFile(j.src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skillsync: read source %s: %v\n", j.src, err)
			os.Exit(1)
		}
		for _, t := range j.targets {
			if err := os.MkdirAll(filepath.Dir(t), 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "skillsync: mkdir %s: %v\n", filepath.Dir(t), err)
				os.Exit(1)
			}
			if err := writeIfChanged(t, data); err != nil {
				fmt.Fprintf(os.Stderr, "skillsync: write %s: %v\n", t, err)
				os.Exit(1)
			}
		}
	}
}

// writeIfChanged avoids touching the file's mtime when the bytes are
// already identical. CI's `git diff --exit-code` parity check still
// works either way, but a stable mtime keeps incremental Go build
// caches happy when go:embed reads the asset on subsequent builds.
func writeIfChanged(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if len(existing) == len(data) {
			same := true
			for i := range existing {
				if existing[i] != data[i] {
					same = false
					break
				}
			}
			if same {
				fmt.Printf("skillsync: %s unchanged\n", path)
				return nil
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("skillsync: wrote %s (%d bytes)\n", path, len(data))
	return nil
}
