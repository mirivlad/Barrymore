package thread_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/thread"
)

func newService(t *testing.T, path string) (*thread.Service, *event.Journal, func()) {
	t.Helper()
	clk := testsupport.Clock()
	db := testsupport.OpenDBAt(t, path)
	j := event.NewJournal(db, clk)
	return thread.NewService(db, j, clk), j, func() { _ = db.Close() }
}

func TestThreadSurvivesRestartWithDivergentPositions(t *testing.T) {
	// Сценарий A: нить, история и две различающиеся позиции переживают рестарт,
	// и восстановление не требует транскрипта переписки.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "barrymore.db")

	svc, _, closeDB := newService(t, path)

	th, err := svc.Create(ctx, thread.CreateRequest{
		Title: "Аудит mirvmon", Kind: thread.KindProject,
		Origin: "владелец попросил разобраться с состоянием репозитория",
	})
	if err != nil {
		t.Fatalf("создание нити: %v", err)
	}

	if _, err := svc.SetPosition(ctx, th.ID, thread.PositionRequest{
		Owner: thread.OwnerPerson, Statement: "сначала аудит, править ничего не нужно",
		Confidence: 0.9,
	}); err != nil {
		t.Fatalf("позиция пользователя: %v", err)
	}
	if _, err := svc.SetPosition(ctx, th.ID, thread.PositionRequest{
		Owner: thread.OwnerBarrymore, Statement: "аудит стоит ограничить одним каталогом",
		Confidence: 0.6, Basis: "объём репозитория и стоимость запуска",
	}); err != nil {
		t.Fatalf("позиция Бэрримора: %v", err)
	}
	if _, err := svc.RecordDecision(ctx, th.ID, thread.DecisionRequest{
		Statement: "первое поручение выполняется без изменения репозитория",
		DecidedBy: thread.OwnerPerson, Rationale: "проверяем контур, а не правим код",
		Alternatives: []string{"сразу разрешить worktree_write"},
	}); err != nil {
		t.Fatalf("решение: %v", err)
	}
	if _, err := svc.OpenQuestion(ctx, th.ID,
		"нужен ли отдельный worktree, если запуск только на чтение?", thread.OwnerBarrymore,
		event.Actor{Type: event.ActorBarrymore}); err != nil {
		t.Fatalf("вопрос: %v", err)
	}
	closeDB()

	// Рестарт.
	svc2, _, _ := newService(t, path)
	got, err := svc2.Detail(ctx, th.ID)
	if err != nil {
		t.Fatalf("чтение нити после рестарта: %v", err)
	}

	if got.Thread.Title != "Аудит mirvmon" || got.Thread.Origin == "" {
		t.Fatalf("нить восстановлена неполно: %+v", got.Thread)
	}
	if len(got.Positions) != 2 {
		t.Fatalf("позиций после рестарта %d, ожидалось 2", len(got.Positions))
	}
	byOwner := map[string]thread.Position{}
	for _, p := range got.Positions {
		byOwner[p.Owner] = p
	}
	person, barrymore := byOwner[thread.OwnerPerson], byOwner[thread.OwnerBarrymore]
	if person.Statement == barrymore.Statement {
		t.Fatal("позиции сторон слились в одну; разногласие должно сохраняться")
	}
	if person.ValidUntil != nil || barrymore.ValidUntil != nil {
		t.Fatal("действующие позиции ошибочно закрыты")
	}
	if len(got.Decisions) != 1 || len(got.Questions) != 1 {
		t.Fatalf("решений %d, вопросов %d", len(got.Decisions), len(got.Questions))
	}
	if got.Questions[0].Status != thread.QuestionOpen {
		t.Fatalf("открытый вопрос превратился в %q", got.Questions[0].Status)
	}
}

