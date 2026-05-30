package orchestrator

import "context"

// taskStatus is a task's terminal outcome within the working phase.
type taskStatus string

const (
	statusDone   taskStatus = "done"
	statusFailed taskStatus = "failed"
)

// runTaskFunc executes one ready task and returns its terminal status. The
// scheduler never calls it for a task whose dependency failed.
type runTaskFunc func(ctx context.Context, t Task) taskStatus

// schedule runs tasks honoring their depends_on edges and a concurrency cap,
// and returns the terminal status of every task keyed by id.
//
// Up to maxConcurrent run functions execute at once. A task becomes eligible
// once all its dependencies have resolved; if any dependency resolved to
// statusFailed, the task is marked statusFailed *without running* (failure
// propagates transitively to every downstream task). The input graph is
// assumed acyclic — parseTasks guarantees that before this is called.
func schedule(ctx context.Context, tasks []Task, maxConcurrent int, run runTaskFunc) map[string]taskStatus {
	return scheduleFrom(ctx, tasks, maxConcurrent, run, nil)
}

// scheduleFrom is schedule with a seed of already-resolved task statuses. Tasks
// present in seed are treated as resolved (never run, never re-dispatched) and
// their status feeds dependency resolution exactly as if they had just run —
// this is the crash-resume path, where completed tasks (a worker_done event)
// are pre-seeded as statusDone so their downstream dependents proceed and only
// the unfinished tasks are re-dispatched. A nil/empty seed is the fresh path.
func scheduleFrom(ctx context.Context, tasks []Task, maxConcurrent int, run runTaskFunc, seed map[string]taskStatus) map[string]taskStatus {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	byID := make(map[string]Task, len(tasks))
	pending := make(map[string]struct{}, len(tasks))
	status := make(map[string]taskStatus, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
		if s, ok := seed[t.ID]; ok {
			status[t.ID] = s
			continue
		}
		pending[t.ID] = struct{}{}
	}
	// depsState reports whether every dependency of t has resolved, and whether
	// any of them failed.
	depsState := func(t Task) (resolved bool, anyFailed bool) {
		for _, dep := range t.DependsOn {
			st, ok := status[dep]
			if !ok {
				return false, false
			}
			if st == statusFailed {
				anyFailed = true
			}
		}
		return true, anyFailed
	}

	type completion struct {
		id string
		st taskStatus
	}
	completions := make(chan completion)
	running := 0

	for len(status) < len(tasks) {
		// Launch every ready task the cap allows, and short-circuit any task
		// whose dependency already failed.
		for id := range pending {
			t := byID[id]
			resolved, anyFailed := depsState(t)
			if !resolved {
				continue
			}
			if anyFailed {
				status[id] = statusFailed
				delete(pending, id)
				continue
			}
			if running >= maxConcurrent {
				continue
			}
			delete(pending, id)
			running++
			go func(t Task) {
				completions <- completion{t.ID, run(ctx, t)}
			}(t)
		}

		if running == 0 {
			// Nothing in flight: either we're done, or every remaining task was
			// just short-circuited as failed and the loop condition will catch it.
			continue
		}
		c := <-completions
		status[c.id] = c.st
		running--
	}
	return status
}
