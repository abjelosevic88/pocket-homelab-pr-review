package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-pr-review/internal/store"
)

// Pocket Core app links (docs/links.md in pocket-homelab-core), PR Review as the
// target: another Pocket app the person allowed in Core may act here for
// them with a pcx_ token, only within these scopes. Every route not listed
// in linkRoutes stays closed to it (403).

var linkOffers = []core.Offer{
	{Scope: "prs:read", Name: "See your pull request reviews", Description: "See the pull requests waiting for your review, their walkthroughs and the reviews you sent each day — for your journal, dashboards and issue trackers."},
	{Scope: "prs:write", Name: "Add pull requests to review", Description: "Add a pull request to your list and make its walkthrough (an issue tracker linking the PR it made). It never sends a review for you."},
}

type linkRoute struct {
	methods string
	path    *regexp.Regexp
	scopes  string // any of these (space separated); "" = any linked app
}

var linkRoutes = []linkRoute{
	{"GET", regexp.MustCompile(`^/api/v1/auth/me$`), ""},
	{"GET", regexp.MustCompile(`^/api/v1/(dashboard|pulls)$`), "prs:read prs:write"},
	{"GET", regexp.MustCompile(`^/api/v1/(day|summary)/\d{4}-\d{2}-\d{2}$`), "prs:read"},
	{"GET", regexp.MustCompile(`^/api/v1/lookup$`), "prs:read prs:write"},
	{"GET", regexp.MustCompile(`^/api/v1/pulls/\d+$`), "prs:read"},
	{"GET", regexp.MustCompile(`^/api/v1/pulls/\d+/walkthrough(\.md)?$`), "prs:read"},
	{"POST", regexp.MustCompile(`^/api/v1/pulls$`), "prs:write"},
	{"POST", regexp.MustCompile(`^/api/v1/pulls/\d+/walkthrough$`), "prs:write"},
}

var errLinkScope = errors.New("outside the link's scope")

// linkAllows reports whether a link token with these scopes may make this request.
func linkAllows(scopes []string, r *http.Request) bool {
	for _, lr := range linkRoutes {
		if !strings.Contains(" "+lr.methods+" ", " "+r.Method+" ") || !lr.path.MatchString(r.URL.Path) {
			continue
		}
		if lr.scopes == "" {
			return true
		}
		for _, sc := range scopes {
			for _, want := range strings.Fields(lr.scopes) {
				if sc == want {
					return true
				}
			}
		}
	}
	return false
}

// linkUser validates a pcx_ token, checks the route against its scopes and
// maps it to the person it acts for. The second value is the acting app's slug.
func (s *Server) linkUser(ctx context.Context, tok string, r *http.Request) (*store.User, string, error) {
	in, err := s.core.Introspect(ctx, tok)
	if err != nil {
		return nil, "", err
	}
	if !in.Active || !in.Delegated() || in.Audience != "pr-review" || in.User == nil || in.User.Email == "" || in.App == nil || in.App.Slug == "" {
		return nil, "", http.ErrNoCookie
	}
	if !linkAllows(strings.Fields(in.Scope), r) {
		return nil, "", errLinkScope
	}
	u, err := s.coreIdentityUser(ctx, in)
	return u, in.App.Slug, err
}

const linkAppKey ctxKey = 2

// linkApp is the acting app's slug when the request came in on a link token.
func linkApp(ctx context.Context) string {
	a, _ := ctx.Value(linkAppKey).(string)
	return a
}
