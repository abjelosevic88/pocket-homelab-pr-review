package store

import (
	"context"
	"encoding/json"
)

const walkCols = `id, user_id, pull_id, head_sha, base_sha, status, progress, error, ai, doc, timeline, created_at, finished_at`

func scanWalk(row interface{ Scan(...any) error }) (*Walkthrough, error) {
	var w Walkthrough
	var doc, tl string
	if err := row.Scan(&w.ID, &w.UserID, &w.PullID, &w.HeadSHA, &w.BaseSHA, &w.Status, &w.Progress, &w.Error, &w.AI, &doc, &tl, &w.CreatedAt, &w.FinishedAt); err != nil {
		return nil, notFound(err)
	}
	w.Doc, w.Timeline = rawJSON(doc, "{}"), rawJSON(tl, "[]")
	return &w, nil
}

func rawJSON(s, def string) json.RawMessage {
	if s == "" || !json.Valid([]byte(s)) {
		return json.RawMessage(def)
	}
	return json.RawMessage(s)
}

// CreateWalk queues a walkthrough of a pull request at its head commit.
func (s *Store) CreateWalk(ctx context.Context, uid, pullID int64, head, base string) (*Walkthrough, error) {
	return scanWalk(s.DB.QueryRowContext(ctx, `INSERT INTO walkthroughs (user_id, pull_id, head_sha, base_sha, status) VALUES (?, ?, ?, ?, 'queued')
		RETURNING `+walkCols, uid, pullID, head, base))
}

// GetWalk loads one of a person's walkthroughs.
func (s *Store) GetWalk(ctx context.Context, uid, id int64) (*Walkthrough, error) {
	return scanWalk(s.DB.QueryRowContext(ctx, `SELECT `+walkCols+` FROM walkthroughs WHERE user_id = ? AND id = ?`, uid, id))
}

// LatestWalk is the newest walkthrough of a pull request.
func (s *Store) LatestWalk(ctx context.Context, uid, pullID int64) (*Walkthrough, error) {
	return scanWalk(s.DB.QueryRowContext(ctx, `SELECT `+walkCols+` FROM walkthroughs WHERE user_id = ? AND pull_id = ? ORDER BY id DESC LIMIT 1`, uid, pullID))
}

// LatestReadyWalk is the newest finished walkthrough (shown while a new one is made).
func (s *Store) LatestReadyWalk(ctx context.Context, uid, pullID int64) (*Walkthrough, error) {
	return scanWalk(s.DB.QueryRowContext(ctx, `SELECT `+walkCols+` FROM walkthroughs WHERE user_id = ? AND pull_id = ? AND status = 'ready' ORDER BY id DESC LIMIT 1`, uid, pullID))
}

// WalkVersion is one walkthrough in a pull request's history.
type WalkVersion struct {
	ID         int64  `json:"id"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	AI         string `json:"ai"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at"`
}

// WalkVersions lists a pull request's walkthroughs, newest first.
func (s *Store) WalkVersions(ctx context.Context, uid, pullID int64) ([]WalkVersion, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, head_sha, status, ai, created_at, finished_at FROM walkthroughs WHERE user_id = ? AND pull_id = ? ORDER BY id DESC`, uid, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WalkVersion{}
	for rows.Next() {
		var v WalkVersion
		if err := rows.Scan(&v.ID, &v.HeadSHA, &v.Status, &v.AI, &v.CreatedAt, &v.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// LatestWalks maps pull id → its newest walkthrough, for a person's list.
func (s *Store) LatestWalks(ctx context.Context, uid int64) (map[int64]*Walkthrough, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+walkCols+` FROM walkthroughs w WHERE user_id = ?
		AND id = (SELECT MAX(id) FROM walkthroughs x WHERE x.user_id = w.user_id AND x.pull_id = w.pull_id)`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*Walkthrough{}
	for rows.Next() {
		w, err := scanWalk(rows)
		if err != nil {
			return nil, err
		}
		out[w.PullID] = w
	}
	return out, rows.Err()
}

// SetWalkProgress moves a walkthrough along (working, with a line on where it is).
func (s *Store) SetWalkProgress(ctx context.Context, id int64, status, progress string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE walkthroughs SET status = ?, progress = ? WHERE id = ?`, status, progress, id)
	return err
}

