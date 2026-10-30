package server

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// ---- export / import of one person's data ----
//
// The export holds what only this app knows: the pull requests followed,
// what was looked at, the comments and the reviews sent. Walkthroughs are
// left out — they are made again from GitHub. Importing the same file twice
// changes nothing the second time.

type exportPull struct {
	store.Pull
	Viewed   map[string]map[string]string `json:"viewed"`
	Comments []store.Comment              `json:"comments"`
	Reviews  []store.Review               `json:"reviews"`
}

type exportDoc struct {
	App        string       `json:"app"`
	Version    string       `json:"version"`
	ExportedAt string       `json:"exported_at"`
	Pulls      []exportPull `json:"pulls"`
}

func (s *Server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	list, err := s.store.ListPulls(ctx, uid, store.PullFilter{State: "all"})
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	arch, err := s.store.ListPulls(ctx, uid, store.PullFilter{State: "all", Archived: true})
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	doc := exportDoc{App: "pocket-pr-review", Version: Version, ExportedAt: store.Now(), Pulls: []exportPull{}}
	for _, p := range append(list, arch...) {
		ep := exportPull{Pull: *p}
		ep.Viewed, _ = s.store.ViewedFor(ctx, uid, p.ID)
		ep.Comments, _ = s.store.Comments(ctx, uid, p.ID, "")
		ep.Reviews, _ = s.store.Reviews(ctx, uid, p.ID, "")
		doc.Pulls = append(doc.Pulls, ep)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pocket-pr-review-%s.json"`, time.Now().Format("2006-01-02")))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
}

// handleExportCSV is the reviews I sent, one per row.
func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rs, err := s.store.Reviews(ctx, actorID(ctx), 0, "")
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pocket-pr-review-reviews-%s.csv"`, time.Now().Format("2006-01-02")))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"date", "repository", "number", "title", "review", "comments", "commit", "url", "text"})
	for _, x := range rs {
		_ = cw.Write([]string{x.Day, x.Repo, strconv.Itoa(x.Number), x.Title, x.Event, strconv.Itoa(x.Comments), x.CommitSHA, x.GHURL, x.Body})
	}
	cw.Flush()
}

func (s *Server) handleImportJSON(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	b, err := s.readBody(r)
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "the file is too large")
		return
	}
	var doc exportDoc
	if err := json.Unmarshal(b, &doc); err != nil || doc.App != "pocket-pr-review" {
		writeErr(w, http.StatusBadRequest, "this is not a Pocket PR Review export")
		return
	}
	res := map[string]int{"pulls": 0, "comments": 0, "reviews": 0, "viewed": 0}
	for _, ep := range doc.Pulls {
		in := ep.Pull
		if in.Repo == "" || in.Number <= 0 {
			continue
		}
		old, err := s.store.GetPullByRef(ctx, uid, in.Repo, in.Number)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail(w, err, "import")
			return
		}
		p := in
		p.ID, p.UserID = 0, uid
		if old != nil {
			p = *old // what is here wins; the import only fills in
		} else {
			if err := s.store.SavePull(ctx, &p); err != nil {
				s.fail(w, err, "import")
				return
			}
			res["pulls"]++
		}
		have, _ := s.store.ViewedFor(ctx, uid, p.ID)
		for path, hs := range ep.Viewed {
			var add []string
			for h := range hs {
				if have[path][h] == "" {
					add = append(add, h)
				}
			}
			if len(add) > 0 {
				_ = s.store.SetViewed(ctx, uid, p.ID, path, add, true)
				res["viewed"] += len(add)
			}
		}
		known, _ := s.store.Comments(ctx, uid, p.ID, "")
		seen := map[string]bool{}
		for _, c := range known {
			seen[fmt.Sprint(c.Path, c.Line, c.Step, c.Body)] = true
		}
		for _, c := range ep.Comments {
			if c.State != "draft" || seen[fmt.Sprint(c.Path, c.Line, c.Step, c.Body)] {
				continue // sent ones live on GitHub
			}
			c.ID, c.UserID, c.PullID = 0, uid, p.ID
			if err := s.store.CreateComment(ctx, &c); err == nil {
				res["comments"]++
			}
		}
		sent, _ := s.store.Reviews(ctx, uid, p.ID, "")
		ids := map[int64]bool{}
		for _, x := range sent {
			ids[x.GHID] = true
		}
		for _, x := range ep.Reviews {
			if x.GHID != 0 && ids[x.GHID] {
				continue
			}
			x.ID, x.UserID, x.PullID = 0, uid, p.ID
			if err := s.store.SaveReview(ctx, &x, nil); err == nil {
				res["reviews"]++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "imported": res})
}
