package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/config"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// What Pocket Search, Pocket Day and Pocket Hub ask (capabilities.go). Alice
// has her own pull requests and one she was asked to review; Bob has one of
// his own. GitHub is a tripwire: none of these routes may call it.

type capTester struct {
	*tester
	core       *fakeCapCore
	ghCalls    *atomic.Int64
	alice, bob *store.User
}

// fakeCapCore is a Pocket Core that vouches for link tokens
// pcx_<alice|bob>_<search|day|widget|capture|read|write> from Pocket Hub and
// remembers the manifest PR Review registers with.
type fakeCapCore struct {
	*httptest.Server
	mu       sync.Mutex
	manifest map[string]any
}

func newCapTester(t *testing.T) *capTester {
	t.Helper()
	fc := &fakeCapCore{}
	scopes := map[string]string{"search": core.ScopeSearch, "day": core.ScopeDay, "widget": core.ScopeWidget, "capture": core.ScopeCapture,
		"read": "prs:read", "write": "prs:write"}
	fc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/apps/register":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			fc.mu.Lock()
			fc.manifest = m
			fc.mu.Unlock()
			io.WriteString(w, `{}`)
		case "/api/v1/auth/introspect":
			var in struct{ Token string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			parts := strings.Split(in.Token, "_")
			if len(parts) != 3 || parts[0] != "pcx" || scopes[parts[2]] == "" {
				io.WriteString(w, `{"active":false}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "kind": "delegated", "audience": "pr-review", "scope": scopes[parts[2]],
				"user": map[string]any{"id": 1, "sub": parts[1] + "@example.com", "email": parts[1] + "@example.com", "name": parts[1], "role": "member"},
				"app":  map[string]any{"id": 9, "slug": "hub", "name": "Pocket Hub"}})
		default:
			io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(fc.Close)
	tt, _ := newTester(t, func(c *config.Config) { c.CoreURL, c.CoreRegistrationKey = fc.URL, "key" })
	calls := &atomic.Int64{}
	trip := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"message":"nobody should ask GitHub here"}`, 500)
	}))
	t.Cleanup(trip.Close)
	tt.s.ghBase = trip.URL
	ctx := context.Background()
	ct := &capTester{tester: tt, core: fc, ghCalls: calls}
	ct.alice, _ = tt.s.store.GetUserByEmail(ctx, "alice@example.com")
	ct.bob, _ = tt.s.store.GetUserByEmail(ctx, "bob@example.com")
	for _, u := range []*store.User{ct.alice, ct.bob} {
		st, _ := tt.s.store.GetSettings(ctx, u.ID)
		st.GitHubToken, st.GitHubLogin = "ghp_test", u.Email[:strings.Index(u.Email, "@")]
		if err := tt.s.store.SaveUserSettings(ctx, u.ID, st); err != nil {
			t.Fatal(err)
		}
	}
	return ct
}

// pull keeps a pull request for a person as the sync would.
func (ct *capTester) pull(u *store.User, p store.Pull) *store.Pull {
	ct.t.Helper()
	p.UserID = u.ID
	if p.State == "" {
		p.State = "open"
	}
	if p.Source == "" {
		p.Source = "sync"
	}
	if err := ct.s.store.SavePull(context.Background(), &p); err != nil {
		ct.t.Fatal(err)
	}
	return &p
}

// status keeps where a pull request stood the last time the board looked, ago.
func (ct *capTester) status(u *store.User, p *store.Pull, st *github.Status, ago time.Duration) {
	ct.t.Helper()
	b, _ := json.Marshal(statusOut{Status: st, CheckedAt: time.Now().UTC().Format(time.RFC3339)})
	key := "status:" + itoa64(u.ID) + ":" + itoa64(p.ID)
	if _, err := ct.s.store.DB.Exec(`INSERT INTO cache (key, value, fetched_at) VALUES (?, ?, ?)`, key, string(b), store.TS(time.Now().Add(-ago))); err != nil {
		ct.t.Fatal(err)
	}
}

