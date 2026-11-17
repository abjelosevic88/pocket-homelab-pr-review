package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/walk"
)

// Reviewing a whole stack: the stack in one minute (how the pull requests
// build on each other, what to watch across them), then each one, bottom
// first, with its own walkthrough.

// StackMember is one pull request of a stack as the stack page shows it.
type StackMember struct {
	StackItem
	Additions    int              `json:"additions"`
	Deletions    int              `json:"deletions"`
	ChangedFiles int              `json:"changed_files"`
	Walk         *store.WalkBrief `json:"walkthrough,omitempty"`
	TLDR         string           `json:"tldr,omitempty"`
	Budget       []walk.Budget    `json:"budget,omitempty"`
	Steps        []string         `json:"steps,omitempty"`
	Later        map[string][]int `json:"later"`   // its files a pull request higher up changes again
	Earlier      map[string][]int `json:"earlier"` // its files a pull request lower down changed first
}

// StackSummary is the AI's view of the whole stack.
type StackSummary struct {
	TLDR  string `json:"tldr"`
	Story []struct {
		Number int    `json:"number"`
		Adds   string `json:"adds"`
	} `json:"story"`
	Risks []struct {
		Level   string `json:"level"`
		Text    string `json:"text"`
		Numbers []int  `json:"numbers"`
	} `json:"risks"`
	Questions []string `json:"questions"`
	Heads     string   `json:"heads"` // the head commits it was written for
	At        string   `json:"at"`
}

// stackMembers gathers what the stack page shows about every member.
func (s *Server) stackMembers(ctx context.Context, uid int64, p *store.Pull) (*Stack, []StackMember, error) {
	stack, gh, err := s.loadStack(ctx, uid, p, false)
	if err != nil || stack == nil {
		return stack, nil, err
	}
	files := map[int]map[string]bool{}
	for _, it := range stack.Items {
		files[it.Number] = map[string]bool{}
		for _, f := range s.memberFiles(ctx, gh, p.Repo, it.Number) {
			files[it.Number][f] = true
		}
	}
	var local []*store.Pull
	members := make([]StackMember, len(stack.Items))
	for i, it := range stack.Items {
		m := StackMember{StackItem: it, Later: map[string][]int{}, Earlier: map[string][]int{}}
		for _, o := range stack.above(it.Number) {
			for f := range files[it.Number] {
				if files[o.Number][f] {
					m.Later[f] = append(m.Later[f], o.Number)
				}
			}
		}
		for x := it.Parent; x != 0; x = parentOf(stack, x) {
			for f := range files[it.Number] {
				if files[x][f] {
					m.Earlier[f] = append(m.Earlier[f], x)
				}
			}
		}
		if it.LocalID > 0 {
			if lp, err := s.store.GetPull(ctx, uid, it.LocalID); err == nil {
				local = append(local, lp)
				m.Additions, m.Deletions, m.ChangedFiles = lp.Additions, lp.Deletions, lp.ChangedFiles
			}
		} else if gp, err := gh.GetPull(ctx, p.Repo, it.Number); err == nil {
			m.Additions, m.Deletions, m.ChangedFiles = gp.Additions, gp.Deletions, gp.ChangedFiles
		}
		members[i] = m
	}
	_ = s.decorate(ctx, uid, local)
	for i := range members {
		for _, lp := range local {
			if lp.ID != members[i].LocalID || lp.Walk == nil {
				continue
			}
			members[i].Walk = lp.Walk
			if wk, err := s.store.LatestReadyWalk(ctx, uid, lp.ID); err == nil {
				var d walk.Doc
				if json.Unmarshal(wk.Doc, &d) == nil {
					members[i].TLDR = firstNonEmpty(d.TLDR, oneLine(d.Overview, 240))
					members[i].Budget = d.Budget
					for _, st := range d.Steps {
						members[i].Steps = append(members[i].Steps, st.Title)
					}
				}
			}
		}
	}
	return stack, members, nil
}

func parentOf(st *Stack, n int) int {
	for _, it := range st.Items {
		if it.Number == n {
			return it.Parent
		}
	}
	return 0
}

func stackHeads(stack *Stack, members []StackMember) string {
	var b strings.Builder
	for _, m := range members {
		w := ""
		if m.Walk != nil {
			w = m.Walk.HeadSHA
		}
		fmt.Fprintf(&b, "%d:%s;", m.Number, w)
	}
	return b.String()
}

