package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// What Pocket Search, Pocket Day and Pocket Hub ask PR Review (the
// capabilities value in links.go): one route each, in the shape every Pocket
// app answers in, as the request's person.
//
// None of them asks GitHub. They read what the app already keeps: the pull
// requests and walkthroughs from its syncs, the reviews sent from here, and
// where each pull request stood the last time the Yours board or the pull
// request page looked (kept a day, as the board keeps it). What only GitHub
// knows right now is left out rather than fetched.

// pullPath opens a pull request in the web app; walkPath one walkthrough of it.
func pullPath(id int64) string { return "/pr/" + strconv.FormatInt(id, 10) }

func walkPath(pullID, walkID int64) string {
	return pullPath(pullID) + "?v=" + strconv.FormatInt(walkID, 10)
}

// dots joins the parts of a line that are there: "acme/shop#7 · open · updated 20 Nov".
func dots(parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// counted: "1 review", "3 reviews".
func counted(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// shortDay is a stored time as "20 Nov" (with the year when it is not this one).
func shortDay(at string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return ""
	}
	t = t.In(now.Location())
	if t.Year() != now.Year() {
		return t.Format("2 Jan 2006")
	}
	return t.Format("2 Jan")
}

// standing is where my open pull request stands by its kept status, in the
// board's own terms: needs me (red), ready to merge (green), or neither.
type standing struct {
	needs, ready bool
	words        []string // "CI failing", "changes asked", "ready to merge"…
}

func standOf(st *github.Status) standing {
	var s standing
	if st == nil {
		return s
	}
	ck := st.Checks
	if ck.Failed > 0 {
		s.words = append(s.words, "CI failing")
	}
	if len(st.ChangesWanted) > 0 {
		s.words = append(s.words, "changes asked")
	}
	if st.Mergeable == "CONFLICTING" || st.MergeState == "DIRTY" {
		s.words = append(s.words, "conflicts")
	}
	s.needs = len(s.words) > 0
	if !s.needs && (st.MergeState == "CLEAN" || st.MergeState == "HAS_HOOKS") && ck.Running == 0 {
		s.ready = true
		s.words = append(s.words, "ready to merge")
	}
	if !s.needs && !s.ready {
		switch {
		case ck.Running > 0:
			s.words = append(s.words, "CI running")
		case st.Unresolved > 0:
			s.words = append(s.words, counted(st.Unresolved, "unresolved conversation", "unresolved conversations"))
		case st.Draft:
			s.words = append(s.words, "draft")
		case len(st.Requested) > 0:
			s.words = append(s.words, "waiting for review")
		}
	}
	return s
}

// stateWords says how a pull request stands in plain words: "merged",
// "open, 2 approvals, CI failing", "open, you approved".
func stateWords(p *store.Pull, st *github.Status) string {
	switch p.State {
	case "merged", "closed":
		return p.State
	}
	parts := []string{"open"}
	if p.Draft {
		parts[0] = "draft"
	}
	if st == nil {
		switch p.MyReview {
		case "APPROVED":
			parts = append(parts, "you approved")
		case "CHANGES_REQUESTED":
			parts = append(parts, "you asked for changes")
		}
		return strings.Join(parts, ", ")
	}
	if n := len(st.Approved); n > 0 {
		parts = append(parts, counted(n, "approval", "approvals"))
	}
	if len(st.ChangesWanted) > 0 {
		parts = append(parts, "changes asked")
	}
	if st.Mergeable == "CONFLICTING" || st.MergeState == "DIRTY" {
		parts = append(parts, "conflicts")
	}
	ck := st.Checks
	switch {
	case ck.Failed > 0:
		parts = append(parts, "CI failing")
	case ck.Running > 0:
		parts = append(parts, "CI running")
	case ck.Known && ck.Passed > 0:
		parts = append(parts, "CI passing")
	}
	return strings.Join(parts, ", ")
}

// kept is the status the app last read for an open pull request (demo data
// makes one up, as the board does); nil when none was read in the last day.
func (s *Server) kept(ctx context.Context, uid int64, p *store.Pull) *github.Status {
	if p.State != "open" {
		return nil
	}
	if s.cfg.DemoData {
		return demoStatus(p)
	}
	return s.keptStatus(ctx, uid, p)
}

// changedAt is when a pull request last changed: on GitHub when the sync
// read it, else here.
func changedAt(p *store.Pull) string {
	if p.GHUpdatedAt != "" {
		return p.GHUpdatedAt
	}
	return p.UpdatedAt
}

// ---------- search ----------

// pocketSearch answers Pocket Search with what GET /pulls/find finds (by
// address, owner/repo#12, #12, or words of the title, repository, author or
// branch; open, closed and archived alike), and with the walkthroughs whose
// own title or summary holds the words when their pull request does not.
func (s *Server) pocketSearch(r *http.Request, q string, limit int) ([]core.SearchItem, error) {
	ctx := r.Context()
	uid := actorID(ctx)
	// no letter or digit: nothing to look for (find would take every word as found)
	if strings.IndexFunc(q, func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }) < 0 {
		return nil, nil
	}
	pulls, err := s.findPulls(ctx, uid, q, limit)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]core.SearchItem, 0, len(pulls))
	found := map[int64]bool{}
	for _, p := range pulls {
		found[p.ID] = true
		at := changedAt(p)
		out = append(out, core.SearchItem{ID: "pull:" + strconv.FormatInt(p.ID, 10), Title: p.Title, Kind: "pull request", Date: at,
			URL: pullPath(p.ID), Link: p.URL, Snippet: dots(p.Ref(), stateWords(p, s.kept(ctx, uid, p)), "updated "+shortDay(at, now))})
	}
	// a number or an address names a pull request, not words of a story
	if _, _, ok := github.ParseRef(q); ok || numberOnly.MatchString(q) || len(out) >= limit {
		return out, nil
	}
	heads, err := s.store.WalkHeads(ctx, uid)
	if err != nil {
		return nil, err
	}
	words := strings.Fields(strings.ToLower(q))
	for _, h := range heads {
		if found[h.PullID] || !hasWords(strings.ToLower(h.Title+" "+h.TLDR), words) {
			continue
		}
		p, err := s.store.GetPull(ctx, uid, h.PullID)
		if err != nil {
			continue
		}
		title := h.Title
		if title == "" {
			title = p.Title
		}
		out = append(out, core.SearchItem{ID: "walkthrough:" + strconv.FormatInt(h.ID, 10), Title: title, Kind: "walkthrough", Date: h.FinishedAt,
			URL: walkPath(p.ID, h.ID), Link: p.URL, Snippet: dots(p.Ref(), stateWords(p, s.kept(ctx, uid, p)), "made "+shortDay(h.FinishedAt, now))})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ---------- day ----------

// reviewWords is what a review sent from here said.
var reviewWords = map[string]string{"APPROVE": "approved", "REQUEST_CHANGES": "asked for changes", "COMMENT": "commented"}

// utcBound is a moment as the stored RFC 3339 times compare with it as text:
// the milliseconds keep "10:00:00Z" after "10:00:00.000Z" and before
// "10:00:00.5Z", so a range is exact to the moment.
func utcBound(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// pocketDay answers Pocket Day with the person's pull request work on a day,
// all of it from what the app keeps: the reviews sent from here (on the day
// GET /day/{date} files them under), the walkthroughs that were ready that
// day, and their own pull requests merged that day. When a pull request was
// opened is not kept (only when it came onto the list here), so it is not
// part of the day.
func (s *Server) pocketDay(r *http.Request, date string, tz *time.Location) (*core.Day, error) {
	ctx := r.Context()
	uid := actorID(ctx)
	loc := time.Local
	if tz != nil {
		loc = tz
	}
	start, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, nil
	}
	from, to := utcBound(start), utcBound(start.AddDate(0, 0, 1))
	// HH:MM of a stored time, when it falls on the day where the person is
	clock := func(at string) string {
		t, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return ""
		}
		if t = t.In(loc); t.Format("2006-01-02") != date {
			return ""
		}
		return t.Format("15:04")
	}

	reviews, err := s.store.Reviews(ctx, uid, 0, date)
	if err != nil {
		return nil, err
	}
	walks, err := s.store.WalksFinished(ctx, uid, from, to)
	if err != nil {
		return nil, err
	}
	// the merge itself is GitHub's to say; the sync reads a pull request
	// once more when it leaves the open list, and GitHub's last change then
	// is the merge
	merged, err := s.store.PullsChangedBetween(ctx, uid, "merged", from, to)
	if err != nil {
		return nil, err
	}
	st, _ := s.store.GetSettings(ctx, uid)
	day := &core.Day{Date: date}
	nMerged, approved := 0, 0
	for _, p := range merged {
		if !mine(p, st.GitHubLogin) {
			continue
		}
		nMerged++
		day.Items = append(day.Items, core.DayItem{ID: "pull:" + strconv.FormatInt(p.ID, 10), Time: clock(p.GHUpdatedAt), Title: p.Title, Sub: p.Ref(),
			Kind: "merged", Done: true, URL: pullPath(p.ID)})
	}
	for _, rv := range reviews {
		if rv.Event == "APPROVE" {
			approved++
		}
		// a review keeps the day it was sent; its own time only when it was sent here then (not brought in by Pocket Move)
		at := ""
		if t, err := time.Parse(time.RFC3339Nano, rv.CreatedAt); err == nil && t.In(time.Local).Format("2006-01-02") == rv.Day {
			at = clock(rv.CreatedAt)
		}
		day.Items = append(day.Items, core.DayItem{ID: "review:" + strconv.FormatInt(rv.ID, 10), Time: at, Title: rv.Title,
			Sub: dots(rv.Repo+"#"+strconv.Itoa(rv.Number), reviewWords[rv.Event]), Kind: "reviewed", URL: pullPath(rv.PullID)})
	}
	nWalks := 0
	for _, wk := range walks {
		p, err := s.store.GetPull(ctx, uid, wk.PullID)
		if err != nil {
			continue
		}
		nWalks++
		day.Items = append(day.Items, core.DayItem{ID: "walkthrough:" + strconv.FormatInt(wk.ID, 10), Time: clock(wk.FinishedAt), Title: p.Title,
			Sub: dots(p.Ref(), "walkthrough"), Kind: "walkthrough", URL: walkPath(p.ID, wk.ID)})
	}
	if len(day.Items) == 0 {
		return nil, nil
	}

	// "1 pull request merged, 2 reviews sent, 1 walkthrough made"
	var parts []string
	if nMerged > 0 {
		parts = append(parts, counted(nMerged, "pull request", "pull requests")+" merged")
		day.Metrics = append(day.Metrics, core.Metric{Label: "Merged", Value: strconv.Itoa(nMerged)})
	}
	if len(reviews) > 0 {
		parts = append(parts, counted(len(reviews), "review", "reviews")+" sent")
		day.Metrics = append(day.Metrics, core.Metric{Label: "Reviews", Value: strconv.Itoa(len(reviews))})
		if approved > 0 {
			day.Metrics = append(day.Metrics, core.Metric{Label: "Approved", Value: strconv.Itoa(approved)})
		}
	}
	if nWalks > 0 {
		parts = append(parts, counted(nWalks, "walkthrough", "walkthroughs")+" made")
		day.Metrics = append(day.Metrics, core.Metric{Label: "Walkthroughs", Value: strconv.Itoa(nWalks)})
	}
	day.Summary = strings.Join(parts, ", ")
	return day, nil
}

