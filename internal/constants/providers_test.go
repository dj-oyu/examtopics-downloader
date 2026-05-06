package constants

import (
	"slices"
	"sort"
	"testing"
)

func TestKnownProviders_NonEmpty(t *testing.T) {
	if len(KnownProviders) == 0 {
		t.Fatal("KnownProviders is empty")
	}
}

func TestKnownProviders_AmazonPresent(t *testing.T) {
	if !slices.Contains(KnownProviders, "amazon") {
		t.Errorf("amazon must be in KnownProviders, got %v", KnownProviders)
	}
}

func TestKnownProviders_SortedAndDedup(t *testing.T) {
	if !sort.StringsAreSorted(KnownProviders) {
		t.Errorf("KnownProviders is not sorted: %v", KnownProviders)
	}
	seen := map[string]struct{}{}
	for _, p := range KnownProviders {
		if _, dup := seen[p]; dup {
			t.Errorf("duplicate provider %q in KnownProviders", p)
		}
		seen[p] = struct{}{}
	}
}

func TestKnownProviders_LowercaseAlnum(t *testing.T) {
	for _, p := range KnownProviders {
		if p == "" {
			t.Error("empty provider entry")
			continue
		}
		for _, r := range p {
			ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
			if !ok {
				t.Errorf("provider %q has non-ascii or non-allowed char %q", p, r)
				break
			}
		}
	}
}
