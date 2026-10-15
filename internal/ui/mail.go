package ui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"bakku.dev/sherlook/internal/config"
	"bakku.dev/sherlook/internal/graph"
	"bakku.dev/sherlook/internal/htmltext"
)

// MailClient is the subset of the Graph client the mail view needs.
type MailClient interface {
	ListInbox(ctx context.Context) (graph.Page, error)
	ListNext(ctx context.Context, nextLink string) (graph.Page, error)
	GetMessage(ctx context.Context, id string) (*graph.Message, error)
	SetRead(ctx context.Context, id string, read bool) error
	MoveToDeletedItems(ctx context.Context, id string) error
	PermanentDelete(ctx context.Context, id string) error
	MIME(ctx context.Context, id string) ([]byte, error)
	TestConnection(ctx context.Context) error
}

const (
	// narrowWidth is the terminal width below which the split view collapses.
	narrowWidth     = 100
	previewDebounce = 150 * time.Millisecond
	// loadMoreMargin: fetch the next page when the cursor gets this close to the end.
	loadMoreMargin = 10
	requestTimeout = 5 * time.Minute
)

type focus int

const (
	focusList focus = iota
	focusPreview
)

type modal int

const (
	modalNone modal = iota
	modalHelp
	modalDelete
	modalPurge
	modalExportPath
	modalOverwrite
)

// Messages produced by mail view commands. view identifies the mailView
// instance so results arriving after a mailbox switch are dropped.
type (
	pageMsg struct {
		view, gen int
		more      bool
		page      graph.Page
		err       error
	}
	previewTickMsg struct{ view, seq int }
	messageMsg     struct {
		view int
		id   string
		msg  *graph.Message
		err  error
	}
	readMsg struct {
		view int
		id   string
		read bool
		err  error
	}
	deletedMsg struct {
		view      int
		id        string
		permanent bool
		err       error
	}
	exportedMsg struct {
		view int
		path string
		err  error
	}
)

var mailViewSeq int

type mailView struct {
	id        int
	mb        config.Mailbox
	client    MailClient
	exportDir string
	st        *status
	now       func() time.Time

	width, height int

	msgs        []graph.MessageSummary
	next        string
	loading     bool
	loadingMore bool
	loadErr     error
	gen         int
	cursor      int
	offset      int

	focus       focus
	cache       map[string]*graph.Message
	bodies      map[string]string
	previewErrs map[string]error
	fetching    map[string]bool
	previewSeq  int
	renderedKey string
	renderedID  string
	vp          viewport.Model

	modal  modal
	target graph.MessageSummary
	input  textinput.Model
	busy   map[string]bool
}

func newMailView(mb config.Mailbox, client MailClient, exportDir string, st *status, width, height int) *mailView {
	mailViewSeq++
	vp := viewport.New(0, 0)
	// Free up d/u for delete etc.; half-page scrolling stays on ctrl+d/ctrl+u.
	vp.KeyMap.HalfPageDown = key.NewBinding(key.WithKeys("ctrl+d"))
	vp.KeyMap.HalfPageUp = key.NewBinding(key.WithKeys("ctrl+u"))
	vp.KeyMap.PageDown = key.NewBinding(key.WithKeys("pgdown", " ", "f"))
	vp.KeyMap.PageUp = key.NewBinding(key.WithKeys("pgup", "b"))

	v := &mailView{
		id:          mailViewSeq,
		mb:          mb,
		client:      client,
		exportDir:   exportDir,
		st:          st,
		now:         time.Now,
		cache:       map[string]*graph.Message{},
		bodies:      map[string]string{},
		previewErrs: map[string]error{},
		fetching:    map[string]bool{},
		busy:        map[string]bool{},
		vp:          vp,
	}
	v.setSize(width, height)
	return v
}

func (v *mailView) Init() tea.Cmd {
	v.loading = true
	return v.fetchPage("")
}

// --- layout ---------------------------------------------------------------

func (v *mailView) narrow() bool { return v.width < narrowWidth }

func (v *mailView) bodyHeight() int { return max(v.height-2, 1) }

func (v *mailView) listWidth() int {
	if v.narrow() {
		return v.width
	}
	return v.width * 2 / 5
}

func (v *mailView) previewWidth() int {
	if v.narrow() {
		return v.width
	}
	return max(v.width-v.listWidth()-1, 1)
}

