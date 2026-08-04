package runtime_test

import (
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/runtime"
)

// servingExpectation создаёт стоячее ожидание по локальной модели.
func servingExpectation(t *testing.T, h *harness) runtime.Expectation {
	t.Helper()
	return mustExpectation(t, h, runtime.ExpectationRequest{
		SubjectType: runtime.SubjectProvider,
		SubjectID:   "local-model",
		Kind:        runtime.KindLocalModelServing,
		Params: runtime.ParamsLocalModelServing{
			Endpoint:     "http://127.0.0.1:18080",
			CheckEvery:   30 * time.Second,
			SilenceAfter: 150 * time.Second,
		},
		Basis:            "разговорный слой настроен на локальную модель",
		SeverityIfMissed: runtime.SeverityWarning,
		CheckInterval:    30 * time.Second,
		FirstCheckAfter:  15 * time.Second,
	})
}

func observeLocalModel(t *testing.T, h *harness, p runtime.LocalModelStatePayload) {
	t.Helper()
	mustObserve(t, h, runtime.ObservationRequest{
		Kind:          runtime.ObsLocalModel,
		SubjectType:   runtime.SubjectProvider,
		SubjectID:     "local-model",
		Source:        "test",
		SourceQuality: runtime.QualityDirect,
		Confidence:    1,
		Payload:       p,
	})
}

// Работающая модель не закрывает ожидание: оно стоячее и должно пережить
// сколько угодно удачных проверок, иначе следующая пропажа останется незамеченной.
func TestLocalModelServingStaysPending(t *testing.T) {
	h := newHarness(t, tempDBPath(t), nil)
	exp := servingExpectation(t, h)

	for i := 0; i < 3; i++ {
		observeLocalModel(t, h, runtime.LocalModelStatePayload{
			Serving: true, Endpoint: "http://127.0.0.1:18080", Managed: true,
		})
		h.clk.Advance(40 * time.Second)
		tick(t, h)
		if got := reload(t, h, exp.ID); got.Status != runtime.ExpectationPending {
			t.Fatalf("проверка %d: статус %q, ожидался pending", i, got.Status)
		}
	}
	if n := openCount(t, h); n != 0 {
		t.Fatalf("при работающей модели открытых расхождений %d, ожидалось 0", n)
	}
}

// Загрузка весов расхождением не является: большая модель поднимается минутами,
// и перезапуск в это время не дал бы ей запуститься никогда.
func TestLocalModelLoadingIsNotDiscrepancy(t *testing.T) {
	h := newHarness(t, tempDBPath(t), nil)
	exp := servingExpectation(t, h)

	observeLocalModel(t, h, runtime.LocalModelStatePayload{
		Loading: true, Managed: true, Reason: "процесс жив, идёт загрузка весов",
	})
	h.clk.Advance(40 * time.Second)
	tick(t, h)

	if got := reload(t, h, exp.ID); got.Status != runtime.ExpectationPending {
		t.Fatalf("во время загрузки статус %q, ожидался pending", got.Status)
	}
	if n := openCount(t, h); n != 0 {
		t.Fatalf("во время загрузки открытых расхождений %d, ожидалось 0", n)
	}
}

func TestLocalModelDownRaisesDiscrepancy(t *testing.T) {
	h := newHarness(t, tempDBPath(t), nil)
	servingExpectation(t, h)

	observeLocalModel(t, h, runtime.LocalModelStatePayload{
		Endpoint: "http://127.0.0.1:18080",
		Reason:   "сервер модели не работает: процесс 4242 не существует",
	})
	h.clk.Advance(40 * time.Second)
	tick(t, h)

	open := mustDiscrepancies(t, h, true)
	if len(open) != 1 {
		t.Fatalf("расхождений %d, ожидалось 1", len(open))
	}
	if open[0].Kind != runtime.KindLocalModelServing {
		t.Fatalf("класс расхождения %q", open[0].Kind)
	}
	if open[0].Observed == "" {
		t.Fatal("расхождение без наблюдаемой стороны не объясняет владельцу, что произошло")
	}

	// Повторная проверка при том же положении дел не плодит расхождений.
	h.clk.Advance(40 * time.Second)
	tick(t, h)
	if n := openCount(t, h); n != 1 {
		t.Fatalf("после повторной проверки открытых расхождений %d, ожидалось 1", n)
	}
}

// Молчание наблюдателя — не то же самое, что исправность: неизвестное
// состояние тоже должно становиться расхождением.
func TestLocalModelSilenceBecomesDiscrepancy(t *testing.T) {
	h := newHarness(t, tempDBPath(t), nil)
	servingExpectation(t, h)

	observeLocalModel(t, h, runtime.LocalModelStatePayload{
		Serving: true, Endpoint: "http://127.0.0.1:18080", Managed: true,
	})
	h.clk.Advance(40 * time.Second)
	tick(t, h)
	if n := openCount(t, h); n != 0 {
		t.Fatalf("сразу после наблюдения открытых расхождений %d, ожидалось 0", n)
	}

	// Наблюдений больше нет — состояние перестало быть известным.
	h.clk.Advance(5 * time.Minute)
	tick(t, h)

	open := mustDiscrepancies(t, h, true)
	if len(open) != 1 {
		t.Fatalf("после молчания расхождений %d, ожидалось 1", len(open))
	}
	if open[0].DedupeKey != "local_model_unobserved:local-model" {
		t.Fatalf("ключ расхождения %q: молчание наблюдателя должно отличаться от отказа модели",
			open[0].DedupeKey)
	}
}
