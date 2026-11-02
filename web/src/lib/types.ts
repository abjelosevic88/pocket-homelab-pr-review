export interface User { id: number; email: string; name: string; role: 'admin' | 'member'; created_at: string }
export interface APIToken { id: number; user_id: number; name: string; prefix: string; last_used_at: string | null; created_at: string }

export type ConnMode = 'core' | 'direct' | 'off'
export type ConnVia = '' | 'core' | 'direct'

export interface Settings {
  core_configured: boolean
  core_registered?: boolean
  title: string; date_format: string; time_format: string; week_start: number
  github_token: string; github_api_url: string; github_login: string; github_configured: boolean
  sync_requested: boolean; sync_authored: boolean; sync_reviewed: boolean; sync_repos: string; exclude_drafts: boolean
  auto_walk: 'requested' | 'all' | 'off'; walk_language: string; walk_hints: string; open_view: View; diff_wrap: boolean
  notify_requested: boolean; notify_ready: boolean
  notify_url: string; notify_format: string; notify_configured: boolean
  ai_mode: ConnMode; ai_via: ConnVia; ai_detail: string; ai_open_url: string; ai_configured: boolean
  ai_url: string; ai_token: string; ai_public_url: string
  ai_provider: 'auto' | 'pocket' | 'openai'; ai_pocket_model: string; openai_api_key: string; openai_model: string; openai_base_url: string
  setup_complete: boolean; base_url: string; demo: boolean
}

export type View = 'steps' | 'built' | 'files'
export type Reason = 'requested' | 'authored' | 'reviewed' | 'added'

export interface WalkBrief { id: number; status: WalkStatus; progress: string; head_sha: string; steps: number; units: number; viewed: number; stale: boolean }
export type WalkStatus = 'queued' | 'working' | 'ready' | 'failed'

export interface Pull {
  id: number; repo: string; number: number; title: string; body: string; url: string; author: string; author_avatar: string
  state: 'open' | 'closed' | 'merged'; draft: boolean; base_ref: string; head_ref: string; base_sha: string; head_sha: string
  additions: number; deletions: number; changed_files: number; commits: number; reason: Reason; my_review: string; reviewed_sha: string
  source: string; archived: boolean; gh_updated_at: string; synced_at: string; created_at: string; updated_at: string
  walkthrough?: WalkBrief; drafts?: number
}

export interface PullList { pulls: Pull[]; github: { connected: boolean; login: string }; sync: { at: string; error: string }; syncing: boolean; demo: boolean }

export interface Unit { id: string; path: string; hunks: number[] | null; hashes: string[]; summary: string; layer: string }
export interface Step { key: string; title: string; why: string; layer: string; risks: string[]; units: Unit[] }
export interface Risk { level: 'high' | 'medium' | 'low'; title?: string; text: string; units: string[] }
export interface Stage { title: string; items: string[]; steps: number[] }
export interface Term { term: string; means: string }
export interface Doc {
  title: string; tldr?: string; overview: string; flow?: Stage[]; terms?: Term[]; before: string; after: string; steps: Step[]; noise: Unit[]; risks: Risk[]; questions: string[]
  order: 'ai' | 'layers'; warnings: string[]; stats: { steps: number; units: number; files: number; additions: number; deletions: number }
}
export interface TFile { path: string; status: string; additions: number; deletions: number; patch?: string; note?: string }
export interface Event { kind: 'commit' | 'edit'; at: string; title: string; body?: string; sha?: string; author?: string; files: TFile[] }
export interface Walkthrough { id: number; pull_id: number; head_sha: string; base_sha: string; status: WalkStatus; progress: string; error: string; ai: string; doc: Doc; timeline: Event[]; created_at: string; finished_at: string }

export interface Line { t: '+' | '-' | ' ' | '\\'; o?: number; n?: number; s: string }
export interface Hunk { header: string; old_start: number; old_lines: number; new_start: number; new_lines: number; lines: Line[]; hash: string }
export interface WFile { id: number; path: string; old_path: string; status: string; additions: number; deletions: number; layer: string; summary: string; binary: boolean; truncated: boolean; sort: number; hunks: Hunk[] }

export interface Comment { id: number; pull_id: number; commit_sha: string; path: string; side: 'LEFT' | 'RIGHT'; line: number; start_line: number; step: string; body: string; state: 'draft' | 'sent'; review_id: number; created_at: string; updated_at: string }
export interface Review { id: number; pull_id: number; event: 'APPROVE' | 'REQUEST_CHANGES' | 'COMMENT'; body: string; commit_sha: string; comments: number; gh_id: number; gh_url: string; day: string; created_at: string; repo?: string; number?: number; title?: string }
export interface WalkVersion { id: number; head_sha: string; status: WalkStatus; ai: string; created_at: string; finished_at: string }

export interface GHComment { id: number; in_reply_to_id: number; path: string; line: number | null; side: string; body: string; user: { login: string; avatar_url: string }; html_url: string; created_at: string }
export interface Thread { id: number; path: string; line: number; side: string; outdated: boolean; url: string; comments: GHComment[] }

export type Viewed = Record<string, Record<string, string>>

export interface PullPage {
  pull: Pull
  walkthrough?: Walkthrough
  building?: { id: number; status: WalkStatus; progress: string; error: string; head_sha: string; created_at: string }
  files: WFile[]
  versions: WalkVersion[]
  viewed: Viewed
  comments: Comment[]
  reviews: Review[]
  github: { connected: boolean; login: string }
}

export interface Dashboard { waiting: number; open: number; mine: number; walkthroughs_to_read: number; walkthroughs_working: number; draft_comments: number; reviews_today: number }
