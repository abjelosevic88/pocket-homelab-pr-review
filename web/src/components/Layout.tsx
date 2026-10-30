// The app shell: sidebar, top bar, search palette, shortcuts — the Pocket Calendar layout.
import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { Outlet, useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { Dashboard, PullList } from '../lib/types'
import { Icon } from './icons'
import { Sidebar } from './Sidebar'
import { CommandPalette, type PaletteCommand } from './CommandPalette'
import { ShortcutsSheet } from './ShortcutsSheet'
import { AddPull } from './AddPull'
import { Avatar, type MenuItem, MenuButton, modalOpen, useIsMobile, useMediaQuery } from './ui'
import { AskButton } from './AskButton'

const SIDEBAR_KEY = 'pwr-sidebar'

/** Something was added or changed from the shell (or a page): the counts and the pages reload. */
export const announceChange = () => window.dispatchEvent(new CustomEvent('pwr:changed'))
export function useOnChange(cb: () => void) {
  const ref = useRef(cb)
  ref.current = cb
  useEffect(() => {
    const h = () => ref.current()
    window.addEventListener('pwr:changed', h)
    return () => window.removeEventListener('pwr:changed', h)
  }, [])
}

/** The page's own name for the top bar breadcrumb. */
const CrumbCtx = createContext<(t: string) => void>(() => {})
export function useCrumb(title: string | undefined) {
  const set = useContext(CrumbCtx)
  useEffect(() => { set(title || ''); return () => set('') }, [title, set])
}

/** The person's open pull requests and the numbers, for every page; reload() after a change. */
const PullsCtx = createContext<{ list: PullList | null; dash: Dashboard | null; reload: () => void }>({ list: null, dash: null, reload: () => {} })
export const usePulls = () => useContext(PullsCtx)

/** Open the "add a pull request" box from anywhere. */
export const openAdd = () => window.dispatchEvent(new CustomEvent('pwr:add'))

export default function Layout() {
  const navigate = useNavigate()
  const loc = useLocation()
  const [sp, setSp] = useSearchParams()
  const mobile = useIsMobile()
  const narrow = useMediaQuery('(max-width: 1099px)')
  const drawer = mobile || narrow
  const [sidebarOpen, setSidebarOpen] = useState(() => !drawer && localStorage.getItem(SIDEBAR_KEY) !== '0')
  useEffect(() => { setSidebarOpen(!drawer && localStorage.getItem(SIDEBAR_KEY) !== '0') }, [drawer])
  const drawerOpenedAt = useRef(0)
  const toggleSidebar = () => setSidebarOpen(o => { drawerOpenedAt.current = performance.now(); const n = !o; if (!drawer) localStorage.setItem(SIDEBAR_KEY, n ? '1' : '0'); return n })
  const closeDrawer = () => { if (drawer) setSidebarOpen(false) }
  useEffect(() => { document.querySelector('.content')?.scrollTo({ top: 0 }) }, [loc.pathname])

  const [crumb, setCrumbRaw] = useState('')
  const setCrumb = useCallback((t: string) => setCrumbRaw(t), [])

  const [list, setList] = useState<PullList | null>(null)
  const [dash, setDash] = useState<Dashboard | null>(null)
  const load = useCallback(() => {
    api.get<PullList>(`${V1}/pulls`).then(setList).catch(() => {})
    api.get<Dashboard>(`${V1}/dashboard`).then(setDash).catch(() => {})
  }, [])
  useEffect(() => { load() }, [loc.pathname, load])
  useOnChange(load)
  // the list follows GitHub: look again every minute while the tab is open
  useEffect(() => {
    const t = setInterval(() => { if (document.visibilityState === 'visible') load() }, 60000)
    return () => clearInterval(t)
  }, [load])

  const [palette, setPalette] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const [adding, setAdding] = useState(false)

  // /?add=<url> (a bookmarklet, the share sheet) opens the add box
  useEffect(() => { if (sp.get('add') !== null) { setAdding(true) } }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const add = () => { closeDrawer(); setAdding(true) }
  const sync = async () => {
    try { await api.post(`${V1}/sync`) } catch { /* shown on the list */ }
    load()
  }

  useEffect(() => {
    const open = () => setShortcuts(true)
    const mk = () => setAdding(true)
    window.addEventListener('pwr:shortcuts', open)
    window.addEventListener('pwr:add', mk)
    return () => { window.removeEventListener('pwr:shortcuts', open); window.removeEventListener('pwr:add', mk) }
  }, [])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette(p => !p); return }
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (t.closest('input, textarea, select, [contenteditable="true"]') || modalOpen() || document.querySelector('.menu, .popover, .sel-pop')) return
      if (loc.pathname.startsWith('/pr/') && 'jkvnpeacr'.includes(e.key)) return // the PR page has its own keys
      const go = (to: string) => { e.preventDefault(); navigate(to) }
      switch (e.key) {
        case '/': e.preventDefault(); setPalette(true); break
        case '?': e.preventDefault(); setShortcuts(true); break
        case 'n': e.preventDefault(); setAdding(true); break
        case 'r': e.preventDefault(); sync(); break
        case 'h': go('/'); break
        case 'o': go('/open'); break
        case 'm': go('/mine'); break
        case 's': go('/reviews'); break
        case ',': go('/settings'); break
        case '[': e.preventDefault(); toggleSidebar(); break
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }) // eslint-disable-line react-hooks/exhaustive-deps

  const commands: PaletteCommand[] = [
    { id: 'add', label: 'Add a pull request', icon: 'plus', hint: 'N', keywords: 'new paste url link github', run: () => add() },
    { id: 'sync', label: 'Check GitHub now', icon: 'refresh', hint: 'R', keywords: 'sync refresh update', run: () => sync() },
    { id: 'home', label: 'Waiting for you', icon: 'inbox', hint: 'H', keywords: 'review requested inbox', run: () => navigate('/') },
    { id: 'open', label: 'All open pull requests', icon: 'pull', hint: 'O', run: () => navigate('/open') },
    { id: 'mine', label: 'Yours', icon: 'user', hint: 'M', keywords: 'mine authored my', run: () => navigate('/mine') },
    { id: 'done', label: 'Closed and merged', icon: 'check-circle', keywords: 'done finished merged', run: () => navigate('/done') },
    { id: 'reviews', label: 'Reviews you sent', icon: 'message', hint: 'S', keywords: 'history sent approved', run: () => navigate('/reviews') },
    { id: 'settings', label: 'Settings', icon: 'settings', hint: ',', run: () => navigate('/settings') },
    { id: 'github', label: 'Settings: GitHub', icon: 'github', keywords: 'token connect account', run: () => navigate('/settings/github') },
    { id: 'walk', label: 'Settings: walkthroughs', icon: 'steps', keywords: 'order language hints automatic', run: () => navigate('/settings/walk') },
    { id: 'apps', label: 'Settings: Pocket AI', icon: 'bot', keywords: 'agent coding core connected apps', run: () => navigate('/settings/apps') },
    { id: 'shortcuts', label: 'Keyboard shortcuts', icon: 'keyboard', hint: '?', run: () => setShortcuts(true) },
  ]

  return (
    <CrumbCtx.Provider value={setCrumb}>
      <PullsCtx.Provider value={{ list, dash, reload: load }}>
        <div className={`app ${sidebarOpen ? 'with-sidebar' : 'no-sidebar'} ${mobile ? 'is-mobile' : ''} ${drawer ? 'drawer-mode' : ''}`}>
          {drawer && sidebarOpen && <div className="drawer-backdrop" onClick={e => { if (e.timeStamp - drawerOpenedAt.current > 400) setSidebarOpen(false) }} />}
          <div className={`sidebar-wrap ${sidebarOpen ? 'open' : ''}`}>
            <Sidebar onAdd={add} onNavigate={closeDrawer} list={list} dash={dash} />
          </div>
          <div className="main">
            <TopBar onMenu={toggleSidebar} onPalette={() => setPalette(true)} mobile={mobile} compact={narrow} sidebarOpen={sidebarOpen} crumb={crumb} />
            <main className="content"><div className={`content-inner ${loc.pathname.startsWith('/pr/') ? 'wide' : ''}`}><Outlet /></div></main>
          </div>
          {mobile && !loc.pathname.startsWith('/pr/') && <button type="button" className="fab" aria-label="Add a pull request" title="Add a pull request" onClick={add}><Icon name="plus" size={24} strokeWidth={2.2} /></button>}
          {palette && <CommandPalette onClose={() => setPalette(false)} commands={commands} pulls={list?.pulls || null} />}
          {shortcuts && <ShortcutsSheet onClose={() => setShortcuts(false)} />}
          {adding && <AddPull initial={sp.get('add') || ''} onClose={() => { setAdding(false); if (sp.get('add') !== null) setSp(new URLSearchParams(), { replace: true }) }} onAdded={id => { setAdding(false); announceChange(); navigate(`/pr/${id}`) }} />}
        </div>
      </PullsCtx.Provider>
    </CrumbCtx.Provider>
  )
}

