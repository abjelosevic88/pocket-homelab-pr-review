package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/walk"
)

// ---- the list ----

func (s *Server) handleListPulls(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	q := r.URL.Query()
	f := store.PullFilter{State: q.Get("state"), Reason: q.Get("reason"), Repo: q.Get("repo"), Archived: queryBool(r, "archived"), Limit: queryInt(r, "limit")}
	if f.Archived && f.State == "" {
		f.State = "all"
	}
	list, err := s.store.ListPulls(ctx, uid, f)
	if err != nil {
		s.fail(w, err, "pull requests")
		return
	}
	if err := s.decorate(ctx, uid, list); err != nil {
		s.fail(w, err, "pull requests")
		return
	}
	drafts, _ := s.store.DraftCounts(ctx, uid)
	st, _ := s.store.GetSettings(ctx, uid)
	type row struct {
		*store.Pull
		Drafts int `json:"drafts"`
	}
	out := make([]row, 0, len(list))
	for _, p := range list {
		out = append(out, row{p, drafts[p.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pulls": out, "github": map[string]any{"connected": st.GitHubToken != "", "login": st.GitHubLogin},
		"sync": s.lastSync(ctx, uid), "syncing": s.isSyncing(uid), "demo": s.cfg.DemoData})
}

func (s *Server) isSyncing(uid int64) bool {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	return s.syncing[uid]
}

// decorate adds each pull request's newest walkthrough and how far I got.
func (s *Server) decorate(ctx context.Context, uid int64, list []*store.Pull) error {
	walks, err := s.store.LatestWalks(ctx, uid)
	if err != nil {
		return err
	}
	viewed, err := s.store.ViewedCounts(ctx, uid)
	if err != nil {
		return err
	}
	for _, p := range list {
		wk := walks[p.ID]
		if wk == nil {
			continue
		}
		b := &store.WalkBrief{ID: wk.ID, Status: wk.Status, Progress: wk.Progress, HeadSHA: wk.HeadSHA, Stale: p.HeadSHA != "" && wk.HeadSHA != p.HeadSHA}
		shown := wk
		if wk.Status != store.WalkReady {
			if rw, err := s.store.LatestReadyWalk(ctx, uid, p.ID); err == nil {
				shown = rw
			}
		}
		var d walk.Doc
		if json.Unmarshal(shown.Doc, &d) == nil {
			b.Steps = len(d.Steps)
			for _, st := range d.Steps {
				for _, u := range st.Units {
					b.Units++
					if unitViewed(u, viewed[p.ID]) {
						b.Viewed++
					}
				}
			}
		}
		p.Walk = b
	}
	return nil
}

func unitViewed(u walk.Unit, seen map[string]bool) bool {
	if len(u.Hashes) == 0 {
		return false
	}
	for _, h := range u.Hashes {
		if !seen[u.Path+"\x00"+h] {
			return false
		}
	}
	return true
}

// handleAddPull adds a pull request by its address and starts its walkthrough.
func (s *Server) handleAddPull(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in struct {
		URL  string `json:"url"`
		Walk *bool  `json:"walk"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	repo, number, ok := github.ParseRef(in.URL)
	if !ok {
		writeErr(w, http.StatusBadRequest, "paste a pull request address, like https://github.com/owner/repo/pull/12 (or owner/repo#12)")
		return
	}
	old, err := s.store.GetPullByRef(ctx, uid, repo, number)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err, "pull request")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		old = nil
	}
	st, _ := s.store.GetSettings(ctx, uid)
	var p *store.Pull
	if s.cfg.DemoData && old != nil {
		p = old
	} else if s.cfg.DemoData {
		// the demo has no GitHub: a pull request from its own picker list
		for _, d := range demoPicks("all", repo, "") {
			if d.Number == number {
				p = &store.Pull{UserID: uid, Repo: repo, Number: number, Title: d.Title, URL: d.URL, Author: d.Author, State: "open",
					BaseRef: "main", HeadRef: fmt.Sprintf("pr-%d", number), HeadSHA: fmt.Sprintf("demo%d", number), GHUpdatedAt: d.UpdatedAt}
			}
		}
		if p == nil {
			writeErr(w, http.StatusBadRequest, "the demo has no GitHub to ask; pick one from the list")
			return
		}
	} else {
		gh, err := s.ghFor(st)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		p, err = s.fetchPull(ctx, gh, uid, st.GitHubLogin, repo, number, old)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	p.Archived = false
	if old == nil {
		p.Reason = store.ReasonAdded
		if app := linkApp(ctx); app != "" {
			p.Source = app
		}
	}
	if err := s.store.SavePull(ctx, p); err != nil {
		s.fail(w, err, "save pull request")
		return
	}
	created := old == nil
	if (in.Walk == nil || *in.Walk) && (!s.cfg.DemoData || s.demoInput(p) != nil) {
		if last, err := s.store.LatestWalk(ctx, uid, p.ID); err != nil || last.HeadSHA != p.HeadSHA || last.Status == store.WalkFailed {
			if _, err := s.queueWalk(ctx, uid, p); err != nil {
				s.fail(w, err, "walkthrough")
				return
			}
		}
	}
	_ = s.decorate(ctx, uid, []*store.Pull{p})
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"pull": p, "created": created, "open_url": s.pullURL(p)})
}

// handleLookup finds a pull request by address (for other apps' badges).
func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	repo, number, ok := github.ParseRef(r.URL.Query().Get("url"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "url is a pull request address")
		return
	}
	p, err := s.store.GetPullByRef(ctx, uid, repo, number)
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	_ = s.decorate(ctx, uid, []*store.Pull{p})
	writeJSON(w, http.StatusOK, map[string]any{"pull": p, "open_url": s.pullURL(p)})
}

func (s *Server) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	if s.cfg.DemoData {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": syncResult{}, "demo": true})
		return
	}
	res, err := s.syncUser(ctx, uid)
	if errors.Is(err, errBusy) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "busy": true})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

func (s *Server) handleArchivePull(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Archived bool `json:"archived"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := s.store.SetPullArchived(r.Context(), actorID(r.Context()), idParam(r, "id"), in.Archived); err != nil {
		s.fail(w, err, "archive")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeletePull(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeletePull(r.Context(), actorID(r.Context()), idParam(r, "id")); err != nil {
		s.fail(w, err, "delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- one pull request ----

// fileOut is a changed file as the page shows it: its diff already split into hunks.
type fileOut struct {
	store.WFile
	Patch string      `json:"-"`
	Hunks []walk.Hunk `json:"hunks"`
}

func filesOut(files []store.WFile) []fileOut {
	out := make([]fileOut, 0, len(files))
	for _, f := range files {
		fo := fileOut{WFile: f, Hunks: walk.ParsePatch(f.Patch)}
		if fo.Hunks == nil {
			fo.Hunks = []walk.Hunk{}
		}
		out = append(out, fo)
	}
	return out
}

// handleGetPull is the PR page: the pull request, the walkthrough shown (the
// newest finished one, or the one asked for), the one being made, its files
// with their hunks, what I looked at and my comments.
func (s *Server) handleGetPull(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	// a quick look at GitHub for new commits (at most once a minute)
	if !s.cfg.DemoData && linkApp(ctx) == "" {
		if t, err := time.Parse(time.RFC3339Nano, p.SyncedAt); err != nil || time.Since(t) > time.Minute {
			c, cancel := context.WithTimeout(ctx, 8*time.Second)
			if np, err := s.refreshPull(c, uid, p); err == nil {
				p = np
			}
			cancel()
		}
	}
	out := map[string]any{"pull": p}
	latest, err := s.store.LatestWalk(ctx, uid, p.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err, "walkthrough")
		return
	}
	var shown *store.Walkthrough
	if v := queryInt(r, "v"); v > 0 {
		if shown, err = s.store.GetWalk(ctx, uid, int64(v)); err != nil || shown.PullID != p.ID {
			writeErr(w, http.StatusNotFound, "no such walkthrough")
			return
		}
	} else if latest != nil {
		shown = latest
		if latest.Status != store.WalkReady {
			if rw, err := s.store.LatestReadyWalk(ctx, uid, p.ID); err == nil {
				shown = rw
			}
		}
	}
	if latest != nil && (latest.Status == store.WalkQueued || latest.Status == store.WalkWorking || (latest.Status == store.WalkFailed && (shown == nil || shown.ID != latest.ID))) {
		out["building"] = map[string]any{"id": latest.ID, "status": latest.Status, "progress": latest.Progress, "error": latest.Error, "head_sha": latest.HeadSHA, "created_at": latest.CreatedAt}
	}
	if shown != nil {
		out["walkthrough"] = shown
		if shown.Status == store.WalkReady {
			files, err := s.store.WalkFiles(ctx, uid, shown.ID, true)
			if err != nil {
				s.fail(w, err, "files")
				return
			}
			out["files"] = filesOut(files)
		}
	}
	if out["files"] == nil {
		out["files"] = []fileOut{}
	}
	versions, _ := s.store.WalkVersions(ctx, uid, p.ID)
	viewed, _ := s.store.ViewedFor(ctx, uid, p.ID)
	comments, _ := s.store.Comments(ctx, uid, p.ID, "")
	reviews, _ := s.store.Reviews(ctx, uid, p.ID, "")
	st, _ := s.store.GetSettings(ctx, uid)
	out["versions"], out["viewed"], out["comments"], out["reviews"] = versions, viewed, comments, reviews
	out["github"] = map[string]any{"connected": st.GitHubToken != "" || s.cfg.DemoData, "login": st.GitHubLogin}
	writeJSON(w, http.StatusOK, out)
}

// handleRewalk makes a new walkthrough (new commits, or just again).
func (s *Server) handleRewalk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	if !s.cfg.DemoData {
		if np, err := s.refreshPull(ctx, uid, p); err == nil {
			p = np
		} else if errors.Is(err, errNoGitHub) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	wk, err := s.queueWalk(ctx, uid, p)
	if err != nil {
		s.fail(w, err, "walkthrough")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": wk.ID, "status": wk.Status})
}

// handleWalkStatus is what the page polls while a walkthrough is being made.
func (s *Server) handleWalkStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wk, err := s.store.GetWalk(ctx, actorID(ctx), idParam(r, "wid"))
	if err != nil || wk.PullID != idParam(r, "id") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": wk.ID, "status": wk.Status, "progress": wk.Progress, "error": wk.Error})
}

// handleWalkMarkdown is the shown walkthrough as Markdown.
func (s *Server) handleWalkMarkdown(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, d, ok := s.readyDoc(w, r)
	if !ok {
		return
	}
	_ = ctx
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(walk.Markdown(d, p.URL, p.Title, nil)))
}

// handlePostWalk writes the walkthrough as a comment on the pull request.
func (s *Server) handlePostWalk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, d, ok := s.readyDoc(w, r)
	if !ok {
		return
	}
	if s.cfg.DemoData {
		writeErr(w, http.StatusBadRequest, "the demo does not write to GitHub")
		return
	}
	st, _ := s.store.GetSettings(ctx, actorID(ctx))
	gh, err := s.ghFor(st)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	url, err := gh.Comment(ctx, p.Repo, p.Number, walk.Markdown(d, p.URL, p.Title, nil))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": url})
}

func (s *Server) readyDoc(w http.ResponseWriter, r *http.Request) (*store.Pull, *walk.Doc, bool) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return nil, nil, false
	}
	wk, err := s.store.LatestReadyWalk(ctx, uid, p.ID)
	if v := queryInt(r, "v"); v > 0 {
		wk, err = s.store.GetWalk(ctx, uid, int64(v))
		if err == nil && (wk.PullID != p.ID || wk.Status != store.WalkReady) {
			err = store.ErrNotFound
		}
	}
	if err != nil {
		writeErr(w, http.StatusNotFound, "there is no finished walkthrough yet")
		return nil, nil, false
	}
	var d walk.Doc
	if err := json.Unmarshal(wk.Doc, &d); err != nil {
		s.fail(w, err, "walkthrough")
		return nil, nil, false
	}
	return p, &d, true
}

// ---- what I looked at ----

func (s *Server) handleSetViewed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	var in struct {
		Items []struct {
			Path   string   `json:"path"`
			Hashes []string `json:"hashes"`
		} `json:"items"`
		Viewed bool `json:"viewed"`
	}
	if err := decode(r, &in); err != nil || len(in.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "say which files (items: path + hashes)")
		return
	}
	for _, it := range in.Items {
		var hs []string
		for _, h := range it.Hashes {
			if h = strings.TrimSpace(h); h != "" && len(h) <= 64 {
				hs = append(hs, h)
			}
		}
		if it.Path == "" || len(hs) == 0 {
			continue
		}
		if err := s.store.SetViewed(ctx, uid, p.ID, it.Path, hs, in.Viewed); err != nil {
			s.fail(w, err, "viewed")
			return
		}
	}
	viewed, _ := s.store.ViewedFor(ctx, uid, p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"viewed": viewed})
}

