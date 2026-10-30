package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/pocket"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/walk"
)

// fakeGitHub answers the API calls PR Review makes, for one pull request.
type fakeGitHub struct {
	*httptest.Server
	mu       sync.Mutex
	reviews  []map[string]any
	head     string
	searches []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{head: "aaa111"}
	mux := http.NewServeMux()
	js := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"login": "alice"})
	})
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, r *http.Request) {
		js(w, []any{map[string]any{"full_name": "acme/shop", "private": true, "owner": map[string]any{"login": "acme", "type": "Organization"}},
			map[string]any{"full_name": "alice/notes", "owner": map[string]any{"login": "alice", "type": "User"}},
			map[string]any{"full_name": "acme/old", "archived": true, "owner": map[string]any{"login": "acme", "type": "Organization"}}})
	})
	mux.HandleFunc("GET /search/issues", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.searches = append(g.searches, r.URL.Query().Get("q"))
		g.mu.Unlock()
		items := []any{}
		if strings.Contains(r.URL.Query().Get("q"), "review-requested:@me") {
			items = append(items, map[string]any{"number": 7, "title": "Coupons", "html_url": "https://github.com/acme/shop/pull/7", "state": "open",
				"updated_at": "2026-10-30T10:00:00Z", "repository_url": g.URL + "/repos/acme/shop", "user": map[string]any{"login": "sara"}})
		}
		js(w, map[string]any{"items": items})
	})
	mux.HandleFunc("GET /repos/acme/shop/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		js(w, map[string]any{"number": 7, "title": "Coupons", "body": "Adds coupons.", "html_url": "https://github.com/acme/shop/pull/7", "state": "open",
			"updated_at": "2026-10-30T10:00:00Z", "user": map[string]any{"login": "sara"}, "base": map[string]any{"ref": "main", "sha": "base1"},
			"head": map[string]any{"ref": "coupons", "sha": g.head}, "additions": 3, "deletions": 1, "changed_files": 3, "commits": 1})
	})
	mux.HandleFunc("GET /repos/acme/shop/pulls/7/files", func(w http.ResponseWriter, r *http.Request) {
		js(w, []any{
			map[string]any{"filename": "web/src/Coupons.tsx", "status": "added", "additions": 1, "changes": 1, "patch": "@@ -0,0 +1 @@\n+export default 1"},
			map[string]any{"filename": "db/migrations/7.sql", "status": "added", "additions": 1, "changes": 1, "patch": "@@ -0,0 +1 @@\n+CREATE TABLE coupons (id int);"},
			map[string]any{"filename": "internal/coupons/service.go", "status": "modified", "additions": 1, "deletions": 1, "changes": 2, "patch": "@@ -1,2 +1,2 @@\n package coupons\n-var a = 1\n+var a = 2"},
		})
	})
	mux.HandleFunc("GET /repos/acme/shop/pulls/7/commits", func(w http.ResponseWriter, r *http.Request) {
		js(w, []any{map[string]any{"sha": "aaa111", "commit": map[string]any{"message": "Coupons\n\nAll of it.", "author": map[string]any{"name": "Sara", "date": "2026-10-30T09:00:00Z"}}}})
	})
	mux.HandleFunc("GET /repos/acme/shop/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		out := []any{}
		for _, rv := range g.reviews {
			out = append(out, map[string]any{"id": 1, "user": map[string]any{"login": "alice"}, "state": reviewEvents[rv["event"].(string)], "commit_id": rv["commit_id"]})
		}
		js(w, out)
	})
	mux.HandleFunc("GET /repos/acme/shop/pulls/7/comments", func(w http.ResponseWriter, r *http.Request) { js(w, []any{}) })
	mux.HandleFunc("GET /repos/acme/shop/contents/", func(w http.ResponseWriter, r *http.Request) { http.Error(w, `{"message":"Not Found"}`, 404) })
	mux.HandleFunc("POST /repos/acme/shop/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		g.mu.Lock()
		g.reviews = append(g.reviews, in)
		g.mu.Unlock()
		js(w, map[string]any{"id": 99, "html_url": "https://github.com/acme/shop/pull/7#pullrequestreview-99", "commit_id": in["commit_id"]})
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

type tester struct {
	t   *testing.T
	ts  *httptest.Server
	s   *Server
	tok map[string]string // email → API token
}

func newTester(t *testing.T) (*tester, *fakeGitHub) {
	gh := newFakeGitHub(t)
	dir := t.TempDir()
	cfg := testConfig(dir)
	st := openTestStore(t, cfg)
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	s.ghBase = gh.URL
	s.testAI = nil
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	tt := &tester{t: t, ts: ts, s: s, tok: map[string]string{}}
	for i, email := range []string{"alice@example.com", "bob@example.com"} {
		h, _ := bcrypt.GenerateFromPassword([]byte("password1"), bcrypt.MinCost)
		role := "member"
		if i == 0 {
			role = "admin"
		}
		u := &store.User{Email: email, Name: email[:3], PasswordHash: string(h), Role: role}
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		tok, _, err := st.CreateAPIToken(ctx, u.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
		tt.tok[email] = tok
	}
	return tt, gh
}

func (tt *tester) do(who, method, path string, body any, out any) int {
	tt.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, tt.ts.URL+"/api/v1"+path, rd)
	req.Header.Set("Authorization", "Bearer "+tt.tok[who])
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		tt.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			tt.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return res.StatusCode
}

func (tt *tester) waitWalk(who string, id int64) map[string]any {
	tt.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var page map[string]any
		tt.do(who, "GET", "/pulls/"+itoa64(id), nil, &page)
		if w, ok := page["walkthrough"].(map[string]any); ok && w["status"] == "ready" {
			return page
		}
		time.Sleep(50 * time.Millisecond)
	}
	tt.t.Fatal("the walkthrough was never ready")
	return nil
}

