// One pull request, read as a story: the walkthrough's steps in reading
// order, how it was built (commits and an agent's edits), or every file.
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useParams, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { Comment, Doc, Event, PullPage, Step, Thread, Unit, View, Viewed, WFile } from '../lib/types'
import { parsePatch, short } from '../lib/diff'
import { ago, fmtDateTime } from '../lib/format'
import { announceChange, useCrumb } from '../components/Layout'
import { Icon } from '../components/icons'
import { Composer, Diff, type DiffActions, MyComment } from '../components/Diff'
import { ReviewModal } from '../components/ReviewModal'
import { AgentModal } from '../components/AgentModal'
import { Select } from '../components/Select'
import { DataFlow } from '../components/DataFlow'
import { PullStatus } from '../components/PullStatus'
import { AIDock, Conversations, useAIDock } from '../components/AIDock'
import { AIMark } from '../components/AIMark'
import { Empty, Loading, MenuButton, type MenuItem, Modal, Segmented, Spinner, copyText, errMsg, modalOpen, useIsMobile, useToast } from '../components/ui'
import { WalkPill, reviewBadge } from './Inbox'

const OVERVIEW = 'overview'

/** Older or partial walkthroughs may leave lists out: make every list a list. */
function normalize(p: PullPage): PullPage {
  const d = p.walkthrough?.doc
  if (d) {
    d.steps = (d.steps || []).map(s => ({ ...s, risks: s.risks || [], units: (s.units || []).map(u => ({ ...u, hashes: u.hashes || [] })) }))
    d.noise = d.noise || []; d.risks = (d.risks || []).map(r => ({ ...r, units: r.units || [] })); d.questions = d.questions || []; d.warnings = d.warnings || []
    d.flow = (d.flow || []).map(g => ({ ...g, items: g.items || [], steps: g.steps || [] })); d.terms = d.terms || []
    if (d.diagram) d.diagram = { nodes: d.diagram.nodes || [], edges: d.diagram.edges || [] }
  }
  if (p.walkthrough) p.walkthrough.timeline = (p.walkthrough.timeline || []).map(e => ({ ...e, files: e.files || [] }))
  p.files = (p.files || []).map(f => ({ ...f, hunks: f.hunks || [] }))
  p.comments = p.comments || []; p.versions = p.versions || []; p.reviews = p.reviews || []; p.viewed = p.viewed || {}
  return p
}

const unitDone = (u: Unit, viewed: Viewed) => u.hashes.length > 0 && u.hashes.every(h => !!viewed[u.path]?.[h])
const unitChanged = (u: Unit, viewed: Viewed) => !unitDone(u, viewed) && !!viewed[u.path] && Object.keys(viewed[u.path]).length > 0
const stepDone = (s: Step, viewed: Viewed) => s.units.length > 0 && s.units.every(u => unitDone(u, viewed))

/** A test: in the tests layer, or named like one (foo.test.ts, foo_test.go, tests/, __tests__/, spec/). */
const TEST_PATH = /(^|\/)(__tests__|tests?|spec|e2e)\/|[._-](test|spec)\.[a-z0-9]+$|_test\.go$|(^|\/)test_[^/]+\.py$/i
const isTest = (u: Unit) => u.layer === 'tests' || TEST_PATH.test(u.path)

/** ⌘ on a Mac, Ctrl elsewhere: held, a click anywhere on a file marks it as looked at. */
const MAC = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const modKey = MAC ? '⌘' : 'Ctrl'
const modDown = (e: { metaKey: boolean; ctrlKey: boolean }) => MAC ? e.metaKey : e.ctrlKey

const LAYER_LABEL: Record<string, string> = { schema: 'database', model: 'model', data: 'data access', logic: 'logic', api: 'API', config: 'config', ui: 'UI', tests: 'tests', docs: 'docs', build: 'build', noise: 'generated' }