// walk keeps a finished walkthrough with this title, ready at that moment.
func (ct *capTester) walk(u *store.User, p *store.Pull, title, tldr, at string) int64 {
	ct.t.Helper()
	ctx := context.Background()
	w, err := ct.s.store.CreateWalk(ctx, u.ID, p.ID, p.HeadSHA, p.BaseSHA)
	if err != nil {
		ct.t.Fatal(err)
	}
	doc, _ := json.Marshal(map[string]any{"title": title, "tldr": tldr, "steps": []any{}})
	if err := ct.s.store.FinishWalk(ctx, w.ID, store.WalkReady, "", "", doc, nil); err != nil {
		ct.t.Fatal(err)
	}
	ct.stamp(`UPDATE walkthroughs SET finished_at = ? WHERE id = ?`, at, w.ID)
	return w.ID
}

// stamp moves when something happened (nothing in the API can).
func (ct *capTester) stamp(query string, args ...any) {
	ct.t.Helper()
	res, err := ct.s.store.DB.Exec(query, args...)
	if err != nil {
		ct.t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		ct.t.Fatalf("nothing stamped by %s %v", query, args)
	}
}

// ask calls a route with a person's own token or a link token and reads the
// JSON answer into out (emptied first).
func (ct *capTester) ask(who, method, path string, body []byte, out any) int {
	ct.t.Helper()
	tok := who
	if t, ok := ct.tok[who+"@example.com"]; ok {
		tok = t
	}
	code, b := ct.moveRaw(tok, method, path, "application/json", body)
	if out != nil {
		reflect.ValueOf(out).Elem().SetZero()
		if err := json.Unmarshal(b, out); err != nil {
			ct.t.Fatalf("%s %s %s: %d %s", who, method, path, code, b)
		}
	}
	return code
}

func capTitles[T any](items []T, line func(T) string) string {
	out := []string{}
	for _, it := range items {
		out = append(out, line(it))
	}
	return strings.Join(out, " | ")
}

var (
	failing = &github.Status{State: "OPEN", Mergeable: "MERGEABLE", MergeState: "UNSTABLE", Approved: []string{"sara", "dino"},
		Checks: github.Checks{Known: true, State: "FAILURE", Passed: 3, Failed: 1}}
	readyToMerge = &github.Status{State: "OPEN", Mergeable: "MERGEABLE", MergeState: "CLEAN", Approved: []string{"sara"},
		Checks: github.Checks{Known: true, State: "SUCCESS", Passed: 4}}
	changesAsked = &github.Status{State: "OPEN", Mergeable: "MERGEABLE", MergeState: "BLOCKED", ChangesWanted: []string{"dino"},
		Checks: github.Checks{Known: true, State: "SUCCESS", Passed: 4}}
	running = &github.Status{State: "OPEN", Mergeable: "MERGEABLE", MergeState: "BLOCKED", Requested: []string{"sara"},
		Checks: github.Checks{Known: true, State: "PENDING", Passed: 1, Running: 2}}
)

