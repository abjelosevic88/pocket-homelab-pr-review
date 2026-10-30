# API

Everything under `/api/v1`, JSON in and out. Authenticate with a session cookie, a personal token (`Authorization: Bearer pwr_…`, Settings → API & shortcuts) or, from another Pocket app, a Pocket Core link token. Everything is per person: another person's ids answer 404.

## Lists

| Call | What |
|---|---|
| `GET /dashboard` | `waiting` (asking for your review, changed since your last one), `open`, `mine`, `walkthroughs_to_read`, `walkthroughs_working`, `draft_comments`, `reviews_today` — for Homepage |
| `GET /pulls?state=open\|done\|all&reason=requested\|authored\|reviewed\|added&repo=&archived=1` | Pull requests with their newest walkthrough (`walkthrough`: status, steps, units, viewed, stale) and `drafts`; plus `github` (connected, login) and the last `sync` |
| `POST /pulls` | `{"url":"https://github.com/o/r/pull/12","walk":true}` — add one (or put it back) and make its walkthrough |
| `GET /lookup?url=` | One pull request by its address (for badges in other apps) |
| `POST /sync` | Check GitHub now |
| `GET /reviews` | The reviews you sent, newest first |
| `GET /day/YYYY-MM-DD` | The reviews you sent that day — what Pocket Day reads |

## One pull request

| Call | What |
|---|---|
| `GET /pulls/{id}[?v=walkthroughID]` | The pull request, the walkthrough shown (`doc`: overview, before/after, steps → units, noise, risks, questions, warnings; `timeline`: commits and agent edits), the one `building`, `files` with their hunks, `viewed`, `comments`, `reviews`, `versions` |
| `POST /pulls/{id}/walkthrough` | Make a new walkthrough (at the head commit) |
| `GET /pulls/{id}/walkthroughs/{wid}/status` | While it is being made: `status`, `progress` |
| `GET /pulls/{id}/walkthrough.md` | The walkthrough as Markdown |
| `POST /pulls/{id}/walkthrough/post` | Post it as a comment on the pull request |
| `PUT /pulls/{id}/viewed` | `{"items":[{"path","hashes":[…]}],"viewed":true}` — mark hunks as looked at (by content: a commit that changes one unmarks it) |
| `GET /pulls/{id}/threads` | The GitHub line comments, as threads |
| `POST /pulls/{id}/reply` | `{"to":commentID,"body"}` — reply on GitHub now |
| `GET/POST /pulls/{id}/comments`, `PUT/DELETE /pulls/{id}/comments/{cid}` | Your draft comments: `{"path","line","start_line","side":"RIGHT\|LEFT","body"}` on a line, or `{"step","body"}` on a step |
| `POST /pulls/{id}/review` | `{"event":"APPROVE\|REQUEST_CHANGES\|COMMENT","body"}` — sends the review with your drafts to GitHub (step notes go into the text) |
| `POST /pulls/{id}/ask` | `{"question","step":stepKey}` → the AI's answer about the pull request or one step |
| `POST /pulls/{id}/archive`, `DELETE /pulls/{id}` | Take off the list / forget here |

## A coding agent (Pocket AI)

| Call | What |
|---|---|
| `GET /agent/options?repo=` | Whether Pocket AI is connected, its machines and agents, the folder remembered for the repository |
| `GET /agent/dirs?host=&path=` | Browse folders on a machine |
| `GET /pulls/{id}/agent/prompt` | The message with your comments (drafts, else those of your last review) |
| `POST /pulls/{id}/agent` | `{"host","cwd","provider","title","text","worktree":true,"remember":true}` → `url`: the prepared conversation in Pocket AI |
| `GET /pulls/{id}/agent` | Conversations started from this pull request |

## Settings, people, data

`GET/PUT /settings`, `POST /integrations/test` (`{"which":"github\|ai\|openai\|notify"}`), `GET /ai/status`, `GET /ai/models`, `/users`, `/tokens`, `GET /backup` (admin), `GET /export.json`, `GET /export.csv`, `POST /import`.

## Pocket Core links

Offers `prs:read` (lists, a pull request and its walkthrough, the day) and `prs:write` (add a pull request and make its walkthrough — never send a review). Uses Pocket AI's `threads:write` (prepare a conversation with your comments) and `threads:read` (the order an agent edited the files in) and inference. Publishes `pr-review.review_requested`, `pr-review.walkthrough_ready`, `pr-review.review_sent`.