export default function Pull() {
  const { id } = useParams()
  const [sp, setSp] = useSearchParams()
  const { settings } = useApp()
  const { toast } = useToast()
  const mobile = useIsMobile()
  // holding the modifier shows which file a click would mark (CSS: .mod-held)
  useEffect(() => {
    const root = document.documentElement
    const set = (on: boolean) => root.classList.toggle('mod-held', on)
    const key = (e: KeyboardEvent) => set(modDown(e))
    const move = (e: MouseEvent) => set(modDown(e))
    const off = () => set(false)
    window.addEventListener('keydown', key); window.addEventListener('keyup', key); window.addEventListener('mousemove', move, { passive: true }); window.addEventListener('blur', off)
    return () => { window.removeEventListener('keydown', key); window.removeEventListener('keyup', key); window.removeEventListener('mousemove', move); window.removeEventListener('blur', off); off() }
  }, [])
  // the tabs stick to the top; a file's header sticks right under them
  const [tabsH, setTabsH] = useState(0)
  const tabsRO = useRef<ResizeObserver | null>(null)
  const tabsRef = useCallback((el: HTMLDivElement | null) => {
    tabsRO.current?.disconnect()
    tabsRO.current = null
    if (!el) return
    tabsRO.current = new ResizeObserver(() => setTabsH(el.offsetHeight))
    tabsRO.current.observe(el)
  }, [])
  const [page, setPage] = useState<PullPage | null>(null)
  // Pocket AI on the right, as in Pocket Projects (tabs per conversation, kept per pull request)
  const dock = useAIDock(page?.pull, mobile)
  const dockRef = useRef(dock)
  dockRef.current = dock
  const [err, setErr] = useState('')
  const [threads, setThreads] = useState<Thread[]>([])
  const [modal, setModal] = useState<'review' | 'agent' | 'ask' | 'warnings' | null>(null)
  const v = sp.get('v')
  const load = useCallback(async () => {
    try { setPage(normalize(await api.get<PullPage>(`${V1}/pulls/${id}${v ? `?v=${v}` : ''}`))); setErr('') } catch (e) { setErr(errMsg(e)) }
  }, [id, v])
  useEffect(() => { setPage(null); load() }, [load])
  const loadThreads = useCallback((fresh = false) => { api.get<{ threads: Thread[] }>(`${V1}/pulls/${id}/threads${fresh ? '?fresh=1' : ''}`).then(r => setThreads(r.threads || [])).catch(() => {}) }, [id])
  useEffect(() => { loadThreads() }, [loadThreads])
  useCrumb(page ? `${page.pull.repo}#${page.pull.number}` : undefined)

  // while a walkthrough is being made, look every few seconds
  const building = page?.building && (page.building.status === 'queued' || page.building.status === 'working') ? page.building : null
  useEffect(() => {
    if (!building) return
    const t = setInterval(async () => {
      try {
        const s = await api.get<{ status: string; progress: string }>(`${V1}/pulls/${id}/walkthroughs/${building.id}/status`)
        if (s.status === 'ready' || s.status === 'failed') { load(); announceChange() } else setPage(p => p && p.building ? { ...p, building: { ...p.building, status: s.status as 'working', progress: s.progress } } : p)
      } catch { /* next time */ }
    }, 2500)
    return () => clearInterval(t)
  }, [building?.id, id, load]) // eslint-disable-line react-hooks/exhaustive-deps

  const view: View = (sp.get('view') as View) || settings?.open_view || 'steps'
  const setView = (nv: View) => { const n = new URLSearchParams(sp); n.set('view', nv); n.delete('step'); setSp(n, { replace: true }) }
  const doc: Doc | undefined = page?.walkthrough?.status === 'ready' ? page.walkthrough.doc : undefined
  const steps = doc?.steps || []
  const stepKey = sp.get('step') || OVERVIEW
  const stepIdx = steps.findIndex(s => s.key === stepKey)
  const goStep = useCallback((key: string) => { const n = new URLSearchParams(sp); n.set('step', key); if (!n.get('view')) n.set('view', 'steps'); setSp(n, { replace: false }); document.querySelector('.content')?.scrollTo({ top: 0 }) }, [sp, setSp])

  const files = useMemo(() => new Map((page?.files || []).map(f => [f.path, f])), [page?.files])
  const comments = page?.comments || []
  const drafts = comments.filter(c => c.state === 'draft')
  const viewed = page?.viewed || {}

  const setViewed = async (units: Unit[], on: boolean) => {
    try {
      const r = await api.put<{ viewed: Viewed }>(`${V1}/pulls/${id}/viewed`, { items: units.map(u => ({ path: u.path, hashes: u.hashes })), viewed: on })
      setPage(p => p && { ...p, viewed: r.viewed })
      announceChange()
    } catch (e) { toast(errMsg(e), 'err') }
  }
  // Settings → test files count as looked at: mark the ones not looked at yet,
  // once per walkthrough (remembered in this browser, so a box unchecked to
  // read a test stays unchecked after a reload)
  const wid = page?.walkthrough?.status === 'ready' ? page.walkthrough.id : 0
  useEffect(() => {
    const k = `ppr-auto-tests:${wid}`
    if (!settings?.auto_view_tests || !doc || !wid || localStorage.getItem(k)) return
    localStorage.setItem(k, '1')
    const todo = doc.steps.flatMap(s => s.units).filter(u => isTest(u) && u.hashes.length > 0 && !unitDone(u, viewed))
    if (todo.length) setViewed(todo, true)
  }, [settings?.auto_view_tests, wid]) // eslint-disable-line react-hooks/exhaustive-deps
  const actions: DiffActions = {
    canComment: !!page,
    onAdd: async c => { const nc = await api.post<Comment>(`${V1}/pulls/${id}/comments`, { ...c, commit_sha: page?.walkthrough?.head_sha }); setPage(p => p && { ...p, comments: [...p.comments, nc] }); announceChange() },
    onEdit: async (cid, body) => { const nc = await api.put<Comment>(`${V1}/pulls/${id}/comments/${cid}`, { body }); setPage(p => p && { ...p, comments: p.comments.map(x => x.id === cid ? nc : x) }) },
    onDelete: async cid => { await api.del(`${V1}/pulls/${id}/comments/${cid}`); setPage(p => p && { ...p, comments: p.comments.filter(x => x.id !== cid) }); announceChange() },
    onReply: page?.github.connected && !settings?.demo ? async (to, body) => { await api.post(`${V1}/pulls/${id}/reply`, { to, body }); loadThreads(true) } : undefined,
  }
  const addStepNote = async (step: Step, body: string) => {
    const nc = await api.post<Comment>(`${V1}/pulls/${id}/comments`, { step: step.title, body })
    setPage(p => p && { ...p, comments: [...p.comments, nc] }); announceChange()
  }

  const rewalk = async () => { try { await api.post(`${V1}/pulls/${id}/walkthrough`); toast('Making a new walkthrough'); if (v) { const n = new URLSearchParams(sp); n.delete('v'); setSp(n) } else load() } catch (e) { toast(errMsg(e), 'err') } }
  const copyMd = async () => { try { const md = await api.getText(`${V1}/pulls/${id}/walkthrough.md${v ? `?v=${v}` : ''}`); toast(await copyText(md) ? 'Copied as Markdown' : 'Could not copy', 'ok') } catch (e) { toast(errMsg(e), 'err') } }
  const postMd = async () => { try { const r = await api.post<{ url: string }>(`${V1}/pulls/${id}/walkthrough/post`); toast('Posted on the pull request'); window.open(r.url, '_blank', 'noopener') } catch (e) { toast(errMsg(e), 'err') } }
  const markAndNext = () => {
    if (stepIdx < 0) { if (steps[0]) goStep(steps[0].key); return }
    const s = steps[stepIdx]
    if (!stepDone(s, viewed)) setViewed(s.units, true)
    if (steps[stepIdx + 1]) goStep(steps[stepIdx + 1].key)
    else toast('That was the last step — finish the review when you are ready')
  }

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if (e.metaKey || e.ctrlKey || e.altKey || t.closest('input, textarea, [contenteditable="true"]') || modalOpen() || document.querySelector('.menu, .popover, .sel-pop')) return
      const keys = [OVERVIEW, ...steps.map(s => s.key)]
      const at = keys.indexOf(stepKey)
      switch (e.key) {
        case 'j': e.preventDefault(); if (view !== 'steps') setView('steps'); else if (keys[at + 1]) goStep(keys[at + 1]); break
        case 'k': e.preventDefault(); if (at > 0) goStep(keys[at - 1]); break
        case 'v': e.preventDefault(); markAndNext(); break
        case '1': setView('steps'); break
        case '2': setView('built'); break
        case '3': setView('files'); break
        case 'c': e.preventDefault(); setModal('review'); break
        case 'a': if (doc) { e.preventDefault(); setModal('ask') } break
        case 'e': e.preventDefault(); setModal('agent'); break
        case 'i': e.preventDefault(); dockRef.current.toggle(); break
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })

  if (err) return <Empty icon="alert" title="Could not open this pull request">{err}</Empty>
  if (!page) return <Loading />
  const p = page.pull
  const w = page.walkthrough
  const stale = !!w && w.head_sha !== p.head_sha
  const doneUnits = steps.reduce((n, s) => n + s.units.filter(u => unitDone(u, viewed)).length, 0)
  const allUnits = steps.reduce((n, s) => n + s.units.length, 0)
  const more: MenuItem[] = [
    { label: 'Open on GitHub', icon: 'external', onSelect: () => window.open(p.url, '_blank', 'noopener') },
    { label: 'Copy the walkthrough as Markdown', icon: 'copy', disabled: !doc, onSelect: copyMd },
    { label: 'Post the walkthrough on the pull request', icon: 'github', disabled: !doc || !page.github.connected || !!settings?.demo, onSelect: postMd },
    { label: 'Hand the comments to a coding agent', icon: 'bot', hint: 'E', onSelect: () => setModal('agent') },
    'sep',
    { label: 'Make the walkthrough again', icon: 'refresh', onSelect: rewalk, disabled: !!building },
    ...(page.versions.length > 1 ? [{ heading: 'Earlier walkthroughs' } as MenuItem, ...page.versions.filter(x => x.status === 'ready').slice(0, 5).map((x, i): MenuItem => ({
      label: `${short(x.head_sha)} · ${ago(x.finished_at || x.created_at)}${i === 0 ? ' (newest)' : ''}`, checked: (w?.id || 0) === x.id,
      onSelect: () => { const n = new URLSearchParams(sp); if (i === 0) n.delete('v'); else n.set('v', String(x.id)); setSp(n) },
    }))] : []),
  ]

  return (
    <div className="pr-page" style={{ '--tabs-h': `${tabsH}px` } as React.CSSProperties}>
      <header className="pr-head">
        <div className="pr-head-main">
          <h2 className="pr-h-title">{p.title} <a className="muted pr-h-num" href={p.url} target="_blank" rel="noreferrer">#{p.number}</a></h2>
          <div className="pr-h-meta muted small">
            <span className={`state-chip ${p.state} ${p.draft ? 'draft' : ''}`}><Icon name={p.state === 'merged' ? 'check-circle' : 'pull'} size={13} />{p.draft ? 'Draft' : p.state === 'open' ? 'Open' : p.state === 'merged' ? 'Merged' : 'Closed'}</span>
            <span>{p.repo}</span>
            <span className="pr-author">{p.author_avatar && <img src={p.author_avatar} alt="" />}{p.author}</span>
            <span className="branch"><code>{p.base_ref}</code> ← <code>{p.head_ref}</code></span>
            <span className="tnum"><span className="add">+{p.additions}</span> <span className="del">−{p.deletions}</span> · {p.changed_files} file{p.changed_files === 1 ? '' : 's'} · {p.commits} commit{p.commits === 1 ? '' : 's'}</span>
            {reviewBadge(p)}
          </div>
          {(page.github.connected || settings?.demo) && <PullStatus key={`${p.id}:${p.head_sha}`} pullId={p.id} base={p.base_ref} prUrl={p.url} />}
        </div>
        <div className="pr-head-actions">
          <button type="button" className={`btn ${dock.open ? 'active' : ''}`} onClick={dock.toggle} disabled={dock.busy} title="Ask Pocket AI about this pull request (i)" aria-pressed={dock.open}>
            {dock.busy ? <Spinner size={14} /> : <Icon name="sparkles" size={15} />}{!mobile && 'Pocket AI'}<AIMark refName={`${p.repo}#${p.number}`} /></button>
          <button type="button" className="btn primary" onClick={() => setModal('review')} title="Finish the review (c)"><Icon name="check" size={15} />{mobile ? 'Review' : 'Finish the review'}{drafts.length > 0 && <span className="badge">{drafts.length}</span>}</button>
          <MenuButton items={more} label="More" align="right" width={300}><Icon name="more" size={16} /></MenuButton>
        </div>
      </header>

      {building && <div className="callout info building"><Spinner size={14} /><div><b>{w ? 'Making a new walkthrough' : 'Making the walkthrough'}</b> — {building.progress || 'waiting for its turn'}. {w ? 'The one below is for an earlier commit.' : 'This takes a minute or two for a large pull request.'}</div></div>}
      {page.building?.status === 'failed' && <div className="callout warn"><Icon name="alert" size={15} /><div>The newest walkthrough failed: {page.building.error}. <button type="button" className="link-btn" onClick={rewalk}>Try again</button></div></div>}
      {stale && !building && <div className="callout warn"><Icon name="commit" size={15} /><div>There are new commits since this walkthrough (it shows <code>{short(w!.head_sha)}</code>, the pull request is at <code>{short(p.head_sha)}</code>). What you looked at stays marked unless it changed. <button type="button" className="link-btn" onClick={rewalk}>Update the walkthrough</button></div></div>}
      {!w && !building && page.building?.status !== 'failed' && <Empty icon="steps" title="No walkthrough yet"><button type="button" className="btn primary mt" onClick={rewalk}>Make the walkthrough</button></Empty>}

      {w && w.status === 'ready' && doc && (
        <>
          <div className="pr-tabs" ref={tabsRef}>
            <Segmented value={view} onChange={setView} options={[
              { value: 'steps', label: mobile ? 'Steps' : 'Walkthrough' },
              { value: 'built', label: mobile ? 'Built' : 'As it was built' },
              { value: 'files', label: mobile ? 'Files' : 'All files' },
            ]} />
            <span className="spacer" />
            <span className="muted small hide-mobile">{doneUnits} of {allUnits} looked at</span>
            <span className="walk-bar wide" aria-hidden><span style={{ width: `${allUnits ? (doneUnits / allUnits) * 100 : 0}%` }} /></span>
            {doc.warnings.length > 0 && <button type="button" className="btn ghost sm" onClick={() => setModal('warnings')} title="Notes on this walkthrough"><Icon name="info" size={14} />{doc.warnings.length}</button>}
          </div>

          {view === 'steps' && (
            <div className="walk">
              {!mobile && <StepList steps={steps} doc={doc} current={stepKey} viewed={viewed} drafts={drafts} onGo={goStep} after={<Conversations pull={p} dock={dock} />} />}
              <div className="walk-main">
                {mobile && stepIdx < 0 && <Conversations pull={p} dock={dock} />}
                {mobile && <Select ariaLabel="Step" value={stepKey} onChange={goStep} options={[{ value: OVERVIEW, label: 'In one minute' }, ...steps.map((s, i) => ({ value: s.key, label: `${i + 1}. ${s.title}${stepDone(s, viewed) ? ' ✓' : ''}` }))]} />}
                {stepIdx < 0
                  ? <Overview doc={doc} pull={page} viewed={viewed} mobile={mobile} onGo={goStep} onStart={() => steps[0] && goStep(steps[0].key)} onAsk={() => setModal('ask')} />
                  : <StepView step={steps[stepIdx]} index={stepIdx} total={steps.length} files={files} viewed={viewed} comments={comments} threads={threads} actions={actions}
                      wrap={!!settings?.diff_wrap} onViewed={setViewed} onNote={addStepNote}
                      onPrev={() => goStep(stepIdx > 0 ? steps[stepIdx - 1].key : OVERVIEW)} onNext={markAndNext} last={stepIdx === steps.length - 1} onFinish={() => setModal('review')} onAsk={() => setModal('ask')} />}
              </div>
            </div>
          )}
          {view === 'built' && <Built events={w.timeline || []} doc={doc} onGoStep={k => goStep(k)} />}
          {view === 'files' && <AllFiles files={page.files} doc={doc} viewed={viewed} comments={comments} threads={threads} actions={actions} wrap={!!settings?.diff_wrap} onViewed={setViewed} />}
        </>
      )}

      <AIDock pull={p} dock={dock} mobile={mobile} />
      {modal === 'review' && <ReviewModal pull={p} drafts={drafts} canSend={page.github.connected} demo={!!settings?.demo} onClose={() => setModal(null)} onRemove={actions.onDelete}
        onSent={url => { setModal(null); toast(settings?.demo ? 'Review saved (the demo does not send it)' : 'Review sent to GitHub'); if (url) window.open(url, '_blank', 'noopener'); load(); loadThreads(true); announceChange() }} />}
      {modal === 'agent' && <AgentModal pull={p} onClose={() => setModal(null)} />}
      {modal === 'ask' && doc && <AskModal pullId={p.id} step={stepIdx >= 0 ? steps[stepIdx] : undefined} onClose={() => setModal(null)} />}
      {modal === 'warnings' && doc && <Modal title="Notes on this walkthrough" onClose={() => setModal(null)}><ul className="warn-list">{doc.warnings.map((x, i) => <li key={i}>{x}</li>)}</ul></Modal>}
    </div>
  )
}