func TestPocketSearch(t *testing.T) {
	ct := newCapTester(t)
	ct.s.now = func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) } // "20 Nov" wherever the tests run
	coupons := ct.pull(ct.alice, store.Pull{Repo: "acme/shop", Number: 7, Title: "Coupons at checkout", Author: "alice", Reason: store.ReasonAuthored,
		HeadRef: "coupon-codes", URL: "https://github.com/acme/shop/pull/7", GHUpdatedAt: "2026-11-20T10:00:00Z"})
	ct.status(ct.alice, coupons, failing, time.Minute)
	asked := ct.pull(ct.alice, store.Pull{Repo: "acme/api", Number: 12, Title: "Rate limits", Author: "sara", Reason: store.ReasonRequested,
		HeadRef: "limits", URL: "https://github.com/acme/api/pull/12", MyReview: "APPROVED", GHUpdatedAt: "2026-11-21T10:00:00Z"})
	ct.walk(ct.alice, asked, "Throttling every route", "A token bucket in front of the handlers", "2026-11-21T11:00:00Z")
	merged := ct.pull(ct.alice, store.Pull{Repo: "acme/shop", Number: 3, Title: "Old cart", Author: "alice", Reason: store.ReasonAuthored, State: "merged",
		HeadRef: "cart", GHUpdatedAt: "2026-11-01T10:00:00Z"})
	stale := ct.pull(ct.alice, store.Pull{Repo: "acme/shop", Number: 9, Title: "Coupon admin", Author: "alice", Reason: store.ReasonAuthored, GHUpdatedAt: "2026-11-19T10:00:00Z"})
	ct.status(ct.alice, stale, failing, 48*time.Hour) // too old to say
	ct.pull(ct.bob, store.Pull{Repo: "acme/shop", Number: 8, Title: "Bob's coupons", Author: "bob", Reason: store.ReasonAuthored, HeadRef: "coupon-bob"})
	bobs := ct.pull(ct.bob, store.Pull{Repo: "acme/web", Number: 2, Title: "Header", Author: "bob", Reason: store.ReasonAuthored})
	ct.walk(ct.bob, bobs, "Throttling the header", "", "2026-11-21T11:00:00Z")

	var got core.SearchResults
	if code := ct.ask("alice", "GET", "/api/v1/pocket/search?q=coupon", nil, &got); code != 200 || len(got.Items) != 2 {
		t.Fatalf("search: %d %+v", code, got)
	}
	it := got.Items[0]
	if it.ID != "pull:"+itoa64(coupons.ID) || it.Title != "Coupons at checkout" || it.Kind != "pull request" || it.URL != "/pr/"+itoa64(coupons.ID) ||
		it.Link != "https://github.com/acme/shop/pull/7" || it.Date != "2026-11-20T10:00:00Z" ||
		it.Snippet != "acme/shop#7 · open, 2 approvals, CI failing · updated 20 Nov" {
		t.Errorf("my pull request: %+v", it)
	}
	if it := got.Items[1]; it.Title != "Coupon admin" || it.Snippet != "acme/shop#9 · open · updated 19 Nov" {
		t.Errorf("a status two days old says nothing: %+v", it)
	}
	// by branch, number, address; a merged one says so
	for q, want := range map[string]string{"coupon-codes": "Coupons at checkout", "%2312": "Rate limits", "acme/shop%233": "Old cart", "cart": "Old cart",
		"https://github.com/acme/api/pull/12": "Rate limits", "acme/api": "Rate limits"} {
		if ct.ask("alice", "GET", "/api/v1/pocket/search?q="+q, nil, &got); len(got.Items) == 0 || got.Items[0].Title != want {
			t.Errorf("q=%s: %+v", q, got.Items)
		}
	}
	if ct.ask("alice", "GET", "/api/v1/pocket/search?q=old+cart", nil, &got); len(got.Items) != 1 || got.Items[0].Snippet != "acme/shop#3 · merged · updated 1 Nov" ||
		got.Items[0].URL != "/pr/"+itoa64(merged.ID) {
		t.Errorf("merged: %+v", got.Items)
	}
	// one I was asked to review: what I said, without a status
	if ct.ask("alice", "GET", "/api/v1/pocket/search?q=rate", nil, &got); len(got.Items) != 1 || got.Items[0].Snippet != "acme/api#12 · open, you approved · updated 21 Nov" {
		t.Errorf("asked to review: %+v", got.Items)
	}
	// a walkthrough by what its story says, opening that walkthrough
	if ct.ask("alice", "GET", "/api/v1/pocket/search?q=token+bucket", nil, &got); len(got.Items) != 1 || got.Items[0].Kind != "walkthrough" ||
		got.Items[0].Title != "Throttling every route" || !strings.HasPrefix(got.Items[0].URL, "/pr/"+itoa64(asked.ID)+"?v=") ||
		got.Items[0].Date != "2026-11-21T11:00:00Z" || got.Items[0].Snippet != "acme/api#12 · open, you approved · made 21 Nov" {
		t.Errorf("walkthrough: %+v", got.Items)
	}
	if ct.ask("alice", "GET", "/api/v1/pocket/search?q=coupon&limit=1", nil, &got); len(got.Items) != 1 {
		t.Errorf("limit=1: %+v", got.Items)
	}
	// nothing to look for: nothing found (find would list everything)
	for _, q := range []string{"", "%3F%3F", "-", "%2A"} {
		if code := ct.ask("alice", "GET", "/api/v1/pocket/search?q="+q, nil, &got); code != 200 || got.Items == nil || len(got.Items) != 0 {
			t.Errorf("q=%q: %d %+v", q, code, got)
		}
	}
	// strictly per person
	for _, q := range []string{"bob", "header", "throttling", "acme/web%232", "8"} {
		ct.ask("alice", "GET", "/api/v1/pocket/search?q="+q, nil, &got)
		for _, it := range got.Items {
			if strings.Contains(it.Title, "Bob") || strings.Contains(it.Title, "header") || strings.Contains(it.Snippet, "acme/web") {
				t.Errorf("alice finds bob's with %q: %+v", q, it)
			}
		}
	}
	if ct.ask("bob", "GET", "/api/v1/pocket/search?q=coupon", nil, &got); len(got.Items) != 1 || got.Items[0].Title != "Bob's coupons" {
		t.Errorf("bob: %+v", got.Items)
	}
	if code := ct.ask("nobody", "GET", "/api/v1/pocket/search?q=coupon", nil, nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if n := ct.ghCalls.Load(); n != 0 {
		t.Errorf("search asked GitHub %d times", n)
	}
}

