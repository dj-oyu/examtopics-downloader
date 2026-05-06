package fetch

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"

	"examtopics-downloader/internal/constants"
	"examtopics-downloader/internal/models"
	"examtopics-downloader/internal/utils"

	"github.com/PuerkitoBio/goquery"
)

// cleanAnswer normalizes the raw text from `.correct-answer` into a compact
// answer string like "A", "BD", "ACE". Whitespace and newlines are removed;
// the FULL letter sequence is preserved (the legacy `[0]`-truncation bug at
// scraper.go:35 turned multi-correct answers like BD/AE into B/A).
// `md_to_sqlite.py` parses `**Answer:**` with regex `[A-Z]+`, so multi-letter
// answers in the MD output are forward-compatible.
func cleanAnswer(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\t", "")
	return s
}

func getDataFromLink(link string) *models.QuestionData {
	doc, err := ParseHTML(link, *client)
	if err != nil {
		log.Printf("Failed parsing HTML data from link: %v", err)
		return nil
	}

	var allQuestions []string
	doc.Find("li.multi-choice-item").Each(func(i int, s *goquery.Selection) {
		allQuestions = append(allQuestions, utils.CleanText(s.Text()))
	})

	answer := cleanAnswer(doc.Find(".correct-answer").Text())

	return &models.QuestionData{
		Title:           utils.CleanText(doc.Find("h1").Text()),
		Header:          strings.ReplaceAll(strings.TrimSpace(doc.Find(".question-discussion-header").Text()), "\t", ""),
		Content:         utils.CleanText(doc.Find(".card-text").Text()),
		Questions:       allQuestions,
		Answer:          answer,
		SuggestedAnswer: answer,
		Timestamp:       utils.CleanText(doc.Find(".discussion-meta-data > i").Text()),
		QuestionLink:    link,
		Comments:        utils.CleanText(doc.Find(".discussion-container").Text()),
	}
}

var counter int = 0 //start counter at 1
func getJSONFromLink(link string) []*models.QuestionData {
	initialResp := FetchURL(link, *client)

	var githubResp map[string]any
	err := json.Unmarshal(initialResp, &githubResp)
	if err != nil {
		log.Printf("error unmarshalling GitHub API response: %v", err)
		return nil
	}

	downloadURL, ok := githubResp["download_url"].(string)
	if !ok {
		log.Printf("couldn't find download_url in GitHub API response")
		return nil
	}

	jsonResp := FetchURL(downloadURL, *client)

	var content models.JSONResponse
	err = json.Unmarshal(jsonResp, &content)
	if err != nil {
		log.Printf("error unmarshalling the questions data: %v", err)
		return nil
	}

	fmt.Println("Processing content from:", downloadURL)

	if content.PageProps.Questions == nil {
		log.Printf("no questions found in JSON content")
		return nil
	}

	return ConvertCachedJSON(content, utils.GetNameFromLink(link))
}

