package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/Lithial/ManageBot/internal/fsm"
)

// cancelRegistry maps a run to the cancel func of the context backing its
// in-flight subprocesses, so `wrap kill` can stop active work promptly. Empty
// for runs that are merely parked at a gate.
type cancelRegistry struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newCancelRegistry() *cancelRegistry {
	return &cancelRegistry{cancels: map[string]context.CancelFunc{}}
}

func (r *cancelRegistry) register(runID string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels[runID] = cancel
}

func (r *cancelRegistry) deregister(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancels, runID)
}

// cancel cancels the run's in-flight context if one is registered, returning
// whether it found one.
func (r *cancelRegistry) cancel(runID string) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[runID]
	r.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// WatchKills polls for runs marked `killed` (by the API) and cancels any
// in-flight work for them, so their subprocesses die promptly. Run it as a
// goroutine alongside Run; returns when ctx is cancelled.
func (o *Orchestrator) WatchKills(ctx context.Context, interval time.Duration) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			killed, err := o.cfg.Store.ListRunsByPhase(ctx, string(fsm.PhaseKilled))
			if err != nil {
				continue
			}
			for _, r := range killed {
				o.kills.cancel(r.ID)
			}
		}
	}
}

// isKilled reports whether a run has been killed out from under an in-flight
// drive, so the drive can stop without overwriting the terminal `killed` phase.
func (o *Orchestrator) isKilled(ctx context.Context, runID string) bool {
	r, err := o.cfg.Store.GetRun(ctx, runID)
	if err != nil {
		return false
	}
	return r.Phase == string(fsm.PhaseKilled)
}