// handleStackReview is the stack page: every member with its size, its
// walkthrough and progress, the files the others change again, and the
// summary when one was written for these heads.
func (s *Server) handleStackReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	stack, members, err := s.stackMembers(ctx, uid, p)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"stack": nil, "error": err.Error()})
		return
	}
	out := map[string]any{"stack": stack, "members": members, "pull": p}
	if stack != nil {
		if v, _, ok := s.store.CacheGet(ctx, fmt.Sprintf("stacksum:%d:%s#%d", uid, p.Repo, stack.Items[0].Number)); ok {
			var sum StackSummary
			if json.Unmarshal([]byte(v), &sum) == nil {
				out["summary"] = sum
				out["summary_stale"] = sum.Heads != stackHeads(stack, members)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleStackAdd puts every member of the stack on the list and makes the
// walkthroughs that are missing or out of date.
func (s *Server) handleStackAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	stack, gh, err := s.loadStack(ctx, uid, p, true)
	if err != nil || stack == nil {
		writeErr(w, http.StatusBadRequest, firstNonEmpty(errText(err), "this pull request is not in a stack"))
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	added, walks := 0, 0
	for _, it := range stack.Items {
		if it.State == "closed" {
			continue
		}
		old, err := s.store.GetPullByRef(ctx, uid, p.Repo, it.Number)
		if err != nil {
			old = nil
		}
		np, err := s.fetchPull(ctx, gh, uid, st.GitHubLogin, p.Repo, it.Number, old)
		if err != nil {
			writeErr(w, http.StatusBadGateway, fmt.Sprintf("#%d: %s", it.Number, err.Error()))
			return
		}
		np.Archived = false
		if old == nil {
			np.Reason = store.ReasonAdded
			added++
		}
		if err := s.store.SavePull(ctx, np); err != nil {
			s.fail(w, err, "save pull request")
			return
		}
		if it.State == "merged" {
			continue
		}
		if last, err := s.store.LatestWalk(ctx, uid, np.ID); err != nil || last.HeadSHA != np.HeadSHA || last.Status == store.WalkFailed {
			if _, err := s.queueWalk(ctx, uid, np); err == nil {
				walks++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "walkthroughs": walks})
}

const stackVoice = `You help someone review a stack of pull requests: each one builds on the one it names and is merged into it, the bottom one into the main branch; two may build on the same one (side branches). You get each pull request's walkthrough (what it does, its steps, its risks) and which files a later pull request changes again.
Write, plainly and briefly, for a reviewer who has never seen them:
- tldr: one sentence of at most 25 words: what the whole stack does when all of it is merged
- story: for every pull request, in the order given, what it adds on top of the one it builds on, in at most 20 words (number and adds)
- risks: up to 5 concerns that only show across the stack: a lower pull request that is unsafe or broken on its own until a higher one lands, things a higher one undoes or rewrites, an order that would break if merged differently, a missing piece between them. Each: level high, medium or low, text of at most 30 words, numbers of the pull requests it is about. Empty when there are none; never generic advice
- questions: up to 3 short things to ask the author about the stack as a whole
Answer with JSON only: {"tldr":"…","story":[{"number":1,"adds":"…"}],"risks":[{"level":"medium","text":"…","numbers":[1,2]}],"questions":["…"]}`

// handleStackSummary writes the stack in one minute from the members'
// walkthroughs (all of them need one) and keeps it for these heads.
func (s *Server) handleStackSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	stack, members, err := s.stackMembers(ctx, uid, p)
	if err != nil || stack == nil {
		writeErr(w, http.StatusBadRequest, firstNonEmpty(errText(err), "this pull request is not in a stack"))
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	cl, _, why := s.aiFor(ctx, uid, st)
	if cl == nil {
		writeErr(w, http.StatusBadRequest, firstNonEmpty(why, "no AI is set up (Settings → AI)"))
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s. The bottom pull request merges into %s.\n", p.Repo, stack.Base)
	for _, m := range members {
		on := "the main branch"
		if m.Parent != 0 {
			on = fmt.Sprintf("#%d", m.Parent)
		}
		fmt.Fprintf(&b, "\n## #%d %s (%s, +%d −%d, %d files; builds on %s)\n", m.Number, m.Title, m.State, m.Additions, m.Deletions, m.ChangedFiles, on)
		if m.LocalID == 0 {
			b.WriteString("(not on the list: no walkthrough)\n")
			continue
		}
		wk, err := s.store.LatestReadyWalk(ctx, uid, m.LocalID)
		if err != nil {
			b.WriteString("(no walkthrough yet)\n")
			continue
		}
		var d walk.Doc
		if json.Unmarshal(wk.Doc, &d) != nil {
			continue
		}
		md := walk.Markdown(&d, m.URL, m.Title, func(string) string { return "#" })
		b.WriteString(clip(md, 24000))
		if len(m.Later) > 0 {
			b.WriteString("\nFiles a later pull request changes again:")
			for f, ns := range m.Later {
				fmt.Fprintf(&b, " %s (%v);", f, ns)
			}
			b.WriteString("\n")
		}
	}
	text, err := cl.Complete(ctx, stackVoice+languageNote(st.WalkLanguage), clip(b.String(), 300000), true)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var sum StackSummary
	t := strings.TrimSpace(text)
	if i, j := strings.Index(t, "{"), strings.LastIndex(t, "}"); i >= 0 && j > i {
		t = t[i : j+1]
	}
	if err := json.Unmarshal([]byte(t), &sum); err != nil {
		writeErr(w, http.StatusBadGateway, "the AI did not answer in the expected form")
		return
	}
	sum.Heads = stackHeads(stack, members)
	sum.At = time.Now().UTC().Format(time.RFC3339)
	js, _ := json.Marshal(sum)
	_ = s.store.CachePut(ctx, fmt.Sprintf("stacksum:%d:%s#%d", uid, p.Repo, stack.Items[0].Number), string(js))
	writeJSON(w, http.StatusOK, map[string]any{"summary": sum})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…"
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
