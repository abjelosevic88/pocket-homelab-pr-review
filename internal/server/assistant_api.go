package server

// Read-only answers sized for an assistant (Pocket AI through Core's tool
// catalog): find a pull request, read it in one call without its diffs,
// read one file's diff, and search every changed line. The pull request page
// itself carries every diff and is too large for a tool call.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/walk"
)

const (
	briefBody     = 4000   // characters of the description
	diffCap       = 200000 // bytes of one file's diff
	grepMatches   = 200
	grepLineChars = 300
)

// briefPull is a pull request as a list row for an assistant.
type briefPull struct {
	ID      int64  `json:"id"`
	Ref     string `json:"ref"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	Author  string `json:"author"`
	Reason  string `json:"reason"`
	URL     string `json:"url"`
	Updated string `json:"updated_at"`
}

func toBrief(p *store.Pull) briefPull {
	return briefPull{ID: p.ID, Ref: p.Ref(), Title: p.Title, State: p.State, Draft: p.Draft, Author: p.Author, Reason: p.Reason, URL: p.URL, Updated: p.GHUpdatedAt}
}

var numberOnly = regexp.MustCompile(`^#?(\d+)$`)

// handleFindPulls finds pull requests by address, owner/repo#12, #12 or 12,
// or words of the title or repository — open, closed and archived alike.
func (s *Server) handleFindPulls(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if repo, n, ok := github.ParseRef(q); ok {
		if p, err := s.store.GetPullByRef(ctx, uid, repo, n); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"pulls": []briefPull{toBrief(p)}})
			return
		}
	}
	all, err := s.store.ListPulls(ctx, uid, store.PullFilter{State: "all", Limit: 1000})
	if err != nil {
		s.fail(w, err, "pull requests")
		return
	}
	if archived, err := s.store.ListPulls(ctx, uid, store.PullFilter{State: "all", Archived: true, Limit: 1000}); err == nil {
		all = append(all, archived...)
	}
	seen := map[int64]bool{}
	out := []briefPull{}
	num := 0
	if m := numberOnly.FindStringSubmatch(q); m != nil {
		num, _ = strconv.Atoi(m[1])
	}
	words := strings.Fields(strings.ToLower(q))
	for _, p := range all {
		if seen[p.ID] {
			continue
		}
		hay := strings.ToLower(p.Ref() + " " + p.Title + " " + p.Author + " " + p.HeadRef)
		hit := q == ""
		if num > 0 {
			hit = p.Number == num
		} else if len(words) > 0 {
			hit = true
			for _, w := range words {
				hit = hit && strings.Contains(hay, w)
			}
		}
		if hit {
			seen[p.ID] = true
			out = append(out, toBrief(p))
		}
		if len(out) >= 50 {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pulls": out})
}

// readyWalk is the pull request with its newest finished walkthrough.
func (s *Server) readyWalk(w http.ResponseWriter, r *http.Request) (*store.Pull, *store.Walkthrough, *walk.Doc, bool) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return nil, nil, nil, false
	}
	wk, err := s.store.LatestReadyWalk(ctx, uid, p.ID)
	if err != nil {
		return p, nil, nil, true
	}
	var d walk.Doc
	if err := json.Unmarshal(wk.Doc, &d); err != nil {
		s.fail(w, err, "walkthrough")
		return nil, nil, nil, false
	}
	return p, wk, &d, true
}

