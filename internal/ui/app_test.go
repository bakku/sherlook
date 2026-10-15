package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bakku.dev/sherlook/internal/config"
	"bakku.dev/sherlook/internal/graph"
)

type memKeyring map[string]string

func (k memKeyring) Get(a string) (string, error) {
	if s, ok := k[a]; ok {
		return s, nil
	}
	return "", config.ErrSecretNotFound
}
func (k memKeyring) Set(a, s string) error { k[a] = s; return nil }
func (k memKeyring) Delete(a string) error { delete(k, a); return nil }

func newTestApp(t *testing.T, addresses ...string) (*App, *fakeClient) {
	t.Helper()
	store, _, err := config.Open(filepath.Join(t.TempDir(), "config.toml"), memKeyring{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addresses {
		if _, err := store.Upsert("", config.Mailbox{Address: a, TenantID: "t", ClientID: "c"}, "s"); err != nil {
			t.Fatal(err)
		}
	}
	fc := &fakeClient{pages: map[string]graph.Page{"": {Messages: summaries(1)}}, failOps: map[string]error{}}
	return New(store, func(config.Mailbox, string) MailClient { return fc }, nil), fc
}

func TestStartupScreens(t *testing.T) {
	a, _ := newTestApp(t)
	a.Init()
	if a.screen != screenForm {
		t.Errorf("no mailboxes: screen = %v, want form", a.screen)
	}

	a, _ = newTestApp(t, "one@x.com")
	a.Init()
	if a.screen != screenMail || a.mail == nil {
		t.Errorf("one mailbox: screen = %v, want mail", a.screen)
	}

	a, _ = newTestApp(t, "one@x.com", "two@x.com")
	a.Init()
	if a.screen != screenPicker {
		t.Errorf("two mailboxes: screen = %v, want picker", a.screen)
	}
}

func TestAddMailboxRunsConnectionTest(t *testing.T) {
	a, fc := newTestApp(t)
	a.Init()
	_, cmd := a.Update(formSubmitMsg{mailbox: config.Mailbox{Address: "new@x.com", TenantID: "t", ClientID: "c"}, secret: "s"})
	if a.screen != screenConfig || len(a.mailboxes()) != 1 {
		t.Fatalf("screen=%v mailboxes=%v", a.screen, a.mailboxes())
	}
	for _, msg := range exec(cmd) {
		a.Update(msg)
	}
	if !fc.has("test") || !strings.Contains(a.st.text, "connection OK") {
		t.Errorf("calls=%v status=%q", fc.calls, a.st.text)
	}
}

func TestFormErrorKeepsForm(t *testing.T) {
	a, _ := newTestApp(t)
	a.Init()
	a.Update(formSubmitMsg{mailbox: config.Mailbox{Address: "bad", TenantID: "t", ClientID: "c"}, secret: "s"})
	if a.screen != screenForm || a.form.err == "" {
		t.Errorf("screen=%v err=%q", a.screen, a.form.err)
	}
}

func TestExportFilename(t *testing.T) {
	m := graph.MessageSummary{Subject: `  Re: Invoice 12/2026 <urgent>?  `, ReceivedDateTime: time.Date(2026, 10, 14, 12, 0, 0, 0, time.Local)}
	if got := exportFilename(m); got != "2026-10-14_Re_Invoice_12_2026_urgent.eml" {
		t.Errorf("got %q", got)
	}
	m.Subject = "///"
	if got := exportFilename(m); got != "2026-10-14_no-subject.eml" {
		t.Errorf("got %q", got)
	}
}

func TestFormatListDate(t *testing.T) {
	now := time.Date(2026, 10, 15, 18, 0, 0, 0, time.Local)
	cases := map[time.Time]string{
		time.Date(2026, 10, 15, 9, 5, 0, 0, time.Local): "09:05",
		time.Date(2026, 3, 1, 9, 5, 0, 0, time.Local):   "01 Mar",
		time.Date(2025, 12, 31, 9, 5, 0, 0, time.Local): "2025-12-31",
	}
	for in, want := range cases {
		if got := formatListDate(in, now); got != want {
			t.Errorf("formatListDate(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	for in, want := range map[int64]string{512: "512 B", 2048: "2.0 KB", 5 * 1024 * 1024: "5.0 MB"} {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}
