# Skill: Exam Translator Orchestrator

This skill enables the agent to autonomously manage high-volume translation of AWS exam questions while maintaining data integrity and context efficiency.

## Pre-requisites
- SQLite database containing ExamTopics questions.
- `tools/translate.py` with `bulk-next` and `bulk-save` commands.
- `tools/batch_helper.py` for safe I/O on Windows.

## Orchestration Workflow (CRITICAL)

When this skill is activated, follow these steps to fulfill a translation request:

1.  **Preparation**:
    - Ensure `tools/translation_master_prompt.md` exists as the ground truth for subagents.
    - Check current status: `uv run tools/translate.py -d [DB] status`.

2.  **Delegation (The Power Move)**:
    - Invoke the `generalist` subagent with the following prompt template:
      > "Load `tools/translation_master_prompt.md` and complete the translation of `[DB]`. 
      > Use `tools/batch_helper.py` for all saving operations to ensure UTF-8. 
      > Aim for `questions_pending: 0`. Report ONLY range summaries and final status."

3.  **Monitoring**:
    - If the subagent reaches its limit before completion, analyze the reported ID range.
    - Immediately re-invoke the subagent for the next batch without asking the user.
    - Continue until `questions_pending` is 0.

4.  **Final Validation**:
    - Run `uv run tools/translate.py -d [DB] status` and verify `questions_pending == 0`.
    - Sample 3-5 rows to ensure no mojibake: `uv run tools/translate.py -d [DB] bulk-next --limit 3`.

## Mandates for Subagent Prompts
- Always explicitly state: "You are in EXECUTION MODE. Do not enter Plan Mode."
- Always include the "UTF-8 temporary file" requirement for Windows stability.
- Always demand "Summary only" reporting to save main-session context tokens.
