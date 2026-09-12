package translate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillMarkdown_NotEmpty(t *testing.T) {
	if len(SkillMarkdown) == 0 {
		t.Fatal("SkillMarkdown is empty (embed.FS read returned no bytes)")
	}
	if !strings.Contains(SkillMarkdown, "exam-translator") {
		t.Errorf("SkillMarkdown does not look like the exam-translator skill (no 'exam-translator' substring)")
	}
}

func TestKnownClients_IncludesAllPlannedTargets(t *testing.T) {
	got := map[string]struct{}{}
	for _, c := range KnownClients() {
		got[c.Name] = struct{}{}
	}
	for _, want := range []string{"gemini", "claude", "codex", "exec"} {
		if _, ok := got[want]; !ok {
			t.Errorf("KnownClients missing %q (have %v)", want, got)
		}
	}
}

func TestClientByName_FoundAndNotFound(t *testing.T) {
	c, ok := ClientByName("gemini")
	if !ok {
		t.Fatal("ClientByName(gemini) returned ok=false")
	}
	if c.Name != "gemini" {
		t.Errorf("ClientByName(gemini).Name = %q", c.Name)
	}
	if _, ok := ClientByName("nope"); ok {
		t.Error("ClientByName(nope) returned ok=true, want false")
	}
}

func TestMaterialize_WritesSkillToClientLayout(t *testing.T) {
	root := t.TempDir()
	c := Client{
		Name:          "test-client",
		SkillRelPath:  filepath.Join(".test-client", "skills", "exam-translator", "SKILL.md"),
	}
	written, err := Materialize(c, root)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := filepath.Join(root, c.SkillRelPath)
	if written != want {
		t.Errorf("Materialize returned %q, want %q", written, want)
	}
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read materialized file: %v", err)
	}
	if string(got) != SkillMarkdown {
		t.Errorf("materialized bytes != embedded skill (%d vs %d bytes)", len(got), len(SkillMarkdown))
	}
}

func TestMaterialize_CreatesParentDirs(t *testing.T) {
	root := t.TempDir()
	c := Client{
		Name:         "deep",
		SkillRelPath: filepath.Join("a", "b", "c", "d", "SKILL.md"),
	}
	if _, err := Materialize(c, root); err != nil {
		t.Fatalf("Materialize with deep path: %v", err)
	}
}

func TestMaterialize_OverwritesExisting(t *testing.T) {
	root := t.TempDir()
	c := Client{Name: "overwrite", SkillRelPath: "out.md"}
	target := filepath.Join(root, c.SkillRelPath)
	if err := os.WriteFile(target, []byte("stale content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(c, root); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != SkillMarkdown {
		t.Errorf("Materialize did not overwrite stale file (%d bytes)", len(got))
	}
}
