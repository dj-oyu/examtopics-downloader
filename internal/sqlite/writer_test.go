package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestWriter_UpsertQuestion_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	w := NewWriter(db)
	if err := w.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	rec := &QuestionRecord{
		Exam:              "AWS Certified Developer Associate DVA C02",
		ExamID:            24,
		Topic:             1,
		QuestionNumber:    7,
		QuestionText:      "Which service?",
		SuggestedAnswer:   "BD",
		ConfirmedAnswer:   "BD",
		Timestamp:         "2023-01-02 03:04:05",
		URL:               "https://example.com/q1",
		Comments:          "[Alice] foo\n[Bob] bar",
		IsMC:              true,
		AnswerDescription: "Lambda is right because ...",
		QuestionImages:    []string{"img1.png"},
		AnswerImages:      []string{},
		Choices: map[string]string{
			"A": "first",
			"B": "second",
			"C": "third",
			"D": "fourth",
		},
		Discussion: []DiscussionRow{
			{Idx: 0, Poster: "Alice", Content: "foo", UpvoteCount: 3, PostedAt: "1 week ago"},
			{Idx: 1, Poster: "Bob", Content: "bar", UpvoteCount: 0, PostedAt: "yesterday"},
		},
	}
	qid, err := w.UpsertQuestion(rec)
	if err != nil {
		t.Fatalf("UpsertQuestion: %v", err)
	}
	if qid <= 0 {
		t.Fatalf("expected positive qid, got %d", qid)
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var (
		exam, qtext, sugg, conf, comments, ad, qimgs, aimgs, ch string
		topic, qnum, examID, isMC                               int
	)
	row := db.QueryRow(`SELECT exam, exam_id, topic, question_number,
		question_text, suggested_answer, confirmed_answer, comments,
		is_mc, answer_description, question_images, answer_images, content_hash
		FROM questions WHERE url = ?`, rec.URL)
	if err := row.Scan(&exam, &examID, &topic, &qnum, &qtext, &sugg, &conf,
		&comments, &isMC, &ad, &qimgs, &aimgs, &ch); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if sugg != "BD" {
		t.Errorf("multi-letter suggested_answer truncated: %q", sugg)
	}
	if conf != "BD" {
		t.Errorf("multi-letter confirmed_answer truncated: %q", conf)
	}
	if examID != 24 || topic != 1 || qnum != 7 {
		t.Errorf("scalar mismatch: examID=%d topic=%d qnum=%d", examID, topic, qnum)
	}
	if isMC != 1 {
		t.Errorf("is_mc not 1: %d", isMC)
	}
	if ad != "Lambda is right because ..." {
		t.Errorf("answer_description: %q", ad)
	}
	if qimgs != `["img1.png"]` {
		t.Errorf("question_images JSON: %q", qimgs)
	}
	if aimgs != `[]` {
		t.Errorf("answer_images JSON: %q", aimgs)
	}
	if ch == "" || len(ch) != 64 {
		t.Errorf("content_hash should be 64 hex chars, got %q", ch)
	}

	// choices
	rows, err := db.Query(`SELECT label, text FROM choices WHERE question_id=? ORDER BY label`, qid)
	if err != nil {
		t.Fatalf("choices: %v", err)
	}
	got := map[string]string{}
	for rows.Next() {
		var l, txt string
		if err := rows.Scan(&l, &txt); err != nil {
			t.Fatal(err)
		}
		got[l] = txt
	}
	_ = rows.Close()
	if len(got) != 4 || got["A"] != "first" || got["D"] != "fourth" {
		t.Errorf("choices mismatch: %v", got)
	}

	// discussion
	var dn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM discussion WHERE question_id=?`, qid).Scan(&dn); err != nil {
		t.Fatal(err)
	}
	if dn != 2 {
		t.Errorf("discussion rows = %d, want 2", dn)
	}
}

// Critical invariant: re-importing a question must update upstream English
// columns but never disturb _ja columns owned by translate.py.
func TestWriter_UpsertQuestion_PreservesJaOnConflict(t *testing.T) {
	db := newTestDB(t)
	w := NewWriter(db)

	// First import.
	if err := w.Begin(); err != nil {
		t.Fatal(err)
	}
	rec := &QuestionRecord{
		Exam: "X", Topic: 1, QuestionNumber: 1,
		QuestionText:    "Original?",
		SuggestedAnswer: "A",
		URL:             "https://example.com/q-ja",
		Choices:         map[string]string{"A": "first", "B": "second"},
	}
	qid, err := w.UpsertQuestion(rec)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}

	// Translator (Python) fills in _ja columns out-of-band.
	mustExec(t, db, `UPDATE questions SET question_text_ja=?, explanation_ja=? WHERE id=?`,
		"原文?", "正解は A", qid)
	mustExec(t, db, `UPDATE choices SET text_ja=? WHERE question_id=? AND label=?`,
		"いちばん", qid, "A")
	mustExec(t, db, `UPDATE choices SET text_ja=? WHERE question_id=? AND label=?`,
		"にばん", qid, "B")

	// Second import: upstream edited the question text and added a choice.
	if err := w.Begin(); err != nil {
		t.Fatal(err)
	}
	rec2 := &QuestionRecord{
		Exam: "X", Topic: 1, QuestionNumber: 1,
		QuestionText:    "Edited upstream?",
		SuggestedAnswer: "B",
		URL:             "https://example.com/q-ja",
		Choices:         map[string]string{"A": "first(EDITED)", "B": "second", "C": "new"},
	}
	if _, err := w.UpsertQuestion(rec2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}

	// English columns updated, _ja columns retained.
	var qtext, qja, eja string
	if err := db.QueryRow(`SELECT question_text, question_text_ja, explanation_ja
		FROM questions WHERE url=?`, rec2.URL).Scan(&qtext, &qja, &eja); err != nil {
		t.Fatal(err)
	}
	if qtext != "Edited upstream?" {
		t.Errorf("English question_text not updated: %q", qtext)
	}
	if qja != "原文?" {
		t.Errorf("question_text_ja drifted on upsert: %q", qja)
	}
	if eja != "正解は A" {
		t.Errorf("explanation_ja drifted on upsert: %q", eja)
	}

	// Choices: A and B keep their text_ja; A.text was updated; C was added with NULL text_ja.
	var aText, aJa, cJa sql.NullString
	if err := db.QueryRow(`SELECT text, text_ja FROM choices WHERE question_id=(SELECT id FROM questions WHERE url=?) AND label='A'`, rec2.URL).Scan(&aText, &aJa); err != nil {
		t.Fatal(err)
	}
	if aText.String != "first(EDITED)" {
		t.Errorf("choices.A text not updated: %q", aText.String)
	}
	if aJa.String != "いちばん" {
		t.Errorf("choices.A text_ja drifted: %q", aJa.String)
	}
	if err := db.QueryRow(`SELECT text_ja FROM choices WHERE question_id=(SELECT id FROM questions WHERE url=?) AND label='C'`, rec2.URL).Scan(&cJa); err != nil {
		t.Fatal(err)
	}
	if cJa.Valid {
		t.Errorf("new choice C should have NULL text_ja, got %q", cJa.String)
	}
}

// Re-importing a question must replace its discussion atomically — no
// duplicates, no leftover stale rows from prior import.
func TestWriter_UpsertQuestion_ReplacesDiscussion(t *testing.T) {
	db := newTestDB(t)
	w := NewWriter(db)
	if err := w.Begin(); err != nil {
		t.Fatal(err)
	}
	rec := &QuestionRecord{
		Exam: "X", Topic: 1, QuestionNumber: 1,
		QuestionText: "Q?", SuggestedAnswer: "A",
		URL:     "https://example.com/q-disc",
		Choices: map[string]string{"A": "x"},
		Discussion: []DiscussionRow{
			{Idx: 0, Poster: "alice", Content: "old1"},
			{Idx: 1, Poster: "bob", Content: "old2"},
			{Idx: 2, Poster: "carol", Content: "old3"},
		},
	}
	qid, err := w.UpsertQuestion(rec)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	if err := w.Begin(); err != nil {
		t.Fatal(err)
	}
	rec.Discussion = []DiscussionRow{
		{Idx: 0, Poster: "dave", Content: "new1"},
		{Idx: 1, Poster: "eve", Content: "new2"},
	}
	if _, err := w.UpsertQuestion(rec); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("second commit: %v", err)
	}

	rows, err := db.Query(`SELECT poster, content FROM discussion WHERE question_id=? ORDER BY idx`, qid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var p, c string
		if err := rows.Scan(&p, &c); err != nil {
			t.Fatal(err)
		}
		got = append(got, p+":"+c)
	}
	want := []string{"dave:new1", "eve:new2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %q want %q", i, got[i], want[i])
		}
	}
}

// On URL conflict, topic and question_number must be preserved from the prior
// import — they are the UI's sort key and the existing Python-imported value
// is the canonical one. Re-imports should not shuffle the sort order.
func TestWriter_UpsertQuestion_PreservesTopicAndQNumOnConflict(t *testing.T) {
	db := newTestDB(t)
	w := NewWriter(db)
	if err := w.Begin(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.UpsertQuestion(&QuestionRecord{
		Exam: "X", Topic: 5, QuestionNumber: 42,
		QuestionText: "Q?", SuggestedAnswer: "A",
		URL: "https://example.com/q-stable", Choices: map[string]string{"A": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}

	// Re-import with garbled topic / qnum (e.g. 0 from URL extraction failure).
	if err := w.Begin(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.UpsertQuestion(&QuestionRecord{
		Exam: "X", Topic: 0, QuestionNumber: 0,
		QuestionText: "Edited?", SuggestedAnswer: "B",
		URL: "https://example.com/q-stable", Choices: map[string]string{"A": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}

	var topic, qnum int
	if err := db.QueryRow(`SELECT topic, question_number FROM questions WHERE url=?`,
		"https://example.com/q-stable").Scan(&topic, &qnum); err != nil {
		t.Fatal(err)
	}
	if topic != 5 || qnum != 42 {
		t.Errorf("topic/qnum drifted on re-import: got %d/%d, want 5/42", topic, qnum)
	}
}

func TestWriter_Rollback_DiscardsWrites(t *testing.T) {
	db := newTestDB(t)
	w := NewWriter(db)
	if err := w.Begin(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.UpsertQuestion(&QuestionRecord{
		Exam: "X", Topic: 1, QuestionNumber: 1,
		QuestionText: "Q?", SuggestedAnswer: "A",
		URL: "https://example.com/abandoned", Choices: map[string]string{"A": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Rollback(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM questions WHERE url=?`,
		"https://example.com/abandoned").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("rollback failed; row count = %d", n)
	}
}