function StepList({ steps, doc, current, viewed, drafts, onGo, after }: { steps: Step[]; doc: Doc; current: string; viewed: Viewed; drafts: Comment[]; onGo: (k: string) => void; after?: React.ReactNode }) {
  const notesFor = (s: Step) => drafts.filter(d => (d.path && s.units.some(u => u.path === d.path)) || d.step === s.title).length
  return (
    <nav className="step-list" aria-label="Steps">
      {after}
      <button type="button" className={`step-item ov ${current === OVERVIEW ? 'active' : ''}`} onClick={() => onGo(OVERVIEW)}>
        <span className="step-num"><Icon name="info" size={13} /></span><span className="step-t">In one minute</span>
      </button>
      {steps.map((s, i) => {
        const done = stepDone(s, viewed)
        const changed = !done && s.units.some(u => unitChanged(u, viewed))
        const n = notesFor(s)
        return (
          <button key={s.key} type="button" className={`step-item ${current === s.key ? 'active' : ''} ${done ? 'done' : ''}`} onClick={() => onGo(s.key)}>
            <span className="step-num">{done ? <Icon name="check" size={13} strokeWidth={2.6} /> : i + 1}</span>
            <span className="step-t">{s.title}<span className="step-sub">{s.units.length} {s.units.length === 1 ? 'file' : 'files'}{changed ? ' · changed since you looked' : ''}{s.risks.length ? ` · ${s.risks.length} to check` : ''}</span></span>
            {n > 0 && <span className="badge sm" title="Your comments">{n}</span>}
          </button>
        )
      })}
      {doc.noise.length > 0 && <div className="step-noise muted small"><Icon name="package" size={13} />{doc.noise.length} generated or lock file{doc.noise.length === 1 ? '' : 's'} left out</div>}
      <div className="step-order muted small">{doc.order === 'ai' ? 'Order and sentences by AI, checked so every change is in it once.' : 'Ordered by layer and imports.'}</div>
    </nav>
  )
}

