package skill_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/skill"
	"github.com/mirivlad/barrymore/internal/store"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

// openPolicy разрешает всё: границы каталогов проверяются отдельным тестом,
// а остальным незачем тащить за собой настройки.
type openPolicy struct{ deny string }

func (p openPolicy) AllowWorkspace(path string) error {
	if p.deny != "" && strings.HasPrefix(path, p.deny) {
		return errDenied
	}
	return nil
}

var errDenied = &deniedError{}

type deniedError struct{}

func (*deniedError) Error() string {
	return "каталог не разрешён владельцем"
}

type harness struct {
	svc    *skill.Service
	dir    string
	db     *store.DB
	clk    *clock.Fake
	policy skill.WorkspacePolicy
}

func newHarness(t *testing.T, policy skill.WorkspacePolicy) *harness {
	t.Helper()
	clk := testsupport.Clock()
	db := testsupport.OpenDBAt(t, filepath.Join(t.TempDir(), "barrymore.db"))
	h := &harness{dir: t.TempDir(), db: db, clk: clk, policy: policy}
	h.svc = h.service(t)
	return h
}

// service поднимает службу на той же базе — так проверяется перезапуск.
func (h *harness) service(t *testing.T) *skill.Service {
	t.Helper()
	svc := skill.New(skill.Config{
		DB: h.db, Journal: event.NewJournal(h.db, h.clk), Clock: h.clk,
		Policy: h.policy, Logger: testsupport.Logger(t),
	})
	reg := projection.NewRegistry()
	svc.Projections(reg)
	return svc
}

// repo создаёт настоящий git-репозиторий: подделывать вывод git означало бы
// проверять собственную выдумку, а не умение.
func repo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("тест\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-qm", "первый")
}

// --- граница полномочий ---

// Умение — не обход политики каталогов. Иначе запрет обходился бы сменой
// способа: то, что нельзя отдать исполнителю, Бэрримор прочитал бы сам.
func TestSkillObeysWorkspacePolicy(t *testing.T) {
	h := newHarness(t, openPolicy{deny: os.TempDir()})
	repo(t, h.dir)

	_, err := h.svc.Apply(context.Background(), skill.Request{
		SkillID: skill.SkillSurvey, Target: h.dir,
	})
	if err == nil {
		t.Fatal("умение прочитало каталог, запрещённый владельцем")
	}
}

func TestUnknownSkillIsRefused(t *testing.T) {
	h := newHarness(t, openPolicy{})
	if _, err := h.svc.Apply(context.Background(), skill.Request{
		SkillID: "выдуманное", Target: h.dir,
	}); err == nil {
		t.Fatal("выдуманное умение применилось")
	}
}

// Аргумент, которого примитив не объявлял, не должен доехать до исполнения.
func TestLearnedSkillWithUnknownPrimitiveIsRefused(t *testing.T) {
	h := newHarness(t, openPolicy{})
	_, err := h.svc.Learn(context.Background(), skill.Skill{
		ID: "выдумка", Title: "выдумка", Question: "что-нибудь",
		Steps: []skill.Step{{Primitive: "shell.run", Args: map[string]string{"cmd": "rm -rf /"}}},
	}, event.Actor{Type: event.ActorPerson})
	if err == nil {
		t.Fatal("умение из несуществующего примитива освоено")
	}
	if !strings.Contains(err.Error(), "не существует") {
		t.Fatalf("отказ не назвал причину: %v", err)
	}
}

func TestLearnedSkillWithUndeclaredArgIsRefused(t *testing.T) {
	h := newHarness(t, openPolicy{})
	_, err := h.svc.Learn(context.Background(), skill.Skill{
		ID: "подмена", Title: "подмена", Question: "что-нибудь",
		Steps: []skill.Step{{
			Primitive: skill.PrimGitStatus,
			Args:      map[string]string{"path": skill.Target, "upstream": "origin"},
		}},
	}, event.Actor{Type: event.ActorPerson})
	if err == nil {
		t.Fatal("примитиву передан аргумент, которого он не объявлял")
	}
}

// --- умение отвечает на вопрос ---

