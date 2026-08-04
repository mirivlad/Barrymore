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
		                     enabled, auth_state, cost_policy, discovered_at, last_probe_at, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
		    display_name    = excluded.display_name,
		    executable_path = excluded.executable_path,
		    version         = excluded.version,
		    auth_state      = excluded.auth_state,
		    cost_policy     = excluded.cost_policy,
		    last_probe_at   = excluded.last_probe_at,
		    notes           = excluded.notes`,
		w.ID, w.AdapterID, w.DisplayName, w.ExecutablePath, w.Version, w.TrustLevel,
		enabled, w.AuthState, w.CostPolicy, ts(w.DiscoveredAt), tsp(w.LastProbeAt), w.Notes)
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
	       auth_state, cost_policy, discovered_at, last_probe_at, notes
	FROM workers`

type scanner interface{ Scan(dest ...any) error }

func scanWorker(row scanner) (Worker, error) {
	var (
		w            Worker
		enabled      int
		discoveredAt string
		lastProbe    sql.NullString
	)
	err := row.Scan(&w.ID, &w.AdapterID, &w.DisplayName, &w.ExecutablePath, &w.Version,
		&w.TrustLevel, &enabled, &w.AuthState, &w.CostPolicy, &discoveredAt, &lastProbe, &w.Notes)
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
