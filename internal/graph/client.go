// Package graph is a minimal Microsoft Graph client for a single mailbox,
// authenticated with the OAuth2 client credentials flow.
package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	DefaultGraphURL = "https://graph.microsoft.com/v1.0"
	defaultTokenURL = "https://login.microsoftonline.com/%s/oauth2/v2.0/token"
	graphScope      = "https://graph.microsoft.com/.default"

	// PageSize is the number of messages fetched per inbox page.
	PageSize = 50

	maxRetries    = 3
	maxRetryDelay = 60 * time.Second
)

const summaryFields = "id,subject,from,receivedDateTime,isRead,hasAttachments"

// Credentials identify the app registration used to access a mailbox.
type Credentials struct {
	TenantID     string
	ClientID     string
	ClientSecret string
}

// Options tweak the client; the zero value targets global Azure.
type Options struct {
	GraphURL   string       // default DefaultGraphURL
	TokenURL   string       // default login.microsoftonline.com for the tenant
	HTTPClient *http.Client // default: 60s timeout client

	// Sleep waits between retries; overridable for tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client accesses one mailbox.
type Client struct {
	mailbox  string
	graphURL string
	http     *http.Client
	sleep    func(ctx context.Context, d time.Duration) error
	ccConfig clientcredentials.Config

	mu sync.Mutex
	ts oauth2.TokenSource
}

// New creates a client for the given mailbox (UPN or SMTP address).
func New(mailbox string, creds Credentials, opts Options) *Client {
	if opts.GraphURL == "" {
		opts.GraphURL = DefaultGraphURL
	}
	if opts.TokenURL == "" {
		opts.TokenURL = fmt.Sprintf(defaultTokenURL, url.PathEscape(creds.TenantID))
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepCtx
	}
	return &Client{
		mailbox:  mailbox,
		graphURL: strings.TrimRight(opts.GraphURL, "/"),
		http:     opts.HTTPClient,
		sleep:    opts.Sleep,
		ccConfig: clientcredentials.Config{
			ClientID:     creds.ClientID,
			ClientSecret: creds.ClientSecret,
			TokenURL:     opts.TokenURL,
			Scopes:       []string{graphScope},
			AuthStyle:    oauth2.AuthStyleInParams,
		},
	}
}

// APIError is a non-success response from Graph.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" && e.Message == "" {
		return fmt.Sprintf("graph: HTTP %d", e.Status)
	}
	return fmt.Sprintf("graph: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

// AuthError means no access token could be obtained.
type AuthError struct {
	Err error
}

func (e *AuthError) Error() string {
	var re *oauth2.RetrieveError
	if errors.As(e.Err, &re) {
		desc := re.ErrorDescription
		if desc == "" {
			var body struct {
				Description string `json:"error_description"`
			}
			if json.Unmarshal(re.Body, &body) == nil {
				desc = body.Description
			}
		}
		if desc == "" {
			desc = re.ErrorCode
		}
		if desc == "" {
			desc = strings.TrimSpace(string(re.Body))
		}
		// AAD descriptions carry trace/correlation IDs after the first line.
		if i := strings.IndexAny(desc, "\r\n"); i >= 0 {
			desc = desc[:i]
		}
		return "authentication failed: " + desc
	}
	return "authentication failed: " + e.Err.Error()
}

func (e *AuthError) Unwrap() error { return e.Err }

// ListInbox returns the first page of the inbox, newest first.
func (c *Client) ListInbox(ctx context.Context) (Page, error) {
	u := c.mailboxURL("/mailFolders/inbox/messages") + "?" + query(
		"$top", strconv.Itoa(PageSize),
		"$orderby", "receivedDateTime desc",
		"$select", summaryFields,
	)
	return c.listPage(ctx, u)
}

// ListNext follows an @odata.nextLink returned by a previous page.
func (c *Client) ListNext(ctx context.Context, nextLink string) (Page, error) {
	// Never send the bearer token anywhere but Graph.
	if !strings.HasPrefix(nextLink, c.graphURL+"/") {
		return Page{}, fmt.Errorf("graph: refusing to follow foreign nextLink %q", nextLink)
	}
	return c.listPage(ctx, nextLink)
}

func (c *Client) listPage(ctx context.Context, u string) (Page, error) {
	data, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Page{}, err
	}
	var resp struct {
		Value    []MessageSummary `json:"value"`
		NextLink string           `json:"@odata.nextLink"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return Page{}, fmt.Errorf("graph: decoding message list: %w", err)
	}
	return Page{Messages: resp.Value, NextLink: resp.NextLink}, nil
}

// GetMessage fetches a message with body, recipients and attachment metadata.
// It does not change the message's read state.
func (c *Client) GetMessage(ctx context.Context, id string) (*Message, error) {
	u := c.messageURL(id, "") + "?" + query(
		"$select", summaryFields+",toRecipients,ccRecipients,body",
	)
	data, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("graph: decoding message: %w", err)
	}
	// hasAttachments is false when a message only has inline attachments,
	// so also look when the body references cid: images.
	if msg.HasAttachments || strings.Contains(msg.Body.Content, "cid:") {
		msg.Attachments, err = c.listAttachments(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	return &msg, nil
}

func (c *Client) listAttachments(ctx context.Context, id string) ([]Attachment, error) {
	u := c.messageURL(id, "/attachments") + "?" + query(
		"$select", "id,name,contentType,size,isInline",
	)
	data, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Value []Attachment `json:"value"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("graph: decoding attachments: %w", err)
	}
	return resp.Value, nil
}

