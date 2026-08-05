package thread_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/thread"
)

func str(v string) *string { return &v }

// Поручение уточняет, где мы остановились, и не имеет права переписать цель,
// о которой договаривались люди.
func TestCanonPatchDoesNotOverwriteWhatItDoesNotMention(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newService(t, filepath.Join(t.TempDir(), "barrymore.db"))

	th, err := svc.Create(ctx, thread.CreateRequest{Title: "Rollboard", Kind: thread.KindProject})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetCanon(ctx, th.ID, thread.CanonPatch{
		Goal:      str("починить сборку в worktree"),
		Situation: str("сборка падает на этапе линковки"),
		Obstacles: &[]string{"непонятно, чей это worktree"},
	}, thread.CanonFromTalk, "из разговора", event.Actor{Type: event.ActorBarrymore}); err != nil {
		t.Fatal(err)
	}

	after, err := svc.SetCanon(ctx, th.ID, thread.CanonPatch{
		Situation: str("поручение «Аудит» выполнено: проверок пройдено 5 из 5"),
	}, thread.CanonFromOrder, "по итогам поручения", event.Actor{Type: event.ActorRuntime})
	if err != nil {
		t.Fatal(err)
	}

	if after.Canon.Goal != "починить сборку в worktree" {
		t.Fatalf("цель затёрта поручением: %q", after.Canon.Goal)
	}
	if len(after.Canon.Obstacles) != 1 {
		t.Fatalf("препятствия затёрты: %v", after.Canon.Obstacles)
	}
	if after.Canon.Source != thread.CanonFromOrder {
		t.Fatalf("источник %q: владелец должен видеть, кто что утверждает", after.Canon.Source)
	}
}

// Бэрримор ведёт нить сам, значит, иногда ошибается. Без отмены автоматическая
// правка становится необратимой.
func TestCanonUndoRestoresPreviousStatement(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newService(t, filepath.Join(t.TempDir(), "barrymore.db"))

	th, err := svc.Create(ctx, thread.CreateRequest{Title: "Rollboard", Kind: thread.KindProject})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetCanon(ctx, th.ID, thread.CanonPatch{
		Situation: str("ждём ответа от коллеги"),
	}, thread.CanonFromPerson, "слова владельца", event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetCanon(ctx, th.ID, thread.CanonPatch{
		Situation: str("ошибочная догадка"),
	}, thread.CanonFromTalk, "из разговора", event.Actor{Type: event.ActorBarrymore}); err != nil {
		t.Fatal(err)
	}

	back, err := svc.UndoCanon(ctx, th.ID, event.Actor{Type: event.ActorPerson})
	if err != nil {
		t.Fatal(err)
	}
	if back.Canon.Situation != "ждём ответа от коллеги" {
		t.Fatalf("после отмены осталось %q", back.Canon.Situation)
	}
	if back.Canon.Source != thread.CanonFromPerson {
		t.Fatalf("после отмены источник %q вместо прежнего", back.Canon.Source)
	}
}

// Каноническое состояние — такая же часть нити, как позиции и решения: оно
// обязано восстанавливаться из журнала целиком.
func TestCanonSurvivesProjectionRebuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "barrymore.db")
	clk := testsupport.Clock()
	db := testsupport.OpenDBAt(t, path)
	j := event.NewJournal(db, clk)
	svc := thread.NewService(db, j, clk)

	th, err := svc.Create(ctx, thread.CreateRequest{Title: "Rollboard", Kind: thread.KindProject})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetCanon(ctx, th.ID, thread.CanonPatch{
		Goal:      str("починить сборку"),
		NextStep:  str("запустить аудит каталога"),
		Waiting:   &[]string{"результата поручения"},
		Obstacles: &[]string{"нет доступа к каталогу"},
	}, thread.CanonFromTalk, "из разговора", event.Actor{Type: event.ActorBarrymore}); err != nil {
		t.Fatal(err)
	}
	before, err := svc.Get(ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}

	reg := projection.NewRegistry()
	svc.Projections(reg)
	if err := projection.Rebuild(ctx, db, j, reg); err != nil {
		t.Fatalf("пересборка проекций: %v", err)
	}

	after, err := svc.Get(ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Canon.Goal != before.Canon.Goal || after.Canon.NextStep != before.Canon.NextStep {
		t.Fatalf("состояние нити не пережило пересборку: %+v", after.Canon)
	}
	if len(after.Canon.Waiting) != 1 || len(after.Canon.Obstacles) != 1 {
		t.Fatalf("списки состояния потерялись: %+v", after.Canon)
	}
}

// Источник обязателен: запись после поручения и запись со слов владельца
// имеют разный вес.
func TestCanonWithoutSourceIsRefused(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newService(t, filepath.Join(t.TempDir(), "barrymore.db"))
	th, err := svc.Create(ctx, thread.CreateRequest{Title: "X", Kind: thread.KindIdea})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetCanon(ctx, th.ID, thread.CanonPatch{Goal: str("что-то")},
		"", "", event.Actor{Type: event.ActorBarrymore}); err == nil {
		t.Fatal("принято состояние нити без источника")
	}
}
