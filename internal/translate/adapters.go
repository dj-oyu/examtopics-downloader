package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Adapter spawns the chosen LLM CLI and arranges for it to read the
// input JSON at RunOpts.InputPath and write a Translation JSON to
// RunOpts.OutputPath. Each implementation owns the per-vendor
// argument convention (allowed tools, output format, model selection)
// — Retranslate above only cares that OutputPath exists and parses
// after Run returns.
type Adapter interface {
	Name() string
	Run(ctx context.Context, opts RunOpts) error
}

// RunOpts is the contract Retranslate hands to an Adapter. Question
// is supplied so adapters that prefer to embed the row inline in the
// prompt (instead of pointing the model at InputPath) can do so
// without re-reading the file.
type RunOpts struct {
	InputPath  string
	OutputPath string
	Question   *QuestionPayload
}

// retranslateInstructions is the prompt body shared across adapters.
// Style rules mirror buildRetranslatePrompt in web/src/agent.ts so a
// /retranslate triggered from the web produces output indistinguishable
// from an `examtopicsdl translate retranslate` invocation.
const retranslateInstructions = `You are translating ONE AWS exam question into Japanese for the Exam Studio DB.

Translation rules (do NOT diverge):
- AWS service names stay in English (Amazon S3, AWS Lambda, Amazon Aurora).
- 平叙文 / である調 (NOT 敬体).
- If suggested_answer length > 1 (multi-select), prepend "(複数選択) " to question_text_ja.
- Choices A–E: keep length and tone consistent.
- explanation_ja: 2–3 sentences covering 正解の根拠 + 主要な不正解の根拠. Use the comments column when it adds counter-arguments or community consensus.

Steps:
1. Read the input row from the file passed in <INPUT_PATH>. Its JSON includes question_text, choices[].text, suggested_answer, comments, and an existing_ja block (for context only — overwrite it).
2. Translate per the rules above.
3. Write a JSON OBJECT (not array) to the file at <OUTPUT_PATH> (UTF-8) with this exact shape:
   {
     "id": <id from input>,
     "question_text_ja": "...",
     "explanation_ja": "...",
     "choices_ja": { "A": "...", "B": "...", "C": "...", "D": "..." }
   }
   Include every choice label that exists in the input.
4. Stop after the file is written. Do not echo the translated text in your response.`

func buildPrompt(opts RunOpts) string {
	return strings.NewReplacer(
		"<INPUT_PATH>", opts.InputPath,
		"<OUTPUT_PATH>", opts.OutputPath,
	).Replace(retranslateInstructions)
}

// ClaudeAdapter spawns `claude -p <prompt> --output-format json
// --allowed-tools Read,Write` so the model can pull in the staged
// input file and write the result back. The binary path can be
// overridden via the CLAUDE_BIN env var (matches web/src/agent.ts).
// Model, when non-empty, is passed through as `--model <model>` so
// the same adapter handles claude-sonnet / claude-opus / etc.
type ClaudeAdapter struct {
	Bin   string
	Model string
}

func (a *ClaudeAdapter) Name() string { return "claude" }

func (a *ClaudeAdapter) Run(ctx context.Context, opts RunOpts) error {
	bin := a.Bin
	if bin == "" {
		bin = "claude"
	}
	args := []string{
		"-p", buildPrompt(opts),
		"--output-format", "json",
		"--allowed-tools", "Read,Write",
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude exec: %w (stdout/stderr: %s)", err, truncate(string(out), 1000))
	}
	return nil
}

// GeminiAdapter is a placeholder — the gemini-cli skill discovery
// expects .gemini/skills/ layout and a worker subagent pattern, which
// task 4-H deliberately does not wire yet. Keeping the type in the
// interface map lets `-list-clients` enumerate the future surface
// without erroring.
type GeminiAdapter struct{}

func (a *GeminiAdapter) Name() string { return "gemini" }
func (a *GeminiAdapter) Run(_ context.Context, _ RunOpts) error {
	return errors.New("gemini adapter not yet wired — pending §3.6 worker dispatcher")
}

// CodexAdapter is a placeholder.
type CodexAdapter struct{}

func (a *CodexAdapter) Name() string { return "codex" }
func (a *CodexAdapter) Run(_ context.Context, _ RunOpts) error {
	return errors.New("codex adapter not yet wired")
}

// ExecAdapter runs a user-configured argv with the staged input/output
// paths exposed via env (EXAMTOPICS_TRANSLATE_INPUT / _OUTPUT) so a
// custom translator binary can be plugged in without touching this
// package. Empty Argv falls back to a clear error rather than a noisy
// exec of "" with surprising exit codes.
type ExecAdapter struct {
	Argv []string
}

func (a *ExecAdapter) Name() string { return "exec" }
func (a *ExecAdapter) Run(ctx context.Context, opts RunOpts) error {
	if len(a.Argv) == 0 {
		return errors.New("exec adapter: argv is empty (set tools.translate.exec.cmd in config)")
	}
	cmd := exec.CommandContext(ctx, a.Argv[0], a.Argv[1:]...)
	cmd.Env = append(os.Environ(),
		"EXAMTOPICS_TRANSLATE_INPUT="+opts.InputPath,
		"EXAMTOPICS_TRANSLATE_OUTPUT="+opts.OutputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("exec %v: %w (stdout/stderr: %s)", a.Argv, err, truncate(string(out), 1000))
	}
	return nil
}