func (v *mailView) visibleRows() int { return max(v.bodyHeight()/2, 1) }

func (v *mailView) setSize(w, h int) {
	v.width, v.height = w, h
	v.vp.Width = v.previewWidth()
	v.vp.Height = v.bodyHeight()
	v.renderedKey = ""
	v.ensureVisible()
	v.renderPreview()
}

func (v *mailView) ensureVisible() {
	rows := v.visibleRows()
	if v.cursor < v.offset {
		v.offset = v.cursor
	}
	if v.cursor >= v.offset+rows {
		v.offset = v.cursor - rows + 1
	}
	v.offset = max(min(v.offset, len(v.msgs)-rows), 0)
}

// --- commands -------------------------------------------------------------

func (v *mailView) fetchPage(link string) tea.Cmd {
	view, gen, client := v.id, v.gen, v.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		var page graph.Page
		var err error
		if link == "" {
			page, err = client.ListInbox(ctx)
		} else {
			page, err = client.ListNext(ctx, link)
		}
		return pageMsg{view: view, gen: gen, more: link != "", page: page, err: err}
	}
}

func (v *mailView) fetchMessage(id string) tea.Cmd {
	v.fetching[id] = true
	view, client := v.id, v.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		msg, err := client.GetMessage(ctx, id)
		return messageMsg{view: view, id: id, msg: msg, err: err}
	}
}

func (v *mailView) current() (graph.MessageSummary, bool) {
	if v.cursor < 0 || v.cursor >= len(v.msgs) {
		return graph.MessageSummary{}, false
	}
	return v.msgs[v.cursor], true
}

func (v *mailView) indexOf(id string) int {
	for i, m := range v.msgs {
		if m.ID == id {
			return i
		}
	}
	return -1
}

// cursorMoved updates scrolling and preview, and schedules paging/preview loads.
func (v *mailView) cursorMoved() tea.Cmd {
	v.ensureVisible()
	v.renderPreview()
	var cmds []tea.Cmd
	if v.next != "" && !v.loading && !v.loadingMore && v.cursor >= len(v.msgs)-loadMoreMargin {
		v.loadingMore = true
		cmds = append(cmds, v.fetchPage(v.next))
	}
	if m, ok := v.current(); ok && v.cache[m.ID] == nil && !v.fetching[m.ID] {
		v.previewSeq++
		view, seq := v.id, v.previewSeq
		cmds = append(cmds, tea.Tick(previewDebounce, func(time.Time) tea.Msg {
			return previewTickMsg{view: view, seq: seq}
		}))
	}
	return tea.Batch(cmds...)
}

func (v *mailView) moveTo(i int) tea.Cmd {
	i = max(min(i, len(v.msgs)-1), 0)
	if i == v.cursor {
		return nil
	}
	v.cursor = i
	return v.cursorMoved()
}

// --- update ---------------------------------------------------------------

func (v *mailView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case pageMsg:
		if msg.view != v.id || msg.gen != v.gen {
			return nil
		}
		return v.onPage(msg)

	case previewTickMsg:
		if msg.view != v.id || msg.seq != v.previewSeq {
			return nil
		}
		if m, ok := v.current(); ok && v.cache[m.ID] == nil && !v.fetching[m.ID] {
			delete(v.previewErrs, m.ID)
			return v.fetchMessage(m.ID)
		}

	case messageMsg:
		if msg.view != v.id {
			return nil
		}
		delete(v.fetching, msg.id)
		if msg.err != nil {
			v.previewErrs[msg.id] = msg.err
		} else {
			v.cache[msg.id] = msg.msg
			delete(v.previewErrs, msg.id)
		}
		v.renderPreview()

	case readMsg:
		if msg.view != v.id {
			return nil
		}
		delete(v.busy, msg.id)
		if msg.err != nil {
			v.st.set(levelError, "Could not change read state: %s", describeError(msg.err))
			return nil
		}
		if i := v.indexOf(msg.id); i >= 0 {
			v.msgs[i].IsRead = msg.read
		}
		if m := v.cache[msg.id]; m != nil {
			m.IsRead = msg.read
		}
		if msg.read {
			v.st.set(levelOK, "Marked as read")
		} else {
			v.st.set(levelOK, "Marked as unread")
		}

	case deletedMsg:
		if msg.view != v.id {
			return nil
		}
		delete(v.busy, msg.id)
		if msg.err != nil {
			v.st.set(levelError, "Delete failed: %s", describeError(msg.err))
			return nil
		}
		if msg.permanent {
			v.st.set(levelOK, "Message permanently deleted")
		} else {
			v.st.set(levelOK, "Message moved to Deleted Items")
		}
		return v.remove(msg.id)

	case exportedMsg:
		if msg.view != v.id {
			return nil
		}
		if msg.err != nil {
			v.st.set(levelError, "Export failed: %s", describeError(msg.err))
		} else {
			v.st.set(levelOK, "Exported to %s", msg.path)
		}

	case tea.KeyMsg:
		return v.onKey(msg)
	}
	return nil
}

