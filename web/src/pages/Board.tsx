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
  return 'ok'
}
const RANK: Record<Tone, number> = { bad: 0, warn: 1, ok: 2, muted: 3 }

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
      ) : (
        <ul className="pr-list board-list" aria-label="Your open pull requests">
          {shown.map(r => <BoardRow key={r.pull.id} r={r} news={news[r.pull.id]} />)}
        </ul>
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

function BoardRow({ r, news }: { r: Row; news?: { words: string[]; at: number } }) {
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const p = r.pull, st = r.status
  const tone = health(r)
  const fresh = news && Date.now() - news.at < 10 * 60_000
  const d = st ? describeStatus(st, p.base_ref) : null
  const failing = st ? st.checks.items.filter(c => c.result === 'failed') : []
  const chipIcon = (t: Tone) => t === 'ok' ? 'check-circle' : t === 'bad' ? 'x-circle' : t === 'warn' ? 'clock' : 'circle'
  return (
    <li className={`board-row ${fresh ? 'fresh' : ''}`} data-tone={tone}>
      <div className="board-top">
        <button type="button" className="pr-main" onClick={() => navigate(`/pr/${p.id}`)}>
          <span className={`board-health ${tone}`} title={tone === 'bad' ? 'Needs you' : tone === 'warn' ? 'Waiting on something' : tone === 'ok' ? 'All good' : 'Not known yet'}>
            <Icon name={tone === 'bad' ? 'x-circle' : tone === 'warn' ? 'clock' : tone === 'ok' ? 'check-circle' : 'pull'} size={17} />
          </span>
          <span className="pr-body">
            <span className="pr-title">{p.title}{(p.draft || st?.draft) && <span className="badge sm">draft</span>}</span>
            <span className="pr-meta muted small">
              <span>{p.repo}#{p.number}</span>
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
          <span role="listitem" className={`ghs-chip ${d.merge[0]}`} title={d.merge[2]}><Icon name={st.state === 'MERGED' ? 'check-circle' : 'branch'} size={13} />{d.merge[1]}</span>
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