func TestPocketDay(t *testing.T) {
	ct := newCapTester(t)
	ctx := context.Background()
	honolulu, _ := time.LoadLocation("Pacific/Honolulu")
	// Honolulu is ten hours behind UTC: the 20th there runs 10:00 UTC on the 20th to 10:00 on the 21st
	shipped := ct.pull(ct.alice, store.Pull{Repo: "acme/shop", Number: 7, Title: "Coupons at checkout", Author: "alice", Reason: store.ReasonAuthored,
		State: "merged", GHUpdatedAt: "2026-11-21T09:30:00Z"}) // 23:30 on the 20th
	ct.pull(ct.alice, store.Pull{Repo: "acme/shop", Number: 5, Title: "Merged the day before", Author: "alice", Reason: store.ReasonAuthored,
		State: "merged", GHUpdatedAt: "2026-11-20T09:30:00Z"})
	ct.pull(ct.alice, store.Pull{Repo: "acme/shop", Number: 6, Title: "Closed, not merged", Author: "alice", Reason: store.ReasonAuthored,
		State: "closed", GHUpdatedAt: "2026-11-20T20:00:00Z"})
	ct.pull(ct.alice, store.Pull{Repo: "acme/shop", Number: 4, Title: "Sara's, merged", Author: "sara", Reason: store.ReasonReviewed,
		State: "merged", GHUpdatedAt: "2026-11-20T20:00:00Z"}) // not mine: not my merge
	asked := ct.pull(ct.alice, store.Pull{Repo: "acme/api", Number: 12, Title: "Rate limits", Author: "sara", Reason: store.ReasonRequested})
	wid := ct.walk(ct.alice, asked, "Throttling", "", "2026-11-20T22:00:00.25Z") // 12:00
	ct.walk(ct.alice, shipped, "Coupons", "", "2026-11-21T10:00:00Z")            // midnight: the 21st
	sent := time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local)
	for _, ev := range []string{"APPROVE", "COMMENT"} {
		rv := &store.Review{UserID: ct.alice.ID, PullID: asked.ID, Event: ev, Day: "2026-11-20"}
		if err := ct.s.store.SaveReview(ctx, rv, nil); err != nil {
			t.Fatal(err)
		}
		ct.stamp(`UPDATE reviews SET created_at = ? WHERE id = ?`, store.TS(sent), rv.ID)
	}
	// Bob's day is his own
	bobs := ct.pull(ct.bob, store.Pull{Repo: "acme/web", Number: 2, Title: "Bob's", Author: "bob", Reason: store.ReasonAuthored, State: "merged", GHUpdatedAt: "2026-11-20T20:00:00Z"})
	ct.walk(ct.bob, bobs, "Bob's story", "", "2026-11-20T20:00:00Z")

	var day core.Day
	if code := ct.ask("alice", "GET", "/api/v1/pocket/day/2026-11-20?tz=Pacific/Honolulu", nil, &day); code != 200 {
		t.Fatalf("day: %d", code)
	}
	if day.Summary != "1 pull request merged, 2 reviews sent, 1 walkthrough made" || len(day.Items) != 4 {
		t.Fatalf("the 20th in Honolulu: %+v", day)
	}
	by := map[string]core.DayItem{}
	for _, it := range day.Items {
		by[it.ID] = it
	}
	if it := by["pull:"+itoa64(shipped.ID)]; it.Title != "Coupons at checkout" || it.Sub != "acme/shop#7" || it.Kind != "merged" || !it.Done || it.Time != "23:30" || it.URL != "/pr/"+itoa64(shipped.ID) {
		t.Errorf("merged: %+v", it)
	}
	if it := by["walkthrough:"+itoa64(wid)]; it.Title != "Rate limits" || it.Sub != "acme/api#12 · walkthrough" || it.Kind != "walkthrough" || it.Time != "12:00" ||
		it.URL != "/pr/"+itoa64(asked.ID)+"?v="+itoa64(wid) {
		t.Errorf("walkthrough: %+v", it)
	}
	wantClock := ""
	if sent.In(honolulu).Format("2006-01-02") == "2026-11-20" {
		wantClock = sent.In(honolulu).Format("15:04")
	}
	subs := []string{}
	for _, it := range day.Items {
		if it.Kind == "reviewed" {
			subs = append(subs, it.Sub)
			if it.Title != "Rate limits" || it.Time != wantClock || it.URL != "/pr/"+itoa64(asked.ID) {
				t.Errorf("review: %+v (want time %q)", it, wantClock)
			}
		}
	}
	if strings.Join(subs, " | ") != "acme/api#12 · commented | acme/api#12 · approved" {
		t.Errorf("reviews: %v", subs)
	}
	want := []core.Metric{{Label: "Merged", Value: "1"}, {Label: "Reviews", Value: "2"}, {Label: "Approved", Value: "1"}, {Label: "Walkthroughs", Value: "1"}}
	if !reflect.DeepEqual(day.Metrics, want) {
		t.Errorf("metrics: %+v", day.Metrics)
	}
	// the reviews are the ones GET /day files under that day
	var old map[string]any
	ct.do("alice@example.com", "GET", "/day/2026-11-20", nil, &old)
	if old["count"] != float64(2) || old["approved"] != float64(1) {
		t.Errorf("GET /day: %v", old)
	}
	// the next day there: the walkthrough at midnight, nothing merged
	if ct.ask("alice", "GET", "/api/v1/pocket/day/2026-11-21?tz=Pacific/Honolulu", nil, &day); day.Summary != "1 walkthrough made" || len(day.Items) != 1 || day.Items[0].Time != "00:00" {
		t.Errorf("the 21st in Honolulu: %+v", day)
	}
	// strictly per person
	if ct.ask("bob", "GET", "/api/v1/pocket/day/2026-11-20?tz=Pacific/Honolulu", nil, &day); day.Summary != "1 pull request merged, 1 walkthrough made" ||
		strings.Contains(capTitles(day.Items, func(it core.DayItem) string { return it.Title }), "Rate") {
		t.Errorf("bob's 20th: %+v", day)
	}
	if code := ct.ask("alice", "GET", "/api/v1/pocket/day/1999-01-01", nil, &day); code != 200 || day.Summary != "" || day.Items == nil || len(day.Items) != 0 {
		t.Errorf("a day with nothing: %d %+v", code, day)
	}
	if code := ct.ask("alice", "GET", "/api/v1/pocket/day/yesterday", nil, nil); code != 400 {
		t.Errorf("not a day: %d", code)
	}
	if n := ct.ghCalls.Load(); n != 0 {
		t.Errorf("the day asked GitHub %d times", n)
	}
}

