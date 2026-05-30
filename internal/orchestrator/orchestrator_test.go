package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lithial/ManageBot/internal/orchestrator"
	"github.com/Lithial/ManageBot/internal/store"
	"github.com/Lithial/ManageBot/internal/testutil"
)

func TestTick_pendingToPlanGate(t *testing.T) {
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude not built: %v (run `make fake-claude`)", err)
	}
	repo := testutil.InitGitRepo(t) // skips if git missing

	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	pid, _ := st.InsertProject(context.Background(), store.Project{
		Name: "proj", RepoPath: repo, DefaultGatesJSON: "{}",
	})
	rid, _ := st.InsertRun(context.Background(), store.Run{
		ProjectID: pid, IntakeKind: "cli", SpecMD: "build a thing", GatesJSON: "{}",
	})

	// Script: emit a plan and exit 0.
	scriptPath := filepath.Join(stateDir, "planner.jsonl")
	lines := []string{
		`{"kind":"progress","msg":"thinking"}`,
		`{"kind":"plan","plan_md":"# Plan\n- step","tasks_json":"[{\"id\":\"t1\",\"title\":\"do it\"}]"}`,
		`{"kind":"exit","code":0}`,
	}
	if err := os.WriteFile(scriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmdFactory := func(spec string) *exec.Cmd {
		c := exec.Command(fakeClaude)
		c.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
		return c
	}
	o := orchestrator.New(orchestrator.Config{
		Store:       st,
		StateDir:    stateDir,
		PlannerCmd:  cmdFactory,
		StepTimeout: 10 * time.Second,
	})

	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.GetRun(context.Background(), rid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "plan_gate" {
		t.Fatalf("phase = %q, want plan_gate", got.Phase)
	}
	plan, err := st.GetPlanByRun(context.Background(), rid)
	if err != nil {
		t.Fatalf("GetPlanByRun: %v", err)
	}
	if !strings.Contains(plan.PlanMD, "# Plan") {
		t.Errorf("plan_md = %q", plan.PlanMD)
	}
	var tasks []map[string]string
	if err := json.Unmarshal([]byte(plan.TasksJSON), &tasks); err != nil {
		t.Errorf("tasks_json parse: %v", err)
	}
	if len(tasks) != 1 || tasks[0]["id"] != "t1" {
		t.Errorf("tasks = %+v", tasks)
	}
}

func TestTick_plannerExitNonZero_failsRun(t *testing.T) {
	fakeClaude, err := testutil.LocateBinary("fake-claude")
	if err != nil {
		t.Skipf("fake-claude not built: %v", err)
	}
	repo := testutil.InitGitRepo(t)

	stateDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(stateDir, "wrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	pid, _ := st.InsertProject(context.Background(), store.Project{
		Name: "proj", RepoPath: repo, DefaultGatesJSON: "{}",
	})
	rid, _ := st.InsertRun(context.Background(), store.Run{
		ProjectID: pid, IntakeKind: "cli", SpecMD: "x", GatesJSON: "{}",
	})

	scriptPath := filepath.Join(stateDir, "fail.jsonl")
	if err := os.WriteFile(scriptPath, []byte(`{"kind":"exit","code":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdFactory := func(spec string) *exec.Cmd {
		c := exec.Command(fakeClaude)
		c.Env = append(os.Environ(), "FAKE_CLAUDE_SCRIPT="+scriptPath)
		return c
	}
	o := orchestrator.New(orchestrator.Config{
		Store:       st,
		StateDir:    stateDir,
		PlannerCmd:  cmdFactory,
		StepTimeout: 10 * time.Second,
	})
	if err := o.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, _ := st.GetRun(context.Background(), rid)
	if got.Phase != "failed" {
		t.Errorf("phase = %q, want failed", got.Phase)
	}
}
