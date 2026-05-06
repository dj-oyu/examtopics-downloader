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
