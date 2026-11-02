// One file's diff (all of it, or some of its hunks), with my comments and the
// GitHub conversation under their lines. Click a line number to comment.
import React, { useMemo, useState } from 'react'
import type { Comment, Hunk, Thread } from '../lib/types'
import { wordMarks } from '../lib/diff'
import { highlightHunk, languageOf, withMark, type Token } from '../lib/highlight'
import { ago } from '../lib/format'
import { Icon } from './icons'
import { Markdown } from './Markdown'
import { errMsg, useToast } from './ui'

export interface DiffActions {
  canComment: boolean
  onAdd: (c: { path: string; side: 'LEFT' | 'RIGHT'; line: number; start_line: number; body: string }) => Promise<void>
  onEdit: (id: number, body: string) => Promise<void>
  onDelete: (id: number) => Promise<void>
  onReply?: (to: number, body: string) => Promise<void>
}

type Anchor = { side: 'LEFT' | 'RIGHT'; line: number }
const key = (a: Anchor) => `${a.side}:${a.line}`

export function Diff({ path, hunks, only, comments, threads, actions, wrap }: {
  path: string; hunks: Hunk[]; only?: number[] | null; comments: Comment[]; threads: Thread[]; actions: DiffActions; wrap?: boolean
}) {
  const [open, setOpen] = useState<Anchor | null>(null)
  const [from, setFrom] = useState<Anchor | null>(null) // shift-click: a range
  const shown = only && only.length ? hunks.map((h, i) => ({ h, i })).filter(x => only.includes(x.i)) : hunks.map((h, i) => ({ h, i }))
  const byLine = useMemo(() => {
    const m = new Map<string, { mine: Comment[]; gh: Thread[] }>()
    const at = (k: string) => { if (!m.has(k)) m.set(k, { mine: [], gh: [] }); return m.get(k)! }
    for (const c of comments) if (c.path === path && c.line) at(`${c.side}:${c.line}`).mine.push(c)
    for (const t of threads) if (t.path === path && t.line && !t.outdated) at(`${t.side || 'RIGHT'}:${t.line}`).gh.push(t)
    return m
  }, [comments, threads, path])
  const lang = languageOf(path)
  const colours = useMemo(() => new Map(shown.map(({ h, i }) => [i, highlightHunk(h.lines, lang)])), [hunks, only, lang]) // eslint-disable-line react-hooks/exhaustive-deps
  if (shown.length === 0) return <div className="diff-empty muted small">No text changes to show.</div>
  const click = (a: Anchor, e: React.MouseEvent) => {
    if (!actions.canComment) return
    if (e.shiftKey && open && open.side === a.side && a.line > open.line) { setFrom(open); setOpen(a); return }
    setFrom(null)
    setOpen(o => (o && key(o) === key(a) ? null : a))
  }
  return (
    <div className={`diff ${wrap ? 'wrap' : ''}`}>
      {shown.map(({ h, i }) => {
        const marks = wordMarks(h.lines)
        return (
          <div className="diff-hunk" key={i}>
            <div className="diff-hhead"><span className="tnum">@@ −{h.old_start},{h.old_lines} +{h.new_start},{h.new_lines} @@</span>{h.header && <span className="diff-hctx">{h.header}</span>}</div>
            <table className="diff-table"><tbody>
              {h.lines.map((l, li) => {
                if (l.t === '\\') return <tr key={li} className="dl note"><td className="dn" /><td className="dn" /><td className="dc" colSpan={2}>{l.s}</td></tr>
                const a: Anchor = l.t === '-' ? { side: 'LEFT', line: l.o! } : { side: 'RIGHT', line: l.n! }
                const here = byLine.get(key(a))
                const isOpen = open && key(open) === key(a)
                const inRange = from && open && from.side === a.side && a.line >= from.line && a.line <= open.line
                const mk = marks.get(li)
                const toks = colours.get(i)?.get(li)
                return (
                  <React.Fragment key={li}>
                    <tr className={`dl ${l.t === '+' ? 'add' : l.t === '-' ? 'del' : ''} ${inRange || isOpen ? 'picked' : ''}`}>
                      <td className="dn" onClick={e => click(a, e)} title={actions.canComment ? 'Comment on this line (shift-click for several)' : undefined}>{l.o ?? ''}</td>
                      <td className="dn" onClick={e => click(a, e)}>{l.n ?? ''}</td>
                      <td className="ds">{l.t === ' ' ? '' : l.t === '-' ? '−' : '+'}</td>
                      <td className="dc">
                        {actions.canComment && <button type="button" className="dl-add" aria-label="Comment on this line" onClick={e => click(a, e)}><Icon name="plus" size={12} strokeWidth={2.4} /></button>}
                        {toks && toks.length > 0 ? <Code toks={toks} mk={mk} />
                          : mk ? <>{l.s.slice(0, mk[0])}<mark>{l.s.slice(mk[0], mk[1])}</mark>{l.s.slice(mk[1])}</> : l.s || ' '}
                      </td>
                    </tr>
                    {(here || isOpen) && (
                      <tr className="dl-notes"><td colSpan={4}>
                        {here?.gh.map(t => <GHThread key={t.id} t={t} onReply={actions.onReply} />)}
                        {here?.mine.map(c => <MyComment key={c.id} c={c} actions={actions} />)}
                        {isOpen && <Composer label={from ? `Lines ${from.line}–${open!.line}` : `Line ${a.line}${a.side === 'LEFT' ? ' (old)' : ''}`} onCancel={() => { setOpen(null); setFrom(null) }}
                          onSave={async body => { await actions.onAdd({ path, side: a.side, line: a.line, start_line: from ? from.line : 0, body }); setOpen(null); setFrom(null) }} />}
                      </td></tr>
                    )}
                  </React.Fragment>
                )
              })}
            </tbody></table>
          </div>
        )
      })}
    </div>
  )
}

