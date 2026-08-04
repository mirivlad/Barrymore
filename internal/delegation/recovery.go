package delegation

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/runner"
	"github.com/mirivlad/barrymore/internal/runtime"
)

// Идентификаторы локальных реакций.
const (
	// PolicyReattach восстанавливает чтение вывода живого процесса.
	PolicyReattach = "run.reattach"
	// PolicyStopOnCharge останавливает запуск, за который начали списывать.
	PolicyStopOnCharge = "run.stop_on_charge"
)

// RegisterReflexes добавляет локальные реакции на расхождения запусков.
//
// Реакции существуют только как зарегистрированные функции с бюджетом и
// областью действия. Свободная команда из текста модели сюда попасть не может
// (ADR 0004, 06_SECURITY §4).
func (s *Service) RegisterReflexes() error {
	if err := s.rt.Reflexes().Register(&runtime.ReflexPolicy{
		ID:               PolicyStopOnCharge,
		DiscrepancyKinds: []string{runtime.KindRunCostPolicy},
		// Одна попытка: остановить запуск нужно немедленно и один раз.
		MaxAttempts: 1,
		ActionClass: "read",
		EscalateTo:  "user",
		Act:         s.stopOnChargeAction,
	}); err != nil {
		return err
	}
	return s.rt.Reflexes().Register(&runtime.ReflexPolicy{
		ID:               PolicyReattach,
		DiscrepancyKinds: []string{runtime.KindRunSignal},
		// Сценарий Q: две попытки, третья требует решения.
		MaxAttempts: 2,
		Cooldown:    10 * time.Second,
		// Класс действия — чтение: реакция не расширяет права запуска.
		ActionClass: "read",
		EscalateTo:  "user",
		Act:         s.reattachAction,
	})
}

// reattachAction диагностирует тишину и, если процесс жив, восстанавливает чтение.
//
// Порядок важен: сначала наблюдение, потом вывод. Отсутствие вывода само по
// себе не означает зависания (сценарий P).
func (s *Service) reattachAction(ctx context.Context, in runtime.ReflexInput) (runtime.ReflexOutcome, error) {
	runID := in.Discrepancy.SubjectID
	run, err := s.runByID(ctx, runID)
	if err != nil {
		return runtime.ReflexOutcome{}, err
	}

	id := runner.ProcessIdentity{
		UnitName: run.UnitName, PID: run.PID, StartTicks: run.PIDStartTicks,
	}
	liveness := s.runner.Liveness(id)

	// Наблюдение записывается независимо от исхода: оно объясняет решение.
	obs := []runtime.ObservationRequest{{
		Kind:          runtime.ObsProcessLiveness,
		SubjectType:   runtime.SubjectWorkerRun,
		SubjectID:     runID,
		Source:        "reflex:" + PolicyReattach,
		SourceQuality: runtime.QualityDirect,
		Payload:       liveness,
	}}

	if !liveness.Alive {
		// Процесс мёртв. Это не повод для повторного подключения; ситуацией
		// займётся приёмка результата или эскалация.
		return runtime.ReflexOutcome{
			Succeeded:    false,
			Detail:       "процесс не жив: " + liveness.Detail,
			Observations: obs,
		}, nil
	}

	if s.runner.Attached(runID) {
		// Чтение идёт, а сигналов нет. Значит исполнитель действительно молчит:
		// восстанавливать нечего, вопрос выходит за пределы локальной реакции.
		return runtime.ReflexOutcome{
			Succeeded: false,
			Detail: "процесс жив и вывод читается, но новых событий нет: " +
				"локальная реакция здесь бессильна",
			Observations: obs,
		}, nil
	}

	w, err := s.registry.Get(ctx, run.WorkerID)
	if err != nil {
		return runtime.ReflexOutcome{}, err
	}
	adapter, ok := s.registry.Adapter(w.AdapterID)
	if !ok {
		return runtime.ReflexOutcome{}, fmt.Errorf("adapter %q не зарегистрирован", w.AdapterID)
	}

	s.runner.Attach(runID, adapter, filepath.Join(run.RunDir, runner.StdoutFile), run.StdoutOffset)
	obs = append(obs, runtime.ObservationRequest{
		Kind:          runtime.ObsRunAttached,
		SubjectType:   runtime.SubjectWorkerRun,
		SubjectID:     runID,
		Source:        "reflex:" + PolicyReattach,
		SourceQuality: runtime.QualityDirect,
		Payload: map[string]any{
			"resumed_from_offset": run.StdoutOffset,
			"attempt":             in.AttemptNo,
		},
	})
	return runtime.ReflexOutcome{
		Succeeded:    true,
		Detail:       fmt.Sprintf("чтение вывода восстановлено со смещения %d", run.StdoutOffset),
		Resolution:   "процесс жив, чтение вывода восстановлено без вмешательства пользователя",
		Observations: obs,
	}, nil
}

