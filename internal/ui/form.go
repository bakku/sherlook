package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"bakku.dev/sherlook/internal/config"
)

const (
	fieldLabel = iota
	fieldAddress
	fieldTenant
	fieldClient
	fieldSecret
	fieldCount
)

var fieldNames = [fieldCount]string{"Label", "Mailbox address", "Tenant ID", "Client ID", "Client secret"}

var fieldHints = [fieldCount]string{
	"optional display name, e.g. Support DE",
	"SMTP address / UPN of the shared mailbox",
	"Directory (tenant) ID of the app registration",
	"Application (client) ID of the app registration",
	"client secret value (stored in the OS keyring)",
}

// form adds or edits a mailbox.
type form struct {
	oldAddress string
	inputs     [fieldCount]textinput.Model
	focus      int
	err        string
}

func newForm(existing *config.Mailbox) form {
	f := form{}
	for i := range f.inputs {
		in := textinput.New()
		in.Prompt = ""
		in.CharLimit = 512
		f.inputs[i] = in
	}
	f.inputs[fieldSecret].EchoMode = textinput.EchoPassword
	f.inputs[fieldSecret].EchoCharacter = '•'
	f.inputs[fieldTenant].Placeholder = "00000000-0000-0000-0000-000000000000"
	f.inputs[fieldClient].Placeholder = "00000000-0000-0000-0000-000000000000"
	f.inputs[fieldAddress].Placeholder = "support@contoso.com"

	if existing != nil {
		f.oldAddress = existing.Address
		f.inputs[fieldLabel].SetValue(existing.Label)
		f.inputs[fieldAddress].SetValue(existing.Address)
		f.inputs[fieldTenant].SetValue(existing.TenantID)
		f.inputs[fieldClient].SetValue(existing.ClientID)
		f.inputs[fieldSecret].Placeholder = "(unchanged; type to replace)"
	}
	return f
}

func (f *form) init() tea.Cmd {
	f.focus = 0
	return f.setFocus(0)
}

func (f *form) setFocus(i int) tea.Cmd {
	f.focus = (i + fieldCount) % fieldCount
	for j := range f.inputs {
		f.inputs[j].Blur()
	}
	return f.inputs[f.focus].Focus()
}

func (f *form) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		f.inputs[f.focus], cmd = f.inputs[f.focus].Update(msg)
		return cmd
	}
	switch k.String() {
	case "esc":
		return navigate(showConfigMsg{})
	case "tab", "down":
		return f.setFocus(f.focus + 1)
	case "shift+tab", "up":
		return f.setFocus(f.focus - 1)
	case "enter":
		if f.focus < fieldCount-1 {
			return f.setFocus(f.focus + 1)
		}
		return f.submit()
	case "ctrl+s":
		return f.submit()
	}
	f.err = ""
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(k)
	return cmd
}

func (f *form) submit() tea.Cmd {
	return navigate(formSubmitMsg{
		oldAddress: f.oldAddress,
		mailbox: config.Mailbox{
			Label:    f.inputs[fieldLabel].Value(),
			Address:  f.inputs[fieldAddress].Value(),
			TenantID: f.inputs[fieldTenant].Value(),
			ClientID: f.inputs[fieldClient].Value(),
		},
		secret: f.inputs[fieldSecret].Value(),
	})
}

func (f *form) view(a *App) string {
	title := "Add mailbox"
	if f.oldAddress != "" {
		title = "Edit mailbox " + f.oldAddress
	}
	const labelW = 17
	inputW := max(a.width-labelW-6, 20)

	var b strings.Builder
	b.WriteString("\n")
	for i := range f.inputs {
		f.inputs[i].Width = inputW
		marker := "  "
		label := styleLabel.Render(fmt.Sprintf("%-*s", labelW, fieldNames[i]))
		if i == f.focus {
			marker = styleAccent.Render("▸ ")
			label = styleBold.Render(fmt.Sprintf("%-*s", labelW, fieldNames[i]))
		}
		b.WriteString(" " + marker + label + f.inputs[i].View() + "\n")
		b.WriteString(styleFaint.Render(strings.Repeat(" ", labelW+3)+fieldHints[i]) + "\n")
	}
	if f.err != "" {
		b.WriteString("\n")
		b.WriteString(styleError.Render("  ✗ "+f.err) + "\n")
	}
	h := max(a.height-2, 1)
	return header(a.width, styleBold.Render(title), "") + "\n" +
		fill(b.String(), a.width, h) + "\n" +
		footer(a.width, a.st, "tab/↑↓ move · enter next/save · ctrl+s save · esc cancel")
}
