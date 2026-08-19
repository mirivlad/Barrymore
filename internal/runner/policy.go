package runner

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// StopAllForNetworkPolicy is the barrier used before changing the global
// network route for external staff.
//
// The owner expects a worker-proxy setting to describe all staff that are
// running now, not only processes started after the setting changed. Therefore
// Barrymore must not switch the route while an older worker is still alive.
//
// The method first asks every known worker process to terminate, waits for the
// old generation to disappear, and escalates to a hard stop if necessary. A
// caller may apply the new proxy only after this method returns nil.
func (r *Runner) StopAllForNetworkPolicy(ctx context.Context) (int, error) {
	ids := snapshotIdentities()
	if len(ids) == 0 {
		return 0, nil
	}

	var errs []error
	for runID, id := range ids {
		if !r.Alive(id) {
			continue
		}
		if err := r.Cancel(ctx, runID, id, false); err != nil {
			errs = append(errs, fmt.Errorf("мягкая остановка %s: %w", runID, err))
		}
	}

	if waitWorkersGone(ctx, r, ids, 4*time.Second) {
		return len(ids), errors.Join(errs...)
	}

	// A proxy change is a security boundary, not a polite preference. If a CLI
	// ignored the graceful termination request, it must not remain on the old
	// route while Barrymore reports that the new policy is active.
	for runID, id := range ids {
		if !r.Alive(id) {
			continue
		}
		if err := r.Cancel(ctx, runID, id, true); err != nil {
			errs = append(errs, fmt.Errorf("жёсткая остановка %s: %w", runID, err))
		}
	}
	if !waitWorkersGone(ctx, r, ids, 2*time.Second) {
		errs = append(errs, errors.New(
			"сетевую политику нельзя менять: хотя бы один старый worker всё ещё жив"))
	}
	return len(ids), errors.Join(errs...)
}

func snapshotIdentities() map[string]ProcessIdentity {
	identityMu.RLock()
	defer identityMu.RUnlock()
	out := make(map[string]ProcessIdentity, len(identities))
	for runID, id := range identities {
		out[runID] = id
	}
	return out
}

func waitWorkersGone(ctx context.Context, r *Runner, ids map[string]ProcessIdentity, limit time.Duration) bool {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	allGone := func() bool {
		for _, id := range ids {
			if r.Alive(id) {
				return false
			}
		}
		return true
	}
	if allGone() {
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return allGone()
		case <-tick.C:
			if allGone() {
				return true
			}
		}
	}
}
