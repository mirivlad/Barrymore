package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/store"
)

// ErrNotFound возвращается, когда исполнитель отсутствует в реестре.
var ErrNotFound = errors.New("исполнитель не найден")

// Registry ведёт реестр исполнителей.
type Registry struct {
	db       *store.DB
	journal  *event.Journal
	clock    clock.Clock
	rt       *runtime.Runtime
	adapters map[string]Adapter
	order    []string
}

// NewRegistry создаёт реестр.
func NewRegistry(db *store.DB, j *event.Journal, clk clock.Clock, rt *runtime.Runtime) *Registry {
	return &Registry{db: db, journal: j, clock: clk, rt: rt, adapters: map[string]Adapter{}}
}

// Register добавляет adapter.
func (r *Registry) Register(a Adapter) error {
	id := a.Descriptor().ID
	if id == "" {
		return fmt.Errorf("adapter без идентификатора")
	}
	if _, dup := r.adapters[id]; dup {
		return fmt.Errorf("adapter %q уже зарегистрирован", id)
	}
	r.adapters[id] = a
	r.order = append(r.order, id)
	return nil
}

// Adapter возвращает adapter по идентификатору.
func (r *Registry) Adapter(id string) (Adapter, bool) {
	a, ok := r.adapters[id]
	return a, ok
}

// AdapterIDs перечисляет зарегистрированные adapter'ы в порядке добавления.
func (r *Registry) AdapterIDs() []string { return append([]string(nil), r.order...) }

// DiscoverResult — итог обнаружения.
type DiscoverResult struct {
	Found   []View   `json:"found"`
	Missing []string `json:"missing"`
}

// Discover обходит adapter'ы и обновляет реестр.
//
// Обнаружение не выполняет платных запросов (05_STAFF_AND_DELEGATION §4).
func (r *Registry) Discover(ctx context.Context, actor event.Actor) (DiscoverResult, error) {
	if actor.Type == "" {
		actor = event.Actor{Type: event.ActorPerson}
	}
	res := DiscoverResult{Found: []View{}, Missing: []string{}}

	for _, id := range r.order {
		adapter := r.adapters[id]
		desc := adapter.Descriptor()

		inst, found, err := adapter.Discover(ctx)
		if err != nil {
			return res, fmt.Errorf("обнаружение %s: %w", id, err)
		}
		if !found {
			res.Missing = append(res.Missing, id)
			continue
		}

		w, err := r.upsert(ctx, desc, inst, actor)
		if err != nil {
			return res, err
		}
		if err := r.recordAvailability(ctx, adapter, w, inst, actor); err != nil {
			return res, err
		}
		view, err := r.View(ctx, w.ID)
		if err != nil {
			return res, err
		}
		res.Found = append(res.Found, view)
	}
	return res, nil
}

type upsertPayload struct {
	Worker       Worker       `json:"worker"`
	Capabilities []Capability `json:"capabilities"`
	AuthDetail   string       `json:"auth_detail,omitempty"`
}

func (r *Registry) upsert(ctx context.Context, desc Descriptor, inst Installation, actor event.Actor) (Worker, error) {
	now := r.clock.Now()

	existing, err := r.byAdapterPath(ctx, desc.ID, inst.ExecutablePath)
	if err != nil {
		return Worker{}, err
	}

	w := Worker{
		ID:             existing.ID,
		AdapterID:      desc.ID,
		DisplayName:    desc.DisplayName,
		ExecutablePath: inst.ExecutablePath,
		Version:        inst.Version,
		TrustLevel:     desc.DefaultTrust,
		Enabled:        true,
		AuthState:      inst.AuthState,
		CostPolicy:     desc.CostPolicy,
		DiscoveredAt:   now,
		LastProbeAt:    &now,
		Notes:          desc.Notes,
	}
	eventType := EvDiscovered
	if existing.ID != "" {
		// Повторное обнаружение сохраняет исходную дату находки, уровень доверия
		// и признак включённости: они могли быть изменены владельцем вручную.
		w.DiscoveredAt = existing.DiscoveredAt
		w.TrustLevel = existing.TrustLevel
		w.Enabled = existing.Enabled
		eventType = EvProbed
	} else {
		w.ID = ids.New(ids.Worker)
	}

	caps := make([]Capability, 0, len(desc.DeclaredCapabilities)+1)
	for _, c := range desc.DeclaredCapabilities {
		caps = append(caps, Capability{
			ID: ids.New("cap"), WorkerID: w.ID, Capability: c,
			// Заявленная возможность — это обещание документации, а не факт.
			Evidence: EvidenceDeclared, Confidence: 0.4, ObservedAt: now,
			Detail: "объявлено манифестом adapter'а, запуском не подтверждено",
		})
	}
	if inst.Version != "" {
		caps = append(caps, Capability{
			ID: ids.New("cap"), WorkerID: w.ID, Capability: CapNonInteractive,
			Evidence: EvidenceProbe, Confidence: 0.9, ObservedAt: now,
			Detail: "исполняемый файл ответил на опрос версии: " + inst.Version,
		})
	}

	p := upsertPayload{Worker: w, Capabilities: caps, AuthDetail: inst.AuthDetail}
	_, err = r.journal.Write(ctx, func(tx *sql.Tx, tw *event.TxWriter) error {
		if _, err := tw.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: w.ID, ExpectedRevision: event.AnyRevision,
			EventType: eventType, Actor: actor, Payload: p,
		}); err != nil {
			return err
		}
		return applyUpsert(ctx, tx, p)
	})
	if err != nil {
		return Worker{}, err
	}
	return w, nil
}

