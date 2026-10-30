// Package pocket talks to sibling Pocket apps — here Pocket AI, which
// prepares a conversation with a coding agent from review comments. Same
// bearer-token JSON API everywhere.
package pocket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a bearer-token JSON client for one Pocket app.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New creates a client.
func New(baseURL, token string, timeout time.Duration) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: timeout}}
}

// Configured reports whether URL and token are set.
func (c *Client) Configured() bool { return c != nil && c.BaseURL != "" && c.Token != "" }

// ErrNotFound is a 404 from the sibling app.
var ErrNotFound = errors.New("not found")

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, v any) error {
	if !c.Configured() {
		return errors.New("not configured")
	}
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return errors.New("token rejected (401)")
	}
	if res.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		_ = json.Unmarshal(b, &e)
		if e.Error != "" {
			return fmt.Errorf("HTTP %d: %s", res.StatusCode, e.Error)
		}
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	if v == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(v)
}

// Health is the connection test for any Pocket app: /healthz plus a token check.
func (c *Client) Health(ctx context.Context) (string, error) {
	var out struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if !c.Configured() {
		return "", errors.New("not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/healthz", nil)
	if err != nil {
		return "", err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", res.StatusCode)
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	var me map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/v1/auth/me", nil, nil, &me); err != nil {
		return out.Version, err
	}
	return out.Version, nil
}

// ---- Pocket AI (docs/links.md there: threads:write) ----

// AgentHost is a machine Pocket AI can run an agent on (0 = its own).
type AgentHost struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// AgentOptions are the agents, logins and machines the person may use.
type AgentOptions struct {
	Providers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"providers"`
	Hosts           []AgentHost `json:"hosts"`
	DefaultProvider string      `json:"default_provider"`
	Home            string      `json:"home"`
	Roots           []string    `json:"roots"`
}

// AgentOptions reads what the person can choose from.
func (c *Client) AgentOptions(ctx context.Context) (*AgentOptions, error) {
	var o AgentOptions
	return &o, c.do(ctx, http.MethodGet, "/api/v1/link/agent-options", nil, nil, &o)
}

// Dirs is a folder listing on one machine.
type Dirs struct {
	Path   string `json:"path"`
	Parent string `json:"parent"`
	Dirs   []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Git  bool   `json:"git"`
	} `json:"dirs"`
	Roots   []string `json:"roots"`
	Home    string   `json:"home"`
	Places  []string `json:"places,omitempty"`
	Missing string   `json:"missing,omitempty"`
}

// Dirs lists the sub-folders of path on a machine (host 0 = Pocket AI's own).
func (c *Client) Dirs(ctx context.Context, host int64, path string) (*Dirs, error) {
	q := url.Values{}
	if host != 0 {
		q.Set("host", fmt.Sprint(host))
	}
	if path != "" {
		q.Set("path", path)
	}
	var d Dirs
	return &d, c.do(ctx, http.MethodGet, "/api/v1/link/dirs", q, nil, &d)
}

// Draft is a conversation prepared for the person; they read it and press send.
type Draft struct {
	Provider string         `json:"provider,omitempty"`
	HostID   int64          `json:"host_id,omitempty"`
	Mode     string         `json:"mode,omitempty"`
	Cwd      string         `json:"cwd"`
	Title    string         `json:"title"`
	Text     string         `json:"text"`
	System   string         `json:"system,omitempty"`
	Source   string         `json:"source"`
	Ref      string         `json:"ref,omitempty"`
	RefURL   string         `json:"ref_url,omitempty"`
	RefLabel string         `json:"ref_label,omitempty"`
	Worktree map[string]any `json:"worktree,omitempty"`
}

// CreateDraft prepares the conversation and returns where it opens.
func (c *Client) CreateDraft(ctx context.Context, d Draft) (id, openURL string, err error) {
	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	err = c.do(ctx, http.MethodPost, "/api/v1/link/drafts", nil, d, &out)
	return out.ID, out.URL, err
}

// AgentThread is a conversation an app started, as Pocket AI lists it.
type AgentThread struct {
	DraftID      string   `json:"draft_id"`
	ThreadID     *string  `json:"thread_id"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	URL          string   `json:"url"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	PullRequests []string `json:"pull_requests"`
	Worktree     string   `json:"worktree,omitempty"`
}

// Threads lists the conversations this app started for a ref ("" = all).
func (c *Client) Threads(ctx context.Context, source, ref string) ([]AgentThread, error) {
	q := url.Values{"source": {source}}
	if ref != "" {
		q.Set("ref", ref)
	}
	var out []AgentThread
	return out, c.do(ctx, http.MethodGet, "/api/v1/link/threads", q, nil, &out)
}

// AgentEdit is one file edit an agent made in a conversation.
type AgentEdit struct {
	At       string `json:"at"`
	Path     string `json:"path"` // relative to the repository
	Tool     string `json:"tool"`
	Note     string `json:"note,omitempty"` // what the agent said just before
	ThreadID string `json:"thread_id"`
}

// Edits are the file edits of the person's conversations that made a pull
// request (Pocket AI ≥ the version that offers threads:read). Older Pocket
// AI versions answer 404, which is not an error here.
func (c *Client) Edits(ctx context.Context, prURL string) ([]AgentEdit, error) {
	var out struct {
		Edits []AgentEdit `json:"edits"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/link/edits", url.Values{"pr": {prURL}}, nil, &out)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return out.Edits, err
}
