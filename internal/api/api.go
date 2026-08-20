// Package api отдаёт versioned HTTP API и поток событий.
//
// 08_API_AND_EVENTS: /api/v1, JSON, problem details, оптимистичная конкурентность
// через ревизию, SSE. ADR 0010: поток возобновляется по Last-Event-ID.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/app"
	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/delegation"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/initiative"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/thread"
	"github.com/mirivlad/barrymore/internal/worker"
)

// Server — HTTP-интерфейс Бэрримора.
type Server struct {
	app *app.App
	log *slog.Logger
}

// NewServer создаёт сервер.
func NewServer(a *app.App) *Server {
	return &Server{app: a, log: a.Log}
}

// Handler собирает маршруты.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)

	mux.HandleFunc("GET /api/v1/system/state", s.systemState)
	mux.HandleFunc("GET /api/v1/stream", s.stream)

	mux.HandleFunc("GET /api/v1/threads", s.listThreads)
	mux.HandleFunc("POST /api/v1/threads", s.createThread)
	mux.HandleFunc("GET /api/v1/threads/{id}", s.getThread)
	mux.HandleFunc("PATCH /api/v1/threads/{id}", s.patchThread)
	mux.HandleFunc("POST /api/v1/threads/{id}/positions", s.addPosition)
	mux.HandleFunc("POST /api/v1/threads/{id}/decisions", s.addDecision)
	mux.HandleFunc("POST /api/v1/threads/{id}/questions", s.addQuestion)
	mux.HandleFunc("GET /api/v1/threads/{id}/timeline", s.threadTimeline)
	mux.HandleFunc("POST /api/v1/threads/{id}/canon", s.setCanon)
	mux.HandleFunc("POST /api/v1/threads/{id}/canon/undo", s.undoCanon)

	mux.HandleFunc("GET /api/v1/workers", s.listWorkers)
	mux.HandleFunc("POST /api/v1/workers/discover", s.discoverWorkers)
	mux.HandleFunc("POST /api/v1/workers/{id}/probe", s.probeWorker)
	mux.HandleFunc("POST /api/v1/workers/{id}/models/refresh", s.refreshModels)
	mux.HandleFunc("POST /api/v1/workers/{id}/model", s.setPreferredModel)

	mux.HandleFunc("GET /api/v1/work-orders", s.listOrders)
	mux.HandleFunc("POST /api/v1/work-orders", s.proposeOrder)
	mux.HandleFunc("GET /api/v1/work-orders/{id}", s.getOrder)
	mux.HandleFunc("POST /api/v1/work-orders/{id}/start", s.startOrder)
	mux.HandleFunc("POST /api/v1/work-orders/{id}/cancel", s.cancelOrder)
	mux.HandleFunc("GET /api/v1/work-orders/{id}/report", s.orderReport)
	mux.HandleFunc("POST /api/v1/work-orders/{id}/changes/apply", s.applyChanges)
	mux.HandleFunc("POST /api/v1/work-orders/{id}/changes/discard", s.discardChanges)

	mux.HandleFunc("GET /api/v1/approvals/pending", s.pendingApprovals)
	mux.HandleFunc("POST /api/v1/approvals/{id}/grant", s.grantApproval)
	mux.HandleFunc("POST /api/v1/approvals/{id}/deny", s.denyApproval)

	mux.HandleFunc("GET /api/v1/expectations", s.listExpectations)
	mux.HandleFunc("GET /api/v1/discrepancies", s.listDiscrepancies)
	mux.HandleFunc("POST /api/v1/discrepancies/{id}/acknowledge", s.ackDiscrepancy)
	mux.HandleFunc("GET /api/v1/observations", s.listObservations)

	mux.HandleFunc("GET /api/v1/conversations", s.listConversations)
	mux.HandleFunc("POST /api/v1/conversations", s.createConversation)
	mux.HandleFunc("GET /api/v1/conversations/{id}", s.getConversation)
	mux.HandleFunc("GET /api/v1/conversations/{id}/messages", s.conversationMessages)
	mux.HandleFunc("POST /api/v1/conversations/{id}/messages", s.sendMessage)
	mux.HandleFunc("GET /api/v1/conversations/{id}/proposal", s.lastProposal)
	mux.HandleFunc("POST /api/v1/conversations/{id}/thread", s.setConversationThread)
	mux.HandleFunc("POST /api/v1/conversations/{id}/threads", s.startThreadFromTalk)
	mux.HandleFunc("POST /api/v1/conversations/{id}/work-orders", s.orderFromTalk)

	mux.HandleFunc("GET /api/v1/memory/candidates", s.memoryCandidates)
	mux.HandleFunc("POST /api/v1/memory/candidates/{id}/accept", s.acceptCandidate)
	mux.HandleFunc("POST /api/v1/memory/candidates/{id}/reject", s.rejectCandidate)
	mux.HandleFunc("GET /api/v1/memories", s.listMemories)
	mux.HandleFunc("POST /api/v1/memories", s.rememberDirect)
	mux.HandleFunc("DELETE /api/v1/memories/{id}", s.forgetMemory)

	mux.HandleFunc("GET /api/v1/local-model", s.localModelState)
	mux.HandleFunc("POST /api/v1/local-model/start", s.startLocalModel)
	mux.HandleFunc("POST /api/v1/local-model/stop", s.stopLocalModel)
	mux.HandleFunc("GET /api/v1/local-model/available", s.availableModels)
	mux.HandleFunc("POST /api/v1/local-model/select", s.selectLocalModel)

	mux.HandleFunc("GET /api/v1/skills", s.listSkills)
	mux.HandleFunc("POST /api/v1/skills/{id}/apply", s.applySkill)
	mux.HandleFunc("POST /api/v1/skills/{id}/retire", s.retireSkill)
	mux.HandleFunc("POST /api/v1/skills/learn", s.learnSkill)

	mux.HandleFunc("POST /api/v1/harness/study", s.studyHarness)
	mux.HandleFunc("POST /api/v1/harness/adopt", s.adoptHarness)

	mux.HandleFunc("GET /api/v1/notices", s.listNotices)
	mux.HandleFunc("POST /api/v1/notices/{id}/read", s.readNotice)
	mux.HandleFunc("POST /api/v1/notices/mute", s.muteNotices)
	mux.HandleFunc("POST /api/v1/notices/unmute", s.unmuteNotices)

	mux.HandleFunc("GET /api/v1/settings", s.getSettings)
	mux.HandleFunc("POST /api/v1/settings/workspace-roots", s.addWorkspaceRoot)
	mux.HandleFunc("DELETE /api/v1/settings/workspace-roots", s.removeWorkspaceRoot)
	mux.HandleFunc("POST /api/v1/workers/{id}/enabled", s.setWorkerEnabled)
	mux.HandleFunc("POST /api/v1/settings/model-policy", s.setModelPolicy)

	mux.Handle("/", s.ui())

	return withCommonHeaders(mux, s.log)
}

