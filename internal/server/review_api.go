package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/walk"
)

// ---- my comments (drafts until the review is sent) ----

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	list, err := s.store.Comments(ctx, uid, p.ID, r.URL.Query().Get("state"))
	if err != nil {
		s.fail(w, err, "comments")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	var in store.Comment
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" {
		writeErr(w, http.StatusBadRequest, "write the comment")
		return
	}
	if len(in.Body) > 60000 {
		writeErr(w, http.StatusBadRequest, "the comment is too long")
		return
	}
	in.Side = strings.ToUpper(in.Side)
	if in.Side != "LEFT" {
		in.Side = "RIGHT"
	}
	if in.Path != "" && in.Line <= 0 {
		writeErr(w, http.StatusBadRequest, "a comment on a file needs its line")
		return
	}
	if in.Path == "" && strings.TrimSpace(in.Step) == "" {
		writeErr(w, http.StatusBadRequest, "say which line (path + line) or which step the comment is about")
		return
	}
	if in.StartLine >= in.Line {
		in.StartLine = 0
	}
	in.UserID, in.PullID = uid, p.ID
	if in.CommitSHA == "" {
		in.CommitSHA = p.HeadSHA
	}
	if err := s.store.CreateComment(ctx, &in); err != nil {
		s.fail(w, err, "save comment")
		return
	}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleUpdateComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Body string `json:"body"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Body) == "" {
		writeErr(w, http.StatusBadRequest, "write the comment")
		return
	}
	uid := actorID(ctx)
	if err := s.store.UpdateCommentBody(ctx, uid, idParam(r, "cid"), strings.TrimSpace(in.Body)); err != nil {
		s.fail(w, err, "comment")
		return
	}
	c, err := s.store.GetComment(ctx, uid, idParam(r, "cid"))
	if err != nil {
		s.fail(w, err, "comment")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteComment(r.Context(), actorID(r.Context()), idParam(r, "cid")); err != nil {
		s.fail(w, err, "comment")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- the conversation on GitHub ----

// thread is a GitHub line comment with its replies.
type thread struct {
	ID       int64                `json:"id"`
	Path     string               `json:"path"`
	Line     int                  `json:"line"` // 0 = outdated
	Side     string               `json:"side"`
	Outdated bool                 `json:"outdated"`
	URL      string               `json:"url"`
	Comments []github.LineComment `json:"comments"`
}

// handleThreads lists the pull request's line comments on GitHub, as threads.
func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	if s.cfg.DemoData {
		writeJSON(w, http.StatusOK, map[string]any{"threads": demoThreads(p)})
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	gh, err := s.ghFor(st)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"threads": []thread{}, "error": err.Error()})
		return
	}
	key := fmt.Sprintf("threads:%d:%d", uid, p.ID)
	if v, at, ok := s.store.CacheGet(ctx, key); ok && !queryBool(r, "fresh") {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < 30*time.Second {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(v))
			return
		}
	}
	list, err := gh.LineComments(ctx, p.Repo, p.Number)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"threads": []thread{}, "error": err.Error()})
		return
	}
	b, _ := json.Marshal(map[string]any{"threads": threads(list)})
	_ = s.store.CachePut(ctx, key, string(b))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}

func threads(list []github.LineComment) []thread {
	byID := map[int64]*thread{}
	var order []int64
	for _, c := range list {
		if c.InReplyTo != 0 {
			if t := byID[c.InReplyTo]; t != nil {
				t.Comments = append(t.Comments, c)
				continue
			}
		}
		t := &thread{ID: c.ID, Path: c.Path, Side: c.Side, URL: c.HTMLURL, Comments: []github.LineComment{c}}
		if c.Line != nil {
			t.Line = *c.Line
		} else {
			t.Outdated = true
			if c.OrigLine != nil {
				t.Line = *c.OrigLine
			}
		}
		if t.Side == "" {
			t.Side = "RIGHT"
		}
		byID[c.ID] = t
		order = append(order, c.ID)
	}
	out := make([]thread, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// handleReply answers a GitHub thread right away.
func (s *Server) handleReply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	var in struct {
		To   int64  `json:"to"`
		Body string `json:"body"`
	}
	if err := decode(r, &in); err != nil || in.To == 0 || strings.TrimSpace(in.Body) == "" {
		writeErr(w, http.StatusBadRequest, "write the reply")
		return
	}
	if s.cfg.DemoData {
		writeErr(w, http.StatusBadRequest, "the demo does not write to GitHub")
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	gh, err := s.ghFor(st)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := gh.Reply(ctx, p.Repo, p.Number, in.To, strings.TrimSpace(in.Body))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = s.store.CacheDelete(ctx, fmt.Sprintf("threads:%d:%d", uid, p.ID))
	writeJSON(w, http.StatusCreated, c)
}

// ---- sending the review ----

var reviewEvents = map[string]string{"APPROVE": "APPROVED", "REQUEST_CHANGES": "CHANGES_REQUESTED", "COMMENT": "COMMENTED"}

// handleSubmitReview sends my review — the text, my draft comments on lines,
// my notes on steps (in the text) — to GitHub.
func (s *Server) handleSubmitReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	var in struct {
		Event string `json:"event"`
		Body  string `json:"body"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Event = strings.ToUpper(strings.TrimSpace(in.Event))
	state, ok := reviewEvents[in.Event]
	if !ok {
		writeErr(w, http.StatusBadRequest, "approve, request changes or comment")
		return
	}
	drafts, err := s.store.Comments(ctx, uid, p.ID, "draft")
	if err != nil {
		s.fail(w, err, "comments")
		return
	}
	body := strings.TrimSpace(in.Body)
	var notes []string
	var line []github.NewComment
	var ids []int64
	commit := ""
	for _, c := range drafts {
		ids = append(ids, c.ID)
		if c.Path == "" {
			notes = append(notes, fmt.Sprintf("- **%s** — %s", c.Step, c.Body))
			continue
		}
		nc := github.NewComment{Path: c.Path, Body: c.Body, Line: c.Line, Side: c.Side}
		if c.StartLine > 0 && c.StartLine < c.Line {
			nc.StartLine, nc.StartSide = c.StartLine, c.Side
		}
		line = append(line, nc)
		if commit == "" {
			commit = c.CommitSHA
		}
	}
	if len(notes) > 0 {
		if body != "" {
			body += "\n\n"
		}
		body += "**By step**\n\n" + strings.Join(notes, "\n")
	}
	if in.Event != "APPROVE" && body == "" && len(line) == 0 {
		writeErr(w, http.StatusBadRequest, "write what you think, or add comments, before sending")
		return
	}
	if in.Event == "REQUEST_CHANGES" && body == "" {
		body = "Please see the comments."
	}
	if commit == "" {
		commit = p.HeadSHA
	}
	rv := &store.Review{UserID: uid, PullID: p.ID, Event: in.Event, Body: body, CommitSHA: commit, Comments: len(line), Day: s.now().Format("2006-01-02")}
	if !s.cfg.DemoData {
		st, _ := s.store.GetSettings(ctx, uid)
		gh, err := s.ghFor(st)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		got, err := gh.CreateReview(ctx, p.Repo, p.Number, commit, in.Event, body, line)
		if err != nil {
			msg := err.Error()
			var ge *github.Error
			if errors.As(err, &ge) && ge.Status == http.StatusUnprocessableEntity {
				msg += " — a comment may point at a line that changed since; update the walkthrough and move it."
			}
			writeErr(w, http.StatusBadGateway, msg)
			return
		}
		rv.GHID, rv.GHURL = got.ID, got.HTMLURL
		if got.CommitID != "" {
			rv.CommitSHA = got.CommitID
		}
	}
	if err := s.store.SaveReview(ctx, rv, ids); err != nil {
		s.fail(w, err, "save review")
		return
	}
	_ = s.store.SetMyReview(ctx, uid, p.ID, state, rv.CommitSHA)
	_ = s.store.CacheDelete(ctx, fmt.Sprintf("threads:%d:%d", uid, p.ID))
	s.publish(ctx, uid, "pr-review.review_sent", p.Ref(), map[string]any{"repo": p.Repo, "number": p.Number, "title": p.Title, "url": p.URL,
		"event": in.Event, "comments": len(line), "review_url": rv.GHURL})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "review": rv, "demo": s.cfg.DemoData})
}