// ---------- widget ----------

// pocketWidget answers Pocket Hub with the person's own open pull requests
// as the Yours board last saw them: the ones that need them first (CI
// failing, changes asked, conflicts), then the ones ready to merge, then the
// rest. A pull request whose status was not read in the last day is listed
// without one and counted in neither.
func (s *Server) pocketWidget(r *http.Request) (*core.WidgetData, error) {
	ctx := r.Context()
	uid := actorID(ctx)
	st, _ := s.store.GetSettings(ctx, uid)
	open, err := s.store.ListPulls(ctx, uid, store.PullFilter{State: "open"})
	if err != nil {
		return nil, err
	}
	login := st.GitHubLogin
	if s.cfg.DemoData && login == "" && len(open) > 0 {
		login = open[0].Author
	}
	type row struct {
		p *store.Pull
		s standing
	}
	var needs, ready, rest []row
	for _, p := range open {
		if !mine(p, login) && !s.cfg.DemoData {
			continue
		}
		x := row{p, standOf(s.kept(ctx, uid, p))}
		switch {
		case x.s.needs:
			needs = append(needs, x)
		case x.s.ready:
			ready = append(ready, x)
		default:
			rest = append(rest, x)
		}
	}
	all := append(append(needs, ready...), rest...)
	if len(all) == 0 {
		return nil, nil
	}
	w := &core.WidgetData{URL: "/mine", Metrics: []core.Metric{{Label: "Open", Value: strconv.Itoa(len(all))}}}
	var line []string
	if n := len(needs); n > 0 {
		if n == 1 {
			line = append(line, "1 needs you")
		} else {
			line = append(line, strconv.Itoa(n)+" need you")
		}
		w.Metrics = append(w.Metrics, core.Metric{Label: "Need you", Value: strconv.Itoa(n)})
	}
	if n := len(ready); n > 0 {
		line = append(line, strconv.Itoa(n)+" ready to merge")
		w.Metrics = append(w.Metrics, core.Metric{Label: "Ready to merge", Value: strconv.Itoa(n)})
	}
	w.Line = strings.Join(line, ", ")
	for _, x := range all {
		if len(w.Items) == 8 {
			break
		}
		w.Items = append(w.Items, core.WidgetItem{ID: strconv.FormatInt(x.p.ID, 10), Title: x.p.Title, Detail: dots(x.p.Ref(), strings.Join(x.s.words, ", ")),
			URL: pullPath(x.p.ID)})
	}
	return w, nil
}