const SECTION: Record<string, [string, string]> = { '': ['Waiting for you', '/'], open: ['Open', '/open'], mine: ['Yours', '/mine'], done: ['Closed and merged', '/done'], archived: ['Taken off the list', '/archived'], repo: ['Repository', '/open'], pr: ['Pull requests', '/'], reviews: ['Reviews you sent', '/reviews'] }

function TopBar({ onMenu, onPalette, mobile, compact, sidebarOpen, crumb }: { onMenu: () => void; onPalette: () => void; mobile: boolean; compact: boolean; sidebarOpen: boolean; crumb: string }) {
  const { user, themeMode, setThemeMode, status, setUser } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const [section, sectionTo] = SECTION[loc.pathname.split('/')[1] || ''] || ['', '/']
  const signOut = async () => { await api.post(`${V1}/auth/logout`).catch(() => {}); setUser(null); navigate('/login') }
  const avatarMenu = (): MenuItem[] => [
    { heading: user?.name || user?.email || '' },
    { label: 'Profile', icon: 'user', onSelect: () => navigate('/settings/profile') },
    { label: 'Settings', icon: 'settings', onSelect: () => navigate('/settings'), hint: ',' },
    { label: 'Keyboard shortcuts', icon: 'keyboard', onSelect: () => window.dispatchEvent(new CustomEvent('pwr:shortcuts')), hint: '?' },
    'sep',
    { heading: 'Appearance' },
    { label: 'System', checked: themeMode === 'system', onSelect: () => setThemeMode('system') },
    { label: 'Light', checked: themeMode === 'light', onSelect: () => setThemeMode('light') },
    { label: 'Dark', checked: themeMode === 'dark', onSelect: () => setThemeMode('dark') },
    ...(status?.auth_mode === 'local' ? ['sep' as const, { label: 'Sign out', icon: 'log-out', onSelect: signOut }] : []),
  ]
  return (
    <header className="topbar">
      <button type="button" className="btn ghost icon-only" onClick={onMenu} aria-label={sidebarOpen ? 'Hide the sidebar' : 'Show the sidebar'} title="Menu ([)"><Icon name={mobile || compact ? 'menu' : 'sidebar'} size={18} /></button>
      <h1 className="range-title">
        {crumb ? <><button type="button" className="crumb" onClick={() => navigate(sectionTo)}>{section}</button><Icon name="chevron-right" size={16} className="crumb-sep" /><span className="ellipsis">{crumb}</span></> : section}
      </h1>
      <span className="spacer" />
      <button type="button" className="btn ghost icon-only" onClick={onPalette} aria-label="Search" title="Search (⌘K or /)"><Icon name="search" size={18} /></button>
      <AskButton />
      <MenuButton label="Account" className="avatar-btn" align="right" width={220} items={avatarMenu}>
        <Avatar name={user?.name} email={user?.email} size={30} />
      </MenuButton>
    </header>
  )
}
