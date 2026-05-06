package main

import "os"

// subcommand registers a non-legacy entry point. Each subcommand lives
// in its own cmd/<name>.go file. A nil run means "consume the
// subcommand word and fall through to the legacy main() body" — used
// by 'fetch' so `examtopicsdl fetch -p amazon -s saa-c03` behaves
// identically to the legacy `examtopicsdl -p amazon -s saa-c03`.
//
// Keeping the dispatch table here (instead of inside main()) lets the
// upstream-shaped main() body in cmd/main.go stay untouched: the only
// hook added there is the four-line dispatchSubcommand call at the
// very top of main(), which never overlaps with the legacy flag
// parsing region that upstream patches typically modify.
type subcommand struct {
	name string
	desc string
	run  func(args []string) int
}

var subcommands = []subcommand{
	{"config", "Print the resolved runtime configuration as JSON", runConfig},
	{"fetch", "Scrape exam pages and write Markdown / SQLite output (legacy flag-style)", nil},
	{"providers", "List known examtopics provider slugs", runProviders},
	{"version", "Print version and exit", runVersionCmd},
}

// dispatchSubcommand inspects args (= os.Args[1:]) and routes to the
// matching subcommand. Returns (exitCode, true) when a subcommand
// handled the call, or (_, false) when the caller should continue
// with upstream's legacy flag parsing. A leading flag (-x ...) or
// any unrecognized first word always falls through.
//
// Special case: when the matched subcommand has a nil run (currently
// "fetch"), the function strips the subcommand word from os.Args and
// returns (_, false) so the surrounding main() body sees the remaining
// args as if the legacy form had been typed.
func dispatchSubcommand(args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	head := args[0]
	if len(head) > 0 && head[0] == '-' {
		return 0, false
	}
	for _, sc := range subcommands {
		if sc.name != head {
			continue
		}
		if sc.run == nil {
			// Drop the subcommand word and let the legacy body run.
			os.Args = append([]string{os.Args[0]}, args[1:]...)
			return 0, false
		}
		return sc.run(args[1:]), true
	}
	return 0, false
}
