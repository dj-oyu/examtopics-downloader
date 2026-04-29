package sqlite

import "testing"

func TestQuestionRecord_ContentHash_Stable(t *testing.T) {
	r := QuestionRecord{
		QuestionText:    "Pick the best service.",
		SuggestedAnswer: "BD",
		Choices: map[string]string{
			"A": "first",
			"B": "second",
			"C": "third",
			"D": "fourth",
		},
	}
	h1 := r.ContentHash()
	h2 := r.ContentHash()
	if h1 == "" {
		t.Fatalf("hash empty")
	}
	if h1 != h2 {
		t.Fatalf("hash not stable across calls: %q vs %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char sha256 hex, got %d-char %q", len(h1), h1)
	}
}

func TestQuestionRecord_ContentHash_OrderInsensitiveOnChoices(t *testing.T) {
	a := QuestionRecord{
		QuestionText:    "Q?",
		SuggestedAnswer: "A",
		Choices:         map[string]string{"A": "x", "B": "y", "C": "z"},
	}
	b := QuestionRecord{
		QuestionText:    "Q?",
		SuggestedAnswer: "A",
		Choices:         map[string]string{"C": "z", "B": "y", "A": "x"},
	}
	if a.ContentHash() != b.ContentHash() {
		t.Errorf("hash should be order-insensitive across map iteration: %q vs %q",
			a.ContentHash(), b.ContentHash())
	}
}

func TestQuestionRecord_ContentHash_ChangesOnFieldEdit(t *testing.T) {
	base := QuestionRecord{
		QuestionText:    "Q?",
		SuggestedAnswer: "A",
		Choices:         map[string]string{"A": "x", "B": "y"},
	}
	cases := []struct {
		name string
		mut  func(*QuestionRecord)
	}{
		{"question_text", func(r *QuestionRecord) { r.QuestionText = "Different?" }},
		{"suggested_answer", func(r *QuestionRecord) { r.SuggestedAnswer = "B" }},
		{"choice text", func(r *QuestionRecord) { r.Choices = map[string]string{"A": "X!", "B": "y"} }},
		{"choice label set", func(r *QuestionRecord) { r.Choices = map[string]string{"A": "x", "B": "y", "C": "z"} }},
	}
	baseHash := base.ContentHash()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			r.Choices = map[string]string{}
			for k, v := range base.Choices {
				r.Choices[k] = v
			}
			tc.mut(&r)
			if r.ContentHash() == baseHash {
				t.Errorf("hash unchanged after mutating %s", tc.name)
			}
		})
	}
}
