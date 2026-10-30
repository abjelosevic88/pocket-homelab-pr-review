package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/ai"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/github"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/pocket"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// ---- settings ----

func mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "••••"
	}
	return s[:4] + "••••" + s[len(s)-4:]
}

// publicSettings is what one person sees: their settings with the secrets
// masked and, per Pocket app, the mode in effect (<app>_mode), whether it
// works and how (<app>_via: core | direct | "") and why not (<app>_detail).
func (s *Server) publicSettings(ctx context.Context, u *store.User, st store.Settings) map[string]any {
	out := map[string]any{
		"core_configured": s.core.Configured(), "core_registered": s.coreStatus()["registered"],
		"title": st.Title, "date_format": st.DateFormat, "time_format": st.TimeFormat, "week_start": st.WeekStart,
		"github_token": mask(st.GitHubToken), "github_api_url": st.GitHubAPIURL, "github_login": st.GitHubLogin, "github_configured": st.GitHubToken != "",
		"sync_requested": st.SyncRequested, "sync_authored": st.SyncAuthored, "sync_reviewed": st.SyncReviewed, "sync_repos": st.SyncRepos,
		"exclude_drafts": st.ExcludeDrafts, "auto_walk": st.AutoWalk, "walk_language": st.WalkLanguage, "walk_hints": st.WalkHints,
		"open_view": st.OpenView, "diff_wrap": st.DiffWrap,
		"notify_requested": st.NotifyRequested, "notify_ready": st.NotifyReady,
		"notify_url": st.NotifyURL, "notify_format": st.NotifyFormat, "notify_configured": st.NotifyURL != "",
		"ai_url": st.AIAgentURL, "ai_token": mask(st.AIAgentToken), "ai_public_url": st.AIAgentPublicURL,
		"ai_provider": st.AIProvider, "ai_pocket_model": st.AIPocketModel, "openai_api_key": mask(st.OpenAIKey), "openai_model": st.OpenAIModel, "openai_base_url": st.OpenAIBaseURL,
		"setup_complete": st.SetupComplete, "base_url": s.cfg.BaseURL, "demo": s.cfg.DemoData,
	}
	for _, a := range pocketApps {
		mode, via, detail := s.connStatus(ctx, u, st, a)
		out[a.slug+"_mode"], out[a.slug+"_via"], out[a.slug+"_detail"] = mode, via, detail
		out[a.slug+"_configured"] = via != ""
		_, _, _, public := connSettings(st, a)
		if public == "" && via != "" {
			public = s.resolveSibling(ctx, a.slug)
		}
		out[a.slug+"_open_url"] = public
	}
	return out
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetSettings(r.Context(), actorID(r.Context()))
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, s.publicSettings(r.Context(), userFrom(r.Context()), st))
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)?$`)

// handlePutSettings saves the caller's personal settings. The box-wide title
// is only taken from an admin.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	me := userFrom(r.Context())
	isAdmin := me.Role == "admin"
	cur, err := s.store.GetSettings(r.Context(), me.ID)
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	var in store.Settings
	keys, err := decodeKeys(r, &in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	bad := func(msg string) { writeErr(w, http.StatusBadRequest, msg) }
	if keys["title"] && isAdmin {
		cur.Title = strings.TrimSpace(in.Title)
		if cur.Title == "" {
			cur.Title = store.DefaultTitle
		}
	}
	if keys["date_format"] {
		cur.DateFormat = in.DateFormat
	}
	if keys["time_format"] {
		cur.TimeFormat = in.TimeFormat
	}
	if keys["week_start"] && (in.WeekStart == 1 || in.WeekStart == 7 || in.WeekStart == 6) {
		cur.WeekStart = in.WeekStart
	}
	secret := func(k string, dst *string, v string) bool {
		if keys[k] && !strings.Contains(v, "••") {
			*dst = strings.TrimSpace(v)
			return true
		}
		return false
	}
	url := func(k string, dst *string, v string) {
		if keys[k] {
			*dst = strings.TrimRight(strings.TrimSpace(v), "/")
		}
	}
	text := func(k string, dst *string, v string) {
		if keys[k] {
			*dst = strings.TrimSpace(v)
		}
	}
	boolIn := func(k string, dst *bool, v bool) {
		if keys[k] {
			*dst = v
		}
	}
	oldToken, oldAPI := cur.GitHubToken, cur.GitHubAPIURL
	secret("github_token", &cur.GitHubToken, in.GitHubToken)
	url("github_api_url", &cur.GitHubAPIURL, in.GitHubAPIURL)
	if cur.GitHubToken != oldToken || cur.GitHubAPIURL != oldAPI {
		cur.GitHubLogin = "" // learnt again on the next sync or test
		if cur.GitHubToken != "" {
			if gh, err := s.ghFor(cur); err == nil {
				c, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				if me, err := gh.Me(c); err == nil {
					cur.GitHubLogin = me.Login
				}
				cancel()
			}
		}
	}
	boolIn("sync_requested", &cur.SyncRequested, in.SyncRequested)
	boolIn("sync_authored", &cur.SyncAuthored, in.SyncAuthored)
	boolIn("sync_reviewed", &cur.SyncReviewed, in.SyncReviewed)
	boolIn("exclude_drafts", &cur.ExcludeDrafts, in.ExcludeDrafts)
	if keys["sync_repos"] {
		var keep []string
		for _, f := range strings.FieldsFunc(in.SyncRepos, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' || r == '\t' }) {
			if !repoRe.MatchString(f) {
				bad("“" + f + "” is not a repository (owner/name) or an owner")
				return
			}
			keep = append(keep, strings.ToLower(f))
		}
		cur.SyncRepos = strings.Join(keep, " ")
	}
	if keys["auto_walk"] {
		if in.AutoWalk != "requested" && in.AutoWalk != "all" && in.AutoWalk != "off" {
			bad("make walkthroughs for requested, all or off")
			return
		}
		cur.AutoWalk = in.AutoWalk
	}
	if keys["open_view"] {
		if in.OpenView != "steps" && in.OpenView != "built" && in.OpenView != "files" {
			bad("a pull request opens on steps, built or files")
			return
		}
		cur.OpenView = in.OpenView
	}
	text("walk_language", &cur.WalkLanguage, in.WalkLanguage)
	if keys["walk_hints"] {
		if len(in.WalkHints) > 8000 {
			bad("the hints are longer than 8,000 characters")
			return
		}
		cur.WalkHints = strings.TrimSpace(in.WalkHints)
	}
	boolIn("diff_wrap", &cur.DiffWrap, in.DiffWrap)
	boolIn("notify_requested", &cur.NotifyRequested, in.NotifyRequested)
	boolIn("notify_ready", &cur.NotifyReady, in.NotifyReady)
	text("notify_url", &cur.NotifyURL, in.NotifyURL)
	if keys["notify_format"] && in.NotifyFormat != "" {
		cur.NotifyFormat = in.NotifyFormat
	}
	url("ai_url", &cur.AIAgentURL, in.AIAgentURL)
	secret("ai_token", &cur.AIAgentToken, in.AIAgentToken)
	url("ai_public_url", &cur.AIAgentPublicURL, in.AIAgentPublicURL)
	if keys["ai_provider"] {
		if !validAIProvider(in.AIProvider) {
			bad("AI is automatic, Pocket AI or OpenAI")
			return
		}
		cur.AIProvider = in.AIProvider
	}
	if keys["ai_pocket_model"] {
		if !validPocketModel(in.AIPocketModel) {
			bad("unknown model")
			return
		}
		cur.AIPocketModel = in.AIPocketModel
	}
	secret("openai_api_key", &cur.OpenAIKey, in.OpenAIKey)
	text("openai_model", &cur.OpenAIModel, in.OpenAIModel)
	url("openai_base_url", &cur.OpenAIBaseURL, in.OpenAIBaseURL)
	if keys["ai_mode"] && (in.AIAgentMode == modeCore || in.AIAgentMode == modeDirect || in.AIAgentMode == modeOff) {
		cur.AIAgentMode = in.AIAgentMode
	}
	if err := s.store.SaveUserSettings(r.Context(), me.ID, cur); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	if isAdmin && keys["title"] {
		if err := s.store.SaveBoxSettings(r.Context(), cur); err != nil {
			s.fail(w, err, "save settings")
			return
		}
	}
	for _, k := range modeKeys {
		if keys[k] {
			s.reportModesSoon()
			break
		}
	}
	if cur.GitHubToken != oldToken && cur.GitHubToken != "" && !s.cfg.DemoData {
		go func(uid int64) {
			c, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			_, _ = s.syncUser(c, uid)
		}(me.ID)
	}
	writeJSON(w, http.StatusOK, s.publicSettings(r.Context(), me, cur))
}

// handleIntegrationTest checks a connection with the caller's own saved
// settings (or the URL/key typed into the form).
func (s *Server) handleIntegrationTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Which  string `json:"which"`
		URL    string `json:"url"`
		Key    string `json:"key"`
		Format string `json:"format"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	uid := actorID(r.Context())
	st, _ := s.store.GetSettings(r.Context(), uid)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	keyOr := func(saved string) string {
		if strings.Contains(in.Key, "••") || in.Key == "" {
			return saved
		}
		return in.Key
	}
	urlOr := func(saved string) string {
		if strings.TrimSpace(in.URL) == "" {
			return saved
		}
		return strings.TrimRight(strings.TrimSpace(in.URL), "/")
	}
	var msg string
	var err error
	switch {
	case in.Which == "github":
		t := st
		t.GitHubToken, t.GitHubAPIURL = keyOr(st.GitHubToken), urlOr(st.GitHubAPIURL)
		var gh *github.Client
		if gh, err = s.ghFor(t); err == nil {
			var me *github.User
			if me, err = gh.Me(ctx); err == nil {
				msg = "Signed in to GitHub as " + me.Login
				if me.Name != "" {
					msg += " (" + me.Name + ")"
				}
				var found []github.SearchItem
				if found, err = gh.Search(ctx, "is:pr is:open review-requested:@me archived:false", 100); err == nil {
					msg += fmt.Sprintf("; %d open pull request(s) wait for your review", len(found))
				}
			}
		}
	case in.Which == "ai":
		a := appAI
		mode, savedURL, savedTok, _ := connSettings(st, a)
		typed := in.Key != "" && !strings.Contains(in.Key, "••")
		var conn *siblingConn
		if tok := keyOr(savedTok); tok != "" && (typed || s.connMode(mode, savedTok) == modeDirect) {
			base := urlOr(savedURL)
			if base == "" {
				base = s.resolveSibling(ctx, a.slug)
			}
			conn = &siblingConn{client: pocket.New(base, tok, 15*time.Second), via: modeDirect}
		} else {
			conn, err = s.clients(ctx, uid).conn(ctx, a, scopeThreadsWrite)
		}
		if err == nil {
			how := "direct connection"
			if conn.via == modeCore {
				how = "through Pocket Core"
			}
			var v string
			if v, err = conn.client.Health(ctx); err == nil {
				var o *pocket.AgentOptions
				if o, err = conn.client.AgentOptions(ctx); err == nil {
					msg = fmt.Sprintf("Pocket AI %s, %s, %d agent(s), %d other machine(s)", v, how, len(o.Providers), len(o.Hosts))
				}
			}
		}
	case in.Which == "openai":
		c := ai.New(urlOr(st.OpenAIBaseURL), keyOr(st.OpenAIKey), st.OpenAIModel, 20*time.Second)
		var ids []string
		if ids, err = c.Models(ctx); err == nil {
			msg = fmt.Sprintf("The key works (%d models)", len(ids))
		}
	case in.Which == "notify":
		f := in.Format
		if f == "" {
			f = st.NotifyFormat
		}
		n := notify.New(urlOr(st.NotifyURL), f)
		err = n.Send(ctx, notify.Message{Title: "Pocket PR Review", Body: "Notifications work. Pull requests that ask for your review will arrive here.", Priority: "info", Tags: []string{"eyes"}})
		msg = "Test message sent via " + n.Format
	default:
		writeErr(w, http.StatusBadRequest, "unknown integration")
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := s.store.GetSettings(ctx, actorID(ctx))
	n := notify.New(st.NotifyURL, st.NotifyFormat)
	if err := n.Send(ctx, notify.Message{Title: "Pocket PR Review", Body: "Notifications work.", Priority: "info", Tags: []string{"eyes"}}); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- system / backup / export ----

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	var size int64
	if fi, err := os.Stat(s.cfg.DBPath); err == nil {
		size = fi.Size()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": Version, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"core":      s.coreStatus(),
		"auth_mode": s.cfg.AuthMode, "data_dir": s.cfg.DataDir, "db_path": s.cfg.DBPath, "db_bytes": size,
		"uptime": time.Since(s.started).Round(time.Second).String(), "timezone": time.Now().Location().String(),
		"counts": s.store.CountsFor(r.Context(), actorID(r.Context())), "walkthroughs_made": s.walks.Load(),
	})
}

// handleBackup downloads the whole database, an admin-only box operation.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	dir := filepath.Join(s.cfg.DataDir, "backups")
	name := fmt.Sprintf("pocket-pr-review-%s.db", time.Now().Format("20060102-150405"))
	dst := filepath.Join(dir, name)
	if err := s.store.Backup(r.Context(), dst); err != nil {
		s.fail(w, err, "backup")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, dst)
}
