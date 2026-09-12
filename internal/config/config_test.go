package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withIsolatedHome sets HOME / USERPROFILE / APPDATA / XDG_CONFIG_HOME and
// EXAMTOPICS_CONFIG to point at t.TempDir() so Load doesn't read the
// developer's real config or write to ~/.config/examtopics.
func withIsolatedHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("APPDATA", filepath.Join(dir, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("EXAMTOPICS_CONFIG", "")
	t.Setenv("EXAMTOPICS_DATA_DIR", "")
	t.Setenv("EXAMTOPICS_LOG_DIR", "")
	t.Setenv("EXAMTOPICS_DOWNLOADER_BIN", "")
	t.Setenv("EXAMTOPICS_HOST_ID", "")
	t.Setenv("EXAMTOPICS_TRANSLATE_CLIENT", "")
	t.Setenv("EXAMTOPICS_TRANSLATE_MODEL", "")
	t.Setenv("EXAMTOPICS_TRANSLATE_BIN", "")
	// Force cwd into the temp dir so Load doesn't accidentally pick up the
	// repo's own files when run from `go test ./...`.
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}
	return dir
}

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.Web.Host != "127.0.0.1" || c.Web.Port != 8787 || !c.Web.AdminEnabled {
		t.Fatalf("unexpected web defaults: %+v", c.Web)
	}
	if c.Scrape.DefaultProvider != "amazon" {
		t.Fatalf("unexpected scrape defaults: %+v", c.Scrape)
	}
}

func TestLoad_NoConfigFile_UsesDefaultsAndGeneratesHostID(t *testing.T) {
	withIsolatedHome(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HostID == "" {
		t.Fatal("HostID should be generated when no config exists")
	}
	if cfg.Web.Port != 8787 {
		t.Fatalf("default port not preserved: got %d", cfg.Web.Port)
	}
	// HostID should now be persisted to the user-config path.
	up := userConfigPath()
	if up == "" {
		t.Fatal("userConfigPath should resolve under isolated HOME")
	}
	if _, err := os.Stat(up); err != nil {
		t.Fatalf("HostID was not persisted to %s: %v", up, err)
	}
}

func TestLoad_HostIDIsStableAcrossRuns(t *testing.T) {
	withIsolatedHome(t)
	first, err := Load()
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	second, err := Load()
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if first.HostID != second.HostID {
		t.Fatalf("HostID drift: %q -> %q", first.HostID, second.HostID)
	}
}

func TestLoad_FromExplicitConfigEnv(t *testing.T) {
	dir := withIsolatedHome(t)
	cfgPath := filepath.Join(dir, "myconfig.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"hostId": "fixture-id",
		"dataDir": "/data",
		"web": { "port": 9090 }
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXAMTOPICS_CONFIG", cfgPath)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HostID != "fixture-id" {
		t.Errorf("HostID = %q, want fixture-id", cfg.HostID)
	}
	if cfg.Web.Port != 9090 {
		t.Errorf("Web.Port = %d, want 9090", cfg.Web.Port)
	}
	if cfg.LoadedFrom != cfgPath {
		t.Errorf("LoadedFrom = %q, want %q", cfg.LoadedFrom, cfgPath)
	}
}

func TestLoad_EnvOverridesJSON(t *testing.T) {
	dir := withIsolatedHome(t)
	cfgPath := filepath.Join(dir, "config.json")
	jsonDir := filepath.Join(dir, "from-json")
	envDir := filepath.Join(dir, "from-env")
	_ = os.WriteFile(cfgPath, []byte(`{
		"hostId": "from-json",
		"dataDir": `+jsonQuote(jsonDir)+`
	}`), 0o600)
	t.Setenv("EXAMTOPICS_CONFIG", cfgPath)
	t.Setenv("EXAMTOPICS_HOST_ID", "from-env")
	t.Setenv("EXAMTOPICS_DATA_DIR", envDir)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HostID != "from-env" {
		t.Errorf("HostID = %q, want from-env", cfg.HostID)
	}
	if cfg.DataDir != envDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, envDir)
	}
}

