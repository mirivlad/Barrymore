package learning_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/learning"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/skill"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

// retirer запоминает, что было снято с применения.
type retirer struct {
	id  string
	why string
}

func (r *retirer) Retire(_ context.Context, id, why string, _ event.Actor) error {
	r.id, r.why = id, why
	return nil
}

// runs отдаёт заранее заданные применения умений.
type runs struct{ items []skill.Run }

func (r runs) Runs(context.Context, int) ([]skill.Run, error) { return r.items, nil }

type harness struct {
	svc *learning.Service
	ret *retirer
	clk *clock.Fake
}

func newHarness(t *testing.T, src learning.RunSource) *harness {
	t.Helper()
	clk := testsupport.Clock()
	db := testsupport.OpenDBAt(t, filepath.Join(t.TempDir(), "barrymore.db"))
	ret := &retirer{}
	svc := learning.New(learning.Config{
		DB: db, Journal: event.NewJournal(db, clk), Clock: clk,
		Skills: ret, Runs: src, Logger: testsupport.Logger(t),
	})
	reg := projection.NewRegistry()
	svc.Projections(reg)
	return &harness{svc: svc, ret: ret, clk: clk}
}

func outcome(ref, result, evidence string, ms int64) learning.Outcome {
	return learning.Outcome{
		Kind: learning.KindOwn, Ref: ref, Title: "разобраться с рабочими копиями",
		Result: result, Evidence: evidence, TookMS: ms,
	}
}

// --- опыт становится записью ---

// Практика без основания — мнение, а мнение не должно менять поведение системы.
func TestOutcomeWithoutEvidenceIsRefused(t *testing.T) {
	h := newHarness(t, runs{})
	err := h.svc.Note(context.Background(),
		learning.Outcome{Kind: learning.KindOwn, Ref: "git.status", Result: learning.OutcomeGood})
	if err == nil {
		t.Fatal("исход без основания записан")
	}
}

func TestExperienceAccumulatesIntoAReadableRecord(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, runs{})
	for i := 0; i < 3; i++ {
		if err := h.svc.Note(ctx, outcome("git.worktree.diagnose",
			learning.OutcomeGood, "нашёл висящую копию", 40)); err != nil {
			t.Fatal(err)
		}
	}
	ps, err := h.svc.Practices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("практик %d, ожидалась одна", len(ps))
	}
	p := ps[0]
	if p.Applied != 3 || p.Succeeded != 3 {
		t.Fatalf("опыт посчитан неверно: %+v", p)
	}
	if p.AvgMS != 40 {
		t.Fatalf("цена способа потеряна: %d мс", p.AvgMS)
	}
	// Запись читается вслух, иначе она не попадёт ни в контекст, ни на экран.
	if !strings.Contains(p.Record(), "без осечек") {
		t.Fatalf("запись нечитаема: %q", p.Record())
	}
}

// --- опыт меняет поведение ---

// Главное во всём контуре: способ, переставший работать, перестают применять.
func TestThreeFailuresInARowRetireTheApproach(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, runs{})
	for i := 0; i < 3; i++ {
		if err := h.svc.Note(ctx, outcome("git.worktree.diagnose",
			learning.OutcomeBad, "git worktree list завершился ошибкой", 12)); err != nil {
			t.Fatal(err)
		}
	}
	if h.ret.id != "git.worktree.diagnose" {
		t.Fatal("негодный способ остался в применении")
	}
	if !strings.Contains(h.ret.why, "подряд") || !strings.Contains(h.ret.why, "ошибкой") {
		t.Fatalf("причина снятия ничего не объясняет: %q", h.ret.why)
	}

	p, err := h.svc.Practice(ctx, "own:git.worktree.diagnose")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Stale || p.StaleWhy == "" {
		t.Fatalf("практика не помечена негодной: %+v", p)
	}
	if p.Reliable() {
		t.Fatal("на негодный способ по-прежнему предлагается полагаться")
	}
	if !strings.Contains(p.Record(), "больше не пользуюсь") {
		t.Fatalf("владельцу не сказано, что способ снят: %q", p.Record())
	}
}

// Две неудачи — ещё не свойство: разовый отказ бывает от чужой заминки.
func TestTwoFailuresDoNotRetireYet(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, runs{})
	for i := 0; i < 2; i++ {
		if err := h.svc.Note(ctx, outcome("git.status", learning.OutcomeBad, "не вышло", 5)); err != nil {
			t.Fatal(err)
		}
	}
	if h.ret.id != "" {
		t.Fatalf("способ снят после двух неудач: %q", h.ret.id)
	}
}

// Удача обнуляет счётчик: иначе три неудачи за год снимали бы рабочий способ.
func TestSuccessResetsTheStreak(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, runs{})
	note := func(res, ev string) {
		t.Helper()
		if err := h.svc.Note(ctx, outcome("git.status", res, ev, 5)); err != nil {
			t.Fatal(err)
		}
	}
	note(learning.OutcomeBad, "не вышло")
	note(learning.OutcomeBad, "не вышло")
	note(learning.OutcomeGood, "ветка main")
	note(learning.OutcomeBad, "не вышло")
	note(learning.OutcomeBad, "не вышло")

	if h.ret.id != "" {
		t.Fatalf("способ снят, хотя между неудачами он работал: %q", h.ret.id)
	}
}