// withCommonHeaders закрывает страницу от встраивания и определения типа.
//
// net/http сам перехватывает panic в handler, но при этом намеренно закрывает
// соединение. Для браузера длинный запрос к локальной модели тогда выглядит как
// безымянный NetworkError. Здесь panic перехватывается раньше: stack trace
// остаётся в журнале Barrymore, а клиент получает нормальный problem+json.
func withCommonHeaders(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if log == nil {
					log = slog.Default()
				}
				log.Error("panic в HTTP handler",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(recovered),
					"stack", string(debug.Stack()))
				writeProblem(w, http.StatusInternalServerError,
					"внутренняя ошибка",
					"обработчик запроса аварийно завершился; подробности записаны в журнал Бэрримора")
			}
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// ---------- служебные ----------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DB.Integrity(r.Context()); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "база данных недоступна", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready",
		"notes":  s.app.StartupNotes,
	})
}

// systemState отдаёт наблюдаемое состояние системы.
//
// Неизвестное остаётся неизвестным: оптимистичных умолчаний здесь нет.
func (s *Server) systemState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caps := s.app.Delegation.Runner().Capabilities()

	head, err := s.app.Journal.Head(ctx)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "журнал недоступен", err.Error())
		return
	}
	open, err := s.app.Runtime.Discrepancies(ctx, true, 100)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "расхождения недоступны", err.Error())
		return
	}
	pending, err := s.app.Delegation.PendingApprovals(ctx)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "подтверждения недоступны", err.Error())
		return
	}
	active, err := s.app.Delegation.ActiveRuns(ctx)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "запуски недоступны", err.Error())
		return
	}
	changes, err := s.app.Delegation.PendingChanges(ctx)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "изменения недоступны", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"journal_head":       head,
		"open_discrepancies": open,
		"pending_approvals":  pending,
		"pending_changes":    changes,
		"active_runs":        active,
		"isolation":          caps,
		"workspace_roots":    s.app.Policy.Roots(),
		"startup_notes":      s.app.StartupNotes,
		"conversation":       s.app.Talk.ProviderStatus(ctx),
		"local_model":        s.app.LocalModel.State(),
		"model_policy":       s.app.Config.ModelPolicy.Describe(),
		"memory_policy":      s.app.Memory.Policy().Describe(),
		"expectation_kinds":  s.app.Runtime.Kinds().Names(),
		"reflex_policies":    s.app.Runtime.Reflexes().IDs(),
		"observed_at":        s.app.Clock.Now(),
	})
}

