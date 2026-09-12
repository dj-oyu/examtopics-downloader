package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"examtopics-downloader/internal/config"
	"examtopics-downloader/internal/sqlite"
	syncpkg "examtopics-downloader/internal/sync"
	"examtopics-downloader/internal/utils"
)

// syncOpenOpts builds the sqlite.OpenOpts used by sync subcommands that
// open a destination DB. Loading config gives us a stable hostID to
// stamp into rows preserved across migration 003; the .pre-003.bak
// snapshot is requested so a v2 → v3 transition leaves an intact copy
// behind. A config-load error degrades to the empty-opts default
// rather than aborting the merge — sync against a fresh DB shouldn't
// hard-require a config file.
func syncOpenOpts() sqlite.OpenOpts {
	cfg, err := config.Load()
	if err != nil {
		return sqlite.OpenOpts{Backup: true}
	}
	return sqlite.OpenOpts{HostID: cfg.HostID, Backup: true}
}

// runSync implements `examtopicsdl sync <snapshot|merge|content>`. The
// outer subcommand routes the second positional word to one of three
// runners; each runner owns its own FlagSet so flag definitions don't
// leak across modes.
func runSync(args []string) int {
	return runSyncTo(os.Stdout, args)
}

func runSyncTo(out io.Writer, args []string) int {
	w := utils.NewWriteErr(out)
	if len(args) == 0 {
		w.Println("sync: subcommand required (snapshot | merge | content)")
		return 2
	}
	head, rest := args[0], args[1:]
	switch head {
	case "snapshot":
		return runSyncSnapshot(out, rest)
	case "merge":
		return runSyncMerge(out, rest)
	case "content":
		return runSyncContent(out, rest)
	default:
		w.Printf("sync: unknown subcommand %q\n", head)
		return 2
	}
}

func runSyncSnapshot(out io.Writer, args []string) int {
	w := utils.NewWriteErr(out)
	fs := flag.NewFlagSet("sync snapshot", flag.ContinueOnError)
	fs.SetOutput(out)
	src := fs.String("d", "", "Source DB path (required)")
	dst := fs.String("o", "", "Output snapshot path (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *src == "" || *dst == "" {
		w.Println("sync snapshot: -d and -o are required")
		return 2
	}
	if err := syncpkg.Snapshot(*src, *dst); err != nil {
		fmt.Fprintf(os.Stderr, "sync snapshot: %v\n", err)
		return 1
	}
	w.Printf("snapshot written: %s -> %s\n", *src, *dst)
	return 0
}

func runSyncMerge(out io.Writer, args []string) int {
	w := utils.NewWriteErr(out)
	fs := flag.NewFlagSet("sync merge", flag.ContinueOnError)
	fs.SetOutput(out)
	dst := fs.String("d", "", "Destination DB to merge into (required)")
	from := fs.String("from", "", "Peer DB to merge from (required)")
	skipTime := fs.Bool("skip-time-check", false, "Skip the system clock synchronization probe")
	allowWAL := fs.Bool("allow-wal", false, "Allow merging from a peer DB that still has a populated -wal sidecar")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dst == "" || *from == "" {
		w.Println("sync merge: -d and --from are required")
		return 2
	}
	if err := (syncpkg.Preflight{
		PeerPath:     *from,
		SkipTimeSync: *skipTime,
		SkipWALCheck: *allowWAL,
		Stderr:       os.Stderr,
	}).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "sync merge: %v\n", err)
		return 1
	}
	db, err := sqlite.OpenWith(*dst, syncOpenOpts())
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync merge: open %s: %v\n", *dst, err)
		return 1
	}
	defer func() { _ = db.Close() }()
	if err := syncpkg.Merge(db, *from); err != nil {
		fmt.Fprintf(os.Stderr, "sync merge: %v\n", err)
		return 1
	}
	w.Printf("merged peer rows: %s <- %s\n", *dst, *from)
	return 0
}

func runSyncContent(out io.Writer, args []string) int {
	w := utils.NewWriteErr(out)
	fs := flag.NewFlagSet("sync content", flag.ContinueOnError)
	fs.SetOutput(out)
	dst := fs.String("d", "", "Destination DB to overwrite (required)")
	from := fs.String("from", "", "Master DB whose content to copy (required)")
	skipTime := fs.Bool("skip-time-check", false, "Skip the system clock synchronization probe")
	allowWAL := fs.Bool("allow-wal", false, "Allow ingesting a master DB that still has a populated -wal sidecar")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dst == "" || *from == "" {
		w.Println("sync content: -d and --from are required")
		return 2
	}
	if err := (syncpkg.Preflight{
		PeerPath:     *from,
		SkipTimeSync: *skipTime,
		SkipWALCheck: *allowWAL,
		Stderr:       os.Stderr,
	}).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "sync content: %v\n", err)
		return 1
	}
	db, err := sqlite.OpenWith(*dst, syncOpenOpts())
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync content: open %s: %v\n", *dst, err)
		return 1
	}
	defer func() { _ = db.Close() }()
	if err := syncpkg.ContentSync(db, *from); err != nil {
		fmt.Fprintf(os.Stderr, "sync content: %v\n", err)
		return 1
	}
	w.Printf("content replaced: %s <- %s\n", *dst, *from)
	return 0
}
