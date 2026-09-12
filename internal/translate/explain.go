package translate

// explain.go ports the multi-turn explanation-thread reply path from
// tools/translate.py (cmd_show_thread / cmd_reply) to Go so the web's
// runExplain can spawn `examtopicsdl translate explain` instead of
// embedding a Python toolchain dependency. The validation rules
// (citations / reason_code / translation_fix) mirror the Python tool
// VERBATIM so any agent prompt or skill that worked against the
// Python entry point keeps working unchanged.
//
// SQL note: thread/message ids are BLOB(16) UUIDv7 in the v3 schema
// (see internal/sqlite/migrations/003_multihost_sync.sql). At the
// JSON / CLI boundary they appear as 26-char Crockford base32 strings
// produced by internal/uuidx — that string form is what shows up in
// URLs (/threads/<base32>) and in the input.json the LLM adapter
// reads. Decoding happens once at the edge of every public function;
// the tx layer below works in raw bytes.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"examtopics-downloader/internal/uuidx"
)

// ValidReasonCodes mirrors VALID_REASON_CODES in tools/translate.py.
// Order follows the AGENTS.md priority order (translation > comprehension
// > spec > ambiguous) so error messages naming the four valid codes also
// hint at the priority a well-behaved agent applies.
var ValidReasonCodes = []string{"comprehension", "spec", "ambiguous", "translation"}

// Citation matches the {url, title?} object the agent emits and that
// _validate_citations enforces in tools/translate.py. Title is a
// pointer because the field is optional — distinguishing absent from
// empty avoids accidentally regressing into a "title is required when
// present but may be empty" contract.
type Citation struct {
	URL   string  `json:"url"`
	Title *string `json:"title,omitempty"`
}

// TranslationFix is the JSON shape the agent passes when it picks
// reason_code=translation. Field semantics match _apply_translation_fix:
// fields that are absent leave the corresponding column untouched, an
// empty-string value overwrites with empty (the legacy save/unsave
// path's tolerance), and choices_ja keys not present on the question
// are silently dropped.
type TranslationFix struct {
	QuestionTextJa *string           `json:"question_text_ja,omitempty"`
	ExplanationJa  *string           `json:"explanation_ja,omitempty"`
	ChoicesJa      map[string]string `json:"choices_ja,omitempty"`
}

// HasAnyKey reports whether the fix carries at least one of the three
// payload-bearing keys; used by ValidateReply to mirror the Python check
// (`any(k in translation_fix for k in keys)`).
func (f *TranslationFix) HasAnyKey() bool {
	if f == nil {
		return false
	}
	return f.QuestionTextJa != nil || f.ExplanationJa != nil || f.ChoicesJa != nil
}

// Reply is the JSON payload an LLM adapter writes to OutputPath. It
// matches the cmd_reply contract in tools/translate.py:
//
//	{ content, author?, resolve?, reason_code?,
//	  citations?, translation_fix?, explanation_ja? }
//
// ThreadID is added on top of that contract so the orchestrator can
// reject mismatched results (RunExplain insists the adapter echoes the
// thread it was asked about — same defensive check Retranslate already
// makes for question id).
type Reply struct {
	ThreadID       string          `json:"thread_id,omitempty"`
	Content        string          `json:"content"`
	Author         string          `json:"author,omitempty"`
	Resolve        bool            `json:"resolve,omitempty"`
	ReasonCode     string          `json:"reason_code,omitempty"`
	Citations      []Citation      `json:"citations,omitempty"`
	TranslationFix *TranslationFix `json:"translation_fix,omitempty"`
	ExplanationJa  string          `json:"explanation_ja,omitempty"`
}

// ThreadMessage matches the message rows fetch_thread emits.
// translation_diff is the JSON-serialised before/after blob produced
// by ApplyTranslationFix on a previous reason_code=translation reply.
type ThreadMessage struct {
	ID              string  `json:"id"`
	Role            string  `json:"role"`
	Author          *string `json:"author,omitempty"`
	Content         string  `json:"content"`
	ReasonCode      *string `json:"reason_code,omitempty"`
	Citations       *string `json:"citations,omitempty"`
	TranslationDiff *string `json:"translation_diff,omitempty"`
	CreatedAt       string  `json:"created_at"`
}

