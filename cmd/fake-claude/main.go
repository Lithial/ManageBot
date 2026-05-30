// fake-claude is an env-driven stand-in for `claude -p` used in wrap's
// integration tests.
//
// Modes:
//
//	FAKE_CLAUDE_SCRIPT=<path>    Script mode: read JSONL actions from the
//	                             file and execute them in order. Other
//	                             env vars are ignored in this mode.
//	else                         Legacy mode: simple stdout/stderr/sleep/exit
//	                             driven by the FAKE_CLAUDE_* vars below.
//
// Script actions (one JSON object per line):
//
//	{"kind":"progress","msg":"..."}
//	{"kind":"plan","plan_md":"...","tasks_json":"..."}
//	{"kind":"done","summary":"..."}
//	{"kind":"blocked","reason":"..."}
//	{"kind":"stderr","text":"..."}
//	{"kind":"sleep_ms","ms":N}
//	{"kind":"exit","code":N}
//
// Legacy env vars:
//
//	FAKE_CLAUDE_EXIT_CODE   integer exit code (default 0)
//	FAKE_CLAUDE_SLEEP_MS    pre-exit sleep
//	FAKE_CLAUDE_STDOUT      string to print to stdout
//	FAKE_CLAUDE_STDERR      string to print to stderr
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Lithial/ManageBot/internal/client"
)

type action struct {
	Kind      string `json:"kind"`
	Msg       string `json:"msg,omitempty"`
	PlanMD    string `json:"plan_md,omitempty"`
	TasksJSON string `json:"tasks_json,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Text      string `json:"text,omitempty"`
	Ms        int    `json:"ms,omitempty"`
	Code      int    `json:"code,omitempty"`
}

func main() {
	if script := os.Getenv("FAKE_CLAUDE_SCRIPT"); script != "" {
		os.Exit(runScript(script))
	}
	os.Exit(runLegacy())
}

func runScript(path string) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-claude: open script: %v\n", err)
		return 1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	// Two transports: when the orchestrator scopes us to a worker (WRAP_WORKER_ID +
	// WRAP_MCP_SOCKET), report via the daemon's worker endpoints (simulating
	// claude + the wrap-mcp bridge). Otherwise emit the legacy NDJSON on stdout.
	rep := pickReporter(out)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var a action
		if err := json.Unmarshal(line, &a); err != nil {
			fmt.Fprintf(os.Stderr, "fake-claude: bad action line %q: %v\n", line, err)
			return 1
		}
		switch a.Kind {
		case "progress":
			if err := rep.progress(a.Msg); err != nil {
				fmt.Fprintf(os.Stderr, "fake-claude: report progress: %v\n", err)
				return 1
			}
		case "plan":
			if err := rep.plan(a.PlanMD, a.TasksJSON); err != nil {
				fmt.Fprintf(os.Stderr, "fake-claude: report plan: %v\n", err)
				return 1
			}
		case "done":
			if err := rep.done(a.Summary); err != nil {
				fmt.Fprintf(os.Stderr, "fake-claude: report done: %v\n", err)
				return 1
			}
		case "blocked":
			if err := rep.blocked(a.Reason); err != nil {
				fmt.Fprintf(os.Stderr, "fake-claude: report blocked: %v\n", err)
				return 1
			}
		case "stderr":
			fmt.Fprint(os.Stderr, a.Text)
		case "sleep_ms":
			if a.Ms > 0 {
				_ = out.Flush()
				time.Sleep(time.Duration(a.Ms) * time.Millisecond)
			}
		case "exit":
			_ = out.Flush()
			return a.Code
		default:
			fmt.Fprintf(os.Stderr, "fake-claude: unknown action kind %q\n", a.Kind)
			return 1
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "fake-claude: scan: %v\n", err)
		return 1
	}
	return 0
}

// reporter abstracts how a scripted report is delivered: NDJSON on stdout, or
// MCP-style calls to the daemon's worker endpoints.
type reporter interface {
	progress(msg string) error
	plan(planMD, tasksJSON string) error
	done(summary string) error
	blocked(reason string) error
}

func pickReporter(out *bufio.Writer) reporter {
	wid := os.Getenv("WRAP_WORKER_ID")
	sock := os.Getenv("WRAP_MCP_SOCKET")
	if wid != "" && sock != "" {
		return &mcpReporter{c: client.New(sock), wid: wid}
	}
	return &ndjsonReporter{out: out}
}

type ndjsonReporter struct{ out *bufio.Writer }

func (r *ndjsonReporter) progress(msg string) error {
	return emitJSON(r.out, map[string]any{"method": "report_progress", "params": map[string]any{"msg": msg}})
}
func (r *ndjsonReporter) plan(planMD, tasksJSON string) error {
	return emitJSON(r.out, map[string]any{"method": "report_plan", "params": map[string]any{"plan_md": planMD, "tasks_json": tasksJSON}})
}
func (r *ndjsonReporter) done(summary string) error {
	return emitJSON(r.out, map[string]any{"method": "report_done", "params": map[string]any{"summary": summary}})
}
func (r *ndjsonReporter) blocked(reason string) error {
	return emitJSON(r.out, map[string]any{"method": "report_blocked", "params": map[string]any{"reason": reason}})
}

type mcpReporter struct {
	c   *client.Client
	wid string
}

func (r *mcpReporter) progress(msg string) error {
	return r.c.WorkerReportProgress(context.Background(), r.wid, msg)
}
func (r *mcpReporter) plan(planMD, tasksJSON string) error {
	return r.c.WorkerReportPlan(context.Background(), r.wid, planMD, tasksJSON)
}
func (r *mcpReporter) done(summary string) error {
	return r.c.WorkerReportDone(context.Background(), r.wid, summary)
}
func (r *mcpReporter) blocked(reason string) error {
	return r.c.WorkerReportBlocked(context.Background(), r.wid, reason)
}

func emitJSON(w *bufio.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

func runLegacy() int {
	if s := os.Getenv("FAKE_CLAUDE_SLEEP_MS"); s != "" {
		if ms, err := strconv.Atoi(s); err == nil && ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}
	if s := os.Getenv("FAKE_CLAUDE_STDOUT"); s != "" {
		fmt.Fprint(os.Stdout, s)
	}
	if s := os.Getenv("FAKE_CLAUDE_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	code := 0
	if s := os.Getenv("FAKE_CLAUDE_EXIT_CODE"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			code = n
		}
	}
	return code
}
