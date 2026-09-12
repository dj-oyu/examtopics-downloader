package utils

import (
	"examtopics-downloader/internal/constants"
	"examtopics-downloader/internal/models"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func CleanText(raw string) string {
	// Remove excessive whitespace (newlines, tabs, etc.)
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, "\n", " ")
	raw = strings.ReplaceAll(raw, "\t", " ")

	re := regexp.MustCompile(`\s+`)
	cleaned := re.ReplaceAllString(raw, " ")
	cleaned = strings.TrimSpace(cleaned)

	// Add newline before "Suggested Answer"
	cleaned = strings.Replace(cleaned, "Suggested Answer", "\nSuggested Answer", 1)
	cleaned = strings.ReplaceAll(cleaned, "Forgot my password", "")

	return cleaned
}

type AutoCloseFile struct {
	*os.File
}

func (f *AutoCloseFile) Close() {
	if f.File != nil {
		f.File.Close()
		f.File = nil
	}
}

func CreateFile(filename string) *AutoCloseFile {
	file, err := os.Create(filename)
	if err != nil {
		panic(err)
	}

	// Set up finalizer to ensure file is closed if Close() isn't called
	runtime.SetFinalizer(&AutoCloseFile{file}, (*AutoCloseFile).Close)

	return &AutoCloseFile{file}
}

func DeduplicateLinks(links []string) []string {
	seen := make(map[string]struct{})
	var unique []string
	for _, link := range links {
		if _, exists := seen[link]; !exists {
			seen[link] = struct{}{}
			unique = append(unique, link)
		}
	}
	return unique
}

// ExtractQuestionNum pulls the integer following "question-" in a discussion
// URL fragment. Returns 0 when the segment is absent or unparseable.
func ExtractQuestionNum(url string) int {
	parts := strings.Split(url, "question-")
	if len(parts) < 2 {
		return 0
	}
	numStr := strings.TrimSuffix(parts[1], "/")
	numStr = strings.TrimSuffix(numStr, "-discussion")
	num, _ := strconv.Atoi(numStr)
	return num
}

// ExtractTopicNum pulls the integer following "topic-" in a discussion URL
// fragment. Returns 0 when the segment is absent or unparseable.
func ExtractTopicNum(url string) int {
	parts := strings.Split(url, "topic-")
	if len(parts) < 2 {
		return 0
	}
	subParts := strings.Split(parts[1], "-")
	if len(subParts) < 1 {
		return 0
	}
	num, _ := strconv.Atoi(subParts[0])
	return num
}

// DeriveExamDisplay turns a cache filename like
// "AWS-Certified-Developer---Associate-DVA-C02_5.json?ref=main" into a
// human-readable exam name "AWS Certified Developer Associate DVA C02"
// consistent with what md_to_sqlite.py stores in questions.exam.
func DeriveExamDisplay(filename string) string {
	if i := strings.IndexByte(filename, '?'); i >= 0 {
		filename = filename[:i]
	}
	reSuffix := regexp.MustCompile(`(_\d+)?\.json$`)
	filename = reSuffix.ReplaceAllString(filename, "")
	filename = strings.ReplaceAll(filename, "-", " ")
	return strings.Join(strings.Fields(filename), " ")
}

func SortLinksByQuestionNumber(links []string) []string {
	sort.Slice(links, func(i, j int) bool {
		topicI := ExtractTopicNum(links[i])
		topicJ := ExtractTopicNum(links[j])
		if topicI != topicJ {
			return topicI < topicJ
		}
		return ExtractQuestionNum(links[i]) < ExtractQuestionNum(links[j])
	})
	return links
}

func normalize(s string) string {
	s = strings.ToLower(s)

	// Replace multiple dashes with a single dash
	reDash := regexp.MustCompile(`-+`)
	s = reDash.ReplaceAllString(s, "-")

	// Remove suffix like _xxx.json (if any)
	reSuffix := regexp.MustCompile(`(_\d+)?\.json$`)
	s = reSuffix.ReplaceAllString(s, "")

	return s
}

func GrepString(baseString, searchString string) bool {
	return strings.Contains(
		strings.ToLower(baseString),
		strings.ToLower(searchString),
	)
}

func GrepStringFromCache(baseString, searchString string) bool {
	baseNorm := normalize(baseString)
	searchNorm := normalize(searchString)

	return strings.Contains(
		baseNorm,
		searchNorm,
	)
}

func AddToBaseUrl(addString string) string {
	return fmt.Sprintf("https://www.examtopics.com%s", addString)
}

func CreateRateLimiter(rps float64) *time.Ticker {
	interval := time.Duration(float64(time.Second) / rps)
	return time.NewTicker(interval)
}

