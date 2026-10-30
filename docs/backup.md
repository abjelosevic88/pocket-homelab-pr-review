# Backup

One thing: the SQLite file `DATA_DIR/pocket-pr-review.db` (WAL mode). It holds the pull requests you follow, their walkthroughs (with the diffs as they were), what you looked at, your comments and the reviews you sent. GitHub stays the source of truth for the pull requests themselves.

Take a consistent copy with `pocket-pr-review backup` (writes `DATA_DIR/backups/pocket-pr-review-<time>.db` with `VACUUM INTO`) or Settings → Backup & data → Download database (admin). Back up that copy, not the live database file. `DATA_DIR/core.token*` is the Pocket Core registration.

Restore: stop the app, replace `pocket-pr-review.db` (remove the `-wal`/`-shm` files), start.

Every person can also export their own data as JSON (`/api/v1/export.json` — the pull requests, what was looked at, draft comments and the reviews sent; re-importable, importing twice adds nothing) or the reviews as CSV (`/api/v1/export.csv`). Walkthroughs are not in the export: they are made again from GitHub.
