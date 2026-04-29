package fetch

import (
	"reflect"
	"testing"

	"examtopics-downloader/internal/models"
)

// Cache JSON URLs lack the `-question-N-discussion` segment, so URL-based
// extraction returns 0. The cache path's Title carries "question #N", which
// must be used as the fallback for question_number.
func TestQuestionDataToRecord_CachePath_QuestionNumberFromTitle(t *testing.T) {
	qd := &models.QuestionData{
		Title:        "Examtopics AWS Certified Developer Associate DVA C02 question #42",
		Header:       "Q?",
		Answer:       "A",
		QuestionLink: "https://www.examtopics.com/discussions/amazon/view/102778-exam-aws-certified-developer-associate-dva-c02-topic-1/",
		Extras:       &models.QuestionExtras{},
	}
	rec := QuestionDataToRecord(qd, "X", map[string]string{"A": "x"})
	if rec.QuestionNumber != 42 {
		t.Errorf("expected fallback to Title for question_number, got %d", rec.QuestionNumber)
	}
}

func TestQuestionDataToRecord_CachePath(t *testing.T) {
	qd := &models.QuestionData{
		Header:          "  Which two services?  ",
		Answer:          "BD",
		SuggestedAnswer: "BD",
		Timestamp:       "2023-01-02 03:04:05",
		QuestionLink:    "https://www.examtopics.com/discussions/amazon/view/12345-foo-topic-3-question-7-discussion/",
		Comments:        "[alice] foo\n[bob] bar",
		Extras: &models.QuestionExtras{
			ExamID:            24,
			IsMC:              true,
			AnswerDescription: "B and D are correct.",
			QuestionImages:    []string{"q.png"},
			AnswerImages:      []string{},
			Discussion: []models.DiscussionEntry{
				{Poster: "alice", Content: "foo", UpvoteCount: "3", Timestamp: "1 week"},
				{Poster: "bob", Content: "bar", UpvoteCount: "", Timestamp: "yesterday"},
			},
		},
	}
	choices := map[string]string{"A": "first", "B": "second"}
	rec := QuestionDataToRecord(qd, "AWS Certified Developer Associate DVA C02", choices)

	if rec.Topic != 3 || rec.QuestionNumber != 7 {
		t.Errorf("topic/qnum: %d/%d, want 3/7", rec.Topic, rec.QuestionNumber)
	}
	if rec.QuestionText != "Which two services?" {
		t.Errorf("question_text not trimmed: %q", rec.QuestionText)
	}
	if rec.SuggestedAnswer != "BD" || rec.ConfirmedAnswer != "BD" {
		t.Errorf("answer fields: sugg=%q conf=%q", rec.SuggestedAnswer, rec.ConfirmedAnswer)
	}
	if rec.ExamID != 24 || !rec.IsMC {
		t.Errorf("Extras scalars: ExamID=%d IsMC=%v", rec.ExamID, rec.IsMC)
	}
	if rec.AnswerDescription != "B and D are correct." {
		t.Errorf("AnswerDescription: %q", rec.AnswerDescription)
	}
	if !reflect.DeepEqual(rec.QuestionImages, []string{"q.png"}) {
		t.Errorf("QuestionImages: %v", rec.QuestionImages)
	}
	if len(rec.Discussion) != 2 {
		t.Fatalf("Discussion len = %d, want 2", len(rec.Discussion))
	}
	if rec.Discussion[0].Idx != 0 || rec.Discussion[1].Idx != 1 {
		t.Errorf("Discussion idx not assigned in order: %d %d", rec.Discussion[0].Idx, rec.Discussion[1].Idx)
	}
	if rec.Discussion[0].UpvoteCount != 3 {
		t.Errorf("UpvoteCount parse: got %d, want 3", rec.Discussion[0].UpvoteCount)
	}
	if rec.Discussion[1].UpvoteCount != 0 {
		t.Errorf("empty upvote should parse as 0, got %d", rec.Discussion[1].UpvoteCount)
	}
}

func TestQuestionDataToRecord_ManualPath_NoExtras(t *testing.T) {
	qd := &models.QuestionData{
		Title:        "Exam AWS Certified Developer Associate DVA C02 topic 1 question 5 discussion",
		Header:       "What does Lambda do?",
		Answer:       "AC",
		QuestionLink: "/discussions/amazon/view/9-foo-topic-1-question-5-discussion/",
		Comments:     "raw blob from .discussion-container",
	}
	rec := QuestionDataToRecord(qd, "AWS Certified Developer Associate DVA C02",
		map[string]string{"A": "first", "B": "second", "C": "third"})

	if rec.SuggestedAnswer != "AC" {
		t.Errorf("manual path should fall back to Answer when SuggestedAnswer empty: got %q", rec.SuggestedAnswer)
	}
	if rec.Topic != 1 || rec.QuestionNumber != 5 {
		t.Errorf("topic/qnum extraction: %d/%d", rec.Topic, rec.QuestionNumber)
	}
	if rec.IsMC != false || rec.ExamID != 0 || rec.AnswerDescription != "" {
		t.Errorf("Extras-derived fields should be zero-valued for manual path")
	}
	if len(rec.Discussion) != 0 {
		t.Errorf("Discussion should be empty for manual path, got %d", len(rec.Discussion))
	}
	if rec.Comments == "" {
		t.Errorf("Comments should still propagate from manual path")
	}
}

func TestExtractChoicesFromManualQuestions(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want map[string]string
	}{
		{
			name: "standard A-D",
			in:   []string{"A. first", "B. second", "C. third", "D. fourth"},
			want: map[string]string{"A": "first", "B": "second", "C": "third", "D": "fourth"},
		},
		{
			name: "trailing whitespace",
			in:   []string{"  A. first  ", "B.  second"},
			want: map[string]string{"A": "first", "B": "second"},
		},
		{
			name: "non-letter junk skipped",
			in:   []string{"A. ok", "1. junk", "Z. last"},
			want: map[string]string{"A": "ok", "Z": "last"},
		},
		{
			name: "empty",
			in:   nil,
			want: map[string]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractChoicesFromManualQuestions(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTitleToExamDisplay(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Exam AWS Certified Developer Associate DVA C02 topic 1 question 5 discussion",
			"AWS Certified Developer Associate DVA C02"},
		{"AWS Certified Developer Associate DVA C02",
			"AWS Certified Developer Associate DVA C02"},
		{"", ""},
		{"Exam Foo", "Foo"},
	}
	for _, tc := range cases {
		got := titleToExamDisplay(tc.in)
		if got != tc.want {
			t.Errorf("titleToExamDisplay(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
