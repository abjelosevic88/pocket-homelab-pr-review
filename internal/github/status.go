package github

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Status is where a pull request stands on GitHub now: its checks, whether
// it can merge, its reviews and its open conversations.
type Status struct {
	State          string   `json:"state"` // OPEN | CLOSED | MERGED
	Draft          bool     `json:"draft"`
	Mergeable      string   `json:"mergeable"`   // MERGEABLE | CONFLICTING | UNKNOWN
	MergeState     string   `json:"merge_state"` // CLEAN | DIRTY | BLOCKED | BEHIND | UNSTABLE | HAS_HOOKS | DRAFT | UNKNOWN
	Behind         int      `json:"behind"`      // commits of the base branch the head lacks (-1 = not known)
	Commits        int      `json:"commits"`
	ReviewDecision string   `json:"review_decision"` // APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED | ""
	Approved       []string `json:"approved"`
	ChangesWanted  []string `json:"changes_requested"`
	Commented      []string `json:"commented"`
	Requested      []string `json:"requested"` // still asked to review: people and teams
	Threads        int      `json:"threads"`
	Unresolved     int      `json:"unresolved"`
	Outdated       int      `json:"unresolved_outdated"` // of the unresolved, on lines that changed since
	Comments       int      `json:"comments"`            // every comment, conversation and reviews included
	Checks         Checks   `json:"checks"`
	Warnings       []string `json:"warnings"` // what the token could not read
}

// Checks are the head commit's CI: check runs and commit statuses together.
type Checks struct {
	Known   bool    `json:"known"`
	State   string  `json:"state"` // SUCCESS | FAILURE | PENDING | ERROR | EXPECTED | "" (none)
	Passed  int     `json:"passed"`
	Failed  int     `json:"failed"`
	Running int     `json:"running"`
	Skipped int     `json:"skipped"`
	Items   []Check `json:"items"`
}

// Check is one CI job or commit status.
type Check struct {
	Name   string `json:"name"`
	Result string `json:"result"` // passed | failed | running | skipped
	Detail string `json:"detail"` // GitHub's own word: SUCCESS, TIMED_OUT, IN_PROGRESS…
	URL    string `json:"url"`
}

const statusQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){
state isDraft mergeable mergeStateStatus reviewDecision totalCommentsCount
commits(last:1){totalCount nodes{commit{statusCheckRollup{state contexts(first:100){nodes{__typename
... on CheckRun{name status conclusion detailsUrl checkSuite{workflowRun{workflow{name}}}}
... on StatusContext{context state targetUrl}}}}}}}
latestOpinionatedReviews(first:50){nodes{state author{login}}}
latestReviews(first:50){nodes{state author{login}}}
reviewThreads(first:100){totalCount nodes{isResolved isOutdated}}
baseRefName headRefOid}}}`

// PullStatus reads where a pull request stands. What the token may not read
// (often the checks) is left out and said in Warnings.
func (c *Client) PullStatus(ctx context.Context, repo string, n int) (*Status, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("repository %q is not owner/name", repo)
	}
	var res struct {
		Data struct {
			Repository *struct {
				PullRequest *struct {
					State                   string `json:"state"`
					IsDraft                 bool   `json:"isDraft"`
					Mergeable               string `json:"mergeable"`
					MergeStateStatus        string `json:"mergeStateStatus"`
					ReviewDecision          string `json:"reviewDecision"`
					TotalCommentsCount      int    `json:"totalCommentsCount"`
					BaseRefName, HeadRefOid string
					Commits                 struct {
						TotalCount int `json:"totalCount"`
						Nodes      []struct {
							Commit struct {
								StatusCheckRollup *struct {
									State    string `json:"state"`
									Contexts struct {
										Nodes []struct {
											Typename   string `json:"__typename"`
											Name       string `json:"name"`
											Status     string `json:"status"`
											Conclusion string `json:"conclusion"`
											DetailsURL string `json:"detailsUrl"`
											CheckSuite *struct {
												WorkflowRun *struct {
													Workflow struct {
														Name string `json:"name"`
													} `json:"workflow"`
												} `json:"workflowRun"`
											} `json:"checkSuite"`
											Context   string `json:"context"`
											State     string `json:"state"`
											TargetURL string `json:"targetUrl"`
										} `json:"nodes"`
									} `json:"contexts"`
								} `json:"statusCheckRollup"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
					LatestOpinionatedReviews reviewNodes `json:"latestOpinionatedReviews"`
					LatestReviews            reviewNodes `json:"latestReviews"`
					ReviewThreads            struct {
						TotalCount int `json:"totalCount"`
						Nodes      []struct {
							IsResolved bool `json:"isResolved"`
							IsOutdated bool `json:"isOutdated"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Path    []any  `json:"path"`
			Type    string `json:"type"`
		} `json:"errors"`
	}
	body := map[string]any{"query": statusQuery, "variables": map[string]any{"owner": owner, "name": name, "number": n}}
	if _, err := c.do(ctx, http.MethodPost, c.graphqlURL(), body, &res); err != nil {
		return nil, err
	}
	st := &Status{Behind: -1, Approved: []string{}, ChangesWanted: []string{}, Commented: []string{}, Requested: []string{}, Warnings: []string{}, Checks: Checks{Items: []Check{}}}
	for _, e := range res.Errors {
		where := ""
		for _, p := range e.Path {
			if s, ok := p.(string); ok {
				where = s
			}
		}
		msg := e.Message
		if where != "" {
			msg = where + ": " + msg
		}
		st.Warnings = append(st.Warnings, msg)
	}
	if res.Data.Repository == nil || res.Data.Repository.PullRequest == nil {
		if len(st.Warnings) > 0 {
			return nil, fmt.Errorf("GitHub: %s", st.Warnings[0])
		}
		return nil, fmt.Errorf("GitHub has no pull request %s#%d, or the token cannot see it", repo, n)
	}
	pr := res.Data.Repository.PullRequest
	st.State, st.Draft, st.Mergeable, st.MergeState, st.ReviewDecision = pr.State, pr.IsDraft, pr.Mergeable, pr.MergeStateStatus, pr.ReviewDecision
	st.Commits, st.Comments, st.Threads = pr.Commits.TotalCount, pr.TotalCommentsCount, pr.ReviewThreads.TotalCount
	for _, t := range pr.ReviewThreads.Nodes {
		if !t.IsResolved {
			st.Unresolved++
			if t.IsOutdated {
				st.Outdated++
			}
		}
	}
	// the opinionated list carries approvals and change requests; the plain one adds comment-only reviews
	seen := map[string]bool{}
	for _, r := range pr.LatestOpinionatedReviews.Nodes {
		who := r.Author.Login
		seen[who] = true
		switch r.State {
		case "APPROVED":
			st.Approved = append(st.Approved, who)
		case "CHANGES_REQUESTED":
			st.ChangesWanted = append(st.ChangesWanted, who)
		}
	}
	for _, r := range pr.LatestReviews.Nodes {
		if who := r.Author.Login; !seen[who] && r.State == "COMMENTED" {
			seen[who] = true
			st.Commented = append(st.Commented, who)
		}
	}
	// who is still asked: REST, since team names need read:org in GraphQL
	var rq struct {
		Users []struct {
			Login string `json:"login"`
		} `json:"users"`
		Teams []struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"teams"`
	}
	if _, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", repo, n), nil, &rq); err == nil {
		for _, u := range rq.Users {
			st.Requested = append(st.Requested, u.Login)
		}
		for _, t := range rq.Teams {
			name := t.Name
			if name == "" {
				name = t.Slug
			}
			st.Requested = append(st.Requested, name+" (team)")
		}
	}
	if len(pr.Commits.Nodes) > 0 {
		if roll := pr.Commits.Nodes[0].Commit.StatusCheckRollup; roll != nil {
			st.Checks.Known, st.Checks.State = true, roll.State
			for _, x := range roll.Contexts.Nodes {
				ck := Check{}
				if x.Typename == "CheckRun" {
					ck.Name, ck.URL = x.Name, x.DetailsURL
					if x.CheckSuite != nil && x.CheckSuite.WorkflowRun != nil && x.CheckSuite.WorkflowRun.Workflow.Name != "" {
						ck.Name = x.CheckSuite.WorkflowRun.Workflow.Name + " / " + x.Name
					}
					if x.Status != "COMPLETED" {
						ck.Result, ck.Detail = "running", x.Status
					} else {
						ck.Detail = x.Conclusion
						switch x.Conclusion {
						case "SUCCESS":
							ck.Result = "passed"
						case "SKIPPED", "NEUTRAL", "STALE":
							ck.Result = "skipped"
						default: // FAILURE, TIMED_OUT, CANCELLED, ACTION_REQUIRED, STARTUP_FAILURE
							ck.Result = "failed"
						}
					}
				} else {
					ck.Name, ck.URL, ck.Detail = x.Context, x.TargetURL, x.State
					switch x.State {
					case "SUCCESS":
						ck.Result = "passed"
					case "PENDING", "EXPECTED":
						ck.Result = "running"
					default:
						ck.Result = "failed"
					}
				}
				switch ck.Result {
				case "passed":
					st.Checks.Passed++
				case "failed":
					st.Checks.Failed++
				case "running":
					st.Checks.Running++
				default:
					st.Checks.Skipped++
				}
				st.Checks.Items = append(st.Checks.Items, ck)
			}
			// failing first, then running, then the rest; by name within each
			rank := map[string]int{"failed": 0, "running": 1, "passed": 2, "skipped": 3}
			sort.SliceStable(st.Checks.Items, func(i, j int) bool {
				a, b := st.Checks.Items[i], st.Checks.Items[j]
				if rank[a.Result] != rank[b.Result] {
					return rank[a.Result] < rank[b.Result]
				}
				return a.Name < b.Name
			})
		} else if len(st.Warnings) == 0 {
			st.Checks.Known = true // no CI reported on this commit
		}
	}
	// how far the branch is behind its base (Contents: read is enough)
	if pr.BaseRefName != "" && pr.HeadRefOid != "" {
		var cmp struct {
			BehindBy int `json:"behind_by"`
		}
		if _, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/compare/%s...%s", repo, pr.BaseRefName, pr.HeadRefOid), nil, &cmp); err == nil {
			st.Behind = cmp.BehindBy
		}
	}
	return st, nil
}

type reviewNodes struct {
	Nodes []struct {
		State  string `json:"state"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
	} `json:"nodes"`
}

// graphqlURL is the GraphQL endpoint next to the REST base: api.github.com/graphql,
// or host/api/graphql on GitHub Enterprise (REST at host/api/v3).
func (c *Client) graphqlURL() string {
	if strings.HasSuffix(c.Base, "/api/v3") {
		return strings.TrimSuffix(c.Base, "/v3") + "/graphql"
	}
	return c.Base + "/graphql"
}
