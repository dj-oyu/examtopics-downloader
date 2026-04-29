# AWS Exam Translation Master Instructions (v2)

## Task Overview
Autonomous, bulk translation of AWS exam questions from SQLite to Japanese.

## Guidelines (CRITICAL)
- **Terminology**: AWS Service Names MUST remain in English (e.g., Amazon S3, AWS Lambda).
- **Tone**: Use "である" (plain/declarative) style.
- **Multi-select**: If `suggested_answer` has multiple characters (e.g., "BD"), prepend "(複数選択) " to `question_text_ja`.
- **Explanations**: Craft 2-3 sentences for `explanation_ja`. Use `comments` to capture community consensus or common pitfalls.

## Technical Execution (The "Perfect Loop")
You are in **EXECUTION MODE**. Do not enter Plan Mode. Complete the following loop until `questions_pending` is 0 or your turn limit is near.

1.  **Bulk Fetch**: Run `uv run tools/translate.py -d [DB_PATH] bulk-next --limit 50`.
2.  **Translate**: Process the entire JSON array. Maintain the `id` for each item.
3.  **Staging**: Write the translated results as a JSON array to a temporary UTF-8 file (e.g., `_staging.json`).
4.  **Bulk Save**: Execute via Python to guarantee UTF-8 integrity:
    `python -c "import subprocess; payload=open('_staging.json', 'rb').read(); subprocess.run(['uv', 'run', 'tools/translate.py', '-d', '[DB_PATH]', 'bulk-save'], input=payload)"`
5.  **Cleanup**: Delete `_staging.json`.
6.  **Status**: Check `uv run tools/translate.py -d [DB_PATH] status`.

## Reporting
- Report ONLY: `[Batch Completed: ID XXX-YYY. Remaining Pending: ZZZ]`
- **DO NOT** output the translated Japanese text to the main session. Keep it quiet.
