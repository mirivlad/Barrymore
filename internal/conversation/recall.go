package conversation

import (
	"context"
	"fmt"
	"strings"

	"github.com/mirivlad/barrymore/internal/experience"
)

// recallContext retrieves experience for this question before deliberation.
// It returns candidates, not truth: freshness and feedback stay visible so the
// model can decide whether to reuse a stable fact or repeat a procedure.
func (s *Service) recallContext(ctx context.Context, question string) ([]ContextSection, []string, error) {
	var sections []ContextSection
	var trace []string

	if s.memory != nil {
		items, err := s.memory.Recall(ctx, question, 12)
		if err != nil {
			return nil, nil, err
		}
		if len(items) > 0 {
			var b strings.Builder
			b.WriteString("Это кандидаты из памяти по текущему вопросу, а не автоматически истинный ответ.\n")
			for _, it := range items {
				fresh := it.Stability
				if fresh == "" {
					fresh = "stable"
				}
				b.WriteString(fmt.Sprintf("- [%s; %s; confidence=%.2f] %s",
					it.Type, fresh, it.Confidence, it.Content))
				if it.VerifiedAt != nil {
					b.WriteString("; проверено: " + it.VerifiedAt.Format("2006-01-02 15:04:05Z07:00"))
				}
				b.WriteString("\n")
			}
			b.WriteString("`volatile` и `realtime` нельзя выдавать за текущее состояние без новой проверки.\n")
			sections = append(sections, ContextSection{
				Title: "Что вспомнилось по этому вопросу", Body: b.String(),
			})
			trace = append(trace, fmt.Sprintf("query recall памяти: %d", len(items)))
		} else {
			trace = append(trace, "query recall памяти: совпадений нет")
		}
	}

	if s.experience != nil {
		recalled, err := s.experience.Recall(ctx, question, 8)
		if err != nil {
			return nil, nil, err
		}
		if len(recalled.Episodes) > 0 || len(recalled.Procedures) > 0 {
			var b strings.Builder
			b.WriteString("Старый Episode показывает, что было тогда. Procedure показывает, как снова получить ответ или результат.\n")
			for _, e := range recalled.Episodes {
				ep := e.Episode
				b.WriteString(fmt.Sprintf("- Episode %s: %s; outcome=%s", ep.ID, ep.Goal, ep.Outcome))
				if ep.Result != "" {
					b.WriteString("; тогда результат: " + ep.Result)
				}
				likes, dislikes := feedbackCounts(e.Feedback)
				if likes > 0 || dislikes > 0 {
					b.WriteString(fmt.Sprintf("; feedback +%d/-%d", likes, dislikes))
				}
				b.WriteString("\n")
				for _, src := range e.Sources {
					b.WriteString("  evidence: " + src.Evidence + "\n")
				}
			}
			for _, p := range recalled.Procedures {
				proc := p.Procedure
				b.WriteString(fmt.Sprintf("- Procedure %s: %s; risk=%s; success=%d failure=%d\n",
					proc.ID, proc.Title, proc.RiskClass, proc.Succeeded, proc.Failed))
				for i, step := range proc.Steps {
					b.WriteString(fmt.Sprintf("  %d. %s", i+1, step.Capability))
					if step.Purpose != "" {
						b.WriteString(" — " + step.Purpose)
					}
					b.WriteString("\n")
				}
			}
			b.WriteString("Не повторяй старый RESULT, если состояние могло измениться; повтори подходящую Procedure и получи новое evidence.\n")
			sections = append(sections, ContextSection{Title: "Похожий прошлый опыт", Body: b.String()})
		}
		trace = append(trace, fmt.Sprintf("query recall опыта: episodes=%d procedures=%d",
			len(recalled.Episodes), len(recalled.Procedures)))
	}

	return sections, trace, nil
}

func feedbackCounts(items []experience.Feedback) (likes, dislikes int) {
	for _, fb := range items {
		switch fb.Value {
		case experience.FeedbackLike:
			likes++
		case experience.FeedbackDislike:
			dislikes++
		}
	}
	return likes, dislikes
}
