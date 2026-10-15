package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// picker is the mailbox selection screen.
type picker struct {
	cursor int
}

func (p *picker) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	a.st.clear()
	n := len(a.mailboxes())
	switch k.String() {
	case "q":
		return tea.Quit
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
	case "down", "j":
		p.cursor = max(min(p.cursor+1, n-1), 0)
	case "enter":
		if n > 0 {
			return navigate(openMailboxMsg{index: p.cursor})
		}
	case "c":
		return navigate(showConfigMsg{})
	case "a":
		return navigate(editMailboxMsg{})
	}
	return nil
}

func (p *picker) view(a *App) string {
	var b strings.Builder
	b.WriteString("\n")
	mbs := a.mailboxes()
	if len(mbs) == 0 {
		b.WriteString(styleFaint.Render("  No mailboxes configured. Press a to add one."))
	}
	for i, mb := range mbs {
		address := ""
		if mb.Label != "" {
			address = "  " + mb.Address
		}
		if i == p.cursor {
			b.WriteString(styleSelected.Render(padRight(" ▸ "+mb.DisplayName()+address, a.width-2)) + "\n")
		} else {
			b.WriteString("   " + mb.DisplayName() + styleFaint.Render(address) + "\n")
		}
	}
	h := max(a.height-2, 1)
	return header(a.width, styleBold.Render("Select a shared mailbox"), "") + "\n" +
		fill(b.String(), a.width, h) + "\n" +
		footer(a.width, a.st, "↑↓ select · enter open · a add · c configure · q quit")
}
