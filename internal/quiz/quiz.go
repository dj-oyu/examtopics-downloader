// Package quiz implements the persistence layer for `examtopicsdl quiz`:
// loading questions out of the migrated DB and recording answer attempts
// into the multi-host UUIDv7-keyed attempts table. The interactive TUI
// loop lives in cmd/quiz.go and consumes this package over a thin API so
// the loop itself stays free of test-hostile globals (DB handles, host
// IDs, time sources).
package quiz

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"examtopics-downloader/internal/uuidx"
)

// Choice is one option attached to a Question.
type Choice struct {
	Label string
	Text  string
}

// CommunityVote is one answer combination's share of the community votes.
type CommunityVote struct {
	Label string  `json:"label"`
	Votes int     `json:"votes"`
	Pct   float64 `json:"pct"`
}

// Verdict mirrors one answer_verdicts row (migration 004, written by
// tools/answer_verdict.py): whether the answer is settled, contested or
// unknowable, which answers count as correct, and how the community voted.
type Verdict struct {
	Status     string // settled | ambiguous | unknown
	Accepted   []string
	Community  []CommunityVote
	TotalVotes int
	Rationale  string
}

// Question is a single exam item with its answer choices loaded from the
// questions / choices tables. Multi-letter SuggestedAnswer values like
// "BD" indicate multi-select questions; IsCorrect normalizes letter
// order before comparing.
type Question struct {
	ID              int
	Exam            string
	Topic           int
	QuestionNumber  int
	QuestionText    string
	SuggestedAnswer string
	Choices         []Choice
	Verdict         Verdict
}

// AcceptedAnswers returns every answer that counts as correct: the verdict's
// set when it has one (a contested question accepts more than one), otherwise
// the suggested answer alone.
func (q Question) AcceptedAnswers() []string {
	if len(q.Verdict.Accepted) > 0 {
		return q.Verdict.Accepted
	}
	if s := normalizeLetters(q.SuggestedAnswer); s != "" {
		return []string{s}
	}
	return nil
}

// Outcome is the result of grading one answer.
type Outcome struct {
	Correct bool
	// Ungraded means there was nothing to grade against (the verdict calls the
	// question `unknown`), so no attempts row was written.
	Ungraded bool
}

// LoadQuestions returns every question in the DB along with its choices,
// ordered by (topic, question_number). The order is stable so a quiz
// session sees questions in their natural exam sequence.
func LoadQuestions(db *sql.DB) ([]Question, error) {
	rows, err := db.Query(`SELECT id, exam, topic, question_number, question_text,
		COALESCE(suggested_answer, '') FROM questions ORDER BY topic, question_number`)
	if err != nil {
		return nil, fmt.Errorf("query questions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var qs []Question
	for rows.Next() {
		var q Question
		if err := rows.Scan(&q.ID, &q.Exam, &q.Topic, &q.QuestionNumber, &q.QuestionText, &q.SuggestedAnswer); err != nil {
			return nil, fmt.Errorf("scan question: %w", err)
		}
		qs = append(qs, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range qs {
		cs, err := loadChoices(db, qs[i].ID)
		if err != nil {
			return nil, err
		}
		qs[i].Choices = cs
	}
	verdicts, err := loadVerdicts(db)
	if err != nil {
		return nil, err
	}
	for i := range qs {
		if v, ok := verdicts[qs[i].ID]; ok {
			qs[i].Verdict = v
		}
	}
	return qs, nil
}

// loadVerdicts reads the answer_verdicts table (migration 004). A DB that has
// not been through the migration simply yields no verdicts, which makes every
// question fall back to grading against its suggested answer alone.
func loadVerdicts(db *sql.DB) (map[int]Verdict, error) {
	rows, err := db.Query(`SELECT question_id, status, accepted, community,
		total_votes, COALESCE(rationale, '') FROM answer_verdicts`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("query answer_verdicts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int]Verdict{}
	for rows.Next() {
		var qid int
		var status, accepted, community, rationale string
		var total int
		if err := rows.Scan(&qid, &status, &accepted, &community, &total, &rationale); err != nil {
			return nil, fmt.Errorf("scan verdict: %w", err)
		}
		v := Verdict{Status: status, TotalVotes: total, Rationale: rationale}
		// JSON columns written by the Python tool; a malformed value must not
		// take the whole quiz down, so decode best-effort.
		_ = json.Unmarshal([]byte(accepted), &v.Accepted)
		_ = json.Unmarshal([]byte(community), &v.Community)
		out[qid] = v
	}
	return out, rows.Err()
}

func loadChoices(db *sql.DB, qid int) ([]Choice, error) {
	rows, err := db.Query(`SELECT label, text FROM choices WHERE question_id = ? ORDER BY label`, qid)
	if err != nil {
		return nil, fmt.Errorf("query choices for %d: %w", qid, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Choice
	for rows.Next() {
		var c Choice
		if err := rows.Scan(&c.Label, &c.Text); err != nil {
			return nil, fmt.Errorf("scan choice: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// IsCorrect compares a user-supplied answer against the suggested answer
// in an order-insensitive, case-insensitive way. ExamTopics uses sequences
// like "BD" or "ACE" for multi-select; entering "DB" is the same answer.
func IsCorrect(selected, correct string) bool {
	return normalizeLetters(selected) == normalizeLetters(correct)
}

// IsAccepted reports whether the answer matches ANY of the answers that count as
// correct. On a contested question that means either side of the argument is
// graded right.
func IsAccepted(selected string, accepted []string) bool {
	s := normalizeLetters(selected)
	if s == "" {
		return false
	}
	for _, a := range accepted {
		if normalizeLetters(a) == s {
			return true
		}
	}
	return false
}

func normalizeLetters(s string) string {
	upper := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	letters := []byte(upper)
	slices.Sort(letters)
	return string(letters)
}

// RecordAttempt inserts a single attempts row with a freshly generated
// UUIDv7 primary key and the supplied host_id, returning whether the
// answer matched the correct sequence. attempted_at is stored as
// RFC3339-UTC so it stays directly comparable across hosts (lex order
// matches chronological order under UTC).
func RecordAttempt(db *sql.DB, hostID string, questionID int, selected, correct string) (bool, error) {
	out, err := RecordAttemptAgainst(db, hostID, questionID, selected, []string{correct})
	return out.Correct, err
}

// RecordAttemptAgainst grades the answer against every accepted answer (a
// contested question accepts more than one) and stores the row.
//
// With nothing to grade against — the verdict calls the question `unknown`
// because neither a key nor a community consensus exists — no row is written at
// all: recording a false "incorrect" would be worse than recording nothing.
func RecordAttemptAgainst(db *sql.DB, hostID string, questionID int, selected string, accepted []string) (Outcome, error) {
	if normalizeLetters(selected) == "" || len(accepted) == 0 {
		return Outcome{Ungraded: true}, nil
	}
	id, err := uuidx.New()
	if err != nil {
		return Outcome{}, fmt.Errorf("generate uuid: %w", err)
	}
	correctFlag := 0
	matched := IsAccepted(selected, accepted)
	if matched {
		correctFlag = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO attempts(id, question_id, selected, is_correct, attempted_at, host_id) VALUES(?, ?, ?, ?, ?, ?)`,
		id[:], questionID, selected, correctFlag, now, hostID,
	); err != nil {
		return Outcome{}, fmt.Errorf("insert attempt: %w", err)
	}
	return Outcome{Correct: matched}, nil
}
