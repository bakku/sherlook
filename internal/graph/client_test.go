package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGraph struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	tokens   int
	requests []*http.Request
	bodies   []string
	handler  func(w http.ResponseWriter, r *http.Request)
}

func newFake(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*fakeGraph, *Client, *[]time.Duration) {
	t.Helper()
	f := &fakeGraph{t: t, handler: handler}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	var sleeps []time.Duration
	c := New("Support@Contoso.com", Credentials{TenantID: "tid", ClientID: "cid", ClientSecret: "s3cret"}, Options{
		GraphURL: f.srv.URL + "/v1.0",
		TokenURL: f.srv.URL + "/token",
		Sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
	})
	return f, c, &sleeps
}

func (f *fakeGraph) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" ||
			r.Form.Get("client_id") != "cid" ||
			r.Form.Get("client_secret") != "s3cret" ||
			r.Form.Get("scope") != graphScope {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided.\r\nTrace ID: x"}`)
			return
		}
		f.mu.Lock()
		f.tokens++
		n := f.tokens
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"tok%d","token_type":"Bearer","expires_in":3600}`, n)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, string(body))
	f.mu.Unlock()
	f.handler(w, r)
}

func (f *fakeGraph) last() (*http.Request, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		f.t.Fatal("no graph requests recorded")
	}
	return f.requests[len(f.requests)-1], f.bodies[len(f.bodies)-1]
}

func TestListInbox(t *testing.T) {
	var srvURL string
	f, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"value":[{"id":"m1","subject":"Hello","from":{"emailAddress":{"name":"Alice","address":"alice@x.com"}},"receivedDateTime":"2026-10-14T08:00:00Z","isRead":false,"hasAttachments":true}],"@odata.nextLink":"%s/v1.0/users/x/next?$skiptoken=abc"}`, srvURL)
	})
	srvURL = f.srv.URL

	page, err := c.ListInbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r, _ := f.last()
	if r.URL.EscapedPath() != "/v1.0/users/Support@Contoso.com/mailFolders/inbox/messages" {
		t.Errorf("path = %s", r.URL.EscapedPath())
	}
	if !strings.Contains(r.URL.RawQuery, "$orderby=receivedDateTime%20desc") || !strings.Contains(r.URL.RawQuery, "$top=50") {
		t.Errorf("query = %s", r.URL.RawQuery)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer tok1" {
		t.Errorf("auth header = %q", got)
	}
	if len(page.Messages) != 1 || page.Messages[0].Subject != "Hello" || !page.Messages[0].HasAttachments || page.Messages[0].From.ShortName() != "Alice" {
		t.Errorf("unexpected page %+v", page)
	}
	if !strings.HasSuffix(page.NextLink, "/next?$skiptoken=abc") {
		t.Errorf("nextLink = %q", page.NextLink)
	}

	if _, err := c.ListNext(context.Background(), page.NextLink); err != nil {
		t.Errorf("following own nextLink: %v", err)
	}
	if _, err := c.ListNext(context.Background(), "https://evil.example.com/v1.0/x"); err == nil {
		t.Error("expected foreign nextLink to be refused")
	}
}

func TestGetMessageFetchesAttachments(t *testing.T) {
	f, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/attachments"):
			if !strings.Contains(r.URL.RawQuery, "isInline") || strings.Contains(r.URL.RawQuery, "contentBytes") {
				t.Errorf("attachment query = %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"value":[{"id":"a1","name":"report.pdf","size":2048,"isInline":false},{"id":"a2","name":"logo.png","size":10,"isInline":true}]}`)
		default:
			fmt.Fprint(w, `{"id":"m/1","subject":"S","hasAttachments":true,"body":{"contentType":"html","content":"<p>hi</p>"},"toRecipients":[{"emailAddress":{"address":"a@b.c"}}]}`)
		}
	})
	msg, err := c.GetMessage(context.Background(), "m/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Attachments) != 2 || msg.Attachments[0].Name != "report.pdf" || !msg.Attachments[1].IsInline {
		t.Errorf("attachments = %+v", msg.Attachments)
	}
	if msg.Body.ContentType != "html" || len(msg.ToRecipients) != 1 {
		t.Errorf("message = %+v", msg)
	}
	f.mu.Lock()
	first := f.requests[0]
	f.mu.Unlock()
	if !strings.Contains(first.URL.EscapedPath(), "/messages/m%2F1") {
		t.Errorf("message id not escaped: %s", first.URL.EscapedPath())
	}
}