// jsonQuote is a tiny helper to embed an OS path inside a JSON string
// literal. encoding/json escapes backslashes for us, which matters on
// Windows where filepath.Join produces "\\".
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestLoad_ForbiddenKeysAreStrippedWithWarning(t *testing.T) {
	dir := withIsolatedHome(t)
	cfgPath := filepath.Join(dir, "config.json")
	safeDir := filepath.Join(dir, "safe")
	if err := os.WriteFile(cfgPath, []byte(`{
		"hostId": "with-secret",
		"ghPat": "redacted-fake-value-for-test",
		"adminToken": "redacted-fake-value-for-test",
		"dataDir": `+jsonQuote(safeDir)+`
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXAMTOPICS_CONFIG", cfgPath)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HostID != "with-secret" {
		t.Errorf("HostID = %q, want with-secret", cfg.HostID)
	}
	if cfg.DataDir != safeDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, safeDir)
	}
	// Re-parse the JSON struct to confirm no surprise field.
	raw, _ := json.Marshal(cfg)
	if strings.Contains(string(raw), "ghPat") || strings.Contains(string(raw), "adminToken") {
		t.Errorf("forbidden keys leaked into Config marshal: %s", raw)
	}
}

func TestExpandPath_Tilde(t *testing.T) {
	dir := withIsolatedHome(t)
	got, err := expandPath("~/sub/dir")
	if err != nil {
		t.Fatalf("expandPath: %v", err)
	}
	want := filepath.Join(dir, "sub", "dir")
	if got != want {
		t.Errorf("expandPath = %q, want %q", got, want)
	}
}

func TestExpandPath_Empty(t *testing.T) {
	got, err := expandPath("")
	if err != nil {
		t.Fatalf("expandPath: %v", err)
	}
	if got != "" {
		t.Errorf("expandPath empty = %q, want empty", got)
	}
}

func TestSanitizeHostName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"DESKTOP-WIN", "desktop-win"},
		{"my host 01", "myhost01"},
		{"日本語ホスト", "host"}, // non-ascii stripped → fallback
		{"a_b-c", "a-b-c"},
		{"", "host"},
		{strings.Repeat("a", 100), strings.Repeat("a", 32)},
	}
	for _, c := range cases {
		if got := sanitizeHostName(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoad_HostIDFormat(t *testing.T) {
	withIsolatedHome(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Expect "<sanitized>-<4 hex chars>".
	parts := strings.Split(cfg.HostID, "-")
	if len(parts) < 2 {
		t.Fatalf("HostID %q lacks a suffix", cfg.HostID)
	}
	suffix := parts[len(parts)-1]
	if len(suffix) != 4 {
		t.Fatalf("HostID suffix = %q (len %d), want 4 hex chars", suffix, len(suffix))
	}
	for _, r := range suffix {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			t.Errorf("non-hex char %q in suffix %q", r, suffix)
		}
	}
}

func TestTranslate_DefaultsAreEmpty(t *testing.T) {
	withIsolatedHome(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tools.Translate.Client != "" {
		t.Errorf("default Translate.Client = %q, want empty", cfg.Tools.Translate.Client)
	}
	if cfg.Tools.Translate.Model != "" || cfg.Tools.Translate.Bin != "" {
		t.Errorf("default Translate.{Model,Bin} should be empty: %+v", cfg.Tools.Translate)
	}
	if got := cfg.Tools.Translate.ClientOrDefault(); got != "claude" {
		t.Errorf("ClientOrDefault on empty = %q, want claude", got)
	}
}

func TestTranslate_JSONPopulatesSection(t *testing.T) {
	dir := withIsolatedHome(t)
	cfgPath := filepath.Join(dir, "myconfig.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"tools": {
			"translate": {
				"client": "gemini",
				"model": "gemini-3.1-flash-lite-preview",
				"bin": "/usr/local/bin/gemini"
			}
		}
	}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Setenv("EXAMTOPICS_CONFIG", cfgPath)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tools.Translate.Client != "gemini" {
		t.Errorf("Client = %q, want gemini", cfg.Tools.Translate.Client)
	}
	if cfg.Tools.Translate.Model != "gemini-3.1-flash-lite-preview" {
		t.Errorf("Model = %q", cfg.Tools.Translate.Model)
	}
	if cfg.Tools.Translate.Bin != "/usr/local/bin/gemini" {
		t.Errorf("Bin = %q", cfg.Tools.Translate.Bin)
	}
	if got := cfg.Tools.Translate.ClientOrDefault(); got != "gemini" {
		t.Errorf("ClientOrDefault when set = %q, want gemini", got)
	}
}

func TestTranslate_EnvOverridesJSON(t *testing.T) {
	dir := withIsolatedHome(t)
	cfgPath := filepath.Join(dir, "myconfig.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"tools": { "translate": { "client": "gemini", "model": "g-1" } }
	}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Setenv("EXAMTOPICS_CONFIG", cfgPath)
	t.Setenv("EXAMTOPICS_TRANSLATE_CLIENT", "claude")
	t.Setenv("EXAMTOPICS_TRANSLATE_MODEL", "claude-opus-4-7")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tools.Translate.Client != "claude" {
		t.Errorf("env override Client = %q, want claude", cfg.Tools.Translate.Client)
	}
	if cfg.Tools.Translate.Model != "claude-opus-4-7" {
		t.Errorf("env override Model = %q", cfg.Tools.Translate.Model)
	}
}

func TestUserConfigPath_PerPlatform(t *testing.T) {
	dir := withIsolatedHome(t)
	got := userConfigPath()
	if got == "" {
		t.Fatal("userConfigPath should resolve under isolated HOME")
	}
	if !strings.HasPrefix(got, dir) {
		t.Errorf("userConfigPath %q does not live under isolated HOME %q", got, dir)
	}
	// Smoke-check platform-specific suffix.
	suffix := strings.ToLower(filepath.Join("examtopics", "config.json"))
	if !strings.HasSuffix(strings.ToLower(got), suffix) {
		t.Errorf("userConfigPath %q does not end with examtopics/config.json", got)
	}
}
