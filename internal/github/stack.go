package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// PullsByBase lists the open pull requests that merge into branch: in a
// stack, the ones that build on a pull request whose head is branch.
func (c *Client) PullsByBase(ctx context.Context, repo, branch string) ([]Pull, error) {
	return pages[Pull](ctx, c, fmt.Sprintf("/repos/%s/pulls?state=open&per_page=30&base=%s", repo, url.QueryEscape(branch)), 1)
}

// PullsByHead lists the pull requests (any state, newest first) whose head is
// branch of the repository's owner: in a stack, the one a pull request
// that merges into branch builds on.
func (c *Client) PullsByHead(ctx context.Context, repo, branch string) ([]Pull, error) {
	owner := repo
	if i := strings.Index(repo, "/"); i > 0 {
		owner = repo[:i]
	}
	return pages[Pull](ctx, c, fmt.Sprintf("/repos/%s/pulls?state=all&per_page=10&sort=updated&direction=desc&head=%s", repo, url.QueryEscape(owner+":"+branch)), 1)
}

// BranchInfo is what a stack needs to know about a branch.
type BranchInfo struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
}

// DefaultBranch is the repository's default branch.
func (c *Client) DefaultBranch(ctx context.Context, repo string) (string, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	_, err := c.do(ctx, http.MethodGet, "/repos/"+repo, nil, &r)
	return r.DefaultBranch, err
}

// GetBranch reads one branch (is it protected?).
func (c *Client) GetBranch(ctx context.Context, repo, branch string) (*BranchInfo, error) {
	var b BranchInfo
	_, err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/branches/"+url.PathEscape(branch), nil, &b)
	return &b, err
}
