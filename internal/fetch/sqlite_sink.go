package fetch

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"

	"examtopics-downloader/internal/constants"
	"examtopics-downloader/internal/models"
	"examtopics-downloader/internal/sqlite"
	"examtopics-downloader/internal/utils"

	"github.com/cheggaaa/pb/v3"
)

// questionHashTitleRe matches "question #N" in the cache-path Title format
// "Examtopics <exam name> question #N" — used as a fallback for
// question_number when the URL has no `-question-N-discussion` segment.
var questionHashTitleRe = regexp.MustCompile(`question\s*#\s*(\d+)`)

// QuestionDataToRecord converts a scraped QuestionData into the writer-facing
// QuestionRecord. examDisplay is the value to write into questions.exam.
//
// Topic and question_number are recovered from QuestionData.QuestionLink via
// the URL helpers in internal/utils, then from Extras.QuestionID (cache JSON's
// own per-exam question number) and finally from a "question #N" marker in the
// Title. SuggestedAnswer falls back to the legacy Answer field when not
// explicitly set (defensive — manual path always sets SuggestedAnswer now, but
// cache path or legacy callers might not).
//
// Choices are recovered from QuestionData.Extras when present (cache path);
// otherwise (manual path) callers must populate the choices argument.
func QuestionDataToRecord(qd *models.QuestionData, examDisplay string, choices map[string]string) *sqlite.QuestionRecord {
	suggested := qd.SuggestedAnswer
	if suggested == "" {
		suggested = qd.Answer
	}
	qnum := utils.ExtractQuestionNum(qd.QuestionLink)
	if qnum == 0 && qd.Extras != nil && qd.Extras.QuestionID > 0 {
		qnum = qd.Extras.QuestionID
	}
	if qnum == 0 {
		// Cache-path URLs have no `-question-N-discussion` segment and no
		// QuestionID; fall back to the Title's "question #N" marker written by
		// ConvertCachedJSON.
		if m := questionHashTitleRe.FindStringSubmatch(qd.Title); m != nil {
			fmt.Sscanf(m[1], "%d", &qnum)
		}
	}
	rec := &sqlite.QuestionRecord{
		Exam:            examDisplay,
		Topic:           utils.ExtractTopicNum(qd.QuestionLink),
		QuestionNumber:  qnum,
		QuestionText:    strings.TrimSpace(qd.Header),
		SuggestedAnswer: suggested,
		ConfirmedAnswer: qd.Answer,
		Timestamp:       qd.Timestamp,
		URL:             qd.QuestionLink,
		Comments:        qd.Comments,
		Choices:         choices,
	}
	if qd.Extras != nil {
		rec.ExamID = qd.Extras.ExamID
		rec.IsMC = qd.Extras.IsMC
		rec.AnswerDescription = qd.Extras.AnswerDescription
		rec.QuestionImages = qd.Extras.QuestionImages
		rec.AnswerImages = qd.Extras.AnswerImages
		for _, d := range qd.Extras.Discussion {
			rec.Discussion = append(rec.Discussion, sqlite.DiscussionRow{
				Idx:         len(rec.Discussion),
				Poster:      d.Poster,
				Content:     d.Content,
				UpvoteCount: parseUpvote(d.UpvoteCount),
				PostedAt:    d.Timestamp,
			})
		}
	}
	return rec
}

// parseUpvote turns the JSON "upvote_count" string ("3", "", "12") into an int.
// Empty or unparseable values become 0.
func parseUpvote(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0
	}
	return n
}

