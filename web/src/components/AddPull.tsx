// Add a pull request: pick one of the open pull requests you can reach on
// GitHub, or paste its address. Its walkthrough starts right away.
import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import type { Pull } from '../lib/types'
import { ago } from '../lib/format'
import { Icon } from './icons'
import { Select } from './Select'
import { Modal, Segmented, Spinner, errMsg, useDebounce } from './ui'

const looksLikePR = (s: string) => /\/pull\/\d+|^[\w.-]+\/[\w.-]+#\d+$/.test(s.trim())

interface Pick { repo: string; number: number; title: string; url: string; author: string; author_avatar: string; draft: boolean; updated_at: string; added: number }
interface Repo { name: string; private: boolean }
type Scope = 'all' | 'review' | 'mine' | 'involved'

const SCOPE_KEY = 'pwr-add-scope'
const REPO_KEY = 'pwr-add-repo'

export function AddPull({ initial = '', onClose, onAdded }: { initial?: string; onClose: () => void; onAdded: (id: number) => void }) {
  const navigate = useNavigate()
  const [url, setUrl] = useState(initial)
  const [busy, setBusy] = useState<string | null>(null) // the url being added
  const [err, setErr] = useState('')
  const auto = useRef(looksLikePR(initial))
  const [scope, setScope] = useState<Scope>(() => (localStorage.getItem(SCOPE_KEY) as Scope) || 'all')
  const [repo, setRepo] = useState(() => localStorage.getItem(REPO_KEY) || '')
  const [q, setQ] = useState('')
  const dq = useDebounce(q, 350)
  const [repos, setRepos] = useState<Repo[]>([])
  const [list, setList] = useState<Pick[] | null>(null)
  const [listErr, setListErr] = useState('')

  const add = async (address: string) => {
    if (!address.trim() || busy) return
    setBusy(address); setErr('')
    try {
      const r = await api.post<{ pull: Pull }>(`${V1}/pulls`, { url: address.trim() })
      onAdded(r.pull.id)
    } catch (e) { setErr(errMsg(e)); setBusy(null) }
  }
  // a link from another app (…/?add=https://github.com/…/pull/12) adds at once
  useEffect(() => { if (auto.current) { auto.current = false; add(initial) } }, []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { api.get<{ repos: Repo[] }>(`${V1}/github/repos`).then(r => setRepos(r.repos || [])).catch(() => {}) }, [])
  useEffect(() => {
    localStorage.setItem(SCOPE_KEY, scope)
    if (repo) localStorage.setItem(REPO_KEY, repo); else localStorage.removeItem(REPO_KEY)
    let live = true
    setList(null); setListErr('')
    const p = new URLSearchParams({ scope })
    if (repo) p.set('repo', repo)
    if (dq.trim()) p.set('q', dq.trim())
    api.get<{ pulls: Pick[]; error?: string }>(`${V1}/github/pulls?${p}`)
      .then(r => { if (live) { setList(r.pulls || []); setListErr(r.error || '') } })
      .catch(e => { if (live) { setList([]); setListErr(errMsg(e)) } })
    return () => { live = false }
  }, [scope, repo, dq])

  const paste = async () => { try { const t = await navigator.clipboard.readText(); if (t) setUrl(t.trim()) } catch { /* not allowed */ } }
  const open = (p: Pick) => {
    if (p.added) { onClose(); navigate(`/pr/${p.added}`); return }
    add(p.url)
  }
  return (
    <Modal title="Add a pull request" onClose={onClose} size="lg" className="add-pr" footer={<>
      <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
    </>}>
      <div className="stack">
        <div className="row">
          <input autoFocus={!initial} value={url} onChange={e => setUrl(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') add(url) }}
            placeholder="Paste its address: https://github.com/owner/repo/pull/12" aria-label="Pull request address" />
          <button type="button" className="btn" onClick={paste}>Paste</button>
          <button type="button" className="btn primary" onClick={() => add(url)} disabled={!!busy || !url.trim()}>{busy === url && url ? 'Adding…' : 'Add'}</button>
        </div>
        {err && <p className="bad small">{err}</p>}

        <div className="add-or"><span>or pick one that is open</span></div>
        <div className="add-filters">
          <Segmented size="sm" value={scope} onChange={v => setScope(v as Scope)} options={[
            { value: 'all', label: 'Everything' },
            { value: 'review', label: 'Asks for my review' },
            { value: 'mine', label: 'Mine' },
            { value: 'involved', label: 'I am in' },
          ]} />
          <div className="add-repo">
            <Select ariaLabel="Repository" value={repo} onChange={setRepo} placeholder="All repositories"
              options={[{ value: '', label: 'All repositories' }, ...repos.map(r => ({ value: r.name, label: r.name, hint: r.private ? 'private' : undefined }))]} />
          </div>
          <div className="search-box add-search">
            <Icon name="search" size={15} />
            <input value={q} onChange={e => { const v = e.target.value; if (looksLikePR(v)) { setUrl(v); setQ('') } else setQ(v) }} placeholder="Find by title, number or author…" aria-label="Find a pull request" />
          </div>
        </div>
        <div className="add-list" role="listbox" aria-label="Open pull requests">
          {list === null ? <div className="add-empty muted small"><Spinner size={13} /> Asking GitHub…</div>
            : listErr ? <div className="add-empty bad small">{listErr}</div>
            : list.length === 0 ? <div className="add-empty muted small">No open pull requests here.</div>
            : list.map(p => (
              <button key={p.url} type="button" role="option" aria-selected={false} className="add-row" onClick={() => open(p)} disabled={!!busy}>
                <span className={`pr-state ${p.draft ? 'draft' : ''}`}><Icon name="pull" size={15} /></span>
                <span className="add-row-body">
                  <span className="add-row-title">{p.title}{p.draft && <span className="badge sm">draft</span>}</span>
                  <span className="muted small add-row-meta">
                    <span>{p.repo}#{p.number}</span>
                    <span className="pr-author">{p.author_avatar && <img src={p.author_avatar} alt="" />}{p.author}</span>
                    {p.updated_at && <span>{ago(p.updated_at)}</span>}
                  </span>
                </span>
                {busy === p.url ? <Spinner size={14} /> : p.added ? <span className="badge ok">Open</span> : <span className="add-plus"><Icon name="plus" size={15} /></span>}
              </button>
            ))}
        </div>
      </div>
    </Modal>
  )
}