// stream отдаёт события журнала через SSE с возможностью продолжения.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, http.StatusInternalServerError, "поток недоступен",
			"сервер не поддерживает потоковую передачу")
		return
	}

	from := int64(0)
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			from = n
		}
	}

	ctx := r.Context()
	seq := from

	// Сначала дочитываем журнал, потом ждём уведомлений. Уведомление — только
	// подсказка перечитать базу: канал может схлопнуть несколько записей, но
	// sequence в базе не теряется.
	for {
		events, err := s.app.Journal.After(ctx, seq, 200)
		if err != nil {
			return
		}
		for _, env := range events {
			data, _ := json.Marshal(env)
			fmt.Fprintf(w, "id: %d\nevent: journal\ndata: %s\n\n", env.Sequence, data)
			seq = env.Sequence
		}
		flusher.Flush()

		select {
		case <-ctx.Done():
			return
		case <-s.app.Journal.Notify():
		}
	}
}

// ---------- нити ----------

func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.Threads.List(r.Context(), r.URL.Query().Get("state"), 100)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "нити недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getThread(w http.ResponseWriter, r *http.Request) {
	d, err := s.app.Threads.Detail(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) createThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title  string `json:"title"`
		Kind   string `json:"kind"`
		Origin string `json:"origin"`
	}
	if !decode(w, r, &body) {
		return
	}
	th, err := s.app.Threads.Create(r.Context(), thread.CreateRequest{
		Title: body.Title, Kind: body.Kind, Origin: body.Origin,
		Actor: event.Actor{Type: event.ActorPerson},
	})
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "нить не создана", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, th)
}

func (s *Server) patchThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title    *string `json:"title"`
		State    *string `json:"state"`
		Priority *int    `json:"priority"`
	}
	if !decode(w, r, &body) {
		return
	}
	th, err := s.app.Threads.Update(r.Context(), r.PathValue("id"), thread.UpdateRequest{
		Title: body.Title, State: body.State, Priority: body.Priority,
		Actor: event.Actor{Type: event.ActorPerson},
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, th)
}