/** The first sentence, and the rest, of a paragraph. */
function splitFirst(t: string): [string, string] {
  const m = t.match(/^(.+?[.!?])\s+(.+)$/s)
  return m ? [m[1], m[2]] : [t, '']
}

const baseName = (p: string) => p.split('/').pop() || p

/** Changed lines in one unit: the whole file, or only its hunks in this step. */
function unitLines(u: Unit, f?: WFile) {
  if (!f) return 0
  if (!u.hunks) return f.additions + f.deletions
  return u.hunks.reduce((n, i) => n + (f.hunks[i]?.lines.filter(l => l.t === '+' || l.t === '-').length || 0), 0)
}

function Overview({ doc, pull, viewed, mobile, onGo, onStart, onAsk }: { doc: Doc; pull: PullPage; viewed: Viewed; mobile: boolean; onGo: (k: string) => void; onStart: () => void; onAsk: () => void }) {
  const [more, setMore] = useState(false)
  const files = useMemo(() => new Map(pull.files.map(f => [f.path, f])), [pull.files])
  const stepOfUnit = useMemo(() => {
    const m = new Map<string, Step>()
    for (const s of doc.steps) for (const u of s.units) m.set(u.id, s)
    return m
  }, [doc])
  const sizes = doc.steps.map(s => s.units.reduce((n, u) => n + unitLines(u, files.get(u.path)), 0))
  const biggest = Math.max(1, ...sizes)
  const text = doc.overview || pull.pull.body || ''
  const [lead, rest] = doc.tldr ? [doc.tldr, text] : splitFirst(text)
  const flow = doc.flow || []
  const terms = doc.terms || []
  return (
    <div className="stack loose">
      <section className="ov-card ov-hero">
        <span className="ov-kicker">In one minute</span>
        <p className="ov-tldr">{lead || 'No description.'}</p>
        {rest && (more
          ? <p className="ov-text muted">{rest}</p>
          : <button type="button" className="link-btn small" onClick={() => setMore(true)}>More detail</button>)}
        {(doc.before || doc.after) && (
          <div className="ba">
            {doc.before && <div className="ba-before"><span className="ba-l">Before</span><p>{doc.before}</p></div>}
            <span className="ba-arrow" aria-hidden><Icon name="chevron-right" size={18} /></span>
            {doc.after && <div className="ba-after"><span className="ba-l">After</span><p>{doc.after}</p></div>}
          </div>
        )}
      </section>

      {doc.diagram && doc.diagram.nodes.length > 1 && (
        <section className="ov-card">
          <h3>How the data moves</h3>
          <DataFlow diagram={doc.diagram} vertical={mobile} onStep={n => doc.steps[n - 1] && onGo(doc.steps[n - 1].key)} />
        </section>
      )}

      {!doc.diagram && flow.length > 0 && (
        <section className="ov-card">
          <h3>How it fits together</h3>
          <div className="flow" role="list">
            {flow.map((g, i) => (
              <div key={i} className="flow-cell" role="listitem">
                <div className="flow-stage">
                  <div className="flow-title">{g.title}</div>
                  <ul className="flow-items">{g.items.map((it, j) => <li key={j}>{it}</li>)}</ul>
                  {g.steps.length > 0 && <div className="flow-steps">{g.steps.map(n => doc.steps[n - 1] && <button key={n} type="button" className="flow-step" onClick={() => onGo(doc.steps[n - 1].key)} title={doc.steps[n - 1].title}>step {n}</button>)}</div>}
                </div>
                {i < flow.length - 1 && <span className="flow-arrow" aria-hidden><Icon name="chevron-right" size={18} /></span>}
              </div>
            ))}
          </div>
        </section>
      )}

      <section className="ov-card">
        <h3>Read it in this order</h3>
        <ol className="map">
          {doc.steps.map((s, i) => {
            const done = stepDone(s, viewed)
            return (
              <li key={s.key}>
                <button type="button" className={`map-row ${done ? 'done' : ''}`} onClick={() => onGo(s.key)} title={s.why}>
                  <span className="step-num">{done ? <Icon name="check" size={13} strokeWidth={2.6} /> : i + 1}</span>
                  <span className="map-main">
                    <span className="map-title">{s.title}</span>
                    <span className="map-sub muted">
                      <span className={`layer-chip l-${s.layer}`}>{LAYER_LABEL[s.layer] || s.layer}</span>
                      {s.units.length} {s.units.length === 1 ? 'file' : 'files'}
                      {s.risks.length > 0 && <span className="map-risk"><Icon name="alert" size={12} />{s.risks.length} to check</span>}
                    </span>
                  </span>
                  <span className="map-size" title={`${sizes[i]} changed lines`}><span className={`l-${s.layer}`} style={{ width: `${Math.max(4, (sizes[i] / biggest) * 100)}%` }} /></span>
                </button>
              </li>
            )
          })}
        </ol>
        <div className="row wrap mt">
          <button type="button" className="btn primary" onClick={onStart}><Icon name="play" size={14} />Start with step 1</button>
          <button type="button" className="btn" onClick={onAsk}><Icon name="sparkles" size={14} />Ask about it</button>
        </div>
      </section>

      {doc.risks.length > 0 && (
        <section className="ov-card">
          <h3>Worth a careful look</h3>
          <ul className="risk-list">{doc.risks.map((r, i) => {
            const at = r.units.map(id => ({ id, u: doc.steps.flatMap(s => s.units).find(u => u.id === id), s: stepOfUnit.get(id) })).filter(x => x.u)
            return (
              <li key={i} className={`risk ${r.level}`}>
                <span className={`risk-lvl ${r.level}`}>{r.level}</span>
                <div className="risk-body">
                  {r.title ? <><b>{r.title}</b><span className="muted small">{r.text}</span></> : <span>{r.text}</span>}
                  {at.length > 0 && <span className="risk-files">{at.map(x => <button key={x.id} type="button" className="file-chip" title={x.u!.path} onClick={() => x.s && onGo(x.s.key)}>{baseName(x.u!.path)}</button>)}</span>}
                </div>
              </li>
            )
          })}</ul>
        </section>
      )}

      {terms.length > 0 && (
        <section className="ov-card">
          <h3>Words you will meet</h3>
          <dl className="terms">{terms.map((t, i) => <div key={i}><dt><code>{t.term}</code></dt><dd>{t.means}</dd></div>)}</dl>
        </section>
      )}

      {doc.questions.length > 0 && (
        <section className="ov-card">
          <h3>Questions for the author</h3>
          <ul className="q-list">{doc.questions.map((q, i) => <li key={i}>{q}</li>)}</ul>
        </section>
      )}
    </div>
  )
}

