package fetch

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"examtopics-downloader/internal/utils"
)

// FetchURL used to retry only 503. examtopics.com throttles with 429, so every
// throttled page was dropped on the first response — the silent partial output
// behind the "missing questions" reports. Throttling must be retried.
func TestFetchURL_RetriesThrottling(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	body := FetchURL(srv.URL, *srv.Client())
	if string(body) != "payload" {
		t.Fatalf("body = %q, want %q", body, "payload")
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2 (one 429 then one success)", got)
	}
}

// 5xx is transient infrastructure — also retryable.
func TestFetchURL_RetriesServerErrors(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	if body := FetchURL(srv.URL, *srv.Client()); string(body) != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2", got)
	}
}

// 403 is what the GitHub contents API returns once the anonymous 60 req/hour
// budget is gone. Retrying cannot help within the same run, so it must fail fast
// (one request), be counted, and be logged with the URL — not silently dropped.
func TestFetchURL_DoesNotRetryForbiddenButCountsIt(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	before := FetchFailures()
	if body := FetchURL(srv.URL, *srv.Client()); body != nil {
		t.Fatalf("body = %q, want nil for a 403", body)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1 (403 must not be retried)", got)
	}
	if got := FetchFailures() - before; got != 1 {
		t.Errorf("FetchFailures delta = %d, want 1", got)
	}
}

// FetchCachedLinks swaps the package-level `client` for an authenticated GitHub
// client once a PAT is supplied. examtopics.com must not see that token, so the
// HTML scrape uses `siteClient` instead — this test pins the separation.
func TestGitHubTokenNeverReachesSiteClient(t *testing.T) {
	var seen atomic.Value
	seen.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("<html><body><h1>ok</h1></body></html>"))
	}))
	defer srv.Close()

	// what FetchCachedLinks does when a PAT is present
	client = utils.NewGitHubClient("github_pat_PROBE")
	defer func() { client = utils.NewHTTPClient() }()

	if _, err := ParseHTML(srv.URL, *siteClient); err != nil {
		t.Fatalf("ParseHTML via siteClient: %v", err)
	}
	if got := seen.Load().(string); got != "" {
		t.Errorf("siteClient leaked an Authorization header: %q", got)
	}

	if _, err := ParseHTML(srv.URL, *client); err != nil {
		t.Fatalf("ParseHTML via client: %v", err)
	}
	if got := seen.Load().(string); got != "Bearer github_pat_PROBE" {
		t.Errorf("GitHub client should still send the token, got %q", got)
	}
}

// A provider that was never mirrored in the cache repo 404s; that is an
// expected outcome (the caller falls back to the manual scrape), so it must not
// show up as a lost fetch in the run's incompleteness warning.
func TestFetchURLProbe_NotFoundIsNotAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	before := FetchFailures()
	if body := FetchURLProbe(srv.URL, *srv.Client()); body != nil {
		t.Fatalf("body = %q, want nil for a 404", body)
	}
	if got := FetchFailures() - before; got != 0 {
		t.Errorf("FetchFailures delta = %d, want 0 for an expected 404", got)
	}
}

// …but a 403 on the same probe means the cache data really was lost.
func TestFetchURLProbe_ForbiddenStillCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	before := FetchFailures()
	if body := FetchURLProbe(srv.URL, *srv.Client()); body != nil {
		t.Fatalf("body = %q, want nil for a 403", body)
	}
	if got := FetchFailures() - before; got != 1 {
		t.Errorf("FetchFailures delta = %d, want 1", got)
	}
}

// A Retry-After header must be honoured as a lower bound on the wait, even when
// it exceeds the current backoff, and capped so a huge value cannot stall a run.
func TestFetchURL_HonoursRetryAfter(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("after-retry"))
	}))
	defer srv.Close()

	if body := FetchURL(srv.URL, *srv.Client()); string(body) != "after-retry" {
		t.Fatalf("body = %q, want %q", body, "after-retry")
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2", got)
	}
}

// Exhausting the retries must surface as a failure, not as an empty success.
func TestFetchURL_ExhaustedRetriesCountsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	before := FetchFailures()
	if body := FetchURL(srv.URL, *srv.Client()); body != nil {
		t.Fatalf("body = %q, want nil after exhausting retries", body)
	}
	if got := FetchFailures() - before; got != 1 {
		t.Errorf("FetchFailures delta = %d, want 1", got)
	}
}