// handleBrief is a pull request in one call: what it is, the walkthrough
// (summary, diagram, steps with their files, risks, questions) and every
// changed file with its one sentence — no diffs.
func (s *Server) handleBrief(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, wk, d, ok := s.readyWalk(w, r)
	if !ok {
		return
	}
	body := p.Body
	if len(body) > briefBody {
		body = body[:briefBody] + "…"
	}
	out := map[string]any{
		"pull": map[string]any{"id": p.ID, "ref": p.Ref(), "title": p.Title, "description": body, "url": p.URL, "author": p.Author, "state": p.State, "draft": p.Draft,
			"base": p.BaseRef, "head": p.HeadRef, "head_sha": p.HeadSHA, "additions": p.Additions, "deletions": p.Deletions, "changed_files": p.ChangedFiles, "commits": p.Commits, "my_review": p.MyReview},
		"page_url": s.pullURL(p),
	}
	if d == nil {
		out["walkthrough"] = nil
		out["note"] = "There is no finished walkthrough yet; the files are not known here. Read the pull request on GitHub."
		writeJSON(w, http.StatusOK, out)
		return
	}
	type stepOut struct {
		N     int      `json:"n"`
		Title string   `json:"title"`
		Why   string   `json:"why"`
		Risks []string `json:"risks"`
		Files []string `json:"files"`
	}
	steps := []stepOut{}
	for i, st := range d.Steps {
		files := []string{}
		for _, u := range st.Units {
			if u.Hunks != nil {
				files = append(files, u.Path+" (part)")
			} else {
				files = append(files, u.Path)
			}
		}
		steps = append(steps, stepOut{N: i + 1, Title: st.Title, Why: st.Why, Risks: st.Risks, Files: files})
	}
	noise := []string{}
	for _, u := range d.Noise {
		noise = append(noise, u.Path)
	}
	out["walkthrough"] = map[string]any{"head_sha": wk.HeadSHA, "stale": wk.HeadSHA != p.HeadSHA, "tldr": d.TLDR, "overview": d.Overview, "before": d.Before, "after": d.After,
		"diagram": d.Diagram, "terms": d.Terms, "steps": steps, "risks": d.Risks, "questions": d.Questions, "generated_or_lock_files": noise}
	files, err := s.store.WalkFiles(ctx, actorID(ctx), wk.ID, false)
	if err == nil {
		type fileOut struct {
			Path      string `json:"path"`
			OldPath   string `json:"old_path,omitempty"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
			Layer     string `json:"layer"`
			Summary   string `json:"summary"`
			NoDiff    bool   `json:"no_diff,omitempty"`
		}
		list := []fileOut{}
		for _, f := range files {
			list = append(list, fileOut{f.Path, f.OldPath, f.Status, f.Additions, f.Deletions, f.Layer, f.Summary, f.Binary || f.Truncated})
		}
		out["files"] = list
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFileDiff is one changed file's unified diff, as text.
func (s *Server) handleFileDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, wk, _, ok := s.readyWalk(w, r)
	if !ok {
		return
	}
	if wk == nil {
		writeErr(w, http.StatusNotFound, "there is no finished walkthrough yet")
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeErr(w, http.StatusBadRequest, "path is the changed file's path, as the brief lists it")
		return
	}
	files, err := s.store.WalkFiles(ctx, actorID(ctx), wk.ID, true)
	if err != nil {
		s.fail(w, err, "files")
		return
	}
	var f *store.WFile
	for i := range files {
		if files[i].Path == path {
			f = &files[i]
		}
	}
	if f == nil {
		// a path without its folders, when it names one file
		for i := range files {
			if strings.HasSuffix(files[i].Path, "/"+path) {
				if f != nil {
					writeErr(w, http.StatusBadRequest, "several changed files end with "+path+"; give the whole path")
					return
				}
				f = &files[i]
			}
		}
	}
	if f == nil {
		writeErr(w, http.StatusNotFound, path+" is not changed in "+p.Ref())
		return
	}
	var b strings.Builder
	b.WriteString("--- " + f.Status + ": " + f.Path)
	if f.OldPath != "" && f.OldPath != f.Path {
		b.WriteString(" (was " + f.OldPath + ")")
	}
	b.WriteString(" +" + strconv.Itoa(f.Additions) + " -" + strconv.Itoa(f.Deletions) + "\n")
	switch {
	case f.Binary:
		b.WriteString("(a binary file)\n")
	case f.Truncated || f.Patch == "":
		b.WriteString("(GitHub sends no diff for this file; read it on GitHub: " + p.URL + "/files)\n")
	case len(f.Patch) > diffCap:
		b.WriteString(f.Patch[:diffCap])
		b.WriteString("\n… cut at " + strconv.Itoa(diffCap/1000) + " KB of " + strconv.Itoa(len(f.Patch)/1000) + " KB; use the search for the rest\n")
	default:
		b.WriteString(f.Patch)
		b.WriteString("\n")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// handleGrep finds lines of the pull request's diffs: added, removed and the
// context around them. q is words (all must be on the line, any case) or,
// with regex=1, a regular expression. path narrows to files containing it;
// side = added, removed or all.
func (s *Server) handleGrep(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, wk, _, ok := s.readyWalk(w, r)
	if !ok {
		return
	}
	if wk == nil {
		writeErr(w, http.StatusNotFound, "there is no finished walkthrough yet")
		return
	}
	qv := r.URL.Query()
	q := strings.TrimSpace(qv.Get("q"))
	if q == "" || len(q) > 200 {
		writeErr(w, http.StatusBadRequest, "q is what to look for (up to 200 characters)")
		return
	}
	match := func(string) bool { return false }
	if qv.Get("regex") == "1" || qv.Get("regex") == "true" {
		re, err := regexp.Compile("(?i)" + q)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "q is not a valid regular expression: "+err.Error())
			return
		}
		match = re.MatchString
	} else {
		words := strings.Fields(strings.ToLower(q))
		match = func(line string) bool {
			l := strings.ToLower(line)
			for _, w := range words {
				if !strings.Contains(l, w) {
					return false
				}
			}
			return true
		}
	}
	side := qv.Get("side")
	if side == "" {
		side = "all"
	}
	if side != "all" && side != "added" && side != "removed" {
		writeErr(w, http.StatusBadRequest, "side is added, removed or all")
		return
	}
	pathPart := strings.ToLower(strings.TrimSpace(qv.Get("path")))
	files, err := s.store.WalkFiles(ctx, actorID(ctx), wk.ID, true)
	if err != nil {
		s.fail(w, err, "files")
		return
	}
	type hit struct {
		Path string `json:"path"`
		Line int    `json:"line"` // in the new file, or the old one for a removed line
		Kind string `json:"kind"` // added | removed | context
		Text string `json:"text"`
	}
	hits := []hit{}
	total := 0
	perFile := map[string]int{}
	skipped := []string{}
	for _, f := range files {
		if pathPart != "" && !strings.Contains(strings.ToLower(f.Path), pathPart) {
			continue
		}
		if f.Patch == "" {
			if f.Truncated {
				skipped = append(skipped, f.Path)
			}
			continue
		}
		var oldN, newN int
		for _, line := range strings.Split(f.Patch, "\n") {
			if strings.HasPrefix(line, "@@") {
				oldN, newN = hunkStart(line)
				continue
			}
			if line == "" {
				continue
			}
			kind, n := "context", newN
			switch line[0] {
			case '+':
				kind, n = "added", newN
				newN++
			case '-':
				kind, n = "removed", oldN
				oldN++
			case '\\':
				continue
			default:
				oldN++
				newN++
			}
			if (side == "added" && kind != "added") || (side == "removed" && kind != "removed") {
				continue
			}
			text := line[1:]
			if !match(text) {
				continue
			}
			total++
			perFile[f.Path]++
			if len(hits) < grepMatches {
				if len(text) > grepLineChars {
					text = text[:grepLineChars] + "…"
				}
				hits = append(hits, hit{f.Path, n, kind, strings.TrimRight(text, "\r")})
			}
		}
	}
	out := map[string]any{"pull": p.Ref(), "q": q, "matches": hits, "total": total, "per_file": perFile}
	if total > len(hits) {
		out["note"] = "Only the first " + strconv.Itoa(grepMatches) + " matches are listed; narrow with path or side."
	}
	if len(skipped) > 0 {
		out["not_searched"] = skipped
	}
	writeJSON(w, http.StatusOK, out)
}

var hunkHead = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)`)

func hunkStart(line string) (int, int) {
	m := hunkHead.FindStringSubmatch(line)
	if m == nil {
		return 0, 0
	}
	o, _ := strconv.Atoi(m[1])
	n, _ := strconv.Atoi(m[2])
	return o, n
}