func (v *mailView) onPage(msg pageMsg) tea.Cmd {
	if msg.more {
		v.loadingMore = false
		if msg.err != nil {
			v.st.set(levelError, "Loading more messages failed: %s", describeError(msg.err))
			return nil
		}
		seen := make(map[string]bool, len(v.msgs))
		for _, m := range v.msgs {
			seen[m.ID] = true
		}
		for _, m := range msg.page.Messages {
			if !seen[m.ID] {
				v.msgs = append(v.msgs, m)
			}
		}
		v.next = msg.page.NextLink
		return v.cursorMoved()
	}

	v.loading = false
	if msg.err != nil {
		v.loadErr = msg.err
		v.st.set(levelError, "Loading inbox failed: %s", describeError(msg.err))
		return nil
	}
	prev, hadPrev := v.current()
	v.loadErr = nil
	v.msgs = msg.page.Messages
	v.next = msg.page.NextLink
	v.cursor = 0
	if hadPrev {
		if i := v.indexOf(prev.ID); i >= 0 {
			v.cursor = i
		}
	}
	v.cursor = max(min(v.cursor, len(v.msgs)-1), 0)
	return v.cursorMoved()
}

// remove drops a message from the list after a successful delete; the
// cursor lands on the next message (or the previous one at the end).
func (v *mailView) remove(id string) tea.Cmd {
	i := v.indexOf(id)
	if i < 0 {
		return nil
	}
	v.msgs = append(v.msgs[:i], v.msgs[i+1:]...)
	delete(v.cache, id)
	delete(v.bodies, id)
	if i < v.cursor || v.cursor >= len(v.msgs) {
		v.cursor = max(v.cursor-1, 0)
	}
	if len(v.msgs) == 0 {
		v.focus = focusList
	}
	return v.cursorMoved()
}

func (v *mailView) onKey(k tea.KeyMsg) tea.Cmd {
	if v.modal != modalNone {
		return v.onModalKey(k)
	}
	v.st.clear()

	switch k.String() {
	case "q":
		return tea.Quit
	case "?":
		v.modal = modalHelp
		return nil
	case "m":
		return navigate(showPickerMsg{})
	case "c":
		return navigate(showConfigMsg{})
	case "R":
		return v.refresh()
	case "r":
		return v.toggleRead()
	case "d":
		return v.confirm(modalDelete)
	case "D":
		return v.confirm(modalPurge)
	case "e":
		return v.promptExport()
	case "tab":
		if v.focus == focusList {
			v.openPreview()
		} else {
			v.focus = focusList
		}
		return nil
	case "esc":
		if v.focus == focusPreview {
			v.focus = focusList
			return nil
		}
		return navigate(showPickerMsg{})
	}

	if v.focus == focusList {
		switch k.String() {
		case "enter":
			v.openPreview()
		case "up", "k":
			return v.moveTo(v.cursor - 1)
		case "down", "j":
			return v.moveTo(v.cursor + 1)
		case "pgup", "ctrl+u":
			return v.moveTo(v.cursor - v.visibleRows())
		case "pgdown", "ctrl+d", " ":
			return v.moveTo(v.cursor + v.visibleRows())
		case "home", "g":
			return v.moveTo(0)
		case "end", "G":
			return v.moveTo(len(v.msgs) - 1)
		}
		return nil
	}

	switch k.String() {
	case "home", "g":
		v.vp.GotoTop()
	case "end", "G":
		v.vp.GotoBottom()
	default:
		var cmd tea.Cmd
		v.vp, cmd = v.vp.Update(k)
		return cmd
	}
	return nil
}

