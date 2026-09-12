package translate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"examtopics-downloader/internal/sqlite"
)

// stubAdapter is a deterministic, in-process Adapter for tests. It
// captures the prompt context handed to Run and writes a fixed
// Translation back to OutputPath, mimicking what a real LLM-spawn
// adapter would do without network or subprocess overhead.
type stubAdapter struct {
	out      *Translation
	gotInput *QuestionPayload
	err      error
}

func (s *stubAdapter) Name() string { return "stub" }
func (s *stubAdapter) Run(_ context.Context, opts RunOpts) error {
	s.gotInput = opts.Question
	if s.err != nil {
		return s.err
	}
	if s.out == nil {
		return errors.New("stub: no Translation configured")
	}
	data, err := json.Marshal(s.out)
	if err != nil {
		return err
	}
	return os.WriteFile(opts.OutputPath, data, 0o644)
}

// seedQuestionDB returns a fresh DB with one question + four choices
// suitable for retranslate tests. The schema is provisioned by
// sqlite.OpenWith so any new migrations apply on creation.
func seedQuestionDB(t *testing.T) (*sql.DB, int) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rt.db")
	db, err := sqlite.OpenWith(path, sqlite.OpenOpts{HostID: "test-host"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO questions(id, exam, topic, question_number, question_text, suggested_answer, comments)
		VALUES (1, 'test-exam', 1, 7, 'Which AWS service?', 'C', 'community said C')`); err != nil {
		t.Fatalf("seed question: %v", err)
	}
	for _, c := range []struct {
		label string
		text  string
	}{
		{"A", "S3"},
		{"B", "EBS"},
		{"C", "EFS"},
		{"D", "FSx"},
	} {
		if _, err := db.Exec(`INSERT INTO choices(question_id, label, text) VALUES (?, ?, ?)`, 1, c.label, c.text); err != nil {
			t.Fatalf("seed choice %s: %v", c.label, err)
		}
	}
	return db, 1
}

func TestReadQuestion_PopulatesPayloadFromRow(t *testing.T) {
	db, qid := seedQuestionDB(t)
	p, err := ReadQuestion(db, qid)
	if err != nil {
		t.Fatalf("ReadQuestion: %v", err)
	}
	if p.ID != 1 {
		t.Errorf("id = %d, want 1", p.ID)
	}
	if p.Exam != "test-exam" {
		t.Errorf("exam = %q", p.Exam)
	}
	if len(p.Choices) != 4 {
		t.Fatalf("choices = %d, want 4", len(p.Choices))
	}
	if p.Choices[0].Label != "A" || p.Choices[0].Text != "S3" {
		t.Errorf("first choice = %+v", p.Choices[0])
	}
	if p.Comments != "community said C" {
		t.Errorf("comments = %q", p.Comments)
	}
	if p.ExistingJa.QuestionTextJa != "" {
		t.Errorf("existing question_text_ja should be empty, got %q", p.ExistingJa.QuestionTextJa)
	}
}

func TestReadQuestion_MissingIDIsAnError(t *testing.T) {
	db, _ := seedQuestionDB(t)
	_, err := ReadQuestion(db, 999)
	if err == nil {
		t.Fatal("expected error for missing id, got nil")
	}
}

func TestWriteTranslation_UpdatesAllFields(t *testing.T) {
	db, qid := seedQuestionDB(t)
	tr := &Translation{
		ID:             qid,
		QuestionTextJa: "どの AWS サービス?",
		ExplanationJa:  "EFS は共有ファイルシステムである。",
		ChoicesJa: map[string]string{
			"A": "S3 (オブジェクト)",
			"B": "EBS (ブロック)",
			"C": "EFS (共有ファイル)",
			"D": "FSx",
		},
	}
	if err := WriteTranslation(db, tr); err != nil {
		t.Fatalf("WriteTranslation: %v", err)
	}
	var qja, explja string
	if err := db.QueryRow(`SELECT question_text_ja, explanation_ja FROM questions WHERE id = ?`, qid).
		Scan(&qja, &explja); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if qja != tr.QuestionTextJa || explja != tr.ExplanationJa {
		t.Errorf("question fields not persisted: got (%q, %q)", qja, explja)
	}
	rows, err := db.Query(`SELECT label, text_ja FROM choices WHERE question_id = ? ORDER BY label`, qid)
	if err != nil {
		t.Fatalf("choices readback: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var l, ja string
		if err := rows.Scan(&l, &ja); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[l] = ja
	}
	for k, want := range tr.ChoicesJa {
		if got[k] != want {
			t.Errorf("choice %s = %q, want %q", k, got[k], want)
		}
	}
}

func TestWriteTranslation_SkipsUnknownChoiceLabels(t *testing.T) {
	db, qid := seedQuestionDB(t)
	tr := &Translation{
		ID:             qid,
		QuestionTextJa: "x",
		ExplanationJa:  "y",
		ChoicesJa: map[string]string{
			"A": "ok",
			"Z": "should be ignored — no such label",
		},
	}
	if err := WriteTranslation(db, tr); err != nil {
		t.Fatalf("WriteTranslation: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM choices WHERE question_id = ? AND text_ja IS NOT NULL`, qid).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("translated choice count = %d, want 1 (only A)", n)
	}
}