// stopOnChargeAction прекращает запуск, за который начали списывать деньги.
//
// Модель выбиралась как бесплатная. Появившееся списание означает, что
// провайдер изменил условия: работу надо прекратить, а модель — навсегда
// вывести из числа бесплатных, чтобы ошибка не повторилась.
func (s *Service) stopOnChargeAction(ctx context.Context, in runtime.ReflexInput) (runtime.ReflexOutcome, error) {
	runID := in.Discrepancy.SubjectID
	run, err := s.runByID(ctx, runID)
	if err != nil {
		return runtime.ReflexOutcome{}, err
	}
	order, err := s.Get(ctx, run.WorkOrderID)
	if err != nil {
		return runtime.ReflexOutcome{}, err
	}

	cost := observedRunCost(ctx, s, runID)
	if order.Model != "" {
		if err := s.registry.MarkModelCharged(ctx, order.WorkerID, order.Model, cost,
			s.clock.Now()); err != nil {
			s.log.Error("модель не помечена платной",
				"worker", order.WorkerID, "model", order.Model, "error", err)
		}
	}

	id := runner.ProcessIdentity{
		UnitName: run.UnitName, PID: run.PID, StartTicks: run.PIDStartTicks,
	}
	stopErr := s.runner.Cancel(ctx, runID, id, false)
	s.runner.Detach(runID)

	detail := fmt.Sprintf(
		"запуск остановлен: модель %s списала %.6f, хотя выбиралась как бесплатная",
		order.Model, cost)
	if stopErr != nil {
		detail += "; мягкая остановка не удалась: " + stopErr.Error()
	}

	if err := s.setState(ctx, order.ID, StateFailed, detail,
		event.Actor{Type: event.ActorRuntime}); err != nil {
		return runtime.ReflexOutcome{}, err
	}

	return runtime.ReflexOutcome{
		// Успех здесь означает «нарушение остановлено», а не «работа удалась».
		Succeeded:  true,
		Detail:     detail,
		Resolution: "запуск прекращён, модель выведена из числа бесплатных",
		Observations: []runtime.ObservationRequest{{
			Kind:          runtime.ObsProcessLiveness,
			SubjectType:   runtime.SubjectWorkerRun,
			SubjectID:     runID,
			Source:        "reflex:" + PolicyStopOnCharge,
			SourceQuality: runtime.QualityDirect,
			Payload: map[string]any{
				"action": "stop_on_charge", "model": order.Model, "cost": cost,
			},
		}},
	}, nil
}

// observedRunCost суммирует списания, о которых сообщил исполнитель.
func observedRunCost(ctx context.Context, s *Service, runID string) float64 {
	obs, err := s.rt.Observations(ctx, runtime.SubjectWorkerRun, runID, 500)
	if err != nil {
		return 0
	}
	var total float64
	for _, o := range obs {
		var ev struct {
			Detail map[string]any `json:"detail"`
		}
		if err := o.Decode(&ev); err != nil {
			continue
		}
		if c, ok := ev.Detail["observed_cost"].(float64); ok {
			total += c
		}
	}
	return total
}

// ReconcileResult — итог сверки после рестарта.
type ReconcileResult struct {
	Checked   int      `json:"checked"`
	Resumed   int      `json:"resumed"`
	Orphaned  int      `json:"orphaned"`
	Finalized int      `json:"finalized"`
	Notes     []string `json:"notes"`
}

// Reconcile сверяет незавершённые запуски с реальными процессами.
//
// Сценарий H: после рестарта запуск либо продолжается, либо честно помечается
// потерянным. Поручение не объявляется выполненным ни в том, ни в другом случае.
func (s *Service) Reconcile(ctx context.Context) (ReconcileResult, error) {
	var res ReconcileResult

	runs, err := s.ActiveRuns(ctx)
	if err != nil {
		return res, err
	}
	for _, run := range runs {
		res.Checked++
		id := runner.ProcessIdentity{
			UnitName: run.UnitName, PID: run.PID, StartTicks: run.PIDStartTicks,
		}
		runner.RememberIdentity(run.ID, id)
		liveness := s.runner.Liveness(id)

		if _, err := s.rt.RecordObservation(ctx, runtime.ObservationRequest{
			Kind:          runtime.ObsProcessLiveness,
			SubjectType:   runtime.SubjectWorkerRun,
			SubjectID:     run.ID,
			Source:        "recovery",
			SourceQuality: runtime.QualityDirect,
			Payload:       liveness,
		}); err != nil {
			return res, err
		}

		if liveness.Alive {
			w, err := s.registry.Get(ctx, run.WorkerID)
			if err != nil {
				return res, err
			}
			adapter, ok := s.registry.Adapter(w.AdapterID)
			if !ok {
				res.Notes = append(res.Notes, fmt.Sprintf(
					"запуск %s жив, но adapter %q не зарегистрирован: чтение вывода не восстановлено",
					run.ID, w.AdapterID))
				continue
			}
			s.runner.Attach(run.ID, adapter,
				filepath.Join(run.RunDir, runner.StdoutFile), run.StdoutOffset)
			res.Resumed++
			res.Notes = append(res.Notes, fmt.Sprintf(
				"запуск %s продолжается, чтение вывода возобновлено со смещения %d",
				run.ID, run.StdoutOffset))
			continue
		}

		// Процесс не пережил рестарт. Результат не выбрасывается: логи и
		// каталог запуска на месте, приёмка выполняется по тому, что осталось.
		p := runExitPayload{
			RunID: run.ID, OrderID: run.WorkOrderID, Status: RunOrphaned,
			ExitCode: -1, ExitedAt: s.clock.Now(),
			Error: "процесс не найден после перезапуска Бэрримора: " + liveness.Detail,
		}
		if _, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
			if _, err := w.Append(ctx, event.Request{
				StreamType: StreamType, StreamID: run.WorkOrderID,
				ExpectedRevision: event.AnyRevision,
				EventType:        EvRunOrphaned,
				Actor:            event.Actor{Type: event.ActorRuntime},
				Payload:          p,
			}); err != nil {
				return err
			}
			return applyRunExit(ctx, tx, p)
		}); err != nil {
			return res, err
		}
		res.Orphaned++
		res.Notes = append(res.Notes, fmt.Sprintf(
			"запуск %s потерян: %s", run.ID, liveness.Detail))

		if err := s.Finalize(ctx, run.WorkOrderID, run.ID); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"приёмка результата поручения %s не выполнена: %v", run.WorkOrderID, err))
			continue
		}
		res.Finalized++
	}
	return res, nil
}