// ---- asking the AI about the pull request ----

const askVoice = `You help someone review a pull request. You get the walkthrough (what changed, in reading order) and, when they point at one, the diff of a step or a file. Answer their question plainly and briefly, with the concrete names from the code. Say when the diff does not show enough to know. Do not make up code that is not in the diff.`

func (s *Server) handleAskPull(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, d, ok := s.readyDoc(w, r)
	if !ok {
		return
	}
	var in struct {
		Question string `json:"question"`
		Step     string `json:"step"` // step key
		Path     string `json:"path"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Question) == "" {
		writeErr(w, http.StatusBadRequest, "ask a question")
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	cl, _, why := s.aiFor(ctx, uid, st)
	if cl == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": firstNonEmpty(why, "no AI is set up (Settings → AI)")})
		return
	}
	wk, _ := s.store.LatestReadyWalk(ctx, uid, p.ID)
	files, _ := s.store.WalkFiles(ctx, uid, wk.ID, true)
	want := map[string]bool{}
	if in.Path != "" {
		want[in.Path] = true
	}
	for _, stp := range d.Steps {
		if stp.Key == in.Step {
			for _, u := range stp.Units {
				want[u.Path] = true
			}
		}
	}
	var b strings.Builder
	b.WriteString(walk.Markdown(d, p.URL, p.Title, func(string) string { return "#" }))
	size := 0
	for _, f := range files {
		if !want[f.Path] || f.Patch == "" {
			continue
		}
		if size += len(f.Patch); size > 120000 {
			break
		}
		fmt.Fprintf(&b, "\n\nDiff of %s:\n%s", f.Path, f.Patch)
	}
	b.WriteString("\n\nQuestion: " + strings.TrimSpace(in.Question))
	text, err := cl.Complete(ctx, askVoice+languageNote(st.WalkLanguage), b.String(), false)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "answer": strings.TrimSpace(text)})
}

func languageNote(lang string) string {
	if strings.TrimSpace(lang) == "" || strings.EqualFold(lang, "english") {
		return ""
	}
	return "\nAnswer in " + lang + "."
}