func (s *Server) addPosition(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Owner      string  `json:"owner"`
		Statement  string  `json:"statement"`
		Confidence float64 `json:"confidence"`
		Basis      string  `json:"basis"`
	}
	if !decode(w, r, &body) {
		return
	}
	p, err := s.app.Threads.SetPosition(r.Context(), r.PathValue("id"), thread.PositionRequest{
		Owner: body.Owner, Statement: body.Statement, Confidence: body.Confidence,
		Basis: body.Basis, Actor: event.Actor{Type: event.ActorPerson},
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) addDecision(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Summary string `json:"summary"`
		By      string `json:"by"`
	}
	if !decode(w, r, &body) {
		return
	}
	d, err := s.app.Threads.Decide(r.Context(), r.PathValue("id"),
		body.Summary, body.By, event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) addQuestion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Question string `json:"question"`
		AskedBy  string `json:"asked_by"`
	}
	if !decode(w, r, &body) {
		return
	}
	q, err := s.app.Threads.OpenQuestion(r.Context(), r.PathValue("id"),
		body.Question, body.AskedBy, event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, q)
}

func (s *Server) threadTimeline(w http.ResponseWriter, r *http.Request) {
	events, err := s.app.Threads.Timeline(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events})
}

// ---------- штат ----------

func (s *Server) listWorkers(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.Registry.List(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "реестр недоступен", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"adapters": s.app.Registry.AdapterIDs(),
	})
}

