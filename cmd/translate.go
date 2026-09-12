package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"examtopics-downloader/internal/config"
	"examtopics-downloader/internal/sqlite"
	"examtopics-downloader/internal/translate"
	"examtopics-downloader/internal/utils"
)

// runTranslate implements `examtopicsdl translate`. The bare form
// materializes the portable exam-translator skill into the layout the
// chosen LLM CLI expects (§3.6 of docs/plans/portable-builds.md). A
// `retranslate` sub-subcommand drives the single-question retranslate
// flow end-to-end: read row → spawn adapter → write back, replacing
// the historical web-spawns-claude-spawns-translate.py path.
func runTranslate(args []string) int {
	return runTranslateTo(os.Stdout, args)
}

func runTranslateTo(out io.Writer, args []string) int {
	if len(args) > 0 && args[0] == "retranslate" {
		return runRetranslateTo(out, args[1:])
	}
	if len(args) > 0 && args[0] == "explain" {
		return runExplainSubTo(out, args[1:])
	}
	return runTranslateMaterialize(out, args)
}

func runTranslateMaterialize(out io.Writer, args []string) int {
	w := utils.NewWriteErr(out)
	fs := flag.NewFlagSet("translate", flag.ContinueOnError)
	fs.SetOutput(out)
	clientName := fs.String("client", "claude", "LLM CLI client (claude | codex | exec)")
	root := fs.String("root", ".", "Workspace root where the skill will be materialized")
	dryRun := fs.Bool("dry-run", false, "Materialize the skill but do not invoke the client")
	listClients := fs.Bool("list-clients", false, "Print supported clients and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *listClients {
		for _, c := range translate.KnownClients() {
			w.Printf("%-7s %s\n", c.Name, c.SkillRelPath)
		}
		return 0
	}

	c, ok := translate.ClientByName(*clientName)
	if !ok {
		w.Printf("translate: unknown client %q (try -list-clients)\n", *clientName)
		return 2
	}
	written, err := translate.Materialize(c, *root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "translate: materialize: %v\n", err)
		return 1
	}
	w.Printf("materialized %s skill: %s\n", c.Name, written)

	if *dryRun {
		return 0
	}
	w.Printf("(spawn-client step not yet implemented for the bulk flow; use 'translate retranslate' for single-question retranslate)\n")
	return 0
}

// runRetranslateTo handles `examtopicsdl translate retranslate -db
// <path> -qid <id> [-client <name>] [-model <model>]`. The client
// must be one of the adapters AdapterFor knows about; today only
// -client claude wires through to a real CLI, but the others
// (-client codex / exec) parse and dispatch so end-to-end
// smoke tests catch a missing adapter immediately.
//
// Both -client and -model default to the values in config.json's
// tools.translate section so users don't have to retype them on every
// invocation. Explicit flags still win over config when present.
func runRetranslateTo(out io.Writer, args []string) int {
	w := utils.NewWriteErr(out)
	cfg, err := config.Load()
	if err != nil {
		w.Printf("translate retranslate: load config: %v\n", err)
		return 1
	}

	fs := flag.NewFlagSet("translate retranslate", flag.ContinueOnError)
	fs.SetOutput(out)
	dbPath := fs.String("db", "", "Path to the SQLite DB (required)")
	qid := fs.Int("qid", 0, "Question id to retranslate (required)")
	clientName := fs.String("client", cfg.Tools.Translate.ClientOrDefault(), "LLM CLI client (claude | codex | exec). Defaults to tools.translate.client from config.json.")
	modelFlag := fs.String("model", cfg.Tools.Translate.Model, "Model identifier passed to the client (e.g. claude-sonnet-4-6). Defaults to tools.translate.model from config.json; empty leaves the client default.")
	dryRun := fs.Bool("dry-run", false, "Stage the input.json and stop without invoking the client")
	workDir := fs.String("workdir", "", "Optional staging directory (input.json / output.json live here). Default: per-call temp dir.")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *qid == 0 {
		w.Println("translate retranslate: -db and -qid are required")
		return 2
	}

	db, err := sqlite.OpenWith(*dbPath, sqlite.OpenOpts{HostID: cfg.HostID, Backup: true})
	if err != nil {
		w.Printf("translate retranslate: open %s: %v\n", *dbPath, err)
		return 1
	}
	defer func() { _ = db.Close() }()

	adapter, ok := translate.AdapterFor(*clientName, translate.AdapterOpts{
		Model: *modelFlag,
		Bin:   cfg.Tools.Translate.Bin,
	})
	if !ok {
		w.Printf("translate retranslate: unknown client %q (claude | codex | exec)\n", *clientName)
		return 2
	}

	tr, err := translate.Retranslate(context.Background(), translate.RetranslateOpts{
		DB:      db,
		QID:     *qid,
		Adapter: adapter,
		WorkDir: *workDir,
		DryRun:  *dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "translate retranslate: %v\n", err)
		return 1
	}
	if *dryRun {
		w.Printf("dry-run: input staged for qid=%d via client=%s\n", *qid, *clientName)
		return 0
	}
	w.Printf("retranslated qid=%d via client=%s (question_text_ja: %d chars, %d choices)\n",
		tr.ID, *clientName, len(tr.QuestionTextJa), len(tr.ChoicesJa))
	return 0
}

