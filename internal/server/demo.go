package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/pocket"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/walk"
)

// The demo (DEMO_DATA=true, for screenshots and trying the app): a few pull
// requests of a made-up shop, one of them a full feature — a migration, the
// repository, a service, routes, the chatbot's config and an admin page —
// with commits, an agent's edits and a walkthrough made by a stand-in AI.

type demoPR struct {
	repo    string
	number  int
	title   string
	body    string
	author  string
	reason  string
	head    string
	base    string
	files   []walk.File
	commits []demoCommit
	edits   []demoEdit
	sums    map[string]string
	parts   map[string][]demoPart
	plan    string // the stand-in AI's answer for the order
	ago     time.Duration
	review  string // my last review
	stale   bool   // new commits since the walkthrough
}

type demoCommit struct {
	title, body, author string
	paths               []string
	ago                 time.Duration
}

type demoEdit struct {
	path, note string
	ago        time.Duration
}

type demoPart struct {
	hunks []int
	text  string
}

// newFile is a patch adding a whole file.
func newFile(lines ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, l := range lines {
		b.WriteString("+" + l + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// hunk is one change block: context lines start with " ", added "+", removed "-".
func hunk(oldStart, newStart int, head string, lines ...string) string {
	o, n := 0, 0
	for _, l := range lines {
		switch l[0] {
		case '+':
			n++
		case '-':
			o++
		default:
			o++
			n++
		}
	}
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@ %s\n%s", oldStart, o, newStart, n, head, strings.Join(lines, "\n"))
}

func patchOf(hunks ...string) string { return strings.Join(hunks, "\n") }

func counts(patch string) (int, int) {
	a, d := 0, 0
	for _, l := range strings.Split(patch, "\n") {
		if strings.HasPrefix(l, "@@") {
			continue
		}
		if strings.HasPrefix(l, "+") {
			a++
		} else if strings.HasPrefix(l, "-") {
			d++
		}
	}
	return a, d
}

func demoFile(path, status, patch string) walk.File {
	a, d := counts(patch)
	return walk.File{Path: path, Status: status, Patch: patch, Additions: a, Deletions: d}
}

func demoPRs() []*demoPR {
	coupons := &demoPR{repo: "acme/shop", number: 142, title: "Coupons: create, redeem and manage discount codes",
		body:   "Shoppers can enter a coupon code at checkout. Admins create and pause coupons on a new dashboard page, and the support chatbot can now answer questions about them.\n\nCloses #131.",
		author: "sara-k", reason: store.ReasonRequested, head: "9f3c2a71e4b8", base: "1b7e0d4c22aa", ago: 40 * time.Minute}
	coupons.files = []walk.File{
		demoFile("web/src/pages/admin/Coupons.tsx", "added", newFile(
			`import { useEffect, useState } from 'react'`,
			`import { api } from '../../lib/api'`,
			`import type { Coupon } from '../../lib/types'`,
			``,
			`export default function Coupons() {`,
			`  const [list, setList] = useState<Coupon[]>([])`,
			`  const [code, setCode] = useState('')`,
			`  const [percent, setPercent] = useState(10)`,
			`  useEffect(() => { api.get<Coupon[]>('/admin/coupons').then(setList) }, [])`,
			``,
			`  const create = async () => {`,
			`    const c = await api.post<Coupon>('/admin/coupons', { code, percent_off: percent })`,
			`    setList([c, ...list]); setCode('')`,
			`  }`,
			`  const pause = async (c: Coupon) => {`,
			`    const u = await api.patch<Coupon>('/admin/coupons/' + c.id, { active: !c.active })`,
			`    setList(list.map(x => x.id === u.id ? u : x))`,
			`  }`,
			``,
			`  return (`,
			`    <section className="page">`,
			`      <h1>Coupons</h1>`,
			`      <div className="row">`,
			`        <input value={code} onChange={e => setCode(e.target.value.toUpperCase())} placeholder="SPRING24" />`,
			`        <input type="number" min={1} max={90} value={percent} onChange={e => setPercent(+e.target.value)} />`,
			`        <button onClick={create} disabled={!code}>Create</button>`,
			`      </div>`,
			`      <table>`,
			`        <thead><tr><th>Code</th><th>Off</th><th>Used</th><th>Expires</th><th /></tr></thead>`,
			`        <tbody>{list.map(c => (`,
			`          <tr key={c.id}><td>{c.code}</td><td>{c.percent_off}%</td><td>{c.redeemed}/{c.max_redemptions || '∞'}</td>`,
			`            <td>{c.expires_at?.slice(0, 10) || '—'}</td>`,
			`            <td><button onClick={() => pause(c)}>{c.active ? 'Pause' : 'Resume'}</button></td></tr>`,
			`        ))}</tbody>`,
			`      </table>`,
			`    </section>`,
			`  )`,
			`}`)),
		demoFile("chatbot/tools.yaml", "modified", patchOf(hunk(14, 14, "tables:",
			`   - name: orders`,
			`     description: Orders with their lines, totals and status`,
			`     columns: [id, customer_id, status, total_cents, created_at]`,
			`+  - name: coupons`,
			`+    description: Discount codes — what they take off, how often they were used, when they expire`,
			`+    columns: [code, percent_off, max_redemptions, redeemed, active, expires_at]`,
			`+    hide: [created_by]`,
			`   - name: customers`))),
		demoFile("db/migrations/0042_coupons.sql", "added", newFile(
			`-- Discount codes shoppers enter at checkout.`,
			`CREATE TABLE coupons (`,
			`  id              BIGSERIAL PRIMARY KEY,`,
			`  code            TEXT NOT NULL,`,
			`  percent_off     INTEGER NOT NULL CHECK (percent_off BETWEEN 1 AND 90),`,
			`  max_redemptions INTEGER,`,
			`  redeemed        INTEGER NOT NULL DEFAULT 0,`,
			`  active          BOOLEAN NOT NULL DEFAULT TRUE,`,
			`  expires_at      TIMESTAMPTZ,`,
			`  created_by      BIGINT REFERENCES users(id),`,
			`  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()`,
			`);`,
			`CREATE UNIQUE INDEX coupons_code ON coupons (lower(code));`,
			``,
			`ALTER TABLE orders ADD COLUMN coupon_id BIGINT REFERENCES coupons(id);`)),
		demoFile("internal/api/coupons.go", "added", newFile(
			`package api`,
			``,
			`import (`,
			`	"net/http"`,
			``,
			`	"acme/shop/internal/coupons"`,
			`)`,
			``,
			`// POST /checkout/coupon {code} → the discount for the current cart.`,
			`func (h *Handler) redeemCoupon(w http.ResponseWriter, r *http.Request) {`,
			`	var in struct{ Code string `+"`json:\"code\"`"+` }`,
			`	if err := decode(r, &in); err != nil {`,
			`		writeErr(w, http.StatusBadRequest, "invalid body")`,
			`		return`,
			`	}`,
			`	cart := cartFrom(r.Context())`,
			`	d, err := h.coupons.Apply(r.Context(), in.Code, cart)`,
			`	switch {`,
			`	case err == coupons.ErrUnknown, err == coupons.ErrExpired, err == coupons.ErrUsedUp:`,
			`		writeErr(w, http.StatusUnprocessableEntity, err.Error())`,
			`		return`,
			`	case err != nil:`,
			`		h.fail(w, err)`,
			`		return`,
			`	}`,
			`	writeJSON(w, http.StatusOK, d)`,
			`}`,
			``,
			`// Admin: list, create, pause.`,
			`func (h *Handler) listCoupons(w http.ResponseWriter, r *http.Request) {`,
			`	list, err := h.coupons.List(r.Context())`,
			`	if err != nil {`,
			`		h.fail(w, err)`,
			`		return`,
			`	}`,
			`	writeJSON(w, http.StatusOK, list)`,
			`}`,
			``,
			`func (h *Handler) createCoupon(w http.ResponseWriter, r *http.Request) {`,
			`	var in coupons.New`,
			`	if err := decode(r, &in); err != nil {`,
			`		writeErr(w, http.StatusBadRequest, "invalid body")`,
			`		return`,
			`	}`,
			`	in.CreatedBy = userFrom(r.Context()).ID`,
			`	c, err := h.coupons.Create(r.Context(), in)`,
			`	if err != nil {`,
			`		h.fail(w, err)`,
			`		return`,
			`	}`,
			`	writeJSON(w, http.StatusCreated, c)`,
			`}`)),
		demoFile("internal/api/router.go", "modified", patchOf(
			hunk(41, 41, "func (h *Handler) Routes() http.Handler {",
				`	r.Post("/checkout", h.checkout)`,
				`+	r.Post("/checkout/coupon", h.redeemCoupon)`,
				` `,
				`	r.Group(func(r chi.Router) {`,
				`		r.Use(h.requireAdmin)`,
				`		r.Get("/admin/orders", h.listOrders)`,
				`+		r.Get("/admin/coupons", h.listCoupons)`,
				`+		r.Post("/admin/coupons", h.createCoupon)`,
				`+		r.Patch("/admin/coupons/{id}", h.updateCoupon)`,
				`	})`),
			hunk(88, 91, "func (h *Handler) logRequest(next http.Handler) http.Handler {",
				`		start := time.Now()`,
				`		next.ServeHTTP(ww, r)`,
				`-		log.Printf("%s %s %d", r.Method, r.URL.Path, ww.Status())`,
				`+		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "ms", time.Since(start).Milliseconds())`,
				`	})`))),
		demoFile("internal/coupons/repository.go", "added", newFile(
			`package coupons`,
			``,
			`import (`,
			`	"context"`,
			`	"database/sql"`,
			`)`,
			``,
			`// Repository reads and writes the coupons table.`,
			`type Repository struct{ db *sql.DB }`,
			``,
			`func (r *Repository) ByCode(ctx context.Context, code string) (*Coupon, error) {`,
			`	row := r.db.QueryRowContext(ctx, `+"`SELECT id, code, percent_off, max_redemptions, redeemed, active, expires_at FROM coupons WHERE lower(code) = lower($1)`"+`, code)`,
			`	return scan(row)`,
			`}`,
			``,
			`// Redeem counts one use, unless the coupon is used up (in one statement,`,
			`// so two checkouts at once cannot both take the last use).`,
			`func (r *Repository) Redeem(ctx context.Context, id int64) (bool, error) {`,
			`	res, err := r.db.ExecContext(ctx, `+"`UPDATE coupons SET redeemed = redeemed + 1 WHERE id = $1 AND active AND (max_redemptions IS NULL OR redeemed < max_redemptions)`"+`, id)`,
			`	if err != nil {`,
			`		return false, err`,
			`	}`,
			`	n, _ := res.RowsAffected()`,
			`	return n == 1, nil`,
			`}`,
			``,
			`func (r *Repository) Insert(ctx context.Context, n New) (*Coupon, error) {`,
			`	row := r.db.QueryRowContext(ctx, `+"`INSERT INTO coupons (code, percent_off, max_redemptions, expires_at, created_by) VALUES ($1, $2, $3, $4, $5) RETURNING id, code, percent_off, max_redemptions, redeemed, active, expires_at`"+`,`,
			`		n.Code, n.PercentOff, n.MaxRedemptions, n.ExpiresAt, n.CreatedBy)`,
			`	return scan(row)`,
			`}`,
			``,
			`func (r *Repository) List(ctx context.Context) ([]Coupon, error) {`,
			`	rows, err := r.db.QueryContext(ctx, `+"`SELECT id, code, percent_off, max_redemptions, redeemed, active, expires_at FROM coupons ORDER BY created_at DESC`"+`)`,
			`	if err != nil {`,
			`		return nil, err`,
			`	}`,
			`	defer rows.Close()`,
			`	return scanAll(rows)`,
			`}`)),
		demoFile("internal/coupons/service.go", "added", newFile(
			`package coupons`,
			``,
			`import (`,
			`	"context"`,
			`	"errors"`,
			`	"time"`,
			``,
			`	"acme/shop/internal/cart"`,
			`)`,
			``,
			`var (`,
			`	ErrUnknown = errors.New("no such coupon")`,
			`	ErrExpired = errors.New("this coupon has expired")`,
			`	ErrUsedUp  = errors.New("this coupon has been used up")`,
			`)`,
			``,
			`// Service decides what a coupon takes off a cart.`,
			`type Service struct {`,
			`	repo *Repository`,
			`	now  func() time.Time`,
			`}`,
			``,
			`// Apply checks the code and returns the discount; it counts the use.`,
			`func (s *Service) Apply(ctx context.Context, code string, c *cart.Cart) (*Discount, error) {`,
			`	cp, err := s.repo.ByCode(ctx, code)`,
			`	if err != nil {`,
			`		return nil, ErrUnknown`,
			`	}`,
			`	if cp.ExpiresAt != nil && s.now().After(*cp.ExpiresAt) {`,
			`		return nil, ErrExpired`,
			`	}`,
			`	ok, err := s.repo.Redeem(ctx, cp.ID)`,
			`	if err != nil {`,
			`		return nil, err`,
			`	}`,
			`	if !ok {`,
			`		return nil, ErrUsedUp`,
			`	}`,
			`	off := c.SubtotalCents() * int64(cp.PercentOff) / 100`,
			`	return &Discount{CouponID: cp.ID, Code: cp.Code, AmountCents: off}, nil`,
			`}`)),
		demoFile("internal/coupons/service_test.go", "added", newFile(
			`package coupons`,
			``,
			`func TestApplyTakesPercentOff(t *testing.T) {`,
			`	s := newTestService(t, Coupon{Code: "SPRING", PercentOff: 20})`,
			`	d, err := s.Apply(ctx, "spring", cartOf(5000))`,
			`	if err != nil || d.AmountCents != 1000 {`,
			`		t.Fatalf("got %v, %v; want 1000", d, err)`,
			`	}`,
			`}`,
			``,
			`func TestApplyRefusesExpired(t *testing.T) {`,
			`	s := newTestService(t, Coupon{Code: "OLD", PercentOff: 10, ExpiresAt: ptr(yesterday)})`,
			`	if _, err := s.Apply(ctx, "OLD", cartOf(5000)); err != ErrExpired {`,
			`		t.Fatalf("got %v, want ErrExpired", err)`,
			`	}`,
			`}`)),
		demoFile("internal/coupons/model.go", "added", newFile(
			`package coupons`,
			``,
			`import "time"`,
			``,
			`type Coupon struct {`,
			`	ID             int64      `+"`json:\"id\"`",
			`	Code           string     `+"`json:\"code\"`",
			`	PercentOff     int        `+"`json:\"percent_off\"`",
			`	MaxRedemptions *int       `+"`json:\"max_redemptions\"`",
			`	Redeemed       int        `+"`json:\"redeemed\"`",
			`	Active         bool       `+"`json:\"active\"`",
			`	ExpiresAt      *time.Time `+"`json:\"expires_at\"`",
			`}`,
			``,
			`type New struct {`,
			`	Code           string     `+"`json:\"code\"`",
			`	PercentOff     int        `+"`json:\"percent_off\"`",
			`	MaxRedemptions *int       `+"`json:\"max_redemptions\"`",
			`	ExpiresAt      *time.Time `+"`json:\"expires_at\"`",
			`	CreatedBy      int64      `+"`json:\"-\"`",
			`}`,
			``,
			`type Discount struct {`,
			`	CouponID    int64  `+"`json:\"coupon_id\"`",
			`	Code        string `+"`json:\"code\"`",
			`	AmountCents int64  `+"`json:\"amount_cents\"`",
			`}`)),
		demoFile("web/src/components/AdminNav.tsx", "modified", patchOf(hunk(9, 9, "const links = [",
			`   { to: '/admin/orders', label: 'Orders', icon: 'cart' },`,
			`   { to: '/admin/customers', label: 'Customers', icon: 'people' },`,
			`+  { to: '/admin/coupons', label: 'Coupons', icon: 'tag' },`,
			`   { to: '/admin/reports', label: 'Reports', icon: 'chart' },`))),
		demoFile("docs/coupons.md", "added", newFile(
			`# Coupons`,
			``,
			`A coupon takes a percentage off the cart's subtotal (1–90 %).`,
			`Admins create them under Admin → Coupons; a coupon can expire and can be limited to a number of uses.`,
			`Codes are not case-sensitive.`)),
		demoFile("web/package-lock.json", "modified", patchOf(hunk(2210, 2210, `"node_modules/date-fns": {`,
			`     "node_modules/date-fns": {`,
			`-      "version": "3.6.0",`,
			`+      "version": "4.1.0",`,
			`-      "resolved": "https://registry.npmjs.org/date-fns/-/date-fns-3.6.0.tgz",`,
			`+      "resolved": "https://registry.npmjs.org/date-fns/-/date-fns-4.1.0.tgz",`))),
	}
	coupons.commits = []demoCommit{
		{"Add the coupons table", "", "sara-k", []string{"db/migrations/0042_coupons.sql", "internal/coupons/model.go"}, 26 * time.Hour},
		{"Coupon repository and service", "Redeem counts a use in one UPDATE so two checkouts cannot both take the last use.", "sara-k", []string{"internal/coupons/repository.go", "internal/coupons/service.go", "internal/coupons/service_test.go"}, 22 * time.Hour},
		{"Checkout and admin routes, admin page", "", "sara-k", []string{"internal/api/coupons.go", "internal/api/router.go", "web/src/pages/admin/Coupons.tsx", "web/src/components/AdminNav.tsx", "web/package-lock.json"}, 5 * time.Hour},
		{"Let the chatbot read coupons; docs", "", "sara-k", []string{"chatbot/tools.yaml", "docs/coupons.md"}, 3 * time.Hour},
	}
	coupons.edits = []demoEdit{
		{"db/migrations/0042_coupons.sql", "I'll start with the table: the code is unique regardless of case, percent_off is limited to 1–90.", 26*time.Hour + 20*time.Minute},
		{"internal/coupons/model.go", "I'll start with the table: the code is unique regardless of case, percent_off is limited to 1–90.", 26*time.Hour + 18*time.Minute},
		{"internal/coupons/repository.go", "Now the repository. Redeem has to be atomic, so it is one conditional UPDATE.", 22*time.Hour + 40*time.Minute},
		{"internal/coupons/service.go", "The service checks expiry and counts the use.", 22*time.Hour + 30*time.Minute},
		{"internal/coupons/service_test.go", "Tests for the percentage and an expired coupon.", 22*time.Hour + 25*time.Minute},
		{"internal/api/coupons.go", "Routes: redeem at checkout, list/create/pause for admins.", 5*time.Hour + 30*time.Minute},
		{"internal/api/router.go", "Routes: redeem at checkout, list/create/pause for admins.", 5*time.Hour + 28*time.Minute},
		{"web/src/pages/admin/Coupons.tsx", "The admin page with a small create form and a table.", 5*time.Hour + 10*time.Minute},
		{"web/src/components/AdminNav.tsx", "The admin page with a small create form and a table.", 5*time.Hour + 9*time.Minute},
		{"chatbot/tools.yaml", "The chatbot needs to see the new table to answer coupon questions.", 3*time.Hour + 5*time.Minute},
	}
	coupons.sums = map[string]string{
		"db/migrations/0042_coupons.sql":   "Creates the coupons table (code unique regardless of case, 1–90 % off, optional use limit and expiry) and links orders to the coupon used.",
		"internal/coupons/model.go":        "Defines Coupon, the New input for creating one, and the Discount a checkout gets back.",
		"internal/coupons/repository.go":   "Reads coupons by code, lists and inserts them; Redeem counts one use in a single conditional UPDATE so the last use cannot be taken twice.",
		"internal/coupons/service.go":      "Apply looks the code up, refuses expired or used-up coupons, counts the use and returns the amount off the subtotal.",
		"internal/coupons/service_test.go": "Tests that 20 % comes off a 50.00 cart and that an expired coupon is refused.",
		"internal/api/coupons.go":          "Adds POST /checkout/coupon for shoppers and list/create handlers for admins; unknown, expired or used-up codes answer 422.",
		"chatbot/tools.yaml":               "Lets the support chatbot read the coupons table (code, discount, uses, expiry), without who created it.",
		"web/src/pages/admin/Coupons.tsx":  "New admin page: create a coupon with a code and a percentage, see how often each was used, pause or resume it.",
		"web/src/components/AdminNav.tsx":  "Adds Coupons to the admin menu.",
		"docs/coupons.md":                  "Explains coupons for admins: percentage off, expiry, use limit, codes not case-sensitive.",
	}
	coupons.parts = map[string][]demoPart{
		"internal/api/router.go": {
			{[]int{0}, "Registers the coupon routes: redeem at checkout, and list, create and update for admins."},
			{[]int{1}, "Unrelated: request logging moves from log.Printf to structured slog with the duration."},
		},
	}
	coupons.plan = `{"tldr":"Shoppers can use a coupon code at checkout; admins manage the codes.",
"flow":[{"title":"Shopper or admin","items":["Checkout form","Admin Coupons page"],"steps":[4,6]},{"title":"Routes","items":["POST /checkout/coupon","/admin/coupons"],"steps":[4]},{"title":"Rules","items":["Expiry and use limit","Percent off"],"steps":[3]},{"title":"Storage","items":["coupons table","orders.coupon_id"],"steps":[1,2]},{"title":"Also reads it","items":["Support chatbot"],"steps":[5]}],
"terms":[{"term":"Redeem","means":"Count one use of a code, in a single conditional UPDATE."}],
"overview":"Shoppers can now enter a coupon code at checkout and get a percentage off their cart. A new coupons table holds the codes; a repository and a service check expiry and count each use atomically; new routes expose redeeming at checkout and managing coupons for admins. The support chatbot can read the table, and admins get a Coupons page.",
"before":"No discounts at all: checkout charged the full subtotal and support had to answer coupon questions by hand.",
"after":"A valid code takes 1–90 % off the subtotal once per use; admins create, limit, expire and pause codes; the chatbot answers questions about them.",
"steps":[
{"title":"The coupons table","why":"Everything else stands on this: check the constraints, the case-insensitive unique index and the new orders.coupon_id column on existing data.","units":["u1","u2"],"risks":["orders.coupon_id has no index; reports joining orders to coupons will scan."]},
{"title":"Reading and redeeming coupons","why":"The repository is the only code that writes coupons; Redeem must stay a single conditional UPDATE.","units":["u3"],"risks":[]},
{"title":"What a coupon takes off","why":"The rules: unknown, expired and used-up codes, and the amount off.","units":["u4","u11"],"risks":["Apply counts the use before the order is paid — an abandoned checkout still uses up a coupon."]},
{"title":"Routes for checkout and admins","why":"How shoppers and admins reach it. The router also carries an unrelated logging change.","units":["u5","u6","u7"],"risks":["updateCoupon is registered in the router but not in this diff — is it in another branch?"]},
{"title":"The chatbot can see coupons","why":"The chatbot's table list gains coupons, without who created them.","units":["u8"],"risks":[]},
{"title":"The admin page","why":"What admins see and click.","units":["u10","u9"],"risks":[]},
{"title":"Docs","why":"What admins read.","units":["u12"],"risks":[]}],
"risks":[{"level":"high","title":"Uses counted before payment","text":"A coupon's use is counted when it is applied, not when the order is paid: abandoned carts use up limited coupons.","units":["u4"]},{"level":"medium","title":"Missing updateCoupon handler","text":"The PATCH route for pausing points at h.updateCoupon, which this pull request does not add — the build may fail.","units":["u6"]},{"level":"low","title":"Unmentioned date-fns upgrade","text":"The date-fns major upgrade in package-lock.json is not mentioned in the description.","units":[]}],
"questions":["Should a use be counted at payment instead of when the code is applied?","Where is updateCoupon defined?","Is the date-fns 4 upgrade intended in this PR?"]}`

	rounding := &demoPR{repo: "acme/shop", number: 139, title: "Fix rounding of invoice totals with several tax rates",
		body:   "Totals were rounded per line and then summed, so invoices with mixed VAT rates could be one cent off. Now each rate is summed first and rounded once.",
		author: "marko-p", reason: store.ReasonRequested, head: "c41d9e0a7b23", base: "1b7e0d4c22aa", ago: 3 * time.Hour}
	rounding.files = []walk.File{
		demoFile("internal/invoices/totals.go", "modified", patchOf(hunk(30, 30, "func Totals(lines []Line) Total {",
			`	var t Total`,
			`-	for _, l := range lines {`,
			`-		t.Tax += round(l.Net * l.Rate)`,
			`-	}`,
			`+	byRate := map[int]int64{}`,
			`+	for _, l := range lines {`,
			`+		byRate[l.RateBP] += l.NetCents`,
			`+	}`,
			`+	for bp, net := range byRate {`,
			`+		t.Tax += roundHalfEven(net*int64(bp), 10000)`,
			`+	}`,
			`	return t`))),
		demoFile("internal/invoices/totals_test.go", "modified", patchOf(hunk(52, 52, "func TestTotals(t *testing.T) {",
			`	}`,
			`+	t.Run("mixed rates round once per rate", func(t *testing.T) {`,
			`+		got := Totals([]Line{{NetCents: 333, RateBP: 1700}, {NetCents: 333, RateBP: 1700}, {NetCents: 1000, RateBP: 500}})`,
			`+		if got.Tax != 163 {`,
			`+			t.Fatalf("tax %d, want 163", got.Tax)`,
			`+		}`,
			`+	})`,
			`}`))),
		demoFile("internal/invoices/round.go", "added", newFile(
			`package invoices`,
			``,
			`// roundHalfEven divides and rounds to the nearest, ties to even (banker's rounding).`,
			`func roundHalfEven(n, d int64) int64 {`,
			`	q, r := n/d, n%d`,
			`	switch {`,
			`	case 2*r > d, 2*r == d && q%2 == 1:`,
			`		q++`,
			`	}`,
			`	return q`,
			`}`)),
	}
	rounding.commits = []demoCommit{{"Round tax once per rate", "", "marko-p", []string{"internal/invoices/totals.go", "internal/invoices/round.go", "internal/invoices/totals_test.go"}, 3 * time.Hour}}
	rounding.sums = map[string]string{
		"internal/invoices/round.go":       "Adds roundHalfEven, integer division with banker's rounding.",
		"internal/invoices/totals.go":      "Sums the net amount per tax rate first and rounds the tax once per rate instead of once per line.",
		"internal/invoices/totals_test.go": "Tests an invoice with two 17 % lines and one 5 % line, where per-line rounding was a cent off.",
	}
	rounding.plan = `{"overview":"Invoices with several tax rates could be a cent off because tax was rounded on every line. Now the net amounts are summed per rate and the tax is rounded once per rate, with banker's rounding.","before":"Tax rounded per line, then summed.","after":"Tax summed per rate, rounded once per rate, ties to even.","steps":[{"title":"Banker's rounding","why":"The helper the new total uses — check negative amounts.","units":["u1"],"risks":["roundHalfEven is wrong for negative n (credit notes): Go's % keeps the sign."]},{"title":"Totals per rate","why":"The fix itself.","units":["u2"],"risks":[]},{"title":"The test","why":"The case that was a cent off.","units":["u3"],"risks":[]}],"risks":[{"level":"medium","text":"Credit notes have negative amounts; roundHalfEven does not handle them.","units":["u1"]}],"questions":["Do credit notes go through Totals?"]}`

	dark := &demoPR{repo: "acme/mobile", number: 58, title: "Dark mode for the settings screen", body: "Follows the system setting; colours come from the theme tokens.",
		author: "maya", reason: store.ReasonAuthored, head: "77ab01c9d3e2", base: "0e9a3b1c5f70", ago: 26 * time.Hour}
	dark.files = []walk.File{
		demoFile("app/theme/tokens.ts", "modified", patchOf(hunk(3, 3, "export const light = {", ` export const light = {`, `   bg: '#ffffff',`, `+  card: '#f5f6f8',`, `   text: '#111111',`))),
		demoFile("app/screens/Settings.tsx", "modified", patchOf(hunk(18, 18, "export function Settings() {", `-  const bg = '#fff'`, `+  const { bg, card } = useTheme()`, `   return (`))),
	}
	dark.commits = []demoCommit{{"Settings follow the theme", "", "maya", []string{"app/theme/tokens.ts", "app/screens/Settings.tsx"}, 26 * time.Hour}}
	dark.sums = map[string]string{"app/theme/tokens.ts": "Adds a card colour to the light and dark themes.", "app/screens/Settings.tsx": "The settings screen takes its colours from the theme instead of a fixed white."}

	pg := &demoPR{repo: "acme/infra", number: 12, title: "Upgrade Postgres to 17", body: "Uses pg_upgrade --link; the maintenance window is Sunday 06:00.",
		author: "dino", reason: store.ReasonReviewed, head: "5d2e8f1a9c04", base: "a3c9d7e2f101", ago: 2 * time.Hour, review: "APPROVED", stale: true}
	pg.files = []walk.File{
		demoFile("docker/postgres/Dockerfile", "modified", patchOf(hunk(1, 1, "", `-FROM postgres:16.4`, `+FROM postgres:17.0`))),
		demoFile("scripts/upgrade-postgres.sh", "added", newFile(`#!/bin/sh`, `set -eu`, `pg_upgrade --link -b /usr/lib/postgresql/16/bin -B /usr/lib/postgresql/17/bin -d /data/16 -D /data/17`)),
	}
	pg.commits = []demoCommit{{"Postgres 17", "", "dino", []string{"docker/postgres/Dockerfile", "scripts/upgrade-postgres.sh"}, 30 * time.Hour}}
	pg.sums = map[string]string{"docker/postgres/Dockerfile": "Moves the database image from Postgres 16.4 to 17.0.", "scripts/upgrade-postgres.sh": "Adds the one-off upgrade script: pg_upgrade with --link from the 16 to the 17 data directory."}

	return []*demoPR{coupons, rounding, dark, pg}
}

// demoAI answers like an AI would, from the demo's own sentences.
type demoAI struct{ pr *demoPR }

func (a demoAI) Complete(_ context.Context, system, user string, _ bool) (string, error) {
	if strings.Contains(system, "guided walkthrough") {
		if a.pr.plan == "" {
			return "", fmt.Errorf("no plan for this one")
		}
		return a.pr.plan, nil
	}
	type part struct {
		Hunks   []int  `json:"hunks"`
		Summary string `json:"summary"`
	}
	type file struct {
		Path    string `json:"path"`
		Summary string `json:"summary"`
		Parts   []part `json:"parts,omitempty"`
	}
	var out struct {
		Files []file `json:"files"`
	}
	for _, f := range a.pr.files {
		if !strings.Contains(user, "### "+f.Path+" ") {
			continue
		}
		fl := file{Path: f.Path, Summary: a.pr.sums[f.Path]}
		for _, p := range a.pr.parts[f.Path] {
			fl.Parts = append(fl.Parts, part{p.hunks, p.text})
		}
		out.Files = append(out.Files, fl)
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

type demoBuild struct {
	in     walk.Input
	events []walk.Event
	ai     walk.AI
}

// demoInput is a demo pull request's input (nil for real ones).
func (s *Server) demoInput(p *store.Pull) *demoBuild {
	if !s.cfg.DemoData {
		return nil
	}
	for _, d := range demoPRs() {
		if d.repo != p.Repo || d.number != p.Number {
			continue
		}
		now := s.now()
		in := walk.Input{Repo: d.repo, Title: d.title, Body: d.body, BaseRef: "main", HeadRef: p.HeadRef, Files: d.files}
		var events []walk.Event
		byPath := map[string]walk.File{}
		for _, f := range d.files {
			byPath[f.Path] = f
		}
		for i, c := range d.commits {
			ev := walk.Event{Kind: "commit", At: store.TS(now.Add(-c.ago)), Title: c.title, Body: c.body, SHA: fmt.Sprintf("%07x%d", 0xa1b2c3+i*977, i), Author: c.author, Files: []walk.TFile{}}
			for _, p := range c.paths {
				f := byPath[p]
				ev.Files = append(ev.Files, walk.TFile{Path: p, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch})
			}
			events = append(events, ev)
			in.Commits = append(in.Commits, walk.Commit{SHA: ev.SHA, Message: c.title, Paths: c.paths})
		}
		var edits []pocket.AgentEdit
		seen := map[string]bool{}
		for _, e := range d.edits {
			edits = append(edits, pocket.AgentEdit{At: store.TS(now.Add(-e.ago)), Path: e.path, Tool: "Edit", Note: e.note, ThreadID: "demo-thread"})
			if !seen[e.path] {
				seen[e.path] = true
				in.AgentOrder = append(in.AgentOrder, e.path)
			}
		}
		if len(edits) > 0 {
			events = append(events, editEvents(edits)...)
			sortEvents(events)
		}
		return &demoBuild{in: in, events: events, ai: demoAI{d}}
	}
	return nil
}

// seedDemo fills the owner's account with the demo (runs once).
func (s *Server) seedDemo(ctx context.Context, owner int64) error {
	key := fmt.Sprintf("seeded:%d:demo", owner)
	if _, _, done := s.store.CacheGet(ctx, key); done {
		return nil
	}
	st, _ := s.store.GetSettings(ctx, owner)
	st.GitHubLogin = "maya"
	_ = s.store.SaveUserSettings(ctx, owner, st)
	now := s.now()
	branches := map[int]string{142: "feature/coupons", 139: "fix/invoice-rounding", 58: "dark-settings", 12: "postgres-17"}
	for _, d := range demoPRs() {
		add, del := 0, 0
		for _, f := range d.files {
			add += f.Additions
			del += f.Deletions
		}
		p := &store.Pull{UserID: owner, Repo: d.repo, Number: d.number, Title: d.title, Body: d.body, URL: fmt.Sprintf("https://github.com/%s/pull/%d", d.repo, d.number),
			Author: d.author, State: "open", BaseRef: "main", HeadRef: branches[d.number],
			BaseSHA: d.base, HeadSHA: d.head, Additions: add, Deletions: del, ChangedFiles: len(d.files), Commits: len(d.commits), Reason: d.reason,
			MyReview: d.review, Source: "sync", GHUpdatedAt: store.TS(now.Add(-d.ago)), SyncedAt: store.Now()}
		if d.review != "" {
			p.ReviewedSHA = d.head
		}
		if err := s.store.SavePull(ctx, p); err != nil {
			return err
		}
		w, err := s.store.CreateWalk(ctx, owner, p.ID, p.HeadSHA, p.BaseSHA)
		if err != nil {
			return err
		}
		b := s.demoInput(p)
		doc, files := walk.Build(ctx, b.in, b.ai, nil)
		rows := make([]store.WFile, 0, len(files))
		order := doc.FileOrder()
		for i, f := range files {
			if n, ok := order[f.Path]; ok {
				i = n
			} else {
				i += len(order)
			}
			rows = append(rows, store.WFile{Path: f.Path, OldPath: f.OldPath, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Layer: f.Layer, Summary: f.Summary, Patch: f.Patch, Sort: i})
		}
		if err := s.store.SaveWalkFiles(ctx, owner, w.ID, rows); err != nil {
			return err
		}
		dj, _ := json.Marshal(doc)
		tj, _ := json.Marshal(b.events)
		if err := s.store.FinishWalk(ctx, w.ID, store.WalkReady, "pocket", "", dj, tj); err != nil {
			return err
		}
		// what Maya already looked at
		look := map[int]int{142: 2, 58: 99, 12: 99}[d.number]
		for i, stp := range doc.Steps {
			if i >= look {
				break
			}
			for _, u := range stp.Units {
				_ = s.store.SetViewed(ctx, owner, p.ID, u.Path, u.Hashes, true)
			}
		}
		if d.stale {
			p.HeadSHA = "e8b14c0d2f96"
			p.Commits++
			_ = s.store.SavePull(ctx, p)
		}
		if d.number == 142 {
			for _, c := range []store.Comment{
				{Path: "internal/coupons/service.go", Line: 32, Body: "This counts the use before the order is paid. Could Redeem move to the payment webhook, and Apply only check the code?"},
				{Path: "db/migrations/0042_coupons.sql", Line: 15, Body: "Add an index on orders(coupon_id) — the coupon report joins on it."},
				{Step: "Routes for checkout and admins", Body: "The logging change in router.go is fine, but it belongs in its own PR."},
			} {
				c.UserID, c.PullID, c.CommitSHA, c.Side = owner, p.ID, p.HeadSHA, "RIGHT"
				_ = s.store.CreateComment(ctx, &c)
			}
		}
	}
	// reviews sent over the last days, for the day view
	for i, r := range []struct {
		repo   string
		number int
		event  string
		body   string
		days   int
	}{
		{"acme/infra", 12, "APPROVE", "Looks good; the --link upgrade is the right call for the window.", 1},
		{"acme/shop", 139, "COMMENT", "One question on negative amounts.", 0},
	} {
		p, err := s.store.GetPullByRef(ctx, owner, r.repo, r.number)
		if err != nil {
			continue
		}
		rv := &store.Review{UserID: owner, PullID: p.ID, Event: r.event, Body: r.body, CommitSHA: p.HeadSHA, GHID: int64(9000 + i),
			GHURL: p.URL + "#pullrequestreview-" + fmt.Sprint(9000+i), Day: now.AddDate(0, 0, -r.days).Format("2006-01-02")}
		_ = s.store.SaveReview(ctx, rv, nil)
	}
	return s.store.CachePut(ctx, key, "1")
}

// demoThreads are teammates' comments on GitHub, for the demo.
func demoThreads(p *store.Pull) []thread {
	if p.Number != 142 {
		return []thread{}
	}
	at := time.Now().Add(-90 * time.Minute).UTC().Format(time.RFC3339)
	line := func(n int) *int { return &n }
	return []thread{
		{ID: 501, Path: "internal/coupons/repository.go", Line: 19, Side: "RIGHT", URL: p.URL + "#discussion_r501", Comments: []github.LineComment{
			{ID: 501, Path: "internal/coupons/repository.go", Line: line(19), Side: "RIGHT", Body: "Nice — one UPDATE with the condition means no race on the last use.", User: github.User{Login: "dino"}, CreatedAt: at},
			{ID: 502, InReplyTo: 501, Path: "internal/coupons/repository.go", Line: line(19), Side: "RIGHT", Body: "Thanks! That was the point of doing it in SQL.", User: github.User{Login: "sara-k"}, CreatedAt: at},
		}},
		{ID: 503, Path: "web/src/pages/admin/Coupons.tsx", Line: 25, Side: "RIGHT", URL: p.URL + "#discussion_r503", Comments: []github.LineComment{
			{ID: 503, Path: "web/src/pages/admin/Coupons.tsx", Line: line(25), Side: "RIGHT", Body: "Should the percentage input say “% off” next to it? On the phone it's just a number.", User: github.User{Login: "lena"}, CreatedAt: at},
		}},
	}
}