func (s *Server) discoverWorkers(w http.ResponseWriter, r *http.Request) {
	res, err := s.app.Registry.Discover(r.Context(), event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "обнаружение не выполнено", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) probeWorker(w http.ResponseWriter, r *http.Request) {
	v, err := s.app.Registry.Probe(r.Context(), r.PathValue("id"), event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// refreshModels перечитывает каталог моделей исполнителя.
func (s *Server) refreshModels(w http.ResponseWriter, r *http.Request) {
	v, err := s.app.Registry.RefreshModels(r.Context(), r.PathValue("id"),
		event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// setPreferredModel фиксирует ручной выбор модели владельцем.
func (s *Server) setPreferredModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.app.Registry.SetPreferredModel(r.Context(), r.PathValue("id"), body.Model); err != nil {
		writeProblem(w, http.StatusBadRequest, "модель не выбрана", err.Error())
		return
	}
	v, err := s.app.Registry.View(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---------- поручения ----------

func (s *Server) listOrders(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.Delegation.List(r.Context(), r.URL.Query().Get("thread_id"), 100)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "поручения недоступны", err.Error())
		return
	}
	// Дифф из списка вычищается: сотня поручений с патчами по полмегабайта
	// превратила бы обычное обновление экрана в пересылку десятков мегабайт.
	// Сам дифф отдаётся при открытии поручения.
	for i := range items {
		items[i].ChangeSummary.Patch = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) proposeOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ThreadID      string   `json:"thread_id"`
		Title         string   `json:"title"`
		Goal          string   `json:"goal"`
		Why           string   `json:"why"`
		WorkspaceRoot string   `json:"workspace_root"`
		WorkerID      string   `json:"worker_id"`
		Constraints   []string `json:"constraints"`
		// AllowWrite включает контролируемую запись: исполнитель работает
		// в копии каталога, изменения доходят до владельца отдельным решением.
		AllowWrite bool `json:"allow_write"`
	}
	if !decode(w, r, &body) {
		return
	}
	// Политика проверяется до формирования поручения: отказ должен быть
	// понятным, а не всплывать при запуске процесса.
	if err := s.app.Policy.AllowWorkspace(body.WorkspaceRoot); err != nil {
		writeProblem(w, http.StatusForbidden, "рабочий каталог не разрешён", err.Error())
		return
	}

	p, err := s.app.Delegation.Propose(r.Context(), delegation.ProposeRequest{
		ThreadID: body.ThreadID, Title: body.Title, Goal: body.Goal, Why: body.Why,
		WorkspaceRoot: body.WorkspaceRoot, WorkerID: body.WorkerID,
		Constraints: body.Constraints,
		// Умолчание — только чтение. Запись включается осознанно и явно:
		// молчание запроса не является разрешением менять чужие файлы.
		AuditOnly: !body.AllowWrite,
		Actor:     event.Actor{Type: event.ActorPerson},
	})
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "поручение не сформировано", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	d, err := s.app.Delegation.Detail(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := map[string]any{"order": d}
	if len(d.Runs) > 0 {
		last := d.Runs[len(d.Runs)-1]
		exps, err := s.app.Runtime.Expectations(r.Context(), runtime.SubjectWorkerRun, last.ID)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "ожидания недоступны", err.Error())
			return
		}
		obs, err := s.app.Runtime.Observations(r.Context(), runtime.SubjectWorkerRun, last.ID, 200)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "наблюдения недоступны", err.Error())
			return
		}
		out["expectations"] = exps
		out["observations"] = obs
		out["attached"] = s.app.Delegation.Runner().Attached(last.ID)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) startOrder(w http.ResponseWriter, r *http.Request) {
	run, err := s.app.Delegation.Start(r.Context(), r.PathValue("id"), event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeProblem(w, http.StatusConflict, "поручение не запущено", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.app.Delegation.Cancel(r.Context(), r.PathValue("id"), body.Reason,
		event.Actor{Type: event.ActorPerson}); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled"})
}

// orderReport отдаёт разобранный отчёт исполнителя.
func (s *Server) orderReport(w http.ResponseWriter, r *http.Request) {
	d, err := s.app.Delegation.Detail(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ---------- подтверждения ----------

func (s *Server) pendingApprovals(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.Delegation.PendingApprovals(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "подтверждения недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) grantApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	o, err := s.app.Delegation.Grant(r.Context(), r.PathValue("id"), body.Note,
		event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeProblem(w, http.StatusConflict, "подтверждение не принято", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) denyApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Reason == "" {
		body.Reason = "владелец отказал"
	}
	if err := s.app.Delegation.Deny(r.Context(), r.PathValue("id"), body.Reason,
		event.Actor{Type: event.ActorPerson}); err != nil {
		writeProblem(w, http.StatusConflict, "подтверждение не отклонено", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "denied"})
}

// ---------- наблюдение ----------

func (s *Server) listExpectations(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.Runtime.Expectations(r.Context(), r.URL.Query().Get("subject_type"),
		r.URL.Query().Get("subject_id"))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "ожидания недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listDiscrepancies(w http.ResponseWriter, r *http.Request) {
	openOnly := r.URL.Query().Get("all") != "true"
	items, err := s.app.Runtime.Discrepancies(r.Context(), openOnly, 100)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "расхождения недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) ackDiscrepancy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.app.Runtime.Acknowledge(r.Context(), r.PathValue("id"), body.Note,
		event.Actor{Type: event.ActorPerson}); err != nil {
		writeProblem(w, http.StatusConflict, "расхождение не подтверждено", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "acknowledged"})
}

func (s *Server) listObservations(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.Runtime.Observations(r.Context(), r.URL.Query().Get("subject_type"),
		r.URL.Query().Get("subject_id"), 200)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "наблюдения недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ---------- разговор ----------

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.Talk.List(r.Context(), 50)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "разговоры недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider": s.app.Talk.ProviderStatus(r.Context()),
		"items":    items,
	})
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ThreadID string `json:"thread_id"`
		Title    string `json:"title"`
	}
	if !decode(w, r, &body) {
		return
	}
	c, err := s.app.Talk.Start(r.Context(), body.ThreadID, body.Title)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) conversationMessages(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.Talk.Messages(r.Context(), r.PathValue("id"), 200)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// sendMessage передаёт реплику владельца и возвращает ответ Бэрримора.
//
// Ответ на локальной модели занимает десятки секунд, поэтому запрос долгий
// по своей природе; клиент должен это учитывать.
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	turn, err := s.app.Talk.Send(r.Context(), r.PathValue("id"), body.Text)
	if err != nil {
		if errors.Is(err, conversation.ErrNoProvider) {
			writeProblem(w, http.StatusServiceUnavailable, "Бэрримор не разговаривает",
				err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, turn)
}

// ---------- память ----------

func (s *Server) memoryCandidates(w http.ResponseWriter, r *http.Request) {
	pendingOnly := r.URL.Query().Get("all") != "true"
	items, err := s.app.Memory.Candidates(r.Context(), pendingOnly, 100)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "кандидаты недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) acceptCandidate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	item, err := s.app.Memory.Accept(r.Context(), r.PathValue("id"), "owner", body.Note)
	if err != nil {
		writeProblem(w, http.StatusConflict, "кандидат не принят", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) rejectCandidate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.app.Memory.Reject(r.Context(), r.PathValue("id"), "owner", body.Note); err != nil {
		writeProblem(w, http.StatusConflict, "кандидат не отклонён", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "rejected"})
}

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if q := r.URL.Query().Get("q"); q != "" {
		items, err := s.app.Memory.Search(ctx, q, 50)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "поиск не выполнен", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	items, err := s.app.Memory.All(ctx, 200)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "память недоступна", err.Error())
		return
	}
	// Надгробия отдаются отдельно и по запросу: в обычном списке они выглядели
	// бы так, будто удаление не сработало.
	forgotten := []memory.Item{}
	if r.URL.Query().Get("forgotten") == "true" {
		forgotten, err = s.app.Memory.Forgotten(ctx, 200)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "память недоступна", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "forgotten": forgotten})
}

// rememberDirect записывает то, что владелец прямо попросил запомнить.
//
// Просьба владельца и есть решение: подтверждать её ещё раз незачем.
func (s *Server) rememberDirect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content  string `json:"content"`
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	item, err := s.app.Memory.Remember(r.Context(), memory.ProposeRequest{
		Content: body.Content, Type: body.Type, ThreadID: body.ThreadID,
	})
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "не записано", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

// forgetMemory удаляет содержание записи по требованию владельца.
func (s *Server) forgetMemory(w http.ResponseWriter, r *http.Request) {
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "удалено владельцем"
	}
	if err := s.app.Memory.Forget(r.Context(), r.PathValue("id"), reason); err != nil {
		writeProblem(w, http.StatusBadRequest, "запись не удалена", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "forgotten",
		"note": "содержание удалено из памяти; в журнале остаётся отметка о том, " +
			"что запись была и была удалена",
	})
}

// ---------- локальная модель ----------

func (s *Server) localModelState(w http.ResponseWriter, r *http.Request) {
	st, err := s.app.LocalModel.Observe(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "состояние модели не записано", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// startLocalModel поднимает модель, не дожидаясь загрузки весов.
//
// Ответ приходит сразу: держать соединение открытым минуты означало бы
// притворяться, будто ожидание мгновенно.
func (s *Server) startLocalModel(w http.ResponseWriter, r *http.Request) {
	if !s.app.LocalModel.Enabled() {
		writeProblem(w, http.StatusConflict, "модель поднять нечем",
			s.app.LocalModel.State().Reason)
		return
	}
	go func() {
		if _, err := s.app.LocalModel.Ensure(context.WithoutCancel(r.Context())); err != nil {
			s.app.Log.Warn("локальная модель не поднялась по запросу владельца", "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "starting",
		"note": "модель поднимается; загрузка весов занимает минуты, " +
			"состояние видно в разделе «Состояние»",
		"log_path": s.app.LocalModel.LogPath(),
	})
}

func (s *Server) stopLocalModel(w http.ResponseWriter, r *http.Request) {
	if err := s.app.LocalModel.Stop(r.Context(), false); err != nil {
		writeProblem(w, http.StatusConflict, "модель не остановлена", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "stopped"})
}

// availableModels перечисляет файлы моделей, среди которых можно выбирать.
func (s *Server) availableModels(w http.ResponseWriter, r *http.Request) {
	dir := s.app.Config.ModelsDir
	items, err := app.FindModels(dir, s.app.LocalModel.Spec().ModelPath)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "модели не перечислены", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"dir":   dir,
		"note": "перечислены файлы .gguf; пригодность каждого проверяется только " +
			"запуском, поэтому обещать работоспособность заранее нельзя",
	})
}

// selectLocalModel меняет модель и поднимает её заново.
func (s *Server) selectLocalModel(w http.ResponseWriter, r *http.Request) {
	// Каждое поле необязательно: отсутствие означает «оставить как было».
	// Иначе настройка производительности молча стирала бы выбранную модель.
	var body struct {
		Path        *string `json:"path"`
		ContextSize *int    `json:"context_size"`
		Threads     *int    `json:"threads"`
		GPULayers   *int    `json:"gpu_layers"`
		CPUMoE      *int    `json:"cpu_moe"`
	}
	if !decode(w, r, &body) {
		return
	}
	st, err := s.app.LocalModel.Select(r.Context(), localmodel.SelectRequest{
		ModelPath: body.Path, ContextSize: body.ContextSize, Threads: body.Threads,
		GPULayers: body.GPULayers, CPUMoE: body.CPUMoE,
	})
	if err != nil {
		writeProblem(w, http.StatusConflict, "модель не переключена", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// ---------- настройки ----------

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	views, err := s.app.Registry.List(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "настройки недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_roots": s.app.Policy.Roots(),
		"model_policy":    s.app.Config.ModelPolicy.Describe(),
		"workers":         views,
		"local_model":     s.app.LocalModel.State(),
	})
}

func (s *Server) addWorkspaceRoot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &body) {
		return
	}
	roots, err := s.app.Settings.AddWorkspaceRoot(body.Path)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "каталог не разрешён", err.Error())
		return
	}
	s.app.Policy.SetRoots(roots)
	writeJSON(w, http.StatusOK, map[string]any{"workspace_roots": roots})
}

