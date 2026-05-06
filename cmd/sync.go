package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"examtopics-downloader/internal/sqlite"
	syncpkg "examtopics-downloader/internal/sync"
	"examtopics-downloader/internal/utils"
)

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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dst == "" || *from == "" {
		w.Println("sync merge: -d and --from are required")
		return 2
	}
	db, err := sqlite.Open(*dst)
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dst == "" || *from == "" {
		w.Println("sync content: -d and --from are required")
		return 2
	}
	db, err := sqlite.Open(*dst)
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
