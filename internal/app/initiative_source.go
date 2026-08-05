package app

import (
	"context"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/delegation"
	"github.com/mirivlad/barrymore/internal/initiative"
	"github.com/mirivlad/barrymore/internal/runtime"
)

// workSource поставляет поводы обратиться из того, что Бэрримор и так наблюдает.
//
// Каждый повод — свершившийся факт: поручение закончилось, изменения ждут,
// реакция не справилась. Догадок вроде «вы давно этим не занимались» здесь
// нет и быть не может: это не сведение о мире, а давление на человека
// (07_USER_EXPERIENCE §4).
type workSource struct{ app *App }

// Candidates перечисляет поводы на текущий момент.
func (s workSource) Candidates(ctx context.Context, now time.Time) ([]initiative.Candidate, error) {
	var out []initiative.Candidate

	orders, err := s.app.Delegation.List(ctx, "", 100)
	if err != nil {
		return nil, err
	}
	for _, o := range orders {
		out = append(out, s.fromOrder(o, now)...)
	}

	// Расхождения, с которыми Бэрримор не справился сам. Это единственный
	// повод, который проходит и в тихие часы: молчать о собственной неудаче
	// хуже, чем разбудить.
	disc, err := s.app.Runtime.Discrepancies(ctx, true, 50)
	if err != nil {
		return nil, err
	}
	for _, d := range disc {
		if d.Status != runtime.DiscrepancyEscalated {
			continue
		}
		out = append(out, initiative.Candidate{
			Kind:        initiative.KindEscalated,
			SubjectType: d.SubjectType, SubjectID: d.SubjectID,
			Level: initiative.LevelUrgent,
			Title: "Не справился сам: " + d.Kind,
			Why: fmt.Sprintf(
				"ожидалось: %s; наблюдалось: %s. Локальные попытки исчерпаны, "+
					"дальше нужно ваше решение", d.Expected, d.Observed),
			DedupeKey:  "escalated:" + d.ID,
			ObservedAt: d.LastSeen,
		})
	}

	// Кандидаты в память, которые Бэрримор не стал записывать сам.
	cands, err := s.app.Memory.Candidates(ctx, true, 50)
	if err != nil {
		return nil, err
	}
	if len(cands) > 0 {
		// Одно обращение на всю пачку: пять уведомлений о пяти кандидатах —
		// уже назойливость. Ключ повтора включает число, поэтому о новых
		// кандидатах Бэрримор напомнит, а об одних и тех же — нет.
		out = append(out, initiative.Candidate{
			Kind:        initiative.KindMemoryWaiting,
			SubjectType: "memory",
			Level:       initiative.LevelRoutine,
			Title:       fmt.Sprintf("Память ждёт решения: %d сведений", len(cands)),
			Why: "я не стал записывать это сам — либо не уверен, либо счёл " +
				"чувствительным",
			DedupeKey:  fmt.Sprintf("memory_waiting:%d", len(cands)),
			ObservedAt: now,
		})
	}
	return out, nil
}

// fromOrder выводит поводы из состояния поручения.
func (s workSource) fromOrder(o delegation.WorkOrder, now time.Time) []initiative.Candidate {
	var out []initiative.Candidate

	switch o.State {
	case delegation.StateCompleted, delegation.StateFailed:
		if o.FinishedAt != nil {
			level := initiative.LevelAttention
			title := "Поручение выполнено: " + o.Title
			why := "исполнитель закончил, все проверки пройдены — результат готов"
			if o.State == delegation.StateFailed {
				title = "Поручение не вышло: " + o.Title
				why = "не пройдены проверки приёмки: " + o.FailureReason
			}
			out = append(out, initiative.Candidate{
				Kind:        initiative.KindOrderFinished,
				SubjectType: "work_order", SubjectID: o.ID,
				Level: level, Title: title, Why: why,
				DedupeKey:  "order_finished:" + o.ID,
				ObservedAt: *o.FinishedAt,
			})
		}
	case delegation.StateProposed:
		// Подтверждение стоит между владельцем и работой, которую он сам
		// заказал. Через час это уже не «он думает», а «похоже, забыл».
		patience := s.app.Initiative.Policy().ApprovalPatience
		if patience > 0 && now.Sub(o.CreatedAt) > patience {
			out = append(out, initiative.Candidate{
				Kind:        initiative.KindApprovalWaiting,
				SubjectType: "work_order", SubjectID: o.ID,
				Level: initiative.LevelRoutine,
				Title: "Ждёт вашего подтверждения: " + o.Title,
				Why: fmt.Sprintf("поручение сформировано %s назад и до сих пор "+
					"не запущено — без вашего разрешения я его не начну",
					now.Sub(o.CreatedAt).Round(time.Minute)),
				DedupeKey:  "approval_waiting:" + o.ID,
				ObservedAt: o.CreatedAt,
			})
		}
	}

	if o.ChangeState == delegation.ChangeCollected {
		out = append(out, initiative.Candidate{
			Kind:        initiative.KindChangesWaiting,
			SubjectType: "work_order", SubjectID: o.ID,
			Level: initiative.LevelAttention,
			Title: fmt.Sprintf("Изменения ждут решения: %s", o.Title),
			Why: fmt.Sprintf(
				"исполнитель изменил %d файлов в копии; ваш каталог не тронут, "+
					"и без вашего решения ничего в него не попадёт",
				len(o.ChangeSummary.Files)),
			DedupeKey:  "changes_waiting:" + o.ID,
			ObservedAt: o.UpdatedAt,
		})
	}
	return out
}