// AdapterOpts threads config-derived knobs (model, binary override)
// into AdapterFor / ExplainAdapterFor without forcing every test that
// just wants a default adapter to construct an opts struct. Callers
// pass zero or one AdapterOpts; extras are ignored.
type AdapterOpts struct {
	Model string // passed as --model when the client supports it
	Bin   string // overrides CLAUDE_BIN / equivalent client default
}

// AdapterFor returns the Adapter that pairs with the named client.
// Caller should treat ok=false as "unknown client name"; the four
// names mirror KnownClients() so -list-clients output and -client
// flag values stay aligned.
//
// AdapterOpts is variadic so existing test call sites (`AdapterFor("claude")`)
// keep compiling. Production CLI / Web paths pass an opts populated
// from config.Tools.Translate.
func AdapterFor(name string, opts ...AdapterOpts) (Adapter, bool) {
	var o AdapterOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	switch name {
	case "claude":
		bin := o.Bin
		if bin == "" {
			bin = os.Getenv("CLAUDE_BIN")
		}
		return &ClaudeAdapter{Bin: bin, Model: o.Model}, true
	case "gemini":
		return &GeminiAdapter{}, true
	case "codex":
		return &CodexAdapter{}, true
	case "exec":
		// Argv is intentionally empty; callers that want exec are
		// expected to wire tools.translate.exec.cmd from config and
		// instantiate the struct directly.
		return &ExecAdapter{}, true
	}
	return nil, false
}

// ExplainAdapter spawns the chosen LLM CLI for the multi-turn
// explanation-thread reply path. It is a separate interface from
// Adapter because explain has a session-id round-trip and an
// allowed-tools list (AWS Docs MCP) that retranslate doesn't share —
// shoehorning both into one Run signature would either bloat RunOpts
// with always-empty fields or force every adapter to implement a
// stub for the other side. A separate type also keeps the test
// stubs (stubAdapter / stubExplainAdapter) cleanly distinct.
type ExplainAdapter interface {
	Name() string
	RunExplain(ctx context.Context, opts ExplainRunOpts) (ExplainRunResult, error)
}

// ExplainRunOpts is the contract RunExplain hands to an ExplainAdapter.
// SessionID is the previously-stored agent_session_id (empty on the
// first turn, populated on follow-up turns); Thread is the parsed
// payload so adapters that prefer to embed parts of it inline can do
// so without re-reading InputPath.
type ExplainRunOpts struct {
	InputPath  string
	OutputPath string
	Thread     *ThreadPayload
	SessionID  string
}

// ExplainRunResult lets the adapter surface the LLM's session_id back
// to the orchestrator so RunExplain can persist it on the thread for
// the next turn's --resume.
type ExplainRunResult struct {
	SessionID string
}

// explainAllowedTools matches EXPLAIN_ALLOWED_TOOLS in
// web/src/agent.ts. Centralised here so a future MCP rename lands in
// one place; the constant is verbatim what the historical
// claude-spawned-from-web invocation passed.
const explainAllowedTools = "Bash,mcp__claude_ai_AWS_Knowledge_MCP_Server__aws___search_documentation," +
	"mcp__claude_ai_AWS_Knowledge_MCP_Server__aws___read_documentation," +
	"mcp__claude_ai_AWS_Knowledge_MCP_Server__aws___recommend"

// explainInstructions is the prompt body the claude adapter uses on
// the first turn (no session id). It points the LLM at InputPath +
// OutputPath instead of pasting the JSON inline so the adapter does
// not need to know about the thread payload's fields. The validation
// rules referenced here live in ValidateReply (Go) and mirror
// _validate_reason in tools/translate.py — the LLM must produce a
// payload that survives ValidateReply or RunExplain rejects it.
const explainInstructions = `You are the explanation agent for an AWS exam study tool. The user asked a question on a specific exam item; you must reply with a structured agent message.

Steps:
1. Read the thread payload from <INPUT_PATH>. Its JSON includes thread_id, status, the question (with English + Japanese fields, comments, and choices), and the full message history. The DB is the source of truth — do not rely on memory alone.
2. Compose a Japanese reply for the latest user message. Choose exactly one reason_code (priority: translation > comprehension > spec > ambiguous). Do NOT set resolve=true automatically; only set it if the user explicitly indicated the thread is done.
3. For reason_code='spec' or 'ambiguous', fetch citations via the AWS Documentation MCP tools (aws___search_documentation, aws___read_documentation). At least one citation URL must contain 'docs.aws.amazon.com'.
4. For reason_code='translation', set translation_fix to a non-empty object containing at least one of {question_text_ja, explanation_ja, choices_ja}.
5. Write a JSON OBJECT (not array) to <OUTPUT_PATH> (UTF-8) with this exact shape:
   {
     "thread_id": "<thread_id from input>",
     "content": "...",                       // required Japanese reply text
     "author": "claude-code",
     "resolve": false,
     "reason_code": "comprehension|spec|ambiguous|translation", // optional
     "citations": [{"url": "...", "title": "..."}],             // required for spec/ambiguous
     "translation_fix": { ... },                                 // required for translation
     "explanation_ja": "..."                                     // optional legacy field; ignored when reason_code==translation
   }
6. Stop after the file is written. Do not echo the reply text in your response.`

