package constants

// KnownProviders is the curated list of examtopics.com provider slugs
// the CLI will surface from `examtopicsdl providers` and the future web
// admin UI's provider whitelist (§3.4 of docs/plans/portable-builds.md).
//
// The list is alphabetically sorted, lowercase, and ASCII-only — see
// providers_test.go for the invariants. Any provider slug accepted by
// examtopics.com URLs (https://www.examtopics.com/exams/<slug>/) can be
// added; the slug must be sortable in place to keep the list in order.
var KnownProviders = []string{
	"amazon",
	"cisco",
	"comptia",
	"google",
	"hashicorp",
	"isc",
	"juniper",
	"microsoft",
	"oracle",
	"pmi",
	"redhat",
	"salesforce",
	"servicenow",
	"vmware",
}