// ---- other apps and dashboards ----

// handleDashboard is the numbers for Homepage and other apps.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	list, err := s.store.ListPulls(ctx, uid, store.PullFilter{})
	if err != nil {
		s.fail(w, err, "dashboard")
		return
	}
	_ = s.decorate(ctx, uid, list)
	waiting, ready, mine, working := 0, 0, 0, 0
	for _, p := range list {
		if p.Reason == store.ReasonRequested && (p.ReviewedSHA == "" || p.ReviewedSHA != p.HeadSHA) {
			waiting++
		}
		if p.Reason == store.ReasonAuthored {
			mine++
		}
		if p.Walk != nil {
			switch p.Walk.Status {
			case store.WalkReady:
				if p.Walk.Viewed < p.Walk.Units {
					ready++
				}
			case store.WalkQueued, store.WalkWorking:
				working++
			}
		}
	}
	drafts, _ := s.store.DraftCounts(ctx, uid)
	nd := 0
	for _, n := range drafts {
		nd += n
	}
	today, _ := s.store.Reviews(ctx, uid, 0, s.now().Format("2006-01-02"))
	writeJSON(w, http.StatusOK, map[string]any{"waiting": waiting, "open": len(list), "mine": mine, "walkthroughs_to_read": ready,
		"walkthroughs_working": working, "draft_comments": nd, "reviews_today": len(today)})
}

// handleDay is one day for Pocket Day: the reviews I sent.
func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	day := chiParam(r, "date")
	if !validDate(day) || day == "" {
		writeErr(w, http.StatusBadRequest, "date is YYYY-MM-DD")
		return
	}
	ctx := r.Context()
	rs, err := s.store.Reviews(ctx, actorID(ctx), 0, day)
	if err != nil {
		s.fail(w, err, "day")
		return
	}
	approved, changes := 0, 0
	for _, x := range rs {
		switch x.Event {
		case "APPROVE":
			approved++
		case "REQUEST_CHANGES":
			changes++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"date": day, "reviews": rs, "count": len(rs), "approved": approved, "changes_requested": changes})
}

// handleReviews lists the reviews I sent, newest first.
func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rs, err := s.store.Reviews(ctx, actorID(ctx), 0, "")
	if err != nil {
		s.fail(w, err, "reviews")
		return
	}
	writeJSON(w, http.StatusOK, rs)
}
