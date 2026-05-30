package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	headerStyle   = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	gateStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	dimStyle      = lipgloss.NewStyle().Faint(true)
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.mode == modeDetail {
		return m.detailView()
	}
	return m.listView()
}

func (m Model) listView() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("wrap — runs") + "\n\n")
	if len(m.runs) == 0 {
		b.WriteString(dimStyle.Render("no runs yet") + "\n")
	}
	for i, r := range m.runs {
		cursor := "  "
		line := fmt.Sprintf("%-26s %-12s", r.RunID, r.Phase)
		if r.PendingGateKind != "" {
			line += gateStyle.Render("  " + r.PendingGateKind + " gate pending")
		}
		if i == m.cursor {
			cursor = "> "
			line = selectedStyle.Render(line)
		}
		b.WriteString(cursor + line + "\n")
	}
	b.WriteString("\n" + m.footer("↑/↓ move • enter open • q quit"))
	return b.String()
}

func (m Model) detailView() string {
	var b strings.Builder
	d := m.detail
	b.WriteString(headerStyle.Render("wrap — run "+m.selectedID) + "\n\n")
	if !m.haveDetail {
		b.WriteString(dimStyle.Render("loading…") + "\n")
		return b.String()
	}
	fmt.Fprintf(&b, "phase:   %s\n", d.Phase)
	if d.PlanMD != "" {
		fmt.Fprintf(&b, "plan:    %s\n", firstLine(d.PlanMD))
	}
	if d.MergeSummary != "" {
		fmt.Fprintf(&b, "merge:   %s\n", firstLine(d.MergeSummary))
	}
	if d.MergeBranch != "" {
		fmt.Fprintf(&b, "branch:  %s\n", d.MergeBranch)
	}

	help := "esc back • q quit"
	if d.PendingGateKind != "" {
		b.WriteString("\n" + gateStyle.Render("▸ "+d.PendingGateKind+" gate awaiting decision") + "\n")
		help = "a approve • r reject • esc back • q quit"
	}
	if m.status != "" {
		b.WriteString(dimStyle.Render("gate "+m.status) + "\n")
	}
	b.WriteString("\n" + m.footer(help))
	return b.String()
}

func (m Model) footer(help string) string {
	if m.err != nil {
		return errStyle.Render("error: "+m.err.Error()) + "\n" + dimStyle.Render(help)
	}
	return dimStyle.Render(help)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 60
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
