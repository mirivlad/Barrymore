package initiative_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/initiative"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/store"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

// scriptedSource отдаёт заранее заданные поводы.
type scriptedSource struct {
	cands []initiative.Candidate
	calls int
	err   error
}

func (s *scriptedSource) Candidates(context.Context, time.Time) ([]initiative.Candidate, error) {
	s.calls++
	return s.cands, s.err
}

type harness struct {
	svc *initiative.Service
	clk *clock.Fake
	db  *store.DB
	src *scriptedSource
}

func newHarness(t *testing.T, p initiative.Policy, cands ...initiative.Candidate) *harness {
	t.Helper()
	clk := testsupport.Clock()
	db := testsupport.OpenDBAt(t, filepath.Join(t.TempDir(), "barrymore.db"))
	j := event.NewJournal(db, clk)
	svc := initiative.NewService(db, j, clk, p, testsupport.Logger(t))
	src := &scriptedSource{cands: cands}
	svc.AddSource(src)

	reg := projection.NewRegistry()
	svc.Projections(reg)
	return &harness{svc: svc, clk: clk, db: db, src: src}
}

func candidate(kind, subject string) initiative.Candidate {
	return initiative.Candidate{
		Kind: kind, SubjectType: "work_order", SubjectID: subject,
		Level: initiative.LevelAttention,
		Title: "Поручение выполнено: аудит",
		Why:   "исполнитель закончил, все проверки пройдены",
	}
}

// --- повод и причина ---

// Обращение без причины невозможно: оно неотличимо от навязчивости.
func TestNoticeWithoutReasonIsRefused(t *testing.T) {
	bad := candidate(initiative.KindOrderFinished, "wo_1")
	bad.Why = ""
	h := newHarness(t, initiative.DefaultPolicy(), bad)

	if _, _, err := h.svc.Tick(context.Background()); err == nil {
		t.Fatal("повод без причины обращения должен быть ошибкой")
	}
}

func TestNoticeCarriesWhyNow(t *testing.T) {
	h := newHarness(t, quietFreePolicy(), candidate(initiative.KindOrderFinished, "wo_1"))
	created, _, err := h.svc.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("создано обращений %d, ожидалось 1", created)
	}

	sum, err := h.svc.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Waiting) != 1 {
		t.Fatalf("ждут %d обращений, ожидалось 1", len(sum.Waiting))
	}
	if sum.Waiting[0].Why == "" {
		t.Fatal("обращение без ответа на «почему сейчас»")
	}
}

// Один повод — одно обращение. Два письма об одном и том же уже назойливость.
func TestSameReasonNeverRepeats(t *testing.T) {
	h := newHarness(t, quietFreePolicy(), candidate(initiative.KindOrderFinished, "wo_1"))
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, _, err := h.svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		h.clk.Advance(time.Hour)
	}
	all, err := h.svc.List(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("обращений %d, ожидалось 1: один повод не должен звать пять раз", len(all))
	}
}

// --- тихие часы ---

// Тихие часы откладывают, но не отменяют: повод никуда не денется.
func TestQuietHoursDelayButDoNotCancel(t *testing.T) {
	p := initiative.DefaultPolicy()
	p.QuietFrom, p.QuietTo = 23, 8
	h := newHarness(t, p, candidate(initiative.KindOrderFinished, "wo_1"))
	// Полночь по местному времени: тихие часы объявлены в нём.
	h.clk.Set(time.Date(2026, 8, 5, 0, 30, 0, 0, time.Local).UTC())
	ctx := context.Background()

	if _, delivered, err := h.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	} else if delivered != 0 {
		t.Fatal("в тихие часы обычное обращение показываться не должно")
	}

	sum, _ := h.svc.Pending(ctx)
	if sum.HeldCount != 1 {
		t.Fatalf("удержано %d, ожидалось 1", sum.HeldCount)
	}
	if sum.HeldReason == "" {
		t.Fatal("владельцу не сказано, почему обращение ждёт")
	}

	// Утро наступило — обращение доходит.
	h.clk.Set(time.Date(2026, 8, 5, 9, 0, 0, 0, time.Local).UTC())
	if _, delivered, err := h.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	} else if delivered != 1 {
		t.Fatalf("после тихих часов показано %d, ожидалось 1", delivered)
	}
}

// Срочное проходит и ночью: молчать о том, с чем Бэрримор не справился,
// значило бы скрывать неудачу.
func TestUrgentPassesThroughQuietHours(t *testing.T) {
	urgent := candidate(initiative.KindEscalated, "run_1")
	urgent.Level = initiative.LevelUrgent
	urgent.Title = "Не справился сам"
	urgent.Why = "локальные попытки исчерпаны"

	p := initiative.DefaultPolicy()
	h := newHarness(t, p, urgent)
	h.clk.Set(time.Date(2026, 8, 5, 2, 0, 0, 0, time.Local).UTC())

	if _, delivered, err := h.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	} else if delivered != 1 {
		t.Fatal("срочное обращение задержано тихими часами")
	}
}

