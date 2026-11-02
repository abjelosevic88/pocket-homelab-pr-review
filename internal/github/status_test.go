package github

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPullStatus(t *testing.T) {
	gql := `{"data":{"repository":{"pullRequest":{"state":"OPEN","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"BEHIND","reviewDecision":"CHANGES_REQUESTED","totalCommentsCount":9,
"baseRefName":"main","headRefOid":"abc123",
"commits":{"totalCount":4,"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE","contexts":{"nodes":[
 {"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://x/1","checkSuite":{"workflowRun":{"workflow":{"name":"CI"}}}},
 {"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://x/2","checkSuite":null},
 {"__typename":"CheckRun","name":"e2e","status":"IN_PROGRESS","conclusion":null,"detailsUrl":"https://x/3","checkSuite":null},
 {"__typename":"CheckRun","name":"docs","status":"COMPLETED","conclusion":"SKIPPED","detailsUrl":"","checkSuite":null},
 {"__typename":"StatusContext","context":"vercel","state":"SUCCESS","targetUrl":"https://v"}]}}}}]},
"latestOpinionatedReviews":{"nodes":[{"state":"APPROVED","author":{"login":"ann"}},{"state":"CHANGES_REQUESTED","author":{"login":"bob"}}]},
"latestReviews":{"nodes":[{"state":"APPROVED","author":{"login":"ann"}},{"state":"COMMENTED","author":{"login":"bot"}}]},
"reviewThreads":{"totalCount":3,"nodes":[{"isResolved":false,"isOutdated":true},{"isResolved":false,"isOutdated":false},{"isResolved":true,"isOutdated":false}]}}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/graphql":
			_, _ = w.Write([]byte(gql))
		case r.URL.Path == "/repos/o/r/pulls/7/requested_reviewers":
			_, _ = w.Write([]byte(`{"users":[{"login":"cy"}],"teams":[{"name":"core","slug":"core"}]}`))
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/compare/main...abc123"):
			_, _ = w.Write([]byte(`{"behind_by":5,"ahead_by":4}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "t", "", 0)
	st, err := c.PullStatus(t.Context(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	ck := st.Checks
	if !ck.Known || ck.Passed != 2 || ck.Failed != 1 || ck.Running != 1 || ck.Skipped != 1 {
		t.Fatalf("checks: %+v", ck)
	}
	if ck.Items[0].Name != "CI / test" || ck.Items[0].Result != "failed" || ck.Items[1].Result != "running" {
		t.Fatalf("order: %+v", ck.Items)
	}
	if st.Behind != 5 || st.MergeState != "BEHIND" || st.Commits != 4 {
		t.Fatalf("merge: %+v", st)
	}
	if strings.Join(st.Approved, ",") != "ann" || strings.Join(st.ChangesWanted, ",") != "bob" || strings.Join(st.Commented, ",") != "bot" || strings.Join(st.Requested, ",") != "cy,core (team)" {
		t.Fatalf("reviews: %+v", st)
	}
	if st.Threads != 3 || st.Unresolved != 2 || st.Outdated != 1 {
		t.Fatalf("threads: %+v", st)
	}
	if got := New("https://ghe.example.com/api/v3", "t", "", 0).graphqlURL(); got != "https://ghe.example.com/api/graphql" {
		t.Fatal(got)
	}
}

// A token without access to checks still gets the rest, and a warning.
func TestPullStatusPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/graphql" {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"state":"OPEN","mergeable":"CONFLICTING","mergeStateStatus":"DIRTY","reviewDecision":null,
"commits":{"totalCount":1,"nodes":[{"commit":{"statusCheckRollup":null}}]},"latestOpinionatedReviews":{"nodes":[]},"latestReviews":{"nodes":[]},
"reviewThreads":{"totalCount":0,"nodes":[]}}}},
"errors":[{"type":"FORBIDDEN","path":["repository","pullRequest","commits","nodes",0,"commit","statusCheckRollup"],"message":"Resource not accessible by personal access token"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	st, err := New(srv.URL, "t", "", 0).PullStatus(t.Context(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if st.Checks.Known || st.Mergeable != "CONFLICTING" || st.Behind != -1 || len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "statusCheckRollup") {
		t.Fatalf("%+v", st)
	}
}
