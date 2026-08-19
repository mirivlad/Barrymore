package app

import (
	"context"
	"fmt"
	"os"

	"github.com/mirivlad/barrymore/internal/runner"
)

// SetWorkerProxy changes the global network policy for all external staff.
//
// The important ordering is stop -> persist/apply. We never report the new
// setting while an older worker may still be alive on the previous route.
// This keeps the user's mental model simple: at any instant all running
// external workers obey the one current policy.
func (a *App) SetWorkerProxy(ctx context.Context, raw string) (stopped int, err error) {
	normalized, err := runner.NormalizeWorkerProxy(raw)
	if err != nil {
		return 0, err
	}

	current := a.Settings.Get().WorkerProxy
	current, err = runner.NormalizeWorkerProxy(current)
	if err != nil {
		return 0, fmt.Errorf("сохранённый прокси персонала повреждён: %w", err)
	}
	if current == normalized {
		return 0, nil
	}

	stopped, err = a.Delegation.Runner().StopAllForNetworkPolicy(ctx)
	if err != nil {
		return stopped, fmt.Errorf("сетевой маршрут персонала не изменён: %w", err)
	}

	if normalized == "" {
		if err := os.Unsetenv(runner.WorkerProxyEnv); err != nil {
			return stopped, fmt.Errorf("прокси персонала не отключён: %w", err)
		}
	} else if err := os.Setenv(runner.WorkerProxyEnv, normalized); err != nil {
		return stopped, fmt.Errorf("прокси персонала не включён: %w", err)
	}

	if _, err := a.Settings.Update(func(cur Settings) Settings {
		cur.WorkerProxy = normalized
		return cur
	}); err != nil {
		// The process environment has already changed, so fail loudly instead of
		// pretending persistence succeeded. On restart the mismatch is visible
		// and can be corrected by the owner.
		return stopped, fmt.Errorf("новая proxy-политика действует, но не сохранена: %w", err)
	}
	a.Config.Settings.WorkerProxy = normalized
	return stopped, nil
}
