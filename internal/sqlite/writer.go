package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Writer streams QuestionRecords into a SQLite DB inside a single transaction.
// One Writer wraps one DB; call Begin → UpsertQuestion(...)*N → Commit.
//
// Invariants:
//   - Never writes to *_ja columns (translate.py owns those).
//   - Never creates or writes to attempts/explanation_threads/explanation_messages
//     (web/src/db.ts and translate.py own those).
//   - URL conflict on questions updates English columns only and preserves _ja.
//   - Per-question choices are upserted (text updated, text_ja preserved).
//   - Per-question discussion is replaced atomically (DELETE then INSERT).
type Writer struct {
	db *sql.DB
	tx *sql.Tx
}

func NewWriter(db *sql.DB) *Writer { return &Writer{db: db} }

func (w *Writer) Begin() error {
	if w.tx != nil {
		return fmt.Errorf("sqlite.Writer: Begin called with active tx")
	}
	tx, err := w.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	w.tx = tx
	return nil
}

func (w *Writer) Commit() error {
	if w.tx == nil {
		return fmt.Errorf("sqlite.Writer: Commit without active tx")
	}
	err := w.tx.Commit()
	w.tx = nil
	return err
}

func (w *Writer) Rollback() error {
	if w.tx == nil {
		return nil
	}
	err := w.tx.Rollback()
	w.tx = nil
	return err
}

// UpsertQuestion writes or updates one question, its choices, and its
// discussion rows in the active transaction. Returns the questions.id of the
// (possibly pre-existing) row.
func (w *Writer) UpsertQuestion(rec *QuestionRecord) (int64, error) {
	if w.tx == nil {
		return 0, fmt.Errorf("sqlite.Writer: UpsertQuestion without active tx")
	}

	qImgJSON, err := jsonArray(rec.QuestionImages)
	if err != nil {
		return 0, fmt.Errorf("encode question_images: %w", err)
	}
	aImgJSON, err := jsonArray(rec.AnswerImages)
	if err != nil {
		return 0, fmt.Errorf("encode answer_images: %w", err)
	}
	hash := rec.ContentHash()
	isMC := 0
	if rec.IsMC {
		isMC = 1
	}

	// Upsert questions; English-only columns updated on conflict, *_ja never.
	const upsertQ = `
		INSERT INTO questions (
			exam, topic, question_number, question_text,
			suggested_answer, confirmed_answer, timestamp, url, comments,
			exam_id, is_mc, answer_description, question_images, answer_images,
			content_hash, imported_at
		) VALUES (?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(url) DO UPDATE SET
			exam = excluded.exam,
			-- topic and question_number are write-once: the first import (often
			-- from md_to_sqlite.py) sets the canonical sort-order value. Later
			-- re-imports may carry 0 (cache URLs lack the question segment) and
			-- must not shuffle the UI's listing.
			question_text = excluded.question_text,
			suggested_answer = excluded.suggested_answer,
			confirmed_answer = excluded.confirmed_answer,
			timestamp = excluded.timestamp,
			comments = excluded.comments,
			exam_id = excluded.exam_id,
			is_mc = excluded.is_mc,
			answer_description = excluded.answer_description,
			question_images = excluded.question_images,
			answer_images = excluded.answer_images,
			content_hash = excluded.content_hash,
			imported_at = CURRENT_TIMESTAMP
	`
	if _, err := w.tx.Exec(upsertQ,
		rec.Exam, rec.Topic, rec.QuestionNumber, rec.QuestionText,
		rec.SuggestedAnswer, rec.ConfirmedAnswer, rec.Timestamp, rec.URL, rec.Comments,
		rec.ExamID, isMC, rec.AnswerDescription, qImgJSON, aImgJSON,
		hash,
	); err != nil {
		return 0, fmt.Errorf("upsert question: %w", err)
	}

	var qid int64
	if err := w.tx.QueryRow(`SELECT id FROM questions WHERE url = ?`, rec.URL).Scan(&qid); err != nil {
		return 0, fmt.Errorf("read back qid: %w", err)
	}

	// Choices: upsert per (qid, label) — preserves text_ja.
	const upsertC = `
		INSERT INTO choices (question_id, label, text)
		VALUES (?, ?, ?)
		ON CONFLICT(question_id, label) DO UPDATE SET text = excluded.text
	`
	for label, text := range rec.Choices {
		if _, err := w.tx.Exec(upsertC, qid, label, text); err != nil {
			return 0, fmt.Errorf("upsert choice %s: %w", label, err)
		}
	}

	// Discussion: delete-then-insert (no _ja stored here).
	if _, err := w.tx.Exec(`DELETE FROM discussion WHERE question_id = ?`, qid); err != nil {
		return 0, fmt.Errorf("clear discussion: %w", err)
	}
	const insertD = `
		INSERT INTO discussion (question_id, idx, poster, content, upvote_count, posted_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`
	for _, d := range rec.Discussion {
		if _, err := w.tx.Exec(insertD, qid, d.Idx, nullIfEmpty(d.Poster), d.Content,
			d.UpvoteCount, nullIfEmpty(d.PostedAt)); err != nil {
			return 0, fmt.Errorf("insert discussion idx=%d: %w", d.Idx, err)
		}
	}

	return qid, nil
}

func jsonArray(s []string) (string, error) {
	if s == nil {
		s = []string{}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
