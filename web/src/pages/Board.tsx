// Your pull requests, live: every open one with its CI, reviews, open
// conversations and merge state from GitHub, looked at again by itself while
// the page is open; and the ones closed or merged in the last month.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import type { Pull } from '../lib/types'
import { ago } from '../lib/format'
import { openAdd } from '../components/Layout'
import { Icon } from '../components/icons'
import { ChecksList, describeStatus, type GHStatus, type Tone } from '../components/PullStatus'
import { Empty, Loading, Spinner, errMsg, useToast } from '../components/ui'

interface Row { pull: Pull; status: GHStatus | null; checked_at: string; error?: string }
interface BoardData {
  pulls: Row[]; done: Pull[]; login: string; connected: boolean; sync_authored: boolean
  sync: { at: string; error: string }; syncing: boolean; checked_at: string
}

type Filter = 'all' | 'red' | 'running' | 'talk' | 'changes' | 'ready'

const POLL_MS = 20_000

// how a row stands overall: red needs you, amber is waiting on something, green is fine
function health(r: Row): Tone {
  const st = r.status
  if (!st) return 'muted'
  const ck = st.checks
  if (ck.failed > 0 || st.changes_requested.length > 0 || st.mergeable === 'CONFLICTING' || st.merge_state === 'DIRTY') return 'bad'
  if (ck.running > 0 || st.unresolved > 0 || st.merge_state === 'BLOCKED' || st.merge_state === 'BEHIND' || st.merge_state === 'UNSTABLE') return 'warn'
  if (st.draft) return 'muted'
  if (ck.known && ck.passed === 0 && ck.items.length > 0) return 'muted' // every check skipped
  return 'ok'
}
const RANK: Record<Tone, number> = { bad: 0, warn: 1, ok: 2, muted: 3 }
const worst = (ts: Tone[]): Tone => ts.reduce<Tone>((a, t) => RANK[t] < RANK[a] ? t : a, 'muted' as Tone)

// A stack is my pull requests built on each other: B merges into A's branch.
// Members come bottom first (merge order) on one rail; a second pull request
// on the same branch is a side branch and steps in one level.
interface Member { r: Row; level: number; parent?: number }
interface Group { key: string; members: Member[]; tone: Tone; updated: string }

function groupStacks(rows: Row[]): Group[] {
  const byHead = new Map(rows.map(r => [`${r.pull.repo}\0${r.pull.head_ref}`, r]))
  const parentOf = (r: Row) => { const q = byHead.get(`${r.pull.repo}\0${r.pull.base_ref}`); return q && q !== r ? q : undefined }
  const kids = new Map<number, Row[]>()
  for (const r of rows) { const q = parentOf(r); if (q) kids.set(q.pull.id, [...(kids.get(q.pull.id) || []), r]) }
  const out: Group[] = []
  const seen = new Set<number>()
  for (const root of rows.filter(r => !parentOf(r))) {
    const members: Member[] = []
    const walk = (r: Row, level: number, parent?: number) => {
      if (seen.has(r.pull.id)) return
      seen.add(r.pull.id)
      members.push({ r, level, parent })
      const ks = (kids.get(r.pull.id) || []).sort((a, b) => a.pull.number - b.pull.number)
      ks.forEach((k, i) => walk(k, i === 0 ? level : level + 1, r.pull.number))
    }
    walk(root, 0)
    out.push({
      key: members.length > 1 ? `stack:${root.pull.id}` : `pr:${root.pull.id}`, members,
      tone: worst(members.map(m => health(m.r))),
      updated: members.map(m => m.r.pull.gh_updated_at || '').sort().pop() || '',
    })
  }
  // a cycle (branches merging into each other) still shows every row
  for (const r of rows) if (!seen.has(r.pull.id)) out.push({ key: `pr:${r.pull.id}`, members: [{ r, level: 0 }], tone: health(r), updated: r.pull.gh_updated_at || '' })
  out.sort((a, b) => RANK[a.tone] - RANK[b.tone] || b.updated.localeCompare(a.updated))
  return out
}

