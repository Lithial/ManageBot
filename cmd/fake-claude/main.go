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
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

type action struct {
	Kind      string `json:"kind"`
	Msg       string `json:"msg,omitempty"`
	PlanMD    string `json:"plan_md,omitempty"`
	TasksJSON string `json:"tasks_json,omitempty"`
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
			emitJSON(out, map[string]any{
				"method": "report_progress",
				"params": map[string]any{"msg": a.Msg},
			})
		case "plan":
			emitJSON(out, map[string]any{
				"method": "report_plan",
				"params": map[string]any{
					"plan_md":    a.PlanMD,
					"tasks_json": a.TasksJSON,
				},
			})
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

func emitJSON(w *bufio.Writer, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
	_ = w.WriteByte('\n')
	_ = w.Flush()
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
