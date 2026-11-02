package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.RequestID)
	r.Use(s.logging)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))

	r.Get("/pocket-ask.js", s.core.AskWidget("pr-review"))
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Get("/api/health", s.handleHealth)
	if s.cfg.MetricsEnabled {
		r.Get("/metrics", s.handleMetrics)
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.Timeout(120 * time.Second))
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/setup", s.handleSetup)
		r.Get("/auth/status", s.handleAuthStatus)
		r.Get("/auth/oidc/start", s.handleOIDCStart)
		r.Get("/auth/oidc/callback", s.handleOIDCCallback)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Put("/auth/me", s.handleUpdateMe)

			// the list
			r.Get("/dashboard", s.handleDashboard)
			r.Get("/day/{date}", s.handleDay)
			r.Get("/summary/{date}", s.handleDay)
			r.Get("/pulls", s.handleListPulls)
			r.Post("/pulls", s.handleAddPull)
			r.Get("/lookup", s.handleLookup)
			r.Post("/sync", s.handleSyncNow)
			r.Get("/reviews", s.handleReviews)
			r.Get("/github/repos", s.handleGitHubRepos)
			r.Get("/github/pulls", s.handleGitHubPulls)

			// one pull request
			// for assistants: find, read without diffs, one file's diff, search the diffs
			r.Get("/pulls/find", s.handleFindPulls)
			r.Get("/pulls/{id}/brief", s.handleBrief)
			r.Get("/pulls/{id}/diff", s.handleFileDiff)
			r.Get("/pulls/{id}/grep", s.handleGrep)
			r.Get("/pulls/{id}", s.handleGetPull)
			r.Delete("/pulls/{id}", s.handleDeletePull)
			r.Post("/pulls/{id}/archive", s.handleArchivePull)
			r.Post("/pulls/{id}/walkthrough", s.handleRewalk)
			r.Get("/pulls/{id}/walkthrough", s.handleGetPull)
			r.Get("/pulls/{id}/walkthrough.md", s.handleWalkMarkdown)
			r.Post("/pulls/{id}/walkthrough/post", s.handlePostWalk)
			r.Get("/pulls/{id}/walkthroughs/{wid}/status", s.handleWalkStatus)
			r.Put("/pulls/{id}/viewed", s.handleSetViewed)
			r.Get("/pulls/{id}/threads", s.handleThreads)
			r.Get("/pulls/{id}/status", s.handlePullStatus)
			r.Post("/pulls/{id}/reply", s.handleReply)
			r.Get("/pulls/{id}/comments", s.handleListComments)
			r.Post("/pulls/{id}/comments", s.handleCreateComment)
			r.Put("/pulls/{id}/comments/{cid}", s.handleUpdateComment)
			r.Delete("/pulls/{id}/comments/{cid}", s.handleDeleteComment)
			r.Post("/pulls/{id}/review", s.handleSubmitReview)
			r.Post("/pulls/{id}/ask", s.handleAskPull)

			// a coding agent (Pocket AI)
			r.Get("/agent/options", s.handleAgentOptions)
			r.Get("/ai/active", s.handleAIActive)
			r.Get("/agent/dirs", s.handleAgentDirs)
			r.Get("/pulls/{id}/agent", s.handleAgentThreads)
			r.Get("/pulls/{id}/agent/prompt", s.handleAgentPrompt)
			r.Post("/pulls/{id}/agent", s.handleAgentSend)
			r.Post("/pulls/{id}/chat", s.handleChatStart)

			// AI
			r.Get("/ai/status", s.handleAIStatus)
			r.Get("/ai/models", s.handleAIModels)

			r.Post("/integrations/test", s.handleIntegrationTest)
			r.Post("/notify/test", s.handleNotifyTest)

			r.Get("/users", s.handleListUsers)
			r.Post("/users", s.handleCreateUser)
			r.Put("/users/{id}", s.handleUpdateUser)
			r.Delete("/users/{id}", s.handleDeleteUser)

			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handlePutSettings)
			r.Get("/settings/system", s.handleSystemInfo)

			r.Get("/tokens", s.handleListTokens)
			r.Post("/tokens", s.handleCreateToken)
			r.Delete("/tokens/{id}", s.handleDeleteToken)

			r.Get("/backup", s.handleBackup)
			r.Get("/export.json", s.handleExportJSON)
			r.Get("/export.csv", s.handleExportCSV)
			r.Post("/import", s.handleImportJSON)
		})
	})

	r.NotFound(s.handleSPA)
	return r
}