func buildExplainPrompt(opts ExplainRunOpts) string {
	return strings.NewReplacer(
		"<INPUT_PATH>", opts.InputPath,
		"<OUTPUT_PATH>", opts.OutputPath,
	).Replace(explainInstructions)
}

// extractClaudeSessionID mirrors web/src/agent.ts extractSessionId:
// the wrapper JSON `claude -p --output-format json` prints carries a
// session_id field. Try whole-stdout JSON first, then fall back to the
// last non-empty line. Returning "" means "no id found"; the caller
// should preserve whatever the DB already stored in that case.
func extractClaudeSessionID(stdout string) string {
	tryParse := func(s string) string {
		var o struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &o); err == nil && o.SessionID != "" {
			return o.SessionID
		}
		return ""
	}
	if id := tryParse(stdout); id != "" {
		return id
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if id := tryParse(lines[i]); id != "" {
			return id
		}
	}
	return ""
}

// ClaudeExplainAdapter spawns `claude -p ... --output-format json
// --allowed-tools <EXPLAIN_ALLOWED_TOOLS>` and, when SessionID is
// non-empty, also passes `--resume <SessionID>`. The wrapper JSON's
// session_id is extracted and surfaced back to the orchestrator so the
// next turn can resume on the same conversation.
//
// Bin defaults to "claude" (mirrors CLAUDE_BIN env in web/src/agent.ts);
// Model, when non-empty, is passed through as `--model <model>` so
// the same adapter handles claude-sonnet / claude-opus / etc.
type ClaudeExplainAdapter struct {
	Bin   string
	Model string
}

func (a *ClaudeExplainAdapter) Name() string { return "claude" }

func (a *ClaudeExplainAdapter) RunExplain(ctx context.Context, opts ExplainRunOpts) (ExplainRunResult, error) {
	bin := a.Bin
	if bin == "" {
		bin = "claude"
	}
	args := []string{}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	args = append(args,
		"-p", buildExplainPrompt(opts),
		"--output-format", "json",
		"--allowed-tools", explainAllowedTools,
	)
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return ExplainRunResult{}, fmt.Errorf("claude exec: %w (stderr: %s)", err, truncate(stderr, 1000))
	}
	return ExplainRunResult{SessionID: extractClaudeSessionID(string(out))}, nil
}

// GeminiExplainAdapter / CodexExplainAdapter / ExecExplainAdapter are
// placeholders — their non-explain twins (GeminiAdapter, CodexAdapter,
// ExecAdapter) likewise stub out, and the explain path was never wired
// to anything but claude. Returning a clear error keeps `-client gemini`
// failing fast instead of silently writing nothing.
type GeminiExplainAdapter struct{}

func (a *GeminiExplainAdapter) Name() string { return "gemini" }
func (a *GeminiExplainAdapter) RunExplain(_ context.Context, _ ExplainRunOpts) (ExplainRunResult, error) {
	return ExplainRunResult{}, errors.New("gemini explain adapter not yet wired — pending §3.6 worker dispatcher")
}

type CodexExplainAdapter struct{}

func (a *CodexExplainAdapter) Name() string { return "codex" }
func (a *CodexExplainAdapter) RunExplain(_ context.Context, _ ExplainRunOpts) (ExplainRunResult, error) {
	return ExplainRunResult{}, errors.New("codex explain adapter not yet wired")
}

type ExecExplainAdapter struct{}

func (a *ExecExplainAdapter) Name() string { return "exec" }
func (a *ExecExplainAdapter) RunExplain(_ context.Context, _ ExplainRunOpts) (ExplainRunResult, error) {
	return ExplainRunResult{}, errors.New("exec explain adapter not yet wired")
}

// ExplainAdapterFor mirrors AdapterFor for the explain path. Today
// only claude returns a working adapter; the others surface a clear
// "not yet wired" error from RunExplain so misconfigured `-client`
// fails fast at runtime rather than silently producing an empty
// reply.
//
// AdapterOpts is variadic to keep existing test call sites working
// without an explicit empty struct. Production callers pass an opts
// populated from config.Tools.Translate so model + bin overrides
// propagate uniformly.
func ExplainAdapterFor(name string, opts ...AdapterOpts) (ExplainAdapter, bool) {
	var o AdapterOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	switch name {
	case "claude":
		bin := o.Bin
		if bin == "" {
			bin = os.Getenv("CLAUDE_BIN")
		}
		return &ClaudeExplainAdapter{Bin: bin, Model: o.Model}, true
	case "gemini":
		return &GeminiExplainAdapter{}, true
	case "codex":
		return &CodexExplainAdapter{}, true
	case "exec":
		return &ExecExplainAdapter{}, true
	}
	return nil, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