const FOLD_KEY = 'pr-review:board-folded'
function useFolded(): [Set<string>, (k: string) => void] {
  const [f, setF] = useState<Set<string>>(() => { try { return new Set(JSON.parse(localStorage.getItem(FOLD_KEY) || '[]')) } catch { return new Set() } })
  const toggle = (k: string) => setF(old => {
    const n = new Set(old)
    if (n.has(k)) n.delete(k); else n.add(k)
    try { localStorage.setItem(FOLD_KEY, JSON.stringify([...n])) } catch {}
    return n
  })
  return [f, toggle]
}

const is: Record<Exclude<Filter, 'all'>, (r: Row) => boolean> = {
  red: r => !!r.status && r.status.checks.failed > 0,
  running: r => !!r.status && r.status.checks.running > 0,
  talk: r => !!r.status && r.status.unresolved > 0,
  changes: r => !!r.status && r.status.changes_requested.length > 0,
  ready: r => !!r.status && (r.status.merge_state === 'CLEAN' || r.status.merge_state === 'HAS_HOOKS') && r.status.checks.failed === 0 && r.status.checks.running === 0,
}

// what changed since the last look, in a few words (for the toast and the row)
function changes(a: GHStatus, b: GHStatus): string[] {
  const out: string[] = []
  if (a.state !== b.state && b.state !== 'OPEN') out.push(b.state === 'MERGED' ? 'merged' : 'closed')
  if (a.checks.failed === 0 && b.checks.failed > 0) out.push(`CI failed (${b.checks.items.filter(c => c.result === 'failed').map(c => c.name).slice(0, 2).join(', ')})`)
  else if (b.checks.failed > a.checks.failed) out.push(`${b.checks.failed - a.checks.failed} more check${b.checks.failed - a.checks.failed === 1 ? '' : 's'} failing`)
  if ((a.checks.failed > 0 || a.checks.running > 0) && b.checks.failed === 0 && b.checks.running === 0 && b.checks.passed > 0) out.push('CI is green')
  if (a.checks.running === 0 && b.checks.running > 0 && b.checks.failed === 0) out.push('CI started')
  if (b.unresolved > a.unresolved) out.push(`${b.unresolved - a.unresolved} new unresolved conversation${b.unresolved - a.unresolved === 1 ? '' : 's'}`)
  else if (a.unresolved > 0 && b.unresolved === 0) out.push('all conversations resolved')
  const newly = (xs: string[], ys: string[]) => ys.filter(y => !xs.includes(y))
  const ap = newly(a.approved, b.approved), ch = newly(a.changes_requested, b.changes_requested), cm = newly(a.commented, b.commented)
  if (ap.length) out.push(`approved by ${ap.join(', ')}`)
  if (ch.length) out.push(`changes requested by ${ch.join(', ')}`)
  if (cm.length) out.push(`reviewed by ${cm.join(', ')}`)
  if (b.comments > a.comments && !ch.length && !cm.length && b.unresolved <= a.unresolved) out.push(`${b.comments - a.comments} new comment${b.comments - a.comments === 1 ? '' : 's'}`)
  if (a.mergeable !== 'CONFLICTING' && b.mergeable === 'CONFLICTING') out.push('has conflicts now')
  return out
}

