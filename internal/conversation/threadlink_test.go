package conversation_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/thread"
)

// reply собирает ответ модели по контракту.
func reply(text string, extra map[string]any) string {
	out := map[string]any{
		"reply":                text,
		"memory_candidates":    []any{},
		"work_order_proposals": []any{},
		"open_questions":       []any{},
	}
	for k, v := range extra {
		out[k] = v
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

func match(threadID, newTitle, why string) map[string]any {
	return map[string]any{"thread_match": map[string]any{
		"thread_id": threadID, "new_thread_title": newTitle,
		"new_thread_kind": "project", "why": why,
	}}
}

// Главное обещание среза: владелец пишет о деле, не выбирая нить.
func TestBarrymoreAttachesConversationToExistingThreadHimself(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{}
	h := newHarness(t, prov, memory.DefaultPolicy())

	th, err := h.threads.Create(ctx, thread.CreateRequest{
		Title: "Rollboard", Kind: thread.KindProject,
	})
	if err != nil {
		t.Fatal(err)
	}
	prov.reply = reply("Смотрю на Rollboard.",
		match(th.ID, "", "разговор о сборке Rollboard"))

	c := h.conversation(t, "")
	turn, err := h.talk.Send(ctx, c.ID, "Rollboard завис в worktree")
	if err != nil {
		t.Fatal(err)
	}

	if !turn.Thread.Attached || turn.Thread.ThreadID != th.ID {
		t.Fatalf("разговор не отнесён к нити: %+v", turn.Thread)
	}
	if turn.Thread.Why == "" {
		t.Fatal("владельцу не сказано, почему выбрана именно эта нить")
	}

	got, err := h.talk.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ThreadID != th.ID {
		t.Fatalf("связь не сохранилась: %q", got.ThreadID)
	}

	// Реплики, сказанные до связывания, принадлежат нити так же, как сказанные
	// после: половина разговора вне нити — потерянная половина.
	msgs, err := h.talk.Messages(ctx, c.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.ThreadID != th.ID {
			t.Fatalf("реплика %s осталась без нити", m.ID)
		}
	}
}

// Список нитей — граница полномочий, а не подсказка. Сослаться на нить,
// которой не предлагали, значит сослаться на догадку.
func TestInventedThreadIDIsRefusedAndExplained(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{reply: reply("Понял.",
		match("th_которой_нет", "", "мне кажется, это та нить"))}
	h := newHarness(t, prov, memory.DefaultPolicy())

	c := h.conversation(t, "")
	turn, err := h.talk.Send(ctx, c.ID, "продолжаем")
	if err != nil {
		t.Fatal(err)
	}
	if turn.Thread.Attached {
		t.Fatal("разговор привязан к несуществующей нити")
	}
	if turn.Thread.Refused == "" {
		t.Fatal("отказ не объяснён владельцу")
	}
	got, _ := h.talk.Get(ctx, c.ID)
	if got.ThreadID != "" {
		t.Fatalf("связь всё же появилась: %q", got.ThreadID)
	}
}

// Завести нить — не то же, что связать с существующей. Сущности, возникающие
// молча, засоряют систему быстрее, чем приносят пользу.
func TestNewThreadIsProposedNotCreated(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{reply: reply("Похоже, это новое дело.",
		merge(match("", "Переезд mirvmon", "речь о новом репозитории"),
			state("перенести mirvmon", "ещё ничего не начато", "выяснить состав", nil, nil)))}
	h := newHarness(t, prov, memory.DefaultPolicy())

	c := h.conversation(t, "")
	turn, err := h.talk.Send(ctx, c.ID, "надо разобраться с mirvmon")
	if err != nil {
		t.Fatal(err)
	}
	if turn.Thread.Proposed == nil {
		t.Fatalf("нить не предложена: %+v", turn.Thread)
	}
	if turn.Thread.Proposed.Title != "Переезд mirvmon" {
		t.Fatalf("название предложения %q", turn.Thread.Proposed.Title)
	}
	if turn.Thread.Proposed.State.Goal == "" {
		t.Fatal("предложение без состояния: владельцу пришлось бы заполнять его руками")
	}
	list, err := h.threads.List(ctx, thread.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("нитей создано %d, ожидалось 0 до решения владельца", len(list))
	}

	// Одно нажатие — и нить есть вместе с состоянием и связью.
	th, err := h.talk.StartThread(ctx, c.ID, *turn.Thread.Proposed)
	if err != nil {
		t.Fatal(err)
	}
	if th.Canon.Goal != "перенести mirvmon" {
		t.Fatalf("состояние не перенесено в нить: %+v", th.Canon)
	}
	got, _ := h.talk.Get(ctx, c.ID)
	if got.ThreadID != th.ID {
		t.Fatal("разговор не отнесён к заведённой нити")
	}
}

// Нить ведёт Бэрримор: состояние появляется само, без формы.
func TestThreadStateIsWrittenByBarrymore(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{}
	h := newHarness(t, prov, memory.DefaultPolicy())
	th := h.thread(t)

	prov.reply = reply("Записал, где мы.",
		state("починить сборку", "падает на линковке", "запустить аудит",
			[]string{"нет доступа к каталогу"}, []string{"ответа коллеги"}))

	c := h.conversation(t, th.ID)
	if _, err := h.talk.Send(ctx, c.ID, "сборка падает"); err != nil {
		t.Fatal(err)
	}

	got, err := h.threads.Get(ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Canon.Goal != "починить сборку" || got.Canon.NextStep != "запустить аудит" {
		t.Fatalf("состояние нити не записано: %+v", got.Canon)
	}
	if got.Canon.Source != thread.CanonFromTalk {
		t.Fatalf("источник состояния %q", got.Canon.Source)
	}
	if len(got.Canon.Obstacles) != 1 || len(got.Canon.Waiting) != 1 {
		t.Fatalf("препятствия и ожидания потеряны: %+v", got.Canon)
	}
}

// Уже отнесённый разговор не переезжает на другую нить сам: нить не должна
// уезжать из-под владельца посреди обсуждения.
func TestAttachedConversationIsNotReassigned(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{}
	h := newHarness(t, prov, memory.DefaultPolicy())
	first := h.thread(t)
	second, err := h.threads.Create(ctx, thread.CreateRequest{
		Title: "Другая", Kind: thread.KindIdea,
	})
	if err != nil {
		t.Fatal(err)
	}

	prov.reply = reply("Ответ.", match(second.ID, "", "вроде бы про это"))
	c := h.conversation(t, first.ID)
	if _, err := h.talk.Send(ctx, c.ID, "продолжаем"); err != nil {
		t.Fatal(err)
	}

	got, _ := h.talk.Get(ctx, c.ID)
	if got.ThreadID != first.ID {
		t.Fatalf("разговор переехал на нить %q", got.ThreadID)
	}
}

// Владелец должен уметь поправить автоматическую связь.
func TestOwnerCanDetachAndAttachByHand(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, &scriptedProvider{}, memory.DefaultPolicy())
	th := h.thread(t)
	c := h.conversation(t, th.ID)

	if err := h.talk.Detach(ctx, c.ID, "не про это", event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.talk.Get(ctx, c.ID); got.ThreadID != "" {
		t.Fatalf("связь не снята: %q", got.ThreadID)
	}
	if err := h.talk.Attach(ctx, c.ID, th.ID, "всё-таки про это", event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.talk.Get(ctx, c.ID); got.ThreadID != th.ID {
		t.Fatal("связь не восстановлена")
	}
}

// Поручение оформляется из того, что Бэрримор сказал, а не из того, что
// прислал браузер.
func TestProposalIsReadBackFromJournal(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{}
	h := newHarness(t, prov, memory.DefaultPolicy())
	th := h.thread(t)

	prov.reply = reply("Предлагаю аудит.", map[string]any{
		"work_order_proposals": []any{map[string]any{
			"title": "Аудит Rollboard", "goal": "понять, почему падает сборка",
			"why": "владелец не может продолжить работу", "workspace_hint": "/tmp/rollboard",
			"acceptance_criteria": []string{"названа причина падения"},
			"needs_write":         false,
		}},
	})

	c := h.conversation(t, th.ID)
	turn, err := h.talk.Send(ctx, c.ID, "почему падает сборка?")
	if err != nil {
		t.Fatal(err)
	}

	got, err := h.talk.ProposalFor(ctx, c.ID, turn.Reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WorkOrders) != 1 {
		t.Fatalf("предложений %d", len(got.WorkOrders))
	}
	wo := got.WorkOrders[0]
	if wo.Title == "" || wo.Goal == "" || wo.Why == "" || len(wo.AcceptanceCriteria) != 1 {
		t.Fatalf("предложение неполно, владельцу придётся допечатывать: %+v", wo)
	}
}

// Модель обязана видеть список нитей — иначе сопоставить ей нечего.
func TestOpenThreadsAreOfferedToModel(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{reply: reply("Ответ.", nil)}
	h := newHarness(t, prov, memory.DefaultPolicy())
	th := h.thread(t)

	c := h.conversation(t, "")
	if _, err := h.talk.Send(ctx, c.ID, "привет"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prov.lastReq.System, th.ID) {
		t.Fatal("список нитей не показан модели: сопоставлять ей не с чем")
	}
}

func merge(maps ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func state(goal, situation, next string, obstacles, waiting []string) map[string]any {
	if obstacles == nil {
		obstacles = []string{}
	}
	if waiting == nil {
		waiting = []string{}
	}
	return map[string]any{"thread_state": map[string]any{
		"goal": goal, "situation": situation, "next_step": next,
		"obstacles": obstacles, "waiting": waiting,
	}}
}

var _ = conversation.StateProposal{}

// Найдено живьём: на чистой базе, где нитей нет вовсе, модель выдумала
// идентификатор вместо того, чтобы предложить новую нить. Раздел контекста
// просто отсутствовал, и «нитей нет» читалось как «раздела нет».
func TestEmptyThreadListIsStatedOutLoud(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{reply: reply("Ответ.", nil)}
	h := newHarness(t, prov, memory.DefaultPolicy())

	c := h.conversation(t, "")
	if _, err := h.talk.Send(ctx, c.ID, "привет"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prov.lastReq.System, "Ни одной нити пока нет") {
		t.Fatal("модели не сказано, что нитей нет: пустоту она достроит сама")
	}
}