function StepView({ step, index, total, files, viewed, comments, threads, actions, wrap, onViewed, onNote, onPrev, onNext, last, onFinish, onAsk }: {
  step: Step; index: number; total: number; files: Map<string, WFile>; viewed: Viewed; comments: Comment[]; threads: Thread[]; actions: DiffActions; wrap: boolean
  onViewed: (u: Unit[], on: boolean) => void; onNote: (s: Step, body: string) => Promise<void>; onPrev: () => void; onNext: () => void; last: boolean; onFinish: () => void; onAsk: () => void
}) {
  const [noting, setNoting] = useState(false)
  const notes = comments.filter(c => !c.path && c.step === step.title)
  const done = stepDone(step, viewed)
  const top = useRef<HTMLDivElement>(null)
  return (
    <div className="step-view" ref={top}>
      <div className="step-head">
        <span className="muted small">Step {index + 1} of {total}</span>
        <h3>{step.title}</h3>
        {step.why && <p className="step-why">{step.why}</p>}
        {step.risks.length > 0 && <div className="callout warn"><Icon name="alert" size={15} /><div><b>Check here</b><ul className="tight-list">{step.risks.map((r, i) => <li key={i}>{r}</li>)}</ul></div></div>}
        <div className="row wrap">
          <button type="button" className="btn sm" onClick={() => setNoting(true)}><Icon name="message" size={13} />Note on this step</button>
          <button type="button" className="btn sm ghost" onClick={onAsk}><Icon name="sparkles" size={13} />Ask about this step</button>
        </div>
        {notes.map(c => <MyComment key={c.id} c={c} actions={actions} />)}
        {noting && <Composer label="A note on the whole step (it goes into the review's text)" onCancel={() => setNoting(false)} onSave={async b => { await onNote(step, b); setNoting(false) }} />}
      </div>
      {step.units.map(u => <UnitView key={u.id} u={u} file={files.get(u.path)} viewed={viewed} comments={comments} threads={threads} actions={actions} wrap={wrap} onViewed={onViewed} />)}
      <div className="step-foot">
        <button type="button" className="btn" onClick={onPrev}><Icon name="chevron-left" size={15} />{index === 0 ? 'Overview' : 'Previous'}</button>
        <span className="spacer" />
        {last && done
          ? <button type="button" className="btn primary" onClick={onFinish}><Icon name="check" size={15} />Finish the review</button>
          : <button type="button" className="btn primary" onClick={onNext} title="Mark as looked at, then the next step (v)">{done ? 'Next step' : last ? 'Looked at' : 'Looked at — next step'}<Icon name="chevron-right" size={15} /></button>}
      </div>
    </div>
  )
}