func (v *mailView) openPreview() {
	if _, ok := v.current(); ok {
		v.focus = focusPreview
	}
}

func (v *mailView) refresh() tea.Cmd {
	if v.loading {
		return nil
	}
	v.gen++
	v.loading = true
	v.loadingMore = false
	v.st.set(levelInfo, "Refreshing…")
	return v.fetchPage("")
}

func (v *mailView) toggleRead() tea.Cmd {
	m, ok := v.current()
	if !ok || v.busy[m.ID] {
		return nil
	}
	v.busy[m.ID] = true
	view, client, read := v.id, v.client, !m.IsRead
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		return readMsg{view: view, id: m.ID, read: read, err: client.SetRead(ctx, m.ID, read)}
	}
}

func (v *mailView) confirm(kind modal) tea.Cmd {
	m, ok := v.current()
	if !ok || v.busy[m.ID] {
		return nil
	}
	v.target = m
	v.modal = kind
	return nil
}

func (v *mailView) delete(m graph.MessageSummary, permanent bool) tea.Cmd {
	v.busy[m.ID] = true
	if permanent {
		v.st.set(levelInfo, "Deleting permanently…")
	} else {
		v.st.set(levelInfo, "Moving to Deleted Items…")
	}
	view, client := v.id, v.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		var err error
		if permanent {
			err = client.PermanentDelete(ctx, m.ID)
		} else {
			err = client.MoveToDeletedItems(ctx, m.ID)
		}
		return deletedMsg{view: view, id: m.ID, permanent: permanent, err: err}
	}
}

func (v *mailView) promptExport() tea.Cmd {
	m, ok := v.current()
	if !ok {
		return nil
	}
	dir := expandHome(v.exportDir)
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	v.target = m
	v.modal = modalExportPath
	v.input = textinput.New()
	v.input.Prompt = ""
	v.input.Width = max(v.width-len(" Export to: ")-2, 10)
	v.input.SetValue(filepath.Join(dir, exportFilename(m)))
	v.input.CursorEnd()
	return v.input.Focus()
}

func (v *mailView) export(m graph.MessageSummary, path string) tea.Cmd {
	v.st.set(levelInfo, "Exporting…")
	view, client := v.id, v.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		data, err := client.MIME(ctx, m.ID)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(path), 0o755)
		}
		if err == nil {
			err = os.WriteFile(path, data, 0o600)
		}
		return exportedMsg{view: view, path: path, err: err}
	}
}

func (v *mailView) onModalKey(k tea.KeyMsg) tea.Cmd {
	kind := v.modal
	switch kind {
	case modalHelp:
		v.modal = modalNone
	case modalDelete, modalPurge:
		v.modal = modalNone
		if k.String() == "y" || k.String() == "Y" {
			return v.delete(v.target, kind == modalPurge)
		}
	case modalExportPath:
		switch k.String() {
		case "esc":
			v.modal = modalNone
		case "enter":
			path := expandHome(strings.TrimSpace(v.input.Value()))
			if path == "" {
				v.modal = modalNone
				return nil
			}
			v.input.SetValue(path)
			if _, err := os.Stat(path); err == nil {
				v.modal = modalOverwrite
				return nil
			} else if !errors.Is(err, fs.ErrNotExist) {
				v.modal = modalNone
				v.st.set(levelError, "Export failed: %v", err)
				return nil
			}
			v.modal = modalNone
			return v.export(v.target, path)
		default:
			var cmd tea.Cmd
			v.input, cmd = v.input.Update(k)
			return cmd
		}
	case modalOverwrite:
		v.modal = modalNone
		if k.String() == "y" || k.String() == "Y" {
			return v.export(v.target, v.input.Value())
		}
	}
	return nil
}

// --- view -----------------------------------------------------------------

