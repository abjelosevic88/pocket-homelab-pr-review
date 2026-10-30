// Package github is the small part of the GitHub REST API Pocket PR Review
// needs: finding pull requests, reading their diff and commits, and sending
// a review back. Every call runs with one person's own token.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultAPI is github.com's API.
const DefaultAPI = "https://api.github.com"

// Client talks to GitHub as one person.
type Client struct {
	Base  string
	Token string
	UA    string
	HTTP  *http.Client
}

// New makes a client; base "" = github.com.
func New(base, token, ua string, timeout time.Duration) *Client {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = DefaultAPI
	}
	if ua == "" {
		ua = "PocketPRReview"
	}
	return &Client{Base: base, Token: strings.TrimSpace(token), UA: ua, HTTP: &http.Client{Timeout: timeout}}
}

// Error is an answer GitHub refused.
type Error struct {
	Status  int
	Message string
	Details []string
}

func (e *Error) Error() string {
	msg := e.Message
	if len(e.Details) > 0 {
		msg += ": " + strings.Join(e.Details, "; ")
	}
	switch e.Status {
	case http.StatusUnauthorized:
		return "GitHub did not accept the token (" + msg + ")"
	case http.StatusForbidden:
		if strings.Contains(strings.ToLower(msg), "rate limit") {
			return "GitHub's rate limit is used up for now; try again in a few minutes"
		}
		return "GitHub refused: " + msg + " (does the token have access to this repository?)"
	case http.StatusNotFound:
		return "GitHub has no such thing, or the token cannot see it"
	}
	return fmt.Sprintf("GitHub answered %d: %s", e.Status, msg)
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) (http.Header, error) {
	if c.Token == "" {
		return nil, errors.New("no GitHub token (Settings → GitHub)")
	}
	u := path
	if !strings.HasPrefix(path, "http") {
		u = c.Base + path
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.UA)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub did not answer: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
			Errors  []any  `json:"errors"`
		}
		_ = json.Unmarshal(b, &e)
		ge := &Error{Status: res.StatusCode, Message: e.Message}
		if ge.Message == "" {
			ge.Message = strings.TrimSpace(string(b))
			if len(ge.Message) > 200 {
				ge.Message = ge.Message[:200]
			}
		}
		for _, d := range e.Errors {
			switch v := d.(type) {
			case string:
				ge.Details = append(ge.Details, v)
			case map[string]any:
				if m, ok := v["message"].(string); ok {
					ge.Details = append(ge.Details, m)
				} else if f, ok := v["field"].(string); ok {
					ge.Details = append(ge.Details, fmt.Sprint(f, " ", v["code"]))
				}
			}
		}
		return res.Header, ge
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return res.Header, fmt.Errorf("GitHub sent something unexpected: %w", err)
		}
	}
	return res.Header, nil
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// pages follows "next" links, up to max pages.
func pages[T any](ctx context.Context, c *Client, path string, max int) ([]T, error) {
	var out []T
	for i := 0; i < max && path != ""; i++ {
		var page []T
		h, err := c.do(ctx, http.MethodGet, path, nil, &page)
		if err != nil {
			return out, err
		}
		out = append(out, page...)
		path = ""
		if m := nextLink.FindStringSubmatch(h.Get("Link")); m != nil {
			path = m[1]
		}
	}
	return out, nil
}

// ---- people ----

// User is a GitHub account.
type User struct {
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
}

// Me is who the token belongs to.
func (c *Client) Me(ctx context.Context) (*User, error) {
	var u User
	_, err := c.do(ctx, http.MethodGet, "/user", nil, &u)
	return &u, err
}

// ---- repositories ----

// Repo is a repository the token reaches.
type Repo struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	Archived bool   `json:"archived"`
	PushedAt string `json:"pushed_at"`
	Owner    struct {
		Login string `json:"login"`
		Type  string `json:"type"` // User | Organization
	} `json:"owner"`
}