// Исход применения умения попадает в опыт сам, без отдельного обхода.
func TestSkillRunFeedsExperienceDirectly(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, runs{})
	h.svc.SkillApplied(ctx, skill.Run{
		SkillID: "workspace.survey", SkillTitle: "осмотреть каталог",
		Status: skill.StatusDone, Answer: "Репозиторий на ветке main", TookMS: 33,
	})
	p, err := h.svc.Practice(ctx, "own:workspace.survey")
	if err != nil {
		t.Fatal(err)
	}
	if p.Applied != 1 || p.LastNote == "" {
		t.Fatalf("применение не отразилось в опыте: %+v", p)
	}
}

// --- опыт порождает новые умения ---

func session(target string, at time.Time, ids ...string) []skill.Run {
	out := []skill.Run{}
	for i, id := range ids {
		out = append(out, skill.Run{
			ID: fmt.Sprintf("act_%s_%d", target, i), SkillID: id,
			SkillTitle: "умение " + id, Target: target, Status: skill.StatusDone,
			StartedAt: at.Add(time.Duration(i) * time.Minute),
		})
	}
	return out
}

// Повторившийся порядок действий превращается в предложение умения.
// Это не изобретение: владелец уже делал так дважды.
func TestRepeatedSequenceBecomesASuggestion(t *testing.T) {
	base := testsupport.Epoch
	var all []skill.Run
	all = append(all, session("/a", base, "workspace.survey", "workspace.who")...)
	all = append(all, session("/b", base.Add(2*time.Hour), "workspace.survey", "workspace.who")...)
	// Runs отдаёт свежие первыми.
	reverse(all)

	h := newHarness(t, runs{items: all})
	sg, err := h.svc.Suggest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sg) != 1 {
		t.Fatalf("предложений %d, ожидалось одно: %+v", len(sg), sg)
	}
	if sg[0].SeenTimes != 2 {
		t.Fatalf("повторов насчитано %d", sg[0].SeenTimes)
	}
	if len(sg[0].Skills) != 2 {
		t.Fatalf("шаги предложения потеряны: %+v", sg[0].Skills)
	}
}

// Тот же порядок на том же каталоге — тоже повтор.
//
// Это самый частый случай: владелец возвращается к одному репозиторию
// и выясняет одно и то же. Если считать заход по каталогу, эти повторы
// сольются в один длинный заход и не будут замечены никогда.
func TestSameTargetTwiceIsAlsoARepeat(t *testing.T) {
	base := testsupport.Epoch
	var all []skill.Run
	all = append(all, session("/a", base, "workspace.survey", "workspace.who")...)
	all = append(all, session("/a", base.Add(10*time.Minute),
		"workspace.survey", "workspace.who")...)
	reverse(all)

	h := newHarness(t, runs{items: all})
	sg, err := h.svc.Suggest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sg) != 1 || sg[0].SeenTimes != 2 {
		t.Fatalf("повтор на том же каталоге не замечен: %+v", sg)
	}
}

// Один раз — ещё не порядок работы.
func TestSingleSequenceIsNotSuggested(t *testing.T) {
	all := session("/a", testsupport.Epoch, "workspace.survey", "workspace.who")
	reverse(all)
	h := newHarness(t, runs{items: all})
	sg, err := h.svc.Suggest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sg) != 0 {
		t.Fatalf("однократное совпадение выдано за способ работы: %+v", sg)
	}
}

// Собранное умение состоит из шагов уже существующих: новых полномочий
// предложение не даёт.
func TestComposedSkillReusesExistingSteps(t *testing.T) {
	known := map[string]skill.Skill{}
	for _, sk := range skill.Builtin() {
		known[sk.ID] = sk
	}
	sg := learning.Suggestion{
		ID: "seq:workspace.survey+workspace.who", Title: "осмотр и кто держит",
		Question: "то, что вы уже выясняли этим порядком",
		Skills:   []string{skill.SkillSurvey, skill.SkillWhoIsWorking},
	}
	got, err := learning.Compose(sg, known)
	if err != nil {
		t.Fatal(err)
	}
	want := len(known[skill.SkillSurvey].Steps) + len(known[skill.SkillWhoIsWorking].Steps)
	if len(got.Steps) != want {
		t.Fatalf("шагов %d, ожидалось %d", len(got.Steps), want)
	}
	if got.Origin != skill.OriginLearned {
		t.Fatal("освоенное умение выдаёт себя за встроенное")
	}
	if !got.NeedsTarget {
		t.Fatal("каталог потерян: умение не будет знать, куда смотреть")
	}
}

func TestComposeRefusesVanishedSkill(t *testing.T) {
	_, err := learning.Compose(learning.Suggestion{
		Skills: []string{"того.чего.нет"},
	}, map[string]skill.Skill{})
	if err == nil {
		t.Fatal("умение собрано из исчезнувшего")
	}
}

func reverse(rs []skill.Run) {
	for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
		rs[i], rs[j] = rs[j], rs[i]
	}
}
