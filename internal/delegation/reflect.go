package delegation

import (
	"context"
	"fmt"
	"strings"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/thread"
)

// reflectStart отмечает в нити, что работа началась.
//
// «Ждём результата» — не догадка о будущем, а состояние дел: поручение
// запущено, и до его конца нить действительно ждёт.
func (s *Service) reflectStart(ctx context.Context, order WorkOrder) {
	waiting := []string{fmt.Sprintf("результата поручения «%s»", order.Title)}
	s.patchThread(ctx, order, thread.CanonPatch{
		Situation: strp(fmt.Sprintf("поручение «%s» выполняется", order.Title)),
		Waiting:   &waiting,
	}, "поручение запущено")
}

// reflectFinish переносит итог поручения в нить.
//
// Только подтверждённые факты: состояние поручения, число пройденных проверок,
// число изменённых файлов. Ни слова из отчёта исполнителя — «готово» в отчёте
// не является доказательством готовности (00_PRODUCT_VISION §9.7), и переносить
// его в каноническое состояние нити значило бы отмывать непроверенное
// утверждение до факта.
func (s *Service) reflectFinish(ctx context.Context, order WorkOrder, checks []Verification) {
	passed, total := 0, 0
	var failures []string
	for _, c := range checks {
		if c.Status == VerifySkipped {
			continue
		}
		total++
		if c.Status == VerifyPassed {
			passed++
		} else {
			failures = append(failures, c.Name)
		}
	}

	patch := thread.CanonPatch{}
	empty := []string{}
	patch.Waiting = &empty

	switch order.State {
	case StateCompleted:
		patch.Situation = strp(fmt.Sprintf(
			"поручение «%s» выполнено: проверок пройдено %d из %d", order.Title, passed, total))
		switch {
		case order.ChangeState == ChangeCollected:
			patch.NextStep = strp(fmt.Sprintf(
				"решить, применять ли изменения (файлов: %d)", len(order.ChangeSummary.Files)))
			waiting := []string{"вашего решения по изменениям"}
			patch.Waiting = &waiting
		default:
			patch.NextStep = strp("прочитать отчёт исполнителя")
		}
	case StateFailed:
		patch.Situation = strp(fmt.Sprintf(
			"поручение «%s» не принято: проверок пройдено %d из %d", order.Title, passed, total))
		patch.NextStep = strp("разобраться, почему работа не прошла приёмку")
		if len(failures) > 0 {
			obstacles := []string{"не пройдено: " + strings.Join(failures, ", ")}
			patch.Obstacles = &obstacles
		}
	default:
		return
	}
	s.patchThread(ctx, order, patch, "по итогам поручения")
}

// patchThread записывает изменение состояния нити от имени runtime.
//
// Неудача записи не отменяет поручение: результат уже получен и проверен,
// и терять его из-за проблемы с нитью нельзя.
func (s *Service) patchThread(ctx context.Context, order WorkOrder,
	patch thread.CanonPatch, reason string) {

	if order.ThreadID == "" {
		return
	}
	if _, err := s.threads.SetCanon(ctx, order.ThreadID, patch,
		thread.CanonFromOrder, reason, event.Actor{Type: event.ActorRuntime}); err != nil {
		s.log.Error("состояние нити не обновлено",
			"thread", order.ThreadID, "order", order.ID, "error", err)
	}
}

func strp(v string) *string { return &v }
