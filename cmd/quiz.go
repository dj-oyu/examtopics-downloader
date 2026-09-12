package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"examtopics-downloader/internal/config"
	"examtopics-downloader/internal/quiz"
	"examtopics-downloader/internal/sqlite"
	"examtopics-downloader/internal/utils"
)

// runQuiz implements `examtopicsdl quiz`. It loads the host id from the
// runtime config (auto-generating one on first use), opens the supplied
// DB, and walks each question in order, prompting the user for a letter
// answer (e.g., "A" or "BD") and recording every attempt with a UUIDv7
// primary key. EOF on stdin halts the loop early — useful for piping
// answers in tests or scripted dry-runs.
func runQuiz(args []string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "quiz: load config: %v\n", err)
		return 1
	}
	return runQuizTo(os.Stdin, os.Stdout, cfg.HostID, args)
}

// runQuizTo is the testable seam: callers supply the I/O streams and the
// host id explicitly so quiz_test.go can drive the loop with a string
// reader and a bytes.Buffer.
func runQuizTo(in io.Reader, out io.Writer, hostID string, args []string) int {
	fs := flag.NewFlagSet("quiz", flag.ContinueOnError)
	fs.SetOutput(out)
	dbPath := fs.String("db", "", "Path to the SQLite DB to quiz against (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	w := utils.NewWriteErr(out)
	if *dbPath == "" {
		w.Println("quiz: -db is required")
		return 2
	}
	db, err := sqlite.OpenWith(*dbPath, sqlite.OpenOpts{HostID: hostID, Backup: true})
	if err != nil {
		w.Printf("quiz: open %s: %v\n", *dbPath, err)
		return 1
	}
	defer func() { _ = db.Close() }()

	questions, err := quiz.LoadQuestions(db)
	if err != nil {
		w.Printf("quiz: load questions: %v\n", err)
		return 1
	}
	if len(questions) == 0 {
		w.Println("quiz: no questions in DB")
		return 0
	}

	reader := bufio.NewReader(in)
	asked, correctCount := 0, 0
	for _, q := range questions {
		w.Printf("\nQ%d.%d  %s\n", q.Topic, q.QuestionNumber, q.QuestionText)
		for _, c := range q.Choices {
			w.Printf("  %s) %s\n", c.Label, c.Text)
		}
		w.Printf("answer (letter[s], blank to skip): ")
		line, readErr := reader.ReadString('\n')
		if errors.Is(readErr, io.EOF) && line == "" {
			break
		}
		answer := stripLine(line)
		if answer == "" {
			w.Println("(skipped)")
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		matched, err := quiz.RecordAttemptAgainst(db, hostID, q.ID, answer, q.AcceptedAnswers())
		if err != nil {
			fmt.Fprintf(os.Stderr, "quiz: record: %v\n", err)
			return 1
		}
		asked++
		switch {
		case matched.Ungraded:
			w.Println("— 判定不能（正解を特定できない問題のため採点対象外）")
		case matched.Correct:
			correctCount++
			w.Println("Correct.")
		default:
			w.Printf("Incorrect. Accepted: %s\n", strings.Join(q.AcceptedAnswers(), " or "))
		}
		printVerdict(w, q)
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	w.Printf("\nDone — %d / %d answered correctly\n", correctCount, asked)
	return 0
}

// printVerdict reports the community split and, when the question is contested,
// why more than one answer counts — shown only once the learner has answered.
func printVerdict(w *utils.WriteErr, q quiz.Question) {
	accepted := q.AcceptedAnswers()
	switch q.Verdict.Status {
	case "ambiguous":
		w.Printf("この問題はコミュニティでも意見が割れています（%s のいずれも正答として扱います）\n",
			strings.Join(accepted, " / "))
	case "unknown":
		w.Println("正解を特定できない問題です（採点対象外）")
	}
	if len(q.Verdict.Community) > 0 {
		parts := make([]string, 0, len(q.Verdict.Community))
		for _, v := range q.Verdict.Community {
			mark := ""
			if quiz.IsAccepted(v.Label, accepted) {
				mark = "*"
			}
			parts = append(parts, fmt.Sprintf("%s%s %.1f%% (%d票)", v.Label, mark, v.Pct, v.Votes))
		}
		w.Printf("コミュニティ投票 (%d票): %s\n", q.Verdict.TotalVotes, strings.Join(parts, "  "))
	}
	if q.Verdict.Rationale != "" {
		w.Printf("%s\n", q.Verdict.Rationale)
	}
}

func stripLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}