func TestPocketWidget(t *testing.T) {
	ct := newCapTester(t)
	var got core.WidgetData
	// nothing of mine open: nothing to show
	ct.pull(ct.alice, store.Pull{Repo: "acme/api", Number: 12, Title: "Rate limits", Author: "sara", Reason: store.ReasonRequested})
	if code := ct.ask("alice", "GET", "/api/v1/pocket/widget", nil, &got); code != 200 || got.Line != "" || got.Items == nil || len(got.Items) != 0 || len(got.Metrics) != 0 {
		t.Fatalf("nothing of mine: %d %+v", code, got)
	}

	// newest activity first within each group (the list's order)
	mk := func(n int, title, at string, st *github.Status) *store.Pull {
		p := ct.pull(ct.alice, store.Pull{Repo: "acme/shop", Number: n, Title: title, Author: "alice", Reason: store.ReasonAuthored, GHUpdatedAt: at})
		if st != nil {
			ct.status(ct.alice, p, st, time.Minute)
		}
		return p
	}
	mk(1, "Waiting on CI", "2026-11-20T09:00:00Z", running)
	mk(2, "Ready", "2026-11-20T08:00:00Z", readyToMerge)
	red := mk(3, "Red", "2026-11-20T07:00:00Z", failing)
	mk(4, "Asked to change", "2026-11-20T06:00:00Z", changesAsked)
	mk(5, "Never looked at", "2026-11-20T05:00:00Z", nil)
	old := mk(6, "Looked at long ago", "2026-11-20T04:00:00Z", nil)
	ct.status(ct.alice, old, failing, 30*time.Hour)
	ct.pull(ct.alice, store.Pull{Repo: "acme/shop", Number: 7, Title: "Merged already", Author: "alice", Reason: store.ReasonAuthored, State: "merged"})
	// someone else's, kept with a status: not mine
	other := ct.pull(ct.alice, store.Pull{Repo: "acme/api", Number: 13, Title: "Sara's red one", Author: "sara", Reason: store.ReasonReviewed})
	ct.status(ct.alice, other, failing, time.Minute)

	ct.ask("alice", "GET", "/api/v1/pocket/widget", nil, &got)
	if got.Line != "2 need you, 1 ready to merge" || got.URL != "/mine" {
		t.Errorf("line: %+v", got)
	}
	want := []core.Metric{{Label: "Open", Value: "6"}, {Label: "Need you", Value: "2"}, {Label: "Ready to merge", Value: "1"}}
	if !reflect.DeepEqual(got.Metrics, want) {
		t.Errorf("metrics: %+v", got.Metrics)
	}
	rows := capTitles(got.Items, func(it core.WidgetItem) string { return it.Title + " (" + it.Detail + ")" })
	wantRows := "Red (acme/shop#3 · CI failing) | Asked to change (acme/shop#4 · changes asked) | Ready (acme/shop#2 · ready to merge) | " +
		"Waiting on CI (acme/shop#1 · CI running) | Never looked at (acme/shop#5) | Looked at long ago (acme/shop#6)"
	if rows != wantRows {
		t.Errorf("rows: %s\n want: %s", rows, wantRows)
	}
	if got.Items[0].ID != itoa64(red.ID) || got.Items[0].URL != "/pr/"+itoa64(red.ID) {
		t.Errorf("row address: %+v", got.Items[0])
	}
	// one that needs me: singular
	bobs := ct.pull(ct.bob, store.Pull{Repo: "acme/web", Number: 2, Title: "Bob's", Author: "bob", Reason: store.ReasonAuthored})
	ct.status(ct.bob, bobs, changesAsked, time.Minute)
	if ct.ask("bob", "GET", "/api/v1/pocket/widget", nil, &got); got.Line != "1 needs you" || len(got.Items) != 1 || got.Items[0].Title != "Bob's" {
		t.Errorf("bob's widget: %+v", got)
	}
	// open but nothing to say: the rows, no line
	ct.pull(ct.bob, store.Pull{Repo: "acme/web", Number: 3, Title: "Quiet", Author: "bob", Reason: store.ReasonAuthored})
	ct.stamp(`DELETE FROM cache WHERE key = ?`, "status:"+itoa64(ct.bob.ID)+":"+itoa64(bobs.ID))
	if ct.ask("bob", "GET", "/api/v1/pocket/widget", nil, &got); got.Line != "" || len(got.Items) != 2 {
		t.Errorf("nothing to say: %+v", got)
	}
	// never more than eight rows
	for i := 10; i < 20; i++ {
		mk(i, "More", "2026-11-01T00:00:00Z", nil)
	}
	if ct.ask("alice", "GET", "/api/v1/pocket/widget", nil, &got); len(got.Items) != 8 || got.Items[0].Title != "Red" || got.Metrics[0].Value != "16" {
		t.Errorf("a long list: %d rows %+v", len(got.Items), got.Metrics)
	}
	if n := ct.ghCalls.Load(); n != 0 {
		t.Errorf("the widget asked GitHub %d times", n)
	}
}