// ConvertCachedJSON turns a parsed cache-side JSONResponse into the
// QuestionData slice the rest of the pipeline consumes. Compared to the
// legacy in-loop conversion this used to do inline, it ALSO populates
//   - QuestionData.SuggestedAnswer (full multi-letter, no truncation)
//   - QuestionData.Extras (per-poster discussion, images, IsMC, ExamID,
//     AnswerDescription) which the SQLite-direct writer reads.
//
// Legacy fields (Title, Header, Answer, Comments) keep their MD-output
// formatting, so the existing markdown writer is unaffected.
func ConvertCachedJSON(content models.JSONResponse, name string) []*models.QuestionData {
	var out []*models.QuestionData
	for _, q := range content.PageProps.Questions {
		var sb strings.Builder
		for _, d := range q.Discussion {
			sb.WriteString("[")
			sb.WriteString(d.Poster)
			sb.WriteString("] ")
			sb.WriteString(d.Content)
			sb.WriteString("\n")
		}
		commentsFlat := utils.CleanText(sb.String())

		var choicesHeader strings.Builder
		keys := make([]string, 0, len(q.Choices))
		for key := range q.Choices {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			choicesHeader.WriteString("**")
			choicesHeader.WriteString(key)
			choicesHeader.WriteString(":** ")
			choicesHeader.WriteString(q.Choices[key])
			choicesHeader.WriteString("\n\n")
		}

		discussion := make([]models.DiscussionEntry, 0, len(q.Discussion))
		for _, d := range q.Discussion {
			discussion = append(discussion, models.DiscussionEntry{
				Poster:      d.Poster,
				Content:     d.Content,
				UpvoteCount: d.UpvoteCount,
				Timestamp:   d.Timestamp,
			})
		}

		counter++
		out = append(out, &models.QuestionData{
			Title:           "Examtopics " + strings.ReplaceAll(name, ".json?ref=main", "") + " question #" + strconv.Itoa(counter),
			Header:          q.QuestionText,
			Content:         strings.Join(q.QuestionImages, "\n"),
			Questions:       []string{choicesHeader.String()},
			Answer:          q.Answer,
			SuggestedAnswer: q.Answer,
			Timestamp:       q.Timestamp,
			QuestionLink:    q.URL,
			Comments:        commentsFlat,
			Extras: &models.QuestionExtras{
				ExamID:            q.ExamID,
				IsMC:              q.IsMC,
				AnswerDescription: q.AnswerDescription,
				QuestionImages:    q.QuestionImages,
				AnswerImages:      q.AnswerImages,
				Discussion:        discussion,
			},
		})
	}
	return out
}

func fetchAllPageLinksConcurrently(providerName, grepStr string, numPages, concurrency int) []string {
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	results := make(chan []string, numPages)
	bar := utils.NewProgressBar("page links", numPages)
	startTime := utils.StartTime()

	rateLimiter := utils.CreateRateLimiter(constants.RequestsPerSecond)
	defer rateLimiter.Stop()

	for i := 1; i <= numPages; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			<-rateLimiter.C

			url := fmt.Sprintf("https://www.examtopics.com/discussions/%s/%d", providerName, i)
			results <- getLinksFromPage(url, grepStr)
			bar.Increment()
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// about 10 questions per examtopics page, we can preallocate
	all := make([]string, 0, numPages*10)
	for res := range results {
		all = append(all, res...)
	}

	bar.Finish()
	fmt.Printf("Scraping completed in %s.\n", utils.TimeSince(startTime))
	return all
}

// Main concurrent page scraping logic
func GetAllPages(providerName string, grepStr string) []models.QuestionData {
	baseURL := fmt.Sprintf("https://www.examtopics.com/discussions/%s/", providerName)
	numPages := getMaxNumPages(baseURL)
	fmt.Printf("Fetching %d pages for provider '%s'\n", numPages, providerName)

	allLinks := fetchAllPageLinksConcurrently(providerName, grepStr, numPages, constants.MaxConcurrentRequests)

	unique := utils.DeduplicateLinks(allLinks)
	sortedLinks := utils.SortLinksByQuestionNumber(unique)

	fmt.Printf("Found %d unique matching links:\n", len(sortedLinks))

	var wg sync.WaitGroup
	sem := make(chan struct{}, constants.MaxConcurrentRequests)
	results := make([]*models.QuestionData, len(sortedLinks))
	startTime := utils.StartTime()
	bar := utils.NewProgressBar("question pages", len(sortedLinks))

	rateLimiter := utils.CreateRateLimiter(constants.RequestsPerSecond)
	defer rateLimiter.Stop()

	for i, link := range sortedLinks {
		wg.Add(1)
		url := utils.AddToBaseUrl(link)

		go func(i int, url string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			<-rateLimiter.C

			data := getDataFromLink(url)
			if data != nil {
				results[i] = data
			}
			bar.Increment()
		}(i, url)
	}

	wg.Wait()
	bar.Finish()
	// Filter out nil entries
	var finalData []models.QuestionData
	for _, entry := range results {
		if entry != nil {
			finalData = append(finalData, *entry)
		}
	}

	fmt.Printf("Scraping completed in %s.\n", utils.TimeSince(startTime))

	return finalData
}
