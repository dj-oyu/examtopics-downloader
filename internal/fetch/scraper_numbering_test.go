package fetch

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"examtopics-downloader/internal/models"
)

// The cache JSON carries the exam's own sequential question number
// (`question_id`). ConvertCachedJSON used to number questions from a
// package-level counter that every caller goroutine incremented, which made the
// numbering order-dependent (and tripped the race detector).
func TestConvertCachedJSON_QuestionNumbersComeFromCacheJSON(t *testing.T) {
	resp := decodeCacheJSON(t, `{"pageProps":{"questions":[
		{"id":"a","question_id":7,"exam_id":24,"question_text":"q7","answer":"A","choices":{"A":"x"},"url":"https://www.examtopics.com/discussions/amazon/view/1-e-topic-1-question/"},
		{"id":"b","question_id":8,"exam_id":24,"question_text":"q8","answer":"B","choices":{"A":"x"},"url":"https://www.examtopics.com/discussions/amazon/view/2-e-topic-1-question/"},
		{"id":"c","question_id":9,"exam_id":24,"question_text":"q9","answer":"C","choices":{"A":"x"},"url":"https://www.examtopics.com/discussions/amazon/view/3-e-topic-1-question/"}
	]}}`)

	got := ConvertCachedJSON(resp, "Amazon-AIF-C01_2.json?ref=main")
	want := []string{
		"Examtopics Amazon-AIF-C01_2 question #7",
		"Examtopics Amazon-AIF-C01_2 question #8",
		"Examtopics Amazon-AIF-C01_2 question #9",
	}
	if len(got) != len(want) {
		t.Fatalf("converted %d questions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Title != want[i] {
			t.Errorf("Title[%d] = %q, want %q", i, got[i].Title, want[i])
		}
		if got[i].Extras == nil || got[i].Extras.QuestionID != 7+i {
			t.Errorf("Extras.QuestionID[%d] = %+v, want %d", i, got[i].Extras, 7+i)
		}
	}
}

// Concurrent conversions (the real scrape path fans out per cache file) must
// each produce their own deterministic numbering. Fails the race detector on
// the old shared-counter implementation.
func TestConvertCachedJSON_ConcurrentNumberingIsStable(t *testing.T) {
	resp := decodeCacheJSON(t, `{"pageProps":{"questions":[
		{"id":"a","question_id":1,"question_text":"q1","answer":"A","choices":{"A":"x"},"url":"https://www.examtopics.com/discussions/amazon/view/1-e-topic-1-question/"},
		{"id":"b","question_id":2,"question_text":"q2","answer":"B","choices":{"A":"x"},"url":"https://www.examtopics.com/discussions/amazon/view/2-e-topic-1-question/"},
		{"id":"c","question_id":3,"question_text":"q3","answer":"C","choices":{"A":"x"},"url":"https://www.examtopics.com/discussions/amazon/view/3-e-topic-1-question/"}
	]}}`)

	const workers = 64
	results := make(chan []string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var titles []string
			for _, qd := range ConvertCachedJSON(resp, "Amazon-AIF-C01_1.json?ref=main") {
				titles = append(titles, qd.Title)
			}
			results <- titles
		}()
	}
	wg.Wait()
	close(results)

	want := []string{
		"Examtopics Amazon-AIF-C01_1 question #1",
		"Examtopics Amazon-AIF-C01_1 question #2",
		"Examtopics Amazon-AIF-C01_1 question #3",
	}
	seen := 0
	for titles := range results {
		if len(titles) != len(want) {
			t.Fatalf("conversion produced %d titles, want %d", len(titles), len(want))
		}
		for i := range want {
			if titles[i] != want[i] {
				t.Fatalf("worker numbering drifted: got %q at %d, want %q", titles[i], i, want[i])
			}
		}
		seen++
	}
	if seen != workers {
		t.Fatalf("checked %d workers, want %d", seen, workers)
	}
}

// Without question_id in the cache JSON the numbering falls back to the
// position inside the file — deterministic, and never colliding with a
// different file's range because callers key on the URL.
func TestConvertCachedJSON_FallsBackToOneBasedPosition(t *testing.T) {
	resp := decodeCacheJSON(t, `{"pageProps":{"questions":[
		{"id":"a","question_text":"q1","answer":"A","choices":{"A":"x"},"url":"https://www.examtopics.com/discussions/amazon/view/1-e-topic-1-question/"},
		{"id":"b","question_text":"q2","answer":"B","choices":{"A":"x"},"url":"https://www.examtopics.com/discussions/amazon/view/2-e-topic-1-question/"}
	]}}`)

	got := ConvertCachedJSON(resp, "Amazon-AIF-C01_12.json?ref=main")
	if !strings.HasSuffix(got[0].Title, "question #1") || !strings.HasSuffix(got[1].Title, "question #2") {
		t.Errorf("titles = %q, %q; want #1 and #2", got[0].Title, got[1].Title)
	}
}

// decodeCacheJSON unmarshals a cache-shaped payload through the real struct, so
// the json tags (notably "question_id") are exercised too.
func decodeCacheJSON(t *testing.T, raw string) models.JSONResponse {
	t.Helper()
	var resp models.JSONResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal cache json: %v", err)
	}
	if len(resp.PageProps.Questions) == 0 {
		t.Fatal("fixture decoded to zero questions — json tags drifted?")
	}
	return resp
}