// runExplainSubTo handles `examtopicsdl translate explain -db <path>
// -tid <26-char-base32> [-client <name>] [-model <model>]`. It mirrors
// the retranslate sub-subcommand layout: the Go binary owns the
// read-thread → spawn adapter → validate → write-reply loop, freeing
// web/src/agent.ts to just `Bun.spawn` the binary instead of also
// embedding the LLM prompt and the JSON validation rules.
//
// Both -client and -model default to the values in config.json's
// tools.translate section so users don't have to retype them on every
// invocation. Explicit flags still win over config when present.
func runExplainSubTo(out io.Writer, args []string) int {
	w := utils.NewWriteErr(out)
	cfg, err := config.Load()
	if err != nil {
		w.Printf("translate explain: load config: %v\n", err)
		return 1
	}

	fs := flag.NewFlagSet("translate explain", flag.ContinueOnError)
	fs.SetOutput(out)
	dbPath := fs.String("db", "", "Path to the SQLite DB (required)")
	tid := fs.String("tid", "", "Thread id as 26-char Crockford base32 (required)")
	clientName := fs.String("client", cfg.Tools.Translate.ClientOrDefault(), "LLM CLI client (claude | codex | exec). Defaults to tools.translate.client from config.json.")
	modelFlag := fs.String("model", cfg.Tools.Translate.Model, "Model identifier passed to the client (e.g. claude-sonnet-4-6). Defaults to tools.translate.model from config.json; empty leaves the client default.")
	dryRun := fs.Bool("dry-run", false, "Stage the input.json and stop without invoking the client")
	workDir := fs.String("workdir", "", "Optional staging directory (input.json / output.json live here). Default: per-call temp dir.")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *tid == "" {
		w.Println("translate explain: -db and -tid are required")
		return 2
	}

	db, err := sqlite.OpenWith(*dbPath, sqlite.OpenOpts{HostID: cfg.HostID, Backup: true})
	if err != nil {
		w.Printf("translate explain: open %s: %v\n", *dbPath, err)
		return 1
	}
	defer func() { _ = db.Close() }()

	adapter, ok := translate.ExplainAdapterFor(*clientName, translate.AdapterOpts{
		Model: *modelFlag,
		Bin:   cfg.Tools.Translate.Bin,
	})
	if !ok {
		w.Printf("translate explain: unknown client %q (claude | codex | exec)\n", *clientName)
		return 2
	}

	reply, err := translate.RunExplain(context.Background(), translate.ExplainOpts{
		DB:      db,
		TID:     *tid,
		HostID:  cfg.HostID,
		Adapter: adapter,
		WorkDir: *workDir,
		DryRun:  *dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "translate explain: %v\n", err)
		return 1
	}
	if *dryRun {
		w.Printf("dry-run: input staged for tid=%s via client=%s\n", *tid, *clientName)
		return 0
	}
	reason := reply.ReasonCode
	if reason == "" {
		reason = "(none)"
	}
	w.Printf("explained tid=%s via client=%s (reason=%s, content=%d chars, resolve=%t)\n",
		*tid, *clientName, reason, len(reply.Content), reply.Resolve)
	return 0
}
