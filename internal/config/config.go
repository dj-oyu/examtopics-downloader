// Package config loads runtime configuration from a single config.json
// resolved through the search order defined in docs/plans/portable-builds.md
// §3.5, applies process-environment overrides, and lazily generates the
// machine-local hostId required by the multi-host sync design (§3.7.3).
//
// Secrets (PATs, admin tokens) are intentionally absent from the schema
// — they live in .env / process env so the JSON file stays safe to share
// or commit accidentally. Forbidden keys are detected and ignored with
// a warning to stderr.
package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config is the parsed shape of config.json plus computed fields. It is
// safe to read from multiple goroutines after Load returns.
type Config struct {
	HostID        string `json:"hostId"`
	DataDir       string `json:"dataDir"`
	LogDir        string `json:"logDir"`
	DownloaderBin string `json:"downloaderBin"`
	Web           struct {
		Host         string `json:"host"`
		Port         int    `json:"port"`
		AdminEnabled bool   `json:"adminEnabled"`
	} `json:"web"`
	Scrape struct {
		DefaultProvider string `json:"defaultProvider"`
		NoCache         bool   `json:"noCache"`
	} `json:"scrape"`
	Tools struct {
		Translate TranslateSection `json:"translate"`
	} `json:"tools"`

	// LoadedFrom is the absolute path of the JSON file that contributed
	// values, or "" if no config.json was found and defaults are in use.
	LoadedFrom string `json:"-"`
}

// TranslateSection drives `examtopicsdl translate retranslate / explain`
// adapter selection without recompile or per-call flags. Empty fields
// fall back to the CLI's hard-coded defaults (see ClientOrDefault) so
// users can opt in incrementally.
type TranslateSection struct {
	// Client is the LLM CLI to spawn ("claude" | "gemini" | "codex" |
	// "exec"). Empty defers to the binary's flag default.
	Client string `json:"client"`
	// Model is passed to the chosen client as its native model flag
	// (e.g. claude --model). Empty leaves the client's own default.
	Model string `json:"model"`
	// Bin overrides the path to the chosen client's executable.
	// Empty falls through to the per-client env (CLAUDE_BIN, etc.)
	// and finally PATH lookup.
	Bin string `json:"bin"`
}

// ClientOrDefault returns the configured client or the binary-wide
// default ("claude") so flag-default and adapter dispatch see one
// canonical answer.
func (t TranslateSection) ClientOrDefault() string {
	if t.Client == "" {
		return "claude"
	}
	return t.Client
}

// forbiddenKeys must never appear in config.json. They are inspected at
// parse time and stripped with a stderr warning so an accidentally
// committed config.json does not leak credentials.
var forbiddenKeys = []string{
	"ghPat", "GH_PAT", "token", "Token",
	"adminToken", "EXAMTOPICS_ADMIN_TOKEN",
	"apiKey", "secret",
}

// Defaults returns a Config populated with hard-coded defaults.
func Defaults() *Config {
	cfg := &Config{}
	cfg.Web.Host = "127.0.0.1"
	cfg.Web.Port = 8787
	cfg.Web.AdminEnabled = true
	cfg.Scrape.DefaultProvider = "amazon"
	cfg.Scrape.NoCache = false
	return cfg
}

// Load resolves config.json from the search paths, applies env overrides,
// expands ~ in path fields, and ensures HostID is set (generating and
// persisting one on first use). Missing config.json is not an error —
// callers can still operate on defaults.
func Load() (*Config, error) {
	cfg := Defaults()
	for _, p := range candidatePaths() {
		err := tryLoad(p, cfg)
		if err == nil {
			cfg.LoadedFrom = p
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("load %s: %w", p, err)
		}
	}
	cfg.applyEnvOverrides()
	if err := cfg.expandPaths(); err != nil {
		return nil, err
	}
	if cfg.HostID == "" {
		if err := cfg.ensureHostID(); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// candidatePaths returns config.json search locations in priority order
// (highest first). The first existing file wins.
func candidatePaths() []string {
	var out []string
	if v := os.Getenv("EXAMTOPICS_CONFIG"); v != "" {
		out = append(out, v)
	}
	if cwd, err := os.Getwd(); err == nil {
		out = append(out, filepath.Join(cwd, "config.json"))
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), "config.json"))
	}
	if up := userConfigPath(); up != "" {
		out = append(out, up)
	}
	return out
}

// UserConfigDir returns the platform-conventional per-user config
// directory for examtopics (e.g., %APPDATA%\examtopics on Windows,
// $XDG_CONFIG_HOME/examtopics or ~/.config/examtopics elsewhere).
// Empty string means we couldn't determine a sensible default (no
// HOME, no APPDATA, no XDG_CONFIG_HOME). Callers append their own
// filename — config.json here, .env in internal/utils.
func UserConfigDir() string {
	if runtime.GOOS == "windows" {
		if v := os.Getenv("APPDATA"); v != "" {
			return filepath.Join(v, "examtopics")
		}
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "examtopics")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "examtopics")
}

