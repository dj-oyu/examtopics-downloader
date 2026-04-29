package utils

import (
	"os"
	"path/filepath"
	"testing"
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
	defer os.Unsetenv("PLAIN")
	defer os.Unsetenv("SPACED")
	defer os.Unsetenv("DQUOTED")
	defer os.Unsetenv("SQUOTED")
	defer os.Unsetenv("NO_VALUE")

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
