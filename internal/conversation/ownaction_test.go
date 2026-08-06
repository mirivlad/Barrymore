package conversation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/skill"
)

// stubCatalog отдаёт заранее известный набор умений и окружения.
type stubCatalog struct {
	items   []skill.Skill
	ambient []skill.Fact
}

func (c stubCatalog) Live() []skill.Skill { return c.items }

func (c stubCatalog) Ambient(context.Context) []skill.Fact { return c.ambient }

func ownAction(id, target, why string) map[string]any {
	return map[string]any{"own_actions": []any{map[string]any{
		"skill_id": id, "target": target, "why": why,
	}}}
}

var catalog = stubCatalog{
	items: []skill.Skill{{
		ID: "git.worktree.diagnose", Title: "разобраться с рабочими копиями",
		Question: "что происходит с worktree", NeedsTarget: true,
		Origin: skill.OriginBuiltin, Enabled: true,
	}},
	ambient: []skill.Fact{{Text: "место на дисках: / 33.0 ГБ свободно из 218.8 ГБ (15%)"}},
}

// Умение, которое Бэрримор показал сам, применимо: разговор доводит его
// до владельца одним предложением, а не превращает в поручение.
func TestOwnActionFromOfferedSkillPasses(t *testing.T) {
	ctx := context.Background()
	h := newHarnessWithSkills(t, memory.DefaultPolicy(), catalog)
	h.prov.reply = reply("Сейчас посмотрю сам.",
		ownAction("git.worktree.diagnose", "/home/x/git/rollboard", "вопрос о worktree"))

	c := h.conversation(t, "")
	turn, err := h.talk.Send(ctx, c.ID, "Rollboard завис в worktree")
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.OwnActions) != 1 {
		t.Fatalf("собственное умение не предложено: %+v", turn.OwnActions)
	}
	a := turn.OwnActions[0]
	if a.Refused != "" {
		t.Fatalf("умение из показанного списка отвергнуто: %q", a.Refused)
	}
	if a.Title == "" || a.Question == "" {
		t.Fatal("умение показано без названия и вопроса: выбрать по нему нельзя")
	}
	if a.Target != "/home/x/git/rollboard" {
		t.Fatalf("каталог потерян: %q", a.Target)
	}
}

// Придуманное умение отвергается так же, как придуманная нить: граница
// полномочий совпадает с тем, что система показала сама.
func TestInventedSkillIsRefusedWithExplanation(t *testing.T) {
	ctx := context.Background()
	h := newHarnessWithSkills(t, memory.DefaultPolicy(), catalog)
	h.prov.reply = reply("Запущу диагностику.",
		ownAction("shell.run", "/home/x", "почему бы и нет"))

	c := h.conversation(t, "")
	turn, err := h.talk.Send(ctx, c.ID, "разберись")
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.OwnActions) != 1 {
		t.Fatalf("отказ не показан владельцу: %+v", turn.OwnActions)
	}
	if turn.OwnActions[0].Refused == "" {
		t.Fatal("выдуманное умение принято")
	}
	if !strings.Contains(turn.OwnActions[0].Refused, "shell.run") {
		t.Fatalf("отказ не называет, что именно отвергнуто: %q", turn.OwnActions[0].Refused)
	}
}

// Модель должна знать, что у неё есть руки. Без раздела с умениями она
// вынуждена предлагать поручение на всё подряд.
func TestSkillsAreShownToTheModel(t *testing.T) {
	ctx := context.Background()
	h := newHarnessWithSkills(t, memory.DefaultPolicy(), catalog)
	h.prov.reply = reply("Понял.", nil)

	c := h.conversation(t, "")
	if _, err := h.talk.Send(ctx, c.ID, "что там с worktree?"); err != nil {
		t.Fatal(err)
	}
	system := h.prov.lastReq.System
	if !strings.Contains(system, "Что ты умеешь сам") {
		t.Fatal("модели не сказано, что Бэрримор что-то умеет сам")
	}
	if !strings.Contains(system, "git.worktree.diagnose") {
		t.Fatal("умение не названо: выбрать из показанного нечего")
	}
	if !strings.Contains(system, "work_order_proposals` пустым") &&
		!strings.Contains(system, "оставляй `work_order_proposals`") {
		t.Fatal("не сказано, что своё умение отменяет поручение")
	}
}

