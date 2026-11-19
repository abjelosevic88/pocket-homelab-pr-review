package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// boardRow is one of my pull requests with where it stands on GitHub now.
type boardRow struct {
	Pull      *store.Pull    `json:"pull"`
	Status    *github.Status `json:"status"`
	CheckedAt string         `json:"checked_at"`
	Error     string         `json:"error,omitempty"`
}

// mine says whether a pull request is one I opened.
func mine(p *store.Pull, login string) bool {
	return p.Reason == store.ReasonAuthored || login != "" && strings.EqualFold(p.Author, login)
}

// statusAge is how long a status is kept on the board: short while CI runs
// or the merge state is still being worked out, longer once it settled.
func statusAge(prev *github.Status) time.Duration {
	if prev == nil || prev.Checks.Running > 0 || prev.MergeState == "UNKNOWN" || prev.Mergeable == "UNKNOWN" {
		return 25 * time.Second
	}
	return 75 * time.Second
}

// handleMineBoard is the board of my pull requests: every open one with its
// CI, reviews, open conversations and merge state, read live from GitHub,
// and the ones closed or merged in the last month.
func (s *Server) handleMineBoard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	st, _ := s.store.GetSettings(ctx, uid)
	open, err := s.store.ListPulls(ctx, uid, store.PullFilter{State: "open"})
	if err != nil {
		s.fail(w, err, "pull requests")
		return
	}
	done, err := s.store.ListPulls(ctx, uid, store.PullFilter{State: "done", Limit: 200})
	if err != nil {
		s.fail(w, err, "pull requests")
		return
	}
	login := st.GitHubLogin
	if s.cfg.DemoData && login == "" && len(open) > 0 {
		login = open[0].Author
	}
	rows := []*boardRow{}
	for _, p := range open {
		if mine(p, login) || s.cfg.DemoData {
			rows = append(rows, &boardRow{Pull: p})
		}
	}
	finished := []*store.Pull{}
	since := time.Now().AddDate(0, -1, 0).UTC().Format(time.RFC3339)
	for _, p := range done {
		if mine(p, login) && (p.GHUpdatedAt == "" || p.GHUpdatedAt >= since) && len(finished) < 20 {
			finished = append(finished, p)
		}
	}

	connected := st.GitHubToken != "" || s.cfg.DemoData
	if s.cfg.DemoData {
		now := time.Now().UTC().Format(time.RFC3339)
		for _, row := range rows {
			row.Status, row.CheckedAt = demoStatus(row.Pull), now
		}
	} else if gh, err := s.ghFor(st); err == nil {
		// my new pull requests come in with the sync; keep it fresh while I watch
		if last := s.lastSync(ctx, uid); last.At == "" || olderThan(last.At, 2*time.Minute) {
			go func() {
				c, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
				defer cancel()
				_, _ = s.syncUser(c, uid)
			}()
		}
		fresh := queryBool(r, "fresh")
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for _, row := range rows {
			wg.Add(1)
			go func(row *boardRow) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				c, cancel := context.WithTimeout(ctx, 40*time.Second)
				defer cancel()
				age := statusAge(s.keptStatus(c, uid, row.Pull))
				if fresh {
					age = 0
				}
				cs, err := s.cachedStatus(c, gh, uid, row.Pull, age)
				if err != nil {
					row.Error = err.Error()
					return
				}
				row.Status, row.CheckedAt = cs.Status, cs.CheckedAt
			}(row)
		}
		wg.Wait()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pulls": rows, "done": finished, "login": login, "connected": connected,
		"sync_authored": st.SyncAuthored, "sync": s.lastSync(ctx, uid), "syncing": s.isSyncing(uid),
		"checked_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// keptStatus is the last status read for a pull request, however old.
func (s *Server) keptStatus(ctx context.Context, uid int64, p *store.Pull) *github.Status {
	cs, err := s.cachedStatus(ctx, nil, uid, p, 24*time.Hour)
	if err != nil {
		return nil
	}
	return cs.Status
}

func olderThan(at string, d time.Duration) bool {
	t, err := time.Parse(time.RFC3339Nano, at)
	return err != nil || time.Since(t) > d
}
