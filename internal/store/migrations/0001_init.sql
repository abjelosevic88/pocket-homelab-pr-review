CREATE TABLE users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'member',
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT
);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Box-wide settings (title, setup flag); everything else is personal.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE user_settings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, key)
);

CREATE TABLE cache (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL
);


-- A pull request someone follows here: asked to review, their own, or added
-- by hand. One row per person and PR; GitHub stays the source of truth.
CREATE TABLE pulls (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  repo          TEXT NOT NULL,                  -- owner/name
  number        INTEGER NOT NULL,
  title         TEXT NOT NULL DEFAULT '',
  body          TEXT NOT NULL DEFAULT '',
  url           TEXT NOT NULL DEFAULT '',
  author        TEXT NOT NULL DEFAULT '',
  author_avatar TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'open',   -- open | closed | merged
  draft         INTEGER NOT NULL DEFAULT 0,
  base_ref      TEXT NOT NULL DEFAULT '',
  head_ref      TEXT NOT NULL DEFAULT '',
  base_sha      TEXT NOT NULL DEFAULT '',
  head_sha      TEXT NOT NULL DEFAULT '',
  additions     INTEGER NOT NULL DEFAULT 0,
  deletions     INTEGER NOT NULL DEFAULT 0,
  changed_files INTEGER NOT NULL DEFAULT 0,
  commits       INTEGER NOT NULL DEFAULT 0,
  reason        TEXT NOT NULL DEFAULT 'added',  -- requested | authored | reviewed | added
  my_review     TEXT NOT NULL DEFAULT '',       -- APPROVED | CHANGES_REQUESTED | COMMENTED | ''
  reviewed_sha  TEXT NOT NULL DEFAULT '',       -- the head when I last sent a review
  source        TEXT NOT NULL DEFAULT 'app',    -- app | sync | <pocket app slug>
  archived      INTEGER NOT NULL DEFAULT 0,     -- taken off the list (comes back when asked again)
  gh_updated_at TEXT NOT NULL DEFAULT '',
  synced_at     TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX pulls_user_ref ON pulls(user_id, repo, number);
CREATE INDEX pulls_user_list ON pulls(user_id, archived, state, gh_updated_at);

-- A walkthrough: one pull request, at one head commit, told as an ordered
-- set of steps. doc is the story (overview, steps → units), timeline is how
-- it was built (commits, an agent's edits).
CREATE TABLE walkthroughs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  pull_id     INTEGER NOT NULL REFERENCES pulls(id) ON DELETE CASCADE,
  head_sha    TEXT NOT NULL DEFAULT '',
  base_sha    TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'queued',   -- queued | working | ready | failed
  progress    TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT '',
  ai          TEXT NOT NULL DEFAULT '',         -- pocket | openai | '' (ordered without AI)
  doc         TEXT NOT NULL DEFAULT '{}',
  timeline    TEXT NOT NULL DEFAULT '[]',
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  finished_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX walkthroughs_pull ON walkthroughs(user_id, pull_id, id);
CREATE INDEX walkthroughs_status ON walkthroughs(status);

-- The changed files of a walkthrough, kept as they were at its head commit.
CREATE TABLE wt_files (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  walkthrough_id INTEGER NOT NULL REFERENCES walkthroughs(id) ON DELETE CASCADE,
  path           TEXT NOT NULL,
  old_path       TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'modified', -- added | modified | removed | renamed | copied | changed
  additions      INTEGER NOT NULL DEFAULT 0,
  deletions      INTEGER NOT NULL DEFAULT 0,
  layer          TEXT NOT NULL DEFAULT '',
  summary        TEXT NOT NULL DEFAULT '',
  patch          TEXT NOT NULL DEFAULT '',
  binary         INTEGER NOT NULL DEFAULT 0,
  truncated      INTEGER NOT NULL DEFAULT 0,     -- GitHub sent no patch (too large)
  sort           INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX wt_files_walk ON wt_files(user_id, walkthrough_id, sort);

-- What someone has looked at: a hunk of a file, by its content, so a later
-- commit that changes it makes it "changed since you looked".
CREATE TABLE viewed (
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  pull_id   INTEGER NOT NULL REFERENCES pulls(id) ON DELETE CASCADE,
  path      TEXT NOT NULL,
  hunk_hash TEXT NOT NULL,
  viewed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (user_id, pull_id, path, hunk_hash)
);

-- My review comments: drafts until the review is sent to GitHub. path '' is a
-- note on a whole step (it goes into the review's text).
CREATE TABLE comments (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  pull_id    INTEGER NOT NULL REFERENCES pulls(id) ON DELETE CASCADE,
  commit_sha TEXT NOT NULL DEFAULT '',          -- the head the line belongs to
  path       TEXT NOT NULL DEFAULT '',
  side       TEXT NOT NULL DEFAULT 'RIGHT',     -- RIGHT (new) | LEFT (old)
  line       INTEGER NOT NULL DEFAULT 0,
  start_line INTEGER NOT NULL DEFAULT 0,        -- 0 = one line
  step       TEXT NOT NULL DEFAULT '',          -- the step's title, for a step note
  body       TEXT NOT NULL DEFAULT '',
  state      TEXT NOT NULL DEFAULT 'draft',     -- draft | sent
  review_id  INTEGER NOT NULL DEFAULT 0,        -- reviews.id once sent
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX comments_pull ON comments(user_id, pull_id, state);

-- Reviews I sent to GitHub from here.
CREATE TABLE reviews (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  pull_id    INTEGER NOT NULL REFERENCES pulls(id) ON DELETE CASCADE,
  event      TEXT NOT NULL,                     -- APPROVE | REQUEST_CHANGES | COMMENT
  body       TEXT NOT NULL DEFAULT '',
  commit_sha TEXT NOT NULL DEFAULT '',
  comments   INTEGER NOT NULL DEFAULT 0,
  gh_id      INTEGER NOT NULL DEFAULT 0,
  gh_url     TEXT NOT NULL DEFAULT '',
  day        TEXT NOT NULL DEFAULT '',          -- local date, for the day view
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX reviews_user_day ON reviews(user_id, day);