function UnitView({ u, file, viewed, comments, threads, actions, wrap, onViewed }: {
  u: Unit; file?: WFile; viewed: Viewed; comments: Comment[]; threads: Thread[]; actions: DiffActions; wrap: boolean; onViewed: (u: Unit[], on: boolean) => void
}) {
  const done = unitDone(u, viewed)
  const changed = unitChanged(u, viewed)
  const [open, setOpen] = useState(!done)
  useEffect(() => { if (done) setOpen(false) }, [done])
  const parts = file ? file.hunks.length : 0
  return (
    <section className={`unit ${done ? 'done' : ''}`} data-hint={`${modKey} Click to ${done ? 'unmark' : 'mark as looked at'}`}
      onClickCapture={e => {
        if (!modDown(e) || (e.target as HTMLElement).closest('a')) return
        e.preventDefault(); e.stopPropagation()
        window.getSelection()?.removeAllRanges()
        onViewed([u], !done)
      }}>
      <div className="unit-head">
        <button type="button" className="unit-toggle" onClick={() => setOpen(o => !o)} aria-expanded={open} aria-label={open ? 'Fold' : 'Unfold'}><Icon name={open ? 'chevron-down' : 'chevron-right'} size={15} /></button>
        <div className="unit-title">
          <code className="unit-path">{file?.old_path && file.status === 'renamed' ? <><span className="muted">{file.old_path} →</span> {u.path}</> : u.path}</code>
          <span className="unit-chips">
            {file && <span className={`fstat ${file.status}`}>{file.status}</span>}
            <span className="layer-chip">{LAYER_LABEL[u.layer] || u.layer}</span>
            {u.hunks && parts > 1 && <span className="badge sm" title="Only the part of this file that belongs to this step">part: {u.hunks.length} of {parts} changes</span>}
            {file && <span className="tnum small"><span className="add">+{file.additions}</span> <span className="del">−{file.deletions}</span></span>}
            {changed && <span className="badge warn sm">changed since you looked</span>}
          </span>
          <p className="unit-sum">{u.summary}</p>
        </div>
        <label className="viewed-box" title="Mark as looked at">
          <input type="checkbox" checked={done} onChange={e => onViewed([u], e.target.checked)} />
          <span className="hide-mobile">Looked at</span>
        </label>
      </div>
      {open && file && (file.truncated ? <div className="diff-empty muted small">GitHub sends no diff for a file this large — look at it on GitHub.</div>
        : file.binary ? <div className="diff-empty muted small">A binary file.</div>
        : <Diff path={u.path} hunks={file.hunks} only={u.hunks} comments={comments} threads={threads} actions={actions} wrap={wrap} />)}
    </section>
  )
}

