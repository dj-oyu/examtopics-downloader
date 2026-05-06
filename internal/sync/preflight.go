package sync

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"time"
)

// Preflight bundles the safety checks that should fire before merge /
// content sync touches the destination DB. The two probes — system
// clock synchronization and peer-file WAL cleanliness — protect
// against the two distinct ways a multi-host sync can lose data:
//
//   - LWW on explanation_threads.updated_at trusts the local clock.
//     If a peer's clock has skewed backwards (NTP failure, manual
//     adjustment), newer remote updates can be silently classified
//     as older and discarded. Refusing to merge until the system
//     reports synchronized closes that hole.
//
//   - SQLite WAL-mode databases keep their most recent commits in a
//     -wal sidecar that may not have been checkpointed when the
//     file was scp'd. Merging a peer DB that still has a populated
//     -wal next to it can produce missing rows or FK violations.
//     `examtopicsdl sync snapshot` (VACUUM INTO) is the fix; the
//     check warns the operator if they skipped that step.
//
// Both checks have an opt-out so an offline / emergency run can
// proceed. The opt-outs are surfaced as CLI flags (`--skip-time-check`,
// `--allow-wal`) on the merge / content subcommands.
type Preflight struct {
	PeerPath     string
	SkipTimeSync bool
	SkipWALCheck bool
	// Stderr receives soft warnings (e.g. unsupported platform). Hard
	// errors are returned, never written here.
	Stderr io.Writer
	// Injectable hooks for tests.
	TimeSyncFn func() error
	WALFn      func(path string) error
}

func (p Preflight) Run() error {
	stderr := p.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	if !p.SkipTimeSync {
		fn := p.TimeSyncFn
		if fn == nil {
			fn = CheckTimeSync
		}
		if err := fn(); err != nil {
			var soft *SoftError
			if errors.As(err, &soft) {
				fmt.Fprintf(stderr, "[sync] warn: %v (continuing)\n", err)
			} else {
				return fmt.Errorf("clock not synchronized: %w (override with --skip-time-check)", err)
			}
		}
	}
	if p.PeerPath != "" && !p.SkipWALCheck {
		fn := p.WALFn
		if fn == nil {
			fn = CheckWALCleanliness
		}
		if err := fn(p.PeerPath); err != nil {
			return fmt.Errorf("peer DB has stale WAL: %w (run `examtopicsdl sync snapshot` on the source first, or pass --allow-wal to bypass)", err)
		}
	}
	return nil
}

// SoftError is returned by probes that couldn't make a definitive
// statement about the underlying system (unsupported OS, missing
// command). Callers may log and continue.
type SoftError struct{ Msg string }

func (e *SoftError) Error() string { return e.Msg }

// CheckTimeSync probes the platform's time-sync stack and returns
// nil iff the system reports a healthy synchronized clock. Linux
// reads `timedatectl status`; Windows reads `w32tm /query /status`;
// other platforms produce a SoftError so the merge can proceed with
// a warning rather than failing.
func CheckTimeSync() error {
	switch runtime.GOOS {
	case "linux":
		return checkTimedatectl()
	case "windows":
		return checkW32tm()
	default:
		return &SoftError{Msg: fmt.Sprintf("time-sync probe not implemented for %s — verify manually before sync", runtime.GOOS)}
	}
}

// timedatectlSyncedRe matches the "System clock synchronized: yes"
// row regardless of trailing whitespace differences across distros.
var timedatectlSyncedRe = regexp.MustCompile(`(?im)^\s*System clock synchronized:\s*yes\s*$`)

func checkTimedatectl() error {
	out, err := exec.Command("timedatectl", "status").Output()
	if err != nil {
		return &SoftError{Msg: fmt.Sprintf("timedatectl not available: %v — install systemd-timesyncd or skip with --skip-time-check", err)}
	}
	if !timedatectlSyncedRe.Match(out) {
		return errors.New("timedatectl reports the system clock is not synchronized")
	}
	return nil
}

// w32tmDateRe matches the last-successful-sync timestamp in
// `w32tm /query /status` regardless of OS display language. Windows
// localizes the row label (`Last Successful Sync Time:` → `最終正常
// 同期時刻:` etc.) but the date format itself stays YYYY/MM/DD
// HH:MM:SS. Falling back to a date pattern also avoids the Stratum
// route, which is locale-dependent and `w32tm /query /source` (the
// other obvious option) requires elevation.
var w32tmDateRe = regexp.MustCompile(`(\d{4})/(\d{1,2})/(\d{1,2})\s+(\d{1,2}):(\d{2}):(\d{2})`)

// w32tmMaxAge is the longest the last sync may be before we treat
// the host as a sync risk. A week is generous — typical desktops
// poll every few hours — but small enough to catch a desktop that
// stopped syncing weeks ago and accumulated visible drift.
const w32tmMaxAge = 7 * 24 * time.Hour

func checkW32tm() error {
	out, err := exec.Command("w32tm", "/query", "/status").CombinedOutput()
	if err != nil {
		return &SoftError{Msg: fmt.Sprintf("w32tm not available: %v — verify time sync manually before sync", err)}
	}
	m := w32tmDateRe.FindSubmatch(out)
	if m == nil {
		return &SoftError{Msg: "w32tm output did not include a parseable last-sync timestamp — verify time sync manually"}
	}
	parsed, err := time.ParseInLocation(
		"2006/1/2 15:04:05",
		fmt.Sprintf("%s/%s/%s %s:%s:%s", m[1], m[2], m[3], m[4], m[5], m[6]),
		time.Local,
	)
	if err != nil {
		return &SoftError{Msg: fmt.Sprintf("could not parse w32tm sync date: %v", err)}
	}
	age := time.Since(parsed)
	if age > w32tmMaxAge {
		return fmt.Errorf("last w32tm sync was %s ago (threshold %s) — clock drift may have accumulated",
			age.Round(time.Minute), w32tmMaxAge)
	}
	return nil
}

// CheckWALCleanliness inspects path's -wal and -shm sidecars and
// returns an error if either is present and non-empty. A live
// SQLite process keeps the WAL populated mid-transaction; a copy
// of the .db file alone (e.g., naive `scp file.db`) loses those
// uncheckpointed pages and may produce FK-violating merges.
//
// `examtopicsdl sync snapshot` uses VACUUM INTO and emits a fresh
// single-file copy with no sidecars, so a snapshot output passes
// this check by construction.
func CheckWALCleanliness(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		p := path + suffix
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.Size() > 0 {
			return fmt.Errorf("%s exists (%d bytes); the source DB has uncheckpointed pages", p, st.Size())
		}
	}
	return nil
}
