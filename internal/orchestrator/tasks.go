package orchestrator

import (
	"encoding/json"
	"fmt"
)

// Task is one unit of work from a plan's tasks_json. DependsOn lists the ids
// of tasks that must reach `done` before this one may be spawned.
type Task struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	DependsOn []string `json:"depends_on"`
}

// parseTasks decodes and validates a plan's tasks_json. It enforces:
//   - at least one task (an empty plan is a planner bug),
//   - non-empty, unique task ids,
//   - every depends_on entry references a declared task,
//   - no dependency cycles (including self-edges).
//
// Validation lives here, before any worktree or subprocess is created, so a
// malformed plan fails the run cleanly rather than mid-spawn.
func parseTasks(tasksJSON string) ([]Task, error) {
	var tasks []Task
	if err := json.Unmarshal([]byte(tasksJSON), &tasks); err != nil {
		return nil, fmt.Errorf("parse tasks_json: %w", err)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("plan has no tasks")
	}

	index := make(map[string]Task, len(tasks))
	for _, t := range tasks {
		if t.ID == "" {
			return nil, fmt.Errorf("task with empty id (title=%q)", t.Title)
		}
		if _, dup := index[t.ID]; dup {
			return nil, fmt.Errorf("duplicate task id %q", t.ID)
		}
		index[t.ID] = t
	}
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if _, ok := index[dep]; !ok {
				return nil, fmt.Errorf("task %q depends on unknown task %q", t.ID, dep)
			}
		}
	}
	if err := detectCycle(index); err != nil {
		return nil, err
	}
	return tasks, nil
}

// detectCycle reports an error if the depends_on graph contains a cycle, via
// DFS with white/grey/black colouring.
func detectCycle(index map[string]Task) error {
	const (
		white = 0 // unvisited
		grey  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(index))
	var visit func(id string) error
	visit = func(id string) error {
		color[id] = grey
		for _, dep := range index[id].DependsOn {
			switch color[dep] {
			case grey:
				return fmt.Errorf("dependency cycle detected at task %q", dep)
			case white:
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}
	for id := range index {
		if color[id] == white {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}
