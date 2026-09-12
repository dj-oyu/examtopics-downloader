package fetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"examtopics-downloader/internal/constants"
	"examtopics-downloader/internal/models"
	"examtopics-downloader/internal/utils"

	"github.com/PuerkitoBio/goquery"
)

var client = utils.NewHTTPClient()

// siteClient talks to examtopics.com and is deliberately separate from `client`:
// FetchCachedLinks swaps `client` for an authenticated GitHub client when a PAT
// is supplied, and reusing that same client for the HTML scrape would send the
// GitHub token to examtopics.com.
var siteClient = utils.NewHTTPClient()

// fetchFailures counts URLs that FetchURL gave up on in this process. A nil
// body means the caller lost a page (manual path) or a whole cache file's worth
// of questions (cache path), so runs report it instead of claiming success.
var fetchFailures atomic.Int64

// FetchFailures reports how many fetches have failed so far in this process.
func FetchFailures() int { return int(fetchFailures.Load()) }

// FetchURL fetches url, retrying throttling (429) and server errors (5xx) with
// exponential backoff plus jitter, and honouring Retry-After when present.
//
// Anything else — including 403, which is what the GitHub contents API returns
// once the anonymous 60 requests/hour budget runs out — gives up immediately
// and is logged with the URL, so a partial run is visible rather than silent.
// A nil return means this URL's content is lost, and is counted in
// FetchFailures.
func FetchURL(url string, client http.Client) []byte {
	return fetchURL(url, client, false)
}

// FetchURLProbe is FetchURL for requests whose absence is an expected outcome
// rather than a loss — probing for a provider's cache directory, where the
// cache legitimately 404s for providers that were never mirrored. Only 404 is
// exempted: a 403 there still means we lost cache data.
func FetchURLProbe(url string, client http.Client) []byte {
	return fetchURL(url, client, true)
}

func fetchURL(url string, client http.Client, notFoundIsExpected bool) []byte {
	backoff := constants.InitalBackoff

	for attempt := 0; attempt <= constants.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := utils.DelayTime(backoff)
			log.Printf("Retry attempt %d for URL: %s after waiting %v", attempt, url, delay)
			utils.Sleep(delay)
			backoff = utils.BackoffTime(backoff, constants.BackoffFactor)
		}

		resp, err := client.Get(url)
		if err != nil {
			log.Printf("failed to fetch URL (attempt %d): %v", attempt, err)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				log.Printf("failed to read response body: %v", err)
				fetchFailures.Add(1)
				return nil
			}
			return body
		}

		retryable := utils.RetryableStatus(resp.StatusCode)
		retryAfter := utils.RetryAfterDelay(resp)
		status := resp.StatusCode
		resp.Body.Close()

		if !retryable {
			hint := ""
			if status == http.StatusForbidden {
				hint = " — GitHub/bot rate limit? pass -t <PAT> or set GH_PAT to lift the 60 req/hour anonymous limit"
			}
			log.Printf("request failed with status code: %d for %s%s (not retried)", status, url, hint)
			if !(notFoundIsExpected && status == http.StatusNotFound) {
				fetchFailures.Add(1)
			}
			return nil
		}

		// Prefer the server's own pacing over our backoff when it asks for more.
		if retryAfter > backoff {
			backoff = retryAfter
		}
	}

	log.Printf("exhausted retries for URL: %s", url)
	fetchFailures.Add(1)
	return nil
}

func ParseHTML(url string, client http.Client) (*goquery.Document, error) {
	body := FetchURL(url, client)
	if body == nil {
		return nil, fmt.Errorf("empty response body from URL %q", url)
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML from URL %q: %w", url, err)
	}

	return doc, nil
}

// Fetches total number of pages
func getMaxNumPages(url string) int {
	doc, err := ParseHTML(url, *siteClient)
	if err != nil {
		log.Panicf("Failed parsing HTML for number of pages: %v", err)
	}

	var pageCount int
	doc.Find(".discussion-list-page-indicator strong").Each(func(i int, s *goquery.Selection) {
		if i == 1 {
			pageCount, _ = strconv.Atoi(strings.TrimSpace(s.Text()))
		}
	})

	// Handle the null case
	if pageCount == 0 {
		pageCount = 1
	}

	return pageCount
}

func GetProviderExams(providerName string) []string {
	baseURL := fmt.Sprintf("https://www.examtopics.com/exams/%s/", providerName)
	doc, err := ParseHTML(baseURL, *siteClient)
	if err != nil {
		log.Fatalf("Failed to parse HTML for provider exams: %v", err)
	}

	var allExams []string
	doc.Find(".popular-exam-link").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if exists {
			allExams = append(allExams, utils.CleanText(href))
		}
	})

	return allExams
}

// Extracts matching links from a single page
func getLinksFromPage(url string, grepStr string) []string {
	doc, err := ParseHTML(url, *siteClient)
	if err != nil {
		log.Printf("Failed to parse HTML for %s: %v", url, err)
		return nil
	}

	var matchingLinks []string
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if exists && utils.GrepString(href, "/discussions") && utils.GrepString(href, grepStr) {
			matchingLinks = append(matchingLinks, href)
		}
	})

	return matchingLinks
}

func FetchCachedLinks(providerName string, grepStr string, token string) []string {
	parsedProviderName := utils.CapitalizeFirstLetter(strings.ToLower(providerName))
	baseURL := fmt.Sprintf("https://api.github.com/repos/thatonecodes/examtopics-data/contents/%s", parsedProviderName)
	// `client` is the GitHub-facing client only: examtopics.com traffic goes
	// through `siteClient` (see its doc comment), so this token never leaves
	// GitHub hosts.
	if token != "" {
		client = utils.NewGitHubClient(token)
	}
	resp := FetchURLProbe(baseURL, *client)

	var content []models.FileInfo

	if resp == nil {
		log.Printf("the response body was nil, %v", resp)
		return nil
	}

	err := json.Unmarshal(resp, &content)
	if err != nil {
		log.Fatalf("error unmarshaling response: %v", err)
	}

	var linksWithNumbers []models.FileInfo
	for _, item := range content {
		link := item.URL
		number := utils.ExtractNumberFromPath(item.Name)
		if utils.GrepStringFromCache(link, grepStr) {
			linksWithNumbers = append(linksWithNumbers, models.FileInfo{
				URL:    link,
				Name:   item.Name,
				Number: number,
			})
		}
	}

	return utils.SortCachedLinks(linksWithNumbers)
}

func GetCachedPages(providerName string, grepStr string, token string) []models.QuestionData {
	failuresBefore := FetchFailures()
	links := FetchCachedLinks(providerName, grepStr, token)
	var allData []models.QuestionData

	var wg sync.WaitGroup
	dataChan := make(chan models.QuestionData)

	for _, link := range links {
		wg.Add(1)
		go func(link string) {
			defer wg.Done()
			dataList := getJSONFromLink(link)
			if dataList == nil {
				return
			}
			for _, data := range dataList {
				dataChan <- *data // send each QuestionData into the channel
			}
		}(link)
	}

	go func() {
		wg.Wait()
		close(dataChan)
	}()

	for data := range dataChan {
		allData = append(allData, data)
	}

	reportFetchFailures("cache scrape", failuresBefore)
	return utils.SortQuestionDataByPageNumber(allData)
}
