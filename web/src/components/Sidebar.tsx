// Left column: the Add button, the lists with their counts, the repositories — the Pocket Calendar sidebar.
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import type { Dashboard, PullList } from '../lib/types'
import { Icon } from './icons'

export function Sidebar({ onAdd, onNavigate, list, dash }: { onAdd: () => void; onNavigate: () => void; list: PullList | null; dash: Dashboard | null }) {
  const { status, settings } = useApp()
  const navigate = useNavigate()
  const title = settings?.title || status?.title || 'Pocket PR Review'
  const pulls = list?.pulls || []
  const nav: { to: string; label: string; icon: string; end?: boolean; count?: number }[] = [
    { to: '/', label: 'Waiting for you', icon: 'inbox', end: true, count: dash?.waiting },
    { to: '/open', label: 'All open', icon: 'pull', count: pulls.length || undefined },
    { to: '/mine', label: 'Yours', icon: 'user', count: dash?.mine },
    { to: '/done', label: 'Closed and merged', icon: 'check-circle' },
    { to: '/reviews', label: 'Reviews you sent', icon: 'message' },
  ]
  const repos = new Map<string, number>()
  for (const p of pulls) repos.set(p.repo, (repos.get(p.repo) || 0) + 1)
  const go = (url: string) => { onNavigate(); navigate(url) }
  return (
    <aside className="sidebar" aria-label="Pull requests">
      <div className="sb-brand">
        <img src="/favicon.svg" alt="" className="sb-logo" />
        <span className="sb-title">{title !== 'Pocket PR Review' ? title : <>Pocket <b>PR Review</b></>}</span>
        {status?.version && <span className="sb-version" title={`Version ${status.version}`}>{/^v?\d+\.\d+/.test(status.version) ? 'v' + status.version.replace(/^v/, '').replace(/[-+].*$/, '') : status.version}</span>}
      </div>
      <button type="button" className="btn primary create-btn" onClick={onAdd}>
        <Icon name="plus" size={15} strokeWidth={2.2} />Add a pull request
      </button>

      <nav className="sb-nav" aria-label="Lists">
        {nav.map(n => (
          <NavLink key={n.to} to={n.to} end={n.end} onClick={onNavigate} className={({ isActive }) => `sb-nav-item ${isActive ? 'active' : ''}`}>
            <Icon name={n.icon} size={16} />{n.label}
            {!!n.count && <span className="sb-count">{n.count}</span>}
          </NavLink>
        ))}
      </nav>

      {dash && (dash.draft_comments > 0 || dash.walkthroughs_working > 0) && (
        <section className="sb-sec sb-note" aria-label="Now">
          {dash.walkthroughs_working > 0 && <div className="sb-note-row"><Icon name="steps" size={15} className="muted" />{dash.walkthroughs_working} walkthrough{dash.walkthroughs_working === 1 ? '' : 's'} being made</div>}
          {dash.draft_comments > 0 && <div className="sb-note-row"><Icon name="message" size={15} className="muted" />{dash.draft_comments} comment{dash.draft_comments === 1 ? '' : 's'} not sent yet</div>}
        </section>
      )}

      {repos.size > 0 && (
        <section className="sb-sec">
          <h4 className="sb-h">Repositories</h4>
          <div className="sb-decks">
            {[...repos.entries()].sort((a, b) => a[0].localeCompare(b[0])).slice(0, 16).map(([r, n]) => (
              <button key={r} type="button" className="sb-deck" onClick={() => go(`/repo/${r}`)} title={r}>
                <Icon name="github" size={14} className="muted" />
                <span className="ellipsis grow">{r}</span>
                <span className="sb-count">{n}</span>
              </button>
            ))}
          </div>
        </section>
      )}

      <div className="sb-foot">
        <Link to="/settings" className="sb-foot-link" onClick={onNavigate}><Icon name="settings" size={15} />Settings</Link>
        <button type="button" className="sb-foot-link" onClick={() => { onNavigate(); window.dispatchEvent(new CustomEvent('pwr:shortcuts')) }}><Icon name="keyboard" size={15} />Shortcuts</button>
      </div>
    </aside>
  )
}