func TestRetranslate_HappyPath(t *testing.T) {
	db, qid := seedQuestionDB(t)
	stub := &stubAdapter{
		out: &Translation{
			ID:             qid,
			QuestionTextJa: "翻訳後の問題",
			ExplanationJa:  "解説。",
			ChoicesJa:      map[string]string{"A": "選A", "B": "選B", "C": "選C", "D": "選D"},
		},
	}
	got, err := Retranslate(context.Background(), RetranslateOpts{
		DB:      db,
		QID:     qid,
		Adapter: stub,
	})
	if err != nil {
		t.Fatalf("Retranslate: %v", err)
	}
	if got.QuestionTextJa != "翻訳後の問題" {
		t.Errorf("returned QuestionTextJa = %q", got.QuestionTextJa)
	}
	if stub.gotInput == nil || stub.gotInput.ID != qid {
		t.Errorf("adapter did not receive input payload (got %+v)", stub.gotInput)
	}
	// Verify writeback landed.
	var qja string
	if err := db.QueryRow(`SELECT question_text_ja FROM questions WHERE id = ?`, qid).Scan(&qja); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if qja != "翻訳後の問題" {
		t.Errorf("DB question_text_ja = %q", qja)
	}
}

func TestRetranslate_DryRunStopsBeforeAdapter(t *testing.T) {
	db, qid := seedQuestionDB(t)
	stub := &stubAdapter{err: errors.New("adapter must not be called in dry-run")}
	dir := t.TempDir()
	_, err := Retranslate(context.Background(), RetranslateOpts{
		DB: db, QID: qid, Adapter: stub, WorkDir: dir, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	// input.json should exist; adapter should not have been invoked.
	if _, err := os.Stat(filepath.Join(dir, "input.json")); err != nil {
		t.Errorf("dry-run did not write input.json: %v", err)
	}
	if stub.gotInput != nil {
		t.Errorf("adapter was called despite dry-run")
	}
}

func TestRetranslate_AdapterErrorPropagates(t *testing.T) {
	db, qid := seedQuestionDB(t)
	stub := &stubAdapter{err: errors.New("LLM offline")}
	_, err := Retranslate(context.Background(), RetranslateOpts{DB: db, QID: qid, Adapter: stub})
	if err == nil {
		t.Fatal("expected error from adapter, got nil")
	}
}

func TestRetranslate_RejectsMismatchedID(t *testing.T) {
	db, qid := seedQuestionDB(t)
	stub := &stubAdapter{out: &Translation{ID: qid + 100, QuestionTextJa: "x"}}
	_, err := Retranslate(context.Background(), RetranslateOpts{DB: db, QID: qid, Adapter: stub})
	if err == nil {
		t.Fatal("expected mismatched-id error, got nil")
	}
}

func TestAdapterFor_KnownNames(t *testing.T) {
	for _, name := range []string{"claude", "codex", "exec"} {
		a, ok := AdapterFor(name)
		if !ok {
			t.Errorf("AdapterFor(%q) = !ok", name)
		}
		if a.Name() != name {
			t.Errorf("AdapterFor(%q).Name() = %q", name, a.Name())
		}
	}
	if _, ok := AdapterFor("nope"); ok {
		t.Error("AdapterFor(nope) returned ok")
	}
}

func TestExecAdapter_RunsArgvAndPropagatesStagePaths(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "output.json")
	in := filepath.Join(dir, "input.json")
	if err := os.WriteFile(in, []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed input: %v", err)
	}
	// Use the system 'cp' (or copy on Windows) to simulate a translator
	// binary. The exec adapter doesn't shell out, so we hand it argv
	// directly with the staged paths.
	a := &ExecAdapter{Argv: copyArgvFor(t, in, out)}
	if err := a.Run(context.Background(), RunOpts{InputPath: in, OutputPath: out}); err != nil {
		t.Fatalf("exec adapter: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("output not produced: %v", err)
	}
}
