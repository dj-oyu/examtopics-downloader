package utils

import (
	"net/http"
	"testing"
	"time"

	"examtopics-downloader/internal/constants"
	"examtopics-downloader/internal/models"
)

// The cache path writes Titles of the form
// "Examtopics <name>_<shard> question #N", which the legacy page-shard parser
// (ExtractNumberFromPath) cannot read — every element compared equal and the
// output order was arbitrary. Ordering must come from the cache JSON's
// question_id when available, and the sort must be stable.
func TestSortQuestionDataByPageNumber_PrefersQuestionID(t *testing.T) {
	in := []models.QuestionData{
		{Title: "Examtopics Foo_3 question #9", Extras: &models.QuestionExtras{QuestionID: 9}},
		{Title: "Examtopics Foo_1 question #3", Extras: &models.QuestionExtras{QuestionID: 3}},
		{Title: "Examtopics Foo_2 question #6", Extras: &models.QuestionExtras{QuestionID: 6}},
	}

	got := SortQuestionDataByPageNumber(in)
	want := []int{3, 6, 9}
	for i, q := range got {
		if q.Extras.QuestionID != want[i] {
			t.Fatalf("position %d: got question_id %d, want %d", i, q.Extras.QuestionID, want[i])
		}
	}
	// The caller's slice must not be reordered in place.
	if in[0].Extras.QuestionID != 9 {
		t.Error("SortQuestionDataByPageNumber must not mutate its input")
	}
}

// Without Extras it falls back to the legacy "_<shard>.json" title form.
func TestSortQuestionDataByPageNumber_FallsBackToShardSuffix(t *testing.T) {
	in := []models.QuestionData{
		{Title: "Examtopics Foo_10.json question #1"},
		{Title: "Examtopics Foo_2.json question #1"},
	}

	got := SortQuestionDataByPageNumber(in)
	if got[0].Title != "Examtopics Foo_2.json question #1" {
		t.Errorf("expected shard 2 first, got %q", got[0].Title)
	}
}

func TestExtractQuestionNum(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want int
	}{
		{
			name: "standard discussion link",
			url:  "/discussions/amazon/view/12345-exam-aws-certified-developer-associate-dva-c02-topic-1-question-5-discussion/",
			want: 5,
		},
		{
			name: "absolute url",
			url:  "https://www.examtopics.com/discussions/amazon/view/12345-foo-topic-2-question-42-discussion/",
			want: 42,
		},
		{
			name: "missing question segment",
			url:  "/discussions/amazon/view/12345-foo-bar/",
			want: 0,
		},
		{
			name: "trailing slash variation",
			url:  "/discussions/amazon/view/12345-foo-topic-1-question-7-discussion",
			want: 7,
		},
		{
			name: "empty input",
			url:  "",
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractQuestionNum(tc.url)
			if got != tc.want {
				t.Errorf("ExtractQuestionNum(%q) = %d, want %d", tc.url, got, tc.want)
			}
		})
	}
}

func TestExtractTopicNum(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want int
	}{
		{
			name: "standard discussion link",
			url:  "/discussions/amazon/view/12345-exam-aws-certified-developer-associate-dva-c02-topic-1-question-5-discussion/",
			want: 1,
		},
		{
			name: "two-digit topic",
			url:  "/discussions/amazon/view/12345-foo-topic-12-question-3-discussion/",
			want: 12,
		},
		{
			name: "missing topic segment",
			url:  "/discussions/amazon/view/12345-foo-question-5-discussion/",
			want: 0,
		},
		{
			name: "empty input",
			url:  "",
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractTopicNum(tc.url)
			if got != tc.want {
				t.Errorf("ExtractTopicNum(%q) = %d, want %d", tc.url, got, tc.want)
			}
		})
	}
}

func TestDeriveExamDisplay(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "shard with json suffix",
			in:   "AWS-Certified-Developer---Associate-DVA-C02_5.json",
			want: "AWS Certified Developer Associate DVA C02",
		},
		{
			name: "shard with json and ref query",
			in:   "AWS-Certified-Developer---Associate-DVA-C02_5.json?ref=main",
			want: "AWS Certified Developer Associate DVA C02",
		},
		{
			name: "no shard, just json",
			in:   "AWS-Certified-Cloud-Practitioner-CLF-C02.json",
			want: "AWS Certified Cloud Practitioner CLF C02",
		},
		{
			name: "already display string",
			in:   "AWS Certified Developer Associate DVA C02",
			want: "AWS Certified Developer Associate DVA C02",
		},
		{
			name: "leading and trailing spaces collapse",
			in:   "  AWS-Certified---Foo  ",
			want: "AWS Certified Foo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveExamDisplay(tc.in)
			if got != tc.want {
				t.Errorf("DeriveExamDisplay(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Regression: SortLinksByQuestionNumber must keep working after the closures
// are lifted to exported helpers.
func TestSortLinksByQuestionNumber_PreservedAfterRefactor(t *testing.T) {
	in := []string{
		"/x/topic-2-question-3-discussion/",
		"/x/topic-1-question-10-discussion/",
		"/x/topic-1-question-2-discussion/",
		"/x/topic-2-question-1-discussion/",
	}
	want := []string{
		"/x/topic-1-question-2-discussion/",
		"/x/topic-1-question-10-discussion/",
		"/x/topic-2-question-1-discussion/",
		"/x/topic-2-question-3-discussion/",
	}
	got := SortLinksByQuestionNumber(append([]string(nil), in...))
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// Retry policy: throttling and server errors are transient, client errors are
// not. 403 in particular means "you are out of anonymous GitHub quota" — the
// caller must report the loss instead of spinning.
func TestRetryableStatus(t *testing.T) {
	cases := map[int]bool{
		200: false,
		301: false,
		400: false,
		403: false,
		404: false,
		429: true,
		500: true,
		502: true,
		503: true,
		504: true,
	}
	for code, want := range cases {
		if got := RetryableStatus(code); got != want {
			t.Errorf("RetryableStatus(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestRetryAfterDelay(t *testing.T) {
	mk := func(value string) *http.Response {
		resp := &http.Response{Header: http.Header{}}
		if value != "" {
			resp.Header.Set("Retry-After", value)
		}
		return resp
	}

	if got := RetryAfterDelay(mk("")); got != 0 {
		t.Errorf("absent header = %v, want 0", got)
	}
	if got := RetryAfterDelay(mk("2")); got != 2*time.Second {
		t.Errorf("delta-seconds = %v, want 2s", got)
	}
	if got := RetryAfterDelay(mk("0")); got != 0 {
		t.Errorf("zero seconds = %v, want 0", got)
	}
	if got := RetryAfterDelay(mk("garbage")); got != 0 {
		t.Errorf("unparseable = %v, want 0", got)
	}
	if got := RetryAfterDelay(mk("99999")); got != constants.RetryAfterCap {
		t.Errorf("huge value = %v, want the cap %v", got, constants.RetryAfterCap)
	}
	if got := RetryAfterDelay(nil); got != 0 {
		t.Errorf("nil response = %v, want 0", got)
	}

	// HTTP-date form: a past date means no wait, a near-future one is honoured.
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	if got := RetryAfterDelay(mk(past)); got != 0 {
		t.Errorf("past HTTP-date = %v, want 0", got)
	}
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	if got := RetryAfterDelay(mk(future)); got <= 0 || got > 6*time.Second {
		t.Errorf("future HTTP-date = %v, want ~5s", got)
	}
}