export default function Board() {
  const [data, setData] = useState<BoardData | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [filter, setFilter] = useState<Filter>('all')
  const [news, setNews] = useState<Record<number, { words: string[]; at: number }>>({})
  const [, tick] = useState(0)
  const prev = useRef<Map<number, GHStatus>>(new Map())
  const loading = useRef(false)
  const { toast } = useToast()

  const load = useCallback(async (fresh = false) => {
    if (loading.current) return
    loading.current = true
    setBusy(true)
    try {
      const d = await api.get<BoardData>(`${V1}/mine/board${fresh ? '?fresh=1' : ''}`)
      const found: Record<number, { words: string[]; at: number }> = {}
      for (const r of d.pulls) {
        const old = prev.current.get(r.pull.id)
        if (old && r.status) {
          const w = changes(old, r.status)
          if (w.length) found[r.pull.id] = { words: w, at: Date.now() }
        }
        if (r.status) prev.current.set(r.pull.id, r.status)
      }
      const ids = Object.keys(found).map(Number)
      if (ids.length) {
        setNews(n => ({ ...n, ...found }))
        const r = d.pulls.find(x => x.pull.id === ids[0])!
        const bad = found[ids[0]].words.some(w => /fail|changes requested|conflict|unresolved/.test(w))
        toast(`#${r.pull.number} ${found[ids[0]].words.join(' · ')}${ids.length > 1 ? ` (+${ids.length - 1} more)` : ''}`, bad ? 'err' : undefined)
      }
      setData(d); setErr('')
    } catch (e) { setErr(errMsg(e)) } finally { loading.current = false; setBusy(false) }
  }, [toast])

  // live: look again every 20 seconds while the page is in front, at once when it comes back
  useEffect(() => {
    load()
    const t = setInterval(() => { if (document.visibilityState === 'visible') load() }, POLL_MS)
    const vis = () => { if (document.visibilityState === 'visible') load() }
    document.addEventListener('visibilitychange', vis)
    const clock = setInterval(() => tick(n => n + 1), 5_000)
    return () => { clearInterval(t); clearInterval(clock); document.removeEventListener('visibilitychange', vis) }
  }, [load])

  const rows = useMemo(() => {
    const list = [...(data?.pulls || [])]
    list.sort((a, b) => RANK[health(a)] - RANK[health(b)] || (b.pull.gh_updated_at || '').localeCompare(a.pull.gh_updated_at || ''))
    return list
  }, [data])
  const counts = useMemo(() => {
    const c = { all: rows.length } as Record<Filter, number>
    for (const k of Object.keys(is) as (keyof typeof is)[]) c[k] = rows.filter(is[k]).length
    return c
  }, [rows])
  const shown = filter === 'all' ? rows : rows.filter(is[filter])
  const groups = useMemo(() => groupStacks(rows), [rows])
  const [folded, toggleFold] = useFolded()
  const stacks = groups.filter(g => g.members.length > 1).length

  if (!data) return err ? <div className="callout warn"><Icon name="alert" size={16} /><div>{err}</div></div> : <Loading />

  const tiles: { k: Filter; label: string; tone: Tone; icon: string }[] = [
    { k: 'all', label: 'Open', tone: 'muted', icon: 'pull' },
    { k: 'red', label: 'CI failing', tone: 'bad', icon: 'x-circle' },
    { k: 'running', label: 'CI running', tone: 'warn', icon: 'clock' },
    { k: 'talk', label: 'Unresolved', tone: 'warn', icon: 'message' },
    { k: 'changes', label: 'Changes asked', tone: 'bad', icon: 'users' },
    { k: 'ready', label: 'Ready to merge', tone: 'ok', icon: 'check-circle' },
  ]

  return (
    <div className="stack loose">
      <div className="page-head board-head">
        <div className="grow">
          <h2 className="page-title">Your pull requests</h2>
          <p className="muted small">Live from GitHub: CI, reviews and open conversations of every pull request you opened. It looks again by itself.</p>
        </div>
        <div className="row">
          {data.connected && (
            <span className="muted small sync-at live-at" title={`New pull requests of yours come in with the sync${data.sync.at ? ` (last ${ago(data.sync.at)})` : ''}`}>
              {busy ? <Spinner size={12} /> : <span className="live-dot" aria-hidden />}
              {busy ? 'Checking GitHub…' : `Live · checked ${ago(data.checked_at)}`}
            </span>
          )}
          {data.connected && <button type="button" className="btn" onClick={() => load(true)} disabled={busy} title="Check GitHub now"><Icon name="refresh" size={15} />Check now</button>}
        </div>
      </div>

      {!data.connected && (
        <div className="callout info"><Icon name="github" size={16} /><div>
          <b>Connect GitHub to see your pull requests here.</b> <Link to="/settings/github">Connect GitHub</Link>
        </div></div>
      )}
      {data.connected && !data.sync_authored && (
        <div className="callout info"><Icon name="info" size={16} /><div>
          Your own pull requests are not brought in from GitHub. Turn on “Pull requests you opened” in <Link to="/settings/github">Settings → GitHub</Link>.
        </div></div>
      )}
      {err && <div className="callout warn"><Icon name="alert" size={16} /><div>The last look at GitHub failed: {err}</div></div>}

      {rows.length > 0 && (
        <div className="board-tiles" role="group" aria-label="Show">
          {tiles.map(t => (
            <button key={t.k} type="button" className={`board-tile ${t.tone} ${filter === t.k ? 'on' : ''} ${counts[t.k] === 0 && t.k !== 'all' ? 'zero' : ''}`}
              onClick={() => setFilter(f => f === t.k ? 'all' : t.k)} aria-pressed={filter === t.k}>
              <span className="board-n tnum">{counts[t.k]}</span>
              <span className="board-l"><Icon name={t.icon} size={13} />{t.label}</span>
            </button>
          ))}
        </div>
      )}

      {rows.length === 0 ? (
        <Empty icon="pull" title="No open pull requests of yours">
          When you open one on GitHub it comes in by itself within a few minutes.
          <div className="row center mt"><button type="button" className="btn" onClick={openAdd}><Icon name="plus" size={15} />Add a pull request</button></div>
        </Empty>
      ) : shown.length === 0 ? (
        <p className="muted small">None right now. <button type="button" className="link-btn" onClick={() => setFilter('all')}>Show all</button></p>
      ) : filter !== 'all' ? (
        <ul className="pr-list board-list" aria-label="Your open pull requests">
          {shown.map(r => <BoardRow key={r.pull.id} r={r} news={news[r.pull.id]} />)}
        </ul>
      ) : (
        <div className="stack">
          {stacks > 0 && (
            <div className="board-fold-all muted small">
              {stacks} stack{stacks === 1 ? '' : 's'}
              <button type="button" className="link-btn" onClick={() => {
                const ks = groups.filter(g => g.members.length > 1).map(g => g.key)
                const allFolded = ks.every(k => folded.has(k))
                ks.forEach(k => { if (folded.has(k) === allFolded) toggleFold(k) })
              }}>{groups.filter(g => g.members.length > 1).every(g => folded.has(g.key)) ? 'Open all' : 'Fold all'}</button>
            </div>
          )}
          {blocks(groups).map(b => b.stack
            ? <StackGroup key={b.stack.key} g={b.stack} folded={folded.has(b.stack.key)} onToggle={() => toggleFold(b.stack!.key)} news={news} />
            : <ul key={b.singles[0].key} className="pr-list board-list" aria-label="Your open pull requests">
                {b.singles.map(g => <BoardRow key={g.key} r={g.members[0].r} news={news[g.members[0].r.pull.id]} />)}
              </ul>)}
        </div>
      )}

      {data.done.length > 0 && (
        <section className="stack">
          <h3 className="board-h">Closed and merged in the last month</h3>
          <ul className="pr-list" aria-label="Closed and merged">
            {data.done.map(p => <DoneRow key={p.id} p={p} />)}
          </ul>
        </section>
      )}
    </div>
  )
}

