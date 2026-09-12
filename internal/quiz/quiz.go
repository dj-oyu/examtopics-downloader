// Package quiz implements the persistence layer for `examtopicsdl quiz`:
// loading questions out of the migrated DB and recording answer attempts
// into the multi-host UUIDv7-keyed attempts table. The interactive TUI
// loop lives in cmd/quiz.go and consumes this package over a thin API so
// the loop itself stays free of test-hostile globals (DB handles, host
// IDs, time sources).
package quiz

import (
	"database/sql"
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
	return qs, nil
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
	id, err := uuidx.New()
	if err != nil {
		return false, fmt.Errorf("generate uuid: %w", err)
	}
	correctFlag := 0
	matched := IsCorrect(selected, correct)
	if matched {
		correctFlag = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO attempts(id, question_id, selected, is_correct, attempted_at, host_id) VALUES(?, ?, ?, ?, ?, ?)`,
		id[:], questionID, selected, correctFlag, now, hostID,
	); err != nil {
		return false, fmt.Errorf("insert attempt: %w", err)
	}
	return matched, nil
}
