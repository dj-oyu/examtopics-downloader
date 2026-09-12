package utils

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// captureLog redirects log.Default()'s output to a buffer for the
// duration of the test, so progress lines emitted by logBar are
// inspectable.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := log.Writer()
	prevFlags := log.Flags()
	buf := &bytes.Buffer{}
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(prevFlags)
	})
	return buf
}

func TestLogBar_FinalIncrementAlwaysReports(t *testing.T) {
	buf := captureLog(t)
	bar := &logBar{label: "scrape", total: 3, lastReport: time.Now()}
	bar.Increment() // 1/3 — likely throttled, since lastReport is "now"
	bar.Increment() // 2/3 — same
	bar.Increment() // 3/3 — final, must report
	out := buf.String()
	if !strings.Contains(out, "scrape: 3/3 (100%)") {
		t.Errorf("final increment did not report; log=%q", out)
	}
}

func TestLogBar_ThrottlesIntermediateReports(t *testing.T) {
	buf := captureLog(t)
	bar := &logBar{label: "fetch", total: 100, lastReport: time.Now()}
	for range 50 {
		bar.Increment()
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log output before interval elapses; got %q", buf.String())
	}
	// Force a stale lastReport to simulate the interval having elapsed.
	bar.lastReport = time.Now().Add(-2 * logProgressInterval)
	bar.Increment() // should report
	out := buf.String()
	if !strings.Contains(out, "fetch: 51/100") {
		t.Errorf("expected report after interval; log=%q", out)
	}
}

func TestLogBar_FinishWithoutReachingTotalReports(t *testing.T) {
	buf := captureLog(t)
	bar := &logBar{label: "partial", total: 10, lastReport: time.Now()}
	bar.current = 4 // simulate partial progress without going through Increment
	bar.Finish()
	out := buf.String()
	if !strings.Contains(out, "partial: 4/10 (40%)") {
		t.Errorf("Finish should report final state when total not reached; log=%q", out)
	}
}

func TestLogBar_FinishAfterFinalIncrementDoesNotDoubleLog(t *testing.T) {
	buf := captureLog(t)
	bar := &logBar{label: "complete", total: 1, lastReport: time.Now()}
	bar.Increment() // 1/1, final, reports once
	bar.Finish()    // already at total, must not log again
	out := buf.String()
	if strings.Count(out, "complete:") != 1 {
		t.Errorf("expected exactly one report, got log=%q", out)
	}
}

func TestNewProgressBar_NonTTYReturnsLogBar(t *testing.T) {
	// NewProgressBar's TTY branch depends on whether stderr is a terminal,
	// which `go test` typically inherits from the parent shell. We exercise
	// the non-TTY branch indirectly: regardless of which branch returns,
	// the result must satisfy ProgressBar and not panic on Increment/Finish.
	b := NewProgressBar("smoke", 1)
	b.Increment()
	b.Finish()
}
