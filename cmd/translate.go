package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"examtopics-downloader/internal/translate"
	"examtopics-downloader/internal/utils"
)

// runTranslate implements `examtopicsdl translate`. Today the subcommand
// covers materialization of the portable exam-translator skill into the
// layout each LLM CLI expects (§3.6 of docs/plans/portable-builds.md).
// Spawning the chosen client and driving the actual translation loop is
// scoped to follow-up commits — this entry point intentionally lands the
// skill plumbing first so the tests can pin its behavior independent of
// the CLI integrations, which depend on per-vendor conventions.
func runTranslate(args []string) int {
	return runTranslateTo(os.Stdout, args)
}

func runTranslateTo(out io.Writer, args []string) int {
	w := utils.NewWriteErr(out)
	fs := flag.NewFlagSet("translate", flag.ContinueOnError)
	fs.SetOutput(out)
	clientName := fs.String("client", "gemini", "LLM CLI client (gemini | claude | codex | exec)")
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
	// Spawning the chosen CLI is intentionally deferred: each vendor's
	// argument convention (and authentication contract) is large enough
	// to warrant its own follow-up. For now print the next step so the
	// user can chain `examtopicsdl translate -client X` with the actual
	// CLI invocation manually.
	w.Printf("(spawn-client step not yet implemented; invoke %q manually after this materialization step)\n", c.Name)
	return 0
}
