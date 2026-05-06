package translate

import (
	"context"
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
type ClaudeAdapter struct {
	Bin string
}

func (a *ClaudeAdapter) Name() string { return "claude" }

func (a *ClaudeAdapter) Run(ctx context.Context, opts RunOpts) error {
	bin := a.Bin
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin,
		"-p", buildPrompt(opts),
		"--output-format", "json",
		"--allowed-tools", "Read,Write",
	)
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

// AdapterFor returns the Adapter that pairs with the named client.
// Caller should treat ok=false as "unknown client name"; the four
// names mirror KnownClients() so -list-clients output and -client
// flag values stay aligned.
func AdapterFor(name string) (Adapter, bool) {
	switch name {
	case "claude":
		return &ClaudeAdapter{Bin: os.Getenv("CLAUDE_BIN")}, true
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