// --- предел обращений ---

func TestDailyLimitHoldsRatherThanDrops(t *testing.T) {
	p := quietFreePolicy()
	p.MaxPerDay = 2
	var cands []initiative.Candidate
	for _, id := range []string{"wo_1", "wo_2", "wo_3", "wo_4"} {
		cands = append(cands, candidate(initiative.KindOrderFinished, id))
	}
	h := newHarness(t, p, cands...)
	ctx := context.Background()

	if _, delivered, err := h.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	} else if delivered != 2 {
		t.Fatalf("показано %d, ожидалось 2 по пределу", delivered)
	}

	sum, _ := h.svc.Pending(ctx)
	if sum.HeldCount != 2 {
		t.Fatalf("удержано %d, ожидалось 2: повод не отменяется пределом", sum.HeldCount)
	}
	if sum.HeldReason == "" {
		t.Fatal("не сказано, почему остальное ждёт")
	}

	// Назавтра предел обновляется.
	h.clk.Advance(25 * time.Hour)
	if _, delivered, err := h.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	} else if delivered != 2 {
		t.Fatalf("на следующий день показано %d, ожидалось 2", delivered)
	}
}

// --- заглушка ---

// Сценарий M: mute соблюдается, а после его снятия обращение содержит причину.
func TestMuteIsRespectedAndReasonSurvives(t *testing.T) {
	h := newHarness(t, quietFreePolicy(), candidate(initiative.KindOrderFinished, "wo_1"))
	ctx := context.Background()

	h.svc.Mute(initiative.KindOrderFinished, "")
	if created, _, err := h.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	} else if created != 0 {
		t.Fatal("заглушённый повод всё равно стал обращением")
	}

	h.svc.Unmute(initiative.KindOrderFinished, "")
	if created, _, err := h.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	} else if created != 1 {
		t.Fatal("после снятия заглушки обращение не появилось")
	}
	sum, _ := h.svc.Pending(ctx)
	if len(sum.Waiting) != 1 || sum.Waiting[0].Why == "" {
		t.Fatal("обращение после снятия заглушки должно содержать причину (сценарий M)")
	}
}

func TestMuteBySubjectDoesNotSilenceOthers(t *testing.T) {
	h := newHarness(t, quietFreePolicy(),
		candidate(initiative.KindOrderFinished, "wo_1"),
		candidate(initiative.KindOrderFinished, "wo_2"))
	h.svc.Mute("", "wo_1")

	created, _, err := h.svc.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("создано %d обращений, ожидалось 1: заглушено только одно поручение", created)
	}
}

// Выключенная инициатива молчит полностью и не тратит источники.
func TestDisabledInitiativeAsksNothing(t *testing.T) {
	p := initiative.DefaultPolicy()
	p.Enabled = false
	h := newHarness(t, p, candidate(initiative.KindOrderFinished, "wo_1"))

	created, delivered, err := h.svc.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 || delivered != 0 {
		t.Fatal("выключенная инициатива обратилась к владельцу")
	}
	if h.src.calls != 0 {
		t.Fatal("выключенная инициатива всё равно опрашивала источники")
	}
}

// --- отпавший повод ---

// Владелец решил вопрос сам — звать его разбираться уже не нужно.
func TestStaleNoticeIsWithdrawn(t *testing.T) {
	h := newHarness(t, quietFreePolicy(), candidate(initiative.KindChangesWaiting, "wo_1"))
	ctx := context.Background()
	if _, _, err := h.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if err := h.svc.MarkStale(ctx, "changes.waiting:wo_1", "владелец применил изменения сам"); err != nil {
		t.Fatal(err)
	}
	sum, _ := h.svc.Pending(ctx)
	if len(sum.Waiting) != 0 {
		t.Fatal("обращение о деле, которого больше нет, осталось висеть")
	}
}

func TestMarkReadRemovesFromWaiting(t *testing.T) {
	h := newHarness(t, quietFreePolicy(), candidate(initiative.KindOrderFinished, "wo_1"))
	ctx := context.Background()
	if _, _, err := h.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	sum, _ := h.svc.Pending(ctx)
	if len(sum.Waiting) != 1 {
		t.Fatalf("ждут %d", len(sum.Waiting))
	}

	if err := h.svc.MarkRead(ctx, sum.Waiting[0].ID); err != nil {
		t.Fatal(err)
	}
	sum, _ = h.svc.Pending(ctx)
	if len(sum.Waiting) != 0 {
		t.Fatal("прочитанное обращение продолжает ждать")
	}
}

// --- рамки ---