func DelayTime(backoff time.Duration) time.Duration {
	return backoff + time.Duration(rand.Intn(500))*time.Millisecond
}

// RetryableStatus reports whether an HTTP status is worth retrying: request
// throttling (429) and server-side failures (5xx).
//
// 403 is deliberately NOT retryable. examtopics.com answers 429 when it
// throttles, while the GitHub contents API answers 403 once the unauthenticated
// 60 requests/hour budget is gone — retrying within the same second cannot help
// there, and the caller must surface the loss instead of pretending success.
func RetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// RetryAfterDelay reads a response's Retry-After header, accepting either
// delta-seconds or an HTTP-date, and caps the result at
// constants.RetryAfterCap. Returns 0 when the header is absent or unusable.
func RetryAfterDelay(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}

	var d time.Duration
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs <= 0 {
			return 0
		}
		d = time.Duration(secs) * time.Second
	} else if when, err := http.ParseTime(raw); err == nil {
		d = time.Until(when)
	} else {
		return 0
	}

	if d <= 0 {
		return 0
	}
	if d > constants.RetryAfterCap {
		return constants.RetryAfterCap
	}
	return d
}

func BackoffTime(backoff time.Duration, backoffFactor float64) time.Duration {
	return time.Duration(float64(backoff) * backoffFactor)
}

func Sleep(seconds time.Duration) {
	time.Sleep(seconds)
}

func SortCachedLinks(linksWithNumbers []models.FileInfo) []string {
	sort.Slice(linksWithNumbers, func(i, j int) bool {
		return linksWithNumbers[i].Number < linksWithNumbers[j].Number
	})

	// Collect sorted links
	var sortedLinks []string
	for _, linkWithNumber := range linksWithNumbers {
		sortedLinks = append(sortedLinks, linkWithNumber.URL)
	}
	return sortedLinks
}

func ExtractNumberFromPath(filename string) int {
	num := -1 // Default if no number found
	parts := strings.Split(filename, "_")
	if len(parts) > 1 {
		numStr := strings.Split(parts[1], ".")[0]
		parsedNum, err := strconv.Atoi(numStr)
		if err == nil {
			num = parsedNum
		}
	}
	return num
}

func FilterOutNilData(results []*models.QuestionData) []models.QuestionData {
	var finalData []models.QuestionData
	for _, entry := range results {
		if entry != nil {
			finalData = append(finalData, *entry)
		}
	}
	return finalData
}

func CapitalizeFirstLetter(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(string(s[0])) + s[1:]
}

// NewGitHubClient creates an authenticated HTTP client with optimized transport
func NewGitHubClient(token string) *http.Client {
	transport := models.OptimizedTransport()

	return &http.Client{
		Timeout: constants.HttpTimeout,
		Transport: &models.AuthTransport{
			Token:     token,
			Transport: transport,
		},
	}
}

// NewHTTPClient creates an optimized HTTP client
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   constants.HttpTimeout,
		Transport: models.OptimizedTransport(),
	}
}

func GetNameFromLink(link string) string {
	name := strings.TrimSuffix(path.Base(link), ".json")
	name = strings.ReplaceAll(name, "-", " ")
	return strings.Join(strings.Fields(name), " ")
}

// SortQuestionDataByPageNumber orders cache-path results by the exam's own
// question number (Extras.QuestionID) when the cache supplied one, falling back
// to the page-shard number parsed out of the Title.
//
// The previous implementation only did the latter, but the current cache Title
// format ("Examtopics <name>_<shard> question #N") never matches
// ExtractNumberFromPath's "part after the first underscore is a number"
// assumption, so every element compared equal (-1) and the output order was
// whatever the goroutines happened to produce. The sort is stable so equal keys
// keep their input order.
func SortQuestionDataByPageNumber(data []models.QuestionData) []models.QuestionData {
	sortedData := make([]models.QuestionData, len(data))
	copy(sortedData, data)

	sort.SliceStable(sortedData, func(i, j int) bool {
		return questionSortKey(sortedData[i]) < questionSortKey(sortedData[j])
	})

	return sortedData
}

// questionSortKey prefers the cache JSON's question number, then the legacy
// page-shard number embedded in the Title.
func questionSortKey(q models.QuestionData) int {
	if q.Extras != nil && q.Extras.QuestionID > 0 {
		return q.Extras.QuestionID
	}
	return ExtractNumberFromPath(q.Title)
}

func StartTime() time.Time {
	return time.Now()
}

func TimeSince(startTime time.Time) string {
	duration := time.Since(startTime)

	hours := int(duration.Hours())
	minutes := int(duration.Minutes()) % 60
	seconds := int(duration.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh%dm%ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}