// Repos lists the repositories the person can see, the most recently pushed first (up to 300).
func (c *Client) Repos(ctx context.Context) ([]Repo, error) {
	return pages[Repo](ctx, c, "/user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member", 3)
}

// ---- finding pull requests ----

// SearchItem is a pull request as the search finds it.
type SearchItem struct {
	Number        int    `json:"number"`
	Title         string `json:"title"`
	HTMLURL       string `json:"html_url"`
	State         string `json:"state"`
	Draft         bool   `json:"draft"`
	UpdatedAt     string `json:"updated_at"`
	RepositoryURL string `json:"repository_url"`
	User          User   `json:"user"`
	PullRequest   struct {
		MergedAt *string `json:"merged_at"`
	} `json:"pull_request"`
}

// Repo is "owner/name" from the item's repository URL.
func (it SearchItem) Repo() string {
	i := strings.Index(it.RepositoryURL, "/repos/")
	if i < 0 {
		return ""
	}
	return strings.ToLower(it.RepositoryURL[i+len("/repos/"):])
}

// Search finds pull requests (q in GitHub's search syntax), newest first.
func (c *Client) Search(ctx context.Context, q string, max int) ([]SearchItem, error) {
	var out []SearchItem
	for page := 1; len(out) < max && page <= 10; page++ {
		var res struct {
			Items []SearchItem `json:"items"`
		}
		path := fmt.Sprintf("/search/issues?q=%s&sort=updated&order=desc&per_page=100&page=%d", url.QueryEscape(q), page)
		if _, err := c.do(ctx, http.MethodGet, path, nil, &res); err != nil {
			return out, err
		}
		out = append(out, res.Items...)
		if len(res.Items) < 100 {
			break
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// ---- one pull request ----

// Branch is one side of a pull request.
type Branch struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

// Pull is a pull request in full.
type Pull struct {
	Number       int     `json:"number"`
	Title        string  `json:"title"`
	Body         string  `json:"body"`
	HTMLURL      string  `json:"html_url"`
	State        string  `json:"state"`
	Draft        bool    `json:"draft"`
	Merged       bool    `json:"merged"`
	MergedAt     *string `json:"merged_at"`
	UpdatedAt    string  `json:"updated_at"`
	User         User    `json:"user"`
	Base         Branch  `json:"base"`
	Head         Branch  `json:"head"`
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	ChangedFiles int     `json:"changed_files"`
	Commits      int     `json:"commits"`
}

// StateName is open, closed or merged.
func (p *Pull) StateName() string {
	if p.Merged || p.MergedAt != nil {
		return "merged"
	}
	return p.State
}

// GetPull reads a pull request.
func (c *Client) GetPull(ctx context.Context, repo string, n int) (*Pull, error) {
	var p Pull
	_, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, n), nil, &p)
	return &p, err
}

// File is one changed file with its diff (GitHub leaves patch out of very
// large ones).
type File struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Changes          int    `json:"changes"`
	Patch            string `json:"patch"`
}

// Files lists a pull request's changed files (GitHub stops at 3000).
func (c *Client) Files(ctx context.Context, repo string, n int) ([]File, error) {
	return pages[File](ctx, c, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100", repo, n), 30)
}

// Commit is one commit of a pull request.
type Commit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"author"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
	Author *User  `json:"author"`
	Files  []File `json:"files"`
}

