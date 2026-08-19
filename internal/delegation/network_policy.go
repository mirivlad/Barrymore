package delegation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/runner"
)

// StopForNetworkPolicyChange останавливает весь работающий внешний персонал
// перед изменением глобального сетевого маршрута.
//
// Сетевая настройка у Бэрримора одна на весь персонал. Поэтому после её смены
// не должно существовать смеси «этот worker ещё на старом proxy, этот уже на
// новом, а этот напрямую». Сначала все текущие поручения прекращаются, и только
// после подтверждённой смерти процессов вызывающий код имеет право менять
// policy.
func (s *Service) StopForNetworkPolicyChange(ctx context.Context, reason string) (int, error) {
	runs, err := s.ActiveRuns(ctx)
	if err != nil {
		return 0, err
	}
	if len(runs) == 0 {
		return 0, nil
	}
	if reason == "" {
		reason = "изменена сетевая политика персонала"
	}

	// Cancel работает на уровне поручения и сам останавливает все его активные
	// запуски. Дедупликация нужна на случай, если у поручения в проекции по
	// какой-то причине оказалось больше одного незавершённого запуска.
	orders := make(map[string]struct{}, len(runs))
	var errs []error
	for _, run := range runs {
		if _, seen := orders[run.WorkOrderID]; seen {
			continue
		}
		orders[run.WorkOrderID] = struct{}{}
		if err := s.Cancel(ctx, run.WorkOrderID, reason,
			event.Actor{Type: event.ActorRuntime}); err != nil {
			errs = append(errs, fmt.Errorf("поручение %s не остановлено мягко: %w", run.WorkOrderID, err))
		}
	}

	// Большинство CLI завершается от мягкого сигнала сразу. Даём им короткое
	// окно для штатной уборки, затем добиваем оставшиеся процессы: сетевую
	// политику нельзя считать изменённой, пока старый маршрут ещё используется.
	graceUntil := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(graceUntil) {
		alive := false
		for _, run := range runs {
			id := runner.ProcessIdentity{
				UnitName: run.UnitName, PID: run.PID, StartTicks: run.PIDStartTicks,
			}
			if s.runner.Alive(id) {
				alive = true
				break
			}
		}
		if !alive {
			break
		}
		select {
		case <-ctx.Done():
			return len(runs), ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}

	for _, run := range runs {
		id := runner.ProcessIdentity{
			UnitName: run.UnitName, PID: run.PID, StartTicks: run.PIDStartTicks,
		}
		if !s.runner.Alive(id) {
			continue
		}
		if err := s.runner.Cancel(ctx, run.ID, id, true); err != nil {
			errs = append(errs, fmt.Errorf("запуск %s не остановлен жёстко: %w", run.ID, err))
		}
		s.runner.Detach(run.ID)
	}

	// SIGKILL тоже не считается доказательством сам по себе: перед переключением
	// маршрута проверяем наблюдаемый факт смерти процесса.
	deadline := time.Now().Add(2 * time.Second)
	for {
		remaining := []string{}
		for _, run := range runs {
			id := runner.ProcessIdentity{
				UnitName: run.UnitName, PID: run.PID, StartTicks: run.PIDStartTicks,
			}
			if s.runner.Alive(id) {
				remaining = append(remaining, run.ID)
			}
		}
		if len(remaining) == 0 {
			break
		}
		if time.Now().After(deadline) {
			errs = append(errs, fmt.Errorf(
				"сетевая политика не изменена: после остановки всё ещё живы запуски %v", remaining))
			break
		}
		select {
		case <-ctx.Done():
			return len(runs), errors.Join(append(errs, ctx.Err())...)
		case <-time.After(50 * time.Millisecond):
		}
	}

	return len(runs), errors.Join(errs...)
}