// ThreadQuestion matches the question payload nested inside fetch_thread.
type ThreadQuestion struct {
	ID              int64          `json:"id"`
	Exam            string         `json:"exam"`
	Topic           int            `json:"topic"`
	QuestionNumber  int            `json:"question_number"`
	URL             *string        `json:"url,omitempty"`
	SuggestedAnswer string         `json:"suggested_answer"`
	QuestionText    string         `json:"question_text"`
	QuestionTextJa  *string        `json:"question_text_ja,omitempty"`
	ExplanationJa   *string        `json:"explanation_ja,omitempty"`
	Comments        *string        `json:"comments,omitempty"`
	Choices         []ThreadChoice `json:"choices"`
}

// ThreadChoice mirrors the choices entries inside fetch_thread.
type ThreadChoice struct {
	Label  string  `json:"label"`
	Text   string  `json:"text"`
	TextJa *string `json:"text_ja,omitempty"`
}

// ThreadPayload is the JSON the LLM receives. ThreadID is serialised
// as the 26-char Crockford base32 string so prompts can name threads
// in their human-friendly URL form (decoded back to BLOB only at the
// SQL edge).
type ThreadPayload struct {
	ThreadID  string          `json:"thread_id"`
	Status    string          `json:"status"`
	CreatedAt string          `json:"created_at"`
	ClosedAt  *string         `json:"closed_at,omitempty"`
	Question  ThreadQuestion  `json:"question"`
	Messages  []ThreadMessage `json:"messages"`
}

