package translate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"examtopics-downloader/internal/sqlite"
	"examtopics-downloader/internal/uuidx"
)

const testHostID = "test-host"

// stubExplainAdapter is the explain-side analogue of stubAdapter: it
// captures the ExplainRunOpts the orchestrator hands it, writes a
// fixed reply payload to OutputPath, and returns a configurable
// SessionID — enough to drive RunExplain end-to-end without spawning
// a real LLM.
type stubExplainAdapter struct {
	out       *Reply
	gotInput  *ThreadPayload
	gotResume string
	sessionID string
	err       error
}

func (s *stubExplainAdapter) Name() string { return "stub-explain" }
func (s *stubExplainAdapter) RunExplain(_ context.Context, opts ExplainRunOpts) (ExplainRunResult, error) {
	s.gotInput = opts.Thread
	s.gotResume = opts.SessionID
	if s.err != nil {
		return ExplainRunResult{}, s.err
	}
	if s.out == nil {
		return ExplainRunResult{}, errors.New("stub: no Reply configured")
	}
	data, err := json.Marshal(s.out)
	if err != nil {
		return ExplainRunResult{}, err
	}
	if err := os.WriteFile(opts.OutputPath, data, 0o644); err != nil {
		return ExplainRunResult{}, err
	}
	return ExplainRunResult{SessionID: s.sessionID}, nil
}