// SetRead marks a message as read or unread.
func (c *Client) SetRead(ctx context.Context, id string, read bool) error {
	body, _ := json.Marshal(map[string]bool{"isRead": read})
	_, err := c.do(ctx, http.MethodPatch, c.messageURL(id, ""), body)
	return err
}

// MoveToDeletedItems soft-deletes a message by moving it to the Deleted Items
// folder. (A plain DELETE would bypass Deleted Items and land in Recoverable Items.)
func (c *Client) MoveToDeletedItems(ctx context.Context, id string) error {
	body, _ := json.Marshal(map[string]string{"destinationId": "deleteditems"})
	_, err := c.do(ctx, http.MethodPost, c.messageURL(id, "/move"), body)
	return err
}

// PermanentDelete purges a message; it cannot be recovered by the user.
func (c *Client) PermanentDelete(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, c.messageURL(id, "/permanentDelete"), nil)
	return err
}

// MIME returns the raw RFC 822 message, suitable for saving as .eml.
func (c *Client) MIME(ctx context.Context, id string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, c.messageURL(id, "/$value"), nil)
}

// TestConnection obtains a token and reads a single inbox message id.
func (c *Client) TestConnection(ctx context.Context) error {
	u := c.mailboxURL("/mailFolders/inbox/messages") + "?" + query("$top", "1", "$select", "id")
	_, err := c.do(ctx, http.MethodGet, u, nil)
	return err
}

func (c *Client) mailboxURL(path string) string {
	return c.graphURL + "/users/" + url.PathEscape(c.mailbox) + path
}

func (c *Client) messageURL(id, suffix string) string {
	return c.mailboxURL("/messages/" + url.PathEscape(id) + suffix)
}

// query builds an OData query string, encoding spaces as %20 rather than '+'.
func query(kv ...string) string {
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, kv[i]+"="+strings.ReplaceAll(url.QueryEscape(kv[i+1]), "+", "%20"))
	}
	return strings.Join(parts, "&")
}

func (c *Client) tokenSource() oauth2.TokenSource {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ts == nil {
		ctx := context.WithValue(context.Background(), oauth2.HTTPClient, c.http)
		c.ts = c.ccConfig.TokenSource(ctx)
	}
	return c.ts
}

func (c *Client) resetToken() {
	c.mu.Lock()
	c.ts = nil
	c.mu.Unlock()
}

// do performs a request and returns the body of a 2xx response. It refreshes
// the token once on 401 and retries throttling/transient errors with backoff.
func (c *Client) do(ctx context.Context, method, u string, body []byte) ([]byte, error) {
	refreshed := false
	retries := 0
	for {
		tok, err := c.tokenSource().Token()
		if err != nil {
			return nil, &AuthError{Err: err}
		}
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return data, nil
		}

		switch {
		case resp.StatusCode == http.StatusUnauthorized && !refreshed:
			refreshed = true
			c.resetToken()
			continue
		case retryable(resp.StatusCode) && retries < maxRetries:
			retries++
			if err := c.sleep(ctx, retryDelay(resp.Header.Get("Retry-After"), retries)); err != nil {
				return nil, err
			}
			continue
		}
		return nil, parseAPIError(resp.StatusCode, data)
	}
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// retryDelay honours Retry-After (seconds or HTTP date), else backs off 1s, 2s, 4s.
func retryDelay(header string, attempt int) time.Duration {
	d := time.Duration(1<<(attempt-1)) * time.Second
	if header != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
			d = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(header); err == nil {
			d = time.Until(t)
		}
	}
	return min(max(d, 0), maxRetryDelay)
}

func parseAPIError(status int, data []byte) *APIError {
	e := &APIError{Status: status}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil {
		e.Code = body.Error.Code
		e.Message = body.Error.Message
	}
	return e
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
