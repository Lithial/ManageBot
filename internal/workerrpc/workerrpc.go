// Package workerrpc defines the small NDJSON protocol that worker
// subprocesses speak on their stdout to report progress and outcomes.
// Method names mirror the planned MCP tool surface so the transport can
// be swapped (stdio NDJSON → real MCP) without changing call sites.
package workerrpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Method names. Add new ones here; unknown methods are tolerated at decode time.
const (
	MethodReportProgress = "report_progress"
	MethodReportPlan     = "report_plan"
	MethodReportDone     = "report_done"    // Phase 3, worker shape
	MethodReportBlocked  = "report_blocked" // Phase 3+
)

// Message is one decoded NDJSON line.
type Message struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Typed params for the methods Phase 2 uses.

type ProgressParams struct {
	Msg string `json:"msg"`
}

type PlanParams struct {
	PlanMD    string `json:"plan_md"`
	TasksJSON string `json:"tasks_json"`
}

type DoneParams struct {
	Summary string `json:"summary"`
}

type BlockedParams struct {
	Reason string `json:"reason"`
}

// DecodeAll reads NDJSON from r until EOF. Returns:
//   - msgs: every JSON-object line that parsed cleanly and had a non-empty method.
//   - malformed: count of lines that started with '{' but failed json.Unmarshal.
//     Non-zero indicates a worker protocol bug (truncated output, encoding error,
//     etc.). The caller (supervisor) should log this; the orchestrator may treat
//     a worker that emitted any malformed lines as suspect.
//   - err: I/O error from the scanner (e.g. bufio.ErrTooLong if a single line
//     exceeded 1 MiB). Wrapped, so errors.Is preserves sentinels.
//
// Non-JSON lines (plain stdout chatter) are silently skipped — that's protocol,
// not a bug.
func DecodeAll(r io.Reader) ([]Message, int, error) {
	var out []Message
	var malformed int
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			malformed++
			continue
		}
		if m.Method == "" {
			continue
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return out, malformed, fmt.Errorf("scan: %w", err)
	}
	return out, malformed, nil
}

// AsProgress decodes a Message as ProgressParams. Errors include the method
// and the raw params so a wrong-shape worker message can be diagnosed from
// a single log line.
func AsProgress(m Message) (ProgressParams, error) {
	if m.Method != MethodReportProgress {
		return ProgressParams{}, fmt.Errorf("AsProgress: method = %q", m.Method)
	}
	var p ProgressParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return ProgressParams{}, fmt.Errorf("AsProgress unmarshal (method=%q, raw=%s): %w", m.Method, m.Params, err)
	}
	return p, nil
}

// AsPlan decodes a Message as PlanParams. Same error-rich shape as AsProgress.
func AsPlan(m Message) (PlanParams, error) {
	if m.Method != MethodReportPlan {
		return PlanParams{}, fmt.Errorf("AsPlan: method = %q", m.Method)
	}
	var p PlanParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return PlanParams{}, fmt.Errorf("AsPlan unmarshal (method=%q, raw=%s): %w", m.Method, m.Params, err)
	}
	return p, nil
}

// AsDone decodes a Message as DoneParams. Same error-rich shape as AsProgress.
func AsDone(m Message) (DoneParams, error) {
	if m.Method != MethodReportDone {
		return DoneParams{}, fmt.Errorf("AsDone: method = %q", m.Method)
	}
	var p DoneParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return DoneParams{}, fmt.Errorf("AsDone unmarshal (method=%q, raw=%s): %w", m.Method, m.Params, err)
	}
	return p, nil
}

// AsBlocked decodes a Message as BlockedParams. Same error-rich shape as AsProgress.
func AsBlocked(m Message) (BlockedParams, error) {
	if m.Method != MethodReportBlocked {
		return BlockedParams{}, fmt.Errorf("AsBlocked: method = %q", m.Method)
	}
	var p BlockedParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return BlockedParams{}, fmt.Errorf("AsBlocked unmarshal (method=%q, raw=%s): %w", m.Method, m.Params, err)
	}
	return p, nil
}
