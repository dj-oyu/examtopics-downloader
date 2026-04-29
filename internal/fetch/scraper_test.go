package fetch

import (
	"testing"

	"examtopics-downloader/internal/models"
)

// Regression for scraper.go:35 truncation bug — multi-letter answers like
// "BD" / "AE" must survive the cleaner. The previous implementation took
// the first byte only, so BD became B.
func TestCleanAnswer_PreservesMultiLetter(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"single letter", "A", "A"},
		{"two letters bare", "BD", "BD"},
		{"two letters with whitespace", "  BD  ", "BD"},
		{"two letters split by space", "B D", "BD"},
		{"two letters split by newline", "B\nD", "BD"},
		{"three letters", "ACE", "ACE"},
		{"empty", "", ""},
		{"only whitespace", "   \n\t ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanAnswer(tc.in)
			if got != tc.want {
				t.Errorf("cleanAnswer(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ConvertCachedJSON must hydrate Extras (per-poster discussion, images,
// AnswerDescription, ExamID, IsMC) AND populate SuggestedAnswer with the FULL
// multi-letter answer string. It must also still produce the legacy fields
// (Comments flattened "[Poster] content") so the MD output is unchanged.
func TestConvertCachedJSON_HydratesExtrasAndSuggestedAnswer(t *testing.T) {
	var resp models.JSONResponse
	resp.PageProps.Questions = append(resp.PageProps.Questions, struct {
		Choices           map[string]string `json:"choices"`
		ID                string            `json:"id"`
		ExamID            int               `json:"exam_id"`
		QuestionText      string            `json:"question_text"`
		Answer            string            `json:"answer"`
		AnswerET          string            `json:"answer_ET"`
		Topic             string            `json:"topic"`
		IsMC              bool              `json:"isMC"`
		AnswerDescription string            `json:"answer_description"`
		Discussion        []struct {
			Content     string `json:"content"`
			UpvoteCount string `json:"upvote_count"`
			Poster      string `json:"poster"`
			Timestamp   string `json:"timestamp"`
		} `json:"discussion"`
		AnswerImages   []string `json:"answer_images"`
		QuestionImages []string `json:"question_images"`
		URL            string   `json:"url"`
		Timestamp      string   `json:"timestamp"`
	}{
		Choices: map[string]string{"A": "first", "B": "second", "C": "third", "D": "fourth"},
		ID:               "abc123",
		ExamID:           24,
		QuestionText:     "Which two services?",
		Answer:           "BD",
		AnswerET:         "BD",
		Topic:            "1",
		IsMC:             true,
		AnswerDescription: "B and D are correct because ...",
		Discussion: []struct {
			Content     string `json:"content"`
			UpvoteCount string `json:"upvote_count"`
			Poster      string `json:"poster"`
			Timestamp   string `json:"timestamp"`
		}{
			{Content: "first thought", UpvoteCount: "3", Poster: "alice", Timestamp: "1 week"},
			{Content: "second", UpvoteCount: "", Poster: "bob", Timestamp: "yesterday"},
		},
		QuestionImages: []string{"q1.png"},
		AnswerImages:   []string{},
		URL:            "https://www.examtopics.com/discussions/amazon/view/12345-foo-topic-1-question-7-discussion/",
		Timestamp:      "2023-01-02 03:04:05",
	})

	got := ConvertCachedJSON(resp, "AWS-Certified-Developer---Associate-DVA-C02_5.json")
	if len(got) != 1 {
		t.Fatalf("expected 1 converted question, got %d", len(got))
	}
	q := got[0]
	if q.SuggestedAnswer != "BD" {
		t.Errorf("SuggestedAnswer = %q, want BD", q.SuggestedAnswer)
	}
	if q.Answer != "BD" {
		t.Errorf("legacy Answer should also carry full string for MD: got %q", q.Answer)
	}
	if q.Extras == nil {
		t.Fatalf("Extras should not be nil for cache path")
	}
	if q.Extras.ExamID != 24 || !q.Extras.IsMC {
		t.Errorf("scalar Extras: ExamID=%d IsMC=%v", q.Extras.ExamID, q.Extras.IsMC)
	}
	if q.Extras.AnswerDescription == "" {
		t.Errorf("AnswerDescription should be populated")
	}
	if len(q.Extras.QuestionImages) != 1 || q.Extras.QuestionImages[0] != "q1.png" {
		t.Errorf("QuestionImages: %v", q.Extras.QuestionImages)
	}
	if len(q.Extras.Discussion) != 2 {
		t.Fatalf("Discussion len = %d, want 2", len(q.Extras.Discussion))
	}
	if q.Extras.Discussion[0].Poster != "alice" || q.Extras.Discussion[0].Content != "first thought" ||
		q.Extras.Discussion[0].UpvoteCount != "3" {
		t.Errorf("Discussion[0]: %+v", q.Extras.Discussion[0])
	}
	// Comments still flattened for back-compat with the MD path / audit_comments.py.
	if q.Comments == "" {
		t.Errorf("legacy Comments should still be populated for MD back-compat")
	}
	// QuestionLink should be the question's URL from the JSON (not the GitHub link).
	if q.QuestionLink != "https://www.examtopics.com/discussions/amazon/view/12345-foo-topic-1-question-7-discussion/" {
		t.Errorf("QuestionLink not from JSON.url: %q", q.QuestionLink)
	}
}
