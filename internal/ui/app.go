// Package ui implements sherlook's terminal interface.
package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"bakku.dev/sherlook/internal/config"
)

// ClientFactory builds a Graph client for a mailbox and its resolved secret.
type ClientFactory func(mb config.Mailbox, secret string) MailClient

type screen int

const (
	screenPicker screen = iota
	screenConfig
	screenForm
	screenMail
)

// Navigation messages exchanged between screens.
type (
	openMailboxMsg struct{ index int }
	showPickerMsg  struct{}
	showConfigMsg  struct{}
	editMailboxMsg struct{ address string } // "" adds a new mailbox
	formSubmitMsg  struct {
		oldAddress string
		mailbox    config.Mailbox
		secret     string
	}
	removeMailboxMsg struct{ address string }
	connTestMsg      struct {
		address string
		err     error
	}
)

func navigate(msg tea.Msg) tea.Cmd {
	return func() tea.Msg { return msg }
}

// App is the root Bubble Tea model.
type App struct {
	store     *config.Store
	newClient ClientFactory
	clients   map[string]MailClient
	st        *status

	screen        screen
	width, height int

	picker  picker
	cfgView configView
	form    form
	mail    *mailView
}

// New creates the app. Startup warnings (e.g. config file permissions) are
// shown in the status line.
func New(store *config.Store, newClient ClientFactory, warnings []string) *App {
	a := &App{
		store:     store,
		newClient: newClient,
		clients:   map[string]MailClient{},
		st:        &status{},
		width:     80,
		height:    24,
	}
	if len(warnings) > 0 {
		a.st.set(levelWarn, "⚠ %s", strings.Join(warnings, "; "))
	}
	return a
}

func (a *App) mailboxes() []config.Mailbox { return a.store.Config.Mailboxes }

func (a *App) Init() tea.Cmd {
	switch len(a.mailboxes()) {
	case 0:
		a.screen = screenConfig
		return a.openForm("")
	case 1:
		return a.openMailbox(0)
	}
	a.screen = screenPicker
	return nil
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		if a.mail != nil {
			a.mail.setSize(msg.Width, msg.Height)
		}
		return a, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}

	case openMailboxMsg:
		return a, a.openMailbox(msg.index)

	case showPickerMsg:
		a.mail = nil
		a.screen = screenPicker
		a.picker.cursor = min(a.picker.cursor, max(len(a.mailboxes())-1, 0))
		return a, nil

	case showConfigMsg:
		a.mail = nil
		a.screen = screenConfig
		a.cfgView.cursor = min(a.cfgView.cursor, max(len(a.mailboxes())-1, 0))
		return a, nil

	case editMailboxMsg:
		return a, a.openForm(msg.address)

	case formSubmitMsg:
		return a, a.saveMailbox(msg)

	case removeMailboxMsg:
		if err := a.store.Remove(msg.address); err != nil {
			a.st.set(levelError, "Could not remove %s: %v", msg.address, err)
		} else {
			delete(a.clients, strings.ToLower(msg.address))
			a.st.set(levelOK, "Removed %s", msg.address)
		}
		a.cfgView.cursor = min(a.cfgView.cursor, max(len(a.mailboxes())-1, 0))
		return a, nil

	case connTestMsg:
		if msg.err != nil {
			a.st.set(levelError, "⚠ %s saved, but the connection test failed: %s", msg.address, describeError(msg.err))
		} else {
			a.st.set(levelOK, "✓ %s saved; connection OK", msg.address)
		}
		return a, nil
	}

	switch a.screen {
	case screenPicker:
		return a, a.picker.update(a, msg)
	case screenConfig:
		return a, a.cfgView.update(a, msg)
	case screenForm:
		return a, a.form.update(a, msg)
	case screenMail:
		if a.mail != nil {
			return a, a.mail.Update(msg)
		}
	}
	return a, nil
}

func (a *App) View() string {
	switch a.screen {
	case screenConfig:
		return a.cfgView.view(a)
	case screenForm:
		return a.form.view(a)
	case screenMail:
		if a.mail != nil {
			return a.mail.View()
		}
	}
	return a.picker.view(a)
}

func (a *App) clientFor(mb config.Mailbox) (MailClient, error) {
	key := strings.ToLower(mb.Address)
	if c, ok := a.clients[key]; ok {
		return c, nil
	}
	secret, err := a.store.Secret(mb)
	if err != nil {
		return nil, err
	}
	c := a.newClient(mb, secret)
	a.clients[key] = c
	return c, nil
}

func (a *App) openMailbox(i int) tea.Cmd {
	if i < 0 || i >= len(a.mailboxes()) {
		return nil
	}
	mb := a.mailboxes()[i]
	client, err := a.clientFor(mb)
	if err != nil {
		a.screen = screenPicker
		a.st.set(levelError, "%v", err)
		return nil
	}
	a.picker.cursor = i
	a.st.clear()
	a.mail = newMailView(mb, client, a.store.Config.ExportDir, a.st, a.width, a.height)
	a.screen = screenMail
	return a.mail.Init()
}

func (a *App) openForm(address string) tea.Cmd {
	var existing *config.Mailbox
	if address != "" {
		if i := a.store.Config.Index(address); i >= 0 {
			mb := a.mailboxes()[i]
			existing = &mb
		}
	}
	a.form = newForm(existing)
	a.screen = screenForm
	return a.form.init()
}

func (a *App) saveMailbox(msg formSubmitMsg) tea.Cmd {
	warning, err := a.store.Upsert(msg.oldAddress, msg.mailbox, msg.secret)
	if err != nil {
		a.form.err = err.Error()
		return nil
	}
	delete(a.clients, strings.ToLower(msg.oldAddress))
	delete(a.clients, strings.ToLower(msg.mailbox.Address))

	a.screen = screenConfig
	a.cfgView.cursor = max(a.store.Config.Index(msg.mailbox.Address), 0)
	if warning != "" {
		a.st.set(levelWarn, "⚠ %s; testing connection…", warning)
	} else {
		a.st.set(levelInfo, "Saved %s; testing connection…", msg.mailbox.Address)
	}

	mb := a.mailboxes()[a.cfgView.cursor]
	client, err := a.clientFor(mb)
	if err != nil {
		return navigate(connTestMsg{address: mb.Address, err: err})
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return connTestMsg{address: mb.Address, err: client.TestConnection(ctx)}
	}
}
