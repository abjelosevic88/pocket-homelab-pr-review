package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/pocket"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/walk"
)

// Walkthroughs are made in the background, two at a time; a restart picks
// up the ones it interrupted.

const (
	walkWorkers   = 2
	maxTimeline   = 40    // commits read one by one for "how it was built"
	timelinePatch = 12000 // bytes of a commit's patch kept per file
	keepWalks     = 5
)

// queueWalk starts a walkthrough of the pull request at its head commit
// (or returns the one already being made).
func (s *Server) queueWalk(ctx context.Context, uid int64, p *store.Pull) (*store.Walkthrough, error) {
	s.walkMu.Lock()
	if s.walkBusy[p.ID] {
		s.walkMu.Unlock()
		return s.store.LatestWalk(ctx, uid, p.ID)
	}
	s.walkBusy[p.ID] = true
	s.walkMu.Unlock()
	w, err := s.store.CreateWalk(ctx, uid, p.ID, p.HeadSHA, p.BaseSHA)
	if err != nil {
		s.walkMu.Lock()
		delete(s.walkBusy, p.ID)
		s.walkMu.Unlock()
		return nil, err
	}
	select {
	case s.walkQ <- w.ID:
	default: // full: it stays queued and is picked up on the next start
	}
	return w, nil
}

// walker runs the queue.
func (s *Server) walker(ctx context.Context) {
	if left, err := s.store.UnfinishedWalks(ctx); err == nil {
		for _, w := range left {
			_ = s.store.SetWalkProgress(ctx, w.ID, store.WalkQueued, "")
			s.walkMu.Lock()
			s.walkBusy[w.PullID] = true
			s.walkMu.Unlock()
			select {
			case s.walkQ <- w.ID:
			default:
			}
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < walkWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-s.walkQ:
					s.makeWalk(ctx, id)
				}
			}
		}()
	}
	wg.Wait()
}