func (v *mailView) View() string {
	right := "Inbox"
	switch {
	case v.loading && len(v.msgs) == 0:
		right += " · loading…"
	case v.next != "":
		right += fmt.Sprintf(" · %d+ messages", len(v.msgs))
	default:
		right += fmt.Sprintf(" · %d messages", len(v.msgs))
	}
	left := styleBold.Render(v.mb.DisplayName())
	if v.mb.Label != "" {
		left += styleFaint.Render(" <" + v.mb.Address + ">")
	}

	h := v.bodyHeight()
	var body string
	switch {
	case v.modal == modalHelp:
		body = fill(helpText, v.width, h)
	case v.narrow() && v.focus == focusPreview:
		body = fill(v.vp.View(), v.width, h)
	case v.narrow():
		body = v.listView(v.width, h)
	default:
		sep := styleFaint.Render(strings.TrimSuffix(strings.Repeat("│\n", h), "\n"))
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			v.listView(v.listWidth(), h), sep, fill(v.vp.View(), v.previewWidth(), h))
	}
	return header(v.width, left, right) + "\n" + body + "\n" + v.footerView()
}

func (v *mailView) footerView() string {
	subject := truncate(subjectOf(v.target), 40)
	switch v.modal {
	case modalDelete:
		return styleWarn.Render(truncate(fmt.Sprintf(" Move %q to Deleted Items? [y/N]", subject), v.width))
	case modalPurge:
		return styleDanger.Render(truncate(fmt.Sprintf(" PERMANENTLY delete %q? This cannot be undone. [y/N] ", subject), v.width))
	case modalExportPath:
		return truncate(styleBold.Render(" Export to: ")+v.input.View(), v.width)
	case modalOverwrite:
		return styleWarn.Render(truncate(fmt.Sprintf(" %s already exists. Overwrite? [y/N]", v.input.Value()), v.width))
	case modalHelp:
		return styleFaint.Render(" press any key to close help")
	}
	hints := "enter open · r read/unread · d delete · D purge · e export · R refresh · m mailboxes · ? help · q quit"
	if v.focus == focusPreview {
		hints = "↑↓/pgup/pgdn scroll · tab/esc list · r read/unread · d delete · D purge · e export · ? help"
	}
	return footer(v.width, v.st, hints)
}

func (v *mailView) listView(w, h int) string {
	switch {
	case v.loading && len(v.msgs) == 0:
		return fill(styleFaint.Render(" Loading inbox…"), w, h)
	case v.loadErr != nil && len(v.msgs) == 0:
		msg := ansi.Wrap(describeError(v.loadErr), w-2, "")
		return fill(styleError.Render(" Could not load inbox:")+"\n"+indent(msg, " ")+"\n\n"+styleFaint.Render(" Press R to retry."), w, h)
	case len(v.msgs) == 0:
		return fill(styleFaint.Render(" Inbox is empty."), w, h)
	}

	var lines []string
	now := v.now()
	for i := v.offset; i < len(v.msgs) && len(lines)+2 <= h; i++ {
		l1, l2 := v.row(v.msgs[i], w, i == v.cursor, now)
		lines = append(lines, l1, l2)
	}
	if v.loadingMore && len(lines) < h {
		lines = append(lines, styleFaint.Render(" Loading more…"))
	}
	return fill(strings.Join(lines, "\n"), w, h)
}

// row renders a message as two lines: markers, sender, date / subject.
func (v *mailView) row(m graph.MessageSummary, w int, selected bool, now time.Time) (string, string) {
	marker := " "
	if !m.IsRead {
		marker = "●"
	}
	clip := "  "
	if m.HasAttachments {
		clip = "📎"
	}
	sender := "(unknown sender)"
	if m.From != nil {
		sender = m.From.ShortName()
	}
	date := formatListDate(m.ReceivedDateTime, now)

	const prefixW = 6 // " ● 📎 "
	senderW := max(w-prefixW-ansi.StringWidth(date)-2, 1)
	sender = padRight(truncate(sender, senderW), senderW)
	subject := truncate(subjectOf(m), max(w-prefixW-1, 1))

	base := lipgloss.NewStyle()
	if !m.IsRead {
		base = base.Bold(true)
	}
	if selected {
		st := styleSelDim
		if v.focus == focusList {
			st = styleSelected
		}
		st = st.Inherit(base)
		l1 := padRight(" "+marker+" "+clip+" "+sender+" "+date, w)
		l2 := padRight(strings.Repeat(" ", prefixW)+subject, w)
		return st.Render(l1), st.Render(l2)
	}
	l1 := " " + styleAccent.Render(marker) + " " + clip + " " + base.Render(sender) + " " + styleFaint.Render(date)
	l2 := strings.Repeat(" ", prefixW) + base.Render(subject)
	return l1, l2
}