// FinishWalk stores the finished story (or why it failed).
func (s *Store) FinishWalk(ctx context.Context, id int64, status, ai, errText string, doc, timeline []byte) error {
	if doc == nil {
		doc = []byte("{}")
	}
	if timeline == nil {
		timeline = []byte("[]")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE walkthroughs SET status = ?, ai = ?, error = ?, doc = ?, timeline = ?, progress = '', finished_at = ? WHERE id = ?`,
		status, ai, errText, string(doc), string(timeline), Now(), id)
	return err
}

// UnfinishedWalks are walkthroughs a restart interrupted.
func (s *Store) UnfinishedWalks(ctx context.Context) ([]*Walkthrough, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+walkCols+` FROM walkthroughs WHERE status IN ('queued', 'working') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Walkthrough
	for rows.Next() {
		w, err := scanWalk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// PruneWalks keeps the newest few walkthroughs of a pull request.
func (s *Store) PruneWalks(ctx context.Context, uid, pullID int64, keep int) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM walkthroughs WHERE user_id = ? AND pull_id = ? AND id NOT IN
		(SELECT id FROM walkthroughs WHERE user_id = ? AND pull_id = ? ORDER BY id DESC LIMIT ?)`, uid, pullID, uid, pullID, keep)
	return err
}

// ---- files ----

// SaveWalkFiles replaces the files of a walkthrough.
func (s *Store) SaveWalkFiles(ctx context.Context, uid, walkID int64, files []WFile) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM wt_files WHERE user_id = ? AND walkthrough_id = ?`, uid, walkID); err != nil {
		return err
	}
	for i := range files {
		f := &files[i]
		if err := tx.QueryRowContext(ctx, `INSERT INTO wt_files (user_id, walkthrough_id, path, old_path, status, additions, deletions, layer, summary, patch, binary, truncated, sort)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`, uid, walkID, f.Path, f.OldPath, f.Status, f.Additions, f.Deletions, f.Layer, f.Summary,
			f.Patch, boolInt(f.Binary), boolInt(f.Truncated), f.Sort).Scan(&f.ID); err != nil {
			return err
		}
		f.WalkthroughID = walkID
	}
	return tx.Commit()
}

// SetFileSummaries writes the one-line summaries (path → text).
func (s *Store) SetFileSummaries(ctx context.Context, uid, walkID int64, sums map[string]string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for p, t := range sums {
		if _, err := tx.ExecContext(ctx, `UPDATE wt_files SET summary = ? WHERE user_id = ? AND walkthrough_id = ? AND path = ?`, t, uid, walkID, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// WalkFiles lists a walkthrough's files (with their diffs when patches is set).
func (s *Store) WalkFiles(ctx context.Context, uid, walkID int64, patches bool) ([]WFile, error) {
	patch := `''`
	if patches {
		patch = `patch`
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, walkthrough_id, path, old_path, status, additions, deletions, layer, summary, `+patch+`, binary, truncated, sort
		FROM wt_files WHERE user_id = ? AND walkthrough_id = ? ORDER BY sort, id`, uid, walkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WFile{}
	for rows.Next() {
		var f WFile
		var bin, trunc int
		if err := rows.Scan(&f.ID, &f.WalkthroughID, &f.Path, &f.OldPath, &f.Status, &f.Additions, &f.Deletions, &f.Layer, &f.Summary, &f.Patch, &bin, &trunc, &f.Sort); err != nil {
			return nil, err
		}
		f.Binary, f.Truncated = bin == 1, trunc == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---- what I looked at ----

// SetViewed marks hunks of a file as looked at (or not).
func (s *Store) SetViewed(ctx context.Context, uid, pullID int64, path string, hashes []string, on bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, h := range hashes {
		if on {
			_, err = tx.ExecContext(ctx, `INSERT INTO viewed (user_id, pull_id, path, hunk_hash) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, uid, pullID, path, h)
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM viewed WHERE user_id = ? AND pull_id = ? AND path = ? AND hunk_hash = ?`, uid, pullID, path, h)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ViewedFor is everything a person looked at in a pull request: path → hash → when.
func (s *Store) ViewedFor(ctx context.Context, uid, pullID int64) (map[string]map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT path, hunk_hash, viewed_at FROM viewed WHERE user_id = ? AND pull_id = ?`, uid, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var p, h, at string
		if err := rows.Scan(&p, &h, &at); err != nil {
			return nil, err
		}
		if out[p] == nil {
			out[p] = map[string]string{}
		}
		out[p][h] = at
	}
	return out, rows.Err()
}

// ViewedCounts maps pull id → its viewed hunks (path + "\x00" + hash), for a person's list.
func (s *Store) ViewedCounts(ctx context.Context, uid int64) (map[int64]map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT pull_id, path, hunk_hash FROM viewed WHERE user_id = ?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]bool{}
	for rows.Next() {
		var id int64
		var p, h string
		if err := rows.Scan(&id, &p, &h); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]bool{}
		}
		out[id][p+"\x00"+h] = true
	}
	return out, rows.Err()
}