// stacks get a card each; the single pull requests between them share one list
function blocks(groups: Group[]): { stack?: Group; singles: Group[] }[] {
  const out: { stack?: Group; singles: Group[] }[] = []
  for (const g of groups) {
    if (g.members.length > 1) out.push({ stack: g, singles: [] })
    else if (out.length && !out[out.length - 1].stack) out[out.length - 1].singles.push(g)
    else out.push({ singles: [g] })
  }
  return out
}

function StackGroup({ g, folded, onToggle, news }: { g: Group; folded: boolean; onToggle: () => void; news: Record<number, { words: string[]; at: number }> }) {
  const root = g.members[0].r.pull
  const sts = g.members.map(m => m.r.status).filter((x): x is GHStatus => !!x)
  const failing = g.members.filter(m => is.red(m.r)).length
  const running = g.members.filter(m => is.running(m.r)).length
  const unresolved = sts.reduce((n, s) => n + s.unresolved, 0)
  const conflicts = sts.filter(s => s.mergeable === 'CONFLICTING' || s.merge_state === 'DIRTY').length
  const changes = g.members.filter(m => is.changes(m.r)).length
  const sum: [Tone, string, string][] = []
  if (failing) sum.push(['bad', 'x-circle', `${failing} CI failing`])
  if (conflicts) sum.push(['bad', 'branch', `${conflicts} with conflicts`])
  if (changes) sum.push(['bad', 'users', `${changes} changes asked`])
  if (running) sum.push(['warn', 'clock', `${running} CI running`])
  if (unresolved) sum.push(['warn', 'message', `${unresolved} unresolved`])
  if (!sum.length && sts.length === g.members.length) sum.push(['ok', 'check-circle', 'CI green, nothing unresolved'])
  return (
    <section className={`board-stack ${folded ? 'folded' : ''}`} data-tone={g.tone} aria-label={`Stack of ${g.members.length} on ${root.base_ref}`}>
      <div className="board-stack-h">
        <button type="button" className="board-stack-toggle" onClick={onToggle} aria-expanded={!folded}>
          <Icon name={folded ? 'chevron-right' : 'chevron-down'} size={16} />
          <span className="board-health sm" data-tone={g.tone}><Icon name="layers" size={15} /></span>
          <span className="board-stack-t">
            <span className="board-stack-title">Stack of {g.members.length} <span className="muted">· {root.repo} · on <span className="mono-sm">{root.base_ref}</span></span></span>
            <span className="board-stack-sum">
              {sum.map(([t, ic, w]) => <span key={w} className={`ghs-chip plain ${t}`}><Icon name={ic} size={12} />{w}</span>)}
            </span>
          </span>
        </button>
        <Link to={`/pr/${root.id}/stack`} className="btn ghost sm hide-mobile" title="Review the stack, bottom first">Review the stack</Link>
      </div>
      {folded ? (
        <div className="board-stack-dots" aria-label="In this stack">
          {g.members.map(m => (
            <Link key={m.r.pull.id} to={`/pr/${m.r.pull.id}`} className="board-dot" data-tone={health(m.r)} title={m.r.pull.title}>
              <span className="board-dot-i" aria-hidden />#{m.r.pull.number}
            </Link>
          ))}
        </div>
      ) : (
        <ul className="pr-list board-list" aria-label="Pull requests in the stack, bottom first">
          {g.members.map((m, i) => <BoardRow key={m.r.pull.id} r={m.r} news={news[m.r.pull.id]} parent={m.parent} rail={i === 0 ? 'first' : i === g.members.length - 1 ? 'last' : 'mid'} level={m.level} />)}
        </ul>
      )}
    </section>
  )
}

