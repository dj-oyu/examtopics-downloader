package translate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// QuestionPayload is the shape sent to the LLM adapter as input JSON.
// Field names mirror tools/translate.py fetch_payload so existing
// prompts and tests that key on those names keep working unchanged.
type QuestionPayload struct {
	ID              int                 `json:"id"`
	Exam            string              `json:"exam"`
	Topic           int                 `json:"topic"`
	QuestionNumber  int                 `json:"question_number"`
	URL             string              `json:"url,omitempty"`
	SuggestedAnswer string              `json:"suggested_answer"`
	ConfirmedAnswer string              `json:"confirmed_answer,omitempty"`
	QuestionText    string              `json:"question_text"`
	Choices         []ChoicePayload     `json:"choices"`
	Comments        string              `json:"comments,omitempty"`
	ExistingJa      ExistingTranslation `json:"existing_ja"`
}

type ChoicePayload struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

type ExistingTranslation struct {
	QuestionTextJa string            `json:"question_text_ja,omitempty"`
	ExplanationJa  string            `json:"explanation_ja,omitempty"`
	ChoicesJa      map[string]string `json:"choices_ja,omitempty"`
}

// Translation is the shape the adapter writes back to OutputPath. Only
// fields populated by the LLM are honoured — missing entries are
// treated as "leave the existing column NULL". choices_ja keys must
// match an existing choice label or that label is silently skipped.
type Translation struct {
	ID             int               `json:"id"`
	QuestionTextJa string            `json:"question_text_ja"`
	ExplanationJa  string            `json:"explanation_ja"`
	ChoicesJa      map[string]string `json:"choices_ja"`
}

// ReadQuestion fetches the question + choices needed to build the
// translator prompt. Mirrors tools/translate.py fetch_payload —
// includes existing_ja so the LLM can see prior translations and
// avoid regressions on partial retranslates.
func ReadQuestion(db *sql.DB, qid int) (*QuestionPayload, error) {
	var p QuestionPayload
	var url, confirmed, comments, qtxja, explja sql.NullString
	err := db.QueryRow(`
		SELECT id, exam, topic, question_number, url, suggested_answer,
		       confirmed_answer, question_text, comments,
		       question_text_ja, explanation_ja
		  FROM questions WHERE id = ?`, qid).Scan(
		&p.ID, &p.Exam, &p.Topic, &p.QuestionNumber, &url, &p.SuggestedAnswer,
		&confirmed, &p.QuestionText, &comments, &qtxja, &explja,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("read question %d: not found", qid)
		}
		return nil, fmt.Errorf("read question %d: %w", qid, err)
	}
	p.URL = url.String
	p.ConfirmedAnswer = confirmed.String
	p.Comments = comments.String
	p.ExistingJa.QuestionTextJa = qtxja.String
	p.ExistingJa.ExplanationJa = explja.String

	rows, err := db.Query(`SELECT label, text, text_ja FROM choices WHERE question_id = ? ORDER BY label`, qid)
	if err != nil {
		return nil, fmt.Errorf("read choices for %d: %w", qid, err)
	}
	defer func() { _ = rows.Close() }()
	choicesJa := map[string]string{}
	for rows.Next() {
		var c ChoicePayload
		var ja sql.NullString
		if err := rows.Scan(&c.Label, &c.Text, &ja); err != nil {
			return nil, fmt.Errorf("scan choice: %w", err)
		}
		p.Choices = append(p.Choices, c)
		if ja.Valid && ja.String != "" {
			choicesJa[c.Label] = ja.String
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(choicesJa) > 0 {
		p.ExistingJa.ChoicesJa = choicesJa
	}
	return &p, nil
}

// WriteTranslation applies a Translation to the DB. UPDATE-only, so
// callers that want a fresh start should clear existing_ja columns
// first (web's clearQuestionTranslation already does this for the
// retranslate flow). Choice labels not present in choices(question_id)
// are silently dropped to match the Python tool's tolerance.
func WriteTranslation(db *sql.DB, t *Translation) error {
	if t == nil {
		return errors.New("write translation: nil payload")
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if t.QuestionTextJa != "" {
		if _, err := tx.Exec(`UPDATE questions SET question_text_ja = ? WHERE id = ?`, t.QuestionTextJa, t.ID); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("update question_text_ja: %w", err)
		}
	}
	if t.ExplanationJa != "" {
		if _, err := tx.Exec(`UPDATE questions SET explanation_ja = ? WHERE id = ?`, t.ExplanationJa, t.ID); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("update explanation_ja: %w", err)
		}
	}
	// Sort labels so the UPDATE order is deterministic — useful for
	// tests and for `git diff`-style row-by-row inspection on a busy DB.
	labels := make([]string, 0, len(t.ChoicesJa))
	for k := range t.ChoicesJa {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	for _, label := range labels {
		text := t.ChoicesJa[label]
		if text == "" {
			continue
		}
		if _, err := tx.Exec(`UPDATE choices SET text_ja = ? WHERE question_id = ? AND label = ?`, text, t.ID, label); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("update choice %s: %w", label, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// RetranslateOpts bundles the inputs needed to translate one row via
// a chosen Adapter. WorkDir is the temp directory where input/output
// JSON files live for the duration of the call; if empty Retranslate
// allocates one under os.TempDir().
type RetranslateOpts struct {
	DB      *sql.DB
	QID     int
	Adapter Adapter
	WorkDir string
	DryRun  bool
}

// Retranslate reads question qid from db, hands it off to the
// configured Adapter (which produces Japanese translations as JSON in
// a file the adapter writes), then UPDATEs the *_ja columns on
// success. WorkDir holds the input/output JSON files; on a clean run
// they are removed before return.
//
// DryRun stops after writing the input file — useful for inspecting
// the prompt the adapter would receive without burning LLM tokens.
func Retranslate(ctx context.Context, opts RetranslateOpts) (*Translation, error) {
	if opts.DB == nil {
		return nil, errors.New("retranslate: DB is required")
	}
	if opts.Adapter == nil {
		return nil, errors.New("retranslate: Adapter is required")
	}

	q, err := ReadQuestion(opts.DB, opts.QID)
	if err != nil {
		return nil, err
	}

	dir := opts.WorkDir
	if dir == "" {
		d, err := os.MkdirTemp("", "examtopics-retranslate-")
		if err != nil {
			return nil, fmt.Errorf("mkdir temp: %w", err)
		}
		dir = d
		defer func() { _ = os.RemoveAll(dir) }()
	}

	inputPath := filepath.Join(dir, "input.json")
	outputPath := filepath.Join(dir, "output.json")
	inJSON, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal input: %w", err)
	}
	if err := os.WriteFile(inputPath, inJSON, 0o644); err != nil {
		return nil, fmt.Errorf("write input: %w", err)
	}

	if opts.DryRun {
		return nil, nil
	}

	runOpts := RunOpts{
		InputPath:  inputPath,
		OutputPath: outputPath,
		Question:   q,
	}
	if err := opts.Adapter.Run(ctx, runOpts); err != nil {
		return nil, fmt.Errorf("adapter %s run: %w", opts.Adapter.Name(), err)
	}

	outBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read adapter output %s: %w", outputPath, err)
	}
	var t Translation
	if err := json.Unmarshal(outBytes, &t); err != nil {
		return nil, fmt.Errorf("parse adapter output: %w", err)
	}
	if t.ID != q.ID {
		return nil, fmt.Errorf("adapter output id=%d, expected %d", t.ID, q.ID)
	}

	if err := WriteTranslation(opts.DB, &t); err != nil {
		return nil, err
	}
	return &t, nil
}