func subjectOf(m graph.MessageSummary) string {
	if strings.TrimSpace(m.Subject) == "" {
		return "(no subject)"
	}
	return m.Subject
}

// renderPreview refreshes the viewport content for the highlighted message.
func (v *mailView) renderPreview() {
	m, ok := v.current()
	state := "none"
	switch {
	case !ok:
	case v.cache[m.ID] != nil:
		state = "loaded"
	case v.previewErrs[m.ID] != nil:
		state = "error"
	default:
		state = "loading"
	}
	key := fmt.Sprintf("%s|%d|%s", m.ID, v.previewWidth(), state)
	if key == v.renderedKey {
		return
	}
	v.renderedKey = key

	w := max(v.previewWidth()-2, 10)
	var content string
	switch state {
	case "none":
		content = ""
	case "error":
		content = styleError.Render("Could not load message:") + "\n" + ansi.Wrap(describeError(v.previewErrs[m.ID]), w, "")
	case "loading":
		content = styleFaint.Render("Loading…")
	case "loaded":
		content = v.previewContent(v.cache[m.ID], w)
	}
	v.vp.SetContent(indent(content, " "))
	if m.ID != v.renderedID {
		v.vp.GotoTop()
		v.renderedID = m.ID
	}
}

func (v *mailView) previewContent(m *graph.Message, w int) string {
	var b strings.Builder
	b.WriteString(ansi.Wrap(styleBold.Render(subjectOf(m.MessageSummary)), w, ""))
	b.WriteString("\n\n")

	field := func(label, value string) {
		if value == "" {
			return
		}
		b.WriteString(ansi.Wrap(styleLabel.Render(fmt.Sprintf("%-6s", label))+" "+value, w, ""))
		b.WriteByte('\n')
	}
	if m.From != nil {
		field("From", m.From.String())
	}
	field("To", joinRecipients(m.ToRecipients))
	field("Cc", joinRecipients(m.CcRecipients))
	field("Date", formatFullDate(m.ReceivedDateTime))

	inline := 0
	var files []graph.Attachment
	for _, a := range m.Attachments {
		if a.IsInline {
			inline++
		} else {
			files = append(files, a)
		}
	}
	if len(files) > 0 || inline > 0 {
		b.WriteByte('\n')
	}
	for _, a := range files {
		b.WriteString(ansi.Wrap("📎 "+a.Name+styleFaint.Render(" ("+humanSize(a.Size)+")"), w, ""))
		b.WriteByte('\n')
	}
	if inline > 0 {
		noun := "item"
		if inline > 1 {
			noun = "items"
		}
		b.WriteString(styleFaint.Render(fmt.Sprintf("+%d inline %s", inline, noun)))
		b.WriteByte('\n')
	}

	b.WriteString(styleFaint.Render(strings.Repeat("─", w)))
	b.WriteString("\n\n")

	body, ok := v.bodies[m.ID]
	if !ok {
		if strings.EqualFold(m.Body.ContentType, "html") {
			body = htmltext.Convert(m.Body.Content)
		} else {
			body = strings.TrimSpace(strings.ReplaceAll(m.Body.Content, "\r\n", "\n"))
		}
		v.bodies[m.ID] = body
	}
	if body == "" {
		b.WriteString(styleFaint.Render("(no content)"))
	} else {
		b.WriteString(ansi.Wrap(body, w, ""))
	}
	return b.String()
}

func joinRecipients(rs []graph.Recipient) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = r.String()
	}
	return strings.Join(parts, ", ")
}

func indent(s, prefix string) string {
	if s == "" {
		return s
	}
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

const helpText = `
  Keys

  Message list
    ↑/k ↓/j        move
    pgup/pgdn      page up/down
    g/home G/end   first / last message
    enter, tab     focus the preview (opens full-screen in narrow terminals)
    esc            back to the mailbox picker

  Preview
    ↑/k ↓/j        scroll
    pgup/pgdn/spc  page up/down
    tab, esc       back to the list

  Actions (on the highlighted message)
    r              toggle read / unread
    d              move to Deleted Items
    D              delete permanently
    e              export as .eml

  General
    R              refresh the inbox
    m              switch mailbox
    c              configure mailboxes
    ?              this help
    q, ctrl+c      quit
`
