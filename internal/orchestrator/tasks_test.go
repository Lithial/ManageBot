package orchestrator

import "testing"

func TestParseTasks_valid(t *testing.T) {
	tasks, err := parseTasks(`[
		{"id":"a","title":"first"},
		{"id":"b","title":"second","depends_on":["a"]}
	]`)
	if err != nil {
		t.Fatalf("parseTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("len=%d, want 2", len(tasks))
	}
	if tasks[0].ID != "a" || tasks[0].Title != "first" {
		t.Errorf("tasks[0] = %+v", tasks[0])
	}
	if len(tasks[1].DependsOn) != 1 || tasks[1].DependsOn[0] != "a" {
		t.Errorf("tasks[1].DependsOn = %+v", tasks[1].DependsOn)
	}
}

func TestParseTasks_emptyArrayIsError(t *testing.T) {
	// A plan with zero tasks is a planner bug; the working phase has nothing to do.
	if _, err := parseTasks(`[]`); err == nil {
		t.Fatal("parseTasks([]): want error, got nil")
	}
}

func TestParseTasks_badJSON(t *testing.T) {
	if _, err := parseTasks(`not json`); err == nil {
		t.Fatal("parseTasks(bad): want error, got nil")
	}
}

func TestParseTasks_emptyID(t *testing.T) {
	if _, err := parseTasks(`[{"id":"","title":"x"}]`); err == nil {
		t.Fatal("parseTasks(empty id): want error, got nil")
	}
}

func TestParseTasks_duplicateID(t *testing.T) {
	if _, err := parseTasks(`[{"id":"a","title":"x"},{"id":"a","title":"y"}]`); err == nil {
		t.Fatal("parseTasks(dup id): want error, got nil")
	}
}

func TestParseTasks_danglingDependency(t *testing.T) {
	if _, err := parseTasks(`[{"id":"a","title":"x","depends_on":["ghost"]}]`); err == nil {
		t.Fatal("parseTasks(dangling dep): want error, got nil")
	}
}

func TestParseTasks_cycle(t *testing.T) {
	if _, err := parseTasks(`[
		{"id":"a","title":"x","depends_on":["b"]},
		{"id":"b","title":"y","depends_on":["a"]}
	]`); err == nil {
		t.Fatal("parseTasks(cycle): want error, got nil")
	}
}

func TestParseTasks_selfCycle(t *testing.T) {
	if _, err := parseTasks(`[{"id":"a","title":"x","depends_on":["a"]}]`); err == nil {
		t.Fatal("parseTasks(self cycle): want error, got nil")
	}
}