// seedThreadDB returns a fresh DB with one question/choices/thread/user
// message, suitable as the input for an explain reply round-trip. The
// returned thread id is the 26-char Crockford base32 form a CLI / URL
// would receive.
func seedThreadDB(t *testing.T) (db *sql.DB, qid int64, tidStr string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "explain.db")
	db, err := sqlite.OpenWith(path, sqlite.OpenOpts{HostID: testHostID})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	res, err := db.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, comments, question_text_ja, explanation_ja)
		VALUES ('test-exam', 1, 7, 'Which AWS service?', 'C', 'community said C', 'どの AWS サービス?', '初期解説')`)
	if err != nil {
		t.Fatalf("seed question: %v", err)
	}
	qid, err = res.LastInsertId()
	if err != nil {
		t.Fatalf("last id: %v", err)
	}
	for _, c := range []struct {
		label, text, textJa string
	}{
		{"A", "S3", "オブジェクト"},
		{"B", "EBS", "ブロック"},
		{"C", "EFS", "ファイル"},
		{"D", "FSx", "高機能ファイル"},
	} {
		if _, err := db.Exec(`INSERT INTO choices(question_id, label, text, text_ja) VALUES (?, ?, ?, ?)`,
			qid, c.label, c.text, c.textJa); err != nil {
			t.Fatalf("seed choice %s: %v", c.label, err)
		}
	}

	tid := uuidx.MustNew()
	tidStr = uuidx.Encode(tid)
	now := nowUTC()
	if _, err := db.Exec(`INSERT INTO explanation_threads(id, question_id, status, created_at, updated_at, host_id)
		VALUES (?, ?, 'open', ?, ?, ?)`, tid[:], qid, now, now, testHostID); err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	mid := uuidx.MustNew()
	if _, err := db.Exec(`INSERT INTO explanation_messages(id, thread_id, role, author, content, created_at, host_id)
		VALUES (?, ?, 'user', ?, ?, ?, ?)`, mid[:], tid[:], "web-user", "なぜ EFS なのか?", now, testHostID); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	return db, qid, tidStr
}

func TestReadThread_PopulatesPayload(t *testing.T) {
	db, qid, tidStr := seedThreadDB(t)
	p, err := ReadThread(db, tidStr)
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	if p.ThreadID != tidStr {
		t.Errorf("thread_id = %q, want %q", p.ThreadID, tidStr)
	}
	if p.Status != "open" {
		t.Errorf("status = %q, want open", p.Status)
	}
	if p.Question.ID != qid {
		t.Errorf("question.id = %d, want %d", p.Question.ID, qid)
	}
	if p.Question.QuestionTextJa == nil || *p.Question.QuestionTextJa != "どの AWS サービス?" {
		t.Errorf("question.question_text_ja = %v", p.Question.QuestionTextJa)
	}
	if len(p.Question.Choices) != 4 {
		t.Fatalf("choices = %d, want 4", len(p.Question.Choices))
	}
	if p.Question.Choices[0].Label != "A" || p.Question.Choices[0].TextJa == nil || *p.Question.Choices[0].TextJa != "オブジェクト" {
		t.Errorf("first choice = %+v / textJa=%v", p.Question.Choices[0], p.Question.Choices[0].TextJa)
	}
	if len(p.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(p.Messages))
	}
	if p.Messages[0].Role != "user" || p.Messages[0].Content != "なぜ EFS なのか?" {
		t.Errorf("first message = %+v", p.Messages[0])
	}
	if len(p.Messages[0].ID) != 26 {
		t.Errorf("message id should be 26-char base32, got %q", p.Messages[0].ID)
	}
}

func TestReadThread_MissingTIDError(t *testing.T) {
	db, _, _ := seedThreadDB(t)
	bogus := uuidx.Encode(uuidx.MustNew())
	if _, err := ReadThread(db, bogus); err == nil {
		t.Fatal("expected error for missing thread, got nil")
	}
}

func TestValidateReply_UnknownReasonCode(t *testing.T) {
	err := ValidateReply(&Reply{Content: "ok", ReasonCode: "weird"})
	if err == nil || !strings.Contains(err.Error(), "reason_code must be one of") {
		t.Errorf("unknown reason_code: err=%v", err)
	}
}

func TestValidateReply_SpecRequiresCitations(t *testing.T) {
	err := ValidateReply(&Reply{Content: "ok", ReasonCode: "spec"})
	if err == nil || !strings.Contains(err.Error(), "non-empty 'citations'") {
		t.Errorf("spec without citations: err=%v", err)
	}
}

func TestValidateReply_AmbiguousRequiresAWSDocsURL(t *testing.T) {
	err := ValidateReply(&Reply{
		Content:    "ok",
		ReasonCode: "ambiguous",
		Citations:  []Citation{{URL: "https://example.com/foo"}},
	})
	if err == nil || !strings.Contains(err.Error(), "docs.aws.amazon.com") {
		t.Errorf("ambiguous w/o aws docs url: err=%v", err)
	}
}

func TestValidateReply_TranslationRequiresFix(t *testing.T) {
	err := ValidateReply(&Reply{Content: "ok", ReasonCode: "translation"})
	if err == nil || !strings.Contains(err.Error(), "translation_fix") {
		t.Errorf("translation w/o fix: err=%v", err)
	}
}

func TestValidateReply_TranslationFixMustHaveAtLeastOneKey(t *testing.T) {
	err := ValidateReply(&Reply{
		Content:        "ok",
		ReasonCode:     "translation",
		TranslationFix: &TranslationFix{},
	})
	if err == nil || !strings.Contains(err.Error(), "translation_fix") {
		t.Errorf("empty translation_fix: err=%v", err)
	}
}

func TestValidateReply_ChoicesJaTypeIsAlwaysObject(t *testing.T) {
	// In Go the typed map[string]string already enforces "object"
	// shape — we exercise the happy path here so the contract is
	// pinned in tests rather than implied.
	err := ValidateReply(&Reply{
		Content:    "ok",
		ReasonCode: "translation",
		TranslationFix: &TranslationFix{
			ChoicesJa: map[string]string{"A": "ok"},
		},
	})
	if err != nil {
		t.Errorf("choices_ja as object should pass: %v", err)
	}
}

func TestValidateReply_ContentRequiredWhenReasonSet(t *testing.T) {
	tStr := "fix"
	err := ValidateReply(&Reply{
		ReasonCode: "translation",
		TranslationFix: &TranslationFix{
			QuestionTextJa: &tStr,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "'content' is required") {
		t.Errorf("missing content: err=%v", err)
	}
}

func TestValidateReply_NoReasonRequiresContentOrExpl(t *testing.T) {
	err := ValidateReply(&Reply{})
	if err == nil || !strings.Contains(err.Error(), "must include") {
		t.Errorf("empty payload: err=%v", err)
	}
	if err := ValidateReply(&Reply{ExplanationJa: "解説"}); err != nil {
		t.Errorf("legacy explanation_ja path should be allowed: %v", err)
	}
}

func TestApplyTranslationFix_UpdatesAndReturnsDiff(t *testing.T) {
	db, qid, _ := seedThreadDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	newQText := "修正後の問題文"
	fix := &TranslationFix{
		QuestionTextJa: &newQText,
		ChoicesJa: map[string]string{
			"A": "修正後 A",
			"C": "修正後 C",
		},
	}
	diff, err := ApplyTranslationFix(tx, qid, fix)
	if err != nil {
		t.Fatalf("ApplyTranslationFix: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Diff JSON shape: {before, after}
	var parsed struct {
		Before map[string]any `json:"before"`
		After  map[string]any `json:"after"`
	}
	if err := json.Unmarshal([]byte(diff), &parsed); err != nil {
		t.Fatalf("parse diff: %v\n%s", err, diff)
	}
	if parsed.Before["question_text_ja"] != "どの AWS サービス?" {
		t.Errorf("before.question_text_ja = %v", parsed.Before["question_text_ja"])
	}
	if parsed.After["question_text_ja"] != newQText {
		t.Errorf("after.question_text_ja = %v", parsed.After["question_text_ja"])
	}
	beforeChoices, _ := parsed.Before["choices_ja"].(map[string]any)
	if beforeChoices["A"] != "オブジェクト" {
		t.Errorf("before.choices_ja[A] = %v", beforeChoices["A"])
	}
	afterChoices, _ := parsed.After["choices_ja"].(map[string]any)
	if afterChoices["A"] != "修正後 A" || afterChoices["C"] != "修正後 C" {
		t.Errorf("after.choices_ja = %v", afterChoices)
	}

	// Verify DB writeback.
	var qja string
	if err := db.QueryRow(`SELECT question_text_ja FROM questions WHERE id = ?`, qid).Scan(&qja); err != nil {
		t.Fatalf("readback question_text_ja: %v", err)
	}
	if qja != newQText {
		t.Errorf("DB question_text_ja = %q, want %q", qja, newQText)
	}
	var aText, cText string
	if err := db.QueryRow(`SELECT text_ja FROM choices WHERE question_id = ? AND label = 'A'`, qid).Scan(&aText); err != nil {
		t.Fatalf("readback A: %v", err)
	}
	if aText != "修正後 A" {
		t.Errorf("choice A text_ja = %q", aText)
	}
	if err := db.QueryRow(`SELECT text_ja FROM choices WHERE question_id = ? AND label = 'C'`, qid).Scan(&cText); err != nil {
		t.Fatalf("readback C: %v", err)
	}
	if cText != "修正後 C" {
		t.Errorf("choice C text_ja = %q", cText)
	}
}

func TestWriteReply_PlainContentMessage(t *testing.T) {
	db, _, tidStr := seedThreadDB(t)
	r := &Reply{Content: "解説します", Author: "claude-code"}
	if err := WriteReply(db, tidStr, r, testHostID, ""); err != nil {
		t.Fatalf("WriteReply: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM explanation_messages WHERE role = 'agent'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("agent message count = %d, want 1", n)
	}
	var content, author, hostID string
	var reasonCode, citations sql.NullString
	if err := db.QueryRow(`SELECT content, author, reason_code, citations, host_id FROM explanation_messages WHERE role = 'agent'`).
		Scan(&content, &author, &reasonCode, &citations, &hostID); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if content != "解説します" || author != "claude-code" || hostID != testHostID {
		t.Errorf("readback row = %q/%q/%q", content, author, hostID)
	}
	if reasonCode.Valid {
		t.Errorf("reason_code should be NULL, got %q", reasonCode.String)
	}
	if citations.Valid {
		t.Errorf("citations should be NULL, got %q", citations.String)
	}
}

func TestWriteReply_SpecPersistsCitations(t *testing.T) {
	db, _, tidStr := seedThreadDB(t)
	title := "ALB rules"
	r := &Reply{
		Content:    "ALB は top-down 評価。",
		Author:     "claude-code",
		ReasonCode: "spec",
		Citations: []Citation{
			{URL: "https://docs.aws.amazon.com/elasticloadbalancing/latest/application/listener-update-rules.html", Title: &title},
		},
	}
	if err := WriteReply(db, tidStr, r, testHostID, ""); err != nil {
		t.Fatalf("WriteReply: %v", err)
	}
	var citations string
	if err := db.QueryRow(`SELECT citations FROM explanation_messages WHERE role = 'agent'`).Scan(&citations); err != nil {
		t.Fatalf("read citations: %v", err)
	}
	if !strings.Contains(citations, "docs.aws.amazon.com") || !strings.Contains(citations, "ALB rules") {
		t.Errorf("citations JSON missing url/title: %q", citations)
	}
}

func TestWriteReply_TranslationAppliesFixAndStoresDiff(t *testing.T) {
	db, qid, tidStr := seedThreadDB(t)
	newQ := "修正後の問題"
	r := &Reply{
		Content:    "翻訳を直しました。",
		Author:     "claude-code",
		ReasonCode: "translation",
		TranslationFix: &TranslationFix{
			QuestionTextJa: &newQ,
			ChoicesJa:      map[string]string{"A": "新A"},
		},
	}
	if err := WriteReply(db, tidStr, r, testHostID, ""); err != nil {
		t.Fatalf("WriteReply: %v", err)
	}
	var qja string
	if err := db.QueryRow(`SELECT question_text_ja FROM questions WHERE id = ?`, qid).Scan(&qja); err != nil {
		t.Fatalf("readback q: %v", err)
	}
	if qja != newQ {
		t.Errorf("question_text_ja = %q, want %q", qja, newQ)
	}
	var diff string
	if err := db.QueryRow(`SELECT translation_diff FROM explanation_messages WHERE role = 'agent'`).Scan(&diff); err != nil {
		t.Fatalf("read diff: %v", err)
	}
	if !strings.Contains(diff, "before") || !strings.Contains(diff, "after") {
		t.Errorf("diff JSON missing before/after: %q", diff)
	}
}

func TestWriteReply_ResolveClosesThread(t *testing.T) {
	db, _, tidStr := seedThreadDB(t)
	r := &Reply{Content: "回答", Author: "claude-code", Resolve: true}
	if err := WriteReply(db, tidStr, r, testHostID, ""); err != nil {
		t.Fatalf("WriteReply: %v", err)
	}
	tidBytes, _ := uuidx.Decode(tidStr)
	var status string
	var closedAt sql.NullString
	if err := db.QueryRow(`SELECT status, closed_at FROM explanation_threads WHERE id = ?`, tidBytes[:]).
		Scan(&status, &closedAt); err != nil {
		t.Fatalf("read thread: %v", err)
	}
	if status != "resolved" {
		t.Errorf("status = %q, want resolved", status)
	}
	if !closedAt.Valid || closedAt.String == "" {
		t.Errorf("closed_at should be set, got %+v", closedAt)
	}
}

func TestWriteReply_SessionIDWriteback(t *testing.T) {
	db, _, tidStr := seedThreadDB(t)
	r := &Reply{Content: "test", Author: "claude-code"}
	if err := WriteReply(db, tidStr, r, testHostID, "sess-123"); err != nil {
		t.Fatalf("WriteReply: %v", err)
	}
	tidBytes, _ := uuidx.Decode(tidStr)
	var ns sql.NullString
	if err := db.QueryRow(`SELECT agent_session_id FROM explanation_threads WHERE id = ?`, tidBytes[:]).Scan(&ns); err != nil {
		t.Fatalf("read session id: %v", err)
	}
	if !ns.Valid || ns.String != "sess-123" {
		t.Errorf("agent_session_id = %+v, want sess-123", ns)
	}
}

func TestWriteReply_RejectsNonOpenThread(t *testing.T) {
	db, _, tidStr := seedThreadDB(t)
	tidBytes, _ := uuidx.Decode(tidStr)
	if _, err := db.Exec(`UPDATE explanation_threads SET status = 'resolved' WHERE id = ?`, tidBytes[:]); err != nil {
		t.Fatalf("seed close: %v", err)
	}
	err := WriteReply(db, tidStr, &Reply{Content: "x", Author: "a"}, testHostID, "")
	if err == nil || !strings.Contains(err.Error(), "not open") {
		t.Errorf("expected not-open error, got %v", err)
	}
}

func TestWriteReply_LegacyExplanationJaUpdatesQuestion(t *testing.T) {
	db, qid, tidStr := seedThreadDB(t)
	r := &Reply{
		Content:       "legacy path",
		Author:        "claude-code",
		ExplanationJa: "新しい解説",
	}
	if err := WriteReply(db, tidStr, r, testHostID, ""); err != nil {
		t.Fatalf("WriteReply: %v", err)
	}
	var ja string
	if err := db.QueryRow(`SELECT explanation_ja FROM questions WHERE id = ?`, qid).Scan(&ja); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if ja != "新しい解説" {
		t.Errorf("explanation_ja = %q, want 新しい解説", ja)
	}
}

func TestRunExplain_HappyPath(t *testing.T) {
	db, _, tidStr := seedThreadDB(t)
	stub := &stubExplainAdapter{
		out: &Reply{
			ThreadID:   tidStr,
			Content:    "回答です",
			Author:     "claude-code",
			ReasonCode: "comprehension",
		},
		sessionID: "sess-ABC",
	}
	got, err := RunExplain(context.Background(), ExplainOpts{
		DB:      db,
		TID:     tidStr,
		HostID:  testHostID,
		Adapter: stub,
	})
	if err != nil {
		t.Fatalf("RunExplain: %v", err)
	}
	if got.Content != "回答です" {
		t.Errorf("content = %q", got.Content)
	}
	if stub.gotInput == nil || stub.gotInput.ThreadID != tidStr {
		t.Errorf("adapter did not receive input thread; got %+v", stub.gotInput)
	}
	// Session id should be persisted.
	tidBytes, _ := uuidx.Decode(tidStr)
	var ns sql.NullString
	if err := db.QueryRow(`SELECT agent_session_id FROM explanation_threads WHERE id = ?`, tidBytes[:]).Scan(&ns); err != nil {
		t.Fatalf("read session id: %v", err)
	}
	if !ns.Valid || ns.String != "sess-ABC" {
		t.Errorf("agent_session_id = %+v, want sess-ABC", ns)
	}
}

func TestRunExplain_RejectsMismatchedThreadID(t *testing.T) {
	db, _, tidStr := seedThreadDB(t)
	other := uuidx.Encode(uuidx.MustNew())
	stub := &stubExplainAdapter{out: &Reply{ThreadID: other, Content: "x", Author: "a"}}
	_, err := RunExplain(context.Background(), ExplainOpts{
		DB:      db,
		TID:     tidStr,
		HostID:  testHostID,
		Adapter: stub,
	})
	if err == nil || !strings.Contains(err.Error(), "thread_id") {
		t.Errorf("expected thread_id mismatch, got %v", err)
	}
}

func TestRunExplain_DryRunStopsBeforeAdapter(t *testing.T) {
	db, _, tidStr := seedThreadDB(t)
	stub := &stubExplainAdapter{err: errors.New("must not run")}
	dir := t.TempDir()
	_, err := RunExplain(context.Background(), ExplainOpts{
		DB:      db,
		TID:     tidStr,
		HostID:  testHostID,
		Adapter: stub,
		WorkDir: dir,
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "input.json")); err != nil {
		t.Errorf("dry-run did not write input.json: %v", err)
	}
	if stub.gotInput != nil {
		t.Errorf("adapter was called despite dry-run")
	}
}

func TestRunExplain_ResumesOnPriorSessionID(t *testing.T) {
	db, _, tidStr := seedThreadDB(t)
	tidBytes, _ := uuidx.Decode(tidStr)
	if _, err := db.Exec(`UPDATE explanation_threads SET agent_session_id = ? WHERE id = ?`, "prior-sess", tidBytes[:]); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	stub := &stubExplainAdapter{
		out:       &Reply{ThreadID: tidStr, Content: "ok", Author: "a"},
		sessionID: "fresh-sess",
	}
	if _, err := RunExplain(context.Background(), ExplainOpts{
		DB: db, TID: tidStr, HostID: testHostID, Adapter: stub,
	}); err != nil {
		t.Fatalf("RunExplain: %v", err)
	}
	if stub.gotResume != "prior-sess" {
		t.Errorf("adapter saw resume id %q, want %q", stub.gotResume, "prior-sess")
	}
}

func TestExplainAdapterFor_KnownNames(t *testing.T) {
	for _, name := range []string{"claude", "codex", "exec"} {
		a, ok := ExplainAdapterFor(name)
		if !ok {
			t.Errorf("ExplainAdapterFor(%q) = !ok", name)
		}
		if a.Name() != name {
			t.Errorf("ExplainAdapterFor(%q).Name() = %q", name, a.Name())
		}
	}
	if _, ok := ExplainAdapterFor("nope"); ok {
		t.Error("ExplainAdapterFor(nope) returned ok")
	}
}

func TestExtractClaudeSessionID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"whole-stdout-json", `{"session_id":"abc","other":1}`, "abc"},
		{"last-line-json", "garbage line\n{\"session_id\":\"xyz\"}", "xyz"},
		{"missing", `{"foo":"bar"}`, ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractClaudeSessionID(c.in); got != c.want {
				t.Errorf("extractClaudeSessionID(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Stub-explain adapters of non-claude clients should fail with a
// "not yet wired" error so a misconfigured `-client` exits
// non-zero rather than silently producing an empty reply.
func TestExplainAdapterFor_CodexExecErrorOnRun(t *testing.T) {
	for _, name := range []string{"codex", "exec"} {
		a, ok := ExplainAdapterFor(name)
		if !ok {
			t.Fatalf("ExplainAdapterFor(%q) ok=false", name)
		}
		_, err := a.RunExplain(context.Background(), ExplainRunOpts{})
		if err == nil {
			t.Errorf("%s adapter should error, got nil", name)
		}
	}
}