func (s *Server) removeWorkspaceRoot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &body) {
		return
	}
	roots, err := s.app.Settings.RemoveWorkspaceRoot(body.Path)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "каталог не убран", err.Error())
		return
	}
	s.app.Policy.SetRoots(roots)
	writeJSON(w, http.StatusOK, map[string]any{"workspace_roots": roots})
}

func (s *Server) setModelPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AllowFree         bool `json:"allow_free"`
		AllowSubscription bool `json:"allow_subscription"`
		AllowPaid         bool `json:"allow_paid"`
	}
	if !decode(w, r, &body) {
		return
	}
	p := worker.CostPolicy{
		AllowFree: body.AllowFree, AllowSubscription: body.AllowSubscription,
		AllowPaid: body.AllowPaid,
	}
	if err := p.Validate(); err != nil {
		writeProblem(w, http.StatusBadRequest, "политика стоимости не сохранена", err.Error())
		return
	}
	if err := s.app.Settings.SetModelPolicy(p); err != nil {
		writeProblem(w, http.StatusInternalServerError, "политика не сохранена", err.Error())
		return
	}
	s.app.Config.ModelPolicy.Set(p)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) setWorkerEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "решение владельца"
	}
	if err := s.app.Registry.SetEnabled(r.Context(), r.PathValue("id"), body.Enabled, reason,
		event.Actor{Type: event.ActorPerson}); err != nil {
		writeDomainError(w, err)
		return
	}
	v, err := s.app.Registry.View(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---------- вспомогательное ----------

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeProblem(w, http.StatusBadRequest, "запрос не разобран", err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Ответ уже начат: остаётся только зафиксировать неудачу в логе.
		slog.Default().Error("ответ не отправлен", "error", err)
	}
}

