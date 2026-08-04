// Package api отдаёт versioned HTTP API и поток событий.
//
// 08_API_AND_EVENTS: /api/v1, JSON, problem details, оптимистичная конкурентность
// через ревизию, SSE. ADR 0010: поток возобновляется по Last-Event-ID.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/app"
	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/delegation"
	"github.com/mirivlad/barrymore/internal/event"
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

	mux.HandleFunc("GET /api/v1/approvals/pending", s.pendingApprovals)
	mux.HandleFunc("POST /api/v1/approvals/{id}/grant", s.grantApproval)
	mux.HandleFunc("POST /api/v1/approvals/{id}/deny", s.denyApproval)

	mux.HandleFunc("GET /api/v1/expectations", s.listExpectations)
	mux.HandleFunc("GET /api/v1/discrepancies", s.listDiscrepancies)
	mux.HandleFunc("POST /api/v1/discrepancies/{id}/acknowledge", s.ackDiscrepancy)
	mux.HandleFunc("GET /api/v1/observations", s.listObservations)

	mux.HandleFunc("GET /api/v1/conversations", s.listConversations)
	mux.HandleFunc("POST /api/v1/conversations", s.createConversation)
	mux.HandleFunc("GET /api/v1/conversations/{id}/messages", s.conversationMessages)
	mux.HandleFunc("POST /api/v1/conversations/{id}/messages", s.sendMessage)

	mux.HandleFunc("GET /api/v1/memory/candidates", s.memoryCandidates)
	mux.HandleFunc("POST /api/v1/memory/candidates/{id}/accept", s.acceptCandidate)
	mux.HandleFunc("POST /api/v1/memory/candidates/{id}/reject", s.rejectCandidate)
	mux.HandleFunc("GET /api/v1/memories", s.listMemories)
	mux.HandleFunc("POST /api/v1/memories", s.rememberDirect)
	mux.HandleFunc("DELETE /api/v1/memories/{id}", s.forgetMemory)

	mux.Handle("/", s.ui())

	return withCommonHeaders(mux)
}

// withCommonHeaders закрывает страницу от встраивания и определения типа.
func withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	writeJSON(w, http.StatusOK, map[string]any{
		"journal_head":       head,
		"open_discrepancies": open,
		"pending_approvals":  pending,
		"active_runs":        active,
		"isolation":          caps,
		"workspace_roots":    s.app.Policy.Roots(),
		"startup_notes":      s.app.StartupNotes,
		"conversation":       s.app.Talk.ProviderStatus(ctx),
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
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			from = parsed
		}
	}
	if v := r.URL.Query().Get("from"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			from = parsed
		}
	}

	sub, backlog, err := s.app.Journal.Broker().Subscribe(r.Context(), s.app.Journal, from, 256)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "подписка не создана", err.Error())
		return
	}
	defer sub.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	last := from
	send := func(env event.Envelope) bool {
		if env.Seq <= last {
			return true // уже доставлено; дубликат отбрасывается по seq
		}
		data, err := json.Marshal(env)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n",
			env.Seq, env.EventType, data); err != nil {
			return false
		}
		last = env.Seq
		flusher.Flush()
		return true
	}

	for _, env := range backlog {
		if !send(env) {
			return
		}
	}

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case env, ok := <-sub.C:
			if !ok {
				return
			}
			if !send(env) {
				return
			}
		case <-keepalive.C:
			// Комментарий SSE держит соединение и не мешает разбору.
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ---------- нити ----------

func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) {
	var states []string
	if v := r.URL.Query().Get("state"); v != "" {
		states = strings.Split(v, ",")
	}
	items, err := s.app.Threads.List(r.Context(), thread.ListFilter{States: states})
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "нити недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title   string `json:"title"`
		Kind    string `json:"kind"`
		Summary string `json:"summary"`
		Origin  string `json:"origin"`
	}
	if !decode(w, r, &body) {
		return
	}
	th, err := s.app.Threads.Create(r.Context(), thread.CreateRequest{
		Title: body.Title, Kind: body.Kind, Summary: body.Summary, Origin: body.Origin,
		Actor: event.Actor{Type: event.ActorPerson},
	})
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "нить не создана", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, th)
}

func (s *Server) getThread(w http.ResponseWriter, r *http.Request) {
	d, err := s.app.Threads.Detail(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	orders, err := s.app.Delegation.List(r.Context(), d.Thread.ID, 50)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "поручения недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": d, "work_orders": orders})
}