func TestSurveyAnswersWhatKindOfDirectoryItIs(t *testing.T) {
	h := newHarness(t, openPolicy{})
	repo(t, h.dir)

	run, err := h.svc.Apply(context.Background(), skill.Request{
		SkillID: skill.SkillSurvey, Target: h.dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != skill.StatusDone {
		t.Fatalf("умение не сработало: %+v", run)
	}
	if !strings.Contains(run.Answer, "main") {
		t.Fatalf("ветка не названа: %q", run.Answer)
	}
	if !strings.Contains(run.Answer, "Репозиторий") {
		t.Fatalf("ответ не отвечает на вопрос: %q", run.Answer)
	}
}

// Ради этого вопроса раньше запускался внешний исполнитель на минуту.
func TestWorktreeDiagnoseFindsAStuckWorktree(t *testing.T) {
	h := newHarness(t, openPolicy{})
	repo(t, h.dir)

	// Рабочая копия заводится и тут же удаляется мимо git: ровно так каталог
	// и «зависает» — репозиторий продолжает считать его своим.
	gone := filepath.Join(t.TempDir(), "ушедшая")
	cmd := exec.Command("git", "-C", h.dir, "worktree", "add", "-q", "-b", "побочная", gone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git worktree недоступен: %v\n%s", err, out)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	run, err := h.svc.Apply(context.Background(), skill.Request{
		SkillID: skill.SkillWorktree, Target: h.dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.Answer, "висящих") {
		t.Fatalf("зависшая копия не найдена: %q\n%+v", run.Answer, run.Steps)
	}
	// Цена умения — довод в пользу того, чтобы не звать исполнителя.
	if run.TookMS > 15000 {
		t.Fatalf("умение шло %d мс — это уже работа, а не собственное умение", run.TookMS)
	}
}

func TestSkillOnPlainDirectorySaysSoWithoutFailing(t *testing.T) {
	h := newHarness(t, openPolicy{})
	if err := os.WriteFile(filepath.Join(h.dir, "заметка.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	run, err := h.svc.Apply(context.Background(), skill.Request{
		SkillID: skill.SkillWorktree, Target: h.dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != skill.StatusDone {
		t.Fatalf("обычный каталог принят за поломку: %+v", run)
	}
	if !strings.Contains(run.Answer, "не под git") {
		t.Fatalf("ответ уклончив: %q", run.Answer)
	}
}

// Владелец спросил «сколько свободного места», модель ответила выдуманным
// числом — потому что умение требовало каталог и на общий вопрос его не звали.
// Теперь умение отвечает на тот же вопрос, что и `df -h`, и каталога не просит.
func TestFreeSpaceAnswersWithoutATarget(t *testing.T) {
	h := newHarness(t, openPolicy{})
	run, err := h.svc.Apply(context.Background(), skill.Request{SkillID: skill.SkillFreeSpace})
	if err != nil {
		t.Fatalf("умение потребовало каталог там, где его не бывает: %v", err)
	}
	if run.Status != skill.StatusDone {
		t.Fatalf("не сработало: %+v", run)
	}
	if len(run.Steps[0].Facts) == 0 {
		t.Fatal("ни одна файловая система не названа")
	}
	// Ответ должен указывать на самое узкое место, а не усреднять.
	if !strings.Contains(run.Answer, "Теснее всего") {
		t.Fatalf("ответ не называет, где место кончается: %q", run.Answer)
	}
	// Псевдофайловые системы — шум, в котором тонет настоящий ответ.
	for _, f := range run.Steps[0].Facts {
		for _, noise := range []string{"/proc ", "/sys ", "/dev "} {
			if strings.HasPrefix(f.Text, noise) {
				t.Fatalf("в ответе служебная файловая система: %q", f.Text)
			}
		}
	}
}

// --- след в журнале ---

func TestRunIsRecordedAndReadableBack(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, openPolicy{})
	repo(t, h.dir)

	run, err := h.svc.Apply(ctx, skill.Request{
		SkillID: skill.SkillSurvey, Target: h.dir, ThreadID: "thr_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := h.svc.Runs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("применение не попало в проекцию: %+v", runs)
	}
	if runs[0].ThreadID != "thr_1" {
		t.Fatal("применение не привязано к нити: связать вывод с делом будет нечем")
	}
	if len(runs[0].Steps) != 3 {
		t.Fatalf("шаги потеряны: %+v", runs[0].Steps)
	}
}

// Отчёт показывает не только вывод, но и то, из чего он сделан.
func TestReportShowsFactsBehindTheAnswer(t *testing.T) {
	h := newHarness(t, openPolicy{})
	repo(t, h.dir)

	run, err := h.svc.Apply(context.Background(), skill.Request{
		SkillID: skill.SkillSurvey, Target: h.dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	report := run.Report()
	if !strings.HasPrefix(report, "Посмотрел сам.") {
		t.Fatalf("не сказано, что сделано самим: %q", report)
	}
	if strings.Count(report, "\n·") < 3 {
		t.Fatalf("вывод без оснований: %q", report)
	}
}

// --- освоение и снятие ---

func TestLearnedSkillBecomesApplicableAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, openPolicy{})
	repo(t, h.dir)

	learned := skill.Skill{
		ID: "git.recent", Title: "посмотреть последние коммиты",
		Question: "что делали в репозитории в последнее время", NeedsTarget: true,
		Steps: []skill.Step{{
			Primitive: skill.PrimGitLog,
			Args:      map[string]string{"path": skill.Target, "count": "3"},
			Why:       "прочитать историю",
		}},
	}
	if _, err := h.svc.Learn(ctx, learned, event.Actor{Type: event.ActorPerson}); err != nil {
		t.Fatal(err)
	}
	run, err := h.svc.Apply(ctx, skill.Request{SkillID: "git.recent", Target: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.Answer, "первый") {
		t.Fatalf("освоенное умение ничего не увидело: %q", run.Answer)
	}

	// Освоенное умение живёт в проекции, а не в памяти процесса: после
	// перезапуска Бэрримор не должен разучиваться.
	after := h.service(t)
	if _, ok := after.Get("git.recent"); ok {
		t.Fatal("умение нашлось до восстановления: значит, оно не из журнала")
	}
	if err := after.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	restored, ok := after.Get("git.recent")
	if !ok {
		t.Fatal("после перезапуска умение забыто")
	}
	if len(restored.Steps) != 1 || restored.Steps[0].Args["count"] != "3" {
		t.Fatalf("шаги умения потеряны при восстановлении: %+v", restored.Steps)
	}
	if _, err := after.Apply(ctx, skill.Request{SkillID: "git.recent", Target: h.dir}); err != nil {
		t.Fatalf("восстановленное умение не применяется: %v", err)
	}
}

// Снятие переживает перезапуск: иначе негодный способ вернулся бы сам.
func TestRetirementSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, openPolicy{})
	const why = "на этом хосте примитив врёт"
	if err := h.svc.Retire(ctx, skill.SkillFreeSpace, why,
		event.Actor{Type: event.ActorBarrymore}); err != nil {
		t.Fatal(err)
	}
	after := h.service(t)
	if err := after.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	sk, ok := after.Get(skill.SkillFreeSpace)
	if !ok {
		t.Fatal("встроенное умение пропало вовсе")
	}
	if sk.Live() {
		t.Fatal("снятое умение вернулось после перезапуска")
	}
	if sk.RetiredWhy != why {
		t.Fatalf("причина снятия потеряна: %q", sk.RetiredWhy)
	}
}

func TestRetiredSkillIsNotAppliedAndSaysWhy(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, openPolicy{})
	repo(t, h.dir)

	const why = "git worktree list врёт на репозиториях с submodule"
	if err := h.svc.Retire(ctx, skill.SkillWorktree, why,
		event.Actor{Type: event.ActorBarrymore}); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.Apply(ctx, skill.Request{SkillID: skill.SkillWorktree, Target: h.dir})
	if err == nil {
		t.Fatal("снятое умение применилось")
	}
	if !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("отказ не сказал, почему способ негоден: %v", err)
	}
	for _, sk := range h.svc.Live() {
		if sk.ID == skill.SkillWorktree {
			t.Fatal("снятое умение осталось в списке, показываемом модели")
		}
	}
}

func TestRetireWithoutReasonIsRefused(t *testing.T) {
	h := newHarness(t, openPolicy{})
	if err := h.svc.Retire(context.Background(), skill.SkillSurvey, "  ",
		event.Actor{Type: event.ActorBarrymore}); err == nil {
		t.Fatal("умение снято без причины: оспорить это будет нечем")
	}
}
