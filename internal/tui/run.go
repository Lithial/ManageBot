package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Run launches the TUI against the given client. A non-empty startRunID opens
// directly in the detail (attach) view; empty opens the dashboard list. It
// blocks until the user quits.
func Run(client DaemonClient, startRunID string) error {
	p := tea.NewProgram(New(client, startRunID), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
