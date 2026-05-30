package orchestrator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedule_linearChainRunsInOrder(t *testing.T) {
	tasks := []Task{
		{ID: "a", Title: "a"},
		{ID: "b", Title: "b", DependsOn: []string{"a"}},
		{ID: "c", Title: "c", DependsOn: []string{"b"}},
	}
	var mu sync.Mutex
	var order []string
	run := func(_ context.Context, t Task) taskStatus {
		mu.Lock()
		order = append(order, t.ID)
		mu.Unlock()
		return statusDone
	}

	got := schedule(context.Background(), tasks, 4, run)

	for _, id := range []string{"a", "b", "c"} {
		if got[id] != statusDone {
			t.Errorf("status[%s] = %q, want done", id, got[id])
		}
	}
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("run order = %v, want [a b c]", order)
	}
}

func TestSchedule_respectsConcurrencyCap(t *testing.T) {
	// Five independent tasks, cap of 2: never more than 2 running at once.
	tasks := []Task{
		{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"},
	}
	var current, max int32
	run := func(_ context.Context, _ Task) taskStatus {
		n := atomic.AddInt32(&current, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		return statusDone
	}

	got := schedule(context.Background(), tasks, 2, run)

	if len(got) != 5 {
		t.Fatalf("resolved %d tasks, want 5", len(got))
	}
	if max > 2 {
		t.Errorf("max concurrency = %d, want <= 2", max)
	}
}

func TestSchedule_failurePropagatesToDependents(t *testing.T) {
	// a fails; b depends on a (must not run, marked failed); c independent (runs, done).
	tasks := []Task{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c"},
	}
	var ran sync.Map
	run := func(_ context.Context, t Task) taskStatus {
		ran.Store(t.ID, true)
		if t.ID == "a" {
			return statusFailed
		}
		return statusDone
	}

	got := schedule(context.Background(), tasks, 4, run)

	if got["a"] != statusFailed {
		t.Errorf("status[a] = %q, want failed", got["a"])
	}
	if got["b"] != statusFailed {
		t.Errorf("status[b] = %q, want failed (propagated)", got["b"])
	}
	if got["c"] != statusDone {
		t.Errorf("status[c] = %q, want done", got["c"])
	}
	if _, ok := ran.Load("b"); ok {
		t.Error("b was run, but its dependency failed — it should be skipped")
	}
}

func TestScheduleFrom_seededTaskIsNotReRunAndUnblocksDependents(t *testing.T) {
	// Crash-resume shape: a (done) is pre-seeded; b depends on a and must run
	// (a's seeded status unblocks it); c is independent and runs. a must NOT run.
	tasks := []Task{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c"},
	}
	var ran sync.Map
	run := func(_ context.Context, t Task) taskStatus {
		ran.Store(t.ID, true)
		return statusDone
	}

	got := scheduleFrom(context.Background(), tasks, 4, run, map[string]taskStatus{"a": statusDone})

	for _, id := range []string{"a", "b", "c"} {
		if got[id] != statusDone {
			t.Errorf("status[%s] = %q, want done", id, got[id])
		}
	}
	if _, ok := ran.Load("a"); ok {
		t.Error("a was run, but it was seeded as already-done — it should be skipped")
	}
	if _, ok := ran.Load("b"); !ok {
		t.Error("b was not run, but its only dependency was seeded done — it should run")
	}
}

func TestSchedule_transitiveFailure(t *testing.T) {
	// a fails -> b (dep a) skipped -> c (dep b) skipped.
	tasks := []Task{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	run := func(_ context.Context, t Task) taskStatus {
		if t.ID == "a" {
			return statusFailed
		}
		return statusDone
	}
	got := schedule(context.Background(), tasks, 4, run)
	for _, id := range []string{"a", "b", "c"} {
		if got[id] != statusFailed {
			t.Errorf("status[%s] = %q, want failed", id, got[id])
		}
	}
}
