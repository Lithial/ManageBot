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

// DecodeAll reads NDJSON from r until EOF and returns every JSON-object
// line that parses cleanly. Non-JSON lines (plain stdout chatter) are
// silently skipped. An I/O error other than EOF is returned.
func DecodeAll(r io.Reader) ([]Message, error) {
	var out []Message
	sc := bufio.NewScanner(r)
	// Worker output is bounded by Phase 2 protocols, but plans can be large.
	// Allow 1 MiB lines (default is 64 KiB).
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			continue // tolerant: looked like JSON but wasn't valid
		}
		if m.Method == "" {
			continue
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return out, nil
}

// AsProgress decodes a Message as a ProgressParams, returning an error if
// the method does not match.
func AsProgress(m Message) (ProgressParams, error) {
	if m.Method != MethodReportProgress {
		return ProgressParams{}, fmt.Errorf("AsProgress: method = %q", m.Method)
	}
	var p ProgressParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return ProgressParams{}, fmt.Errorf("AsProgress unmarshal: %w", err)
	}
	return p, nil
}

// AsPlan decodes a Message as a PlanParams.
func AsPlan(m Message) (PlanParams, error) {
	if m.Method != MethodReportPlan {
		return PlanParams{}, fmt.Errorf("AsPlan: method = %q", m.Method)
	}
	var p PlanParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return PlanParams{}, fmt.Errorf("AsPlan unmarshal: %w", err)
	}
	return p, nil
}