func TestPocketLinkTokens(t *testing.T) {
	ct := newCapTester(t)
	p := ct.pull(ct.alice, store.Pull{Repo: "acme/shop", Number: 7, Title: "Coupons", Author: "alice", Reason: store.ReasonAuthored})
	ct.status(ct.alice, p, failing, time.Minute)
	const day = "/api/v1/pocket/day/2026-11-20"
	capture := []byte(`{"request_id":"req-0000000000000001","text":"Coupons"}`)
	for _, c := range []struct {
		tok, method, path string
		body              []byte
		want              int
	}{
		{"pcx_alice_search", "GET", "/api/v1/pocket/search?q=coupons", nil, 200},
		{"pcx_alice_day", "GET", day, nil, 200},
		{"pcx_alice_widget", "GET", "/api/v1/pocket/widget", nil, 200},
		{"pcx_alice_widget", "GET", "/api/v1/auth/me", nil, 200},
		// another capability's route
		{"pcx_alice_search", "GET", day, nil, 403},
		{"pcx_alice_search", "GET", "/api/v1/pocket/widget", nil, 403},
		{"pcx_alice_day", "GET", "/api/v1/pocket/search?q=coupons", nil, 403},
		{"pcx_alice_day", "GET", "/api/v1/pocket/widget", nil, 403},
		{"pcx_alice_widget", "GET", "/api/v1/pocket/search?q=coupons", nil, 403},
		{"pcx_alice_widget", "GET", day, nil, 403},
		{"pcx_alice_widget", "POST", "/api/v1/pocket/widget", nil, 405},
		// nothing can be kept here: there is no such route, and the capture scope opens nothing else
		{"pcx_alice_capture", "POST", "/api/v1/pocket/capture", capture, 404},
		{"pcx_alice_capture", "POST", "/api/v1/pulls", []byte(`{"url":"acme/shop#8"}`), 403},
		// the rest of the app
		{"pcx_alice_search", "GET", "/api/v1/pulls/find?q=coupons", nil, 403},
		{"pcx_alice_search", "GET", "/api/v1/pulls", nil, 403},
		{"pcx_alice_day", "GET", "/api/v1/day/2026-11-20", nil, 403},
		{"pcx_alice_widget", "GET", "/api/v1/mine/board", nil, 403},
		{"pcx_alice_widget", "GET", "/api/v1/dashboard", nil, 403},
		{"pcx_alice_widget", "GET", "/api/v1/pulls/" + itoa64(p.ID), nil, 403},
		{"pcx_alice_widget", "POST", "/api/v1/pulls/" + itoa64(p.ID) + "/review", []byte(`{"event":"APPROVE"}`), 403},
		{"pcx_alice_search", "GET", "/api/v1/settings", nil, 403},
		// the app's own scopes do not open the new routes
		{"pcx_alice_read", "GET", "/api/v1/pocket/search?q=coupons", nil, 403},
		{"pcx_alice_read", "GET", day, nil, 403},
		{"pcx_alice_read", "GET", "/api/v1/pocket/widget", nil, 403},
		{"pcx_alice_write", "GET", "/api/v1/pocket/widget", nil, 403},
		{"pcx_alice_write", "POST", "/api/v1/pocket/capture", capture, 404},
		{"pcx_alice_read", "GET", "/api/v1/dashboard", nil, 200},
		{"pcx_alice_read", "GET", "/api/v1/day/2026-11-20", nil, 200},
		{"pcx_nobody", "GET", "/api/v1/pocket/widget", nil, 401},
	} {
		if code := ct.ask(c.tok, c.method, c.path, c.body, nil); code != c.want {
			t.Errorf("%s %s %s → %d, want %d", c.tok, c.method, c.path, code, c.want)
		}
	}
	// a person signed in finds no capture route either
	if code := ct.ask("alice", "POST", "/api/v1/pocket/capture", capture, nil); code != 404 {
		t.Errorf("capture as alice: %d", code)
	}
	// the token acts for its person only
	var found core.SearchResults
	if ct.ask("pcx_alice_search", "GET", "/api/v1/pocket/search?q=coupons", nil, &found); len(found.Items) != 1 || found.Items[0].Title != "Coupons" {
		t.Errorf("alice through the link: %+v", found)
	}
	if ct.ask("pcx_bob_search", "GET", "/api/v1/pocket/search?q=coupons", nil, &found); len(found.Items) != 0 {
		t.Errorf("bob through the link finds alice's: %+v", found)
	}
	var w core.WidgetData
	if ct.ask("pcx_alice_widget", "GET", "/api/v1/pocket/widget", nil, &w); w.Line != "1 needs you" {
		t.Errorf("alice's widget through the link: %+v", w)
	}
	if ct.ask("pcx_bob_widget", "GET", "/api/v1/pocket/widget", nil, &w); len(w.Items) != 0 {
		t.Errorf("bob's widget through the link: %+v", w)
	}
	if n := ct.ghCalls.Load(); n != 0 {
		t.Errorf("asked GitHub %d times", n)
	}
}

