package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/pocket"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/walk"
)

// ---- handing the review to a coding agent (Pocket AI) ----
//
// PR Review prepares a conversation in Pocket AI with the comments to work
// on, in the repository's folder (remembered per repository); the person
// reads it there and presses send themselves.

type repoFolder struct {
	Host int64  `json:"host"`
	Cwd  string `json:"cwd"`
}

func repoFolders(st store.Settings) map[string]repoFolder {
	m := map[string]repoFolder{}
	if st.RepoFolders != "" {
		_ = json.Unmarshal([]byte(st.RepoFolders), &m)
	}
	return m
}

func (s *Server) handleAgentOptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	st, _ := s.store.GetSettings(ctx, uid)
	out := map[string]any{"connected": false, "folder": repoFolders(st)[store.NormRepo(r.URL.Query().Get("repo"))]}
	sib, err := s.clients(ctx, uid).conn(ctx, appAI, scopeThreadsWrite)
	if err != nil {
		out["detail"] = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	opts, err := sib.client.AgentOptions(ctx)
	if err != nil {
		out["detail"] = "Pocket AI did not answer: " + err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	hosts := []pocket.AgentHost{{ID: 0, Name: "This server"}}
	hosts = append(hosts, opts.Hosts...)
	out["connected"], out["hosts"], out["providers"], out["default_provider"], out["home"] = true, hosts, opts.Providers, opts.DefaultProvider, opts.Home
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAgentDirs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	sib, err := s.clients(ctx, uid).conn(ctx, appAI, scopeThreadsWrite)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d, err := sib.client.Dirs(ctx, int64(queryInt(r, "host")), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Pocket AI: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// agentPrompt is the message for the agent: what to fix, where.
func (s *Server) agentPrompt(p *store.Pull, comments []store.Comment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Please work on the review of pull request %s “%s” (%s), branch `%s`.\n\n", p.Ref(), p.Title, p.URL, p.HeadRef)
	if len(comments) == 0 {
		b.WriteString("(No comments yet — describe what should change.)\n")
	} else {
		b.WriteString("Review comments:\n")
		for i, c := range comments {
			where := "Step “" + c.Step + "”"
			if c.Path != "" {
				where = fmt.Sprintf("`%s` line %d", c.Path, c.Line)
				if c.StartLine > 0 {
					where = fmt.Sprintf("`%s` lines %d–%d", c.Path, c.StartLine, c.Line)
				}
				if c.Side == "LEFT" {
					where += " (old version)"
				}
			}
			fmt.Fprintf(&b, "%d. %s — %s\n", i+1, where, strings.TrimSpace(c.Body))
		}
	}
	fmt.Fprintf(&b, "\nWork on the branch `%s` (check it out if needed). Make the changes, run the tests, then commit and push to the same branch so the pull request updates.\n", p.HeadRef)
	return b.String()
}

// agentComments are the comments to hand over: my drafts, else those of the
// last review I sent.
func (s *Server) agentComments(r *http.Request, p *store.Pull) []store.Comment {
	ctx := r.Context()
	uid := actorID(ctx)
	drafts, _ := s.store.Comments(ctx, uid, p.ID, "draft")
	if len(drafts) > 0 && r.URL.Query().Get("from") != "sent" {
		return drafts
	}
	sent, _ := s.store.Comments(ctx, uid, p.ID, "sent")
	var last int64
	for _, c := range sent {
		if c.ReviewID > last {
			last = c.ReviewID
		}
	}
	var out []store.Comment
	for _, c := range sent {
		if c.ReviewID == last {
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) handleAgentPrompt(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.store.GetPull(ctx, actorID(ctx), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	cs := s.agentComments(r, p)
	writeJSON(w, http.StatusOK, map[string]any{"title": "Review of " + p.Ref() + ": " + oneLine(p.Title, 80), "text": s.agentPrompt(p, cs), "comments": len(cs)})
}

func (s *Server) handleAgentSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	var in struct {
		Host     int64  `json:"host"`
		Cwd      string `json:"cwd"`
		Provider string `json:"provider"`
		Title    string `json:"title"`
		Text     string `json:"text"`
		Worktree bool   `json:"worktree"`
		Remember bool   `json:"remember"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Cwd = strings.TrimSpace(in.Cwd)
	if in.Cwd == "" {
		writeErr(w, http.StatusBadRequest, "choose the repository's folder")
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		in.Text = s.agentPrompt(p, s.agentComments(r, p))
	}
	sib, err := s.clients(ctx, uid).conn(ctx, appAI, scopeThreadsWrite)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d := pocket.Draft{Provider: in.Provider, HostID: in.Host, Cwd: in.Cwd, Title: firstNonEmpty(strings.TrimSpace(in.Title), "Review of "+p.Ref()),
		Text: in.Text, Source: "pr-review", Ref: p.Ref(), RefURL: p.URL, RefLabel: p.Ref() + " " + oneLine(p.Title, 100),
		System: "The person is reviewing this pull request in Pocket PR Review. Keep the changes to what the comments ask for."}
	if in.Worktree && p.HeadRef != "" {
		d.Worktree = map[string]any{"name": p.HeadRef}
	}
	_, openURL, err := sib.client.CreateDraft(ctx, d)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Pocket AI: "+err.Error())
		return
	}
	if in.Remember {
		st, _ := s.store.GetSettings(ctx, uid)
		m := repoFolders(st)
		m[p.Repo] = repoFolder{Host: in.Host, Cwd: in.Cwd}
		b, _ := json.Marshal(m)
		st.RepoFolders = string(b)
		_ = s.store.SaveUserSettings(ctx, uid, st)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "url": openURL})
}

// handleAgentThreads lists the conversations started from this pull request.
func (s *Server) handleAgentThreads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	sib, err := optional(s.clients(ctx, uid).conn(ctx, appAI, scopeThreadsWrite))
	if err != nil || sib == nil {
		writeJSON(w, http.StatusOK, []pocket.AgentThread{})
		return
	}
	list, err := sib.client.Threads(ctx, "pr-review", p.Ref())
	if err != nil || list == nil {
		list = []pocket.AgentThread{}
	}
	writeJSON(w, http.StatusOK, list)
}

// ---- Pocket AI beside the pull request ----
//
// The panel on the right of a pull request is a Pocket AI conversation about
// it, as in Pocket Projects: the repository's folder when one is remembered
// (else the home folder), the pull request in the hidden context, and Pocket
// PR Review's tools (find, brief, diff, grep, status) to read it.

func (s *Server) handleChatStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	_ = decode(r, &in)
	sib, err := s.clients(ctx, uid).conn(ctx, appAI, scopeThreadsWrite)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	f, known := repoFolders(st)[p.Repo]
	if !known || f.Cwd == "" {
		f = repoFolder{Cwd: "~"}
	}
	text := strings.TrimSpace(in.Text)
	send := text != ""
	if text == "" {
		text = fmt.Sprintf("Let's talk about pull request %s “%s”.", p.Ref(), p.Title)
	}
	d := pocket.Draft{HostID: f.Host, Cwd: f.Cwd, Title: p.Ref() + " · " + oneLine(p.Title, 70), Text: text,
		Source: "pr-review", Ref: p.Ref(), RefURL: p.URL, RefLabel: p.Ref() + " " + oneLine(p.Title, 100), System: s.chatContext(ctx, p, known)}
	_, openURL, err := sib.client.CreateDraft(ctx, d)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Pocket AI: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"url": openURL, "send": send, "folder": known})
}

// chatContext is the hidden context of a conversation about the pull request.
func (s *Server) chatContext(ctx context.Context, p *store.Pull, inRepo bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The person is reviewing pull request %s “%s” in Pocket PR Review (%s) and asks about it here.\n", p.Ref(), p.Title, s.pullURL(p))
	fmt.Fprintf(&b, "GitHub: %s — by %s, branch `%s` into `%s`, head commit %s, +%d −%d in %d files, %d commits.\n", p.URL, p.Author, p.HeadRef, p.BaseRef, short(p.HeadSHA), p.Additions, p.Deletions, p.ChangedFiles, p.Commits)
	fmt.Fprintf(&b, "Read it with the Pocket PR Review tools (pocket_find_tools \"pr-review\", then pocket_call_tool) and id %d: pocket_pr-review_brief (description, walkthrough, every changed file with one sentence), pocket_pr-review_diff (one file's diff), pocket_pr-review_grep (search every changed line), pocket_pr-review_status (CI, merging, reviews), pocket_pr-review_conversation (GitHub threads).\n", p.ID)
	if inRepo {
		fmt.Fprintf(&b, "You are in the repository's folder. To read code around the change: git fetch origin %s, then git show / git diff origin/%s...origin/%s. Do not check out branches, edit files, commit or push unless the person asks.\n", p.HeadRef, p.BaseRef, p.HeadRef)
	} else {
		b.WriteString("You are not in the repository's folder; read the pull request through the tools above. Do not change anything unless the person asks.\n")
	}
	if wk, err := s.store.LatestReadyWalk(ctx, actorID(ctx), p.ID); err == nil {
		var d walk.Doc
		if json.Unmarshal(wk.Doc, &d) == nil {
			if d.TLDR != "" {
				b.WriteString("In one sentence: " + d.TLDR + "\n")
			}
			if len(d.Steps) > 0 {
				b.WriteString("Its walkthrough, in reading order:\n")
				for i, st := range d.Steps {
					fmt.Fprintf(&b, "%d. %s (%d files)\n", i+1, st.Title, len(st.Units))
				}
			}
		}
	}
	b.WriteString("Answer briefly and concretely, citing files and lines.")
	return b.String()
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
