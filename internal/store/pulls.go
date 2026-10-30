package store

import (
	"context"
	"database/sql"
	"strings"
)

const pullCols = `id, user_id, repo, number, title, body, url, author, author_avatar, state, draft, base_ref, head_ref, base_sha, head_sha,
	additions, deletions, changed_files, commits, reason, my_review, reviewed_sha, source, archived, gh_updated_at, synced_at, created_at, updated_at`

func scanPull(row interface{ Scan(...any) error }) (*Pull, error) {
	var p Pull
	var draft, archived int
	if err := row.Scan(&p.ID, &p.UserID, &p.Repo, &p.Number, &p.Title, &p.Body, &p.URL, &p.Author, &p.AuthorAvatar, &p.State, &draft,
		&p.BaseRef, &p.HeadRef, &p.BaseSHA, &p.HeadSHA, &p.Additions, &p.Deletions, &p.ChangedFiles, &p.Commits, &p.Reason,
		&p.MyReview, &p.ReviewedSHA, &p.Source, &archived, &p.GHUpdatedAt, &p.SyncedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	p.Draft, p.Archived = draft == 1, archived == 1
	return &p, nil
}

// NormRepo is "owner/name" in lower case (GitHub names are case-insensitive).
func NormRepo(repo string) string { return strings.ToLower(strings.Trim(strings.TrimSpace(repo), "/")) }

// SavePull inserts a pull request or updates the one with the same repo and
// number (for that person). p.ID is set.
func (s *Store) SavePull(ctx context.Context, p *Pull) error {
	p.Repo = NormRepo(p.Repo)
	if p.Source == "" {
		p.Source = "app"
	}
	if p.Reason == "" {
		p.Reason = ReasonAdded
	}
	now := Now()
	err := s.DB.QueryRowContext(ctx, `INSERT INTO pulls (user_id, repo, number, title, body, url, author, author_avatar, state, draft, base_ref, head_ref,
		base_sha, head_sha, additions, deletions, changed_files, commits, reason, my_review, reviewed_sha, source, archived, gh_updated_at, synced_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id, repo, number) DO UPDATE SET title = excluded.title, body = excluded.body, url = excluded.url, author = excluded.author,
		author_avatar = excluded.author_avatar, state = excluded.state, draft = excluded.draft, base_ref = excluded.base_ref, head_ref = excluded.head_ref,
		base_sha = excluded.base_sha, head_sha = excluded.head_sha, additions = excluded.additions, deletions = excluded.deletions,
		changed_files = excluded.changed_files, commits = excluded.commits, reason = excluded.reason, my_review = excluded.my_review,
		reviewed_sha = excluded.reviewed_sha, archived = excluded.archived, gh_updated_at = excluded.gh_updated_at, synced_at = excluded.synced_at,
		updated_at = excluded.updated_at
		RETURNING id, created_at`,
		p.UserID, p.Repo, p.Number, p.Title, p.Body, p.URL, p.Author, p.AuthorAvatar, p.State, boolInt(p.Draft), p.BaseRef, p.HeadRef,
		p.BaseSHA, p.HeadSHA, p.Additions, p.Deletions, p.ChangedFiles, p.Commits, p.Reason, p.MyReview, p.ReviewedSHA, p.Source,
		boolInt(p.Archived), p.GHUpdatedAt, p.SyncedAt, now).Scan(&p.ID, &p.CreatedAt)
	p.UpdatedAt = now
	return err
}

// GetPull loads one of a person's pull requests.
func (s *Store) GetPull(ctx context.Context, uid, id int64) (*Pull, error) {
	return scanPull(s.DB.QueryRowContext(ctx, `SELECT `+pullCols+` FROM pulls WHERE user_id = ? AND id = ?`, uid, id))
}

// GetPullByRef finds a person's pull request by repo and number.
func (s *Store) GetPullByRef(ctx context.Context, uid int64, repo string, number int) (*Pull, error) {
	return scanPull(s.DB.QueryRowContext(ctx, `SELECT `+pullCols+` FROM pulls WHERE user_id = ? AND repo = ? AND number = ?`, uid, NormRepo(repo), number))
}

// PullFilter narrows ListPulls.
type PullFilter struct {
	Archived bool   // the ones taken off the list instead
	State    string // "" = open; closed | merged | all
	Reason   string
	Repo     string
	Limit    int
}

// ListPulls lists a person's pull requests, newest activity first.
func (s *Store) ListPulls(ctx context.Context, uid int64, f PullFilter) ([]*Pull, error) {
	q := `SELECT ` + pullCols + ` FROM pulls WHERE user_id = ? AND archived = ?`
	args := []any{uid, boolInt(f.Archived)}
	switch f.State {
	case "", "open":
		q += ` AND state = 'open'`
	case "all":
	case "done":
		q += ` AND state <> 'open'`
	default:
		q += ` AND state = ?`
		args = append(args, f.State)
	}
	if f.Reason != "" {
		q += ` AND reason = ?`
		args = append(args, f.Reason)
	}
	if f.Repo != "" {
		q += ` AND repo = ?`
		args = append(args, NormRepo(f.Repo))
	}
	q += ` ORDER BY CASE WHEN gh_updated_at = '' THEN updated_at ELSE gh_updated_at END DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Pull{}
	for rows.Next() {
		p, err := scanPull(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SyncedPulls are a person's open pull requests that came from the sync,
// keyed by "repo#number" (to notice the ones that left the list).
func (s *Store) SyncedPulls(ctx context.Context, uid int64) (map[string]*Pull, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+pullCols+` FROM pulls WHERE user_id = ? AND state = 'open'`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Pull{}
	for rows.Next() {
		p, err := scanPull(rows)
		if err != nil {
			return nil, err
		}
		out[p.Ref()] = p
	}
	return out, rows.Err()
}

// SetPullArchived takes a pull request off the list (or puts it back).
func (s *Store) SetPullArchived(ctx context.Context, uid, id int64, archived bool) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE pulls SET archived = ?, updated_at = ? WHERE user_id = ? AND id = ?`, boolInt(archived), Now(), uid, id)
	return affected(res, err)
}

// SetMyReview records the review I just sent.
func (s *Store) SetMyReview(ctx context.Context, uid, id int64, state, sha string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE pulls SET my_review = ?, reviewed_sha = ?, updated_at = ? WHERE user_id = ? AND id = ?`, state, sha, Now(), uid, id)
	return affected(res, err)
}

// DeletePull forgets a pull request, its walkthroughs and my drafts.
func (s *Store) DeletePull(ctx context.Context, uid, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM pulls WHERE user_id = ? AND id = ?`, uid, id)
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