function Built({ events, doc, onGoStep }: { events: Event[]; doc: Doc; onGoStep: (k: string) => void }) {
  const [open, setOpen] = useState<string | null>(null)
  const stepOf = useMemo(() => {
    const m = new Map<string, Step>()
    for (const s of doc.steps) for (const u of s.units) if (!m.has(u.path)) m.set(u.path, s)
    return m
  }, [doc])
  if (events.length === 0) return <Empty icon="commit" title="Nothing to show">GitHub sent no commits for this pull request.</Empty>
  const agent = events.some(e => e.kind === 'edit')
  return (
    <div className="built">
      <p className="muted small">{agent ? 'The commits and, from Pocket AI, the files the coding agent edited — in the order it happened.' : 'The commits, oldest first, with the files each one touched.'}</p>
      <ol className="tl">
        {events.map((e, i) => (
          <li key={i} className={`tl-ev ${e.kind}`}>
            <span className="tl-dot"><Icon name={e.kind === 'edit' ? 'bot' : 'commit'} size={14} /></span>
            <div className="tl-body">
              <div className="tl-head"><b className="tl-title">{e.title}</b><span className="muted small">{e.kind === 'commit' ? <><code>{short(e.sha)}</code> · {e.author}</> : 'agent'} · {e.at ? fmtDateTime(e.at) : ''}</span></div>
              {e.body && e.kind === 'commit' && <p className="tl-text muted small">{e.body}</p>}
              <div className="tl-files">
                {e.files.map(f => {
                  const k = `${i}:${f.path}`
                  const st = stepOf.get(f.path)
                  return (
                    <div key={k} className="tl-file">
                      <button type="button" className="tl-file-btn" onClick={() => setOpen(open === k ? null : k)} disabled={!f.patch}>
                        <Icon name={open === k ? 'chevron-down' : 'chevron-right'} size={13} /><code>{f.path}</code>
                        {(f.additions || f.deletions) ? <span className="tnum small"><span className="add">+{f.additions}</span> <span className="del">−{f.deletions}</span></span> : null}
                      </button>
                      {st && <button type="button" className="badge sm step-link" onClick={() => onGoStep(st.key)} title="Its step in the walkthrough">{st.title}</button>}
                      {open === k && f.patch && <Diff path={f.path} hunks={parsePatch(f.patch)} comments={[]} threads={[]} actions={{ canComment: false, onAdd: async () => {}, onEdit: async () => {}, onDelete: async () => {} }} />}
                    </div>
                  )
                })}
              </div>
            </div>
          </li>
        ))}
      </ol>
    </div>
  )
}

