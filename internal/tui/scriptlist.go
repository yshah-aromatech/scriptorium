package tui

import (
	"image/color"
	"io"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/yshah-aromatech/scriptorium/internal/format"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/tui/textkit"
)

// scriptItem is one row of the Run view's left pane.
type scriptItem struct{ s scripts.Script }

// FilterValue satisfies list.Item. Filtering is phase 11 (it needs the input
// overlay), but the value is the name either way.
func (i scriptItem) FilterValue() string { return i.s.Name }

// scriptDelegate renders a script row: what happened last, what it is, and how
// long ago. Everything the eye needs to pick a script out of a column that is
// only about a third of the terminal wide.
//
// A name too long for its column scrolls — but only on the SELECTED row, and
// only after a pause (marquee, below). Every other row truncates, which is
// honest and, more importantly, still.
type scriptDelegate struct{ m *Model }

func (d scriptDelegate) Height() int  { return 1 }
func (d scriptDelegate) Spacing() int { return 0 }

func (d scriptDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d scriptDelegate) Render(w io.Writer, l list.Model, index int, item list.Item) {
	it, ok := item.(scriptItem)
	if !ok {
		return
	}
	m := d.m
	th := m.th

	var bg color.Color
	bar := " "
	if index == l.Index() {
		bg = th.C.SelBg
		bar = tint(th.S.Primary, bg).Render("▎")
	}

	last := m.statuses[it.s.Name]
	st := last.Status
	switch {
	case m.run.isRunning(it.s.Name):
		st = "running"
	case m.run.isQueued(it.s.Name):
		st = "queued"
	case m.isLive(it.s.Name):
		st = "running"
	}

	age := "—"
	if !last.At.IsZero() {
		age = format.RelativeTime(m.now().Sub(last.At).Seconds())
	}

	width := l.Width()
	gap := tint(th.S.Base, bg).Render(" ")
	nameW := nameColWidth(width)

	name := it.s.Name
	if index == l.Index() {
		name = m.run.marqueeName(m, name, nameW)
	}

	var b strings.Builder
	b.WriteString(bar)
	b.WriteString(badge(th, st, bg))
	b.WriteString(gap)
	b.WriteString(tint(th.S.Base, bg).Render(textkit.Fit(name, nameW)))
	b.WriteString(gap)
	b.WriteString(runtimeTag(th, it.s.Runtime, bg))
	b.WriteString(m.scheduleGlyph(it.s.Name, bg))
	b.WriteString(tint(th.S.Desc, bg).Render(right(age, 5)))
	_, _ = io.WriteString(w, fillTo(b.String(), width, bg))
}

// newScriptList is a bubbles/list stripped to the one thing this pane is: a
// scrollable column of rows. Its chrome (title, status line, pagination, help)
// is the root frame's job, and its key map is reduced to the four bindings
// this app owns — leaving list.DefaultKeyMap in place would quietly bind q and
// / to the list's own idea of quitting and filtering.
func newScriptList(m *Model) list.Model {
	l := list.New(nil, scriptDelegate{m: m}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(false)
	l.SetShowHelp(false)
	l.SetShowFilter(false)
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()
	k := m.keys
	l.KeyMap = list.KeyMap{
		CursorUp:   k.Up,
		CursorDown: k.Down,
		GoToStart:  k.Top,
		GoToEnd:    k.Bottom,
		// everything else stays unbound: this pane does not own those keys
		NextPage:             key.NewBinding(),
		PrevPage:             key.NewBinding(),
		Filter:               key.NewBinding(),
		ClearFilter:          key.NewBinding(),
		CancelWhileFiltering: key.NewBinding(),
		AcceptWhileFiltering: key.NewBinding(),
		ShowFullHelp:         key.NewBinding(),
		CloseFullHelp:        key.NewBinding(),
		Quit:                 key.NewBinding(),
		ForceQuit:            key.NewBinding(),
	}
	return l
}

// nameColWidth is the name column inside a list pane w cells wide: the bar,
// badge, gaps, runtime tag, schedule glyph and age take the rest. Both the row
// renderer and the marquee measure through this, so they cannot disagree about
// when a name overflows.
func nameColWidth(w int) int { return max(w-(1+1+1+2+1+2+5), 4) }

func scriptItems(all []scripts.Script) []list.Item {
	items := make([]list.Item, len(all))
	for i, s := range all {
		items[i] = scriptItem{s: s}
	}
	return items
}
