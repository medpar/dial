// Package httpx is a shared HTTP client with tight timeouts and helpers
// that never echo response bodies containing tokens.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var Client = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        8,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 8 * time.Second,
		ForceAttemptHTTP2:   true,
	},
}

// StatusError is a non-2xx response; Body is truncated and safe to show.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

// Do sends a request and decodes a JSON response into out (if non-nil).
func Do(ctx context.Context, method, url string, headers map[string]string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Status: resp.StatusCode, Body: safeSnippet(data)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// safeSnippet extracts an error message without leaking token fields.
func safeSnippet(data []byte) string {
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		for _, k := range []string{"error_description", "message", "detail"} {
			if s, ok := m[k].(string); ok {
				return trim(s)
			}
		}
		if e, ok := m["error"].(map[string]any); ok {
			if s, ok := e["message"].(string); ok {
				return trim(s)
			}
		}
		if s, ok := m["error"].(string); ok {
			return trim(s)
		}
		return ""
	}
	s := string(data)
	if strings.Contains(strings.ToLower(s), "token") {
		return ""
	}
	return trim(s)
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
