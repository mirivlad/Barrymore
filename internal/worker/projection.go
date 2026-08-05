package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/runtime"
)

type trustPayload struct {
	WorkerID  string    `json:"worker_id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Reason    string    `json:"reason,omitempty"`
	ChangedAt time.Time `json:"changed_at"`
}

func applyUpsert(ctx context.Context, tx *sql.Tx, p upsertPayload) error {
	w := p.Worker
	enabled := 0
	if w.Enabled {
		enabled = 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO workers (id, adapter_id, display_name, executable_path, version, trust_level,
		                     enabled, auth_state, cost_policy, discovered_at, last_probe_at, notes,
		                     class, preferred_model)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
		    display_name    = excluded.display_name,
		    executable_path = excluded.executable_path,
		    version         = excluded.version,
		    auth_state      = excluded.auth_state,
		    cost_policy     = excluded.cost_policy,
		    last_probe_at   = excluded.last_probe_at,
		    notes           = excluded.notes,
		    class           = excluded.class,
		    preferred_model = excluded.preferred_model`,
		w.ID, w.AdapterID, w.DisplayName, w.ExecutablePath, w.Version, w.TrustLevel,
		enabled, w.AuthState, w.CostPolicy, ts(w.DiscoveredAt), tsp(w.LastProbeAt), w.Notes,
		w.Class, w.PreferredModel)
	if err != nil {
		return fmt.Errorf("проекция исполнителя %s: %w", w.ID, err)
	}

	for _, c := range p.Capabilities {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO worker_capabilities (id, worker_id, capability, evidence, confidence,
			                                 observed_at, detail)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (worker_id, capability, evidence) DO UPDATE SET
			    confidence  = excluded.confidence,
			    observed_at = excluded.observed_at,
			    detail      = excluded.detail`,
			c.ID, c.WorkerID, c.Capability, c.Evidence, c.Confidence, ts(c.ObservedAt), c.Detail)
		if err != nil {
			return fmt.Errorf("проекция возможности %s исполнителя %s: %w",
				c.Capability, w.ID, err)
		}
	}
	return nil
}

func projectUpsert(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p upsertPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyUpsert(ctx, tx, p)
}

// enabledPayload — решение владельца о том, привлекать ли исполнителя.
type enabledPayload struct {
	WorkerID  string    `json:"worker_id"`
	Enabled   bool      `json:"enabled"`
	Reason    string    `json:"reason,omitempty"`
	ChangedAt time.Time `json:"changed_at"`
}

func applyEnabled(ctx context.Context, tx *sql.Tx, p enabledPayload) error {
	v := 0
	if p.Enabled {
		v = 1
	}
	_, err := tx.ExecContext(ctx, `UPDATE workers SET enabled = ? WHERE id = ?`, v, p.WorkerID)
	if err != nil {
		return fmt.Errorf("проекция участия исполнителя %s: %w", p.WorkerID, err)
	}
	return nil
}

func projectEnabled(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p enabledPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyEnabled(ctx, tx, p)
}

func applyTrust(ctx context.Context, tx *sql.Tx, p trustPayload) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE workers SET trust_level = ? WHERE id = ?`, p.To, p.WorkerID)
	if err != nil {
		return fmt.Errorf("проекция уровня доверия %s: %w", p.WorkerID, err)
	}
	return nil
}

func projectTrust(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p trustPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyTrust(ctx, tx, p)
}

const selectWorkerColumns = `
	SELECT id, adapter_id, display_name, executable_path, version, trust_level, enabled,
	       auth_state, cost_policy, discovered_at, last_probe_at, notes, class,
	       preferred_model, models_refreshed_at
	FROM workers`

type scanner interface{ Scan(dest ...any) error }