func TestGetMessageSkipsAttachmentsWhenNone(t *testing.T) {
	f, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"m1","hasAttachments":false,"body":{"contentType":"text","content":"plain"}}`)
	})
	if _, err := c.GetMessage(context.Background(), "m1"); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1 {
		t.Errorf("expected 1 request, got %d", len(f.requests))
	}
}

func TestActions(t *testing.T) {
	f, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/$value") {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "From: a@b.c\r\nSubject: x\r\n\r\nbody")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})
	ctx := context.Background()

	tests := []struct {
		name       string
		call       func() error
		method     string
		pathSuffix string
		body       map[string]any
	}{
		{"mark read", func() error { return c.SetRead(ctx, "m1", true) }, http.MethodPatch, "/messages/m1", map[string]any{"isRead": true}},
		{"mark unread", func() error { return c.SetRead(ctx, "m1", false) }, http.MethodPatch, "/messages/m1", map[string]any{"isRead": false}},
		{"soft delete", func() error { return c.MoveToDeletedItems(ctx, "m1") }, http.MethodPost, "/messages/m1/move", map[string]any{"destinationId": "deleteditems"}},
		{"permanent delete", func() error { return c.PermanentDelete(ctx, "m1") }, http.MethodPost, "/messages/m1/permanentDelete", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err != nil {
				t.Fatal(err)
			}
			r, body := f.last()
			if r.Method != tt.method || !strings.HasSuffix(r.URL.Path, tt.pathSuffix) {
				t.Errorf("got %s %s", r.Method, r.URL.Path)
			}
			if tt.body != nil {
				var got map[string]any
				if err := json.Unmarshal([]byte(body), &got); err != nil {
					t.Fatalf("body %q: %v", body, err)
				}
				if fmt.Sprint(got) != fmt.Sprint(tt.body) {
					t.Errorf("body = %v, want %v", got, tt.body)
				}
			}
		})
	}

	mime, err := c.MIME(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mime), "From: a@b.c") {
		t.Errorf("mime = %q", mime)
	}
}

func TestRetriesThrottling(t *testing.T) {
	calls := 0
	_, c, sleeps := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			if calls == 1 {
				w.Header().Set("Retry-After", "7")
			}
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":"TooManyRequests","message":"slow down"}}`)
			return
		}
		fmt.Fprint(w, `{"value":[]}`)
	})
	if _, err := c.ListInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("calls = %d", calls)
	}
	if want := []time.Duration{7 * time.Second, 2 * time.Second}; fmt.Sprint(*sleeps) != fmt.Sprint(want) {
		t.Errorf("sleeps = %v, want %v", *sleeps, want)
	}
}

func TestRetriesGiveUp(t *testing.T) {
	calls := 0
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err := c.ListInbox(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v", err)
	}
	if calls != 1+maxRetries {
		t.Errorf("calls = %d", calls)
	}
}

func TestRefreshesTokenOn401(t *testing.T) {
	var auths []string
	f, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		if len(auths) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"value":[]}`)
	})
	if err := c.TestConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(auths) != "[Bearer tok1 Bearer tok2]" || f.tokens != 2 {
		t.Errorf("auths = %v, tokens = %d", auths, f.tokens)
	}
}

func TestAPIError(t *testing.T) {
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":"ErrorAccessDenied","message":"Access is denied."}}`)
	})
	err := c.TestConnection(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 || apiErr.Code != "ErrorAccessDenied" {
		t.Fatalf("err = %#v", err)
	}
}

func TestAuthError(t *testing.T) {
	f := &fakeGraph{t: t, handler: func(w http.ResponseWriter, r *http.Request) {}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	defer f.srv.Close()
	c := New("x@y.z", Credentials{TenantID: "tid", ClientID: "cid", ClientSecret: "wrong"}, Options{
		GraphURL: f.srv.URL + "/v1.0", TokenURL: f.srv.URL + "/token",
	})
	err := c.TestConnection(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %#v", err)
	}
	if got := err.Error(); got != "authentication failed: AADSTS7000215: Invalid client secret provided." {
		t.Errorf("message = %q", got)
	}
}

func TestRetryDelay(t *testing.T) {
	if d := retryDelay("", 3); d != 4*time.Second {
		t.Errorf("backoff = %v", d)
	}
	if d := retryDelay("9999", 1); d != maxRetryDelay {
		t.Errorf("capped = %v", d)
	}
}
