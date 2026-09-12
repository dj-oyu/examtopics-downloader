package sync

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// seedTranslatedQuestion inserts one question row. Empty strings are stored as
// NULL so the "untranslated" state matches what the pipeline writes.
func seedTranslatedQuestion(t *testing.T, db *sql.DB, url, qJa, explJa string) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO questions
		(exam, topic, question_number, question_text, suggested_answer, url, question_text_ja, explanation_ja)
		VALUES('TEST', 1, 1, 'Q', 'A', ?, NULLIF(?, ''), NULLIF(?, ''))`, url, qJa, explJa)
	if err != nil {
		t.Fatalf("seed question %s: %v", url, err)
	}
	qid, _ := res.LastInsertId()
	return int(qid)
}

func seedTranslatedChoice(t *testing.T, db *sql.DB, qid int, label, text, textJa string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO choices(question_id, label, text, text_ja)
		VALUES(?, ?, ?, NULLIF(?, ''))`, qid, label, text, textJa); err != nil {
		t.Fatalf("seed choice %s: %v", label, err)
	}
}

func readQuestionJA(t *testing.T, db *sql.DB, url string) (sql.NullString, sql.NullString) {
	t.Helper()
	var qJa, explJa sql.NullString
	if err := db.QueryRow(`SELECT question_text_ja, explanation_ja FROM questions WHERE url = ?`, url).
		Scan(&qJa, &explJa); err != nil {
		t.Fatalf("read question %s: %v", url, err)
	}
	return qJa, explJa
}

