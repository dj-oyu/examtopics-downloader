package quiz

import (
	"database/sql"
	"path/filepath"
	"testing"

	"examtopics-downloader/internal/sqlite"
	_ "modernc.org/sqlite"
)

// seedQuestion inserts a single question + its choices into a freshly
// migrated DB so the quiz layer has something to read and attempt against.
func seedQuestion(t *testing.T, db *sql.DB, examName, qtext, answer string, choices map[string]string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url) VALUES(?, 1, 1, ?, ?, ?)`,
		examName, qtext, answer, "https://example.test/q/"+qtext)
	if err != nil {
		t.Fatalf("insert question: %v", err)
	}
	qid, _ := res.LastInsertId()
	for label, text := range choices {
		if _, err := db.Exec(`INSERT INTO choices(question_id, label, text) VALUES(?, ?, ?)`, qid, label, text); err != nil {
			t.Fatalf("insert choice %s: %v", label, err)
		}
	}
	return qid
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quiz.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRecordAttempt_InsertsRowWithUUIDAndHostID(t *testing.T) {
	db := openTestDB(t)
	qid := seedQuestion(t, db, "TEST", "Q1", "A", map[string]string{"A": "right", "B": "wrong"})
	correct, err := RecordAttempt(db, "test-host", int(qid), "A", "A")
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if !correct {
		t.Errorf("answer A == correct A, want correct=true")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE question_id=? AND host_id='test-host'`, qid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("attempts row count = %d, want 1", n)
	}
	// id is BLOB(16) - verify length
	var idLen int
	if err := db.QueryRow(`SELECT length(id) FROM attempts WHERE question_id=?`, qid).Scan(&idLen); err != nil {
		t.Fatal(err)
	}
	if idLen != 16 {
		t.Errorf("attempts.id length = %d bytes, want 16 (UUIDv7 BLOB)", idLen)
	}
}

func TestRecordAttempt_WrongAnswerStillRecordsButFlagsIncorrect(t *testing.T) {
	db := openTestDB(t)
	qid := seedQuestion(t, db, "TEST", "Q1", "B", map[string]string{"A": "x", "B": "y"})
	correct, err := RecordAttempt(db, "test-host", int(qid), "A", "B")
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if correct {
		t.Errorf("answer A != correct B, want correct=false")
	}
	var got int
	if err := db.QueryRow(`SELECT is_correct FROM attempts WHERE question_id=?`, qid).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("is_correct = %d, want 0", got)
	}
}

func TestRecordAttempt_MultiLetterAnswerIsOrderInsensitive(t *testing.T) {
	db := openTestDB(t)
	qid := seedQuestion(t, db, "TEST", "Q1", "BD", map[string]string{"A": "x", "B": "y", "C": "z", "D": "w"})
	correct, err := RecordAttempt(db, "test-host", int(qid), "DB", "BD")
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if !correct {
		t.Errorf("answer DB should match correct BD (order-insensitive), got correct=false")
	}
}