// ---------- helpers ----------

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.requests.Add(1)
		if ww.Status() >= 500 {
			s.errors.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			return
		}
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "dur", time.Since(start).String(), "ip", r.RemoteAddr)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func (s *Server) fail(w http.ResponseWriter, err error, context string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, store.ErrConflict), errors.Is(err, core.ErrOperationConflict):
		writeErr(w, http.StatusConflict, "already exists")
		return
	}
	s.log.Error(context, "err", err)
	writeErr(w, http.StatusInternalServerError, context+": "+err.Error())
}

func (s *Server) readBody(r *http.Request) ([]byte, error) {
	limit := s.cfg.MaxBodyBytes
	if limit <= 0 {
		limit = 64 << 20
	}
	return io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20))
	return dec.Decode(v)
}

// decodeKeys decodes JSON into v and also reports which top-level keys were
// present, so PUT handlers can tell "not sent" from "sent empty".
func decodeKeys(r *http.Request, v any) (map[string]bool, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(raw))
	for k := range raw {
		keys[k] = true
	}
	return keys, json.Unmarshal(b, v)
}

func chiParam(r *http.Request, name string) string { return chi.URLParam(r, name) }

func idParam(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return n
}

func queryInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func queryBool(r *http.Request, name string) bool {
	v := strings.ToLower(r.URL.Query().Get(name))
	return v == "1" || v == "true" || v == "yes"
}

func validDate(s string) bool {
	if s == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// ---------- health / metrics ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"core_outbox": s.coreOutboxHealth(r.Context()), "status": "ok", "version": Version, "uptime": time.Since(s.started).Round(time.Second).String()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts := s.store.Counts(ctx)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP pocket_pr_review_info Build info.\n# TYPE pocket_pr_review_info gauge\npocket_pr_review_info{version=%q} 1\n", Version)
	fmt.Fprintf(w, "# HELP pocket_pr_review_uptime_seconds Process uptime.\n# TYPE pocket_pr_review_uptime_seconds gauge\npocket_pr_review_uptime_seconds %d\n", int(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# HELP pocket_pr_review_http_requests_total HTTP requests served.\n# TYPE pocket_pr_review_http_requests_total counter\npocket_pr_review_http_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# HELP pocket_pr_review_http_errors_total HTTP 5xx responses.\n# TYPE pocket_pr_review_http_errors_total counter\npocket_pr_review_http_errors_total %d\n", s.errors.Load())
	fmt.Fprintf(w, "# HELP pocket_pr_review_rows Rows per table.\n# TYPE pocket_pr_review_rows gauge\n")
	for k, v := range counts {
		fmt.Fprintf(w, "pocket_pr_review_rows{table=%q} %d\n", k, v)
	}
	fmt.Fprintf(w, "# HELP pocket_pr_review_walkthroughs_total Walkthroughs made since start.\n# TYPE pocket_pr_review_walkthroughs_total counter\npocket_pr_review_walkthroughs_total %d\n", s.walks.Load())
}

// ---------- static / SPA ----------

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.webFS == nil {
		http.Error(w, "web UI not built (run `make web`)", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if f, err := s.webFS.Open(path); err == nil {
		f.Close()
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.FileServerFS(s.webFS).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	r.URL.Path = "/"
	http.FileServerFS(s.webFS).ServeHTTP(w, r)
}
