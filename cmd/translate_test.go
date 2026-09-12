package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTranslateTo_ListClients(t *testing.T) {
	var buf bytes.Buffer
	exit := runTranslateTo(&buf, []string{"-list-clients"})
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	// gemini-cli support was removed (upstream development wound down): the
	// remaining clients are claude (wired), codex and exec (placeholders).
	for _, want := range []string{"claude", "codex", "exec"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("list output missing %q\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "gemini") {
		t.Errorf("list output should no longer offer gemini:\n%s", buf.String())
	}
}

func TestRunTranslateTo_DryRunMaterializesSkill(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	exit := runTranslateTo(&buf, []string{"-client", "claude", "-root", root, "-dry-run"})
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\n%s", exit, buf.String())
	}
	target := filepath.Join(root, ".claude", "skills", "exam-translator", "SKILL.md")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected materialized file at %s: %v", target, err)
	}
	if len(got) == 0 {
		t.Errorf("materialized file is empty")
	}
}

func TestRunTranslateTo_UnknownClientReturnsUsage(t *testing.T) {
	var buf bytes.Buffer
	exit := runTranslateTo(&buf, []string{"-client", "noop", "-dry-run"})
	if exit != 2 {
		t.Errorf("exit = %d, want 2", exit)
	}
	if !strings.Contains(buf.String(), "unknown client") {
		t.Errorf("missing usage hint\n%s", buf.String())
	}
}

func TestRunTranslateTo_ExplainRequiresDBAndTID(t *testing.T) {
	var buf bytes.Buffer
	exit := runTranslateTo(&buf, []string{"explain"})
	if exit != 2 {
		t.Errorf("exit = %d, want 2", exit)
	}
	if !strings.Contains(buf.String(), "-db and -tid are required") {
		t.Errorf("missing usage hint\n%s", buf.String())
	}
}

func TestRunTranslateTo_ExplainUnknownClientReturnsUsage(t *testing.T) {
	var buf bytes.Buffer
	exit := runTranslateTo(&buf, []string{
		"explain",
		"-db", filepath.Join(t.TempDir(), "explain.db"),
		"-tid", "01HZX5K2E8VCQK00000000000Q",
		"-client", "nope",
	})
	if exit != 2 {
		t.Errorf("exit = %d, want 2\n%s", exit, buf.String())
	}
	if !strings.Contains(buf.String(), "unknown client") {
		t.Errorf("missing usage hint\n%s", buf.String())
	}
}
