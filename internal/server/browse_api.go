package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// ---- choosing a pull request on GitHub (the Add dialog) ----

type repoOut struct {
	Name     string `json:"name"` // owner/name
	Private  bool   `json:"private"`
	PushedAt string `json:"pushed_at"`
	Org      bool   `json:"org"`
}

// reposFor lists the repositories the person's token reaches (cached ten minutes).
func (s *Server) reposFor(r *http.Request, st store.Settings) ([]repoOut, error) {
	ctx := r.Context()
	uid := actorID(ctx)
	key := fmt.Sprintf("repos:%d", uid)
	if v, at, ok := s.store.CacheGet(ctx, key); ok && !queryBool(r, "fresh") {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < 10*time.Minute {
			var out []repoOut
			if json.Unmarshal([]byte(v), &out) == nil {
				return out, nil
			}
		}
	}
	gh, err := s.ghFor(st)
	if err != nil {
		return nil, err
	}
	list, err := gh.Repos(ctx)
	if err != nil {
		return nil, err
	}
	out := []repoOut{}
	for _, rp := range list {
		if rp.Archived {
			continue
		}
		out = append(out, repoOut{Name: strings.ToLower(rp.FullName), Private: rp.Private, PushedAt: rp.PushedAt, Org: rp.Owner.Type == "Organization"})
	}
	b, _ := json.Marshal(out)
	_ = s.store.CachePut(ctx, key, string(b))
	return out, nil
}

func (s *Server) handleGitHubRepos(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.cfg.DemoData {
		writeJSON(w, http.StatusOK, map[string]any{"repos": demoRepos()})
		return
	}
	st, _ := s.store.GetSettings(ctx, actorID(ctx))
	repos, err := s.reposFor(r, st)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"repos": []repoOut{}, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

// pickOut is an open pull request one can add.
type pickOut struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Author    string `json:"author"`
	Avatar    string `json:"author_avatar"`
	Draft     bool   `json:"draft"`
	UpdatedAt string `json:"updated_at"`
	Added     int64  `json:"added"` // the pull request's id here, 0 = not on the list
}

var searchWords = regexp.MustCompile(`[^\p{L}\p{N} ._#/-]+`)

// handleGitHubPulls lists open pull requests the person can reach:
// ?scope=review|mine|involved|all, ?repo=owner/name, ?q=words.
func (s *Server) handleGitHubPulls(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	q := r.URL.Query()
	scope, repo := q.Get("scope"), store.NormRepo(q.Get("repo"))
	words := strings.TrimSpace(searchWords.ReplaceAllString(q.Get("q"), " "))
	if len(words) > 120 {
		words = words[:120]
	}
	var items []pickOut
	if s.cfg.DemoData {
		items = demoPicks(scope, repo, words)
	} else {
		st, _ := s.store.GetSettings(ctx, uid)
		gh, err := s.ghFor(st)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"pulls": []pickOut{}, "error": err.Error()})
			return
		}
		query := "is:pr is:open archived:false"
		switch scope {
		case "review":
			query += " review-requested:@me"
		case "mine":
			query += " author:@me"
		case "involved":
			query += " involves:@me"
		}
		if repo != "" {
			if !repoRe.MatchString(repo) || !strings.Contains(repo, "/") {
				writeErr(w, http.StatusBadRequest, "repo is owner/name")
				return
			}
			query += " repo:" + repo
		} else if scope == "" || scope == "all" {
			// everything the person can reach: their repositories' owners (or the
			// repositories chosen in Settings), never all of GitHub
			owners := map[string]bool{}
			var quals []string
			if strings.TrimSpace(st.SyncRepos) != "" {
				for _, f := range strings.Fields(st.SyncRepos) {
					if strings.Contains(f, "/") {
						quals = append(quals, "repo:"+f)
					} else {
						quals = append(quals, "user:"+f)
					}
				}
			} else {
				repos, err := s.reposFor(r, st)
				if err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"pulls": []pickOut{}, "error": err.Error()})
					return
				}
				for _, rp := range repos {
					o := strings.SplitN(rp.Name, "/", 2)[0]
					if !owners[o] && len(quals) < 15 {
						owners[o] = true
						if rp.Org {
							quals = append(quals, "org:"+o)
						} else {
							quals = append(quals, "user:"+o)
						}
					}
				}
			}
			if len(quals) == 0 {
				query += " involves:@me"
			} else {
				query += " " + strings.Join(quals, " ")
			}
		}
		if words != "" {
			query += " " + words
		}
		found, err := gh.Search(ctx, query, 60)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"pulls": []pickOut{}, "error": err.Error()})
			return
		}
		for _, it := range found {
			if it.Repo() == "" {
				continue
			}
			items = append(items, pickOut{Repo: it.Repo(), Number: it.Number, Title: it.Title, URL: it.HTMLURL, Author: it.User.Login,
				Avatar: it.User.AvatarURL, Draft: it.Draft, UpdatedAt: it.UpdatedAt})
		}
	}
	have, _ := s.store.SyncedPulls(ctx, uid)
	for i := range items {
		if p := have[fmt.Sprintf("%s#%d", items[i].Repo, items[i].Number)]; p != nil && !p.Archived {
			items[i].Added = p.ID
		}
	}
	if items == nil {
		items = []pickOut{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pulls": items})
}

// ---- the demo's GitHub ----

func demoRepos() []repoOut {
	return []repoOut{{Name: "acme/shop", Private: true, Org: true}, {Name: "acme/mobile", Private: true, Org: true}, {Name: "acme/infra", Private: true, Org: true}, {Name: "maya/dotfiles"}}
}

func demoPicks(scope, repo, words string) []pickOut {
	now := time.Now()
	all := []pickOut{}
	for _, d := range demoPRs() {
		all = append(all, pickOut{Repo: d.repo, Number: d.number, Title: d.title, URL: fmt.Sprintf("https://github.com/%s/pull/%d", d.repo, d.number), Author: d.author, UpdatedAt: store.TS(now.Add(-d.ago))})
	}
	more := []struct {
		repo, title, author string
		n                   int
		ago                 time.Duration
	}{
		{"acme/shop", "Search: rank products by sales in the last 30 days", "marko-p", 145, 2 * time.Hour},
		{"acme/shop", "Checkout: remember the last delivery address", "lena", 141, 9 * time.Hour},
		{"acme/mobile", "Push notifications for order status", "sara-k", 61, 30 * time.Hour},
		{"acme/infra", "Nightly database dumps to object storage", "dino", 14, 50 * time.Hour},
	}
	for _, m := range more {
		all = append(all, pickOut{Repo: m.repo, Number: m.n, Title: m.title, URL: fmt.Sprintf("https://github.com/%s/pull/%d", m.repo, m.n), Author: m.author, UpdatedAt: store.TS(now.Add(-m.ago))})
	}
	var out []pickOut
	for _, p := range all {
		if repo != "" && p.Repo != repo {
			continue
		}
		if scope == "mine" && p.Author != "maya" || scope == "review" && p.Author == "maya" {
			continue
		}
		if words != "" && !strings.Contains(strings.ToLower(p.Title+" "+p.Repo+fmt.Sprint(p.Number)), strings.ToLower(words)) {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}
