package utils

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/mattn/go-isatty"
)

// ProgressBar is the minimal interface the fetch package uses to report
// long-running scrape progress. NewProgressBar returns an animated
// implementation on a TTY (cheggaaa/pb/v3) and a periodic-log
// implementation everywhere else (CI logs, redirected output, IDE run
// panes), keeping the captured output readable in both contexts.
type ProgressBar interface {
	Increment()
	Finish()
}

// logProgressInterval is how often the non-TTY bar emits a status line
// while a long task is running. Final completion (current == total) is
// always logged immediately regardless of this throttle.
const logProgressInterval = 5 * time.Second

// NewProgressBar returns a TTY-or-log progress bar based on whether
// stderr is connected to a terminal. Label is used as the prefix in
// non-TTY log lines so multiple bars in the same run stay distinct.
func NewProgressBar(label string, total int) ProgressBar {
	if isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd()) {
		return &ttyBar{bar: pb.StartNew(total)}
	}
	return &logBar{label: label, total: total, lastReport: time.Now()}
}

type ttyBar struct {
	bar *pb.ProgressBar
}

func (t *ttyBar) Increment() { t.bar.Increment() }
func (t *ttyBar) Finish()    { t.bar.Finish() }

type logBar struct {
	mu         sync.Mutex
	label      string
	total      int
	current    int
	lastReport time.Time
}

func (b *logBar) Increment() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.current++
	if b.current == b.total || time.Since(b.lastReport) >= logProgressInterval {
		b.report()
	}
}

func (b *logBar) Finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.current == b.total {
		// already reported by Increment
		return
	}
	b.report()
}

func (b *logBar) report() {
	pct := 0.0
	if b.total > 0 {
		pct = 100 * float64(b.current) / float64(b.total)
	}
	log.Printf("%s: %d/%d (%.0f%%)", b.label, b.current, b.total, pct)
	b.lastReport = time.Now()
}