func scanWorker(row scanner) (Worker, error) {
	var (
		w                    Worker
		enabled              int
		discoveredAt         string
		lastProbe, modelsRef sql.NullString
	)
	err := row.Scan(&w.ID, &w.AdapterID, &w.DisplayName, &w.ExecutablePath, &w.Version,
		&w.TrustLevel, &enabled, &w.AuthState, &w.CostPolicy, &discoveredAt, &lastProbe,
		&w.Notes, &w.Class, &w.PreferredModel, &modelsRef)
	if err != nil {
		return Worker{}, err
	}
	w.Enabled = enabled == 1
	if w.DiscoveredAt, err = parseTS(discoveredAt); err != nil {
		return Worker{}, err
	}
	if lastProbe.Valid && lastProbe.String != "" {
		t, err := parseTS(lastProbe.String)
		if err != nil {
			return Worker{}, err
		}
		w.LastProbeAt = &t
	}
	if modelsRef.Valid && modelsRef.String != "" {
		t, err := parseTS(modelsRef.String)
		if err != nil {
			return Worker{}, err
		}
		w.ModelsRefreshedAt = &t
	}
	return w, nil
}

// snapshotInto распаковывает payload снимка в структуру доступности.
func snapshotInto(snap runtime.Snapshot, av *Availability) error {
	if len(snap.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(snap.Payload, av); err != nil {
		return fmt.Errorf("разбор снимка доступности %s: %w", snap.ID, err)
	}
	return nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func tsp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

type modelsPayload struct {
	WorkerID   string    `json:"worker_id"`
	Models     []Model   `json:"models"`
	ObservedAt time.Time `json:"observed_at"`
}

// applyModels заменяет каталог моделей исполнителя наблюдённым списком.
//
// Модели, исчезнувшие из списка, удаляются: провайдеры вводят и убирают
// бесплатные модели, и держать в реестре то, чего исполнитель больше не
// предлагает, значит подсовывать владельцу мираж.
//
// Подтверждённая запуском стоимость при этом не теряется: обновление списка
// не должно откатывать знание, добытое настоящим запуском.
func applyModels(ctx context.Context, tx *sql.Tx, p modelsPayload) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM worker_models WHERE worker_id = ?`, p.WorkerID); err != nil {
		return fmt.Errorf("очистка каталога моделей %s: %w", p.WorkerID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE workers SET models_refreshed_at = ? WHERE id = ?`,
		ts(p.ObservedAt), p.WorkerID); err != nil {
		return fmt.Errorf("отметка обновления каталога %s: %w", p.WorkerID, err)
	}
	for _, m := range p.Models {
		isDefault := 0
		if m.IsDefault {
			isDefault = 1
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO worker_models (id, worker_id, model_ref, provider, name, cost_tier,
			                           source, evidence, is_default, observed_at,
			                           confidence, last_cost, verified_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (worker_id, model_ref) DO UPDATE SET
			    cost_tier = excluded.cost_tier, source = excluded.source,
			    evidence = excluded.evidence, is_default = excluded.is_default,
			    observed_at = excluded.observed_at, confidence = excluded.confidence`,
			m.ID, p.WorkerID, m.Ref, m.Provider, m.Name, m.CostTier,
			m.Source, m.Evidence, isDefault, ts(m.ObservedAt),
			m.Confidence, m.LastCost, tsp(m.VerifiedAt))
		if err != nil {
			return fmt.Errorf("проекция модели %s исполнителя %s: %w", m.Ref, p.WorkerID, err)
		}
	}
	return nil
}

func projectModels(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p modelsPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyModels(ctx, tx, p)
}

type modelCostPayload struct {
	WorkerID   string    `json:"worker_id"`
	ModelRef   string    `json:"model_ref"`
	CostTier   string    `json:"cost_tier"`
	Evidence   string    `json:"evidence"`
	Cost       float64   `json:"cost"`
	ObservedAt time.Time `json:"observed_at"`
}

// applyModelCost записывает стоимость, подтверждённую фактическим запуском.
func applyModelCost(ctx context.Context, tx *sql.Tx, p modelCostPayload) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE worker_models
		   SET cost_tier = ?, source = 'run-observed', evidence = ?,
		       confidence = 1, last_cost = ?, verified_at = ?
		 WHERE worker_id = ? AND model_ref = ?`,
		p.CostTier, p.Evidence, p.Cost, ts(p.ObservedAt), p.WorkerID, p.ModelRef)
	if err != nil {
		return fmt.Errorf("проекция стоимости модели %s: %w", p.ModelRef, err)
	}
	return nil
}

func projectModelCost(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	var p modelCostPayload
	if err := env.Decode(&p); err != nil {
		return err
	}
	return applyModelCost(ctx, tx, p)
}
