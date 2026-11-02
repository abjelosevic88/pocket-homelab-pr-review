// Pocket AI beside a pull request, as Pocket Projects has it beside an issue:
// a panel on the right with a tab per conversation (Pocket AI with ?embed=1),
// resizable, remembered per pull request. "@" in a conversation offers the
// walkthrough and the changed files (their diffs); a fork opens as a tab.
import { useEffect, useRef, useState } from 'react'
import { api, V1 } from '../lib/api'
import type { Pull } from '../lib/types'
import { Icon } from './icons'
import { MenuButton, type MenuItem, errMsg, useToast } from './ui'

export interface ChatTab { key: string; url: string; title: string; send?: boolean }
interface Saved { tabs: { url: string; title: string }[]; active: number; open: boolean }
interface AgentThread { thread_id: string | null; title: string; url: string; status?: string; updated_at?: string }

const W_KEY = 'ppr-ai-dock-w'
const clampW = (w: number) => Math.round(Math.max(360, Math.min(w, window.innerWidth - 480, 1100)))
const savedKey = (pullId: number) => `ppr-ai-dock:${pullId}`
const newKey = () => Date.now().toString(36) + Math.random().toString(36).slice(2, 6)

function embedURL(u: string, send: boolean) {
  const x = new URL(u, location.href)
  x.searchParams.set('embed', '1')
  x.searchParams.set('tabs', '1') // a fork made in the frame opens as a tab here
  if (send && x.searchParams.has('draft')) x.searchParams.set('send', '1')
  return x.href
}

