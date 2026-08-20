package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/experience"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/research"
)

// maxResearchSteps is deliberately small. Research is iterative, not
// unbounded: a weak model cannot keep Barrymore probing forever.
const maxResearchSteps = 3

type deliberationResult struct {
	Proposal  Proposal
	Response  model.Response
	EpisodeID string
	Trace     []string
}

// deliberate runs the model until it either has enough evidence for a final
// answer or exhausts a small read-only research budget. Intermediate model
// replies are never shown or persisted as conversation messages.
func (s *Service) deliberate(ctx context.Context, conv Conversation, question string,
	base []ContextSection, history []model.Message) (deliberationResult, error) {

	sections := append([]ContextSection(nil), base...)
	var aggregate model.Response
	var episode *experience.Episode
	var steps []experience.Step
	seen := map[string]bool{}
	successes, failures := 0, 0
	var trace []string

	for call := 0; ; call++ {
		// After maxResearchSteps observations there is one last model call, but
		// no more tools. It must answer from the evidence it already has.
		forceFinal := call >= maxResearchSteps
		callSections := sections
		if forceFinal {
			callSections = append(append([]ContextSection(nil), sections...), ContextSection{
				Title: "Лимит исследования",
				Body: "Новых исследовательских шагов на этом ходу больше не будет. " +
					"Поставь research.capability_id пустым и дай честный финальный ответ " +
					"по уже полученным evidence; если их недостаточно — прямо скажи, чего не удалось установить.\n",
			})
		}

		resp, proposal, err := s.completeProposal(ctx, callSections, history)
		aggregateResponse(&aggregate, resp)
		if err != nil {
			return deliberationResult{}, err
		}

		capID := strings.TrimSpace(proposal.Research.CapabilityID)
		if capID == "" || forceFinal {
			if forceFinal && capID != "" {
				// The runtime owns the loop boundary. A model cannot overrun it by
				// asking again. Its textual reply may honestly say evidence is lacking.
				proposal.Research = ResearchProposal{Args: json.RawMessage(`{}`)}
				trace = append(trace, "research: дальнейший шаг отклонён лимитом")
			}
			if strings.TrimSpace(proposal.Reply) == "" {
				return deliberationResult{}, fmt.Errorf("финальный ответ модели пуст")
			}
			episodeID, err := s.finishResearchEpisode(ctx, episode, question, proposal.Reply,
				steps, successes, failures, aggregate.Model)
			if err != nil {
				return deliberationResult{}, err
			}
			return deliberationResult{
				Proposal: proposal, Response: aggregate, EpisodeID: episodeID, Trace: trace,
			}, nil
		}

		if s.research == nil {
			return deliberationResult{}, fmt.Errorf("модель запросила исследование %q, но research runtime не настроен", capID)
		}
		if episode == nil {
			initial, _ := json.Marshal(map[string]any{"question": question})
			ep, err := s.experience.Begin(ctx, experience.StartRequest{
				Goal: question, ThreadID: conv.ThreadID, ConversationID: conv.ID,
				InitialContext: initial,
			}, event.Actor{Type: event.ActorBarrymore})
			if err != nil {
				return deliberationResult{}, fmt.Errorf("начало исследовательского эпизода: %w", err)
			}
			episode = &ep
			trace = append(trace, "research episode "+ep.ID+" начат")
		}

		args := proposal.Research.Args
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		key := capID + "\x00" + string(args)
		step := experience.Step{
			Capability: capID, Purpose: strings.TrimSpace(proposal.Research.Why), Args: args,
		}
		steps = append(steps, step)

		if seen[key] {
			failures++
			msg := "Повтор того же исследовательского шага отклонён: " + capID
			sections = append(sections, researchFailureSection(capID, msg))
			_ = s.recordResearchFailure(ctx, episode.ID, capID, msg)
			trace = append(trace, "research "+capID+": повтор отклонён")
			continue
		}
		seen[key] = true

		res, err := s.research.Execute(ctx, research.Request{
			CapabilityID: capID, Args: args, Why: proposal.Research.Why,
		})
		if err != nil {
			failures++
			msg := err.Error()
			sections = append(sections, researchFailureSection(capID, msg))
			_ = s.recordResearchFailure(ctx, episode.ID, capID, msg)
			trace = append(trace, "research "+capID+": ошибка")
			continue
		}

		successes++
		if _, err := s.experience.AddSource(ctx, episode.ID, experience.Source{
			Kind: capID, Locator: res.Locator, Title: res.Title, Evidence: res.Evidence,
			Confidence: res.Confidence, Stability: res.Stability, ObservedAt: res.ObservedAt,
		}, event.Actor{Type: event.ActorRuntime}); err != nil {
			return deliberationResult{}, fmt.Errorf("запись evidence исследования: %w", err)
		}
		sections = append(sections, researchResultSection(res))
		trace = append(trace, fmt.Sprintf("research %s: evidence, stability=%s", capID, res.Stability))
	}
}