func TestNewPositionSupersedesPreviousOfSameOwner(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newService(t, filepath.Join(t.TempDir(), "barrymore.db"))

	th, err := svc.Create(ctx, thread.CreateRequest{Title: "Смена мнения", Kind: thread.KindIdea})
	if err != nil {
		t.Fatalf("создание нити: %v", err)
	}
	first, err := svc.SetPosition(ctx, th.ID, thread.PositionRequest{
		Owner: thread.OwnerBarrymore, Statement: "лучше отложить", Confidence: 0.5,
	})
	if err != nil {
		t.Fatalf("первая позиция: %v", err)
	}
	second, err := svc.SetPosition(ctx, th.ID, thread.PositionRequest{
		Owner: thread.OwnerBarrymore, Statement: "стоит начать сейчас", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("вторая позиция: %v", err)
	}

	d, err := svc.Detail(ctx, th.ID)
	if err != nil {
		t.Fatalf("чтение нити: %v", err)
	}
	var old, current thread.Position
	for _, p := range d.Positions {
		switch p.ID {
		case first.ID:
			old = p
		case second.ID:
			current = p
		}
	}
	if old.ValidUntil == nil {
		t.Fatal("прежняя позиция не получила срока действия")
	}
	if old.SupersededBy != second.ID {
		t.Fatalf("прежняя позиция не ссылается на пришедшую на смену: %q", old.SupersededBy)
	}
	if current.ValidUntil != nil {
		t.Fatal("действующая позиция ошибочно закрыта")
	}
}

func TestConcurrentUpdateIsRejected(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newService(t, filepath.Join(t.TempDir(), "barrymore.db"))

	th, err := svc.Create(ctx, thread.CreateRequest{Title: "Нить", Kind: thread.KindIdea})
	if err != nil {
		t.Fatalf("создание нити: %v", err)
	}

	title := "Первое изменение"
	if _, err := svc.Update(ctx, th.ID, thread.UpdateRequest{
		Title: &title, ExpectedRevision: th.Revision,
	}); err != nil {
		t.Fatalf("первое изменение: %v", err)
	}

	// Второй писатель считает, что ревизия прежняя.
	stale := "Устаревшее изменение"
	_, err = svc.Update(ctx, th.ID, thread.UpdateRequest{
		Title: &stale, ExpectedRevision: th.Revision,
	})
	if !errors.Is(err, event.ErrConcurrency) {
		t.Fatalf("ожидался конфликт ревизий, получено: %v", err)
	}

	got, err := svc.Get(ctx, th.ID)
	if err != nil {
		t.Fatalf("чтение нити: %v", err)
	}
	if got.Title != "Первое изменение" {
		t.Fatalf("устаревшая запись всё-таки применилась: %q", got.Title)
	}
}

func TestThreadProjectionsRebuildFromJournal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "barrymore.db")
	clk := testsupport.Clock()
	db := testsupport.OpenDBAt(t, path)
	j := event.NewJournal(db, clk)
	svc := thread.NewService(db, j, clk)

	th, err := svc.Create(ctx, thread.CreateRequest{Title: "Нить", Kind: thread.KindProject})
	if err != nil {
		t.Fatalf("создание нити: %v", err)
	}
	other, err := svc.Create(ctx, thread.CreateRequest{Title: "Смежная", Kind: thread.KindResearch})
	if err != nil {
		t.Fatalf("создание смежной нити: %v", err)
	}
	if _, err := svc.SetPosition(ctx, th.ID, thread.PositionRequest{
		Owner: thread.OwnerPerson, Statement: "делаем", Confidence: 1,
	}); err != nil {
		t.Fatalf("позиция: %v", err)
	}
	if _, err := svc.Link(ctx, th.ID, other.ID, thread.LinkRelatedTo, "общая тема",
		event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatalf("связь: %v", err)
	}
	if _, err := svc.ChangeState(ctx, th.ID, thread.StateWaiting, "ждём результат аудита",
		event.AnyRevision, event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatalf("смена состояния: %v", err)
	}

	before, err := svc.Detail(ctx, th.ID)
	if err != nil {
		t.Fatalf("чтение до пересборки: %v", err)
	}

	reg := projection.NewRegistry()
	svc.Projections(reg)
	if err := projection.Rebuild(ctx, db, j, reg); err != nil {
		t.Fatalf("пересборка проекций: %v", err)
	}

	after, err := svc.Detail(ctx, th.ID)
	if err != nil {
		t.Fatalf("чтение после пересборки: %v", err)
	}
	if after.Thread.State != before.Thread.State {
		t.Fatalf("состояние после пересборки %q вместо %q", after.Thread.State, before.Thread.State)
	}
	if after.Thread.Revision != before.Thread.Revision {
		t.Fatalf("ревизия после пересборки %d вместо %d",
			after.Thread.Revision, before.Thread.Revision)
	}
	if len(after.Positions) != len(before.Positions) || len(after.Links) != len(before.Links) {
		t.Fatalf("состав нити изменился: позиций %d, связей %d",
			len(after.Positions), len(after.Links))
	}
}

func TestInvalidKindAndStateAreRejected(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newService(t, filepath.Join(t.TempDir(), "barrymore.db"))

	if _, err := svc.Create(ctx, thread.CreateRequest{Title: "X", Kind: "нечто"}); err == nil {
		t.Fatal("создана нить с недопустимым видом")
	}
	th, err := svc.Create(ctx, thread.CreateRequest{Title: "X", Kind: thread.KindIdea})
	if err != nil {
		t.Fatalf("создание нити: %v", err)
	}
	if _, err := svc.ChangeState(ctx, th.ID, "летит", "", event.AnyRevision,
		event.Actor{Type: event.ActorPerson}); err == nil {
		t.Fatal("принято недопустимое состояние нити")
	}
}
