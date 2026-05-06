package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"examtopics-downloader/internal/fetch"
	"examtopics-downloader/internal/sqlite"
	"examtopics-downloader/internal/utils"
)

// version is overridden at build time via -ldflags="-X main.version=$tag"
// in release.yml; "dev" is the default for un-tagged local builds.
var version = "dev"

// shouldEmitMarkdown decides whether we should run the legacy Markdown writer
// path. The default is "yes" (preserves prior behavior). The only case we skip
// MD is when the user opted into -sqlite without explicitly setting -o, which
// would otherwise auto-clobber examtopics_output.md as a side effect.
func shouldEmitMarkdown(sqliteSet, oExplicit bool) bool {
	if !sqliteSet {
		return true
	}
	return oExplicit
}

func main() {
	// Subcommand dispatch — see cmd/dispatch.go. The shim sits ahead of
	// upstream's main() body so new subcommand handling does not
	// interleave with the legacy flag-style scrape logic. This is the
	// ONLY structural change to upstream's main(); future upstream
	// patches to the body below apply cleanly because the shim is at a
	// boundary they never touch.
	if exit, ok := dispatchSubcommand(os.Args[1:]); ok {
		os.Exit(exit)
	}

	// ----- legacy upstream main() body below -----
	// Best-effort load of ./.env so a committed PAT in $GH_PAT is picked up
	// without the user having to `source` it. Existing env vars win.
	if err := utils.LoadDotEnv(".env"); err != nil {
		log.Printf("warning: failed to read .env: %v", err)
	}

	provider := flag.String("p", "google", "Name of the exam provider (default -> google)")
	grepStr := flag.String("s", "", "String to grep for in discussion links")
	outputPath := flag.String("o", "examtopics_output.md", "Optional path of the file where the data will be outputted")
	fileType := flag.String("type", "md", "Optionally include file type (default -> .md)")
	commentBool := flag.Bool("c", false, "Optionally include all the comment/discussion text")
	examsFlag := flag.Bool("exams", false, "Optionally show all the possible exams for your selected provider and exit")
	saveUrls := flag.Bool("save-links", false, "Optional argument to save unique links to questions")
	noCache := flag.Bool("no-cache", false, "Optional argument, set to disable looking through cached data on github")
	token := flag.String("t", "", "GitHub PAT for cached scrape (env GH_PAT used when flag is empty)")
	sqlitePath := flag.String("sqlite", "", "Optional path to a SQLite DB. When set, scraped data is written directly into this DB (cache JSON preserves all fields; manual fallback writes a subset).")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version)
		os.Exit(0)
	}

	// Fall back to GH_PAT (possibly loaded from .env) when -t is empty.
	if *token == "" {
		if envTok := os.Getenv("GH_PAT"); envTok != "" {
			*token = envTok
		}
	}

	if *examsFlag {
		exams := fetch.GetProviderExams(*provider)
		fmt.Printf("Exams for provider '%s'\n\n", *provider)
		for _, exam := range exams {
			fmt.Println(utils.AddToBaseUrl(exam))
		}
		os.Exit(0)
	}

	if *grepStr == "" {
		log.Printf("running without a valid string to search for with -s, (no_grep_str)!")
	}

	// Detect explicit `-o` so we know whether the user actually wants MD output
	// or is just inheriting the default value.
	oExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "o" {
			oExplicit = true
		}
	})
	emitMarkdown := shouldEmitMarkdown(*sqlitePath != "", oExplicit)

	if *sqlitePath != "" {
		if err := runSQLiteMode(*sqlitePath, *provider, *grepStr, *token, *noCache, *saveUrls); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if !emitMarkdown {
			return
		}
	}

	if !*noCache {
		links := fetch.GetCachedPages(*provider, *grepStr, *token)
		if len(links) > 0 {
			utils.WriteData(links, *outputPath, *commentBool, *fileType)
			fmt.Printf("Successfully saved cached output to %s (filetype: %s).\n", *outputPath, *fileType)
			os.Exit(0)
		}
	}

	fmt.Println("Going to manual scraping, cached data failed.")
	links := fetch.GetAllPages(*provider, *grepStr)

	if *saveUrls {
		utils.SaveLinks("saved-links.txt", links)
	}
	utils.WriteData(links, *outputPath, *commentBool, *fileType)
	fmt.Printf("Successfully saved output to %s (filetype: %s).\n", *outputPath, *fileType)
}

// runSQLiteMode opens the target DB and writes scraped data straight into it.
// Tries the cache path first; on zero results falls back to the manual scrape.
// Errors out (non-zero) if the combined run wrote zero questions, which is how
// we surface the silent "0 matches" cases (bad -s grep, GitHub 1000-listing
// cap miss without manual hits).
func runSQLiteMode(path, provider, grep, token string, noCache, saveUrls bool) error {
	db, err := sqlite.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = db.Close() }()

	w := sqlite.NewWriter(db)
	if err := w.Begin(); err != nil {
		return err
	}

	cachedCount := 0
	if !noCache {
		n, err := fetch.GetCachedPagesToSQLite(provider, grep, token, w)
		if err != nil {
			_ = w.Rollback()
			return fmt.Errorf("cache write: %w", err)
		}
		cachedCount = n
	}

	manualCount := 0
	if cachedCount == 0 {
		fmt.Println("Going to manual scraping, cached data failed.")
		n, err := fetch.GetAllPagesToSQLite(provider, grep, w)
		if err != nil {
			_ = w.Rollback()
			return fmt.Errorf("manual write: %w", err)
		}
		manualCount = n
	}
	_ = saveUrls // intentionally unused in SQLite mode for now

	total := cachedCount + manualCount
	if total == 0 {
		_ = w.Rollback()
		return fmt.Errorf("no questions matched %q for provider %q (check -s spelling; GitHub cache caps directory listing at 1000 entries — exams alphabetically after 'AWS-Certified-SAP-on-AWS' miss the cache)", grep, provider)
	}
	if err := w.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	fmt.Printf("Successfully saved %d questions to %s (cache=%d, manual=%d).\n", total, path, cachedCount, manualCount)
	return nil
}
