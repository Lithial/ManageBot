package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Lithial/ManageBot/internal/intake"
)

// fakeClient is an in-memory DaemonClient for testing the model and commands.
type fakeClient struct {
	runs       []intake.RunSummary
	run        intake.GetRunResponse
	listErr    error
	approveErr error
	approvedID string
	rejectedID string
}

func (f *fakeClient) ListRuns(_ context.Context) ([]intake.RunSummary, error) {
	return f.runs, f.listErr
}
func (f *fakeClient) GetRun(_ context.Context, _ string) (intake.GetRunResponse, error) {
	return f.run, nil
}
func (f *fakeClient) Approve(_ context.Context, id, _ string) (intake.ResolveGateResponse, error) {
	f.approvedID = id
	return intake.ResolveGateResponse{Status: "approved"}, f.approveErr
}
func (f *fakeClient) Reject(_ context.Context, id, _ string) (intake.ResolveGateResponse, error) {
	f.rejectedID = id
	return intake.ResolveGateResponse{Status: "rejected"}, nil
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func TestUpdate_cursorNavigation(t *testing.T) {
	m := New(&fakeClient{}, "")
	m.runs = []intake.RunSummary{{RunID: "a"}, {RunID: "b"}, {RunID: "c"}}

	m, _ = update(t, m, key("down"))
	if m.cursor != 1 {
		t.Fatalf("after down, cursor = %d, want 1", m.cursor)
	}
	m, _ = update(t, m, key("down"))
	m, _ = update(t, m, key("down")) // clamp at last
	if m.cursor != 2 {
		t.Errorf("cursor = %d, want clamped at 2", m.cursor)
	}
	m, _ = update(t, m, key("up"))
	if m.cursor != 1 {
		t.Errorf("after up, cursor = %d, want 1", m.cursor)
	}
}

func TestUpdate_enterEntersDetail(t *testing.T) {
	m := New(&fakeClient{}, "")
	m.runs = []intake.RunSummary{{RunID: "a"}, {RunID: "b"}}
	m.cursor = 1

	m, cmd := update(t, m, key("enter"))
	if m.mode != modeDetail {
		t.Fatalf("mode = %v, want modeDetail", m.mode)
	}
	if m.selectedID != "b" {
		t.Errorf("selectedID = %q, want b", m.selectedID)
	}
	if cmd == nil {
		t.Error("enter should return a fetch-detail command")
	}
}

func TestUpdate_escReturnsToList(t *testing.T) {
	m := New(&fakeClient{}, "run1") // starts in detail
	if m.mode != modeDetail {
		t.Fatalf("attach should start in detail mode")
	}
	m, cmd := update(t, m, key("esc"))
	if m.mode != modeList {
		t.Errorf("mode = %v, want modeList", m.mode)
	}
	if cmd == nil {
		t.Error("esc should refetch the run list")
	}
}

func TestUpdate_approveOnlyWhenGatePending(t *testing.T) {
	m := New(&fakeClient{}, "run1")

	// No pending gate: 'a' is a no-op (nil cmd).
	m.detail = intake.GetRunResponse{RunID: "run1"}
	_, cmd := update(t, m, key("a"))
	if cmd != nil {
		t.Error("approve with no pending gate should be a no-op")
	}

	// Pending gate: 'a' returns a resolve command.
	m.detail = intake.GetRunResponse{RunID: "run1", PendingGateKind: "plan"}
	_, cmd = update(t, m, key("a"))
	if cmd == nil {
		t.Fatal("approve with a pending gate should return a command")
	}
	if _, ok := cmd().(resolvedMsg); !ok {
		t.Error("approve command should produce a resolvedMsg")
	}
}

func TestUpdate_quit(t *testing.T) {
	m := New(&fakeClient{}, "")
	m, cmd := update(t, m, key("q"))
	if !m.quitting {
		t.Error("q should set quitting")
	}
	if cmd == nil {
		t.Error("q should return tea.Quit")
	}
}

func TestUpdate_runsMsgClampsCursor(t *testing.T) {
	m := New(&fakeClient{}, "")
	m.runs = []intake.RunSummary{{RunID: "a"}, {RunID: "b"}, {RunID: "c"}}
	m.cursor = 2
	// A poll returns a shorter list; cursor must clamp into range.
	m, _ = update(t, m, runsMsg{runs: []intake.RunSummary{{RunID: "a"}}})
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want clamped to 0", m.cursor)
	}
}

func TestUpdate_tickRefetchesAndReschedules(t *testing.T) {
	m := New(&fakeClient{}, "")
	_, cmd := update(t, m, tickMsg{})
	if cmd == nil {
		t.Fatal("tick should return a batched refetch+reschedule command")
	}
}

func TestCmd_fetchRunsReturnsRunsMsg(t *testing.T) {
	fc := &fakeClient{runs: []intake.RunSummary{{RunID: "x", Phase: "done"}}}
	m := New(fc, "")
	msg := m.fetchRuns()
	rm, ok := msg.(runsMsg)
	if !ok {
		t.Fatalf("msg = %T, want runsMsg", msg)
	}
	if len(rm.runs) != 1 || rm.runs[0].RunID != "x" {
		t.Errorf("runs = %+v", rm.runs)
	}
}

func TestCmd_fetchRunsError(t *testing.T) {
	m := New(&fakeClient{listErr: errors.New("boom")}, "")
	if _, ok := m.fetchRuns().(errMsg); !ok {
		t.Error("list error should produce errMsg")
	}
}

func TestCmd_resolveApproveCallsClient(t *testing.T) {
	fc := &fakeClient{}
	m := New(fc, "run9")
	cmd := m.resolve("run9", true)
	if _, ok := cmd().(resolvedMsg); !ok {
		t.Fatal("resolve should produce resolvedMsg")
	}
	if fc.approvedID != "run9" {
		t.Errorf("approvedID = %q, want run9", fc.approvedID)
	}
}
