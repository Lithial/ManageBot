package workerrpc_test

import (
	"strings"
	"testing"

	"github.com/Lithial/ManageBot/internal/workerrpc"
)

func TestDecoder_progressAndPlan(t *testing.T) {
	input := strings.Join([]string{
		`{"method":"report_progress","params":{"msg":"starting"}}`,
		`{"method":"report_plan","params":{"plan_md":"# Plan","tasks_json":"[]"}}`,
		``, // trailing newline produces empty final line; decoder should ignore
	}, "\n")

	got, err := workerrpc.DecodeAll(strings.NewReader(input))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2: %+v", len(got), got)
	}
	if got[0].Method != "report_progress" {
		t.Errorf("got[0].Method = %q", got[0].Method)
	}
	prog, err := workerrpc.AsProgress(got[0])
	if err != nil {
		t.Fatalf("AsProgress: %v", err)
	}
	if prog.Msg != "starting" {
		t.Errorf("prog.Msg = %q", prog.Msg)
	}
	plan, err := workerrpc.AsPlan(got[1])
	if err != nil {
		t.Fatalf("AsPlan: %v", err)
	}
	if plan.PlanMD != "# Plan" || plan.TasksJSON != "[]" {
		t.Errorf("plan = %+v", plan)
	}
}

func TestDecoder_skipsNonJSONLines(t *testing.T) {
	// Real claude (and noisy shims) may interleave plain stdout text with
	// JSON-RPC. The decoder must skip non-JSON lines silently rather than fail.
	input := "starting up...\n" +
		`{"method":"report_progress","params":{"msg":"ok"}}` + "\n" +
		"done.\n"
	got, err := workerrpc.DecodeAll(strings.NewReader(input))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(got) != 1 || got[0].Method != "report_progress" {
		t.Errorf("got = %+v, want one report_progress", got)
	}
}

func TestDecoder_unknownMethodKept(t *testing.T) {
	// Unknown methods are returned to the caller as-is; it's the caller's
	// job to decide whether to ignore them (forward compatibility).
	input := `{"method":"future_thing","params":{}}` + "\n"
	got, err := workerrpc.DecodeAll(strings.NewReader(input))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(got) != 1 || got[0].Method != "future_thing" {
		t.Errorf("got = %+v", got)
	}
}

func TestAsProgress_wrongMethod(t *testing.T) {
	m := workerrpc.Message{Method: "report_plan"}
	if _, err := workerrpc.AsProgress(m); err == nil {
		t.Fatal("AsProgress on report_plan: want error, got nil")
	}
}
