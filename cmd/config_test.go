package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// withIsolatedConfigEnv neutralizes HOME / APPDATA / XDG_CONFIG_HOME
// and cwd so config.Load doesn't read or write the developer's real
// config when a test does not supply EXAMTOPICS_CONFIG explicitly.
func withIsolatedConfigEnv(t *testing.T) string {
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
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}
	return dir
}

func TestRunConfigTo_OutputsValidJSON(t *testing.T) {
	dir := withIsolatedConfigEnv(t)
	cfgPath := filepath.Join(dir, "myconfig.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"hostId": "config-test-host",
		"dataDir": "`+filepath.ToSlash(filepath.Join(dir, "data"))+`",
		"web": {"port": 9090}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXAMTOPICS_CONFIG", cfgPath)

	var buf bytes.Buffer
	exit := runConfigTo(&buf, nil)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if got["hostId"] != "config-test-host" {
		t.Errorf("hostId = %v, want config-test-host", got["hostId"])
	}
	if got["loadedFrom"] != cfgPath {
		t.Errorf("loadedFrom = %v, want %s", got["loadedFrom"], cfgPath)
	}
	web, ok := got["web"].(map[string]any)
	if !ok {
		t.Fatalf("web is not a map: %v", got["web"])
	}
	if web["port"] != float64(9090) {
		t.Errorf("web.port = %v, want 9090", web["port"])
	}
}

func TestRunConfigTo_NoConfigFileStillSucceeds(t *testing.T) {
	withIsolatedConfigEnv(t)
	var buf bytes.Buffer
	exit := runConfigTo(&buf, nil)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0 (config.json absence is not an error)", exit)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got["hostId"] == "" || got["hostId"] == nil {
		t.Errorf("hostId should be auto-generated when no config exists, got %v", got["hostId"])
	}
}
