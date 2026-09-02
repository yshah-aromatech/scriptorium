// Package tui is the interactive frontend: four workflow views (Fleet, Run,
// History, Schedules) over the internal/app facade, built on Bubble Tea v2.
//
// The package is arranged as a root model that owns size, view and focus
// (root.go), one file per view, pure render functions that take
// (data, width, theme) and return rows (cards.go), and one keymap that feeds
// both the key handling and the footer hints (keys.go). The message vocabulary
// and the two ways anything asynchronous may reach the update loop are in
// messages.go — read that before adding a goroutine.
package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/app"
)

// Run starts the TUI and blocks until the user quits.
func Run(a *app.App) error {
	_, err := tea.NewProgram(New(a, time.Now)).Run()
	return err
}
