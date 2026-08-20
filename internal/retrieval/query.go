// Package retrieval contains deterministic retrieval helpers shared by memory
// and durable experience. It deliberately does not call an LLM: finding a
// small candidate set is cheap infrastructure; deliberation decides what the
// candidates mean afterwards.
package retrieval

import (
	"strings"
	"unicode"
)

var stopWords = map[string]struct{}{
	"а": {}, "без": {}, "бы": {}, "был": {}, "была": {}, "были": {}, "было": {},
	"в": {}, "вам": {}, "вас": {}, "вы": {}, "где": {}, "да": {}, "для": {},
	"до": {}, "его": {}, "ее": {}, "её": {}, "если": {}, "есть": {}, "же": {},
	"за": {}, "и": {}, "из": {}, "или": {}, "как": {}, "какая": {}, "какие": {},
	"какой": {}, "когда": {}, "ли": {}, "мне": {}, "мой": {}, "моя": {}, "мы": {},
	"на": {}, "над": {}, "не": {}, "нет": {}, "но": {}, "о": {}, "об": {},
	"он": {}, "она": {}, "они": {}, "от": {}, "по": {}, "под": {}, "при": {},
	"про": {}, "с": {}, "сейчас": {}, "так": {}, "там": {}, "тебе": {}, "тебя": {},
	"то": {}, "ты": {}, "у": {}, "уже": {}, "что": {}, "это": {}, "я": {},
}

// Terms returns useful unique tokens in their original order. Punctuation and
// FTS operators never survive this step, so caller text cannot become FTS
// syntax accidentally.
func Terms(text string, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	var out []string
	seen := map[string]bool{}
	var word []rune
	flush := func() {
		if len(word) == 0 {
			return
		}
		v := strings.ToLower(string(word))
		word = word[:0]
		if _, stop := stopWords[v]; stop {
			return
		}
		if len([]rune(v)) < 3 || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			word = append(word, r)
			continue
		}
		flush()
		if len(out) >= limit {
			return out[:limit]
		}
	}
	flush()
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FTS converts natural text into a conservative FTS5 OR query. Longer words
// use a short prefix so Russian inflection (модель/модели, сервер/сервере)
// does not make a previous episode invisible. The candidate set may be broad;
// it is intentionally not the final truth decision.
func FTS(text string, limit int) string {
	terms := Terms(text, limit)
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		r := []rune(term)
		prefix := r
		switch {
		case len(r) >= 7:
			prefix = r[:len(r)-2]
		case len(r) >= 5:
			prefix = r[:len(r)-1]
		}
		parts = append(parts, `"`+strings.ReplaceAll(string(prefix), `"`, `""`)+`"*`)
	}
	return strings.Join(parts, " OR ")
}