func readChoiceJA(t *testing.T, db *sql.DB, qid int, label string) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT text_ja FROM choices WHERE question_id = ? AND label = ?`, qid, label).
		Scan(&v); err != nil {
		t.Fatalf("read choice %s: %v", label, err)
	}
	return v
}

const urlA = "https://example.test/q/A"
const urlB = "https://example.test/q/B"

func TestTranslationsSync_FillsEmptyLocalFieldsFromPeer(t *testing.T) {
	local, _ := openMigratedDB(t, "local.db")
	peer, peerPath := openMigratedDB(t, "peer.db")

	seedTranslatedQuestion(t, local, urlA, "", "")
	peerQ := seedTranslatedQuestion(t, peer, urlA, "和訳本文", "解説本文")

	res, err := TranslationsSync(local, peerPath, TranslationSyncOptions{})
	if err != nil {
		t.Fatalf("TranslationsSync: %v", err)
	}
	if res.FilledQuestions != 2 {
		t.Errorf("FilledQuestions = %d, want 2", res.FilledQuestions)
	}
	qJa, explJa := readQuestionJA(t, local, urlA)
	if qJa.String != "和訳本文" || explJa.String != "解説本文" {
		t.Errorf("local got (%q, %q), want the peer's wording", qJa.String, explJa.String)
	}
	_ = peerQ
}

func TestTranslationsSync_FillsChoicesByLabel(t *testing.T) {
	local, _ := openMigratedDB(t, "local.db")
	peer, peerPath := openMigratedDB(t, "peer.db")

	localQ := seedTranslatedQuestion(t, local, urlA, "和訳本文", "解説本文")
	peerQ := seedTranslatedQuestion(t, peer, urlA, "和訳本文", "解説本文")
	seedTranslatedChoice(t, local, localQ, "A", "alpha", "")
	seedTranslatedChoice(t, local, localQ, "B", "beta", "")
	seedTranslatedChoice(t, peer, peerQ, "A", "alpha", "選択肢Aの和訳")
	seedTranslatedChoice(t, peer, peerQ, "B", "beta", "")
	seedTranslatedChoice(t, peer, peerQ, "C", "gamma", "ピアだけが持つ選択肢")

	res, err := TranslationsSync(local, peerPath, TranslationSyncOptions{})
	if err != nil {
		t.Fatalf("TranslationsSync: %v", err)
	}
	if res.FilledChoices != 1 {
		t.Errorf("FilledChoices = %d, want 1", res.FilledChoices)
	}
	if got := readChoiceJA(t, local, localQ, "A").String; got != "選択肢Aの和訳" {
		t.Errorf("choice A = %q, want the peer's text", got)
	}
	if got := readChoiceJA(t, local, localQ, "B"); got.Valid {
		t.Errorf("choice B = %q, want it left untranslated (the peer had nothing)", got.String)
	}
	// A choice the peer has and we do not must not be invented.
	var n int
	if err := local.QueryRow(`SELECT COUNT(*) FROM choices WHERE question_id = ?`, localQ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("local choice count = %d, want 2 (no rows invented)", n)
	}
}

func TestTranslationsSync_ReportsConflictsAndKeepsLocal(t *testing.T) {
	local, _ := openMigratedDB(t, "local.db")
	peer, peerPath := openMigratedDB(t, "peer.db")

	seedTranslatedQuestion(t, local, urlA, "こちらの訳", "こちらの解説")
	peerQ := seedTranslatedQuestion(t, peer, urlA, "むこうの訳", "むこうの解説")
	localQ := peerQ
	seedTranslatedChoice(t, local, localQ, "A", "alpha", "こちらA")
	seedTranslatedChoice(t, peer, peerQ, "A", "alpha", "むこうA")

	res, err := TranslationsSync(local, peerPath, TranslationSyncOptions{})
	if err != nil {
		t.Fatalf("TranslationsSync: %v", err)
	}
	if res.FilledQuestions != 0 || res.FilledChoices != 0 {
		t.Errorf("nothing should be filled, got (%d, %d)", res.FilledQuestions, res.FilledChoices)
	}
	if len(res.Conflicts) != 3 {
		t.Fatalf("conflicts = %d, want 3 (two question fields + one choice)", len(res.Conflicts))
	}
	seen := map[string]bool{}
	for _, c := range res.Conflicts {
		seen[c.URL+"|"+c.Field] = true
	}
	for _, want := range []string{urlA + "|question_text_ja", urlA + "|explanation_ja", urlA + "|choice:A"} {
		if !seen[want] {
			t.Errorf("missing conflict %s (got %v)", want, res.Conflicts)
		}
	}
	if qJa, _ := readQuestionJA(t, local, urlA); qJa.String != "こちらの訳" {
		t.Errorf("local wording was overwritten without --prefer peer: %q", qJa.String)
	}
}

func TestTranslationsSync_PreferPeerResolvesConflicts(t *testing.T) {
	local, _ := openMigratedDB(t, "local.db")
	peer, peerPath := openMigratedDB(t, "peer.db")

	seedTranslatedQuestion(t, local, urlA, "こちらの訳", "こちらの解説")
	peerQ := seedTranslatedQuestion(t, peer, urlA, "むこうの訳", "むこうの解説")
	seedTranslatedChoice(t, local, peerQ, "A", "alpha", "こちらA")
	seedTranslatedChoice(t, peer, peerQ, "A", "alpha", "むこうA")

	res, err := TranslationsSync(local, peerPath, TranslationSyncOptions{PreferPeer: true})
	if err != nil {
		t.Fatalf("TranslationsSync: %v", err)
	}
	if res.Overwritten != 3 {
		t.Errorf("Overwritten = %d, want 3", res.Overwritten)
	}
	qJa, explJa := readQuestionJA(t, local, urlA)
	if qJa.String != "むこうの訳" || explJa.String != "むこうの解説" {
		t.Errorf("local = (%q, %q), want the peer's wording", qJa.String, explJa.String)
	}
	if got := readChoiceJA(t, local, peerQ, "A").String; got != "むこうA" {
		t.Errorf("choice A = %q, want the peer's wording", got)
	}
}

func TestTranslationsSync_EqualValuesAreNotConflicts(t *testing.T) {
	local, _ := openMigratedDB(t, "local.db")
	peer, peerPath := openMigratedDB(t, "peer.db")

	seedTranslatedQuestion(t, local, urlA, "同じ訳", "同じ解説")
	peerQ := seedTranslatedQuestion(t, peer, urlA, "同じ訳", "同じ解説")
	seedTranslatedChoice(t, local, peerQ, "A", "alpha", "同じA")
	seedTranslatedChoice(t, peer, peerQ, "A", "alpha", "同じA")

	res, err := TranslationsSync(local, peerPath, TranslationSyncOptions{})
	if err != nil {
		t.Fatalf("TranslationsSync: %v", err)
	}
	if len(res.Conflicts) != 0 || res.FilledQuestions != 0 || res.FilledChoices != 0 {
		t.Errorf("identical wording must be a no-op, got %+v", res)
	}
}

func TestTranslationsSync_DryRunWritesNothing(t *testing.T) {
	local, _ := openMigratedDB(t, "local.db")
	peer, peerPath := openMigratedDB(t, "peer.db")

	seedTranslatedQuestion(t, local, urlA, "", "")
	peerQ := seedTranslatedQuestion(t, peer, urlA, "むこうの訳", "むこうの解説")
	seedTranslatedChoice(t, local, peerQ, "A", "alpha", "")
	seedTranslatedChoice(t, peer, peerQ, "A", "alpha", "むこうA")

	res, err := TranslationsSync(local, peerPath, TranslationSyncOptions{DryRun: true})
	if err != nil {
		t.Fatalf("TranslationsSync: %v", err)
	}
	if res.FilledQuestions != 2 || res.FilledChoices != 1 {
		t.Errorf("dry run should report the pending work, got %+v", res)
	}
	if qJa, _ := readQuestionJA(t, local, urlA); qJa.Valid {
		t.Errorf("dry run wrote question text: %q", qJa.String)
	}
	if got := readChoiceJA(t, local, peerQ, "A"); got.Valid {
		t.Errorf("dry run wrote choice text: %q", got.String)
	}
}

func TestTranslationsSync_MatchesByURLNotRowID(t *testing.T) {
	local, _ := openMigratedDB(t, "local.db")
	peer, peerPath := openMigratedDB(t, "peer.db")

	// Peer's row ids differ: it holds an extra question this DB never scraped.
	seedTranslatedQuestion(t, peer, "https://example.test/q/extra", "余分な訳", "")
	localA := seedTranslatedQuestion(t, local, urlA, "", "")
	peerA := seedTranslatedQuestion(t, peer, urlA, "URLで突き合わせた訳", "")
	if localA == peerA {
		t.Fatalf("fixture is wrong: local and peer row ids both %d", localA)
	}

	res, err := TranslationsSync(local, peerPath, TranslationSyncOptions{})
	if err != nil {
		t.Fatalf("TranslationsSync: %v", err)
	}
	if res.FilledQuestions != 1 {
		t.Errorf("FilledQuestions = %d, want 1", res.FilledQuestions)
	}
	if qJa, _ := readQuestionJA(t, local, urlA); qJa.String != "URLで突き合わせた訳" {
		t.Errorf("url-matched fill failed: %q", qJa.String)
	}
	// The peer-only question must not be imported.
	var n int
	if err := local.QueryRow(`SELECT COUNT(*) FROM questions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("local questions = %d, want 1 (sync translations never adds questions)", n)
	}
}

