package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/walk"
)

// A message for Slack: Pocket AI writes a line about the pull request, the
// person picks who to @mention and copies it. Nothing is sent from here.

const slackDefaultPrompt = "One short sentence, at most 15 words: what the pull request changes, in plain words. No details, caveats, test status or file names."

const slackVoice = `You write short Slack messages that ask teammates to review a pull request.
Write only the message text: no greeting, no @mentions, no link, no quotes around it, no Markdown headings.`

func (s *Server) handleGetSlack(w http.ResponseWriter, r *http.Request) {
	st, _ := s.store.GetSettings(r.Context(), actorID(r.Context()))
	people := st.SlackPeople
	if people == nil {
		people = []store.SlackPerson{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"people": people, "prompt": st.SlackPrompt, "default_prompt": slackDefaultPrompt})
}

func (s *Server) handlePutSlack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in struct {
		People *[]store.SlackPerson `json:"people"`
		Prompt *string              `json:"prompt"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	st, err := s.store.GetSettings(ctx, uid)
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	if in.People != nil {
		out := []store.SlackPerson{}
		seen := map[string]bool{}
		for _, p := range *in.People {
			p.Name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.Name), "@"))
			p.Handle = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.Handle), "@"))
			p.GitHub = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.GitHub), "@"))
			if p.Name == "" || seen[strings.ToLower(p.Name)] || len(p.Name) > 80 || len(p.Handle) > 80 || len(p.GitHub) > 60 {
				continue
			}
			seen[strings.ToLower(p.Name)] = true
			out = append(out, p)
		}
		if len(out) > 100 {
			out = out[:100]
		}
		st.SlackPeople = out
	}
	if in.Prompt != nil {
		p := strings.TrimSpace(*in.Prompt)
		if len(p) > 2000 {
			writeErr(w, http.StatusBadRequest, "keep it under 2,000 characters")
			return
		}
		if p == slackDefaultPrompt {
			p = ""
		}
		st.SlackPrompt = p
	}
	if err := s.store.SaveUserSettings(ctx, uid, st); err != nil {
		s.fail(w, err, "settings")
		return
	}
	s.handleGetSlack(w, r)
}

// handleSlackDraft writes the line about one pull request. Kept per head
// commit and way of writing, unless asked to write it again.
func (s *Server) handleSlackDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	p, err := s.store.GetPull(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "pull request")
		return
	}
	var in struct {
		Again bool `json:"again"`
	}
	_ = decode(r, &in)
	st, _ := s.store.GetSettings(ctx, uid)
	how := firstNonEmpty(st.SlackPrompt, slackDefaultPrompt)
	out := map[string]any{"text": p.Title, "link": p.URL, "note": ""}
	sum := sha256.Sum256([]byte(how + "\x00" + p.HeadSHA + "\x00" + p.Title))
	key := fmt.Sprintf("slack:%d:%d:%s", uid, p.ID, hex.EncodeToString(sum[:8]))
	if v, _, ok := s.store.CacheGet(ctx, key); ok && !in.Again && v != "" {
		out["text"] = v
		writeJSON(w, http.StatusOK, out)
		return
	}
	if s.cfg.DemoData {
		out["text"] = "Adds " + strings.ToLower(p.Title[:1]) + p.Title[1:] + "."
		writeJSON(w, http.StatusOK, out)
		return
	}
	cl, _, why := s.aiFor(ctx, uid, st)
	if cl == nil {
		out["note"] = "Pocket AI is not set up, so this is the title: " + firstNonEmpty(why, "Settings → AI")
		writeJSON(w, http.StatusOK, out)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "How to write it: %s\n\nPull request: %s#%d\nTitle: %s\nBranch: %s into %s\nSize: +%d −%d in %d files\n\nDescription:\n%s\n",
		how, p.Repo, p.Number, p.Title, p.HeadRef, p.BaseRef, p.Additions, p.Deletions, p.ChangedFiles, clip(p.Body, 4000))
	if wk, err := s.store.LatestReadyWalk(ctx, uid, p.ID); err == nil {
		var d walk.Doc
		if json.Unmarshal(wk.Doc, &d) == nil {
			fmt.Fprintf(&b, "\nWhat the walkthrough found:\n%s\n%s\nBefore: %s\nAfter: %s\n", d.TLDR, clip(d.Overview, 3000), clip(d.Before, 800), clip(d.After, 800))
		}
	}
	actx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	text, err := cl.Complete(actx, slackVoice+languageNote(st.WalkLanguage), b.String(), false)
	text = strings.Trim(strings.TrimSpace(text), "\"“”")
	if err != nil || text == "" {
		out["note"] = "Pocket AI did not answer, so this is the title"
		if err != nil {
			out["note"] = out["note"].(string) + ": " + err.Error()
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	_ = s.store.CachePut(ctx, key, text)
	out["text"] = text
	writeJSON(w, http.StatusOK, out)
}
