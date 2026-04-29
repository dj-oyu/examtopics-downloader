package main

import "testing"

func TestShouldEmitMarkdown(t *testing.T) {
	cases := []struct {
		name       string
		sqliteSet  bool
		oExplicit  bool
		want       bool
		rationale  string
	}{
		{
			name:      "no flags (default behavior)",
			sqliteSet: false, oExplicit: false, want: true,
			rationale: "preserves current behavior — MD is the only output today",
		},
		{
			name:      "only -o explicit",
			sqliteSet: false, oExplicit: true, want: true,
			rationale: "user asked for an MD file",
		},
		{
			name:      "only -sqlite",
			sqliteSet: true, oExplicit: false, want: false,
			rationale: "skip auto-clobbering examtopics_output.md when user opted into SQLite-only",
		},
		{
			name:      "both -sqlite and -o explicit",
			sqliteSet: true, oExplicit: true, want: true,
			rationale: "user explicitly opted into both outputs",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldEmitMarkdown(tc.sqliteSet, tc.oExplicit)
			if got != tc.want {
				t.Errorf("shouldEmitMarkdown(sqliteSet=%v, oExplicit=%v) = %v, want %v (%s)",
					tc.sqliteSet, tc.oExplicit, got, tc.want, tc.rationale)
			}
		})
	}
}