// userConfigPath returns the per-user config.json location, or "" if
// no such path can be determined.
func userConfigPath() string {
	dir := UserConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.json")
}

// tryLoad reads path, strips forbidden keys (with a stderr warning), and
// merges the remaining values into cfg.
func tryLoad(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cleaned, err := stripForbidden(data, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(cleaned, cfg); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	return nil
}

// stripForbidden parses data into a generic map, removes any forbidden
// top-level keys with a warning, and returns the cleaned JSON bytes.
// If parsing fails outright, it returns the original bytes unchanged so
// the upstream Unmarshal error message stays useful.
func stripForbidden(data []byte, source string) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data, nil
	}
	dirty := false
	for _, k := range forbiddenKeys {
		if _, found := raw[k]; found {
			fmt.Fprintf(os.Stderr,
				"[config] warning: key %q in %s is not allowed — use .env or environment variable instead. Ignoring.\n",
				k, source)
			delete(raw, k)
			dirty = true
		}
	}
	if !dirty {
		return data, nil
	}
	return json.Marshal(raw)
}

// applyEnvOverrides lets process env vars override JSON values. We only
// wire the variables that have an existing user-facing contract today;
// further fields will be added as the CLI/web grow new config knobs.
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("EXAMTOPICS_HOST_ID"); v != "" {
		c.HostID = v
	}
	if v := os.Getenv("EXAMTOPICS_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("EXAMTOPICS_LOG_DIR"); v != "" {
		c.LogDir = v
	}
	if v := os.Getenv("EXAMTOPICS_DOWNLOADER_BIN"); v != "" {
		c.DownloaderBin = v
	}
	if v := os.Getenv("EXAMTOPICS_TRANSLATE_CLIENT"); v != "" {
		c.Tools.Translate.Client = v
	}
	if v := os.Getenv("EXAMTOPICS_TRANSLATE_MODEL"); v != "" {
		c.Tools.Translate.Model = v
	}
	if v := os.Getenv("EXAMTOPICS_TRANSLATE_BIN"); v != "" {
		c.Tools.Translate.Bin = v
	}
}

// expandPaths replaces a leading "~" in path fields with the user's home
// directory and turns relative paths into absolute ones.
func (c *Config) expandPaths() error {
	var err error
	if c.DataDir, err = expandPath(c.DataDir); err != nil {
		return err
	}
	if c.LogDir, err = expandPath(c.LogDir); err != nil {
		return err
	}
	if c.DownloaderBin, err = expandPath(c.DownloaderBin); err != nil {
		return err
	}
	return nil
}

func expandPath(s string) (string, error) {
	if s == "" {
		return s, nil
	}
	if s == "~" || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return s, err
		}
		s = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(s, "~/"), "~"))
	}
	if filepath.IsAbs(s) {
		return s, nil
	}
	return filepath.Abs(s)
}

// ensureHostID synthesizes a host id and writes it back to the source
// config (or to the user-config path when none was loaded). The format
// is "<sanitized hostname>-<4 hex chars>", stable across restarts once
// written.
func (c *Config) ensureHostID() error {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "host"
	}
	host = sanitizeHostName(host)
	var rnd [2]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("generate random host suffix: %w", err)
	}
	c.HostID = fmt.Sprintf("%s-%02x%02x", host, rnd[0], rnd[1])
	return c.persistHostID()
}

// persistHostID writes hostId into the loaded config file when one
// exists, or creates a new minimal config.json at the user-config
// location otherwise. It preserves any other top-level keys present.
func (c *Config) persistHostID() error {
	target := c.LoadedFrom
	if target == "" {
		target = userConfigPath()
	}
	if target == "" {
		// No persistable location (no HOME, no APPDATA): keep the in-memory
		// id but warn — successive runs will get different ids.
		fmt.Fprintf(os.Stderr,
			"[config] warning: generated hostId %q but no writable config path; this id will not persist across restarts.\n",
			c.HostID)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
	}
	raw := map[string]any{}
	if data, err := os.ReadFile(target); err == nil {
		_ = json.Unmarshal(data, &raw)
	}
	raw["hostId"] = c.HostID
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if err := os.WriteFile(target, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	fmt.Fprintf(os.Stderr, "[config] generated hostId %q and wrote it to %s\n", c.HostID, target)
	if c.LoadedFrom == "" {
		c.LoadedFrom = target
	}
	return nil
}

// sanitizeHostName lowercases and strips characters unsafe for ids.
// Empty result falls back to "host" so downstream id construction always
// produces a non-empty prefix.
func sanitizeHostName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		return "host"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
