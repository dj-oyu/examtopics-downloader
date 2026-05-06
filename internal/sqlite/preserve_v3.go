package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"examtopics-downloader/internal/uuidx"
)

// Migration 003 (multihost sync) drops the legacy INTEGER-PK forms of
// attempts / explanation_threads / explanation_messages and replaces
// them with BLOB(16) UUIDv7-keyed tables. Naive application would lose
// any existing rows. The hooks below run inside the same migration
// transaction: captureLegacyV2 reads the legacy rows before the SQL
// drops the tables, restoreLegacyV2 reinserts them into the freshly
// created v3 tables with newly minted UUIDv7 ids and a host_id stamp.
// Both halves succeed or roll back together with the rest of the
// migration, so a partial preservation cannot leave a v3 schema with
// missing data.

type v2Attempt struct {
	questionID  int64
	selected    string
	isCorrect   int64
	attemptedAt sql.NullString
}

type v2Thread struct {
	id          int64
	questionID  int64
	status      string
	createdAt   string
	closedAt    sql.NullString
	agentSessID sql.NullString
}

type v2Message struct {
	id              int64
	threadID        int64
	role            string
	author          sql.NullString
	content         string
	reasonCode      sql.NullString
	citations       sql.NullString
	translationDiff sql.NullString
	createdAt       string
}

type capturedV2 struct {
	attempts []v2Attempt
	threads  []v2Thread
	messages []v2Message
}

// hasV2Schema reports whether the named table exists with the legacy
// INTEGER-PK shape that predates migration 003. The signal is the
// absence of the host_id column — every v3 multihost table has one,
// none of the v2 forms do.
func hasV2Schema(tx *sql.Tx, table string) (bool, error) {
	rows, err := tx.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false, fmt.Errorf("pragma_table_info(%s): %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols := map[string]struct{}{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		cols[n] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(cols) == 0 {
		return false, nil
	}
	_, hasHost := cols["host_id"]
	return !hasHost, nil
}

// captureLegacyV2 reads every row from each multihost table that still
// has the legacy v2 shape. Tables already at v3 (host_id present) are
// skipped — captureLegacyV2 is only called from the v3 migration hook,
// so a mixed state should not occur, but the guard keeps the function
// safe to call defensively.
func captureLegacyV2(tx *sql.Tx) (*capturedV2, error) {
	c := &capturedV2{}

	if has, err := hasV2Schema(tx, "attempts"); err != nil {
		return nil, err
	} else if has {
		rows, err := tx.Query(`SELECT question_id, selected, is_correct, attempted_at FROM attempts`)
		if err != nil {
			return nil, fmt.Errorf("read v2 attempts: %w", err)
		}
		for rows.Next() {
			var a v2Attempt
			if err := rows.Scan(&a.questionID, &a.selected, &a.isCorrect, &a.attemptedAt); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan v2 attempts: %w", err)
			}
			c.attempts = append(c.attempts, a)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}

	if has, err := hasV2Schema(tx, "explanation_threads"); err != nil {
		return nil, err
	} else if has {
		rows, err := tx.Query(`SELECT id, question_id, status, created_at, closed_at, agent_session_id FROM explanation_threads`)
		if err != nil {
			return nil, fmt.Errorf("read v2 threads: %w", err)
		}
		for rows.Next() {
			var t v2Thread
			if err := rows.Scan(&t.id, &t.questionID, &t.status, &t.createdAt, &t.closedAt, &t.agentSessID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan v2 threads: %w", err)
			}
			c.threads = append(c.threads, t)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}

	if has, err := hasV2Schema(tx, "explanation_messages"); err != nil {
		return nil, err
	} else if has {
		rows, err := tx.Query(`SELECT id, thread_id, role, author, content, reason_code, citations, translation_diff, created_at FROM explanation_messages`)
		if err != nil {
			return nil, fmt.Errorf("read v2 messages: %w", err)
		}
		for rows.Next() {
			var m v2Message
			if err := rows.Scan(&m.id, &m.threadID, &m.role, &m.author, &m.content, &m.reasonCode, &m.citations, &m.translationDiff, &m.createdAt); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan v2 messages: %w", err)
			}
			c.messages = append(c.messages, m)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}

	return c, nil
}

// restoreLegacyV2 inserts captured v2 rows into the freshly created v3
// tables. Each row receives a newly generated UUIDv7 BLOB id; the
// integer thread_id foreign keys carried by messages are translated
// through a map built while inserting threads, so referential integrity
// holds even with PRAGMA foreign_keys = ON. Any orphan messages whose
// parent thread is missing from the captured set are silently dropped:
// they would fail the FK on insert otherwise, and a v2 DB with such
// orphans is already broken.
func restoreLegacyV2(tx *sql.Tx, c *capturedV2, hostID string) error {
	if c == nil {
		return nil
	}
	if hostID == "" {
		return fmt.Errorf("preserve v3: hostID must not be empty")
	}

	threadIDMap := make(map[int64][]byte, len(c.threads))
	for _, t := range c.threads {
		id := uuidx.MustNew()
		idCopy := append([]byte(nil), id[:]...)
		threadIDMap[t.id] = idCopy
		// LWW tracking column: bump updated_at to the latest of
		// created_at / closed_at so a freshly imported thread sorts
		// alongside any other peer's view of the same row.
		updatedAt := t.createdAt
		if t.closedAt.Valid && t.closedAt.String > updatedAt {
			updatedAt = t.closedAt.String
		}
		if _, err := tx.Exec(`
			INSERT INTO explanation_threads
			  (id, question_id, status, agent_session_id, created_at, closed_at, updated_at, host_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, idCopy, t.questionID, t.status, t.agentSessID, t.createdAt, t.closedAt, updatedAt, hostID); err != nil {
			return fmt.Errorf("insert preserved thread (legacy id=%d): %w", t.id, err)
		}
	}

	for _, a := range c.attempts {
		id := uuidx.MustNew()
		attemptedAt := a.attemptedAt
		if !attemptedAt.Valid || attemptedAt.String == "" {
			attemptedAt = sql.NullString{String: time.Now().UTC().Format(time.RFC3339Nano), Valid: true}
		}
		if _, err := tx.Exec(`
			INSERT INTO attempts (id, question_id, selected, is_correct, attempted_at, host_id)
			VALUES (?, ?, ?, ?, ?, ?)
		`, id[:], a.questionID, a.selected, a.isCorrect, attemptedAt.String, hostID); err != nil {
			return fmt.Errorf("insert preserved attempt: %w", err)
		}
	}

	for _, m := range c.messages {
		newThreadID, ok := threadIDMap[m.threadID]
		if !ok {
			continue
		}
		id := uuidx.MustNew()
		if _, err := tx.Exec(`
			INSERT INTO explanation_messages
			  (id, thread_id, role, author, content, reason_code, citations, translation_diff, created_at, host_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id[:], newThreadID, m.role, m.author, m.content, m.reasonCode, m.citations, m.translationDiff, m.createdAt, hostID); err != nil {
			return fmt.Errorf("insert preserved message (legacy thread_id=%d): %w", m.threadID, err)
		}
	}

	return nil
}
