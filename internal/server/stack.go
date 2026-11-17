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
	Depth    int    `json:"depth"`  // 0 = the bottom of the stack
	Parent   int    `json:"parent"` // the pull request it merges into (0: the bottom one)
	Here     bool   `json:"here"`   // the pull request on screen
	Line     bool   `json:"line"`   // below or above the one on screen (not on a side branch)
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
	stack, gh, err := s.loadStack(ctx, uid, p, queryBool(r, "fresh"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"stack": nil, "error": err.Error()})
		return
	}
	var later map[string][]int
	if stack != nil {
		later = s.laterChanges(ctx, gh, p.Repo, stack, p.Number)
	}
	writeJSON(w, http.StatusOK, map[string]any{"stack": stack, "later": later})
}

// loadStack finds the stack a pull request is in (kept a few minutes) and
// marks which members are on the person's list. Nil when it stands alone.
func (s *Server) loadStack(ctx context.Context, uid int64, p *store.Pull, fresh bool) (*Stack, *github.Client, error) {
	if s.cfg.DemoData {
		return nil, nil, nil
	}
	st, _ := s.store.GetSettings(ctx, uid)
	gh, err := s.ghFor(st)
	if err != nil {
		return nil, nil, err
	}
	key := fmt.Sprintf("stack:%d:%s#%d:%s", uid, p.Repo, p.Number, p.HeadSHA)
	var stack *Stack
	if v, at, ok := s.store.CacheGet(ctx, key); ok && !fresh {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < stackTTL {
			_ = json.Unmarshal([]byte(v), &stack)
		}
	}
	if stack == nil {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		stack, err = findStack(c, gh, p)
		cancel()
		if err != nil {
			return nil, gh, err
		}
		b, _ := json.Marshal(stack)
		_ = s.store.CachePut(ctx, key, string(b))
	}
	if stack != nil {
		for i := range stack.Items {
			it := &stack.Items[i]
			if lp, err := s.store.GetPullByRef(ctx, uid, p.Repo, it.Number); err == nil {
				it.LocalID, it.MyReview = lp.ID, lp.MyReview
				it.Reviewed = lp.ReviewedSHA != "" && lp.ReviewedSHA == lp.HeadSHA
			}
		}
	}
	return stack, gh, nil
}

// memberFiles is the paths a pull request changes (kept a few minutes).
func (s *Server) memberFiles(ctx context.Context, gh *github.Client, repo string, n int) []string {
	key := fmt.Sprintf("prfiles:%s#%d", repo, n)
	if v, at, ok := s.store.CacheGet(ctx, key); ok {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < stackTTL {
			var out []string
			if json.Unmarshal([]byte(v), &out) == nil {
				return out
			}
		}
	}
	files, err := gh.Files(ctx, repo, n)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Filename)
	}
	b, _ := json.Marshal(out)
	_ = s.store.CachePut(ctx, key, string(b))
	return out
}

// laterChanges is, for one member of a stack, which of its files a pull
// request built on it (higher up its own line) changes again: path → their numbers.
func (s *Server) laterChanges(ctx context.Context, gh *github.Client, repo string, stack *Stack, number int) map[string][]int {
	out := map[string][]int{}
	above := stack.above(number)
	if len(above) == 0 {
		return out
	}
	mine := map[string]bool{}
	for _, f := range s.memberFiles(ctx, gh, repo, number) {
		mine[f] = true
	}
	for _, it := range above {
		for _, f := range s.memberFiles(ctx, gh, repo, it.Number) {
			if mine[f] {
				out[f] = append(out[f], it.Number)
			}
		}
	}
	return out
}

// above is the open pull requests built on number, directly or not.
func (st *Stack) above(number int) []StackItem {
	parent := map[int]int{}
	for _, it := range st.Items {
		parent[it.Number] = it.Parent
	}
	var out []StackItem
	for _, it := range st.Items {
		if it.Number == number || it.State == "closed" {
			continue
		}
		for x := parent[it.Number]; x != 0; x = parent[x] {
			if x == number {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

// findStack walks down to the bottom of the stack (what this one builds on),
// then up the whole tree from there (what builds on each), so side branches
// show too. The walk down stops at the default branch and at protected
// branches, so a release pull request (development → main) is not taken for
// a parent. Items come in review order: each one after the one it builds on.
// Nil when the pull request stands alone.
func findStack(ctx context.Context, gh *github.Client, p *store.Pull) (*Stack, error) {
	def, err := gh.DefaultBranch(ctx, p.Repo)
	if err != nil {
		return nil, err
	}
	item := func(x *github.Pull) StackItem {
		return StackItem{Number: x.Number, Title: x.Title, URL: x.HTMLURL, State: x.StateName(), Draft: x.Draft, Author: x.User.Login, BaseRef: x.Base.Ref, HeadRef: x.Head.Ref}
	}
	here := StackItem{Number: p.Number, Title: p.Title, URL: p.URL, State: p.State, Draft: p.Draft, Author: p.Author, BaseRef: p.BaseRef, HeadRef: p.HeadRef, Here: true}

	// down: the pull request whose head is my base, and so on
	below := []StackItem{} // nearest first
	seen := map[int]bool{p.Number: true}
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
	root := here
	if len(below) > 0 {
		root = below[len(below)-1]
	}

	// up: the whole tree from the bottom one, depth first, so each pull
	// request comes right after the one it builds on
	kids := map[int][]StackItem{}
	count := 1
	var grow func(it StackItem, depth int)
	grow = func(it StackItem, depth int) {
		if depth >= stackDepth*2 || it.HeadRef == "" || it.HeadRef == def {
			return
		}
		list, err := gh.PullsByBase(ctx, p.Repo, it.HeadRef)
		if err != nil {
			return
		}
		for i := range list {
			k := item(&list[i])
			if k.Number == root.Number || count >= stackMax {
				continue
			}
			if k.Number == p.Number {
				k = here
			}
			count++
			k.Parent = it.Number
			kids[it.Number] = append(kids[it.Number], k)
		}
		for _, k := range kids[it.Number] {
			grow(k, depth+1)
		}
	}
	grow(root, 0)
	var items []StackItem
	var walk func(it StackItem, depth int)
	walk = func(it StackItem, depth int) {
		it.Depth = depth
		items = append(items, it)
		for _, k := range kids[it.Number] {
			walk(k, depth+1)
		}
	}
	walk(root, 0)
	found := false
	for _, it := range items {
		found = found || it.Number == p.Number
	}
	if !found { // the tree did not reach back up to me (a closed parent): my line only
		items = nil
		for i := len(below) - 1; i >= 0; i-- {
			it := below[i]
			if i < len(below)-1 {
				it.Parent = below[i+1].Number
			}
			items = append(items, it)
		}
		if len(below) > 0 {
			here.Parent = below[0].Number
		}
		items = append(items, here)
	}
	if len(items) < 2 {
		return nil, nil
	}
	// which ones are on my line: below me (my parents) or above me (built on me)
	parent := map[int]int{}
	for _, it := range items {
		parent[it.Number] = it.Parent
	}
	onLine := func(n int) bool {
		for x := n; x != 0; x = parent[x] { // n builds on me, or is me
			if x == p.Number {
				return true
			}
		}
		for x := parent[p.Number]; x != 0; x = parent[x] { // n is below me
			if x == n {
				return true
			}
		}
		return false
	}
	for i := range items {
		items[i].Line = onLine(items[i].Number)
	}
	return &Stack{Base: items[0].BaseRef, Items: items}, nil
}