func (s *Server) patchThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title            *string `json:"title"`
		Summary          *string `json:"summary"`
		State            string  `json:"state"`
		Reason           string  `json:"reason"`
		ExpectedRevision *int64  `json:"expected_revision"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	rev := event.AnyRevision
	if body.ExpectedRevision != nil {
		rev = *body.ExpectedRevision
	}

	if body.State != "" {
		th, err := s.app.Threads.ChangeState(r.Context(), id, body.State, body.Reason, rev,
			event.Actor{Type: event.ActorPerson})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, th)
		return
	}

	th, err := s.app.Threads.Update(r.Context(), id, thread.UpdateRequest{
		Title: body.Title, Summary: body.Summary,
		ExpectedRevision: rev, Actor: event.Actor{Type: event.ActorPerson},
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
	pos, err := s.app.Threads.SetPosition(r.Context(), r.PathValue("id"), thread.PositionRequest{
		Owner: body.Owner, Statement: body.Statement,
		Confidence: body.Confidence, Basis: body.Basis,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pos)
}

func (s *Server) addDecision(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Statement    string   `json:"statement"`
		DecidedBy    string   `json:"decided_by"`
		Rationale    string   `json:"rationale"`
		Alternatives []string `json:"alternatives"`
	}
	if !decode(w, r, &body) {
		return
	}
	d, err := s.app.Threads.RecordDecision(r.Context(), r.PathValue("id"), thread.DecisionRequest{
		Statement: body.Statement, DecidedBy: body.DecidedBy,
		Rationale: body.Rationale, Alternatives: body.Alternatives,
	})
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
		// Первое поручение всегда только на чтение (12_BOOTSTRAP_PROMPT).
		AuditOnly: true,
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
	if len(d.Runs) == 0 {
		writeProblem(w, http.StatusNotFound, "отчёта нет", "поручение ещё не запускалось")
		return
	}
	last := d.Runs[len(d.Runs)-1]
	report, ok := delegation.ParseReport(last.RunDir)
	if !ok {
		writeProblem(w, http.StatusNotFound, "отчёта нет",
			"исполнитель не оставил разбираемого отчёта")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"report":        report,
		"verifications": d.Verifications,
		// Отчёт исполнителя — заявление, а не факт: рядом всегда идут проверки.
		"note": "содержимое отчёта не является подтверждённым фактом; " +
			"состояние поручения определяют проверки",
	})
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
	a, err := s.app.Delegation.Approve(r.Context(), r.PathValue("id"), "owner",
		event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeProblem(w, http.StatusConflict, "подтверждение не выдано", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) denyApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	a, err := s.app.Delegation.Deny(r.Context(), r.PathValue("id"), "owner", body.Reason,
		event.Actor{Type: event.ActorPerson})
	if err != nil {
		writeProblem(w, http.StatusConflict, "решение не записано", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// ---------- предиктивный контур ----------

func (s *Server) listExpectations(w http.ResponseWriter, r *http.Request) {
	subjectType := r.URL.Query().Get("subject_type")
	subjectID := r.URL.Query().Get("subject_id")
	if subjectType == "" || subjectID == "" {
		writeProblem(w, http.StatusBadRequest, "не хватает параметров",
			"укажите subject_type и subject_id")
		return
	}
	items, err := s.app.Runtime.Expectations(r.Context(), subjectType, subjectID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "ожидания недоступны", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listDiscrepancies(w http.ResponseWriter, r *http.Request) {
	openOnly := r.URL.Query().Get("open") != "false"
	items, err := s.app.Runtime.Discrepancies(r.Context(), openOnly, 200)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "расхождения недоступны", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, d := range items {
		attempts, err := s.app.Runtime.Attempts(r.Context(), d.ID)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "попытки недоступны", err.Error())
			return
		}
		out = append(out, map[string]any{"discrepancy": d, "attempts": attempts})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) ackDiscrepancy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.app.Runtime.AcknowledgeDiscrepancy(r.Context(), r.PathValue("id"), body.Note,
		event.Actor{Type: event.ActorPerson}); err != nil {
		writeProblem(w, http.StatusBadRequest, "расхождение не отмечено", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "acknowledged"})
}

func (s *Server) listObservations(w http.ResponseWriter, r *http.Request) {
	subjectType := r.URL.Query().Get("subject_type")
	subjectID := r.URL.Query().Get("subject_id")
	if subjectType == "" || subjectID == "" {
		writeProblem(w, http.StatusBadRequest, "не хватает параметров",
			"укажите subject_type и subject_id")
		return
	}
	items, err := s.app.Runtime.Observations(r.Context(), subjectType, subjectID, 300)
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
		"items":    items,
		"provider": s.app.Talk.ProviderStatus(r.Context()),
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
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
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
