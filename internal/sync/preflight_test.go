package sync

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflight_PassesWhenAllChecksGreen(t *testing.T) {
	dir := t.TempDir()
	peer := filepath.Join(dir, "peer.db")
	if err := os.WriteFile(peer, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write peer: %v", err)
	}
	pf := Preflight{
		PeerPath:   peer,
		TimeSyncFn: func() error { return nil },
		WALFn:      func(_ string) error { return nil },
	}
	if err := pf.Run(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestPreflight_TimeSyncHardErrorAborts(t *testing.T) {
	pf := Preflight{
		TimeSyncFn: func() error { return errors.New("clock dead") },
	}
	err := pf.Run()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "--skip-time-check") {
		t.Errorf("error message should mention --skip-time-check; got %v", err)
	}
}

func TestPreflight_TimeSyncSoftWarnContinues(t *testing.T) {
	var buf bytes.Buffer
	pf := Preflight{
		Stderr:     &buf,
		TimeSyncFn: func() error { return &SoftError{Msg: "macOS not implemented"} },
	}
	if err := pf.Run(); err != nil {
		t.Fatalf("soft warn should not abort, got %v", err)
	}
	if !strings.Contains(buf.String(), "warn") {
		t.Errorf("expected stderr warn, got %q", buf.String())
	}
}

func TestPreflight_SkipTimeSyncBypassesProbe(t *testing.T) {
	pf := Preflight{
		SkipTimeSync: true,
		TimeSyncFn:   func() error { return errors.New("would fail") },
	}
	if err := pf.Run(); err != nil {
		t.Fatalf("expected nil with SkipTimeSync, got %v", err)
	}
}

func TestPreflight_WALCheckSkippedWhenNoPeerPath(t *testing.T) {
	pf := Preflight{
		TimeSyncFn: func() error { return nil },
		WALFn:      func(_ string) error { t.Fatal("WALFn should not be called when PeerPath empty"); return nil },
	}
	if err := pf.Run(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestPreflight_WALErrorAborts(t *testing.T) {
	pf := Preflight{
		PeerPath:   "/some/peer.db",
		TimeSyncFn: func() error { return nil },
		WALFn:      func(_ string) error { return errors.New("peer.db-wal exists") },
	}
	err := pf.Run()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "--allow-wal") {
		t.Errorf("error should mention --allow-wal; got %v", err)
	}
	if !strings.Contains(err.Error(), "sync snapshot") {
		t.Errorf("error should hint at sync snapshot; got %v", err)
	}
}

func TestCheckWALCleanliness_NoSidecarIsClean(t *testing.T) {
	dir := t.TempDir()
	peer := filepath.Join(dir, "peer.db")
	if err := os.WriteFile(peer, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write peer: %v", err)
	}
	if err := CheckWALCleanliness(peer); err != nil {
		t.Errorf("expected clean, got %v", err)
	}
}

func TestCheckWALCleanliness_NonEmptyWALFlagsAbort(t *testing.T) {
	dir := t.TempDir()
	peer := filepath.Join(dir, "peer.db")
	if err := os.WriteFile(peer, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write peer: %v", err)
	}
	if err := os.WriteFile(peer+"-wal", []byte("uncheckpointed pages"), 0o644); err != nil {
		t.Fatalf("write wal: %v", err)
	}
	err := CheckWALCleanliness(peer)
	if err == nil {
		t.Fatal("expected WAL detection to flag uncheckpointed sidecar")
	}
	if !strings.Contains(err.Error(), "-wal") {
		t.Errorf("error should mention -wal; got %v", err)
	}
}

func TestCheckWALCleanliness_EmptyWALIsTolerated(t *testing.T) {
	// An empty -wal next to the .db can survive a clean shutdown of a
	// WAL-mode SQLite connection. We don't want to flag that as a
	// hazard — only non-zero size means uncheckpointed pages.
	dir := t.TempDir()
	peer := filepath.Join(dir, "peer.db")
	if err := os.WriteFile(peer, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write peer: %v", err)
	}
	if err := os.WriteFile(peer+"-wal", nil, 0o644); err != nil {
		t.Fatalf("write empty wal: %v", err)
	}
	if err := CheckWALCleanliness(peer); err != nil {
		t.Errorf("empty WAL should be OK, got %v", err)
	}
}

func TestSoftError_IsErrorsAsCompatible(t *testing.T) {
	var soft *SoftError
	err := error(&SoftError{Msg: "x"})
	if !errors.As(err, &soft) {
		t.Fatal("errors.As(*SoftError) failed")
	}
	if soft.Msg != "x" {
		t.Errorf("Msg = %q", soft.Msg)
	}
}