// Commits lists a pull request's commits, oldest first (up to 250).
func (c *Client) Commits(ctx context.Context, repo string, n int) ([]Commit, error) {
	return pages[Commit](ctx, c, fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=100", repo, n), 3)
}

// GetCommit reads one commit with its files.
func (c *Client) GetCommit(ctx context.Context, repo, sha string) (*Commit, error) {
	var cm Commit
	_, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s", repo, sha), nil, &cm)
	return &cm, err
}

// FileAt reads a file at a ref; nil when there is none.
func (c *Client) FileAt(ctx context.Context, repo, path, ref string) ([]byte, error) {
	var res struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	_, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/contents/%s?ref=%s", repo, path, url.QueryEscape(ref)), nil, &res)
	if IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if res.Encoding != "base64" {
		return []byte(res.Content), nil
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(res.Content, "\n", ""))
}

// ---- reviews ----

// ReviewInfo is a review someone sent.
type ReviewInfo struct {
	ID       int64  `json:"id"`
	User     User   `json:"user"`
	State    string `json:"state"` // APPROVED | CHANGES_REQUESTED | COMMENTED | DISMISSED | PENDING
	CommitID string `json:"commit_id"`
	Body     string `json:"body"`
	HTMLURL  string `json:"html_url"`
	At       string `json:"submitted_at"`
}

// Reviews lists a pull request's reviews, oldest first.
func (c *Client) Reviews(ctx context.Context, repo string, n int) ([]ReviewInfo, error) {
	return pages[ReviewInfo](ctx, c, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, n), 5)
}

// LineComment is a review comment on a line.
type LineComment struct {
	ID          int64  `json:"id"`
	InReplyTo   int64  `json:"in_reply_to_id"`
	Path        string `json:"path"`
	Line        *int   `json:"line"` // nil = outdated (the line is gone)
	OrigLine    *int   `json:"original_line"`
	StartLine   *int   `json:"start_line"`
	Side        string `json:"side"`
	Body        string `json:"body"`
	User        User   `json:"user"`
	HTMLURL     string `json:"html_url"`
	CommitID    string `json:"commit_id"`
	CreatedAt   string `json:"created_at"`
	ReviewID    int64  `json:"pull_request_review_id"`
	SubjectType string `json:"subject_type"` // line | file
}

// LineComments lists a pull request's line comments.
func (c *Client) LineComments(ctx context.Context, repo string, n int) ([]LineComment, error) {
	return pages[LineComment](ctx, c, fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100", repo, n), 10)
}

// NewComment is a line comment in a review being sent.
type NewComment struct {
	Path      string `json:"path"`
	Body      string `json:"body"`
	Line      int    `json:"line"`
	Side      string `json:"side,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
}

// CreateReview sends a review (APPROVE | REQUEST_CHANGES | COMMENT).
func (c *Client) CreateReview(ctx context.Context, repo string, n int, commit, event, body string, comments []NewComment) (*ReviewInfo, error) {
	in := map[string]any{"event": event, "body": body}
	if commit != "" {
		in["commit_id"] = commit
	}
	if len(comments) > 0 {
		in["comments"] = comments
	}
	var r ReviewInfo
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, n), in, &r)
	return &r, err
}

// Reply answers a line comment.
func (c *Client) Reply(ctx context.Context, repo string, n int, to int64, body string) (*LineComment, error) {
	var lc LineComment
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/comments/%d/replies", repo, n, to), map[string]string{"body": body}, &lc)
	return &lc, err
}

// Comment writes a comment on the pull request's conversation.
func (c *Client) Comment(ctx context.Context, repo string, n int, body string) (string, error) {
	var res struct {
		HTMLURL string `json:"html_url"`
	}
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, n), map[string]string{"body": body}, &res)
	return res.HTMLURL, err
}

// ---- references ----

var prURL = regexp.MustCompile(`(?i)^(?:https?://[^/]+/)?([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/(\d+)`)
var prRef = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#(\d+)$`)

// ParseRef reads "https://github.com/o/r/pull/12", "o/r/pull/12" or "o/r#12".
func ParseRef(s string) (repo string, number int, ok bool) {
	s = strings.TrimSpace(s)
	m := prURL.FindStringSubmatch(s)
	if m == nil {
		m = prRef.FindStringSubmatch(s)
	}
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return strings.ToLower(m[1] + "/" + strings.TrimSuffix(m[2], ".git")), n, true
}