/** The panel's state for one pull request: its tabs, which one shows, whether it is open. */
export function useAIDock(pull: Pull | undefined, mobile: boolean) {
  const { toast } = useToast()
  const [tabs, setTabs] = useState<ChatTab[]>([])
  const [active, setActive] = useState('')
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const pid = pull?.id || 0
  useEffect(() => {
    if (!pid) return
    let s: Saved = { tabs: [], active: 0, open: false }
    try { s = { ...s, ...JSON.parse(localStorage.getItem(savedKey(pid)) || '{}') } } catch { /* a fresh start */ }
    const list = (s.tabs || []).map(t => ({ ...t, key: newKey() }))
    setTabs(list); setActive(list[s.active]?.key || list[0]?.key || ''); setOpen(!mobile && !!s.open && list.length > 0)
  }, [pid]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!pid) return
    const s: Saved = { tabs: tabs.map(({ url, title }) => ({ url, title })), active: Math.max(0, tabs.findIndex(t => t.key === active)), open }
    localStorage.setItem(savedKey(pid), JSON.stringify(s))
  }, [tabs, active, open, pid])

  const add = (url: string, title: string, send = false) => {
    const t = { key: newKey(), url, title, send }
    setTabs(l => [...l, t]); setActive(t.key); setOpen(true)
  }
  const start = async (text = '') => {
    if (!pull || busy) return
    setBusy(true)
    try {
      const r = await api.post<{ url: string; send: boolean }>(`${V1}/pulls/${pull.id}/chat`, text ? { text } : {})
      add(r.url, `Conversation ${tabs.length + 1}`, r.send)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const toggle = () => {
    if (open) { setOpen(false); return }
    if (tabs.length) setOpen(true); else start()
  }
  const close = (key: string) => {
    setTabs(l => {
      const i = l.findIndex(t => t.key === key)
      const rest = l.filter(t => t.key !== key)
      if (key === active) setActive(rest[Math.min(i, rest.length - 1)]?.key || '')
      if (!rest.length) setOpen(false)
      return rest
    })
  }
  const moved = (key: string, threadId: string, title?: string) => setTabs(l => l.map(t => {
    if (t.key !== key) return t
    const url = threadId ? `${new URL(t.url, location.href).origin}/t/${threadId}` : t.url
    return { ...t, url, title: title || t.title }
  }))
  return { tabs, active, open, busy, setActive, setOpen, start, toggle, close, add, moved }
}
export type AIDockState = ReturnType<typeof useAIDock>

/** One conversation's frame; its address is set once (changing it would reload the conversation). */
function Frame({ tab, shown, pull, onMoved, onOpen }: { tab: ChatTab; shown: boolean; pull: Pull; onMoved: (key: string, threadId: string) => void; onOpen: (url: string, title: string) => void }) {
  const ref = useRef<HTMLIFrameElement>(null)
  const [src] = useState(() => embedURL(tab.url, !!tab.send))
  const cb = useRef({ onMoved, onOpen, pull })
  cb.current = { onMoved, onOpen, pull }
  useEffect(() => {
    const origin = new URL(tab.url, location.href).origin
    const h = (e: MessageEvent) => {
      if (e.source !== ref.current?.contentWindow || e.data?.type !== 'pocket-ai') return
      const p = cb.current.pull
      if (e.data.want === 'context' || e.data.want === 'context-item') {
        // "@": the walkthrough and every changed file, answered to this frame only
        const reply = (body: Record<string, unknown>) => ref.current?.contentWindow?.postMessage({ type: 'pocket-ai-context', req: e.data.req, ...body }, origin)
        const work = e.data.want === 'context' ? contextItems(p).then(items => ({ label: `${p.repo}#${p.number}`, items })) : contextItem(p, String(e.data.id || ''))
        work.then(reply, err => reply({ error: (err as Error).message }))
        return
      }
      if (e.data.open) {
        const o = e.data.open as { url?: string; title?: string }
        if (typeof o.url === 'string' && new URL(o.url).origin === origin) cb.current.onOpen(o.url, o.title || 'Fork')
        return
      }
      if (e.data.thread_id) cb.current.onMoved(tab.key, String(e.data.thread_id))
    }
    window.addEventListener('message', h)
    return () => window.removeEventListener('message', h)
  }, [tab.key, tab.url])
  return <iframe ref={ref} src={src} title={tab.title} className={shown ? '' : 'dock-hidden'} allow="clipboard-read; clipboard-write; camera; microphone" />
}

async function contextItems(p: Pull) {
  const r = await api.get<{ files?: { path: string; status: string; summary: string; additions: number; deletions: number }[]; walkthrough?: unknown }>(`${V1}/pulls/${p.id}/brief`)
  const items: { id: string; kind: 'note' | 'file' | 'link'; title: string; sub?: string }[] = []
  if (r.walkthrough) items.push({ id: 'walkthrough', kind: 'note', title: 'The walkthrough', sub: 'Summary, steps, risks and questions' })
  for (const f of r.files || []) items.push({ id: 'file:' + f.path, kind: 'file', title: f.path, sub: `${f.status} +${f.additions} −${f.deletions}${f.summary ? ' · ' + f.summary : ''}` })
  return items
}

async function contextItem(p: Pull, id: string): Promise<{ file?: File; text?: string }> {
  const get = async (url: string) => { const res = await fetch(url, { credentials: 'include' }); if (!res.ok) throw new Error(`could not read it (${res.status})`); return res.text() }
  if (id === 'walkthrough') return { file: new File([await get(`${V1}/pulls/${p.id}/walkthrough.md`)], `walkthrough-${p.number}.md`, { type: 'text/markdown' }) }
  if (id.startsWith('file:')) {
    const path = id.slice(5)
    const text = await get(`${V1}/pulls/${p.id}/diff?path=${encodeURIComponent(path)}`)
    return { file: new File([text], `${path.split('/').pop()}.diff`, { type: 'text/plain' }) }
  }
  throw new Error('unknown item')
}

export function AIDock({ pull, dock, mobile }: { pull: Pull; dock: AIDockState; mobile: boolean }) {
  const [w, setW] = useState(() => clampW(Number(localStorage.getItem(W_KEY)) || 520))
  const [drag, setDrag] = useState(false)
  const [history, setHistory] = useState<AgentThread[]>([])
  const cur = dock.tabs.find(t => t.key === dock.active) || dock.tabs[0]
  // the page leaves room for the panel
  useEffect(() => {
    const root = document.documentElement
    root.classList.toggle('pr-dock-open', dock.open && !mobile)
    root.style.setProperty('--pr-dock-w', `${w}px`)
    return () => { root.classList.remove('pr-dock-open') }
  }, [dock.open, mobile, w])
  const loadHistory = () => api.get<AgentThread[]>(`${V1}/pulls/${pull.id}/agent`).then(l => setHistory(Array.isArray(l) ? l : [])).catch(() => {})
  useEffect(() => { if (dock.open) loadHistory() }, [dock.open, pull.id]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!dock.open || !cur) return null
  const resize = (e: React.PointerEvent) => {
    e.preventDefault()
    const x0 = e.clientX, w0 = w
    setDrag(true)
    const mv = (ev: PointerEvent) => setW(clampW(w0 + x0 - ev.clientX))
    const up = (ev: PointerEvent) => {
      setDrag(false); localStorage.setItem(W_KEY, String(clampW(w0 + x0 - ev.clientX)))
      window.removeEventListener('pointermove', mv); window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', mv); window.addEventListener('pointerup', up)
  }
  const openIds = new Set(dock.tabs.map(t => t.url.match(/\/t\/([^/?]+)/)?.[1]).filter(Boolean))
  const earlier = history.filter(h => h.thread_id && !openIds.has(h.thread_id))
  const plus: MenuItem[] = [
    { label: 'New conversation', icon: 'plus', onSelect: () => dock.start() },
    ...(earlier.length ? ['sep' as const, { heading: 'Earlier about this pull request' }, ...earlier.slice(0, 12).map(h => ({ label: h.title || 'Conversation', icon: 'message', onSelect: () => dock.add(h.url, h.title || 'Conversation') }))] : []),
  ]
  return (
    <aside className={`ai-dock ${mobile ? 'sheet' : ''} ${drag ? 'dragging' : ''}`} style={mobile ? undefined : { width: w }} aria-label="Pocket AI">
      {!mobile && <div className="ai-dock-grip" onPointerDown={resize} role="separator" aria-orientation="vertical" aria-label="Resize" />}
      <div className="ai-dock-head">
        <div className="dock-tabs" role="tablist">
          {dock.tabs.map(t => (
            <div key={t.key} role="tab" aria-selected={t.key === cur.key} tabIndex={0} title={t.title} className={`dock-tab ${t.key === cur.key ? 'on' : ''}`}
              onClick={() => dock.setActive(t.key)} onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); dock.setActive(t.key) } }}
              onAuxClick={e => { if (e.button === 1) { e.preventDefault(); dock.close(t.key) } }}>
              <Icon name="sparkles" size={13} className="ai-dock-ico" />
              <span className="ellipsis">{t.title}</span>
              <button type="button" className="dock-tab-x" onClick={e => { e.stopPropagation(); dock.close(t.key) }} aria-label={`Close ${t.title}`} title="Close"><Icon name="x" size={12} /></button>
            </div>
          ))}
          <MenuButton items={plus} label="New conversation" className="btn ghost icon-only sm dock-new" width={300}><Icon name="plus" size={15} /></MenuButton>
        </div>
        <a className="btn ghost icon-only sm" href={cur.url} target="_blank" rel="noreferrer" title="Open in Pocket AI" aria-label="Open in Pocket AI"><Icon name="external" size={15} /></a>
        <button type="button" className="btn ghost icon-only sm" onClick={() => dock.setOpen(false)} title="Hide the panel" aria-label="Hide the panel"><Icon name="x" size={15} /></button>
      </div>
      <div className="dock-frames">
        {dock.tabs.map(t => <Frame key={t.key} tab={t} pull={pull} shown={t.key === cur.key} onMoved={dock.moved} onOpen={(u, title) => dock.add(u, title)} />)}
      </div>
    </aside>
  )
}
