package utils

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"examtopics-downloader/internal/models"
)

// questionWithAnswer builds a minimal QuestionData the way the scraper does.
func questionWithAnswer(suggested, legacy string) models.QuestionData {
	return models.QuestionData{
		Title:           "Exam 010-160 topic 1 question 1 discussion",
		Header:          "Actual exam question from LPI's 010-160",
		Content:         "Which records hold an IP address?",
		Questions:       []string{"A. A", "B. CNAME", "C. MX", "D. AAAA"},
		Answer:          legacy,
		SuggestedAnswer: suggested,
		Timestamp:       "Feb. 26, 2020, 2:58 p.m.",
		QuestionLink:    "https://www.examtopics.com/discussions/lpi/view/1-discussion/",
		Comments:        "alice Selected Answer: BD upvoted 3 times",
	}
}

// The MD `Suggested Answer:` line is what tools/md_to_sqlite.py parses into the
// `suggested_answer` column, and it is the only place a multi-select answer
// survives intact in the Markdown output.
func TestWriteData_EmitsSuggestedAnswerLineForLetterSets(t *testing.T) {
	t.Chdir(t.TempDir())

	WriteData([]models.QuestionData{questionWithAnswer("BD", "BD")}, "out.md", true, "md")

	body, err := os.ReadFile("out.md")
	if err != nil {
		t.Fatalf("reading md output: %v", err)
	}
	got := string(body)

	if !strings.Contains(got, "Suggested Answer: BD 🗳️") {
		t.Errorf("expected a `Suggested Answer: BD 🗳️` line, got:\n%s", got)
	}
	if !strings.Contains(got, "**Answer: BD**") {
		t.Errorf("expected the legacy `**Answer:**` line to stay, got:\n%s", got)
	}
	if strings.Index(got, "Suggested Answer:") > strings.Index(got, "**Answer: ") {
		t.Error("Suggested Answer line must precede **Answer:** so md_to_sqlite.py can bound the choice region")
	}
}

// Legacy producers (and the mock fixtures the integration tests use) put the
// full choice text in Answer. That must not leak into a `Suggested Answer:`
// line, which downstream parsers read as a bare letter set.
func TestWriteData_SkipsSuggestedAnswerLineForProseAnswers(t *testing.T) {
	t.Chdir(t.TempDir())

	WriteData([]models.QuestionData{questionWithAnswer("", "B. To define filesystem mount points")}, "out.md", true, "md")

	body, err := os.ReadFile("out.md")
	if err != nil {
		t.Fatalf("reading md output: %v", err)
	}
	got := string(body)

	if strings.Contains(got, "Suggested Answer:") {
		t.Errorf("prose answer should not produce a Suggested Answer line, got:\n%s", got)
	}
	if !strings.Contains(got, "**Answer: B. To define filesystem mount points**") {
		t.Errorf("legacy answer text should still be emitted, got:\n%s", got)
	}
}

// WriteData must keep the fork's extra fields (SuggestedAnswer/Extras) on the
// snake_case wire shape that upstream's `-type json` feature established.
func TestWriteData_JSONOutputUsesSnakeCase(t *testing.T) {
	t.Chdir(t.TempDir())

	q := questionWithAnswer("AE", "AE")
	q.Extras = &models.QuestionExtras{
		ExamID:            42,
		IsMC:              true,
		AnswerDescription: "A and E hold addresses.",
		QuestionImages:    []string{"https://example.com/q.png"},
		Discussion:        []models.DiscussionEntry{{Poster: "alice", Content: "AE", UpvoteCount: "3"}},
	}

	WriteData([]models.QuestionData{q}, "out.md", true, "json")

	// NB: the intermediate markdown is only removed when the user answers the
	// interactive "Delete Markdown file after conversion?" prompt (see
	// deleteMarkdownFile), so its presence here is not asserted.
	raw, err := os.ReadFile("out.json")
	if err != nil {
		t.Fatalf("reading json output: %v", err)
	}

	for _, key := range []string{"\"question_link\"", "\"suggested_answer\"", "\"extras\"", "\"is_mc\""} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("expected json output to contain %s, got:\n%s", key, raw)
		}
	}
	if strings.Contains(string(raw), "\"SuggestedAnswer\"") {
		t.Errorf("Go field name leaked into json output:\n%s", raw)
	}

	var decoded []models.QuestionData
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json output is not decodable: %v", err)
	}
	if len(decoded) != 1 || decoded[0].SuggestedAnswer != "AE" {
		t.Errorf("round-trip lost the suggested answer: %+v", decoded)
	}
	if decoded[0].Extras == nil || decoded[0].Extras.ExamID != 42 {
		t.Errorf("round-trip lost Extras: %+v", decoded[0].Extras)
	}
}