// Пустой каталог не должен молчать: молчание модель читает как отсутствие
// раздела, а не как отсутствие умений.
func TestEmptyCatalogIsStatedOutLoud(t *testing.T) {
	ctx := context.Background()
	h := newHarnessWithSkills(t, memory.DefaultPolicy(), stubCatalog{})
	h.prov.reply = reply("Понял.", nil)

	c := h.conversation(t, "")
	if _, err := h.talk.Send(ctx, c.ID, "посмотри каталог"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.prov.lastReq.System, "Умений пока нет") {
		t.Fatal("пустота умений не названа вслух")
	}
}

// Одно и то же умение дважды в одном ходу — не два дела.
func TestDuplicateOwnActionsCollapse(t *testing.T) {
	ctx := context.Background()
	h := newHarnessWithSkills(t, memory.DefaultPolicy(), catalog)
	h.prov.reply = reply("Посмотрю.", map[string]any{"own_actions": []any{
		map[string]any{"skill_id": "git.worktree.diagnose", "target": "/x", "why": "раз"},
		map[string]any{"skill_id": "git.worktree.diagnose", "target": "/x", "why": "два"},
	}})

	c := h.conversation(t, "")
	turn, err := h.talk.Send(ctx, c.ID, "смотри")
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.OwnActions) != 1 {
		t.Fatalf("одно умение показано %d раз", len(turn.OwnActions))
	}
}

var _ conversation.SkillCatalog = stubCatalog{}

// Найдено живьём: на вопрос «сколько свободного места» модель ответила
// выдуманным числом — 18.4 ГБ из 238.5 — вместо того чтобы посмотреть.
// Владелец сверил с `df -h` и увидел совсем другое.
//
// Правило не проверить кодом: свободный текст ответа валидировать нельзя.
// Но убедиться, что оно вообще сказано модели, — можно и нужно.
func TestModelIsForbiddenToInventFactsAboutTheMachine(t *testing.T) {
	ctx := context.Background()
	h := newHarnessWithSkills(t, memory.DefaultPolicy(), catalog)
	h.prov.reply = reply("Понял.", nil)

	c := h.conversation(t, "")
	if _, err := h.talk.Send(ctx, c.ID, "сколько свободного места?"); err != nil {
		t.Fatal(err)
	}
	system := h.prov.lastReq.System
	for _, must := range []string{
		"Выдуманное число хуже",
		"сказанное без проверки",
		"не смотрел, не знаю",
	} {
		if !strings.Contains(system, must) {
			t.Fatalf("модели не сказано главное: нет фразы %q", must)
		}
	}
}

// Дешёвый факт о машине не требует умения: он уже в контексте.
//
// Это и есть ответ на «что, на каждый вопрос писать умение?». Класс таких
// фактов закрыт — всё, что не требует цели и стоит миллисекунды, — и потому
// закрыт список того, что здесь надо предусмотреть.
func TestAmbientFactsReachTheModelWithoutAnySkill(t *testing.T) {
	ctx := context.Background()
	h := newHarnessWithSkills(t, memory.DefaultPolicy(), catalog)
	h.prov.reply = reply("Понял.", nil)

	c := h.conversation(t, "")
	if _, err := h.talk.Send(ctx, c.ID, "сколько свободного места?"); err != nil {
		t.Fatal(err)
	}
	system := h.prov.lastReq.System
	if !strings.Contains(system, "Что ты видишь прямо сейчас") {
		t.Fatal("окружение не подано модели: отвечать ей будет нечем, кроме выдумки")
	}
	if !strings.Contains(system, "218.8 ГБ") {
		t.Fatal("наблюдение о диске не дошло до модели")
	}
	if !strings.Contains(system, "Текущее время") {
		t.Fatal("время не подано: дату модель тоже сочинит")
	}
}
