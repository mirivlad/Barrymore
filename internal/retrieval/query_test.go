package retrieval_test

import (
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/retrieval"
)

func TestTermsDropNoiseAndKeepMeaningfulRussianWords(t *testing.T) {
	got := retrieval.Terms("Какая модель у тебя сейчас запущена? Модель!", 8)
	want := []string{"модель", "запущена"}
	if len(got) != len(want) {
		t.Fatalf("Terms() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Terms()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFTSUsesPrefixesAndNeverKeepsOperators(t *testing.T) {
	q := retrieval.FTS(`модель OR "DROP"; сервере`, 8)
	if strings.Contains(q, " DROP ") || strings.Contains(q, ";") {
		t.Fatalf("raw syntax leaked into FTS query: %q", q)
	}
	if !strings.Contains(q, `"модел"*`) {
		t.Fatalf("Russian prefix for модель missing: %q", q)
	}
	if !strings.Contains(q, `"сервер"*`) {
		t.Fatalf("Russian prefix for сервере missing: %q", q)
	}
}

func TestFTSEmptyQuestionProducesNoQuery(t *testing.T) {
	if q := retrieval.FTS("что это и как?", 8); q != "" {
		t.Fatalf("only stop words should not create query: %q", q)
	}
}