function AllFiles({ files, doc, viewed, comments, threads, actions, wrap, onViewed }: {
  files: WFile[]; doc: Doc; viewed: Viewed; comments: Comment[]; threads: Thread[]; actions: DiffActions; wrap: boolean; onViewed: (u: Unit[], on: boolean) => void
}) {
  const unitsOf = useMemo(() => {
    const m = new Map<string, Unit[]>()
    for (const u of [...doc.steps.flatMap(s => s.units), ...doc.noise]) m.set(u.path, [...(m.get(u.path) || []), u])
    return m
  }, [doc])
  const sorted = [...files].sort((a, b) => a.path.localeCompare(b.path))
  return (
    <div className="stack">
      <p className="muted small">Every changed file, by name — as GitHub shows them. The walkthrough has them in reading order.</p>
      {sorted.map(f => {
        const us = unitsOf.get(f.path) || []
        const whole: Unit = { id: f.path, path: f.path, hunks: null, hashes: us.flatMap(u => u.hashes), summary: f.summary, layer: f.layer }
        return <UnitView key={f.path} u={whole} file={f} viewed={viewed} comments={comments} threads={threads} actions={actions} wrap={wrap} onViewed={() => onViewed(us, !unitDone(whole, viewed))} />
      })}
    </div>
  )
}

function AskModal({ pullId, step, onClose }: { pullId: number; step?: Step; onClose: () => void }) {
  const [q, setQ] = useState('')
  const [a, setA] = useState<{ q: string; text: string; err?: boolean }[]>([])
  const [busy, setBusy] = useState(false)
  const ask = async () => {
    if (!q.trim()) return
    setBusy(true)
    const question = q.trim()
    try {
      const r = await api.post<{ ok: boolean; answer?: string; error?: string }>(`${V1}/pulls/${pullId}/ask`, { question, step: step?.key || '' })
      setA(x => [...x, { q: question, text: r.ok ? r.answer! : r.error || 'no answer', err: !r.ok }])
      setQ('')
    } catch (e) { setA(x => [...x, { q: question, text: errMsg(e), err: true }]) } finally { setBusy(false) }
  }
  return (
    <Modal title={step ? `Ask about “${step.title}”` : 'Ask about this pull request'} onClose={onClose} size="lg">
      <div className="stack">
        {a.map((x, i) => (
          <div key={i} className="ask-qa"><div className="ask-q"><b>{x.q}</b></div><div className={`ask-a ${x.err ? 'bad' : ''}`}>{x.text}</div></div>
        ))}
        {busy && <div className="muted small row"><Spinner size={12} /> Thinking…</div>}
        <div className="row">
          <input autoFocus value={q} onChange={e => setQ(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') ask() }} placeholder={step ? 'Why is this needed? What could break?' : 'What does this change for users?'} aria-label="Question" />
          <button type="button" className="btn primary" onClick={ask} disabled={busy || !q.trim()}>Ask</button>
        </div>
        <p className="muted small">The AI sees the walkthrough{step ? ' and the diff of this step' : ''}.</p>
      </div>
    </Modal>
  )
}