// writeProblem отдаёт ошибку по RFC 7807.
func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "about:blank", "title": title, "status": status, "detail": detail,
	})
}

func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, thread.ErrNotFound), errors.Is(err, delegation.ErrNotFound),
		errors.Is(err, worker.ErrNotFound), errors.Is(err, conversation.ErrNotFound),
		errors.Is(err, memory.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "не найдено", err.Error())
	case errors.Is(err, event.ErrConcurrency):
		writeProblem(w, http.StatusConflict, "запись устарела", err.Error())
	default:
		writeProblem(w, http.StatusInternalServerError, "внутренняя ошибка", err.Error())
	}
}

// ---------- изменения исполнителя ----------

// applyChanges переносит изменения из копии в каталог владельца.
//
// Отдельное действие по отдельному решению: до него каталог владельца не
// тронут вообще.
func (s *Server) applyChanges(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	if r.ContentLength > 0 && !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	res, err := s.app.Delegation.ApplyChanges(r.Context(), id, body.Note,
		event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeProblem(w, http.StatusConflict, "изменения не применены", err.Error())
		return
	}
	// Повод отпал: звать разбираться с тем, что владелец только что решил, —
	// мелкая, но обидная потеря доверия.
	s.withdrawNotice("changes.waiting:"+id, "владелец применил изменения")
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) discardChanges(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	if r.ContentLength > 0 && !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if err := s.app.Delegation.DiscardChanges(r.Context(), id, body.Note,
		event.Actor{Type: event.ActorPerson}); err != nil {
		writeProblem(w, http.StatusConflict, "изменения не отброшены", err.Error())
		return
	}
	s.withdrawNotice("changes.waiting:"+id, "владелец отказался от изменений")
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "discarded",
		"note":   "копия удалена; каталог остался таким, каким был",
	})
}

