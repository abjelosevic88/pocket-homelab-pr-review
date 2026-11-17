package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// A stack is pull requests built on each other: B merges into A's branch,
// C into B's. The page shows the whole stack, bottom first, so each one is
// reviewed on top of the one below it.

const (
	stackDepth = 8  // pull requests above and below at most
	stackMax   = 20 // in the whole stack
	stackTTL   = 3 * time.Minute
)

// StackItem is one pull request of a stack.
type StackItem struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	State    string `json:"state"` // open | closed | merged
	Draft    bool   `json:"draft"`
	Author   string `json:"author"`
	BaseRef  string `json:"base_ref"`
	HeadRef  string `json:"head_ref"`
	Depth    int    `json:"depth"` // 0 = the bottom of the stack
	Here     bool   `json:"here"`  // the pull request on screen
	LocalID  int64  `json:"local_id,omitempty"`
	MyReview string `json:"my_review,omitempty"`
	Reviewed bool   `json:"reviewed_current,omitempty"` // my last review is on its head
}

// Stack is the pull requests below and above one, and the branch at the bottom.
type Stack struct {
	Base  string      `json:"base"` // what the bottom one merges into
	Items []StackItem `json:"items"`
}

func (s *Server) handleStack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	if s.cfg.DemoData {
		writeJSON(w, http.StatusOK, map[string]any{"stack": nil})
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	gh, err := s.ghFor(st)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"stack": nil, "error": err.Error()})
		return
	}
	key := fmt.Sprintf("stack:%d:%s#%d:%s", uid, p.Repo, p.Number, p.HeadSHA)
	var stack *Stack
	if v, at, ok := s.store.CacheGet(ctx, key); ok && !queryBool(r, "fresh") {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < stackTTL {
			_ = json.Unmarshal([]byte(v), &stack)
		}
	}
	if stack == nil {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		stack, err = findStack(c, gh, p)
		cancel()
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"stack": nil, "error": err.Error()})
			return
		}
		b, _ := json.Marshal(stack)
		_ = s.store.CachePut(ctx, key, string(b))
	}
	// which of them are on my list, and what I said about them
	if stack != nil {
		for i := range stack.Items {
			it := &stack.Items[i]
			if lp, err := s.store.GetPullByRef(ctx, uid, p.Repo, it.Number); err == nil {
				it.LocalID, it.MyReview = lp.ID, lp.MyReview
				it.Reviewed = lp.ReviewedSHA != "" && lp.ReviewedSHA == lp.HeadSHA
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"stack": stack})
}

// findStack walks down (what this one builds on) and up (what builds on it).
// The walk down stops at the default branch and at protected branches, so a
// release pull request (development → main) is not taken for a parent.
// Nil when the pull request stands alone.
func findStack(ctx context.Context, gh *github.Client, p *store.Pull) (*Stack, error) {
	def, err := gh.DefaultBranch(ctx, p.Repo)
	if err != nil {
		return nil, err
	}
	item := func(x *github.Pull) StackItem {
		return StackItem{Number: x.Number, Title: x.Title, URL: x.HTMLURL, State: x.StateName(), Draft: x.Draft, Author: x.User.Login, BaseRef: x.Base.Ref, HeadRef: x.Head.Ref}
	}
	seen := map[int]bool{p.Number: true}
	here := StackItem{Number: p.Number, Title: p.Title, URL: p.URL, State: p.State, Draft: p.Draft, Author: p.Author, BaseRef: p.BaseRef, HeadRef: p.HeadRef, Here: true}

	// below: the pull request whose head is my base, and so on
	var below []StackItem
	base := p.BaseRef
	for len(below) < stackDepth && base != "" && base != def {
		if b, err := gh.GetBranch(ctx, p.Repo, base); err == nil && b.Protected {
			break
		}
		list, err := gh.PullsByHead(ctx, p.Repo, base)
		if err != nil || len(list) == 0 {
			break
		}
		parent := &list[0]
		for i := range list { // an open one wins over an old closed one
			if list[i].State == "open" {
				parent = &list[i]
				break
			}
		}
		if seen[parent.Number] {
			break
		}
		seen[parent.Number] = true
		below = append(below, item(parent))
		base = parent.Base.Ref
	}

	// above: open pull requests that merge into my head, breadth first
	type node struct {
		head  string
		depth int
	}
	var above []StackItem
	queue := []node{{p.HeadRef, 1}}
	for len(queue) > 0 && len(below)+len(above) < stackMax {
		n := queue[0]
		queue = queue[1:]
		if n.depth > stackDepth || n.head == "" || n.head == def {
			continue
		}
		kids, err := gh.PullsByBase(ctx, p.Repo, n.head)
		if err != nil {
			continue
		}
		for i := range kids {
			k := &kids[i]
			if seen[k.Number] || len(below)+len(above) >= stackMax {
				continue
			}
			seen[k.Number] = true
			it := item(k)
			it.Depth = n.depth
			above = append(above, it)
			queue = append(queue, node{k.Head.Ref, n.depth + 1})
		}
	}
	if len(below) == 0 && len(above) == 0 {
		return nil, nil
	}
	out := &Stack{}
	for i := len(below) - 1; i >= 0; i-- { // bottom first
		it := below[i]
		it.Depth = len(below) - 1 - i
		out.Items = append(out.Items, it)
	}
	here.Depth = len(below)
	out.Items = append(out.Items, here)
	for _, it := range above {
		it.Depth += len(below)
		out.Items = append(out.Items, it)
	}
	out.Base = out.Items[0].BaseRef
	return out, nil
}
