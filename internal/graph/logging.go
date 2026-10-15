package graph

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"time"
)

const maxLoggedErrorBody = 4096

// LoggingTransport logs method, URL, status and duration of every request.
// Response bodies are logged only for error responses (which carry Graph or
// AAD error details, never mail content). Headers, and therefore tokens and
// secrets, are never logged.
type LoggingTransport struct {
	Base   http.RoundTripper
	Logger *log.Logger
}

func (t *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	start := time.Now()
	target := req.URL.Host + req.URL.EscapedPath()
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}

	resp, err := base.RoundTrip(req)
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		t.Logger.Printf("%s %s -> error after %s: %v", req.Method, target, elapsed, err)
		return nil, err
	}
	t.Logger.Printf("%s %s -> %d (%s)", req.Method, target, resp.StatusCode, elapsed)

	if resp.StatusCode >= 400 {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, maxLoggedErrorBody))
		t.Logger.Printf("  error body: %s", bytes.TrimSpace(head))
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
	}
	return resp, nil
}
