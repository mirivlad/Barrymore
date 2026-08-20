package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/experience"
)

func TestEpisodeFeedbackIsExplicitIdempotentAndRevisable(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	exp := s.app.Talk.Experience()
	if exp == nil {
		t.Fatal("experience service не подключён к разговору")
	}

	ep, err := exp.Begin(ctx, experience.StartRequest{Goal: "проверить ответ"},
		event.Actor{Type: event.ActorBarrymore})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exp.Complete(ctx, ep.ID, experience.CompleteRequest{
		Outcome: experience.OutcomeSuccess, Result: "готово",
	}, event.Actor{Type: event.ActorBarrymore}); err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/episodes/" + ep.ID + "/feedback"
	created := s.mustDo(http.MethodPost, path, map[string]any{
		"value": experience.FeedbackLike,
	}, http.StatusCreated)
	if created["unchanged"] != false {
		t.Fatalf("первая оценка ошибочно признана повтором: %v", created)
	}

	got := s.mustDo(http.MethodGet, path, nil, http.StatusOK)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("оценок %d, ожидалась одна: %v", len(items), got)
	}
	current, _ := got["current"].(map[string]any)
	if current["value"] != experience.FeedbackLike {
		t.Fatalf("текущая оценка не like: %v", current)
	}

	// Двойной тап не должен становиться двумя независимыми обучающими сигналами.
	repeated := s.mustDo(http.MethodPost, path, map[string]any{
		"value": experience.FeedbackLike,
	}, http.StatusOK)
	if repeated["unchanged"] != true {
		t.Fatalf("повтор одинаковой оценки не идемпотентен: %v", repeated)
	}
	got = s.mustDo(http.MethodGet, path, nil, http.StatusOK)
	items, _ = got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("повтор создал лишний feedback event: %v", got)
	}

	// Владелец имеет право изменить мнение. Старый сигнал остаётся в истории,
	// но последняя явная оценка становится текущей.
	s.mustDo(http.MethodPost, path, map[string]any{
		"value": experience.FeedbackDislike,
		"note":  "ответ технически сработал, но способ плохой",
	}, http.StatusCreated)
	got = s.mustDo(http.MethodGet, path, nil, http.StatusOK)
	items, _ = got["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("смена оценки не сохранила историю: %v", got)
	}
	current, _ = got["current"].(map[string]any)
	if current["value"] != experience.FeedbackDislike {
		t.Fatalf("текущая оценка не dislike: %v", current)
	}
}

func TestFeedbackRejectsOpenEpisodeAndUnknownValue(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	exp := s.app.Talk.Experience()
	ep, err := exp.Begin(ctx, experience.StartRequest{Goal: "ещё не закончили"},
		event.Actor{Type: event.ActorBarrymore})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/episodes/" + ep.ID + "/feedback"

	if code, _ := s.do(http.MethodPost, path, map[string]any{"value": "like"}); code != http.StatusConflict {
		t.Fatalf("открытый episode принял feedback: HTTP %d", code)
	}

	if _, err := exp.Complete(ctx, ep.ID, experience.CompleteRequest{
		Outcome: experience.OutcomeSuccess, Result: "теперь закончили",
	}, event.Actor{Type: event.ActorBarrymore}); err != nil {
		t.Fatal(err)
	}
	if code, _ := s.do(http.MethodPost, path, map[string]any{"value": "meh"}); code != http.StatusConflict {
		t.Fatalf("неизвестная оценка принята: HTTP %d", code)
	}
}