func TestQuietHoursAcrossMidnight(t *testing.T) {
	p := initiative.DefaultPolicy()
	p.QuietFrom, p.QuietTo = 23, 8
	cases := []struct {
		hour  int
		quiet bool
	}{{23, true}, {0, true}, {3, true}, {7, true}, {8, false}, {12, false}, {22, false}}
	for _, c := range cases {
		// Момент задаётся местным временем: тихие часы объявлены в нём.
		at := time.Date(2026, 8, 5, c.hour, 0, 0, 0, time.Local)
		if got := p.Quiet(at); got != c.quiet {
			t.Errorf("%02d:00 → тихо=%v, ожидалось %v", c.hour, got, c.quiet)
		}
	}
}

// Часы Бэрримора внутри идут в UTC, а тихие часы объявлены местным временем.
// Считать их по UTC значило бы для владельца в UTC+8 молчать с семи утра
// до четырёх дня — ровно наоборот задуманному.
func TestQuietHoursFollowLocalTimeNotUTC(t *testing.T) {
	p := initiative.DefaultPolicy()
	p.QuietFrom, p.QuietTo = 23, 8

	// Полдень по местному времени — говорить можно, каким бы ни был UTC.
	noon := time.Date(2026, 8, 5, 12, 0, 0, 0, time.Local)
	if p.Quiet(noon.UTC()) {
		t.Fatalf("полдень (%s местного, %s UTC) признан тихим часом",
			noon.Format("15:04"), noon.UTC().Format("15:04"))
	}

	// Глубокая ночь по местному — молчим.
	night := time.Date(2026, 8, 5, 2, 0, 0, 0, time.Local)
	if !p.Quiet(night.UTC()) {
		t.Fatal("два часа ночи по местному времени не признаны тихим часом")
	}
	// И конец тишины считается тоже по местному.
	if got := p.NextAudible(night.UTC()).Local().Hour(); got != 8 {
		t.Fatalf("тишина кончается в %02d:00 местного, ожидалось 08:00", got)
	}
}

func TestPolicyModes(t *testing.T) {
	on, err := initiative.ParsePolicy("on")
	if err != nil || !on.Enabled {
		t.Fatalf("режим on: %v", err)
	}
	off, err := initiative.ParsePolicy("off")
	if err != nil || off.Enabled {
		t.Fatalf("режим off: %v", err)
	}
	urgent, err := initiative.ParsePolicy("urgent-only")
	if err != nil {
		t.Fatal(err)
	}
	if !urgent.Muted(initiative.KindOrderFinished, "") {
		t.Fatal("в режиме urgent-only обычные поводы должны молчать")
	}
	if urgent.Muted(initiative.KindEscalated, "") {
		t.Fatal("в режиме urgent-only несправившаяся реакция обязана дойти")
	}
	if _, err := initiative.ParsePolicy("что-нибудь"); err == nil {
		t.Fatal("принят неизвестный режим инициативы")
	}
}

// quietFreePolicy убирает тихие часы, чтобы проверять остальное отдельно.
func quietFreePolicy() initiative.Policy {
	p := initiative.DefaultPolicy()
	p.QuietFrom, p.QuietTo = 0, 0
	return p
}

// Причина удержания должна быть настоящей. Сказать «предел исчерпан», когда
// обращение просто ждёт утра, — объяснение, которое звучит уверенно и вводит
// в заблуждение.
func TestHeldReasonNamesTheRealCause(t *testing.T) {
	p := initiative.DefaultPolicy()
	p.QuietFrom, p.QuietTo = 23, 8
	h := newHarness(t, p, candidate(initiative.KindOrderFinished, "wo_1"))
	h.clk.Set(time.Date(2026, 8, 5, 1, 0, 0, 0, time.Local).UTC())
	ctx := context.Background()

	if _, _, err := h.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	sum, _ := h.svc.Pending(ctx)
	if sum.HeldCount != 1 {
		t.Fatalf("удержано %d", sum.HeldCount)
	}
	if !strings.Contains(sum.HeldReason, "тихих часов") {
		t.Fatalf("причина удержания названа неверно: %q", sum.HeldReason)
	}
	if strings.Contains(sum.HeldReason, "уже") {
		t.Fatalf("удержание по времени выдано за исчерпанный предел: %q", sum.HeldReason)
	}
}

// А когда предел действительно исчерпан — так и сказано.
func TestHeldReasonNamesTheLimitWhenItIsTheCause(t *testing.T) {
	p := quietFreePolicy()
	p.MaxPerDay = 1
	h := newHarness(t, p,
		candidate(initiative.KindOrderFinished, "wo_1"),
		candidate(initiative.KindOrderFinished, "wo_2"))
	ctx := context.Background()

	if _, _, err := h.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	sum, _ := h.svc.Pending(ctx)
	if sum.HeldCount != 1 {
		t.Fatalf("удержано %d, ожидалось 1", sum.HeldCount)
	}
	if !strings.Contains(sum.HeldReason, "завтра") {
		t.Fatalf("исчерпанный предел не назван причиной: %q", sum.HeldReason)
	}
}