func (r *Registry) recordAvailability(ctx context.Context, a Adapter, w Worker, inst Installation, actor event.Actor) error {
	av, err := a.Availability(ctx, inst)
	if err != nil {
		return fmt.Errorf("оценка доступности %s: %w", w.ID, err)
	}
	// Наблюдение и снимок различаются намеренно: наблюдение — то, что увидели,
	// снимок — текущая оценка состояния с TTL.
	if _, err := r.rt.RecordObservation(ctx, runtime.ObservationRequest{
		Kind:          runtime.ObsWorkerVersion,
		SubjectType:   runtime.SubjectWorker,
		SubjectID:     w.ID,
		Source:        "discovery",
		SourceQuality: runtime.QualityDirect,
		Payload: map[string]any{
			"executable":  inst.ExecutablePath,
			"version":     inst.Version,
			"auth_state":  inst.AuthState,
			"auth_detail": inst.AuthDetail,
		},
		Actor: actor,
	}); err != nil {
		return err
	}

	_, err = r.rt.UpdateSnapshot(ctx, runtime.SnapshotRequest{
		Scope:      SnapshotScope(w.ID),
		Status:     av.Status,
		Confidence: av.Confidence,
		ObservedAt: av.ObservedAt,
		ValidUntil: av.ValidUntil,
		Source:     av.Source,
		Reason:     av.Reason,
		Payload:    av,
		Actor:      actor,
	})
	return err
}

// Probe перепроверяет одного исполнителя.
func (r *Registry) Probe(ctx context.Context, workerID string, actor event.Actor) (View, error) {
	w, err := r.Get(ctx, workerID)
	if err != nil {
		return View{}, err
	}
	adapter, ok := r.adapters[w.AdapterID]
	if !ok {
		return View{}, fmt.Errorf("для исполнителя %s не зарегистрирован adapter %q", workerID, w.AdapterID)
	}
	inst, found, err := adapter.Discover(ctx)
	if err != nil {
		return View{}, err
	}
	if !found {
		// Исполнитель исчез: это наблюдение, а не повод удалить запись.
		if _, err := r.rt.UpdateSnapshot(ctx, runtime.SnapshotRequest{
			Scope: SnapshotScope(workerID), Status: StatusOffline, Confidence: 1,
			Source: "probe", Reason: "исполняемый файл больше не найден в PATH",
			Actor: actor,
		}); err != nil {
			return View{}, err
		}
		return r.View(ctx, workerID)
	}
	if _, err := r.upsert(ctx, adapter.Descriptor(), inst, actor); err != nil {
		return View{}, err
	}
	if err := r.recordAvailability(ctx, adapter, w, inst, actor); err != nil {
		return View{}, err
	}
	return r.View(ctx, workerID)
}

// SetTrust меняет уровень доверия исполнителя.
func (r *Registry) SetTrust(ctx context.Context, workerID, trust, reason string, actor event.Actor) error {
	w, err := r.Get(ctx, workerID)
	if err != nil {
		return err
	}
	p := trustPayload{WorkerID: workerID, From: w.TrustLevel, To: trust, Reason: reason,
		ChangedAt: r.clock.Now()}
	_, err = r.journal.Write(ctx, func(tx *sql.Tx, tw *event.TxWriter) error {
		if _, err := tw.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: workerID, ExpectedRevision: event.AnyRevision,
			EventType: EvTrustChanged, Actor: actor, Payload: p,
		}); err != nil {
			return err
		}
		return applyTrust(ctx, tx, p)
	})
	return err
}