func TestRecordAttempt_GeneratesUniqueIDs(t *testing.T) {
	db := openTestDB(t)
	qid := seedQuestion(t, db, "TEST", "Q1", "A", map[string]string{"A": "x"})
	for range 5 {
		if _, err := RecordAttempt(db, "test-host", int(qid), "A", "A"); err != nil {
			t.Fatalf("RecordAttempt: %v", err)
		}
	}
	var distinct int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT id) FROM attempts WHERE question_id=?`, qid).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != 5 {
		t.Errorf("distinct attempt ids = %d, want 5 (UUIDv7 collisions)", distinct)
	}
}

func TestLoadQuestions_ReturnsSeededRows(t *testing.T) {
	db := openTestDB(t)
	seedQuestion(t, db, "TEST", "Q1", "A", map[string]string{"A": "right", "B": "wrong"})
	seedQuestion(t, db, "TEST", "Q2", "B", map[string]string{"A": "wrong", "B": "right"})
	qs, err := LoadQuestions(db)
	if err != nil {
		t.Fatalf("LoadQuestions: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("loaded %d questions, want 2", len(qs))
	}
	if qs[0].SuggestedAnswer != "A" || qs[1].SuggestedAnswer != "B" {
		t.Errorf("unexpected answers: %+v", qs)
	}
	if len(qs[0].Choices) != 2 {
		t.Errorf("Q1 choices = %d, want 2", len(qs[0].Choices))
	}
}

// --- answer verdicts (migration 004 / tools/answer_verdict.py) -------------

func TestIsAccepted_MatchesEverySideOfTheArgument(t *testing.T) {
	accepted := []string{"A", "D"}
	for _, tc := range []struct {
		selected string
		want     bool
	}{
		{"A", true},
		{"D", true},
		{"d", true}, // case-insensitive
		{"B", false},
		{"", false}, // an empty selection is never right
	} {
		if got := IsAccepted(tc.selected, accepted); got != tc.want {
			t.Errorf("IsAccepted(%q, %v) = %v, want %v", tc.selected, accepted, got, tc.want)
		}
	}
	if !IsAccepted("DA", []string{"AD"}) {
		t.Error("letter order must not matter")
	}
}

func TestLoadQuestions_CarriesTheVerdict(t *testing.T) {
	db := openTestDB(t)
	qid := seedQuestion(t, db, "TEST", "Q", "A", map[string]string{"A": "a", "D": "d"})
	if _, err := db.Exec(`INSERT INTO answer_verdicts
		(question_id, status, accepted, community, total_votes, source, rationale)
		VALUES(?, 'ambiguous', '["A","D"]',
		       '[{"label":"D","votes":13,"pct":65},{"label":"A","votes":6,"pct":30}]',
		       20, 'discussion', 'contest')`, qid); err != nil {
		t.Fatalf("insert verdict: %v", err)
	}
	qs, err := LoadQuestions(db)
	if err != nil {
		t.Fatalf("LoadQuestions: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("loaded %d questions, want 1", len(qs))
	}
	if qs[0].Verdict.Status != "ambiguous" {
		t.Errorf("verdict status = %q, want ambiguous", qs[0].Verdict.Status)
	}
	if got := qs[0].AcceptedAnswers(); len(got) != 2 || got[0] != "A" || got[1] != "D" {
		t.Errorf("AcceptedAnswers() = %v, want [A D]", got)
	}
	if len(qs[0].Verdict.Community) != 2 || qs[0].Verdict.Community[0].Label != "D" {
		t.Errorf("community = %+v, want D first", qs[0].Verdict.Community)
	}
	if qs[0].Verdict.TotalVotes != 20 {
		t.Errorf("total votes = %d, want 20", qs[0].Verdict.TotalVotes)
	}
}

func TestAcceptedAnswers_FallsBackToTheSuggestedAnswer(t *testing.T) {
	q := Question{SuggestedAnswer: "bd"}
	if got := q.AcceptedAnswers(); len(got) != 1 || got[0] != "BD" {
		t.Errorf("AcceptedAnswers() = %v, want [BD] (normalised)", got)
	}
	empty := Question{}
	if got := empty.AcceptedAnswers(); got != nil {
		t.Errorf("AcceptedAnswers() = %v, want nil when there is no key at all", got)
	}
}

func TestRecordAttemptAgainst_AcceptsEitherSide(t *testing.T) {
	db := openTestDB(t)
	qid := seedQuestion(t, db, "TEST", "Q", "A", map[string]string{"A": "a", "D": "d"})
	for _, tc := range []struct {
		selected string
		want     bool
	}{{"D", true}, {"A", true}, {"B", false}} {
		out, err := RecordAttemptAgainst(db, "host", int(qid), tc.selected, []string{"A", "D"})
		if err != nil {
			t.Fatalf("RecordAttemptAgainst(%q): %v", tc.selected, err)
		}
		if out.Correct != tc.want || out.Ungraded {
			t.Errorf("RecordAttemptAgainst(%q) = %+v, want correct=%v graded", tc.selected, out, tc.want)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE question_id=?`, qid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("attempts rows = %d, want 3", n)
	}
}

func TestRecordAttemptAgainst_UngradeableQuestionWritesNoRow(t *testing.T) {
	db := openTestDB(t)
	qid := seedQuestion(t, db, "TEST", "Q", "", map[string]string{"A": "a"})
	out, err := RecordAttemptAgainst(db, "host", int(qid), "A", nil)
	if err != nil {
		t.Fatalf("RecordAttemptAgainst: %v", err)
	}
	if !out.Ungraded || out.Correct {
		t.Errorf("outcome = %+v, want ungraded (not a false miss)", out)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE question_id=?`, qid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("attempts rows = %d, want 0 — an ungradeable question must not record a miss", n)
	}
}
