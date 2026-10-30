package store

import (
	"context"
	"time"
)

const commentCols = `id, user_id, pull_id, commit_sha, path, side, line, start_line, step, body, state, review_id, created_at, updated_at`

func scanComment(row interface{ Scan(...any) error }) (*Comment, error) {
	var c Comment
	if err := row.Scan(&c.ID, &c.UserID, &c.PullID, &c.CommitSHA, &c.Path, &c.Side, &c.Line, &c.StartLine, &c.Step, &c.Body, &c.State, &c.ReviewID, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

// CreateComment saves a draft comment.
func (s *Store) CreateComment(ctx context.Context, c *Comment) error {
	if c.Side == "" {
		c.Side = "RIGHT"
	}
	c.State = "draft"
	got, err := scanComment(s.DB.QueryRowContext(ctx, `INSERT INTO comments (user_id, pull_id, commit_sha, path, side, line, start_line, step, body)
		VALUES (?,?,?,?,?,?,?,?,?) RETURNING `+commentCols, c.UserID, c.PullID, c.CommitSHA, c.Path, c.Side, c.Line, c.StartLine, c.Step, c.Body))
	if err != nil {
		return err
	}
	*c = *got
	return nil
}

// GetComment loads one of a person's comments.
func (s *Store) GetComment(ctx context.Context, uid, id int64) (*Comment, error) {
	return scanComment(s.DB.QueryRowContext(ctx, `SELECT `+commentCols+` FROM comments WHERE user_id = ? AND id = ?`, uid, id))
}

// UpdateCommentBody changes a draft's text.
func (s *Store) UpdateCommentBody(ctx context.Context, uid, id int64, body string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE comments SET body = ?, updated_at = ? WHERE user_id = ? AND id = ? AND state = 'draft'`, body, Now(), uid, id)
	return affected(res, err)
}

// DeleteComment removes a draft.
func (s *Store) DeleteComment(ctx context.Context, uid, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM comments WHERE user_id = ? AND id = ? AND state = 'draft'`, uid, id)
	return affected(res, err)
}

// Comments lists my comments on a pull request (state "" = all).
func (s *Store) Comments(ctx context.Context, uid, pullID int64, state string) ([]Comment, error) {
	q := `SELECT ` + commentCols + ` FROM comments WHERE user_id = ? AND pull_id = ?`
	args := []any{uid, pullID}
	if state != "" {
		q += ` AND state = ?`
		args = append(args, state)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY path, line, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// DraftCounts maps pull id → my draft comments, for a person's list.
func (s *Store) DraftCounts(ctx context.Context, uid int64) (map[int64]int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT pull_id, COUNT(1) FROM comments WHERE user_id = ? AND state = 'draft' GROUP BY pull_id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// SaveReview records a review sent to GitHub and marks its drafts as sent.
func (s *Store) SaveReview(ctx context.Context, r *Review, commentIDs []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if r.Day == "" {
		r.Day = time.Now().Format("2006-01-02")
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO reviews (user_id, pull_id, event, body, commit_sha, comments, gh_id, gh_url, day)
		VALUES (?,?,?,?,?,?,?,?,?) RETURNING id, created_at`, r.UserID, r.PullID, r.Event, r.Body, r.CommitSHA, r.Comments, r.GHID, r.GHURL, r.Day).Scan(&r.ID, &r.CreatedAt); err != nil {
		return err
	}
	for _, id := range commentIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE comments SET state = 'sent', review_id = ?, updated_at = ? WHERE user_id = ? AND id = ?`, r.ID, Now(), r.UserID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Reviews lists reviews I sent: on one pull request (pullID > 0) or on one day.
func (s *Store) Reviews(ctx context.Context, uid, pullID int64, day string) ([]Review, error) {
	q := `SELECT r.id, r.pull_id, r.event, r.body, r.commit_sha, r.comments, r.gh_id, r.gh_url, r.day, r.created_at, p.repo, p.number, p.title
		FROM reviews r JOIN pulls p ON p.id = r.pull_id WHERE r.user_id = ?`
	args := []any{uid}
	if pullID > 0 {
		q += ` AND r.pull_id = ?`
		args = append(args, pullID)
	}
	if day != "" {
		q += ` AND r.day = ?`
		args = append(args, day)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY r.id DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Review{}
	for rows.Next() {
		var r Review
		if err := rows.Scan(&r.ID, &r.PullID, &r.Event, &r.Body, &r.CommitSHA, &r.Comments, &r.GHID, &r.GHURL, &r.Day, &r.CreatedAt, &r.Repo, &r.Number, &r.Title); err != nil {
			return nil, err
		}
		r.UserID = uid
		out = append(out, r)
	}
	return out, rows.Err()
}
