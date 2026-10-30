package store

import "encoding/json"

// User is an account on this box.
type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // admin | member
	CreatedAt    string `json:"created_at"`
}

// APIToken is a personal access token (hash stored, plaintext shown once).
type APIToken struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
}

// Why a pull request is on someone's list.
const (
	ReasonRequested = "requested" // GitHub asks them to review it
	ReasonAuthored  = "authored"  // they opened it
	ReasonReviewed  = "reviewed"  // they reviewed it before
	ReasonAdded     = "added"     // they added it by hand (or another app did)
)

// Pull is a pull request someone follows here.
type Pull struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"-"`
	Repo         string `json:"repo"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	URL          string `json:"url"`
	Author       string `json:"author"`
	AuthorAvatar string `json:"author_avatar"`
	State        string `json:"state"`
	Draft        bool   `json:"draft"`
	BaseRef      string `json:"base_ref"`
	HeadRef      string `json:"head_ref"`
	BaseSHA      string `json:"base_sha"`
	HeadSHA      string `json:"head_sha"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changed_files"`
	Commits      int    `json:"commits"`
	Reason       string `json:"reason"`
	MyReview     string `json:"my_review"`
	ReviewedSHA  string `json:"reviewed_sha"`
	Source       string `json:"source"`
	Archived     bool   `json:"archived"`
	GHUpdatedAt  string `json:"gh_updated_at"`
	SyncedAt     string `json:"synced_at"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`

	// Filled in for lists: the newest walkthrough and how far I got.
	Walk *WalkBrief `json:"walkthrough,omitempty"`
}

// Ref is "owner/name#12".
func (p *Pull) Ref() string { return p.Repo + "#" + itoa(p.Number) }

// WalkBrief is a walkthrough as a list shows it.
type WalkBrief struct {
	ID       int64  `json:"id"`
	Status   string `json:"status"`
	Progress string `json:"progress"`
	HeadSHA  string `json:"head_sha"`
	Steps    int    `json:"steps"`
	Units    int    `json:"units"`
	Viewed   int    `json:"viewed"` // units I have looked at
	Stale    bool   `json:"stale"`  // the PR has newer commits
}

// Walkthrough statuses.
const (
	WalkQueued  = "queued"
	WalkWorking = "working"
	WalkReady   = "ready"
	WalkFailed  = "failed"
)

// Walkthrough is one pull request at one head commit, told in steps.
type Walkthrough struct {
	ID         int64           `json:"id"`
	UserID     int64           `json:"-"`
	PullID     int64           `json:"pull_id"`
	HeadSHA    string          `json:"head_sha"`
	BaseSHA    string          `json:"base_sha"`
	Status     string          `json:"status"`
	Progress   string          `json:"progress"`
	Error      string          `json:"error"`
	AI         string          `json:"ai"`
	Doc        json.RawMessage `json:"doc"`
	Timeline   json.RawMessage `json:"timeline"`
	CreatedAt  string          `json:"created_at"`
	FinishedAt string          `json:"finished_at"`
}

// WFile is one changed file of a walkthrough.
type WFile struct {
	ID            int64  `json:"id"`
	WalkthroughID int64  `json:"-"`
	Path          string `json:"path"`
	OldPath       string `json:"old_path"`
	Status        string `json:"status"`
	Additions     int    `json:"additions"`
	Deletions     int    `json:"deletions"`
	Layer         string `json:"layer"`
	Summary       string `json:"summary"`
	Patch         string `json:"patch"`
	Binary        bool   `json:"binary"`
	Truncated     bool   `json:"truncated"`
	Sort          int    `json:"sort"`
}

// Viewed is a hunk someone looked at.
type Viewed struct {
	Path     string `json:"path"`
	HunkHash string `json:"hunk_hash"`
	ViewedAt string `json:"viewed_at"`
}

// Comment is one of my review comments.
type Comment struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"-"`
	PullID    int64  `json:"pull_id"`
	CommitSHA string `json:"commit_sha"`
	Path      string `json:"path"`
	Side      string `json:"side"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line"`
	Step      string `json:"step"`
	Body      string `json:"body"`
	State     string `json:"state"`
	ReviewID  int64  `json:"review_id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Review is a review I sent to GitHub from here.
type Review struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"-"`
	PullID    int64  `json:"pull_id"`
	Event     string `json:"event"`
	Body      string `json:"body"`
	CommitSHA string `json:"commit_sha"`
	Comments  int    `json:"comments"`
	GHID      int64  `json:"gh_id"`
	GHURL     string `json:"gh_url"`
	Day       string `json:"day"`
	CreatedAt string `json:"created_at"`

	Repo   string `json:"repo,omitempty"`
	Number int    `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
}

// Settings are box-wide (title, setup flag) plus one person's own.
type Settings struct {
	Title         string `json:"title"`
	SetupComplete bool   `json:"setup_complete"`

	DateFormat string `json:"date_format"`
	TimeFormat string `json:"time_format"`
	WeekStart  int    `json:"week_start"` // 1 = Monday, 7 = Sunday

	// GitHub: the person's own token (never a box-wide one).
	GitHubToken  string `json:"github_token"`
	GitHubAPIURL string `json:"github_api_url"` // "" = https://api.github.com (GitHub Enterprise: https://host/api/v3)
	GitHubLogin  string `json:"github_login"`   // who the token belongs to (filled in on save / test)

	// The list.
	SyncRequested bool   `json:"sync_requested"` // PRs that ask for my review
	SyncAuthored  bool   `json:"sync_authored"`  // my own open PRs
	SyncReviewed  bool   `json:"sync_reviewed"`  // open PRs I reviewed before
	SyncRepos     string `json:"sync_repos"`     // only these repos (owner/name, space separated); "" = all
	ExcludeDrafts bool   `json:"exclude_drafts"`

	// Walkthroughs.
	AutoWalk     string `json:"auto_walk"`     // requested | all | off: make one by itself
	WalkLanguage string `json:"walk_language"` // "" = English
	WalkHints    string `json:"walk_hints"`    // the person's own hints for the order (any repo)
	OpenView     string `json:"open_view"`     // steps | built | files: what a PR opens on
	DiffWrap     bool   `json:"diff_wrap"`     // wrap long lines

	// Notifications.
	NotifyURL       string `json:"notify_url"`
	NotifyFormat    string `json:"notify_format"` // apprise | ntfy | json
	NotifyRequested bool   `json:"notify_requested"`
	NotifyReady     bool   `json:"notify_ready"`

	// Pocket AI: send review comments to a coding agent.
	AIAgentMode      string `json:"ai_mode"`
	AIAgentURL       string `json:"ai_url"`
	AIAgentToken     string `json:"ai_token"`
	AIAgentPublicURL string `json:"ai_public_url"`
	RepoFolders      string `json:"repo_folders"` // JSON {"owner/name":{"host":0,"cwd":"/path"}}

	// AI for the walkthrough: Pocket AI through Core, or an OpenAI-compatible
	// API with the person's own key.
	AIProvider    string `json:"ai_provider"` // auto | pocket | openai
	AIPocketModel string `json:"ai_pocket_model"`
	OpenAIKey     string `json:"openai_api_key"`
	OpenAIModel   string `json:"openai_model"`
	OpenAIBaseURL string `json:"openai_base_url"`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