// makeWalk makes one walkthrough from start to finish.
func (s *Server) makeWalk(ctx context.Context, id int64) {
	var w *store.Walkthrough
	row := s.store.DB.QueryRowContext(ctx, `SELECT user_id FROM walkthroughs WHERE id = ?`, id)
	var uid int64
	if err := row.Scan(&uid); err != nil {
		return
	}
	w, err := s.store.GetWalk(ctx, uid, id)
	if err != nil {
		return
	}
	defer func() {
		s.walkMu.Lock()
		delete(s.walkBusy, w.PullID)
		s.walkMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	fail := func(msg string) {
		_ = s.store.FinishWalk(context.WithoutCancel(ctx), id, store.WalkFailed, "", msg, nil, nil)
	}
	p, err := s.store.GetPull(ctx, uid, w.PullID)
	if err != nil {
		fail("the pull request is gone")
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	lastProgress := ""
	progress := func(t string) {
		if t != lastProgress {
			lastProgress = t
			_ = s.store.SetWalkProgress(ctx, id, store.WalkWorking, t)
		}
	}
	progress("Reading the pull request from GitHub")
	in, events, err := s.walkInput(ctx, uid, st, p, progress)
	if err != nil {
		fail(err.Error())
		return
	}
	var model walk.AI
	used := ""
	if s.testAI != nil {
		model, used = s.testAI, "test"
	} else if cl, how, _ := s.aiFor(ctx, uid, st); cl != nil {
		model, used = cl, how
	}
	doc, files := walk.Build(ctx, in, model, progress)
	if used == "" {
		doc.Warnings = append([]string{"No AI is set up (Settings → AI), so the files are ordered by layer and have no sentences of their own."}, doc.Warnings...)
	}
	progress("Saving")
	rows := make([]store.WFile, 0, len(files))
	order := doc.FileOrder()
	for i, f := range files {
		if n, ok := order[f.Path]; ok {
			i = n
		} else {
			i += len(order)
		}
		rows = append(rows, store.WFile{Path: f.Path, OldPath: f.OldPath, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions,
			Layer: f.Layer, Summary: f.Summary, Patch: f.Patch, Binary: f.Binary, Truncated: f.Truncated, Sort: i})
	}
	if err := s.store.SaveWalkFiles(ctx, uid, id, rows); err != nil {
		fail("saving the files: " + err.Error())
		return
	}
	docJSON, _ := json.Marshal(doc)
	tlJSON, _ := json.Marshal(events)
	if err := s.store.FinishWalk(ctx, id, store.WalkReady, used, "", docJSON, tlJSON); err != nil {
		fail("saving: " + err.Error())
		return
	}
	_ = s.store.PruneWalks(ctx, uid, p.ID, keepWalks)
	s.walks.Add(1)
	s.publish(ctx, uid, "pr-review.walkthrough_ready", p.Ref(), map[string]any{"repo": p.Repo, "number": p.Number, "title": p.Title,
		"url": p.URL, "steps": doc.Stats.Steps, "files": doc.Stats.Files, "open_url": s.pullURL(p)})
	if st.NotifyReady {
		s.notifyUser(ctx, st, notify.Message{Title: "Walkthrough ready: " + p.Ref(), Body: fmt.Sprintf("%s — %d steps, %d files", p.Title, doc.Stats.Steps, doc.Stats.Files), Link: s.pullURL(p), Tags: []string{"books"}})
	}
}

// walkInput gathers what a walkthrough is made from: the diff, the commits
// (how it was built), the repository's own config and, through Pocket AI,
// the order a coding agent edited the files in.
func (s *Server) walkInput(ctx context.Context, uid int64, st store.Settings, p *store.Pull, progress func(string)) (walk.Input, []walk.Event, error) {
	in := walk.Input{Repo: p.Repo, Title: p.Title, Body: p.Body, BaseRef: p.BaseRef, HeadRef: p.HeadRef, Hints: st.WalkHints, Language: st.WalkLanguage}
	if demo := s.demoInput(p); demo != nil {
		return demo.in, demo.events, nil
	}
	gh, err := s.ghFor(st)
	if err != nil {
		return in, nil, err
	}
	files, err := gh.Files(ctx, p.Repo, p.Number)
	if err != nil {
		return in, nil, err
	}
	for _, f := range files {
		in.Files = append(in.Files, walk.File{Path: f.Filename, OldPath: f.PreviousFilename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions,
			Patch: f.Patch, Binary: f.Patch == "" && f.Changes == 0 && f.Status != "renamed", Truncated: f.Patch == "" && f.Changes > 0})
	}
	if b, err := gh.FileAt(ctx, p.Repo, walk.ConfigPath, p.HeadSHA); err == nil && len(b) > 0 {
		if cfg, err := walk.ParseConfig(b); err == nil {
			in.Config = cfg
		} else {
			s.log.Debug("repo config", "repo", p.Repo, "err", err)
		}
	}
	progress("Reading how it was built")
	var events []walk.Event
	commits, err := gh.Commits(ctx, p.Repo, p.Number)
	if err == nil {
		for i, c := range commits {
			ev := walk.Event{Kind: "commit", SHA: c.SHA, At: firstNonEmpty(c.Commit.Author.Date, c.Commit.Committer.Date), Files: []walk.TFile{}}
			ev.Title, ev.Body = splitMessage(c.Commit.Message)
			ev.Author = c.Commit.Author.Name
			if c.Author != nil && c.Author.Login != "" {
				ev.Author = c.Author.Login
			}
			cm := walk.Commit{SHA: c.SHA, Message: c.Commit.Message}
			if i < maxTimeline && len(commits) > 1 {
				if full, err := gh.GetCommit(ctx, p.Repo, c.SHA); err == nil {
					for _, f := range full.Files {
						ev.Files = append(ev.Files, walk.TFile{Path: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Patch: clipBytes(f.Patch, timelinePatch)})
						cm.Paths = append(cm.Paths, f.Filename)
					}
				}
			}
			events = append(events, ev)
			in.Commits = append(in.Commits, cm)
		}
	}
	if edits := s.agentEdits(ctx, uid, st, p); len(edits) > 0 {
		events = append(events, editEvents(edits)...)
		seen := map[string]bool{}
		for _, e := range edits {
			if !seen[e.Path] {
				seen[e.Path] = true
				in.AgentOrder = append(in.AgentOrder, e.Path)
			}
		}
		sortEvents(events)
	}
	return in, events, nil
}

// agentEdits asks Pocket AI which files the person's coding conversations
// edited for this pull request, in order (nothing when it is not connected).
func (s *Server) agentEdits(ctx context.Context, uid int64, st store.Settings, p *store.Pull) []pocket.AgentEdit {
	cl := s.clients(ctx, uid)
	cl.st = st
	sib, err := optional(cl.conn(ctx, appAI, scopeThreadsRead))
	if err != nil || sib == nil {
		return nil
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	edits, err := sib.client.Edits(c, p.URL)
	if err != nil {
		s.log.Debug("pocket ai: edits", "err", err)
		return nil
	}
	return edits
}

// editEvents groups an agent's edits: one event per thing it said before
// editing, with the files it then changed.
func editEvents(edits []pocket.AgentEdit) []walk.Event {
	var out []walk.Event
	for _, e := range edits {
		if n := len(out); n > 0 && out[n-1].Body == e.Note && out[n-1].SHA == e.ThreadID && e.Note != "" {
			last := &out[n-1]
			dup := false
			for _, f := range last.Files {
				if f.Path == e.Path {
					dup = true
				}
			}
			if !dup {
				last.Files = append(last.Files, walk.TFile{Path: e.Path, Status: "edited"})
			}
			continue
		}
		title := "The agent edited " + e.Path
		if e.Note != "" {
			title = oneLine(e.Note, 140)
		}
		out = append(out, walk.Event{Kind: "edit", At: e.At, Title: title, Body: e.Note, SHA: e.ThreadID, Author: "agent", Files: []walk.TFile{{Path: e.Path, Status: "edited", Note: e.Tool}}})
	}
	return out
}

func sortEvents(ev []walk.Event) {
	// stable by time; commits and edits interleave as they happened
	for i := 1; i < len(ev); i++ {
		for j := i; j > 0 && ev[j].At < ev[j-1].At; j-- {
			ev[j], ev[j-1] = ev[j-1], ev[j]
		}
	}
}

func splitMessage(m string) (string, string) {
	m = strings.TrimSpace(m)
	if i := strings.Index(m, "\n"); i >= 0 {
		return strings.TrimSpace(m[:i]), strings.TrimSpace(m[i+1:])
	}
	return m, ""
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndex(s[:n], "\n")
	if cut < 0 {
		cut = n
	}
	return s[:cut]
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

var _ = github.DefaultAPI