func itoa64(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestPullRequestFlowAndIsolation(t *testing.T) {
	tt, gh := newTester(t)
	const alice, bob = "alice@example.com", "bob@example.com"
	if code := tt.do(alice, "PUT", "/settings", map[string]any{"github_token": "ghp_test", "auto_walk": "off"}, nil); code != 200 {
		t.Fatalf("settings %d", code)
	}

	// saving the token starts a sync; it finds the PR that asks for Alice's review
	var sync struct {
		OK     bool        `json:"ok"`
		Busy   bool        `json:"busy"`
		Result *syncResult `json:"result"`
	}
	for i := 0; i < 100; i++ {
		tt.do(alice, "POST", "/sync", nil, &sync)
		if !sync.Busy {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sync.OK || sync.Result == nil || sync.Result.Found != 1 {
		t.Fatalf("sync: %+v", sync)
	}
	var list struct {
		Pulls []store.Pull `json:"pulls"`
	}
	tt.do(alice, "GET", "/pulls", nil, &list)
	if len(list.Pulls) != 1 || list.Pulls[0].Reason != store.ReasonRequested || list.Pulls[0].HeadSHA != "aaa111" {
		t.Fatalf("list: %+v", list.Pulls)
	}
	id := list.Pulls[0].ID

	// adding it by hand makes the walkthrough (no AI: ordered by layer)
	var added struct {
		Pull store.Pull `json:"pull"`
	}
	if code := tt.do(alice, "POST", "/pulls", map[string]any{"url": "https://github.com/acme/shop/pull/7"}, &added); code != 200 || added.Pull.ID != id {
		t.Fatalf("add: %d %+v", code, added.Pull)
	}
	page := tt.waitWalk(alice, id)
	doc := page["walkthrough"].(map[string]any)["doc"].(map[string]any)
	steps := doc["steps"].([]any)
	if len(steps) != 3 || steps[0].(map[string]any)["title"] != "Database and migrations" || steps[2].(map[string]any)["title"] != "User interface" {
		t.Fatalf("steps: %v", steps)
	}
	if n := len(page["files"].([]any)); n != 3 {
		t.Fatalf("files %d", n)
	}
	tl := page["walkthrough"].(map[string]any)["timeline"].([]any)
	if len(tl) != 1 || tl[0].(map[string]any)["title"] != "Coupons" {
		t.Fatalf("timeline: %v", tl)
	}

	// Bob sees none of it
	var bobList struct {
		Pulls []store.Pull `json:"pulls"`
	}
	tt.do(bob, "GET", "/pulls", nil, &bobList)
	if len(bobList.Pulls) != 0 {
		t.Fatal("Bob sees Alice's pull requests")
	}
	for _, p := range []string{"/pulls/" + itoa64(id), "/pulls/" + itoa64(id) + "/walkthrough.md", "/pulls/" + itoa64(id) + "/comments"} {
		if code := tt.do(bob, "GET", p, nil, nil); code != 404 {
			t.Fatalf("Bob GET %s: %d", p, code)
		}
	}
	if code := tt.do(bob, "POST", "/pulls/"+itoa64(id)+"/review", map[string]any{"event": "APPROVE"}, nil); code != 404 {
		t.Fatalf("Bob reviews Alice's PR: %d", code)
	}

	// mark the first step as looked at
	unit := steps[0].(map[string]any)["units"].([]any)[0].(map[string]any)
	var v struct {
		Viewed map[string]map[string]string `json:"viewed"`
	}
	tt.do(alice, "PUT", "/pulls/"+itoa64(id)+"/viewed", map[string]any{"items": []any{map[string]any{"path": unit["path"], "hashes": unit["hashes"]}}, "viewed": true}, &v)
	if len(v.Viewed["db/migrations/7.sql"]) != 1 {
		t.Fatalf("viewed: %v", v.Viewed)
	}
	if code := tt.do(bob, "PUT", "/pulls/"+itoa64(id)+"/viewed", map[string]any{"items": []any{map[string]any{"path": "x", "hashes": []string{"y"}}}, "viewed": true}, nil); code != 404 {
		t.Fatalf("Bob marks: %d", code)
	}

	// a line comment and a step note, then the review goes to GitHub
	var c store.Comment
	if code := tt.do(alice, "POST", "/pulls/"+itoa64(id)+"/comments", map[string]any{"path": "internal/coupons/service.go", "line": 2, "side": "RIGHT", "body": "Why 2?"}, &c); code != 201 {
		t.Fatalf("comment %d", code)
	}
	if code := tt.do(bob, "DELETE", "/pulls/"+itoa64(id)+"/comments/"+itoa64(c.ID), nil, nil); code != 404 {
		t.Fatalf("Bob deletes Alice's comment: %d", code)
	}
	tt.do(alice, "POST", "/pulls/"+itoa64(id)+"/comments", map[string]any{"step": "Logic and services", "body": "Fine otherwise."}, nil)
	var sent struct {
		OK bool `json:"ok"`
	}
	if code := tt.do(alice, "POST", "/pulls/"+itoa64(id)+"/review", map[string]any{"event": "REQUEST_CHANGES", "body": "Close."}, &sent); code != 200 || !sent.OK {
		t.Fatalf("review %d", code)
	}
	gh.mu.Lock()
	got := gh.reviews[0]
	gh.mu.Unlock()
	if got["event"] != "REQUEST_CHANGES" || got["commit_id"] != "aaa111" || !strings.Contains(got["body"].(string), "**Logic and services** — Fine otherwise.") {
		t.Fatalf("sent to GitHub: %v", got)
	}
	if cs := got["comments"].([]any); len(cs) != 1 || cs[0].(map[string]any)["line"] != float64(2) {
		t.Fatalf("line comments: %v", got["comments"])
	}
	var drafts []store.Comment
	tt.do(alice, "GET", "/pulls/"+itoa64(id)+"/comments?state=draft", nil, &drafts)
	if len(drafts) != 0 {
		t.Fatal("drafts left after sending")
	}
	var day struct {
		Count int `json:"count"`
	}
	tt.do(alice, "GET", "/day/"+time.Now().Format("2006-01-02"), nil, &day)
	if day.Count != 1 {
		t.Fatalf("day: %+v", day)
	}

	// export, then import into Bob twice: the second time adds nothing
	req, _ := http.NewRequest("GET", tt.ts.URL+"/api/v1/export.json", nil)
	req.Header.Set("Authorization", "Bearer "+tt.tok[alice])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	export, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var exp map[string]any
	_ = json.Unmarshal(export, &exp)
	var imp struct {
		Imported map[string]int `json:"imported"`
	}
	var raw map[string]any
	code := tt.do(bob, "POST", "/import", exp, &raw)
	b2, _ := json.Marshal(raw)
	_ = json.Unmarshal(b2, &imp)
	if imp.Imported["pulls"] != 1 || imp.Imported["reviews"] != 1 || imp.Imported["viewed"] != 1 {
		t.Fatalf("import %d: %v (%d bytes exported)", code, raw, len(export))
	}
	tt.do(bob, "POST", "/import", exp, &imp)
	for k, n := range imp.Imported {
		if n != 0 {
			t.Fatalf("second import added %d %s", n, k)
		}
	}

	// a new commit: the walkthrough is stale; what did not change stays looked at
	gh.mu.Lock()
	gh.head = "bbb222"
	gh.mu.Unlock()
	if code := tt.do(alice, "POST", "/pulls/"+itoa64(id)+"/walkthrough", nil, nil); code != 202 {
		t.Fatalf("rewalk %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var p map[string]any
		tt.do(alice, "GET", "/pulls/"+itoa64(id), nil, &p)
		if w, ok := p["walkthrough"].(map[string]any); ok && w["head_sha"] == "bbb222" && w["status"] == "ready" {
			vw := p["viewed"].(map[string]any)
			if vw["db/migrations/7.sql"] == nil {
				t.Fatal("an unchanged file lost its mark")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no new walkthrough")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLinkScopes(t *testing.T) {
	r := func(method, path string) *http.Request { return httptest.NewRequest(method, path, nil) }
	cases := []struct {
		scopes []string
		req    *http.Request
		ok     bool
	}{
		{[]string{"prs:read"}, r("GET", "/api/v1/pulls"), true},
		{[]string{"prs:read"}, r("GET", "/api/v1/pulls/3/walkthrough.md"), true},
		{[]string{"prs:read"}, r("POST", "/api/v1/pulls"), false},
		{[]string{"prs:write"}, r("POST", "/api/v1/pulls"), true},
		{[]string{"prs:write"}, r("POST", "/api/v1/pulls/3/review"), false},
		{[]string{"prs:write"}, r("GET", "/api/v1/lookup"), true},
		{[]string{"prs:read", "prs:write"}, r("PUT", "/api/v1/settings"), false},
		{[]string{"prs:read"}, r("GET", "/api/v1/day/2026-10-30"), true},
		{nil, r("GET", "/api/v1/auth/me"), true},
	}
	for _, c := range cases {
		if got := linkAllows(c.scopes, c.req); got != c.ok {
			t.Errorf("%v %s %s: %v, want %v", c.scopes, c.req.Method, c.req.URL.Path, got, c.ok)
		}
	}
}

func TestDemoSeeds(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.DemoData, cfg.AuthMode = true, "none"
	st := openTestStore(t, cfg)
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err := s.Bootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Router())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/v1/pulls/1/walkthrough.md")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	for _, want := range []string{"The coupons table", "Routes for checkout and admins", "chatbot/tools.yaml", "package-lock.json"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("demo walkthrough lacks %q:\n%s", want, b)
		}
	}
}

func TestMatchEdits(t *testing.T) {
	files := []walk.File{{Path: "db/7.sql"}, {Path: "web/src/App.tsx"}}
	got := matchEdits([]pocket.AgentEdit{{Path: "/work/shop/db/7.sql"}, {Path: "src/App.tsx"}, {Path: "notes.txt"}}, files)
	if len(got) != 2 || got[0].Path != "db/7.sql" || got[1].Path != "web/src/App.tsx" {
		t.Fatalf("%+v", got)
	}
}

func TestPickFromGitHub(t *testing.T) {
	tt, gh := newTester(t)
	const alice = "alice@example.com"
	tt.do(alice, "PUT", "/settings", map[string]any{"github_token": "ghp_test", "auto_walk": "off", "sync_requested": false, "sync_authored": false, "sync_reviewed": false}, nil)
	var repos struct {
		Repos []repoOut `json:"repos"`
	}
	tt.do(alice, "GET", "/github/repos", nil, &repos)
	if len(repos.Repos) != 2 || repos.Repos[0].Name != "acme/shop" || !repos.Repos[0].Org {
		t.Fatalf("repos: %+v", repos.Repos)
	}
	var pulls struct {
		Pulls []pickOut `json:"pulls"`
		Error string    `json:"error"`
	}
	// everything = the owners of the person's repositories, never all of GitHub
	tt.do(alice, "GET", "/github/pulls?scope=all&q=coupons", nil, &pulls)
	gh.mu.Lock()
	last := gh.searches[len(gh.searches)-1]
	gh.mu.Unlock()
	if !strings.Contains(last, "org:acme") || !strings.Contains(last, "user:alice") || !strings.Contains(last, "coupons") || !strings.Contains(last, "is:open") {
		t.Fatalf("query: %q", last)
	}
	tt.do(alice, "GET", "/github/pulls?scope=review&repo=acme/shop", nil, &pulls)
	if len(pulls.Pulls) != 1 || pulls.Pulls[0].Repo != "acme/shop" || pulls.Pulls[0].Number != 7 || pulls.Pulls[0].Added != 0 {
		t.Fatalf("pulls: %+v %s", pulls.Pulls, pulls.Error)
	}
	var added struct {
		Pull store.Pull `json:"pull"`
	}
	tt.do(alice, "POST", "/pulls", map[string]any{"url": pulls.Pulls[0].URL, "walk": false}, &added)
	tt.do(alice, "GET", "/github/pulls?scope=review&repo=acme/shop", nil, &pulls)
	if pulls.Pulls[0].Added != added.Pull.ID || added.Pull.ID == 0 {
		t.Fatalf("not marked as added: %+v", pulls.Pulls)
	}
	if code := tt.do(alice, "GET", "/github/pulls?repo=not%20a%20repo", nil, nil); code != 400 {
		t.Fatalf("bad repo: %d", code)
	}
}
