// Package translate ships the portable exam-translation skill markdown
// (single source of truth at internal/translate/assets/exam-translator.md)
// and the per-LLM-CLI adapters that materialize the skill into the layout
// each client expects. The actual LLM call is delegated to the chosen CLI;
// this package owns the skill content and the file layout, not the
// reasoning. See §3.6 of docs/plans/portable-builds.md for the design.
//
// Adding a new client is a one-file change: append a Client struct to the
// known list with the relative path the CLI looks up its skills from. The
// embedded SkillMarkdown is written verbatim so cross-client consistency
// follows from the single asset.
package translate

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// SkillMarkdown is the raw markdown content of the exam-translator skill,
// embedded at build time from internal/translate/assets/exam-translator.md.
//
// The canonical source of truth lives at <repo>/skills/exam-translator.md;
// the asset file beside this package and the CLI's own skill layout
// SKILL.md are both regenerated from it via `go generate ./...`. CI runs
// the generator and `git diff --exit-code` to refuse drift in either
// direction. Edit skills/exam-translator.md, then `go generate` to
// propagate.
//
//go:generate go run ../../tools/skillsync
//go:embed assets/exam-translator.md
var SkillMarkdown string

// Client describes one LLM CLI's expected skill layout. SkillRelPath is
// the path (relative to the workspace root passed to Materialize) where
// the CLI will find the skill markdown when invoked.
type Client struct {
	Name         string
	SkillRelPath string
}

// known lists the supported clients per §3.6: Claude Code,
// OpenAI Codex CLI, and a generic exec adapter for arbitrary command
// runners. SkillRelPath is what the CLI's own skill discovery rules
// expect; if a CLI later changes its convention, only this table moves.
var known = []Client{
	{Name: "claude", SkillRelPath: filepath.Join(".claude", "skills", "exam-translator", "SKILL.md")},
	{Name: "codex", SkillRelPath: filepath.Join(".codex", "skills", "exam-translator", "SKILL.md")},
	{Name: "exec", SkillRelPath: filepath.Join("skills", "exam-translator.md")},
}

// KnownClients returns a copy of the registered client table so callers
// can iterate without risk of mutating the package-level slice.
func KnownClients() []Client {
	out := make([]Client, len(known))
	copy(out, known)
	return out
}

// ClientByName looks up a client by Name (e.g., "claude") and reports
// whether it was found.
func ClientByName(name string) (Client, bool) {
	for _, c := range known {
		if c.Name == name {
			return c, true
		}
	}
	return Client{}, false
}

// Materialize writes the embedded SkillMarkdown to the path
// filepath.Join(root, c.SkillRelPath), creating any missing parent
// directories. Returns the absolute target path written. Existing files
// are overwritten — Materialize is the only sanctioned writer for the
// client-specific skill copies, so divergence is by design impossible
// when callers funnel through this entry point.
func Materialize(c Client, root string) (string, error) {
	target := filepath.Join(root, c.SkillRelPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(SkillMarkdown), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", target, err)
	}
	return target, nil
}