func TestPocketLinkScopes(t *testing.T) {
	req := func(method, path string) *http.Request { return httptest.NewRequest(method, path, nil) }
	for _, c := range []struct {
		scope, method, path string
		want                bool
	}{
		{core.ScopeSearch, "GET", "/api/v1/pocket/search?q=coupons", true},
		{core.ScopeDay, "GET", "/api/v1/pocket/day/2026-11-20", true},
		{core.ScopeWidget, "GET", "/api/v1/pocket/widget", true},
		{core.ScopeWidget, "GET", "/api/v1/auth/me", true},
		{core.ScopeDay, "GET", "/api/v1/pocket/day/2026-11-20/more", false},
		{core.ScopeDay, "GET", "/api/v1/day/2026-11-20", false},
		{core.ScopeSearch, "POST", "/api/v1/pocket/search", false},
		{core.ScopeCapture, "POST", "/api/v1/pocket/capture", false},
		{"prs:read", "GET", "/api/v1/pocket/widget", false},
		{"prs:write", "POST", "/api/v1/pocket/capture", false},
		{"prs:read", "GET", "/api/v1/dashboard", true},
	} {
		if got := linkAllows([]string{c.scope}, req(c.method, c.path)); got != c.want {
			t.Errorf("%s %s %s = %v, want %v", c.scope, c.method, c.path, got, c.want)
		}
	}
}

