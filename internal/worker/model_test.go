package worker_test

import (
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/worker"
)

var now = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func named(ref string) worker.Model {
	tier, evidence, confidence := worker.ClassifyModelRef(ref)
	return worker.Model{
		Ref: ref, CostTier: tier, Evidence: evidence, Confidence: confidence,
		Source: "cli-list", LastCost: -1, ObservedAt: now,
	}
}

func TestFreeIsDecidedBeforeRunFromProviderMarking(t *testing.T) {
	// Бесплатность определяется до запуска: провайдер помечает такие модели
	// в названии, и другого бесплатного способа узнать цену заранее нет.
	cases := []struct {
		ref  string
		tier string
	}{
		{"opencode/deepseek-v4-flash-free", worker.CostFree},
		{"openrouter/google/gemma-4-31b-it:free", worker.CostFree},
		{"ollama/qwen3-30b", worker.CostFree},
		{"openrouter/anthropic/claude-opus-4.8", worker.CostUnknown},
		{"opencode/big-pickle", worker.CostUnknown},
	}
	for _, tc := range cases {
		tier, evidence, _ := worker.ClassifyModelRef(tc.ref)
		if tier != tc.tier {
			t.Errorf("%s → %q, ожидалось %q", tc.ref, tier, tc.tier)
		}
		if evidence == "" {
			t.Errorf("%s: основание классификации не указано", tc.ref)
		}
	}
}

func TestFreeOnlyPolicyRefusesUnmarkedModels(t *testing.T) {
	// Модель без пометки бесплатной таковой не считается: молчание провайдера
	// не является разрешением тратить деньги владельца.
	models := []worker.Model{
		named("openrouter/anthropic/claude-opus-4.8"),
		named("opencode/big-pickle"),
	}
	if _, _, err := worker.SelectModel(models, worker.FreeOnly(), "", now); err == nil {
		t.Fatal("при политике «только бесплатные» выбрана модель без пометки")
	}

	models = append(models, named("opencode/ling-3.0-flash-free"))
	got, reason, err := worker.SelectModel(models, worker.FreeOnly(), "", now)
	if err != nil {
		t.Fatalf("выбор модели: %v", err)
	}
	if got.Ref != "opencode/ling-3.0-flash-free" {
		t.Fatalf("выбрана модель %s вместо бесплатной", got.Ref)
	}
	if reason == "" {
		t.Fatal("выбор модели не объяснён")
	}
}

func TestChargedModelIsPermanentlyExcludedFromFreeWork(t *testing.T) {
	// Модель считалась бесплатной, но за неё списали деньги. С этого момента
	// она платная, как бы ни называлась.
	free := named("opencode/ling-3.0-flash-free")
	if free.CostTier != worker.CostFree {
		t.Fatalf("исходная классификация %q", free.CostTier)
	}

	charged := worker.MarkCharged(free, 0.0042, now)
	if charged.CostTier != worker.CostPaid {
		t.Fatalf("после списания модель осталась %q", charged.CostTier)
	}
	if !charged.Charged() {
		t.Fatal("факт списания не зафиксирован")
	}

	// Под политикой «только бесплатные» она больше не выбирается,
	// хотя пометка free в названии никуда не делась.
	_, _, err := worker.SelectModel([]worker.Model{charged}, worker.FreeOnly(), "", now)
	if err == nil {
		t.Fatal("модель с зафиксированным списанием снова выбрана как бесплатная")
	}

	// И спустя долгое время тоже: отметка постоянная.
	later := now.Add(90 * 24 * time.Hour)
	if _, _, err := worker.SelectModel([]worker.Model{charged}, worker.FreeOnly(), "", later); err == nil {
		t.Fatal("отметка о списании выветрилась со временем")
	}
}

