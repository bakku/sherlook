package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// configView lists configured mailboxes for adding, editing and removing.
type configView struct {
	cursor        int
	confirmRemove bool
}

func (c *configView) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	mbs := a.mailboxes()

	if c.confirmRemove {
		c.confirmRemove = false
		if (k.String() == "y" || k.String() == "Y") && c.cursor < len(mbs) {
			return navigate(removeMailboxMsg{address: mbs[c.cursor].Address})
		}
		return nil
	}

	a.st.clear()
	switch k.String() {
	case "q":
		return tea.Quit
	case "up", "k":
		c.cursor = max(c.cursor-1, 0)
	case "down", "j":
		c.cursor = max(min(c.cursor+1, len(mbs)-1), 0)
	case "a":
		return navigate(editMailboxMsg{})
	case "e", "enter":
		if c.cursor < len(mbs) {
			return navigate(editMailboxMsg{address: mbs[c.cursor].Address})
		}
	case "x", "delete":
		if c.cursor < len(mbs) {
			c.confirmRemove = true
		}
	case "esc", "m":
		return navigate(showPickerMsg{})
	}
	return nil
}

func (c *configView) view(a *App) string {
	var b strings.Builder
	b.WriteString("\n")
	mbs := a.mailboxes()
	if len(mbs) == 0 {
		b.WriteString(styleFaint.Render("  No mailboxes configured. Press a to add one.") + "\n")
	}
	for i, mb := range mbs {
		secret := "keyring"
		if a.store.SecretInFile(mb) {
			secret = styleWarn.Render("config file")
		}
		title := mb.DisplayName()
		if mb.Label != "" {
			title += "  " + mb.Address
		}
		detail := fmt.Sprintf("     tenant %s · client %s · secret in %s", mb.TenantID, mb.ClientID, secret)
		if i == c.cursor {
			b.WriteString(styleSelected.Render(padRight(" ▸ "+title, a.width-2)) + "\n")
		} else {
			b.WriteString("   " + styleBold.Render(title) + "\n")
		}
		b.WriteString(styleFaint.Render(detail) + "\n\n")
	}
	fmt.Fprintf(&b, "%s\n", styleFaint.Render("  Config file: "+a.store.Path))

	foot := footer(a.width, a.st, "↑↓ select · a add · e/enter edit · x remove · esc back · q quit")
	if c.confirmRemove && c.cursor < len(mbs) {
		foot = styleWarn.Render(truncate(fmt.Sprintf(" Remove %s and its stored secret? [y/N]", mbs[c.cursor].Address), a.width))
	}
	h := max(a.height-2, 1)
	return header(a.width, styleBold.Render("Configure mailboxes"), "") + "\n" +
		fill(b.String(), a.width, h) + "\n" + foot
}