func TestManifestCapabilities(t *testing.T) {
	ct := newCapTester(t)
	if err := ct.s.registerWithCore(context.Background()); err != nil {
		t.Fatal(err)
	}
	ct.core.mu.Lock()
	m := ct.core.manifest
	ct.core.mu.Unlock()
	if m["slug"] != "pr-review" || m["category"] != "Work" {
		t.Fatalf("manifest: %v %v", m["slug"], m["category"])
	}
	search, _ := m["search"].(map[string]any)
	day, _ := m["day"].(map[string]any)
	widget, _ := m["widget"].(map[string]any)
	if search == nil || day == nil || day["label"] != "Pull requests" || day["one"] != "pull request" || day["many"] != "pull requests" ||
		widget == nil || widget["title"] != "Your PRs" || m["capture"] != nil {
		t.Fatalf("capabilities: %v %v %v %v", search, day, widget, m["capture"])
	}
	if b, _ := json.Marshal(search["bangs"]); string(b) != `["pr","prs"]` {
		t.Errorf("bangs: %s", b)
	}
	if b, _ := json.Marshal(search["kinds"]); string(b) != `["pull request","walkthrough"]` {
		t.Errorf("kinds: %s", b)
	}
	offered := map[string]bool{}
	for _, o := range m["offers"].([]any) {
		offered[o.(map[string]any)["scope"].(string)] = true
	}
	for _, sc := range []string{"prs:read", "prs:write", core.ScopeSearch, core.ScopeDay, core.ScopeWidget, core.ScopeMoveExport, core.ScopeMoveImport} {
		if !offered[sc] {
			t.Errorf("offer %s missing: %v", sc, offered)
		}
	}
	if offered[core.ScopeCapture] {
		t.Errorf("capture offered: %v", offered)
	}
}
