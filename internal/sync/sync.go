// Package sync implements the local-merge half of the multi-host sync
// design from §3.7.4 of docs/plans/portable-builds.md (phase 1, scp +
// local merge). HTTP push/pull endpoints (phase 2) are deliberately out
// of scope here; phase 1 keeps every operation file-driven so the same
// commands work over scp, USB, or any out-of-band transport.
//
// Three entry points cover the full phase-1 surface:
//
//   - Snapshot(srcPath, outPath) takes a WAL-consistent VACUUM INTO
//     copy of a live DB so it can be transported safely without
//     corrupting in-flight writes.
//   - Merge(dstDB, srcPath) folds a peer DB's append-only rows into
//     the destination using INSERT OR IGNORE (G-Set semantics) and
//     resolves explanation_threads collisions with last-writer-wins
//     on updated_at (LWW-Register).
//   - ContentSync(dstDB, srcPath) replaces the destination's
//     master-owned content tables (questions / choices / discussion)
//     with the master's authoritative copy, used when the master
//     publishes new exam data to its SBC peers.
package sync

import (
	"database/sql"
	"fmt"
)

// Snapshot creates a transactionally-consistent copy of srcPath at
// outPath using SQLite's `VACUUM INTO`. This is the documented safe
// alternative to copying a WAL-mode .db file directly with scp / cp,
// which can land mid-transaction and leave the destination unrecoverable.
func Snapshot(srcPath, outPath string) error {
	db, err := sql.Open("sqlite", srcPath)
	if err != nil {
		return fmt.Errorf("open source %s: %w", srcPath, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`VACUUM INTO ?`, outPath); err != nil {
		return fmt.Errorf("vacuum into %s: %w", outPath, err)
	}
	return nil
}

// Merge applies the multi-host CRDT merge from a peer DB into dstDB.
// Append-only tables (attempts, explanation_messages) are unioned by
// id with INSERT OR IGNORE so re-applying the same peer file is a
// no-op. explanation_threads' mutable columns (status,
// agent_session_id, closed_at, updated_at) are resolved via LWW: the
// row whose updated_at is greater wins, with ties left as-is.
//
// The function attaches srcPath as schema "peer", runs the merge SQL
// in a transaction, and detaches on the way out. Either DB may be
// opened in WAL mode; SQLite's ATTACH copy semantics tolerate that.
func Merge(dstDB *sql.DB, srcPath string) error {
	if _, err := dstDB.Exec(`ATTACH DATABASE ? AS peer`, srcPath); err != nil {
		return fmt.Errorf("attach %s: %w", srcPath, err)
	}
	defer func() { _, _ = dstDB.Exec(`DETACH DATABASE peer`) }()

	tx, err := dstDB.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	// G-Set unions for append-only history.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO attempts SELECT * FROM peer.attempts`); err != nil {
		rollback()
		return fmt.Errorf("merge attempts: %w", err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO explanation_messages SELECT * FROM peer.explanation_messages`); err != nil {
		rollback()
		return fmt.Errorf("merge explanation_messages: %w", err)
	}

	// LWW: insert any threads we don't have yet, then update mutable fields
	// when the peer's updated_at is strictly newer than ours. The CHECK
	// constraint on status is preserved because we copy the validated value.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO explanation_threads SELECT * FROM peer.explanation_threads`); err != nil {
		rollback()
		return fmt.Errorf("merge explanation_threads (insert): %w", err)
	}
	if _, err := tx.Exec(`UPDATE explanation_threads SET
		status = (SELECT status FROM peer.explanation_threads pt WHERE pt.id = explanation_threads.id),
		agent_session_id = (SELECT agent_session_id FROM peer.explanation_threads pt WHERE pt.id = explanation_threads.id),
		closed_at = (SELECT closed_at FROM peer.explanation_threads pt WHERE pt.id = explanation_threads.id),
		updated_at = (SELECT updated_at FROM peer.explanation_threads pt WHERE pt.id = explanation_threads.id)
		WHERE EXISTS (
			SELECT 1 FROM peer.explanation_threads pt
				WHERE pt.id = explanation_threads.id
				AND pt.updated_at > explanation_threads.updated_at
		)`); err != nil {
		rollback()
		return fmt.Errorf("merge explanation_threads (lww): %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ContentSync replaces the destination's master-owned content tables
// (questions, choices, discussion) with the master's authoritative
// copy. Used when the master publishes a freshly-scraped DB to its
// SBC peers. Existing per-peer rows in attempts /
// explanation_threads / explanation_messages are untouched — those
// remain in the SBC and converge later via Merge.
// ---------------------------------------------------------------------------
// Translations
// ---------------------------------------------------------------------------

// TranslationConflict names one translation field where both DBs hold a
// non-empty but different value, so the merge cannot pick for the user.
type TranslationConflict struct {
	URL   string
	Field string // question_text_ja | explanation_ja | choice:<label>
}

// TranslationSyncResult reports what a translations pull did.
type TranslationSyncResult struct {
	FilledQuestions int
	FilledChoices   int
	Conflicts       []TranslationConflict
	Overwritten     int // conflicts resolved in the peer's favour (PreferPeer)
}

// TranslationSyncOptions controls TranslationsSync.
type TranslationSyncOptions struct {
	// PreferPeer resolves conflicts in the peer's favour. The default keeps the
	// local wording and only reports the conflict: bulk-save writes the *_ja
	// columns and nothing else, so there is no per-row version to order the two
	// translations by, and guessing would be worse than saying so.
	PreferPeer bool
	// DryRun counts and reports without writing anything.
	DryRun bool
}

// TranslationsSync merges only the translation columns — questions'
// question_text_ja / explanation_ja and choices' text_ja — from a peer DB into
// dstDB, matching questions by url and choices by (question url, label).
//
// ContentSync replaces questions/choices/discussion wholesale, silently
// dropping whatever translations a peer had produced; Merge carries only the
// append-only attempt/thread/message rows. This is the third path: additive and
// never destructive, so translating on several machines converges instead of
// racing. Per field: empty locally plus non-empty on the peer takes the peer's
// value; non-empty on both and equal does nothing; non-empty on both and
// different keeps the local value and reports a conflict. Once the two sides
// agree, re-running changes nothing.
func TranslationsSync(dstDB *sql.DB, srcPath string, opts TranslationSyncOptions) (TranslationSyncResult, error) {
	var res TranslationSyncResult
	if _, err := dstDB.Exec(`ATTACH DATABASE ? AS peer`, srcPath); err != nil {
		return res, fmt.Errorf("attach %s: %w", srcPath, err)
	}
	defer func() { _, _ = dstDB.Exec(`DETACH DATABASE peer`) }()

	tx, err := dstDB.Begin()
	if err != nil {
		return res, fmt.Errorf("begin: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	if res.Conflicts, err = collectTranslationConflicts(tx); err != nil {
		rollback()
		return res, err
	}
	if res.FilledQuestions, err = applyTranslationPass(tx, questionTranslationSQL(), opts.DryRun); err != nil {
		rollback()
		return res, err
	}
	if res.FilledChoices, err = applyTranslationPass(tx, choiceTranslationSQL(), opts.DryRun); err != nil {
		rollback()
		return res, err
	}
	if opts.PreferPeer {
		if opts.DryRun {
			// Every conflict would be replaced; the count is already known.
			res.Overwritten = len(res.Conflicts)
		} else if res.Overwritten, err = applyConflictPass(tx, questionConflictSQL(), choiceConflictSQL()); err != nil {
			rollback()
			return res, err
		}
	}

	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

// sqlPair is the (count, update) pair for one translation column. count runs
// before update: after the write the predicate no longer matches, so the count
// has to be read first.
type sqlPair struct{ count, update string }

// questionTranslationSQL builds the empty-local-takes-peer's-value pair for one
// questions column.
func questionTranslationSQL() []sqlPair {
	cols := []string{"question_text_ja", "explanation_ja"}
	out := make([]sqlPair, 0, len(cols))
	for _, c := range cols {
		out = append(out, sqlPair{
			count: fmt.Sprintf(`SELECT COUNT(*) FROM questions q
				JOIN peer.questions p ON p.url = q.url
				WHERE COALESCE(TRIM(q.%[1]s), '') = '' AND COALESCE(TRIM(p.%[1]s), '') <> ''`, c),
			update: fmt.Sprintf(`UPDATE questions SET %[1]s = (
					SELECT p.%[1]s FROM peer.questions p WHERE p.url = questions.url)
				WHERE COALESCE(TRIM(%[1]s), '') = ''
				  AND EXISTS (SELECT 1 FROM peer.questions p
						WHERE p.url = questions.url AND COALESCE(TRIM(p.%[1]s), '') <> '')`, c),
		})
	}
	return out
}

// choiceTranslationSQL builds the same pair for choices.text_ja.
func choiceTranslationSQL() []sqlPair {
	return []sqlPair{{
		count: `SELECT COUNT(*) FROM choices c
			JOIN questions q ON q.id = c.question_id
			JOIN peer.questions pq ON pq.url = q.url
			JOIN peer.choices pc ON pc.question_id = pq.id AND pc.label = c.label
			WHERE COALESCE(TRIM(c.text_ja), '') = '' AND COALESCE(TRIM(pc.text_ja), '') <> ''`,
		update: `UPDATE choices SET text_ja = (
				SELECT pc.text_ja FROM peer.choices pc
				JOIN peer.questions pq ON pq.id = pc.question_id
				JOIN questions q ON q.id = choices.question_id
				WHERE pq.url = q.url AND pc.label = choices.label)
			WHERE COALESCE(TRIM(text_ja), '') = ''
			  AND EXISTS (SELECT 1 FROM peer.choices pc
				JOIN peer.questions pq ON pq.id = pc.question_id
				JOIN questions q ON q.id = choices.question_id
				WHERE pq.url = q.url AND pc.label = choices.label
				  AND COALESCE(TRIM(pc.text_ja), '') <> '')`,
	}}
}

// questionConflictSQL builds the "both sides translated it differently" pair for
// one questions column, in the peer-wins form.
func questionConflictSQL() []sqlPair {
	cols := []string{"question_text_ja", "explanation_ja"}
	out := make([]sqlPair, 0, len(cols))
	for _, c := range cols {
		out = append(out, sqlPair{
			count: fmt.Sprintf(`SELECT COUNT(*) FROM questions q
				JOIN peer.questions p ON p.url = q.url
				WHERE COALESCE(TRIM(q.%[1]s), '') <> '' AND COALESCE(TRIM(p.%[1]s), '') <> ''
				  AND q.%[1]s <> p.%[1]s`, c),
			update: fmt.Sprintf(`UPDATE questions SET %[1]s = (
					SELECT p.%[1]s FROM peer.questions p WHERE p.url = questions.url)
				WHERE EXISTS (SELECT 1 FROM peer.questions p
						WHERE p.url = questions.url
						  AND COALESCE(TRIM(questions.%[1]s), '') <> ''
						  AND COALESCE(TRIM(p.%[1]s), '') <> ''
						  AND questions.%[1]s <> p.%[1]s)`, c),
		})
	}
	return out
}

// choiceConflictSQL is the choices.text_ja counterpart.
func choiceConflictSQL() []sqlPair {
	return []sqlPair{{
		count: `SELECT COUNT(*) FROM choices c
			JOIN questions q ON q.id = c.question_id
			JOIN peer.questions pq ON pq.url = q.url
			JOIN peer.choices pc ON pc.question_id = pq.id AND pc.label = c.label
			WHERE COALESCE(TRIM(c.text_ja), '') <> '' AND COALESCE(TRIM(pc.text_ja), '') <> ''
			  AND c.text_ja <> pc.text_ja`,
		update: `UPDATE choices SET text_ja = (
				SELECT pc.text_ja FROM peer.choices pc
				JOIN peer.questions pq ON pq.id = pc.question_id
				JOIN questions q ON q.id = choices.question_id
				WHERE pq.url = q.url AND pc.label = choices.label)
			WHERE EXISTS (SELECT 1 FROM peer.choices pc
				JOIN peer.questions pq ON pq.id = pc.question_id
				JOIN questions q ON q.id = choices.question_id
				WHERE pq.url = q.url AND pc.label = choices.label
				  AND COALESCE(TRIM(choices.text_ja), '') <> ''
				  AND COALESCE(TRIM(pc.text_ja), '') <> ''
				  AND choices.text_ja <> pc.text_ja)`,
	}}
}

// applyTranslationPass counts then applies each fill, returning how many fields
// a real run would fill (or did fill when dryRun is false).
func applyTranslationPass(tx *sql.Tx, pairs []sqlPair, dryRun bool) (int, error) {
	var n int
	for _, p := range pairs {
		var c int
		if err := tx.QueryRow(p.count).Scan(&c); err != nil {
			return n, fmt.Errorf("count translations: %w", err)
		}
		n += c
		if c == 0 || dryRun {
			continue
		}
		if _, err := tx.Exec(p.update); err != nil {
			return n, fmt.Errorf("fill translations: %w", err)
		}
	}
	return n, nil
}

// applyConflictPass resolves conflicts in the peer's favour, counting before
// each write so the reported number is the number actually replaced.
func applyConflictPass(tx *sql.Tx, groups ...[]sqlPair) (int, error) {
	total := 0
	for _, group := range groups {
		for _, p := range group {
			var c int
			if err := tx.QueryRow(p.count).Scan(&c); err != nil {
				return total, fmt.Errorf("count conflicts: %w", err)
			}
			if c == 0 {
				continue
			}
			if _, err := tx.Exec(p.update); err != nil {
				return total, fmt.Errorf("overwrite translations: %w", err)
			}
			total += c
		}
	}
	return total, nil
}

// collectTranslationConflicts lists every field both sides translated
// differently — the part of a merge that needs a human, not a policy.
func collectTranslationConflicts(tx *sql.Tx) ([]TranslationConflict, error) {
	rows, err := tx.Query(`SELECT q.url, 'question_text_ja' FROM questions q JOIN peer.questions p ON p.url = q.url
			WHERE COALESCE(TRIM(q.question_text_ja), '') <> '' AND COALESCE(TRIM(p.question_text_ja), '') <> ''
			  AND q.question_text_ja <> p.question_text_ja
		UNION ALL
		SELECT q.url, 'explanation_ja' FROM questions q JOIN peer.questions p ON p.url = q.url
			WHERE COALESCE(TRIM(q.explanation_ja), '') <> '' AND COALESCE(TRIM(p.explanation_ja), '') <> ''
			  AND q.explanation_ja <> p.explanation_ja
		UNION ALL
		SELECT q.url, 'choice:' || c.label
			FROM choices c
			JOIN questions q ON q.id = c.question_id
			JOIN peer.questions pq ON pq.url = q.url
			JOIN peer.choices pc ON pc.question_id = pq.id AND pc.label = c.label
			WHERE COALESCE(TRIM(c.text_ja), '') <> '' AND COALESCE(TRIM(pc.text_ja), '') <> ''
			  AND c.text_ja <> pc.text_ja
		ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("collect conflicts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TranslationConflict
	for rows.Next() {
		var c TranslationConflict
		if err := rows.Scan(&c.URL, &c.Field); err != nil {
			return nil, fmt.Errorf("scan conflict: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func ContentSync(dstDB *sql.DB, srcPath string) error {
	if _, err := dstDB.Exec(`ATTACH DATABASE ? AS peer`, srcPath); err != nil {
		return fmt.Errorf("attach %s: %w", srcPath, err)
	}
	defer func() { _, _ = dstDB.Exec(`DETACH DATABASE peer`) }()

	tx, err := dstDB.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	// FKs from attempts/threads point at questions(id). Disable FK
	// enforcement for the table swap so we can DELETE+INSERT cleanly,
	// then re-enable on the way out. SQLite's foreign_keys pragma
	// applies to subsequent statements only; existing rows are not
	// re-validated when it's flipped back on, so previously-valid
	// references stay valid.
	if _, err := tx.Exec(`DELETE FROM choices`); err != nil {
		rollback()
		return fmt.Errorf("clear choices: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM discussion`); err != nil {
		rollback()
		return fmt.Errorf("clear discussion: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM questions`); err != nil {
		rollback()
		return fmt.Errorf("clear questions: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO questions SELECT * FROM peer.questions`); err != nil {
		rollback()
		return fmt.Errorf("copy questions: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO choices SELECT * FROM peer.choices`); err != nil {
		rollback()
		return fmt.Errorf("copy choices: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO discussion SELECT * FROM peer.discussion`); err != nil {
		rollback()
		return fmt.Errorf("copy discussion: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