// ---------- инициатива ----------

// listNotices отдаёт то, что Бэрримор хочет сказать сам.
func (s *Server) listNotices(w http.ResponseWriter, r *http.Request) {
	sum, err := s.app.Initiative.Pending(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "обращения недоступны", err.Error())
		return
	}
	all, err := s.app.Initiative.List(r.Context(), 50)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "обращения недоступны", err.Error())
		return
	}
	reasons := []map[string]string{}
	for _, x := range initiative.Reasons() {
		reasons = append(reasons, map[string]string{"kind": x.Kind, "label": x.Label})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"waiting":     sum.Waiting,
		"held_count":  sum.HeldCount,
		"held_reason": sum.HeldReason,
		"policy":      sum.Policy,
		"history":     all,
		"reasons":     reasons,
	})
}

func (s *Server) readNotice(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Initiative.MarkRead(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, http.StatusBadRequest, "обращение не отмечено", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "read"})
}

// muteNotices просит молчать о поводе или о конкретном предмете.
//
// Заглушка не отменяет наблюдение: Бэрримор продолжает знать о происходящем,
// он лишь не обращается первым. Иначе «не беспокоить» превращалось бы
// в «не замечать».
func (s *Server) muteNotices(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind      string `json:"kind"`
		SubjectID string `json:"subject_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	p := s.app.Initiative.Mute(body.Kind, body.SubjectID)
	writeJSON(w, http.StatusOK, map[string]any{
		"policy": p,
		"note": "Бэрримор продолжает наблюдать за этим, но обращаться первым " +
			"не станет; всё видно в разделе «Состояние»",
	})
}

func (s *Server) unmuteNotices(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind      string `json:"kind"`
		SubjectID string `json:"subject_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policy": s.app.Initiative.Unmute(body.Kind, body.SubjectID),
	})
}

// withdrawNotice снимает обращение, повод которого отпал.
//
// Ошибка здесь не должна ломать основное действие: владелец уже принял
// решение, и отказывать ему из-за неубранного уведомления было бы нелепо.
func (s *Server) withdrawNotice(dedupeKey, reason string) {
	if err := s.app.Initiative.MarkStale(context.Background(), dedupeKey, reason); err != nil {
		s.log.Warn("обращение не снято", "key", dedupeKey, "error", err)
	}
}