/** A line's coloured tokens, its changed words marked. */
function Code({ toks, mk }: { toks: Token[]; mk?: [number, number] }) {
  return <>{withMark(toks, mk).map((t, k) => {
    const span = t.c ? <span key={k} className={t.c}>{t.s}</span> : t.s
    return t.mark ? <mark key={k}>{span}</mark> : <React.Fragment key={k}>{span}</React.Fragment>
  })}</>
}

export function Composer({ label, initial = '', onSave, onCancel, saveLabel = 'Add to the review' }: { label?: string; initial?: string; onSave: (body: string) => Promise<void>; onCancel: () => void; saveLabel?: string }) {
  const [body, setBody] = useState(initial)
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  const save = async () => {
    if (!body.trim()) return
    setBusy(true)
    try { await onSave(body.trim()) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <div className="composer">
      {label && <div className="composer-label muted small">{label}</div>}
      <textarea autoFocus rows={3} value={body} onChange={e => setBody(e.target.value)} placeholder="What should change, or what you want to ask…"
        onKeyDown={e => { if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') save(); if (e.key === 'Escape') { e.stopPropagation(); onCancel() } }} />
      <div className="row end">
        <button type="button" className="btn ghost sm" onClick={onCancel}>Cancel</button>
        <button type="button" className="btn primary sm" onClick={save} disabled={busy || !body.trim()}>{saveLabel}</button>
      </div>
    </div>
  )
}

export function MyComment({ c, actions }: { c: Comment; actions: DiffActions }) {
  const [editing, setEditing] = useState(false)
  if (editing) return <Composer initial={c.body} saveLabel="Save" onCancel={() => setEditing(false)} onSave={async b => { await actions.onEdit(c.id, b); setEditing(false) }} />
  return (
    <div className={`note-card mine ${c.state}`}>
      <div className="note-head"><span className="badge accent sm">{c.state === 'draft' ? 'Your comment · not sent' : 'You · sent'}</span>
        {c.start_line > 0 && <span className="muted small">lines {c.start_line}–{c.line}</span>}
        <span className="spacer" />
        {c.state === 'draft' && <>
          <button type="button" className="btn ghost xs" onClick={() => setEditing(true)}>Edit</button>
          <button type="button" className="btn ghost xs danger-text" onClick={() => actions.onDelete(c.id)}>Remove</button>
        </>}
      </div>
      <Markdown className="note-body" md={c.body} />
    </div>
  )
}

function GHThread({ t, onReply }: { t: Thread; onReply?: (to: number, body: string) => Promise<void> }) {
  const [replying, setReplying] = useState(false)
  return (
    <div className="note-card gh">
      {t.comments.map(c => (
        <div key={c.id} className="gh-c">
          <div className="note-head">{c.user.avatar_url && <img className="gh-ava" src={c.user.avatar_url} alt="" />}<b>{c.user.login}</b><span className="muted small">{ago(c.created_at)}</span></div>
          <Markdown className="note-body" md={c.body} html={c.body_html} />
        </div>
      ))}
      <div className="row">
        {onReply && !replying && <button type="button" className="btn ghost xs" onClick={() => setReplying(true)}>Reply</button>}
        <a className="btn ghost xs" href={t.url} target="_blank" rel="noreferrer">On GitHub <Icon name="external" size={12} /></a>
      </div>
      {replying && onReply && <Composer saveLabel="Reply on GitHub" onCancel={() => setReplying(false)} onSave={async b => { await onReply(t.id, b); setReplying(false) }} />}
    </div>
  )
}