// titleToExamDisplay extracts "AWS Certified Developer Associate DVA C02"
// from the H1 title style "Exam AWS Certified ... DVA C02 topic 1 question 5 discussion".
// Falls back to the input verbatim when the expected anchors are missing.
func titleToExamDisplay(title string) string {
	t := strings.TrimSpace(title)
	t = strings.TrimPrefix(t, "Exam ")
	if i := strings.Index(t, " topic "); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// cachedSinkResult is one converted question on its way to the writer.
// Used internally by the cache-path goroutines.
type cachedSinkResult struct {
	examDisplay string
	choices     map[string]string
	qd          *models.QuestionData
}

// GetCachedPagesToSQLite mirrors GetCachedPages but writes each question
// directly into w (single transaction managed by the caller). Returns the
// number of questions written.
func GetCachedPagesToSQLite(providerName, grepStr, token string, w *sqlite.Writer) (int, error) {
	links := FetchCachedLinks(providerName, grepStr, token)
	if len(links) == 0 {
		return 0, nil
	}

	resCh := make(chan cachedSinkResult, len(links)*16)

	var wg sync.WaitGroup
	for _, link := range links {
		wg.Add(1)
		go func(link string) {
			defer wg.Done()
			feedCachedSink(link, resCh)
		}(link)
	}
	go func() { wg.Wait(); close(resCh) }()

	count := 0
	for r := range resCh {
		rec := QuestionDataToRecord(r.qd, r.examDisplay, r.choices)
		if _, err := w.UpsertQuestion(rec); err != nil {
			return count, fmt.Errorf("upsert %s: %w", rec.URL, err)
		}
		count++
	}
	return count, nil
}

// feedCachedSink fetches one cached file (GitHub API → raw download), parses
// it, and emits one cachedSinkResult per question.
func feedCachedSink(link string, ch chan<- cachedSinkResult) {
	initial := FetchURL(link, *client)
	if initial == nil {
		return
	}
	var meta map[string]any
	if err := json.Unmarshal(initial, &meta); err != nil {
		log.Printf("github meta parse: %v", err)
		return
	}
	dl, ok := meta["download_url"].(string)
	if !ok {
		return
	}
	body := FetchURL(dl, *client)
	if body == nil {
		return
	}
	var content models.JSONResponse
	if err := json.Unmarshal(body, &content); err != nil {
		log.Printf("cache json parse: %v", err)
		return
	}
	name := utils.GetNameFromLink(link)
	exam := utils.DeriveExamDisplay(name)
	qds := ConvertCachedJSON(content, name)
	for i, qd := range qds {
		ch <- cachedSinkResult{
			examDisplay: exam,
			choices:     content.PageProps.Questions[i].Choices,
			qd:          qd,
		}
	}
}

// GetAllPagesToSQLite mirrors GetAllPages but writes each question into w as
// the manual scrape produces it. Manual-path comments stay as a single text
// blob (no per-poster split available from HTML), and Extras stay nil.
// Choices are extracted from QuestionData.Questions (the H1-style list of
// answer items) since the manual path doesn't carry a structured map.
func GetAllPagesToSQLite(providerName, grepStr string, w *sqlite.Writer) (int, error) {
	baseURL := fmt.Sprintf("https://www.examtopics.com/discussions/%s/", providerName)
	numPages := getMaxNumPages(baseURL)
	fmt.Printf("Fetching %d pages for provider '%s'\n", numPages, providerName)

	allLinks := fetchAllPageLinksConcurrently(providerName, grepStr, numPages, constants.MaxConcurrentRequests)
	unique := utils.DeduplicateLinks(allLinks)
	sortedLinks := utils.SortLinksByQuestionNumber(unique)
	fmt.Printf("Found %d unique matching links:\n", len(sortedLinks))
	if len(sortedLinks) == 0 {
		return 0, nil
	}

	bar := pb.StartNew(len(sortedLinks))
	defer bar.Finish()

	rl := utils.CreateRateLimiter(constants.RequestsPerSecond)
	defer rl.Stop()

	count := 0
	for _, link := range sortedLinks {
		<-rl.C
		full := utils.AddToBaseUrl(link)
		qd := getDataFromLink(full)
		bar.Increment()
		if qd == nil {
			continue
		}
		choices := extractChoicesFromManualQuestions(qd.Questions)
		examDisplay := titleToExamDisplay(qd.Title)
		rec := QuestionDataToRecord(qd, examDisplay, choices)
		if _, err := w.UpsertQuestion(rec); err != nil {
			return count, fmt.Errorf("upsert %s: %w", rec.URL, err)
		}
		count++
	}
	return count, nil
}

// extractChoicesFromManualQuestions parses the cleaned `li.multi-choice-item`
// strings into a label→text map. Each item arrives like "A. first choice"
// after CleanText strips whitespace. Items that don't start with a single
// uppercase letter + dot are skipped silently.
func extractChoicesFromManualQuestions(items []string) map[string]string {
	out := map[string]string{}
	for _, raw := range items {
		s := strings.TrimSpace(raw)
		if len(s) < 3 || s[1] != '.' {
			continue
		}
		label := s[:1]
		if label[0] < 'A' || label[0] > 'Z' {
			continue
		}
		out[label] = strings.TrimSpace(s[2:])
	}
	return out
}
