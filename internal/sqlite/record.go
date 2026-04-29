package sqlite

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// DiscussionRow is one community-discussion comment, normalized for storage in
// the discussion table. Cache JSON populates all fields; manual scrape leaves
// most empty (HTML doesn't preserve per-poster split).
type DiscussionRow struct {
	Idx         int
	Poster      string
	Content     string
	UpvoteCount int
	PostedAt    string
}

// QuestionRecord is the writer-facing representation of one question. Mirrors
// the questions table columns the Go writer is allowed to touch (no _ja).
// Choices is provided as label→text; the writer turns it into rows in choices.
type QuestionRecord struct {
	Exam              string
	ExamID            int
	Topic             int
	QuestionNumber    int
	QuestionText      string
	SuggestedAnswer   string
	ConfirmedAnswer   string
	Timestamp         string
	URL               string
	Comments          string
	IsMC              bool
	AnswerDescription string
	QuestionImages    []string
	AnswerImages      []string
	Choices           map[string]string
	Discussion        []DiscussionRow
}

// ContentHash returns a stable sha256 hex over the fields that determine
// whether the upstream English content has actually changed: question_text,
// suggested_answer, and the canonicalized choice set. Comment threads, images,
// and timestamps are intentionally excluded — they churn without representing
// a real content edit.
func (r *QuestionRecord) ContentHash() string {
	var b strings.Builder
	b.WriteString("q:")
	b.WriteString(r.QuestionText)
	b.WriteString("\nans:")
	b.WriteString(r.SuggestedAnswer)
	b.WriteString("\nchoices:\n")

	labels := make([]string, 0, len(r.Choices))
	for k := range r.Choices {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	for _, l := range labels {
		b.WriteString(l)
		b.WriteByte('=')
		b.WriteString(r.Choices[l])
		b.WriteByte('\n')
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