// Get возвращает исполнителя.
func (r *Registry) Get(ctx context.Context, id string) (Worker, error) {
	row := r.db.Reader().QueryRowContext(ctx, selectWorkerColumns+` WHERE id = ?`, id)
	w, err := scanWorker(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Worker{}, fmt.Errorf("чтение исполнителя %s: %w", id, err)
	}
	return w, nil
}

// View возвращает исполнителя вместе с возможностями и доступностью.
func (r *Registry) View(ctx context.Context, id string) (View, error) {
	w, err := r.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	caps, err := r.capabilities(ctx, id)
	if err != nil {
		return View{}, err
	}
	av, fresh, err := r.Availability(ctx, id)
	if err != nil {
		return View{}, err
	}
	return View{Worker: w, Capabilities: caps, Availability: av, AvailabilityFresh: fresh}, nil
}

// Availability возвращает последнюю известную доступность и её свежесть.
//
// Отсутствие снимка честно отображается как unknown, а не как «доступен».
func (r *Registry) Availability(ctx context.Context, workerID string) (Availability, bool, error) {
	snap, ok, err := r.rt.LatestSnapshot(ctx, SnapshotScope(workerID))
	if err != nil {
		return Availability{}, false, err
	}
	if !ok {
		return Availability{
			Status: StatusUnknown, Confidence: 0, Source: "нет наблюдений",
			Reason: "доступность ни разу не проверялась",
		}, false, nil
	}
	var av Availability
	if err := snapshotInto(snap, &av); err != nil {
		return Availability{}, false, err
	}
	// Значения из снимка первичны, но статус и время берём из самой записи:
	// снимок мог быть создан не adapter'ом, а probe или реакцией.
	av.Status = snap.Status
	av.Confidence = snap.Confidence
	av.ObservedAt = snap.ObservedAt
	av.ValidUntil = snap.ValidUntil
	if av.Source == "" {
		av.Source = snap.Source
	}
	if av.Reason == "" {
		av.Reason = snap.Reason
	}
	return av, snap.Fresh(r.clock.Now()), nil
}