// ReadThread builds the JSON the LLM reads. It mirrors fetch_thread in
// tools/translate.py exactly: thread row → question row → choices ordered
// by label → messages ordered by id ASC. Returns sql.ErrNoRows-wrapped
// error when the thread does not exist so callers can match.
func ReadThread(db *sql.DB, tidStr string) (*ThreadPayload, error) {
	if db == nil {
		return nil, errors.New("read thread: nil db")
	}
	tidBytes, err := uuidx.Decode(tidStr)
	if err != nil {
		return nil, fmt.Errorf("decode thread id %q: %w", tidStr, err)
	}

	var (
		tStatus    string
		tCreated   string
		tClosed    sql.NullString
		questionID int64
	)
	err = db.QueryRow(`SELECT status, created_at, closed_at, question_id
		FROM explanation_threads WHERE id = ?`, tidBytes[:]).Scan(
		&tStatus, &tCreated, &tClosed, &questionID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("read thread %s: not found", tidStr)
		}
		return nil, fmt.Errorf("read thread %s: %w", tidStr, err)
	}

	q := ThreadQuestion{}
	var qURL, qTextJa, qExplJa, qComments sql.NullString
	err = db.QueryRow(`SELECT id, exam, topic, question_number, url, suggested_answer,
			question_text, question_text_ja, explanation_ja, comments
		FROM questions WHERE id = ?`, questionID).Scan(
		&q.ID, &q.Exam, &q.Topic, &q.QuestionNumber, &qURL, &q.SuggestedAnswer,
		&q.QuestionText, &qTextJa, &qExplJa, &qComments,
	)
	if err != nil {
		return nil, fmt.Errorf("read question %d: %w", questionID, err)
	}
	if qURL.Valid {
		s := qURL.String
		q.URL = &s
	}
	if qTextJa.Valid {
		s := qTextJa.String
		q.QuestionTextJa = &s
	}
	if qExplJa.Valid {
		s := qExplJa.String
		q.ExplanationJa = &s
	}
	if qComments.Valid {
		s := qComments.String
		q.Comments = &s
	}

	rows, err := db.Query(`SELECT label, text, text_ja FROM choices
		WHERE question_id = ? ORDER BY label`, questionID)
	if err != nil {
		return nil, fmt.Errorf("read choices for %d: %w", questionID, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c ThreadChoice
		var tja sql.NullString
		if err := rows.Scan(&c.Label, &c.Text, &tja); err != nil {
			return nil, fmt.Errorf("scan choice: %w", err)
		}
		if tja.Valid {
			s := tja.String
			c.TextJa = &s
		}
		q.Choices = append(q.Choices, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	mrows, err := db.Query(`SELECT id, role, author, content, reason_code,
			citations, translation_diff, created_at
		FROM explanation_messages WHERE thread_id = ? ORDER BY id ASC`, tidBytes[:])
	if err != nil {
		return nil, fmt.Errorf("read messages for thread: %w", err)
	}
	defer func() { _ = mrows.Close() }()
	var messages []ThreadMessage
	for mrows.Next() {
		var midRaw []byte
		var m ThreadMessage
		var author, reasonCode, citations, translationDiff sql.NullString
		if err := mrows.Scan(&midRaw, &m.Role, &author, &m.Content,
			&reasonCode, &citations, &translationDiff, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		var midArr [16]byte
		if len(midRaw) != 16 {
			return nil, fmt.Errorf("message id is %d bytes, want 16", len(midRaw))
		}
		copy(midArr[:], midRaw)
		m.ID = uuidx.Encode(midArr)
		if author.Valid {
			s := author.String
			m.Author = &s
		}
		if reasonCode.Valid {
			s := reasonCode.String
			m.ReasonCode = &s
		}
		if citations.Valid {
			s := citations.String
			m.Citations = &s
		}
		if translationDiff.Valid {
			s := translationDiff.String
			m.TranslationDiff = &s
		}
		messages = append(messages, m)
	}
	if err := mrows.Err(); err != nil {
		return nil, err
	}

	out := &ThreadPayload{
		ThreadID:  tidStr,
		Status:    tStatus,
		CreatedAt: tCreated,
		Question:  q,
		Messages:  messages,
	}
	if tClosed.Valid {
		s := tClosed.String
		out.ClosedAt = &s
	}
	return out, nil
}

// ValidateReply mirrors _validate_citations + _validate_reason in
// tools/translate.py. The error messages are matched VERBATIM to the
// Python tool so existing prompts that grep for substrings still
// trigger their fallback paths unchanged.
func ValidateReply(reply *Reply) error {
	if reply == nil {
		return errors.New("reply: payload is nil")
	}
	if err := validateCitations(reply.Citations); err != nil {
		return err
	}
	if err := validateReason(reply.ReasonCode, reply.Citations, reply.TranslationFix); err != nil {
		return err
	}
	// Python requires content when reason_code is set.
	content := strings.TrimSpace(reply.Content)
	if reply.ReasonCode != "" && content == "" {
		return errors.New("'content' is required (non-empty) when reason_code is set")
	}
	if reply.ReasonCode == "" && content == "" && strings.TrimSpace(reply.ExplanationJa) == "" {
		return errors.New("payload must include 'content' or 'explanation_ja' (or set 'reason_code' for the structured path)")
	}
	return nil
}

// validateCitations: mirrors _validate_citations. Top-level value is
// already typed (Citation has string URL), but the Python tool also
// validates the URL string itself is a string — we get that for free.
// Empty list is treated the same as nil (Python's check is "if
// citations is None: return None").
func validateCitations(citations []Citation) error {
	for _, c := range citations {
		if c.URL == "" {
			return errors.New("each citation must be an object with a string 'url' field")
		}
	}
	return nil
}

// validateReason mirrors _validate_reason. Spec/ambiguous require at
// least one citation whose url contains "docs.aws.amazon.com" — the
// AWS Docs MCP grounding rule. Translation requires a non-empty
// translation_fix carrying at least one of the three payload keys;
// choices_ja must be a JSON object (Python check `isinstance(...,
// dict)` — Go enforces it via the typed map[string]string).
func validateReason(reasonCode string, citations []Citation, fix *TranslationFix) error {
	if reasonCode == "" {
		return nil
	}
	known := false
	for _, ok := range ValidReasonCodes {
		if reasonCode == ok {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("reason_code must be one of %s, got %q",
			strings.Join(ValidReasonCodes, "|"), reasonCode)
	}
	if reasonCode == "spec" || reasonCode == "ambiguous" {
		if len(citations) == 0 {
			return fmt.Errorf("reason_code=%s requires non-empty 'citations'", reasonCode)
		}
		hasAWS := false
		for _, c := range citations {
			if strings.Contains(c.URL, "docs.aws.amazon.com") {
				hasAWS = true
				break
			}
		}
		if !hasAWS {
			return fmt.Errorf("reason_code=%s requires at least one citation whose url contains 'docs.aws.amazon.com' (AWS Docs MCP grounding)", reasonCode)
		}
	}
	if reasonCode == "translation" {
		if fix == nil || !fix.HasAnyKey() {
			return errors.New("reason_code=translation requires non-empty 'translation_fix'")
		}
	}
	return nil
}

// ApplyTranslationFix snapshots the existing _ja columns, applies the
// supplied fix UPDATE-by-UPDATE, and returns the JSON-encoded {before,
// after} diff string the caller stores in
// explanation_messages.translation_diff. Behaviour mirrors
// _apply_translation_fix in tools/translate.py:
//
//   - "before" includes the full set of _ja columns (question fields
//     and every existing choice text_ja keyed by label) so the diff is
//     self-contained for audit.
//   - "after" includes only the keys the fix touched.
//   - choices_ja entries whose label is unknown are silently dropped
//     (matches WriteTranslation's tolerance).
func ApplyTranslationFix(tx *sql.Tx, qid int64, fix *TranslationFix) (string, error) {
	if tx == nil {
		return "", errors.New("apply translation fix: nil tx")
	}
	if fix == nil {
		return "", errors.New("apply translation fix: nil fix")
	}

	// Snapshot existing values for `before`.
	var oldQTextJa, oldExplJa sql.NullString
	if err := tx.QueryRow(`SELECT question_text_ja, explanation_ja FROM questions WHERE id = ?`, qid).
		Scan(&oldQTextJa, &oldExplJa); err != nil {
		return "", fmt.Errorf("snapshot question %d: %w", qid, err)
	}
	rows, err := tx.Query(`SELECT label, text_ja FROM choices WHERE question_id = ? ORDER BY label`, qid)
	if err != nil {
		return "", fmt.Errorf("snapshot choices: %w", err)
	}
	defer func() { _ = rows.Close() }()
	beforeChoices := map[string]any{}
	for rows.Next() {
		var label string
		var ja sql.NullString
		if err := rows.Scan(&label, &ja); err != nil {
			return "", fmt.Errorf("scan choice snapshot: %w", err)
		}
		if ja.Valid {
			beforeChoices[label] = ja.String
		} else {
			beforeChoices[label] = nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	before := map[string]any{
		"question_text_ja": nullableString(oldQTextJa),
		"explanation_ja":   nullableString(oldExplJa),
		"choices_ja":       beforeChoices,
	}
	after := map[string]any{}

	if fix.QuestionTextJa != nil {
		if _, err := tx.Exec(`UPDATE questions SET question_text_ja = ? WHERE id = ?`, *fix.QuestionTextJa, qid); err != nil {
			return "", fmt.Errorf("update question_text_ja: %w", err)
		}
		after["question_text_ja"] = *fix.QuestionTextJa
	}
	if fix.ExplanationJa != nil {
		if _, err := tx.Exec(`UPDATE questions SET explanation_ja = ? WHERE id = ?`, *fix.ExplanationJa, qid); err != nil {
			return "", fmt.Errorf("update explanation_ja: %w", err)
		}
		after["explanation_ja"] = *fix.ExplanationJa
	}
	if len(fix.ChoicesJa) > 0 {
		afterChoices := map[string]string{}
		labels := make([]string, 0, len(fix.ChoicesJa))
		for k := range fix.ChoicesJa {
			labels = append(labels, k)
		}
		sort.Strings(labels)
		for _, label := range labels {
			text := fix.ChoicesJa[label]
			if _, err := tx.Exec(`UPDATE choices SET text_ja = ? WHERE question_id = ? AND label = ?`, text, qid, label); err != nil {
				return "", fmt.Errorf("update choice %s: %w", label, err)
			}
			afterChoices[label] = text
		}
		after["choices_ja"] = afterChoices
	}

	diff, err := json.Marshal(map[string]any{"before": before, "after": after})
	if err != nil {
		return "", fmt.Errorf("marshal diff: %w", err)
	}
	return string(diff), nil
}

// nullableString flattens sql.NullString into a value usable in the
// JSON before/after blob. Python emits None for NULL columns; we mirror
// that with a typed nil so json.Marshal produces "null" rather than an
// empty string.
func nullableString(ns sql.NullString) any {
	if ns.Valid {
		return ns.String
	}
	return nil
}

// nowUTC formats a current UTC timestamp in the same shape SQLite's
// CURRENT_TIMESTAMP would (YYYY-MM-DD HH:MM:SS) — kept as a function so
// tests can stub it later if a timestamp-stable test becomes useful.
// Today the only timestamps stored on writes are agent insertions; they
// surface back to the LLM via ReadThread's created_at field.
func nowUTC() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}

// WriteReply persists an agent reply and any translation_fix in a
// single transaction. It mirrors cmd_reply in tools/translate.py with
// these additions/adjustments demanded by the v3 schema:
//
//   - Message and (when present) thread session-id updates carry
//     host_id and updated_at columns the v2 schema didn't have. The
//     same now() value is used everywhere so updated_at-based LWW
//     merges see one consistent bump per write.
//   - Message ids are minted with uuidx.New() (UUIDv7) — they sort
//     monotonically with creation time, which matters for the
//     ORDER BY id ASC reads ReadThread emits.
//   - Thread status is verified to be 'open'; resolved/dismissed
//     threads return an error that surfaces as a non-zero CLI exit so
//     the autoresponder doesn't silently no-op.
//
// hostID is the value config.Load() resolved (or fallbackHostID()
// when none was set). sessionID is the LLM-supplied session_id the
// adapter pulled out of the wrapper JSON; pass "" to leave the
// thread's existing session_id alone.
func WriteReply(db *sql.DB, tidStr string, reply *Reply, hostID, sessionID string) error {
	if db == nil {
		return errors.New("write reply: nil db")
	}
	if reply == nil {
		return errors.New("write reply: nil reply")
	}
	if hostID == "" {
		return errors.New("write reply: hostID is required")
	}
	tidBytes, err := uuidx.Decode(tidStr)
	if err != nil {
		return fmt.Errorf("decode thread id %q: %w", tidStr, err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	var (
		status     string
		questionID int64
	)
	if err := tx.QueryRow(`SELECT status, question_id FROM explanation_threads WHERE id = ?`, tidBytes[:]).
		Scan(&status, &questionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("write reply: thread %s not found", tidStr)
		}
		return fmt.Errorf("lookup thread: %w", err)
	}
	if status != "open" {
		return fmt.Errorf("write reply: thread %s is %s (not open)", tidStr, status)
	}

	now := nowUTC()
	var translationDiff *string
	if reply.ReasonCode == "translation" {
		diff, err := ApplyTranslationFix(tx, questionID, reply.TranslationFix)
		if err != nil {
			return err
		}
		translationDiff = &diff
	}

	mid, err := uuidx.New()
	if err != nil {
		return fmt.Errorf("mint message id: %w", err)
	}
	var citationsStr *string
	if len(reply.Citations) > 0 {
		c, err := json.Marshal(reply.Citations)
		if err != nil {
			return fmt.Errorf("marshal citations: %w", err)
		}
		s := string(c)
		citationsStr = &s
	}
	var authorStr *string
	if reply.Author != "" {
		s := reply.Author
		authorStr = &s
	}
	var reasonStr *string
	if reply.ReasonCode != "" {
		s := reply.ReasonCode
		reasonStr = &s
	}
	if _, err := tx.Exec(`INSERT INTO explanation_messages
		(id, thread_id, role, author, content, reason_code, citations, translation_diff, created_at, host_id)
		VALUES(?, ?, 'agent', ?, ?, ?, ?, ?, ?, ?)`,
		mid[:], tidBytes[:], authorStr, reply.Content, reasonStr, citationsStr, translationDiff, now, hostID); err != nil {
		return fmt.Errorf("insert message: %w", err)
	}

	// Session-id writeback: LWW updated_at bumps to `now` so the same
	// value persists on a peer DB after sync merge — without the bump
	// a peer's older session_id could win when its updated_at is newer.
	if sessionID != "" {
		if _, err := tx.Exec(`UPDATE explanation_threads SET agent_session_id = ?, updated_at = ? WHERE id = ?`,
			sessionID, now, tidBytes[:]); err != nil {
			return fmt.Errorf("update session id: %w", err)
		}
	}

	if reply.Resolve {
		if _, err := tx.Exec(`UPDATE explanation_threads SET status = 'resolved', closed_at = ?, updated_at = ? WHERE id = ?`,
			now, now, tidBytes[:]); err != nil {
			return fmt.Errorf("resolve thread: %w", err)
		}
	}

	// Legacy explanation_ja path: only honoured when the agent did NOT
	// pick reason_code=translation (in that case the structured fix is
	// already authoritative and the legacy field would silently
	// overwrite it).
	if reply.ReasonCode != "translation" && strings.TrimSpace(reply.ExplanationJa) != "" {
		if _, err := tx.Exec(`UPDATE questions SET explanation_ja = ? WHERE id = ?`,
			reply.ExplanationJa, questionID); err != nil {
			return fmt.Errorf("update explanation_ja: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ExplainOpts bundles the inputs needed to drive one
// thread-reply round-trip. Layout mirrors RetranslateOpts so callers
// that already know the retranslate pattern can transfer the muscle
// memory directly.
type ExplainOpts struct {
	DB        *sql.DB
	TID       string
	HostID    string
	Adapter   ExplainAdapter
	WorkDir   string
	DryRun    bool
	SessionID string // override (rare); usually empty so the adapter pulls from DB
}

// RunExplain reads the named thread, hands it to the adapter, validates
// the produced Reply, and persists it. Returns the validated Reply on
// success so callers (CLI, eventual HTTP API) can echo a summary line.
//
// On dry-run the function stops after writing the input.json — useful
// for staging prompts during local debugging.
func RunExplain(ctx context.Context, opts ExplainOpts) (*Reply, error) {
	if opts.DB == nil {
		return nil, errors.New("run explain: DB is required")
	}
	if opts.Adapter == nil {
		return nil, errors.New("run explain: Adapter is required")
	}
	if opts.HostID == "" {
		return nil, errors.New("run explain: HostID is required")
	}
	if opts.TID == "" {
		return nil, errors.New("run explain: TID is required")
	}

	thread, err := ReadThread(opts.DB, opts.TID)
	if err != nil {
		return nil, err
	}
	if thread.Status != "open" {
		return nil, fmt.Errorf("run explain: thread %s is %s (not open)", opts.TID, thread.Status)
	}

	dir := opts.WorkDir
	if dir == "" {
		d, err := os.MkdirTemp("", "examtopics-explain-")
		if err != nil {
			return nil, fmt.Errorf("mkdir temp: %w", err)
		}
		dir = d
		defer func() { _ = os.RemoveAll(dir) }()
	}

	inputPath := filepath.Join(dir, "input.json")
	outputPath := filepath.Join(dir, "output.json")
	inJSON, err := json.MarshalIndent(thread, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal input: %w", err)
	}
	if err := os.WriteFile(inputPath, inJSON, 0o644); err != nil {
		return nil, fmt.Errorf("write input: %w", err)
	}

	if opts.DryRun {
		return nil, nil
	}

	// Resolve initial session id: explicit override beats stored value.
	sessionID := opts.SessionID
	if sessionID == "" {
		var ns sql.NullString
		// Decoding is cheap; reuse to avoid stamping the SQL with the
		// raw 26-char string when the column is BLOB-keyed.
		tidBytes, err := uuidx.Decode(opts.TID)
		if err != nil {
			return nil, fmt.Errorf("decode thread id %q: %w", opts.TID, err)
		}
		if err := opts.DB.QueryRow(`SELECT agent_session_id FROM explanation_threads WHERE id = ?`, tidBytes[:]).Scan(&ns); err != nil {
			return nil, fmt.Errorf("read session id: %w", err)
		}
		if ns.Valid {
			sessionID = ns.String
		}
	}

	res, err := opts.Adapter.RunExplain(ctx, ExplainRunOpts{
		InputPath:  inputPath,
		OutputPath: outputPath,
		Thread:     thread,
		SessionID:  sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("adapter %s run: %w", opts.Adapter.Name(), err)
	}

	outBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read adapter output %s: %w", outputPath, err)
	}
	var reply Reply
	if err := json.Unmarshal(outBytes, &reply); err != nil {
		return nil, fmt.Errorf("parse adapter output: %w", err)
	}
	if reply.ThreadID != "" && reply.ThreadID != opts.TID {
		return nil, fmt.Errorf("adapter output thread_id=%s, expected %s", reply.ThreadID, opts.TID)
	}
	if err := ValidateReply(&reply); err != nil {
		return nil, fmt.Errorf("validate reply: %w", err)
	}

	newSessionID := res.SessionID
	if newSessionID == "" {
		// Adapter didn't surface a session id; keep whatever was stored
		// (passing "" to WriteReply leaves the column alone).
		newSessionID = ""
	}

	if err := WriteReply(opts.DB, opts.TID, &reply, opts.HostID, newSessionID); err != nil {
		return nil, err
	}
	// Echo back so callers see the value that was actually persisted —
	// helps tests assert end-to-end without re-querying the DB.
	reply.ThreadID = opts.TID
	return &reply, nil
}
