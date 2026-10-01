// Package insights implements transparent rules, not machine learning.
package insights

import (
	"strings"
	"unicode"
)

type Sentiment struct {
	Label                     string
	Positive, Negative, Score int
}

var positive = wordSet("great love excellent amazing helpful good thanks awesome")
var negative = wordSet("bad hate terrible awful horrible wrong broken useless")

func wordSet(words string) map[string]bool {
	result := make(map[string]bool)
	for _, word := range strings.Fields(words) {
		result[word] = true
	}
	return result
}

// Words splits punctuation and preserves Unicode letters/numbers. Whole words
// avoid false matches such as “bad” in “badge”; every occurrence counts.
func Words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// Analyze uses POST CONTENT ONLY, as required by the subject. Negation and
// sarcasm are intentionally not inferred: “not good” contains one positive word.
func Analyze(content string) Sentiment {
	result := Sentiment{Label: "Neutral"}
	for _, word := range Words(content) {
		if positive[word] {
			result.Positive++
		}
		if negative[word] {
			result.Negative++
		}
	}
	result.Score = result.Positive - result.Negative
	if result.Score > 0 {
		result.Label = "Positive"
	} else if result.Score < 0 {
		result.Label = "Negative"
	}
	return result
}
