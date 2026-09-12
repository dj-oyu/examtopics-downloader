package models

type QuestionData struct {
	Title        string   `json:"title"`
	Header       string   `json:"header"`
	Content      string   `json:"content"`
	Questions    []string `json:"questions"`
	Answer       string   `json:"answer"`
	Timestamp    string   `json:"timestamp"`
	QuestionLink string   `json:"question_link"`
	Comments     string   `json:"comments"`

	// SuggestedAnswer carries the FULL answer string ("BD", "AE", ...) when
	// available. The legacy Answer field above is kept untouched for MD-output
	// back-compat; new SQLite-direct writes should consume SuggestedAnswer
	// instead. When unset (zero value), fall back to Answer.
	//
	// json tag: keeps `-type json` output snake_case-consistent with the
	// upstream fields above instead of leaking the Go field name.
	SuggestedAnswer string `json:"suggested_answer"`

	// Extras holds the structured cache-only fields (per-poster discussion,
	// images, etc.). Nil on the manual scrape path; populated on the cache
	// path. Consumers that don't need them can ignore the pointer.
	Extras *QuestionExtras `json:"extras,omitempty"`
}

// QuestionExtras captures fields that are present in the cache JSON but lost
// when squeezed into a Markdown round-trip. Only the SQLite-direct write path
// reads these.
type QuestionExtras struct {
	ExamID            int               `json:"exam_id"`
	IsMC              bool              `json:"is_mc"`
	QuestionID        int               `json:"question_id"`
	AnswerDescription string            `json:"answer_description"`
	QuestionImages    []string          `json:"question_images"`
	AnswerImages      []string          `json:"answer_images"`
	Discussion        []DiscussionEntry `json:"discussion"`
}

// DiscussionEntry is one comment from the cache JSON's `discussion` array.
// UpvoteCount is intentionally string-typed because the JSON ships it as a
// string ("3", "" for none); cast at write time.
type DiscussionEntry struct {
	Poster      string `json:"poster"`
	Content     string `json:"content"`
	UpvoteCount string `json:"upvote_count"`
	Timestamp   string `json:"timestamp"`
}

type FileInfo struct {
	URL    string
	Name   string
	Number int
}

type JSONResponse struct {
	PageProps struct {
		Questions []struct {
			Choices           map[string]string `json:"choices"`
			ID                string            `json:"id"`
			ExamID            int               `json:"exam_id"`
			QuestionID        int               `json:"question_id"`
			QuestionText      string            `json:"question_text"`
			Answer            string            `json:"answer"`
			AnswerET          string            `json:"answer_ET"`
			Topic             string            `json:"topic"`
			IsMC              bool              `json:"isMC"`
			AnswerDescription string            `json:"answer_description"`
			Discussion        []struct {
				Content     string `json:"content"`
				UpvoteCount string `json:"upvote_count"`
				Poster      string `json:"poster"`
				Timestamp   string `json:"timestamp"`
			} `json:"discussion"`
			AnswerImages   []string `json:"answer_images"`
			QuestionImages []string `json:"question_images"`
			URL            string   `json:"url"`
			Timestamp      string   `json:"timestamp"`
		} `json:"questions"`
	} `json:"pageProps"`
}