func TestTranslationsSync_IsIdempotent(t *testing.T) {
	local, _ := openMigratedDB(t, "local.db")
	peer, peerPath := openMigratedDB(t, "peer.db")

	seedTranslatedQuestion(t, local, urlA, "", "")
	seedTranslatedQuestion(t, local, urlB, "こちらの訳", "")
	peerA := seedTranslatedQuestion(t, peer, urlA, "むこうの訳", "むこうの解説")
	seedTranslatedQuestion(t, peer, urlB, "むこうの訳B", "")
	seedTranslatedChoice(t, local, peerA, "A", "alpha", "")
	seedTranslatedChoice(t, peer, peerA, "A", "alpha", "むこうA")

	first, err := TranslationsSync(local, peerPath, TranslationSyncOptions{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	// urlA: two empty question fields filled. urlB: both sides translated the
	// question text differently (a conflict, nothing to fill) and neither wrote
	// an explanation, so the fill count is 2, not 3.
	if first.FilledQuestions != 2 || first.FilledChoices != 1 {
		t.Fatalf("first run filled (%d, %d), want (2, 1)", first.FilledQuestions, first.FilledChoices)
	}
	second, err := TranslationsSync(local, peerPath, TranslationSyncOptions{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.FilledQuestions != 0 || second.FilledChoices != 0 {
		t.Errorf("second run filled (%d, %d), want nothing", second.FilledQuestions, second.FilledChoices)
	}
	// urlB disagreed and stays disagreed — reported both times, never rewritten.
	if len(second.Conflicts) != 1 || !strings.HasSuffix(second.Conflicts[0].URL, "/B") {
		t.Errorf("conflicts on the second run = %v, want the urlB question only", second.Conflicts)
	}
	if qJa, _ := readQuestionJA(t, local, urlB); qJa.String != "こちらの訳" {
		t.Errorf("urlB local wording changed: %q", qJa.String)
	}
}

func TestTranslationsSync_EmptyLocalAndPeerIsNoOp(t *testing.T) {
	local, _ := openMigratedDB(t, "local.db")
	peer, peerPath := openMigratedDB(t, "peer.db")

	seedTranslatedQuestion(t, local, urlA, "", "")
	seedTranslatedQuestion(t, peer, urlA, "", "")

	res, err := TranslationsSync(local, peerPath, TranslationSyncOptions{})
	if err != nil {
		t.Fatalf("TranslationsSync: %v", err)
	}
	if res.FilledQuestions != 0 || res.FilledChoices != 0 || len(res.Conflicts) != 0 {
		t.Errorf("both sides untranslated must be a no-op, got %+v", res)
	}
}

func TestTranslationsSync_RejectsAPeerWithoutContentTables(t *testing.T) {
	local, _ := openMigratedDB(t, "local.db")
	seedTranslatedQuestion(t, local, urlA, "", "")

	// A bare SQLite file: no questions table to pull from.
	bare := filepath.Join(t.TempDir(), "bare.db")
	raw, err := sql.Open("sqlite", bare)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE unrelated(x INTEGER)`); err != nil {
		t.Fatalf("seed bare: %v", err)
	}
	_ = raw.Close()

	if _, err := TranslationsSync(local, bare, TranslationSyncOptions{}); err == nil {
		t.Fatal("expected an error for a peer without questions/choices, got nil")
	}
}
