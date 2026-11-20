package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/config"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// newMoveTester is newTester behind a Pocket Core that vouches for Pocket
// Move's link tokens: pcx_<alice|bob>_<in|out|read>.
func newMoveTester(t *testing.T) *tester {
	t.Helper()
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/auth/introspect" {
			io.WriteString(w, `{}`)
			return
		}
		var in struct{ Token string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		parts := strings.Split(in.Token, "_")
		if len(parts) != 3 || parts[0] != "pcx" {
			io.WriteString(w, `{"active":false}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "kind": "delegated", "audience": "pr-review",
			"scope": map[string]string{"in": core.ScopeMoveImport, "out": core.ScopeMoveExport, "read": "prs:read"}[parts[2]],
			"user":  map[string]any{"id": 1, "sub": parts[1] + "@example.com", "email": parts[1] + "@example.com", "name": parts[1], "role": "member"},
			"app":   map[string]any{"id": 9, "slug": "move", "name": "Pocket Move"}})
	}))
	t.Cleanup(coreSrv.Close)
	tt, _ := newTester(t, func(c *config.Config) { c.CoreURL = coreSrv.URL })
	return tt
}

func (tt *tester) moveRaw(tok, method, path, ctype string, body []byte) (int, []byte) {
	tt.t.Helper()
	req, _ := http.NewRequest(method, tt.ts.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		tt.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func TestMoveLinkScopes(t *testing.T) {
	tt := newMoveTester(t)
	out, in, read := "pcx_bob_out", "pcx_bob_in", "pcx_bob_read"
	for _, c := range []struct {
		tok, method, path string
		want              int
	}{
		{out, "GET", core.PortablePath, 200},
		{out, "GET", "/api/v1/export.json", 200},
		{out, "GET", "/api/v1/export.csv", 200},
		{out, "POST", "/api/v1/import", 403},
		{out, "GET", "/api/v1/pulls", 403},
		{out, "GET", "/api/v1/settings", 403},
		{out, "GET", "/api/v1/backup", 403},
		{in, "GET", core.PortablePath, 200},
		{in, "GET", "/api/v1/export.json", 403},
		{in, "POST", "/api/v1/pulls", 403},
		{in, "PUT", "/api/v1/settings", 403},
		{read, "GET", core.PortablePath, 403},
		{read, "GET", "/api/v1/export.json", 403},
	} {
		if code, body := tt.moveRaw(c.tok, c.method, c.path, "application/json", []byte("{}")); code != c.want {
			t.Errorf("%s %s %s → %d, want %d (%s)", c.tok, c.method, c.path, code, c.want, body)
		}
	}
	_, body := tt.moveRaw(out, "GET", core.PortablePath, "", nil)
	var p core.Portable
	if err := json.Unmarshal(body, &p); err != nil || len(p.Exports) != 2 || len(p.Imports) != 1 || p.Exports[0].Kind != core.KindPocket {
		t.Fatalf("portable: %s", body)
	}
}

// Move brings Alice's pull requests to Bob: the import adds once (a review
// never sent to GitHub too), and nothing of one person shows up for the other.
func TestMoveImportAddsOnce(t *testing.T) {
	tt := newMoveTester(t)
	ctx := context.Background()
	alice, _ := tt.s.store.GetUserByEmail(ctx, "alice@example.com")
	p := &store.Pull{UserID: alice.ID, Repo: "acme/shop", Number: 7, Title: "Faster checkout", State: "open"}
	if err := tt.s.store.SavePull(ctx, p); err != nil {
		t.Fatal(err)
	}
	_ = tt.s.store.CreateComment(ctx, &store.Comment{UserID: alice.ID, PullID: p.ID, Path: "cart.go", Line: 3, Body: "why twice?", State: "draft"})
	_ = tt.s.store.SaveReview(ctx, &store.Review{UserID: alice.ID, PullID: p.ID, Event: "COMMENT", Body: "never sent", Day: "2026-11-19"}, nil)
	_ = tt.s.store.SaveReview(ctx, &store.Review{UserID: alice.ID, PullID: p.ID, Event: "APPROVE", Body: "ok", GHID: 99, Day: "2026-11-19"}, nil)

	code, dump := tt.moveRaw("pcx_alice_out", "GET", "/api/v1/export.json", "", nil)
	if code != 200 || !strings.Contains(string(dump), "Faster checkout") {
		t.Fatalf("alice's export: %d %s", code, dump)
	}
	if _, b := tt.moveRaw("pcx_bob_out", "GET", "/api/v1/export.json", "", nil); strings.Contains(string(b), "acme/shop") {
		t.Fatalf("bob's export holds alice's pulls: %s", b)
	}
	if _, c := tt.moveRaw("pcx_alice_out", "GET", "/api/v1/export.csv", "", nil); !strings.Contains(string(c), "acme/shop") {
		t.Fatalf("alice's csv: %s", c)
	}
	for run, want := range []float64{1, 0} {
		code, body := tt.moveRaw("pcx_bob_in", "POST", "/api/v1/import", "application/json", dump)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		n := func(k string) float64 { v, _ := m[k].(float64); return v }
		if code != 200 || n("pulls") != want || n("comments") != want || n("reviews") != 2*want {
			t.Fatalf("import run %d: %d %s", run+1, code, body)
		}
	}
	bob, _ := tt.s.store.GetUserByEmail(ctx, "bob@example.com")
	if rs, _ := tt.s.store.Reviews(ctx, bob.ID, 0, ""); len(rs) != 2 {
		t.Fatalf("bob has %d reviews", len(rs))
	}
	if rs, _ := tt.s.store.Reviews(ctx, alice.ID, 0, ""); len(rs) != 2 {
		t.Fatalf("alice has %d reviews", len(rs))
	}
}
