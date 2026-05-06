package utils

import (
	"os"
	"path/filepath"
	"testing"

	"examtopics-downloader/internal/config"
)

func TestLoadDotEnv_BasicAndQuotes(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(
		"# comment\n"+
			"\n"+
			"PLAIN=alpha\n"+
			"  SPACED  =  beta  \n"+
			"DQUOTED=\"gamma=zeta\"\n"+
			"SQUOTED='delta'\n"+
			"NO_VALUE=\n"+
			"=missing_key\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, k := range []string{"PLAIN", "SPACED", "DQUOTED", "SQUOTED", "NO_VALUE"} {
			_ = os.Unsetenv(k)
		}
	})

	if err := LoadDotEnv(envPath); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"PLAIN":    "alpha",
		"SPACED":   "beta",
		"DQUOTED":  "gamma=zeta",
		"SQUOTED":  "delta",
		"NO_VALUE": "",
	}
	for k, want := range cases {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s: got %q want %q", k, got, want)
		}
	}
	if got := os.Getenv(""); got != "" {
		t.Errorf("empty key should not be set, got %q", got)
	}
}

func TestLoadDotEnv_PreExistingWins(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("GH_PAT=from_dotenv\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_PAT", "from_shell")
	if err := LoadDotEnv(envPath); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GH_PAT"); got != "from_shell" {
		t.Errorf("explicit env should win: got %q", got)
	}
}

func TestLoadDotEnv_MissingFileNoError(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Errorf("missing file should be silent, got %v", err)
	}
}

// withDotEnvAutoIsolation isolates HOME / APPDATA / XDG_CONFIG_HOME and
// EXAMTOPICS_ENV_FILE to the supplied temp dir, and chdir's into it so
// LoadDotEnvAuto won't accidentally touch the developer's real .env.
func withDotEnvAutoIsolation(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("APPDATA", filepath.Join(dir, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("EXAMTOPICS_ENV_FILE", "")
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}
	return dir
}

func TestLoadDotEnvAuto_NoFiles_NoError(t *testing.T) {
	withDotEnvAutoIsolation(t)
	if err := LoadDotEnvAuto(); err != nil {
		t.Fatalf("LoadDotEnvAuto with no files should not error: %v", err)
	}
}

func TestLoadDotEnvAuto_ExplicitPathWinsOverCwd(t *testing.T) {
	dir := withDotEnvAutoIsolation(t)
	cwdEnv := filepath.Join(dir, ".env")
	explicit := filepath.Join(dir, "explicit.env")
	if err := os.WriteFile(cwdEnv, []byte("AUTO_KEY=from_cwd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(explicit, []byte("AUTO_KEY=from_explicit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXAMTOPICS_ENV_FILE", explicit)
	t.Cleanup(func() { _ = os.Unsetenv("AUTO_KEY") })
	if err := LoadDotEnvAuto(); err != nil {
		t.Fatalf("LoadDotEnvAuto: %v", err)
	}
	if got := os.Getenv("AUTO_KEY"); got != "from_explicit" {
		t.Errorf("AUTO_KEY = %q, want from_explicit (explicit path is higher priority than cwd)", got)
	}
}

func TestLoadDotEnvAuto_CwdWinsOverUserDir(t *testing.T) {
	dir := withDotEnvAutoIsolation(t)
	cwdEnv := filepath.Join(dir, ".env")
	userDir := config.UserConfigDir()
	if userDir == "" {
		t.Skip("UserConfigDir unresolvable on this platform/env")
	}
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	userEnv := filepath.Join(userDir, ".env")
	if err := os.WriteFile(cwdEnv, []byte("AUTO_KEY=from_cwd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userEnv, []byte("AUTO_KEY=from_user\nUSER_ONLY=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("AUTO_KEY")
		_ = os.Unsetenv("USER_ONLY")
	})
	if err := LoadDotEnvAuto(); err != nil {
		t.Fatalf("LoadDotEnvAuto: %v", err)
	}
	if got := os.Getenv("AUTO_KEY"); got != "from_cwd" {
		t.Errorf("AUTO_KEY = %q, want from_cwd (cwd outranks user dir)", got)
	}
	if got := os.Getenv("USER_ONLY"); got != "yes" {
		t.Errorf("USER_ONLY = %q, want yes (lower-priority files still fill missing keys)", got)
	}
}

func TestLoadDotEnvAuto_PreExistingEnvWinsOverAllFiles(t *testing.T) {
	dir := withDotEnvAutoIsolation(t)
	cwdEnv := filepath.Join(dir, ".env")
	if err := os.WriteFile(cwdEnv, []byte("AUTO_KEY=from_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTO_KEY", "from_shell")
	if err := LoadDotEnvAuto(); err != nil {
		t.Fatalf("LoadDotEnvAuto: %v", err)
	}
	if got := os.Getenv("AUTO_KEY"); got != "from_shell" {
		t.Errorf("AUTO_KEY = %q, want from_shell (process env beats every .env)", got)
	}
}
