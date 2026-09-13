package word

import "testing"

// The vocabularies this serves, spelled out: every tax kind and every
// contract line kind that reaches a sentence.
func TestArticleOverTheVocabulariesItServes(t *testing.T) {
	for word, want := range map[string]string{
		"standard":       "a",
		"zero-rated":     "a",
		"zero_rated":     "a",
		"reverse charge": "a",
		"exempt":         "an",
		"out of state":   "an",
		"out_of_state":   "an",
		"allowance":      "an",
		"tier":           "a",
		"commitment":     "a",
		"":               "a",
		"  Exempt  ":     "an",
	} {
		if got := Article(word); got != want {
			t.Errorf("Article(%q) = %q, want %q", word, got, want)
		}
	}
}