function BoardRow({ r, news, parent, rail, level = 0 }: { r: Row; news?: { words: string[]; at: number }; parent?: number; rail?: 'first' | 'mid' | 'last'; level?: number }) {
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const p = r.pull, st = r.status
  const tone = health(r)
  const fresh = news && Date.now() - news.at < 10 * 60_000
  // a stacked one's base is a long branch name; its tail is enough here (the whole name is in the title)
  const base = p.base_ref.length > 28 ? '…' + p.base_ref.slice(-26) : p.base_ref
  const d = st ? describeStatus(st, base) : null
  const failing = st ? st.checks.items.filter(c => c.result === 'failed') : []
  const chipIcon = (t: Tone) => t === 'ok' ? 'check-circle' : t === 'bad' ? 'x-circle' : t === 'warn' ? 'clock' : 'circle'
  return (
    <li className={`board-row ${fresh ? 'fresh' : ''}`} data-tone={tone}>
      <div className="board-tree">
      {rail && <span className={`btg ${rail}`} data-tone={tone} aria-hidden />}
      <div className="board-cell" style={level ? { paddingLeft: level * 22 } : undefined}>
      <div className="board-top">
        <button type="button" className="pr-main" onClick={() => navigate(`/pr/${p.id}`)}>
          <span className={`board-health ${tone}`} title={tone === 'bad' ? 'Needs you' : tone === 'warn' ? 'Waiting on something' : tone === 'ok' ? 'All good' : 'Not known yet'}>
            <Icon name={tone === 'bad' ? 'x-circle' : tone === 'warn' ? 'clock' : tone === 'ok' ? 'check-circle' : 'pull'} size={17} />
          </span>
          <span className="pr-body">
            <span className="pr-title">{p.title}{(p.draft || st?.draft) && <span className="badge sm">draft</span>}</span>
            <span className="pr-meta muted small">
              <span>{p.repo}#{p.number}</span>
              {parent && <span className="board-on" title={`Merges into #${parent}'s branch`}>on #{parent}</span>}
              <span className="hide-mobile mono-sm">{p.head_ref}</span>
              {p.gh_updated_at && <span>{ago(p.gh_updated_at)}</span>}
              <span className="tnum"><span className="add">+{p.additions}</span> <span className="del">−{p.deletions}</span></span>
            </span>
          </span>
        </button>
        <a className="btn ghost icon-only" href={p.url} target="_blank" rel="noreferrer" title="Open on GitHub" aria-label="Open on GitHub"><Icon name="external" size={15} /></a>
      </div>

      {fresh && <div className="board-news"><Icon name="zap" size={12} />{news!.words.join(' · ')} <span className="muted">{ago(new Date(news!.at).toISOString())}</span></div>}

      {!st ? (
        <div className="ghs board-chips muted small">{r.error ? <span title={r.error}><Icon name="alert" size={13} /> GitHub status unavailable: {r.error.length > 90 ? r.error.slice(0, 90) + '…' : r.error}</span> : <><Spinner size={12} /> GitHub status…</>}</div>
      ) : d && (
        <div className="ghs board-chips" role="list" aria-label="Status on GitHub">
          {d.ci && (
            <button type="button" role="listitem" className={`ghs-chip ${d.ci[0]}`} onClick={() => st.checks.items.length && setOpen(o => !o)} aria-expanded={open} disabled={!st.checks.items.length} title={d.ci[2] || 'The checks on the newest commit'}>
              <Icon name={st.checks.running && !st.checks.failed ? 'clock' : chipIcon(d.ci[0])} size={13} /><span className="ghs-k">CI</span>{d.ci[1]}{st.checks.items.length > 0 && <Icon name={open ? 'chevron-up' : 'chevron-down'} size={12} />}
            </button>
          )}
          {d.conv
            ? <a role="listitem" className={`ghs-chip ${d.conv[0]}`} href={p.url} target="_blank" rel="noreferrer" title={d.conv[2] || 'Open the conversations on GitHub'}><Icon name="message" size={13} />{d.conv[1]}</a>
            : <span role="listitem" className="ghs-chip muted"><Icon name="message" size={13} />No conversations</span>}
          <span role="listitem" className={`ghs-chip ${d.rv[0]}`} title={d.rv[2]}><Icon name="users" size={13} />{d.rv[1]}</span>
          <span role="listitem" className={`ghs-chip ${d.merge[0]}`} title={[d.merge[2], `Merges into ${p.base_ref}`].filter(Boolean).join('\n')}><Icon name={st.state === 'MERGED' ? 'check-circle' : 'branch'} size={13} />{d.merge[1]}</span>
        </div>
      )}

      {!open && failing.length > 0 && (
        <div className="board-failing small">
          <span className="muted">Failing:</span>
          {failing.slice(0, 3).map((c, i) => c.url
            ? <a key={i} href={c.url} target="_blank" rel="noreferrer">{c.name}</a>
            : <span key={i}>{c.name}</span>)}
          {failing.length > 3 && <button type="button" className="link-btn" onClick={() => setOpen(true)}>+{failing.length - 3} more</button>}
        </div>
      )}
      {open && st && st.checks.items.length > 0 && <div className="board-checks"><ChecksList items={st.checks.items} /></div>}
      </div>
      </div>
    </li>
  )
}

function DoneRow({ p }: { p: Pull }) {
  const navigate = useNavigate()
  return (
    <li className="pr-row">
      <button type="button" className="pr-main" onClick={() => navigate(`/pr/${p.id}`)}>
        <span className={`pr-state ${p.state}`} title={p.state}><Icon name={p.state === 'merged' ? 'check-circle' : 'x-circle'} size={17} /></span>
        <span className="pr-body">
          <span className="pr-title">{p.title}</span>
          <span className="pr-meta muted small">
            <span>{p.repo}#{p.number}</span>
            <span className={`badge sm ${p.state === 'merged' ? 'ok' : ''}`}>{p.state === 'merged' ? 'Merged' : 'Closed'}</span>
            {p.gh_updated_at && <span>{ago(p.gh_updated_at)}</span>}
          </span>
        </span>
      </button>
      <a className="btn ghost icon-only" href={p.url} target="_blank" rel="noreferrer" title="Open on GitHub" aria-label="Open on GitHub"><Icon name="external" size={15} /></a>
    </li>
  )
}
