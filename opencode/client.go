// Package opencode talks to a running OpenCode v2 server over its HTTP API.
//
// The server is the same one the OpenCode apps (terminal, desktop, and OpenCode
// Mobile) connect to: it owns sessions, configuration, skills, credentials and
// tool execution. This package is the only place that speaks its protocol, so
// the web UI and the mobile pairing flow share one implementation.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is an HTTP client bound to one OpenCode server.
type Client struct {
	base string // e.g. http://127.0.0.1:4096
	pass string // service password, sent as HTTP basic auth
	http *http.Client
}

// New returns a client for baseURL authenticated with password.
func New(baseURL, password string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		pass: password,
		// No timeout: the event stream is long-lived. Per-call deadlines are
		// applied by the methods that need them.
		http: &http.Client{
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// BaseURL is the server origin.
func (c *Client) BaseURL() string { return c.base }

// Password is the credential used for API calls. It is also what a paired app
// receives as its session token, so it must never be rendered to the browser.
func (c *Client) Password() string { return c.pass }

// request performs one API call. body is JSON-encoded when non-nil.
func (c *Client) request(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.SetBasicAuth("opencode", c.pass)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%s %s: reading body: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: apiMessage(payload)}
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("%s %s: decoding response: %w", method, path, err)
		}
	}
	return nil
}

// APIError is a non-2xx response from the OpenCode server.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("opencode %s %s: %d %s", e.Method, e.Path, e.Status, e.Body)
}

// Unauthorized reports whether the server rejected our credential.
func (e *APIError) Unauthorized() bool { return e.Status == http.StatusUnauthorized }

// NotFound reports whether the resource is gone (e.g. a session was deleted).
func (e *APIError) NotFound() bool { return e.Status == http.StatusNotFound }

// apiMessage extracts the message from the server's error envelope.
func apiMessage(b []byte) string {
	var e struct {
		Tag     string `json:"_tag"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil {
		if e.Message != "" {
			return e.Message
		}
		if e.Error != "" {
			return e.Error
		}
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// Info is the server identity from GET /api/info.
type Info struct {
	Version string   `json:"version"`
	PID     int      `json:"pid"`
	URLs    []string `json:"urls"`
}

// Info returns the server version and reachable URLs.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out Info
	if err := c.request(ctx, http.MethodGet, "/api/info", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Location identifies the project directory a session runs in. Every session,
// agent and skill lookup is scoped by it, which is how a clone on this server
// gets its own model and role.
type Location struct {
	Directory string `json:"directory"`
}

// Session is an OpenCode session.
type Session struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Project  string   `json:"projectID"`
	Agent    string   `json:"agent"`
	Time     TimeInfo `json:"time"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

// TimeInfo holds session timestamps in Unix milliseconds.
type TimeInfo struct {
	Created int64 `json:"created"`
	Updated int64 `json:"updated"`
	Busy    int64 `json:"busy"`
	Idle    int64 `json:"idle"`
}

// Updated is the timestamp the UI sorts by (ms).
func (s Session) Updated() int64 {
	if s.Time.Updated != 0 {
		return s.Time.Updated
	}
	return s.Time.Created
}

// CreateSession opens a session in dir, optionally naming it.
func (c *Client) CreateSession(ctx context.Context, dir, title string) (*Session, error) {
	body := map[string]any{"location": Location{Directory: dir}}
	if title != "" {
		body["title"] = title
	}
	var out struct {
		Data Session `json:"data"`
	}
	if err := c.request(ctx, http.MethodPost, "/api/session", nil, body, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// ListSessions returns recent sessions for dir, newest first.
func (c *Client) ListSessions(ctx context.Context, dir string, limit int) ([]Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	q := url.Values{}
	q.Set("order", "desc")
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	if dir != "" {
		q.Set("directory", dir)
	}
	var out struct {
		Data []Session `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/session", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Prompt sends one user turn to a session. resume=false admits the message
// without starting the agent loop; the default starts it.
func (c *Client) Prompt(ctx context.Context, sessionID, text string) error {
	body := map[string]any{"text": text}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.request(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sessionID)+"/prompt", nil, body, nil)
}

// Interrupt stops the running turn for a session.
func (c *Client) Interrupt(ctx context.Context, sessionID string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.request(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sessionID)+"/interrupt", nil, map[string]any{}, nil)
}

// DeleteSession removes a session.
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := c.request(ctx, http.MethodDelete, "/api/session/"+url.PathEscape(sessionID), nil, nil, nil)
	if e, ok := err.(*APIError); ok && e.NotFound() {
		return nil
	}
	return err
}

// RenameSession sets a session title.
func (c *Client) RenameSession(ctx context.Context, sessionID, title string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.request(ctx, http.MethodPatch, "/api/session/"+url.PathEscape(sessionID), nil,
		map[string]any{"title": title}, nil)
}

// Skill is one skill the server made available to a location.
type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Skills lists the skills visible in dir. Local skills deployed into the
// OpenCode config directory show up here once the server reloads config.
func (c *Client) Skills(ctx context.Context, dir string) ([]Skill, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	q := url.Values{}
	if dir != "" {
		q.Set("location[directory]", dir)
	}
	var out struct {
		Data []Skill `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/skill", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Model is one model the server can run.
type Model struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name"`
}

// Models lists the chat models the server knows about.
func (c *Client) Models(ctx context.Context, dir string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	q := url.Values{}
	if dir != "" {
		q.Set("location[directory]", dir)
	}
	var out struct {
		Data []Model `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/model", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// CredentialKind is the shape of a stored provider credential.
const CredentialKindKey = "key"

// CredentialInput stores a provider API key.
type CredentialInput struct {
	IntegrationID string
	Label         string
	Key           string
	Activate      bool
}

// Credential is a stored provider credential returned by the server. The key
// itself is deliberately never logged by this package.
type Credential struct {
	ID            string `json:"id"`
	IntegrationID string `json:"integrationID"`
	Label         string `json:"label"`
	Active        bool   `json:"active"`
}

// SaveCredential stores (or replaces) an API key for a provider integration,
// e.g. integrationID "openrouter" with the key from the deployment env.
func (c *Client) SaveCredential(ctx context.Context, in CredentialInput) (*Credential, error) {
	body := map[string]any{
		"integrationID": in.IntegrationID,
		"activate":      in.Activate,
		"value":         map[string]any{"type": CredentialKindKey, "key": in.Key},
	}
	if in.Label != "" {
		body["label"] = in.Label
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out struct {
		Data Credential `json:"data"`
	}
	if err := c.request(ctx, http.MethodPost, "/api/credential", nil, body, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Credentials lists stored provider credentials (secrets are not decoded).
func (c *Client) Credentials(ctx context.Context) ([]Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out struct {
		Data []Credential `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/credential", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Reload asks the server to re-read configuration files, so skills and agents
// written to the config directory apply to the next session.
func (c *Client) Reload(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.request(ctx, http.MethodPost, "/api/location/reload", nil, map[string]any{}, nil)
}

// PairingCode is a one-time code for a /auth/connect/<code> link.
type PairingCode struct {
	Code      string `json:"code"`
	ExpiresIn int    `json:"expires_in"`
}

// CreatePairing requests a one-time pairing code.
func (c *Client) CreatePairing(ctx context.Context) (*PairingCode, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var out PairingCode
	if err := c.request(ctx, http.MethodPost, "/api/pair", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
