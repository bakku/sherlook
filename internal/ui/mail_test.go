package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"bakku.dev/sherlook/internal/config"
	"bakku.dev/sherlook/internal/graph"
)

type fakeClient struct {
	mu      sync.Mutex
	pages   map[string]graph.Page // "" = first page, else nextLink
	calls   []string
	failOps map[string]error
	mime    []byte
}

func (f *fakeClient) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	op := strings.SplitN(call, " ", 2)[0]
	return f.failOps[op]
}

func (f *fakeClient) ListInbox(context.Context) (graph.Page, error) {
	return f.pages[""], f.record("list")
}
func (f *fakeClient) ListNext(_ context.Context, link string) (graph.Page, error) {
	return f.pages[link], f.record("next " + link)
}
func (f *fakeClient) GetMessage(_ context.Context, id string) (*graph.Message, error) {
	err := f.record("get " + id)
	return &graph.Message{
		MessageSummary: graph.MessageSummary{ID: id, Subject: "Subject " + id},
		Body:           graph.ItemBody{ContentType: "html", Content: "<p>Body of " + id + "</p>"},
		Attachments:    []graph.Attachment{{Name: "report.pdf", Size: 2048}, {Name: "logo.png", IsInline: true}},
	}, err
}
func (f *fakeClient) SetRead(_ context.Context, id string, read bool) error {
	return f.record(fmt.Sprintf("read %s %v", id, read))
}
func (f *fakeClient) MoveToDeletedItems(_ context.Context, id string) error {
	return f.record("move " + id)
}
func (f *fakeClient) PermanentDelete(_ context.Context, id string) error {
	return f.record("purge " + id)
}
func (f *fakeClient) MIME(_ context.Context, id string) ([]byte, error) {
	return f.mime, f.record("mime " + id)
}
func (f *fakeClient) TestConnection(context.Context) error { return f.record("test") }

func (f *fakeClient) has(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func summaries(n int) []graph.MessageSummary {
	out := make([]graph.MessageSummary, n)
	for i := range out {
		out[i] = graph.MessageSummary{
			ID:               fmt.Sprintf("m%d", i+1),
			Subject:          fmt.Sprintf("Subject %d", i+1),
			From:             &graph.Recipient{EmailAddress: graph.EmailAddress{Name: "Alice", Address: "alice@x.com"}},
			ReceivedDateTime: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC),
		}
	}
	return out
}

