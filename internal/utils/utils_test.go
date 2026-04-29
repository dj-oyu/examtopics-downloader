package utils

import "testing"

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
