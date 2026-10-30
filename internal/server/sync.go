package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// syncEvery is how often a person's list is checked against GitHub.
const syncEvery = 5 * time.Minute

var errNoGitHub = errors.New("connect GitHub first (Settings → GitHub)")

// ghFor is a person's GitHub, with their own token.
func (s *Server) ghFor(st store.Settings) (*github.Client, error) {
	if strings.TrimSpace(st.GitHubToken) == "" {
		return nil, errNoGitHub
	}
	base := st.GitHubAPIURL
	if s.ghBase != "" {
		base = s.ghBase
	}
	return github.New(base, st.GitHubToken, s.cfg.UserAgent, 30*time.Second), nil
}

// syncState is what the list shows about the last sync.
type syncState struct {
	At    string `json:"at"`
	Error string `json:"error"`
}

func syncKey(uid int64) string { return fmt.Sprintf("sync:%d", uid) }

func (s *Server) lastSync(ctx context.Context, uid int64) syncState {
	v, at, ok := s.store.CacheGet(ctx, syncKey(uid))
	if !ok {
		return syncState{}
	}
	return syncState{At: at, Error: v}
}

// syncDue syncs everybody whose list is older than syncEvery.
func (s *Server) syncDue(ctx context.Context) {
	ids, err := s.store.SettingsUsers(ctx)
	if err != nil {
		return
	}
	for _, uid := range ids {
		st, err := s.store.GetSettings(ctx, uid)
		if err != nil || st.GitHubToken == "" {
			continue
		}
		if last := s.lastSync(ctx, uid); last.At != "" {
			if t, err := time.Parse(time.RFC3339Nano, last.At); err == nil && time.Since(t) < syncEvery {
				continue
			}
		}
		go func(uid int64) {
			c, cancel := context.WithTimeout(ctx, 4*time.Minute)
			defer cancel()
			if _, err := s.syncUser(c, uid); err != nil && !errors.Is(err, errBusy) {
				s.log.Debug("sync", "user", uid, "err", err)
			}
		}(uid)
	}
}

var errBusy = errors.New("already syncing")

// syncResult says what a sync changed.
type syncResult struct {
	Found   int `json:"found"`
	New     int `json:"new"`
	Updated int `json:"updated"`
	Queued  int `json:"queued"`
}

// syncUser brings one person's list up to date: the open pull requests that
// ask for their review, their own, and the ones they reviewed before.
func (s *Server) syncUser(ctx context.Context, uid int64) (*syncResult, error) {
	s.syncMu.Lock()
	if s.syncing[uid] {
		s.syncMu.Unlock()
		return nil, errBusy
	}
	s.syncing[uid] = true
	s.syncMu.Unlock()
	defer func() {
		s.syncMu.Lock()
		delete(s.syncing, uid)
		s.syncMu.Unlock()
	}()
	res, err := s.doSync(ctx, uid)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_ = s.store.CachePut(ctx, syncKey(uid), msg)
	return res, err
}

func (s *Server) doSync(ctx context.Context, uid int64) (*syncResult, error) {
	st, err := s.store.GetSettings(ctx, uid)
	if err != nil {
		return nil, err
	}
	gh, err := s.ghFor(st)
	if err != nil {
		return nil, err
	}
	login := st.GitHubLogin
	if login == "" {
		me, err := gh.Me(ctx)
		if err != nil {
			return nil, err
		}
		login = me.Login
		st.GitHubLogin = login
		_ = s.store.SaveUserSettings(ctx, uid, st)
	}
	scope := " is:pr is:open archived:false"
	if st.ExcludeDrafts {
		scope += " draft:false"
	}
	for _, r := range strings.Fields(st.SyncRepos) {
		if strings.Contains(r, "/") {
			scope += " repo:" + strings.ToLower(r)
		} else {
			scope += " org:" + strings.ToLower(r)
		}
	}
	type found struct {
		item   github.SearchItem
		reason string
	}
	items := map[string]*found{}
	var order []string
	ask := func(q, reason string) error {
		list, err := gh.Search(ctx, q+scope, 200)
		if err != nil {
			return err
		}
		for _, it := range list {
			ref := fmt.Sprintf("%s#%d", it.Repo(), it.Number)
			if it.Repo() == "" {
				continue
			}
			if items[ref] == nil {
				items[ref] = &found{item: it, reason: reason}
				order = append(order, ref)
			}
		}
		return nil
	}
	// the first reason that finds a PR wins: asked to review > mine > reviewed before
	if st.SyncRequested {
		if err := ask("review-requested:@me", store.ReasonRequested); err != nil {
			return nil, err
		}
	}
	if st.SyncAuthored {
		if err := ask("author:@me", store.ReasonAuthored); err != nil {
			return nil, err
		}
	}
	if st.SyncReviewed {
		if err := ask("reviewed-by:@me -author:@me", store.ReasonReviewed); err != nil {
			return nil, err
		}
	}
	have, err := s.store.SyncedPulls(ctx, uid)
	if err != nil {
		return nil, err
	}
	res := &syncResult{Found: len(order)}
	for _, ref := range order {
		f := items[ref]
		old := have[ref]
		if old != nil && old.GHUpdatedAt == f.item.UpdatedAt && old.Reason == f.reason && old.HeadSHA != "" {
			continue // nothing new
		}
		p, err := s.fetchPull(ctx, gh, uid, login, f.item.Repo(), f.item.Number, old)
		if err != nil {
			s.log.Debug("sync: pull", "ref", ref, "err", err)
			continue
		}
		asked := f.reason == store.ReasonRequested && (old == nil || old.Reason != store.ReasonRequested)
		p.Reason = f.reason
		if old != nil && old.Reason == store.ReasonAdded && f.reason != store.ReasonRequested {
			p.Reason = store.ReasonAdded
		}
		if old == nil {
			p.Source = "sync"
		}
		if asked {
			p.Archived = false // asked again: back on the list
		}
		if err := s.store.SavePull(ctx, p); err != nil {
			return res, err
		}
		if old == nil {
			res.New++
		} else {
			res.Updated++
		}
		if asked {
			s.publish(ctx, uid, "pr-review.review_requested", p.Ref(), map[string]any{"repo": p.Repo, "number": p.Number, "title": p.Title, "url": p.URL, "author": p.Author})
			if st.NotifyRequested {
				s.notifyUser(ctx, st, notify.Message{Title: "Review asked: " + p.Repo + "#" + fmt.Sprint(p.Number), Body: p.Title + " — by " + p.Author, Link: s.pullURL(p), Tags: []string{"eyes"}})
			}
		}
		if s.wantsAutoWalk(st, p) {
			if q, _ := s.autoWalk(ctx, uid, p); q {
				res.Queued++
			}
		}
	}
	// the ones no longer found: closed, merged, or not asked any more
	n := 0
	for ref, old := range have {
		if items[ref] != nil || old.Source != "sync" && old.Reason == store.ReasonAdded {
			continue
		}
		if n++; n > 30 {
			break
		}
		p, err := s.fetchPull(ctx, gh, uid, login, old.Repo, old.Number, old)
		if err != nil {
			continue
		}
		if p.State == "open" && old.Reason != store.ReasonAdded {
			p.Reason = store.ReasonAdded // still open, it just stays on the list
		}
		_ = s.store.SavePull(ctx, p)
	}
	return res, nil
}

