package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Lithial/ManageBot/internal/store"
)

func TestInsertAndListEvents(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)

	pid, _ := s.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}"})
	rid, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}"})
	wid, _ := s.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t1", Branch: "b", WorktreePath: "/wt"})

	id1, err := s.InsertEvent(ctx, store.Event{RunID: rid, Kind: "run_created", PayloadJSON: "{}"})
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	id2, err := s.InsertEvent(ctx, store.Event{RunID: rid, WorkerID: wid, Kind: "worker_done", PayloadJSON: `{"summary":"did it"}`})
	if err != nil {
		t.Fatalf("InsertEvent worker: %v", err)
	}
	if id2 <= id1 {
		t.Errorf("event ids not increasing: id1=%d id2=%d", id1, id2)
	}

	got, err := s.ListEventsByRun(ctx, rid)
	if err != nil {
		t.Fatalf("ListEventsByRun: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2: %+v", len(got), got)
	}
	if got[0].Kind != "run_created" || got[1].Kind != "worker_done" {
		t.Errorf("events out of order: %+v", got)
	}
	if got[0].WorkerID != "" {
		t.Errorf("run_created WorkerID = %q, want empty", got[0].WorkerID)
	}
	if got[1].WorkerID != wid {
		t.Errorf("worker_done WorkerID = %q, want %q", got[1].WorkerID, wid)
	}
	if got[1].TS == 0 {
		t.Errorf("event TS = 0, want set")
	}
}

func TestLatestEventByKind(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	pid, _ := s.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}"})
	rid, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}"})

	_, _ = s.InsertEvent(ctx, store.Event{RunID: rid, Kind: "merge_done", PayloadJSON: `{"summary":"first"}`})
	_, _ = s.InsertEvent(ctx, store.Event{RunID: rid, Kind: "merge_done", PayloadJSON: `{"summary":"second"}`})

	got, err := s.LatestEventByKind(ctx, rid, "merge_done")
	if err != nil {
		t.Fatalf("LatestEventByKind: %v", err)
	}
	if got.PayloadJSON != `{"summary":"second"}` {
		t.Errorf("payload = %q, want the latest", got.PayloadJSON)
	}
}

func TestLatestWorkerEventByKind(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	pid, _ := s.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}"})
	rid, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "s", GatesJSON: "{}"})
	w1, _ := s.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t1", Branch: "b1", WorktreePath: "/w1"})
	w2, _ := s.InsertWorker(ctx, store.Worker{RunID: rid, TaskID: "t2", Branch: "b2", WorktreePath: "/w2"})

	_, _ = s.InsertEvent(ctx, store.Event{RunID: rid, WorkerID: w1, Kind: "worker_report_done", PayloadJSON: `{"summary":"one"}`})
	_, _ = s.InsertEvent(ctx, store.Event{RunID: rid, WorkerID: w2, Kind: "worker_report_done", PayloadJSON: `{"summary":"two"}`})

	got, err := s.LatestWorkerEventByKind(ctx, w2, "worker_report_done")
	if err != nil {
		t.Fatalf("LatestWorkerEventByKind: %v", err)
	}
	if got.PayloadJSON != `{"summary":"two"}` {
		t.Errorf("payload = %q, want worker w2's", got.PayloadJSON)
	}

	if _, err := s.LatestWorkerEventByKind(ctx, w1, "worker_report_blocked"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for absent kind", err)
	}
}

func TestLatestEventByKind_notFound(t *testing.T) {
	ctx := context.Background()
	s := openTempStore(t)
	pid, _ := s.InsertProject(ctx, store.Project{Name: "p", RepoPath: "/tmp/repo", DefaultGatesJSON: "{}"})
	rid, _ := s.InsertRun(ctx, store.Run{ProjectID: pid, IntakeKind: "cli", SpecMD: "spec", GatesJSON: "{}"})
	if _, err := s.LatestEventByKind(ctx, rid, "merge_done"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