func (s *Service) completeProposal(ctx context.Context, sections []ContextSection,
	history []model.Message) (model.Response, Proposal, error) {

	req := model.Request{
		System:          s.identity.SystemPrompt(sections, s.clock.Now()),
		Messages:        history,
		Schema:          ResponseSchema(),
		SchemaName:      "barrymore_reply",
		MaxTokens:       s.maxTokens,
		Temperature:     0.3,
		DisableThinking: true,
	}
	resp, err := s.provider.Complete(ctx, req)
	if err != nil {
		return model.Response{}, Proposal{}, fmt.Errorf("модель не ответила: %w", err)
	}
	proposal, err := parseProposal(resp.Content)
	if err != nil {
		return resp, Proposal{}, fmt.Errorf("ответ модели не соответствует контракту: %w", err)
	}
	return resp, proposal, nil
}

func aggregateResponse(dst *model.Response, src model.Response) {
	dst.Content = src.Content
	dst.Model = src.Model
	dst.FinishReason = src.FinishReason
	dst.PromptTokens += src.PromptTokens
	dst.CompletionTokens += src.CompletionTokens
	dst.Latency += src.Latency
}

func researchResultSection(res research.Result) ContextSection {
	var b strings.Builder
	b.WriteString("Capability: " + res.CapabilityID + "\n")
	b.WriteString("Наблюдалось: " + res.ObservedAt.Format("2006-01-02 15:04:05Z07:00") + "\n")
	b.WriteString("Свежесть: " + res.Stability + "\n")
	b.WriteString(fmt.Sprintf("Confidence: %.2f\n", res.Confidence))
	b.WriteString("Evidence: " + res.Evidence + "\n")
	if len(res.Data) > 0 && string(res.Data) != "null" {
		b.WriteString("Structured data: " + string(res.Data) + "\n")
	}
	return ContextSection{Title: "Новое evidence исследования", Body: b.String()}
}

func researchFailureSection(capability, why string) ContextSection {
	return ContextSection{
		Title: "Неудавшийся исследовательский шаг",
		Body: "Capability: " + capability + "\nОшибка: " + why +
			"\nЭто не evidence о состоянии предмета; выбери другой способ или честно скажи, что установить не удалось.\n",
	}
}

func (s *Service) recordResearchFailure(ctx context.Context, episodeID, capability, why string) error {
	if s.experience == nil || episodeID == "" {
		return nil
	}
	_, err := s.experience.AddSource(ctx, episodeID, experience.Source{
		Kind: "capability_error", Title: capability, Evidence: why,
		Confidence: 1, Stability: experience.StabilityRealtime, ObservedAt: s.clock.Now(),
	}, event.Actor{Type: event.ActorRuntime})
	return err
}

func (s *Service) finishResearchEpisode(ctx context.Context, ep *experience.Episode, question, answer string,
	steps []experience.Step, successes, failures int, modelName string) (string, error) {
	if ep == nil {
		return "", nil
	}
	outcome := experience.OutcomeUnknown
	switch {
	case successes > 0 && failures == 0:
		outcome = experience.OutcomeSuccess
	case successes > 0 && failures > 0:
		outcome = experience.OutcomePartial
	case failures > 0:
		outcome = experience.OutcomeFailure
	}
	verification, _ := json.Marshal(map[string]any{
		"research_steps": len(steps), "successful_steps": successes,
		"failed_steps": failures, "answer_model": modelName,
	})
	if _, err := s.experience.Complete(ctx, ep.ID, experience.CompleteRequest{
		Outcome: outcome, Result: answer, Verification: verification,
	}, event.Actor{Type: event.ActorBarrymore}); err != nil {
		return "", fmt.Errorf("завершение исследовательского эпизода: %w", err)
	}

	// A clean successful route becomes procedural memory. Re-running a route is
	// more valuable for volatile state than caching yesterday's answer.
	if successes > 0 && failures == 0 && len(steps) > 0 {
		ids := make([]string, 0, len(steps))
		seenCaps := map[string]bool{}
		for _, st := range steps {
			if !seenCaps[st.Capability] {
				ids = append(ids, st.Capability)
				seenCaps[st.Capability] = true
			}
		}
		intent := "research:" + strings.Join(ids, "+")
		proc := experience.Procedure{
			Intent: intent, Title: "Повторить исследование: " + strings.TrimSpace(question),
			SourceEpisodeID: ep.ID, Steps: steps, RequiredCapabilities: ids,
			ExpectedResult: "актуальное evidence для ответа владельцу",
			Verification: []string{"получить новое evidence, не использовать старый volatile/realtime result"},
			RiskClass: experience.RiskReadOnly, Status: experience.ProcedureActive,
		}
		if old, err := s.experience.ProceduresByIntent(ctx, intent, 1); err == nil && len(old) > 0 {
			proc.ID = old[0].ID
			proc.Title = old[0].Title
		}
		if _, err := s.experience.SaveProcedure(ctx, proc,
			event.Actor{Type: event.ActorBarrymore}); err != nil {
			s.log.Error("процедура исследования не сохранена", "episode", ep.ID, "error", err)
		}
	}
	return ep.ID, nil
}
