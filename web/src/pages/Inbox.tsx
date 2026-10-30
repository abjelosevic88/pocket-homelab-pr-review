// The lists: what waits for your review, all open pull requests, yours, the closed ones.
import { useEffect, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import type { Pull, PullList } from '../lib/types'
import { ago } from '../lib/format'
import { announceChange, openAdd, useCrumb, usePulls } from '../components/Layout'
import { Icon } from '../components/icons'
import { Empty, Loading, MenuButton, type MenuItem, Spinner, errMsg, useToast } from '../components/ui'

type ListView = 'waiting' | 'open' | 'mine' | 'done' | 'archived' | 'repo'

const TITLES: Record<ListView, [string, string]> = {
  waiting: ['Waiting for you', 'Pull requests that ask for your review and have changed since you last sent one.'],
  open: ['All open', 'Everything you follow here that is still open.'],
  mine: ['Yours', 'The pull requests you opened.'],
  done: ['Closed and merged', 'Finished pull requests you followed.'],
  archived: ['Taken off the list', 'They come back by themselves when GitHub asks for your review again.'],
  repo: ['', 'Open pull requests in this repository.'],
}

export function waitsForMe(p: Pull) { return p.reason === 'requested' && p.state === 'open' && (!p.reviewed_sha || p.reviewed_sha !== p.head_sha) }

export default function Inbox({ view }: { view: ListView }) {
  const { owner, name } = useParams()
  const repo = owner && name ? `${owner}/${name}` : ''
  const { list, reload } = usePulls()
  const { toast } = useToast()
  const loc = useLocation()
  const [other, setOther] = useState<Pull[] | null>(null)
  const [syncing, setSyncing] = useState(false)
  useCrumb(view === 'repo' ? repo : undefined)
  useEffect(() => {
    setOther(null)
    if (view === 'done') api.get<PullList>(`${V1}/pulls?state=done`).then(r => setOther(r.pulls)).catch(() => setOther([]))
    if (view === 'archived') api.get<PullList>(`${V1}/pulls?archived=1`).then(r => setOther(r.pulls)).catch(() => setOther([]))
  }, [view, loc.key])

  const sync = async () => {
    setSyncing(true)
    try {
      const r = await api.post<{ ok: boolean; error?: string; busy?: boolean; result?: { new: number; updated: number } }>(`${V1}/sync`)
      if (!r.ok) toast(r.error || 'GitHub did not answer', 'err')
      else if (r.result && (r.result.new || r.result.updated)) toast(`${r.result.new} new, ${r.result.updated} changed`)
      else if (!r.busy) toast('Up to date')
    } catch (e) { toast(errMsg(e), 'err') } finally { setSyncing(false); reload() }
  }

  if (!list) return <Loading />
  let rows: Pull[] | null = list.pulls
  if (view === 'waiting') rows = list.pulls.filter(waitsForMe)
  if (view === 'mine') rows = list.pulls.filter(p => p.reason === 'authored')
  if (view === 'repo') rows = list.pulls.filter(p => p.repo === repo)
  if (view === 'done' || view === 'archived') rows = other
  const [title, sub] = TITLES[view]
  const connected = list.github.connected || list.demo
  return (
    <div className="stack loose">
      <div className="page-head">
        <div className="grow">
          <h2 className="page-title">{title || repo}</h2>
          <p className="muted small">{sub}</p>
        </div>
        <div className="row">
          {connected && <span className="muted small sync-at" title={list.sync.error || undefined}>{list.syncing || syncing ? <><Spinner size={12} /> Checking GitHub…</> : list.sync.at ? `Checked ${ago(list.sync.at)}` : ''}</span>}
          {connected && <button type="button" className="btn" onClick={sync} disabled={syncing || list.syncing} title="Check GitHub now (r)"><Icon name="refresh" size={15} />Check now</button>}
          <button type="button" className="btn primary hide-mobile" onClick={openAdd}><Icon name="plus" size={15} />Add</button>
        </div>
      </div>

      {!connected && (
        <div className="callout info"><Icon name="github" size={16} /><div>
          <b>Connect GitHub to bring your pull requests in.</b> With a token of your own, the pull requests that ask for your review, yours and the ones you reviewed come in by themselves, and you can send your review back. <Link to="/settings/github">Connect GitHub</Link>
        </div></div>
      )}
      {connected && list.sync.error && <div className="callout warn"><Icon name="alert" size={16} /><div>The last check of GitHub failed: {list.sync.error}</div></div>}

      {rows === null ? <Loading /> : rows.length === 0 ? (
        <Empty icon={view === 'waiting' ? 'check-circle' : 'pull'} title={view === 'waiting' ? 'Nothing waits for your review' : 'No pull requests here'}>
          {view === 'waiting' ? 'When someone asks for your review on GitHub it shows up here, with its walkthrough ready.' : 'Add one by its address, or let GitHub bring them in.'}
          <div className="row center mt"><button type="button" className="btn" onClick={openAdd}><Icon name="plus" size={15} />Add a pull request</button></div>
        </Empty>
      ) : (
        <ul className="pr-list" aria-label={title || repo}>
          {rows.map(p => <PullRow key={p.id} p={p} onChange={() => { announceChange(); if (view === 'done' || view === 'archived') setOther(o => o && o.filter(x => x.id !== p.id)) }} />)}
        </ul>
      )}
    </div>
  )
}

export function reviewBadge(p: Pull) {
  if (!p.my_review) return null
  const stale = p.reviewed_sha && p.reviewed_sha !== p.head_sha
  const [label, cls] = p.my_review === 'APPROVED' ? ['You approved', 'ok'] : p.my_review === 'CHANGES_REQUESTED' ? ['You asked for changes', 'warn'] : ['You commented', '']
  return <span className={`badge ${cls}`} title={stale ? 'There are new commits since your review' : undefined}>{label}{stale ? ' · new commits' : ''}</span>
}

export function WalkPill({ p }: { p: Pull }) {
  const w = p.walkthrough
  if (!w) return <span className="walk-pill none">No walkthrough</span>
  if (w.status === 'queued' || w.status === 'working') return <span className="walk-pill working"><Spinner size={11} />{w.progress || 'Making the walkthrough'}</span>
  if (w.status === 'failed' && !w.units) return <span className="walk-pill bad" title={w.progress}>Walkthrough failed</span>
  const pct = w.units ? Math.round((w.viewed / w.units) * 100) : 0
  return (
    <span className={`walk-pill ${w.viewed >= w.units && w.units > 0 ? 'done' : ''}`} title={`${w.viewed} of ${w.units} parts looked at`}>
      <span className="walk-bar" aria-hidden><span style={{ width: `${pct}%` }} /></span>
      {w.viewed >= w.units && w.units > 0 ? 'Read' : `${w.steps} steps`}
      {w.stale && <span className="walk-stale" title="The pull request has newer commits">· new commits</span>}
    </span>
  )
}

function PullRow({ p, onChange }: { p: Pull; onChange: () => void }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const act = async (fn: () => Promise<unknown>, msg: string) => { try { await fn(); toast(msg); onChange() } catch (e) { toast(errMsg(e), 'err') } }
  const items: MenuItem[] = [
    { label: 'Open on GitHub', icon: 'external', onSelect: () => window.open(p.url, '_blank', 'noopener') },
    { label: 'Make the walkthrough again', icon: 'refresh', onSelect: () => act(() => api.post(`${V1}/pulls/${p.id}/walkthrough`), 'Making a new walkthrough') },
    'sep',
    p.archived
      ? { label: 'Put back on the list', icon: 'undo', onSelect: () => act(() => api.post(`${V1}/pulls/${p.id}/archive`, { archived: false }), 'Back on the list') }
      : { label: 'Take off the list', icon: 'archive', onSelect: () => act(() => api.post(`${V1}/pulls/${p.id}/archive`, { archived: true }), 'Taken off the list') },
    { label: 'Forget it here', icon: 'trash', danger: true, onSelect: () => act(() => api.del(`${V1}/pulls/${p.id}`), 'Forgotten (GitHub keeps it)') },
  ]
  return (
    <li className="pr-row">
      <button type="button" className="pr-main" onClick={() => navigate(`/pr/${p.id}`)}>
        <span className={`pr-state ${p.state} ${p.draft ? 'draft' : ''}`} title={p.draft ? 'Draft' : p.state}><Icon name={p.state === 'merged' ? 'check-circle' : 'pull'} size={17} /></span>
        <span className="pr-body">
          <span className="pr-title">{p.title}{p.draft && <span className="badge sm">draft</span>}</span>
          <span className="pr-meta muted small">
            <span>{p.repo}#{p.number}</span>
            <span className="pr-author">{p.author_avatar ? <img src={p.author_avatar} alt="" /> : null}{p.author}</span>
            <span className="hide-mobile">{p.head_ref}</span>
            {p.gh_updated_at && <span>{ago(p.gh_updated_at)}</span>}
            <span className="tnum"><span className="add">+{p.additions}</span> <span className="del">−{p.deletions}</span></span>
          </span>
          <span className="pr-tags">
            <WalkPill p={p} />
            {reviewBadge(p)}
            {!!p.drafts && <span className="badge"><Icon name="message" size={11} />{p.drafts} not sent</span>}
            {p.reason === 'requested' && !p.my_review && <span className="badge accent">Asks for your review</span>}
          </span>
        </span>
      </button>
      <MenuButton items={items} label="More" align="right"><Icon name="more" size={16} /></MenuButton>
    </li>
  )
}