func TestCatalogRefreshDoesNotForgetCharges(t *testing.T) {
	// Провайдер обновил список и снова написал в названии "free".
	// Память о списании должна пережить обновление каталога.
	charged := worker.MarkCharged(named("opencode/ling-3.0-flash-free"), 0.01, now)
	fresh := []worker.Model{
		named("opencode/ling-3.0-flash-free"),
		named("opencode/mimo-v2.5-free"),
	}

	merged := worker.CarryCharges(fresh, []worker.Model{charged})

	var restored worker.Model
	for _, m := range merged {
		if m.Ref == "opencode/ling-3.0-flash-free" {
			restored = m
		}
	}
	if restored.CostTier != worker.CostPaid {
		t.Fatalf("после обновления каталога модель снова считается %q", restored.CostTier)
	}
	if !restored.Charged() {
		t.Fatal("отметка о списании потеряна при обновлении каталога")
	}

	// Незатронутая модель остаётся бесплатной.
	for _, m := range merged {
		if m.Ref == "opencode/mimo-v2.5-free" && m.CostTier != worker.CostFree {
			t.Fatalf("чужая отметка перекинулась на модель %s", m.Ref)
		}
	}
}

func TestDisappearedModelIsNotOffered(t *testing.T) {
	// Провайдер убрал бесплатную модель. Каталог заменяется целиком,
	// поэтому исчезнувшая модель больше не предлагается.
	before := []worker.Model{named("opencode/ling-3.0-flash-free")}
	if _, _, err := worker.SelectModel(before, worker.FreeOnly(), "", now); err != nil {
		t.Fatalf("выбор до исчезновения: %v", err)
	}

	after := worker.CarryCharges([]worker.Model{named("opencode/mimo-v2.5-free")}, before)
	for _, m := range after {
		if m.Ref == "opencode/ling-3.0-flash-free" {
			t.Fatal("исчезнувшая модель осталась в каталоге")
		}
	}

	// Ручной выбор исчезнувшей модели тоже отклоняется.
	if _, _, err := worker.SelectModel(after, worker.FreeOnly(),
		"opencode/ling-3.0-flash-free", now); err == nil {
		t.Fatal("выбрана модель, которой больше нет в каталоге")
	}
}

func TestManualChoiceCannotBypassCostPolicy(t *testing.T) {
	paid := named("openrouter/anthropic/claude-opus-4.8")
	models := []worker.Model{paid, named("opencode/mimo-v2.5-free")}

	_, _, err := worker.SelectModel(models, worker.FreeOnly(), paid.Ref, now)
	if err == nil {
		t.Fatal("ручной выбор обошёл политику стоимости")
	}

	// При разрешающей политике тот же выбор проходит.
	got, _, err := worker.SelectModel(models, worker.AnyCost(), paid.Ref, now)
	if err != nil {
		t.Fatalf("ручной выбор при разрешающей политике: %v", err)
	}
	if got.Ref != paid.Ref {
		t.Fatalf("выбрана модель %s вместо запрошенной", got.Ref)
	}
}

func TestStaleCatalogIsReportedNotHidden(t *testing.T) {
	// Состав бесплатных моделей меняется, поэтому у каталога есть срок годности.
	v := worker.View{Worker: worker.Worker{}}
	stale, note := v.ModelsStale(now)
	if !stale || note == "" {
		t.Fatal("каталог, который ни разу не обновлялся, не помечен устаревшим")
	}

	recent := now.Add(-time.Hour)
	v.Worker.ModelsRefreshedAt = &recent
	if stale, _ := v.ModelsStale(now); stale {
		t.Fatal("свежий каталог помечен устаревшим")
	}

	old := now.Add(-worker.ModelCatalogTTL - time.Minute)
	v.Worker.ModelsRefreshedAt = &old
	stale, note = v.ModelsStale(now)
	if !stale || note == "" {
		t.Fatal("просроченный каталог не помечен устаревшим")
	}
}

func TestPolicyNames(t *testing.T) {
	free, err := worker.ParseModelPolicy("free")
	if err != nil {
		t.Fatalf("разбор политики: %v", err)
	}
	if free.Allows(worker.CostPaid) || free.AllowSpecialists {
		t.Fatal("политика «только бесплатные» разрешает лишнее")
	}
	if _, err := worker.ParseModelPolicy("что-нибудь"); err == nil {
		t.Fatal("принята неизвестная политика стоимости")
	}
}