// List возвращает всех известных исполнителей.
func (r *Registry) List(ctx context.Context) ([]View, error) {
	rows, err := r.db.Reader().QueryContext(ctx, selectWorkerColumns+` ORDER BY display_name`)
	if err != nil {
		return nil, fmt.Errorf("чтение реестра исполнителей: %w", err)
	}
	var workers []Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		workers = append(workers, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]View, 0, len(workers))
	for _, w := range workers {
		v, err := r.View(ctx, w.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *Registry) capabilities(ctx context.Context, workerID string) ([]Capability, error) {
	rows, err := r.db.Reader().QueryContext(ctx, `
		SELECT id, worker_id, capability, evidence, confidence, observed_at, detail
		  FROM worker_capabilities WHERE worker_id = ? ORDER BY capability, evidence`, workerID)
	if err != nil {
		return nil, fmt.Errorf("чтение возможностей %s: %w", workerID, err)
	}
	defer rows.Close()

	out := []Capability{}
	for rows.Next() {
		var (
			c          Capability
			observedAt string
		)
		if err := rows.Scan(&c.ID, &c.WorkerID, &c.Capability, &c.Evidence,
			&c.Confidence, &observedAt, &c.Detail); err != nil {
			return nil, err
		}
		if c.ObservedAt, err = parseTS(observedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Registry) byAdapterPath(ctx context.Context, adapterID, path string) (Worker, error) {
	row := r.db.Reader().QueryRowContext(ctx,
		selectWorkerColumns+` WHERE adapter_id = ? AND executable_path = ?`, adapterID, path)
	w, err := scanWorker(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, nil
	}
	if err != nil {
		return Worker{}, fmt.Errorf("поиск исполнителя %s/%s: %w", adapterID, path, err)
	}
	return w, nil
}

// Ranked — исполнитель с объяснимой оценкой пригодности.
type Ranked struct {
	View    View     `json:"view"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons"`
	Blocked bool     `json:"blocked"`
	// BlockReason объясняет, почему исполнитель не может взять поручение.
	BlockReason string `json:"block_reason,omitempty"`
}

// RankRequest — условия отбора.
type RankRequest struct {
	// RequiredCapabilities — без них исполнитель не рассматривается.
	RequiredCapabilities []string
	// AuditOnly требует собственного read-only режима либо внешней изоляции.
	AuditOnly bool
	// RequireRunnable отсекает adapter'ы, не умеющие запускать поручения.
	RequireRunnable bool
}

// Rank строит объяснимый список кандидатов.
//
// 05_STAFF_AND_DELEGATION §8: итоговый список формирует runtime, а не модель.
// Каждый пункт сопровождается причиной, и пользователь может переопределить выбор.
func (r *Registry) Rank(ctx context.Context, req RankRequest) ([]Ranked, error) {
	views, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Ranked, 0, len(views))

	for _, v := range views {
		rk := Ranked{View: v, Score: 0, Reasons: []string{}}

		if !v.Worker.Enabled {
			rk.Blocked, rk.BlockReason = true, "исполнитель отключён владельцем"
			out = append(out, rk)
			continue
		}

		adapter, hasAdapter := r.adapters[v.Worker.AdapterID]
		runnable := false
		if hasAdapter {
			if _, err := adapter.Plan(ctx, Installation{ExecutablePath: v.Worker.ExecutablePath},
				RunRequest{WorkDir: "/", RunID: "probe"}); err == nil {
				runnable = true
			}
		}
		if req.RequireRunnable && !runnable {
			rk.Blocked = true
			rk.BlockReason = "adapter обнаруживает исполнителя, но не умеет запускать поручения"
			out = append(out, rk)
			continue
		}

		have := map[string]Capability{}
		for _, c := range v.Capabilities {
			// При нескольких основаниях побеждает более уверенное.
			if prev, ok := have[c.Capability]; !ok || c.Confidence > prev.Confidence {
				have[c.Capability] = c
			}
		}
		missing := []string{}
		for _, need := range req.RequiredCapabilities {
			c, ok := have[need]
			if !ok {
				missing = append(missing, need)
				continue
			}
			rk.Score += c.Confidence
			rk.Reasons = append(rk.Reasons,
				fmt.Sprintf("%s: подтверждено основанием %s (уверенность %.1f)",
					need, c.Evidence, c.Confidence))
		}
		if len(missing) > 0 {
			rk.Blocked = true
			rk.BlockReason = fmt.Sprintf("нет требуемых возможностей: %v", missing)
			out = append(out, rk)
			continue
		}

		if req.AuditOnly {
			if hasAdapter && adapter.Descriptor().SupportsAuditOnly {
				rk.Score += 1.0
				rk.Reasons = append(rk.Reasons,
					"есть собственный режим только для чтения — второй слой поверх внешней изоляции")
			} else {
				rk.Reasons = append(rk.Reasons,
					"собственного read-only режима нет: audit-only держится только на внешней изоляции")
			}
		}

		switch v.Availability.Status {
		case StatusAvailable:
			rk.Score += 2.0
			rk.Reasons = append(rk.Reasons, "доступность подтверждена")
		case StatusLikelyAvailable:
			rk.Score += 1.0
			rk.Reasons = append(rk.Reasons, "вероятно доступен: "+v.Availability.Reason)
		case StatusUnknown:
			rk.Reasons = append(rk.Reasons, "доступность неизвестна, оптимистичное допущение не делается")
		case StatusQuotaExhausted:
			rk.Blocked, rk.BlockReason = true, "квота исчерпана"
		case StatusAuthRequired:
			rk.Blocked, rk.BlockReason = true, "требуется авторизация: "+v.Availability.Reason
		case StatusPaymentRequired:
			rk.Blocked, rk.BlockReason = true, "запуск потребует подтверждения оплаты"
		case StatusOffline, StatusBroken:
			rk.Blocked, rk.BlockReason = true, "исполнитель недоступен: "+v.Availability.Reason
		}

		if !v.AvailabilityFresh && !rk.Blocked {
			// Сценарий R: устаревший снимок снижает оценку и требует probe,
			// а не подменяется выдуманной доступностью.
			rk.Score -= 0.5
			rk.Reasons = append(rk.Reasons,
				"снимок доступности просрочен — перед запуском нужен повторный probe")
		}
		if !v.AvailabilityFresh && v.Availability.Status == StatusUnknown {
			rk.Reasons = append(rk.Reasons, "о состоянии квоты сведений нет")
		}

		out = append(out, rk)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Blocked != out[j].Blocked {
			return !out[i].Blocked
		}
		return out[i].Score > out[j].Score
	})
	return out, nil
}

// Projections регистрирует проекторы штата.
func (r *Registry) Projections(reg *projection.Registry) {
	reg.Tables(ProjectionTables...)
	reg.On(EvDiscovered, projectUpsert)
	reg.On(EvProbed, projectUpsert)
	reg.On(EvUpdated, projectUpsert)
	reg.On(EvTrustChanged, projectTrust)
	reg.OnAudit(EvAvailabilityObserve, EvCapabilityObserved)
}

func parseTS(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("разбор времени %q: %w", s, err)
	}
	return t.UTC(), nil
}