// fetchPull reads a pull request (and my last review on it) from GitHub,
// keeping what only this app knows from old.
func (s *Server) fetchPull(ctx context.Context, gh *github.Client, uid int64, login, repo string, number int, old *store.Pull) (*store.Pull, error) {
	g, err := gh.GetPull(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	p := &store.Pull{UserID: uid, Repo: repo, Number: number, Reason: store.ReasonAdded}
	if old != nil {
		*p = *old
	}
	p.Title, p.Body, p.URL = g.Title, g.Body, g.HTMLURL
	p.Author, p.AuthorAvatar = g.User.Login, g.User.AvatarURL
	p.State, p.Draft = g.StateName(), g.Draft
	p.BaseRef, p.HeadRef, p.BaseSHA, p.HeadSHA = g.Base.Ref, g.Head.Ref, g.Base.SHA, g.Head.SHA
	p.Additions, p.Deletions, p.ChangedFiles, p.Commits = g.Additions, g.Deletions, g.ChangedFiles, g.Commits
	p.GHUpdatedAt, p.SyncedAt = g.UpdatedAt, store.Now()
	if login != "" {
		if rs, err := gh.Reviews(ctx, repo, number); err == nil {
			for _, r := range rs {
				if strings.EqualFold(r.User.Login, login) && r.State != "PENDING" && r.State != "DISMISSED" {
					p.MyReview, p.ReviewedSHA = r.State, r.CommitID
				}
			}
		}
	}
	return p, nil
}

// refreshPull re-reads one pull request now (opening it, adding it by hand).
func (s *Server) refreshPull(ctx context.Context, uid int64, p *store.Pull) (*store.Pull, error) {
	st, _ := s.store.GetSettings(ctx, uid)
	gh, err := s.ghFor(st)
	if err != nil {
		return p, err
	}
	np, err := s.fetchPull(ctx, gh, uid, st.GitHubLogin, p.Repo, p.Number, p)
	if err != nil {
		return p, err
	}
	if err := s.store.SavePull(ctx, np); err != nil {
		return p, err
	}
	return np, nil
}

func (s *Server) wantsAutoWalk(st store.Settings, p *store.Pull) bool {
	if p.State != "open" || p.Archived {
		return false
	}
	switch st.AutoWalk {
	case "all":
		return true
	case "requested", "":
		return p.Reason == store.ReasonRequested
	}
	return false
}

// autoWalk queues a walkthrough when there is none for the head commit;
// a pull request that keeps getting pushes waits 30 minutes between them.
func (s *Server) autoWalk(ctx context.Context, uid int64, p *store.Pull) (bool, error) {
	last, err := s.store.LatestWalk(ctx, uid, p.ID)
	if err == nil {
		if last.HeadSHA == p.HeadSHA || last.Status == store.WalkQueued || last.Status == store.WalkWorking {
			return false, nil
		}
		if t, err := time.Parse(time.RFC3339Nano, last.CreatedAt); err == nil && time.Since(t) < 30*time.Minute {
			return false, nil
		}
	}
	_, err = s.queueWalk(ctx, uid, p)
	return err == nil, err
}

func (s *Server) pullURL(p *store.Pull) string {
	if s.cfg.BaseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/pr/%d", s.cfg.BaseURL, p.ID)
}

func (s *Server) notifyUser(ctx context.Context, st store.Settings, m notify.Message) {
	if st.NotifyURL == "" {
		return
	}
	n := notify.New(st.NotifyURL, st.NotifyFormat)
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := n.Send(c, m); err != nil {
			s.log.Debug("notify", "err", err)
		}
	}()
	_ = ctx
}
