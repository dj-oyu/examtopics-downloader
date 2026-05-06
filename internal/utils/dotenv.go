package utils

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"examtopics-downloader/internal/config"
)

// LoadDotEnv reads KEY=VALUE pairs from path and sets each in the process
// environment via os.Setenv, but only when the key is not already set so an
// explicit `GH_PAT=... go run ...` always wins. Lines starting with '#' and
// blank lines are skipped. Surrounding double or single quotes around the
// value are stripped. Missing file is not an error — callers should treat
// .env as best-effort.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 {
			first, last := val[0], val[len(val)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return err
		}
	}
	return sc.Err()
}

// LoadDotEnvAuto walks the .env search order from
// docs/plans/portable-builds.md §3.5 and merges each existing file's
// keys into the process environment. Higher-priority paths come first;
// because LoadDotEnv leaves already-set keys alone, the first file to
// define a key wins and subsequent files only fill in missing keys.
//
// Search order:
//  1. EXAMTOPICS_ENV_FILE (explicit override)
//  2. <cwd>/.env
//  3. <bin dir>/.env
//  4. <user config dir>/.env (XDG_CONFIG_HOME / APPDATA)
//
// Missing files are silently skipped so the function works in
// environments where only some of these locations exist.
func LoadDotEnvAuto() error {
	for _, p := range dotEnvCandidatePaths() {
		if err := LoadDotEnv(p); err != nil {
			return fmt.Errorf("load %s: %w", p, err)
		}
	}
	return nil
}

// dotEnvCandidatePaths returns .env search locations in priority order
// (highest first). Mirrors config.candidatePaths but scoped to the
// secrets file, sharing the user-config dir resolution via
// config.UserConfigDir so the two paths stay co-located by design.
func dotEnvCandidatePaths() []string {
	var out []string
	if v := os.Getenv("EXAMTOPICS_ENV_FILE"); v != "" {
		out = append(out, v)
	}
	if cwd, err := os.Getwd(); err == nil {
		out = append(out, filepath.Join(cwd, ".env"))
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), ".env"))
	}
	if dir := config.UserConfigDir(); dir != "" {
		out = append(out, filepath.Join(dir, ".env"))
	}
	return out
}