// exec runs a command, executing batches recursively and skipping debounce ticks.
func exec(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, exec(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// pump feeds a command's results back into the view until it settles.
func pump(v *mailView, cmd tea.Cmd) {
	for i := 0; cmd != nil && i < 10; i++ {
		var next []tea.Cmd
		for _, msg := range exec(cmd) {
			next = append(next, v.Update(msg))
		}
		cmd = tea.Batch(next...)
	}
}

func press(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func newTestView(t *testing.T, n int, width int) (*mailView, *fakeClient) {
	t.Helper()
	fc := &fakeClient{pages: map[string]graph.Page{"": {Messages: summaries(n)}}, failOps: map[string]error{}}
	v := newMailView(config.Mailbox{Address: "support@contoso.com", Label: "Support"}, fc, t.TempDir(), &status{}, width, 30)
	pump(v, v.Init())
	return v, fc
}

func ids(v *mailView) string {
	var s []string
	for _, m := range v.msgs {
		s = append(s, m.ID)
	}
	return strings.Join(s, ",")
}

func TestLoadsInboxAndPreview(t *testing.T) {
	v, fc := newTestView(t, 3, 120)
	if ids(v) != "m1,m2,m3" || v.cursor != 0 {
		t.Fatalf("msgs=%s cursor=%d", ids(v), v.cursor)
	}
	if !fc.has("get m1") || v.cache["m1"] == nil {
		t.Fatalf("preview not loaded: calls=%v", fc.calls)
	}
	out := ansi.Strip(v.View())
	for _, want := range []string{"Support", "Subject 1", "Body of m1", "📎 report.pdf (2.0 KB)", "+1 inline item", "3 messages"} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q:\n%s", want, out)
		}
	}
	if fc.has("read m1 true") {
		t.Error("opening a message must not mark it read")
	}
}

func TestInfiniteScroll(t *testing.T) {
	fc := &fakeClient{pages: map[string]graph.Page{
		"":      {Messages: summaries(12), NextLink: "page2"},
		"page2": {Messages: append(summaries(12)[11:], graph.MessageSummary{ID: "m13"})},
	}, failOps: map[string]error{}}
	v := newMailView(config.Mailbox{Address: "a@b.c"}, fc, "", &status{}, 120, 30)
	pump(v, v.Init())
	if fc.has("next page2") {
		t.Fatal("next page must not load while the cursor is far from the end")
	}
	pump(v, v.Update(press("j")))
	pump(v, v.Update(press("j")))
	if !fc.has("next page2") {
		t.Fatalf("expected next page to be fetched, calls=%v", fc.calls)
	}
	if ids(v) != "m1,m2,m3,m4,m5,m6,m7,m8,m9,m10,m11,m12,m13" {
		t.Errorf("msgs=%s (duplicates must be dropped)", ids(v))
	}
	if v.next != "" || v.loadingMore {
		t.Errorf("next=%q loadingMore=%v", v.next, v.loadingMore)
	}
}

func TestSoftDeleteMovesCursorToNext(t *testing.T) {
	v, fc := newTestView(t, 3, 120)
	pump(v, v.Update(press("j")))
	pump(v, v.Update(press("d")))
	if v.modal != modalDelete {
		t.Fatal("expected delete confirmation")
	}
	pump(v, v.Update(press("y")))
	if !fc.has("move m2") {
		t.Fatalf("calls=%v", fc.calls)
	}
	if ids(v) != "m1,m3" || v.cursor != 1 {
		t.Errorf("msgs=%s cursor=%d", ids(v), v.cursor)
	}
	if !strings.Contains(v.st.text, "Deleted Items") {
		t.Errorf("status=%q", v.st.text)
	}
}

func TestPermanentDeleteLastMovesCursorUp(t *testing.T) {
	v, fc := newTestView(t, 3, 120)
	pump(v, v.Update(press("G")))
	pump(v, v.Update(press("D")))
	if v.modal != modalPurge || !strings.Contains(ansi.Strip(v.View()), "cannot be undone") {
		t.Fatal("expected permanent delete warning")
	}
	pump(v, v.Update(press("y")))
	if !fc.has("purge m3") || ids(v) != "m1,m2" || v.cursor != 1 {
		t.Errorf("calls=%v msgs=%s cursor=%d", fc.calls, ids(v), v.cursor)
	}
}

func TestDeleteDeclinedAndFailed(t *testing.T) {
	v, fc := newTestView(t, 2, 120)
	pump(v, v.Update(press("d")))
	pump(v, v.Update(press("n")))
	if fc.has("move m1") || v.modal != modalNone {
		t.Fatal("declined delete must not call Graph")
	}

	fc.failOps["move"] = &graph.APIError{Status: 403, Code: "ErrorAccessDenied"}
	pump(v, v.Update(press("d")))
	pump(v, v.Update(press("y")))
	if ids(v) != "m1,m2" {
		t.Errorf("failed delete must keep message, msgs=%s", ids(v))
	}
	if v.st.level != levelError || !strings.Contains(v.st.text, "access denied") {
		t.Errorf("status=%q", v.st.text)
	}
}

func TestToggleRead(t *testing.T) {
	v, fc := newTestView(t, 2, 120)
	pump(v, v.Update(press("r")))
	if !fc.has("read m1 true") || !v.msgs[0].IsRead {
		t.Fatalf("calls=%v read=%v", fc.calls, v.msgs[0].IsRead)
	}
	pump(v, v.Update(press("r")))
	if !fc.has("read m1 false") || v.msgs[0].IsRead {
		t.Fatalf("calls=%v read=%v", fc.calls, v.msgs[0].IsRead)
	}
}

func TestStaleResultsIgnored(t *testing.T) {
	v, _ := newTestView(t, 2, 120)
	other := &graph.Message{MessageSummary: graph.MessageSummary{ID: "m2"}}
	v.Update(messageMsg{view: v.id + 1, id: "m2", msg: other})
	if v.cache["m2"] != nil {
		t.Error("result from another view instance must be ignored")
	}
	v.Update(pageMsg{view: v.id, gen: v.gen - 1, page: graph.Page{}})
	if len(v.msgs) != 2 {
		t.Error("stale page generation must be ignored")
	}
	if cmd := v.Update(previewTickMsg{view: v.id, seq: v.previewSeq - 1}); cmd != nil {
		t.Error("stale debounce tick must not fetch")
	}
}

func TestNarrowLayout(t *testing.T) {
	v, _ := newTestView(t, 2, 80)
	out := ansi.Strip(v.View())
	if strings.Contains(out, "Body of m1") || !strings.Contains(out, "Subject 1") {
		t.Fatalf("narrow list view should not show the preview:\n%s", out)
	}
	pump(v, v.Update(press("enter")))
	out = ansi.Strip(v.View())
	if !strings.Contains(out, "Body of m1") || strings.Contains(out, "│") {
		t.Fatalf("enter should open the message full-screen:\n%s", out)
	}
	v.setSize(140, 30)
	out = ansi.Strip(v.View())
	if !strings.Contains(out, "│") || !strings.Contains(out, "Body of m1") {
		t.Fatalf("widening should restore the split view:\n%s", out)
	}
	pump(v, v.Update(press("esc")))
	if v.focus != focusList {
		t.Error("esc in preview should return to the list")
	}
	if cmd := v.Update(press("esc")); cmd == nil {
		t.Error("esc in list should navigate to the picker")
	} else if _, ok := cmd().(showPickerMsg); !ok {
		t.Error("expected showPickerMsg")
	}
}

func TestExport(t *testing.T) {
	v, fc := newTestView(t, 1, 120)
	fc.mime = []byte("From: a@b.c\r\n\r\nhi")

	pump(v, v.Update(press("e")))
	if v.modal != modalExportPath {
		t.Fatal("expected export prompt")
	}
	if want := filepath.Join(v.exportDir, "2026-10-14_Subject_1.eml"); v.input.Value() != want {
		t.Errorf("default path = %q, want %q", v.input.Value(), want)
	}
	path := v.input.Value()
	pump(v, v.Update(press("enter")))
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(fc.mime) {
		t.Fatalf("exported %q, %v", data, err)
	}

	// Exporting again asks before overwriting; declining keeps the file.
	_ = os.WriteFile(path, []byte("keep"), 0o600)
	pump(v, v.Update(press("e")))
	pump(v, v.Update(press("enter")))
	if v.modal != modalOverwrite {
		t.Fatal("expected overwrite confirmation")
	}
	pump(v, v.Update(press("n")))
	if data, _ := os.ReadFile(path); string(data) != "keep" {
		t.Error("declined overwrite must keep the file")
	}
}

func TestPreviewErrorShown(t *testing.T) {
	fc := &fakeClient{pages: map[string]graph.Page{"": {Messages: summaries(1)}}, failOps: map[string]error{
		"get": errors.New("boom"),
	}}
	v := newMailView(config.Mailbox{Address: "a@b.c"}, fc, "", &status{}, 120, 30)
	pump(v, v.Init())
	if out := ansi.Strip(v.View()); !strings.Contains(out, "Could not load message") || !strings.Contains(out, "boom") {
		t.Errorf("view:\n%s", out)
	}
}
