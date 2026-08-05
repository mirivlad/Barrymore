package localmodel

import (
	"context"
	"strconv"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/runtime"
)

// RegisterReflexes добавляет реакцию на пропажу локальной модели.
//
// Реакция ограничена: три попытки с выдержкой, дальше вопрос уходит владельцу.
// Бесконечный перезапуск скрывал бы настоящую причину — нехватку памяти,
// испорченный файл весов, занятый порт.
func (s *Supervisor) RegisterReflexes() error {
	return s.rt.Reflexes().Register(&runtime.ReflexPolicy{
		ID:               PolicyRestart,
		DiscrepancyKinds: []string{runtime.KindLocalModelServing},
		MaxAttempts:      3,
		Cooldown:         30 * time.Second,
		ActionClass:      ActionClass,
		EscalateTo:       "user",
		Act:              s.restartAction,
	})
}

// restartAction поднимает сервер модели заново.
//
// Успехом считается не «команда выполнилась», а наблюдаемая готовность:
// запущенный процесс, который не отвечает, — это не решённая задача.
func (s *Supervisor) restartAction(ctx context.Context, in runtime.ReflexInput) (runtime.ReflexOutcome, error) {
	if !s.Enabled() {
		return runtime.ReflexOutcome{
			Succeeded: false,
			Detail: "поднять модель нечем: " + s.initialReason() +
				"; это решается вне локальной реакции",
		}, nil
	}

	st, err := s.Ensure(ctx)
	obs := []runtime.ObservationRequest{{
		Kind:          runtime.ObsLocalModel,
		SubjectType:   runtime.SubjectProvider,
		SubjectID:     SubjectID,
		ObservedAt:    st.ObservedAt,
		Source:        "reflex:" + PolicyRestart,
		SourceQuality: runtime.QualityDirect,
		Confidence:    1,
		Payload: runtime.LocalModelStatePayload{
			Serving: st.Serving, Loading: st.Loading, Endpoint: st.Endpoint,
			Reason: st.Reason, Managed: st.Managed,
		},
	}}

	if err != nil {
		return runtime.ReflexOutcome{
			Succeeded:    false,
			Detail:       "попытка " + strconv.Itoa(in.AttemptNo) + ": " + err.Error(),
			Observations: obs,
		}, nil
	}
	if !st.Serving {
		return runtime.ReflexOutcome{
			Succeeded:    false,
			Detail:       "модель всё ещё не отвечает: " + st.Reason,
			Observations: obs,
		}, nil
	}
	return runtime.ReflexOutcome{
		Succeeded:    true,
		Detail:       "локальная модель поднята и отвечает на " + st.Endpoint,
		Resolution:   "сервер модели перезапущен локальной реакцией, вмешательство не потребовалось",
		Observations: obs,
	}, nil
}

// EnsureExpectation создаёт стоячее ожидание, если открытого ещё нет.
//
// Ожидание не пересоздаётся при каждом запуске: иначе перезапуск Бэрримора
// плодил бы дубликаты, и одно и то же расхождение считалось бы несколько раз.
func (s *Supervisor) EnsureExpectation(ctx context.Context) error {
	spec := s.Spec()
	if !spec.Configured() || s.rt == nil {
		return nil
	}
	existing, err := s.rt.Expectations(ctx, runtime.SubjectProvider, SubjectID)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e.Kind == runtime.KindLocalModelServing && e.Status == runtime.ExpectationPending {
			return nil
		}
	}

	basis := "разговорный слой настроен на локальную модель на " + spec.Endpoint()
	if !s.Enabled() {
		// Ожидание нужно и тогда, когда Бэрримор не может поднять сервер сам:
		// он всё равно обязан заметить пропажу и сказать о ней.
		basis += "; поднять её Бэрримор не может (" + s.initialReason() + ")"
	}
	reaction := PolicyRestart
	if !s.Enabled() {
		reaction = ""
	}

	_, err = s.rt.CreateExpectation(ctx, runtime.ExpectationRequest{
		SubjectType: runtime.SubjectProvider,
		SubjectID:   SubjectID,
		Kind:        runtime.KindLocalModelServing,
		Params: runtime.ParamsLocalModelServing{
			Endpoint:   spec.Endpoint(),
			CheckEvery: spec.ObserveEvery * 2,
			// Молчание наблюдателя дольше десяти интервалов означает, что
			// состояние неизвестно. Это не то же самое, что исправность.
			SilenceAfter: spec.ObserveEvery * 10,
		},
		Basis:            basis,
		Confidence:       0.9,
		SeverityIfMissed: runtime.SeverityWarning,
		CheckInterval:    spec.ObserveEvery * 2,
		FirstCheckAfter:  spec.ObserveEvery,
		ReactionPolicy:   reaction,
		Actor:            event.Actor{Type: event.ActorRuntime},
	})
	return err
}
