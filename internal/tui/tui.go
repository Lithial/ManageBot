// Package tui is a Bubble Tea terminal UI over the daemon's read/resolve API.
// It owns no daemon logic: it lists runs, shows a run's detail, and resolves
// gates via the same socket every other client uses. Per the project testing
// strategy, the Model.Update function and the commands are unit-tested; the
// rendered View output is never snapshot-tested.
package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Lithial/ManageBot/internal/intake"
)

// DaemonClient is the subset of the wrapd client the TUI needs. *client.Client
// satisfies it; tests use a fake.
type DaemonClient interface {
	ListRuns(ctx context.Context) ([]intake.RunSummary, error)
	GetRun(ctx context.Context, id string) (intake.GetRunResponse, error)
	Approve(ctx context.Context, id, by string) (intake.ResolveGateResponse, error)
	Reject(ctx context.Context, id, by string) (intake.ResolveGateResponse, error)
}

type mode int

const (
	modeList mode = iota
	modeDetail
)

const defaultPoll = time.Second

// Model is the Bubble Tea model. All I/O happens inside the commands; Update is
// a pure transition over (model, msg).
type Model struct {
	client    DaemonClient
	pollEvery time.Duration

	mode   mode
	runs   []intake.RunSummary
	cursor int

	selectedID string
	detail     intake.GetRunResponse
	haveDetail bool

	status   string // transient line, e.g. "approved"
	err      error
	quitting bool
}

// New builds a model. A non-empty startRunID opens directly in the detail view
// (the `wrap attach <run-id>` entry point); empty opens the dashboard list.
func New(client DaemonClient, startRunID string) Model {
	m := Model{client: client, pollEvery: defaultPoll}
	if startRunID != "" {
		m.mode = modeDetail
		m.selectedID = startRunID
	}
	return m
}

// Messages.
type runsMsg struct{ runs []intake.RunSummary }
type detailMsg struct{ run intake.GetRunResponse }
type resolvedMsg struct{ status string }
type errMsg struct{ err error }
type tickMsg struct{}

func (m Model) Init() tea.Cmd {
	first := m.fetchRuns
	if m.mode == modeDetail {
		first = m.fetchDetail(m.selectedID)
	}
	return tea.Batch(first, m.scheduleTick())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case runsMsg:
		m.runs = msg.runs
		if m.cursor > len(m.runs)-1 {
			m.cursor = len(m.runs) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		return m, nil
	case detailMsg:
		m.detail = msg.run
		m.haveDetail = true
		return m, nil
	case resolvedMsg:
		m.status = msg.status
		// Refetch detail so the view reflects the daemon's next tick.
		return m, m.fetchDetail(m.selectedID)
	case errMsg:
		m.err = msg.err
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.refresh(), m.scheduleTick())
	}
	return m, nil
}

func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "up", "k":
		if m.mode == modeList && m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.mode == modeList && m.cursor < len(m.runs)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		if m.mode == modeList && len(m.runs) > 0 {
			m.mode = modeDetail
			m.selectedID = m.runs[m.cursor].RunID
			m.haveDetail = false
			m.status = ""
			return m, m.fetchDetail(m.selectedID)
		}
		return m, nil
	case "esc":
		if m.mode == modeDetail {
			m.mode = modeList
			m.status = ""
			m.err = nil
			return m, m.fetchRuns
		}
		return m, nil
	case "a":
		if m.mode == modeDetail && m.detail.PendingGateKind != "" {
			return m, m.resolve(m.selectedID, true)
		}
		return m, nil
	case "r":
		if m.mode == modeDetail && m.detail.PendingGateKind != "" {
			return m, m.resolve(m.selectedID, false)
		}
		return m, nil
	}
	return m, nil
}

// refresh refetches whatever the current view shows.
func (m Model) refresh() tea.Cmd {
	if m.mode == modeDetail {
		return m.fetchDetail(m.selectedID)
	}
	return m.fetchRuns
}

func (m Model) scheduleTick() tea.Cmd {
	return tea.Tick(m.pollEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

// Commands (all I/O lives here).

func (m Model) fetchRuns() tea.Msg {
	runs, err := m.client.ListRuns(context.Background())
	if err != nil {
		return errMsg{err}
	}
	return runsMsg{runs}
}

func (m Model) fetchDetail(id string) tea.Cmd {
	return func() tea.Msg {
		run, err := m.client.GetRun(context.Background(), id)
		if err != nil {
			return errMsg{err}
		}
		return detailMsg{run}
	}
}

func (m Model) resolve(id string, approve bool) tea.Cmd {
	return func() tea.Msg {
		var err error
		if approve {
			_, err = m.client.Approve(context.Background(), id, "tui")
		} else {
			_, err = m.client.Reject(context.Background(), id, "tui")
		}
		if err != nil {
			return errMsg{err}
		}
		if approve {
			return resolvedMsg{status: "approved"}
		}
		return resolvedMsg{status: "rejected"}
	}
}
